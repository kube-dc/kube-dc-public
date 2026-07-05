package main

import (
	"context"
	"fmt"
	"time"

	apierrors "k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/client-go/kubernetes"
	"k8s.io/client-go/tools/clientcmd"

	"github.com/shalb/kube-dc/cli/internal/bootstrap/clusterinit"
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
