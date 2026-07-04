package clusterinit

import (
	"bytes"
	"context"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/shalb/kube-dc/cli/internal/bootstrap/ports"
)

// fakeScriptRunner emits a canned list of stdout/stderr/exit Lines
// + records the args it was called with. Lets us exercise Scaffold
// without forking add-cluster.sh.
type fakeScriptRunner struct {
	calls []fakeScriptCall
	// lines is replayed verbatim through Scaffold's drain.
	lines []ports.Line
	// runErr is returned by Run before any lines stream.
	runErr error
	// onRun is called inside Run with the destination directory so
	// the test can write the script's "output files" before the
	// drain consumes the exit line. Allows simulating either a
	// happy path (files written, encrypted) or a sad path
	// (plaintext leak).
	onRun func(clusterDir string) error
	// fleetRoot is the absolute repo path so onRun can compute
	// clusterDir from the args.
	fleetRoot string
}

type fakeScriptCall struct {
	Kind ports.ScriptKind
	Env  map[string]string
	Args []string
}

// WithSentinelCallback satisfies the ports.ScriptRunner interface
// (M5-T01 added this method). The fake doesn't actually wire the
// callback through Run since no Scaffold/Apply test exercises
// sentinel-emitting scripts; the method is here for interface
// completeness so the fake compiles against the port.
func (f *fakeScriptRunner) WithSentinelCallback(_ ports.SentinelCallback) ports.ScriptRunner {
	return f
}

func (f *fakeScriptRunner) Run(ctx context.Context, name ports.ScriptKind, env map[string]string, args ...string) (<-chan ports.Line, error) {
	f.calls = append(f.calls, fakeScriptCall{Kind: name, Env: env, Args: args})
	if f.runErr != nil {
		return nil, f.runErr
	}
	if f.onRun != nil {
		if len(args) >= 1 && f.fleetRoot != "" {
			if err := f.onRun(filepath.Join(f.fleetRoot, "clusters", args[0])); err != nil {
				return nil, err
			}
		}
	}
	out := make(chan ports.Line, len(f.lines)+1)
	for _, ln := range f.lines {
		out <- ln
	}
	close(out)
	return out, nil
}

// --- redactor table ---

func TestRedactAddClusterLine(t *testing.T) {
	cases := []struct {
		name string
		in   string
		want string
	}{
		{
			name: "keycloak password redacted",
			in:   "    KEYCLOAK_ADMIN_PASSWORD: ZqV9bF2nP0mWtL3jXcKa",
			want: "    KEYCLOAK_ADMIN_PASSWORD: [REDACTED — see secrets.enc.yaml]",
		},
		{
			name: "grafana password with double-space",
			in:   "    GRAFANA_ADMIN_PASSWORD:  XwQbN7cM2vTyU1eA8sDp",
			want: "    GRAFANA_ADMIN_PASSWORD:  [REDACTED — see secrets.enc.yaml]",
		},
		{
			name: "minio password redacted",
			in:   "    LOKI_MINIO_ROOT_PASSWORD: H8jK3lP5qR2sN1mB4xZc",
			want: "    LOKI_MINIO_ROOT_PASSWORD: [REDACTED — see secrets.enc.yaml]",
		},
		// Negative cases — must be passed through unchanged.
		{
			name: "status line passthrough",
			in:   "==> Creating cluster overlay: cloudacropolis",
			want: "==> Creating cluster overlay: cloudacropolis",
		},
		{
			name: "non-password key passthrough",
			in:   "    Domain:    kdc.acropolis.example.com",
			want: "    Domain:    kdc.acropolis.example.com",
		},
		{
			name: "label that mentions PASSWORD but no value",
			in:   "    (save these somewhere safe — you won't see them again)",
			want: "    (save these somewhere safe — you won't see them again)",
		},
		{
			name: "PASSWORD as substring of another word",
			in:   "    WARNING: PASSWORDS were saved encrypted",
			want: "    WARNING: PASSWORDS were saved encrypted",
		},
	}
	for _, tc := range cases {
		tc := tc
		t.Run(tc.name, func(t *testing.T) {
			got := redactAddClusterLine(tc.in)
			if got != tc.want {
				t.Errorf("redact(%q) = %q\nwant %q", tc.in, got, tc.want)
			}
		})
	}
}

