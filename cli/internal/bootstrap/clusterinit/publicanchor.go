package clusterinit

import (
	"encoding/binary"
	"fmt"
	"net"
	"sort"
	"strings"
)

// Public-VLAN per-node anchor derivation + validation.
//
// A MetalLB L2 VIP on the routed public VLAN is announced from an OVS
// internal port (EXT_NET_PUBLIC_ANCHOR_INTERFACE, access-tagged with
// the public VLAN) on each gateway node. That port MUST hold a
// per-node address, and the announcing node needs a policy route so
// VIP-sourced replies leave via the public VLAN's gateway:
//
//   - with no address the kernel cannot ARP the public gateway (a
//     sender of 0.0.0.0 is ignored by typical DC firewalls), so
//     replies can never be forwarded back out the public segment;
//   - with no route to the public CIDR the reply routes out the
//     node's default (management VLAN) — asymmetric through a
//     stateful upstream firewall, which drops it. The VIP then
//     accepts the TCP SYN and the client sees a silent timeout.
//
// The fleet's ext-net-bridge-tag DaemonSet consumes
// EXT_NET_PUBLIC_ANCHOR_IPS and enforces address + policy routing
// continuously; init's job is to derive a correct map and reserve the
// addresses in kube-ovn IPAM before the first tenant allocation.
//
// RESERVATION IS LOAD-BEARING: kube-ovn honors excludeIps only for
// NEW allocations. An LRP or EIP that grabbed an address before the
// exclusion keeps it, and the host + OVN then both answer ARP for one
// IP on one L2 segment. Deriving the exclusion at init time — before
// any tenant exists — is what makes the anchor block safe.

// derivePublicAnchorEnv fills the public-anchor keys that follow
// mechanically from the operator's public-VLAN inputs. Called from
// EnvMapFor after --set layering: only empty keys are filled, so an
// explicit operator value always wins.
//
// Derived only when METALLB_FLOATING_IP belongs to EXT_PUBLIC_CIDR.
// An ext-cloud VIP on a cluster that also has ext-public must keep the
// operator's ext-cloud interface and must not activate public anchors.
// BGP reserves the VIP but does not derive L2 host state.
//
//   - L2: EXT_NET_PUBLIC_ANCHOR_VLAN ← EXT_PUBLIC_VLAN_ID
//   - L2: METALLB_INTERFACE          ← EXT_NET_PUBLIC_ANCHOR_INTERFACE
//     (replacing the add-cluster.sh CHANGEME placeholder; the L2
//     advertisement must ride the tagged anchor port, not the bare
//     bridge — the bridge's own port carries the cloud VLAN tag)
//   - L2: EXT_NET_PUBLIC_ANCHOR_IPS  ← one address per KUBE_OVN_GW_NODES
//     entry, allocated VIP+1, VIP+2, … (skipping the gateway),
//     formatted node=ip/prefix with the public CIDR's prefix
//   - EXT_PUBLIC_EXCLUDE_IPS_1    ← L2: "<VIP>..<last-anchor>";
//     BGP: "<VIP>". This reserves every host-owned address in IPAM.
//   - EXT_PUBLIC_EXCLUDE_IPS_2    ← "<gateway>" (kube-ovn auto-excludes
//     the subnet gateway; this keeps the key non-empty for Flux's
//     strict envsubst and survives a later gateway change)
func publicVIPNetwork(env map[string]string) (net.IP, *net.IPNet, bool) {
	_, cidr, err := net.ParseCIDR(strings.TrimSpace(env["EXT_PUBLIC_CIDR"]))
	if err != nil || cidr.IP.To4() == nil {
		return nil, nil, false
	}
	vip := net.ParseIP(strings.TrimSpace(env["METALLB_FLOATING_IP"]))
	if vip = vip.To4(); vip == nil || !cidr.Contains(vip) {
		return vip, cidr, false
	}
	return vip, cidr, true
}

func publicL2VIPUsesPublicSubnet(env map[string]string) bool {
	mode := strings.TrimSpace(env["METALLB_MODE"])
	if mode == "bgp" {
		return false
	}
	_, _, ok := publicVIPNetwork(env)
	return ok && strings.TrimSpace(env["EXT_PUBLIC_VLAN_ID"]) != ""
}

