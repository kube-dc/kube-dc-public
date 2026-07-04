package main

import (
	"bytes"
	"errors"
	"strings"
	"testing"

	"github.com/shalb/kube-dc/cli/internal/bootstrap/doctor"
	"github.com/shalb/kube-dc/cli/internal/bootstrap/ports"
)

// Hermetic exit-code coverage — the M0-T06 batch-2 review's P1
// concern is that mock-mode doctor runs are deterministic. With
// the scenario-backed probe factory wired into the session, every
// developer's laptop produces the same exit code regardless of
// their local kubectl/gh/bao installation state.
//
// Mapping:
//
//	KUBE_DC_MOCK=cloud           - all tools installed/scoped, fresh fixtures: exit 0
//	KUBE_DC_MOCK=fresh           - bao not installed (Blocker): exit 2
//	KUBE_DC_MOCK=openbao-sealed  - all tools installed, sealed-pod state is cluster-level (not in this slice): exit 0
//
// The plan's loose "exit 1" expectations from §6 don't account
// for the actual severity contract (`bao=false` is Blocker not
// Warn). This test pins the severity-driven exit codes that the
// printer + factory actually produce.
func TestBootstrapDoctorCmd_MockExitCodes(t *testing.T) {
	cases := []struct {
		scenario string
		wantCode int
	}{
		{"cloud", 0},
		{"fresh", 2},
		{"openbao-sealed", 0},
	}
	for _, c := range cases {
		t.Run(c.scenario, func(t *testing.T) {
			t.Setenv("KUBE_DC_MOCK", c.scenario)
			t.Setenv("NO_COLOR", "1")

			var out bytes.Buffer
			fleetRepo := ""
			cmd := bootstrapDoctorCmd(&fleetRepo)
			cmd.SetOut(&out)
			cmd.SetErr(&out)
			cmd.SetArgs([]string{"--no-tty"})

			err := cmd.Execute()
			gotCode := 0
			if err != nil {
				var de *doctorExitCodeErr
				if !errors.As(err, &de) {
					t.Fatalf("%s: unexpected error %v", c.scenario, err)
				}
				gotCode = de.ExitCode()
			}
			if gotCode != c.wantCode {
				t.Errorf("%s exit code = %d, want %d\nOUTPUT:\n%s", c.scenario, gotCode, c.wantCode, out.String())
			}
		})
	}
}

// Section presence varies by --domain (DNS section is opt-in via
// the operator-supplied domain).
func TestBootstrapDoctorCmd_NoDomain_NoDNSSection(t *testing.T) {
	t.Setenv("KUBE_DC_MOCK", "cloud")
	t.Setenv("NO_COLOR", "1")

	var out bytes.Buffer
	fleetRepo := ""
	cmd := bootstrapDoctorCmd(&fleetRepo)
	cmd.SetOut(&out)
	cmd.SetErr(&out)
	cmd.SetArgs([]string{"--no-tty"})
	_ = cmd.Execute()

	body := out.String()
	// Without --domain, the DNS-specific probe rows must be absent.
	// M5-T07 added openbao-policy-generation to the CLI-verifies-
	// suggests category, so the SECTION HEADER may still render;
	// assert on the DNS-row names instead of the section header.
	for _, forbidden := range []string{"wildcard-dns", "explicit-fqdn-dns"} {
		if strings.Contains(body, forbidden) {
			t.Errorf("DNS probe %q should be absent without --domain\nOUTPUT:\n%s", forbidden, body)
		}
	}
}

func TestBootstrapDoctorCmd_WithDomain_AddsDNSSection(t *testing.T) {
	t.Setenv("KUBE_DC_MOCK", "cloud")
	t.Setenv("NO_COLOR", "1")

	var out bytes.Buffer
	fleetRepo := ""
	cmd := bootstrapDoctorCmd(&fleetRepo)
	cmd.SetOut(&out)
	cmd.SetErr(&out)
	cmd.SetArgs([]string{"--no-tty", "--domain", "kube-dc.cloud"})
	_ = cmd.Execute()

	body := out.String()
	mustContain(t, body, "CLI verifies + suggests:")
	mustContain(t, body, "wildcard-dns")
	mustContain(t, body, "explicit-fqdn-dns")
}

