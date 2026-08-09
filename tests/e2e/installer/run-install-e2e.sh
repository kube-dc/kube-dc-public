#!/usr/bin/env bash
#
# Greenfield installer E2E harness (installer-bugs "D3").
#
# Codifies the manual customer-flow acceptance run into a repeatable,
# assertion-driven script: `bootstrap install` -> `fetch-kubeconfig` ->
# CRD-absent precheck -> `bootstrap init` -> health assertions. The governing
# rule from docs/internal/e2e-installer-test-plan.md §8 holds: ANY required
# manual intervention is a FINDING, not a workaround — this script performs
# zero `kubectl patch`/bridge commands between steps, so if the flow needs one
# to pass, that's a real bug to fix in the CLI/fleet, not here.
#
# Every real value is supplied via env/flags — nothing about a specific
# operator/cluster is hardcoded (enforced by cli/internal/lint/no_real_infra_test.go,
# which scans this tree). Copy installer-e2e.env.example, fill it in OUTSIDE
# the repo, and `source` it (or export the vars) before running.
#
# Required env:
#   E2E_NODE            RKE2 node name to install (e.g. master-1)
#   E2E_SSH_HOST        user@host for the node (public FIP/EIP), e.g. ubuntu@203.0.113.10
#   E2E_DOMAIN          cluster wildcard domain, e.g. kdc.example.com
#   E2E_KUBECONFIG      path the fetched admin kubeconfig is written to
#   E2E_INIT_CONFIG     path to the `bootstrap init --config` env spec
# Optional env:
#   E2E_PRESET          install/init preset (default: internal-only)
#   E2E_CLI             path to the kube-dc binary (default: kube-dc on PATH)
#   E2E_SSH_KEY         private key to add to a scratch ssh-agent (default: use the running agent)
#   E2E_PLATFORM_TIMEOUT  seconds to wait for the platform Kustomization Ready (default: 1500)
#
# Exit non-zero on the first failed gate so it doubles as a CI gate.

set -o pipefail
set -u

# ---- config from env ----
NODE="${E2E_NODE:?set E2E_NODE (RKE2 node name)}"
SSH_HOST="${E2E_SSH_HOST:?set E2E_SSH_HOST (user@host, node public IP)}"
DOMAIN="${E2E_DOMAIN:?set E2E_DOMAIN (wildcard domain)}"
KUBECONFIG_OUT="${E2E_KUBECONFIG:?set E2E_KUBECONFIG (path to write the fetched kubeconfig)}"
INIT_CONFIG="${E2E_INIT_CONFIG:?set E2E_INIT_CONFIG (path to the bootstrap init --config spec)}"
PRESET="${E2E_PRESET:-internal-only}"
CLI="${E2E_CLI:-kube-dc}"
PLATFORM_TIMEOUT="${E2E_PLATFORM_TIMEOUT:-1500}"

export KUBECONFIG="$KUBECONFIG_OUT"

RED=$'\033[0;31m'; GREEN=$'\033[0;32m'; YELLOW=$'\033[1;33m'; NC=$'\033[0m'
mark(){ echo; echo "########## $* ## $(date -u +%H:%M:%S) ##########"; }
pass(){ echo "${GREEN}✓ $*${NC}"; }
fail(){ echo "${RED}✗ $*${NC}"; exit 1; }

# Optional scratch ssh-agent (a CI runner has no agent; a laptop usually does).
if [ -n "${E2E_SSH_KEY:-}" ]; then
  eval "$(ssh-agent -s)" >/dev/null
  ssh-add "$E2E_SSH_KEY" >/dev/null 2>&1 || fail "ssh-add $E2E_SSH_KEY failed"
  trap 'ssh-agent -k >/dev/null 2>&1 || true' EXIT
fi

command -v "$CLI" >/dev/null 2>&1 || fail "kube-dc CLI not found ($CLI)"
command -v kubectl >/dev/null 2>&1 || fail "kubectl not found"

# ---- STEP 1: bootstrap install (fresh RKE2) ----
mark "STEP 1: bootstrap install ($NODE, preset=$PRESET)"
"$CLI" bootstrap install "$NODE" \
  --ssh-host "$SSH_HOST" --domain "$DOMAIN" --preset "$PRESET" \
  --ssh-accept-new-host-keys || fail "bootstrap install (INSTALL_RC=$?)"
pass "INSTALL_RC=0"

# ---- STEP 2: fetch-kubeconfig ----
mark "STEP 2: fetch-kubeconfig"
"$CLI" bootstrap fetch-kubeconfig "$NODE" \
  --ssh-host "$SSH_HOST" --domain "$DOMAIN" \
  --kubeconfig "$KUBECONFIG_OUT" --set-current || fail "fetch-kubeconfig (FETCH_RC=$?)"
kubectl get --raw='/healthz' >/dev/null 2>&1 || fail "fetched kubeconfig can't reach the apiserver"
pass "FETCH_RC=0, apiserver reachable"

