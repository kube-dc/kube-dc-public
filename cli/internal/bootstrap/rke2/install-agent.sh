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

# --- Private-CA trust for the NODE (docs/prd/platform-trust-bundle.md) ---
#
# The platform distributes its CA into ConfigMaps so PODS can verify internal
# endpoints. That does nothing for this host: containerd, kubelet and rke2 read
# the OS trust store. On an air-gapped install the registry is internal and
# served by the org's own CA, so without this the FIRST image pull fails —
# before any pod exists, and therefore before pod-layer trust could help.
#
# Runs before rke2 starts, deliberately. TRUSTED_CA_FILE empty = public CAs
# only, which is correct for an ACME/internet-connected cluster.
install_node_trusted_ca() {
    local src="${1}"

    # Resolve the distro anchor directory up front: it is needed both to install
    # and to report what a node already trusts when no bundle was supplied.
    local anchor_dir suffix
    local -a update_cmd
    if [[ -d /usr/local/share/ca-certificates ]]; then
        # Debian/Ubuntu. The .crt extension is REQUIRED: update-ca-certificates
        # ignores any other suffix, so a .pem lands in the directory, reports
        # success, and is never trusted.
        anchor_dir=/usr/local/share/ca-certificates
        suffix=crt
        update_cmd=(update-ca-certificates)
    elif [[ -d /etc/pki/ca-trust/source/anchors ]]; then
        anchor_dir=/etc/pki/ca-trust/source/anchors
        suffix=pem
        update_cmd=(update-ca-trust extract)
    else
        anchor_dir=""
        suffix=""
        update_cmd=()
    fi

    if [[ -z "${src}" ]]; then
        # No bundle requested. Anchors from an EARLIER install are deliberately
        # left in place — silently untrusting a CA could cut a running node off
        # from its registry. But staying silent would let a re-run advertised as
        # "public CAs only" leave a private CA trusted with no mention of it, so
        # say what this host actually trusts.
        if [[ -n "${anchor_dir}" ]] && compgen -G "${anchor_dir}/kube-dc-platform-ca-*.${suffix}" >/dev/null 2>&1; then
            log_warn "No CA bundle was supplied, but this node STILL TRUSTS a kube-dc CA from an earlier install:"
            local existing
            for existing in "${anchor_dir}"/kube-dc-platform-ca-*."${suffix}"; do
                log_warn "  ${existing} — $(openssl x509 -in "${existing}" -noout -subject 2>/dev/null || echo unreadable)"
            done
            log_warn "To stop trusting it: remove those files and run ${update_cmd[0]}."
        fi
        return 0
    fi

    if [[ ! -s "${src}" ]]; then
        log_error "TRUSTED_CA_FILE=${src} is missing or empty"
        return 1
    fi
    if ! grep -q "BEGIN CERTIFICATE" "${src}"; then
        log_error "TRUSTED_CA_FILE=${src} contains no PEM certificate"
        return 1
    fi
    # Refuse to install anything carrying a private key onto a fleet of hosts.
    if grep -qE "BEGIN (RSA |EC |DSA |OPENSSH |ENCRYPTED |)PRIVATE KEY" "${src}"; then
        log_error "TRUSTED_CA_FILE=${src} contains a PRIVATE KEY — refusing to install it as a node trust anchor"
        return 1
    fi

    # Bind what we install to what the CLI validated.
    #
    # The Go side parses the bundle properly (certificates only, CA only, size
    # bounded); this script only looks for PEM markers. Between the two, the file
    # sits at a predictable path that any local user can reach, so without this
    # the weaker parser decides what becomes a fleet-wide trust anchor. The CLI
    # passes the SHA-256 it validated; a mismatch means the bytes changed after
    # validation and is fatal, never a warning.
    local actual_fp
    if command -v sha256sum >/dev/null 2>&1; then
        actual_fp="$(sha256sum < "${src}" | cut -d' ' -f1)"
    elif command -v openssl >/dev/null 2>&1; then
        actual_fp="$(openssl dgst -sha256 "${src}" | awk '{print $NF}')"
    fi
    if [[ -n "${TRUSTED_CA_SHA256:-}" ]]; then
        if [[ -z "${actual_fp}" ]]; then
            log_error "Cannot hash ${src} (no sha256sum or openssl); refusing to install an unverified trust anchor"
            return 1
        fi
        if [[ "${actual_fp}" != "${TRUSTED_CA_SHA256}" ]]; then
            log_error "TRUSTED_CA_FILE=${src} does NOT match the bundle the installer validated"
            log_error "  expected sha256 ${TRUSTED_CA_SHA256}"
            log_error "  found    sha256 ${actual_fp}"
            log_error "The staged file changed between validation and install; refusing to trust it."
            return 1
        fi
    fi

    if [[ -z "${anchor_dir}" ]]; then
        log_error "No CA anchor directory found (looked for /usr/local/share/ca-certificates and /etc/pki/ca-trust/source/anchors)"
        log_error "This host's distribution is not supported for automatic node trust; install the CA manually before re-running."
        return 1
    fi
    if ! command -v "${update_cmd[0]}" >/dev/null 2>&1; then
        log_error "${update_cmd[0]} not found; cannot refresh the node trust store"
        return 1
    fi
    if ! command -v openssl >/dev/null 2>&1; then
        # Without openssl nothing can confirm the CA became usable, and an
        # unverifiable air-gapped install fails at the first image pull instead.
        log_error "openssl not found; cannot verify that the node trust store accepted the CA"
        return 1
    fi

    # ONE CERTIFICATE PER FILE.
    #
    # Debian's update-ca-certificates only creates CApath hash symlinks for
    # single-certificate files — measured on debian:12: two certs in one .crt
    # produced ZERO new links, the same two certs in two files produced two. The
    # multi-cert file still reaches ca-certificates.crt, so CAfile consumers (Go,
    # and therefore containerd) are fine, but anything using SSL_CERT_DIR alone
    # silently would not trust it.
    local work total i
    work="$(mktemp -d)"
    awk -v dir="${work}" '
        /BEGIN CERTIFICATE/ { n++ }
        n > 0 { print > (dir "/cert-" n ".pem") }
    ' "${src}"
    total="$(find "${work}" -name 'cert-*.pem' | wc -l)"
    if [[ "${total}" -eq 0 ]]; then
        rm -rf "${work}"
        log_error "No certificates could be extracted from ${src}"
        return 1
    fi

    # Keep the previous anchors so a failed update can be rolled back rather than
    # leaving the node with neither the old nor the new trust configuration.
    local backup
    backup="$(mktemp -d)"
    if compgen -G "${anchor_dir}/kube-dc-platform-ca-*.${suffix}" >/dev/null 2>&1; then
        cp -a "${anchor_dir}"/kube-dc-platform-ca-*."${suffix}" "${backup}/" 2>/dev/null || true
    fi

    local changed=0 dest
    for (( i = 1; i <= total; i++ )); do
        dest="${anchor_dir}/kube-dc-platform-ca-${i}.${suffix}"
        if [[ -f "${dest}" ]] && [[ "$(sha256sum < "${work}/cert-${i}.pem" | cut -d' ' -f1)" == "$(sha256sum < "${dest}" | cut -d' ' -f1)" ]]; then
            continue
        fi
        if ! install -m 0644 "${work}/cert-${i}.pem" "${dest}"; then
            # errexit does not apply here: the function runs inside `if !`, which
            # disables it for everything it calls, so an unchecked failure would
            # sail on to the updater and report success.
            log_error "Failed to install ${dest}"
            rm -rf "${work}" "${backup}"
            return 1
        fi
        changed=1
    done
    # Remove anchors left by a LARGER previous bundle, or they stay trusted for ever.
    local stale n
    for stale in "${anchor_dir}"/kube-dc-platform-ca-*."${suffix}"; do
        [[ -e "${stale}" ]] || continue
        n="${stale##*kube-dc-platform-ca-}"
        n="${n%%.*}"
        if [[ "${n}" =~ ^[0-9]+$ ]] && (( n > total )); then
            rm -f "${stale}"
            changed=1
        fi
    done
    rm -rf "${work}"

    if (( changed )); then
        if ! "${update_cmd[@]}" >/dev/null 2>&1; then
            log_error "${update_cmd[0]} failed; rolling back to the previous anchors"
            rm -f "${anchor_dir}"/kube-dc-platform-ca-*."${suffix}"
            if compgen -G "${backup}/kube-dc-platform-ca-*.${suffix}" >/dev/null 2>&1; then
                cp -a "${backup}"/kube-dc-platform-ca-*."${suffix}" "${anchor_dir}/" 2>/dev/null || true
                "${update_cmd[@]}" >/dev/null 2>&1 || true
            fi
            rm -rf "${backup}"
            return 1
        fi
    fi
    rm -rf "${backup}"

    # Prove it landed rather than trusting the exit code. update-ca-certificates
    # exits 0 while silently SKIPPING any file it does not like — most notably one
    # whose name does not end .crt — so a "successful" run can leave the CA
    # completely untrusted. Measured on debian:12: the .pem case exits 0 and the
    # CA is absent from the bundle.
    #
    # Verified even when nothing changed: a previous run could have written the
    # anchor and then failed to rebuild the store, and byte-equality alone would
    # call that success for ever afterwards.
    #
    # -no_check_time because an EXPIRED root is legitimate here: a rotation bundle
    # carries the outgoing CA alongside the incoming one, and that overlap is what
    # makes rotation safe. Measured: without it, `openssl verify` rejects the
    # expired anchor and this function would abort an install that is entirely
    # correct. -partial_chain so an intermediate-only bundle verifies against the
    # installed intermediate rather than demanding a root we were never given.
    local bundle probe rc=1
    for bundle in /etc/ssl/certs/ca-certificates.crt /etc/pki/tls/certs/ca-bundle.crt /etc/ssl/cert.pem; do
        [[ -f "${bundle}" ]] || continue
        rc=0
        # EVERY certificate is probed. Checking only the first would report
        # success whenever that one happened to be trusted already, while the
        # anchors that actually matter were skipped.
        for (( i = 1; i <= total; i++ )); do
            probe="${anchor_dir}/kube-dc-platform-ca-${i}.${suffix}"
            if ! openssl verify -no_check_time -partial_chain -CAfile "${bundle}" "${probe}" >/dev/null 2>&1; then
                log_error "${update_cmd[0]} reported success but the CA is NOT trusted via ${bundle}"
                log_error "  not trusted: $(openssl x509 -in "${probe}" -noout -subject 2>/dev/null || echo "${probe}")"
                log_error "On Debian/Ubuntu the anchor MUST end in .crt or update-ca-certificates ignores it silently."
                rc=1
                break
            fi
        done
        break
    done
    if (( rc != 0 )); then
        return 1
    fi
    log_info "Node trust store updated and verified (${total} CA certificate(s) in ${anchor_dir})"
    return 0
}

