// Package fleetconfig holds the pure helpers behind `kube-dc bootstrap
// config` — the day-2 editor for a cluster's `cluster-config.env`. Parsing
// / reading / writing the file is `internal/bootstrap/config`; this
// package adds the get/list/set semantics on top: inline-comment
// stripping for display, value validation, and change planning (so the
// cobra layer just wires I/O + the git transaction around it).
package fleetconfig

import (
	"fmt"
	"strings"

	"github.com/shalb/kube-dc/cli/internal/bootstrap/config"
)

// Errors.
var (
	ErrBadAssignment = fmt.Errorf("config: assignment must be KEY=VALUE")
	ErrEmptyKey      = fmt.Errorf("config: empty key")
	ErrBadValue      = fmt.Errorf("config: invalid value")
	ErrUnknownKey    = fmt.Errorf("config: key not present in cluster-config.env (pass --add to create it)")
)

// Change is one planned key mutation, for the diff/plan the operator
// reviews before a `set` commits.
type Change struct {
	Key   string
	Old   string // current value (inline-comment-stripped), "" if Added
	New   string
	Added bool // key did not exist before
}

// NoOp reports whether the change leaves the value unchanged.
func (c Change) NoOp() bool { return !c.Added && c.Old == c.New }

// StripInlineComment removes a trailing ` # comment` from a
// cluster-config.env value. The fleet convention is `KEY=value  # why`
// (whitespace before the #), so we only strip a `#` that follows
// whitespace — a `#` glued to the value (rare, e.g. a URL fragment) is
// preserved. Trailing whitespace is trimmed.
func StripInlineComment(v string) string {
	for i := 0; i < len(v); i++ {
		if v[i] == '#' && i > 0 && (v[i-1] == ' ' || v[i-1] == '\t') {
			return strings.TrimRight(v[:i], " \t")
		}
	}
	return strings.TrimRight(v, " \t")
}

// ValidateValue rejects values that would corrupt the env file or the
// downstream ConfigMap: empty, or containing a newline / CR / NUL.
// (cluster-config.env is a flat KEY=VALUE file — one line per key.)
func ValidateValue(val string) error {
	if strings.TrimSpace(val) == "" {
		return fmt.Errorf("%w: empty", ErrBadValue)
	}
	if strings.ContainsAny(val, "\n\r\x00") {
		return fmt.Errorf("%w: contains a newline or NUL", ErrBadValue)
	}
	return nil
}

// ParseAssignments parses `KEY=VALUE` args into an ordered list of
// (key,value) pairs, validating each key + value. Order is preserved so
// the plan reads in the order the operator typed them. A duplicate key
// is an error (ambiguous).
func ParseAssignments(args []string) ([]KV, error) {
	seen := map[string]bool{}
	out := make([]KV, 0, len(args))
	for _, a := range args {
		eq := strings.IndexByte(a, '=')
		if eq <= 0 {
			return nil, fmt.Errorf("%w: %q", ErrBadAssignment, a)
		}
		key := strings.TrimSpace(a[:eq])
		val := strings.TrimSpace(a[eq+1:])
		if key == "" {
			return nil, ErrEmptyKey
		}
		if err := ValidateValue(val); err != nil {
			return nil, fmt.Errorf("%s: %w", key, err)
		}
		if seen[key] {
			return nil, fmt.Errorf("%w: %q given twice", ErrBadAssignment, key)
		}
		seen[key] = true
		out = append(out, KV{Key: key, Value: val})
	}
	return out, nil
}

// KV is one parsed assignment.
type KV struct {
	Key   string
	Value string
}

// Plan computes the ordered changes a `set` would make against env.
// Unknown keys (not already in the file) are rejected unless allowAdd.
// No-op changes (same value) are still returned (marked NoOp) so the
// caller can report "nothing to change" honestly.
func Plan(env *config.Env, sets []KV, allowAdd bool) ([]Change, error) {
	changes := make([]Change, 0, len(sets))
	for _, kv := range sets {
		old, exists := env.Get(kv.Key)
		if !exists && !allowAdd {
			return nil, fmt.Errorf("%w: %q", ErrUnknownKey, kv.Key)
		}
		changes = append(changes, Change{
			Key:   kv.Key,
			Old:   StripInlineComment(old),
			New:   kv.Value,
			Added: !exists,
		})
	}
	return changes, nil
}

// HasEffective reports whether any change actually mutates a value
// (used to short-circuit the commit when every set is a no-op).
func HasEffective(changes []Change) bool {
	for _, c := range changes {
		if !c.NoOp() {
			return true
		}
	}
	return false
}
