#!/usr/bin/env bash
#
# A real Pod, attached using the manifest the TENANT API itself hands out.
#
# The snippet is fetched from the product rather than written here on purpose.
# Two of the three most dangerous bugs found in this feature were wrong
# manifests the UI generated — a VM template that detached the tenant VPC, and a
# Deployment annotation that never reached the pods. Both produced a healthy
# looking workload with no VLAN and no error. Writing the manifest by hand in
# this script would have proven the controller works while shipping a product
# that does not.

source "$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)/lib.sh"

NAD=$(k get projectnetwork "$B10_BINDING" -o jsonpath='{.status.nadName}' 2>/dev/null)
[ -n "$NAD" ] || { fail "no NAD published for $B10_BINDING"; finish; }

step "take the attach annotation from the tenant API, not from this script"
ANNOTATION=$(tenant_api "/api/network/${B10_NS}/project-networks" 2>/dev/null |
    python3 -c "
import json,sys
try: d=json.load(sys.stdin)
except Exception: sys.exit(1)
for i in d.get('items',[]):
    if i.get('ready') and i.get('attach'):
        print(i['attach']['podAnnotation']); break
" || true)
if [ -n "$ANNOTATION" ]; then
    pass "tenant API produced an attach annotation: $ANNOTATION"
else
    fail "the tenant API returned no attach snippet for a Ready binding"
    ANNOTATION="${B10_NS}/${NAD}"
    info "falling back to ${ANNOTATION} so the rest of the run still yields data"
fi

step "create the pod with exactly that annotation"
k delete pod b10-pod -n "$B10_NS" --ignore-not-found >/dev/null 2>&1 || true
cat <<EOF | k apply -f - >/dev/null
apiVersion: v1
kind: Pod
metadata:
  name: b10-pod
  namespace: ${B10_NS}
  annotations:
    k8s.v1.cni.cncf.io/networks: ${ANNOTATION}
spec:
  containers:
    - name: net
      image: nicolaka/netshoot:latest
      command: ["sh","-c","sleep 1d"]
      securityContext:
        capabilities:
          add: ["NET_RAW"]
EOF
if wait_for "pod Running" 180 bash -c "[ \"\$(kubectl get pod b10-pod -n $B10_NS -o jsonpath='{.status.phase}')\" = Running ]"; then
    pass "pod is Running"
else
    fail "pod never ran: $(k get pod b10-pod -n "$B10_NS" -o jsonpath='{.status.conditions[*].message}' 2>/dev/null)"
    finish
fi
info "scheduled on: $(k get pod b10-pod -n "$B10_NS" -o jsonpath='{.spec.nodeName}')"

step "the pod really has the VLAN NIC"
ifaces=$(in_pod ip -o link show | awk -F': ' '{print $2}' | tr '\n' ' ')
info "interfaces: $ifaces"
n=$(in_pod ip -o link show | grep -vc ' lo:' || true)
[ "${n:-0}" -ge 2 ] && pass "more than one NIC present" || fail "only the VPC NIC is present — the VLAN did not attach"

step "the VLAN address comes from the granted range"
addrs=$(in_pod ip -o -4 addr show | awk '{print $2" "$4}' | tr '\n' '; ')
info "addresses: $addrs"
net=$(python3 - "$B10_CIDR" <<'PY'
import ipaddress,sys; print(ipaddress.ip_network(sys.argv[1], strict=False))
PY
)
if in_pod ip -o -4 addr show | awk '{print $4}' | cut -d/ -f1 | python3 -c "
import ipaddress,sys
net=ipaddress.ip_network('$net')
print('yes' if any(ipaddress.ip_address(l.strip()) in net for l in sys.stdin if l.strip()) else 'no')" | grep -q yes; then
    pass "an address from $B10_CIDR is configured"
else
    fail "no address from the granted range — IPAM did not serve this binding"
fi

step "THE DEFAULT ROUTE MUST NOT BE CAPTURED BY THE VLAN"
# If the VLAN NIC takes the default route, every tenant workload silently loses
# its platform egress — and the customer's own gateway starts carrying traffic
# it never agreed to carry.
#
# Assert by ADDRESS, not by interface name. In the dual-home layout eth0 is the
# infra NIC and the tenant VPC arrives as net1, so an "is it eth0?" check
# reports a false failure on a correctly-routed pod. What matters is only that
# the default route does not leave via the VLAN.
defroute=$(in_pod ip route show default)
info "default: $defroute"
defdev=$(echo "$defroute" | awk '{for(i=1;i<=NF;i++) if($i=="dev") print $(i+1)}')
vlandev=$(in_pod ip -o -4 addr show | python3 -c "
import ipaddress,sys
net=ipaddress.ip_network('$B10_CIDR', strict=False)
for l in sys.stdin:
    f=l.split()
    if len(f)>3 and '/' in f[3]:
        try:
            if ipaddress.ip_address(f[3].split('/')[0]) in net: print(f[1]); break
        except ValueError: pass")
info "VLAN interface: ${vlandev:-<none>}   default via: ${defdev:-<none>}"
if [ -z "$defdev" ]; then
    fail "the pod has no default route at all"
elif [ "$defdev" = "$vlandev" ]; then
    fail "the VLAN NIC ($vlandev) captured the default route — tenant egress now leaves via the customer gateway"
else
    pass "default route stays off the VLAN (via $defdev, not $vlandev)"
fi

step "MTU is what the segment declared"
mtu=$(in_pod ip -o link show | grep -v ' lo:' | grep -oE 'mtu [0-9]+' | awk '{print $2}' | sort -u | tr '\n' ' ')
info "MTUs: $mtu"
want="${B10_MTU:-1400}"
echo "$mtu" | grep -qw "$want" && pass "an interface carries MTU $want" || fail "expected MTU $want, saw: $mtu"

step "reach a REAL machine on the VLAN (not another pod)"
# Pod-to-pod proves the overlay works. It does not prove we are on the
# customer's physical broadcast domain, which is the entire product claim.
if in_pod ping -c 4 -W 3 "$B10_EXTERNAL_PEER" | tail -3 | grep -q ' 0% packet loss'; then
    pass "reached external peer $B10_EXTERNAL_PEER with no loss"
else
    fail "could not reach $B10_EXTERNAL_PEER — we are not on the physical wire"
    info "$(in_pod ping -c 2 -W 3 "$B10_EXTERNAL_PEER" 2>&1 | tail -3)"
fi

step "large frames, not just small ping"
# MTU asymmetry passes a 56-byte ping and breaks real traffic. Test at the
# declared MTU with DF set, and just above it — the second MUST fail.
payload=$(( want - 28 ))
if in_pod ping -c 2 -W 3 -M do -s "$payload" "$B10_EXTERNAL_PEER" | grep -q ' 0% packet loss'; then
    pass "a full-MTU frame ($want) crosses the wire"
else
    fail "full-MTU frames do not cross — MTU asymmetry between us and the fabric"
fi
if in_pod ping -c 2 -W 3 -M do -s $(( payload + 20 )) "$B10_EXTERNAL_PEER" >/dev/null 2>&1; then
    fail "an OVERSIZED frame passed — the declared MTU is not being enforced"
else
    pass "oversized frames are correctly rejected"
fi

step "cross-node: the wire, not the overlay"
# Same-node traffic can succeed through OVS without touching the physical VLAN.
nodes=$(segment_ready_nodes)
info "segment ready nodes: $nodes"
info "pod node: $(k get pod b10-pod -n "$B10_NS" -o jsonpath='{.spec.nodeName}')"
skip "cross-node pod-to-pod is covered by 40-vm.sh placing the VM on the other node"

finish
