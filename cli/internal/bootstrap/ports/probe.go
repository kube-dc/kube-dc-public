package ports

import "context"

// Status describes the state of a single probed thing in the operator's
// environment. The vocabulary is deliberately small — every probe collapses
// its world into one of these four values plus an optional FixHint.
//
//	missing    nothing there
//	partial    present but incomplete (e.g. RKE2 installed, kubeconfig missing)
//	installed  present, not managed by us (a foreign cert-manager Helm install,
//	           an existing K8s cluster we'd adopt)
//	managed    present AND reconciled by Flux from this fleet repo
//
// Status is the *state*, not the *severity*. Severity is derived per-probe
// (see Result.Severity) and depends on which CLI mode is asking — a missing
// Namespace/flux-system is `Info` for `doctor`, `Blocker` for `init --resume`.
type Status string

const (
	StatusMissing   Status = "missing"
	StatusPartial   Status = "partial"
	StatusInstalled Status = "installed"
	StatusManaged   Status = "managed"
)

// Severity is the actionable weight of a probe Result. `doctor`'s exit code
// is the max severity across all probes (Info=0, Warn=1, Blocker=2, Fatal=3).
// `init` refuses to mutate when any probe returns Blocker (unless an explicit
// opt-out flag is supplied).
//
// Severity is computed by each probe based on its Status + the CLI mode
// (doctor / doctor --post-rke2 / init). The mapping table lives in
// docs/prd/installer-agentic-implementation-plan.md §6.
type Severity int

const (
	SeverityInfo    Severity = iota // benign signal; not actionable
	SeverityWarn                    // worth surfacing; not blocking
	SeverityBlocker                 // init refuses to proceed without it
	SeverityFatal                   // unrecoverable — exit 3, no retry hint
)

// Probe runs a single read-only check against the operator's environment.
// Probe implementations live in `cli/internal/bootstrap/discover/`; the
// ports package owns only the contract.
//
// All probes MUST:
//   - Be read-only (no mutations to host, cluster, fleet, or anywhere else)
//   - Honour context cancellation (return immediately on `<-ctx.Done()`)
//   - Return within a per-probe timeout (the probe set runner caps at 5s
//     each so a slow probe doesn't block the whole report)
//   - Never log secret material — log the field name only, not the value
type Probe interface {
	// Name is the stable identifier shown in the doctor printer's rows
	// (e.g. "kubectl", "rke2-server", "wildcard-dns"). Used as the
	// snapshot-test key in M0-T03 scenarios — stable across releases.
	Name() string

	// Run executes the probe and returns its Result. Result.Status is
	// always populated. Result.FixHint is populated only when there's a
	// concrete suggestion the operator can act on.
	Run(ctx context.Context) Result
}

// Result is what a Probe produces. The fields are deliberately small so the
// doctor printer (M1-T05) can render any probe with the same template.
type Result struct {
	// Status is the state. Required.
	Status Status

	// Severity is the per-probe-per-mode actionable weight. Computed by
	// the probe (not the printer — the printer is mode-agnostic).
	Severity Severity

	// Version is a tool/component version string if applicable
	// (e.g. "v1.32.1+rke2r1", "v2.4.0"). Empty otherwise.
	Version string

	// Detail is a one-line human-readable summary shown in the doctor
	// printer's middle column. Plain prose; no Markdown, no colour codes
	// (the printer applies styling).
	Detail string

	// FixHint is an optional next-step suggestion. Populated only when
	// Status is missing/partial and there's a concrete fix.
	FixHint FixHint
}

// FixHint is what the doctor printer renders under a probe's row when the
// probe has a concrete suggestion. The Text field is always populated; the
// Action field is populated only when the TUI can dispatch the fix as a
// one-keypress action (Enter on the row).
//
// See installer-prd.md §9.9.3 "Actionable status hints".
type FixHint struct {
	// Text is the human-readable suggestion. Required when FixHint is
	// populated. Multi-line allowed (printer reflows).
	Text string

	// Action is the structured form of the fix the TUI can dispatch via
	// `tea.ExecProcess` or an in-process call. Empty when the fix can't
	// be automated (e.g. "wire DNS in your provider" — operator's job).
	Action *FixAction

	// Records is populated when the fix is "create these DNS records".
	// Doctor printer renders this as a copy-paste block. Empty otherwise.
	// See B-003 in installer-agentic-implementation-plan.md.
	Records []DNSRecord
}

// FixAction is the machine-readable form of a FixHint. Distinct from
// `discover.FixAction` (which is the cluster-row-state variant shipped with
// T2/T2a) — that one will fold into this once the probe-engine refactor lands.
type FixAction struct {
	Kind FixActionKind

	// Cluster / Domain / Org are populated based on Kind. See
	// installer-prd.md §9.9.3 examples.
	Cluster string
	Domain  string
	Org     string

	// Cmd is populated when Kind=RunCommand. The TUI dispatches via
	// `tea.ExecProcess` after operator confirmation; never silently.
	Cmd []string
}

// FixActionKind enumerates the action types the TUI knows how to dispatch.
// Add new values here as new fix types appear; the TUI's row-Enter handler
// grows a switch arm per Kind so unknown actions degrade gracefully (Enter
// no-ops with a footer hint instead of crashing).
type FixActionKind string

const (
	// FixActionNone: no automatable action (operator must do something
	// outside the CLI's reach — wire DNS, plug in a NIC, etc.).
	FixActionNone FixActionKind = ""

	// FixActionAdminLogin: run `kube-dc login --domain <Domain> --admin`.
	FixActionAdminLogin FixActionKind = "admin-login"

	// FixActionTenantLogin: run `kube-dc login --domain <Domain> --org <Org>`.
	FixActionTenantLogin FixActionKind = "tenant-login"

	// FixActionInstallPrereqs: run `scripts/install-prerequisites.sh` from
	// the fleet repo.
	FixActionInstallPrereqs FixActionKind = "install-prereqs"

	// FixActionRefreshPolicy: run `kube-dc bootstrap openbao refresh-policy
	// <Cluster>` to apply new OpenBao paths after a milestone upgrade.
	FixActionRefreshPolicy FixActionKind = "openbao-refresh-policy"

	// FixActionUnseal: run `kube-dc bootstrap openbao unseal <Cluster>`.
	FixActionUnseal FixActionKind = "openbao-unseal"

	// FixActionRunCommand: dispatch the literal Cmd slice via
	// tea.ExecProcess. Used sparingly — most fixes should have a dedicated
	// Kind so unknown text in Cmd doesn't surprise the operator.
	FixActionRunCommand FixActionKind = "run-command"
)
