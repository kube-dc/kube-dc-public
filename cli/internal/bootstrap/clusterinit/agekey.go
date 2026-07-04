package clusterinit

import (
	"errors"
	"fmt"
	"regexp"
	"sort"
	"strings"
)

// M4-T09 — age key handling.
//
// Per installer-ux §4.2 the age-key flow has three branches:
//
//   1. **Greenfield**: no fleet repo on disk, no key. `init` runs
//      `bootstrap/generate-age-key.sh` and registers the operator
//      as the sole `.sops.yaml` recipient.
//   2. **Existing-fleet, operator enrolled** (the atlantis
//      path): the operator already has a key, the pubkey is in
//      `.sops.yaml`'s recipients — proceed silently.
//   3. **Existing-fleet, NOT enrolled**: the pubkey isn't in
//      `.sops.yaml`. The CLI refuses with a clear instruction for
//      an existing keyholder to run `bootstrap/add-engineer.sh`.
//      The CLI deliberately doesn't auto-enroll — security boundary,
//      needs an existing keyholder's authority.
//
// This file owns the pure functions for branches 2 and 3. Branch
// 1 (greenfield-generate) landed at SHA `1ace3c9d` and lives in
// `bootstrap_init_agekey.go`'s `autoGenerateAgeKey` — it auto-runs
// `bootstrap/generate-age-key.sh` via ScriptRunner when
// `--fleet-mode=new-repo` and no `<fleet>/age.key` exists.
//
// Cobra wiring in `bootstrap_init_agekey.go` reads the operator's
// pubkey via the sops adapter's `DerivePubKey` and the recipients
// via `ParseSOPSConfigRecipients(<repo>/.sops.yaml)`. Tests for the
// pure layer use hand-crafted byte buffers.

// AgeKeySource records which precedence slot the resolved age key
// came from. The cobra layer logs this so operators understand
// which file was consulted (debugging "wrong key picked" complaints).
type AgeKeySource string

const (
	AgeKeySourceFlag           AgeKeySource = "--age-key flag"
	AgeKeySourceFleetRepo      AgeKeySource = "<fleet>/age.key"
	AgeKeySourceSOPSEnvVar     AgeKeySource = "$SOPS_AGE_KEY_FILE"
	AgeKeySourceDefaultUserDir AgeKeySource = "~/.config/sops/age/keys.txt"
	AgeKeySourceNone           AgeKeySource = "<none — no key resolved>"
)

// AgeKeyResolution is the result of walking the precedence chain.
// Path is empty when no key could be located.
type AgeKeyResolution struct {
	Path   string
	Source AgeKeySource
}

// --- Errors ---

// ErrAgeKeyNotEnrolled is returned by CheckAgeKeyEnrollment when the
// operator's derived pubkey is not in the fleet's `.sops.yaml`
// recipients list. The error message includes the operator's pubkey
// + the add-engineer.sh contract so the operator can hand the
// remediation step to an existing keyholder verbatim.
var ErrAgeKeyNotEnrolled = errors.New("init: operator's age pubkey is not in the fleet's .sops.yaml recipients")

// ErrAgeKeyNotFound is returned when none of the precedence-chain
// paths resolve to an existing readable file. The error message
// lists every path checked so the operator knows where to look.
var ErrAgeKeyNotFound = errors.New("init: no age key file found")

// ErrAgeKeyDryRunSkip signals that the greenfield-generate branch
// was reached under `--dry-run` and refused to mutate the fleet.
// Dry-run must be side-effect-free; running the generate script
// would create `age.key` + `.sops.yaml` on disk. The cobra RunE
// downgrades this to a `[sops] WARNING:` line and lets the plan
// render — apply-time re-runs will hit the auto-generate path
// unimpeded.
var ErrAgeKeyDryRunSkip = errors.New("init: dry-run skipped age-key auto-generation (would create <fleet>/age.key); re-run without --dry-run to generate + enroll")

// ErrSOPSConfigMissing is returned when `.sops.yaml` doesn't exist
// at the fleet repo root. Indicates a malformed fleet repo (every
// kube-dc-fleet has a `.sops.yaml` at the root); cobra surfaces
// this as a hint to inspect the repo.
var ErrSOPSConfigMissing = errors.New("init: .sops.yaml missing from fleet repo root")

// --- Pure parsers ---

// agePubkeyRegex matches the canonical age public key shape:
// `age1` + 58 base32 characters (X25519 pubkey).
var agePubkeyRegex = regexp.MustCompile(`age1[a-z0-9]{58}`)

// ParseSOPSConfigRecipients extracts every age recipient pubkey
// from a `.sops.yaml` body. The canonical shape is:
//
//	creation_rules:
//	  - path_regex: '\.enc\.yaml$'
//	    age: 'age10msk…,age1fug…,age16nk…'
//
// (the `age:` value is a comma-separated string per the sops format).
// Recipients are de-duplicated and returned in original order; an
// empty input or one without any `age1xxx…` pubkeys returns an
// empty slice (caller decides whether that's an error).
//
// Pure — no I/O, no YAML library dependency (regex catches every
// `age1<58-base32>` pattern wherever it appears, which is robust
// against both the inline-comma shape and the rare YAML-list shape).
func ParseSOPSConfigRecipients(body []byte) []string {
	matches := agePubkeyRegex.FindAllString(string(body), -1)
	if len(matches) == 0 {
		return nil
	}
	seen := make(map[string]bool, len(matches))
	out := make([]string, 0, len(matches))
	for _, m := range matches {
		if seen[m] {
			continue
		}
		seen[m] = true
		out = append(out, m)
	}
	return out
}

// CheckAgeKeyEnrollment returns nil when `pubkey` is in `recipients`,
// ErrAgeKeyNotEnrolled (wrapped with the operator-facing remediation
// instruction) otherwise. Empty `pubkey` is rejected with a clear
// "no pubkey" message — callers should have run DerivePubKey first.
func CheckAgeKeyEnrollment(pubkey, operatorName string, recipients []string) error {
	if pubkey == "" {
		return fmt.Errorf("%w: empty pubkey (key file unreadable or malformed)", ErrAgeKeyNotEnrolled)
	}
	for _, r := range recipients {
		if r == pubkey {
			return nil // enrolled — silent pass per installer-ux §4.2
		}
	}
	// Build the operator-actionable remediation. Use the operator's
	// own name as a placeholder so the printed command is
	// copy-paste-ready; if the operator didn't supply one (zero
	// value), the instruction still parses with `<your-name>`.
	name := operatorName
	if name == "" {
		name = "<your-name>"
	}
	sortedRecipients := append([]string(nil), recipients...)
	sort.Strings(sortedRecipients)
	return fmt.Errorf("%w (pubkey=%s; current recipients: %s)\n"+
		"To get enrolled, ask any existing keyholder to run:\n"+
		"  bash bootstrap/add-engineer.sh %s %s\n"+
		"  bash bootstrap/add-engineer.sh --reencrypt\n"+
		"  git push",
		ErrAgeKeyNotEnrolled, pubkey,
		strings.Join(sortedRecipients, ", "),
		name, pubkey)
}
