package k8s

import (
	"context"
	"fmt"
	"sort"
	"strings"
	"time"

	appsv1 "k8s.io/api/apps/v1"
	corev1 "k8s.io/api/core/v1"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
	"k8s.io/apimachinery/pkg/runtime/schema"
	"k8s.io/apimachinery/pkg/types"

	"github.com/shalb/kube-dc/cli/internal/bootstrap/ports"
)

const (
	hamiDRANamespace    = "hami-system"
	hamiDRAName         = "hami-dra-driver-kubelet-plugin"
	legacyHAMiNamespace = "gpu-operator"
)

var (
	deviceClassGVR   = schema.GroupVersionResource{Group: "resource.k8s.io", Version: "v1", Resource: "deviceclasses"}
	resourceSliceGVR = schema.GroupVersionResource{Group: "resource.k8s.io", Version: "v1", Resource: "resourceslices"}
	resourceClaimGVR = schema.GroupVersionResource{Group: "resource.k8s.io", Version: "v1", Resource: "resourceclaims"}
)

// GPUDRAStatus reads only the operator facts required by the DRA support gate.
// It deliberately avoids raw device identity in the returned port type.
func (c *Client) GPUDRAStatus(ctx context.Context, driver, deviceClass string) (ports.GPUDRAStatus, error) {
	status := ports.GPUDRAStatus{Driver: driver, DeviceClass: deviceClass}
	version, err := c.core.Discovery().ServerVersion()
	if err != nil {
		return status, fmt.Errorf("k8s: discover server version: %w", err)
	}
	status.ServerVersion = version.GitVersion

	classes, available, err := c.optionalDRAList(ctx, deviceClassGVR, false)
	if err != nil {
		return status, err
	}
	status.StableAPI = available
	gpuClasses := map[string]bool{deviceClass: true}
	for i := range classes {
		if classes[i].GetName() == deviceClass {
			status.DeviceClassPresent = true
		}
		labels := classes[i].GetLabels()
		if labels["app.kubernetes.io/managed-by"] == "kube-dc" && labels["kube-dc.com/gpu-profile"] != "" {
			gpuClasses[classes[i].GetName()] = true
		}
	}
	if !available {
		return status, nil
	}

	slices, _, err := c.optionalDRAList(ctx, resourceSliceGVR, false)
	if err != nil {
		return status, err
	}
	for i := range slices {
		sliceDriver, _, _ := unstructured.NestedString(slices[i].Object, "spec", "driver")
		if sliceDriver != driver {
			continue
		}
		status.ResourceSlices++
		devices, _, _ := unstructured.NestedSlice(slices[i].Object, "spec", "devices")
		for _, raw := range devices {
			device, ok := raw.(map[string]any)
			if !ok {
				continue
			}
			status.Devices++
			multiple, _, _ := unstructured.NestedBool(device, "allowMultipleAllocations")
			if multiple {
				status.ShareableDevices++
			}
			capacity, _, _ := unstructured.NestedMap(device, "capacity")
			for _, rawCapacity := range capacity {
				entry, ok := rawCapacity.(map[string]any)
				if !ok {
					continue
				}
				if _, ok := entry["requestPolicy"]; ok {
					status.ConsumableCapacity = true
				}
			}
		}
	}

	gpuClaims := map[string]bool{}
	claims, _, err := c.optionalDRAList(ctx, resourceClaimGVR, true)
	if err != nil {
		return status, err
	}
	for i := range claims {
		key := claims[i].GetNamespace() + "/" + claims[i].GetName()
		targetClaim := claimMatchesDRA(&claims[i], driver, deviceClass)
		if claimMatchesDeviceClasses(&claims[i], gpuClasses) {
			gpuClaims[key] = true
			status.GPUClaims++
		}
		if !targetClaim {
			continue
		}
		status.Claims++
		if allocation, ok, _ := unstructured.NestedMap(claims[i].Object, "status", "allocation"); ok && len(allocation) > 0 {
			status.AllocatedClaims++
		} else {
			status.PendingClaims++
		}
	}

	if err := c.populateDRAWorkloads(ctx, &status, gpuClaims); err != nil {
		return status, err
	}
	if err := c.populateDRAOperators(ctx, &status); err != nil {
		return status, err
	}
	if err := c.populateDRACanary(ctx, &status); err != nil {
		return status, err
	}
	return status, nil
}

