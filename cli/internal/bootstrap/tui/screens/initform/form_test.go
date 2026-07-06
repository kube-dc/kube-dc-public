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

// TestBuild_ConstructsWithoutPanic — the form wires ~10 groups with
// hide-funcs over shared state; construction must be panic-free and
// the hint must land in the object-storage description.
func TestBuild_ConstructsWithoutPanic(t *testing.T) {
	st := baseState()
	f := Build(st, "template sibling eu/dc1 uses rook-ceph-pvc")
	if f == nil {
		t.Fatal("nil form")
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
