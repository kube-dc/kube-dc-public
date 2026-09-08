package clusterinit

import (
	"encoding/binary"
	"fmt"
	"io"
	"net"
	"os"
	"path/filepath"
	"strings"
)

// Management-VPC SNAT address (EXT_NET_MGMT_SNAT_IP).
//
// Every system→tenant connection (cert-manager HTTP-01 to a tenant route, CAPI
// bootstrap, the DB operators, kube-dc-manager) leaves the management VPC
// through the ovn-cluster-<ext> router port and is source-NATed to that port's
// address. kube-dc-manager discovers the address from kube-ovn's OvnSnatRule
// and exempts it in every tenant firewall; without it every Project stays
// NotReady.
//
// kube-ovn's legacy external-gateway handler picks that address itself when
// it first creates the port — and on a greenfield cluster the pick was never
// recorded as an OvnEip / in IPAM, so the OvnSnatRule never published it and
// the first tenant EIP was allocated the SAME address (webdock 2026-08-31;
// kube-dc docs/internal/issues/ovn-cluster-ext-cloud-lrp-missing.md). The fleet now
// DECLARES the OvnEip (infra-mgmt-snat, applied before the ConfigMap that
// wakes the handler, so the handler adopts it) and this file gives the
// address a deterministic, reserved value.
//
// Derivation, when the scaffold left EXT_NET_MGMT_SNAT_IP=CHANGEME: the
// address right AFTER the reserved block in EXT_NET_EXCLUDE_IPS (a single
// address or one `a..b` range — the Subnet template renders exactly one
// excludeIps item), and the block is widened to cover it. On the fleet's
// existing conventions that yields <gw>+1 for a bare-gateway exclusion and
// .101 for the `.1...100` block cloud/stage use — which is the address those
// clusters' hand-created OvnEips already carry.

const (
	keyMgmtSnatIP  = "EXT_NET_MGMT_SNAT_IP"
	keyExtExclude  = "EXT_NET_EXCLUDE_IPS"
	keyExtCIDR     = "EXT_NET_CIDR"
	keyExtGateway  = "EXT_NET_GATEWAY"
	mgmtSnatUnsetV = "CHANGEME"
)

// ResolveMgmtSnatIP fills EXT_NET_MGMT_SNAT_IP in cluster-config.env when the
// starter left the CHANGEME placeholder, widening EXT_NET_EXCLUDE_IPS to
// reserve it. An explicit value is validated (ValidateMgmtSnatIP) and left
// alone. Missing key = older starter without infra-mgmt-snat: nothing to do.
func ResolveMgmtSnatIP(fleetRepo, clusterName string, out io.Writer) error {
	if out == nil {
		out = io.Discard
	}
	path := filepath.Join(fleetRepo, "clusters", clusterName, "cluster-config.env")
	body, err := os.ReadFile(path)
	if err != nil {
		return nil
	}
	env := string(body)
	cur := strings.TrimSpace(envValue(env, keyMgmtSnatIP))
	if cur == "" {
		return nil // starter predates the key
	}
	envMap := map[string]string{
		keyExtCIDR:    strings.TrimSpace(envValue(env, keyExtCIDR)),
		keyExtGateway: strings.TrimSpace(envValue(env, keyExtGateway)),
		keyExtExclude: strings.TrimSpace(envValue(env, keyExtExclude)),
		keyMgmtSnatIP: cur,
	}
	if cur != mgmtSnatUnsetV {
		if msg := ValidateMgmtSnatIP(envMap); msg != "" {
			return fmt.Errorf("%s: %s", keyMgmtSnatIP, msg)
		}
		return nil
	}
	addr, exclude, err := DeriveMgmtSnatIP(envMap[keyExtCIDR], envMap[keyExtGateway], envMap[keyExtExclude])
	if err != nil {
		return fmt.Errorf("%s: %w — set it explicitly in clusters/%s/cluster-config.env (a free host address in %s, also listed in %s)",
			keyMgmtSnatIP, err, clusterName, keyExtCIDR, keyExtExclude)
	}
	lines := strings.Split(env, "\n")
	lines, _, _ = setEnvValueLine(keyMgmtSnatIP, addr)(lines)
	lines, _, _ = setEnvValueLine(keyExtExclude, exclude)(lines)
	if err := os.WriteFile(path, []byte(strings.Join(lines, "\n")), 0o644); err != nil {
		return fmt.Errorf("write %s: %w", path, err)
	}
	fmt.Fprintf(out, "[scaffold] management SNAT address %s=%s (reserved: %s=%s) — kube-ovn will adopt it for the ovn-cluster external router port\n",
		keyMgmtSnatIP, addr, keyExtExclude, exclude)
	return nil
}

