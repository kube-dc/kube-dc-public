package mock

import (
	"context"
	"fmt"
	"sort"

	"github.com/shalb/kube-dc/cli/internal/bootstrap/ports"
)

// K8sClient returns fixture data from the scenario's Cluster fixture.
// Methods return ErrFluxNotInstalled / empty maps / etc. per the
// nil-vs-empty contract documented on each port method.
type K8sClient struct {
	scenario *Scenario
}

func NewK8sClient(s *Scenario) *K8sClient { return &K8sClient{scenario: s} }

func (c *K8sClient) DiscoverFluxGraph(ctx context.Context) (ports.Graph, error) {
	if err := ctx.Err(); err != nil {
		return ports.Graph{}, err
	}
	if c.scenario == nil || c.scenario.Cluster == nil {
		// Cluster unreachable / not configured — distinct from
		// "reachable but no Flux".
		return ports.Graph{}, fmt.Errorf("mock: cluster unreachable")
	}
	if !c.scenario.Cluster.FluxInstalled {
		return ports.Graph{}, ports.ErrFluxNotInstalled
	}
	nodes := make([]ports.GraphNode, 0, len(c.scenario.Cluster.Kustomizations))
	for _, k := range c.scenario.Cluster.Kustomizations {
		ns := k.Namespace
		if ns == "" {
			ns = "flux-system"
		}
		nodes = append(nodes, ports.GraphNode{
			Name:        k.Name,
			Namespace:   ns,
			DependsOn:   k.DependsOn,
			Path:        k.Path,
			Ready:       k.Ready,
			Reconciling: k.Reconciling,
			Reason:      k.Reason,
			Message:     k.Message,
		})
	}
	return ports.Graph{Nodes: nodes}, nil
}

func (c *K8sClient) NodeLabels(ctx context.Context) (map[string]map[string]string, error) {
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	if c.scenario == nil || c.scenario.Cluster == nil {
		return map[string]map[string]string{}, nil
	}
	out := make(map[string]map[string]string, len(c.scenario.Cluster.NodeLabels))
	for node, labels := range c.scenario.Cluster.NodeLabels {
		cp := make(map[string]string, len(labels))
		for k, v := range labels {
			cp[k] = v
		}
		out[node] = cp
	}
	return out, nil
}

func (c *K8sClient) DeploymentImages(ctx context.Context, ns string) (map[string]string, error) {
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	if c.scenario == nil || c.scenario.Cluster == nil || c.scenario.Cluster.DeploymentImages == nil {
		return map[string]string{}, nil
	}
	imgs, ok := c.scenario.Cluster.DeploymentImages[ns]
	if !ok {
		return map[string]string{}, nil
	}
	cp := make(map[string]string, len(imgs))
	for k, v := range imgs {
		cp[k] = v
	}
	return cp, nil
}

func (c *K8sClient) ListNamespaces(ctx context.Context) ([]string, error) {
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	if c.scenario == nil || c.scenario.Cluster == nil || c.scenario.Cluster.DeploymentImages == nil {
		return []string{}, nil
	}
	out := make([]string, 0, len(c.scenario.Cluster.DeploymentImages))
	for ns := range c.scenario.Cluster.DeploymentImages {
		out = append(out, ns)
	}
	return out, nil
}

func (c *K8sClient) ListPodNames(ctx context.Context, ns, _ string) ([]string, error) {
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	if ns != "openbao" || c.scenario == nil || c.scenario.OpenBao == nil {
		return []string{}, nil
	}
	out := make([]string, 0, len(c.scenario.OpenBao.Pods))
	for _, pod := range c.scenario.OpenBao.Pods {
		out = append(out, pod.Name)
	}
	sort.Strings(out)
	return out, nil
}

// ListCRDs returns the scenario's modelled CRDs (cluster.crds). Most
// scenarios model none (nil) — the greenfield default; scenarios that
// exercise the `bootstrap adopt` / init --mode=adopt detection path set
// `cluster.crds`. Adopt *engine* tests still use a purpose-built fake;
// this backs the cmd-level init/adopt paths.
func (c *K8sClient) ListCRDs(ctx context.Context) ([]string, error) {
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	if c.scenario == nil || c.scenario.Cluster == nil {
		return nil, nil
	}
	return append([]string(nil), c.scenario.Cluster.CRDs...), nil
}

// HelmReleaseChartVersions is a stub — same rationale as ListCRDs; adopt
// tests use a purpose-built fake.
func (c *K8sClient) HelmReleaseChartVersions(ctx context.Context) (map[string]string, error) {
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	return nil, nil
}

