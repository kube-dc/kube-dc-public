package ports

import (
	"context"
	"time"
)

// ScriptRunner executes fleet-repo bash scripts and streams their output as
// typed `Line` messages. This is the v1 backend for every fleet-script-driven
// step in `init` and `openbao` — see agent rule 5 of the plan.
//
// **Why an interface, not just a wrapper**: the real adapter uses os/exec;
// the mock adapter replays canned scenario YAML. Same engine, different
// driver. Future v2 may swap the real adapter for a Go-native implementation
// (e.g. flux-install.sh → flux pkg/runtime calls) without touching consumers.
//
// **Secret-stream sentinel handling** (agent rule 7 of the plan + B-003):
// when a script emits a sentinel-delimited payload (e.g. `openbao-init.sh`
// emits `KUBE_DC_INIT_JSON_BEGIN`/`END` around the 5 Shamir shares + root
// token), the ScriptRunner captures the enclosed bytes into a registered
// callback (in-memory only) and emits a single placeholder Line to the
// caller's stream — the raw payload NEVER enters the channel that drives
// the log file or TUI viewport. M5-T01 uses this to keep OpenBao shares
// from leaking into logs.
type ScriptRunner interface {
	// Run executes the named script with the supplied env vars and args.
	// Returns a channel that emits Line values until the script exits;
	// the final Line has Stream=StreamExit and Text=<exitCode>.
	//
	// The caller MUST drain the channel until it closes (otherwise the
	// adapter may leak the underlying process). Context cancellation is
	// propagated: on `<-ctx.Done()` the adapter sends SIGTERM, then
	// SIGKILL after a 5s grace period, then closes the channel.
	//
	// The `name` argument is a registered script identifier from
	// ScriptKind (M3-T03 registry); the registry validates required env
	// keys + accepted args before exec. An unknown name returns
	// (nil, ErrUnknownScript).
	//
	// Env values are scrubbed from logs per the redaction rules in
	// M0-T05 — never log a value, log "KEY=[REDACTED]" for any key
	// matching the secret regex.
	//
	// Sentinel callbacks (for secret payload capture) are configured at
	// runner construction, not per-call — see the SentinelCallback type.
	// Per-invocation overrides go through WithSentinelCallback.
	Run(ctx context.Context, name ScriptKind, env map[string]string, args ...string) (<-chan Line, error)

	// WithSentinelCallback returns a copy of the runner with the
	// sentinel callback replaced. Used by M5-T01 to attach the
	// share-capture callback for the single `openbao-init.sh`
	// invocation while leaving the surrounding session unchanged —
	// the session's runner stays callback-nil (sentinel payloads
	// dropped) so a stray openbao-init invocation outside the M5
	// engine doesn't accidentally surface shares.
	WithSentinelCallback(cb SentinelCallback) ScriptRunner
}

// ScriptKind enumerates the fleet-repo scripts the runner knows how to
// invoke. The registry (M3-T03) maps each Kind to:
//   - the script's relative path inside the fleet repo
//   - the required env keys (validated before exec)
//   - the accepted positional args (validated before exec)
//   - the sentinel patterns to capture (e.g. ScriptOpenBaoInit emits the
//     KUBE_DC_INIT_JSON_BEGIN/END pair)
//
// Adding a new script means: add a Kind constant here, register it in
// M3-T03's registry, and the runner can invoke it.
type ScriptKind string

