package setup

import (
	"context"
	"strings"
	"testing"

	corev1 "k8s.io/api/core/v1"
	storagev1 "k8s.io/api/storage/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/client-go/kubernetes/fake"
)

func TestProviderStorageInspectionDoesNotInferBlockSupportOrCapacity(t *testing.T) {
	expand, capacity := true, true
	binding := storagev1.VolumeBindingWaitForFirstConsumer
	client := fake.NewClientset(&storagev1.StorageClass{
		ObjectMeta: metav1.ObjectMeta{Name: "provider-block"}, Provisioner: "csi.example.test",
		AllowVolumeExpansion: &expand, VolumeBindingMode: &binding,
		AllowedTopologies: []corev1.TopologySelectorTerm{{MatchLabelExpressions: []corev1.TopologySelectorLabelRequirement{{Key: "topology.kubernetes.io/zone", Values: []string{"zone-a"}}}}},
	}, &storagev1.CSIDriver{ObjectMeta: metav1.ObjectMeta{Name: "csi.example.test"}, Spec: storagev1.CSIDriverSpec{StorageCapacity: &capacity}})
	result, err := InspectProviderStorage(context.Background(), client, "provider-block")
	if err != nil || !result.Expansion || result.BindingMode != "WaitForFirstConsumer" || !result.CSIStorageCapacity || len(result.TopologyKeys) != 1 {
		t.Fatalf("incomplete class inspection: %+v %v", result, err)
	}
	if !strings.Contains(strings.Join(result.Summary(), " "), "unverified") {
		t.Fatal("class settings were treated as provisioning evidence")
	}
	for _, action := range client.Actions() {
		if action.GetVerb() != "get" {
			t.Fatal("inspection changed the target")
		}
	}
	if _, err := InspectProviderStorage(context.Background(), client, "absent"); err == nil {
		t.Fatal("missing class reported as inspected")
	}
}
