// Package k8s is the real ports.K8sClient adapter. It wraps client-go +
// the dynamic client against the operator's current kubeconfig.
//
// **Why a dynamic client for Flux**: Kustomization / HelmRelease CRDs
// live outside the apiextensions-controlled core schema; vendoring the
// fluxcd typed clients would couple the CLI's go.mod to flux's release
// cadence (and pull in its dependency tree). The dynamic client gives
// us untyped access via GVK without that coupling — we read the
// fields we care about as `unstructured.Unstructured` and translate to
// the typed `ports.GraphNode` shape.
//
// **No transitive escape (agent rule 4)**: every public method
// returns `ports.*` types only. Callers in the engine package never
// see client-go types. If a caller would benefit from a new method,
// add it to the port interface first.
package k8s

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"os"
	"os/exec"
	"sort"
	"strconv"
	"strings"
	"sync"

	corev1 "k8s.io/api/core/v1"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
	"k8s.io/apimachinery/pkg/runtime/schema"
	"k8s.io/apimachinery/pkg/types"
	"k8s.io/client-go/dynamic"
	"k8s.io/client-go/kubernetes"
	"k8s.io/client-go/kubernetes/scheme"
	"k8s.io/client-go/rest"
	"k8s.io/client-go/tools/clientcmd"
	"k8s.io/client-go/tools/remotecommand"
	utilexec "k8s.io/client-go/util/exec"

	"github.com/shalb/kube-dc/cli/internal/bootstrap/helm"
	"github.com/shalb/kube-dc/cli/internal/bootstrap/ports"
)

// fluxSystemNamespace is the conventional namespace Flux installs into
// and the only namespace M1/M2 care about for the dependsOn graph.
const fluxSystemNamespace = "flux-system"

// kustomizationGVR + helmReleaseGVR are the v1 + v2 GVRs Flux ships
// today. If Flux ever bumps these (e.g. v2 Kustomization), the
// constants here are the only place to change.
var (
	kustomizationGVR = schema.GroupVersionResource{
		Group:    "kustomize.toolkit.fluxcd.io",
		Version:  "v1",
		Resource: "kustomizations",
	}
	// crdGVR is cluster-scoped (no .Namespace()) — used by ListCRDs for
	// `bootstrap adopt` component detection.
	crdGVR = schema.GroupVersionResource{
		Group:    "apiextensions.k8s.io",
		Version:  "v1",
		Resource: "customresourcedefinitions",
	}
)

// Client implements ports.K8sClient.
type Client struct {
	rest *rest.Config
	core kubernetes.Interface
	dyn  dynamic.Interface

	// kubeconfigPath is the operator-supplied path (empty = standard
	// resolution: $KUBECONFIG → ~/.kube/config → in-cluster). Saved
	// here so the kubectl fallback streamer can pass it through to
	// the kubectl child process via $KUBECONFIG.
	kubeconfigPath string

	// execStreamer is the post-build factory for kubectl-exec-style
	// WebSocket exec streams. Indirected so tests can swap in a stub
	// that returns canned output without spinning up a real apiserver.
	// stdin is the optional secret-carrying payload — see the
	// PodExec port doc for the "stdin not argv" contract.
	execStreamer func(ctx context.Context, ns, pod string, cmd []string, stdin []byte) ([]byte, error)

	// kubectlStreamer is the F-bootstrap-3 fallback: shell out to the
	// local `kubectl` binary instead of the in-process WebSocket
	// streamer. Same signature as execStreamer — interchangeable
	// from the caller's perspective. nil means "kubectl not on
	// PATH"; the openbao adapter treats that as fallback-unavailable
	// and surfaces the original WS-drop error. Indirected so tests
	// can stub it out.
	kubectlStreamer func(ctx context.Context, ns, pod string, cmd []string, stdin []byte) ([]byte, error)

	// kubectlOnce + kubectlPath cache the result of exec.LookPath so
	// we don't fork-exec it for every call when kubectl is absent.
	kubectlOnce sync.Once
	kubectlPath string
}

