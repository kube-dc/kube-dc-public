#!/bin/bash
#
# RKE2 Agent Installation Script
# Joins a worker node to existing RKE2 cluster
#
# Usage:
#   ./install-agent.sh <token> <server-ip> [node-ip]
#
# Arguments:
#   token      - Node token from first server (/var/lib/rancher/rke2/server/node-token)
#   server-ip  - Control plane server IP
#   node-ip    - (Optional) This node's IP for internal traffic
#
# Environment variables (optional):
#   RKE2_VERSION  - RKE2 version (default: v1.35.0+rke2r1)
#   NODE_NAME     - Node name (default: hostname)
#   CP_PORT       - Control plane port (default: 9345)
#

set -euo pipefail

# Colors for output
RED='\033[0;31m'
GREEN='\033[0;32m'
YELLOW='\033[1;33m'
NC='\033[0m'

log_info()  { echo -e "${GREEN}[INFO]${NC} $*"; }
log_warn()  { echo -e "${YELLOW}[WARN]${NC} $*"; }
log_error() { echo -e "${RED}[ERROR]${NC} $*" >&2; }

# Validate required arguments
if [[ -z "${1:-}" ]]; then
    log_error "Missing required argument: token"
    echo "Usage: $0 <token> <server-ip> [node-ip]"
    exit 1
fi

if [[ -z "${2:-}" ]]; then
    log_error "Missing required argument: server-ip"
    echo "Usage: $0 <token> <server-ip> [node-ip]"
    exit 1
fi

# Configuration
SERVER_TOKEN="$1"
CP_HOST="$2"
CP_PORT="${CP_PORT:-9345}"
RKE2_VERSION="${RKE2_VERSION:-v1.35.0+rke2r1}"
NODE_NAME="${NODE_NAME:-$(hostname)}"
NODE_IP="${3:-$(hostname -I | awk '{print $1}')}"

RANCHER_DIR="/etc/rancher/rke2"

log_info "=== RKE2 Agent Installation ==="
log_info "Version:    ${RKE2_VERSION}"
log_info "Node Name:  ${NODE_NAME}"
log_info "Node IP:    ${NODE_IP}"
log_info "Server:     ${CP_HOST}:${CP_PORT}"

# Check if running as root
if [[ $EUID -ne 0 ]]; then
    log_error "This script must be run as root"
    exit 1
fi

# Ensure DNS resolution works (fix for servers with systemd-resolved)
if ! host get.rke2.io >/dev/null 2>&1; then
    log_warn "DNS resolution failed, configuring fallback DNS..."
    # Disable systemd-resolved if active (prevents DNS conflicts)
    if systemctl is-active systemd-resolved >/dev/null 2>&1; then
        log_warn "Disabling systemd-resolved..."
        systemctl stop systemd-resolved
        systemctl disable systemd-resolved
        rm -f /etc/resolv.conf
    fi
    echo -e "nameserver 8.8.8.8\nnameserver 1.1.1.1" > /etc/resolv.conf
fi

# Create config directory
mkdir -p "${RANCHER_DIR}"
mkdir -p /etc/rancher

# Kernel sysctls — same set as install-server.sh. See that file
# for rationale (virt-handler / inotify-heavy workload density).
log_info "Writing /etc/sysctl.d/99-kube-dc.conf and reloading kernel..."
cat > /etc/sysctl.d/99-kube-dc.conf <<'SYSCTL_EOF'
# Managed by kube-dc rke2 install scripts. See
# bootstrap/rke2/install-server.sh and install-agent.sh.
fs.inotify.max_user_instances = 8192
fs.inotify.max_user_watches = 524288
kernel.pid_max = 4194304
net.core.somaxconn = 32768
net.core.netdev_max_backlog = 16384
net.ipv4.ip_local_port_range = 1024 65535
net.ipv4.tcp_max_syn_backlog = 8192
vm.max_map_count = 262144
SYSCTL_EOF
sysctl --system >/dev/null

# OIDC authn moved off /etc/rancher/auth-conf.yaml to OpenIDConnect CRs
# in kube-dc v0.3.0 (commit shalb/kube-dc 7877184). Agents have no
# kube-apiserver, so they need nothing on disk for authn.

# Memory tier auto-sizing — see install-server.sh for rationale.
MEM_TOTAL_GIB=$(awk '/^MemTotal:/ {printf "%d", $2/1048576}' /proc/meminfo)
if [ "${MEM_TOTAL_GIB:-0}" -ge 64 ]; then
    KUBELET_SYS_RESERVED="cpu=500m,memory=4Gi"
    KUBELET_KUBE_RESERVED="cpu=500m,memory=4Gi"
    KUBELET_EVICTION_HARD="memory.available<2Gi,nodefs.available<10%"
    KUBELET_MAX_PODS="250"
elif [ "${MEM_TOTAL_GIB:-0}" -ge 32 ]; then
    KUBELET_SYS_RESERVED="cpu=300m,memory=2Gi"
    KUBELET_KUBE_RESERVED="cpu=300m,memory=2Gi"
    KUBELET_EVICTION_HARD="memory.available<1Gi,nodefs.available<10%"
    KUBELET_MAX_PODS="220"
else
    KUBELET_SYS_RESERVED="cpu=200m,memory=1Gi"
    KUBELET_KUBE_RESERVED="cpu=200m,memory=1Gi"
    KUBELET_EVICTION_HARD="memory.available<500Mi,nodefs.available<10%"
    KUBELET_MAX_PODS="200"
fi
log_info "Node memory: ${MEM_TOTAL_GIB} GiB → system-reserved=${KUBELET_SYS_RESERVED}, max-pods=${KUBELET_MAX_PODS}"

# Generate config.yaml
# Memory reservation + eviction protects kubelet/containerd from kernel OOM
# under sudden pressure. See bootstrap/rke2/install-server.sh + the
# 2026-05-06 cloud incident postmortem for rationale.
cat > "${RANCHER_DIR}/config.yaml" <<EOF
server: https://${CP_HOST}:${CP_PORT}
token: ${SERVER_TOKEN}
node-name: ${NODE_NAME}
node-ip: ${NODE_IP}
kubelet-arg:
  - system-reserved=${KUBELET_SYS_RESERVED}
  - kube-reserved=${KUBELET_KUBE_RESERVED}
  - eviction-hard=${KUBELET_EVICTION_HARD}
  - max-pods=${KUBELET_MAX_PODS}
EOF

log_info "Config written to ${RANCHER_DIR}/config.yaml"

# Install RKE2 as agent
log_info "Installing RKE2 agent ${RKE2_VERSION}..."
export INSTALL_RKE2_VERSION="${RKE2_VERSION}"
export INSTALL_RKE2_TYPE="agent"
curl -sfL https://get.rke2.io | sh -

# Enable and start service
log_info "Enabling rke2-agent service..."
systemctl enable rke2-agent.service

log_info "Starting rke2-agent service..."
systemctl start rke2-agent.service

# Wait for RKE2 agent to become active
log_info "Waiting for RKE2 agent to start..."
for i in {1..12}; do
    if systemctl is-active rke2-agent.service >/dev/null 2>&1; then
        log_info "RKE2 agent is active"
        break
    fi
    sleep 10
    echo -n "."
done
echo ""

log_info "=== Installation Complete ==="
log_info ""
log_info "To monitor agent logs:"
log_info "  journalctl -u rke2-agent -f"
log_info ""
log_info "Verify on control plane with:"
log_info "  kubectl get nodes"
