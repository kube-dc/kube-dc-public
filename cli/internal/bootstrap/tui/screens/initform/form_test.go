package initform

import (
	"strings"
	"testing"

	"github.com/shalb/kube-dc/cli/internal/bootstrap/clusterinit"
)

// baseState fills the minimum the wizard would collect for a
// multi-node run.
func baseState() *State {
	return &State{
		Name: "atlantis", Domain: "kdc.atlantis.example.com",
		NodeIP: "203.0.113.52", Email: "ops@atlantis.example.com",
		Mode: "install", FleetMode: "existing-fleet", Repo: "/tmp/fleet",
		Provider: "github", Owner: "kube-dc", RepoName: "kube-dc-fleet",
		Preset:    "cloud+public-vlan",
		NetVLANID: "1103", NetInterface: "bond0",
		PubVLANID: "1100", PubCIDR: "203.0.113.48/29", PubGateway: "203.0.113.49",
		OSMode:    "rook-ceph-multi-node",
		CephNode1: "host5-a=sdb", CephNode2: "host6-a=sdb", CephNode3: "host7-a=sdc",
		AllowDNSNotReady: true,
	}
}

// TestApply_ThenValidatePasses — the thin-generator contract's core:
// what the wizard assembles must pass the SAME Validate() the flag
// path runs.
func TestApply_ThenValidatePasses(t *testing.T) {
	o := &clusterinit.InitOptions{Yes: true}
	if err := baseState().Apply(o); err != nil {
		t.Fatalf("Apply: %v", err)
	}
	if err := o.Validate(); err != nil {
		t.Fatalf("wizard-assembled options must pass Validate, got: %v", err)
	}
	if o.CephNodes["host7-a"] != "sdc" {
		t.Errorf("ceph nodes not mapped: %v", o.CephNodes)
	}
	if o.Sets["EXT_PUBLIC_CIDR"] != "203.0.113.48/29" {
		t.Errorf("public preset keys not mapped: %v", o.Sets)
	}
}

func TestApply_GPUThinGeneratorParity(t *testing.T) {
	st := baseState()
	st.GPUPlatform = "enabled"
	st.GPUDriverSource = "gpu-operator"
	st.GPUOperatorVersion = clusterinit.DefaultGPUOperatorVersion
	st.NVIDIADriverVersion = clusterinit.DefaultNVIDIADriverVersion
	st.NVIDIAToolkitVersion = clusterinit.DefaultNVIDIAToolkitVersion
	st.GPUNodeModes = "gpu-worker-b=vm-passthrough,gpu-worker-a=pod-hami"
	st.GPUProfiles = "nvidia-v100-hami,nvidia-v100-passthrough"
	st.HAMiEnabled = true
	st.HAMiVersion = clusterinit.DefaultHAMiVersion
	st.HAMiSchedulerVersion = clusterinit.DefaultHAMiSchedulerKubeVersion
	o := &clusterinit.InitOptions{Yes: true}
	if err := st.Apply(o); err != nil {
		t.Fatal(err)
	}
	if err := o.Validate(); err != nil {
		t.Fatalf("GPU panel options: %v", err)
	}
	flags := st.EquivalentFlags(o)
	for _, want := range []string{
		"--gpu-platform=enabled", "--gpu-node-mode=gpu-worker-a=pod-hami",
		"--gpu-node-mode=gpu-worker-b=vm-passthrough", "--hami-enabled",
		"--gpu-profile=nvidia-v100-hami",
	} {
		if !strings.Contains(flags, want) {
			t.Errorf("equivalent flags missing %q:\n%s", want, flags)
		}
	}
}

func TestApply_NonPublicPresetDropsPublicKeys(t *testing.T) {
	st := baseState()
	st.Preset = "cloud-vlan"
	o := &clusterinit.InitOptions{}
	if err := st.Apply(o); err != nil {
		t.Fatal(err)
	}
	if _, ok := o.Sets["EXT_PUBLIC_CIDR"]; ok {
		t.Error("EXT_PUBLIC_* must not leak into non-public presets")
	}
}

func TestApply_LocalModeFields(t *testing.T) {
	st := baseState()
	st.OSMode, st.OSDNode, st.OSDSizeGB, st.OSDDevice = "rook-ceph-local", "host6-a", "500", "sdb"
	st.CephNode1, st.CephNode2, st.CephNode3 = "", "", ""
	o := &clusterinit.InitOptions{Yes: true}
	if err := st.Apply(o); err != nil {
		t.Fatal(err)
	}
	if o.RookOSDNode != "host6-a" || o.RookOSDSizeGB != 500 || o.RookOSDDevice != "sdb" {
		t.Errorf("local fields not mapped: %+v", o)
	}
	if err := o.Validate(); err != nil {
		t.Fatalf("local wizard options must validate: %v", err)
	}
}

