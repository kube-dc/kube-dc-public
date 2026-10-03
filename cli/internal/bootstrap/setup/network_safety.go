package setup

import (
	"context"
	"encoding/json"
	"fmt"
	"net/netip"
	"reflect"
	"sort"
	"strconv"
	"strings"
	"time"

	"github.com/shalb/kube-dc/cli/internal/bootstrap/ports"
)

const sshConnectionCommand = `printf '%s\n' "$SSH_CONNECTION"`

// Hash configuration on the host. NetworkManager profiles can contain passwords;
// neither their contents nor individual hashes enter an ordinary report.
const networkConfigurationCommand = `sudo -n bash -o pipefail -c 'set -eu; for d in /etc/netplan /etc/systemd/network /etc/network /etc/NetworkManager/system-connections; do if test -d "$d"; then find "$d" -maxdepth 3 -type f -exec sha256sum -- {} +; fi; done | LC_ALL=C sort | sha256sum'`
const networkManagersCommand = `systemctl show -p Id -p LoadState -p ActiveState systemd-networkd.service NetworkManager.service networking.service`

type NetworkSafety struct {
	MachineID           string           `json:"machineId"`
	ConfigurationSHA256 string           `json:"configurationSHA256"`
	Managers            string           `json:"managers"`
	Carrier             string           `json:"carrier"`
	CarrierOwner        string           `json:"carrierOwner"`
	Peer                string           `json:"peer"`
	LocalAddress        string           `json:"localAddress"`
	ReturnRoute         SSHReturnRoute   `json:"returnRoute"`
	Inventory           NetworkInventory `json:"inventory"`
	NICs                []NICFact        `json:"nics"`
}

type SSHReturnRoute struct {
	Interface string `json:"interface"`
	Gateway   string `json:"gateway,omitempty"`
	Source    string `json:"source"`
	Table     string `json:"table,omitempty"`
}

