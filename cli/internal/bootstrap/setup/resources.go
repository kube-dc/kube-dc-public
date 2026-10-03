package setup

import (
	"context"
	"encoding/json"
	"fmt"
	"strings"
	"time"

	"github.com/shalb/kube-dc/cli/internal/bootstrap/clusterinit"
	"github.com/shalb/kube-dc/cli/internal/bootstrap/ports"
)

// ResourceRequest is deliberately independent of a complete setup spec. An
// operator must be able to inspect hardware before choosing network and storage.
type ResourceRequest struct {
	Host        Host
	Intent      clusterinit.Mode
	KVMRequired bool
	// PlatformInit observes a prepared RKE2 host. Existing RKE2 units and
	// listeners are expected; fresh raw-disk safety still follows Intent.
	PlatformInit bool
}

type DiskCandidate struct {
	Path         string
	SizeBytes    uint64
	WWN, Serial  string
	InUse        bool
	Model, Media string
	StableID     string
	Checked      bool
	Reason       string
}

type HostResources struct {
	Host             HostObservation
	Disks            []DiskCandidate
	DefaultRouteNICs []string
	Network          NetworkInventory
	// FileBackingAvailable qualifies only the default path and /dev/loop0.
	// It is an observation, not a reservation or permission to create storage.
	FileBackingAvailable bool
}

const diskListCommand = "sudo -n lsblk -J -T -b -p -o PATH,TYPE,SIZE,FSTYPE,PTTYPE,MOUNTPOINTS,RO,MAJ:MIN,WWN,SERIAL,MODEL,ROTA,TRAN"
const defaultRouteCommand = "ip -j route show default"
const loopListCommand = "sudo -n losetup --json --list --output NAME,BACK-FILE"
const backingStateCommand = "sudo -n sh -c 'if test -e /var/lib/ceph-osd-block.img || test -L /var/lib/ceph-osd-block.img || test -e /etc/systemd/system/ceph-loop-device.service || test -L /etc/systemd/system/ceph-loop-device.service; then printf occupied; else printf clear; fi'"

