package mock

import (
	"context"
	"errors"
	"strings"
	"testing"
	"time"

	"github.com/shalb/kube-dc/cli/internal/bootstrap/ports"
)

// TestListScenarios sanity-checks that the three M0-T03 scenarios load
// from the embedded FS. Adding new scenarios extends this test
// implicitly (we just check inclusion, not exclusion).
func TestListScenarios(t *testing.T) {
	got, err := ListScenarios()
	if err != nil {
		t.Fatalf("ListScenarios: %v", err)
	}
	required := []string{"fresh", "cloud", "openbao-sealed"}
	for _, want := range required {
		found := false
		for _, g := range got {
			if g == want {
				found = true
				break
			}
		}
		if !found {
			t.Errorf("scenarios %v missing required %q", got, want)
		}
	}
}

// TestLoad_UnknownScenario surfaces the friendly error path —
// production CLI prints this when KUBE_DC_MOCK=<garbage>.
func TestLoad_UnknownScenario(t *testing.T) {
	_, err := Load("definitely-not-a-real-scenario")
	if err == nil {
		t.Fatal("Load(unknown) returned no error")
	}
	if !strings.Contains(err.Error(), "available:") {
		t.Errorf("Load(unknown) error didn't list available scenarios: %v", err)
	}
}

// TestNewSession_AllScenariosExerciseEveryPort is the smoke test the
// M0-T02 acceptance gate requires: for each scenario, construct a
// Session, then call every port's read methods to confirm no panic
// and sensible defaults.
func TestNewSession_AllScenariosExerciseEveryPort(t *testing.T) {
	scenarios := []string{"fresh", "cloud", "openbao-sealed"}
	for _, name := range scenarios {
		t.Run(name, func(t *testing.T) {
			s, err := NewSession(name)
			if err != nil {
				t.Fatalf("NewSession(%q): %v", name, err)
			}
			ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
			defer cancel()

			// Every port must exist + respond.
			if s.Scripts == nil {
				t.Fatal("Scripts nil")
			}
			if s.Flux == nil {
				t.Fatal("Flux nil")
			}
			if s.K8s == nil {
				t.Fatal("K8s nil")
			}
			if s.OpenBao == nil {
				t.Fatal("OpenBao nil")
			}
			if s.Git == nil {
				t.Fatal("Git nil")
			}
			if s.SOPS == nil {
				t.Fatal("SOPS nil")
			}
			if s.Systemctl == nil {
				t.Fatal("Systemctl nil")
			}
			if s.Netplan == nil {
				t.Fatal("Netplan nil")
			}
			if s.DNS == nil {
				t.Fatal("DNS nil")
			}
			if s.SSH == nil {
				t.Fatal("SSH nil")
			}

			// K8s: NodeLabels, ListNamespaces, DeploymentImages
			// always non-error in mock (per the port contract for
			// "unreachable cluster" we return DiscoverFluxGraph
			// errors specifically — covered below).
			if _, err := s.K8s.NodeLabels(ctx); err != nil {
				t.Errorf("K8s.NodeLabels: %v", err)
			}
			if _, err := s.K8s.ListNamespaces(ctx); err != nil {
				t.Errorf("K8s.ListNamespaces: %v", err)
			}
			if _, err := s.K8s.DeploymentImages(ctx, "kube-dc"); err != nil {
				t.Errorf("K8s.DeploymentImages: %v", err)
			}

			// DiscoverFluxGraph: scenario-dependent. fresh has cluster
			// but no flux → ErrFluxNotInstalled. cloud + openbao-sealed
			// have flux → no error.
			_, err = s.K8s.DiscoverFluxGraph(ctx)
			switch name {
			case "fresh":
				if !errors.Is(err, ports.ErrFluxNotInstalled) {
					t.Errorf("fresh: DiscoverFluxGraph err=%v, want ErrFluxNotInstalled", err)
				}
			default:
				if err != nil {
					t.Errorf("%s: DiscoverFluxGraph err=%v, want nil", name, err)
				}
			}

			// OpenBao: PodList + Status round-trip.
			pods, err := s.OpenBao.PodList(ctx)
			if err != nil {
				t.Errorf("OpenBao.PodList: %v", err)
			}
			for _, pod := range pods {
				if _, err := s.OpenBao.Status(ctx, pod); err != nil {
					t.Errorf("OpenBao.Status(%q): %v", pod, err)
				}
			}

			// DNS resolve a known wildcard. fresh has *.fresh.example,
			// cloud + openbao-sealed have *.kube-dc.cloud.
			var wildcardSuffix, wantIP string
			switch name {
			case "fresh":
				wildcardSuffix, wantIP = "fresh.example", "192.168.1.10"
			default:
				wildcardSuffix, wantIP = "kube-dc.cloud", "213.111.154.233"
			}
			// Randomised sub-label — exactly what M1-T03 will do.
			ips, err := s.DNS.Resolve(ctx, "kdc-dns-test-abc123."+wildcardSuffix, "A")
			if err != nil {
				t.Errorf("DNS.Resolve wildcard: %v", err)
			}
			if len(ips) != 1 || ips[0] != wantIP {
				t.Errorf("DNS.Resolve wildcard sub-label: got %v, want [%s]", ips, wantIP)
			}

			// Systemctl: rke2-server lookup.
			active, _, err := s.Systemctl.IsActive(ctx, "rke2-server")
			if err != nil {
				t.Errorf("Systemctl.IsActive: %v", err)
			}
			switch name {
			case "fresh":
				if !active {
					t.Errorf("fresh: rke2-server expected active, got inactive")
				}
			}

			// SOPS: Encrypt + SetStringData + Decrypt round-trip.
			path := "clusters/test/secrets.enc.yaml"
			if err := s.SOPS.Encrypt(ctx, path); err != nil {
				t.Errorf("SOPS.Encrypt: %v", err)
			}
			if err := s.SOPS.SetStringData(ctx, path, "TEST_KEY", []byte("test-value")); err != nil {
				t.Errorf("SOPS.SetStringData: %v", err)
			}
			body, err := s.SOPS.Decrypt(ctx, path)
			if err != nil {
				t.Errorf("SOPS.Decrypt: %v", err)
			}
			if !strings.Contains(string(body), "TEST_KEY: test-value") {
				t.Errorf("SOPS roundtrip: body=%q does not contain expected stringData entry", body)
			}

			// Git: Head + dirty + commit + reset round-trip.
			dir := "/tmp/mock-repo-" + name
			if err := s.Git.Clone(ctx, "https://github.com/mock/repo.git", dir, "tok"); err != nil {
				t.Errorf("Git.Clone: %v", err)
			}
			pre, err := s.Git.Head(ctx, dir)
			if err != nil {
				t.Errorf("Git.Head: %v", err)
			}
			s.Git.MarkDirty(dir, "clusters/test/cluster-config.env", "M")
			diff, err := s.Git.Diff(ctx, dir)
			if err != nil {
				t.Errorf("Git.Diff: %v", err)
			}
			if len(diff.Files) != 1 {
				t.Errorf("Git.Diff: got %d files, want 1", len(diff.Files))
			}
			commit, err := s.Git.CommitAndPush(ctx, dir, "test commit", "tok")
			if err != nil {
				t.Errorf("Git.CommitAndPush: %v", err)
			}
			if commit == pre {
				t.Error("Git.CommitAndPush returned the same SHA as pre-commit Head")
			}
			if err := s.Git.ResetHard(ctx, dir, pre); err != nil {
				t.Errorf("Git.ResetHard to pre-commit SHA: %v", err)
			}
			now, _ := s.Git.Head(ctx, dir)
			if now != pre {
				t.Errorf("Git.ResetHard didn't unwind: head=%s, pre=%s", now, pre)
			}
		})
	}
}

