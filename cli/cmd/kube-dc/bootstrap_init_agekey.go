package main

import (
	"context"
	"errors"
	"fmt"
	"io"
	"io/fs"
	"os"
	"path/filepath"

	"github.com/shalb/kube-dc/cli/internal/bootstrap"
	sopsadapter "github.com/shalb/kube-dc/cli/internal/bootstrap/adapters/sops"
	"github.com/shalb/kube-dc/cli/internal/bootstrap/clusterinit"
	"github.com/shalb/kube-dc/cli/internal/bootstrap/ports"
)

// M4-T09 cobra wiring — resolves the operator's age key path, derives
// the pubkey, parses the fleet's `.sops.yaml` recipients, and runs
// the enrollment check. For cloudacropolis (existing-fleet) this
// surfaces silent-pass when enrolled or a clean keyholder-action
// message when not.
//
// **Greenfield generation** (M4-T09 close, SHA `1ace3c9d`): when
// --fleet-mode=new-repo and no `<fleet>/age.key` exists, this
// wiring auto-runs `bootstrap/generate-age-key.sh` via
// `NewScriptOnly` — the operator no longer runs it manually. When
// invoked under `--dry-run`, the auto-run is suppressed (returns
// `ErrAgeKeyDryRunSkip`) so plan previews stay side-effect-free.

// resolveAgeKeyPath picks the operator's age key file per the
// installer-ux §4.2 precedence:
//
//  1. `--age-key=<path>` flag override (NOT in v1 flag surface yet —
//     reserved for future). v1 starts with slot 2.
//  2. `<fleet>/age.key` if it exists.
//  3. `$SOPS_AGE_KEY_FILE` env var.
//  4. `~/.config/sops/age/keys.txt`.
//
// Returns `(AgeKeyResolution{Path:"", Source:AgeKeySourceNone}, nil)`
// when no candidate path resolves — the caller decides whether
// that's a hard error (existing-fleet) or expected (greenfield
// generate path).
//
// **Error handling per slot** (review-pass P3): high-priority
// slots (fleet age.key, future --age-key flag) surface non-NotExist
// stat errors directly — a permission-denied or symlink-loop on
// the explicit operator-supplied path is a real signal, not a
// "skip and try the next slot" condition. Otherwise the operator
// would silently land on a different (wrong) key and get a
// confusing "not enrolled" outcome. Low-priority slots ($SOPS_AGE_KEY_FILE,
// user dir) treat any error as "skip" — those are best-effort
// fallbacks.
func resolveAgeKeyPath(o *clusterinit.InitOptions) (clusterinit.AgeKeyResolution, error) {
	// Slot 1 (--age-key flag) reserved for a future cobra flag.

	// Slot 2: <fleet>/age.key — high priority. Non-NotExist stat
	// errors propagate so operators see permission/symlink issues.
	fleetKey := filepath.Join(o.Repo, "age.key")
	st, err := os.Stat(fleetKey)
	switch {
	case err == nil && !st.IsDir():
		return clusterinit.AgeKeyResolution{Path: fleetKey, Source: clusterinit.AgeKeySourceFleetRepo}, nil
	case err != nil && !errors.Is(err, fs.ErrNotExist):
		return clusterinit.AgeKeyResolution{}, fmt.Errorf("stat %s: %w", fleetKey, err)
	}

	// Slot 3: $SOPS_AGE_KEY_FILE — low-priority fallback; silent
	// skip on any error so a misconfigured env var doesn't block.
	if envPath := os.Getenv("SOPS_AGE_KEY_FILE"); envPath != "" {
		if st, err := os.Stat(envPath); err == nil && !st.IsDir() {
			return clusterinit.AgeKeyResolution{Path: envPath, Source: clusterinit.AgeKeySourceSOPSEnvVar}, nil
		}
	}

	// Slot 4: ~/.config/sops/age/keys.txt — same low-priority
	// silent-skip semantics.
	if home, err := os.UserHomeDir(); err == nil {
		homePath := filepath.Join(home, ".config", "sops", "age", "keys.txt")
		if st, err := os.Stat(homePath); err == nil && !st.IsDir() {
			return clusterinit.AgeKeyResolution{Path: homePath, Source: clusterinit.AgeKeySourceDefaultUserDir}, nil
		}
	}

	return clusterinit.AgeKeyResolution{Source: clusterinit.AgeKeySourceNone}, nil
}