func derivePublicAnchorEnv(env map[string]string) {
	// Cloud-side twin first (same DaemonSet consumes it): the bridge's own
	// access tag follows the cloud VLAN. "0" means untagged, so the
	// tagger stays idle.
	if strings.TrimSpace(env["EXT_NET_ANCHOR_VLAN"]) == "" {
		if v := strings.TrimSpace(env["EXT_NET_VLAN_ID"]); v != "" && v != "0" {
			env["EXT_NET_ANCHOR_VLAN"] = v
		}
	}

	if strings.TrimSpace(env["EXT_PUBLIC_VLAN_ID"]) == "" {
		return
	}
	vip, cidr, vipInPublic := publicVIPNetwork(env)
	if !vipInPublic {
		// A cloud+public-vlan cluster may intentionally announce its platform
		// VIP on ext-cloud. The mere existence of ext-public must not move the
		// L2 interface or require public anchors.
		return
	}
	gw := net.ParseIP(strings.TrimSpace(env["EXT_PUBLIC_GATEWAY"]))
	gw = gw.To4()

	mode := strings.TrimSpace(env["METALLB_MODE"])
	if mode == "" {
		mode = "l2"
	}
	var lastAnchor net.IP
	if mode == "l2" {
		iface := strings.TrimSpace(env["EXT_NET_PUBLIC_ANCHOR_INTERFACE"])
		if strings.TrimSpace(env["EXT_NET_PUBLIC_ANCHOR_VLAN"]) == "" {
			env["EXT_NET_PUBLIC_ANCHOR_VLAN"] = strings.TrimSpace(env["EXT_PUBLIC_VLAN_ID"])
		}
		if v := strings.TrimSpace(env["METALLB_INTERFACE"]); (v == "" || v == "CHANGEME") && iface != "" {
			env["METALLB_INTERFACE"] = iface
		}

		if strings.TrimSpace(env["EXT_NET_PUBLIC_ANCHOR_IPS"]) == "" {
			nodes := splitNonEmpty(env["KUBE_OVN_GW_NODES"])
			if len(nodes) > 0 {
				ones, _ := cidr.Mask.Size()
				var pairs []string
				next := ipAdd(vip, 1)
				for _, node := range nodes {
					for gw != nil && next.Equal(gw) {
						next = ipAdd(next, 1)
					}
					if !isUsableHostIP(next, cidr) {
						pairs = nil // half a map is worse than no map
						break
					}
					pairs = append(pairs, fmt.Sprintf("%s=%s/%d", node, next, ones))
					lastAnchor = next
					next = ipAdd(next, 1)
				}
				if len(pairs) > 0 {
					env["EXT_NET_PUBLIC_ANCHOR_IPS"] = strings.Join(pairs, ",")
				} else {
					lastAnchor = nil
				}
			}
		}
	}

	// The VIP is never available to tenant IPAM. L2 additionally reserves
	// the automatically-derived anchor run; BGP needs no host anchors.
	if strings.TrimSpace(env["EXT_PUBLIC_EXCLUDE_IPS_1"]) == "" {
		if lastAnchor != nil {
			env["EXT_PUBLIC_EXCLUDE_IPS_1"] = fmt.Sprintf("%s..%s", vip, lastAnchor)
		} else {
			env["EXT_PUBLIC_EXCLUDE_IPS_1"] = vip.String()
		}
	}
	if strings.TrimSpace(env["EXT_PUBLIC_EXCLUDE_IPS_2"]) == "" && gw != nil {
		env["EXT_PUBLIC_EXCLUDE_IPS_2"] = gw.String()
	}
}