// TestScriptRunner_UnknownScript verifies the ErrUnknownScript path.
func TestScriptRunner_UnknownScript(t *testing.T) {
	s, err := NewSession("fresh")
	if err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithTimeout(context.Background(), 1*time.Second)
	defer cancel()

	_, err = s.Scripts.Run(ctx, "not-a-registered-script.sh", nil)
	if !errors.Is(err, ports.ErrUnknownScript) {
		t.Errorf("Run(unknown) err=%v, want ErrUnknownScript", err)
	}
}

// TestScriptRunner_Replay confirms a fixture's lines stream through in
// order and the final StreamExit Line carries the exit code.
func TestScriptRunner_Replay(t *testing.T) {
	s, err := NewSession("fresh")
	if err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
	defer cancel()

	out, err := s.Scripts.Run(ctx, ports.ScriptGenerateAgeKey, nil)
	if err != nil {
		t.Fatal(err)
	}
	var lines []ports.Line
	for line := range out {
		lines = append(lines, line)
	}
	if len(lines) == 0 {
		t.Fatal("got 0 lines, want >0")
	}
	last := lines[len(lines)-1]
	if last.Stream != ports.StreamExit {
		t.Errorf("last line stream=%q, want %q", last.Stream, ports.StreamExit)
	}
	if last.Text != "0" {
		t.Errorf("last line text=%q, want 0", last.Text)
	}
}

