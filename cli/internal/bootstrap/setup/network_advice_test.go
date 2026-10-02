package setup

import (
	"context"
	"strings"
	"testing"
	"time"
)

func networkAdviceFixture(t *testing.T) HostResources {
	t.Helper()
	stub, req := resourceFixture()
	stub.answers["server-1"]["ip -j addr show"] = `[{"ifname":"eth0","mtu":1500,"address":"02:00:00:00:00:01","addr_info":[{"local":"192.0.2.10"}]},{"ifname":"eth1","mtu":1500,"address":"02:00:00:00:00:02","addr_info":[]}]`
	stub.answers["server-1"][networkLinksCommand] = `[{"ifname":"eth0","mtu":1500,"address":"02:00:00:00:00:01","link_type":"ether","operstate":"UP","flags":["UP"]},{"ifname":"eth1","mtu":1500,"address":"02:00:00:00:00:02","link_type":"ether","operstate":"UP","flags":["UP"]}]`
	h, err := DiscoverResources(context.Background(), req, stub)
	if err != nil {
		t.Fatal(err)
	}
	if !h.Network.Complete {
		t.Fatal("network inventory not collected")
	}
	return h
}

func TestNetworkSuggestionsRespectLinkOwnership(t *testing.T) {
	for _, tc := range []struct {
		name                  string
		selectable, suggested bool
	}{
		{"unused", true, true}, {"bond", true, true}, {"addressed", true, false}, {"policy-route", true, false},
		{"down", true, false}, {"bond-member", false, false}, {"ovs-member", false, false}, {"vlan", false, false},
		{"veth", false, false}, {"vlan-parent", true, false}, {"identity-change", false, false}, {"missing-address-inventory", false, false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			h := networkAdviceFixture(t)
			link := &h.Network.Links[1]
			switch tc.name {
			case "bond":
				link.Kind = "bond"
			case "addressed":
				h.Host.Facts.NICs[1].Addresses = []string{"fe80::1"}
			case "policy-route":
				h.Network.RouteNICs = append(h.Network.RouteNICs, "eth1")
			case "down":
				link.State, link.Up = "DOWN", false
			case "bond-member":
				link.Master = "bond0"
			case "ovs-member":
				link.Master = "ovs-system"
			case "vlan":
				link.Kind, link.Parent = "vlan", "eth0"
			case "veth":
				link.Kind = "veth"
			case "vlan-parent":
				h.Network.Links = append(h.Network.Links, NetworkLink{Name: "eth1.200", Parent: "eth1", Kind: "vlan"})
			case "identity-change":
				link.MAC = "02:00:00:00:00:03"
			case "missing-address-inventory":
				h.Host.Facts.NICs = h.Host.Facts.NICs[:1]
			}
			found := false
			for _, c := range NetworkCandidates(h, time.Now()) {
				if c.Name == "eth0" && c.Suggested {
					t.Fatal("management link suggested")
				}
				if c.Name == "eth1" {
					found = true
					if c.Selectable != tc.selectable || c.Suggested != tc.suggested {
						t.Fatalf("wrong advice: %+v", c)
					}
				}
			}
			if !found {
				t.Fatal("link evidence missing")
			}
		})
	}
}

func TestNetworkSuggestionsRequireFreshVerifiedInventory(t *testing.T) {
	for _, variant := range []string{"expired", "future", "no-pin", "untrusted", "incomplete"} {
		t.Run(variant, func(t *testing.T) {
			h := networkAdviceFixture(t)
			switch variant {
			case "expired":
				h.Host.ObservedAt = time.Now().Add(-6 * time.Minute)
			case "future":
				h.Host.ObservedAt = time.Now().Add(time.Minute)
			case "no-pin":
				h.Host.Facts.HostKey = nil
			case "untrusted":
				h.Host.Checks = nil
			case "incomplete":
				h.Network.Complete = false
			}
			if len(NetworkCandidates(h, time.Now())) != 0 {
				t.Fatal("unverified network suggestion")
			}
		})
	}
}

func TestNetworkRouteInventoryIncludesPolicyTablesAndECMP(t *testing.T) {
	links := []byte(`[{"ifname":"eth0","link_type":"ether"},{"ifname":"bond0","link_type":"ether","linkinfo":{"info_kind":"bond"}}]`)
	routes := []byte(`[{"dst":"198.51.100.0/24","table":129,"nexthops":[{"dev":"eth0"},{"dev":"bond0"}]}]`)
	inv, err := parseNetworkInventory(links, routes)
	if err != nil || strings.Join(inv.RouteNICs, ",") != "bond0,eth0" {
		t.Fatalf("ECMP route omitted: %+v %v", inv, err)
	}
	for _, bad := range []string{`null`, `{}`, `[{"dev":"missing"}]`} {
		if _, err := parseNetworkInventory(links, []byte(bad)); err == nil {
			t.Fatalf("bad routes accepted: %s", bad)
		}
	}
}

func TestNetworkReadRetainsPinnedIdentity(t *testing.T) {
	stub, req := resourceFixture()
	stub.differentCommand = networkRoutesCommand
	stub.differentEvidence = stub.evidence
	stub.differentEvidence.FingerprintSHA256 = "changed"
	h, err := DiscoverResources(context.Background(), req, stub)
	if err == nil || h.Network.Complete || len(NetworkCandidates(h, time.Now())) != 0 {
		t.Fatal("late identity change accepted")
	}
	if stub.uncappedRuns != 0 || stub.writeCalls != 0 {
		t.Fatal("network inventory performed an uncapped read or mutation")
	}
}