// New constructs a Client wired against the operator's kubeconfig. An
// empty kubeconfigPath uses the standard resolution (KUBECONFIG env →
// ~/.kube/config → in-cluster).
func New(kubeconfigPath string) (*Client, error) {
	cfg, err := loadRESTConfig(kubeconfigPath)
	if err != nil {
		return nil, err
	}
	core, err := kubernetes.NewForConfig(cfg)
	if err != nil {
		return nil, fmt.Errorf("k8s: build core client: %w", err)
	}
	dyn, err := dynamic.NewForConfig(cfg)
	if err != nil {
		return nil, fmt.Errorf("k8s: build dynamic client: %w", err)
	}
	c := &Client{rest: cfg, core: core, dyn: dyn, kubeconfigPath: kubeconfigPath}
	c.execStreamer = c.realExecStreamer
	c.kubectlStreamer = c.realKubectlStreamer
	return c, nil
}

// loadRESTConfig honours the standard kubeconfig precedence
// (--kubeconfig flag > KUBECONFIG env > ~/.kube/config > in-cluster).
// Returns a typed error rather than client-go's loadingrules surface
// so the bootstrap engine doesn't have to import them.
func loadRESTConfig(path string) (*rest.Config, error) {
	rules := clientcmd.NewDefaultClientConfigLoadingRules()
	if path != "" {
		rules.ExplicitPath = path
	}
	cfg, err := clientcmd.NewNonInteractiveDeferredLoadingClientConfig(
		rules, &clientcmd.ConfigOverrides{},
	).ClientConfig()
	if err != nil {
		return nil, fmt.Errorf("k8s: load kubeconfig: %w", err)
	}
	return cfg, nil
}

// ---------- ports.K8sClient ----------

func (c *Client) DiscoverFluxGraph(ctx context.Context) (ports.Graph, error) {
	// Namespace-existence probe via a typed Get — gives us the clean
	// ErrFluxNotInstalled signal without depending on the dynamic
	// client's behaviour on a missing CRD.
	if _, err := c.core.CoreV1().Namespaces().Get(ctx, fluxSystemNamespace, metav1.GetOptions{}); err != nil {
		if apierrors.IsNotFound(err) {
			return ports.Graph{}, ports.ErrFluxNotInstalled
		}
		return ports.Graph{}, fmt.Errorf("k8s: probe flux-system namespace: %w", err)
	}

	list, err := c.dyn.Resource(kustomizationGVR).Namespace(fluxSystemNamespace).List(ctx, metav1.ListOptions{})
	if err != nil {
		if apierrors.IsNotFound(err) {
			// CRD absent — Flux's apiservice isn't registered. Treat
			// as not-installed even though the namespace exists.
			return ports.Graph{}, ports.ErrFluxNotInstalled
		}
		return ports.Graph{}, fmt.Errorf("k8s: list Kustomizations: %w", err)
	}

	nodes := make([]ports.GraphNode, 0, len(list.Items))
	for _, item := range list.Items {
		nodes = append(nodes, kustomizationToNode(&item))
	}
	// Topological-ish sort: roots (no dependsOn) first, then alpha
	// within ranks. Cheap O(n²) two-pass — fine for the dozens of
	// Kustomizations a real cluster has.
	topoSort(nodes)
	return ports.Graph{Nodes: nodes}, nil
}

// ListCRDs returns every installed CustomResourceDefinition name. CRDs
// are cluster-scoped, so no .Namespace() — the dynamic list needs only
// list perms on apiextensions.k8s.io. Empty (nil) when the cluster has
// no CRDs; errors otherwise.
func (c *Client) ListCRDs(ctx context.Context) ([]string, error) {
	list, err := c.dyn.Resource(crdGVR).List(ctx, metav1.ListOptions{})
	if err != nil {
		return nil, fmt.Errorf("k8s: list CRDs: %w", err)
	}
	out := make([]string, 0, len(list.Items))
	for i := range list.Items {
		out = append(out, list.Items[i].GetName())
	}
	return out, nil
}

// HelmReleaseChartVersions lists every helm.sh/release.v1 Secret
// (owner=helm), keeps the highest revision per release, and decodes each
// release payload to its chart version. Keyed by "<namespace>/<release>".
// A Secret that fails to decode is skipped (best-effort — one malformed
// release must not blank the whole map).
func (c *Client) HelmReleaseChartVersions(ctx context.Context) (map[string]string, error) {
	secrets, err := c.core.CoreV1().Secrets(metav1.NamespaceAll).List(ctx, metav1.ListOptions{
		LabelSelector: "owner=helm",
	})
	if err != nil {
		return nil, fmt.Errorf("k8s: list helm release secrets: %w", err)
	}
	// Collect all release Secrets; LatestChartVersions picks the highest
	// revision per release FIRST, then decodes only that one — so a
	// corrupt latest revision surfaces as undetected instead of pinning a
	// stale older revision (downgrade risk on adoption).
	rels := make([]helm.ReleaseSecret, 0, len(secrets.Items))
	for i := range secrets.Items {
		s := &secrets.Items[i]
		blob, ok := s.Data["release"]
		if !ok {
			continue
		}
		rev, _ := strconv.Atoi(s.Labels["version"])
		rels = append(rels, helm.ReleaseSecret{
			Namespace: s.Namespace,
			Name:      s.Labels["name"],
			Revision:  rev,
			Data:      blob,
		})
	}
	return helm.LatestChartVersions(rels), nil
}