// --- SOPS encryption verifier ---

func TestVerifySOPSEncrypted_AcceptsCanonicalSOPS(t *testing.T) {
	// Minimal canonical sops-encrypted shape: `ENC[AES256_GCM,...]`
	// in a stringData value + a top-level `sops:` mapping.
	body := `apiVersion: v1
kind: Secret
stringData:
    KEYCLOAK_ADMIN_PASSWORD: ENC[AES256_GCM,data:abc,iv:xyz,type:str]
sops:
    age:
        - recipient: age1xxx
          enc: |
              -----BEGIN AGE ENCRYPTED FILE-----
              -----END AGE ENCRYPTED FILE-----
    mac: ENC[AES256_GCM,data:mac,iv:miv]
`
	dir := t.TempDir()
	path := filepath.Join(dir, "secrets.enc.yaml")
	if err := os.WriteFile(path, []byte(body), 0o644); err != nil {
		t.Fatalf("write: %v", err)
	}
	if err := verifySOPSEncrypted(path); err != nil {
		t.Errorf("encrypted file should pass, got %v", err)
	}
}

func TestVerifySOPSEncrypted_RejectsPlaintext(t *testing.T) {
	// The exact shape add-cluster.sh writes when sops falls back.
	body := `apiVersion: v1
kind: Secret
metadata:
    name: cluster-secrets
    namespace: flux-system
stringData:
    KEYCLOAK_ADMIN_PASSWORD: ZqV9bF2nP0mWtL3jXcKa
    GRAFANA_ADMIN_PASSWORD: XwQbN7cM2vTyU1eA8sDp
`
	dir := t.TempDir()
	path := filepath.Join(dir, "secrets.enc.yaml")
	if err := os.WriteFile(path, []byte(body), 0o644); err != nil {
		t.Fatalf("write: %v", err)
	}
	err := verifySOPSEncrypted(path)
	if !errors.Is(err, ErrScaffoldSecretsNotEncrypted) {
		t.Fatalf("plaintext should be rejected, got %v", err)
	}
	if !strings.Contains(err.Error(), path) {
		t.Errorf("error should name the path: %v", err)
	}
	if !strings.Contains(err.Error(), "sops -e -i") {
		t.Errorf("error should include remediation: %v", err)
	}
}

func TestVerifySOPSEncrypted_PartialEncryption_Rejected(t *testing.T) {
	// Defensive: file with the `sops:` block but no `ENC[` markers
	// in the values (or vice versa) is malformed — refuse.
	body := `apiVersion: v1
stringData:
    KEYCLOAK_ADMIN_PASSWORD: ENC[AES256_GCM,data:abc,iv:xyz,type:str]
# Note: no sops: footer
`
	dir := t.TempDir()
	path := filepath.Join(dir, "secrets.enc.yaml")
	if err := os.WriteFile(path, []byte(body), 0o644); err != nil {
		t.Fatalf("write: %v", err)
	}
	err := verifySOPSEncrypted(path)
	if !errors.Is(err, ErrScaffoldSecretsNotEncrypted) {
		t.Fatalf("missing sops footer should be rejected, got %v", err)
	}
}

// --- post-process cluster-config.env ---