// DeriveMgmtSnatIP returns the first address after the reserved exclusion
// block and the widened block that covers it. exclude is EXT_NET_EXCLUDE_IPS:
// one address or one inclusive `a..b` range (the fleet renders exactly one
// excludeIps item); empty means "just the gateway".
func DeriveMgmtSnatIP(cidr, gateway, exclude string) (addr, newExclude string, err error) {
	_, ipnet, perr := net.ParseCIDR(strings.TrimSpace(cidr))
	if perr != nil {
		return "", "", fmt.Errorf("%s %q is not a CIDR", keyExtCIDR, cidr)
	}
	gw := net.ParseIP(strings.TrimSpace(gateway)).To4()
	if gw == nil || !ipnet.Contains(gw) {
		return "", "", fmt.Errorf("%s %q is not an IPv4 address inside %s", keyExtGateway, gateway, cidr)
	}
	start, end := gw, gw
	if ex := strings.TrimSpace(exclude); ex != "" {
		if strings.Contains(ex, ",") {
			return "", "", fmt.Errorf("%s %q has several entries; the Subnet template renders ONE — use a single address or one a..b range", keyExtExclude, ex)
		}
		a, b, ok := parseExcludeEntry(ex)
		if !ok {
			return "", "", fmt.Errorf("%s %q is not an address or an a..b range", keyExtExclude, ex)
		}
		if !ipnet.Contains(a) || !ipnet.Contains(b) {
			return "", "", fmt.Errorf("%s %q is not inside %s", keyExtExclude, ex, cidr)
		}
		start, end = a, b
		if ip4ToU32(gw) < ip4ToU32(start) || ip4ToU32(gw) > ip4ToU32(end) {
			// The reserved block does not even cover the gateway; keep the
			// block as the operator wrote it and reserve the gateway's
			// neighbour instead of guessing at their layout.
			start, end = gw, gw
		}
	}
	cand := u32ToIP4(ip4ToU32(end) + 1)
	if !ipnet.Contains(cand) || isBroadcast(cand, ipnet) {
		return "", "", fmt.Errorf("no free host address after the reserved block %s..%s in %s", start, end, cidr)
	}
	return cand.String(), start.String() + ".." + cand.String(), nil
}

// ValidateMgmtSnatIP checks an explicit EXT_NET_MGMT_SNAT_IP against the
// external network keys. "" = valid (or not applicable).
func ValidateMgmtSnatIP(envMap map[string]string) string {
	v := strings.TrimSpace(envMap[keyMgmtSnatIP])
	if v == "" || v == mgmtSnatUnsetV {
		return ""
	}
	ip := net.ParseIP(v).To4()
	if ip == nil {
		return fmt.Sprintf("%q is not an IPv4 address", v)
	}
	_, ipnet, err := net.ParseCIDR(strings.TrimSpace(envMap[keyExtCIDR]))
	if err != nil {
		return "" // the CIDR check reports its own error
	}
	if !ipnet.Contains(ip) {
		return fmt.Sprintf("%s is outside %s %s", v, keyExtCIDR, envMap[keyExtCIDR])
	}
	if ip.Equal(ipnet.IP.To4()) || isBroadcast(ip, ipnet) {
		return fmt.Sprintf("%s is the network or broadcast address of %s", v, envMap[keyExtCIDR])
	}
	if gw := net.ParseIP(strings.TrimSpace(envMap[keyExtGateway])); gw != nil && gw.Equal(ip) {
		return fmt.Sprintf("%s is the gateway (%s) — the SNAT address must be a free host", v, keyExtGateway)
	}
	if ex := strings.TrimSpace(envMap[keyExtExclude]); ex != "" && !strings.Contains(ex, ",") {
		if a, b, ok := parseExcludeEntry(ex); ok {
			if ip4ToU32(ip) < ip4ToU32(a) || ip4ToU32(ip) > ip4ToU32(b) {
				return fmt.Sprintf("%s is not covered by %s=%s — widen the reserved block to include it, or kube-ovn may hand the same address to a tenant", v, keyExtExclude, ex)
			}
		}
	}
	return ""
}

func parseExcludeEntry(s string) (a, b net.IP, ok bool) {
	s = strings.TrimSpace(s)
	if i := strings.Index(s, ".."); i >= 0 {
		a = net.ParseIP(strings.TrimSpace(s[:i])).To4()
		b = net.ParseIP(strings.TrimSpace(s[i+2:])).To4()
		if a == nil || b == nil || ip4ToU32(a) > ip4ToU32(b) {
			return nil, nil, false
		}
		return a, b, true
	}
	a = net.ParseIP(s).To4()
	if a == nil {
		return nil, nil, false
	}
	return a, a, true
}

func ip4ToU32(ip net.IP) uint32 { return binary.BigEndian.Uint32(ip.To4()) }

func u32ToIP4(v uint32) net.IP {
	b := make(net.IP, 4)
	binary.BigEndian.PutUint32(b, v)
	return b
}

func isBroadcast(ip net.IP, ipnet *net.IPNet) bool {
	ones, bits := ipnet.Mask.Size()
	if bits != 32 || ones >= 31 {
		return false
	}
	return ip4ToU32(ip) == ip4ToU32(ipnet.IP.To4())|(^uint32(0)>>uint(ones))
}
