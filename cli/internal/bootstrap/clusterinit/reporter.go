package clusterinit

// Install-run milestone reporting.
//
// The apply engine (`Apply`) and the init orchestrator (`runApplyEngine`
// in the cobra layer) drive a fixed, ordered set of steps. Historically
// each step just wrote free-text `[apply] …` lines to `ApplyOptions.Out`
// (stdout), which streams *below* any interactive UI — the thing the
// install TUI replaces.
//
// A `StepReporter` lets a caller observe the milestone lifecycle
// (announce → start → done/skip) WITHOUT changing where the detail log
// lines go. The Bubble Tea install-run screen implements it to render a
// live checklist; the plain/CI path passes a `NopReporter` so the
// engine's stdout output is byte-for-byte unchanged (no new banner
// lines, no golden-test churn).
//
// **Contract**: the engine calls `Plan` exactly once, before any
// `Start`, with the full ordered milestone set (so a TUI can draw the
// whole checklist up front, greying out not-yet-started steps). Every
// `Start(id)` is followed by exactly one terminal event for that id —
// `Done(id, err)` (err==nil ⇒ ✓, else ✗) or `Skip(id, reason)`. Steps
// announced in `Plan` but never started render as pending; a step may
// be skipped without ever starting (e.g. flux-install under --no-push).

// StepID identifies one install milestone. Values are stable slugs
// (safe for logs/tests); titles are the human-facing labels in Step.
type StepID string

const (
	StepPrepare         StepID = "prepare"
	StepInstallPrereqs  StepID = "install-prereqs"
	StepDNS             StepID = "dns"
	StepKubeVirt        StepID = "kubevirt-eligibility"
	StepAdoptGate       StepID = "adopt-gate"
	StepNATProbe        StepID = "nat-probe"
	StepCreateRepo      StepID = "create-repo"
	StepRemote          StepID = "configure-remote"
	StepScaffold        StepID = "scaffold"
	StepCommitPush      StepID = "commit-push"
	StepFluxInstall     StepID = "flux-install"
	StepFetchKubeconfig StepID = "fetch-kubeconfig"
	StepReconcile       StepID = "reconcile"
	StepGPUInventory    StepID = "gpu-inventory"
	StepGPUOperator     StepID = "gpu-operator"
	StepGPUHAMi         StepID = "gpu-hami"
	StepGPUProduct      StepID = "gpu-product"
	StepOpenBao         StepID = "openbao-init"
	StepControllerAuth  StepID = "controller-auth"
	StepKeycloakOIDC    StepID = "keycloak-oidc"
)

// Step is one milestone: a stable ID + a human title.
type Step struct {
	ID    StepID
	Title string
}

// StepReporter receives milestone lifecycle events during an apply.
// See the package-level contract. Implementations must be safe to call
// from the engine goroutine; the TUI implementation marshals events
// onto the Bubble Tea program.
type StepReporter interface {
	// Plan announces the ordered milestone set once, before the run.
	Plan(steps []Step)
	// Start marks a milestone as in-progress.
	Start(id StepID)
	// Done marks a milestone terminal: err==nil ⇒ success, else failure.
	Done(id StepID, err error)
	// Skip marks a milestone as intentionally not run (with a reason).
	Skip(id StepID, reason string)
}

// NopReporter is the zero-behavior reporter used by the plain/CI apply
// path so engine stdout stays exactly as it was pre-reporter.
type NopReporter struct{}

func (NopReporter) Plan([]Step)         {}
func (NopReporter) Start(StepID)        {}
func (NopReporter) Done(StepID, error)  {}
func (NopReporter) Skip(StepID, string) {}

// reporterOrNop returns r, or a NopReporter when r is nil, so engine
// call sites never nil-check.
func reporterOrNop(r StepReporter) StepReporter {
	if r == nil {
		return NopReporter{}
	}
	return r
}