func TestPostProcessClusterConfig_AppliesPresetOverrides(t *testing.T) {
	// Simulate what add-cluster.sh wrote: CHANGEME values for
	// VLAN_ID + INTERFACE. Post-process must replace them with
	// the operator's --set values via the preset's resolved env.
	dir := t.TempDir()
	path := filepath.Join(dir, "cluster-config.env")
	scriptOutput := `# Cluster: cloudacropolis
CLUSTER_NAME=cloudacropolis
DOMAIN=kdc.acropolis.example.com
EXT_NET_NAME=ext-cloud
EXT_NET_VLAN_ID=CHANGEME
EXT_NET_INTERFACE=CHANGEME
POD_CIDR=10.100.0.0/16
`
	if err := os.WriteFile(path, []byte(scriptOutput), 0o644); err != nil {
		t.Fatalf("write seed: %v", err)
	}

	plan := &Plan{
		Preset:      PresetCloudPublicVLAN,
		ClusterName: "cloudacropolis",
		InheritedDefaults: map[string]string{
			"KUBE_DC_VERSION": "v0.3.63",
		},
	}
	sets := map[string]string{
		"EXT_NET_VLAN_ID":    "1103",
		"EXT_NET_INTERFACE":  "bond0",
		"EXT_PUBLIC_VLAN_ID": "1100",
		"EXT_PUBLIC_CIDR":    "217.117.26.48/29",
		"EXT_PUBLIC_GATEWAY": "217.117.26.49",
	}
	if err := postProcessClusterConfig(path, plan, sets); err != nil {
		t.Fatalf("postProcess: %v", err)
	}

	body, _ := os.ReadFile(path)
	out := string(body)
	for _, want := range []string{
		"EXT_NET_VLAN_ID=1103",
		"EXT_NET_INTERFACE=bond0",
		"EXT_PUBLIC_VLAN_ID=1100",      // new key appended
		"EXT_PUBLIC_CIDR=217.117.26.48/29",
		"EXT_PUBLIC_GATEWAY=217.117.26.49",
		"KUBE_DC_VERSION=v0.3.63",      // inherited
	} {
		if !strings.Contains(out, want) {
			t.Errorf("env missing %q\nFULL:\n%s", want, out)
		}
	}
	// CHANGEME must be gone.
	if strings.Contains(out, "CHANGEME") {
		t.Errorf("CHANGEME placeholders survived post-process:\n%s", out)
	}
	// Original comment header preserved.
	if !strings.Contains(out, "# Cluster: cloudacropolis") {
		t.Errorf("env header comment lost:\n%s", out)
	}
}

func TestPostProcessClusterConfig_OperatorOverrideBeatsInherited(t *testing.T) {
	// Inherited KUBE_DC_VERSION=v0.3.63 (from siblings) vs operator
	// --set=KUBE_DC_VERSION=v0.4.0. Operator must win.
	dir := t.TempDir()
	path := filepath.Join(dir, "cluster-config.env")
	if err := os.WriteFile(path, []byte("CLUSTER_NAME=test\n"), 0o644); err != nil {
		t.Fatalf("write: %v", err)
	}
	plan := &Plan{
		Preset:      PresetCloudPublicVLAN,
		InheritedDefaults: map[string]string{"KUBE_DC_VERSION": "v0.3.63"},
	}
	sets := map[string]string{
		"EXT_NET_VLAN_ID":    "1103",
		"EXT_NET_INTERFACE":  "bond0",
		"EXT_PUBLIC_VLAN_ID": "1100",
		"EXT_PUBLIC_CIDR":    "10.0.0.0/24",
		"EXT_PUBLIC_GATEWAY": "10.0.0.1",
		"KUBE_DC_VERSION":    "v0.4.0", // operator override
	}
	if err := postProcessClusterConfig(path, plan, sets); err != nil {
		t.Fatalf("postProcess: %v", err)
	}
	body, _ := os.ReadFile(path)
	if !strings.Contains(string(body), "KUBE_DC_VERSION=v0.4.0") {
		t.Errorf("operator override should win; got:\n%s", body)
	}
	if strings.Contains(string(body), "KUBE_DC_VERSION=v0.3.63") {
		t.Errorf("inherited value leaked through operator override:\n%s", body)
	}
}

// --- Scaffold orchestration ---

