#!/usr/bin/env bash
#
# Is this cluster fit to run B-10 on?
#
# Every check here is something that, if wrong, makes the rest of the pass
# produce results that look like feature failures but are not. The most
# expensive mistake in an acceptance run is debugging the wrong thing.

source "$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)/lib.sh"

step "RC identity"
info "cluster:  $(k config current-context)"
info "manager:  $(k get deploy kube-dc-manager -n kube-dc -o jsonpath='{.spec.template.spec.containers[0].image}' 2>/dev/null)"
info "backend:  $(k get deploy kube-dc-backend -n kube-dc -o jsonpath='{.spec.template.spec.containers[0].image}' 2>/dev/null)"
info "kube-ovn: $(k get ds kube-ovn-cni -n kube-system -o jsonpath='{.spec.template.spec.containers[0].image}' 2>/dev/null)"
info "kubevirt: $(k get kubevirt kubevirt -n kubevirt -o jsonpath='{.status.observedKubeVirtVersion}' 2>/dev/null)"

step "no wedged tenancy (a stuck org holds VPCs and hot-loops IPAM)"
stuck=$(k get organizations.kube-dc.com -A -o json 2>/dev/null |
    python3 -c "import json,sys;print(sum(1 for o in json.load(sys.stdin)['items'] if o['metadata'].get('deletionTimestamp')))")
[ "$stuck" = "0" ] && pass "no organizations stuck terminating" || fail "$stuck organization(s) stuck terminating — clear B-09 first"

termns=$(k get ns -o json 2>/dev/null |
    python3 -c "import json,sys;print(sum(1 for n in json.load(sys.stdin)['items'] if n['status']['phase']=='Terminating'))")
[ "$termns" -le 1 ] && pass "namespaces draining normally ($termns terminating)" || fail "$termns namespaces stuck terminating"

step "IPAM is healthy (a saturated pool looks exactly like a broken binding)"
eips=$(k get ovn-eip --no-headers 2>/dev/null | wc -l | tr -d ' ')
noaddr=$(k logs -n kube-system -l app=kube-ovn-controller --since=5m 2>/dev/null | grep -c NoAvailableAddress || true)
info "ovn-eips: $eips   NoAvailableAddress in 5m: $noaddr"
[ "$noaddr" -lt 10 ] && pass "IPAM not saturated" || fail "IPAM is failing allocations — results would be unattributable"

step "fabric: is the VLAN actually there?"
if k get provider-network "$B10_PROVIDER_NETWORK" >/dev/null 2>&1; then
    pass "ProviderNetwork $B10_PROVIDER_NETWORK exists"
    ready=$(k get provider-network "$B10_PROVIDER_NETWORK" -o jsonpath='{.status.readyNodes}' 2>/dev/null)
    info "ready nodes: ${ready:-<none>}"
    n=$(echo "$ready" | tr -cd ',' | wc -c)
    [ "${n:-0}" -ge 1 ] && pass "at least two nodes carry the uplink" \
        || fail "need >=2 VLAN-capable nodes to prove cross-node traffic on the wire"
else
    fail "ProviderNetwork $B10_PROVIDER_NETWORK not found — nothing to test against"
fi

step "a node that is deliberately NOT prepared (for the negative placement case)"
total=$(k get nodes --no-headers 2>/dev/null | wc -l | tr -d ' ')
info "$total node(s) in the cluster"
[ "$total" -ge 3 ] && pass "a third node exists to prove placement is refused there" \
    || skip "fewer than 3 nodes — the unprepared-node case cannot be proven here"

step "is anything else using this cluster?"
# Only 60-resilience.sh is genuinely disruptive — it scales the manager to zero,
# so every attaching pod is refused while it runs. The rest of the pass creates
# its own org, namespace, pods and VM, all isolated from the health suite's
# fixtures, and nothing restarts. So a concurrent run BLOCKS the resilience step
# and merely warns for the others; treating it as fatal for the whole pass means
# waiting for an empty hour to test something that does not need one.
_suite_busy=0
if pgrep -af "playwright:v1.61" 2>/dev/null | grep -q "docker run"; then _suite_busy=1; fi
running=$(k get configmap stage-health -n kube-dc -o jsonpath='{.data.running\.json}' 2>/dev/null || echo '{}')
[ -n "$running" ] && [ "$running" != "{}" ] && _suite_busy=1

if [ "$_suite_busy" = "0" ]; then
    pass "no health run in flight — the full pass, including 60-resilience.sh, is safe"
elif [ "${B10_INCLUDE_RESILIENCE:-no}" = "yes" ]; then
    fail "a health run is IN FLIGHT and 60-resilience.sh is enabled — it would take admission down under them"
    info "in flight: $running"
else
    pass "a health run is in flight, but 60-resilience.sh is not enabled — the rest does not disturb it"
    info "in flight: $running"
fi

step "THE PRECONDITION NO SCRIPT CAN CHECK (§18.4)"
cat <<EOF
  Kube-OVN IPAM will allocate from ${B10_CIDR} on VLAN ${B10_VLAN_ID}.
  The customer's own network allocates on that same wire, and NOTHING here can
  detect a collision: a binding looks perfectly healthy while IPAM hands a
  workload an address their hardware or DHCP already uses. The symptom is
  intermittent ARP flapping and packet loss, blamed on anything but us.

  Confirm, as a human:
    - ${B10_CIDR} is approved by whoever owns that VLAN
    - it is excluded from every other DHCP scope and static assignment
    - exactly one allocator owns it
EOF
if [ "${B10_ADDRESS_AUTHORITY_CONFIRMED:-}" = "yes" ]; then
    pass "address authority confirmed by the operator"
else
    fail "set B10_ADDRESS_AUTHORITY_CONFIRMED=yes once the range is genuinely exclusive"
fi

finish
