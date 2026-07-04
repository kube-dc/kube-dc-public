// Package clusterinit owns the typed `InitOptions` struct and the
// validation rules for `kube-dc bootstrap init`. The cobra command in
// `cli/cmd/kube-dc/bootstrap_init.go` binds CLI flags onto an
// `InitOptions`, calls `Validate`, and then hands the value to the
// engine slices (M4-T03..T13) for execution.
//
// **Why this isn't `package init`** — the plan (installer-agentic-
// implementation-plan §9) calls the directory `init/`, but `init` is a
// predeclared Go identifier (`init() func()` runs at package load) and
// the Go style guide explicitly advises against using it as a package
// name. `clusterinit` is unambiguous, descriptive, and follows the
// sibling pattern (`discover`, `doctor`, `breakglass`).
//
// **Scope of M4-T01** (this file): the struct shape + validation
// framework that the cobra surface depends on. Preset → env mapping is
// M4-T04; auto mode detection is M4-T03; plan rendering is M4-T02;
// scaffold + commit + flux are M4-T10..T12. Engine wiring lands as
// those slices ship.
package clusterinit

import (
	"errors"
	"fmt"
	"net"
	"net/mail"
	"regexp"
	"sort"
	"strings"
)

// Preset selects the network topology defaults — drives M4-T04's
// preset → env mapping. The four values mirror installer-prd §3.2.
type Preset string

const (
	PresetInternalOnly    Preset = "internal-only"
	PresetCloudVLAN       Preset = "cloud-vlan"
	PresetCloudPublicVLAN Preset = "cloud+public-vlan"
	PresetCustom          Preset = "custom"
)

// AllPresets is the validation set + the help-text enumeration. Sorted
// alphabetically for stable cobra output.
var AllPresets = []Preset{
	PresetCloudPublicVLAN, PresetCloudVLAN, PresetCustom, PresetInternalOnly,
}

// Mode is the high-level "what is `init` doing here" decision per
// installer-prd §4.1.1. M4-T03 will auto-detect when the operator
// doesn't pass `--mode`; M4-T01 surfaces the flag only.
type Mode string

const (
	// ModeAuto means "let the CLI infer from probe results" — the
	// default when --mode is not passed. T03's detector picks
	// install/adopt/resume; until T03 lands, an explicit value is
	// required.
	ModeAuto    Mode = "auto"
	ModeInstall Mode = "install"
	ModeAdopt   Mode = "adopt"
	ModeResume  Mode = "resume"
)

var AllModes = []Mode{ModeAdopt, ModeAuto, ModeInstall, ModeResume}

// FleetMode decides how the CLI relates to the kube-dc-fleet git repo.
type FleetMode string

const (
	// FleetNewRepo creates a brand-new fleet repo on GitHub (the
	// greenfield day-1 path).
	FleetNewRepo FleetMode = "new-repo"
	// FleetExistingFleet adds a cluster to an existing fleet repo with
	// sibling clusters already enrolled (cloudacropolis's path).
	FleetExistingFleet FleetMode = "existing-fleet"
	// FleetExistingRepo adopts an existing repo that doesn't yet have
	// kube-dc-fleet structure (greenfield-ish; rare).
	FleetExistingRepo FleetMode = "existing-repo"
)

var AllFleetModes = []FleetMode{FleetExistingFleet, FleetExistingRepo, FleetNewRepo}

// RookMode selects the storage backend strategy. See installer-prd §6
// for the matrix; external-ceph + external-s3 deliberately disable
// the bundled Rook chart.
type RookMode string

const (
	RookDisabled       RookMode = "disabled"
	RookCephLocal      RookMode = "rook-ceph-local"
	RookCephMultiNode  RookMode = "rook-ceph-multi-node"
	RookExternalCeph   RookMode = "external-ceph"
	RookExternalS3     RookMode = "external-s3"
)

var AllRookModes = []RookMode{
	RookCephLocal, RookCephMultiNode, RookDisabled, RookExternalCeph, RookExternalS3,
}

