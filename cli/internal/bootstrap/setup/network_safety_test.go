package setup

import (
	"context"
	"strings"
	"testing"
)

func networkSafetyFixture() (*evidenceHostSSHStub, Host) {
	stub, req := resourceFixture()
	host := req.Host
	host.NIC = "eth1"
	a := stub.answers["server-1"]
	a[sshConnectionCommand] = "198.51.100.7 42000 192.0.2.10 22\n" // Actual jump-host peer.
	a["ip -4 -j route get 198.51.100.7 from 192.0.2.10"] = `[{"dev":"eth0","gateway":"192.0.2.1","prefsrc":"192.0.2.10","table":100}]`
	a[networkConfigurationCommand] = strings.Repeat("a", 64) + "  -\n"
	a[networkManagersCommand] = "ActiveState=active\nLoadState=loaded\nId=systemd-networkd.service\n\nId=NetworkManager.service\nLoadState=not-found\nActiveState=inactive\n\nId=networking.service\nLoadState=not-found\nActiveState=inactive\n"
	a["networkctl --no-pager --no-legend status 'eth1'"] = "Network File: n/a\n"
	a[networkLinksCommand] = `[{"ifname":"eth0","link_type":"ether","address":"02:00:00:00:00:01","mtu":1500,"operstate":"UP","flags":["UP"]},{"ifname":"eth1","link_type":"ether","address":"02:00:00:00:00:02","mtu":1500,"operstate":"UP","flags":["UP"]}]`
	a["ip -j addr show"] = `[{"ifname":"eth0","address":"02:00:00:00:00:01","mtu":1500,"addr_info":[{"local":"192.0.2.10","prefixlen":24}]},{"ifname":"eth1","address":"02:00:00:00:00:02","mtu":1500,"addr_info":[]}]`
	a["ip -6 -j route show table all"] = `[]`
	return stub, host
}

func TestNetworkSafetyUsesActualSSHPeerAndPersistentOwnership(t *testing.T) {
	stub, host := networkSafetyFixture()
	safety, err := InspectNetworkSafety(context.Background(), host, stub)
	if err != nil {
		t.Fatal(err)
	}
	if safety.Peer != "198.51.100.7" || safety.ReturnRoute.Interface != "eth0" || safety.Carrier != "eth1" || safety.ConfigurationSHA256 != strings.Repeat("a", 64) {
		t.Fatalf("incomplete return-path binding: %+v", safety)
	}
	if stub.writeCalls != 0 || stub.uncappedRuns != 0 {
		t.Fatal("network inspection made writes or unbounded reads")
	}
	for _, variant := range []string{"management", "owned", "unknown-owner", "ipv6-route", "child", "key-change", "malformed-peer"} {
		t.Run(variant, func(t *testing.T) {
			stub, host := networkSafetyFixture()
			a := stub.answers["server-1"]
			switch variant {
			case "management":
				host.NIC = "eth0"
			case "owned":
				a["networkctl --no-pager --no-legend status 'eth1'"] = "Network File: /run/systemd/network/10-netplan-eth1.network\n"
			case "unknown-owner":
				a[networkManagersCommand] = "Id=systemd-networkd.service\n"
			case "ipv6-route":
				a["ip -6 -j route show table all"] = `[{"dst":"2001:db8::/64","dev":"eth1"}]`
			case "child":
				a[networkLinksCommand] = strings.TrimSuffix(a[networkLinksCommand], "]") + `,{"ifname":"eth1.10","link_type":"ether","address":"02:00:00:00:00:02","mtu":1500,"link":"eth1","linkinfo":{"info_kind":"vlan","info_data":{"id":10}}}]`
			case "key-change":
				stub.differentCommand = networkConfigurationCommand
				stub.differentEvidence = stub.evidence
				stub.differentEvidence.Address = "another:22"
			case "malformed-peer":
				a[sshConnectionCommand] = "PASSWORD=sensitive-value\n"
			}
			if _, err := InspectNetworkSafety(context.Background(), host, stub); err == nil {
				t.Fatal("unsafe carrier accepted")
			} else if strings.Contains(err.Error(), "sensitive-value") {
				t.Fatal("host output leaked")
			}
			if stub.writeCalls != 0 {
				t.Fatal("failure caused host writes")
			}
		})
	}
}