// validateAgeKeyEnrollment is the cobra-side orchestration for
// M4-T09's existing-fleet branch. It:
//
//  1. Resolves the age key path per `resolveAgeKeyPath`.
//  2. Derives the operator's pubkey via the sops adapter.
//  3. Reads + parses `.sops.yaml` recipients from the fleet repo
//     root.
//  4. Calls `clusterinit.CheckAgeKeyEnrollment` for the typed
//     enrolled/not-enrolled decision.
//
// For --fleet-mode=new-repo, auto-runs
// `bootstrap/generate-age-key.sh` via ScriptRunner when the
// fleet's `age.key` is missing (M4-T09 greenfield-generate close).
// Dry-run suppresses the auto-run and returns `ErrAgeKeyDryRunSkip`
// which the RunE downgrades to a warning. For non-existing-fleet
// modes (existing-repo), this is a no-op — the engine slice that
// adopts an empty repo will handle key setup on its own timeline.
//
// On success (enrolled), prints "[sops] ✓ enrolled (pubkey=… via
// <source>)" so operators see which key the CLI consulted.
func validateAgeKeyEnrollment(ctx context.Context, out io.Writer, o *clusterinit.InitOptions, mutationsAllowed bool) error {
	switch o.FleetMode {
	case clusterinit.FleetNewRepo:
		// M4-T09 greenfield-generate close: if the fleet already
		// carries an age.key + .sops.yaml (operator pre-generated,
		// OR the fleet-starter was extracted into --repo), treat
		// like existing-fleet for the enrollment check — silent
		// pass when the operator's pubkey is a recipient.
		//
		// If the age.key is missing, attempt auto-generation via
		// `bootstrap/generate-age-key.sh` when the script IS
		// available in --repo. Missing script surfaces a specific
		// error naming the fleet-starter extraction step (which is
		// M4-T10 territory; auto-run of that is a separate slice).
		//
		// **Dry-run must NOT mutate** — reviewer P1 fix. The
		// caller passes `mutationsAllowed=!o.DryRun`; when false
		// AND the file is missing, we return `ErrAgeKeyDryRunSkip`
		// so the RunE can downgrade to a WARNING and let the plan
		// preview render. The generate script would otherwise
		// create `age.key` + `.sops.yaml` on the operator's disk,
		// violating the dry-run "no side effects" contract.
		fleetKey := filepath.Join(o.Repo, "age.key")
		if _, err := os.Stat(fleetKey); errors.Is(err, fs.ErrNotExist) {
			if !mutationsAllowed {
				return fmt.Errorf("%w (fleet=%s)", clusterinit.ErrAgeKeyDryRunSkip, o.Repo)
			}
			if genErr := autoGenerateAgeKey(ctx, out, o); genErr != nil {
				return genErr
			}
			// generation succeeded → fall through to enrollment
			// check. The script produces .sops.yaml too so the
			// downstream ParseSOPSConfigRecipients works.
		}
		// Fall through to the enrollment check (either pre-existing
		// key or freshly generated).
	case clusterinit.FleetExistingFleet:
		// Fall through to the enrollment check.
	default:
		// existing-repo (uncommon) — no fleet-level age contract to
		// check yet. Pass through silently.
		return nil
	}

	res, err := resolveAgeKeyPath(o)
	if err != nil {
		return fmt.Errorf("resolve age key path: %w", err)
	}
	if res.Path == "" {
		return fmt.Errorf("%w (checked: <fleet>/age.key, $SOPS_AGE_KEY_FILE, ~/.config/sops/age/keys.txt)",
			clusterinit.ErrAgeKeyNotFound)
	}

	// Derive the operator's pubkey from the key file. The sops
	// adapter's DerivePubKey parses the `# public key:` comment
	// canonical age-keygen writes.
	sops := sopsadapter.New()
	pubkey, err := sops.DerivePubKey(res.Path)
	if err != nil {
		return fmt.Errorf("derive pubkey from %s: %w", res.Path, err)
	}

	// Read + parse the fleet's .sops.yaml.
	configPath := filepath.Join(o.Repo, ".sops.yaml")
	body, err := os.ReadFile(configPath)
	if err != nil {
		if errors.Is(err, os.ErrNotExist) {
			return fmt.Errorf("%w (looked at %s)", clusterinit.ErrSOPSConfigMissing, configPath)
		}
		return fmt.Errorf("read %s: %w", configPath, err)
	}
	recipients := clusterinit.ParseSOPSConfigRecipients(body)
	if len(recipients) == 0 {
		return fmt.Errorf("%s lists no age recipients (malformed fleet?)", configPath)
	}

	// Operator name for the printed remediation. Use the local
	// username for an operator-actionable command (or empty so
	// the pure-layer falls back to `<your-name>`).
	opName := ""
	if u := os.Getenv("USER"); u != "" {
		opName = u
	}

	if err := clusterinit.CheckAgeKeyEnrollment(pubkey, opName, recipients); err != nil {
		return err
	}

	fmt.Fprintf(out, "[sops] enrolled (pubkey=%s via %s)\n", pubkey, res.Source)
	return nil
}

