package discover

import (
	"context"
	"crypto/tls"
	"crypto/x509"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"strings"
	"time"

	"github.com/shalb/kube-dc/cli/pkg/credential"
)

// ClusterStatus is the single per-row badge in the fleet landing screen
// (see installer-prd §9.6). The names match the table there verbatim.
type ClusterStatus string

const (
	StatusReady       ClusterStatus = "Ready"
	StatusReconciling ClusterStatus = "Reconciling"
	StatusDrifted     ClusterStatus = "Drifted"
	StatusFailed      ClusterStatus = "Failed"
	StatusUnreachable ClusterStatus = "Unreachable"
	StatusUnknown     ClusterStatus = "Unknown"
)

// ProbeResult is a single ClusterProbe.Run() outcome.
type ProbeResult struct {
	Status      ClusterStatus
	Detail      string             // one-line summary for the right pane
	FixHint     string             // optional next step the operator can take
	FixAction   *FixAction         // structured form of FixHint — TUI dispatches Enter on this row to it
	Reconcilers []ReconcilerStatus // per-Kustomization breakdown
	Drifts      []ImageDrift       // per-Deployment image-tag drift, empty when in-sync
}

// FixAction is the machine-readable counterpart of FixHint. When the
// fleet TUI surfaces a row whose status text suggests a next command,
// pressing Enter on that row dispatches the corresponding action
// without copy-paste — see installer-prd §9.9.3 "Actionable status hints".
//
// Add new Kind values here as new fix types appear; the TUI's row-Enter
// handler grows a switch arm per Kind so unknown actions degrade
// gracefully (Enter no-ops with a footer hint instead of crashing).
type FixAction struct {
	Kind   FixActionKind
	Domain string // for AdminLogin / TenantLogin
	Org    string // for TenantLogin only — empty until org-prompt lands
}

// FixActionKind enumerates the actions the TUI knows how to dispatch.
type FixActionKind string

const (
	// FixActionAdminLogin: run `kube-dc login --domain <Domain> --admin`.
	// Used when the cluster's Detail says "not logged in" or "auth failed"
	// and the fleet view's canonical identity is platform-admin.
	FixActionAdminLogin FixActionKind = "admin-login"
	// FixActionTenantLogin: run `kube-dc login --domain <Domain> --org <Org>`.
	// Reserved for the per-org tenant flow; not yet wired in v1.
	FixActionTenantLogin FixActionKind = "tenant-login"
)

// ReconcilerStatus is one row in the per-cluster Discover detail view.
// Reason / Message are the Ready condition's reason / message (the
// canonical "what's wrong"); Conditions carries every condition the
// Kustomization reports so the TUI's drill-down (§9.9.4) can show the
// full picture, not just Ready=False.
type ReconcilerStatus struct {
	Name                   string
	Ready                  bool
	Reason                 string
	Message                string
	Suspended              bool        // .spec.suspend
	LastAppliedRevision    string      // last revision that successfully reconciled
	LastAttemptedRevision  string      // revision the controller is currently trying
	Conditions             []Condition // every status.condition entry, verbatim
}

// Condition is the fleet-probe-side view of a Kubernetes condition. We
// duplicate the (private) `condition` JSON struct here as a public type
// so the TUI's drill-down panel can render the full list without
// reaching into discover-internal fields.
type Condition struct {
	Type    string
	Status  string // "True" | "False" | "Unknown"
	Reason  string
	Message string
}

// ImageDrift reports a single Deployment whose running image tag differs
// from the tag pinned in cluster-config.env (or whose Deployment is
// missing entirely). A non-empty Drifts list with all Kustomizations
// Ready promotes ClusterStatus to Drifted.
type ImageDrift struct {
	Deployment string // e.g. "kube-dc-manager"
	Namespace  string // e.g. "kube-dc"
	EnvVar     string // e.g. "KUBE_DC_MANAGER_TAG"
	Expected   string // tag from cluster-config.env
	Running    string // tag from the live Deployment, "" if Deployment missing
}

// ClusterProbe inspects one cluster's Flux Kustomization graph and
// aggregates it into a single ClusterStatus. It uses the kube-dc OIDC
// exec-plugin pattern — the operator's cached OIDC tokens at
// ~/.kube-dc/credentials/ — to mint a bearer token, and fetches the
// API server's CA at construction time so HTTPS verification works for
// self-signed clusters (the kube-apiserver endpoint is rarely fronted
// by a publicly-trusted cert).
//
// When ExpectedTags is non-nil the probe also checks the named
// Deployments' image tags and reports drift; an otherwise-Ready cluster
// with one or more drifts becomes StatusDrifted.
//
// Lifetime: one ClusterProbe per cluster row in the fleet view. The
// HTTP client and CA pool are reusable across .Run() calls.
type ClusterProbe struct {
	apiURL     string
	provider   *credential.Provider
	httpClient *http.Client

	// ExpectedTags maps "<namespace>/<deployment>" → {EnvVar, Tag}.
	// Populated by the caller from cluster-config.env's *_TAG vars.
	ExpectedTags map[string]ExpectedTag
}