// DiscoverResources performs bounded, explicit, read-only discovery. Without
// an operator pin it returns identity evidence only. It never enrolls a key,
// chooses an interface, selects a disk, or permits installation.
func DiscoverResources(ctx context.Context, request ResourceRequest, ssh ports.CappedSSHHostKeyClient) (HostResources, error) {
	h := request.Host
	if _, err := ports.ParseSSHHostTarget(h.SSHAlias); ssh == nil || err != nil {
		return HostResources{}, fmt.Errorf("enter an SSH alias or [user@]host[:port]")
	}
	if h.HostKeySHA256 != "" && !ValidHostKeySHA256(h.HostKeySHA256) {
		return HostResources{}, fmt.Errorf("enter a SHA256: host fingerprint, not a public-key line")
	}
	if h.Disk != "" && !validSelectedDevicePath(h.Disk) {
		return HostResources{}, fmt.Errorf("selected disk must be a device path under /dev")
	}
	ctx, cancel := context.WithTimeout(ctx, hostProbeTimeout)
	defer cancel()
	endpoint := sshHost(h.SSHAlias)
	endpoint.ExpectedHostKeySHA256 = h.HostKeySHA256
	_, identity, err := ssh.RunCappedWithHostKey(ctx, endpoint, "printf KDC-IDENTITY", maxHostProbeOutput)
	if err != nil {
		return HostResources{}, err
	}
	if !ValidHostKeySHA256(identity.FingerprintSHA256) {
		return HostResources{}, fmt.Errorf("SSH adapter returned no verified host fingerprint")
	}
	if h.HostKeySHA256 != "" && identity.FingerprintSHA256 != h.HostKeySHA256 {
		return HostResources{}, fmt.Errorf("host fingerprint does not match the expected value")
	}
	if h.HostKeySHA256 == "" {
		o := HostObservation{ID: h.ID, Role: h.Role, SSHAlias: h.SSHAlias, State: "observed", ObservedAt: time.Now().UTC(), Facts: HostFacts{HostKey: &identity}}
		addHostCheck(&o, "host-key", "unknown", "Verify this fingerprint outside this connection, enter it, then discover again.")
		return HostResources{Host: o}, nil
	}
	o := inspectHostWithPolicy(ctx, ssh, h, request.Intent, request.KVMRequired, request.PlatformInit)
	r := HostResources{Host: o}
	if o.Facts.HostKey == nil || *o.Facts.HostKey != identity {
		return r, fmt.Errorf("host identity changed during discovery")
	}
	for _, check := range o.Checks {
		if check.ID == "host-key" && check.Status != "pass" {
			return r, fmt.Errorf("host identity did not pass discovery")
		}
	}
	identityLost := false
	read := func(command string) ([]byte, error) {
		if identityLost {
			return nil, fmt.Errorf("discovery stopped after identity/read failure")
		}
		out, key, err := ssh.RunCappedWithHostKey(ctx, endpoint, command, maxHostProbeOutput)
		if err != nil {
			identityLost = true
			return nil, err
		}
		if key != identity || len(out) > maxHostProbeOutput {
			identityLost = true
			return nil, fmt.Errorf("identity changed or discovery output exceeded limit")
		}
		return out, nil
	}
	if out, err := read(diskListCommand); err != nil {
		addHostCheck(&r.Host, "disk-candidates", "blocked", safeHostText(err.Error()))
	} else if disks, err := parseDiskCandidates(out); err != nil {
		addHostCheck(&r.Host, "disk-candidates", "blocked", err.Error())
	} else {
		r.Disks = disks
		for i := range r.Disks {
			d := &r.Disks[i]
			if d.InUse {
				continue
			}
			observation := HostObservation{ID: h.ID}
			inspectSelectedDisk(&observation, func(id, command string) string {
				out, err := read(command)
				if err != nil {
					addHostCheck(&observation, id, "unknown", "Disk inspection failed. Discover this host again.")
					return ""
				}
				return strings.TrimSpace(string(out))
			}, d.Path, clusterinit.ModeInstall)
			d.Checked = hostCheckStatusValue(observation, "selected-disk") == "pass" && hostCheckStatusValue(observation, "selected-disk-identity") == "pass"
			for _, check := range observation.Checks {
				if check.Status != "pass" {
					d.Checked = false
					d.Reason = check.Detail
				}
			}
			if fact := observation.Facts.SelectedDisk; fact != nil {
				d.StableID = fact.StableID
				d.InUse = fact.ReadOnly || fact.HasChildren || fact.HasMounts || fact.HasFilesystem || fact.HasPartitionTable || fact.HolderCount > 0 || fact.SignatureCount > 0
				// The list and detailed probe must describe the same device.
				if fact.SizeBytes != d.SizeBytes || fact.WWN != d.WWN || fact.Serial != d.Serial || fact.StableID == "" {
					d.Checked = false
					if d.Reason == "" {
						d.Reason = "Disk identity is missing or changed during discovery."
					}
				}
			} else {
				d.Checked = false
			}
		}
	}
	if out, err := read(loopListCommand); err == nil {
		var loops struct {
			Devices []struct {
				Name string `json:"name"`
			} `json:"loopdevices"`
		}
		if json.Unmarshal(out, &loops) == nil && loops.Devices != nil {
			free := true
			for _, loop := range loops.Devices {
				if loop.Name == "/dev/loop0" || !isLoopSelectedDevice(loop.Name) {
					free = false
				}
			}
			if free {
				state, err := read(backingStateCommand)
				r.FileBackingAvailable = err == nil && string(state) == "clear"
			}
		}
	}
	if out, err := read(defaultRouteCommand); err != nil {
		addHostCheck(&r.Host, "network-routes", "blocked", safeHostText(err.Error()))
	} else {
		var routes []struct {
			Dev string `json:"dev"`
		}
		if err := json.Unmarshal(out, &routes); err != nil {
			addHostCheck(&r.Host, "network-routes", "blocked", "invalid default route inventory")
		} else {
			for _, route := range routes {
				if nicNamePattern.MatchString(route.Dev) {
					r.DefaultRouteNICs = append(r.DefaultRouteNICs, route.Dev)
				}
			}
		}
	}
	if inventory, err := inspectNetworkInventory(read); err != nil {
		addHostCheck(&r.Host, "network-inventory", "unknown", safeHostText(err.Error()))
	} else {
		r.Network = inventory
	}
	if identityLost {
		r.FileBackingAvailable = false
		for i := range r.Disks {
			r.Disks[i].Checked = false
			r.Disks[i].Reason = "Discovery did not complete with a consistent host identity. Inspect again."
		}
		return r, fmt.Errorf("host discovery stopped after an identity or read failure")
	}
	return r, nil
}

func parseDiskCandidates(out []byte) ([]DiskCandidate, error) {
	var raw struct {
		BlockDevices []rawDiskNode `json:"blockdevices"`
	}
	if err := json.Unmarshal(out, &raw); err != nil || raw.BlockDevices == nil {
		return nil, fmt.Errorf("invalid disk inventory")
	}
	if len(raw.BlockDevices) > 64 {
		return nil, fmt.Errorf("disk inventory exceeds 64 devices; inspect the storage host separately")
	}
	var disks []DiskCandidate
	for _, node := range raw.BlockDevices {
		if node.Type != "disk" {
			continue
		}
		if !validSelectedDevicePath(node.Path) {
			return nil, fmt.Errorf("invalid disk candidate path")
		}
		one, _ := json.Marshal(struct {
			BlockDevices []rawDiskNode `json:"blockdevices"`
		}{[]rawDiskNode{node}})
		fact, err := parseSelectedDisk(string(one), node.Path)
		if err != nil {
			return nil, fmt.Errorf("invalid disk candidate: %w", err)
		}
		media := "unknown media"
		if string(node.Rotational) == "true" || string(node.Rotational) == "1" {
			media = "HDD"
		}
		if string(node.Rotational) == "false" || string(node.Rotational) == "0" {
			media = "SSD"
		}
		if node.Transport == "nvme" {
			media = "NVMe"
		}
		inUse := fact.ReadOnly || fact.HasChildren || fact.HasMounts || fact.HasFilesystem || fact.HasPartitionTable
		reason := "Needs identity, holder, and signature checks."
		if inUse {
			reason = "Reserved: mounted, partitioned, formatted, or read-only. No changes proposed."
		}
		disks = append(disks, DiskCandidate{Path: node.Path, SizeBytes: fact.SizeBytes, WWN: fact.WWN, Serial: fact.Serial, InUse: inUse, Model: safeHostText(node.Model), Media: media, Reason: reason})
	}
	return disks, nil
}