func (c *Client) optionalDRAList(ctx context.Context, gvr schema.GroupVersionResource, namespaced bool) ([]unstructured.Unstructured, bool, error) {
	ri := c.dyn.Resource(gvr)
	var list *unstructured.UnstructuredList
	var err error
	if namespaced {
		list, err = ri.Namespace(metav1.NamespaceAll).List(ctx, metav1.ListOptions{})
	} else {
		list, err = ri.List(ctx, metav1.ListOptions{})
	}
	if apierrors.IsNotFound(err) || apierrors.IsMethodNotSupported(err) {
		return nil, false, nil
	}
	if err != nil {
		return nil, false, fmt.Errorf("k8s: list %s: %w", gvr.Resource, err)
	}
	return list.Items, true, nil
}

func claimMatchesDRA(claim *unstructured.Unstructured, driver, deviceClass string) bool {
	requests, _, _ := unstructured.NestedSlice(claim.Object, "spec", "devices", "requests")
	for _, raw := range requests {
		request, ok := raw.(map[string]any)
		if !ok {
			continue
		}
		class, _, _ := unstructured.NestedString(request, "exactly", "deviceClassName")
		if class == deviceClass {
			return true
		}
	}
	results, _, _ := unstructured.NestedSlice(claim.Object, "status", "allocation", "devices", "results")
	for _, raw := range results {
		result, ok := raw.(map[string]any)
		if ok && result["driver"] == driver {
			return true
		}
	}
	return false
}

func claimMatchesDeviceClasses(claim *unstructured.Unstructured, classes map[string]bool) bool {
	requests, _, _ := unstructured.NestedSlice(claim.Object, "spec", "devices", "requests")
	for _, raw := range requests {
		request, ok := raw.(map[string]any)
		if !ok {
			continue
		}
		class, _, _ := unstructured.NestedString(request, "exactly", "deviceClassName")
		if classes[class] {
			return true
		}
		alternatives, _, _ := unstructured.NestedSlice(request, "firstAvailable")
		for _, rawAlternative := range alternatives {
			alternative, ok := rawAlternative.(map[string]any)
			if !ok {
				continue
			}
			class, _, _ = unstructured.NestedString(alternative, "deviceClassName")
			if classes[class] {
				return true
			}
		}
	}
	return false
}

func (c *Client) populateDRAWorkloads(ctx context.Context, status *ports.GPUDRAStatus, matchingClaims map[string]bool) error {
	pods, err := c.core.CoreV1().Pods(metav1.NamespaceAll).List(ctx, metav1.ListOptions{})
	if err != nil {
		return fmt.Errorf("k8s: list Pods for DRA holders: %w", err)
	}
	for i := range pods.Items {
		pod := &pods.Items[i]
		if pod.Status.Phase == "Succeeded" || pod.Status.Phase == "Failed" {
			continue
		}
		legacy := map[string]bool{}
		for _, container := range append(pod.Spec.InitContainers, pod.Spec.Containers...) {
			for name, quantity := range container.Resources.Requests {
				if !quantity.IsZero() && (strings.HasPrefix(string(name), "nvidia.com/") || strings.HasPrefix(string(name), "requests.nvidia.com/")) {
					legacy[string(name)] = true
				}
			}
		}
		for _, container := range pod.Spec.EphemeralContainers {
			for name, quantity := range container.Resources.Requests {
				if !quantity.IsZero() && (strings.HasPrefix(string(name), "nvidia.com/") || strings.HasPrefix(string(name), "requests.nvidia.com/")) {
					legacy[string(name)] = true
				}
			}
		}
		if len(legacy) > 0 || pod.Spec.SchedulerName == "hami-scheduler" {
			status.LegacyHolders = append(status.LegacyHolders, podGPUHolder(pod.Namespace, pod.Name, pod.OwnerReferences, sortedKeys(legacy)))
		}
		matches := false
		for _, claimStatus := range pod.Status.ResourceClaimStatuses {
			if claimStatus.ResourceClaimName != nil && matchingClaims[pod.Namespace+"/"+*claimStatus.ResourceClaimName] {
				matches = true
			}
		}
		if matches {
			status.DRAHolders = append(status.DRAHolders, podGPUHolder(pod.Namespace, pod.Name, pod.OwnerReferences, []string{"DRA claim"}))
		}
	}
	sortGPUHolders(status.LegacyHolders)
	sortGPUHolders(status.DRAHolders)
	return nil
}

