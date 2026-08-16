package main

import (
	"context"
	"errors"
	"fmt"
	"io"
	"os"
	"time"

	apierrors "k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/client-go/kubernetes"
	"k8s.io/client-go/tools/clientcmd"

	"github.com/shalb/kube-dc/cli/internal/bootstrap/clusterinit"
	"github.com/shalb/kube-dc/cli/internal/bootstrap/mock"
)

// realModeProber implements clusterinit.ModeProber against a live
// kubeconfig. Used by `kube-dc bootstrap init --mode=auto` to drive
// the install / adopt / resume branch decision.
//
// **Design**: three minimal Kubernetes API calls, in order:
//
//  1. GET ns/kube-system  → K8sReachable (canonical "is API alive" check)
//  2. GET ns/flux-system  → FluxSystemPresent
//  3. GET deploy/kube-dc-manager in ns/kube-dc → KubeDCManagerPresent
//
// If step 1 fails, we short-circuit with K8sReachable=false (lets
// DetectMode return the typed ErrK8sUnreachable). Steps 2 + 3 treat
// NotFound as "absent" (not an error); other errors propagate.
//
// **Timeouts**: each call inherits the caller's context. The cobra
// layer wraps with a 5s deadline so a hung kubeconfig context
// doesn't block the whole `init` indefinitely.
type realModeProber struct {
	client kubernetes.Interface
}

// newRealModeProber constructs the prober from the standard
// kubeconfig precedence (--kubeconfig flag value → KUBECONFIG env
// → ~/.kube/config → in-cluster). Returns an error if no config can
// be resolved — auto-detection requires a kubeconfig per
// installer-prd §4.1.1.
//
// Empty `kubeconfigPath` triggers the default precedence; an
// explicit path overrides.
func newRealModeProber(kubeconfigPath string) (*realModeProber, error) {
	rules := clientcmd.NewDefaultClientConfigLoadingRules()
	if kubeconfigPath != "" {
		rules.ExplicitPath = kubeconfigPath
	}
	cfg, err := clientcmd.NewNonInteractiveDeferredLoadingClientConfig(
		rules, &clientcmd.ConfigOverrides{},
	).ClientConfig()
	if err != nil {
		return nil, fmt.Errorf("--mode=auto: load kubeconfig: %w", err)
	}
	core, err := kubernetes.NewForConfig(cfg)
	if err != nil {
		return nil, fmt.Errorf("--mode=auto: build k8s client: %w", err)
	}
	return &realModeProber{client: core}, nil
}

// Probe implements clusterinit.ModeProber. Returns
// ModeProbeInputs{K8sReachable:false} on apiserver failure (so
// DetectMode emits the typed ErrK8sUnreachable) and propagates any
// non-NotFound errors from the existence probes.
func (p *realModeProber) Probe(ctx context.Context) (clusterinit.ModeProbeInputs, error) {
	if p == nil || p.client == nil {
		return clusterinit.ModeProbeInputs{}, fmt.Errorf("modeprobe: nil client (internal wiring bug)")
	}
	in := clusterinit.ModeProbeInputs{}

	// (1) K8s reachable — probe a guaranteed-present system namespace.
	// We use Get rather than List because the latter walks all
	// namespaces and triggers RBAC issues on locked-down clusters
	// while a single-namespace Get only needs `get` on namespaces.
	if _, err := p.client.CoreV1().Namespaces().Get(ctx, "kube-system", metav1.GetOptions{}); err != nil {
		// Any error here (timeout, dial refused, RBAC) means the
		// auto-detect contract is unsatisfiable. Return K8sReachable=false
		// rather than propagating — DetectMode will emit the typed
		// ErrK8sUnreachable, which carries the right operator message.
		return in, nil
	}
	in.K8sReachable = true

	// (2) flux-system namespace presence — NotFound is meaningful
	// (means we're on a fresh K8s); any other error propagates.
	if _, err := p.client.CoreV1().Namespaces().Get(ctx, "flux-system", metav1.GetOptions{}); err != nil {
		if !apierrors.IsNotFound(err) {
			return in, fmt.Errorf("modeprobe: check flux-system namespace: %w", err)
		}
	} else {
		in.FluxSystemPresent = true
	}

	// (3) kube-dc-manager Deployment presence in the kube-dc namespace.
	// Whether the Deployment exists is a stronger signal than the
	// namespace alone (operators sometimes create the kube-dc
	// namespace ahead of time for RBAC; the Deployment is the actual
	// install marker). NotFound on either the namespace or the
	// Deployment counts as "manager absent".
	if _, err := p.client.AppsV1().Deployments("kube-dc").Get(ctx, "kube-dc-manager", metav1.GetOptions{}); err != nil {
		if !apierrors.IsNotFound(err) {
			return in, fmt.Errorf("modeprobe: check kube-dc-manager deployment: %w", err)
		}
	} else {
		in.KubeDCManagerPresent = true
	}

	return in, nil
}

