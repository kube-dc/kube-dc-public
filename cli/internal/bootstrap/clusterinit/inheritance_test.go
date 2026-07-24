package clusterinit

import (
	"errors"
	"strings"
	"testing"
	"time"
)

func sibling(name, domain string, modTime time.Time, env map[string]string) SiblingCluster {
	return SiblingCluster{Name: name, Domain: domain, ModTime: modTime, Env: env}
}

func TestIsVersionKey(t *testing.T) {
	cases := []struct {
		key  string
		want bool
	}{
		{"KUBE_DC_VERSION", true},
		{"OPENBAO_CHART_VERSION", true},
		{"KUBE_DC_MANAGER_TAG", true},
		{"KUBE_DC_BACKEND_TAG", true},
		{"DOMAIN", false},
		{"EXT_NET_VLAN_ID", false},
		{"POD_CIDR", false},
		{"NODE_EXTERNAL_IP", false},
		{"EXT_PUBLIC_VLAN_ID", false}, // ends in _ID, not _VERSION/_TAG
		{"_TAG", true},                // edge case — has the suffix
	}
	for _, tc := range cases {
		tc := tc
		t.Run(tc.key, func(t *testing.T) {
			if got := isVersionKey(tc.key); got != tc.want {
				t.Errorf("isVersionKey(%q) = %t, want %t", tc.key, got, tc.want)
			}
		})
	}
}

func TestInheritFromSiblings_Empty(t *testing.T) {
	got := InheritFromSiblings(nil)
	if got.TemplateName != "" {
		t.Errorf("empty siblings → empty TemplateName, got %q", got.TemplateName)
	}
	if len(got.Defaults) != 0 {
		t.Errorf("empty siblings → empty Defaults, got %v", got.Defaults)
	}
}

func TestInheritFromSiblings_SingleSibling(t *testing.T) {
	t0 := time.Date(2026, 5, 1, 12, 0, 0, 0, time.UTC)
	got := InheritFromSiblings([]SiblingCluster{
		sibling("cloud", "kube-dc.cloud", t0, map[string]string{
			"KUBE_DC_VERSION":     "v0.3.63",
			"KUBE_DC_MANAGER_TAG": "v0.3.63",
			"OPENBAO_VERSION":     "2.5.3",
			"DOMAIN":              "kube-dc.cloud", // not a version key — should be ignored
			"EXT_NET_VLAN_ID":     "1103",          // not a version key
		}),
	})
	if got.TemplateName != "cloud" {
		t.Errorf("TemplateName = %q, want cloud", got.TemplateName)
	}
	for k, want := range map[string]string{
		"KUBE_DC_VERSION":     "v0.3.63",
		"KUBE_DC_MANAGER_TAG": "v0.3.63",
		"OPENBAO_VERSION":     "2.5.3",
	} {
		if got.Defaults[k] != want {
			t.Errorf("Defaults[%s] = %q, want %q", k, got.Defaults[k], want)
		}
	}
	// Non-version keys must NOT appear in Defaults.
	for _, k := range []string{"DOMAIN", "EXT_NET_VLAN_ID"} {
		if _, present := got.Defaults[k]; present {
			t.Errorf("non-version key %q leaked into Defaults", k)
		}
	}
}

func TestInheritFromSiblings_MostRecentWins(t *testing.T) {
	// stage is older with v0.3.60; cloud is newer with v0.3.63. The
	// newer sibling's value must win when the keys collide.
	t0 := time.Date(2026, 4, 1, 12, 0, 0, 0, time.UTC)
	t1 := time.Date(2026, 5, 25, 9, 0, 0, 0, time.UTC)
	siblings := []SiblingCluster{
		sibling("stage", "stage.kube-dc.com", t0, map[string]string{
			"KUBE_DC_VERSION":    "v0.3.60",
			"OPENBAO_VERSION":    "2.5.1",
			"SOMETHING_ELSE_TAG": "v0.1.0", // only in stage
		}),
		sibling("cloud", "kube-dc.cloud", t1, map[string]string{
			"KUBE_DC_VERSION": "v0.3.63",
			"OPENBAO_VERSION": "2.5.3",
		}),
	}
	got := InheritFromSiblings(siblings)
	if got.TemplateName != "cloud" {
		t.Errorf("TemplateName = %q, want cloud (most recent)", got.TemplateName)
	}
	if got.Defaults["KUBE_DC_VERSION"] != "v0.3.63" {
		t.Errorf("KUBE_DC_VERSION = %q, want v0.3.63", got.Defaults["KUBE_DC_VERSION"])
	}
	if got.Defaults["OPENBAO_VERSION"] != "2.5.3" {
		t.Errorf("OPENBAO_VERSION = %q, want 2.5.3", got.Defaults["OPENBAO_VERSION"])
	}
	// Key only in stage (older) should still be inherited — the
	// template just doesn't override it.
	if got.Defaults["SOMETHING_ELSE_TAG"] != "v0.1.0" {
		t.Errorf("SOMETHING_ELSE_TAG = %q, want v0.1.0 (inherited from non-template)",
			got.Defaults["SOMETHING_ELSE_TAG"])
	}
}