func podGPUHolder(namespace, name string, owners []metav1.OwnerReference, resources []string) ports.GPUHolder {
	holder := ports.GPUHolder{Namespace: namespace, Kind: "Pod", Name: name, Resources: resources}
	for _, owner := range owners {
		if owner.Controller != nil && *owner.Controller {
			holder.Kind, holder.Name = owner.Kind, owner.Name
			break
		}
	}
	return holder
}

func sortedKeys(values map[string]bool) []string {
	out := make([]string, 0, len(values))
	for value := range values {
		out = append(out, value)
	}
	sort.Strings(out)
	return out
}

func sortGPUHolders(holders []ports.GPUHolder) {
	sort.Slice(holders, func(i, j int) bool {
		return holders[i].Namespace+"/"+holders[i].Kind+"/"+holders[i].Name < holders[j].Namespace+"/"+holders[j].Kind+"/"+holders[j].Name
	})
}

func (c *Client) populateDRAOperators(ctx context.Context, status *ports.GPUDRAStatus) error {
	nodes, err := c.core.CoreV1().Nodes().List(ctx, metav1.ListOptions{})
	if err != nil {
		return fmt.Errorf("k8s: list Nodes for DRA ownership: %w", err)
	}
	nodeReady := map[string]bool{}
	for i := range nodes.Items {
		for _, condition := range nodes.Items[i].Status.Conditions {
			if condition.Type == corev1.NodeReady && condition.Status == corev1.ConditionTrue {
				nodeReady[nodes.Items[i].Name] = true
			}
		}
		active := nodes.Items[i].Labels["kube-dc.com/gpu.workload-mode"]
		expected := nodes.Items[i].Labels["kube-dc.com/gpu.expected-workload-mode"]
		if active == "pod-hami-dra" || expected == "pod-hami-dra" {
			status.DRANodes = append(status.DRANodes, nodes.Items[i].Name)
			if active != expected {
				status.WrongModeNodes = append(status.WrongModeNodes, nodes.Items[i].Name)
			}
		}
	}
	sort.Strings(status.DRANodes)
	sort.Strings(status.WrongModeNodes)

	daemonSets := make([]appsv1.DaemonSet, 0)
	// Trusted-namespace Lists and the exact checks below are deliberately
	// redundant. Keep both layers so a future broader List cannot make a tenant
	// object authoritative, and a future relaxed filter is still namespace-bound.
	for _, namespace := range []string{hamiDRANamespace, legacyHAMiNamespace} {
		list, err := c.core.AppsV1().DaemonSets(namespace).List(ctx, metav1.ListOptions{})
		if err != nil {
			return fmt.Errorf("k8s: list trusted %s DaemonSets for DRA ownership: %w", namespace, err)
		}
		daemonSets = append(daemonSets, list.Items...)
	}
	images := map[string]bool{}
	draDaemonSets := map[string]types.UID{}
	for i := range daemonSets {
		// Revalidate namespace, name and labels even though the API reads above
		// are already namespace-scoped; this loop is the inner trust boundary.
		ds := &daemonSets[i]
		name := ds.Namespace + "/" + ds.Name
		isDRA := ds.Namespace == hamiDRANamespace && ds.Name == hamiDRAName &&
			ds.Labels["app.kubernetes.io/name"] == "hami-dra-driver" &&
			ds.Labels["app.kubernetes.io/component"] == "kubelet-plugin"
		isLegacy := ds.Namespace == legacyHAMiNamespace && strings.Contains(ds.Name, "hami-device-plugin")
		if (isDRA || isLegacy) && ds.Status.NumberAvailable > 0 {
			status.AllocatorOwners = append(status.AllocatorOwners, name)
		}
		if !isDRA {
			continue
		}
		draDaemonSets[name] = ds.UID
		status.DriverReady += ds.Status.NumberReady
		status.DriverDesired += ds.Status.DesiredNumberScheduled
		for _, container := range append(ds.Spec.Template.Spec.InitContainers, ds.Spec.Template.Spec.Containers...) {
			images[container.Image] = true
		}
	}
	status.DriverImages = sortedKeys(images)
	sort.Strings(status.AllocatorOwners)

	driverPods, err := c.core.CoreV1().Pods(hamiDRANamespace).List(ctx, metav1.ListOptions{LabelSelector: "app.kubernetes.io/name=hami-dra-driver,hami-dra-driver-component=kubelet-plugin"})
	if err != nil {
		return fmt.Errorf("k8s: list trusted HAMi DRA driver Pods: %w", err)
	}
	for i := range driverPods.Items {
		pod := &driverPods.Items[i]
		owner := metav1.GetControllerOf(pod)
		if owner == nil || owner.Kind != "DaemonSet" {
			continue
		}
		daemonSetUID, owned := draDaemonSets[pod.Namespace+"/"+owner.Name]
		if !owned || daemonSetUID == "" || owner.UID != daemonSetUID {
			continue
		}
		ready := false
		var readyTransition time.Time
		for _, condition := range pod.Status.Conditions {
			if condition.Type == corev1.PodReady {
				ready = condition.Status == corev1.ConditionTrue
				readyTransition = condition.LastTransitionTime.Time
				break
			}
		}
		status.DriverPods = append(status.DriverPods, ports.GPUDRADriverPod{
			Namespace: pod.Namespace, Name: pod.Name, Node: pod.Spec.NodeName,
			Phase: string(pod.Status.Phase), Ready: ready, NodeReady: nodeReady[pod.Spec.NodeName],
			Deleting: pod.DeletionTimestamp != nil, CreationTimestamp: pod.CreationTimestamp.Time,
			ReadyLastTransitionTime: readyTransition,
		})
	}
	sort.Slice(status.DriverPods, func(i, j int) bool {
		return status.DriverPods[i].Namespace+"/"+status.DriverPods[i].Name < status.DriverPods[j].Namespace+"/"+status.DriverPods[j].Name
	})

	deployments, err := c.core.AppsV1().Deployments(metav1.NamespaceAll).List(ctx, metav1.ListOptions{})
	if err != nil {
		return fmt.Errorf("k8s: list Deployments for GPU creation gates: %w", err)
	}
	for i := range deployments.Items {
		for _, container := range deployments.Items[i].Spec.Template.Spec.Containers {
			for _, env := range container.Env {
				switch env.Name {
				case "GPU_SHARED_CREATION_ENABLED":
					status.SharedCreationEnabled = strings.EqualFold(env.Value, "true")
				case "GPU_VM_CREATION_ENABLED":
					status.VMCreationEnabled = strings.EqualFold(env.Value, "true")
				}
			}
		}
	}
	return nil
}

func (c *Client) populateDRACanary(ctx context.Context, status *ports.GPUDRAStatus) error {
	cronJob, err := c.core.BatchV1().CronJobs("gpu-operator").Get(ctx, "hami-device-plugin-canary", metav1.GetOptions{})
	if apierrors.IsNotFound(err) {
		return nil
	}
	if err != nil {
		return fmt.Errorf("k8s: get HAMi DRA canary: %w", err)
	}
	status.CanaryPresent = true
	status.CanarySuspended = cronJob.Spec.Suspend != nil && *cronJob.Spec.Suspend
	if cronJob.Status.LastSuccessfulTime != nil {
		value := cronJob.Status.LastSuccessfulTime.Time
		status.CanaryLastSuccessfulTime = &value
	}
	return nil
}