// InitOptions is the typed payload the engine (M4-T03..T13) consumes.
// One field per flag in installer-agentic-implementation-plan §9.M4-T01.
//
// Zero-value safety: every field has a sensible zero (empty slice, "",
// 0, false). The cobra binding fills in defaults that differ from
// zero (e.g. RookMode defaults to Disabled, FleetMode defaults to
// ExistingFleet for cloudacropolis-shape installs).
//
// Secret material: GitHubToken is the only sensitive field. It is
// NEVER logged. The cobra surface accepts it as a flag for CI
// convenience, but the production path is `gh auth token` resolution
// inside the engine — operators in TTY sessions should not pass it on
// the command line at all.
type InitOptions struct {
	// --- Topology ---
	Preset         Preset
	Mode           Mode
	Name           string
	Domain         string
	NodeExternalIP string
	Email          string

	// --- Fleet ---
	FleetMode FleetMode
	Repo      string
	// Provider selects the remote-repo hosting service for M4-T05
	// auto-create + push. Empty defaults to GitHub for backward
	// compatibility with pre-multi-provider callers; explicit
	// values are "github" or "gitlab" (see clusterinit.Provider).
	// The GitHubOwner/GitHubRepo/GitHubToken field names are kept
	// as-is for backward compat — they hold the owner/name/token
	// for the SELECTED provider, not necessarily GitHub. A future
	// slice may rename them to Owner/Name/Token; today they're
	// spelled github-* at the CLI surface because that's what
	// existing operators muscle-memory.
	Provider    Provider
	GitHubOwner string
	GitHubRepo  string
	GitHubToken string // never logged

	// --- Overrides (repeatable flags) ---
	// Sets stores `--set KEY=VALUE` deltas applied on top of the
	// preset's env map. Engine validates against the preset's
	// allow-list at apply time.
	Sets map[string]string
	// NodeNICs maps cluster node name → primary NIC iface for the
	// customInterfaces patch (M4-T11). `--node-nic SRV5-Kub1=enp1s0`.
	NodeNICs map[string]string

	// --- Rook ---
	RookMode      RookMode
	RookOSDNode   string
	RookOSDSizeGB int

	// --- Addons ---
	// Addons is the de-duplicated list of `--addon` values.
	// Validated against the registry (metallb, sso-google,
	// stripe-billing, velero) at apply time.
	Addons []string

	// --- Behaviour gates ---
	AllowDNSNotReady bool
	// AllowNoKubevirtEligible lets Apply proceed on a cluster whose
	// NFD state reports zero kube-dc.com/kubevirt-eligible=true
	// nodes. Default false — M6-T05 NFD gate refuses on 0 count
	// because kube-dc's product surface assumes VMs. Operators who
	// install kube-dc without needing VM workloads (multi-tenancy /
	// GitOps only) opt in.
	AllowNoKubevirtEligible bool
	SSHHost                 string
	NoSSH            bool
	NoInstallPrereqs bool
	NoCreateRepo     bool
	MirrorRegistry   string
	BundlePullSecret string
	OpenBaoSharesOut string

	// --- Plan/apply flow (M4-T02) ---
	DryRun    bool
	PlanFile  string
	ApplyPlan string
	NoPush    bool
	NoTTY     bool
	Yes       bool
}

// --- Errors ---

// ErrValidation is returned by Validate when one or more rules fail.
// Use errors.Is to match; the wrapped message lists every failure on
// a single line so cobra's stderr render stays compact.
var ErrValidation = errors.New("init: invalid options")

// ErrApplyGate is returned by Validate when --no-tty is set without
// one of --yes / --apply-plan / --dry-run. Distinct error so the
// cobra layer can surface the three options as a help block.
var ErrApplyGate = errors.New("init: --no-tty requires --yes, --apply-plan, or --dry-run")

// ErrFleetModeNewRepo is returned when an apply-path fleet-mode
// (new-repo OR existing-fleet without `--no-push`) is set without
// --github-owner + --github-repo. The error message lists
// the two missing flags.
var ErrFleetModeNewRepo = errors.New("init: apply-path fleet modes require --github-owner and --github-repo (used by flux-install.sh to point Flux at the right remote)")

// ErrUnknownProvider is returned when --provider is set to a value
// other than "github" or "gitlab". Empty is valid (defaults to
// GitHub); anything else is a typo or a future provider the
// operator's binary doesn't know about.
var ErrUnknownProvider = errors.New("init: --provider must be `github` or `gitlab`")


