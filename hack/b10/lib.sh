#!/usr/bin/env bash
# Shared helpers for the B-10 acceptance scripts.
#
# Everything here is deliberately explicit about WHICH identity performs an
# action. The whole feature is an authorization story, so a test that quietly
# used cluster-admin where a tenant should have been would prove nothing.

set -euo pipefail

B10_DIR="$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)"
RESULTS="${B10_DIR}/RESULTS.md"

: "${KUBECONFIG:?set KUBECONFIG to the target cluster}"

# Refuse to touch a cluster we were not aimed at.
#
# These scripts create physical-network objects and deliberately try to breach
# tenant isolation. Running them somewhere unintended is the worst failure mode
# this harness has, and it is a very easy mistake: the bastion exports
# KUBECONFIG globally, so any soft default silently inherits whatever cluster
# that points at.
if [ -n "${B10_EXPECT_CONTEXT:-}" ]; then
    _ctx="$(kubectl config current-context 2>/dev/null || true)"
    if [ "$_ctx" != "$B10_EXPECT_CONTEXT" ]; then
        echo "REFUSING TO RUN" >&2
        echo "  expected context: $B10_EXPECT_CONTEXT" >&2
        echo "  actual context:   ${_ctx:-<none>}" >&2
        echo "  KUBECONFIG:       $KUBECONFIG" >&2
        exit 1
    fi
fi
: "${B10_ORG:=b10}"
: "${B10_PROJECT:=wire}"
: "${B10_NS:=${B10_ORG}-${B10_PROJECT}}"

# The physical wire under test. These MUST describe real fabric — a VLAN that is
# actually trunked to the VLAN-capable nodes — or every result is meaningless.
: "${B10_PROVIDER_NETWORK:?set B10_PROVIDER_NETWORK (the kube-ovn ProviderNetwork)}"
: "${B10_VLAN_ID:?set B10_VLAN_ID}"
: "${B10_CIDR:?set B10_CIDR (the customer-approved range, exclusively ours)}"
: "${B10_GATEWAY:?set B10_GATEWAY}"
# A real machine on that VLAN that is NOT part of this cluster. Pod-to-pod
# proves far less than reaching something that was already on the wire.
: "${B10_EXTERNAL_PEER:?set B10_EXTERNAL_PEER (an IP on the VLAN, not in this cluster)}"

B10_SEGMENT="pn-${B10_PROVIDER_NETWORK}-${B10_VLAN_ID}"
B10_BINDING="${B10_BINDING:-dc-vlan}"

# --- output ------------------------------------------------------------------

_c() { printf '\033[%sm%s\033[0m' "$1" "$2"; }
info() { echo "  $*"; }
step() { echo; echo "$(_c '1;36' "── $* ──")"; }
pass() { echo "  $(_c '32' 'PASS')  $*"; _record "PASS" "$*"; }
fail() { echo "  $(_c '31' 'FAIL')  $*"; _record "FAIL" "$*"; FAILED=$((FAILED + 1)); }
skip() { echo "  $(_c '33' 'SKIP')  $*"; _record "SKIP" "$*"; }
FAILED=0

_record() {
    printf '| %s | %s | %s |\n' "$(date -u +%H:%M:%S)" "$1" "$2" >>"$RESULTS"
}

finish() {
    echo
    if [ "$FAILED" -gt 0 ]; then
        echo "$(_c '31' "$FAILED check(s) FAILED")"
        exit 1
    fi
    echo "$(_c '32' 'all checks passed')"
}

# Assert a command succeeds / fails, naming what it proves rather than what it runs.
must() {
    local what="$1"; shift
    if "$@" >/dev/null 2>&1; then pass "$what"; else fail "$what"; fi
}

must_fail() {
    local what="$1"; shift
    if "$@" >/dev/null 2>&1; then fail "$what (it was ALLOWED)"; else pass "$what"; fi
}

# --- cluster helpers ---------------------------------------------------------

k() { kubectl "$@"; }

# The backend admin API, as a platform admin would drive it. Uses the in-cluster
# service so this exercises the same path the console does.
#
# Every call is time-boxed. `kubectl run --rm -i` waits for the pod to schedule,
# pull and complete, and on a busy cluster that can block indefinitely — which
# turns an acceptance script into something that looks hung rather than failed.
: "${B10_CURL_TIMEOUT:=60}"
: "${B10_CURL_IMAGE:=curlimages/curl:8.10.1}"
# The Service port, not the container port. These differ (3333 in-container,
# 8080 on the Service), and using the wrong one yields an empty response that
# looks like "the API is absent" — which is precisely the thing 10-absent.sh is
# trying to distinguish from a real absence.
: "${B10_BACKEND_PORT:=8080}"

_in_cluster_curl() {
    local name="b10-curl-$$-$RANDOM"
    # `kubectl run --rm` writes 'pod "x" deleted' to STDOUT, appended to the
    # SAME line as the payload with no separator — so an HTTP code comes back as
    # '401pod "x" deleted', matches no case arm, and reads as "inconclusive"
    # rather than as the 401 it is. A line-based filter does not catch it; strip
    # the suffix.
    timeout "$B10_CURL_TIMEOUT" kubectl run "$name" --rm -i --restart=Never \
        --image="$B10_CURL_IMAGE" --namespace kube-dc \
        --request-timeout="${B10_CURL_TIMEOUT}s" -- "$@" 2>/dev/null \
        | sed -E 's/pod "[^"]*" deleted$//'
    local rc=${PIPESTATUS[0]}
    # --rm cannot clean up after a timeout kill.
    [ $rc -ne 0 ] && kubectl delete pod "$name" -n kube-dc --ignore-not-found \
        --force --grace-period=0 >/dev/null 2>&1
    return $rc
}