// GetResourceFieldFirst gets one custom resource (namespace "" →
// cluster-scoped) and returns the first non-empty string among the
// candidate dot-path fields. An absent resource is ("", nil) — the
// "not present" signal — so adopt can probe optional operator CRs.
func (c *Client) GetResourceFieldFirst(ctx context.Context, group, version, resource, namespace, name string, fields ...string) (string, error) {
	gvr := schema.GroupVersionResource{Group: group, Version: version, Resource: resource}
	ri := c.dyn.Resource(gvr)
	var obj *unstructured.Unstructured
	var err error
	if namespace == "" {
		obj, err = ri.Get(ctx, name, metav1.GetOptions{})
	} else {
		obj, err = ri.Namespace(namespace).Get(ctx, name, metav1.GetOptions{})
	}
	if err != nil {
		if apierrors.IsNotFound(err) {
			return "", nil
		}
		return "", fmt.Errorf("k8s: get %s/%s %q: %w", group, resource, name, err)
	}
	for _, f := range fields {
		if v, ok, _ := unstructured.NestedString(obj.Object, strings.Split(f, ".")...); ok && v != "" {
			return v, nil
		}
	}
	return "", nil
}

func (c *Client) NodeLabels(ctx context.Context) (map[string]map[string]string, error) {
	list, err := c.core.CoreV1().Nodes().List(ctx, metav1.ListOptions{})
	if err != nil {
		return nil, fmt.Errorf("k8s: list nodes: %w", err)
	}
	out := make(map[string]map[string]string, len(list.Items))
	for _, n := range list.Items {
		labels := make(map[string]string, len(n.Labels))
		for k, v := range n.Labels {
			labels[k] = v
		}
		out[n.Name] = labels
	}
	return out, nil
}

// GPUNodeRuntimes binds exact Node identity to the active device-plugin owners
// scheduled there. It uses one Node GET and one field-selected Pod LIST per
// requested node; unrelated nodes and Pods are never listed or returned.
func (c *Client) GPUNodeRuntimes(ctx context.Context, nodes []string) (map[string]ports.GPUNodeRuntime, error) {
	out := make(map[string]ports.GPUNodeRuntime, len(nodes))
	for _, name := range nodes {
		node, err := c.core.CoreV1().Nodes().Get(ctx, name, metav1.GetOptions{})
		if err != nil {
			return nil, fmt.Errorf("k8s: get GPU node %s: %w", name, err)
		}
		labels := make(map[string]string, len(node.Labels))
		for key, value := range node.Labels {
			labels[key] = value
		}
		pods, err := c.core.CoreV1().Pods(metav1.NamespaceAll).List(ctx, metav1.ListOptions{
			FieldSelector: "spec.nodeName=" + name,
		})
		if err != nil {
			return nil, fmt.Errorf("k8s: list Pods on GPU node %s: %w", name, err)
		}
		owners := make([]ports.GPUPluginOwner, 0)
		for i := range pods.Items {
			pod := &pods.Items[i]
			if pod.DeletionTimestamp != nil || pod.Status.Phase == corev1.PodSucceeded || pod.Status.Phase == corev1.PodFailed {
				continue
			}
			for _, owner := range pod.OwnerReferences {
				if owner.Controller != nil && *owner.Controller {
					owners = append(owners, ports.GPUPluginOwner{Namespace: pod.Namespace, Kind: owner.Kind, Name: owner.Name})
					break
				}
			}
		}
		sort.Slice(owners, func(i, j int) bool {
			left := owners[i].Namespace + "/" + owners[i].Kind + "/" + owners[i].Name
			right := owners[j].Namespace + "/" + owners[j].Kind + "/" + owners[j].Name
			return left < right
		})
		out[name] = ports.GPUNodeRuntime{
			Name: name, SystemUUID: node.Status.NodeInfo.SystemUUID,
			Labels: labels, PluginOwners: owners,
		}
	}
	return out, nil
}