func TestScaffold_HappyPath(t *testing.T) {
	repo := t.TempDir()
	if err := os.MkdirAll(filepath.Join(repo, "clusters"), 0o755); err != nil {
		t.Fatal(err)
	}

	// The fake script "writes" the same files the real script would:
	// cluster-config.env with CHANGEMEs + an encrypted secrets.enc.yaml.
	encryptedSecrets := `apiVersion: v1
stringData:
    KEYCLOAK_ADMIN_PASSWORD: ENC[AES256_GCM,data:abc,iv:xyz,type:str]
sops:
    mac: ENC[AES256_GCM,data:mac,iv:miv]
    age:
        - recipient: age1xxx
`
	envBody := `CLUSTER_NAME=cloudacropolis
DOMAIN=kdc.acropolis.example.com
EXT_NET_VLAN_ID=CHANGEME
EXT_NET_INTERFACE=CHANGEME
`
	runner := &fakeScriptRunner{
		fleetRoot: repo,
		onRun: func(clusterDir string) error {
			if err := os.MkdirAll(clusterDir, 0o755); err != nil {
				return err
			}
			if err := os.WriteFile(filepath.Join(clusterDir, "cluster-config.env"), []byte(envBody), 0o644); err != nil {
				return err
			}
			return os.WriteFile(filepath.Join(clusterDir, "secrets.enc.yaml"), []byte(encryptedSecrets), 0o644)
		},
		lines: []ports.Line{
			{Stream: ports.StreamStdout, Text: "==> Creating cluster overlay: cloudacropolis", Time: time.Now()},
			// Password echo lines — must be redacted in output.
			{Stream: ports.StreamStdout, Text: "    KEYCLOAK_ADMIN_PASSWORD: PLAINTEXT_LEAK_CANARY", Time: time.Now()},
			{Stream: ports.StreamStdout, Text: "    GRAFANA_ADMIN_PASSWORD: PLAINTEXT_LEAK_CANARY", Time: time.Now()},
			{Stream: ports.StreamExit, Text: "0", Time: time.Now()},
		},
	}

	var out bytes.Buffer
	err := Scaffold(context.Background(), ScaffoldOptions{
		Plan: &Plan{
			ClusterName: "cloudacropolis",
			Domain:      "kdc.acropolis.example.com",
			Preset:      PresetCloudPublicVLAN,
		},
		FleetRepo:      repo,
		NodeExternalIP: "217.117.26.52",
		Sets: map[string]string{
			"EXT_NET_VLAN_ID":    "1103",
			"EXT_NET_INTERFACE":  "bond0",
			"EXT_PUBLIC_VLAN_ID": "1100",
			"EXT_PUBLIC_CIDR":    "217.117.26.48/29",
			"EXT_PUBLIC_GATEWAY": "217.117.26.49",
		},
		Runner: runner,
		Out:    &out,
	})
	if err != nil {
		t.Fatalf("Scaffold: %v\nout:\n%s", err, out.String())
	}

	// CRITICAL: PLAINTEXT_LEAK_CANARY must NOT appear in the
	// captured output — redaction kicked in.
	if strings.Contains(out.String(), "PLAINTEXT_LEAK_CANARY") {
		t.Fatalf("password value leaked to operator output:\n%s", out.String())
	}
	if !strings.Contains(out.String(), "[REDACTED — see secrets.enc.yaml]") {
		t.Errorf("redaction marker missing from output:\n%s", out.String())
	}
	// Script args reached the runner correctly.
	if len(runner.calls) != 1 {
		t.Fatalf("expected 1 script call, got %d", len(runner.calls))
	}
	call := runner.calls[0]
	if call.Kind != ports.ScriptAddCluster {
		t.Errorf("script kind = %v, want ScriptAddCluster", call.Kind)
	}
	wantArgs := []string{"cloudacropolis", "kdc.acropolis.example.com", "217.117.26.52"}
	if len(call.Args) != len(wantArgs) {
		t.Fatalf("args length = %d, want %d (%v)", len(call.Args), len(wantArgs), call.Args)
	}
	for i, a := range wantArgs {
		if call.Args[i] != a {
			t.Errorf("args[%d] = %q, want %q", i, call.Args[i], a)
		}
	}
	// Post-process kicked in — CHANGEMEs replaced.
	body, _ := os.ReadFile(filepath.Join(repo, "clusters", "cloudacropolis", "cluster-config.env"))
	if strings.Contains(string(body), "CHANGEME") {
		t.Errorf("CHANGEME survived post-process:\n%s", body)
	}
	if !strings.Contains(string(body), "EXT_NET_VLAN_ID=1103") {
		t.Errorf("preset --set didn't apply:\n%s", body)
	}
}

