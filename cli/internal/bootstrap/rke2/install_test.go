package rke2

import (
	"context"
	"strings"
	"testing"

	"github.com/shalb/kube-dc/cli/internal/bootstrap/ports"
)

// fakeSSH answers Run by first-substring match and records Put calls.
// RFC 5737 / example.com throughout (cli/ is on the public-mirror lint
// surface).
type fakeSSH struct {
	runs     map[string]string // cmd substring -> stdout
	runErr   map[string]error
	fetch    map[string]string // remotePath -> contents
	fetchErr map[string]error
	putPath  string
	putBody  []byte
	putMode  uint32
	putCalls int
	ranCmds  []string
}

func (f *fakeSSH) Run(_ context.Context, _ ports.SSHHost, cmd string) ([]byte, error) {
	f.ranCmds = append(f.ranCmds, cmd)
	for sub, out := range f.runs {
		if strings.Contains(cmd, sub) {
			return []byte(out), f.runErr[sub]
		}
	}
	return nil, nil
}
func (f *fakeSSH) Fetch(_ context.Context, _ ports.SSHHost, remotePath string) ([]byte, error) {
	if err := f.fetchErr[remotePath]; err != nil {
		return nil, err
	}
	if v, ok := f.fetch[remotePath]; ok {
		return []byte(v), nil
	}
	return nil, nil
}
func (f *fakeSSH) Put(_ context.Context, _ ports.SSHHost, remotePath string, body []byte, mode uint32) error {
	f.putCalls++
	f.putPath, f.putBody, f.putMode = remotePath, body, mode
	return nil
}

func (f *fakeSSH) ranAny(sub string) bool {
	for _, c := range f.ranCmds {
		if strings.Contains(c, sub) {
			return true
		}
	}
	return false
}

func baseOpts(ssh ports.SSHClient) InstallOptions {
	return InstallOptions{
		SSH:         ssh,
		Host:        ports.SSHHost{User: "ubuntu", Hostname: "192.0.2.10"},
		NodeName:    "dc1",
		Domain:      "example.com",
		PodCIDR:     "10.100.0.0/16",
		ServiceCIDR: "10.101.0.0/16",
		ClusterDNS:  "10.101.0.11",
	}
}

func TestBuildInstallEnv(t *testing.T) {
	env := buildInstallEnv(InstallOptions{
		NodeName: "dc1", Domain: "example.com",
		PodCIDR: "10.100.0.0/16", ServiceCIDR: "10.101.0.0/16", ClusterDNS: "10.101.0.11",
		NodeIP: "198.51.100.5",
	})
	// ExternalIP defaults to NodeIP; version defaults to the pin.
	for k, want := range map[string]string{
		"NODE_NAME": "dc1", "DOMAIN": "example.com",
		"NODE_IP": "198.51.100.5", "EXTERNAL_IP": "198.51.100.5",
		"POD_CIDR": "10.100.0.0/16", "SERVICE_CIDR": "10.101.0.0/16", "CLUSTER_DNS": "10.101.0.11",
		"RKE2_VERSION": defaultRKE2Version,
	} {
		if env[k] != want {
			t.Errorf("env[%s] = %q, want %q", k, env[k], want)
		}
	}

	// Explicit ExternalIP + version are honored.
	env2 := buildInstallEnv(InstallOptions{NodeIP: "198.51.100.5", ExternalIP: "203.0.113.9", RKE2Version: "v1.36.0+rke2r1"})
	if env2["EXTERNAL_IP"] != "203.0.113.9" || env2["RKE2_VERSION"] != "v1.36.0+rke2r1" {
		t.Errorf("explicit overrides not honored: %+v", env2)
	}
}