func TestApply_VMStorageMode_MappedWhenRook(t *testing.T) {
	st := baseState() // OSMode = rook-ceph-multi-node (rook-backed)
	st.VMStorageMode = "shared-rbd"
	o := &clusterinit.InitOptions{Yes: true}
	if err := st.Apply(o); err != nil {
		t.Fatal(err)
	}
	if o.VMStorageMode != clusterinit.VMStorageSharedRBD {
		t.Errorf("shared-rbd must map through with a rook object-storage mode, got %q", o.VMStorageMode)
	}
}

func TestApply_VMStorageMode_ClearedWhenNotRook(t *testing.T) {
	// The selector is hidden when object storage isn't rook-backed; a stale
	// non-local State value must be cleared so Validate can't fail on a field
	// the user can no longer see (review P2).
	st := baseState()
	st.OSMode = "disabled"
	st.CephNode1, st.CephNode2, st.CephNode3 = "", "", ""
	st.DisabledConsent = true
	st.VMStorageMode = "shared-rbd" // stale, from before switching to disabled
	o := &clusterinit.InitOptions{Yes: true}
	if err := st.Apply(o); err != nil {
		t.Fatal(err)
	}
	if o.VMStorageMode != "" {
		t.Errorf("stale VM mode must be cleared when object storage isn't rook-backed, got %q", o.VMStorageMode)
	}
	if err := o.Validate(); err != nil {
		t.Fatalf("cleared VM mode must validate (disabled + local): %v", err)
	}
}

func TestEquivalentFlags_RoundTripsTheSurface(t *testing.T) {
	o := &clusterinit.InitOptions{Yes: true}
	st := baseState()
	if err := st.Apply(o); err != nil {
		t.Fatal(err)
	}
	flags := st.EquivalentFlags(o)
	for _, want := range []string{
		"--name=atlantis",
		"--object-storage-mode=rook-ceph-multi-node",
		"--ceph-node=host5-a=sdb",
		"--ceph-node=host7-a=sdc",
		"--set=EXT_PUBLIC_CIDR=203.0.113.48/29",
		"--allow-dns-not-ready",
		"--dry-run",
	} {
		if !strings.Contains(flags, want) {
			t.Errorf("equivalent flags missing %q\nFULL:\n%s", want, flags)
		}
	}
}

// The "equivalent" command must be actually equivalent for rook-ceph-pvc
// sizing — --ceph-osd-count / --ceph-osd-volume-size-gb, not just the
// storage class (scriptability promise).
func TestEquivalentFlags_PVCSizing(t *testing.T) {
	st := baseState()
	st.OSMode = "rook-ceph-pvc"
	st.CephNode1, st.CephNode2, st.CephNode3 = "", "", ""
	st.StorageClass = "fast-nvme"
	st.CephOSDCount = "4"
	st.CephOSDVolumeSize = "300"
	o := &clusterinit.InitOptions{Yes: true}
	if err := st.Apply(o); err != nil {
		t.Fatal(err)
	}
	flags := st.EquivalentFlags(o)
	for _, want := range []string{
		"--object-storage-mode=rook-ceph-pvc",
		"--ceph-storage-class=fast-nvme",
		"--ceph-osd-count=4",
		"--ceph-osd-volume-size-gb=300",
	} {
		if !strings.Contains(flags, want) {
			t.Errorf("equivalent flags missing %q\nFULL:\n%s", want, flags)
		}
	}
}

// Reviewer P2: unconsented disabled must never survive Apply — the
// Confirm's Validate blocks it interactively; this guards
// programmatic State construction.
func TestApply_DisabledRequiresConsent(t *testing.T) {
	st := baseState()
	st.OSMode = "disabled"
	st.DisabledConsent = false
	if err := st.Apply(&clusterinit.InitOptions{}); err == nil {
		t.Fatal("disabled without consent must error")
	}
	st.DisabledConsent = true
	o := &clusterinit.InitOptions{Yes: true}
	if err := st.Apply(o); err != nil {
		t.Fatalf("consented disabled must apply: %v", err)
	}
	if o.RookMode != clusterinit.RookDisabled {
		t.Errorf("mode not mapped: %v", o.RookMode)
	}
}

