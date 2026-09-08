package accept

import "testing"

func TestParseKernelVLANs(t *testing.T) {
	out := `24: bond0.1101@bond0: <BROADCAST,MULTICAST,UP,LOWER_UP> mtu 1500 qdisc noqueue state UP mode DEFAULT group default qlen 1000\    link/ether ce:19:20:82:3d:6d brd ff:ff:ff:ff:ff:ff promiscuity 0 minmtu 0 maxmtu 65535 \    vlan protocol 802.1Q id 1101 <REORDER_HDR> numtxqueues 1
25: bond0.1103@bond0: <BROADCAST,MULTICAST,UP,LOWER_UP> mtu 1500 qdisc noqueue state UP mode DEFAULT group default qlen 1000\    link/ether ce:19:20:82:3d:6d brd ff:ff:ff:ff:ff:ff promiscuity 0 minmtu 0 maxmtu 65535 \    vlan protocol 802.1Q id 1103 <REORDER_HDR>
26: eth0.55@eth0: <BROADCAST> mtu 1500 qdisc noop state DOWN mode DEFAULT group default qlen 1000\    link/ether aa:bb:cc:dd:ee:ff brd ff:ff:ff:ff:ff:ff promiscuity 0 vlan id 55 <REORDER_HDR>
garbage line without a vlan
`
	got := parseKernelVLANs(out)
	want := [][3]string{
		{"bond0.1101", "bond0", "1101"},
		{"bond0.1103", "bond0", "1103"},
		{"eth0.55", "eth0", "55"},
	}
	if len(got) != len(want) {
		t.Fatalf("got %d entries, want %d: %v", len(got), len(want), got)
	}
	for i := range want {
		if got[i] != want[i] {
			t.Errorf("entry %d: got %v, want %v", i, got[i], want[i])
		}
	}
}

func TestParseKernelVLANsEmpty(t *testing.T) {
	if got := parseKernelVLANs(""); len(got) != 0 {
		t.Fatalf("empty input parsed to %v", got)
	}
}

func TestParseAnchorMap(t *testing.T) {
	m, bad := parseAnchorMap("master-1=193.182.142.4/24, master-2=193.182.142.5/24")
	if len(bad) != 0 || len(m) != 2 {
		t.Fatalf("got m=%v bad=%v", m, bad)
	}
	if m["master-1"] != "193.182.142.4/24" || m["master-2"] != "193.182.142.5/24" {
		t.Fatalf("CIDRs not retained: %v", m)
	}
}

func TestParseAnchorMapMalformed(t *testing.T) {
	// A silently dropped entry would exempt that node from verification —
	// malformed and duplicate pairs must be reported, not ignored.
	_, bad := parseAnchorMap("broken,x=,=1.2.3.4/8,n1=1.2.3.4,n2=5.6.7.8/24,n2=5.6.7.9/24")
	if len(bad) != 5 {
		t.Fatalf("want 5 bad entries (incl. bare-IP and duplicate), got %v", bad)
	}
}

func TestBuildOVNVLANsDuplicateIDKeepsBothProviders(t *testing.T) {
	vlans := []map[string]any{
		{"metadata": map[string]any{"name": "a-100"}, "spec": map[string]any{"id": int64(100), "provider": "pn-a"}},
		{"metadata": map[string]any{"name": "b-100"}, "spec": map[string]any{"id": int64(100), "provider": "pn-b"}},
	}
	pns := []map[string]any{
		{"metadata": map[string]any{"name": "pn-a"}, "spec": map[string]any{"defaultInterface": "bond0"}},
		{"metadata": map[string]any{"name": "pn-b"}, "spec": map[string]any{"defaultInterface": "bond1"}},
	}
	got := buildOVNVLANs(vlans, pns)
	if len(got["100"]) != 2 {
		t.Fatalf("want both providers kept for id 100, got %+v", got["100"])
	}
	hit := 0
	for _, v := range got["100"] {
		if v.parentMatches("bond1", "n1") {
			hit++
		}
	}
	if hit != 1 {
		t.Errorf("exactly the pn-b candidate must match bond1, got %d of %+v", hit, got["100"])
	}
}

func TestBuildOVNVLANsParentScoping(t *testing.T) {
	vlans := []map[string]any{
		{"metadata": map[string]any{"name": "vlan1101"}, "spec": map[string]any{"id": int64(1101), "provider": "ext-cloud"}},
		{"metadata": map[string]any{"name": "orphan"}, "spec": map[string]any{"id": int64(200), "provider": "unknown-pn"}},
	}
	pns := []map[string]any{
		{"metadata": map[string]any{"name": "ext-cloud"}, "spec": map[string]any{
			"defaultInterface": "bond0",
			"customInterfaces": []any{map[string]any{"interface": "eno3", "nodes": []any{"edge-1"}}},
		}},
	}
	got := buildOVNVLANs(vlans, pns)
	if len(got["1101"]) != 1 {
		t.Fatalf("want 1 candidate for 1101, got %+v", got["1101"])
	}
	v := got["1101"][0]
	if !v.parentMatches("bond0", "any-node") || !v.parentMatches("br-ext-cloud", "any-node") {
		t.Errorf("expected bond0/br-ext-cloud to match on any node: %+v", v)
	}
	// eno3 is node-scoped to edge-1 only.
	if !v.parentMatches("eno3", "edge-1") {
		t.Errorf("eno3 must match on its selected node edge-1")
	}
	if v.parentMatches("eno3", "other-node") {
		t.Errorf("eno3 must NOT match on a node its customInterfaces entry does not select")
	}
	if v.parentMatches("bond1", "any-node") {
		t.Errorf("bond1 must NOT match — VLAN ids are trunk-scoped")
	}
	// Unknown provider: conservative, match any parent.
	if len(got["200"]) != 1 || !got["200"][0].parentMatches("anything", "any-node") {
		t.Errorf("unknown provider should match any parent (fail closed)")
	}
}