// InspectNetworkSafety reads the actual server-side SSH peer, including a jump
// host, and fingerprints persistent configuration without disclosing secrets.
// It only permits an unused carrier. Shared-carrier migration needs a separately
// reviewed recovery mechanism; this reader never guesses that mechanism.
func InspectNetworkSafety(ctx context.Context, host Host, ssh ports.CappedSSHHostKeyClient) (NetworkSafety, error) {
	var out NetworkSafety
	if ssh == nil || !ValidHostKeySHA256(host.HostKeySHA256) || !nicNamePattern.MatchString(host.NIC) {
		return out, fmt.Errorf("network safety requires a pinned host and an explicit carrier")
	}
	ctx, cancel := context.WithTimeout(ctx, hostProbeTimeout)
	defer cancel()
	endpoint := sshHost(host.SSHAlias)
	endpoint.ExpectedHostKeySHA256 = host.HostKeySHA256
	var identity *ports.SSHHostKeyEvidence
	read := func(command string) ([]byte, error) {
		if err := ctx.Err(); err != nil {
			return nil, err
		}
		body, key, err := ssh.RunCappedWithHostKey(ctx, endpoint, command, maxHostProbeOutput)
		if err != nil {
			return nil, fmt.Errorf("network safety read failed")
		}
		if key.FingerprintSHA256 != host.HostKeySHA256 || len(body) > maxHostProbeOutput || (identity != nil && *identity != key) {
			return nil, fmt.Errorf("network safety host identity changed or output exceeded its limit")
		}
		identity = &key
		return body, nil
	}
	connection, err := read(sshConnectionCommand)
	if err != nil {
		return out, err
	}
	fields := strings.Fields(string(connection))
	if len(fields) != 4 {
		return out, fmt.Errorf("SSH return path is unavailable")
	}
	peer, pErr := netip.ParseAddr(fields[0])
	local, lErr := netip.ParseAddr(fields[2])
	if pErr != nil || lErr != nil || peer.Is4() != local.Is4() || peer.Zone() != "" || local.Zone() != "" {
		return out, fmt.Errorf("invalid SSH connection addresses")
	}
	for _, port := range []string{fields[1], fields[3]} {
		if n, err := strconv.Atoi(port); err != nil || n < 1 || n > 65535 {
			return out, fmt.Errorf("invalid SSH connection port")
		}
	}
	family := "-4"
	if peer.Is6() {
		family = "-6"
	}
	route, err := read("ip " + family + " -j route get " + peer.String() + " from " + local.String())
	if err != nil {
		return out, err
	}
	var routes []struct {
		Dev     string          `json:"dev"`
		Gateway string          `json:"gateway"`
		Source  string          `json:"prefsrc"`
		From    string          `json:"src"`
		Table   json.RawMessage `json:"table"`
		Type    string          `json:"type"`
	}
	if err := json.Unmarshal(route, &routes); err != nil || len(routes) != 1 || !nicNamePattern.MatchString(routes[0].Dev) {
		return out, fmt.Errorf("SSH return route is unreadable or ambiguous")
	}
	r := routes[0]
	if r.Type != "" && r.Type != "unicast" && r.Type != "local" {
		return out, fmt.Errorf("SSH return route is not usable")
	}
	source := r.Source
	if source == "" {
		source = r.From
	}
	if source == "" {
		source = local.String()
	}
	if source != local.String() {
		return out, fmt.Errorf("SSH return route uses a different source")
	}
	if r.Gateway != "" {
		if _, err := netip.ParseAddr(r.Gateway); err != nil {
			return out, fmt.Errorf("invalid SSH return gateway")
		}
	}
	configuration, err := read(networkConfigurationCommand)
	if err != nil {
		return out, err
	}
	parts := strings.Fields(string(configuration))
	if len(parts) != 2 || !hexSHA256.MatchString(parts[0]) || parts[1] != "-" {
		return out, fmt.Errorf("persistent network configuration could not be fingerprinted")
	}
	managers, err := read(networkManagersCommand)
	if err != nil {
		return out, err
	}
	active, canonicalManagers, err := parseNetworkManagers(string(managers))
	if err != nil {
		return out, err
	}
	owner := "unmanaged"
	if active["networking.service"] {
		return out, fmt.Errorf("ifupdown ownership needs a reviewed carrier migration")
	}
	if active["systemd-networkd.service"] {
		status, err := read("networkctl --no-pager --no-legend status '" + host.NIC + "'")
		if err != nil {
			return out, err
		}
		found := false
		for _, line := range strings.Split(string(status), "\n") {
			key, value, ok := strings.Cut(strings.TrimSpace(line), ":")
			if ok && key == "Network File" {
				found = true
				if strings.TrimSpace(value) != "n/a" && strings.TrimSpace(value) != "-" {
					owner = "systemd-networkd"
				}
			}
		}
		if !found {
			return out, fmt.Errorf("selected carrier networkd ownership is unknown")
		}
	}
	if active["NetworkManager.service"] {
		status, err := read("nmcli -t -f GENERAL.NM-MANAGED device show '" + host.NIC + "'")
		if err != nil {
			return out, err
		}
		switch strings.TrimSpace(string(status)) {
		case "GENERAL.NM-MANAGED:no":
		case "GENERAL.NM-MANAGED:yes":
			owner = "NetworkManager"
		default:
			return out, fmt.Errorf("selected carrier NetworkManager ownership is unknown")
		}
	}
	links, err := read(networkLinksCommand)
	if err != nil {
		return out, err
	}
	ipv4Routes, err := read(networkRoutesCommand)
	if err != nil {
		return out, err
	}
	inv, err := parseNetworkInventory(links, ipv4Routes)
	if err != nil {
		return out, err
	}
	ipv6Routes, err := read("ip -6 -j route show table all")
	if err != nil {
		return out, err
	}
	v6, err := parseNetworkInventory(links, ipv6Routes)
	if err != nil {
		return out, err
	}
	inv.RouteNICs = append(inv.RouteNICs, v6.RouteNICs...)
	sort.Strings(inv.RouteNICs)
	inv.RouteNICs = compactStrings(inv.RouteNICs)
	addresses, err := read("ip -j addr show")
	if err != nil {
		return out, err
	}
	nics, err := parseNICFacts(string(addresses))
	if err != nil {
		return out, err
	}
	machine, err := read("cat /etc/machine-id")
	if err != nil {
		return out, err
	}
	machineID := strings.TrimSpace(string(machine))
	if !machineIDPattern.MatchString(machineID) {
		return out, fmt.Errorf("invalid network safety machine identity")
	}
	keyEvidence := *identity
	probe := HostResources{Host: HostObservation{ObservedAt: time.Now().UTC(), Facts: HostFacts{MachineID: machineID, HostKey: &keyEvidence, NICs: nics}, Checks: []HostCheck{{ID: "host-key", Status: "pass"}, {ID: "network", Status: "pass"}}}, Network: inv}
	allowed := false
	for _, candidate := range NetworkCandidates(probe, probe.Host.ObservedAt) {
		if candidate.Name == host.NIC && candidate.Suggested {
			allowed = true
		}
	}
	if !allowed || owner != "unmanaged" || host.NIC == r.Dev {
		return out, fmt.Errorf("selected carrier owns host addresses, routes, child links, or persistent configuration; review a separate management path")
	}
	finalConfiguration, err := read(networkConfigurationCommand)
	if err != nil || strings.TrimSpace(string(finalConfiguration)) != strings.TrimSpace(string(configuration)) {
		return out, fmt.Errorf("persistent network configuration changed during inspection")
	}
	out = NetworkSafety{MachineID: machineID, ConfigurationSHA256: parts[0], Managers: canonicalManagers, Carrier: host.NIC, CarrierOwner: owner, Peer: peer.String(), LocalAddress: local.String(), ReturnRoute: SSHReturnRoute{Interface: r.Dev, Gateway: r.Gateway, Source: source, Table: string(r.Table)}, Inventory: inv, NICs: nics}
	return out, nil
}

