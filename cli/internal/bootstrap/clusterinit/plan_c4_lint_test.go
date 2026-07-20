package clusterinit

import (
	"reflect"
	"sort"
	"strings"
	"testing"
)

// C4 re-entrance lint (from installer-agentic-implementation-plan
// §4 "cross-cutting"). Enforces the invariant that every field on
// `InitOptions` is either included in `inputsForHash` (participates
// in the plan hash → dry-run/apply-plan drift detection works) OR
// listed in `hashExcludedFields` (with a per-field rationale
// comment). A field that's in NEITHER set is a bug — it can
// silently drift between dry-run and apply-plan without detection.
//
// This test would have caught the M4-T05 Provider-in-hash gap (a
// new InitOptions.Provider field that participated in engine
// dispatch but wasn't in inputsForHash — an operator could dry-run
// with `--provider=gitlab` and apply-plan with default github and
// no drift would fire). The fix at `054465f3` shipped inputsForHash
// coverage; this lint prevents the pattern from recurring.
//
// The check is bi-directional:
//
//  1. Every `InitOptions` field is either in inputsForHash OR in
//     hashExcludedFields (never both, never neither).
//  2. Every field in inputsForHash has a matching name on
//     `InitOptions` (catches drifted/ghost hash fields — a
//     renamed InitOptions field that leaves stale bytes in the
//     hash struct).
//  3. Every key in hashExcludedFields matches a real InitOptions
//     field (catches stale exclusions — a deleted field whose
//     exclusion entry lingers).
func TestPlanHashCoverage_AllInitOptionsFieldsAccountedFor(t *testing.T) {
	gaps := findCoverageGaps(
		reflect.TypeOf(InitOptions{}),
		reflect.TypeOf(inputsForHash{}),
		hashExcludedFields,
	)
	if len(gaps) > 0 {
		t.Errorf("plan-hash coverage gaps:\n  - %s", strings.Join(gaps, "\n  - "))
	}
}

// findCoverageGaps is the core C4 lint predicate — pure function
// over three inputs so both the real-types check above AND the
// meta-test below (which uses synthetic types simulating a
// regression) can share the same logic without duplication.
// Returns human-readable violation strings, sorted for stable
// diffability across runs.
func findCoverageGaps(optsT, hashT reflect.Type, excluded map[string]string) []string {
	optsFields := reflectExportedFieldNames(optsT)
	hashFields := reflectExportedFieldNames(hashT)

	var gaps []string
	// Contract 1 + 2 (bidirectional membership).
	for _, f := range sortedKeys(optsFields) {
		_, hashed := hashFields[f]
		_, isExcluded := excluded[f]
		switch {
		case hashed && isExcluded:
			gaps = append(gaps, "InitOptions."+f+`: BOTH in inputsForHash AND in hashExcludedFields — pick one`)
		case !hashed && !isExcluded:
			gaps = append(gaps, "InitOptions."+f+`: plan-hash coverage gap — add to inputsForHash OR to hashExcludedFields (with rationale)`)
		}
	}
	// Contract 3 (no ghost hash fields).
	for _, f := range sortedKeys(hashFields) {
		if _, ok := optsFields[f]; !ok {
			gaps = append(gaps, "inputsForHash."+f+": no matching field on InitOptions (stale hash entry)")
		}
	}
	// Contract 3b (no ghost exclusion entries).
	excludedNames := make(map[string]struct{}, len(excluded))
	for k := range excluded {
		excludedNames[k] = struct{}{}
	}
	for _, f := range sortedKeys(excludedNames) {
		if _, ok := optsFields[f]; !ok {
			gaps = append(gaps, "hashExcludedFields["+f+"]: no matching field on InitOptions (stale exclusion)")
		}
	}
	return gaps
}

