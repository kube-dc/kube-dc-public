package clusterinit

import (
	"context"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"regexp"
	"sort"
	"strings"

	"github.com/shalb/kube-dc/cli/internal/bootstrap/config"
	"github.com/shalb/kube-dc/cli/internal/bootstrap/ports"
)

// M4-T10 — scaffold the new cluster overlay.
//
// **What this slice does**:
//
//   1. Preflight: refuse if `<repo>/clusters/<name>/` already exists.
//   2. Run `bootstrap/add-cluster.sh <name> <domain> <node-external-ip>`
//      via the supplied `ports.ScriptRunner`. The script writes
//      cluster-config.env + secrets.enc.yaml + Flux Kustomization
//      manifests under `<repo>/clusters/<name>/`.
//   3. **Redact script stdout** before the lines reach the
//      caller's `Out` writer. The fleet's add-cluster.sh echoes
//      the freshly-generated KEYCLOAK/GRAFANA/MINIO passwords at
//      the end ("==> Generated passwords"); the redactor catches
//      those lines and rewrites the value portion. Without this
//      the operator's terminal + any captured CI log would carry
//      the plaintext credentials.
//   4. **Verify secrets.enc.yaml is SOPS-encrypted**. The script
//      falls back to writing plaintext when sops + age aren't
//      available, exiting 0 with a WARNING. T10's contract is to
//      refuse this fallback: an unencrypted secrets.enc.yaml would
//      be committed to the fleet repo on the apply path, which is
//      a hard "no" per installer-prd §12.3.
//   5. Post-process the generated cluster-config.env: apply
//      preset's resolved env-map (which already includes operator
//      --set deltas) + the plan's InheritedDefaults version pins.
//      Order + comments preserved via `config.Env`'s in-place
//      Set semantics.
//
// **What this slice does NOT do** (deferred to other slices):
//
//   - M4-T11 customInterfaces patch (writes infrastructure.yaml
//     patches when --node-nic is set).
//   - M4-T12 commit + push + flux-install.sh (the post-scaffold
//     fleet transaction).

// ScaffoldOptions is the parameter bundle for Scaffold. Built by
// the cobra layer's apply path from the loaded Plan; tests build
// it directly with a fake ScriptRunner.
type ScaffoldOptions struct {
	// Plan is the previously-validated init plan. Scaffold reads
	// ClusterName, Domain, Preset, InheritedDefaults from it; never
	// re-derives from fleet state (per the apply-plan verbatim
	// contract documented in plan.go).
	Plan *Plan

	// FleetRepo is the absolute path of the fleet repo on disk
	// (e.g. ~/projects/kube-dc-fleet). Scaffold writes under
	// `<FleetRepo>/clusters/<Plan.ClusterName>/`.
	FleetRepo string

	// NodeExternalIP is the IP the script needs as its third
	// positional arg. Not stored on Plan directly so we pass it
	// separately; the cobra layer reads it from o.NodeExternalIP.
	NodeExternalIP string

	// Sets is the resolved operator --set overrides from
	// InitOptions. Layered on top of the preset's defaults during
	// post-process; the Plan struct surfaces overrides via
	// FilesToWrite descriptions but doesn't carry the raw KEY=VALUE
	// map, so the caller (cobra layer) passes them separately.
	Sets map[string]string

	// NodeNICs is the operator --node-nic map (cluster-node-name →
	// primary NIC iface). Triggers the M4-T11 customInterfaces
	// patch step when non-empty; no-op when empty (homogeneous-NIC
	// fleets don't need the patch).
	NodeNICs map[string]string

	// Runner is the ports.ScriptRunner the engine calls. Real flow
	// uses the script adapter; tests use a fake.
	Runner ports.ScriptRunner

	// Out is where redacted stdout + status lines go. nil = ioutil.Discard.
	Out io.Writer
}

// --- Errors ---