func (c *Client) GPUNodeTransitionState(ctx context.Context, name string) (ports.GPUNodeTransitionState, error) {
	node, err := c.core.CoreV1().Nodes().Get(ctx, name, metav1.GetOptions{})
	if err != nil {
		return ports.GPUNodeTransitionState{}, fmt.Errorf("k8s: get GPU node %s: %w", name, err)
	}
	pods, err := c.core.CoreV1().Pods(metav1.NamespaceAll).List(ctx, metav1.ListOptions{FieldSelector: "spec.nodeName=" + name})
	if err != nil {
		return ports.GPUNodeTransitionState{}, fmt.Errorf("k8s: list Pods on GPU node %s: %w", name, err)
	}

	labels := make(map[string]string, len(node.Labels))
	for key, value := range node.Labels {
		labels[key] = value
	}
	owners := make([]ports.GPUPluginOwner, 0)
	holdersByKey := map[string]ports.GPUHolder{}
	for i := range pods.Items {
		pod := &pods.Items[i]
		if pod.DeletionTimestamp != nil || pod.Status.Phase == corev1.PodSucceeded || pod.Status.Phase == corev1.PodFailed {
			continue
		}
		controllerKind, controllerName := "Pod", pod.Name
		for _, owner := range pod.OwnerReferences {
			if owner.Controller != nil && *owner.Controller {
				owners = append(owners, ports.GPUPluginOwner{Namespace: pod.Namespace, Kind: owner.Kind, Name: owner.Name})
				controllerKind, controllerName = owner.Kind, owner.Name
				break
			}
		}
		resources := podGPUResources(pod)
		if len(resources) > 0 {
			key := pod.Namespace + "/" + controllerKind + "/" + controllerName
			holder := holdersByKey[key]
			holder.Namespace, holder.Kind, holder.Name = pod.Namespace, controllerKind, controllerName
			holder.Resources = mergeStrings(holder.Resources, resources)
			holdersByKey[key] = holder
		}
	}
	sort.Slice(owners, func(i, j int) bool {
		return owners[i].Namespace+"/"+owners[i].Kind+"/"+owners[i].Name < owners[j].Namespace+"/"+owners[j].Kind+"/"+owners[j].Name
	})
	holders := make([]ports.GPUHolder, 0, len(holdersByKey))
	for _, holder := range holdersByKey {
		holders = append(holders, holder)
	}
	sort.Slice(holders, func(i, j int) bool {
		return holders[i].Namespace+"/"+holders[i].Kind+"/"+holders[i].Name < holders[j].Namespace+"/"+holders[j].Kind+"/"+holders[j].Name
	})
	ready := false
	for _, condition := range node.Status.Conditions {
		if condition.Type == corev1.NodeReady {
			ready = condition.Status == corev1.ConditionTrue
			break
		}
	}
	return ports.GPUNodeTransitionState{
		Runtime: ports.GPUNodeRuntime{Name: node.Name, SystemUUID: node.Status.NodeInfo.SystemUUID, Labels: labels, PluginOwners: owners},
		Holders: holders, Ready: ready, Unschedulable: node.Spec.Unschedulable,
	}, nil
}

func (c *Client) SetGPUNodeSchedulable(ctx context.Context, name string, schedulable bool) error {
	patch := []byte(`{"spec":{"unschedulable":` + strconv.FormatBool(!schedulable) + `}}`)
	if _, err := c.core.CoreV1().Nodes().Patch(ctx, name, types.MergePatchType, patch, metav1.PatchOptions{}); err != nil {
		return fmt.Errorf("k8s: set GPU node %s schedulable=%t: %w", name, schedulable, err)
	}
	return nil
}

