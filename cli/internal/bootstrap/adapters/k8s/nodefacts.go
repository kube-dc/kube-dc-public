package k8s

import (
	"context"
	"fmt"

	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/client-go/kubernetes"
)

// NodeNetFact is the minimal per-node view the init panel's
// probe-driven prefill needs: identity, the address KUBE_OVN_MASTER_NODES
// wants (InternalIP), and whether the node is control-plane.
//
// Deliberately a package-level helper, NOT a ports.K8sClient method: the
// prefill is a best-effort UX nicety used once, before a Session exists —
// widening the port would ripple through every mock/fake for something
// no engine logic depends on. The pure consumer
// (initform.ProbePrefill.ApplyTo) is tested hermetically on its own type.
type NodeNetFact struct {
	Name         string
	InternalIP   string
	ControlPlane bool
}

// GatherNodeNetworkFacts lists the cluster's nodes and extracts the
// prefill facts. An empty kubeconfigPath uses the standard resolution
// (KUBECONFIG env → ~/.kube/config → in-cluster). Callers treat any
// error as "no prefill" — the panel must keep working offline.
func GatherNodeNetworkFacts(ctx context.Context, kubeconfigPath string) ([]NodeNetFact, error) {
	cfg, err := loadRESTConfig(kubeconfigPath)
	if err != nil {
		return nil, err
	}
	core, err := kubernetes.NewForConfig(cfg)
	if err != nil {
		return nil, fmt.Errorf("k8s: build core client: %w", err)
	}
	list, err := core.CoreV1().Nodes().List(ctx, metav1.ListOptions{})
	if err != nil {
		return nil, fmt.Errorf("k8s: list nodes: %w", err)
	}
	out := make([]NodeNetFact, 0, len(list.Items))
	for _, n := range list.Items {
		f := NodeNetFact{Name: n.Name}
		for _, a := range n.Status.Addresses {
			if a.Type == corev1.NodeInternalIP {
				f.InternalIP = a.Address
				break
			}
		}
		// Both kubeadm ("") and RKE2 ("true") spellings — presence is
		// the signal, same convention the chart's platform-endpoint
		// nodeSelector uses.
		if _, ok := n.Labels["node-role.kubernetes.io/control-plane"]; ok {
			f.ControlPlane = true
		}
		out = append(out, f)
	}
	return out, nil
}