# ---- STEP 2b: greenfield precheck — the CDI StorageProfile CRD must be ABSENT ----
# (The CDI-split fix's whole point is that this CRD arrives at RUNTIME. If it's
#  already present, this isn't a genuine greenfield cluster and the test's
#  central assertion — platform converges without a manual CDI bridge — is void.)
mark "STEP 2b: PRE-ASSERT storageprofiles.cdi.kubevirt.io CRD absent (greenfield)"
if kubectl get crd storageprofiles.cdi.kubevirt.io >/dev/null 2>&1; then
  fail "storageprofiles CRD already present — not a clean greenfield cluster"
fi
pass "storageprofiles CRD absent (clean greenfield)"

# ---- STEP 3: bootstrap init (NO manual workarounds between steps) ----
mark "STEP 3: bootstrap init --config $INIT_CONFIG --no-tty --yes"
"$CLI" bootstrap init --config "$INIT_CONFIG" --no-tty --yes || fail "bootstrap init (INIT_RC=$?)"
pass "INIT_RC=0"

# ---- Assertions: the platform layer converged WITHOUT a manual bridge ----
mark "ASSERT: platform Kustomization + CDI-split converged"
kubectl -n flux-system wait --for=condition=Ready \
  "kustomization/platform" --timeout="${PLATFORM_TIMEOUT}s" || fail "platform Kustomization not Ready"
pass "platform Kustomization Ready"

kubectl -n flux-system wait --for=condition=Ready \
  "kustomization/platform-cdi-storage" --timeout=300s || fail "platform-cdi-storage Kustomization not Ready"
kubectl get crd storageprofiles.cdi.kubevirt.io >/dev/null 2>&1 \
  || fail "storageprofiles CRD never registered (CDI operator didn't install it)"
kubectl get storageprofile local-path >/dev/null 2>&1 \
  || fail "StorageProfile local-path not applied by platform-cdi-storage"
pass "CDI operator registered the CRD at runtime; platform-cdi-storage applied StorageProfile (no manual bridge)"

# ---- OpenBao must be RAFT, never file (the file backend 405s generate-root) ----
mark "ASSERT: OpenBao storage backend is raft"
STORAGE="$(kubectl -n openbao exec openbao-0 -c openbao -- \
  sh -c 'wget -qO- http://127.0.0.1:8200/v1/sys/seal-status 2>/dev/null' 2>/dev/null \
  | grep -o '"storage_type":"[a-z]*"' || true)"
case "$STORAGE" in
  *raft*) pass "OpenBao storage_type=raft" ;;
  *file*) fail "OpenBao on FILE backend — generate-root/controller-auth/KMS will not work (set OPENBAO_HA_ENABLED=true)" ;;
  *)      echo "${YELLOW}! could not read OpenBao storage_type ($STORAGE) — skipping${NC}" ;;
esac

# ---- VERSION GATE: deployed components must match the release pins ----
# The r10 run passed all structural gates while silently installing the
# ANCIENT chart-side fallbacks (KUBE_DC_VERSION:=v0.2.7 class) because no
# pins were written and nothing asserted versions. The starter now ships
# bootstrap/release-pins.env and add-cluster.sh appends it to the cluster
# env; this gate proves the pins actually reached the cluster.
# Fleet dir: E2E_FLEET env, else the KUBE_DC_INIT_REPO key from the
# init-config spec (the same value the CLI used as --repo).
FLEET="${E2E_FLEET:-$(grep -E '^KUBE_DC_INIT_REPO=' "$INIT_CONFIG" 2>/dev/null | head -1 | cut -d= -f2)}"
if [[ -z "$FLEET" || ! -d "$FLEET" ]]; then
  fail "cannot locate the fleet dir for the version gate (set E2E_FLEET or KUBE_DC_INIT_REPO in \$E2E_INIT_CONFIG)"
fi
ENV_FILE="$FLEET/clusters/e2e/cluster-config.env"
PINNED_MGR="$(grep -E '^KUBE_DC_MANAGER_TAG=' "$ENV_FILE" 2>/dev/null | head -1 | cut -d= -f2 | awk '{print $1}')"
if [[ -z "$PINNED_MGR" ]]; then
  fail "no KUBE_DC_MANAGER_TAG pin in $ENV_FILE — greenfield install would take the stale chart-side fallback (release-pins.env missing from the starter?)"
fi
DEPLOYED_MGR="$(kubectl -n kube-dc get deploy kube-dc-manager \
  -o jsonpath='{.spec.template.spec.containers[0].image}' 2>/dev/null | sed 's/.*://')"
if [[ "$DEPLOYED_MGR" == "$PINNED_MGR" ]]; then
  pass "manager version matches release pin ($PINNED_MGR)"
else
  fail "manager version drift: deployed=$DEPLOYED_MGR pinned=$PINNED_MGR — pins did not reach the cluster"
fi

mark "RESULT: greenfield installer E2E PASSED — INIT_RC=0, platform converged, no manual bridge"
echo "Next (optional): post-install health via the Ginkgo suite —"
echo "  KUBE_DC_E2E_DOMAIN=$DOMAIN KUBE_DC_E2E_ORG=<org> KUBE_DC_E2E_PROJECT=<proj> \\"
echo "    make test-e2e-focus FOCUS='KMSKey'   # asserts KMSKey Ready + currentVersion"