func TestRemoteInstallCmd(t *testing.T) {
	env := map[string]string{"NODE_IP": "198.51.100.5", "DOMAIN": "example.com", "POD_CIDR": "10.100.0.0/16"}
	got := remoteInstallCmd(env, "/tmp/x.sh")
	// Non-interactive sudo + env prefix + deterministic (sorted) key order.
	if !strings.HasPrefix(got, "sudo -n env ") {
		t.Errorf("want `sudo -n env` prefix: %q", got)
	}
	if !strings.HasSuffix(got, " bash /tmp/x.sh") {
		t.Errorf("want trailing `bash <script>`: %q", got)
	}
	if strings.Index(got, "DOMAIN=") > strings.Index(got, "NODE_IP=") {
		t.Errorf("keys not sorted (DOMAIN should precede NODE_IP): %q", got)
	}
	if !strings.Contains(got, "NODE_IP='198.51.100.5'") {
		t.Errorf("value not single-quoted: %q", got)
	}
}

func TestShellQuote(t *testing.T) {
	if got := shellQuote("a'b"); got != `'a'\''b'` {
		t.Errorf("shellQuote embedded quote = %q", got)
	}
}

func TestParseRouteSrc(t *testing.T) {
	ok := "1.1.1.1 via 198.51.100.1 dev enp1s0 src 198.51.100.5 uid 1000\n    cache"
	if ip, err := parseRouteSrc([]byte(ok)); err != nil || ip != "198.51.100.5" {
		t.Errorf("parseRouteSrc = %q,%v want 198.51.100.5", ip, err)
	}
	if _, err := parseRouteSrc([]byte("blackhole 1.1.1.1")); err == nil {
		t.Error("want error when no src token")
	}
}

func TestDetectNodeIP_FallsBackToHostnameI(t *testing.T) {
	// Route probe returns empty (just-booted node) → fall back to
	// `hostname -I` first address.
	ssh := &fakeSSH{runs: map[string]string{
		"ip -4 route get": "",
		"hostname -I":     "10.77.0.22 fe80::1 \n",
	}}
	ip, err := DetectNodeIP(context.Background(), ssh, ports.SSHHost{})
	if err != nil || ip != "10.77.0.22" {
		t.Errorf("fallback: got %q,%v want 10.77.0.22", ip, err)
	}
}

func TestFirstIPv4(t *testing.T) {
	if got := firstIPv4([]byte("fe80::1 10.0.0.5 10.0.0.6")); got != "10.0.0.5" {
		t.Errorf("firstIPv4 = %q, want 10.0.0.5", got)
	}
	if got := firstIPv4([]byte("  \n")); got != "" {
		t.Errorf("firstIPv4 empty = %q, want \"\"", got)
	}
}

func TestValidate(t *testing.T) {
	if err := (InstallOptions{}).validate(); err == nil {
		t.Error("empty opts must fail validate (no SSH)")
	}
	ok := baseOpts(&fakeSSH{})
	if err := ok.validate(); err != nil {
		t.Errorf("valid opts failed: %v", err)
	}
	noCIDR := ok
	noCIDR.ClusterDNS = ""
	if err := noCIDR.validate(); err == nil {
		t.Error("missing ClusterDNS must fail validate")
	}
}

func TestValidate_SemanticChecks(t *testing.T) {
	// Each mutation of a valid config must be rejected before any YAML
	// is written to a node (P2).
	mut := map[string]func(*InstallOptions){
		"bad PodCIDR":     func(o *InstallOptions) { o.PodCIDR = "10.100.0.0" }, // no mask
		"bad ServiceCIDR": func(o *InstallOptions) { o.ServiceCIDR = "nope" },
		"bad ClusterDNS":  func(o *InstallOptions) { o.ClusterDNS = "10.101.0" },
		"bad NodeIP":      func(o *InstallOptions) { o.NodeIP = "999.0.0.1" },
		"bad ExternalIP":  func(o *InstallOptions) { o.ExternalIP = "not-ip" },
		"domain newline":  func(o *InstallOptions) { o.Domain = "example.com\nevil: true" },
		"nodeName space":  func(o *InstallOptions) { o.NodeName = "dc 1" },
		"nodeName quote":  func(o *InstallOptions) { o.NodeName = "dc\"1" },
	}
	for name, m := range mut {
		o := baseOpts(&fakeSSH{})
		m(&o)
		if err := o.validate(); err == nil {
			t.Errorf("%s: validate should have rejected it", name)
		}
	}
	// A fully-valid config with explicit IPs passes.
	o := baseOpts(&fakeSSH{})
	o.NodeIP = "198.51.100.5"
	o.ExternalIP = "203.0.113.9"
	if err := o.validate(); err != nil {
		t.Errorf("valid config rejected: %v", err)
	}
}