func TestInheritFromSiblings_TieBreaksByName(t *testing.T) {
	// Same ModTime — alphabetic name order wins (deterministic).
	t0 := time.Date(2026, 5, 1, 12, 0, 0, 0, time.UTC)
	siblings := []SiblingCluster{
		sibling("zulu", "zulu.example", t0, map[string]string{
			"KUBE_DC_VERSION": "v9.9.9",
		}),
		sibling("alpha", "alpha.example", t0, map[string]string{
			"KUBE_DC_VERSION": "v1.1.1",
		}),
	}
	got := InheritFromSiblings(siblings)
	if got.TemplateName != "alpha" {
		t.Errorf("TemplateName = %q, want alpha (alphabetic tiebreak)", got.TemplateName)
	}
	if got.Defaults["KUBE_DC_VERSION"] != "v1.1.1" {
		t.Errorf("KUBE_DC_VERSION = %q, want v1.1.1", got.Defaults["KUBE_DC_VERSION"])
	}
}

func TestInheritFromSiblings_PerKeyMostRecentWinsForNonTemplateKey(t *testing.T) {
	// Review-pass P2: when a key is absent from the template
	// sibling, the winner among the remaining siblings must be the
	// most-recently-modified provider, NOT the alphabetically-first.
	// The prior alphabetic-only fallback would silently pick the
	// wrong value during partial upgrades.
	//
	// Scenario: template (cloud, freshest) lacks NEW_CHART_VERSION;
	// two older siblings disagree on the value. `z-sib` (older
	// alphabetically last) was modified more recently than
	// `a-sib` (older alphabetically first), so z-sib wins per
	// most-recent-provider.
	tNewest := time.Date(2026, 5, 25, 12, 0, 0, 0, time.UTC)
	tMiddle := time.Date(2026, 5, 10, 12, 0, 0, 0, time.UTC)
	tOldest := time.Date(2026, 4, 1, 12, 0, 0, 0, time.UTC)

	siblings := []SiblingCluster{
		sibling("cloud", "kube-dc.cloud", tNewest, map[string]string{
			"KUBE_DC_VERSION": "v0.3.63",
			// no NEW_CHART_VERSION
		}),
		sibling("a-sib", "a.example", tOldest, map[string]string{
			"NEW_CHART_VERSION": "from-a-older",
		}),
		sibling("z-sib", "z.example", tMiddle, map[string]string{
			"NEW_CHART_VERSION": "from-z-newer",
		}),
	}
	got := InheritFromSiblings(siblings)
	if got.TemplateName != "cloud" {
		t.Errorf("TemplateName = %q, want cloud", got.TemplateName)
	}
	if got.Defaults["NEW_CHART_VERSION"] != "from-z-newer" {
		t.Errorf("per-key winner should be most-recent provider z-sib (%q), got %q",
			"from-z-newer", got.Defaults["NEW_CHART_VERSION"])
	}
}

func TestInheritFromSiblings_PerKeyAlphabeticTiebreak(t *testing.T) {
	// When ModTimes are equal among the providers of a non-template
	// key, alphabetic name tiebreak applies (deterministic).
	t0 := time.Date(2026, 5, 1, 12, 0, 0, 0, time.UTC)
	tEarlier := time.Date(2026, 4, 1, 12, 0, 0, 0, time.UTC)
	siblings := []SiblingCluster{
		sibling("template", "t.example", t0, map[string]string{
			"KUBE_DC_VERSION": "v0.3.63",
		}),
		// Two providers of the new key with IDENTICAL ModTime —
		// alphabetic name wins.
		sibling("z-tied", "z.example", tEarlier, map[string]string{
			"NEW_CHART_VERSION": "from-z",
		}),
		sibling("a-tied", "a.example", tEarlier, map[string]string{
			"NEW_CHART_VERSION": "from-a",
		}),
	}
	got := InheritFromSiblings(siblings)
	if got.Defaults["NEW_CHART_VERSION"] != "from-a" {
		t.Errorf("alphabetic tiebreak should pick a-tied; got %q",
			got.Defaults["NEW_CHART_VERSION"])
	}
}

