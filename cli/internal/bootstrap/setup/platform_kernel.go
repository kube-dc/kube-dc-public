package setup

import (
	"context"
	"fmt"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/client-go/kubernetes"
)

type platformKernelInventory struct{ client kubernetes.Interface }

func (k platformKernelInventory) NodeKernelVersions(ctx context.Context) (map[string]string, error) {
	if k.client == nil {
		return nil, fmt.Errorf("verified child API unavailable")
	}
	nodes, err := k.client.CoreV1().Nodes().List(ctx, metav1.ListOptions{})
	if err != nil {
		return nil, err
	}
	versions := map[string]string{}
	for _, node := range nodes.Items {
		versions[node.Name] = node.Status.NodeInfo.KernelVersion
	}
	return versions, nil
}
