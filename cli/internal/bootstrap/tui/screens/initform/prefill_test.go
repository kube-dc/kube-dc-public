package initform

import (
	"testing"

	"github.com/shalb/kube-dc/cli/internal/bootstrap/clusterinit"
)

// FromOptions must seed the whole panel State from a (prefilled)
// InitOptions — the inverse of Apply — so `init --config` / KUBE_DC_INIT_*
// env / flags open the wizard PRE-FILLED across every section.
func TestFromOptions_SeedsPanelState(t *testing.T) {
	o := &clusterinit.InitOptions{
		Name: "dc1", Domain: "kdc.example.com", NodeExternalIP: "203.0.113.10",
		Email: "ops@example.com", Mode: clusterinit.ModeInstall, FleetMode: clusterinit.FleetNewRepo,
		Preset: clusterinit.PresetInternalOnly, GitHubOwner: "acme", GitHubRepo: "acme-fleet",
		SSHHost: "ubuntu@203.0.113.10", RookMode: clusterinit.RookCephLocal, RookOSDNode: "dc1-master-1",
		RookOSDSizeGB: 40, AllowNoKubevirtEligible: true,
		Sets: map[string]string{"EXT_NET_INTERFACE": "enp1s0", "KUBE_OVN_MASTER_NODES": "10.0.0.5", "EXT_NET_VLAN_ID": "0"},
	}
	var s State
	s.FromOptions(o)

	if s.Name != "dc1" || s.Domain != "kdc.example.com" || s.NodeIP != "203.0.113.10" || s.Email != "ops@example.com" {
		t.Errorf("identity not seeded: %+v", s)
	}
	if s.NetInterface != "enp1s0" || s.KubeOVNMasterNodes != "10.0.0.5" || s.NetVLANID != "0" {
		t.Errorf("network overlay not seeded: %+v", s)
	}
	if s.OSMode != "rook-ceph-local" || s.OSDNode != "dc1-master-1" || s.OSDSizeGB != "40" {
		t.Errorf("storage not seeded: %+v", s)
	}
	if s.Mode != "install" || s.FleetMode != "new-repo" || s.Preset != "internal-only" ||
		s.Owner != "acme" || s.RepoName != "acme-fleet" || s.SSHHost != "ubuntu@203.0.113.10" {
		t.Errorf("orchestration not seeded: %+v", s)
	}
	if !s.AllowNoKubevirtEligible {
		t.Error("no-KVM gate not seeded")
	}

	// FromOptions then Apply preserves the input surface (round-trip through
	// the panel is lossless).
	o2 := &clusterinit.InitOptions{}
	if err := s.Apply(o2); err != nil {
		t.Fatalf("Apply after FromOptions: %v", err)
	}
	if o2.Name != o.Name || o2.RookOSDSizeGB != 40 || o2.Sets["EXT_NET_INTERFACE"] != "enp1s0" ||
		!o2.AllowNoKubevirtEligible {
		t.Errorf("FromOptions→Apply lost data: %+v", o2)
	}
}

// P2 regression: a disabled-mode draft WITHOUT consent — Apply still
// returns the consent error (gate kept), but must populate every field
// FIRST so the panel's 'S' save-draft (which ignores the error) captures
// gates + identity rather than dropping them.
func TestApply_DisabledDraftStillPopulatesFields(t *testing.T) {
	st := &State{
		Name: "dc1", Domain: "kdc.example.com", NodeIP: "203.0.113.10",
		Email: "ops@example.com", Mode: "install", OSMode: "disabled",
		AllowDNSNotReady: true, AllowNoKubevirtEligible: true,
	}
	o := &clusterinit.InitOptions{}
	if err := st.Apply(o); err == nil {
		t.Fatal("disabled without consent must still return the consent error")
	}
	if o.Name != "dc1" || o.NodeExternalIP != "203.0.113.10" {
		t.Errorf("identity not populated despite the consent error: %+v", o)
	}
	if !o.AllowDNSNotReady || !o.AllowNoKubevirtEligible {
		t.Errorf("gates dropped despite the consent error — a draft would lose them: %+v", o)
	}
}

