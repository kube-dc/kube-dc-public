package clusterinit

import (
	"fmt"
	"net"
	"strconv"
)

// SettingInfo describes inputs for which the installer knows the value shape.
// Fleet can add other environment names without a CLI release. Unknown keys
// remain available, but their meaning depends on the selected Fleet checkout.
type SettingInfo struct {
	Description string
	Format      string
}

var settingInfo = map[string]SettingInfo{
	"EXT_NET_MTU":              {"MTU of the tenant external network.", "integer: 576-9216"},
	"EXT_NET_VLAN_ID":          {"VLAN ID for the tenant external network.", "VLAN ID"},
	"EXT_PUBLIC_VLAN_ID":       {"VLAN ID for the public network.", "VLAN ID"},
	"EXT_NET_INTERFACE":        {"Default tenant network interface on each node.", "Linux interface"},
	"EXT_NET_ANCHOR_INTERFACE": {"Interface used by the network anchor.", "Linux interface"},
	"METALLB_INTERFACE":        {"Interface that announces the ingress address.", "Linux interface"},
	"POD_CIDR":                 {"Address range for pods.", "CIDR"},
	"SVC_CIDR":                 {"Address range for services.", "CIDR"},
	"JOIN_CIDR":                {"Address range for joined nodes.", "CIDR"},
	"NODE_CIDR":                {"Address range of node management addresses.", "CIDR"},
	"EXT_NET_CIDR":             {"Address range of the tenant external network.", "CIDR"},
	"EXT_PUBLIC_CIDR":          {"Address range of the public network.", "CIDR"},
	"INGRESS_HOST_CIDR":        {"Source range used by ingress nodes.", "CIDR"},
	"EXT_NET_GATEWAY":          {"Gateway of the tenant external network.", "IP address"},
	"EXT_PUBLIC_GATEWAY":       {"Gateway of the public network.", "IP address"},
	"K8S_SERVICE_IP":           {"Service address used by the management API.", "IP address"},
	"KUBE_API_ARRIVAL_IP":      {"Address that receives external Kubernetes API traffic.", "IP address"},
	"NODE_EXTERNAL_IP":         {"External address of the first node.", "IP address"},
	"SMTP_PORT":                {"Port of the SMTP server.", "integer: 1-65535"},
	"METALLB_MODE":             {"Address announcement mode.", "l2 or bgp"},
	"INGRESS_ADDRESS_LAYER":    {"Component that owns the ingress address.", "none, metallb-l2, or metallb-bgp"},
	"KUBE_OVN_GW_TYPE":         {"External gateway placement.", "centralized or distributed"},
}

// KnownSetting returns typed help for selected Fleet inputs. A missing entry
// means the CLI has no scalar schema for that key; it does not mean the key is
// unsupported by Fleet.
func KnownSetting(key string) (SettingInfo, bool) {
	info, ok := settingInfo[key]
	return info, ok
}

func validateKnownSetting(key, value string) error {
	if value == "" {
		return nil // explicit empty may intentionally disable a Fleet default
	}
	bad := func(format string) error { return fmt.Errorf("%s must be %s", key, format) }
	switch key {
	case "EXT_NET_MTU":
		n, err := strconv.Atoi(value)
		if err != nil || n < 576 || n > 9216 {
			return bad("an MTU from 576 to 9216")
		}
	case "EXT_NET_VLAN_ID", "EXT_PUBLIC_VLAN_ID":
		if validateVLANID(value) != "" {
			return bad("a valid VLAN ID")
		}
	case "EXT_NET_INTERFACE", "EXT_NET_ANCHOR_INTERFACE", "METALLB_INTERFACE":
		if validateNICName(value) != "" {
			return bad("a Linux interface name")
		}
	case "POD_CIDR", "SVC_CIDR", "JOIN_CIDR", "NODE_CIDR", "EXT_NET_CIDR", "EXT_PUBLIC_CIDR", "INGRESS_HOST_CIDR":
		if _, _, err := net.ParseCIDR(value); err != nil {
			return bad("a CIDR")
		}
	case "EXT_NET_GATEWAY", "EXT_PUBLIC_GATEWAY", "K8S_SERVICE_IP", "KUBE_API_ARRIVAL_IP", "NODE_EXTERNAL_IP":
		if net.ParseIP(value) == nil {
			return bad("an IP address")
		}
	case "SMTP_PORT":
		n, err := strconv.Atoi(value)
		if err != nil || n < 1 || n > 65535 {
			return bad("a port from 1 to 65535")
		}
	case "METALLB_MODE":
		if value != "l2" && value != "bgp" {
			return bad("l2 or bgp")
		}
	case "INGRESS_ADDRESS_LAYER":
		if value != "none" && value != "metallb-l2" && value != "metallb-bgp" {
			return bad("none, metallb-l2, or metallb-bgp")
		}
	case "KUBE_OVN_GW_TYPE":
		if value != "centralized" && value != "distributed" {
			return bad("centralized or distributed")
		}
	}
	return nil
}