// ExpectedTag holds one expected Deployment image tag and the env var
// it came from (for the drift report).
type ExpectedTag struct {
	EnvVar string // e.g. "KUBE_DC_MANAGER_TAG"
	Tag    string // e.g. "v0.1.35"
}

// DefaultExpectedTags returns the standard kube-dc Deployment → env-var
// map used for drift detection across every cluster (matches the keys
// from CLAUDE.md's "Pushing dev images to live clusters" section). The
// caller fills in the actual tag values from `clusters/<name>/cluster-config.env`.
//
// Returns a fresh map; safe to mutate.
func DefaultExpectedTags(env interface {
	GetOr(key, fallback string) string
}) map[string]ExpectedTag {
	pairs := []struct {
		nsName string
		envVar string
	}{
		{"kube-dc/kube-dc-manager", "KUBE_DC_MANAGER_TAG"},
		{"kube-dc/kube-dc-backend", "KUBE_DC_BACKEND_TAG"},
		{"kube-dc/kube-dc-frontend", "KUBE_DC_FRONTEND_TAG"},
		{"kube-dc/kube-dc-k8-manager", "KUBE_DC_K8_MANAGER_TAG"},
		{"kube-dc/db-manager", "DB_MANAGER_TAG"},
	}
	out := make(map[string]ExpectedTag, len(pairs))
	for _, p := range pairs {
		if tag := env.GetOr(p.envVar, ""); tag != "" {
			out[p.nsName] = ExpectedTag{EnvVar: p.envVar, Tag: tag}
		}
	}
	return out
}

// NewClusterProbe builds a probe for the given API server URL. dialTimeout
// caps the CA-fetch handshake. Returns a probe whose .Run is safe to call
// from a tea.Cmd goroutine.
func NewClusterProbe(ctx context.Context, apiURL string, dialTimeout time.Duration) (*ClusterProbe, error) {
	prov, err := credential.NewProvider()
	if err != nil {
		return nil, fmt.Errorf("init credential provider: %w", err)
	}

	tlsConfig, err := buildTLSConfig(ctx, apiURL, dialTimeout)
	if err != nil {
		// CA fetch failed — fall back to system trust only. .Run() will
		// surface the failure as Unreachable on first call.
		tlsConfig = &tls.Config{} // zero value uses system trust
	}

	return &ClusterProbe{
		apiURL:   strings.TrimRight(apiURL, "/"),
		provider: prov,
		httpClient: &http.Client{
			Timeout:   10 * time.Second,
			Transport: &http.Transport{TLSClientConfig: tlsConfig},
		},
	}, nil
}