func podGPUResources(pod *corev1.Pod) []string {
	seen := map[string]bool{}
	if len(pod.Spec.ResourceClaims) > 0 {
		// During a GPU-node ownership transition every DRA holder is unsafe,
		// even if its DeviceClass belongs to a future non-GPU driver. Blocking
		// is preferable to moving a node while kubelet has prepared devices.
		seen["resource.k8s.io/claim"] = true
	}
	add := func(resources corev1.ResourceList) {
		for name, quantity := range resources {
			key := string(name)
			if quantity.Sign() > 0 && isGPUResourceName(key) {
				seen[key] = true
			}
		}
	}
	for i := range pod.Spec.InitContainers {
		add(pod.Spec.InitContainers[i].Resources.Requests)
		add(pod.Spec.InitContainers[i].Resources.Limits)
	}
	for i := range pod.Spec.Containers {
		add(pod.Spec.Containers[i].Resources.Requests)
		add(pod.Spec.Containers[i].Resources.Limits)
	}
	for i := range pod.Spec.EphemeralContainers {
		add(pod.Spec.EphemeralContainers[i].Resources.Requests)
		add(pod.Spec.EphemeralContainers[i].Resources.Limits)
	}
	add(pod.Spec.Overhead)
	out := make([]string, 0, len(seen))
	for name := range seen {
		out = append(out, name)
	}
	sort.Strings(out)
	return out
}

func isGPUResourceName(name string) bool {
	return strings.HasPrefix(name, "nvidia.com/") || strings.HasPrefix(name, "requests.nvidia.com/") ||
		strings.HasPrefix(name, "amd.com/gpu") || strings.HasPrefix(name, "gpu.intel.com/")
}

func mergeStrings(left, right []string) []string {
	seen := map[string]bool{}
	for _, value := range append(left, right...) {
		seen[value] = true
	}
	out := make([]string, 0, len(seen))
	for value := range seen {
		out = append(out, value)
	}
	sort.Strings(out)
	return out
}

func (c *Client) DeploymentImages(ctx context.Context, ns string) (map[string]string, error) {
	list, err := c.core.AppsV1().Deployments(ns).List(ctx, metav1.ListOptions{})
	if err != nil {
		return nil, fmt.Errorf("k8s: list deployments in %s: %w", ns, err)
	}
	out := make(map[string]string, len(list.Items))
	for _, d := range list.Items {
		if len(d.Spec.Template.Spec.Containers) == 0 {
			continue
		}
		out[d.Name] = d.Spec.Template.Spec.Containers[0].Image
	}
	return out, nil
}

func (c *Client) ListNamespaces(ctx context.Context) ([]string, error) {
	list, err := c.core.CoreV1().Namespaces().List(ctx, metav1.ListOptions{})
	if err != nil {
		return nil, fmt.Errorf("k8s: list namespaces: %w", err)
	}
	out := make([]string, 0, len(list.Items))
	for _, n := range list.Items {
		out = append(out, n.Name)
	}
	sort.Strings(out)
	return out, nil
}

func (c *Client) ListPodNames(ctx context.Context, ns, labelSelector string) ([]string, error) {
	list, err := c.core.CoreV1().Pods(ns).List(ctx, metav1.ListOptions{LabelSelector: labelSelector})
	if err != nil {
		return nil, fmt.Errorf("k8s: list pods in %s with selector %q: %w", ns, labelSelector, err)
	}
	out := make([]string, 0, len(list.Items))
	for _, pod := range list.Items {
		out = append(out, pod.Name)
	}
	sort.Strings(out)
	return out, nil
}

func (c *Client) PodExec(ctx context.Context, ns, pod string, cmd []string, stdin []byte) ([]byte, error) {
	if c.execStreamer == nil {
		return nil, fmt.Errorf("k8s: execStreamer not configured (internal bug)")
	}
	return c.execStreamer(ctx, ns, pod, cmd, stdin)
}

// PodExecViaKubectl is the F-bootstrap-3 fallback path — see the
// port doc for the contract. Resolves `kubectl` on $PATH lazily +
// caches the result so we only fork-exec the lookup once per session.
func (c *Client) PodExecViaKubectl(ctx context.Context, ns, pod string, cmd []string, stdin []byte) ([]byte, error) {
	if c.kubectlStreamer == nil {
		return nil, fmt.Errorf("k8s: kubectlStreamer not configured (internal bug)")
	}
	return c.kubectlStreamer(ctx, ns, pod, cmd, stdin)
}