// ErrFleetModeExistingRepo is returned when --fleet-mode=existing-fleet
// is set without a resolvable --repo. Empty repo in existing-fleet
// mode would otherwise silently produce a misleading plan with an
// empty "Prior clusters" list (M4-T01+T02 review-pass — P2/P3).
var ErrFleetModeExistingRepo = errors.New("init: --fleet-mode=existing-fleet requires --repo to point at the fleet repo")

// ErrModeAutoUnresolved is the safety-net error that fires when
// Validate sees `ModeAuto`. The cobra layer is supposed to call
// `ResolveMode` BEFORE Validate (M4-T03 wired this in
// `bootstrap_init.go:resolveAutoMode`), so seeing ModeAuto here
// means either:
//
//   - A future refactor dropped the resolve step from cobra's RunE.
//   - A caller constructed InitOptions programmatically without
//     running ResolveMode (e.g. a unit test that should be using
//     ModeInstall directly instead of going through auto).
//
// Surfacing this loudly via Validate prevents BuildPlan from ever
// receiving an unresolved Mode and silently rendering plans with
// "auto" in the header.
//
// Naming note: this used to be `ErrModeAutoNotImplemented` while
// M4-T03 was pending. The error name is exported so external
// callers may have errors.Is checks; if you're seeing this in a
// downstream module, the new name is the documented sentinel.
var ErrModeAutoUnresolved = errors.New("init: ModeAuto reached Validate unresolved — cobra must call ResolveMode first; programmatically: pass --mode=install|adopt|resume explicitly")

// ErrModeAutoNotImplemented is the deprecated alias for
// ErrModeAutoUnresolved. Kept for one release so external errors.Is
// checks don't break in the cobra-layer review-pass window. Remove
// in v0.5.
//
// Deprecated: use ErrModeAutoUnresolved.
var ErrModeAutoNotImplemented = ErrModeAutoUnresolved

// --- Validation ---

// clusterNameRegex permits the same shape `add-cluster.sh` accepts —
// lowercase + digits + `-` + `/` (the cs/zrh nested overlay pattern).
// Refuses leading/trailing `/` and `--`.
var clusterNameRegex = regexp.MustCompile(`^[a-z0-9](?:[a-z0-9-]*[a-z0-9])?(?:/[a-z0-9](?:[a-z0-9-]*[a-z0-9])?)*$`)

// domainRegex is a permissive FQDN check — at least one dot, valid
// label chars, total length ≤ 253. Not RFC-1035-perfect, but rejects
// obvious typos like `https://foo.example` (we want the host only).
var domainRegex = regexp.MustCompile(`^(?:[a-z0-9](?:[a-z0-9-]*[a-z0-9])?\.)+[a-z]{2,}$`)

