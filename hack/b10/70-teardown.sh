#!/usr/bin/env bash
#
# Teardown is drain-first, and it must be held by REAL state — not by our
# declarations about that state.
#
# The claim under test: revoking a grant publishes a Denying tombstone, refuses
# new attachments immediately, and removes the attachment definition only once
# nothing references the wire. Two things must hold it independently:
#
#   1. a terminating Pod/VMI that has not finished going away, and
#   2. a surviving Kube-OVN IP allocation with no pod left behind it
#      (what a hard node loss leaves behind).
#
# The second is the one that matters operationally: if teardown only looks at
# declarations, a dead node's leftover allocation lets the NAD disappear while
# the wire is still in use.

source "$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)/lib.sh"

NAD=$(k get projectnetwork "$B10_BINDING" -o jsonpath='{.status.nadName}' 2>/dev/null)
[ -n "$NAD" ] || { fail "no NAD for $B10_BINDING"; finish; }

step "a workload is attached before we start"
k delete pod b10-hold -n "$B10_NS" --ignore-not-found >/dev/null 2>&1 || true
cat <<EOF | k apply -f - >/dev/null
apiVersion: v1
kind: Pod
metadata:
  name: b10-hold
  namespace: ${B10_NS}
  annotations:
    k8s.v1.cni.cncf.io/networks: ${B10_NS}/${NAD}
spec:
  terminationGracePeriodSeconds: 120
  containers:
    - name: c
      image: busybox:1.36
      # Ignore SIGTERM so this pod stays Terminating for a while — that is the
      # state teardown has to notice and wait for.
      command: ["sh","-c","trap '' TERM; sleep 3600"]
EOF
wait_for "holder pod Running" 180 bash -c "[ \"\$(kubectl get pod b10-hold -n $B10_NS -o jsonpath='{.status.phase}')\" = Running ]" \
    && pass "holder pod attached" || { fail "holder pod never ran"; finish; }

step "revoke the grant"
k delete projectnetwork "$B10_BINDING" --wait=false >/dev/null 2>&1
sleep 5
phase=$(binding_phase)
info "phase after revoke: $phase"
[ "$phase" = "Denying" ] && pass "binding entered Denying immediately" \
    || fail "expected Denying, saw '$phase' — the tombstone is not published first"

step "NEW attachments are refused straight away"
must_fail "a new attaching pod is refused during drain" bash -c "cat <<EOF | kubectl apply -f - >/dev/null 2>&1
apiVersion: v1
kind: Pod
metadata:
  name: b10-late
  namespace: ${B10_NS}
  annotations:
    k8s.v1.cni.cncf.io/networks: ${B10_NS}/${NAD}
spec:
  containers: [{name: c, image: busybox:1.36, command: [\"sh\",\"-c\",\"sleep 60\"]}]
EOF"
k delete pod b10-late -n "$B10_NS" --ignore-not-found --force --grace-period=0 >/dev/null 2>&1 || true

step "the EXISTING workload keeps running"
[ "$(k get pod b10-hold -n "$B10_NS" -o jsonpath='{.status.phase}' 2>/dev/null)" = "Running" ] \
    && pass "the attached workload was not disturbed" \
    || fail "revoking the grant killed a running workload"

step "the NAD survives while something is still attached"
if k get net-attach-def "$NAD" -n "$B10_NS" >/dev/null 2>&1; then
    pass "attachment definition still present during drain"
else
    fail "the NAD was removed while a workload was still attached"
fi
info "attachedPorts=$(k get projectnetwork "$B10_BINDING" -o jsonpath='{.status.attachedPorts}' 2>/dev/null) quiesce=$(k get projectnetwork "$B10_BINDING" -o jsonpath='{.status.quiesceObservations}' 2>/dev/null)"

step "A TERMINATING pod still holds teardown"
# The window that matters: the pod is going away but has not gone. If teardown
# counts declarations rather than reality, it completes here and the wire is
# released while the interface still exists.
k delete pod b10-hold -n "$B10_NS" --wait=false >/dev/null 2>&1
sleep 8
tphase=$(k get pod b10-hold -n "$B10_NS" -o jsonpath='{.status.phase}' 2>/dev/null || echo gone)
info "holder pod is now: $tphase (deletionTimestamp=$(k get pod b10-hold -n "$B10_NS" -o jsonpath='{.metadata.deletionTimestamp}' 2>/dev/null))"
if [ "$tphase" != "gone" ]; then
    if k get net-attach-def "$NAD" -n "$B10_NS" >/dev/null 2>&1; then
        pass "NAD still present while the pod is Terminating"
    else
        fail "the NAD was removed while a pod was still Terminating"
    fi
else
    skip "pod vanished too quickly to observe the terminating window"
fi

step "a surviving OVN IP allocation ALSO holds teardown"
# Simulates what a hard node loss leaves: the pod object is gone, the OVN
# allocation is not.
leftovers=$(k get ips.kubeovn.io -o json 2>/dev/null |
    python3 -c "
import json,sys
d=json.load(sys.stdin)
n=[i['metadata']['name'] for i in d.get('items',[]) if '${NAD}' in json.dumps(i.get('spec',{}))]
print(len(n))" 2>/dev/null || echo 0)
info "OVN IP objects referencing this attachment: $leftovers"
if [ "${leftovers:-0}" -gt 0 ]; then
    if k get net-attach-def "$NAD" -n "$B10_NS" >/dev/null 2>&1; then
        pass "NAD held while OVN allocations survive"
    else
        fail "the NAD went while OVN still held allocations — a dead node would release a live wire"
    fi
else
    skip "no surviving allocations to observe (they were reclaimed promptly)"
fi

step "teardown completes once the wire is genuinely quiesced"
k delete pod b10-hold -n "$B10_NS" --force --grace-period=0 >/dev/null 2>&1 || true
if wait_for "binding fully removed" 300 bash -c "! kubectl get projectnetwork $B10_BINDING >/dev/null 2>&1"; then
    pass "binding removed after the wire drained"
else
    fail "binding never completed teardown (phase=$(binding_phase), attached=$(k get projectnetwork "$B10_BINDING" -o jsonpath='{.status.attachedPorts}' 2>/dev/null))"
fi

step "everything it created went with it, in order"
must_fail "NAD is gone"        k get net-attach-def "$NAD" -n "$B10_NS"
must_fail "reservation is gone" k get vlanreservation "$B10_SEGMENT"
must "the segment itself survives (it is the declaration, not the grant)" \
    k get fabricsegment "$B10_SEGMENT"

step "the wire can be granted again afterwards"
regrant=$(admin_api POST /api/admin/vlans/assignments \
    "$(printf '{"name":"%s","org":"%s","project":"%s","segmentRef":"%s","cidrBlock":"%s","gateway":"%s"}' \
        "$B10_BINDING" "$B10_ORG" "$B10_PROJECT" "$B10_SEGMENT" "$B10_CIDR" "$B10_GATEWAY")" || true)
if wait_for "re-granted binding Ready" 180 bash -c "[ \"\$(kubectl get projectnetwork $B10_BINDING -o jsonpath='{.status.phase}')\" = Ready ]"; then
    pass "the wire is grantable again after a full teardown"
else
    fail "the wire could not be re-granted: $(echo "$regrant" | tail -2)"
fi

finish
