package setup

import (
	"fmt"
	"net/netip"
	"sort"
	"strings"
)

type AddressFinding struct{ Key, Detail string }

// ValidateAddressPlan validates allocation roles, not routes. A route into a
// host LAN is intentional and is never treated as a second address pool.
func ValidateAddressPlan(env map[string]string, hosts []HostObservation) []AddressFinding {
	var findings []AddressFinding
	add := func(key, detail string) { findings = append(findings, AddressFinding{key, detail}) }
	pools := map[string]netip.Prefix{}
	for _, key := range []string{"POD_CIDR", "SVC_CIDR", "JOIN_CIDR", "INFRA_ATTACHMENT_CIDR", "EXT_NET_CIDR", "EXT_PUBLIC_CIDR", "NODE_CIDR"} {
		value := strings.TrimSpace(env[key])
		if value == "" {
			continue
		}
		p, err := netip.ParsePrefix(value)
		if err != nil || !p.Addr().Is4() || p != p.Masked() {
			add(key, "expected a canonical IPv4 network")
			continue
		}
		pools[key] = p
	}
	keys := make([]string, 0, len(pools))
	for key := range pools {
		keys = append(keys, key)
	}
	sort.Strings(keys)
	for i, a := range keys {
		for _, b := range keys[i+1:] {
			if (a == "NODE_CIDR" && (b == "EXT_NET_CIDR" || b == "EXT_PUBLIC_CIDR")) || (b == "NODE_CIDR" && (a == "EXT_NET_CIDR" || a == "EXT_PUBLIC_CIDR")) {
				continue
			}
			if pools[a].Overlaps(pools[b]) {
				add(a+"/"+b, "address allocations overlap")
			}
		}
	}
	virtual := []string{"POD_CIDR", "SVC_CIDR", "JOIN_CIDR", "INFRA_ATTACHMENT_CIDR"}
	for _, h := range hosts {
		for _, nic := range h.Facts.NICs {
			for _, value := range nic.Prefixes {
				lan, err := netip.ParsePrefix(value)
				if err != nil {
					add("hosts."+h.ID, "invalid observed host prefix")
					continue
				}
				if lan.Addr().Is6() {
					if !lan.Addr().IsLinkLocalUnicast() && !lan.Addr().IsLoopback() {
						add("hosts."+h.ID, "global IPv6 host routing needs a qualified address plan")
					}
					continue
				}
				for _, key := range virtual {
					if p, ok := pools[key]; ok && p.Overlaps(lan.Masked()) {
						add(key, "overlaps host "+h.ID+" interface "+nic.Name)
					}
				}
			}
		}
	}
	for _, pair := range [][2]string{{"POD_GATEWAY", "POD_CIDR"}, {"CLUSTER_DNS", "SVC_CIDR"}, {"K8S_SERVICE_IP", "SVC_CIDR"}, {"INFRA_ATTACHMENT_GATEWAY", "INFRA_ATTACHMENT_CIDR"}, {"EXT_NET_GATEWAY", "EXT_NET_CIDR"}, {"EXT_PUBLIC_GATEWAY", "EXT_PUBLIC_CIDR"}, {"KUBE_API_INTERNAL_VIP", "EXT_NET_CIDR"}, {"ENVOY_GATEWAY_INTERNAL_VIP", "EXT_NET_CIDR"}, {"EXT_NET_MGMT_SNAT_IP", "EXT_NET_CIDR"}} {
		value := strings.TrimSpace(env[pair[0]])
		if value == "" {
			continue
		}
		ip, err := netip.ParseAddr(value)
		p, ok := pools[pair[1]]
		if err != nil || !ip.Is4() || !ok || !usableInPrefix(p, ip) {
			add(pair[0], "address must be usable in "+pair[1])
		}
	}
	if dns, service := strings.TrimSpace(env["CLUSTER_DNS"]), strings.TrimSpace(env["K8S_SERVICE_IP"]); dns != "" && dns == service {
		add("CLUSTER_DNS/K8S_SERVICE_IP", "DNS and Kubernetes API require distinct service addresses")
	}
	reserved := map[string][]ipInterval{}
	for _, pair := range [][2]string{{"EXT_NET_EXCLUDE_IPS", "EXT_NET_CIDR"}, {"EXT_PUBLIC_EXCLUDE_IPS", "EXT_PUBLIC_CIDR"}} {
		if env[pair[0]] == "" {
			continue
		}
		p, ok := pools[pair[1]]
		if !ok {
			add(pair[0], "reservation has no address pool")
			continue
		}
		for _, value := range splitAddressValues(env[pair[0]]) {
			r, err := parseInterval(value)
			if err != nil || !p.Contains(r.First) || !p.Contains(r.Last) {
				add(pair[0], "reservation is invalid or outside its pool")
				continue
			}
			reserved[pair[1]] = append(reserved[pair[1]], r)
		}
	}
	owned := map[netip.Addr]string{}
	claim := func(key, value, pool string, requireReserved bool) {
		ip, err := netip.ParseAddr(value)
		if err != nil || !ip.Is4() {
			add(key, "invalid or unsupported allocated address")
			return
		}
		if p, ok := pools[pool]; pool != "" && (!ok || !usableInPrefix(p, ip)) {
			add(key, "allocated address is outside "+pool)
			return
		}
		if old, ok := owned[ip]; ok {
			add(key, "address is also allocated by "+old)
		} else {
			owned[ip] = key
		}
		if requireReserved {
			found := false
			for _, r := range reserved[pool] {
				found = found || r.Contains(ip)
			}
			if !found {
				add(key, "allocated address must be excluded from tenant allocation")
			}
		}
		for _, key2 := range virtual {
			if p, ok := pools[key2]; ok && p.Contains(ip) {
				add(key, "platform or host address overlaps "+key2)
			}
		}
	}
	for _, pair := range [][2]string{{"KUBE_API_INTERNAL_VIP", "EXT_NET_CIDR"}, {"ENVOY_GATEWAY_INTERNAL_VIP", "EXT_NET_CIDR"}, {"EXT_NET_MGMT_SNAT_IP", "EXT_NET_CIDR"}} {
		if env[pair[0]] != "" {
			claim(pair[0], env[pair[0]], pair[1], true)
		}
	}
	// Platform access VIPs can use an independent upstream segment. Check
	// collisions, but do not force them into a tenant pool.
	if value := env["METALLB_FLOATING_IP"]; value != "" {
		claim("METALLB_FLOATING_IP", value, "", false)
	}
	for _, pair := range [][2]string{{"EXT_NET_ANCHOR_IPS", "EXT_NET_CIDR"}, {"EXT_NET_PUBLIC_ANCHOR_IPS", "EXT_PUBLIC_CIDR"}} {
		for _, entry := range strings.Split(env[pair[0]], ",") {
			if strings.TrimSpace(entry) == "" {
				continue
			}
			name, value, ok := strings.Cut(strings.TrimSpace(entry), "=")
			p, err := netip.ParsePrefix(value)
			pool, poolOK := pools[pair[1]]
			if !ok || name == "" || err != nil || !p.Addr().Is4() || !poolOK || p.Bits() < pool.Bits() || !pool.Contains(p.Addr()) {
				add(pair[0], "invalid host anchor mapping")
				continue
			}
			claim(pair[0]+"."+name, p.Addr().String(), pair[1], true)
		}
	}
	// Gateways are shared by design; an anchor cannot own the router address.
	for _, key := range []string{"EXT_NET_GATEWAY", "EXT_PUBLIC_GATEWAY"} {
		if ip, err := netip.ParseAddr(env[key]); err == nil {
			if old, ok := owned[ip]; ok {
				add(key, "gateway is also allocated by "+old)
			}
		}
	}
	for _, h := range hosts {
		for _, nic := range h.Facts.NICs {
			for _, value := range nic.Addresses {
				ip, err := netip.ParseAddr(value)
				if err != nil || !ip.Is4() {
					continue
				}
				for _, key := range virtual {
					if p, ok := pools[key]; ok && p.Contains(ip) {
						add(key, "contains an observed host address")
					}
				}
				if old, ok := owned[ip]; ok {
					add(old, "address is already present on host "+h.ID)
				}
				for _, key := range []string{"EXT_NET_CIDR", "EXT_PUBLIC_CIDR"} {
					if p, ok := pools[key]; ok && p.Contains(ip) {
						found := false
						for _, r := range reserved[key] {
							found = found || r.Contains(ip)
						}
						if !found {
							add(key, "host "+h.ID+" address must be excluded from tenant allocation")
						}
					}
				}
			}
		}
	}
	sort.Slice(findings, func(i, j int) bool {
		if findings[i].Key != findings[j].Key {
			return findings[i].Key < findings[j].Key
		}
		return findings[i].Detail < findings[j].Detail
	})
	return findings
}