// TestPlanHashCoverage_LintCatchesKnownRegressions — meta-test.
// Verifies the lint predicate actually fires on the pattern it's
// designed to catch. Otherwise a future refactor could silently
// break the check itself and we'd have false confidence.
//
// Each case uses synthetic struct types simulating a specific
// regression pattern; asserts findCoverageGaps returns the
// expected violation string.
func TestPlanHashCoverage_LintCatchesKnownRegressions(t *testing.T) {
	// Case A: The M4-T05 Provider-in-hash regression shape — a new
	// InitOptions field that participates in engine dispatch but
	// isn't in inputsForHash AND isn't in hashExcludedFields.
	type regressionOpts struct {
		Preset   string
		Provider string // new field — the "regression"
	}
	type regressionHash struct {
		Preset string // Provider missing
	}
	gaps := findCoverageGaps(
		reflect.TypeOf(regressionOpts{}),
		reflect.TypeOf(regressionHash{}),
		map[string]string{}, // no explicit exclusion either
	)
	if len(gaps) != 1 || !strings.Contains(gaps[0], "InitOptions.Provider") {
		t.Errorf("lint should flag the Provider gap; got %v", gaps)
	}

	// Case B: Field in BOTH sets — reviewer discipline violation.
	type doubleOpts struct {
		DryRun bool
		Preset string
	}
	type doubleHash struct {
		DryRun bool // shouldn't be here — DryRun is apply-flow, must be excluded only
		Preset string
	}
	gaps = findCoverageGaps(
		reflect.TypeOf(doubleOpts{}),
		reflect.TypeOf(doubleHash{}),
		map[string]string{"DryRun": "apply-flow flag"},
	)
	found := false
	for _, g := range gaps {
		if strings.Contains(g, "InitOptions.DryRun") && strings.Contains(g, "BOTH") {
			found = true
		}
	}
	if !found {
		t.Errorf("lint should flag DryRun in both sets; got %v", gaps)
	}

	// Case C: Ghost hash field — hash struct references a field
	// that doesn't exist on InitOptions (e.g., a renamed field
	// that left stale bytes behind).
	type ghostOpts struct {
		Preset string
	}
	type ghostHash struct {
		Preset       string
		OldFieldName string // no matching InitOptions field
	}
	gaps = findCoverageGaps(
		reflect.TypeOf(ghostOpts{}),
		reflect.TypeOf(ghostHash{}),
		map[string]string{},
	)
	found = false
	for _, g := range gaps {
		if strings.Contains(g, "inputsForHash.OldFieldName") {
			found = true
		}
	}
	if !found {
		t.Errorf("lint should flag OldFieldName ghost; got %v", gaps)
	}

	// Case D: Stale exclusion — an excluded field name that no
	// longer exists on InitOptions (e.g., the field was deleted
	// but its exclusion entry lingered).
	type staleOpts struct {
		Preset string
	}
	type staleHash struct {
		Preset string
	}
	gaps = findCoverageGaps(
		reflect.TypeOf(staleOpts{}),
		reflect.TypeOf(staleHash{}),
		map[string]string{"RemovedField": "some-reason"},
	)
	found = false
	for _, g := range gaps {
		if strings.Contains(g, "hashExcludedFields[RemovedField]") {
			found = true
		}
	}
	if !found {
		t.Errorf("lint should flag RemovedField stale exclusion; got %v", gaps)
	}
}

// TestPlanHashCoverage_ExcludedFieldsCarryRationale — every entry
// in hashExcludedFields MUST have a non-trivial rationale (>= 20
// chars) so a future reader gets the "why" instead of a bare
// field name. Cheap check that prevents the map from collecting
// empty-string keys under review pressure.
func TestPlanHashCoverage_ExcludedFieldsCarryRationale(t *testing.T) {
	for field, rationale := range hashExcludedFields {
		if strings.TrimSpace(rationale) == "" {
			t.Errorf("hashExcludedFields[%s] rationale is empty — every excluded field needs a comment explaining why it's transient", field)
		}
		if len(strings.TrimSpace(rationale)) < 20 {
			t.Errorf("hashExcludedFields[%s] rationale is too short (%q); a future reader should be able to tell what makes this field transient without reading git blame",
				field, rationale)
		}
	}
}

