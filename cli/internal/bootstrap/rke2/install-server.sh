#!/bin/bash
#
# RKE2 Server Installation Script
# Installs first control-plane node or joins as additional server
#
# Usage:
#   First server:  ./install-server.sh
#   Join server:   ./install-server.sh <token> <server-ip>
#
# Environment variables (optional):
#   RKE2_VERSION     - RKE2 version (default: v1.35.0+rke2r1)
#   NODE_NAME        - Node name (default: hostname)
#   NODE_IP          - Node IP for K8s internal traffic (default: detected)
#   EXTERNAL_IP      - External IP for ingress (default: NODE_IP)
#   DOMAIN           - Cluster base domain (default: kube-dc.cloud). Pass the
#                       same value that ends up in clusters/<name>/cluster-
#                       config.env's DOMAIN= line. tls-san is generated as
#                         [${DOMAIN}, kube-api.${DOMAIN}, ${EXTERNAL_IP},
#                          ${NODE_IP}, …]
#                       so both the bare domain (matches the wildcard cert
#                       for *.${DOMAIN}) AND the kube-api subdomain (used by
#                       every downstream consumer: KUBE_API_EXTERNAL_URL,
#                       kube-dc-manager values, platform's kube-api TLSRoute,
#                       internal CoreDNS overrides) validate against the
#                       apiserver cert. See docs/prd/installer-bugs-real-
#                       install.md §D'''''.9.
#                       (CLUSTER_DOMAIN is honored as a fallback for
#                       transitional installs; it should be the full
#                       kube-api.<domain> form. New callers prefer DOMAIN.)
#   POD_CIDR         - Pod network CIDR (default: 10.100.0.0/16)
#   SERVICE_CIDR     - Service network CIDR (default: 10.101.0.0/16)
#   CLUSTER_DNS      - Cluster DNS IP (default: 10.101.0.11)
#   CP_PORT          - Supervisor port of the server being joined
#                       (join mode only; default: 9345)
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

# Default configuration
RKE2_VERSION="${RKE2_VERSION:-v1.35.0+rke2r1}"
NODE_NAME="${NODE_NAME:-$(hostname)}"
NODE_IP="${NODE_IP:-$(hostname -I | awk '{print $1}')}"
EXTERNAL_IP="${EXTERNAL_IP:-${NODE_IP}}"

# Cluster base domain — single source of truth, matches
# clusters/<name>/cluster-config.env's DOMAIN= line. The full
# kube-api.${DOMAIN} hostname is synthesized below and added to
# tls-san so the apiserver cert validates against both forms.
DOMAIN="${DOMAIN:-}"
if [[ -z "${DOMAIN}" ]]; then
    # Back-compat: legacy callers set CLUSTER_DOMAIN to the full
    # kube-api.<domain> form. Strip the optional kube-api. prefix
    # to recover the bare domain. New callers should pass DOMAIN
    # directly to avoid this fixup.
    DOMAIN="${CLUSTER_DOMAIN:-kube-dc.cloud}"
    DOMAIN="${DOMAIN#kube-api.}"
fi
API_HOSTNAME="kube-api.${DOMAIN}"

POD_CIDR="${POD_CIDR:-10.100.0.0/16}"
SERVICE_CIDR="${SERVICE_CIDR:-10.101.0.0/16}"
CLUSTER_DNS="${CLUSTER_DNS:-10.101.0.11}"

# Join parameters (optional)
JOIN_TOKEN="${1:-}"
JOIN_SERVER="${2:-}"
CP_PORT="${CP_PORT:-9345}"

SCRIPT_DIR="$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)"
RANCHER_DIR="/etc/rancher/rke2"

log_info "=== RKE2 Server Installation ==="
log_info "Version:      ${RKE2_VERSION}"
log_info "Node Name:    ${NODE_NAME}"
log_info "Node IP:      ${NODE_IP}"
log_info "External IP:  ${EXTERNAL_IP}"
log_info "Domain:       ${DOMAIN}"
log_info "API hostname: ${API_HOSTNAME}"

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

# Kernel sysctls — bumped from defaults to handle kube-dc workload
# density (kubevirt VMs + dense controller fleet). The default
# fs.inotify.max_user_instances=128 is far too low: virt-handler
# (kubevirt) crashes with "Failed to create an inotify watcher:
# too many open files" once a few controllers grab their fair
# share. Other limits sized for high-pod-density workloads.
#
# Persistent on disk so the values survive reboot. The fleet ALSO
# ships a node-tuning DaemonSet (infrastructure/node-tuning/) that
# applies the same sysctls at runtime — bootstrap node-tuning is
# defense-in-depth, useful for clusters where the operator skips
# the DaemonSet for whatever reason.
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

