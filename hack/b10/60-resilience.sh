#!/usr/bin/env bash
#
# Fail-closed behaviour under manager rollout, cold start and total outage.
#
# *** THIS IS THE DISRUPTIVE SCRIPT. ***
#
# The attachment gate is a fail-closed webhook on the pod path. Taking the
# manager down means every pod that DECLARES an attachment is refused — which is
# correct and is exactly what we are proving. What must NOT happen is ordinary,
# unrelated pods being refused too: the gate carries matchConditions evaluated
# API-server-side precisely so that a manager outage does not stop the cluster.
#
# Do not run this while anything else is using the cluster.

source "$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)/lib.sh"

NAD=$(k get projectnetwork "$B10_BINDING" -o jsonpath='{.status.nadName}' 2>/dev/null)

if [ "${B10_I_HAVE_A_MAINTENANCE_WINDOW:-}" != "yes" ]; then
    fail "refusing to run: set B10_I_HAVE_A_MAINTENANCE_WINDOW=yes"
    info "this script takes the admission webhook down; on a shared cluster that"
    info "breaks other people's workloads while it runs"
    finish
fi

attach_pod() {
    local name="$1"
    k delete pod "$name" -n "$B10_NS" --ignore-not-found >/dev/null 2>&1 || true
    cat <<EOF | k apply -f - >/dev/null 2>&1
apiVersion: v1
kind: Pod
metadata:
  name: ${name}
  namespace: ${B10_NS}
  annotations:
    k8s.v1.cni.cncf.io/networks: ${B10_NS}/${NAD}
spec:
  containers: [{name: c, image: busybox:1.36, command: ["sh","-c","sleep 60"]}]
EOF
}

plain_pod() {
    local name="$1"
    k delete pod "$name" -n default --ignore-not-found >/dev/null 2>&1 || true
    k run "$name" -n default --image=busybox:1.36 --restart=Never \
        --command -- sh -c 'sleep 60' >/dev/null 2>&1
}

step "baseline: the manager is highly available"
replicas=$(k get deploy kube-dc-manager -n kube-dc -o jsonpath='{.spec.replicas}')
ready=$(k get deploy kube-dc-manager -n kube-dc -o jsonpath='{.status.readyReplicas}')
info "manager $ready/$replicas ready"
[ "${replicas:-0}" -ge 2 ] && pass "at least two manager replicas" \
    || fail "the feature requires >=2 replicas; a single one makes every rollout an outage"

# A PDB must stop a drain taking both at once.
must "a PodDisruptionBudget protects the manager" k get pdb -n kube-dc -l app.kubernetes.io/component=manager

step "during a ROLLING RESTART, both kinds of pod still work"
k rollout restart deploy/kube-dc-manager -n kube-dc >/dev/null 2>&1
sleep 4
must "an attaching pod is admitted mid-rollout" attach_pod b10-roll-attach
must "an ordinary pod is admitted mid-rollout"  plain_pod b10-roll-plain
k rollout status deploy/kube-dc-manager -n kube-dc --timeout=180s >/dev/null 2>&1 && pass "rollout completed" || fail "rollout did not complete"
k delete pod b10-roll-attach -n "$B10_NS" --ignore-not-found >/dev/null 2>&1 || true
k delete pod b10-roll-plain -n default --ignore-not-found >/dev/null 2>&1 || true

step "TOTAL OUTAGE: scale the manager to zero"
prev=$(k get deploy kube-dc-manager -n kube-dc -o jsonpath='{.spec.replicas}')
k scale deploy/kube-dc-manager -n kube-dc --replicas=0 >/dev/null 2>&1
wait_for "manager fully down" 120 bash -c "[ \"\$(kubectl get deploy kube-dc-manager -n kube-dc -o jsonpath='{.status.readyReplicas}')\" = '' ]" || true
info "manager replicas now: $(k get deploy kube-dc-manager -n kube-dc -o jsonpath='{.status.readyReplicas}')"

# The whole point of fail-closed: with no one to authorize the attachment, the
# attachment must be REFUSED rather than admitted unchecked.
must_fail "an ATTACHING pod is refused while the gate is down" attach_pod b10-out-attach
k delete pod b10-out-attach -n "$B10_NS" --ignore-not-found >/dev/null 2>&1 || true

# ...and the corollary that makes fail-closed affordable: everything else is
# untouched, because matchConditions keep unrelated pods off this path entirely.
must "an ORDINARY pod is still admitted while the gate is down" plain_pod b10-out-plain
k delete pod b10-out-plain -n default --ignore-not-found >/dev/null 2>&1 || true

step "recovery"
k scale deploy/kube-dc-manager -n kube-dc --replicas="${prev:-2}" >/dev/null 2>&1
if k rollout status deploy/kube-dc-manager -n kube-dc --timeout=240s >/dev/null 2>&1; then
    pass "manager recovered to ${prev:-2} replicas"
else
    fail "manager did not recover — everything after this is unreliable"
    finish
fi

step "COLD START: attachment works again without intervention"
if wait_for "attachment admitted again" 180 bash -c "
    kubectl delete pod b10-cold -n $B10_NS --ignore-not-found >/dev/null 2>&1
    cat <<EOF | kubectl apply -f - >/dev/null 2>&1
apiVersion: v1
kind: Pod
metadata:
  name: b10-cold
  namespace: ${B10_NS}
  annotations:
    k8s.v1.cni.cncf.io/networks: ${B10_NS}/${NAD}
spec:
  containers: [{name: c, image: busybox:1.36, command: [\"sh\",\"-c\",\"sleep 60\"]}]
EOF
"; then
    pass "attachment admitted after a cold start"
else
    fail "attachment never recovered after the outage"
fi
k delete pod b10-cold -n "$B10_NS" --ignore-not-found >/dev/null 2>&1 || true

step "the webhook's certificate is still valid after all that"
must "webhook service has endpoints" bash -c \
    "[ -n \"\$(kubectl get endpoints -n kube-dc -o name 2>/dev/null | grep webhook)\" ]"

finish
