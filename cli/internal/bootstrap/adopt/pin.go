package adopt

import (
	"context"
	"sort"
)

// EnvReader is the read side of cluster-config.env that PinVersions
// needs — config.Env satisfies it.
type EnvReader interface {
	GetOr(key, fallback string) string
}

// PinChange is one version-pin the operator would write to
// cluster-config.env to adopt a component in place (Flux then reconciles
// to the SAME version → no upgrade/restart).
type PinChange struct {
	Component  string
	VersionKey string
	Current    string // current cluster-config.env value (inline-comment-stripped), "" if unset
	Live       string // version to pin to (live chart version, or a --manual-pin value)
	Manual     bool   // Live came from --manual-pin, not live detection
}

// PinResult is the outcome of PinVersions.
type PinResult struct {
	Pins          []PinChange // version keys that should change (Current != Live)
	AlreadyPinned []string    // "COMPONENT (KEY=version)" already at the live version
	Undetected    []string    // detected components whose live version couldn't be read AND weren't manually pinned
	Skipped       []string    // components excluded by --skip-component
	UnusedManual  []string    // --manual-pin keys that matched no detected component
}

// PinOptions carry the operator's per-component escape hatches for the
// version-pin flow.
type PinOptions struct {
	// Skip excludes a detected component (by Name) entirely — the
	// operator keeps/handles it themselves; it won't be pinned or count
	// as undetected.
	Skip map[string]bool
	// Manual maps a version key → an operator-supplied version, used when
	// the live chart version can't be read (e.g. KubeVirt/CDI, which
	// aren't Helm releases) or to override the detected one.
	Manual map[string]string
}

// HasUnresolved reports whether any detected component is still without a
// version (neither live-detected nor manually pinned) — the signal the
// command uses to fail closed unless --allow-undetected-version.
func (r *PinResult) HasUnresolved() bool { return len(r.Undetected) > 0 }

// stripInlineComment mirrors fleetconfig.StripInlineComment (kept local
// so the engine has no cmd/config dependency): drop a trailing
// whitespace-preceded `# comment`.
func stripInlineComment(v string) string {
	for i := 0; i < len(v); i++ {
		if v[i] == '#' && i > 0 && (v[i-1] == ' ' || v[i-1] == '\t') {
			return trimTrailingWS(v[:i])
		}
	}
	return trimTrailingWS(v)
}

func trimTrailingWS(s string) string {
	for len(s) > 0 && (s[len(s)-1] == ' ' || s[len(s)-1] == '\t') {
		s = s[:len(s)-1]
	}
	return s
}

// PinVersions detects the pre-existing components (via Detect) and, for
// each with a cluster-config.env version key, computes the pin needed to
// adopt it in place. The version comes from --manual-pin (opts.Manual)
// if given, else the live Helm chart version. Splits into: Pins (value
// would change), AlreadyPinned (already correct), Undetected (no version
// available and no --manual-pin), and Skipped (opts.Skip). Pure aside
// from the Inspector calls; deterministic order.
func PinVersions(ctx context.Context, insp Inspector, env EnvReader, opts PinOptions) (*PinResult, error) {
	det, err := Detect(ctx, insp)
	if err != nil {
		return nil, err
	}
	charts, err := insp.HelmReleaseChartVersions(ctx)
	if err != nil {
		return nil, err
	}

	usedManual := map[string]bool{}
	res := &PinResult{}
	for _, f := range det.Findings {
		c := f.Component
		if opts.Skip[c.Name] {
			res.Skipped = append(res.Skipped, c.Name)
			continue
		}
		if c.VersionKey == "" {
			continue // kube-dc has no pinnable base (e.g. ingress-nginx)
		}
		// A manual pin overrides live detection (and rescues components
		// with no readable Helm release, e.g. KubeVirt/CDI).
		live, manual := "", false
		if v, ok := opts.Manual[c.VersionKey]; ok && v != "" {
			live, manual = v, true
			usedManual[c.VersionKey] = true
		} else {
			live = charts[c.HelmReleaseNS+"/"+c.HelmRelease]
		}
		if live == "" {
			res.Undetected = append(res.Undetected, c.Name)
			continue
		}
		current := stripInlineComment(env.GetOr(c.VersionKey, ""))
		if current == live {
			res.AlreadyPinned = append(res.AlreadyPinned, c.Name+" ("+c.VersionKey+"="+live+")")
			continue
		}
		res.Pins = append(res.Pins, PinChange{
			Component:  c.Name,
			VersionKey: c.VersionKey,
			Current:    current,
			Live:       live,
			Manual:     manual,
		})
	}
	for key := range opts.Manual {
		if !usedManual[key] {
			res.UnusedManual = append(res.UnusedManual, key)
		}
	}
	sort.Slice(res.Pins, func(i, j int) bool { return res.Pins[i].VersionKey < res.Pins[j].VersionKey })
	sort.Strings(res.AlreadyPinned)
	sort.Strings(res.Undetected)
	sort.Strings(res.Skipped)
	sort.Strings(res.UnusedManual)
	return res, nil
}