// TestPlanHashCoverage_PopulatorAssignsEveryHashedField — the P2
// half of C4. The bidirectional membership test above verifies
// FIELD NAMES exist on both `InitOptions` and `inputsForHash`, but
// nothing catches the case where the struct definition adds a
// field AND `hashExcludedFields` correctly omits it BUT the
// `InitOptions.inputsForHash()` populator forgets to assign it —
// the projected value would silently stay at the type's zero
// value, so mutating InitOptions.<field> at the input side would
// leave the hash unchanged, and dry-run/apply-plan drift would
// slip past.
//
// Approach: build an InitOptions with every hashed field set to a
// non-zero sentinel via reflection, run the populator, then walk
// the returned projection and assert each field IS non-zero. Any
// zero field on the projection means the populator dropped it on
// the floor.
//
// Per-field escape hatch (`populatorTransforms`): fields with
// intentional transforms (`Provider` → `normalizeProviderForHash`
// collapses github → empty) list the sentinel that survives the
// transform. Without an entry, `reflect.Value.Set` with a
// value derived from `zeroForType` is used.
func TestPlanHashCoverage_PopulatorAssignsEveryHashedField(t *testing.T) {
	optsT := reflect.TypeOf(InitOptions{})
	hashT := reflect.TypeOf(inputsForHash{})

	// Build an InitOptions with sentinel non-zero values for every
	// hashed field. Use InitOptions field types (not projection
	// types) — the populator is what transforms one into the other.
	o := &InitOptions{}
	ov := reflect.ValueOf(o).Elem()
	for _, hashField := range sortedKeys(reflectExportedFieldNames(hashT)) {
		f, ok := optsT.FieldByName(hashField)
		if !ok {
			// Coverage lint above catches this — skip here so we
			// don't double-report; keeps this test focused on
			// populator gaps only.
			continue
		}
		target := ov.FieldByName(hashField)
		if !target.CanSet() {
			continue
		}
		if sentinel, ok := populatorTransforms[hashField]; ok {
			target.Set(reflect.ValueOf(sentinel).Convert(f.Type))
			continue
		}
		target.Set(nonZeroValueFor(f.Type))
	}

	// Run the populator and assert every projection field is
	// non-zero — anything still-zero is a populator gap.
	projected := o.inputsForHash()
	pv := reflect.ValueOf(projected)
	var gaps []string
	for i := 0; i < pv.NumField(); i++ {
		name := pv.Type().Field(i).Name
		if pv.Field(i).IsZero() {
			gaps = append(gaps, "inputsForHash."+name+
				": projection field stays zero after populator ran — InitOptions.inputsForHash() dropped it on the floor")
		}
	}
	if len(gaps) > 0 {
		sort.Strings(gaps)
		t.Errorf("populator coverage gaps:\n  - %s", strings.Join(gaps, "\n  - "))
	}
}

// populatorTransforms names any hashed field whose value at the
// projection differs from the InitOptions field value by design.
// The map value is the InitOptions-side sentinel that survives
// the transform as non-zero on the projection side.
//
// Currently: Provider. `normalizeProviderForHash` collapses the
// explicit github form back to the default empty, so seeding
// `InitOptions.Provider = ProviderGitHub` would leave the
// projection at zero-value. Use `ProviderGitLab` which surfaces
// verbatim.
var populatorTransforms = map[string]interface{}{
	"Provider": ProviderGitLab,
}

// nonZeroValueFor returns a reflect.Value of type `t` set to a
// non-zero sentinel. Handles the primitive + map + slice + named
// string-alias (Preset/Mode/FleetMode/RookMode) cases the hashed
// projection needs — panics on any type the projection doesn't
// currently use, so a future exotic hashed-field type forces a
// deliberate entry here rather than sliding by.
func nonZeroValueFor(t reflect.Type) reflect.Value {
	switch t.Kind() {
	case reflect.String:
		return reflect.ValueOf("sentinel-" + t.Name()).Convert(t)
	case reflect.Bool:
		return reflect.ValueOf(true)
	case reflect.Int, reflect.Int32, reflect.Int64:
		return reflect.ValueOf(int64(42)).Convert(t)
	case reflect.Slice:
		if t.Elem().Kind() == reflect.String {
			return reflect.ValueOf([]string{"sentinel"})
		}
	case reflect.Map:
		if t.Key().Kind() == reflect.String && t.Elem().Kind() == reflect.String {
			m := reflect.MakeMapWithSize(t, 1)
			m.SetMapIndex(reflect.ValueOf("k").Convert(t.Key()), reflect.ValueOf("v").Convert(t.Elem()))
			return m
		}
	}
	panic("nonZeroValueFor: unhandled type " + t.String() +
		" — add a case here or an entry in populatorTransforms")
}

// reflectExportedFieldNames returns the set of exported field names
// on `t` (a struct type) as a set (map[name]{}). Callers that need
// stable iteration pass the result through sortedKeys().
func reflectExportedFieldNames(t reflect.Type) map[string]struct{} {
	out := make(map[string]struct{})
	for i := 0; i < t.NumField(); i++ {
		f := t.Field(i)
		if !f.IsExported() {
			continue
		}
		out[f.Name] = struct{}{}
	}
	return out
}

// sortedKeys returns the map's keys in alphabetical order — used
// to render lint errors in stable order for diffability across runs.
func sortedKeys(m map[string]struct{}) []string {
	out := make([]string, 0, len(m))
	for k := range m {
		out = append(out, k)
	}
	sort.Strings(out)
	return out
}
