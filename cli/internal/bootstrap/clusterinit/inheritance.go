package clusterinit

import (
	"errors"
	"fmt"
	"sort"
	"strings"
	"time"
)

// M4-T13 — existing-fleet inheritance.
//
// Per installer-prd §4.1.3 + installer-agentic-implementation-plan
// §10.M4-T13, when `--fleet-mode=existing-fleet` and prior clusters
// exist in the fleet repo, `init` inherits version pins from
// siblings so the new cluster joins at a sensible default (matching
// the fleet's current platform version). The operator can still
// override via `--set` or by editing cluster-config.env between
// dry-run and apply.
//
// **Rule** (from plan §M4-T13): "parse sibling clusters'
// cluster-config.env for version pins; pre-fill new cluster's env.
// Refuse domain collision. Use most-recently-modified sibling as
// the template."
//
// **What "version pin" means here**: any key matching one of the
// suffix patterns *_VERSION, *_CHART_VERSION, or *_TAG. This
// catches the full set the live `cloud/cluster-config.env` carries
// today (KUBE_DC_VERSION, KUBE_DC_MANAGER_TAG, OPENBAO_VERSION,
// OPENBAO_CHART_VERSION, KUBE_OVN_VERSION, etc.) without
// hardcoding an allow-list that would silently miss future
// additions.

// SiblingCluster is one prior cluster's relevant facts for
// inheritance. Populated by the cobra layer from
// `discover.ListClusters` (or a real config.Env parse); the
// inheritance package stays Env-agnostic so it can be unit-tested
// with hand-built fixtures.
type SiblingCluster struct {
	Name string
	// Domain is the cluster's DOMAIN cluster-config value — used to
	// catch domain collisions before the new cluster's overlay is
	// scaffolded.
	Domain string
	// Env is the parsed cluster-config.env as KEY → value. Inheritance
	// walks this map looking for version-suffix keys.
	Env map[string]string
	// ModTime is the cluster-config.env file's mod time on disk —
	// used as the "most-recently-modified sibling" tiebreaker per
	// plan §M4-T13.
	ModTime time.Time
}

// InheritanceResult is what InheritFromSiblings returns to the
// cobra layer. `Defaults` is the merged map ready to seed
// `Plan.InheritedDefaults`; `TemplateName` is the sibling whose
// values won when siblings disagreed (always populated when there's
// at least one sibling — same name when only one sibling exists).
type InheritanceResult struct {
	// Defaults maps version-pin key → inherited value. Empty when no
	// version pins are found across siblings.
	Defaults map[string]string
	// TemplateName is the sibling whose values won. The plan-render
	// surfaces it under "Detected: existing-fleet → using <name> as
	// the template" so operators see which sibling drove the
	// inherited values.
	TemplateName string
}

// ErrDomainCollision is returned by CheckDomainCollision when the
// new cluster's domain matches any sibling. Cobra surfaces this as
// a clear "this domain is already in the fleet" error before any
// mutations.
var ErrDomainCollision = errors.New("init: domain collides with an existing cluster in the fleet")

// versionKeySuffixes are the cluster-config.env key suffixes
// inheritance treats as "version pins". Suffix-based detection
// keeps the catch-set future-proof: adding a new component to the
// fleet (e.g. CILIUM_VERSION) automatically participates in
// inheritance without code changes here.
var versionKeySuffixes = []string{
	"_VERSION",
	"_CHART_VERSION",
	"_TAG",
}

// isVersionKey reports whether a cluster-config.env key looks like
// a version pin. Exported so callers (tests, future engine slices)
// can run the same check without re-importing the suffix list.
func isVersionKey(key string) bool {
	for _, suf := range versionKeySuffixes {
		if strings.HasSuffix(key, suf) {
			return true
		}
	}
	return false
}

// stripInlineComment removes the trailing ` # ...` annotation that
// the live fleet cluster-config.env files use to document why a
// version was pinned (e.g. `KUBE_DC_MANAGER_TAG=v0.3.63   # rotate-
// root tombstone work folded in`). The annotations are useful in
// the source file but corrupt downstream values — sourcing the file
// from bash would set KEY to "v0.3.63   # rotate-root…" verbatim,
// and serialising the inherited value to the new cluster's env
// would propagate the noise.
//
// Heuristic: take everything before the first `#` that's preceded
// by whitespace (or at position 0). Bash's actual quoting rules
// allow `#` inside values, but cluster-config.env's convention is
// never to use `#` in a value, so the simple split is safe here.
func stripInlineComment(v string) string {
	// Walk for the first whitespace-then-`#` boundary.
	for i := 0; i < len(v); i++ {
		if v[i] != '#' {
			continue
		}
		if i == 0 || v[i-1] == ' ' || v[i-1] == '\t' {
			return strings.TrimSpace(v[:i])
		}
	}
	return strings.TrimSpace(v)
}