// autoGenerateAgeKey runs `bootstrap/generate-age-key.sh` from the
// fleet repo — the M4-T09 greenfield-generate close. Called when
// `--fleet-mode=new-repo` and no `<fleet>/age.key` exists yet.
//
// **Preconditions**: the script must be present in the fleet repo.
// In practice that means either (a) the operator already cloned or
// extracted a fleet-starter into --repo, or (b) they're re-running
// a partial init and the scaffold step from a prior run left the
// script behind. When neither is true (empty --repo before any
// scaffold), the ScriptRunner surfaces "script not found" and we
// wrap it with a specific error pointing at the operator's next
// action.
//
// **Not invoked in dry-run** — the caller (`validateAgeKeyEnrollment`)
// only reaches this branch on `--fleet-mode=new-repo`. Dry-run
// still ends up here but the RunE downstream of Validate treats
// ErrAgeKeyNotFound + ErrAgeKeyGenerateFailed as WARNINGs on
// dry-run (see the surrounding cobra dispatch's downgrade logic).
func autoGenerateAgeKey(ctx context.Context, out io.Writer, o *clusterinit.InitOptions) error {
	if o.Repo == "" {
		return fmt.Errorf("init: greenfield age-key generation needs --repo to point at a local fleet checkout")
	}
	runner, err := bootstrap.NewScriptOnly(o.Repo)
	if err != nil {
		return fmt.Errorf("init: greenfield age-key: build script runner: %w", err)
	}
	fmt.Fprintln(out, "[sops] generating age key via bootstrap/generate-age-key.sh")
	lines, err := runner.Run(ctx, ports.ScriptGenerateAgeKey, nil)
	if err != nil {
		return fmt.Errorf("init: greenfield age-key: run script: %w (ensure the fleet-starter is extracted into %s so bootstrap/generate-age-key.sh exists)",
			err, o.Repo)
	}
	var exitCode int
	for line := range lines {
		if line.Stream == ports.StreamExit {
			if _, perr := fmt.Sscanf(line.Text, "%d", &exitCode); perr != nil {
				exitCode = 1
			}
			continue
		}
		fmt.Fprintln(out, line.Text)
	}
	if exitCode != 0 {
		return fmt.Errorf("init: greenfield age-key: script exited %d", exitCode)
	}

	// Sanity: the script should have produced <fleet>/age.key.
	// Refuse loudly if the file still isn't there — downstream
	// enrollment check would fail with a less-obvious error.
	fleetKey := filepath.Join(o.Repo, "age.key")
	if _, err := os.Stat(fleetKey); err != nil {
		return fmt.Errorf("init: greenfield age-key: script ran (exit 0) but %s doesn't exist: %w",
			fleetKey, err)
	}
	fmt.Fprintf(out, "[sops] generated %s\n", fleetKey)
	return nil
}
