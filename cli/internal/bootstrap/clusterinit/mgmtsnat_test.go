package clusterinit

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestDeriveMgmtSnatIP(t *testing.T) {
	cases := []struct {
		name, cidr, gw, exclude string
		wantAddr, wantExclude   string
		wantErr                 string
	}{
		{"bare gateway exclusion", "100.64.0.0/16", "100.64.0.1", "100.64.0.1", "100.64.0.2", "100.64.0.1..100.64.0.2", ""},
		{"empty exclusion", "100.64.0.0/16", "100.64.0.1", "", "100.64.0.2", "100.64.0.1..100.64.0.2", ""},
		{"fleet .1..100 convention yields .101", "100.65.0.0/16", "100.65.0.1", "100.65.0.1..100.65.0.100", "100.65.0.101", "100.65.0.1..100.65.0.101", ""},
		{"range not covering the gateway keeps its own neighbour", "100.64.0.0/16", "100.64.0.1", "100.64.0.50..100.64.0.60", "100.64.0.2", "100.64.0.1..100.64.0.2", ""},
		{"small onprem /29 has room", "203.0.113.0/29", "203.0.113.1", "203.0.113.1", "203.0.113.2", "203.0.113.1..203.0.113.2", ""},
		{"block reaching the broadcast has none", "203.0.113.0/29", "203.0.113.1", "203.0.113.1..203.0.113.6", "", "", "no free host"},
		{"several entries refused", "100.64.0.0/16", "100.64.0.1", "100.64.0.1,100.64.0.9", "", "", "several entries"},
		{"garbage exclusion", "100.64.0.0/16", "100.64.0.1", "not-an-ip", "", "", "not an address"},
		{"gateway outside cidr", "100.64.0.0/16", "10.0.0.1", "", "", "", "inside"},
		{"bad cidr", "nope", "100.64.0.1", "", "", "", "not a CIDR"},
	}
	for _, c := range cases {
		addr, ex, err := DeriveMgmtSnatIP(c.cidr, c.gw, c.exclude)
		if c.wantErr != "" {
			if err == nil || !strings.Contains(err.Error(), c.wantErr) {
				t.Errorf("%s: want error containing %q, got %v", c.name, c.wantErr, err)
			}
			continue
		}
		if err != nil {
			t.Errorf("%s: unexpected error %v", c.name, err)
			continue
		}
		if addr != c.wantAddr || ex != c.wantExclude {
			t.Errorf("%s: got %s / %s, want %s / %s", c.name, addr, ex, c.wantAddr, c.wantExclude)
		}
	}
}

func TestValidateMgmtSnatIP(t *testing.T) {
	base := func(v, exclude string) map[string]string {
		return map[string]string{keyExtCIDR: "100.64.0.0/16", keyExtGateway: "100.64.0.1", keyExtExclude: exclude, keyMgmtSnatIP: v}
	}
	for _, c := range []struct{ v, exclude, want string }{
		{"", "100.64.0.1", ""},
		{"CHANGEME", "100.64.0.1", ""},
		{"100.64.0.4", "100.64.0.1..100.64.0.4", ""},
		{"100.64.0.4", "100.64.0.1", "not covered"},
		{"100.64.0.1", "100.64.0.1", "gateway"},
		{"100.64.0.0", "100.64.0.0..100.64.0.4", "network or broadcast"},
		{"100.64.255.255", "100.64.0.1..100.64.255.255", "network or broadcast"},
		{"10.0.0.4", "100.64.0.1", "outside"},
		{"x", "100.64.0.1", "not an IPv4"},
	} {
		got := ValidateMgmtSnatIP(base(c.v, c.exclude))
		if c.want == "" && got != "" {
			t.Errorf("%q/%q: unexpected %q", c.v, c.exclude, got)
		}
		if c.want != "" && !strings.Contains(got, c.want) {
			t.Errorf("%q/%q: want %q, got %q", c.v, c.exclude, c.want, got)
		}
	}
}

func TestResolveMgmtSnatIP_FillsPlaceholderAndReserves(t *testing.T) {
	repo := t.TempDir()
	dir := filepath.Join(repo, "clusters", "dc1")
	if err := os.MkdirAll(dir, 0o755); err != nil {
		t.Fatal(err)
	}
	env := "EXT_NET_CIDR=100.65.0.0/16\nEXT_NET_GATEWAY=100.65.0.1\nEXT_NET_EXCLUDE_IPS=100.65.0.1..100.65.0.100\nEXT_NET_MGMT_SNAT_IP=CHANGEME\n"
	p := filepath.Join(dir, "cluster-config.env")
	if err := os.WriteFile(p, []byte(env), 0o644); err != nil {
		t.Fatal(err)
	}
	var out strings.Builder
	if err := ResolveMgmtSnatIP(repo, "dc1", &out); err != nil {
		t.Fatal(err)
	}
	got, _ := os.ReadFile(p)
	for _, want := range []string{"EXT_NET_MGMT_SNAT_IP=100.65.0.101\n", "EXT_NET_EXCLUDE_IPS=100.65.0.1..100.65.0.101\n"} {
		if !strings.Contains(string(got), want) {
			t.Errorf("env missing %q:\n%s", want, got)
		}
	}
	if !strings.Contains(out.String(), "100.65.0.101") {
		t.Errorf("no operator-visible line: %q", out.String())
	}
	// Second run: explicit value now, validated, untouched.
	if err := ResolveMgmtSnatIP(repo, "dc1", &out); err != nil {
		t.Fatalf("re-run must validate and pass: %v", err)
	}
	// An explicit but uncovered value is refused rather than silently kept.
	if err := os.WriteFile(p, []byte("EXT_NET_CIDR=100.65.0.0/16\nEXT_NET_GATEWAY=100.65.0.1\nEXT_NET_EXCLUDE_IPS=100.65.0.1\nEXT_NET_MGMT_SNAT_IP=100.65.0.50\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := ResolveMgmtSnatIP(repo, "dc1", &out); err == nil || !strings.Contains(err.Error(), "not covered") {
		t.Errorf("uncovered explicit address must be refused, got %v", err)
	}
	// Older starter without the key: no-op.
	if err := os.WriteFile(p, []byte("EXT_NET_CIDR=100.65.0.0/16\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := ResolveMgmtSnatIP(repo, "dc1", &out); err != nil {
		t.Errorf("missing key must be a no-op: %v", err)
	}
}
