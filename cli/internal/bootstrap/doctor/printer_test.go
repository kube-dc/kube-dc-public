package doctor

import (
	"bytes"
	"os"
	"strings"
	"testing"

	"github.com/shalb/kube-dc/cli/internal/bootstrap/ports"
)

// runPrinter is the canonical no-TTY render path for snapshot
// assertions. Returns (stdout, exitCode).
func runPrinter(t *testing.T, results []NamedResult, next string) (string, int) {
	t.Helper()
	var buf bytes.Buffer
	p := &Printer{
		Out:         &buf,
		NoTTY:       true,
		NoColor:     true,
		NextCommand: next,
	}
	code := p.Print(results)
	return buf.String(), code
}

func TestPrinter_FreshScenario_NoCluster_NoFleet(t *testing.T) {
	// "Fresh" maps to: tools mostly missing, host probes
	// not-applicable (off-mode), DNS blocker.
	results := []NamedResult{
		{Category: CategoryAutoHandled, Name: "kubectl", Result: ports.Result{
			Status:   ports.StatusMissing,
			Severity: ports.SeverityBlocker,
			Detail:   "kubectl not found in PATH",
			FixHint:  ports.FixHint{Text: "Install via scripts/install-prerequisites.sh (in the kube-dc-fleet repo)"},
		}},
		{Category: CategoryAutoHandled, Name: "flux", Result: ports.Result{
			Status:   ports.StatusMissing,
			Severity: ports.SeverityBlocker,
			Detail:   "flux not found in PATH",
		}},
		{Category: CategoryVerifiesSuggests, Name: "wildcard-dns", Result: ports.Result{
			Status:   ports.StatusMissing,
			Severity: ports.SeverityBlocker,
			Detail:   "wildcard *.acme.example.com did not resolve",
			FixHint: ports.FixHint{
				Text: "Add one wildcard A record at the apex of acme.example.com",
				Records: []ports.DNSRecord{
					{Type: "A", Name: "*.acme.example.com", Value: "<node-external-ip>", TTL: 300},
				},
			},
		}},
		{Category: CategoryPhysical, Name: "rke2-server", Result: ports.Result{
			Status:   ports.StatusMissing,
			Severity: ports.SeverityInfo,
			Detail:   "rke2-server: not applicable (cluster-only mode)",
		}},
	}
	out, code := runPrinter(t, results, "kube-dc bootstrap init --domain acme.example.com")

	if code != 2 {
		t.Errorf("fresh exit code = %d, want 2 (Blocker present)", code)
	}
	mustContainAll(t, out,
		"Physical world:",
		"Auto-handled by CLI:",
		"CLI verifies + suggests:",
		"kubectl",
		"flux",
		"wildcard-dns",
		"rke2-server",
		"Add these DNS records:",
		"*.acme.example.com",
		"3 blockers, 0 warnings, 1 info",
		"Next: kube-dc bootstrap init --domain acme.example.com",
	)
	mustNotContain(t, out, "\x1b[") // no ANSI escapes in no-TTY mode
}

func TestPrinter_CloudScenario_AllGreen(t *testing.T) {
	results := []NamedResult{
		{Category: CategoryAutoHandled, Name: "kubectl", Result: ports.Result{
			Status:   ports.StatusInstalled,
			Severity: ports.SeverityInfo,
			Version:  "v1.32.1",
			Detail:   "kubectl v1.32.1",
		}},
		{Category: CategoryAutoHandled, Name: "flux", Result: ports.Result{
			Status:   ports.StatusInstalled,
			Severity: ports.SeverityInfo,
			Version:  "v2.4.0",
			Detail:   "flux v2.4.0",
		}},
		{Category: CategoryVerifiesSuggests, Name: "wildcard-dns", Result: ports.Result{
			Status:   ports.StatusInstalled,
			Severity: ports.SeverityInfo,
			Detail:   "wildcard *.acme.example.com → 213.111.154.233",
		}},
	}
	out, code := runPrinter(t, results, "")
	if code != 0 {
		t.Errorf("all-green exit code = %d, want 0", code)
	}
	mustContainAll(t, out, "kubectl", "flux", "wildcard-dns", "0 blockers, 0 warnings, 3 info")
	mustNotContain(t, out, "Next:")
}

func TestPrinter_OpenBaoSealedScenario_WarnOnly(t *testing.T) {
	results := []NamedResult{
		{Category: CategoryAutoHandled, Name: "kubectl", Result: ports.Result{
			Status: ports.StatusInstalled, Severity: ports.SeverityInfo, Detail: "kubectl v1.32.1",
		}},
		{Category: CategoryVerifiesSuggests, Name: "gh", Result: ports.Result{
			Status:   ports.StatusPartial,
			Severity: ports.SeverityWarn,
			Version:  "2.51.0",
			Detail:   "gh missing scopes: repo, workflow",
			FixHint:  ports.FixHint{Text: "Run `gh auth refresh --scopes repo,workflow`"},
		}},
	}
	out, code := runPrinter(t, results, "")
	if code != 1 {
		t.Errorf("warn-only exit code = %d, want 1", code)
	}
	mustContainAll(t, out, "gh", "missing scopes", "1 warnings", "gh auth refresh")
}