// validatePublicAnchor enforces the public-anchor schema. It mirrors
// validateAnchorIPs (the ext-cloud twin), requires complete coverage of
// kube-ovn's external-gateway node set for public L2, and verifies that
// gateway, VIP and every anchor fall inside EXT_PUBLIC_EXCLUDE_IPS_*.
// Otherwise tenant IPAM can create a duplicate address on the segment.
func validatePublicAnchor(envMap map[string]string, errs *[]string) {
	anchorInterface := strings.TrimSpace(envMap["EXT_NET_PUBLIC_ANCHOR_INTERFACE"])
	if anchorInterface != "" {
		if msg := validateNICName(anchorInterface); msg != "" {
			*errs = append(*errs, "EXT_NET_PUBLIC_ANCHOR_INTERFACE: "+msg)
		}
	}
	anchorVLAN := strings.TrimSpace(envMap["EXT_NET_PUBLIC_ANCHOR_VLAN"])
	if anchorVLAN != "" {
		if msg := validateVLANID(anchorVLAN); msg != "" {
			*errs = append(*errs, "EXT_NET_PUBLIC_ANCHOR_VLAN: "+msg)
		}
	}

	raw := strings.TrimSpace(envMap["EXT_NET_PUBLIC_ANCHOR_IPS"])
	vip, publicCIDR, vipInPublic := publicVIPNetwork(envMap)
	publicL2 := publicL2VIPUsesPublicSubnet(envMap)
	parentBridge := strings.TrimSpace(envMap["EXT_NET_ANCHOR_INTERFACE"])
	gw := net.ParseIP(strings.TrimSpace(envMap["EXT_PUBLIC_GATEWAY"]))
	gw = gw.To4()

	// Public host anchors are exclusively an L2-VIP mechanism. Leaving them
	// populated after an L2 -> BGP migration (or after moving the VIP back to
	// ext-cloud) keeps addresses and policy-routing state alive on the hosts
	// even though MetalLB no longer needs them. Refuse that stale mixed mode;
	// the fleet worker also retires the state defensively when METALLB_MODE is
	// not l2, covering hand-edited Git that bypasses this validator.
	if !publicL2 && (raw != "" || anchorVLAN != "") {
		*errs = append(*errs,
			"EXT_NET_PUBLIC_ANCHOR_IPS/EXT_NET_PUBLIC_ANCHOR_VLAN: public host anchors are valid only when an L2 METALLB_FLOATING_IP is inside EXT_PUBLIC_CIDR — clear both keys in the same change that switches to BGP or moves the VIP to ext-cloud")
	}

	if vipInPublic && !isUsableHostIP(vip, publicCIDR) {
		*errs = append(*errs, fmt.Sprintf(
			"METALLB_FLOATING_IP: %s is the network/broadcast address of EXT_PUBLIC_CIDR %s, not a usable host address", vip, publicCIDR))
	}
	if gw != nil && publicCIDR != nil && publicCIDR.Contains(gw) && !isUsableHostIP(gw, publicCIDR) {
		*errs = append(*errs, fmt.Sprintf(
			"EXT_PUBLIC_GATEWAY: %s is the network/broadcast address of EXT_PUBLIC_CIDR %s, not a usable host address", gw, publicCIDR))
	}
	if vipInPublic && gw != nil && vip.Equal(gw) {
		*errs = append(*errs, "METALLB_FLOATING_IP: must differ from EXT_PUBLIC_GATEWAY")
	}

	if publicL2 {
		if parentBridge == "" {
			*errs = append(*errs, "EXT_NET_ANCHOR_INTERFACE: required as the parent OVS bridge for EXT_NET_PUBLIC_ANCHOR_INTERFACE in public L2 mode")
		}
		if raw == "" {
			*errs = append(*errs,
				"EXT_NET_PUBLIC_ANCHOR_IPS: empty, but an L2 MetalLB VIP inside EXT_PUBLIC_CIDR needs one per-node anchor on every announcing gateway node (otherwise the VIP accepts connections and silently drops replies) — set KUBE_OVN_GW_NODES so init derives the map, or pass EXT_NET_PUBLIC_ANCHOR_IPS explicitly")
		}
		if anchorInterface == "" {
			*errs = append(*errs, "EXT_NET_PUBLIC_ANCHOR_INTERFACE: required for an L2 VIP inside EXT_PUBLIC_CIDR")
		} else if got := strings.TrimSpace(envMap["METALLB_INTERFACE"]); got != anchorInterface {
			*errs = append(*errs, fmt.Sprintf(
				"METALLB_INTERFACE: %q must equal EXT_NET_PUBLIC_ANCHOR_INTERFACE %q when the L2 VIP is inside EXT_PUBLIC_CIDR", got, anchorInterface))
		}
		publicVLAN := strings.TrimSpace(envMap["EXT_PUBLIC_VLAN_ID"])
		if anchorVLAN == "" {
			*errs = append(*errs, "EXT_NET_PUBLIC_ANCHOR_VLAN: required for an L2 VIP inside EXT_PUBLIC_CIDR")
		} else if anchorVLAN != publicVLAN {
			*errs = append(*errs, fmt.Sprintf(
				"EXT_NET_PUBLIC_ANCHOR_VLAN: %q must equal EXT_PUBLIC_VLAN_ID %q", anchorVLAN, publicVLAN))
		}
	}

	gwNodes := splitNonEmpty(envMap["KUBE_OVN_GW_NODES"])
	gwSet := make(map[string]struct{}, len(gwNodes))
	for _, n := range gwNodes {
		gwSet[n] = struct{}{}
	}
	if raw != "" && len(gwSet) == 0 {
		*errs = append(*errs,
			"EXT_NET_PUBLIC_ANCHOR_IPS set but KUBE_OVN_GW_NODES is empty — the fleet worker uses the map only on declared gateway/announcer nodes")
	}

	excludeRanges, excludeOK := parsePublicExcludeRanges(envMap, errs)
	hostSeen := make(map[string]struct{})
	ipSeen := make(map[string]string)
	var anchorIPs []net.IP

	for _, pair := range strings.Split(raw, ",") {
		pair = strings.TrimSpace(pair)
		if pair == "" {
			continue
		}
		host, cidrStr, ok := strings.Cut(pair, "=")
		if !ok {
			*errs = append(*errs, fmt.Sprintf(
				"EXT_NET_PUBLIC_ANCHOR_IPS: %q missing equals sign (expected node=CIDR, e.g. node1=192.0.2.3/28)", pair))
			continue
		}
		host = strings.TrimSpace(host)
		cidrStr = strings.TrimSpace(cidrStr)
		if host == "" {
			*errs = append(*errs, fmt.Sprintf("EXT_NET_PUBLIC_ANCHOR_IPS: %q has empty node name", pair))
			continue
		}
		if _, dup := hostSeen[host]; dup {
			*errs = append(*errs, fmt.Sprintf(
				"EXT_NET_PUBLIC_ANCHOR_IPS: node %q listed more than once (one anchor per node)", host))
			continue
		}
		hostSeen[host] = struct{}{}
		if len(gwSet) > 0 {
			if _, ok := gwSet[host]; !ok {
				*errs = append(*errs, fmt.Sprintf(
					"EXT_NET_PUBLIC_ANCHOR_IPS: node %q not in KUBE_OVN_GW_NODES (anchors only bind on gateway/announcer nodes)", host))
			}
		}
		ip, anchorNet, err := net.ParseCIDR(cidrStr)
		if err != nil {
			*errs = append(*errs, fmt.Sprintf(
				"EXT_NET_PUBLIC_ANCHOR_IPS: %q invalid CIDR (e.g. 192.0.2.3/28): %v", cidrStr, err))
			continue
		}
		ipKey := ip.String()
		if firstHost, dup := ipSeen[ipKey]; dup {
			*errs = append(*errs, fmt.Sprintf(
				"EXT_NET_PUBLIC_ANCHOR_IPS: IP %s claimed by both %q and %q (duplicate address on one L2 segment)",
				ipKey, firstHost, host))
		} else {
			ipSeen[ipKey] = host
		}
		if gw != nil && ip.Equal(gw) {
			*errs = append(*errs, fmt.Sprintf(
				"EXT_NET_PUBLIC_ANCHOR_IPS: anchor %s for node %q is the public gateway address", ipKey, host))
		}
		if vip != nil && ip.Equal(vip) {
			*errs = append(*errs, fmt.Sprintf(
				"EXT_NET_PUBLIC_ANCHOR_IPS: anchor %s for node %q is the MetalLB floating IP itself", ipKey, host))
		}
		if publicCIDR != nil {
			if !publicCIDR.Contains(ip) {
				*errs = append(*errs, fmt.Sprintf(
					"EXT_NET_PUBLIC_ANCHOR_IPS: anchor %s for node %q is outside EXT_PUBLIC_CIDR %s",
					ipKey, host, publicCIDR.String()))
			} else if !isUsableHostIP(ip, publicCIDR) {
				*errs = append(*errs, fmt.Sprintf(
					"EXT_NET_PUBLIC_ANCHOR_IPS: anchor %s for node %q is the network/broadcast address, not a usable host address", ipKey, host))
			}
			anchorOnes, _ := anchorNet.Mask.Size()
			parentOnes, _ := publicCIDR.Mask.Size()
			if anchorOnes != parentOnes {
				*errs = append(*errs, fmt.Sprintf(
					"EXT_NET_PUBLIC_ANCHOR_IPS: anchor %s for node %q has prefix /%d but EXT_PUBLIC_CIDR is /%d — the anchor mask must match the public network",
					ipKey, host, anchorOnes, parentOnes))
			}
		}
		anchorIPs = append(anchorIPs, ip)
	}

	if publicL2 && raw != "" {
		var missing []string
		for _, node := range gwNodes {
			if _, ok := hostSeen[node]; !ok {
				missing = append(missing, node)
			}
		}
		if len(gwNodes) == 0 {
			*errs = append(*errs, "KUBE_OVN_GW_NODES: required to derive and validate public-L2 announcer anchors")
		} else if len(missing) > 0 {
			sort.Strings(missing)
			*errs = append(*errs, fmt.Sprintf(
				"EXT_NET_PUBLIC_ANCHOR_IPS: gateway/announcer node(s) %s have no public anchor — every KUBE_OVN_GW_NODES entry must be covered", strings.Join(missing, ", ")))
		}
	}

	// Reservation coverage applies in both modes: BGP needs the VIP excluded;
	// L2 additionally needs every host-bound anchor excluded. The gateway is
	// included to keep the generated contract explicit even though kube-ovn
	// normally excludes spec.gateway itself.
	if vipInPublic && excludeOK {
		var uncovered []string
		for _, ip := range anchorIPs {
			if !ipInRanges(ip, excludeRanges) {
				uncovered = append(uncovered, ip.String()+" (anchor)")
			}
		}
		if !ipInRanges(vip, excludeRanges) {
			uncovered = append(uncovered, vip.String()+" (VIP)")
		}
		if gw != nil && publicCIDR.Contains(gw) && !ipInRanges(gw, excludeRanges) {
			uncovered = append(uncovered, gw.String()+" (gateway)")
		}
		if len(uncovered) > 0 {
			sort.Strings(uncovered)
			*errs = append(*errs, fmt.Sprintf(
				"EXT_PUBLIC_EXCLUDE_IPS_*: %s not covered by any exclude range — reserve the gateway, VIP and every anchor before tenant IPAM can allocate them",
				strings.Join(uncovered, ", ")))
		}
	}
}

