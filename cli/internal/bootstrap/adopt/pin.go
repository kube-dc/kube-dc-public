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
	Live       string // detected live chart version to pin to
}

// PinResult is the outcome of PinVersions.
type PinResult struct {
	Pins          []PinChange // version keys that should change (Current != Live)
	AlreadyPinned []string    // "COMPONENT (KEY=version)" already at the live version
	Undetected    []string    // detected components whose live chart version couldn't be read
}

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
// each with a cluster-config.env version key + a readable LIVE Helm
// chart version, computes the pin needed to adopt it in place. Splits
// into: Pins (value would change), AlreadyPinned (already correct), and
// Undetected (no readable live version — pin manually). Pure aside from
// the Inspector calls; deterministic order.
func PinVersions(ctx context.Context, insp Inspector, env EnvReader) (*PinResult, error) {
	det, err := Detect(ctx, insp)
	if err != nil {
		return nil, err
	}
	charts, err := insp.HelmReleaseChartVersions(ctx)
	if err != nil {
		return nil, err
	}

	res := &PinResult{}
	for _, f := range det.Findings {
		c := f.Component
		if c.VersionKey == "" {
			continue // kube-dc has no pinnable base (e.g. ingress-nginx)
		}
		live := charts[c.HelmReleaseNS+"/"+c.HelmRelease]
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
		})
	}
	sort.Slice(res.Pins, func(i, j int) bool { return res.Pins[i].VersionKey < res.Pins[j].VersionKey })
	sort.Strings(res.AlreadyPinned)
	sort.Strings(res.Undetected)
	return res, nil
}