if ! install_node_trusted_ca "${TRUSTED_CA_FILE:-}"; then
    # Remove the staged copy even on failure: it is at a predictable path in a
    # world-writable directory, and leaving it behind is a collision target for
    # the next run.
    [[ -n "${TRUSTED_CA_FILE:-}" ]] && rm -f "${TRUSTED_CA_FILE}"
    log_error "Node trust setup failed; refusing to continue."
    log_error "An air-gapped install WILL fail at the first image pull without it."
    exit 1
fi
[[ -n "${TRUSTED_CA_FILE:-}" ]] && rm -f "${TRUSTED_CA_FILE}"


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

# Ensure DNS resolution works.
#
# Probed with `getent hosts`, which is glibc and therefore always present. This
# used to run `host get.rke2.io` — a binary from bind9-host/dnsutils that minimal
# Ubuntu cloud images do NOT ship. On such a host the probe failed with
# "command not found", which was read as "DNS is broken", and the fallback below
# then disabled systemd-resolved and replaced /etc/resolv.conf with public
# resolvers. On a corporate network that breaks every internal name AND sends
# every subsequent query to a third party — from a node whose DNS was fine.
if ! getent hosts get.rke2.io >/dev/null 2>&1; then
    log_warn "Cannot resolve get.rke2.io — falling back to public DNS."
    log_warn "This REPLACES this node's resolver configuration. If this node is"
    log_warn "supposed to use an internal DNS server, stop and fix DNS instead:"
    log_warn "the platform will later need to resolve your own domain from here."
    # Preserve whatever was there so the change is reversible.
    if [[ -e /etc/resolv.conf && ! -e /etc/resolv.conf.pre-kube-dc ]]; then
        cp -a /etc/resolv.conf /etc/resolv.conf.pre-kube-dc 2>/dev/null || true
        log_warn "Previous resolver saved to /etc/resolv.conf.pre-kube-dc"
    fi
    if systemctl is-active systemd-resolved >/dev/null 2>&1; then
        log_warn "Disabling systemd-resolved..."
        systemctl stop systemd-resolved
        systemctl disable systemd-resolved
        rm -f /etc/resolv.conf
    fi
    echo -e "nameserver 8.8.8.8\nnameserver 1.1.1.1" > /etc/resolv.conf
    if ! getent hosts get.rke2.io >/dev/null 2>&1; then
        log_error "Still cannot resolve get.rke2.io after the DNS fallback."
        log_error "This node has no working DNS or no egress. Fix that first —"
        log_error "every later phase (image pulls, ACME, your own domain) needs it."
        exit 1
    fi
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
# Apply ONLY our file — never `sysctl --system`. --system reloads every
# sysctl.d file on the host, so a PRE-EXISTING malformed entry in a
# base-image/operator tuning file (field incident 2026-07-23: a worker
# carried a broken net.ipv4.tcp_rmem/tcp_wmem tuple; EINVAL made sysctl
# exit 1 and set -e killed the whole join) fails an install for values
# that are none of our business. Our values still apply strictly — a
# failure HERE is a real error. Runtime state is owned by the fleet's
# node-tuning DaemonSet anyway.
sysctl -p /etc/sysctl.d/99-kube-dc.conf >/dev/null

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