// parsePublicExcludeRanges parses EXT_PUBLIC_EXCLUDE_IPS_1/_2 in
// kube-ovn excludeIps syntax: "<ip>" or "<start>..<end>". Returns
// ok=false when neither key holds a usable value (coverage check is
// then skipped — the missing-key failure surfaces in Flux instead).
func parsePublicExcludeRanges(envMap map[string]string, errs *[]string) ([][2]net.IP, bool) {
	var ranges [][2]net.IP
	for _, key := range []string{"EXT_PUBLIC_EXCLUDE_IPS_1", "EXT_PUBLIC_EXCLUDE_IPS_2"} {
		v := strings.TrimSpace(envMap[key])
		if v == "" {
			continue
		}
		start, end, found := strings.Cut(v, "..")
		if !found {
			end = start
		}
		s := net.ParseIP(strings.TrimSpace(start))
		e := net.ParseIP(strings.TrimSpace(end))
		if s = s.To4(); s == nil {
			*errs = append(*errs, fmt.Sprintf("%s: %q is not a valid IPv4 address or a..b range", key, v))
			continue
		}
		if e = e.To4(); e == nil {
			*errs = append(*errs, fmt.Sprintf("%s: %q has an invalid range end", key, v))
			continue
		}
		if ipToU32(s) > ipToU32(e) {
			*errs = append(*errs, fmt.Sprintf("%s: range %q is inverted (start after end)", key, v))
			continue
		}
		ranges = append(ranges, [2]net.IP{s, e})
	}
	return ranges, len(ranges) > 0
}

