package k8s

import (
	"context"
	"testing"
	"time"

	appsv1 "k8s.io/api/apps/v1"
	batchv1 "k8s.io/api/batch/v1"
	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/apimachinery/pkg/types"
	"k8s.io/apimachinery/pkg/version"
	fakediscovery "k8s.io/client-go/discovery/fake"
	"k8s.io/client-go/dynamic/fake"
	kubernetesfake "k8s.io/client-go/kubernetes/fake"
)

func TestGPUDRAStatusAggregatesQualifiedOperatorState(t *testing.T) {
	controller := true
	claimName := "trainer-gpu-abc"
	ownedClaimName := "a100-gpu"
	firstAvailableClaimName := "a100-first-available-gpu"
	foreignClaimName := "foreign-gpu"
	lastSuccess := metav1.NewTime(time.Date(2026, 7, 17, 12, 0, 0, 0, time.UTC))
	core := kubernetesfake.NewSimpleClientset(
		&corev1.Node{ObjectMeta: metav1.ObjectMeta{Name: "gpu-a", Labels: map[string]string{
			"kube-dc.com/gpu.workload-mode": "pod-hami-dra", "kube-dc.com/gpu.expected-workload-mode": "pod-hami-dra",
		}}, Status: corev1.NodeStatus{Conditions: []corev1.NodeCondition{{Type: corev1.NodeReady, Status: corev1.ConditionTrue}}}},
		&corev1.Pod{ObjectMeta: metav1.ObjectMeta{Namespace: "tenant-a", Name: "trainer-abc", OwnerReferences: []metav1.OwnerReference{{Kind: "ReplicaSet", Name: "trainer-123", Controller: &controller}}},
			Status: corev1.PodStatus{Phase: corev1.PodRunning, ResourceClaimStatuses: []corev1.PodResourceClaimStatus{{Name: "gpu", ResourceClaimName: &claimName}}}},
		&corev1.Pod{ObjectMeta: metav1.ObjectMeta{Namespace: "tenant-b", Name: "foreign-trainer"},
			Status: corev1.PodStatus{Phase: corev1.PodRunning, ResourceClaimStatuses: []corev1.PodResourceClaimStatus{{Name: "gpu", ResourceClaimName: &foreignClaimName}}}},
		&corev1.Pod{ObjectMeta: metav1.ObjectMeta{Namespace: "tenant-c", Name: "a100-trainer"},
			Status: corev1.PodStatus{Phase: corev1.PodRunning, ResourceClaimStatuses: []corev1.PodResourceClaimStatus{{Name: "gpu", ResourceClaimName: &ownedClaimName}}}},
		&corev1.Pod{ObjectMeta: metav1.ObjectMeta{Namespace: "tenant-d", Name: "a100-first-available-trainer"},
			Status: corev1.PodStatus{Phase: corev1.PodRunning, ResourceClaimStatuses: []corev1.PodResourceClaimStatus{{Name: "gpu", ResourceClaimName: &firstAvailableClaimName}}}},
		&appsv1.DaemonSet{ObjectMeta: metav1.ObjectMeta{Namespace: "hami-system", Name: "hami-dra-driver-kubelet-plugin", UID: types.UID("dra-ds"), Labels: map[string]string{"app.kubernetes.io/name": "hami-dra-driver", "app.kubernetes.io/component": "kubelet-plugin"}},
			Spec:   appsv1.DaemonSetSpec{Template: corev1.PodTemplateSpec{Spec: corev1.PodSpec{Containers: []corev1.Container{{Name: "driver", Image: "docker.io/projecthami/k8s-dra-driver@sha256:abc"}}}}},
			Status: appsv1.DaemonSetStatus{DesiredNumberScheduled: 1, NumberReady: 1, NumberAvailable: 1}},
		&appsv1.DaemonSet{ObjectMeta: metav1.ObjectMeta{Namespace: "tenant-forgery", Name: "hami-dra-driver-kubelet-plugin", UID: types.UID("forged-ds"), Labels: map[string]string{"app.kubernetes.io/name": "hami-dra-driver", "app.kubernetes.io/component": "kubelet-plugin"}},
			Status: appsv1.DaemonSetStatus{DesiredNumberScheduled: 99, NumberReady: 99, NumberAvailable: 99}},
		&corev1.Pod{ObjectMeta: metav1.ObjectMeta{Namespace: "hami-system", Name: "hami-dra-driver-gpu-a", CreationTimestamp: metav1.NewTime(time.Date(2026, 7, 17, 11, 0, 0, 0, time.UTC)), Labels: map[string]string{"app.kubernetes.io/name": "hami-dra-driver", "hami-dra-driver-component": "kubelet-plugin"}, OwnerReferences: []metav1.OwnerReference{{APIVersion: "apps/v1", Kind: "DaemonSet", Name: "hami-dra-driver-kubelet-plugin", UID: types.UID("dra-ds"), Controller: &controller}}},
			Spec: corev1.PodSpec{NodeName: "gpu-a"}, Status: corev1.PodStatus{Phase: corev1.PodRunning, Conditions: []corev1.PodCondition{{Type: corev1.PodReady, Status: corev1.ConditionTrue, LastTransitionTime: metav1.NewTime(time.Date(2026, 7, 17, 11, 5, 0, 0, time.UTC))}}}},
		&corev1.Pod{ObjectMeta: metav1.ObjectMeta{Namespace: "tenant-forgery", Name: "forged-driver", Labels: map[string]string{"app.kubernetes.io/name": "hami-dra-driver", "hami-dra-driver-component": "kubelet-plugin"}, OwnerReferences: []metav1.OwnerReference{{Kind: "DaemonSet", Name: "hami-dra-driver-kubelet-plugin", UID: types.UID("forged-ds"), Controller: &controller}}},
			Spec: corev1.PodSpec{NodeName: "gpu-a"}},
		&corev1.Pod{ObjectMeta: metav1.ObjectMeta{Namespace: "hami-system", Name: "stale-from-replaced-daemonset", OwnerReferences: []metav1.OwnerReference{{APIVersion: "apps/v1", Kind: "DaemonSet", Name: "hami-dra-driver-kubelet-plugin", UID: types.UID("old-dra-ds"), Controller: &controller}}},
			Spec: corev1.PodSpec{NodeName: "gpu-a"}, Status: corev1.PodStatus{Phase: corev1.PodUnknown}},
		&appsv1.Deployment{ObjectMeta: metav1.ObjectMeta{Namespace: "kube-dc", Name: "kube-dc-backend"}, Spec: appsv1.DeploymentSpec{Template: corev1.PodTemplateSpec{Spec: corev1.PodSpec{Containers: []corev1.Container{{Name: "backend", Env: []corev1.EnvVar{{Name: "GPU_SHARED_CREATION_ENABLED", Value: "false"}, {Name: "GPU_VM_CREATION_ENABLED", Value: "false"}}}}}}}},
		&batchv1.CronJob{ObjectMeta: metav1.ObjectMeta{Namespace: "gpu-operator", Name: "hami-device-plugin-canary"}, Status: batchv1.CronJobStatus{LastSuccessfulTime: &lastSuccess}},
	)
	core.Discovery().(*fakediscovery.FakeDiscovery).FakedServerVersion = &version.Info{GitVersion: "v1.36.2+rke2r1"}

	class := draObject("resource.k8s.io/v1", "DeviceClass", "", "kube-dc-v100-8g", map[string]any{"spec": map[string]any{}})
	ownedClass := draObject("resource.k8s.io/v1", "DeviceClass", "", "kube-dc-a100-8g", map[string]any{"spec": map[string]any{}})
	ownedClass.SetLabels(map[string]string{"app.kubernetes.io/managed-by": "kube-dc", "kube-dc.com/gpu-profile": "nvidia-a100-hami"})
	slice := draObject("resource.k8s.io/v1", "ResourceSlice", "", "gpu-a-slice", map[string]any{"spec": map[string]any{
		"driver": "hami.example.io", "devices": []any{map[string]any{
			"name": "redacted", "allowMultipleAllocations": true,
			"capacity": map[string]any{"memory": map[string]any{"value": "32Gi", "requestPolicy": map[string]any{"default": "1Gi"}}},
		}},
	}})
	claim := draObject("resource.k8s.io/v1", "ResourceClaim", "tenant-a", claimName, map[string]any{
		"spec":   map[string]any{"devices": map[string]any{"requests": []any{map[string]any{"exactly": map[string]any{"deviceClassName": "kube-dc-v100-8g"}}}}},
		"status": map[string]any{"allocation": map[string]any{"devices": map[string]any{"results": []any{map[string]any{"driver": "hami.example.io"}}}}},
	})
	foreignClaim := draObject("resource.k8s.io/v1", "ResourceClaim", "tenant-b", foreignClaimName, map[string]any{
		"spec": map[string]any{"devices": map[string]any{"requests": []any{map[string]any{"exactly": map[string]any{"deviceClassName": "foreign-class"}}}}},
	})
	ownedClaim := draObject("resource.k8s.io/v1", "ResourceClaim", "tenant-c", ownedClaimName, map[string]any{
		"spec": map[string]any{"devices": map[string]any{"requests": []any{map[string]any{"exactly": map[string]any{"deviceClassName": "kube-dc-a100-8g"}}}}},
	})
	firstAvailableClaim := draObject("resource.k8s.io/v1", "ResourceClaim", "tenant-d", firstAvailableClaimName, map[string]any{
		"spec": map[string]any{"devices": map[string]any{"requests": []any{map[string]any{"firstAvailable": []any{
			map[string]any{"name": "preferred", "deviceClassName": "kube-dc-a100-8g"},
			map[string]any{"name": "fallback", "deviceClassName": "foreign-class"},
		}}}}},
	})
	dyn := fake.NewSimpleDynamicClient(runtime.NewScheme(), class, ownedClass, slice, claim, ownedClaim, firstAvailableClaim, foreignClaim)
	client := &Client{core: core, dyn: dyn}
	status, err := client.GPUDRAStatus(context.Background(), "hami.example.io", "kube-dc-v100-8g")
	if err != nil {
		t.Fatal(err)
	}
	if status.ServerVersion != "v1.36.2+rke2r1" || !status.StableAPI || !status.ConsumableCapacity || !status.DeviceClassPresent {
		t.Fatalf("support state=%+v", status)
	}
	if status.ResourceSlices != 1 || status.Devices != 1 || status.ShareableDevices != 1 || status.Claims != 1 || status.GPUClaims != 3 || status.AllocatedClaims != 1 || len(status.DRAHolders) != 3 {
		t.Fatalf("inventory state=%+v", status)
	}
	if status.DriverReady != 1 || status.DriverDesired != 1 || len(status.AllocatorOwners) != 1 || len(status.DRANodes) != 1 || len(status.DriverPods) != 1 || !status.DriverPods[0].Ready || !status.DriverPods[0].NodeReady || status.DriverPods[0].ReadyLastTransitionTime.IsZero() || !status.CanaryPresent || status.CanaryLastSuccessfulTime == nil {
		t.Fatalf("operator state=%+v", status)
	}
}

func draObject(apiVersion, kind, namespace, name string, fields map[string]any) *unstructured.Unstructured {
	object := map[string]any{
		"apiVersion": apiVersion,
		"kind":       kind,
		"metadata":   map[string]any{"name": name},
	}
	if namespace != "" {
		object["metadata"].(map[string]any)["namespace"] = namespace
	}
	for key, value := range fields {
		object[key] = value
	}
	return &unstructured.Unstructured{Object: object}
}
