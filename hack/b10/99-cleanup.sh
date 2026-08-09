#!/usr/bin/env bash
#
# Remove everything this run created and put the cluster back.
#
# Order matters and mirrors teardown: workloads first, then the grant (which
# drains), then the declaration. Forcing it in the wrong order is how you end up
# with a wedged binding and an orphaned reservation — the B-09 shape.
#
# Run this even if the pass failed. Especially then.

source "$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)/lib.sh"

step "workloads"
for p in b10-pod b10-hold b10-late b10-cold b10-out-attach b10-roll-attach \
         b10-breach-1 b10-breach-2 b10-breach-3 b10-breach-4 b10-breach-5 \
         b10-breach-10 b10-breach-11 b10-breach-12; do
    k delete pod "$p" -n "$B10_NS" --ignore-not-found --force --grace-period=0 >/dev/null 2>&1 || true
    [ -n "${B10_OTHER_NS:-}" ] && k delete pod "$p" -n "$B10_OTHER_NS" --ignore-not-found --force --grace-period=0 >/dev/null 2>&1 || true
done
for p in b10-canary b10-roll-plain b10-out-plain; do
    k delete pod "$p" -n default --ignore-not-found --force --grace-period=0 >/dev/null 2>&1 || true
done
k delete vm b10-vm -n "$B10_NS" --ignore-not-found >/dev/null 2>&1 || true
info "workloads removed"

step "stray objects the breach matrix may have left"
k delete net-attach-def b10-selfgrant -n "${B10_OTHER_NS:-default}" --ignore-not-found >/dev/null 2>&1 || true
k delete subnet b10-alias --ignore-not-found >/dev/null 2>&1 || true
k delete vlan b10-alias-vlan --ignore-not-found >/dev/null 2>&1 || true
k delete projectnetwork b10-intruder --ignore-not-found >/dev/null 2>&1 || true
info "stray objects removed"

step "the grant (drains first — this is expected to take a moment)"
if k get projectnetwork "$B10_BINDING" >/dev/null 2>&1; then
    k delete projectnetwork "$B10_BINDING" --wait=false >/dev/null 2>&1 || true
    if wait_for "binding removed" 300 bash -c "! kubectl get projectnetwork $B10_BINDING >/dev/null 2>&1"; then
        pass "binding drained and removed"
    else
        fail "binding did not drain — phase=$(binding_phase), attached=$(k get projectnetwork "$B10_BINDING" -o jsonpath='{.status.attachedPorts}' 2>/dev/null)"
        info "do NOT force-remove its finalizer: that orphans the reservation and the wire"
    fi
else
    pass "no binding to remove"
fi

step "the declaration"
if k get fabricsegment "$B10_SEGMENT" >/dev/null 2>&1; then
    k delete fabricsegment "$B10_SEGMENT" --ignore-not-found >/dev/null 2>&1 || true
    wait_for "segment removed" 120 bash -c "! kubectl get fabricsegment $B10_SEGMENT >/dev/null 2>&1" \
        && pass "segment removed" || fail "segment did not delete"
else
    pass "no segment to remove"
fi

step "nothing of ours is left"
left=$(k get vlanreservation --no-headers 2>/dev/null | grep -c "$B10_SEGMENT" || true)
[ "${left:-0}" = "0" ] && pass "no reservation left behind" || fail "$left reservation(s) still present"

step "restore the disabled state"
cat <<EOF
  This script does NOT flip projectNetwork.enabled back — that is a chart value,
  and changing it here would diverge the cluster from what GitOps says it is.

  To return the cluster to the shipped default:
    - set projectNetwork.enabled=false (and manager.replicaCount back) in the
      fleet overlay for this cluster, and let Flux reconcile it
    - confirm with:  hack/b10/10-absent.sh
EOF

finish