// realKubectlStreamer shells out to `kubectl exec` against the same
// kubeconfig the in-process WebSocket exec uses. The crucial
// difference from realExecStreamer: kubectl is a separate
// implementation of the apiserver exec protocol that, in practice,
// is more reliable than client-go's WebSocket exec against the
// eu/dc1 kube-apiserver 2026-06-08-class flake.
//
// Kubeconfig pass-through:
//   - if c.kubeconfigPath is non-empty, we pass it via the
//     KUBECONFIG env var to the child kubectl process. That keeps
//     the operator's explicit --kubeconfig flag wired through.
//   - otherwise we let kubectl do its own standard resolution
//     ($KUBECONFIG → ~/.kube/config → in-cluster).
//
// Stdin discipline (M0-T06 batch-2 review): when stdin is non-empty
// we add `-i` so kubectl wires the process's stdin to the in-pod
// command's stdin. The bytes ride that pipe — never argv.
func (c *Client) realKubectlStreamer(ctx context.Context, ns, pod string, cmd []string, stdin []byte) ([]byte, error) {
	kubectlBin := c.resolveKubectl()
	if kubectlBin == "" {
		return nil, ports.ErrKubectlNotFound
	}

	args := []string{"exec", "-n", ns}
	if len(stdin) > 0 {
		args = append(args, "-i")
	}
	args = append(args, pod, "--")
	args = append(args, cmd...)

	child := exec.CommandContext(ctx, kubectlBin, args...)

	// Pass-through the kubeconfig env so the child binary resolves
	// the same cluster the in-process WS client uses. Copy the
	// parent's env minus any existing KUBECONFIG override (avoids
	// the double-set-but-different scenario where the operator
	// passed --kubeconfig but the env has another).
	env := os.Environ()
	if c.kubeconfigPath != "" {
		filtered := env[:0]
		for _, kv := range env {
			if len(kv) >= len("KUBECONFIG=") && kv[:len("KUBECONFIG=")] == "KUBECONFIG=" {
				continue
			}
			filtered = append(filtered, kv)
		}
		filtered = append(filtered, "KUBECONFIG="+c.kubeconfigPath)
		child.Env = filtered
	} else {
		child.Env = env
	}

	if len(stdin) > 0 {
		child.Stdin = bytes.NewReader(stdin)
	}

	// Combined stdout+stderr matches the WS streamer's contract.
	var combined bytes.Buffer
	child.Stdout = &combined
	child.Stderr = &combined
	err := child.Run()
	out := combined.Bytes()
	if err != nil {
		return out, fmt.Errorf("k8s: kubectl exec %s/%s: %w", ns, pod, err)
	}
	return out, nil
}

// resolveKubectl returns the absolute path to `kubectl` if it's on
// $PATH, or "" if not. Cached via sync.Once so the lookup runs at
// most once per Client lifetime.
func (c *Client) resolveKubectl() string {
	c.kubectlOnce.Do(func() {
		p, err := exec.LookPath("kubectl")
		if err == nil {
			c.kubectlPath = p
		}
	})
	return c.kubectlPath
}

