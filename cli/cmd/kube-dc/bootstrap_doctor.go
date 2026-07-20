package main

import (
	"context"
	"errors"
	"fmt"
	"io"
	"os"

	"github.com/mattn/go-isatty"
	"github.com/spf13/cobra"

	"github.com/shalb/kube-dc/cli/internal/bootstrap"
	"github.com/shalb/kube-dc/cli/internal/bootstrap/adapters/dns"
	"github.com/shalb/kube-dc/cli/internal/bootstrap/discover"
	"github.com/shalb/kube-dc/cli/internal/bootstrap/doctor"
	"github.com/shalb/kube-dc/cli/internal/bootstrap/ports"
)

// bootstrapDoctorCmd registers `kube-dc bootstrap doctor` — the
// read-only pre-flight validator from installer-ux §4.
//
// Probes run in parallel via doctor.RunAll with a per-probe 5s
// timeout. Results route through doctor.Printer in either TTY
// (lipgloss) or plain text (--no-tty / writer-not-a-terminal /
// `NO_COLOR=1`) mode. Exit code is the max Severity across all
// probes (0 Info, 1 Warn, 2 Blocker, 3 Fatal).
//
// **Hermeticity under KUBE_DC_MOCK**: the session's `Probes`
// factory is either `discover.RealFactory` (real session) or
// `mock.Factory{Scenario}` (mock session). The cobra command
// reads from whichever the session provides — so a doctor run
// against `KUBE_DC_MOCK=cloud` produces the same output on every
// developer's laptop regardless of their local kubectl/gh/etc.
// installation state.
func bootstrapDoctorCmd(fleetRepo *string) *cobra.Command {
	var (
		noTTY          bool
		hostProbes     bool
		domain         string
		nodeExternalIP string
	)

	cmd := &cobra.Command{
		Use: "doctor",
		// Doctor renders its own report + footer. When RunE returns
		// a doctorExitCodeErr, we don't want cobra to re-print
		// "Error: ..." + the usage block on top of our clean output.
		SilenceErrors: true,
		SilenceUsage:  true,
		Short:         "Pre-flight check the operator's environment for bootstrap",
		Long: `Doctor runs a read-only validation pass across:

  - Physical world           (host kernel modules, NICs, DNS the
                              operator must wire up themselves)
  - Accelerators             (PCI class/variant, driver, IOMMU,
                              VFIO and KVM readiness)
  - Auto-handled by CLI      (kubectl/flux/sops/age/git/gh/ssh/bao
                              binaries the CLI offers to install)
  - CLI verifies + suggests  (gh auth scopes, age key recipients,
                              other things the CLI checks but
                              cannot fix without a decision)

Every probe is read-only. The doctor process exit code is the
highest Severity across all probes: 0 Info-only, 1 ≤Warn, 2
Blocker, 3 Fatal. Use this in CI to gate ` + "`init`" + `:

  kube-dc bootstrap doctor --no-tty --domain acme.example.com \\
    && kube-dc bootstrap init --no-tty --domain acme.example.com

When stdout is a TTY (and --no-tty isn't set), output is colourised
via lipgloss. Honours NO_COLOR=1 per no-color.org.`,
		Example: `  # Mock-backed run against the canonical "cloud" scenario
  KUBE_DC_MOCK=cloud kube-dc bootstrap doctor --no-tty

  # Real run with host probes enabled (treat the laptop as install target)
  kube-dc bootstrap doctor --host-probes --domain acme.example.com \\
    --node-external-ip 213.111.154.233`,
		RunE: func(cmd *cobra.Command, _ []string) error {
			session, err := bootstrap.NewSession(bootstrap.Options{
				FleetRepoPath: *fleetRepo,
			})
			// ErrRealAdaptersNotReady means "no kubeconfig" — we
			// still want to run host + tool + DNS probes (none of
			// those need a working kubeconfig). Any other error is
			// fatal.
			if err != nil && !errors.Is(err, bootstrap.ErrRealAdaptersNotReady) {
				return err
			}
			if session != nil {
				defer session.Close()
			}

			ctx := cmd.Context()
			if ctx == nil {
				ctx = context.Background()
			}

			probes := assembleProbes(session, hostProbes, domain, nodeExternalIP)
			results := doctor.RunAll(ctx, probes)

			useNoTTY := noTTY || !isWriterTTY(cmd.OutOrStdout())
			printer := &doctor.Printer{
				Out:         cmd.OutOrStdout(),
				NoTTY:       useNoTTY,
				NextCommand: recommendNext(results, domain),
			}
			exitCode := printer.Print(results)
			if exitCode != 0 {
				return &doctorExitCodeErr{code: exitCode}
			}
			return nil
		},
	}

	cmd.Flags().BoolVar(&noTTY, "no-tty", false, "Force plain-text output even when stdout is a terminal")
	cmd.Flags().BoolVar(&hostProbes, "host-probes", false, "Run host probes (treat this machine as a Kube-DC node target)")
	cmd.Flags().StringVar(&domain, "domain", "", "Cluster domain (e.g. acme.example.com) — required for DNS probes")
	cmd.Flags().StringVar(&nodeExternalIP, "node-external-ip", "", "Operator-supplied node-external IP for DNS cross-check")

	// Subcommands. `anchors` is a sibling probe set that needs a
	// cluster name to know which gw nodes to SSH to — it doesn't
	// plug into the main probe factory because that factory works
	// against the operator's local kubeconfig, not a fleet cluster.
	cmd.AddCommand(bootstrapDoctorAnchorsCmd(fleetRepo))

	// `topology` is another sibling: classifies a cluster's
	// external-networking shape (Class A/B/C) and recommends
	// whether to enable Internal Platform Endpoints. It does NOT
	// need a fleet repo or SSH — it shells out to kubectl against
	// the current kubeconfig — but is still a subcommand rather
	// than a main-factory probe because its output shape (full
	// classification report) doesn't fit the unified probe table.
	cmd.AddCommand(bootstrapDoctorTopologyCmd())

	// --post-rke2 was specified in the M1-T06 plan but its
	// semantics (relax cluster probes for fresh-RKE2 NotReady
	// nodes) depend on the cluster-probe surface from M2-T01,
	// which hasn't landed yet. Flag is intentionally NOT
	// registered until cluster probes exist — shipping an inert
	// flag would create a UX contract gap that operators discover
	// only by running it and seeing nothing change.
	return cmd
}

