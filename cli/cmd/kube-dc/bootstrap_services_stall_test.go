package main

import (
	"context"
	"fmt"
	"strings"
	"testing"
	"time"
)

type fakeManagedServicesStallReader struct {
	release  map[string]any
	err      error
	resets   []int
	conflict bool
}

func (f *fakeManagedServicesStallReader) Get(context.Context, string, string, string) (map[string]any, error) {
	return f.release, f.err
}

func (f *fakeManagedServicesStallReader) ResetHelmRelease(_ context.Context, _, _, version, nonce string, count int) error {
	if _, err := time.Parse(time.RFC3339Nano, nonce); err != nil {
		return err
	}
	if version != f.release["metadata"].(map[string]any)["resourceVersion"] {
		return fmt.Errorf("Conflict")
	}
	if f.conflict {
		f.conflict = false
		metadata := f.release["metadata"].(map[string]any)
		metadata["resourceVersion"] = "124"
		annotations := metadata["annotations"].(map[string]any)
		annotations[managedServicesStallResetCount] = "1"
		annotations["reconcile.fluxcd.io/resetAt"] = nonce
		return fmt.Errorf("Conflict")
	}
	f.resets = append(f.resets, count)
	return nil
}

func stalledManagedServicesRelease() map[string]any {
	return map[string]any{
		"metadata": map[string]any{"name": "strimzi", "namespace": "strimzi-system", "generation": 2, "resourceVersion": "123", "annotations": map[string]any{}},
		"status": map[string]any{
			"observedGeneration": 2,
			"conditions":         []any{map[string]any{"type": "Stalled", "status": "True", "observedGeneration": 2}},
			"history":            []any{map[string]any{"status": "failed"}},
		},
	}
}

func TestResetStalledManagedServicesRelease(t *testing.T) {
	ctx := context.Background()
	t.Run("new install gets one reset", func(t *testing.T) {
		f := &fakeManagedServicesStallReader{release: stalledManagedServicesRelease()}
		reset, err := resetStalledManagedServicesRelease(ctx, f, "strimzi-system", "strimzi")
		if err != nil || !reset || len(f.resets) != 1 || f.resets[0] != 1 {
			t.Fatalf("reset=%t calls=%v err=%v", reset, f.resets, err)
		}
	})
	t.Run("already deployed is not reset", func(t *testing.T) {
		f := &fakeManagedServicesStallReader{release: stalledManagedServicesRelease()}
		f.release["status"].(map[string]any)["history"] = []any{map[string]any{"status": "superseded"}}
		reset, err := resetStalledManagedServicesRelease(ctx, f, "strimzi-system", "strimzi")
		if reset || err == nil || len(f.resets) != 0 {
			t.Fatalf("upgrade reset=%t calls=%v err=%v", reset, f.resets, err)
		}
	})
	t.Run("prior reset must be given time", func(t *testing.T) {
		f := &fakeManagedServicesStallReader{release: stalledManagedServicesRelease()}
		annotations := f.release["metadata"].(map[string]any)["annotations"].(map[string]any)
		annotations[managedServicesStallResetCount] = "1"
		annotations["reconcile.fluxcd.io/resetAt"] = time.Now().UTC().Format(time.RFC3339Nano)
		reset, err := resetStalledManagedServicesRelease(ctx, f, "strimzi-system", "strimzi")
		if err != nil || reset || len(f.resets) != 0 {
			t.Fatalf("premature reset=%t calls=%v err=%v", reset, f.resets, err)
		}
		annotations["reconcile.fluxcd.io/resetAt"] = time.Now().Add(-2 * time.Minute).UTC().Format(time.RFC3339Nano)
		f.release["status"].(map[string]any)["lastHandledResetAt"] = annotations["reconcile.fluxcd.io/resetAt"]
		f.release["status"].(map[string]any)["lastHandledReconcileAt"] = "a-later-reconcile"
		reset, err = resetStalledManagedServicesRelease(ctx, f, "strimzi-system", "strimzi")
		if err != nil || !reset || len(f.resets) != 1 || f.resets[0] != 2 {
			t.Fatalf("second reset=%t calls=%v err=%v", reset, f.resets, err)
		}
		annotations[managedServicesStallResetCount] = "2"
		_, err = resetStalledManagedServicesRelease(ctx, f, "strimzi-system", "strimzi")
		if err == nil || !strings.Contains(err.Error(), "after two resets") {
			t.Fatalf("expected bounded failure, got %v", err)
		}
	})
	t.Run("conflict rechecks another reset", func(t *testing.T) {
		f := &fakeManagedServicesStallReader{release: stalledManagedServicesRelease(), conflict: true}
		reset, err := resetStalledManagedServicesRelease(ctx, f, "strimzi-system", "strimzi")
		if err != nil || reset || len(f.resets) != 0 {
			t.Fatalf("concurrent reset=%t calls=%v err=%v", reset, f.resets, err)
		}
	})
	t.Run("stale condition cannot reset", func(t *testing.T) {
		f := &fakeManagedServicesStallReader{release: stalledManagedServicesRelease()}
		f.release["status"].(map[string]any)["conditions"].([]any)[0].(map[string]any)["observedGeneration"] = 1
		reset, err := resetStalledManagedServicesRelease(ctx, f, "strimzi-system", "strimzi")
		if err != nil || reset {
			t.Fatalf("stale reset=%t err=%v", reset, err)
		}
	})
	t.Run("missing is pending but other read errors fail", func(t *testing.T) {
		f := &fakeManagedServicesStallReader{err: fmt.Errorf("NotFound")}
		if reset, err := resetStalledManagedServicesRelease(ctx, f, "strimzi-system", "strimzi"); err != nil || reset {
			t.Fatalf("missing reset=%t err=%v", reset, err)
		}
		f.err = fmt.Errorf("forbidden")
		if _, err := resetStalledManagedServicesRelease(ctx, f, "strimzi-system", "strimzi"); err == nil {
			t.Fatal("forbidden read was ignored")
		}
	})
}

func TestManagedServicesResetTargetIdentity(t *testing.T) {
	pins := map[string]string{"CLUSTER_NAME": "example", "SERVICES_CELL_ID": "cell-example"}
	for _, tc := range []struct {
		name, cluster, cell string
		ready, fail         bool
	}{
		{"pending", "", "", false, false},
		{"right", "example", "cell-example", true, false},
		{"wrong cell", "example", "cell-other", false, true},
		{"wrong cluster", "other", "cell-example", false, true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			live := map[string]any{"data": map[string]any{"CLUSTER_NAME": tc.cluster, "SERVICES_CELL_ID": tc.cell}}
			ready, err := managedServicesResetTargetReady(live, pins)
			if ready != tc.ready || (err != nil) != tc.fail {
				t.Fatalf("ready=%t err=%v", ready, err)
			}
		})
	}
}