// Adopt mode: the wizard maps the danger-confirm to
// o.AllowUnpinnedAdopt, and the assembled options still pass the SAME
// Validate() the flag path runs (thin-generator contract for adopt).
func TestApply_AdoptModeBypassMapsAndValidates(t *testing.T) {
	st := baseState()
	st.Mode = "adopt"
	st.AllowUnpinnedAdopt = true
	o := &clusterinit.InitOptions{Yes: true}
	if err := st.Apply(o); err != nil {
		t.Fatalf("Apply: %v", err)
	}
	if o.Mode != clusterinit.ModeAdopt {
		t.Errorf("mode not mapped: %v", o.Mode)
	}
	if !o.AllowUnpinnedAdopt {
		t.Error("adopt-mode bypass confirm should set o.AllowUnpinnedAdopt")
	}
	if err := o.Validate(); err != nil {
		t.Fatalf("adopt wizard options must pass Validate: %v", err)
	}
}

// The bypass only carries meaning in adopt mode — an install/resume run
// must never emit it (keeps the plan hash + equivalent flags honest even
// if the danger-confirm state leaks in from a reused State).
func TestApply_BypassIgnoredOutsideAdopt(t *testing.T) {
	st := baseState()
	st.Mode = "install"
	st.AllowUnpinnedAdopt = true // stale/leaked — must be ignored
	o := &clusterinit.InitOptions{Yes: true}
	if err := st.Apply(o); err != nil {
		t.Fatal(err)
	}
	if o.AllowUnpinnedAdopt {
		t.Error("--allow-unpinned-adopt must not survive a non-adopt mode")
	}
	if strings.Contains(st.EquivalentFlags(o), "--allow-unpinned-adopt") {
		t.Error("install-mode equivalent flags must not include --allow-unpinned-adopt")
	}
}

// C6 discipline: the equivalent command the wizard prints for an adopt
// run must itself be a valid, re-runnable invocation — round-trip the
// emitted flags back through Apply→Validate.
func TestEquivalentFlags_AdoptBypass_RoundTripValidates(t *testing.T) {
	st := baseState()
	st.Mode = "adopt"
	st.AllowUnpinnedAdopt = true
	o := &clusterinit.InitOptions{Yes: true}
	if err := st.Apply(o); err != nil {
		t.Fatal(err)
	}
	flags := st.EquivalentFlags(o)
	for _, want := range []string{"--mode=adopt", "--allow-unpinned-adopt"} {
		if !strings.Contains(flags, want) {
			t.Errorf("adopt equivalent flags missing %q\nFULL:\n%s", want, flags)
		}
	}
}

// TestApply_InternalOnlyE2EComplete proves the wizard now produces a
// COMPLETE, valid install config for the e2e internal-only + NAT scenario
// — matching the runbook's flag set (EXT_NET_* + KUBE_OVN_MASTER_NODES +
// SSH host + rook-ceph-local). Guards the two gaps just closed (SSH host,
// KUBE_OVN_MASTER_NODES) from regressing the wizard back to unusable.
func TestApply_InternalOnlyE2EComplete(t *testing.T) {
	st := &State{
		Name: "e2e", Domain: "e2e.kube-dc.cloud",
		NodeIP: "203.0.113.52", SSHHost: "ubuntu@203.0.113.52",
		Email: "ops@example.com", Mode: "install", FleetMode: "new-repo",
		Provider: "github", Owner: "kube-dc", RepoName: "e2e-fleet-r5",
		Preset:             "internal-only",
		NetVLANID:          "0",
		NetInterface:       "enp1s0",
		KubeOVNMasterNodes: "10.77.0.22",
		OSMode:             "rook-ceph-local",
		OSDNode:            "e2e-master-1", OSDSizeGB: "40",
	}
	o := &clusterinit.InitOptions{Yes: true}
	if err := st.Apply(o); err != nil {
		t.Fatalf("Apply: %v", err)
	}
	if err := o.Validate(); err != nil {
		t.Fatalf("Validate: %v", err)
	}
	if err := clusterinit.ValidatePresetRequiredKeys(o); err != nil {
		t.Fatalf("ValidatePresetRequiredKeys: %v", err)
	}
	// The three env keys the install can't come up without.
	for k, want := range map[string]string{
		"EXT_NET_INTERFACE":     "enp1s0",
		"EXT_NET_VLAN_ID":       "0",
		"KUBE_OVN_MASTER_NODES": "10.77.0.22",
	} {
		if o.Sets[k] != want {
			t.Errorf("Sets[%s]=%q, want %q", k, o.Sets[k], want)
		}
	}
	if o.SSHHost != "ubuntu@203.0.113.52" {
		t.Errorf("SSH host (NAT detection) not set: %q", o.SSHHost)
	}
	if o.RookMode != clusterinit.RookCephLocal || o.RookOSDNode != "e2e-master-1" {
		t.Errorf("object storage not mapped: mode=%q node=%q", o.RookMode, o.RookOSDNode)
	}
}