// realExecStreamer builds a WebSocket exec stream (see F-bootstrap-3
// note below) against the apiserver's pod-exec subresource and
// returns combined stdout+stderr. Mirrors what
// `kubectl exec -n NS POD -i -- CMD...` produces when stdin is
// supplied. The first container in the pod is targeted.
//
// **stdin path** (M0-T06 batch-2 review): when callers feed secret
// material into the in-pod process, the bytes ride this stdin
// channel — never an argv slot or an env var. The PodExecOptions
// flag is set only when stdin is non-empty so the apiserver doesn't
// allocate the pipe on no-input calls.
func (c *Client) realExecStreamer(ctx context.Context, ns, pod string, cmd []string, stdin []byte) ([]byte, error) {
	wantStdin := len(stdin) > 0
	req := c.core.CoreV1().RESTClient().
		Post().
		Resource("pods").
		Name(pod).
		Namespace(ns).
		SubResource("exec").
		VersionedParams(&corev1.PodExecOptions{
			Command: cmd,
			Stdin:   wantStdin,
			Stdout:  true,
			Stderr:  true,
		}, scheme.ParameterCodec)

	// F-bootstrap-3 fix: WebSocket-only exec. The legacy SPDY executor
	// (NewSPDYExecutor) has a long-standing stream-demuxer flake that
	// occasionally returns the stdout channel as 0 bytes against
	// busy apiserver targets — observed deterministically against
	// eu/dc1 kube-apiserver 2026-06-08 against the active OpenBao
	// Raft node (under heavier concurrent stream multiplexing than
	// standbys). Symptom: parseStatus reports "no JSON object in
	// bao-status output" against a healthy cluster while `kubectl
	// exec` for the identical command works fine. Confirmed via a
	// back-to-back WS-only vs SPDY-only diagnostic:
	//
	//   WS   [0..2]: 626 bytes each ✓
	//   SPDY [0..2]: 0 bytes, 0 bytes, 626 bytes (flaky)
	//
	// NewWebSocketExecutor (k8s 1.30+, available in client-go v0.30+)
	// uses the modern exec protocol that doesn't share SPDY's
	// stream-framing edge cases. We require k8s 1.30+ on the
	// apiserver side — kube-dc clusters are pinned at 1.35 so this
	// is a non-constraint. If someone deploys against a < 1.30
	// cluster, the WS upgrade fails fast with a clear error rather
	// than the subtle SPDY 0-bytes failure mode.
	//
	// NewFallbackExecutor (WS-first + SPDY-fallback) was considered
	// and rejected — empirically the fallback never triggered on
	// the eu/dc1 failure case (WS succeeded but data path silently
	// returned 0 bytes), but the predicate matched on a transient
	// error and dropped us into SPDY mid-stream where the flake
	// reproduces. WebSocket-only is the simpler + more reliable
	// answer.
	exec, err := remotecommand.NewWebSocketExecutor(c.rest, "GET", req.URL().String())
	if err != nil {
		return nil, fmt.Errorf("k8s: build WebSocket exec executor (requires apiserver k8s 1.30+): %w", err)
	}

	// Use bytes.Buffer (not strings.Builder) so the captured output
	// can be scrubbed if a caller happens to receive secret material
	// via stdout (e.g. `bao token revoke -self` echoing its input).
	var combined bytes.Buffer
	opts := remotecommand.StreamOptions{
		Stdout: &combined,
		Stderr: &combined,
	}
	// Attach the stdin reader only when caller supplied a payload.
	// PodExecOptions.Stdin was set to wantStdin above, so the
	// apiserver doesn't allocate the pipe channel on its side when
	// there's no input — keeps the no-stdin path simple.
	if wantStdin {
		opts.Stdin = bytes.NewReader(stdin)
	}
	err = exec.StreamWithContext(ctx, opts)
	out := combined.Bytes()
	if err != nil {
		var ee utilexec.CodeExitError
		if asExitError(err, &ee) {
			return out, fmt.Errorf("k8s: exec %s/%s exited %d: %w", ns, pod, ee.Code, err)
		}
		return out, fmt.Errorf("k8s: exec %s/%s: %w", ns, pod, err)
	}
	return out, nil
}

// asExitError is a local errors.As that doesn't need a 1-import-for-1-
// use pattern at the call site.
func asExitError(err error, target *utilexec.CodeExitError) bool {
	if err == nil {
		return false
	}
	if ee, ok := err.(utilexec.CodeExitError); ok {
		*target = ee
		return true
	}
	return false
}

// GetServiceAnnotation fetches the named annotation from the named
// Service. Returns ("", nil) when the key is unset; a non-nil error
// only when the apiserver call itself fails (including IsNotFound on
// the Service).
func (c *Client) GetServiceAnnotation(ctx context.Context, ns, svc, key string) (string, error) {
	if svc == "" || key == "" {
		return "", fmt.Errorf("k8s: GetServiceAnnotation needs svc + key")
	}
	s, err := c.core.CoreV1().Services(ns).Get(ctx, svc, metav1.GetOptions{})
	if err != nil {
		return "", fmt.Errorf("k8s: get service %s/%s: %w", ns, svc, err)
	}
	if s.Annotations == nil {
		return "", nil
	}
	return s.Annotations[key], nil
}

// SetServiceAnnotation writes `value` for `key` via a strategic
// merge patch — preserves any other annotations the Service carries
// (Helm metadata, fluxcd labels, etc.). An empty `value` clears the
// annotation (merge-patch with null per JSON-merge semantics).
func (c *Client) SetServiceAnnotation(ctx context.Context, ns, svc, key, value string) error {
	if svc == "" || key == "" {
		return fmt.Errorf("k8s: SetServiceAnnotation needs svc + key")
	}
	return c.SetServiceAnnotations(ctx, ns, svc, map[string]string{key: value})
}