// Validate runs every cobra-time check and returns ErrValidation
// wrapping the combined failures, ErrApplyGate, or ErrFleetModeNewRepo
// (whichever fires first by category).
//
// Validation is split into "structural" (the flag values make sense
// in isolation) and "cross-flag" (combinations are coherent).
// Structural runs first because cross-flag rules depend on parsed
// values.
func (o *InitOptions) Validate() error {
	var errs []string

	// Structural — every field's own contract.
	errs = append(errs, validateName(o.Name)...)
	errs = append(errs, validateDomain(o.Domain)...)
	errs = append(errs, validateNodeIP(o.NodeExternalIP)...)
	errs = append(errs, validateEmail(o.Email)...)
	errs = append(errs, validatePreset(o.Preset)...)
	errs = append(errs, validateMode(o.Mode)...)
	errs = append(errs, validateFleetMode(o.FleetMode)...)
	errs = append(errs, validateRookMode(o.RookMode, o.RookOSDSizeGB)...)
	errs = append(errs, validateAddons(o.Addons)...)
	errs = append(errs, validateNodeNICs(o.NodeNICs)...)
	errs = append(errs, validateSets(o.Sets)...)

	if len(errs) > 0 {
		return fmt.Errorf("%w: %s", ErrValidation, strings.Join(errs, "; "))
	}

	// Cross-flag — only checked after structural pass so we don't
	// double-report a bad value.

	// --mode=auto is reserved for M4-T03; refuse until that detector
	// ships so we don't silently produce plans with an unresolved
	// mode. (M4-T01+T02 review-pass — P1/P2.)
	if o.Mode == ModeAuto {
		return ErrModeAutoNotImplemented
	}

	// --github-owner + --github-repo are required for ANY fleet
	// mode that will eventually reach `flux-install.sh` — the
	// script needs them to point Flux at the right remote.
	// `--no-push` skips flux-install entirely (Flux can't
	// reconcile from a non-pushed commit), so those paths
	// tolerate missing owner/repo. Dry-run also enforces the
	// requirement so a plan preview surfaces the operator's
	// missing-arg mistake BEFORE they discover it at apply time.
	//
	// Reviewer M4-T05 P2 close (revisited): previously we only
	// required these on `--fleet-mode=new-repo`, so an
	// existing-fleet OR existing-repo + --provider=gitlab operator
	// could omit them and `flux-install.sh` would silently default
	// to `kube-dc/kube-dc-fleet` (a GitHub org — wrong remote
	// entirely, wrong provider, wrong URL scheme).
	//
	// All three fleet modes reach `runFluxInstall` when --no-push
	// is unset (see `Apply` — flux-install fires whenever the
	// commit was pushed to a remote), so all three need owner/repo.
	needsFluxInstall := o.FleetMode == FleetNewRepo ||
		o.FleetMode == FleetExistingFleet ||
		o.FleetMode == FleetExistingRepo
	if needsFluxInstall && !o.NoPush {
		missing := []string{}
		if o.GitHubOwner == "" {
			missing = append(missing, "--github-owner")
		}
		if o.GitHubRepo == "" {
			missing = append(missing, "--github-repo")
		}
		if len(missing) > 0 {
			return fmt.Errorf("%w (missing %s)", ErrFleetModeNewRepo, strings.Join(missing, " + "))
		}
	}

	// --provider validation. Empty defaults to GitHub at the engine
	// layer for backward compat; non-empty must be one of the known
	// providers so a typo doesn't silently route to the wrong
	// remote-repo host.
	switch o.Provider {
	case "", ProviderGitHub, ProviderGitLab:
		// OK
	default:
		return fmt.Errorf("%w: got %q (want %q or %q)",
			ErrUnknownProvider, string(o.Provider),
			string(ProviderGitHub), string(ProviderGitLab))
	}

	// --fleet-mode=existing-fleet needs --repo so the engine knows
	// where to look for sibling clusters / age recipients / version
	// pins. Without it BuildPlan would silently render an empty
	// "Prior clusters" list, a misleading footgun. (M4-T01+T02
	// review-pass — P2/P3.)
	if o.FleetMode == FleetExistingFleet && o.Repo == "" {
		return ErrFleetModeExistingRepo
	}

	// --dry-run + --apply-plan are mutually exclusive — applying a
	// plan IS the apply side; dry-run is the print side.
	if o.DryRun && o.ApplyPlan != "" {
		return fmt.Errorf("%w: --dry-run and --apply-plan are mutually exclusive", ErrValidation)
	}

	// --plan-file with --apply-plan is redundant (the plan path is the
	// apply path). Catch this rather than silently dropping --plan-file.
	if o.ApplyPlan != "" && o.PlanFile != "" && o.PlanFile != o.ApplyPlan {
		return fmt.Errorf("%w: --plan-file conflicts with --apply-plan (use --apply-plan alone)", ErrValidation)
	}

	// CI apply gate (installer-agentic-implementation-plan §9.M4-T01).
	// `--no-tty` without one of {--yes, --apply-plan, --dry-run} is
	// refused. The third path — "successful --dry-run in the same
	// session writes a consent cache" — is consumed at engine-time
	// (M4-T02 owns the cache layout). For T01 the cobra-time check
	// only enforces the explicit flag forms.
	if o.NoTTY && !o.Yes && o.ApplyPlan == "" && !o.DryRun {
		return ErrApplyGate
	}

	return nil
}

// --- Structural validators (small, table-friendly, each returns []string
// so multiple rules per option can compose) ---