// doctorExitCodeErr lets RunE communicate a non-zero exit code
// without cobra prepending "Error:" to a non-error doctor summary.
// main.go switches on this type and calls os.Exit accordingly.
type doctorExitCodeErr struct{ code int }

func (e *doctorExitCodeErr) Error() string { return fmt.Sprintf("doctor exit %d", e.code) }
func (e *doctorExitCodeErr) ExitCode() int { return e.code }

// assembleProbes collects every probe + its category from the
// session's `Probes` factory. When the session is nil (no
// kubeconfig resolved + no mock scenario), falls back to a real
// factory + a standalone DNS adapter — DNS validation doesn't
// need a working cluster and should be available even on a fresh
// developer laptop with no kubeconfig.
func assembleProbes(session *bootstrap.Session, hostProbesOn bool, domain, nodeIP string) []doctor.CategorizedProbe {
	factory := factoryFromSession(session)

	mode := discover.HostProbeOff
	if hostProbesOn {
		mode = discover.HostProbeOn
	}

	var out []doctor.CategorizedProbe
	for _, p := range factory.Tools() {
		out = append(out, doctor.CategorizedProbe{Category: doctor.CategoryAutoHandled, Probe: p})
	}
	for _, p := range factory.Host(mode) {
		out = append(out, doctor.CategorizedProbe{Category: doctor.CategoryPhysical, Probe: p})
	}
	for _, p := range factory.Accelerators(mode) {
		out = append(out, doctor.CategorizedProbe{Category: doctor.CategoryAccelerators, Probe: p})
	}

	// M6-T03: cluster-scope probes (NFD reader). These need a
	// K8sClient; skipped silently when the session has none so a
	// pre-kubeconfig `doctor` invocation still renders tool + host +
	// DNS sections cleanly. Category is Physical — NFD surfaces
	// facts about the physical / VM hosts backing the cluster
	// (kubevirt-eligible, GPU presence), which belongs with the
	// other physical-world signals.
	if k8s := k8sFromSession(session); k8s != nil {
		for _, p := range factory.Cluster(k8s) {
			out = append(out, doctor.CategorizedProbe{Category: doctor.CategoryPhysical, Probe: p})
		}
	}

	// M5-T07: openbao-scope probes (currently just the policy-
	// generation drift probe). Category is VerifiesSuggests — drift
	// is a soft signal the CLI verifies + points at a fix
	// (setup-controller-auth --refresh-policy); the operator decides
	// when to run it. Nil-safe: skipped silently when no
	// OpenBaoClient (no kubeconfig / no session).
	if bao := openBaoFromSession(session); bao != nil {
		for _, p := range factory.ClusterOpenBao(bao) {
			out = append(out, doctor.CategorizedProbe{Category: doctor.CategoryVerifiesSuggests, Probe: p})
		}
	}

	// DNS probes don't need a kubeconfig-backed session — they use
	// the system resolver. If the session has a DNSClient (mock or
	// real), prefer it (mock scenarios resolve against fixture
	// data); otherwise spin up a standalone real DNS adapter.
	if domain != "" {
		dnsClient := dnsFromSession(session)
		for _, p := range factory.DNS(domain, nodeIP, dnsClient) {
			out = append(out, doctor.CategorizedProbe{Category: doctor.CategoryVerifiesSuggests, Probe: p})
		}
	}

	return out
}