// InstallSteps returns the ordered milestone set an interactive apply
// will drive, given the resolved options + plan. It mirrors the
// conditionals in the cobra `runApplyEngine` + `Apply` so the TUI can
// render the exact checklist before the run starts. Kept here (next to
// the StepID consts) so the list and the engine can't silently drift;
// a cobra test asserts every announced step is one the engine actually
// visits for a representative option matrix.
//
// The finalize steps (OpenBao/Keycloak) are included only when there's
// a reconciling cluster to finalize against — i.e. a pushed commit
// (!NoPush). Under --no-push the overlay is local-only and nothing is
// live to finalize.
func InstallSteps(o InstallStepInputs) []Step {
	var steps []Step
	add := func(id StepID, title string) { steps = append(steps, Step{ID: id, Title: title}) }

	add(StepPrepare, "Prepare local session")
	if !o.NoInstallPrereqs {
		add(StepInstallPrereqs, "Install prerequisites")
	}
	add(StepDNS, "Verify DNS")
	add(StepKubeVirt, "Check KubeVirt eligibility")
	if o.Adopt {
		add(StepAdoptGate, "Verify adopt version pins")
	}
	if o.SSH {
		add(StepNATProbe, "Probe node topology (NAT)")
	}
	if o.NewRepoCreate {
		add(StepCreateRepo, "Create fleet repo")
	}
	if o.NewRepoRemote {
		add(StepRemote, "Configure fleet remote")
	}
	add(StepScaffold, "Scaffold cluster overlay")
	if o.NoPush {
		add(StepCommitPush, "Commit (local only)")
	} else {
		add(StepCommitPush, "Commit + push")
		add(StepFluxInstall, "Bootstrap Flux")
	}
	if o.SSH {
		add(StepFetchKubeconfig, "Fetch kubeconfig")
	}
	if o.Finalize {
		// Watch Flux converge the platform BEFORE the finalize steps
		// (OpenBao/Keycloak need the platform HelmReleases Ready).
		add(StepReconcile, "Reconcile platform (Flux)")
		if o.GPUEnabled {
			add(StepGPUInventory, "Apply GPU node ownership")
			add(StepGPUOperator, "Validate NVIDIA driver, toolkit + DCGM")
			if o.HAMiEnabled {
				add(StepGPUHAMi, "Validate HAMi scheduler + device plugin")
			}
			add(StepGPUProduct, "Publish GPU catalog, admission + quota support")
		}
		// openbao.Init drives init → unseal → controller-auth in ONE
		// call, so it's a single milestone (no separate controller-auth
		// step). keycloak.Init self-polls the OIDC endpoint internally.
		add(StepOpenBao, "Initialize OpenBao")
		add(StepKeycloakOIDC, "Configure Keycloak OIDC")
	}
	return steps
}

// InstallStepInputs is the minimal decision surface InstallSteps needs.
// Derived from InitOptions by the cobra layer so this package doesn't
// re-encode flag semantics.
type InstallStepInputs struct {
	NoInstallPrereqs bool
	Adopt            bool // Mode == adopt
	SSH              bool // SSHHost != "" && !NoSSH
	NewRepoCreate    bool // new-repo && !NoCreateRepo && !NoPush
	NewRepoRemote    bool // new-repo && !NoPush (origin add/set-url)
	NoPush           bool
	Finalize         bool // drive OpenBao/Keycloak after reconcile (!NoPush)
	GPUEnabled       bool // track GPU Flux layers when products are installed
	HAMiEnabled      bool // include the HAMi layer in GPU progress
}

// GPUInstallStepIDs returns the terminal milestones that must be skipped when
// a post-apply session cannot be built. Keeping this list beside InstallSteps
// prevents interrupted paths from leaving planned TUI rows pending forever.
func GPUInstallStepIDs(hami bool) []StepID {
	ids := []StepID{StepGPUInventory, StepGPUOperator}
	if hami {
		ids = append(ids, StepGPUHAMi)
	}
	return append(ids, StepGPUProduct)
}
