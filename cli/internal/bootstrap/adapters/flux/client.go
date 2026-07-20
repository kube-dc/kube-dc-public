// Package flux is the real ports.FluxClient adapter. Bootstrap shells
// out to the `flux` CLI (per ports/flux.go: "flux is too feature-rich
// to wrap natively in v1"); watches use client-go's dynamic informer
// against the Kustomization / HelmRelease CRDs.
//
// **No fluxcd typed client dependency** — that would couple this
// adapter to flux's release cadence + dependency tree. The dynamic
// client gives us untyped access via GVK and we translate to
// `ports.KustomizationEvent` / `ports.HelmReleaseEvent` shapes.
package flux

import (
	"bytes"
	"context"
	"fmt"
	"os"
	"os/exec"
	"time"

	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
	"k8s.io/apimachinery/pkg/runtime/schema"
	"k8s.io/apimachinery/pkg/watch"
	"k8s.io/client-go/dynamic"
	"k8s.io/client-go/rest"
	"k8s.io/client-go/tools/clientcmd"

	"github.com/shalb/kube-dc/cli/internal/bootstrap/ports"
)

const fluxSystemNamespace = "flux-system"

var (
	kustomizationGVR = schema.GroupVersionResource{
		Group:    "kustomize.toolkit.fluxcd.io",
		Version:  "v1",
		Resource: "kustomizations",
	}
	helmReleaseGVR = schema.GroupVersionResource{
		Group:    "helm.toolkit.fluxcd.io",
		Version:  "v2",
		Resource: "helmreleases",
	}
)

// Client implements ports.FluxClient.
type Client struct {
	rest *rest.Config
	dyn  dynamic.Interface

	// runFluxCmd builds the `flux` invocation. Tests swap to inject a
	// canned exit code without requiring the binary to exist.
	runFluxCmd func(ctx context.Context, env []string, args ...string) ([]byte, []byte, error)
}

// New constructs a Client with the operator's kubeconfig + a real
// `flux` CLI path resolution (whatever `flux` resolves to in $PATH).
func New(kubeconfigPath string) (*Client, error) {
	rules := clientcmd.NewDefaultClientConfigLoadingRules()
	if kubeconfigPath != "" {
		rules.ExplicitPath = kubeconfigPath
	}
	cfg, err := clientcmd.NewNonInteractiveDeferredLoadingClientConfig(rules, &clientcmd.ConfigOverrides{}).ClientConfig()
	if err != nil {
		return nil, fmt.Errorf("flux: load kubeconfig: %w", err)
	}
	dyn, err := dynamic.NewForConfig(cfg)
	if err != nil {
		return nil, fmt.Errorf("flux: build dynamic client: %w", err)
	}
	c := &Client{rest: cfg, dyn: dyn}
	c.runFluxCmd = realFluxCmd
	return c, nil
}

// Bootstrap shells out to `flux bootstrap github`. Synchronous; the
// upstream CLI manages polling for flux-system pod readiness itself.
func (c *Client) Bootstrap(ctx context.Context, opts ports.FluxBootstrapOpts) error {
	if opts.Token == "" {
		return fmt.Errorf("flux: Bootstrap requires Token (GitHub PAT)")
	}
	args := []string{
		"bootstrap", "github",
		"--owner=" + opts.GitHubOwner,
		"--repository=" + opts.GitHubRepo,
		"--path=" + opts.Path,
	}
	if opts.Personal {
		args = append(args, "--personal")
	}
	if opts.Branch != "" {
		args = append(args, "--branch="+opts.Branch)
	} else {
		args = append(args, "--branch=main")
	}
	// Token via env, NEVER as a CLI arg (/proc/PID/cmdline would
	// leak it). Same pattern as flux-install.sh.
	env := []string{
		"GITHUB_TOKEN=" + opts.Token,
	}
	if opts.Kubeconfig != "" {
		env = append(env, "KUBECONFIG="+opts.Kubeconfig)
	}

	_, stderr, err := c.runFluxCmd(ctx, env, args...)
	if err != nil {
		return fmt.Errorf("flux bootstrap github: %w (stderr: %s)", err, bytes.TrimSpace(stderr))
	}
	return nil
}

// WatchKustomizations streams condition changes. Uses client-go's
// dynamic Watch directly — informer caching would be overkill for the
// few dozen Kustomizations a cluster has.
//
// **ctx-aware send** (M0-T06 batch-2 review): sends to `out` are
// guarded by `<-ctx.Done()` so a slow / stopped consumer can't park
// the watch goroutine indefinitely.
func (c *Client) WatchKustomizations(ctx context.Context) (<-chan ports.KustomizationEvent, error) {
	out := make(chan ports.KustomizationEvent, 16)
	go func() {
		defer close(out)
		// Kustomizations we care about live in flux-system.
		c.watchLoop(ctx, kustomizationGVR, fluxSystemNamespace, func(u *unstructured.Unstructured) bool {
			ev := kustomizationFromUnstructured(u)
			select {
			case out <- ev:
				return true
			case <-ctx.Done():
				return false
			}
		})
	}()
	return out, nil
}