func ipInRanges(ip net.IP, ranges [][2]net.IP) bool {
	v4 := ip.To4()
	if v4 == nil {
		return false
	}
	n := ipToU32(v4)
	for _, r := range ranges {
		if n >= ipToU32(r[0]) && n <= ipToU32(r[1]) {
			return true
		}
	}
	return false
}

// isUsableHostIP: inside the CIDR and neither the network nor the
// broadcast address (for prefixes that have them).
func isUsableHostIP(ip net.IP, cidr *net.IPNet) bool {
	if ip == nil || !cidr.Contains(ip) {
		return false
	}
	ones, bits := cidr.Mask.Size()
	if bits != 32 || ones >= 31 {
		return cidr.Contains(ip)
	}
	network := ipToU32(cidr.IP.To4())
	broadcast := network | (1<<uint(32-ones) - 1)
	n := ipToU32(ip)
	return n != network && n != broadcast
}

func ipAdd(ip net.IP, n uint32) net.IP {
	v4 := ip.To4()
	if v4 == nil {
		return nil
	}
	return u32ToIP(ipToU32(v4) + n)
}

func ipToU32(ip net.IP) uint32 {
	return binary.BigEndian.Uint32(ip.To4())
}

func u32ToIP(n uint32) net.IP {
	ip := make(net.IP, 4)
	binary.BigEndian.PutUint32(ip, n)
	return ip
}

func splitNonEmpty(csv string) []string {
	var out []string
	for _, s := range strings.Split(csv, ",") {
		if s = strings.TrimSpace(s); s != "" {
			out = append(out, s)
		}
	}
	return out
}
