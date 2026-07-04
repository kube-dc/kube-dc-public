package anchors

import (
	"strings"
	"testing"
)

func TestParseSSHHostMap_Empty(t *testing.T) {
	for _, in := range []string{"", "   ", "\t\n"} {
		got, err := ParseSSHHostMap(in)
		if err != nil {
			t.Errorf("empty %q: unexpected err %v", in, err)
		}
		if got != nil {
			t.Errorf("empty %q: expected nil, got %+v", in, got)
		}
	}
}

func TestParseSSHHostMap_Valid(t *testing.T) {
	got, err := ParseSSHHostMap("host5-a=203.0.113.52,host6-a=203.0.113.53,host7-a=host7.bastion.example.com")
	if err != nil {
		t.Fatalf("err: %v", err)
	}
	want := map[string]string{
		"host5-a": "203.0.113.52",
		"host6-a": "203.0.113.53",
		"host7-a": "host7.bastion.example.com",
	}
	if len(got) != len(want) {
		t.Fatalf("len=%d want %d", len(got), len(want))
	}
	for k, v := range want {
		if got[k] != v {
			t.Errorf("got[%q] = %q, want %q", k, got[k], v)
		}
	}
}

func TestParseSSHHostMap_Errors(t *testing.T) {
	cases := []struct {
		name, in, wantSub string
	}{
		{"missing equals", "host5-a", "missing '='"},
		{"empty node", "=1.2.3.4", "empty node"},
		{"empty host", "host5-a=", "empty host"},
		{"whitespace in host", "host5-a=foo bar", "whitespace or '='"},
		{"duplicate node", "host5-a=1.1.1.1,host5-a=2.2.2.2", "more than once"},
	}
	for _, tc := range cases {
		tc := tc
		t.Run(tc.name, func(t *testing.T) {
			_, err := ParseSSHHostMap(tc.in)
			if err == nil {
				t.Fatal("expected err, got nil")
			}
			if !strings.Contains(err.Error(), tc.wantSub) {
				t.Errorf("err %q missing %q", err.Error(), tc.wantSub)
			}
		})
	}
}

func TestParseSSHHostMapSlice(t *testing.T) {
	// Repeatable --ssh-host-map flag semantics.
	got, err := ParseSSHHostMapSlice([]string{
		"host5-a=203.0.113.52",
		"host6-a=203.0.113.53",
	})
	if err != nil {
		t.Fatalf("err: %v", err)
	}
	if got["host5-a"] != "203.0.113.52" || got["host6-a"] != "203.0.113.53" {
		t.Errorf("unexpected map: %+v", got)
	}
}

func TestParseSSHHostMapSlice_DuplicateAcrossElements(t *testing.T) {
	_, err := ParseSSHHostMapSlice([]string{
		"host5-a=1.1.1.1",
		"host5-a=2.2.2.2",
	})
	if err == nil || !strings.Contains(err.Error(), "across --ssh-host-map values") {
		t.Errorf("expected dup-across-flags error, got %v", err)
	}
}

func TestMergeSSHHostMaps_FlagOverridesFleet(t *testing.T) {
	fleet := map[string]string{
		"host5-a": "203.0.113.52",
		"host6-a": "203.0.113.53",
	}
	flag := map[string]string{
		"host5-a": "10.0.0.5", // override
		"host7-a": "10.0.0.7", // new
	}
	got := MergeSSHHostMaps(fleet, flag)
	want := map[string]string{
		"host5-a": "10.0.0.5",      // flag wins
		"host6-a": "203.0.113.53", // fleet preserved
		"host7-a": "10.0.0.7",      // flag-only
	}
	for k, v := range want {
		if got[k] != v {
			t.Errorf("got[%q] = %q, want %q", k, got[k], v)
		}
	}
}

func TestMergeSSHHostMaps_BothNil(t *testing.T) {
	got := MergeSSHHostMaps(nil, nil)
	if got == nil {
		t.Error("expected non-nil empty map for nil/nil inputs")
	}
	if len(got) != 0 {
		t.Errorf("expected empty map, got %+v", got)
	}
}

func TestNewHostResolver_Override(t *testing.T) {
	r := NewHostResolver(map[string]string{
		"host5-a": "203.0.113.52",
	})
	got := r("host5-a")
	if got.Alias != "host5-a" {
		t.Errorf("Alias=%q want %q", got.Alias, "host5-a")
	}
	if got.Hostname != "203.0.113.52" {
		t.Errorf("Hostname=%q want %q", got.Hostname, "203.0.113.52")
	}
}

func TestNewHostResolver_FallthroughLegacy(t *testing.T) {
	r := NewHostResolver(nil)
	got := r("host5-a")
	if got.Alias != "host5-a" {
		t.Errorf("legacy Alias=%q want %q", got.Alias, "host5-a")
	}
	if got.Hostname != "" {
		t.Errorf("legacy Hostname should be empty, got %q", got.Hostname)
	}
}

func TestNewHostResolver_PartialOverride(t *testing.T) {
	// Node IN override gets Hostname; node NOT in override falls
	// back to alias-only.
	r := NewHostResolver(map[string]string{
		"host5-a": "10.0.0.5",
	})
	if got := r("host5-a"); got.Hostname != "10.0.0.5" {
		t.Errorf("srv5: Hostname=%q want %q", got.Hostname, "10.0.0.5")
	}
	if got := r("host6-a"); got.Hostname != "" || got.Alias != "host6-a" {
		t.Errorf("srv6 should fall through to alias-only, got %+v", got)
	}
}
