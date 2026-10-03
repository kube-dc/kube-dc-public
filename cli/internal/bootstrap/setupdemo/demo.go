// Package setupdemo provides synthetic operations for the real bootstrap UI.
// It has no production adapters and owns only temporary fixture files.
package setupdemo

import (
	"context"
	"crypto/sha256"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"sync"
	"time"

	"github.com/shalb/kube-dc/cli/internal/bootstrap/clusterinit"
	"github.com/shalb/kube-dc/cli/internal/bootstrap/ports"
	"github.com/shalb/kube-dc/cli/internal/bootstrap/setup"
)

const demoDigest = "aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa"
const HostKey = "SHA256:AAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAA"

type Review struct {
	Plan      setup.Preview
	Readiness setup.ReadinessReport
	CanRun    bool
}

type Coordinator struct {
	dir string
	mu  sync.Mutex
}

func NewCoordinator() (*Coordinator, error) {
	dir, err := os.MkdirTemp("", "kube-dc-setup-demo-*")
	if err != nil {
		return nil, err
	}
	return &Coordinator{dir: dir}, nil
}

func (c *Coordinator) Close() error { c.mu.Lock(); defer c.mu.Unlock(); return os.RemoveAll(c.dir) }

func (c *Coordinator) Check(ctx context.Context, o *clusterinit.InitOptions, selected setup.Host, scenario string) (Review, error) {
	c.mu.Lock()
	defer c.mu.Unlock()
	if err := ctx.Err(); err != nil {
		return Review{}, err
	}
	if scenario != "success" && scenario != "blocked" && scenario != "failed" && scenario != "stopped" {
		return Review{}, fmt.Errorf("unknown demo scenario")
	}
	configPath := filepath.Join(c.dir, "cluster.env")
	if err := clusterinit.WriteSpec(o, configPath); err != nil {
		return Review{}, err
	}
	releasePath := filepath.Join(c.dir, "release.json")
	spec := setup.Spec{
		SchemaVersion: setup.SchemaVersion, Name: o.Name, Profile: "evaluation@v1",
		Release:      setup.Release{RecordFile: releasePath, StarterRef: "oci://example.test/starter@sha256:" + demoDigest, RKE2Version: "v1.33.0+rke2r1"},
		Target:       setup.Target{Intent: clusterinit.ModeInstall},
		Hosts:        []setup.Host{selected},
		Platform:     setup.Platform{ConfigFile: configPath},
		Verification: setup.Verification{Capabilities: []string{"containers"}},
	}
	record := demoReleaseRecord(spec)
	data, err := json.Marshal(record)
	if err != nil {
		return Review{}, err
	}
	if err := os.WriteFile(releasePath, data, 0600); err != nil {
		return Review{}, err
	}
	compiled, err := setup.Compile(spec)
	if err != nil {
		return Review{}, err
	}
	plan, err := setup.BuildPreview(compiled)
	if err != nil {
		return Review{}, err
	}
	release, err := setup.InspectRelease(compiled)
	if err != nil {
		return Review{}, err
	}
	now := time.Now().UTC()
	keyStatus, keyDetail := "pass", "synthetic host key matches the demo draft"
	keyNext := ""
	switch {
	case selected.HostKeySHA256 == "":
		keyStatus, keyDetail = "unknown", "no host-key fingerprint in the demo draft"
		keyNext = "Enter a fingerprint to exercise the demo review gate."
	case selected.HostKeySHA256 != HostKey:
		keyStatus, keyDetail = "blocked", "draft fingerprint does not match the synthetic demo host"
		keyNext = "Restore the demo fingerprint or clear the field to test an unknown result."
	}
	hostState := "observed"
	if keyStatus == "blocked" {
		hostState = "blocked"
	}
	host := setup.HostObservation{ID: selected.ID, Role: "server", SSHAlias: selected.SSHAlias, State: hostState, ObservedAt: now,
		Facts: setup.HostFacts{HostKey: &ports.SSHHostKeyEvidence{Address: "192.0.2.10:22", Algorithm: "ssh-ed25519", FingerprintSHA256: HostKey}},
		Checks: []setup.HostCheck{{ID: "host-key", Scope: "host", Resource: selected.ID, Status: keyStatus, Detail: keyDetail,
			NextAction: keyNext, ObservedAt: now}}}
	if scenario == "blocked" {
		host.State = "blocked"
		host.Checks = append(host.Checks, setup.HostCheck{ID: "capacity", Scope: "host", Resource: selected.ID,
			Status: "blocked", Detail: "simulated host has too little free space", NextAction: "Choose a different scenario in Hosts.", ObservedAt: now})
	}
	hosts := setup.HostInventory{SchemaVersion: setup.HostInventorySchemaVersion, State: host.State, InputHash: compiled.InputHash,
		Hosts: []setup.HostObservation{host}, Unresolved: []string{"real-host-inventory-not-run"}}
	tools := []setup.ToolFact{{Name: "kubectl", Status: "installed", Reason: "simulated workstation"}, {Name: "helm", Status: "installed", Reason: "simulated workstation"}}
	readiness, err := setup.BuildReadiness(compiled, plan, release, tools, &hosts)
	if err != nil {
		return Review{}, err
	}
	return Review{Plan: plan, Readiness: readiness, CanRun: readiness.BlockedCount == 0 && readiness.PendingCount == 0}, nil
}

func (c *Coordinator) Run(ctx context.Context, scenario string) (string, error) {
	if err := ctx.Err(); err != nil {
		return "", err
	}
	switch scenario {
	case "success":
		return "Ready — simulated installation and verification passed", nil
	case "failed":
		return "Failed — simulated verification failed; inspect checks and retry", nil
	case "stopped":
		return "Stopped — simulated run ended before verification; continue after review", nil
	default:
		return "", fmt.Errorf("simulated installation is blocked")
	}
}