// ErrScaffoldTargetExists is returned by Scaffold when
// `clusters/<name>/cluster-config.env` already exists. Marker file —
// not the dir — is the canonical "already scaffolded" signal so
// operators can pre-place a `docs/` README inside the overlay before
// running bootstrap.
var ErrScaffoldTargetExists = errors.New("init: scaffold target already initialised (cluster-config.env present)")

// ErrScaffoldScriptFailed is returned when add-cluster.sh exits
// non-zero. The error wraps the exit code + last few stderr lines.
var ErrScaffoldScriptFailed = errors.New("init: scaffold script failed")

// ErrScaffoldSecretsNotEncrypted is returned when the script
// completes but secrets.enc.yaml is plaintext on disk (the
// script's sops-not-available fallback path). Hard "no" — the
// caller MUST not commit a plaintext credential file to the fleet
// repo.
var ErrScaffoldSecretsNotEncrypted = errors.New("init: secrets.enc.yaml is unencrypted (sops fallback path triggered — refuse to proceed)")

// --- Engine ---

// Scaffold runs the add-cluster.sh script + post-processes the
// generated cluster-config.env. Returns nil on success, a typed
// error otherwise. The new cluster overlay lives at
// `<opts.FleetRepo>/clusters/<opts.Plan.ClusterName>/` when this
// returns.
func Scaffold(ctx context.Context, opts ScaffoldOptions) error {
	if opts.Plan == nil {
		return fmt.Errorf("scaffold: nil Plan")
	}
	if opts.FleetRepo == "" {
		return fmt.Errorf("scaffold: empty FleetRepo")
	}
	if opts.Runner == nil {
		return fmt.Errorf("scaffold: nil ScriptRunner")
	}
	if opts.NodeExternalIP == "" {
		return fmt.Errorf("scaffold: empty NodeExternalIP")
	}
	out := opts.Out
	if out == nil {
		out = io.Discard
	}

	// (1) Preflight: refuse if the marker file already exists.
	// We check `cluster-config.env` rather than the bare directory
	// because operators sometimes pre-place a `docs/` folder in the
	// overlay before running bootstrap (e.g. topology notes). The
	// canonical "this overlay was already scaffolded" signal is the
	// presence of cluster-config.env.
	// Cluster name can contain a slash (cs/zrh shape) — filepath.Join
	// handles that correctly without escaping the fleet root.
	clusterDir := filepath.Join(opts.FleetRepo, "clusters", opts.Plan.ClusterName)
	marker := filepath.Join(clusterDir, "cluster-config.env")
	if _, err := os.Stat(marker); err == nil {
		return fmt.Errorf("%w: %s", ErrScaffoldTargetExists, marker)
	} else if !errors.Is(err, os.ErrNotExist) {
		return fmt.Errorf("scaffold: stat %s: %w", marker, err)
	}

	// (2) Run the script via ScriptRunner. Args mirror the script's
	// `<name> <domain> <node-external-ip> [kubeconfig-path]` shape;
	// kubeconfig-path is optional and defaults to
	// ~/.kube/<name>_config inside the script — we don't pass it
	// because the CLI's apply path manages kubeconfigs through the
	// kubeconfig package, not the script's default.
	lines, err := opts.Runner.Run(ctx, ports.ScriptAddCluster, nil,
		opts.Plan.ClusterName, opts.Plan.Domain, opts.NodeExternalIP)
	if err != nil {
		return fmt.Errorf("scaffold: start add-cluster.sh: %w", err)
	}

	// (3) Drain + redact. Track the exit code line (StreamExit) and
	// surface non-zero as ErrScaffoldScriptFailed.
	exitCode, drainErr := drainAndRedactAddCluster(lines, out)
	if drainErr != nil {
		return fmt.Errorf("scaffold: drain: %w", drainErr)
	}
	if exitCode != 0 {
		return fmt.Errorf("%w (exit=%d)", ErrScaffoldScriptFailed, exitCode)
	}

	// (4) Verify secrets.enc.yaml is sops-encrypted. The script's
	// fallback path silently leaves plaintext; we refuse before any
	// downstream commit/push.
	secretsPath := filepath.Join(clusterDir, "secrets.enc.yaml")
	if err := verifySOPSEncrypted(secretsPath); err != nil {
		return err
	}

	// (5) Post-process cluster-config.env: apply preset defaults +
	// operator --set overrides + inherited version pins. Order and
	// comments preserved via config.Env's in-place Set.
	envPath := filepath.Join(clusterDir, "cluster-config.env")
	if err := postProcessClusterConfig(envPath, opts.Plan, opts.Sets); err != nil {
		return fmt.Errorf("scaffold: post-process %s: %w", envPath, err)
	}

	// (6) M4-T11 customInterfaces patch — apply the inline Kustomize
	// patch when the operator supplied --node-nic mappings. No-op
	// when NodeNICs is empty (homogeneous-NIC fleets don't need
	// the patch).
	if len(opts.NodeNICs) > 0 {
		infraPath := filepath.Join(clusterDir, "infrastructure.yaml")
		if err := WriteCustomInterfacesPatch(infraPath, opts.NodeNICs); err != nil {
			return fmt.Errorf("scaffold: customInterfaces patch: %w", err)
		}
		fmt.Fprintf(out, "[scaffold] customInterfaces patch applied (%d nodes)\n", len(opts.NodeNICs))
	}

	fmt.Fprintf(out, "[scaffold] cluster overlay created at %s\n", clusterDir)
	return nil
}

