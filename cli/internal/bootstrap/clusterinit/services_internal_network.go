package clusterinit

import (
	"encoding/json"
	"fmt"
	"io"
	"net"
	"os"
	"path/filepath"
	"strings"
)

// ResolveManagedServicesInternalNetwork reserves the private HTTPS gateway and
// one return-path anchor per gateway node before Flux first sees the overlay.
// The Fleet scaffold selects this path only for managed services behind 1:1
// NAT, where tenant traffic cannot hairpin through the public address.
func ResolveManagedServicesInternalNetwork(fleetRepo, clusterName string, ingressNodes []string, out io.Writer) error {
	if out == nil {
		out = io.Discard
	}
	path := filepath.Join(fleetRepo, "clusters", clusterName, "cluster-config.env")
	content, err := os.ReadFile(path)
	if err != nil {
		return fmt.Errorf("managed-services internal network: read %s: %w", path, err)
	}
	env := string(content)
	if envValue(env, "SERVICES_INTERNAL_GATEWAY_REQUIRED") != "true" {
		return fmt.Errorf("managed-services internal network: the Fleet scaffold did not declare SERVICES_INTERNAL_GATEWAY_REQUIRED=true")
	}
	cidr := strings.TrimSpace(envValue(env, keyExtCIDR))
	_, network, err := net.ParseCIDR(cidr)
	if err != nil || network.IP.To4() == nil {
		return fmt.Errorf("managed-services internal network: %s must be an IPv4 CIDR", keyExtCIDR)
	}
	gw := net.ParseIP(strings.TrimSpace(envValue(env, keyExtGateway))).To4()
	if gw == nil || !network.Contains(gw) {
		return fmt.Errorf("managed-services internal network: %s must be inside %s", keyExtGateway, cidr)
	}
	exclude := strings.TrimSpace(envValue(env, keyExtExclude))
	if strings.Contains(exclude, ",") {
		return fmt.Errorf("managed-services internal network: %s must be one contiguous reservation", keyExtExclude)
	}
	start, end, ok := parseExcludeEntry(exclude)
	if !ok || !network.Contains(start) || !network.Contains(end) || ip4ToU32(gw) < ip4ToU32(start) || ip4ToU32(gw) > ip4ToU32(end) {
		return fmt.Errorf("managed-services internal network: %s must be a contiguous range inside %s covering the gateway", keyExtExclude, cidr)
	}
	nodes := canonicalIngressNodes(strings.Split(strings.TrimSpace(envValue(env, "KUBE_OVN_GW_NODES")), ","))
	if len(nodes) == 0 {
		nodes = canonicalIngressNodes(ingressNodes)
	}
	if len(nodes) == 0 {
		return fmt.Errorf("managed-services internal network: no gateway nodes; declare KUBE_OVN_GW_NODES or reviewed ingress nodes")
	}
	prefix, _ := network.Mask.Size()
	first := ip4ToU32(end) + 1
	last := first + uint32(len(nodes))
	if last < first {
		return fmt.Errorf("managed-services internal network: address range overflow")
	}
	vip, final := u32ToIP4(first), u32ToIP4(last)
	if !network.Contains(vip) || !network.Contains(final) || isBroadcast(final, network) {
		return fmt.Errorf("managed-services internal network: %s has no room for the HTTPS VIP and %d anchors after %s", cidr, len(nodes), end)
	}
	for _, key := range []string{"KUBE_API_INTERNAL_VIP", "METALLB_FLOATING_IP", "EXT_NET_MGMT_SNAT_IP"} {
		if value := net.ParseIP(strings.TrimSpace(envValue(env, key))).To4(); value != nil && ip4ToU32(value) >= first && ip4ToU32(value) <= last {
			return fmt.Errorf("managed-services internal network: reserved %s=%s overlaps the proposed HTTPS VIP/anchors", key, value)
		}
	}
	if value := strings.TrimSpace(envValue(env, "ENVOY_GATEWAY_INTERNAL_VIP")); value != "" {
		return fmt.Errorf("managed-services internal network: ENVOY_GATEWAY_INTERNAL_VIP is already set; this new-install path cannot replace it")
	}
	anchors := make([]string, 0, len(nodes))
	for i, node := range nodes {
		anchors = append(anchors, fmt.Sprintf("%s=%s/%d", node, u32ToIP4(first+uint32(i)+1), prefix))
	}
	allowlist := func(key string) (string, error) {
		raw := strings.TrimSpace(envValue(env, key))
		if raw == "" {
			raw = "[]"
		}
		var values []string
		if err := json.Unmarshal([]byte(raw), &values); err != nil {
			return "", fmt.Errorf("%s must be a JSON string array: %w", key, err)
		}
		for _, value := range values {
			if value == vip.String() {
				encoded, _ := json.Marshal(values)
				return string(encoded), nil
			}
		}
		values = append(values, vip.String())
		encoded, _ := json.Marshal(values)
		return string(encoded), nil
	}
	ingress, err := allowlist("INGRESS_GLOBAL_ALLOWLIST")
	if err != nil {
		return err
	}
	egress, err := allowlist("EGRESS_GLOBAL_ALLOWLIST")
	if err != nil {
		return err
	}
	// Tenant pods with a VPC NIC send unknown destinations through that NIC.
	// The HTTPS VIP must instead use the infra attachment, whose workload
	// SecurityGroup needs the corresponding exact TCP/443 grant. Without all
	// three values, backups time out before reaching the local object store.
	routes := strings.TrimSpace(envValue(env, "INFRA_ATTACHMENT_ROUTES"))
	if routes == "" {
		return fmt.Errorf("managed-services internal network: INFRA_ATTACHMENT_ROUTES is empty")
	}
	vipRouted := false
	for _, route := range strings.Split(routes, ",") {
		_, routeNet, routeErr := net.ParseCIDR(strings.TrimSpace(route))
		if routeErr != nil {
			return fmt.Errorf("managed-services internal network: invalid INFRA_ATTACHMENT_ROUTES entry %q: %w", route, routeErr)
		}
		vipRouted = vipRouted || routeNet.Contains(vip)
	}
	if !vipRouted {
		routes += "," + vip.String() + "/32"
	}
	updates := [][2]string{
		{"KUBE_OVN_GW_NODES", strings.Join(nodes, ",")},
		{keyExtExclude, start.String() + ".." + final.String()},
		{"ENVOY_GATEWAY_INTERNAL_VIP", vip.String()},
		{"PLATFORM_ENDPOINT_ENVOY_GATEWAY_ENABLED", "true"},
		{"INGRESS_GLOBAL_ALLOWLIST", ingress},
		{"EGRESS_GLOBAL_ALLOWLIST", egress},
		{"EXT_NET_ANCHOR_IPS", strings.Join(anchors, ",")},
		{"EXT_NET_ANCHOR_REQUIRED", "true"},
		{"INFRA_ATTACHMENT_ROUTES", routes},
		{KeyPlatformIngressVIP, vip.String()},
		{"INFRA_ATTACHMENT_PLATFORM_INGRESS_WORKLOAD_EGRESS", "true"},
	}
	lines := strings.Split(env, "\n")
	for _, update := range updates {
		key, value := update[0], update[1]
		found := false
		for _, line := range lines {
			if strings.HasPrefix(strings.TrimSpace(line), key+"=") {
				found = true
				break
			}
		}
		if found {
			lines, _, _ = setEnvValueLine(key, value)(lines)
			continue
		}
		// Starter presets may document optional keys without assigning them.
		// Insert the resolved value rather than silently dropping it.
		entry := key + "=" + value
		if len(lines) > 0 && lines[len(lines)-1] == "" {
			lines = append(lines[:len(lines)-1], entry, "")
		} else {
			lines = append(lines, entry)
		}
	}
	result := strings.Join(lines, "\n")
	if !strings.HasSuffix(result, "\n") {
		result += "\n"
	}
	if err := os.WriteFile(path, []byte(result), 0o644); err != nil {
		return fmt.Errorf("managed-services internal network: write %s: %w", path, err)
	}
	fmt.Fprintf(out, "[scaffold] managed-services internal HTTPS VIP %s, %d return-path anchor(s), reserved %s=%s..%s\n", vip, len(nodes), keyExtExclude, start, final)
	return nil
}