// M6-T03: NFD probe surfaces in the doctor's Physical section.
// Cloud scenario has 4 nodes labelled with the M6-T02 composites —
// probe reports StatusInstalled + Info + counts in the Detail line.
// Also proves the CategoryPhysical placement (NFD rows land next
// to rke2-server, kernel-modules, etc.) rather than in
// Auto-handled or Verifies-suggests.
func TestBootstrapDoctorCmd_NFDInPhysicalSection(t *testing.T) {
	t.Setenv("KUBE_DC_MOCK", "cloud")
	t.Setenv("NO_COLOR", "1")

	var out bytes.Buffer
	fleetRepo := ""
	cmd := bootstrapDoctorCmd(&fleetRepo)
	cmd.SetOut(&out)
	cmd.SetErr(&out)
	cmd.SetArgs([]string{"--no-tty"})
	_ = cmd.Execute()

	body := out.String()
	// nfd probe name appears in the doctor output
	mustContain(t, body, "nfd")
	// cloud fixture has composites → StatusInstalled/Info detail
	// mentions "NFD + composites" (see NFDProbe.Run).
	mustContain(t, body, "NFD + composites")
	// Cloud scenario: 4 nodes total, 3 kubevirt-eligible (blades
	// 179-8, 184-5, 58-2), 1 NVIDIA GPU (blade187-2). Counts
	// derive from the scenario's kube-dc.com/* labels.
	mustContain(t, body, "3 kubevirt-eligible")
	mustContain(t, body, "1 NVIDIA GPU")

	// Assert NFD lands under Physical world (not Auto-handled or
	// CLI verifies + suggests). Section headers render on separate
	// lines; find the nfd row and walk backwards to the most
	// recent section header.
	lines := strings.Split(body, "\n")
	var section string
	for _, line := range lines {
		switch strings.TrimSpace(line) {
		case "Physical world:":
			section = "Physical world"
		case "Auto-handled by CLI:":
			section = "Auto-handled by CLI"
		case "CLI verifies + suggests:":
			section = "CLI verifies + suggests"
		}
		if strings.Contains(line, " nfd ") && section != "" {
			if section != "Physical world" {
				t.Errorf("nfd landed under %q, want Physical world\nOUTPUT:\n%s", section, body)
			}
			return
		}
	}
	t.Errorf("nfd row not found in output\nOUTPUT:\n%s", body)
}

// M6-T03: with no kubeconfig-backed session, the doctor still
// renders — the NFD probe is skipped silently rather than
// crashing on a nil K8sClient. Fresh laptop / pre-install case.
func TestBootstrapDoctorCmd_NoKubeconfig_NFDSkipped(t *testing.T) {
	t.Setenv("KUBE_DC_MOCK", "")
	t.Setenv("KUBECONFIG", "/tmp/nonexistent-kubeconfig-for-nfd-skip-test")
	t.Setenv("HOME", t.TempDir())
	t.Setenv("NO_COLOR", "1")

	var out bytes.Buffer
	fleetRepo := ""
	cmd := bootstrapDoctorCmd(&fleetRepo)
	cmd.SetOut(&out)
	cmd.SetErr(&out)
	cmd.SetArgs([]string{"--no-tty"})
	_ = cmd.Execute()

	body := out.String()
	// No `nfd` row — the K8sClient is nil so assembleProbes' guard
	// (k8sFromSession returning nil) skips factory.Cluster().
	if strings.Contains(body, " nfd ") {
		t.Errorf("nfd row should be absent when no K8sClient wired\nOUTPUT:\n%s", body)
	}
	// Physical section still renders (host probes fall back to
	// "not applicable" when HostProbe mode is Off — matches the
	// existing TestBootstrapDoctorCmd_NoKubeconfig_DNSStillRuns
	// shape).
	mustContain(t, body, "Physical world:")
}