type ipInterval struct{ First, Last netip.Addr }

func (r ipInterval) Contains(ip netip.Addr) bool {
	return ip.Is4() && r.First.Compare(ip) <= 0 && r.Last.Compare(ip) >= 0
}
func parseInterval(v string) (ipInterval, error) {
	a, b, ok := strings.Cut(v, "..")
	if !ok {
		b = a
	}
	first, e1 := netip.ParseAddr(strings.TrimSpace(a))
	last, e2 := netip.ParseAddr(strings.TrimSpace(b))
	if e1 != nil || e2 != nil || !first.Is4() || !last.Is4() || first.Compare(last) > 0 {
		return ipInterval{}, fmt.Errorf("invalid IPv4 reservation")
	}
	return ipInterval{first, last}, nil
}
func splitAddressValues(v string) []string {
	return strings.FieldsFunc(v, func(r rune) bool { return r == ',' || r == ' ' })
}
func usableInPrefix(p netip.Prefix, ip netip.Addr) bool {
	if !p.Contains(ip) {
		return false
	}
	if p.Bits() >= 31 {
		return true
	}
	b := p.Masked().Addr().As4()
	v := uint32(b[0])<<24 | uint32(b[1])<<16 | uint32(b[2])<<8 | uint32(b[3])
	end := v | uint32((uint64(1)<<uint(32-p.Bits()))-1)
	x := ip.As4()
	n := uint32(x[0])<<24 | uint32(x[1])<<16 | uint32(x[2])<<8 | uint32(x[3])
	return n != v && n != end
}
