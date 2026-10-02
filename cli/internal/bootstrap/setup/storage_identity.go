package setup

import (
	"context"
	"fmt"
	"sort"
	"strings"

	"github.com/shalb/kube-dc/cli/internal/bootstrap/clusterinit"
	"github.com/shalb/kube-dc/cli/internal/bootstrap/ports"
)

type StorageIdentity struct {
	Node             string   `json:"node"`
	MachineID        string   `json:"machineId"`
	HostKeySHA256    string   `json:"hostKeySHA256"`
	Device           DiskFact `json:"device"`
	ProvisioningPath string   `json:"provisioningPath"`
}

// InspectStorageIdentities covers the actual consumer list, including devices
// imported through platform configuration. It binds a verified udev by-id path
// to every raw OSD. Rook consumes that path instead of a reusable kernel name.
func InspectStorageIdentities(ctx context.Context, c Compiled, ssh ports.CappedSSHHostKeyClient) ([]StorageIdentity, error) {
	if err := checkCompiled(c); err != nil {
		return nil, err
	}
	var out []StorageIdentity
	seen := map[string]bool{}
	for _, slot := range c.Init.ObjectStorage().RawOSDDevices() {
		var host Host
		for _, h := range c.Spec.Hosts {
			if h.ID == slot[0] {
				host = h
			}
		}
		if host.ID == "" || !ValidHostKeySHA256(host.HostKeySHA256) || ssh == nil {
			return nil, fmt.Errorf("raw storage node %s needs a selected pinned SSH host", slot[0])
		}
		entry, err := inspectStorageIdentity(ctx, host, "/dev/"+slot[1], ssh)
		if err != nil {
			return nil, fmt.Errorf("storage identity for %s: %w", host.ID, err)
		}
		key := entry.Device.WWN
		if key == "" {
			key = entry.Device.Serial
		}
		if seen[key] {
			return nil, fmt.Errorf("raw storage identities are duplicated")
		}
		seen[key] = true
		out = append(out, entry)
	}
	return out, nil
}

func inspectStorageIdentity(ctx context.Context, host Host, device string, ssh ports.CappedSSHHostKeyClient) (StorageIdentity, error) {
	var out StorageIdentity
	ctx, cancel := context.WithTimeout(ctx, hostProbeTimeout)
	defer cancel()
	endpoint := sshHost(host.SSHAlias)
	endpoint.ExpectedHostKeySHA256 = host.HostKeySHA256
	var identity *ports.SSHHostKeyEvidence
	failed := false
	read := func(_ string, command string) string {
		if failed || ctx.Err() != nil {
			failed = true
			return ""
		}
		body, key, err := ssh.RunCappedWithHostKey(ctx, endpoint, command, maxHostProbeOutput)
		if err != nil || len(body) > maxHostProbeOutput || key.FingerprintSHA256 != host.HostKeySHA256 || (identity != nil && *identity != key) {
			failed = true
			return ""
		}
		identity = &key
		return strings.TrimSpace(string(body))
	}
	machine := read("machine-identity", "cat /etc/machine-id")
	if !machineIDPattern.MatchString(machine) {
		return out, fmt.Errorf("machine identity is unavailable")
	}
	observation := HostObservation{ID: host.ID}
	inspectSelectedDisk(&observation, read, device, clusterinit.ModeInstall)
	if failed || hostCheckStatusValue(observation, "selected-disk") != "pass" || observation.Facts.SelectedDisk == nil {
		return out, fmt.Errorf("raw device is unreadable, changed, or occupied")
	}
	fact := *observation.Facts.SelectedDisk
	if fact.WWN == "" && fact.Serial == "" {
		return out, fmt.Errorf("raw device has no durable hardware identity")
	}
	aliases := read("selected-disk", "sudo -n udevadm info --query=symlink --name='"+fact.ResolvedPath+"' && printf '\\nKDC-UDEV-END\\n'")
	fields := strings.Fields(aliases)
	if failed || len(fields) == 0 || fields[len(fields)-1] != "KDC-UDEV-END" {
		return out, fmt.Errorf("raw device has no verified udev identity")
	}
	var choices []string
	for _, alias := range fields[:len(fields)-1] {
		path := "/dev/" + strings.TrimPrefix(alias, "/dev/")
		if strings.HasPrefix(path, "/dev/disk/by-id/") && validSelectedDevicePath(path) {
			choices = append(choices, path)
		}
	}
	sort.Strings(choices)
	if strings.HasPrefix(device, "/dev/disk/by-id/") {
		for i, path := range choices {
			if path == device {
				choices[0], choices[i] = choices[i], choices[0]
				break
			}
		}
	}
	if len(choices) == 0 {
		return out, fmt.Errorf("raw device has no stable by-id provisioning path")
	}
	path := choices[0]
	if resolved := read("selected-disk", "readlink -e -- '"+path+"'"); failed || resolved != fact.ResolvedPath {
		return out, fmt.Errorf("udev identity changed during inspection")
	}
	// Repeat the complete occupancy/identity read after resolving the alias.
	aliasObservation := HostObservation{ID: host.ID}
	inspectSelectedDisk(&aliasObservation, read, path, clusterinit.ModeInstall)
	current := aliasObservation.Facts.SelectedDisk
	if failed || hostCheckStatusValue(aliasObservation, "selected-disk") != "pass" || current == nil || current.WWN != fact.WWN || current.Serial != fact.Serial || current.SizeBytes != fact.SizeBytes || current.ResolvedPath != fact.ResolvedPath {
		return out, fmt.Errorf("raw device changed while resolving its durable identity")
	}
	out = StorageIdentity{Node: host.ID, MachineID: machine, HostKeySHA256: host.HostKeySHA256, Device: fact, ProvisioningPath: path}
	return out, nil
}

// BoundObjectStorage changes only the consumer selector. It refuses a missing
// or extra identity so another raw OSD cannot evade review through config import.
func BoundObjectStorage(spec clusterinit.ObjectStorageSpec, identities []StorageIdentity) (clusterinit.ObjectStorageSpec, error) {
	lookup := map[string]StorageIdentity{}
	for _, entry := range identities {
		if _, exists := lookup[entry.Node]; exists || !strings.HasPrefix(entry.ProvisioningPath, "/dev/disk/by-id/") || !validSelectedDevicePath(entry.ProvisioningPath) || entry.Device.SizeBytes == 0 || (entry.Device.WWN == "" && entry.Device.Serial == "") {
			return spec, fmt.Errorf("invalid reviewed raw storage identity")
		}
		lookup[entry.Node] = entry
	}
	bound := spec
	bound.CephNodes = map[string]string{}
	for node, dev := range spec.CephNodes {
		bound.CephNodes[node] = dev
	}
	raw := spec.RawOSDDevices()
	if len(raw) != len(lookup) {
		return spec, fmt.Errorf("reviewed storage identities do not cover the exact raw OSD list")
	}
	for _, slot := range raw {
		entry, ok := lookup[slot[0]]
		if !ok || entry.Device.SelectedPath != "/dev/"+slot[1] {
			return spec, fmt.Errorf("reviewed raw storage selection changed")
		}
		if bound.Mode == clusterinit.RookCephLocal {
			bound.OSDDevice = entry.ProvisioningPath
		} else {
			bound.CephNodes[slot[0]] = entry.ProvisioningPath
		}
	}
	return bound, nil
}