# OIDC authn was driven by /etc/rancher/auth-conf.yaml until kube-dc v0.3.0
# (commit shalb/kube-dc 7877184). We now use the gardener
# oidc-webhook-authenticator deployed as part of infra-core; the kubeconfig
# at /etc/rancher/oidc-webhook-kubeconfig.yaml is written on each CP node by
# the webhook DaemonSet's init container. Bootstrap therefore writes NO
# authn-config flag — RKE2 starts with cert-only authn until Flux finishes
# infra-core, then the operator runs the per-node cutover from
# docs/internal/oidc-webhook-cloud-rollout.md (kube-dc repo) to add
# --authentication-token-webhook-config-file to kube-apiserver-arg.

# Compute kubelet memory reservations sized to actual node memory.
# Tiers are calibrated to keep system+kube+eviction at ≈10–15% of total
# memory, leaving the bulk for tenant workloads while still protecting
# kubelet/containerd/etcd from kernel OOM (the 2026-05-06 cloud incident
# postmortem motivates this — without reservations, sudden memory
# pressure OOM-killed something in etcd's reconcile path on a 125 GiB
# node, took 4h to recover).
#
# Sizes chosen so the tightest tier (16 GiB) still leaves ~13 GiB for
# pods (≈ 80% allocatable), and the largest tier reserves a flat 4 GiB
# each above 64 GiB (no point reserving more on a 256 GiB box).
MEM_TOTAL_GIB=$(awk '/^MemTotal:/ {printf "%d", $2/1048576}' /proc/meminfo)
if [ "${MEM_TOTAL_GIB:-0}" -ge 64 ]; then
    KUBELET_RESERVED_TIER="large (≥64 GiB)"
    KUBELET_SYS_RESERVED="cpu=500m,memory=4Gi"
    KUBELET_KUBE_RESERVED="cpu=500m,memory=4Gi"
    KUBELET_EVICTION_HARD="memory.available<2Gi,nodefs.available<10%"
    KUBELET_MAX_PODS="250"
elif [ "${MEM_TOTAL_GIB:-0}" -ge 32 ]; then
    KUBELET_RESERVED_TIER="medium (32–64 GiB)"
    KUBELET_SYS_RESERVED="cpu=300m,memory=2Gi"
    KUBELET_KUBE_RESERVED="cpu=300m,memory=2Gi"
    KUBELET_EVICTION_HARD="memory.available<1Gi,nodefs.available<10%"
    KUBELET_MAX_PODS="220"
else
    KUBELET_RESERVED_TIER="small (<32 GiB)"
    KUBELET_SYS_RESERVED="cpu=200m,memory=1Gi"
    KUBELET_KUBE_RESERVED="cpu=200m,memory=1Gi"
    KUBELET_EVICTION_HARD="memory.available<500Mi,nodefs.available<10%"
    KUBELET_MAX_PODS="200"
fi
# max-pods upstream default is 110, sized for ~2 GiB/pod budget. On large
# nodes that's the bottleneck before memory — a 125 GiB CP hit 110 pods
# on 2026-05-06 with multus + rgw stuck Pending. kube-dc is pod-dense: an
# all-in-one / single-node install runs the WHOLE platform on one node and
# exceeds 110 during reconcile ("Too many pods", E2E finding 12 — 200
# worked). So the floor is 200 even on the smallest tier; bigger nodes get
# more headroom (220 / 250).
log_info "Node memory: ${MEM_TOTAL_GIB} GiB → kubelet tier: ${KUBELET_RESERVED_TIER}"
log_info "  system-reserved=${KUBELET_SYS_RESERVED}"
log_info "  kube-reserved=${KUBELET_KUBE_RESERVED}"
log_info "  eviction-hard=${KUBELET_EVICTION_HARD}"
log_info "  max-pods=${KUBELET_MAX_PODS}"

# Generate config.yaml
if [[ -n "${JOIN_TOKEN}" && -n "${JOIN_SERVER}" ]]; then
    log_info "Mode: Joining existing cluster at ${JOIN_SERVER}"

    cat > "${RANCHER_DIR}/config.yaml" <<EOF
server: https://${JOIN_SERVER}:${CP_PORT}
token: ${JOIN_TOKEN}
node-name: ${NODE_NAME}
node-ip: ${NODE_IP}
node-external-ip: ${EXTERNAL_IP}
disable-cloud-controller: true
disable:
  - rke2-ingress-nginx
  # Traefik is RKE2's DEFAULT packaged ingress and must be disabled too:
  # kube-dc serves ingress with Envoy Gateway, which owns the Gateway API
  # CRDs. Left enabled, rke2-traefik-crd fails forever trying to import
  # them ("exists and cannot be imported into the current release") and
  # rke2-traefik then fails on the missing CRDs -- observed on
  # a live cluster at ~600 restarts over 2 days. Worse than the noise:
  # if it ever succeeded it would contend with Envoy for those CRDs and
  # for the :80/:443 hostPorts -- a packaged ingress-nginx once hijacked
  # the API server's :443 exactly that way on a host-network Envoy cluster.
  - rke2-traefik
  - rke2-traefik-crd