// The load-bearing "host all variants" guarantee: a prefilled/cloned
// InitOptions with dedicated-field keys AND advanced overlay keys survives
// the o → State → Apply → o round-trip losslessly. Dedicated pointers
// (gw-nodes/gw-type/replication) land in fields; everything else rides
// ExtraSets; nothing is dropped.
func TestExtraSets_PanelRoundTripLossless(t *testing.T) {
	o := &clusterinit.InitOptions{
		Name: "dc1", Domain: "kdc.example.com", NodeExternalIP: "203.0.113.10", Email: "ops@example.com",
		Mode: clusterinit.ModeInstall, FleetMode: clusterinit.FleetNewRepo, Preset: clusterinit.PresetCloudVLAN,
		RookMode: clusterinit.RookCephLocal, RookOSDNode: "dc1-m1", RookOSDSizeGB: 40,
		Sets: map[string]string{
			// field-backed:
			"EXT_NET_INTERFACE": "bond0", "KUBE_OVN_MASTER_NODES": "10.0.0.5",
			"KUBE_OVN_GW_NODES": "m1,w1", "KUBE_OVN_GW_TYPE": "centralized", "CEPH_REPLICATION_SIZE": "2",
			// advanced (no dedicated field) — must survive via ExtraSets:
			"METALLB_INTERFACE": "bond0", "EXT_NET_ANCHOR_IPS": "m1=203.0.113.0/24",
			"EXT_NET_MTU": "1400", "SMTP_HOST": "smtp.example.com", "SYSTEM_QUOTA_MIMIR_BLOCKS": "50Gi",
		},
	}
	var s State
	s.FromOptions(o)

	if s.GWNodes != "m1,w1" || s.GWType != "centralized" || s.CephReplicationSize != "2" {
		t.Errorf("pointer fields not seeded: gw=%q type=%q repl=%q", s.GWNodes, s.GWType, s.CephReplicationSize)
	}
	for _, k := range []string{"METALLB_INTERFACE", "EXT_NET_ANCHOR_IPS", "EXT_NET_MTU", "SMTP_HOST", "SYSTEM_QUOTA_MIMIR_BLOCKS"} {
		if s.ExtraSets[k] != o.Sets[k] {
			t.Errorf("advanced key %q not preserved in ExtraSets (got %q)", k, s.ExtraSets[k])
		}
	}
	if _, dup := s.ExtraSets["KUBE_OVN_GW_NODES"]; dup {
		t.Error("field-backed key must not also land in ExtraSets")
	}

	o2 := &clusterinit.InitOptions{}
	if err := s.Apply(o2); err != nil {
		t.Fatalf("Apply: %v", err)
	}
	for k, v := range o.Sets {
		if o2.Sets[k] != v {
			t.Errorf("panel round-trip LOST %q: got %q want %q", k, o2.Sets[k], v)
		}
	}
}