func TestInstall_DryRun_ProbesButDoesNotMutate(t *testing.T) {
	ssh := &fakeSSH{runs: map[string]string{"ip -4 route get": "1.1.1.1 dev eth0 src 198.51.100.5 uid 0"}}
	o := baseOpts(ssh)
	o.DryRun = true
	if err := Install(context.Background(), o); err != nil {
		t.Fatalf("dry-run: %v", err)
	}
	if ssh.putCalls != 0 {
		t.Error("dry-run must not Put the installer")
	}
	if ssh.ranAny("bash " + remoteScriptPath) {
		t.Error("dry-run must not run the installer")
	}
	if !ssh.ranAny("ip -4 route get") {
		t.Error("dry-run should still probe node IP")
	}
}

func TestInstall_IdempotentSkipWhenActive(t *testing.T) {
	ssh := &fakeSSH{runs: map[string]string{
		"ip -4 route get":                 "1.1.1.1 dev eth0 src 198.51.100.5",
		"systemctl is-active rke2-server": "active",
	}}
	if err := Install(context.Background(), baseOpts(ssh)); err != nil {
		t.Fatalf("idempotent run: %v", err)
	}
	if ssh.putCalls != 0 {
		t.Error("must NOT re-install when rke2-server already active")
	}
}

func TestInstall_HappyPath(t *testing.T) {
	ssh := &fakeSSH{runs: map[string]string{
		"ip -4 route get":                 "1.1.1.1 dev eth0 src 198.51.100.5",
		"systemctl is-active rke2-server": "inactive",
		"bash " + remoteScriptPath:        "RKE2 installed",
	}}
	if err := Install(context.Background(), baseOpts(ssh)); err != nil {
		t.Fatalf("happy path: %v", err)
	}
	if ssh.putCalls != 1 || ssh.putPath != remoteScriptPath || ssh.putMode != 0o755 {
		t.Errorf("installer not pushed correctly: calls=%d path=%s mode=%o", ssh.putCalls, ssh.putPath, ssh.putMode)
	}
	if len(ssh.putBody) == 0 || !strings.Contains(string(ssh.putBody), "cni: none") {
		t.Error("pushed body should be the RKE2 installer script")
	}
	if !ssh.ranAny("NODE_NAME='dc1'") || !ssh.ranAny("bash "+remoteScriptPath) {
		t.Errorf("installer not run with env: %v", ssh.ranCmds)
	}
}

func TestInstall_ForceReinstallsEvenIfActive(t *testing.T) {
	ssh := &fakeSSH{runs: map[string]string{
		"ip -4 route get":                 "1.1.1.1 dev eth0 src 198.51.100.5",
		"systemctl is-active rke2-server": "active",
		"bash " + remoteScriptPath:        "ok",
	}}
	o := baseOpts(ssh)
	o.Force = true
	if err := Install(context.Background(), o); err != nil {
		t.Fatalf("force: %v", err)
	}
	if ssh.putCalls != 1 {
		t.Error("--force must re-install even when active")
	}
	// --force over a running node must restart to apply the rewritten config.
	if !ssh.ranAny("systemctl restart rke2-server") {
		t.Error("--force over an active node must restart rke2-server to apply config")
	}
}

func TestInstall_FreshDoesNotRestart(t *testing.T) {
	// A fresh install (service not previously active) must NOT issue a
	// restart — the installer's own start loads the config.
	ssh := &fakeSSH{runs: map[string]string{
		"ip -4 route get":                 "1.1.1.1 dev eth0 src 198.51.100.5",
		"systemctl is-active rke2-server": "inactive",
		"bash " + remoteScriptPath:        "ok",
	}}
	if err := Install(context.Background(), baseOpts(ssh)); err != nil {
		t.Fatalf("fresh: %v", err)
	}
	if ssh.ranAny("systemctl restart rke2-server") {
		t.Error("fresh install must not restart rke2-server")
	}
}