# --- Embedded registry mirror (spegel) — vm-startup-acceleration Phase A ---
# Agents don't take the embedded-registry flag (server-only), but they must
# have a mirrors entry in registries.yaml to PARTICIPATE in the P2P mirror
# (a node without one "does not participate in the distributed registry in
# any capacity" — RKE2 docs). Same env knobs as install-server.sh.
# The "*" default is deliberate despite RKE2's equal-trust warning: every node
# this script installs is a platform-owned MANAGEMENT-cluster node. Tenant
# workloads run inside VMs and nested clusters, never on these hosts. Narrow
# REGISTRY_MIRROR_SCOPE only for topologies that break that assumption.
EMBEDDED_REGISTRY="${EMBEDDED_REGISTRY:-true}"
REGISTRY_MIRROR_SCOPE="${REGISTRY_MIRROR_SCOPE:-*}"
# Return success only when registries.yaml contains at least one mirror key.
# Enabling embedded-registry against a configs-only/empty mirrors file hangs
# affected RKE2 versions, so preserve operator files but fail closed.
registries_has_mirror() {
    awk '
        BEGIN { in_mirrors = 0; found = 0 }
        /^mirrors:[[:space:]]*/ {
            in_mirrors = 1
            rest = $0
            sub(/^mirrors:[[:space:]]*/, "", rest)
            sub(/[[:space:]]+#.*/, "", rest)
            sub(/^#.*/, "", rest)
            if (rest != "" && rest != "{}") found = 1
            next
        }
        in_mirrors && /^[^[:space:]#]/ { in_mirrors = 0 }
        in_mirrors && /^[[:space:]]+[^[:space:]#][^:]*:[[:space:]]*/ { found = 1 }
        END { exit(found ? 0 : 1) }
    ' "$1"
}

if [[ "${EMBEDDED_REGISTRY}" == "true" ]]; then
    if [[ -f "${RANCHER_DIR}/registries.yaml" ]]; then
        if ! registries_has_mirror "${RANCHER_DIR}/registries.yaml"; then
            log_error "Refusing to enable the embedded registry: existing ${RANCHER_DIR}/registries.yaml has no mirror entries."
            log_error "Add a non-empty mirrors: mapping, or rerun kube-dc bootstrap install with --embedded-registry=false."
            exit 1
        fi
        # Never truncate an operator-managed registries.yaml (private
        # mirrors/credentials/TLS live here). Keep it; the embedded registry
        # still works with whatever mirrors it defines.
        log_warn "registries.yaml already exists — keeping it (not overwriting with the default mirror entry)"
    else
        cat > "${RANCHER_DIR}/registries.yaml" <<EOF
mirrors:
  "${REGISTRY_MIRROR_SCOPE}":
EOF
    fi
    log_info "Embedded registry mirror participation enabled (mirror scope: ${REGISTRY_MIRROR_SCOPE})"
fi

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

# ASSERT. The loop falls through after 120s, so without this the script printed
# "Installation Complete" for an agent that never started — and the node then
# simply never appears in `kubectl get nodes`, with the operator having been told
# the join succeeded.
if ! systemctl is-active rke2-agent.service >/dev/null 2>&1; then
    log_error "rke2-agent did not become active within 120s."
    log_error "The node has NOT joined. Inspect the cause here:"
    log_error "  journalctl -u rke2-agent -n 200 --no-pager"
    log_error "  systemctl status rke2-agent"
    log_error "Most common causes: wrong join token, the server URL is not reachable"
    log_error "on :9345 from this node, or a clock skew large enough to fail TLS."
    exit 1
fi

log_info "=== Installation Complete ==="
log_info ""
log_info "To monitor agent logs:"
log_info "  journalctl -u rke2-agent -f"
log_info ""
log_info "Verify on control plane with:"
log_info "  kubectl get nodes"