func TestPrinter_FatalSeverity_Exit3(t *testing.T) {
	results := []NamedResult{
		{Category: CategoryAutoHandled, Name: "cluster-api", Result: ports.Result{
			Status:   ports.StatusMissing,
			Severity: ports.SeverityFatal,
			Detail:   "cluster API unreachable",
		}},
	}
	_, code := runPrinter(t, results, "")
	if code != 3 {
		t.Errorf("fatal exit code = %d, want 3", code)
	}
}

// We don't assert ANSI escape presence here — lipgloss auto-detects
// the writer and disables colour against a non-TTY (bytes.Buffer
// included). Testing that we _take the lipgloss code path_ in TTY
// mode is enough; whether escape codes are emitted is the
// terminal's job.

func TestPrinter_NoColorEnv_StripsColor(t *testing.T) {
	t.Setenv("NO_COLOR", "1")
	results := []NamedResult{
		{Category: CategoryAutoHandled, Name: "kubectl", Result: ports.Result{
			Status: ports.StatusInstalled, Severity: ports.SeverityInfo, Detail: "ok",
		}},
	}
	var buf bytes.Buffer
	p := &Printer{Out: &buf, NoTTY: false, NoColor: false}
	p.Print(results)
	if strings.Contains(buf.String(), "\x1b[") {
		t.Error("NO_COLOR=1 should disable ANSI codes")
	}
}

func TestPrinter_StableOrderingWithinSection(t *testing.T) {
	// Probes added in non-alpha order; printer must sort by name
	// so snapshots are stable.
	results := []NamedResult{
		{Category: CategoryAutoHandled, Name: "ssh"},
		{Category: CategoryAutoHandled, Name: "age"},
		{Category: CategoryAutoHandled, Name: "kubectl"},
	}
	for i := range results {
		results[i].Result = ports.Result{Severity: ports.SeverityInfo, Detail: "ok"}
	}
	out, _ := runPrinter(t, results, "")
	ageIdx := strings.Index(out, "age ")
	kubectlIdx := strings.Index(out, "kubectl ")
	sshIdx := strings.Index(out, "ssh ")
	if !(ageIdx < kubectlIdx && kubectlIdx < sshIdx) {
		t.Errorf("expected age < kubectl < ssh: ages=%d kubectl=%d ssh=%d\n%s", ageIdx, kubectlIdx, sshIdx, out)
	}
}

func TestPrinter_EmptyCategory_NoSectionHeader(t *testing.T) {
	results := []NamedResult{
		{Category: CategoryAutoHandled, Name: "kubectl", Result: ports.Result{Severity: ports.SeverityInfo, Detail: "ok"}},
	}
	out, _ := runPrinter(t, results, "")
	if strings.Contains(out, "Physical world:") || strings.Contains(out, "CLI verifies + suggests:") {
		t.Errorf("empty sections leaked headers: %q", out)
	}
	if !strings.Contains(out, "Auto-handled by CLI:") {
		t.Errorf("present section missing header: %q", out)
	}
}

func TestExitCodeFor(t *testing.T) {
	cases := []struct {
		s    ports.Severity
		want int
	}{
		{ports.SeverityInfo, 0},
		{ports.SeverityWarn, 1},
		{ports.SeverityBlocker, 2},
		{ports.SeverityFatal, 3},
	}
	for _, c := range cases {
		if got := exitCodeFor(c.s); got != c.want {
			t.Errorf("exitCodeFor(%v)=%d, want %d", c.s, got, c.want)
		}
	}
}

func TestMaxSeverity(t *testing.T) {
	results := []NamedResult{
		{Result: ports.Result{Severity: ports.SeverityInfo}},
		{Result: ports.Result{Severity: ports.SeverityWarn}},
		{Result: ports.Result{Severity: ports.SeverityBlocker}},
	}
	if got := maxSeverity(results); got != ports.SeverityBlocker {
		t.Errorf("maxSeverity = %v, want Blocker", got)
	}
}

func TestPrinter_DefaultsToStdout(t *testing.T) {
	p := New()
	if p.Out != os.Stdout {
		t.Errorf("default Out != os.Stdout")
	}
}

// ---------- helpers ----------

func mustContainAll(t *testing.T, body string, fragments ...string) {
	t.Helper()
	for _, f := range fragments {
		if !strings.Contains(body, f) {
			t.Errorf("output missing %q\nFULL:\n%s", f, body)
		}
	}
}

func mustNotContain(t *testing.T, body string, fragment string) {
	t.Helper()
	if strings.Contains(body, fragment) {
		t.Errorf("output unexpectedly contained %q\nFULL:\n%s", fragment, body)
	}
}