// P1 regression: Apply is a REPLACEMENT model — editing/clearing a field
// or switching a mode must not leak the prefilled value. Clone a rich
// config, seed State, edit it, Apply back into the SAME o, assert stale
// values are gone.
func TestApply_ReplacementNoStaleLeak(t *testing.T) {
	o := &clusterinit.InitOptions{
		Name: "dc1", Domain: "kdc.example.com", NodeExternalIP: "203.0.113.10", Email: "ops@example.com",
		Mode: clusterinit.ModeInstall, FleetMode: clusterinit.FleetNewRepo,
		Preset:    clusterinit.PresetCloudPublicVLAN,
		RookMode:  clusterinit.RookCephMultiNode,
		CephNodes: map[string]string{"m1": "sdb", "w1": "sdb", "w2": "sdb"},
		Sets: map[string]string{
			"EXT_NET_INTERFACE": "bond0", "KUBE_OVN_MASTER_NODES": "10.0.0.5",
			"KUBE_OVN_GW_NODES": "m1,w1,w2", "KUBE_OVN_GW_TYPE": "centralized",
			"CEPH_REPLICATION_SIZE": "3",
			"EXT_PUBLIC_VLAN_ID":    "200", "EXT_PUBLIC_CIDR": "198.51.100.0/28", "EXT_PUBLIC_GATEWAY": "198.51.100.1",
			"METALLB_INTERFACE": "bond0", // preserved advanced (ExtraSets)
		},
	}
	var s State
	s.FromOptions(o)

	// Operator edits in the panel:
	s.GWNodes = ""                               // cleared
	s.CephReplicationSize = ""                   // cleared
	s.OSMode = string(clusterinit.RookCephLocal) // multi-node → local
	s.OSDNode = "dc1-m1"
	s.Preset = string(clusterinit.PresetInternalOnly) // away from public
	delete(s.ExtraSets, "METALLB_INTERFACE")          // dropped an advanced key

	if err := s.Apply(o); err != nil {
		t.Fatalf("Apply: %v", err)
	}

	for _, k := range []string{
		"KUBE_OVN_GW_NODES", "CEPH_REPLICATION_SIZE", "METALLB_INTERFACE",
		"EXT_PUBLIC_VLAN_ID", "EXT_PUBLIC_CIDR", "EXT_PUBLIC_GATEWAY",
	} {
		if _, leaked := o.Sets[k]; leaked {
			t.Errorf("stale %q survived an edit/switch (got %q)", k, o.Sets[k])
		}
	}
	if len(o.CephNodes) != 0 {
		t.Errorf("stale CephNodes survived multi-node → local switch: %v", o.CephNodes)
	}
	// Kept / new values are correct:
	if o.RookMode != clusterinit.RookCephLocal || o.RookOSDNode != "dc1-m1" {
		t.Errorf("local mode not applied: mode=%q node=%q", o.RookMode, o.RookOSDNode)
	}
	if o.Sets["EXT_NET_INTERFACE"] != "bond0" || o.Sets["KUBE_OVN_GW_TYPE"] != "centralized" {
		t.Errorf("un-edited fields lost: %v", o.Sets)
	}
}

// P2 regression: rook-ceph-pvc OSD count/size (InitOptions-only before)
// now have State fields, so they survive the draft-save path (State →
// Apply into a FRESH scratch, as saveDraft does).
func TestApply_PVCSizingSurvivesDraftPath(t *testing.T) {
	o := &clusterinit.InitOptions{
		Name: "dc1", Domain: "kdc.example.com", NodeExternalIP: "203.0.113.10", Email: "ops@example.com",
		Mode: clusterinit.ModeInstall, FleetMode: clusterinit.FleetNewRepo, Preset: clusterinit.PresetCloudVLAN,
		RookMode: clusterinit.RookCephPVC, CephStorageClass: "fast-nvme", CephOSDCount: 4, CephOSDVolumeSizeGB: 300,
	}
	var s State
	s.FromOptions(o)
	if s.CephOSDCount != "4" || s.CephOSDVolumeSize != "300" {
		t.Fatalf("pvc sizing not seeded into State: count=%q size=%q", s.CephOSDCount, s.CephOSDVolumeSize)
	}
	scratch := &clusterinit.InitOptions{} // fresh — the saveDraft path
	if err := s.Apply(scratch); err != nil {
		t.Fatalf("Apply: %v", err)
	}
	if scratch.CephOSDCount != 4 || scratch.CephOSDVolumeSizeGB != 300 || scratch.CephStorageClass != "fast-nvme" {
		t.Errorf("pvc sizing lost on draft path: count=%d size=%d class=%q",
			scratch.CephOSDCount, scratch.CephOSDVolumeSizeGB, scratch.CephStorageClass)
	}
}

// initialState overlays a prefill onto the first-time defaults: prefilled
// values win; unspecified ones keep the defaults (mode=install, provider,
// fleet-mode=new-repo).
func TestInitialState_PrefillOverlaysDefaults(t *testing.T) {
	o := &clusterinit.InitOptions{Name: "dc1", Preset: clusterinit.PresetInternalOnly}
	st := initialState(o)
	if st.Name != "dc1" {
		t.Errorf("prefill name lost: %q", st.Name)
	}
	if st.Preset != "internal-only" {
		t.Errorf("prefill preset should win over the cloud-vlan default: %q", st.Preset)
	}
	if st.Mode != "install" {
		t.Errorf("default Mode lost: %q", st.Mode)
	}
	if st.FleetMode != "new-repo" {
		t.Errorf("first-time default FleetMode lost: %q", st.FleetMode)
	}
	if st.Provider != "github" {
		t.Errorf("default Provider lost: %q", st.Provider)
	}
}