// InheritFromSiblings walks the prior clusters in fleet order,
// pulls every version-pin key, and returns the merged inheritance.
//
// Conflict resolution (plan §M4-T13, refined by M4-T04+T09+T13
// review-pass P2): the rule is **most-recently-modified provider
// wins per key**, with stable alphabetic-name tiebreak when
// ModTime is equal. This applies uniformly:
//
//   - For keys the template sibling has, the template usually wins
//     (template = most-recently-modified sibling overall).
//   - For keys ONLY in older siblings, the most-recently-modified
//     of those siblings provides the value. Critically, this is
//     NOT alphabetic — the prior implementation walked others in
//     alphabetic-name order, which would silently pick the wrong
//     value during partial upgrades where two older clusters
//     disagree on a newly-introduced pin.
//
// `TemplateName` remains the most-recently-modified sibling
// overall (used for the plan render "Template: …" header line).
// The per-key winner doesn't influence TemplateName because it
// can differ per-key — surfacing N template names per-key would
// be more noise than signal.
//
// Returns an empty InheritanceResult (no Defaults, empty
// TemplateName) when `siblings` is empty.
func InheritFromSiblings(siblings []SiblingCluster) InheritanceResult {
	if len(siblings) == 0 {
		return InheritanceResult{Defaults: map[string]string{}}
	}

	// Pick the overall template = most-recently-modified sibling
	// (for the plan-header `Template:` line). Alphabetic-name on
	// ModTime tie keeps the result deterministic.
	template := siblings[0]
	for _, s := range siblings[1:] {
		switch {
		case s.ModTime.After(template.ModTime):
			template = s
		case s.ModTime.Equal(template.ModTime) && s.Name < template.Name:
			template = s
		}
	}

	// Per-key winner = most-recently-modified sibling that provides
	// the key. Alphabetic-name tiebreak when ModTime equal. This is
	// the corrected rule (review-pass P2) — the prior alphabetic-
	// only fallback ignored ModTime for non-template keys, which
	// produced wrong winners during partial upgrades.
	//
	// Walk siblings in (ModTime desc, Name asc) order. The first
	// sibling we see for each key wins; subsequent siblings are
	// skipped because they're older (or alphabetically later at the
	// same time).
	ordered := append([]SiblingCluster(nil), siblings...)
	sort.SliceStable(ordered, func(i, j int) bool {
		if ordered[i].ModTime.Equal(ordered[j].ModTime) {
			return ordered[i].Name < ordered[j].Name
		}
		return ordered[i].ModTime.After(ordered[j].ModTime)
	})

	out := make(map[string]string)
	for _, s := range ordered {
		for k, v := range s.Env {
			if !isVersionKey(k) {
				continue
			}
			if _, alreadySet := out[k]; alreadySet {
				continue // older sibling — newer winner already set
			}
			out[k] = stripInlineComment(v)
		}
	}

	return InheritanceResult{
		Defaults:     out,
		TemplateName: template.Name,
	}
}

// CheckDomainCollision returns ErrDomainCollision when `newDomain`
// matches any sibling's domain. Case-insensitive comparison; empty
// sibling domains are ignored (malformed cluster-config.env or
// not-yet-populated overlays).
func CheckDomainCollision(newDomain string, siblings []SiblingCluster) error {
	want := strings.ToLower(strings.TrimSpace(newDomain))
	if want == "" {
		return nil // empty domain is a separate validation concern
	}
	var collisions []string
	for _, s := range siblings {
		got := strings.ToLower(strings.TrimSpace(s.Domain))
		if got == "" {
			continue
		}
		if got == want {
			collisions = append(collisions, s.Name)
		}
	}
	if len(collisions) == 0 {
		return nil
	}
	sort.Strings(collisions)
	return fmt.Errorf("%w (domain=%s, used by: %s)", ErrDomainCollision, newDomain, strings.Join(collisions, ", "))
}