// M5-T07: PolicyGenerationProbe surfaces in the doctor's
// verifies+suggests section. Cloud scenario has no policy-generation
// annotation (pre-M5-T07 legacy install), so the probe reports
// StatusMissing with the refresh-policy FixHint.
func TestBootstrapDoctorCmd_PolicyGenerationInVerifiesSuggests(t *testing.T) {
	t.Setenv("KUBE_DC_MOCK", "cloud")
	t.Setenv("NO_COLOR", "1")

	var out bytes.Buffer
	fleetRepo := ""
	cmd := bootstrapDoctorCmd(&fleetRepo)
	cmd.SetOut(&out)
	cmd.SetErr(&out)
	cmd.SetArgs([]string{"--no-tty"})
	_ = cmd.Execute()

	body := out.String()
	// Row appears + description names the legacy case + FixHint
	// carries the refresh-policy recipe.
	for _, want := range []string{
		"openbao-policy-generation",
		"legacy install",
		"kube-dc bootstrap openbao setup-controller-auth",
		"--refresh-policy",
	} {
		if !strings.Contains(body, want) {
			t.Errorf("missing %q in doctor output\nOUTPUT:\n%s", want, body)
		}
	}

	// Section placement — row lands under CLI verifies + suggests,
	// not Physical or Auto-handled. Walk output to prove it.
	lines := strings.Split(body, "\n")
	var section string
	for _, line := range lines {
		switch strings.TrimSpace(line) {
		case "Physical world:":
			section = "Physical world"
		case "Auto-handled by CLI:":
			section = "Auto-handled by CLI"
		case "CLI verifies + suggests:":
			section = "CLI verifies + suggests"
		}
		if strings.Contains(line, " openbao-policy-generation ") && section != "" {
			if section != "CLI verifies + suggests" {
				t.Errorf("openbao-policy-generation landed under %q, want CLI verifies + suggests\nOUTPUT:\n%s", section, body)
			}
			return
		}
	}
	t.Errorf("openbao-policy-generation row not found in output\nOUTPUT:\n%s", body)
}

// DNS must run without a kubeconfig-backed session — the system
// resolver doesn't need a working cluster. Setting KUBECONFIG to a
// nonexistent path simulates the "fresh laptop, no kubeconfig"
// case; doctor should still produce DNS probe rows.
func TestBootstrapDoctorCmd_NoKubeconfig_DNSStillRuns(t *testing.T) {
	// Unset mock so the real-session path runs; it'll fail
	// kubeconfig load + we ride the ErrRealAdaptersNotReady
	// fallback.
	t.Setenv("KUBE_DC_MOCK", "")
	t.Setenv("KUBECONFIG", "/tmp/nonexistent-kubeconfig-for-doctor-test")
	t.Setenv("HOME", t.TempDir())
	t.Setenv("NO_COLOR", "1")

	var out bytes.Buffer
	fleetRepo := ""
	cmd := bootstrapDoctorCmd(&fleetRepo)
	cmd.SetOut(&out)
	cmd.SetErr(&out)
	cmd.SetArgs([]string{"--no-tty", "--domain", "acme.invalid"})

	_ = cmd.Execute()

	body := out.String()
	mustContain(t, body, "CLI verifies + suggests:")
	mustContain(t, body, "wildcard-dns")
	mustContain(t, body, "explicit-fqdn-dns")
}

func TestRecommendNext_BlockerHint(t *testing.T) {
	results := []doctor.NamedResult{
		{Result: ports.Result{Severity: ports.SeverityBlocker}},
	}
	got := recommendNext(results, "acme.example.com")
	if !strings.Contains(got, "address the blockers") {
		t.Errorf("blocker recommend = %q", got)
	}
	if !strings.Contains(got, "acme.example.com") {
		t.Errorf("recommend should include domain: %q", got)
	}
}

func TestRecommendNext_NoBlockerSuggestInit(t *testing.T) {
	results := []doctor.NamedResult{}
	got := recommendNext(results, "acme.example.com")
	if !strings.Contains(got, "bootstrap init") {
		t.Errorf("happy path should suggest init: %q", got)
	}
}

func TestDoctorExitCodeErr_HasExitCode(t *testing.T) {
	e := &doctorExitCodeErr{code: 2}
	if e.ExitCode() != 2 {
		t.Errorf("ExitCode=%d, want 2", e.ExitCode())
	}
	if e.Error() == "" {
		t.Error("Error() should not be empty")
	}
}

// --post-rke2 was intentionally NOT registered in v1 (M2 will add
// cluster probes; flag is meaningless without them). Guard against
// a future accidental registration that surfaces an inert flag.
func TestBootstrapDoctorCmd_NoPostRKE2Flag(t *testing.T) {
	fleetRepo := ""
	cmd := bootstrapDoctorCmd(&fleetRepo)
	if cmd.Flag("post-rke2") != nil {
		t.Error("--post-rke2 flag registered but inert (deferred to M2-T01); should not appear in the help surface")
	}
}

// ---------- helpers ----------

func mustContain(t *testing.T, body, frag string) {
	t.Helper()
	if !strings.Contains(body, frag) {
		t.Errorf("output missing %q\nFULL:\n%s", frag, body)
	}
}
