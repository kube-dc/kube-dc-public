package fleetconfig

import (
	"errors"
	"os"
	"path/filepath"
	"testing"

	"github.com/shalb/kube-dc/cli/internal/bootstrap/config"
)

func TestStripInlineComment(t *testing.T) {
	cases := map[string]string{
		"v0.3.88   # 2026-06-25 release":     "v0.3.88",
		"v0.1.11\t# tab":                     "v0.1.11",
		"10.100.0.0/16":                      "10.100.0.0/16",
		"https://x/y#frag":                   "https://x/y#frag", // '#' glued to value is preserved
		"value ":                             "value",
		"  # only a comment... but no value": "", // leading-space '#' after nothing → stripped to ""
	}
	for in, want := range cases {
		if got := StripInlineComment(in); got != want {
			t.Errorf("StripInlineComment(%q) = %q, want %q", in, got, want)
		}
	}
}

func TestValidateValue(t *testing.T) {
	for _, ok := range []string{"v1.2.3", "10.0.0.0/8", "true"} {
		if err := ValidateValue(ok); err != nil {
			t.Errorf("ValidateValue(%q) unexpected error: %v", ok, err)
		}
	}
	for _, bad := range []string{"", "   ", "a\nb", "a\rb", "a\x00b"} {
		if err := ValidateValue(bad); err == nil {
			t.Errorf("ValidateValue(%q) should error", bad)
		}
	}
}

func TestParseAssignments(t *testing.T) {
	kvs, err := ParseAssignments([]string{"A=1", "B = two words? no—single ", "C=x=y"})
	if err != nil {
		t.Fatalf("parse: %v", err)
	}
	if len(kvs) != 3 || kvs[0].Key != "A" || kvs[0].Value != "1" {
		t.Errorf("A wrong: %+v", kvs)
	}
	if kvs[2].Key != "C" || kvs[2].Value != "x=y" { // only first '=' splits
		t.Errorf("C should split on first '=': %+v", kvs[2])
	}
	// Errors.
	for _, bad := range [][]string{{"noeq"}, {"=v"}, {"A=1", "A=2"}, {"A="}} {
		if _, err := ParseAssignments(bad); err == nil {
			t.Errorf("ParseAssignments(%v) should error", bad)
		}
	}
}

func loadEnvFixture(t *testing.T) *config.Env {
	t.Helper()
	dir := t.TempDir()
	p := filepath.Join(dir, "cluster-config.env")
	body := "KUBE_DC_MANAGER_TAG=v0.3.88   # pinned for the billing fix\n" +
		"PROM_RETENTION=15d\n"
	if err := os.WriteFile(p, []byte(body), 0o644); err != nil {
		t.Fatal(err)
	}
	env, err := config.LoadEnv(p)
	if err != nil {
		t.Fatal(err)
	}
	return env
}

func TestPlan(t *testing.T) {
	env := loadEnvFixture(t)

	// Update an existing key (comment stripped from Old), a no-op, reject unknown.
	changes, err := Plan(env, []KV{
		{Key: "KUBE_DC_MANAGER_TAG", Value: "v0.3.90"},
		{Key: "PROM_RETENTION", Value: "15d"}, // no-op
	}, false)
	if err != nil {
		t.Fatalf("plan: %v", err)
	}
	if changes[0].Old != "v0.3.88" || changes[0].New != "v0.3.90" || changes[0].Added {
		t.Errorf("update change wrong: %+v", changes[0])
	}
	if !changes[1].NoOp() {
		t.Errorf("PROM_RETENTION should be a no-op: %+v", changes[1])
	}
	if !HasEffective(changes) {
		t.Error("HasEffective should be true (manager tag changes)")
	}

	// Unknown key without --add → ErrUnknownKey.
	if _, err := Plan(env, []KV{{Key: "NEW_KEY", Value: "x"}}, false); !errors.Is(err, ErrUnknownKey) {
		t.Errorf("unknown key without --add: want ErrUnknownKey, got %v", err)
	}
	// Unknown key WITH --add → allowed, marked Added.
	added, err := Plan(env, []KV{{Key: "NEW_KEY", Value: "x"}}, true)
	if err != nil || !added[0].Added {
		t.Errorf("--add should allow a new key: %+v %v", added, err)
	}

	// All no-ops → HasEffective false.
	noop, _ := Plan(env, []KV{{Key: "PROM_RETENTION", Value: "15d"}}, false)
	if HasEffective(noop) {
		t.Error("HasEffective should be false for an all-no-op plan")
	}
}
