package ports

import "context"

// K8sClient is the contract for plain-Kubernetes-API reads the bootstrap
// engine performs — node labels, deployment images, namespace existence,
// pod exec, and the Flux Kustomization dependsOn graph.
//
// The real adapter wraps a client-go (or rest.Config + dynamic client)
// session against the operator's current kubeconfig. The mock adapter
// returns fixture data from scenario YAML.
//
// **Important** (agent rule 4): callers of this interface NEVER import
// client-go directly. If you find yourself needing a method that's not on
// K8sClient, add it here first — keep the engine package free of
// apimachinery transitively.
type K8sClient interface {
	// DiscoverFluxGraph enumerates every Kustomization in `flux-system`
	// and returns them as a dependsOn-resolved graph. Used by M1 doctor
	// (cluster probe layer) + M2 status + M7 waterfall.
	//
	// **Empty-vs-missing semantics**:
	//   - When `flux-system` Namespace is absent, returns
	//     (Graph{}, ErrFluxNotInstalled). The Graph.Nodes field is
	//     intentionally a nil slice in this case — the error is the
	//     primary signal.
	//   - When `flux-system` exists but contains zero Kustomizations
	//     (a freshly-bootstrapped Flux that hasn't reconciled anything
	//     yet), returns (Graph{Nodes: []GraphNode{}}, nil). The slice
	//     is empty but non-nil so range/len work naturally.
	// The caller distinguishes "no Flux" from "Flux with zero
	// Kustomizations" via `errors.Is(err, ErrFluxNotInstalled)`, NOT
	// via inspecting Nodes for nil-vs-empty.
	DiscoverFluxGraph(ctx context.Context) (Graph, error)

	// NodeLabels returns labels for every Node, keyed by node name.
	// M1-T04 consumer reads NFD labels via this method — the parser
	// is mode-agnostic, just gives us the full label map.
	NodeLabels(ctx context.Context) (map[string]map[string]string, error)

	// DeploymentImages returns Deployment-name → container-image-tag for
	// every Deployment in `ns`. Used by `discover.ClusterProbe`-style
	// image-drift detection (T2a shipped pattern) and M2 status.
	//
	// For multi-container Deployments, returns the first container's
	// image (matches the convention in the shipped drift detector).
	DeploymentImages(ctx context.Context, ns string) (map[string]string, error)

	// ListNamespaces returns every namespace name. Used by adopt-mode
	// (V2) to detect foreign components (cert-manager, monitoring, etc.)
	// before generating compatibility-decisions form.
	ListNamespaces(ctx context.Context) ([]string, error)

	// ListPodNames returns sorted Pod names in ns matching the Kubernetes
	// label selector. OpenBao day-two operations use this instead of probing
	// guessed StatefulSet ordinals through PodExec: exec transport failures
	// and absent pods must never be mistaken for healthy/unsealed replicas.
	// An empty, successful list is returned as [] rather than nil.
	ListPodNames(ctx context.Context, ns, labelSelector string) ([]string, error)

	// PodExec runs a command inside the named pod and returns combined
	// stdout+stderr. Used by OpenBao adapter (`bao status` per pod, ad-
	// hoc `bao operator unseal`, etc.) — the OpenBao port methods that
	// need pod-side execution delegate here rather than re-implementing
	// kubectl exec.
	//
	// **Secret material on stdin, NOT argv** (M0-T06 batch-2 review):
	// when callers have secret material to feed the in-pod process
	// (OpenBao shares, root tokens), they MUST use the `stdin` arg
	// rather than embedding the secret in `cmd`. argv shows up in the
	// exec request body, audit logs, and the in-pod process listing —
	// stdin is the only path that keeps the bytes out of those
	// surfaces. The adapter writes `stdin` to the pod process's
	// standard input then half-closes the write side.
	//
	// Pass `nil` (or empty slice) for `stdin` when the command takes
	// no input. The caller is responsible for scrubbing `stdin` after
	// the call returns.
	//
	// Timeout enforcement is the caller's responsibility via ctx.
	PodExec(ctx context.Context, ns, pod string, cmd []string, stdin []byte) ([]byte, error)

	// PodExecViaKubectl is the F-bootstrap-3 fallback: shell out to
	// the local `kubectl exec -i -n <ns> <pod> -- <cmd...>` binary
	// instead of the in-process WebSocket streamer. Same return
	// shape as PodExec.
	//
	// **Why a separate method**: client-go's WebSocket exec drops
	// ~30% of stdin/stdout calls against some apiservers (eu/dc1
	// 2026-06-08), while the kubectl binary's stream handling
	// works reliably on the same cluster — different protocol
	// implementation despite hitting the same exec subresource.
	// Callers (the OpenBao adapter's idempotent-write retry helper)
	// fall back to this method ONLY after the WS path exhausts its
	// retry budget with the empty-output WS-drop signature; real
	// bao errors (non-empty stderr) surface immediately.
	//
	// Returns (nil, ErrKubectlNotFound) when kubectl is not on
	// $PATH so the caller can keep the original WS-drop error
	// rather than substitute a confusing "kubectl not found".
	//
	// Stdin discipline is identical to PodExec — secret material
	// rides stdin, never argv.
	PodExecViaKubectl(ctx context.Context, ns, pod string, cmd []string, stdin []byte) ([]byte, error)

	// GetServiceAnnotation returns the value of the named annotation
	// on the named Service. The empty string + nil error is a
	// well-formed "annotation absent" signal — callers distinguish
	// "service missing" from "key missing" by the non-nil error
	// shape.
	//
	// Used by the OpenBao adapter for the two operational markers
	// (kube-dc.com/openbao-bootstrap-finalized,
	// kube-dc.com/openbao-controller-auth-installed) and any future
	// per-service marker. The adapter delegates here rather than
	// holding its own client-go dependency.
	GetServiceAnnotation(ctx context.Context, ns, svc, key string) (string, error)

	// SetServiceAnnotation writes (or replaces) `value` for `key` on
	// the named Service. Uses a merge-patch so unrelated annotations
	// stay intact. An empty `value` clears the annotation.
	SetServiceAnnotation(ctx context.Context, ns, svc, key, value string) error

	// SetServiceAnnotations applies a batch of annotation writes in a
	// single merge-patch — atomic from the apiserver's perspective.
	// Used by init Phase C to stamp both bootstrap markers
	// (finalized + controller-auth-installed) without an intermediate
	// one-key-set observable window. An empty value clears the
	// corresponding key (JSON-merge null semantics).
	SetServiceAnnotations(ctx context.Context, ns, svc string, kv map[string]string) error

	// GetConfigMapData reads a single key from a ConfigMap. Used by
	// the controller-auth setup engine to fetch the in-cluster
	// kube-apiserver CA bundle (`kube-root-ca.crt` ConfigMap, key
	// `ca.crt`) for the `bao write auth/k8s-host/config` step.
	// Returns ("", error) on missing ConfigMap; returns ("", nil) for
	// missing key (well-formed absent signal).
	GetConfigMapData(ctx context.Context, ns, name, key string) (string, error)

	// ListCRDs returns every installed CustomResourceDefinition name
	// (e.g. "certificates.cert-manager.io"). Used by `bootstrap adopt`
	// (V2) to detect components already present on an existing cluster —
	// a CRD is the most reliable presence signal — before kube-dc's Flux
	// would install its own. Cluster-scoped list.
	ListCRDs(ctx context.Context) ([]string, error)

	// HelmReleaseChartVersions returns the LIVE chart version of every
	// Helm 3 release on the cluster, keyed by "<namespace>/<release>"
	// (latest revision only). Used by `bootstrap adopt --pin-versions` to
	// pin cluster-config.env to the running version so Flux adopts a
	// component in place without an upgrade/restart. Reads the
	// `helm.sh/release.v1` Secrets and decodes each release payload.
	HelmReleaseChartVersions(ctx context.Context) (map[string]string, error)

	// GetResourceFieldFirst reads a custom resource (group/version/
	// resource; namespace "" for cluster-scoped) named `name` and returns
	// the first non-empty string among the candidate dot-path fields
	// (e.g. "status.observedKubeVirtVersion"). Returns ("", nil) when the
	// resource is ABSENT — a well-formed "not present" signal — so
	// `bootstrap adopt` can read the version of non-Helm components
	// (KubeVirt, CDI) from their operator CRs.
	GetResourceFieldFirst(ctx context.Context, group, version, resource, namespace, name string, fields ...string) (string, error)
}

