#!/usr/bin/env bash
#
# Declare a segment and grant it — THROUGH THE ADMIN API, not by hand.
#
# This is deliberate. Writing the CRs directly would be easier and would prove
# less: the last two dangerous bugs in this feature were both in what the
# product hands a user, and the admin API is half of that surface. If the
# console cannot produce a working segment, the feature does not work,
# regardless of what the controller can do with a hand-written manifest.

source "$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)/lib.sh"

step "the org/project this grant is for"
if k get ns "$B10_NS" >/dev/null 2>&1; then
    pass "project namespace $B10_NS exists"
else
    fail "namespace $B10_NS does not exist — create the org/project first"
    finish
fi

step "declare the segment via the admin API"
# NB: do NOT inline "${B10_NODE_SELECTOR:-{}}" — bash parses that as
# ${VAR:-{} followed by a literal }, which appends a stray brace and produces
# invalid JSON the API rejects with an opaque 400.
_sel="${B10_NODE_SELECTOR:-}"
[ -z "$_sel" ] && _sel='{}'
body=$(printf '{"driver":"kube-ovn-underlay","providerNetwork":"%s","vlanId":%s,"mtu":%s,"nodeSelector":%s}' \
    "$B10_PROVIDER_NETWORK" "$B10_VLAN_ID" "${B10_MTU:-1400}" "$_sel")
info "POST /api/admin/vlans/segments  $body"
resp=$(admin_api POST /api/admin/vlans/segments "$body" || true)
info "$(echo "$resp" | tail -3)"

if k get fabricsegment "$B10_SEGMENT" >/dev/null 2>&1; then
    pass "segment $B10_SEGMENT created"
else
    fail "segment was not created — the admin API could not declare a wire"
    finish
fi

step "the name is DERIVED, not chosen"
# The name IS the isolation key. If the console could pick it, two declarations
# of one wire would not collide and the whole exclusivity story is gone.
drv=$(k get fabricsegment "$B10_SEGMENT" -o jsonpath='{.spec.driver}')
vln=$(k get fabricsegment "$B10_SEGMENT" -o jsonpath='{.spec.vlanId}')
upl=$(k get fabricsegment "$B10_SEGMENT" -o jsonpath='{.spec.providerNetwork}')
info "driver=$drv uplink=$upl vlan=$vln"
[ "pn-${upl}-${vln}" = "$B10_SEGMENT" ] && pass "name matches the derivation" || fail "name does not match pn-<uplink>-<vlan>"

step "the same wire cannot be declared twice"
# ovs-cni shares the 'pn' class with kube-ovn-underlay precisely so that
# declaring the same wire under a different driver still collides.
dup=$(admin_api POST /api/admin/vlans/segments "$body" || true)
if echo "$dup" | grep -q 'SEGMENT_EXISTS\|409'; then
    pass "a second declaration of the same wire is refused"
else
    fail "the same wire was declared twice: $(echo "$dup" | tail -2)"
fi

step "the segment becomes ready on the fabric"
if wait_for "segment ready" 120 bash -c "[ \"\$(kubectl get fabricsegment $B10_SEGMENT -o jsonpath='{.status.ready}')\" = true ]"; then
    pass "segment reports ready"
    info "ready nodes: $(segment_ready_nodes)"
else
    fail "segment never became ready — check the ProviderNetwork and node labels"
fi

step "foreign objects on the wire are reported, not hidden"
foreign=$(k get fabricsegment "$B10_SEGMENT" -o jsonpath='{.status.foreignObjects}' 2>/dev/null)
if [ -z "$foreign" ] || [ "$foreign" = "[]" ]; then
    pass "no aliases on this wire"
else
    fail "foreign objects present (an alias can bridge another project on): $foreign"
fi

step "grant it to the project via the admin API"
gbody=$(printf '{"name":"%s","org":"%s","project":"%s","segmentRef":"%s","cidrBlock":"%s","gateway":"%s"%s}' \
    "$B10_BINDING" "$B10_ORG" "$B10_PROJECT" "$B10_SEGMENT" "$B10_CIDR" "$B10_GATEWAY" \
    "${B10_EXCLUDE_IPS:+,\"excludeIps\":$B10_EXCLUDE_IPS}")
info "POST /api/admin/vlans/assignments  $gbody"
gresp=$(admin_api POST /api/admin/vlans/assignments "$gbody" || true)
info "$(echo "$gresp" | tail -3)"

if wait_for "binding Ready" 180 bash -c "[ \"\$(kubectl get projectnetwork $B10_BINDING -o jsonpath='{.status.phase}')\" = Ready ]"; then
    pass "binding $B10_BINDING reached Ready"
else
    fail "binding did not reach Ready (phase=$(binding_phase)) — $(k get projectnetwork "$B10_BINDING" -o jsonpath='{.status.conditions[?(@.type=="Ready")].message}' 2>/dev/null)"
fi

step "a second project cannot take the same wire"
# This is the cross-tenant case the entire design exists to prevent. The
# reservation enforces it in storage; the API should refuse before that.
dupg=$(admin_api POST /api/admin/vlans/assignments \
    "$(printf '{"name":"b10-intruder","org":"%s","project":"%s","segmentRef":"%s","cidrBlock":"%s","gateway":"%s"}' \
        "$B10_ORG" "other" "$B10_SEGMENT" "$B10_CIDR" "$B10_GATEWAY")" || true)
if echo "$dupg" | grep -q 'SEGMENT_ALREADY_BOUND\|409'; then
    pass "a second project is refused the wire"
else
    fail "a second grant on one wire was ACCEPTED: $(echo "$dupg" | tail -2)"
fi
k delete projectnetwork b10-intruder --ignore-not-found >/dev/null 2>&1 || true

step "the segment cannot be deleted out from under a live grant"
del=$(admin_api DELETE "/api/admin/vlans/segments/$B10_SEGMENT" || true)
if echo "$del" | grep -q 'SEGMENT_IN_USE\|409'; then
    pass "segment deletion refused while assigned"
else
    fail "the declaration was deletable under a live binding: $(echo "$del" | tail -2)"
fi

step "the NAD the tenant will reference exists"
nad=$(k get projectnetwork "$B10_BINDING" -o jsonpath='{.status.nadName}' 2>/dev/null)
must "NAD $nad published into $B10_NS" k get net-attach-def "$nad" -n "$B10_NS"

finish