// modeProbeTimeout caps the auto-detection probe at 5s — the same
// budget M1 doctor probes use. Long enough for a healthy cluster on
// a slow link; short enough that a hung kubeconfig doesn't block
// the whole `init` run.
const modeProbeTimeout = 5 * time.Second

// ---------------------------------------------------------------------------
// --mode=auto as the DEFAULT (2026-08-16), made safe.
//
// A first attempt defaulted to auto with a "no kubeconfig → install" fallback
// keyed on clientcmd.IsEmptyConfig. Codex review rejected it: IsEmptyConfig
// also fires for a typo'd KUBECONFIG=/missing, an empty file, or a file with
// no current-context — so a live cluster could be silently INSTALLED over,
// skipping the adopt version-pin gate. And no local kubeconfig never proves
// the --ssh-host target is fresh. This is the corrected shape:
//
//  1. greenfield is inferred ONLY when no kubeconfig source is configured at
//     all (KUBECONFIG unset/empty AND no default file AND not in-cluster) —
//     a configured-but-unusable source is an ERROR, never greenfield;
//  2. a greenfield resolution is PROVISIONAL: it is good enough to plan, and
//     the apply engine RE-PROBES after it has fetched the cluster's
//     kubeconfig, before the first mutation; a live answer that is not
//     `install` refuses the run (the safe direction);
//  3. KUBE_DC_MOCK is honoured so mock/CI runs never touch a real kubeconfig.
// ---------------------------------------------------------------------------

// errNoKubeconfigConfigured marks the STRICT greenfield condition: no
// kubeconfig source is configured anywhere. Distinct from "configured but
// unloadable", which stays a hard error.
var errNoKubeconfigConfigured = errors.New("--mode=auto: no kubeconfig source configured (KUBECONFIG unset, no ~/.kube/config, not in-cluster)")

// noKubeconfigSourceConfigured reports the strict greenfield condition. It
// deliberately does NOT use clientcmd.IsEmptyConfig (see the block comment):
// a KUBECONFIG that names a missing file is a configured source that failed,
// not the absence of a source.
func noKubeconfigSourceConfigured() bool {
	return noKubeconfigSourceIn(os.Getenv("KUBECONFIG"),
		clientcmd.NewDefaultClientConfigLoadingRules().Precedence,
		os.Getenv("KUBERNETES_SERVICE_HOST"))
}

// noKubeconfigSourceIn is the testable core of noKubeconfigSourceConfigured.
// `defaults` is the file list client-go itself would consult (its
// RecommendedHomeFile is baked at process start from HOME/USERPROFILE, so
// tests inject a list instead of re-pointing HOME).
func noKubeconfigSourceIn(kubeconfigEnv string, defaults []string, inClusterHost string) bool {
	// Any non-empty KUBECONFIG — even whitespace — is a configured path to
	// client-go (it would try to load/write it), so it counts as a source.
	if kubeconfigEnv != "" {
		return false
	}
	// Any default candidate that exists — or cannot be stat'ed for a reason
	// other than not-exist (permission, broken symlink) — counts as
	// configured: fail closed.
	for _, candidate := range defaults {
		if _, err := os.Stat(candidate); !errors.Is(err, os.ErrNotExist) {
			return false
		}
	}
	if inClusterHost != "" {
		return false // in-cluster config is a source
	}
	return true
}

// mockModeProber implements clusterinit.ModeProber from a KUBE_DC_MOCK
// scenario fixture — the same facts `bootstrap status`/doctor consume — so a
// mock run resolves the scenario's mode instead of probing (or falling back
// on) the developer's real kubeconfig.
type mockModeProber struct{ sc *mock.Scenario }

