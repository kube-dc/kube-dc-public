#!/usr/bin/env bash
#
# The breach matrix — every refusal the design claims, tested as an attacker
# would attempt it.
#
# These are the checks that justify calling the feature multi-tenant safe. Each
# one corresponds to a way a tenant could reach a wire they were not granted, or
# escape the constraints of one they were. A PASS here means the attempt was
# REFUSED.
#
# Run this with the feature ENABLED and a live binding in place.

source "$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)/lib.sh"

NAD=$(k get projectnetwork "$B10_BINDING" -o jsonpath='{.status.nadName}' 2>/dev/null)
VICTIM_NS="$B10_NS"
: "${B10_OTHER_NS:?set B10_OTHER_NS to a DIFFERENT project namespace that holds NO grant}"

# Try to create a pod and report whether admission allowed it.
try_pod() {
    local ns="$1" name="$2"; shift 2
    local ann="$1"; shift
    k delete pod "$name" -n "$ns" --ignore-not-found >/dev/null 2>&1 || true
    cat <<EOF | k apply -f - >/dev/null 2>&1
apiVersion: v1
kind: Pod
metadata:
  name: ${name}
  namespace: ${ns}
  annotations:
${ann}
spec:
  containers:
    - name: c
      image: busybox:1.36
      command: ["sh","-c","sleep 300"]
EOF
}

cleanup_pod() { k delete pod "$2" -n "$1" --ignore-not-found >/dev/null 2>&1 || true; }

step "B1 — a project with no grant cannot reference the NAD"
# The single most important case: cross-namespace Multus reference.
must_fail "pod in $B10_OTHER_NS referencing $VICTIM_NS/$NAD is refused" \
    try_pod "$B10_OTHER_NS" b10-breach-1 "    k8s.v1.cni.cncf.io/networks: ${VICTIM_NS}/${NAD}"
cleanup_pod "$B10_OTHER_NS" b10-breach-1

step "B2 — the JSON array form of the same attempt"
# The comma form and the JSON form are different parsers; a gate that only
# understands one is not a gate.
must_fail "JSON-form cross-namespace reference is refused" \
    try_pod "$B10_OTHER_NS" b10-breach-2 "    k8s.v1.cni.cncf.io/networks: '[{\"name\":\"${NAD}\",\"namespace\":\"${VICTIM_NS}\"}]'"
cleanup_pod "$B10_OTHER_NS" b10-breach-2

step "B3 — v1.multus-cni.io/default-network is also an attach vector"
must_fail "default-network cross-namespace reference is refused" \
    try_pod "$B10_OTHER_NS" b10-breach-3 "    v1.multus-cni.io/default-network: ${VICTIM_NS}/${NAD}"
cleanup_pod "$B10_OTHER_NS" b10-breach-3

step "B4 — naming the logical switch directly, bypassing Multus"
must_fail "direct logical_switch annotation is refused" \
    try_pod "$B10_OTHER_NS" b10-breach-4 "    ovn.kubernetes.io/logical_switch: pn-${B10_BINDING}"
cleanup_pod "$B10_OTHER_NS" b10-breach-4

step "B5 — the other provider spelling of the same annotation"
must_fail "alternate provider-scoped spelling is refused" \
    try_pod "$B10_OTHER_NS" b10-breach-5 "    ${NAD}.${VICTIM_NS}.kubernetes.io/logical_switch: pn-${B10_BINDING}"
cleanup_pod "$B10_OTHER_NS" b10-breach-5

step "B6 — a tenant cannot write a NAD to grant themselves a wire"
must_fail "tenant-created NAD is refused" bash -c "cat <<EOF | kubectl apply -f - >/dev/null 2>&1
apiVersion: k8s.cni.cncf.io/v1
kind: NetworkAttachmentDefinition
metadata:
  name: b10-selfgrant
  namespace: ${B10_OTHER_NS}
spec:
  config: '{\"cniVersion\":\"0.3.1\",\"type\":\"kube-ovn\",\"provider\":\"${NAD}.${VICTIM_NS}.ovn\"}'
EOF"
k delete net-attach-def b10-selfgrant -n "$B10_OTHER_NS" --ignore-not-found >/dev/null 2>&1 || true

step "B7 — an alias Subnet cannot be created on a held wire"
must_fail "alias Subnet on the held VLAN is refused" bash -c "cat <<EOF | kubectl apply -f - >/dev/null 2>&1
apiVersion: kubeovn.io/v1
kind: Subnet
metadata:
  name: b10-alias
spec:
  protocol: IPv4
  cidrBlock: ${B10_CIDR}
  vlan: ${B10_SEGMENT}
  provider: b10-alias.${B10_OTHER_NS}.ovn
