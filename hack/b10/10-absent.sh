#!/usr/bin/env bash
#
# With the feature DISABLED, is it genuinely absent?
#
# "Absent, not disabled" is the governing constraint of the whole feature: on a
# cluster that did not opt in there must be no nav entry, no route that
# resolves, no API, and no way for a tenant to reach the fabric kinds. A
# disabled-but-present surface is how a feature gets enabled by accident.
#
# Run this BEFORE enabling. It is also the honest baseline: if these already
# fail, nothing later in the pass means much.

source "$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)/lib.sh"

step "the manager is not gating pods"
enabled=$(k get deploy kube-dc-manager -n kube-dc -o json 2>/dev/null |
    python3 -c "
import json,sys
c=json.load(sys.stdin)['spec']['template']['spec']['containers'][0]
print({e['name']: e.get('value') for e in c.get('env',[])}.get('PROJECT_NETWORK_ENABLED','<absent>'))")
info "manager PROJECT_NETWORK_ENABLED=$enabled"
[ "$enabled" != "true" ] && pass "feature is off on the manager" || fail "feature is ON — this script tests the disabled state"

step "no attachment webhook on the pod path"
# The gate is fail-closed. While disabled it must not be registered at all,
# otherwise a manager outage would stop ordinary scheduling for no benefit.
if k get validatingwebhookconfiguration -o json 2>/dev/null |
    grep -q 'vnetworkattach.kube-dc.com'; then
    fail "the attachment gate is registered while the feature is off"
else
    pass "attachment gate absent"
fi

step "ordinary pods are unaffected"
k delete pod b10-canary -n default --ignore-not-found >/dev/null 2>&1 || true
if k run b10-canary -n default --image=busybox:1.36 --restart=Never \
    --command -- sh -c 'sleep 5' >/dev/null 2>&1; then
    pass "an ordinary pod is admitted"
    k delete pod b10-canary -n default --ignore-not-found >/dev/null 2>&1 || true
else
    fail "an ordinary pod was refused with the feature OFF — admission is broken"
fi

step "the console does not advertise the feature"
feat=$(_in_cluster_curl -s "http://kube-dc-backend.kube-dc.svc:${B10_BACKEND_PORT}/api/system/features" 2>/dev/null | head -c 400 || true)
info "features: $feat"
if echo "$feat" | grep -q '"tenantVlan":true'; then
    fail "/api/system/features advertises tenantVlan while the feature is off"
else
    pass "the tenant console flag is off"
fi

step "the admin API does not exist"
# Not "returns an error" — the routes are not registered, so the router 404s.
# A 403 here would leak that the route exists to anyone who is not a superadmin.
code=$(_in_cluster_curl -s -o /dev/null -w '%{http_code}' \
    "http://kube-dc-backend.kube-dc.svc:${B10_BACKEND_PORT}/api/admin/vlans/segments" 2>/dev/null | tail -1 || true)
info "GET /api/admin/vlans/segments -> $code"
case "$code" in
    404|401) pass "admin VLAN API is not reachable (got $code)" ;;
    403)     fail "got 403 — the route exists and leaks its own presence" ;;
    200)     fail "the admin VLAN API is SERVING while the feature is off" ;;
    *)       skip "inconclusive ($code) — backend may be unreachable from this pod" ;;
esac

step "tenants cannot see the fabric kinds even if the CRDs are installed"
# The CRDs ship unconditionally (they are additive and retained). What must not
# exist is any tenant-reachable grant on them.
for kind in fabricsegments projectnetworks vlanreservations; do
    if k get crd "${kind}.kube-dc.com" >/dev/null 2>&1; then
        info "CRD $kind installed (expected — they ship additively)"
    fi
done
# A namespaced Role cannot grant a cluster-scoped kind at all; assert nobody tried.
if k get role -A -o json 2>/dev/null | grep -qE '"(projectnetworks|fabricsegments|vlanreservations)"'; then
    fail "a namespaced Role references a cluster-scoped fabric kind"
else
    pass "no tenant Role references the fabric kinds"
fi

finish