func sameNetworkSafety(a, b NetworkSafety) bool { return reflect.DeepEqual(a, b) }

func parseNetworkManagers(body string) (map[string]bool, string, error) {
	allowed := map[string]bool{"systemd-networkd.service": true, "NetworkManager.service": true, "networking.service": true}
	active := map[string]bool{}
	canonical := []string{}
	for _, block := range strings.Split(strings.TrimSpace(body), "\n\n") {
		properties := map[string]string{}
		for _, line := range strings.Split(block, "\n") {
			k, v, ok := strings.Cut(line, "=")
			if !ok || (k != "Id" && k != "LoadState" && k != "ActiveState") {
				return nil, "", fmt.Errorf("invalid network manager state")
			}
			if _, duplicate := properties[k]; duplicate {
				return nil, "", fmt.Errorf("duplicate network manager property")
			}
			properties[k] = v
		}
		id, load, state := properties["Id"], properties["LoadState"], properties["ActiveState"]
		if len(properties) != 3 || !allowed[id] || (load != "loaded" && load != "not-found") || (state != "active" && state != "inactive") || (load == "not-found" && state != "inactive") {
			return nil, "", fmt.Errorf("unresolved network manager state")
		}
		if _, duplicate := active[id]; duplicate {
			return nil, "", fmt.Errorf("duplicate network manager")
		}
		active[id] = state == "active"
		canonical = append(canonical, id+"="+load+","+state)
	}
	if len(active) != 3 {
		return nil, "", fmt.Errorf("incomplete network manager state")
	}
	sort.Strings(canonical)
	return active, strings.Join(canonical, ";"), nil
}
func compactStrings(values []string) []string {
	out := values[:0]
	for _, value := range values {
		if len(out) == 0 || out[len(out)-1] != value {
			out = append(out, value)
		}
	}
	return out
}
