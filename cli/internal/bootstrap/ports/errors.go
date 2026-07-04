package ports

import "errors"

// Typed errors that adapters return + callers switch on.
//
// **Why typed errors here, not in each adapter**: a caller in the engine
// package doesn't import adapter packages (agent rule 4); it imports
// `ports` and switches on these sentinels. Adapters wrap their underlying
// errors with these sentinels via `fmt.Errorf("...: %w", ErrFoo)`.

var (
	// ErrUnknownScript is returned by ScriptRunner.Run when the named
	// ScriptKind is not in the M3-T03 registry. Surfaces as a developer
	// error (the engine asked for a script we don't ship a registration
	// for), not an operator error.
	ErrUnknownScript = errors.New("ports: unknown script kind")

	// ErrMissingRequiredEnv is returned by ScriptRunner.Run when the
	// supplied env map (after host-env passthrough merge) is missing
	// one or more keys the script's M3-T03 contract requires. Wraps
	// with a script-kind-keyed message; the wrapped error names the
	// missing key list so cobra surfaces actionable remediation
	// without the engine having to introspect the chain.
	ErrMissingRequiredEnv = errors.New("ports: script missing required env")

	// ErrInvalidArgCount is returned by ScriptRunner.Run when the
	// supplied positional args don't satisfy the script's M3-T03
	// contract (min/max bounds). Wrapped error names the script's
	// usage shape so the operator-facing message includes a fix-it
	// hint without a second lookup.
	ErrInvalidArgCount = errors.New("ports: script invalid arg count")

	// ErrNotImplementedInV1 is returned by OpenBaoClient.ApplyPolicy /
	// EnableAuthPath / WriteAuthRole when the v1 adapter chooses to
	// punt to ScriptRunner instead. Callers in M5 know to drive the
	// equivalent script when they see this.
	ErrNotImplementedInV1 = errors.New("ports: v1 wraps this via ScriptRunner; see openbao.go NB")

	// ErrFileTooLarge is returned by SSHClient.Fetch when a file
	// exceeds the 4 MiB safety cap.
	ErrFileTooLarge = errors.New("ports: remote file exceeds size limit")

	// ErrNeedsSudo is returned by SystemctlClient.Restart (and NetplanClient
	// operations) when the adapter detects it lacks the privilege to
	// complete the operation. CLI prompts the operator before retrying
	// via sudo.
	ErrNeedsSudo = errors.New("ports: operation requires sudo")

	// ErrDirty is returned by GitClient operations that require a clean
	// working tree (per agent rule 8) when the tree has uncommitted
	// changes. Caller surfaces as "fleet repo has uncommitted changes —
	// review and commit/stash first, then re-run" UNLESS --allow-dirty
	// was passed.
	ErrDirty = errors.New("ports: fleet repo has uncommitted changes")

	// ErrPlanInputDrift is returned by the M4-T02 plan applier when
	// `--apply-plan <file>` is invoked with current inputs that don't
	// match the plan's recorded InputHash. Refusing prevents
	// "I edited cluster-config.env between dry-run and apply" footguns.
	ErrPlanInputDrift = errors.New("ports: plan input hash drift — re-run --dry-run")

	// ErrNoConsent is returned when `init --no-tty` is invoked without
	// any of: --yes, --apply-plan <path>, or a matching consent-cache
	// marker from a prior --dry-run. The CLI exits non-zero and prints
	// the three valid options.
	ErrNoConsent = errors.New("ports: --no-tty requires --yes, --apply-plan, or a matching --dry-run marker")

	// ErrFluxNotInstalled is returned by K8sClient.DiscoverFluxGraph when
	// the `flux-system` Namespace is absent. Distinct from "Flux installed
	// but reconciling zero Kustomizations" (which returns no error + an
	// empty non-nil Nodes slice). Callers use errors.Is to distinguish
	// "no Flux yet" from "Flux is up but the graph is empty".
	ErrFluxNotInstalled = errors.New("ports: flux-system namespace not found")

	// ErrKubectlNotFound is returned by K8sClient.PodExecViaKubectl
	// when `kubectl` is not on $PATH. Callers (the F-bootstrap-3
	// WS-drop fallback path) use errors.Is to detect this and keep
	// the original WS-drop error rather than substitute a less
	// helpful "kubectl: command not found".
	ErrKubectlNotFound = errors.New("ports: kubectl binary not found on $PATH (fallback unavailable)")
)