func TestAddressPlanAllocationsAndIntentionalLANSharing(t *testing.T) {
	env := map[string]string{"POD_CIDR": "10.100.0.0/16", "CLUSTER_DNS": "10.101.0.11", "K8S_SERVICE_IP": "10.101.0.1", "SVC_CIDR": "10.101.0.0/16", "JOIN_CIDR": "100.66.0.0/16", "INFRA_ATTACHMENT_CIDR": "100.67.0.0/16", "EXT_NET_CIDR": "100.65.0.0/16", "EXT_PUBLIC_CIDR": "203.0.113.0/24", "NODE_CIDR": "100.65.0.0/16", "EXT_NET_EXCLUDE_IPS": "100.65.0.1..100.65.0.20", "EXT_NET_GATEWAY": "100.65.0.1", "EXT_NET_MGMT_SNAT_IP": "100.65.0.2", "KUBE_API_INTERNAL_VIP": "100.65.0.3", "ENVOY_GATEWAY_INTERNAL_VIP": "100.65.0.4", "EXT_NET_ANCHOR_IPS": "server-1=100.65.0.10/16", "INFRA_ATTACHMENT_ROUTES": "100.65.0.0/16,100.66.0.0/16,10.100.0.0/16"}
	hosts := []HostObservation{{ID: "server-1", Facts: HostFacts{NICs: []NICFact{{Name: "eth0", Addresses: []string{"100.65.0.11"}, Prefixes: []string{"100.65.0.11/16"}}}}}}
	if findings := ValidateAddressPlan(env, hosts); len(findings) != 0 {
		t.Fatalf("intentional reserved shared LAN or routes rejected: %+v", findings)
	}
	for _, change := range []struct{ key, value string }{{"SVC_CIDR", "10.100.0.0/16"}, {"K8S_SERVICE_IP", "10.101.0.11"}, {"EXT_NET_ANCHOR_IPS", "server-1=100.65.0.10/8"}, {"NODE_CIDR", "10.101.0.0/16"}, {"EXT_NET_MGMT_SNAT_IP", "100.65.0.11"}, {"ENVOY_GATEWAY_INTERNAL_VIP", "100.65.0.3"}, {"EXT_NET_ANCHOR_IPS", "server-1=100.65.0.1/16"}, {"EXT_NET_EXCLUDE_IPS", "100.65.0.1"}, {"EXT_PUBLIC_CIDR", "2001:db8::/64"}, {"POD_CIDR", "100.65.0.0/16"}} {
		t.Run(change.key, func(t *testing.T) {
			copy := map[string]string{}
			for k, v := range env {
				copy[k] = v
			}
			copy[change.key] = change.value
			if len(ValidateAddressPlan(copy, hosts)) == 0 {
				t.Fatal("unsafe address plan accepted")
			}
		})
	}
}

func TestIPv4PlanAllowsNormalIPv6Loopback(t *testing.T) {
	hosts := []HostObservation{{ID: "server-1", Facts: HostFacts{NICs: []NICFact{{Name: "lo", Addresses: []string{"127.0.0.1", "::1"}, Prefixes: []string{"127.0.0.1/8", "::1/128"}}}}}}
	if findings := ValidateAddressPlan(map[string]string{"POD_CIDR": "10.100.0.0/16"}, hosts); len(findings) != 0 {
		t.Fatalf("normal loopback rejected: %+v", findings)
	}
	hosts[0].Facts.NICs[0].Prefixes = append(hosts[0].Facts.NICs[0].Prefixes, "2001:db8::1/64")
	if len(ValidateAddressPlan(map[string]string{}, hosts)) == 0 {
		t.Fatal("unsupported globally routed IPv6 passed")
	}
}