// Run probes the cluster and returns a single aggregate status.
func (p *ClusterProbe) Run(ctx context.Context) ProbeResult {
	if p == nil {
		return ProbeResult{Status: StatusUnknown, Detail: "no probe configured"}
	}

	// Mint an OIDC bearer token. If the operator hasn't `kube-dc login`'d
	// to this cluster, surface that with the right command to copy/paste.
	cred, err := p.provider.GetCredential(p.apiURL)
	if err != nil {
		hint, action := hintLogin(p.apiURL)
		return ProbeResult{
			Status:    StatusUnreachable,
			Detail:    err.Error(),
			FixHint:   hint,
			FixAction: action,
		}
	}

	// List Kustomizations in flux-system. The k8s API returns a typed
	// list under .items even for CRDs.
	url := p.apiURL + "/apis/kustomize.toolkit.fluxcd.io/v1/namespaces/flux-system/kustomizations"
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, url, nil)
	if err != nil {
		return ProbeResult{Status: StatusUnknown, Detail: "build request: " + err.Error()}
	}
	req.Header.Set("Authorization", "Bearer "+cred.Status.Token)
	req.Header.Set("Accept", "application/json")

	resp, err := p.httpClient.Do(req)
	if err != nil {
		return ProbeResult{
			Status:  StatusUnreachable,
			Detail:  shortenErr(err),
			FixHint: "API server not reachable from this host (tunnel? firewall?)",
		}
	}
	defer resp.Body.Close()

	body, _ := io.ReadAll(io.LimitReader(resp.Body, 4*1024*1024))

	switch {
	case resp.StatusCode == http.StatusNotFound:
		// flux-system namespace exists but Kustomization CRD missing →
		// Flux is not installed.
		return ProbeResult{
			Status:  StatusUnknown,
			Detail:  "Flux not installed (no Kustomization CRD)",
			FixHint: "run `kube-dc bootstrap install` (greenfield) or `adopt` (existing)",
		}
	case resp.StatusCode == http.StatusUnauthorized || resp.StatusCode == http.StatusForbidden:
		hint, action := hintLogin(p.apiURL)
		return ProbeResult{
			Status:    StatusUnreachable,
			Detail:    fmt.Sprintf("auth failed (%d)", resp.StatusCode),
			FixHint:   hint,
			FixAction: action,
		}
	case resp.StatusCode >= 500:
		return ProbeResult{
			Status: StatusFailed,
			Detail: fmt.Sprintf("API server %d: %s", resp.StatusCode, firstLine(string(body))),
		}
	case resp.StatusCode != http.StatusOK:
		return ProbeResult{
			Status: StatusUnknown,
			Detail: fmt.Sprintf("unexpected status %d", resp.StatusCode),
		}
	}

	var list kustomizationList
	if err := json.Unmarshal(body, &list); err != nil {
		return ProbeResult{Status: StatusUnknown, Detail: "decode kustomizations: " + err.Error()}
	}
	res := aggregate(list.Items)

	// Drift check: only run when Kustomizations are otherwise healthy
	// (Ready or Reconciling). Drift on a Failed cluster isn't useful —
	// the failed reconciler is the bigger problem and the Failed status
	// already takes precedence.
	if len(p.ExpectedTags) > 0 && (res.Status == StatusReady || res.Status == StatusReconciling) {
		drifts := p.detectDrift(ctx, cred.Status.Token)
		res.Drifts = drifts
		if len(drifts) > 0 && res.Status == StatusReady {
			res.Status = StatusDrifted
			res.Detail = fmt.Sprintf("%d image-tag drift%s (Kustomizations Ready)", len(drifts), pluralise(len(drifts)))
			res.FixHint = "live tags differ from cluster-config.env's *_TAG vars — `flux reconcile kustomization platform` or update env"
		}
	}
	return res
}

// detectDrift fetches every Deployment whose namespace appears in
// p.ExpectedTags and compares the running image tag to the expected tag.
// Returns the list of drifts in deterministic order (sorted by namespace
// then name); an empty slice means in-sync.
//
// Failures (Deployment missing, network glitch, permissions) are surfaced
// as a "missing" drift entry rather than aborting the whole probe — the
// fleet view should still render with stale Kustomization data.
func (p *ClusterProbe) detectDrift(ctx context.Context, token string) []ImageDrift {
	// Group expectations by namespace so we list each namespace once.
	byNamespace := map[string][]struct {
		Name string
		Want ExpectedTag
	}{}
	for nsName, exp := range p.ExpectedTags {
		ns, name := splitNamespacedName(nsName)
		byNamespace[ns] = append(byNamespace[ns], struct {
			Name string
			Want ExpectedTag
		}{Name: name, Want: exp})
	}

	var out []ImageDrift
	for ns, wants := range byNamespace {
		url := fmt.Sprintf("%s/apis/apps/v1/namespaces/%s/deployments", p.apiURL, ns)
		req, err := http.NewRequestWithContext(ctx, http.MethodGet, url, nil)
		if err != nil {
			continue
		}
		req.Header.Set("Authorization", "Bearer "+token)
		req.Header.Set("Accept", "application/json")

		resp, err := p.httpClient.Do(req)
		if err != nil {
			continue
		}
		body, _ := io.ReadAll(io.LimitReader(resp.Body, 4*1024*1024))
		_ = resp.Body.Close()
		if resp.StatusCode != http.StatusOK {
			continue
		}

		var list deploymentList
		if err := json.Unmarshal(body, &list); err != nil {
			continue
		}

		// Index by name for O(1) lookup against expectations.
		live := make(map[string]string, len(list.Items))
		for _, d := range list.Items {
			if len(d.Spec.Template.Spec.Containers) == 0 {
				continue
			}
			// Use the first container — kube-dc Deployments are single-container.
			live[d.Metadata.Name] = imageTag(d.Spec.Template.Spec.Containers[0].Image)
		}

		for _, w := range wants {
			running, found := live[w.Name]
			switch {
			case !found:
				// Deployment doesn't exist on this cluster — surface as
				// drift with empty Running. The HelmRelease may not have
				// reconciled yet, or this Deployment isn't installed.
				out = append(out, ImageDrift{
					Deployment: w.Name, Namespace: ns,
					EnvVar: w.Want.EnvVar, Expected: w.Want.Tag, Running: "",
				})
			case running != w.Want.Tag:
				out = append(out, ImageDrift{
					Deployment: w.Name, Namespace: ns,
					EnvVar: w.Want.EnvVar, Expected: w.Want.Tag, Running: running,
				})
			}
		}
	}
	sortDrifts(out)
	return out
}