func validateName(name string) []string {
	if name == "" {
		return []string{"--name is required"}
	}
	if !clusterNameRegex.MatchString(name) {
		return []string{fmt.Sprintf("--name %q must be lowercase letters/digits/dashes (optionally nested with /, e.g. cs/zrh)", name)}
	}
	return nil
}

func validateDomain(domain string) []string {
	if domain == "" {
		return []string{"--domain is required"}
	}
	if !domainRegex.MatchString(strings.ToLower(domain)) {
		return []string{fmt.Sprintf("--domain %q must be a FQDN (e.g. acropolis.example.com)", domain)}
	}
	return nil
}

func validateNodeIP(ip string) []string {
	if ip == "" {
		return []string{"--node-external-ip is required"}
	}
	parsed := net.ParseIP(ip)
	if parsed == nil {
		return []string{fmt.Sprintf("--node-external-ip %q is not a valid IP address", ip)}
	}
	return nil
}

func validateEmail(email string) []string {
	if email == "" {
		return []string{"--email is required (used for cert-manager + LetsEncrypt registration)"}
	}
	if _, err := mail.ParseAddress(email); err != nil {
		return []string{fmt.Sprintf("--email %q is not a valid address: %v", email, err)}
	}
	return nil
}

func validatePreset(p Preset) []string {
	if p == "" {
		return []string{fmt.Sprintf("--preset is required (one of %s)", joinPresets(AllPresets))}
	}
	for _, ok := range AllPresets {
		if p == ok {
			return nil
		}
	}
	return []string{fmt.Sprintf("--preset %q not recognised (want one of %s)", p, joinPresets(AllPresets))}
}

func validateMode(m Mode) []string {
	if m == "" {
		// Empty is rejected — until M4-T03 ships auto-detection, the
		// operator must say explicitly. Treat "" as "operator forgot
		// the flag" rather than "auto".
		return []string{fmt.Sprintf("--mode is required (one of %s); --mode=auto will become the default once M4-T03 lands", joinModes(AllModes))}
	}
	for _, ok := range AllModes {
		if m == ok {
			return nil
		}
	}
	return []string{fmt.Sprintf("--mode %q not recognised (want one of %s)", m, joinModes(AllModes))}
}

func validateFleetMode(f FleetMode) []string {
	if f == "" {
		return []string{fmt.Sprintf("--fleet-mode is required (one of %s)", joinFleetModes(AllFleetModes))}
	}
	for _, ok := range AllFleetModes {
		if f == ok {
			return nil
		}
	}
	return []string{fmt.Sprintf("--fleet-mode %q not recognised (want one of %s)", f, joinFleetModes(AllFleetModes))}
}

func validateRookMode(r RookMode, osdSizeGB int) []string {
	// Empty rolls up to Disabled at flag-bind time, but if it slipped
	// through (programmatic construction), reject loudly.
	if r == "" {
		return []string{"--rook-mode unset (use --rook-mode=disabled to skip storage)"}
	}
	known := false
	for _, ok := range AllRookModes {
		if r == ok {
			known = true
			break
		}
	}
	if !known {
		return []string{fmt.Sprintf("--rook-mode %q not recognised (want one of %s)", r, joinRookModes(AllRookModes))}
	}
	// rook-ceph-local requires --rook-osd-size-gb > 0; the other modes
	// don't (multi-node sizes per-node from cluster.yaml, external
	// modes don't manage OSDs at all).
	if r == RookCephLocal && osdSizeGB <= 0 {
		return []string{"--rook-mode=rook-ceph-local requires --rook-osd-size-gb > 0"}
	}
	return nil
}

// allowedAddons is the v1 addon registry. Adding an addon means
// adding both the validator entry here and the rendering hook in M4
// engine slices.
var allowedAddons = map[string]bool{
	"metallb":        true,
	"sso-google":     true,
	"stripe-billing": true,
	"velero":         true,
}

func validateAddons(addons []string) []string {
	var errs []string
	seen := map[string]bool{}
	for _, a := range addons {
		if !allowedAddons[a] {
			errs = append(errs, fmt.Sprintf("--addon %q not in registry (allowed: metallb, sso-google, stripe-billing, velero)", a))
			continue
		}
		if seen[a] {
			errs = append(errs, fmt.Sprintf("--addon %q specified more than once", a))
			continue
		}
		seen[a] = true
	}
	return errs
}

