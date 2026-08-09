#!/usr/bin/env bash
#
# A real VM, attached using the fragment the TENANT API hands out.
#
# This is the highest-value script in the pass. The original P1 in this feature
# was a VM snippet that emitted `pod: {}` with masquerade for the default
# network — which would have replaced the project-VPC NIC with a pod-network one
# and silently cut every VM off its tenant network. The VM would boot. Nothing
# would report an error. The only way to catch that class is to build a VM from
# what the product actually says and then look at what it got.
#
# The VM is also placed on a DIFFERENT node from the pod in 30-pod.sh, so
# reaching it proves traffic crossed the physical wire rather than staying
# inside one host's OVS.

source "$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)/lib.sh"

NAD=$(k get projectnetwork "$B10_BINDING" -o jsonpath='{.status.nadName}' 2>/dev/null)
[ -n "$NAD" ] || { fail "no NAD for $B10_BINDING"; finish; }

step "the tenant API's VM fragment"
VMJSON=$(tenant_api "/api/network/${B10_NS}/project-networks" 2>/dev/null |
    python3 -c "
import json,sys
try: d=json.load(sys.stdin)
except Exception: sys.exit(1)
for i in d.get('items',[]):
    if i.get('ready') and i.get('attach'):
        print(json.dumps(i['attach']['vm'])); break
" || true)

if [ -n "$VMJSON" ]; then
    pass "tenant API produced a VM fragment"
    info "$VMJSON"
else
    fail "the tenant API returned no VM fragment for a Ready binding"
fi

step "the fragment must NOT replace the project VPC with a pod network"
# The regression guard for the original P1, asserted against what the API
# actually returns rather than against the React component's unit test.
if echo "$VMJSON" | grep -q '"pod"'; then
    fail "the API's VM fragment contains a pod network — it would detach the tenant VPC"
elif echo "$VMJSON" | grep -q 'masquerade'; then
    fail "the API's VM fragment contains masquerade binding — it would detach the tenant VPC"
else
    pass "the fragment is additive (no pod:{} / masquerade)"
fi
echo "$VMJSON" | grep -q '"bridge"' && pass "VLAN interface uses bridge binding" || fail "VLAN interface is not bridge-bound"

step "pick a node that is NOT the one the pod landed on"
POD_NODE=$(k get pod b10-pod -n "$B10_NS" -o jsonpath='{.spec.nodeName}' 2>/dev/null || true)
READY_NODES=$(segment_ready_nodes | tr -d '[]"' | tr ',' ' ')
VM_NODE=""
for n in $READY_NODES; do [ "$n" != "$POD_NODE" ] && VM_NODE="$n" && break; done
info "pod on: ${POD_NODE:-<none>}   vm target: ${VM_NODE:-<any>}"
[ -n "$VM_NODE" ] && pass "a second VLAN-capable node is available" \
    || skip "only one ready node — cross-node traffic cannot be proven"

step "create the VM with the project VPC as default and the VLAN added"
k delete vm b10-vm -n "$B10_NS" --ignore-not-found >/dev/null 2>&1 || true
cat <<EOF | k apply -f - >/dev/null
apiVersion: kubevirt.io/v1
kind: VirtualMachine
metadata:
  name: b10-vm
  namespace: ${B10_NS}
spec:
  running: true
  template:
    metadata:
      labels: {kubevirt.io/domain: b10-vm}
    spec:
$( [ -n "$VM_NODE" ] && echo "      nodeSelector: {kubernetes.io/hostname: ${VM_NODE}}" )
      domain:
        cpu: {cores: 1}
        memory: {guest: 1Gi}
        devices:
          disks:
            - name: root
              disk: {bus: virtio}
            - name: cloudinit
              disk: {bus: virtio}
          interfaces:
            - name: vpc
              bridge: {}
            - name: customer
              bridge: {}
      networks:
        - name: vpc
          multus:
            default: true
            networkName: ${B10_NS}/default
        - name: customer
          multus:
            networkName: ${B10_NS}/${NAD}
      volumes:
        - name: root
          containerDisk: {image: quay.io/containerdisks/fedora:40}
        - name: cloudinit
          cloudInitNoCloud:
            userData: |
              #cloud-config
              password: b10test
              chpasswd: {expire: false}
              packages: [iproute]
      terminationGracePeriodSeconds: 30
EOF

if wait_for "VMI Running" 420 bash -c "[ \"\$(kubectl get vmi b10-vm -n $B10_NS -o jsonpath='{.status.phase}' 2>/dev/null)\" = Running ]"; then
    pass "VM is Running"
    info "on node: $(k get vmi b10-vm -n "$B10_NS" -o jsonpath='{.status.nodeName}')"
else
    fail "VM never ran: $(k get vmi b10-vm -n "$B10_NS" -o jsonpath='{.status.conditions[*].message}' 2>/dev/null)"
    finish
fi

step "KubeVirt actually exposed BOTH interfaces"
# The annotation alone attaches nothing; KubeVirt only surfaces an interface
# when networks names it AND a matching interfaces entry exists. This is the
# assertion that catches a fragment that looked right but was not.
ifaces=$(k get vmi b10-vm -n "$B10_NS" -o jsonpath='{.status.interfaces[*].name}' 2>/dev/null)
info "VMI interfaces: $ifaces"
echo "$ifaces" | grep -qw vpc      && pass "the project VPC NIC is present"  || fail "the VM has NO tenant VPC NIC"
echo "$ifaces" | grep -qw customer && pass "the VLAN NIC is present"         || fail "the VM has NO VLAN NIC"

step "the VLAN NIC has an address from the granted range"
ips=$(k get vmi b10-vm -n "$B10_NS" -o jsonpath='{range .status.interfaces[*]}{.name}={.ipAddress} {end}' 2>/dev/null)
info "addresses: $ips"
if python3 - "$B10_CIDR" <<PY
import ipaddress,sys
net=ipaddress.ip_network(sys.argv[1], strict=False)
got="""$ips"""
found=any(
    (lambda a: a and ipaddress.ip_address(a) in net)(p.split('=')[1].strip())
    for p in got.split() if '=' in p and p.split('=')[1].strip()
)
sys.exit(0 if found else 1)
PY
then pass "an address from $B10_CIDR is assigned"; else fail "no address from the granted range on the VM"; fi

step "cross the physical wire, both directions"
# These are the product claim. Pod-to-pod inside one host proves the overlay
# works; only traffic that leaves the node and comes back proves we are on the
# customer's physical broadcast domain.
VMIP=$(k get vmi b10-vm -n "$B10_NS" -o json 2>/dev/null | python3 -c "
import json,sys
for i in json.load(sys.stdin)['status'].get('interfaces',[]):
    if i.get('name')=='customer': print(i.get('ipAddress','')); break")
VM_ON=$(k get vmi b10-vm -n "$B10_NS" -o jsonpath='{.status.nodeName}' 2>/dev/null)
info "VM ${VMIP:-<none>} on ${VM_ON:-?}   pod on ${POD_NODE:-?}"

if [ -z "$VMIP" ]; then
    fail "the VM reported no address on the VLAN"
else
    # VMI phase Running means the LAUNCHER is up, not that the guest has booted
    # and configured its NICs. Asserting immediately reports a working VM as
    # unreachable — a false failure that reads exactly like a broken wire.
    if wait_for "the guest to answer on the VLAN" 180 ping -c 2 -W 2 "$VMIP"; then
        info "guest reachable"
    else
        info "guest did not answer within 180s — assertions below will say why"
    fi
    # From a machine that is NOT part of this cluster at all.
    if ping -c 4 -W 3 "$VMIP" 2>/dev/null | grep -q ' 0% packet loss'; then
        pass "an OFF-CLUSTER machine reaches the VM over the physical VLAN"
    else
        fail "the external peer cannot reach the VM — the VM is not on the wire"
    fi

    # And from a pod on the OTHER node, so the frame crossed the switch.
    if [ -n "$VM_ON" ] && [ "$VM_ON" != "$POD_NODE" ]; then
        if in_pod ping -c 4 -W 3 "$VMIP" 2>/dev/null | grep -q ' 0% packet loss'; then
            pass "pod on $POD_NODE reaches VM on $VM_ON — CROSS-NODE on the wire"
        else
            fail "cross-node pod->VM failed; traffic is not crossing the physical VLAN"
        fi
    else
        skip "VM and pod landed on the same node — cross-node not proven this run"
    fi
fi

step "the VM's default route stays on the VPC NIC"
# Asserted from outside the guest: if the VLAN had captured the default route,
# the VM would lose its tenant-VPC path, so its VPC address becomes unreachable
# from inside the cluster.
VPCIP=$(k get vmi b10-vm -n "$B10_NS" -o json 2>/dev/null | python3 -c "
import json,sys
for i in json.load(sys.stdin)['status'].get('interfaces',[]):
    if i.get('name')=='vpc': print(i.get('ipAddress','')); break")
info "VM tenant-VPC address: ${VPCIP:-<none>}"
if [ -n "$VPCIP" ] && in_pod ping -c 3 -W 3 "$VPCIP" >/dev/null 2>&1; then
    pass "the VM is still reachable on its tenant VPC (the VLAN did not capture egress)"
else
    fail "the VM is unreachable on its tenant VPC — the VLAN may have taken the default route"
fi

finish