// kustomizationList mirrors the JSON shape the API server returns for
// `GET /apis/kustomize.toolkit.fluxcd.io/v1/namespaces/flux-system/kustomizations`.
// Only the fields the probe actually reads are declared.
type kustomizationList struct {
	Items []kustomization `json:"items"`
}

type kustomization struct {
	Metadata struct {
		Name string `json:"name"`
	} `json:"metadata"`
	Spec struct {
		Suspend bool `json:"suspend"`
	} `json:"spec"`
	Status struct {
		Conditions            []condition `json:"conditions"`
		LastAppliedRevision   string      `json:"lastAppliedRevision"`
		LastAttemptedRevision string      `json:"lastAttemptedRevision"`
	} `json:"status"`
}

type condition struct {
	Type    string `json:"type"`
	Status  string `json:"status"` // "True" | "False" | "Unknown"
	Reason  string `json:"reason"`
	Message string `json:"message"`
}

// deploymentList mirrors the (subset of) JSON the API server returns for
// `GET /apis/apps/v1/namespaces/<ns>/deployments`. Only fields the drift
// detector reads are declared.
type deploymentList struct {
	Items []deployment `json:"items"`
}

type deployment struct {
	Metadata struct {
		Name      string `json:"name"`
		Namespace string `json:"namespace"`
	} `json:"metadata"`
	Spec struct {
		Template struct {
			Spec struct {
				Containers []struct {
					Image string `json:"image"`
				} `json:"containers"`
			} `json:"spec"`
		} `json:"template"`
	} `json:"spec"`
}

// imageTag extracts the tag from a container image reference. Returns
// "latest" if no tag is specified, matching Docker semantics. Strips
// any digest suffix ("@sha256:…") so tag drift detection isn't fooled
// by digest pinning on the server side.
func imageTag(image string) string {
	if at := strings.IndexByte(image, '@'); at >= 0 {
		image = image[:at]
	}
	// Find the last colon that isn't a port separator (i.e. the colon
	// after the last "/"). registry:5000/foo/bar:v1 → tag "v1".
	slash := strings.LastIndexByte(image, '/')
	colon := strings.LastIndexByte(image, ':')
	if colon > slash {
		return image[colon+1:]
	}
	return "latest"
}

// splitNamespacedName splits "ns/name" into its parts. Returns
// ("", whole) if there's no slash (caller should treat as same-ns).
func splitNamespacedName(ref string) (namespace, name string) {
	i := strings.IndexByte(ref, '/')
	if i < 0 {
		return "", ref
	}
	return ref[:i], ref[i+1:]
}

// sortDrifts orders ImageDrift records by namespace then deployment name
// for deterministic test output and stable rendering in the details pane.
func sortDrifts(d []ImageDrift) {
	for i := 1; i < len(d); i++ {
		for j := i; j > 0; j-- {
			a, b := d[j-1], d[j]
			if a.Namespace > b.Namespace || (a.Namespace == b.Namespace && a.Deployment > b.Deployment) {
				d[j-1], d[j] = b, a
				continue
			}
			break
		}
	}
}