// Graph is a dependsOn-resolved view of `flux-system`. Nodes are sorted
// in topological order (roots first) so a caller can iterate and know
// every dependency was seen earlier in the slice.
type Graph struct {
	Nodes []GraphNode
}

// GraphNode is one Kustomization in the dependsOn graph. The Status
// fields mirror KustomizationEvent shape so a caller can pass either to
// the same renderer.
type GraphNode struct {
	Name        string
	Namespace   string
	DependsOn   []string
	Path        string // .spec.path — useful for surfacing "what does this Kustomization manage?"
	Ready       bool
	Reconciling bool
	Reason      string
	Message     string
}

// NIC is one network interface on a node. Used by the M1-T02 host probe
// (read from /sys/class/net/) and by the M4-T11 customInterfaces patch
// generator (matched against operator-declared `--node-nic` overrides).
//
// Doesn't carry MAC or driver per the wildcard-first DNS / declare-
// don't-introspect philosophy (B-003) — the operator types the name in
// `cluster-config.env`; we cross-check that the name exists in the host
// probe and on the node via kubelet's status.addresses[].
type NIC struct {
	Name  string // Linux interface name (enp94s0f0np0, eno1, …)
	State string // "up" | "down" | "unknown"
	MTU   int    // 0 when unknown
}