// GetResourceFieldFirst is a stub — adopt tests use a purpose-built fake.
func (c *K8sClient) GetResourceFieldFirst(ctx context.Context, _, _, _, _, _ string, _ ...string) (string, error) {
	if err := ctx.Err(); err != nil {
		return "", err
	}
	return "", nil
}

// PodExec is a stub — returns empty bytes + nil error. Specific
// commands the mocked OpenBaoClient or other consumers care about will
// be intercepted at the OpenBaoClient layer (which has typed knowledge
// of `bao status` etc.) rather than via raw PodExec parsing.
//
// The `stdin` argument is accepted to match the port shape (so any
// future scenario fixture can react to it), but the current mock
// discards it.
func (c *K8sClient) PodExec(ctx context.Context, ns, pod string, cmd []string, stdin []byte) ([]byte, error) {
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	return nil, nil
}

// PodExecViaKubectl mirrors PodExec — the mock's exec surface is
// the same shape regardless of which transport the real adapter
// would use. Scenarios that want to model "WS drops but kubectl
// succeeds" can override this in a future iteration; today both
// stubs return (nil, nil).
func (c *K8sClient) PodExecViaKubectl(ctx context.Context, ns, pod string, cmd []string, stdin []byte) ([]byte, error) {
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	return nil, nil
}

// GetServiceAnnotation returns the scenario-configured annotation
// value for the named Service, or "" when unset. Cluster-unreachable
// scenarios still return a clean empty string — the mock treats the
// markers as best-effort state, not a fatal probe target.
func (c *K8sClient) GetServiceAnnotation(ctx context.Context, ns, svc, key string) (string, error) {
	if err := ctx.Err(); err != nil {
		return "", err
	}
	if c.scenario == nil || c.scenario.OpenBao == nil {
		return "", nil
	}
	// The fixture only models OpenBao's two markers today; broaden
	// when other consumers land.
	if ns != "openbao" || svc != "openbao" {
		return "", nil
	}
	switch key {
	case "kube-dc.com/openbao-bootstrap-finalized":
		return c.scenario.OpenBao.BootstrapFinalizedAnnotation, nil
	case "kube-dc.com/openbao-controller-auth-installed":
		return c.scenario.OpenBao.ControllerAuthAnnotation, nil
	}
	return "", nil
}

// SetServiceAnnotation mutates the fixture in-place. Mock tests that
// drive M5's annotation-write flow assert against the resulting state
// via GetServiceAnnotation on the same K8sClient instance.
func (c *K8sClient) SetServiceAnnotation(ctx context.Context, ns, svc, key, value string) error {
	if err := ctx.Err(); err != nil {
		return err
	}
	if c.scenario == nil || c.scenario.OpenBao == nil {
		return fmt.Errorf("mock: no openbao fixture to annotate")
	}
	if ns != "openbao" || svc != "openbao" {
		return fmt.Errorf("mock: SetServiceAnnotation only models openbao/openbao today")
	}
	switch key {
	case "kube-dc.com/openbao-bootstrap-finalized":
		c.scenario.OpenBao.BootstrapFinalizedAnnotation = value
	case "kube-dc.com/openbao-controller-auth-installed":
		c.scenario.OpenBao.ControllerAuthAnnotation = value
	default:
		return fmt.Errorf("mock: unknown annotation key %q (extend fixture or broaden mock)", key)
	}
	return nil
}

// SetServiceAnnotations applies a batch via repeated single-key calls.
// The mock fixture only tracks two known keys; unknown keys still fail
// loudly as in SetServiceAnnotation.
func (c *K8sClient) SetServiceAnnotations(ctx context.Context, ns, svc string, kv map[string]string) error {
	for k, v := range kv {
		if err := c.SetServiceAnnotation(ctx, ns, svc, k, v); err != nil {
			return err
		}
	}
	return nil
}

// GetConfigMapData returns a fixture-backed ConfigMap value. The mock
// fixtures don't model ConfigMaps directly today — for the M5-T08
// setup-controller-auth engine path we just need
// `kube-root-ca.crt`/`ca.crt`, so we return a canned PEM placeholder
// when the request matches and an error otherwise. Real-cluster tests
// route through the real adapter.
func (c *K8sClient) GetConfigMapData(ctx context.Context, ns, name, key string) (string, error) {
	if err := ctx.Err(); err != nil {
		return "", err
	}
	if ns == "kube-dc" && name == "kube-root-ca.crt" && key == "ca.crt" {
		return "-----BEGIN CERTIFICATE-----\nMOCK\n-----END CERTIFICATE-----\n", nil
	}
	return "", fmt.Errorf("mock: configmap %s/%s key=%s not modelled (extend fixture if needed)", ns, name, key)
}