// WatchHelmReleases streams the same shape for HelmReleases.
func (c *Client) WatchHelmReleases(ctx context.Context) (<-chan ports.HelmReleaseEvent, error) {
	out := make(chan ports.HelmReleaseEvent, 16)
	go func() {
		defer close(out)
		// HelmReleases are cluster-wide — kube-dc's land in kube-system,
		// cert-manager, rook-ceph, cnpg-system, monitoring, etc. (NOT
		// flux-system). Watch ALL namespaces ("") or the reconcile tally
		// reports 0 HelmReleases while the platform is full of Ready ones
		// (kube-dc E2E 2026-07-08: showed "0/0" with 7 HRs Ready).
		c.watchLoop(ctx, helmReleaseGVR, metav1.NamespaceAll, func(u *unstructured.Unstructured) bool {
			ev := helmReleaseFromUnstructured(u)
			select {
			case out <- ev:
				return true
			case <-ctx.Done():
				return false
			}
		})
	}()
	return out, nil
}

// Reconcile triggers a one-shot reconcile via an annotation tweak —
// the canonical flux pattern (`flux reconcile` does this same poke
// + watch underneath). We skip the watch half because callers driving
// this typically have a watch stream open already.
// PullArtifact downloads + extracts a Flux OCI artifact into dir via
// `flux pull artifact` — same shell-out posture as Bootstrap (the flux
// CLI is a doctor-checked install prerequisite, so no extra dependency
// and no OCI client library in our tree). Anonymous pull: the
// fleet-starter artifact is public; no creds are read.
func (c *Client) PullArtifact(ctx context.Context, url, dir string) error {
	if url == "" {
		return fmt.Errorf("flux: PullArtifact requires a url")
	}
	if dir == "" {
		return fmt.Errorf("flux: PullArtifact requires an output dir")
	}
	args := []string{"pull", "artifact", url, "--output", dir}
	_, stderr, err := c.runFluxCmd(ctx, nil, args...)
	if err != nil {
		return fmt.Errorf("flux pull artifact %s: %w (stderr: %s)", url, err, bytes.TrimSpace(stderr))
	}
	return nil
}

func (c *Client) Reconcile(ctx context.Context, kind, name string) error {
	gvr, err := gvrForKind(kind)
	if err != nil {
		return err
	}
	patch := []byte(fmt.Sprintf(`{"metadata":{"annotations":{"reconcile.fluxcd.io/requestedAt":%q}}}`, time.Now().UTC().Format(time.RFC3339Nano)))
	_, err = c.dyn.Resource(gvr).Namespace(fluxSystemNamespace).Patch(ctx, name, "application/merge-patch+json", patch, metav1.PatchOptions{}, "")
	if err != nil {
		return fmt.Errorf("flux: reconcile %s/%s: %w", kind, name, err)
	}
	return nil
}

func gvrForKind(kind string) (schema.GroupVersionResource, error) {
	switch kind {
	case "Kustomization":
		return kustomizationGVR, nil
	case "HelmRelease":
		return helmReleaseGVR, nil
	default:
		return schema.GroupVersionResource{}, fmt.Errorf("flux: unsupported reconcile kind %q (want Kustomization|HelmRelease)", kind)
	}
}

// watchLoop drives the dynamic watch with auto-reconnect on transient
// errors. emit is invoked for every successful unstructured event;
// returning false from emit signals "consumer is gone / ctx done"
// and unwinds the loop without further reconnects.
func (c *Client) watchLoop(ctx context.Context, gvr schema.GroupVersionResource, namespace string, emit func(*unstructured.Unstructured) bool) {
	for {
		select {
		case <-ctx.Done():
			return
		default:
		}
		// LIST first, then Watch from the list's resourceVersion. A bare
		// Watch delivers only *changes* — so on an already-converged,
		// quiet cluster (e.g. a resume against a healthy platform) it
		// would emit nothing and a consumer waiting for "platform Ready"
		// would sit until timeout even though everything is Ready. The
		// initial List emits current state as a snapshot; the subsequent
		// Watch (pinned to the list's resourceVersion) picks up changes
		// without a gap. Re-listing on every reconnect also avoids
		// "resourceVersion too old" (HTTP 410) after a dropped watch.
		list, err := c.dyn.Resource(gvr).Namespace(namespace).List(ctx, metav1.ListOptions{})
		if err != nil {
			select {
			case <-ctx.Done():
				return
			case <-time.After(2 * time.Second):
				continue
			}
		}
		for i := range list.Items {
			if !emit(&list.Items[i]) {
				return
			}
		}
		w, err := c.dyn.Resource(gvr).Namespace(namespace).Watch(ctx, metav1.ListOptions{
			ResourceVersion: list.GetResourceVersion(),
		})
		if err != nil {
			select {
			case <-ctx.Done():
				return
			case <-time.After(2 * time.Second):
				continue
			}
		}
		if !c.consume(ctx, w, emit) {
			return
		}
	}
}