# A platform-admin bearer token, obtained the way the console does.
#
# The admin API is behind requirePlatformAdmin (master-realm JWT), so an
# unauthenticated call gets 401 and the whole "drive the product path" premise
# collapses into testing the 401 handler. Credentials come from master-config,
# the same Secret the platform itself reads.
_admin_token() {
    if [ -n "${_B10_TOKEN:-}" ]; then printf '%s' "$_B10_TOKEN"; return; fi
    local u p url
    u=$(k get secret master-config -n kube-dc -o jsonpath='{.data.user}' 2>/dev/null | base64 -d)
    p=$(k get secret master-config -n kube-dc -o jsonpath='{.data.password}' 2>/dev/null | base64 -d)
    url=$(k get secret master-config -n kube-dc -o jsonpath='{.data.url}' 2>/dev/null | base64 -d)
    # client_id matters: the backend verifies audience against
    # kube-dc-admin-console (controllers/admin/middleware/masterRealmAuth.js).
    # An admin-cli token authenticates the same human but carries audience
    # "account", and is rejected as INVALID_TOKEN — which reads like bad
    # credentials rather than a wrong client.
    _B10_TOKEN=$(curl -sk --max-time 20 -X POST \
        "$url/realms/master/protocol/openid-connect/token" \
        -d "client_id=${B10_ADMIN_CLIENT_ID:-kube-dc-admin-console}" \
        -d "username=$u" -d "password=$p" -d grant_type=password \
        2>/dev/null | python3 -c "import json,sys;print(json.load(sys.stdin).get('access_token',''))" 2>/dev/null)
    printf '%s' "$_B10_TOKEN"
}

# A TENANT bearer token, from the org's own Keycloak realm.
#
# The tenant endpoint authorizes with a SelfSubjectAccessReview under the
# CALLER's token, so an admin token proves nothing here — and no token at all
# just exercises the 401 path. Requires B10_TENANT_USER/B10_TENANT_PASSWORD for
# a user who is a member of the project (see the runbook).
_tenant_token() {
    if [ -n "${_B10_TENANT_TOKEN:-}" ]; then printf '%s' "$_B10_TENANT_TOKEN"; return; fi
    [ -n "${B10_TENANT_USER:-}" ] || return 0
    local url
    url=$(k get secret master-config -n kube-dc -o jsonpath='{.data.url}' 2>/dev/null | base64 -d)
    _B10_TENANT_TOKEN=$(curl -sk --max-time 20 -X POST \
        "$url/realms/${B10_ORG}/protocol/openid-connect/token" \
        -d "client_id=${B10_TENANT_CLIENT_ID:-kube-dc}" \
        -d "username=$B10_TENANT_USER" -d "password=$B10_TENANT_PASSWORD" \
        -d grant_type=password 2>/dev/null |
        python3 -c "import json,sys;print(json.load(sys.stdin).get('access_token',''))" 2>/dev/null)
    printf '%s' "$_B10_TENANT_TOKEN"
}

# GET a tenant-scoped endpoint as the tenant.
tenant_api() {
    local path="$1"
    local tok; tok=$(_tenant_token)
    local args=(-s --max-time 25 "http://kube-dc-backend.kube-dc.svc:${B10_BACKEND_PORT}${path}")
    [ -n "$tok" ] && args+=(-H "Authorization: Bearer $tok")
    _in_cluster_curl "${args[@]}"
}

admin_api() {
    local method="$1" path="$2" body="${3:-}"
    local tok; tok=$(_admin_token)
    local args=(-s --max-time 25 -o /dev/stdout -w '\n%{http_code}' -X "$method"
        "http://kube-dc-backend.kube-dc.svc:${B10_BACKEND_PORT}${path}"
        -H 'Content-Type: application/json')
    [ -n "$tok" ] && args+=(-H "Authorization: Bearer $tok")
    [ -n "$body" ] && args+=(-d "$body")
    _in_cluster_curl "${args[@]}"
}

# Nodes the segment considers usable, from live status rather than our assumption.
segment_ready_nodes() {
    k get fabricsegment "$B10_SEGMENT" -o jsonpath='{.status.readyNodes}' 2>/dev/null
}

binding_phase() {
    k get projectnetwork "$B10_BINDING" -o jsonpath='{.status.phase}' 2>/dev/null
}

# Wait for a condition, reporting what it was waiting for on timeout.
wait_for() {
    local what="$1" timeout="$2"; shift 2
    local deadline=$(( SECONDS + timeout ))
    while [ $SECONDS -lt $deadline ]; do
        if "$@" >/dev/null 2>&1; then return 0; fi
        sleep 3
    done
    info "timed out after ${timeout}s waiting for: $what"
    return 1
}

# Run a command inside the test pod on the tenant network.
in_pod() {
    k exec -n "$B10_NS" b10-pod -- "$@" 2>/dev/null
}