func (m *mockModeProber) Probe(context.Context) (clusterinit.ModeProbeInputs, error) {
	if m == nil || m.sc == nil {
		return clusterinit.ModeProbeInputs{}, fmt.Errorf("modeprobe: nil mock scenario")
	}
	c := m.sc.Cluster
	if c == nil {
		return clusterinit.ModeProbeInputs{K8sReachable: false}, nil
	}
	// Scenario contract: a non-nil Cluster IS reachable (status/doctor read
	// it the same way). kube-dc-manager presence = the kube-dc HelmRelease
	// or its deployment image in the fixture — a scenario that lists the
	// platform as installed but omits DeploymentImages (openbao-sealed) must
	// still read as resume, matching `bootstrap status`.
	_, mgr := c.DeploymentImages["kube-dc"]["kube-dc-manager"]
	if !mgr {
		for _, hr := range c.HelmReleases {
			if hr.Name == "kube-dc" {
				mgr = true
				break
			}
		}
	}
	return clusterinit.ModeProbeInputs{
		K8sReachable:         true,
		FluxSystemPresent:    c.FluxInstalled,
		KubeDCManagerPresent: mgr,
	}, nil
}

// newModeProber picks the prober: mock scenario when KUBE_DC_MOCK is set,
// otherwise the real client-go prober. A strict-greenfield environment
// returns errNoKubeconfigConfigured so the caller can choose the
// provisional path; any other construction failure is a real error.
func newModeProber() (clusterinit.ModeProber, error) {
	if scenario := os.Getenv("KUBE_DC_MOCK"); scenario != "" {
		sc, err := mock.Load(scenario)
		if err != nil {
			return nil, fmt.Errorf("--mode=auto: load mock scenario %q: %w", scenario, err)
		}
		return &mockModeProber{sc: sc}, nil
	}
	if noKubeconfigSourceConfigured() {
		return nil, errNoKubeconfigConfigured
	}
	return newRealModeProber("")
}

// reprobeModeAfterFetch is the apply-time safety net for a PROVISIONAL
// greenfield resolution: called after the engine has fetched (or found) the
// cluster's kubeconfig and before the first mutation. If the live cluster
// resolves to anything other than the mode the run was planned with, the run
// is refused with the adopt/resume remediation — a plan reviewed as
// "install" must never be applied over a live Flux-managed cluster.
func reprobeModeAfterFetch(ctx context.Context, out io.Writer, planned clusterinit.Mode) error {
	if ctx == nil {
		ctx = context.Background()
	}
	probeCtx, cancel := context.WithTimeout(ctx, modeProbeTimeout)
	defer cancel()
	prober, err := newModeProber()
	if err != nil {
		if errors.Is(err, errNoKubeconfigConfigured) {
			// Still nothing to inspect (no --ssh-host fetch happened) —
			// the engine's own session build reports that; not our call.
			return nil
		}
		return fmt.Errorf("apply: re-probe cluster mode: %w", err)
	}
	in, err := prober.Probe(probeCtx)
	if err != nil {
		return fmt.Errorf("apply: re-probe cluster mode: %w", err)
	}
	live, reason, err := clusterinit.DetectMode(in)
	if err != nil {
		// FAIL CLOSED (codex 2026-08-16): a timeout / TLS / RBAC / transient
		// failure here must not let an install plan proceed over a cluster
		// we could not classify — connectivity can recover before
		// flux-install.sh runs. The operator re-runs when the cluster
		// answers, or pins --mode explicitly.
		return fmt.Errorf("apply: re-probe cluster mode: %w — refusing to continue with planned mode %s while the cluster cannot be classified (retry when the apiserver answers, or pass --mode explicitly)", err, planned)
	}
	if live != planned {
		return fmt.Errorf("apply: the live cluster resolves to mode %s (%s) but this run was planned as %s — "+
			"refusing to continue. Re-run with --mode=%s (dry-run first) so the plan matches the cluster; "+
			"a %s plan must never be applied over a live Flux-managed cluster",
			live, reason, planned, live, planned)
	}
	fmt.Fprintf(out, "[apply] mode re-probe confirmed: %s — %s\n", live, reason)
	return nil
}