// consume returns false when the consumer signalled "no more events"
// (ctx done while sending). true means the watch ended naturally and
// the loop should reconnect.
func (c *Client) consume(ctx context.Context, w watch.Interface, emit func(*unstructured.Unstructured) bool) bool {
	defer w.Stop()
	for {
		select {
		case <-ctx.Done():
			return false
		case ev, ok := <-w.ResultChan():
			if !ok {
				return true // reconnect
			}
			u, ok := ev.Object.(*unstructured.Unstructured)
			if !ok {
				continue
			}
			if !emit(u) {
				return false
			}
		}
	}
}

// ---------- realFluxCmd ----------

func realFluxCmd(ctx context.Context, env []string, args ...string) ([]byte, []byte, error) {
	cmd := exec.CommandContext(ctx, "flux", args...)
	// Inherit the parent env so flux finds PATH, HOME, HTTPS_PROXY,
	// SSL_CERT_FILE, the operator's git config, etc. Caller-supplied
	// `env` entries layer on top (e.g. GITHUB_TOKEN, KUBECONFIG).
	cmd.Env = append(os.Environ(), env...)
	var stdout, stderr bytes.Buffer
	cmd.Stdout = &stdout
	cmd.Stderr = &stderr
	err := cmd.Run()
	return stdout.Bytes(), stderr.Bytes(), err
}

// ---------- unstructured → typed translators ----------

func kustomizationFromUnstructured(u *unstructured.Unstructured) ports.KustomizationEvent {
	ev := ports.KustomizationEvent{
		Name:      u.GetName(),
		Namespace: u.GetNamespace(),
	}
	if deps, found, _ := unstructured.NestedSlice(u.Object, "spec", "dependsOn"); found {
		for _, d := range deps {
			if m, ok := d.(map[string]interface{}); ok {
				if name, ok := m["name"].(string); ok && name != "" {
					ev.DependsOn = append(ev.DependsOn, name)
				}
			}
		}
	}
	if rev, _, _ := unstructured.NestedString(u.Object, "status", "lastAppliedRevision"); rev != "" {
		ev.Revision = rev
	}
	consumeConditions(u, &ev.Ready, &ev.Reconciling, &ev.Reason, &ev.Message)
	ev.LastApplied, ev.LastAttempt = consumeTimestamps(u)
	return ev
}

func helmReleaseFromUnstructured(u *unstructured.Unstructured) ports.HelmReleaseEvent {
	ev := ports.HelmReleaseEvent{
		Name:      u.GetName(),
		Namespace: u.GetNamespace(),
	}
	if name, _, _ := unstructured.NestedString(u.Object, "spec", "chart", "spec", "chart"); name != "" {
		ev.ChartName = name
	}
	if ver, _, _ := unstructured.NestedString(u.Object, "spec", "chart", "spec", "version"); ver != "" {
		ev.ChartVersion = ver
	}
	consumeConditions(u, &ev.Ready, &ev.Reconciling, &ev.Reason, &ev.Message)
	ev.LastApplied, ev.LastAttempt = consumeTimestamps(u)
	return ev
}

func consumeConditions(u *unstructured.Unstructured, ready, reconciling *bool, reason, message *string) {
	conds, _, _ := unstructured.NestedSlice(u.Object, "status", "conditions")
	for _, c := range conds {
		m, ok := c.(map[string]interface{})
		if !ok {
			continue
		}
		ctype, _ := m["type"].(string)
		cstatus, _ := m["status"].(string)
		creason, _ := m["reason"].(string)
		cmessage, _ := m["message"].(string)
		switch ctype {
		case "Ready":
			*ready = cstatus == "True"
			if !*ready {
				*reason = creason
				*message = cmessage
			}
		case "Reconciling":
			*reconciling = cstatus == "True"
		}
	}
}

func consumeTimestamps(u *unstructured.Unstructured) (lastApplied, lastAttempt time.Time) {
	if s, _, _ := unstructured.NestedString(u.Object, "status", "lastAppliedRevisionTimestamp"); s != "" {
		if t, err := time.Parse(time.RFC3339, s); err == nil {
			lastApplied = t
		}
	}
	if s, _, _ := unstructured.NestedString(u.Object, "status", "lastHandledReconcileAt"); s != "" {
		if t, err := time.Parse(time.RFC3339, s); err == nil {
			lastAttempt = t
		}
	}
	return
}