// k8sFromSession returns the session's K8sClient or nil when the
// session (or its K8s field) isn't wired. Mirrors dnsFromSession
// but no standalone fallback — cluster probes are meaningless
// without a real apiserver, so nil is the right signal for
// assembleProbes to skip them.
func k8sFromSession(session *bootstrap.Session) ports.K8sClient {
	if session == nil {
		return nil
	}
	return session.K8s
}

// openBaoFromSession returns the session's OpenBaoClient or nil.
// Same nil-safe convention as k8sFromSession — openbao cluster
// probes are meaningless without a live adapter.
func openBaoFromSession(session *bootstrap.Session) ports.OpenBaoClient {
	if session == nil {
		return nil
	}
	return session.OpenBao
}

// factoryFromSession returns the session's probe factory or a real
// factory when no session is available.
func factoryFromSession(session *bootstrap.Session) discover.Factory {
	if session != nil && session.Probes != nil {
		return session.Probes
	}
	return discover.RealFactory{}
}

// dnsFromSession returns the session's DNS adapter or a fresh
// standalone real adapter when no session is available.
func dnsFromSession(session *bootstrap.Session) ports.DNSClient {
	if session != nil && session.DNS != nil {
		return session.DNS
	}
	return dns.New()
}

// recommendNext returns a one-line "Next: <cmd>" suggestion for the
// printer's footer. Logic is intentionally simple in v1 — full M4
// init mode-detection lands later.
func recommendNext(results []doctor.NamedResult, domain string) string {
	hasBlocker := false
	for _, r := range results {
		if r.Result.Severity >= ports.SeverityBlocker {
			hasBlocker = true
			break
		}
	}
	if hasBlocker {
		if domain != "" {
			return fmt.Sprintf("address the blockers above, then `kube-dc bootstrap doctor --domain %s`", domain)
		}
		return "address the blockers above, then re-run `kube-dc bootstrap doctor`"
	}
	if domain != "" {
		return fmt.Sprintf("kube-dc bootstrap init --domain %s", domain)
	}
	return "kube-dc bootstrap init --domain <your-domain>"
}

// isWriterTTY reports whether `w` is an *os.File pointing at a
// terminal. Honours the caller-supplied writer (e.g. cmd.OutOrStdout)
// so embedded usage (tests, library consumers) doesn't get coloured
// output meant for an interactive terminal. Falls back to "not a
// TTY" for any writer type that isn't *os.File.
func isWriterTTY(w io.Writer) bool {
	f, ok := w.(*os.File)
	if !ok {
		return false
	}
	return isatty.IsTerminal(f.Fd()) || isatty.IsCygwinTerminal(f.Fd())
}