// SetServiceAnnotations applies multiple annotation writes in a single
// merge-patch. Empty values clear the corresponding key per JSON-merge
// spec. No-op when the map is empty.
func (c *Client) SetServiceAnnotations(ctx context.Context, ns, svc string, kv map[string]string) error {
	if svc == "" {
		return fmt.Errorf("k8s: SetServiceAnnotations needs svc")
	}
	if len(kv) == 0 {
		return nil
	}
	annotations := make(map[string]interface{}, len(kv))
	for k, v := range kv {
		if k == "" {
			return fmt.Errorf("k8s: SetServiceAnnotations: empty key in batch")
		}
		if v == "" {
			annotations[k] = nil
		} else {
			annotations[k] = v
		}
	}
	patch := map[string]interface{}{
		"metadata": map[string]interface{}{
			"annotations": annotations,
		},
	}
	body, err := json.Marshal(patch)
	if err != nil {
		return fmt.Errorf("k8s: build patch: %w", err)
	}
	_, err = c.core.CoreV1().Services(ns).Patch(ctx, svc, types.MergePatchType, body, metav1.PatchOptions{})
	if err != nil {
		return fmt.Errorf("k8s: patch service %s/%s: %w", ns, svc, err)
	}
	return nil
}

// GetConfigMapData reads a single key from a ConfigMap. Returns
// ("", nil) when the ConfigMap exists but the key is absent — that's
// the well-formed "key not found" signal callers can branch on
// without parsing the error. Returns ("", error) when the ConfigMap
// itself can't be fetched (network, RBAC, NotFound).
func (c *Client) GetConfigMapData(ctx context.Context, ns, name, key string) (string, error) {
	if name == "" || key == "" {
		return "", fmt.Errorf("k8s: GetConfigMapData needs name + key")
	}
	cm, err := c.core.CoreV1().ConfigMaps(ns).Get(ctx, name, metav1.GetOptions{})
	if err != nil {
		return "", fmt.Errorf("k8s: get configmap %s/%s: %w", ns, name, err)
	}
	v, ok := cm.Data[key]
	if !ok {
		return "", nil
	}
	return v, nil
}

// ---------- helpers ----------

// kustomizationToNode translates a flux Kustomization Unstructured
// into the typed GraphNode shape. Reads only the fields the doctor +
// status panes consume; ignores everything else.
func kustomizationToNode(u *unstructured.Unstructured) ports.GraphNode {
	node := ports.GraphNode{
		Name:      u.GetName(),
		Namespace: u.GetNamespace(),
	}
	// dependsOn
	if deps, found, _ := unstructured.NestedSlice(u.Object, "spec", "dependsOn"); found {
		for _, d := range deps {
			if m, ok := d.(map[string]interface{}); ok {
				if name, ok := m["name"].(string); ok && name != "" {
					node.DependsOn = append(node.DependsOn, name)
				}
			}
		}
	}
	if path, found, _ := unstructured.NestedString(u.Object, "spec", "path"); found {
		node.Path = path
	}
	// status.conditions[type=Ready], type=Reconciling
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
			node.Ready = cstatus == "True"
			if !node.Ready {
				node.Reason = creason
				node.Message = cmessage
			}
		case "Reconciling":
			node.Reconciling = cstatus == "True"
		}
	}
	return node
}

// topoSort orders nodes so that every node appears after all of its
// declared dependsOn parents. Stable alphabetical tie-break within a
// rank for deterministic output.
func topoSort(nodes []ports.GraphNode) {
	byName := make(map[string]int, len(nodes))
	for i, n := range nodes {
		byName[n.Name] = i
	}
	// Compute depths (longest path from any root) — gives us a
	// reliable rank for partial-order sorting.
	depths := make([]int, len(nodes))
	var visit func(i int, seen map[int]bool) int
	visit = func(i int, seen map[int]bool) int {
		if seen[i] {
			return depths[i] // cycle guard
		}
		seen[i] = true
		max := 0
		for _, dep := range nodes[i].DependsOn {
			j, ok := byName[dep]
			if !ok {
				continue
			}
			d := visit(j, seen) + 1
			if d > max {
				max = d
			}
		}
		depths[i] = max
		return max
	}
	for i := range nodes {
		visit(i, map[int]bool{})
	}
	indices := make([]int, len(nodes))
	for i := range nodes {
		indices[i] = i
	}
	sort.SliceStable(indices, func(a, b int) bool {
		if depths[indices[a]] != depths[indices[b]] {
			return depths[indices[a]] < depths[indices[b]]
		}
		return nodes[indices[a]].Name < nodes[indices[b]].Name
	})
	out := make([]ports.GraphNode, len(nodes))
	for i, idx := range indices {
		out[i] = nodes[idx]
	}
	copy(nodes, out)
}