func TestScaffold_RefusesExistingTarget(t *testing.T) {
	// Marker file present = already scaffolded. Operator must clean
	// up before re-running.
	repo := t.TempDir()
	clusterDir := filepath.Join(repo, "clusters", "cloudacropolis")
	if err := os.MkdirAll(clusterDir, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(clusterDir, "cluster-config.env"),
		[]byte("KUBE_DC_VERSION=v0.0.1\n"), 0o644); err != nil {
		t.Fatal(err)
	}

	runner := &fakeScriptRunner{} // shouldn't be called

	err := Scaffold(context.Background(), ScaffoldOptions{
		Plan: &Plan{ClusterName: "cloudacropolis", Domain: "kdc.acropolis.example.com",
			Preset: PresetCloudPublicVLAN},
		FleetRepo:      repo,
		NodeExternalIP: "217.117.26.52",
		Sets:           map[string]string{"EXT_NET_VLAN_ID": "1103", "EXT_NET_INTERFACE": "bond0", "EXT_PUBLIC_VLAN_ID": "1100", "EXT_PUBLIC_CIDR": "10.0.0.0/24", "EXT_PUBLIC_GATEWAY": "10.0.0.1"},
		Runner:         runner,
	})
	if !errors.Is(err, ErrScaffoldTargetExists) {
		t.Fatalf("expected ErrScaffoldTargetExists, got %v", err)
	}
	if len(runner.calls) != 0 {
		t.Errorf("script must NOT run when target exists; calls=%d", len(runner.calls))
	}
}

func TestScaffold_AllowsPreExistingDocsOnly(t *testing.T) {
	// Operators sometimes pre-place a `docs/` README inside the
	// overlay (topology notes, etc.) before running bootstrap. The
	// preflight must not refuse on that — only on the
	// cluster-config.env marker. We don't run the full Scaffold
	// here (the script + cobra layer aren't being exercised); we
	// just confirm the preflight passes and reaches the script
	// invocation step.
	repo := t.TempDir()
	clusterDir := filepath.Join(repo, "clusters", "cloudacropolis", "docs")
	if err := os.MkdirAll(clusterDir, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(clusterDir, "README.md"),
		[]byte("# topology\n"), 0o644); err != nil {
		t.Fatal(err)
	}

	// Runner returns a non-empty stream so the test doesn't have to
	// thread the whole add-cluster.sh contract — just confirm the
	// preflight didn't refuse, by checking the runner was called.
	// The script "fails" via StreamExit nonzero; we don't care
	// about Scaffold's overall outcome here, only that the preflight
	// permitted the call.
	runner := &fakeScriptRunner{
		lines: []ports.Line{
			{Stream: ports.StreamStdout, Text: "ok"},
			{Stream: ports.StreamExit, Text: "1"},
		},
	}

	err := Scaffold(context.Background(), ScaffoldOptions{
		Plan: &Plan{ClusterName: "cloudacropolis", Domain: "kdc.acropolis.example.com",
			Preset: PresetCloudPublicVLAN},
		FleetRepo:      repo,
		NodeExternalIP: "217.117.26.52",
		Sets:           map[string]string{"EXT_NET_VLAN_ID": "1103", "EXT_NET_INTERFACE": "bond0"},
		Runner:         runner,
	})
	if errors.Is(err, ErrScaffoldTargetExists) {
		t.Fatalf("preflight wrongly refused a docs-only overlay: %v", err)
	}
	if len(runner.calls) == 0 {
		t.Errorf("script should have been invoked (preflight passed); got 0 calls")
	}
}