func validateNodeNICs(nics map[string]string) []string {
	var errs []string
	// Sort keys for deterministic error ordering — Go map iteration is
	// randomized and we want test failures to be reproducible.
	keys := make([]string, 0, len(nics))
	for k := range nics {
		keys = append(keys, k)
	}
	sort.Strings(keys)
	for _, node := range keys {
		iface := nics[node]
		if node == "" {
			errs = append(errs, "--node-nic key (node name) cannot be empty")
			continue
		}
		if iface == "" {
			errs = append(errs, fmt.Sprintf("--node-nic %s= has empty iface", node))
			continue
		}
		// Reuse the canonical NIC-name validator (M4-T11 review-pass
		// — P2): catches shell metacharacters, whitespace, and
		// Linux IFNAMSIZ > 15 chars. Without this, `--node-nic
		// SRV5=enp1s0;rm` would land in the ProviderNetwork patch.
		if msg := validateNICName(iface); msg != "" {
			errs = append(errs, fmt.Sprintf("--node-nic %s=%s: %s", node, iface, msg))
		}
	}
	return errs
}

func validateSets(sets map[string]string) []string {
	var errs []string
	keys := make([]string, 0, len(sets))
	for k := range sets {
		keys = append(keys, k)
	}
	sort.Strings(keys)
	for _, k := range keys {
		if k == "" {
			errs = append(errs, "--set key cannot be empty")
			continue
		}
		// Disallow lowercase-leading keys — cluster-config.env keys
		// are SCREAMING_SNAKE_CASE by convention; a lowercase key
		// (e.g. `--set domain=...`) is almost certainly a typo for
		// the flag form (`--domain ...`).
		if !isUpperKey(k) {
			errs = append(errs, fmt.Sprintf("--set key %q must be SCREAMING_SNAKE_CASE (cluster-config.env convention); did you mean a dedicated flag?", k))
		}
	}
	return errs
}

func isUpperKey(k string) bool {
	if k == "" {
		return false
	}
	for _, r := range k {
		switch {
		case r >= 'A' && r <= 'Z':
		case r >= '0' && r <= '9':
		case r == '_':
		default:
			return false
		}
	}
	// First char must be a letter.
	return k[0] >= 'A' && k[0] <= 'Z'
}

// --- Help-string joiners (stable order for cobra Long help) ---

func joinPresets(p []Preset) string {
	out := make([]string, len(p))
	for i, v := range p {
		out[i] = string(v)
	}
	return strings.Join(out, "|")
}

func joinModes(m []Mode) string {
	out := make([]string, len(m))
	for i, v := range m {
		out[i] = string(v)
	}
	return strings.Join(out, "|")
}

func joinFleetModes(f []FleetMode) string {
	out := make([]string, len(f))
	for i, v := range f {
		out[i] = string(v)
	}
	return strings.Join(out, "|")
}

func joinRookModes(r []RookMode) string {
	out := make([]string, len(r))
	for i, v := range r {
		out[i] = string(v)
	}
	return strings.Join(out, "|")
}

// ParseSetPairs converts a slice of `KEY=VALUE` strings (as cobra
// returns for repeatable --set / --node-nic flags) into a map. Returns
// an error on the first malformed entry — we surface this from cobra
// flag-set rather than letting it land in Validate as a generic
// "empty key" message.
func ParseSetPairs(pairs []string) (map[string]string, error) {
	out := make(map[string]string, len(pairs))
	for _, raw := range pairs {
		k, v, ok := strings.Cut(raw, "=")
		if !ok {
			return nil, fmt.Errorf("expected KEY=VALUE, got %q", raw)
		}
		k = strings.TrimSpace(k)
		v = strings.TrimSpace(v)
		if k == "" {
			return nil, fmt.Errorf("empty key in %q", raw)
		}
		if _, exists := out[k]; exists {
			return nil, fmt.Errorf("duplicate key %q", k)
		}
		out[k] = v
	}
	return out, nil
}
