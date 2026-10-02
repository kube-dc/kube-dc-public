package main

import (
	"context"
	"encoding/json"
	"fmt"
	"os/exec"
	"strconv"
	"strings"
	"time"
)

const managedServicesStallResetCount = "kube-dc.com/managed-services-stall-resets"

func managedServicesResetTargetReady(live map[string]any, pins map[string]string) (bool, error) {
	cluster := nestedString(live, "data", "CLUSTER_NAME")
	cell := nestedString(live, "data", "SERVICES_CELL_ID")
	if cluster == "" || cell == "" {
		return false, nil // Flux has not rendered the identity ConfigMap yet.
	}
	if cluster != pins["CLUSTER_NAME"] || cell != pins["SERVICES_CELL_ID"] {
		return false, fmt.Errorf("managed-services reconcile target differs from the Fleet cluster and cell identity")
	}
	return true, nil
}

type managedServicesStallReader interface {
	Get(context.Context, string, string, string) (map[string]any, error)
	ResetHelmRelease(context.Context, string, string, string, string, int) error
}

func (k managedServicesKubectl) ResetHelmRelease(ctx context.Context, namespace, name, resourceVersion, nonce string, count int) error {
	patch := map[string]any{"metadata": map[string]any{"resourceVersion": resourceVersion, "annotations": map[string]string{
		"reconcile.fluxcd.io/requestedAt": nonce,
		"reconcile.fluxcd.io/resetAt":     nonce,
		managedServicesStallResetCount:    strconv.Itoa(count),
	}}}
	body, err := json.Marshal(patch)
	if err != nil {
		return err
	}
	callCtx, cancel := context.WithTimeout(ctx, 20*time.Second)
	defer cancel()
	args := append(k.kubectlArgs(), "--request-timeout=15s", "-n", namespace,
		"patch", "helmrelease", name, "--type=merge", "--field-manager=kube-dc-cli", "-p", string(body))
	cmd := exec.CommandContext(callCtx, "kubectl", args...)
	if output, err := cmd.CombinedOutput(); err != nil {
		return fmt.Errorf("reset stalled HelmRelease %s/%s: %w: %s", namespace, name, err, strings.TrimSpace(string(output)))
	}
	return nil
}

// resetStalledManagedServicesRelease resets only never-deployed first installs.
// A reset remains pending until the controller handles it, so repeated polls
// cannot spend the two-reset allowance in one reconciliation cycle.
func resetStalledManagedServicesRelease(ctx context.Context, reader managedServicesStallReader, namespace, name string) (bool, error) {
	for attempt := 0; attempt < 3; attempt++ {
		reset, err := resetStalledManagedServicesReleaseOnce(ctx, reader, namespace, name)
		if err == nil || (!strings.Contains(err.Error(), "Conflict") && !strings.Contains(err.Error(), "the object has been modified")) {
			return reset, err
		}
	}
	return false, fmt.Errorf("managed-services HelmRelease %s/%s changed during three reset attempts", namespace, name)
}

func resetStalledManagedServicesReleaseOnce(ctx context.Context, reader managedServicesStallReader, namespace, name string) (bool, error) {
	release, err := reader.Get(ctx, namespace, "helmrelease", name)
	if err != nil {
		if strings.Contains(err.Error(), "NotFound") || strings.Contains(err.Error(), "not found") {
			return false, nil // the release has not been created yet
		}
		return false, err
	}
	if nestedString(release, "metadata", "name") != name || nestedString(release, "metadata", "namespace") != namespace {
		return false, fmt.Errorf("managed-services HelmRelease %s/%s identity mismatch", namespace, name)
	}
	resourceVersion := nestedString(release, "metadata", "resourceVersion")
	if resourceVersion == "" {
		return false, fmt.Errorf("managed-services HelmRelease %s/%s has no resourceVersion", namespace, name)
	}
	generation := nestedInt(release, "metadata", "generation")
	stalled := false
	for _, raw := range nestedSlice(release, "status", "conditions") {
		condition, ok := raw.(map[string]any)
		if !ok || nestedString(condition, "type") != "Stalled" || nestedString(condition, "status") != "True" {
			continue
		}
		observed := nestedInt(condition, "observedGeneration")
		if observed == 0 {
			observed = nestedInt(release, "status", "observedGeneration")
		}
		stalled = observed >= generation
	}
	if !stalled {
		return false, nil
	}
	for _, raw := range nestedSlice(release, "status", "history") {
		entry, ok := raw.(map[string]any)
		if ok && (nestedString(entry, "status") == "deployed" || nestedString(entry, "status") == "superseded") {
			return false, fmt.Errorf("managed-services HelmRelease %s/%s stalled during an upgrade; no first-install reset is permitted", namespace, name)
		}
	}
	countText := nestedString(release, "metadata", "annotations", managedServicesStallResetCount)
	count := 0
	if countText != "" {
		count, err = strconv.Atoi(countText)
		if err != nil || count < 0 || count > 2 {
			return false, fmt.Errorf("invalid stalled-release reset counter on %s/%s", namespace, name)
		}
	}
	if last := nestedString(release, "metadata", "annotations", "reconcile.fluxcd.io/resetAt"); last != "" && count > 0 {
		when, err := time.Parse(time.RFC3339Nano, last)
		if err != nil {
			return false, fmt.Errorf("invalid stalled-release reset timestamp on %s/%s", namespace, name)
		}
		if nestedString(release, "status", "lastHandledResetAt") != last || time.Since(when) < time.Minute {
			return false, nil
		}
	}
	if count >= 2 {
		return false, fmt.Errorf("managed-services HelmRelease %s/%s remains stalled after two resets", namespace, name)
	}
	nonce := time.Now().UTC().Format(time.RFC3339Nano)
	if err := reader.ResetHelmRelease(ctx, namespace, name, resourceVersion, nonce, count+1); err != nil {
		return false, err
	}
	return true, nil
}

func resetStalledManagedServicesReleases(ctx context.Context, reader managedServicesStallReader, includeServices bool) (bool, error) {
	releases := [][2]string{{"valkey-system", "kube-dc-valkey-operator"}, {"strimzi-system", "strimzi"}}
	if includeServices {
		releases = append(releases, [2]string{"kube-dc-services", "kube-dc-services"})
	}
	anyReset := false
	for _, release := range releases {
		reset, err := resetStalledManagedServicesRelease(ctx, reader, release[0], release[1])
		if err != nil {
			return anyReset, err
		}
		anyReset = anyReset || reset
	}
	return anyReset, nil
}