func TestScaffold_RefusesPlaintextSecrets(t *testing.T) {
	// Reviewer P1: script-side sops fallback writes plaintext;
	// Scaffold must refuse before any downstream commit/push.
	repo := t.TempDir()
	if err := os.MkdirAll(filepath.Join(repo, "clusters"), 0o755); err != nil {
		t.Fatal(err)
	}
	plaintextSecrets := `apiVersion: v1
stringData:
    KEYCLOAK_ADMIN_PASSWORD: literal-plaintext-not-good
`
	runner := &fakeScriptRunner{
		fleetRoot: repo,
		onRun: func(clusterDir string) error {
			if err := os.MkdirAll(clusterDir, 0o755); err != nil {
				return err
			}
			if err := os.WriteFile(filepath.Join(clusterDir, "cluster-config.env"), []byte("CLUSTER_NAME=x\n"), 0o644); err != nil {
				return err
			}
			return os.WriteFile(filepath.Join(clusterDir, "secrets.enc.yaml"), []byte(plaintextSecrets), 0o644)
		},
		lines: []ports.Line{
			{Stream: ports.StreamStdout, Text: "==> WARNING: SOPS age key not configured. secrets.enc.yaml is UNENCRYPTED.", Time: time.Now()},
			{Stream: ports.StreamExit, Text: "0", Time: time.Now()},
		},
	}
	err := Scaffold(context.Background(), ScaffoldOptions{
		Plan: &Plan{ClusterName: "cloudacropolis", Domain: "kdc.acropolis.example.com",
			Preset: PresetCloudPublicVLAN},
		FleetRepo:      repo,
		NodeExternalIP: "217.117.26.52",
		Sets:           map[string]string{"EXT_NET_VLAN_ID": "1103", "EXT_NET_INTERFACE": "bond0", "EXT_PUBLIC_VLAN_ID": "1100", "EXT_PUBLIC_CIDR": "10.0.0.0/24", "EXT_PUBLIC_GATEWAY": "10.0.0.1"},
		Runner:         runner,
	})
	if !errors.Is(err, ErrScaffoldSecretsNotEncrypted) {
		t.Fatalf("expected ErrScaffoldSecretsNotEncrypted, got %v", err)
	}
}

func TestScaffold_ScriptNonZeroExit(t *testing.T) {
	repo := t.TempDir()
	if err := os.MkdirAll(filepath.Join(repo, "clusters"), 0o755); err != nil {
		t.Fatal(err)
	}
	runner := &fakeScriptRunner{
		fleetRoot: repo,
		lines: []ports.Line{
			{Stream: ports.StreamStderr, Text: "ERROR: simulated failure", Time: time.Now()},
			{Stream: ports.StreamExit, Text: "1", Time: time.Now()},
		},
	}
	err := Scaffold(context.Background(), ScaffoldOptions{
		Plan: &Plan{ClusterName: "cloudacropolis", Domain: "kdc.acropolis.example.com",
			Preset: PresetCloudPublicVLAN},
		FleetRepo:      repo,
		NodeExternalIP: "217.117.26.52",
		Sets:           map[string]string{"EXT_NET_VLAN_ID": "1103", "EXT_NET_INTERFACE": "bond0", "EXT_PUBLIC_VLAN_ID": "1100", "EXT_PUBLIC_CIDR": "10.0.0.0/24", "EXT_PUBLIC_GATEWAY": "10.0.0.1"},
		Runner:         runner,
	})
	if !errors.Is(err, ErrScaffoldScriptFailed) {
		t.Fatalf("expected ErrScaffoldScriptFailed, got %v", err)
	}
	if !strings.Contains(err.Error(), "exit=1") {
		t.Errorf("error should report exit code: %v", err)
	}
}