EOF"
k delete subnet b10-alias --ignore-not-found >/dev/null 2>&1 || true

step "B8 — an alias Vlan cannot be created on the same tag"
must_fail "second Vlan on the same (provider,id) is refused" bash -c "cat <<EOF | kubectl apply -f - >/dev/null 2>&1
apiVersion: kubeovn.io/v1
kind: Vlan
metadata:
  name: b10-alias-vlan
spec:
  id: ${B10_VLAN_ID}
  provider: ${B10_PROVIDER_NETWORK}
EOF"
k delete vlan b10-alias-vlan --ignore-not-found >/dev/null 2>&1 || true

step "B9 — the reservation cannot be deleted out from under a live wire"
# The reservation is the storage-atomic ownership fence. If it can be removed,
# a second project can claim the segment.
must_fail "deleting the VlanReservation is refused" \
    k delete vlanreservation "$B10_SEGMENT" --ignore-not-found=false

step "B10 — a tenant cannot pin their own IP, MAC or routes ON THE WIRE"
# PROVIDER-SCOPED keys only. The bare ovn.kubernetes.io/* annotations address a
# pod on its OWN tenant VPC, which is the tenant's business and is deliberately
# not gated — gating them is what deadlocked the CNI, since kube-ovn writes its
# own bare bookkeeping onto every pod.
#
# The threat is the provider-scoped form: it pins an address or MAC on the
# CUSTOMER's physical VLAN, where their hardware lives, and is exactly how you
# collide with equipment IPAM cannot see.
_prov="${NAD}.${B10_NS}.ovn.kubernetes.io"
for key in ip_address mac_address routes gateway default_route ip_pool; do
    case "$key" in
        mac_address)   val='"00:11:22:33:44:55"' ;;
        routes)        val="'"'"'[{\"dst\":\"0.0.0.0/0\",\"gw\":\"${B10_GATEWAY}\"}]'"'"'" ;;
        default_route) val='"true"' ;;
        *)             val="${B10_GATEWAY}" ;;
    esac
    must_fail "provider-scoped $key is refused on the VLAN" \
        try_pod "$B10_NS" b10-breach-10 "    ${_prov}/${key}: ${val}
    k8s.v1.cni.cncf.io/networks: ${B10_NS}/${NAD}"
    cleanup_pod "$B10_NS" b10-breach-10
done

step "B11 — placement on a node that is NOT on the wire"
# A node in the ProviderNetwork but not in the VLAN gives a NIC on a wire that
# carries nothing. Admission must refuse rather than produce a silent black hole.
if [ -n "${B10_UNPREPARED_NODE:-}" ]; then
    must_fail "pod pinned to $B10_UNPREPARED_NODE is refused" bash -c "cat <<EOF | kubectl apply -f - >/dev/null 2>&1
apiVersion: v1
kind: Pod
metadata:
  name: b10-breach-11
  namespace: ${B10_NS}
  annotations:
    k8s.v1.cni.cncf.io/networks: ${B10_NS}/${NAD}
spec:
  nodeName: ${B10_UNPREPARED_NODE}
  containers:
    - name: c
      image: busybox:1.36
      command: [\"sh\",\"-c\",\"sleep 300\"]
EOF"
    cleanup_pod "$B10_NS" b10-breach-11
else
    skip "set B10_UNPREPARED_NODE to prove placement is refused off the wire"
fi

step "B12 — stale readyNodes must not authorize a placement"
# The design says admission re-derives usable nodes from LIVE Node and
# ProviderNetwork state rather than trusting FabricSegment.status. Prove it by
# writing a lie into status and checking it is not believed.
if [ -n "${B10_UNPREPARED_NODE:-}" ]; then
    k patch fabricsegment "$B10_SEGMENT" --subresource=status --type=merge \
        -p "{\"status\":{\"readyNodes\":[\"${B10_UNPREPARED_NODE}\"]}}" >/dev/null 2>&1 || true
    must_fail "a forged readyNodes entry does not authorize placement" bash -c "cat <<EOF | kubectl apply -f - >/dev/null 2>&1
apiVersion: v1
kind: Pod
metadata:
  name: b10-breach-12
  namespace: ${B10_NS}
  annotations:
    k8s.v1.cni.cncf.io/networks: ${B10_NS}/${NAD}
spec:
  nodeName: ${B10_UNPREPARED_NODE}
  containers:
    - name: c
      image: busybox:1.36
      command: [\"sh\",\"-c\",\"sleep 300\"]
EOF"
    cleanup_pod "$B10_NS" b10-breach-12
    info "status will be re-derived by the controller on its next pass"
else
    skip "needs B10_UNPREPARED_NODE"
fi

finish
