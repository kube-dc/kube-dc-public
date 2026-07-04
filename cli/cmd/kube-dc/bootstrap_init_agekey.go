package main

import (
	"errors"
	"fmt"
	"io"
	"io/fs"
	"os"
	"path/filepath"

	sopsadapter "github.com/shalb/kube-dc/cli/internal/bootstrap/adapters/sops"
	"github.com/shalb/kube-dc/cli/internal/bootstrap/clusterinit"
)

// M4-T09 cobra wiring — resolves the operator's age key path, derives
// the pubkey, parses the fleet's `.sops.yaml` recipients, and runs
// the enrollment check. For cloudacropolis (existing-fleet) this
// surfaces silent-pass when enrolled or a clean keyholder-action
// message when not.
//
// Greenfield generation (auto-run `bootstrap/generate-age-key.sh`)
// is M4-T05 territory; for v1 the cobra surface returns
// ErrAgeKeyGenerateNotImplemented when --fleet-mode=new-repo so the
// operator runs the script manually then re-runs init.

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
// For --fleet-mode=new-repo, returns `ErrAgeKeyGenerateNotImplemented`
// so operators run `bootstrap/generate-age-key.sh` manually for v1.
// For non-existing-fleet modes (existing-repo), this is a no-op —
// the engine slice that adopts an empty repo will handle key setup
// on its own timeline.
//
// On success (enrolled), prints "[sops] ✓ enrolled (pubkey=… via
// <source>)" so operators see which key the CLI consulted.
func validateAgeKeyEnrollment(out io.Writer, o *clusterinit.InitOptions) error {
	switch o.FleetMode {
	case clusterinit.FleetNewRepo:
		// Greenfield-generate is M4-T05 territory; surface the
		// typed sentinel so cobra can route it cleanly.
		return clusterinit.ErrAgeKeyGenerateNotImplemented
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