// --- Stdout redaction ---

// addClusterPasswordPrefix matches the password lines the script
// echoes ("    KEYCLOAK_ADMIN_PASSWORD: xyz"). The capture group
// is the leading indent + KEY: portion that we keep verbatim; the
// trailing value is replaced with the redaction sentinel. We match
// both single-space and multi-space-after-colon variants ("KEY:
// value" + "KEY:  value") because the script's column-aligned
// output uses one or two spaces.
//
// The pattern intentionally excludes the value to match — matching
// arbitrary trailing content is what makes the redaction safe
// even if the value contains characters that would break a stricter
// pattern.
var addClusterPasswordPrefix = regexp.MustCompile(`^(\s+[A-Z_]+_PASSWORD:\s+)\S.*$`)

// redactAddClusterLine returns the redacted form of `line` when it
// matches the password-echo pattern, or `line` unchanged otherwise.
// Exported for testing.
func redactAddClusterLine(line string) string {
	if loc := addClusterPasswordPrefix.FindStringSubmatchIndex(line); loc != nil {
		// loc[2:4] is the capture group's start/end indices.
		prefix := line[loc[2]:loc[3]]
		return prefix + "[REDACTED — see secrets.enc.yaml]"
	}
	return line
}

// drainAndRedactAddCluster reads `lines` until the channel closes,
// writes each (redacted) line to `out`, and returns the exit code
// extracted from the final StreamExit line. Returns 0 with nil err
// when the channel closes without an explicit exit line (i.e. the
// adapter terminated cleanly with no exit signal — defensive
// fallback; real adapter always emits one).
func drainAndRedactAddCluster(lines <-chan ports.Line, out io.Writer) (int, error) {
	exitCode := 0
	for ln := range lines {
		if ln.Stream == ports.StreamExit {
			n, err := parseExitCode(ln.Text)
			if err != nil {
				return 0, fmt.Errorf("parse exit code %q: %w", ln.Text, err)
			}
			exitCode = n
			continue
		}
		text := redactAddClusterLine(ln.Text)
		streamTag := "stdout"
		if ln.Stream == ports.StreamStderr {
			streamTag = "stderr"
		}
		fmt.Fprintf(out, "[%s] %s\n", streamTag, text)
	}
	return exitCode, nil
}

// parseExitCode parses the integer-as-string emitted by the
// adapter's StreamExit line. Returns the int + nil for valid
// input; the explicit helper keeps the supervise contract
// (`Line{Stream: StreamExit, Text: strconv.Itoa(code)}`) decoded
// in one place.
func parseExitCode(s string) (int, error) {
	// Reject the empty string explicitly — strconv.Atoi("") returns
	// an error too, but the wrapping message gets sharper here.
	if strings.TrimSpace(s) == "" {
		return 0, fmt.Errorf("empty exit code")
	}
	n := 0
	if _, err := fmt.Sscanf(s, "%d", &n); err != nil {
		return 0, err
	}
	return n, nil
}