func demoReleaseRecord(s setup.Spec) setup.InstallerReleaseRecord {
	image := func(name string) string { return "example.test/" + name + "@sha256:" + demoDigest }
	return setup.InstallerReleaseRecord{
		SchemaVersion: setup.ReleaseRecordSchemaVersion, Kind: "kube-dc-installer-release", ReleaseID: "demo-only-synthetic",
		Artifacts: setup.ReleaseArtifacts{
			CLI: setup.ReleaseFile{Version: "demo", SHA256: demoDigest}, StarterRef: s.Release.StarterRef, RKE2Version: s.Release.RKE2Version,
			PlatformChart: setup.ReleaseFile{Version: "demo", SHA256: demoDigest}, BackendImage: image("backend"),
			FrontendImage: image("frontend"), AdminImage: image("admin"),
			ThemeArchives: map[string]string{"kube-dc-theme.jar": demoDigest, "kube-dc-theme-provider.jar": demoDigest},
		},
		Profiles: []setup.ReleaseProfile{{ID: s.Profile, SupportedOS: []string{"ubuntu:24.04"}, Architectures: []string{"x86_64"},
			MinServers: 1, MaxServers: 1, MinAgents: 0, MaxAgents: 0, MinCPUPerHost: 4, MinMemoryKiBPerHost: 8 << 20,
			MinFreeBytesPerHost: 80 << 30, NetworkPresets: []string{"internal-only"}, ObjectStorageModes: []string{"rook-ceph-multi-node", "rook-ceph-local"},
			RequiredTools: []string{"kubectl", "helm"}, RequiredCapabilities: []string{"containers"},
			AcceptanceChecks: []string{"platform-ready"}, Availability: "Demo only; no host failover.",
			Qualification: setup.ReleaseQualification{RunID: "demo-only", Result: "passed", EvidenceSHA256: demoDigest}}},
	}
}

// Discover uses the same resource contract as SSH discovery, with fixed facts.
func (c *Coordinator) Discover(ctx context.Context, request setup.ResourceRequest) (setup.HostResources, error) {
	if err := ctx.Err(); err != nil {
		return setup.HostResources{}, err
	}
	h := request.Host
	key := &ports.SSHHostKeyEvidence{Address: "192.0.2.10:22", Algorithm: "ssh-ed25519", FingerprintSHA256: HostKey}
	obs := setup.HostObservation{ID: h.ID, Role: h.Role, SSHAlias: h.SSHAlias, State: "observed", ObservedAt: time.Now().UTC(), Facts: setup.HostFacts{HostKey: key}}
	if h.HostKeySHA256 != HostKey {
		status := "unknown"
		if h.HostKeySHA256 != "" {
			status = "blocked"
			obs.State = "blocked"
		}
		obs.Checks = []setup.HostCheck{{ID: "host-key", Status: status, Detail: "Use the synthetic SHA256 fingerprint to discover demo resources.", NextAction: "Restore the demo fingerprint in Hosts."}}
		return setup.HostResources{Host: obs}, nil
	}
	machine := sha256.Sum256([]byte(h.SSHAlias))
	obs.Facts.MachineID = fmt.Sprintf("%x", machine[:16])
	wwn := fmt.Sprintf("0x%x", machine[:8])
	obs.Facts.OSID, obs.Facts.OSVersion, obs.Facts.Architecture = "ubuntu", "24.04", "x86_64"
	obs.Facts.CPUs, obs.Facts.MemoryKiB, obs.Facts.FreeBytes = 8, 16<<20, 100<<30
	obs.Facts.NICs = []setup.NICFact{{Name: "eth0", MTU: 1500, MAC: "02:00:00:00:00:10", Addresses: []string{"192.0.2.10"}}, {Name: "eth1", MTU: 1500, MAC: "02:00:00:00:00:11"}}
	obs.Checks = []setup.HostCheck{{ID: "host-key", Status: "pass", Detail: "Synthetic identity matches."}, {ID: "network", Status: "pass", Detail: "Synthetic interfaces observed."}}
	network := setup.NetworkInventory{Complete: true, RouteNICs: []string{"eth0"}, Links: []setup.NetworkLink{
		{Name: "eth0", Kind: "ether", MAC: "02:00:00:00:00:10", MTU: 1500, State: "UP", Up: true},
		{Name: "eth1", Kind: "ether", MAC: "02:00:00:00:00:11", MTU: 1500, State: "UP", Up: true},
	}}
	if h.Disk != "" {
		obs.Facts.SelectedDisk = &setup.DiskFact{SelectedPath: h.Disk, ResolvedPath: h.Disk, WWN: wwn, SizeBytes: 100 << 30}
		obs.Checks = append(obs.Checks, setup.HostCheck{ID: "selected-disk", Status: "pass", Detail: "Synthetic disk inspection only."})
	}
	return setup.HostResources{Host: obs, Network: network, FileBackingAvailable: true, DefaultRouteNICs: []string{"eth0"}, Disks: []setup.DiskCandidate{{Path: "/dev/sda", SizeBytes: 100 << 30, InUse: true, Media: "SSD", Model: "Demo system disk", Reason: "System disk; reserved."}, {Path: "/dev/sdb", SizeBytes: 100 << 30, WWN: wwn, StableID: "wwn:" + wwn, Checked: true, Media: "SSD", Model: "Demo storage disk"}}}, nil
}