// aggregate maps the per-Kustomization condition set to a single cluster
// status. Precedence (highest wins): Failed → Reconciling → Ready → Unknown.
// Drifted detection (image-tag mismatch) is a slice-2 follow-up.
func aggregate(ks []kustomization) ProbeResult {
	if len(ks) == 0 {
		return ProbeResult{
			Status: StatusUnknown,
			Detail: "no Kustomizations in flux-system",
			FixHint: "run `kube-dc bootstrap install` to seed the fleet, or `adopt` if Flux exists elsewhere",
		}
	}

	out := ProbeResult{Reconcilers: make([]ReconcilerStatus, 0, len(ks))}
	var failed, reconciling, readyTrue, unknownReady int

	for _, k := range ks {
		var readyCond *condition
		var reconCond *condition
		conds := make([]Condition, 0, len(k.Status.Conditions))
		for i := range k.Status.Conditions {
			c := &k.Status.Conditions[i]
			conds = append(conds, Condition{Type: c.Type, Status: c.Status, Reason: c.Reason, Message: c.Message})
			switch c.Type {
			case "Ready":
				readyCond = c
			case "Reconciling":
				reconCond = c
			}
		}

		rs := ReconcilerStatus{
			Name:                  k.Metadata.Name,
			Suspended:             k.Spec.Suspend,
			LastAppliedRevision:   k.Status.LastAppliedRevision,
			LastAttemptedRevision: k.Status.LastAttemptedRevision,
			Conditions:            conds,
		}
		switch {
		case readyCond == nil:
			rs.Ready = false
			rs.Reason = "NoReadyCondition"
			rs.Message = "Kustomization has no Ready condition yet — Flux either hasn't observed it or hasn't attempted reconciliation. Try `flux reconcile kustomization " + k.Metadata.Name + " -n flux-system`."
			unknownReady++
		case readyCond.Status == "True":
			rs.Ready = true
			rs.Reason = readyCond.Reason
			rs.Message = readyCond.Message
			readyTrue++
		case readyCond.Status == "False":
			rs.Ready = false
			rs.Reason = readyCond.Reason
			rs.Message = readyCond.Message
			// A Kustomization that's actively reconciling and not yet
			// settled also reports Ready=False; treat those as
			// Reconciling rather than Failed when the explicit
			// Reconciling condition is True.
			if reconCond != nil && reconCond.Status == "True" {
				reconciling++
			} else {
				failed++
			}
		default: // "Unknown" or empty
			rs.Ready = false
			rs.Reason = readyCond.Reason
			rs.Message = readyCond.Message
			unknownReady++
		}
		out.Reconcilers = append(out.Reconcilers, rs)
	}

	switch {
	case failed > 0:
		out.Status = StatusFailed
		out.Detail = fmt.Sprintf("%d failed reconciler%s", failed, pluralise(failed))
	case reconciling > 0:
		out.Status = StatusReconciling
		out.Detail = fmt.Sprintf("%d/%d reconciling", reconciling, len(ks))
	case readyTrue == len(ks):
		out.Status = StatusReady
		out.Detail = fmt.Sprintf("%d/%d Ready", readyTrue, len(ks))
	default:
		out.Status = StatusUnknown
		out.Detail = fmt.Sprintf("%d ready, %d unknown", readyTrue, unknownReady)
	}
	return out
}

// buildTLSConfig fetches the API server's CA via TLS handshake and pins
// it. Returns nil + nil on system-trust success (publicly-trusted chain),
// in which case the caller can use http.DefaultTransport.
func buildTLSConfig(ctx context.Context, apiURL string, dialTimeout time.Duration) (*tls.Config, error) {
	caPEM, err := FetchCA(ctx, apiURL, dialTimeout)
	if err != nil {
		return nil, err
	}
	if caPEM == "" {
		// Public chain — system trust is enough.
		return nil, nil
	}
	pool := x509.NewCertPool()
	if !pool.AppendCertsFromPEM([]byte(caPEM)) {
		return nil, errors.New("failed to add fetched CA to pool")
	}
	return &tls.Config{RootCAs: pool}, nil
}

// hintLogin returns the next-step suggestion for an Unreachable cluster
// row in the fleet view. The fleet view is a platform-operator tool —
// the canonical "I need to manage this cluster" identity is admin, so
// we lead with --admin and list --org as the tenant alternative.
//
// Returns both the human-readable hint (for the row's detail line) and
// the structured FixAction (for Enter-on-row dispatch in the TUI).
func hintLogin(apiURL string) (string, *FixAction) {
	domain := apiURL
	if strings.HasPrefix(domain, "https://kube-api.") {
		rest := strings.TrimPrefix(domain, "https://kube-api.")
		if i := strings.Index(rest, ":"); i >= 0 {
			rest = rest[:i]
		}
		domain = rest
	}
	hint := fmt.Sprintf("run `kube-dc login --domain %s --admin`  (or --org <your-org> for tenant)", domain)
	return hint, &FixAction{Kind: FixActionAdminLogin, Domain: domain}
}

func shortenErr(err error) string {
	s := err.Error()
	// Cut HTTP-client wrapping noise to keep the fleet-row detail tight.
	if i := strings.Index(s, ": "); i > 0 && i < 60 {
		s = s[i+2:]
	}
	if len(s) > 80 {
		s = s[:77] + "…"
	}
	return s
}

func firstLine(s string) string {
	if i := strings.IndexByte(s, '\n'); i >= 0 {
		return s[:i]
	}
	if len(s) > 80 {
		return s[:77] + "…"
	}
	return s
}

func pluralise(n int) string {
	if n == 1 {
		return ""
	}
	return "s"
}