func TestInheritFromSiblings_DeterministicAcrossRuns(t *testing.T) {
	// Two siblings introducing the same new key (not in template).
	// Result must be deterministic regardless of input slice order.
	t0 := time.Date(2026, 4, 1, 12, 0, 0, 0, time.UTC)
	t1 := time.Date(2026, 5, 25, 9, 0, 0, 0, time.UTC)
	template := sibling("cloud", "kube-dc.cloud", t1, map[string]string{
		"KUBE_DC_VERSION": "v0.3.63",
	})
	a := sibling("a-sib", "a.example", t0, map[string]string{
		"NEW_CHART_VERSION": "from-a",
	})
	b := sibling("b-sib", "b.example", t0, map[string]string{
		"NEW_CHART_VERSION": "from-b",
	})
	got1 := InheritFromSiblings([]SiblingCluster{template, a, b})
	got2 := InheritFromSiblings([]SiblingCluster{b, template, a})
	if got1.Defaults["NEW_CHART_VERSION"] != got2.Defaults["NEW_CHART_VERSION"] {
		t.Errorf("non-deterministic: %q vs %q",
			got1.Defaults["NEW_CHART_VERSION"], got2.Defaults["NEW_CHART_VERSION"])
	}
	if got1.Defaults["NEW_CHART_VERSION"] != "from-a" {
		t.Errorf("alphabetic-first sibling should win for non-template keys; got %q",
			got1.Defaults["NEW_CHART_VERSION"])
	}
}

func TestCheckDomainCollision(t *testing.T) {
	siblings := []SiblingCluster{
		{Name: "cloud", Domain: "kube-dc.cloud"},
		{Name: "stage", Domain: "stage.kube-dc.com"},
		{Name: "eu/dc1", Domain: "kdc.eu-dc1.example.net"},
	}
	cases := []struct {
		name      string
		newDomain string
		wantErr   bool
		wantSub   string
	}{
		{"unique domain ok", "kdc.atlantis.example.com", false, ""},
		{"exact collision rejected", "kube-dc.cloud", true, "cloud"},
		{"case-insensitive collision", "KUBE-DC.CLOUD", true, "cloud"},
		{"nested-cluster collision", "kdc.eu-dc1.example.net", true, "eu/dc1"},
		{"empty newDomain ignored", "", false, ""},
	}
	for _, tc := range cases {
		tc := tc
		t.Run(tc.name, func(t *testing.T) {
			err := CheckDomainCollision(tc.newDomain, siblings)
			if tc.wantErr {
				if !errors.Is(err, ErrDomainCollision) {
					t.Fatalf("expected ErrDomainCollision, got %v", err)
				}
				if !strings.Contains(err.Error(), tc.wantSub) {
					t.Errorf("error %q missing sibling name %q", err.Error(), tc.wantSub)
				}
				return
			}
			if err != nil {
				t.Fatalf("expected ok, got %v", err)
			}
		})
	}
}

func TestStripInlineComment(t *testing.T) {
	// The live fleet annotates pins with `KEY=value # rationale` for
	// reviewer context. Inherited values must strip the annotation
	// so downstream serialization to the new cluster's env doesn't
	// propagate the noise.
	cases := []struct {
		in, want string
	}{
		{"v0.3.63", "v0.3.63"},
		{"v0.3.63   # rotate-root tombstone", "v0.3.63"},
		{"v0.3.63 # short comment", "v0.3.63"},
		{"v0.3.63\t# tab-separated comment", "v0.3.63"},
		{"v0.3.63   ", "v0.3.63"},
		{"", ""},
		// Edge case: `#` immediately after the value with no space —
		// not the convention in the fleet but worth a test for stability.
		{"v0.3.63#nospace", "v0.3.63#nospace"},
	}
	for _, tc := range cases {
		tc := tc
		t.Run(tc.in, func(t *testing.T) {
			if got := stripInlineComment(tc.in); got != tc.want {
				t.Errorf("stripInlineComment(%q) = %q, want %q", tc.in, got, tc.want)
			}
		})
	}
}

func TestInheritFromSiblings_StripsInlineComments(t *testing.T) {
	t0 := time.Date(2026, 5, 1, 12, 0, 0, 0, time.UTC)
	got := InheritFromSiblings([]SiblingCluster{
		sibling("cloud", "kube-dc.cloud", t0, map[string]string{
			"KUBE_DC_MANAGER_TAG": "v0.3.63   # rotate-root tombstone rationale",
			"KUBE_DC_VERSION":     "v0.3.63",
		}),
	})
	if got.Defaults["KUBE_DC_MANAGER_TAG"] != "v0.3.63" {
		t.Errorf("inherited value should strip inline comment; got %q",
			got.Defaults["KUBE_DC_MANAGER_TAG"])
	}
}

func TestCheckDomainCollision_IgnoresEmptySiblingDomain(t *testing.T) {
	// Malformed sibling without DOMAIN must not trigger a false-
	// positive collision against the new cluster's empty-string
	// state.
	siblings := []SiblingCluster{
		{Name: "broken", Domain: ""},
	}
	if err := CheckDomainCollision("anything.example.com", siblings); err != nil {
		t.Errorf("empty sibling domain shouldn't collide, got %v", err)
	}
}
