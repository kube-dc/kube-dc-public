package setup

import (
	"encoding/json"
	"fmt"
	"net"
	"sort"
	"strings"
	"time"
)

const networkLinksCommand = "ip -j -d link show"
const networkRoutesCommand = "ip -j route show table all"

// NetworkInventory describes local link ownership. It cannot establish switch
// VLAN membership, address availability, or upstream routing.
type NetworkInventory struct {
	Complete  bool
	Links     []NetworkLink
	RouteNICs []string
}

type NetworkLink struct {
	Name, Kind, Master, Parent, State, MAC string
	MTU                                    int
	Up                                     bool
}

type NetworkCandidate struct {
	Name, Kind, Reason    string
	MTU                   int
	Selectable, Suggested bool
}

func inspectNetworkInventory(read func(string) ([]byte, error)) (NetworkInventory, error) {
	links, err := read(networkLinksCommand)
	if err != nil {
		return NetworkInventory{}, err
	}
	routes, err := read(networkRoutesCommand)
	if err != nil {
		return NetworkInventory{}, err
	}
	return parseNetworkInventory(links, routes)
}

func parseNetworkInventory(links, routes []byte) (NetworkInventory, error) {
	var raw []struct {
		Name   string   `json:"ifname"`
		Type   string   `json:"link_type"`
		MAC    string   `json:"address"`
		Master string   `json:"master"`
		Parent string   `json:"link"`
		MTU    int      `json:"mtu"`
		State  string   `json:"operstate"`
		Flags  []string `json:"flags"`
		Info   struct {
			Kind string `json:"info_kind"`
		} `json:"linkinfo"`
	}
	if err := json.Unmarshal(links, &raw); err != nil || len(raw) == 0 {
		return NetworkInventory{}, fmt.Errorf("invalid detailed link inventory")
	}
	out := NetworkInventory{}
	seen := map[string]bool{}
	for _, link := range raw {
		if !nicNamePattern.MatchString(link.Name) || seen[link.Name] {
			return NetworkInventory{}, fmt.Errorf("invalid or duplicate interface name")
		}
		seen[link.Name] = true
		kind := link.Info.Kind
		if kind == "" {
			kind = link.Type
		}
		up := false
		for _, flag := range link.Flags {
			if flag == "UP" {
				up = true
			}
		}
		out.Links = append(out.Links, NetworkLink{Name: link.Name, Kind: kind, Master: link.Master, Parent: link.Parent, State: link.State, MAC: link.MAC, MTU: link.MTU, Up: up})
	}
	var routeList []struct {
		Dev      string `json:"dev"`
		Nexthops []struct {
			Dev string `json:"dev"`
		} `json:"nexthops"`
	}
	if err := json.Unmarshal(routes, &routeList); err != nil || routeList == nil {
		return NetworkInventory{}, fmt.Errorf("invalid all-table route inventory")
	}
	routed := map[string]bool{}
	for _, route := range routeList {
		if route.Dev != "" {
			routed[route.Dev] = true
		}
		for _, hop := range route.Nexthops {
			if hop.Dev != "" {
				routed[hop.Dev] = true
			}
		}
	}
	for name := range routed {
		if !seen[name] {
			return NetworkInventory{}, fmt.Errorf("route interface is missing from link inventory; inspect again")
		}
		out.RouteNICs = append(out.RouteNICs, name)
	}
	sort.Strings(out.RouteNICs)
	sort.Slice(out.Links, func(i, j int) bool { return out.Links[i].Name < out.Links[j].Name })
	out.Complete = true
	return out, nil
}

// NetworkCandidates suggests only unused, up Ethernet links or bonds. A
// suggestion is a local candidate; the operator must confirm the switch path.
// Addressed/shared carriers remain explicit choices with a review hint.
func NetworkCandidates(host HostResources, now time.Time) []NetworkCandidate {
	h := host.Host
	age := now.Sub(h.ObservedAt)
	if !host.Network.Complete || h.Facts.MachineID == "" || h.Facts.HostKey == nil || !ValidHostKeySHA256(h.Facts.HostKey.FingerprintSHA256) || age < 0 || age >= 5*time.Minute || h.ObservedAt.IsZero() {
		return nil
	}
	trusted, addressesChecked := false, false
	for _, check := range h.Checks {
		if check.ID == "host-key" && check.Status == "pass" {
			trusted = true
		}
		if check.ID == "network" && check.Status == "pass" {
			addressesChecked = true
		}
	}
	if !trusted || !addressesChecked {
		return nil
	}
	facts := map[string]NICFact{}
	for _, f := range h.Facts.NICs {
		facts[f.Name] = f
	}
	routed, parents := map[string]bool{}, map[string]bool{}
	for _, name := range host.Network.RouteNICs {
		routed[name] = true
	}
	for _, link := range host.Network.Links {
		if link.Parent != "" {
			parents[link.Parent] = true
		}
	}
	var candidates []NetworkCandidate
	for _, link := range host.Network.Links {
		c := NetworkCandidate{Name: link.Name, Kind: link.Kind, MTU: link.MTU}
		fact, exists := facts[link.Name]
		_, macErr := net.ParseMAC(link.MAC)
		switch {
		case !exists || fact.MAC != link.MAC || fact.MTU != link.MTU || macErr != nil:
			c.Reason = "Link identity is incomplete or changed. Inspect again."
		case link.Master != "":
			c.Reason = "Member of " + link.Master + ". Review its parent interface."
		case link.Kind == "vlan":
			c.Reason = "Host VLAN interface. Review the parent trunk and OVS VLAN ownership."
		case link.Kind != "ether" && link.Kind != "bond":
			c.Reason = "Virtual or managed link. Use a reviewed host network design."
		case link.MTU < 576:
			c.Reason = "Invalid carrier MTU. Inspect the host configuration."
		default:
			c.Selectable = true
			switch {
			case len(fact.Addresses) > 0 || routed[link.Name]:
				c.Reason = "Carries addresses or routes. Review management access before using this shared link."
			case parents[link.Name]:
				c.Reason = "Has child interfaces. Review VLAN ownership before using this trunk."
			case !link.Up || strings.ToUpper(link.State) != "UP":
				c.Reason = "Link is not up. Check cabling and switch configuration."
			default:
				c.Suggested = true
				c.Reason = "Unused link on this host. Confirm cabling and switch VLANs."
			}
		}
		candidates = append(candidates, c)
	}
	return candidates
}