cni: none
cluster-cidr: "${POD_CIDR}"
service-cidr: "${SERVICE_CIDR}"
cluster-dns: "${CLUSTER_DNS}"
node-label:
  - kube-dc-manager=true
  - kube-ovn/role=master
# kube-apiserver-arg deliberately omitted at bootstrap — the webhook flag is
# added by docs/internal/oidc-webhook-cloud-rollout.md after infra-core
# brings up the OIDC webhook DaemonSet. Bootstrap apiserver runs cert-only.
kube-controller-manager-arg:
  - bind-address=0.0.0.0
  - authorization-always-allow-paths=/metrics
kube-scheduler-arg:
  - bind-address=0.0.0.0
  - authorization-always-allow-paths=/metrics
# Memory reservation + eviction thresholds — protects kubelet/containerd/etcd
# from kernel OOM under sudden pressure. Without these, the cloud cluster's
# 2026-05-06 incident on a control-plane node happened: memory hit 81G/125G with
# 8G swap fully consumed, kernel OOM-killed something in the etcd-tmp
# reconcile path, bbolt corrupted, node took 4 hours to recover. See
# docs/internal/oidc-webhook-cloud-rollout.md (kube-dc repo) §"Memory
# protections" for rationale + alert thresholds.
kubelet-arg:
  - system-reserved=${KUBELET_SYS_RESERVED}
  - kube-reserved=${KUBELET_KUBE_RESERVED}
  - eviction-hard=${KUBELET_EVICTION_HARD}
  - max-pods=${KUBELET_MAX_PODS}
etcd-arg:
  - listen-metrics-urls=http://0.0.0.0:2381
tls-san:
  - ${DOMAIN}
  - ${API_HOSTNAME}
  - ${EXTERNAL_IP}
  - ${NODE_IP}
  - ${JOIN_SERVER}
advertise-address: ${NODE_IP}
debug: true
EOF
else
    log_info "Mode: Initializing new cluster"

    cat > "${RANCHER_DIR}/config.yaml" <<EOF
node-name: ${NODE_NAME}
node-ip: ${NODE_IP}
node-external-ip: ${EXTERNAL_IP}
disable-cloud-controller: true
disable:
  - rke2-ingress-nginx
  # Traefik is RKE2's DEFAULT packaged ingress and must be disabled too:
  # kube-dc serves ingress with Envoy Gateway, which owns the Gateway API
  # CRDs. Left enabled, rke2-traefik-crd fails forever trying to import
  # them ("exists and cannot be imported into the current release") and
  # rke2-traefik then fails on the missing CRDs -- observed on
  # a live cluster at ~600 restarts over 2 days. Worse than the noise:
  # if it ever succeeded it would contend with Envoy for those CRDs and
  # for the :80/:443 hostPorts -- a packaged ingress-nginx once hijacked
  # the API server's :443 exactly that way on a host-network Envoy cluster.
  - rke2-traefik
  - rke2-traefik-crd
cni: none
cluster-cidr: "${POD_CIDR}"
service-cidr: "${SERVICE_CIDR}"
cluster-dns: "${CLUSTER_DNS}"
node-label:
  - kube-dc-manager=true
  - kube-ovn/role=master
# kube-apiserver-arg deliberately omitted at bootstrap — see comment above.
kube-controller-manager-arg:
  - bind-address=0.0.0.0
  - authorization-always-allow-paths=/metrics
kube-scheduler-arg:
  - bind-address=0.0.0.0
  - authorization-always-allow-paths=/metrics
# Memory reservation + eviction thresholds — protects kubelet/containerd/etcd
# from kernel OOM under sudden pressure. Without these, the cloud cluster's
# 2026-05-06 incident on a control-plane node happened: memory hit 81G/125G with
# 8G swap fully consumed, kernel OOM-killed something in the etcd-tmp
# reconcile path, bbolt corrupted, node took 4 hours to recover. See
# docs/internal/oidc-webhook-cloud-rollout.md (kube-dc repo) §"Memory
# protections" for rationale + alert thresholds.
kubelet-arg:
  - system-reserved=${KUBELET_SYS_RESERVED}
  - kube-reserved=${KUBELET_KUBE_RESERVED}
  - eviction-hard=${KUBELET_EVICTION_HARD}
  - max-pods=${KUBELET_MAX_PODS}
etcd-arg:
  - listen-metrics-urls=http://0.0.0.0:2381