// --- SOPS encryption verification ---

// verifySOPSEncrypted returns nil when `path`'s content looks
// SOPS-encrypted; ErrScaffoldSecretsNotEncrypted otherwise.
//
// **Detection strategy**: a SOPS-encrypted YAML file always
// contains a top-level `sops:` mapping with `mac:` and per-recipient
// `enc:` blobs. Plaintext secrets.enc.yaml lacks all three. We
// search for both `sops:` (at column 0) AND `ENC[AES256_GCM,` —
// either being absent means the file isn't encrypted.
func verifySOPSEncrypted(path string) error {
	body, err := os.ReadFile(path)
	if err != nil {
		return fmt.Errorf("scaffold: read %s: %w", path, err)
	}
	bodyStr := string(body)

	// Top-level `sops:` mapping at column 0 (multi-line check).
	hasSopsBlock := strings.Contains(bodyStr, "\nsops:") || strings.HasPrefix(bodyStr, "sops:")
	hasEncMarker := strings.Contains(bodyStr, "ENC[AES256_GCM,")

	if hasSopsBlock && hasEncMarker {
		return nil
	}
	// Build a helpful error: name the path + remediation.
	return fmt.Errorf("%w: %s — run `sops -e -i %s` after configuring an age key in .sops.yaml",
		ErrScaffoldSecretsNotEncrypted, path, path)
}

// --- cluster-config.env post-processing ---

// postProcessClusterConfig applies the preset's resolved env map +
// the plan's InheritedDefaults to the env file generated by
// add-cluster.sh. Existing keys are updated in-place; new keys are
// appended at the end under a dedicated comment section.
//
// **Why preset defaults flow through too**: add-cluster.sh writes
// CHANGEME placeholders for VLAN_ID + INTERFACE; the preset's
// resolved env map carries the real operator-supplied values via
// --set. Without this step the committed env would still say
// CHANGEME.
//
// **Why inherited defaults flow through**: existing-fleet mode
// derives version pins from siblings (M4-T13). The new cluster's
// env should pin to those values so it joins the fleet at the
// same upgrade-cycle position.
//
// Order: preset+set values WIN over inherited defaults (so operator
// explicit override beats fleet inheritance), which WIN over the
// script's CHANGEME defaults. config.Env.Set preserves the
// original line position when a key already exists; otherwise
// appends.
func postProcessClusterConfig(path string, plan *Plan, sets map[string]string) error {
	env, err := config.LoadEnv(path)
	if err != nil {
		return err
	}

	// Build the merged map: preset defaults → inherited defaults →
	// operator --set deltas (operator wins). EnvMapFor handles
	// preset defaults + --set merge for us; layer inherited on top
	// only when the operator didn't override.
	merged, err := EnvMapFor(plan.Preset, sets)
	if err != nil {
		return fmt.Errorf("scaffold: EnvMapFor: %w", err)
	}
	for k, v := range plan.InheritedDefaults {
		if _, set := sets[k]; set {
			continue // operator override beats inheritance
		}
		// Use the inherited value only if it's not in the
		// preset's defaults (preset defaults already cover
		// network shape; inherited defaults are version pins).
		merged[k] = v
	}

	// Walk merged in stable key order so the file written when a
	// new key is appended is deterministic across runs (Go map
	// iteration is randomised — without sorting, two consecutive
	// scaffold runs against the same inputs could produce
	// different file orderings, breaking diff review).
	keys := make([]string, 0, len(merged))
	for k := range merged {
		keys = append(keys, k)
	}
	sort.Strings(keys)
	for _, k := range keys {
		env.Set(k, merged[k])
	}

	if err := env.Write(""); err != nil {
		return err
	}
	return nil
}
