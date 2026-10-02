package main

import (
	"context"
	"testing"
)

type fakeServicesDoctorReader map[string]map[string]any

func (f fakeServicesDoctorReader) Get(_ context.Context, namespace, resource, name string) (map[string]any, error) {
	return f[namespace+"/"+resource+"/"+name], nil
}

func TestManagedServicesDoctorDetectsStaleAdmission(t *testing.T) {
	pins := map[string]string{"CLUSTER_NAME": "example", "SERVICES_CELL_ID": "cell-example", "SERVICES_DATAPLANE_NAME": "example-platform"}
	lease := map[string]any{
		"metadata": map[string]any{"name": "api-1", "uid": "lease-1", "labels": map[string]any{"apiserver.kubernetes.io/identity": "kube-apiserver", "kubernetes.io/hostname": "os-host-1"}},
		"spec":     map[string]any{"holderIdentity": "holder-1"},
	}
	server := map[string]any{
		"address": "192.0.2.1:6443", "tlsServerName": "kubernetes.default.svc",
		"leaseRef": map[string]any{"name": "api-1", "uid": "lease-1"}, "holderIdentity": "holder-1",
	}
	reader := fakeServicesDoctorReader{
		"flux-system/configmap/cluster-config": {"data": map[string]any{"CLUSTER_NAME": "example", "SERVICES_CELL_ID": "cell-example"}},
		"/servicedataplane/example-platform":   {"spec": map[string]any{"backupAdmission": map[string]any{"state": "Active", "revision": 1, "systemNamespaceUID": "system-1", "servers": []any{server}}}},
		"/namespace/kube-system":               {"metadata": map[string]any{"uid": "system-1"}},
		"kube-system/lease/":                   {"items": []any{lease}},
		"/nodes/": {"items": []any{map[string]any{
			"metadata": map[string]any{"name": "node-1"},
			"status":   map[string]any{"addresses": []any{map[string]any{"type": "InternalIP", "address": "192.0.2.1"}}},
		}}},
	}
	inventory := []managedServicesServer{{Name: "node-1", IP: "192.0.2.1", Hostname: "os-host-1"}}
	check := func() error { return checkManagedServicesAdmissionFresh(context.Background(), reader, pins, inventory) }
	if err := check(); err != nil {
		t.Fatalf("fresh topology: %v", err)
	}
	lease["spec"].(map[string]any)["holderIdentity"] = "holder-2"
	if err := check(); err == nil {
		t.Fatalf("API-server restart was not detected: %v", err)
	}
	lease["spec"].(map[string]any)["holderIdentity"] = "holder-1"
	lease["metadata"].(map[string]any)["uid"] = "lease-2"
	if err := check(); err == nil {
		t.Fatal("Lease replacement was not detected")
	}
	lease["metadata"].(map[string]any)["uid"] = "lease-1"
	server["address"] = "192.0.2.2:6443"
	if err := check(); err == nil {
		t.Fatal("wrong API-server endpoint was accepted")
	}
	server["address"] = "192.0.2.1:6443"
	reader["kube-system/lease/"]["items"] = []any{lease, map[string]any{
		"metadata": map[string]any{"name": "api-2", "uid": "lease-2", "labels": map[string]any{"apiserver.kubernetes.io/identity": "kube-apiserver"}},
		"spec":     map[string]any{"holderIdentity": "holder-2"},
	}}
	if err := check(); err == nil {
		t.Fatal("additional API-server Lease was not detected")
	}
}