// TestScriptRunner_Sentinel exercises the OpenBao init payload
// diversion. The payload must NOT appear in the regular stream; the
// callback must receive the raw bytes.
func TestScriptRunner_Sentinel(t *testing.T) {
	scenario, err := Load("fresh")
	if err != nil {
		t.Fatal(err)
	}
	var captured []byte
	runner := NewScriptRunner(scenario, func(kind ports.ScriptKind, marker string, payload []byte) error {
		captured = make([]byte, len(payload))
		copy(captured, payload)
		return nil
	})

	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	out, err := runner.Run(ctx, ports.ScriptOpenBaoInit, nil)
	if err != nil {
		t.Fatal(err)
	}
	for line := range out {
		// Crucial: payload must never leak into the stream.
		if strings.Contains(line.Text, "mock-root-token-fresh") {
			t.Errorf("sentinel payload leaked into stream: %q", line.Text)
		}
	}
	if !strings.Contains(string(captured), "mock-root-token-fresh") {
		t.Errorf("callback didn't capture payload — got %q", captured)
	}
}

// TestScriptRunner_SentinelCallbackFailure verifies the runner
// terminates the script with non-zero exit when the callback returns
// an error, and the placeholder line is still emitted (operator sees
// "something happened") but the payload itself never leaks.
func TestScriptRunner_SentinelCallbackFailure(t *testing.T) {
	scenario, err := Load("fresh")
	if err != nil {
		t.Fatal(err)
	}
	callbackErr := errors.New("simulated buffer-alloc failure")
	runner := NewScriptRunner(scenario, func(kind ports.ScriptKind, marker string, payload []byte) error {
		return callbackErr
	})

	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	out, err := runner.Run(ctx, ports.ScriptOpenBaoInit, nil)
	if err != nil {
		t.Fatal(err)
	}
	var lines []ports.Line
	for line := range out {
		lines = append(lines, line)
		if strings.Contains(line.Text, "mock-root-token-fresh") {
			t.Errorf("payload leaked into stream after callback failure: %q", line.Text)
		}
	}
	last := lines[len(lines)-1]
	if last.Stream != ports.StreamExit {
		t.Fatalf("last line stream=%q, want %q", last.Stream, ports.StreamExit)
	}
	if last.Text != "1" {
		t.Errorf("exit code on callback failure=%q, want 1", last.Text)
	}
}

// TestOpenBaoClient_UnsealMutation: the openbao-sealed scenario starts
// with sealed=true; after Unseal, Status reflects sealed=false.
func TestOpenBaoClient_UnsealMutation(t *testing.T) {
	s, err := NewSession("openbao-sealed")
	if err != nil {
		t.Fatal(err)
	}
	ctx := context.Background()

	pre, _ := s.OpenBao.Status(ctx, "openbao-0")
	if !pre.Sealed {
		t.Fatalf("openbao-0 pre-unseal sealed=false, want true")
	}
	if err := s.OpenBao.Unseal(ctx, "openbao-0", []byte("mock-share-sealed-1")); err != nil {
		t.Fatal(err)
	}
	post, _ := s.OpenBao.Status(ctx, "openbao-0")
	if post.Sealed {
		t.Error("openbao-0 post-unseal sealed=true, want false")
	}
}

// TestOpenBaoClient_GenerateRootTracking verifies the test-helper
// ActiveRootTokens() flips on GenerateRoot + back on RevokeSelf.
// Catches "caller forgot defer RevokeSelf" patterns in M5 tests.
func TestOpenBaoClient_GenerateRootTracking(t *testing.T) {
	s, err := NewSession("cloud")
	if err != nil {
		t.Fatal(err)
	}
	ctx := context.Background()

	tok, err := s.OpenBao.GenerateRoot(ctx, [][]byte{[]byte("s1"), []byte("s2"), []byte("s3")})
	if err != nil {
		t.Fatal(err)
	}
	if len(s.OpenBao.ActiveRootTokens()) != 1 {
		t.Fatalf("active tokens after GenerateRoot=%v, want 1", s.OpenBao.ActiveRootTokens())
	}
	if err := s.OpenBao.RevokeSelf(ctx, tok); err != nil {
		t.Fatal(err)
	}
	if got := s.OpenBao.ActiveRootTokens(); len(got) != 0 {
		t.Errorf("active tokens after RevokeSelf=%v, want []", got)
	}
}