const (
	// ScriptInstallServer = bootstrap/rke2/install-server.sh (Phase 2 of
	// init in non-existing-K8s mode).
	ScriptInstallServer ScriptKind = "rke2/install-server.sh"

	// ScriptInstallAgent = bootstrap/rke2/install-agent.sh (v2 add-node).
	ScriptInstallAgent ScriptKind = "rke2/install-agent.sh"

	// ScriptGenerateAgeKey = bootstrap/generate-age-key.sh (Phase 4a,
	// only on truly fresh fleet — refused otherwise per agent rule 8).
	ScriptGenerateAgeKey ScriptKind = "generate-age-key.sh"

	// ScriptAddCluster = bootstrap/add-cluster.sh (Phase 4b greenfield
	// scaffold; agent rule 5 carves out an exception for existing-fleet
	// where the CLI scaffolds in-process from sibling templates).
	ScriptAddCluster ScriptKind = "add-cluster.sh"

	// ScriptFluxInstall = bootstrap/flux-install.sh (Phase 4e). Accepts
	// `--new-cluster` for greenfield, omits it for adopt.
	ScriptFluxInstall ScriptKind = "flux-install.sh"

	// ScriptOpenBaoInit = bootstrap/openbao-init.sh (Phase 6). Emits a
	// sentinel-delimited JSON payload with the 5 Shamir shares + root
	// token; the runner diverts that to a registered callback.
	ScriptOpenBaoInit ScriptKind = "openbao-init.sh"

	// ScriptSetupKeycloakOIDC = bootstrap/setup-keycloak-oidc.sh
	// (deferred to Phase 5 tail; runs after HelmRelease/keycloak Ready).
	ScriptSetupKeycloakOIDC ScriptKind = "setup-keycloak-oidc.sh"

	// ScriptOpenBaoSetupControllerAuth = kube-dc/hack/openbao-setup-
	// controller-auth.sh (Phase 6 + M5 refresh-policy). Accepts env
	// REFRESH_POLICY=true to rewrite policies in place on milestone
	// upgrades.
	ScriptOpenBaoSetupControllerAuth ScriptKind = "openbao-setup-controller-auth.sh"

	// ScriptAddEngineer = bootstrap/add-engineer.sh (not driven by the
	// CLI directly — surfaced as a FixHint when an operator's age key
	// isn't a .sops.yaml recipient; the keyholder runs it manually).
	// Listed here so the M3-T03 registry knows about it for `doctor`'s
	// preflight existence check.
	ScriptAddEngineer ScriptKind = "add-engineer.sh"

	// ScriptInstallPrereqs = scripts/install-prerequisites.sh.
	ScriptInstallPrereqs ScriptKind = "install-prerequisites.sh"

	// ScriptDumpClusterState = scripts/dump-cluster-state.sh (diagnostic
	// dump for support escalation; `kube-dc bootstrap status` chains to
	// this with a `--save-report` flag).
	ScriptDumpClusterState ScriptKind = "dump-cluster-state.sh"
)

// LineStream identifies which file descriptor the line came from. The
// special `StreamExit` value is emitted as the last Line in the channel
// and carries the exit code in Text (decimal-formatted).
type LineStream string

const (
	StreamStdout LineStream = "stdout"
	StreamStderr LineStream = "stderr"
	StreamExit   LineStream = "exit"
)

// Line is a single output event from a running script.
type Line struct {
	Stream LineStream
	Text   string
	Time   time.Time
}

// SentinelCallback is invoked when ScriptRunner sees a configured
// sentinel-delimited payload in a script's output. The callback receives
// the enclosed bytes (without the sentinel markers) and is responsible for
// copying anything it needs into a `secrets.Buffer` (M0-T04). The byte
// slice MUST NOT be retained beyond the callback's lifetime — the runner
// is free to scrub or overwrite it after the callback returns.
//
// A non-nil error tells the runner the capture failed (parse error,
// buffer allocation failure, downstream validation failed, …). On error
// the runner MUST:
//   - terminate the script process if it's still running (SIGTERM then
//     SIGKILL after 5s grace, same as ctx.Cancel)
//   - emit a final `StreamExit` Line with a non-zero exit text so the
//     caller's drain loop sees the failure
//   - NEVER emit the raw payload (the whole point of the sentinel path
//     is that the payload never reaches the regular stream)
//   - return the callback's error from the original ScriptRunner.Run
//     channel close (the caller `<-chan` close-detection sees a final
//     Line with the callback's error message in Text)
//
// When the callback returns nil, the runner emits a single placeholder
// Line to the caller's stream (something like `[OpenBao init payload
// captured — 5 shares, root token]`) so the operator sees that
// *something* happened, without ever exposing the raw bytes.
type SentinelCallback func(scriptName ScriptKind, sentinel string, payload []byte) error
