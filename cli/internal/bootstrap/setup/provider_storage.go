package setup

import (
	"context"
	"fmt"
	"sort"
	"time"

	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/client-go/kubernetes"
)

// ProviderStorageObservation describes API configuration, not provisioning
// success. StorageClass and CSIDriver objects cannot prove raw-block support,
// free capacity, volume placement, or storage-system failure tolerance.
type ProviderStorageObservation struct {
	Name, Provisioner, BindingMode string
	Expansion                      bool
	TopologyKeys                   []string
	CSIStorageCapacity             bool
	ObservedAt                     time.Time
}

func InspectProviderStorage(ctx context.Context, client kubernetes.Interface, name string) (ProviderStorageObservation, error) {
	var result ProviderStorageObservation
	if client == nil || name == "" {
		return result, fmt.Errorf("select a target cluster and StorageClass first")
	}
	class, err := client.StorageV1().StorageClasses().Get(ctx, name, metav1.GetOptions{})
	if err != nil {
		return result, fmt.Errorf("cannot read the selected StorageClass; check target access")
	}
	result.Name, result.Provisioner = class.Name, class.Provisioner
	result.BindingMode = "Immediate"
	if class.VolumeBindingMode != nil {
		result.BindingMode = string(*class.VolumeBindingMode)
	}
	result.Expansion = class.AllowVolumeExpansion != nil && *class.AllowVolumeExpansion
	keys := map[string]bool{}
	for _, term := range class.AllowedTopologies {
		for _, expression := range term.MatchLabelExpressions {
			keys[expression.Key] = true
		}
	}
	for key := range keys {
		result.TopologyKeys = append(result.TopologyKeys, key)
	}
	sort.Strings(result.TopologyKeys)
	// A missing or unreadable CSIDriver is not evidence of a working driver.
	if driver, err := client.StorageV1().CSIDrivers().Get(ctx, class.Provisioner, metav1.GetOptions{}); err == nil {
		result.CSIStorageCapacity = driver.Spec.StorageCapacity != nil && *driver.Spec.StorageCapacity
	}
	result.ObservedAt = time.Now().UTC()
	return result, nil
}

func (o ProviderStorageObservation) Summary() []string {
	return []string{
		fmt.Sprintf("%s · %s · expansion: %t", o.Provisioner, o.BindingMode, o.Expansion),
		"Raw block, available capacity, and placement: unverified. Run a provisioning test before installation.",
	}
}