// The wizard's SSH-host field maps to o.SSHHost (enables NAT detection
// in the apply path) and round-trips into the equivalent flags — closing
// the gap where a wizard install couldn't handle a NAT / cloud-FIP node.
func TestApply_SSHHostMapsAndFlags(t *testing.T) {
	st := baseState()
	st.SSHHost = "ubuntu@203.0.113.52"
	o := &clusterinit.InitOptions{Yes: true}
	if err := st.Apply(o); err != nil {
		t.Fatal(err)
	}
	if o.SSHHost != "ubuntu@203.0.113.52" {
		t.Errorf("SSH host not mapped: %q", o.SSHHost)
	}
	if err := o.Validate(); err != nil {
		t.Fatalf("wizard options with SSH host must validate: %v", err)
	}
	if !strings.Contains(st.EquivalentFlags(o), "--ssh-host=ubuntu@203.0.113.52") {
		t.Errorf("equivalent flags should include --ssh-host:\n%s", st.EquivalentFlags(o))
	}
	// Empty SSH host → flag omitted (plain public-IP node).
	st.SSHHost = ""
	o2 := &clusterinit.InitOptions{Yes: true}
	_ = st.Apply(o2)
	if strings.Contains(st.EquivalentFlags(o2), "--ssh-host") {
		t.Error("empty SSH host must not emit --ssh-host")
	}
}

// The wizard's "Allow node without /dev/kvm" toggle maps to
// o.AllowNoKubevirtEligible and round-trips into the equivalent flags —
// the gate a first-time user on a nested/cloud VM (no /dev/kvm) needs to
// complete an install without dropping to the flag path.
func TestApply_AllowNoKubevirtEligibleMapsAndFlags(t *testing.T) {
	st := baseState()
	st.AllowNoKubevirtEligible = true
	o := &clusterinit.InitOptions{Yes: true}
	if err := st.Apply(o); err != nil {
		t.Fatal(err)
	}
	if !o.AllowNoKubevirtEligible {
		t.Error("AllowNoKubevirtEligible should map to InitOptions")
	}
	if !strings.Contains(st.EquivalentFlags(o), "--allow-no-kubevirt-eligible") {
		t.Errorf("equivalent flags should include --allow-no-kubevirt-eligible:\n%s", st.EquivalentFlags(o))
	}
	// Off → flag omitted (the normal path where the gate enforces).
	st.AllowNoKubevirtEligible = false
	o2 := &clusterinit.InitOptions{Yes: true}
	_ = st.Apply(o2)
	if strings.Contains(st.EquivalentFlags(o2), "--allow-no-kubevirt-eligible") {
		t.Error("flag must be omitted when the toggle is off")
	}
}

// initialState (the wizard's starting defaults) must serve the first-time
// user: no fleet yet → new-repo. An explicit --fleet-mode still wins.
func TestInitialState_FirstTimeDefaultsToNewRepo(t *testing.T) {
	st := initialState(&clusterinit.InitOptions{})
	if st.FleetMode != string(clusterinit.FleetNewRepo) {
		t.Errorf("first-time default FleetMode should be new-repo, got %q", st.FleetMode)
	}
	if st.Mode != string(clusterinit.ModeInstall) {
		t.Errorf("default Mode should be install, got %q", st.Mode)
	}
	st2 := initialState(&clusterinit.InitOptions{FleetMode: clusterinit.FleetExistingFleet})
	if st2.FleetMode != string(clusterinit.FleetExistingFleet) {
		t.Errorf("an explicit --fleet-mode must be preserved, got %q", st2.FleetMode)
	}
}

// Reviewer P3: the equivalent invocation must be paste-safe.
func TestEquivalentFlags_ShellQuoting(t *testing.T) {
	st := baseState()
	st.Repo = "/home/user/my fleet/repo" // space
	o := &clusterinit.InitOptions{Yes: true}
	if err := st.Apply(o); err != nil {
		t.Fatal(err)
	}
	flags := st.EquivalentFlags(o)
	if !strings.Contains(flags, "--repo='/home/user/my fleet/repo'") {
		t.Errorf("space-bearing path must be quoted:\n%s", flags)
	}
	// Safe values stay unquoted (readability).
	if strings.Contains(flags, "--name='") {
		t.Errorf("safe value needlessly quoted:\n%s", flags)
	}
}

func TestShellQuote_EmbeddedQuote(t *testing.T) {
	got := shellQuote("it's here")
	if got != `'it'\''s here'` {
		t.Errorf("embedded quote escaping wrong: %s", got)
	}
}