tls-san:
  - ${DOMAIN}
  - ${API_HOSTNAME}
  - ${EXTERNAL_IP}
  - ${NODE_IP}
advertise-address: ${NODE_IP}
debug: true
EOF
fi

log_info "Config written to ${RANCHER_DIR}/config.yaml"

# Install RKE2
log_info "Installing RKE2 ${RKE2_VERSION}..."
export INSTALL_RKE2_VERSION="${RKE2_VERSION}"
export INSTALL_RKE2_TYPE="server"
curl -sfL https://get.rke2.io | sh -

# Enable and start service
log_info "Enabling rke2-server service..."
systemctl enable rke2-server.service

log_info "Starting rke2-server service..."
systemctl start rke2-server.service

# Wait for RKE2 to become active
log_info "Waiting for RKE2 to start (this may take a few minutes)..."
for i in {1..30}; do
    if systemctl is-active rke2-server.service >/dev/null 2>&1; then
        log_info "RKE2 server is active"
        break
    fi
    sleep 10
    echo -n "."
done
echo ""

# Setup kubeconfig
log_info "Setting up kubeconfig..."
mkdir -p ~/.kube
cp "${RANCHER_DIR}/rke2.yaml" ~/.kube/config
chmod 600 ~/.kube/config

# Update kubeconfig with correct server address
sed -i "s|https://127.0.0.1:6443|https://${NODE_IP}:6443|g" ~/.kube/config

# Add kubectl to PATH
export PATH="${PATH}:/var/lib/rancher/rke2/bin"
echo 'export PATH="${PATH}:/var/lib/rancher/rke2/bin"' >> ~/.bashrc

# Display node token for joining other nodes
if [[ -z "${JOIN_TOKEN}" ]]; then
    log_info "=== Cluster initialized successfully ==="
    echo ""
    log_info "Node token (save this for joining other nodes):"
    cat /var/lib/rancher/rke2/server/node-token
    echo ""
fi

log_info "=== Installation Complete ==="
log_info "Kubeconfig: ~/.kube/config"
log_info ""
log_info "To check node status:"
log_info "  export PATH=\${PATH}:/var/lib/rancher/rke2/bin"
log_info "  kubectl get nodes"

# Loud reminder about the manual post-install OIDC cutover. This step is
# documented in docs/internal/oidc-webhook-cloud-rollout.md but operators
# have skipped it before (a production cluster 2026-06-01: cluster ran for 28
# days with cert-only authn, every UI write returning HTTP 401 — see
# kube-dc/docs/prd/installer-bugs-real-install.md §D'''''.11). Print the
# requirement as a banner so it's impossible to miss on the install
# output.
echo ""
echo "======================================================================"
echo "  ⚠  MANDATORY POST-INSTALL STEP: enable OIDC webhook authentication"
echo "======================================================================"
echo ""
echo "  This bootstrap installs RKE2 with cert-only authn. Tenant kubectl"
echo "  via Keycloak JWT, the UI's Manage-Organization API calls, and the"
echo "  k8-manager / db-manager operators all need the OIDC webhook flag"
echo "  on every CP node. Without it, every JWT returns HTTP 401."
echo ""
echo "  After Flux finishes 'infra-core' and the oidc-webhook-authenticator"
echo "  DaemonSet is Ready on every CP node, run the per-node cutover from:"
echo "    docs/internal/oidc-webhook-cloud-rollout.md  (kube-dc repo)"
echo ""
echo "  Quick verification before cutover:"
echo "    kubectl -n oidc-webhook-authenticator get pods -o wide   # one per CP, all Ready"
echo "    ls /etc/rancher/oidc-webhook-kubeconfig.yaml              # on each CP node"
echo ""
echo "  Per-node cutover (append two flags to /etc/rancher/rke2/config.yaml,"
echo "  then restart rke2-server, one CP at a time, gated on apiserver Ready):"
echo "    cat >> /etc/rancher/rke2/config.yaml <<EOF"
echo "    kube-apiserver-arg:"
echo "      - authentication-token-webhook-config-file=/etc/rancher/oidc-webhook-kubeconfig.yaml"
echo "      - authentication-token-webhook-cache-ttl=2m"
echo "    EOF"
echo "    systemctl restart rke2-server"
echo ""
echo "  Per-CP-node check after cutover:"
echo "    ssh root@<node> 'ps -ef | grep [k]ube-apiserver | tr \" \" \"\\n\" | grep authentication-token-webhook'"
echo ""
echo "  ⚠  If Envoy gateway is single-replica + hostNetwork on a CP node, you"
echo "  MUST drain it off that node before restarting rke2-server, or"
echo "  kube-apiserver will fail to come back (bind: address already in use"
echo "  on port 6443). See the runbook's 'Envoy gateway on a CP node'"
echo "  pre-flight section."
echo ""
echo "======================================================================"
