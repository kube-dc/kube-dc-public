package main

import (
	"context"
	"strings"
	"testing"

	"github.com/shalb/kube-dc/cli/internal/bootstrap/oidccutover"
	"github.com/shalb/kube-dc/cli/internal/bootstrap/ports"
)

type managedHostnameSSH struct{ ports.SSHClient }

func (managedHostnameSSH) Run(context.Context, ports.SSHHost, string) ([]byte, error) {
	return []byte("os-host\n"), nil
}

func TestManagedServicesAdmissionRequiresCompleteLeaseInventory(t *testing.T) {
	lease := func(node, name, uid string) map[string]any {
		return map[string]any{"metadata": map[string]any{"name": name, "uid": uid, "labels": map[string]any{
			"apiserver.kubernetes.io/identity": "kube-apiserver", "kubernetes.io/hostname": node}},
			"spec": map[string]any{"holderIdentity": name + "_boot"}}
	}
	servers := []managedServicesServer{{Name: "node-a", IP: "192.0.2.1", Hostname: "node-a"}, {Name: "node-b", IP: "192.0.2.2", Hostname: "node-b"}}
	leases := map[string]any{"items": []any{lease("node-a", "apiserver-a", "uid-a"), lease("node-b", "apiserver-b", "uid-b")}}
	got, err := buildManagedServicesAdmissionTopology(servers, leases, "system-uid", 3)
	if err != nil || got.Revision != 4 || got.State != "Active" || got.SystemNamespaceUID != "system-uid" || len(got.Servers) != 2 || got.Servers[0].Address != "192.0.2.1:6443" || got.Servers[0].LeaseRef.UID != "uid-a" {
		t.Fatalf("topology = %+v, %v", got, err)
	}
	for _, tc := range []struct {
		name      string
		inventory []managedServicesServer
		leases    map[string]any
		want      string
	}{
		{"missing lease", servers, map[string]any{"items": []any{lease("node-a", "apiserver-a", "uid-a")}}, "count"},
		{"stale lease", servers[:1], leases, "count"},
		{"wrong hostname", servers, map[string]any{"items": []any{lease("node-a", "apiserver-a", "uid-a"), lease("node-c", "apiserver-c", "uid-c")}}, "no matching"},
		{"duplicate IP", []managedServicesServer{{Name: "node-a", IP: "192.0.2.1", Hostname: "node-a"}, {Name: "node-b", IP: "192.0.2.1", Hostname: "node-b"}}, leases, "duplicate"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			if _, err := buildManagedServicesAdmissionTopology(tc.inventory, tc.leases, "system-uid", 0); err == nil || !strings.Contains(err.Error(), tc.want) {
				t.Fatalf("error = %v, want %q", err, tc.want)
			}
		})
	}
}

func TestManagedServicesAdmissionRefusesForeignPlanePauseAndMembershipChange(t *testing.T) {
	pins := map[string]string{"CLUSTER_NAME": "sample", "SERVICES_CELL_ID": "cell-sample", "SERVICES_DATAPLANE_NAME": "sample-platform"}
	ref := map[string]any{"name": "dp-sample-platform-token", "namespace": "kube-dc-services", "key": "value"}
	plane := map[string]any{"metadata": map[string]any{"name": "sample-platform"}, "spec": map[string]any{
		"cell": "cell-sample", "provider": "kube-dc", "connector": map[string]any{"type": "Direct", "direct": map[string]any{"kubeconfigSecretRef": ref}},
		"backupAdmission": map[string]any{"revision": 1, "state": "Paused", "servers": []any{map[string]any{"address": "192.0.2.1:6443"}}},
	}}
	service := map[string]any{"spec": map[string]any{"values": map[string]any{"dataPlane": map[string]any{"selfName": "sample-platform"}}}}
	reader := fakeManagedServicesReader{
		"flux-system/configmap/cluster-config":               {"data": map[string]any{"CLUSTER_NAME": "sample", "SERVICES_CELL_ID": "cell-sample"}},
		"/servicedataplane/sample-platform":                  plane,
		"kube-dc-services/helmrelease/kube-dc-services":      service,
		"kube-dc-services/serviceaccount/dp-sample-platform": {"metadata": map[string]any{"name": "dp-sample-platform", "uid": "sa-uid"}},
		"kube-dc-services/secret/dp-sample-platform-token": {"metadata": map[string]any{"name": "dp-sample-platform-token", "annotations": map[string]any{
			"kubernetes.io/service-account.name": "dp-sample-platform", "kubernetes.io/service-account.uid": "sa-uid"}},
			"type": "kubernetes.io/service-account-token", "data": map[string]any{"token": "token", "ca.crt": "ca"}},
	}
	inventory := []managedServicesServer{{Name: "node-a", IP: "192.0.2.1"}}
	if err := refreshManagedServicesAdmission(context.Background(), reader, "kubeconfig", pins, inventory, true); err == nil || !strings.Contains(err.Error(), "paused") {
		t.Fatalf("paused plane accepted: %v", err)
	}
	plane["spec"].(map[string]any)["backupAdmission"].(map[string]any)["state"] = "Active"
	inventory[0].IP = "192.0.2.2"
	if err := refreshManagedServicesAdmission(context.Background(), reader, "kubeconfig", pins, inventory, true); err == nil || !strings.Contains(err.Error(), "IP set changed") {
		t.Fatalf("membership change accepted: %v", err)
	}
	inventory[0].IP = "192.0.2.1"
	service["spec"].(map[string]any)["values"].(map[string]any)["dataPlane"].(map[string]any)["selfName"] = "remote"
	if err := refreshManagedServicesAdmission(context.Background(), reader, "kubeconfig", pins, inventory, true); err == nil || !strings.Contains(err.Error(), "self plane") {
		t.Fatalf("foreign plane accepted: %v", err)
	}
	service["spec"].(map[string]any)["values"].(map[string]any)["dataPlane"].(map[string]any)["selfName"] = "sample-platform"
	plane["spec"].(map[string]any)["backupAdmission"] = nil
	if err := refreshManagedServicesAdmission(context.Background(), reader, "kubeconfig", pins, inventory, false); err == nil || !strings.Contains(err.Error(), "requires verified OIDC cutover") {
		t.Fatalf("pre-cutover initial admission accepted: %v", err)
	}
}

func TestManagedServicesNodeNameCanDifferFromLeaseHostname(t *testing.T) {
	resolved, err := resolveManagedServicesOSHostnames(context.Background(), managedHostnameSSH{}, []oidccutover.Node{{Name: "rke2-custom", Host: ports.SSHHost{Hostname: "192.0.2.1"}}})
	if err != nil || len(resolved) != 1 || resolved[0].Hostname != "os-host" {
		t.Fatalf("SSH hostname: %v, %v", resolved, err)
	}
	nodes := map[string]any{"items": []any{map[string]any{
		"metadata": map[string]any{"name": "rke2-custom", "labels": map[string]any{"kubernetes.io/hostname": "os-host"}},
		"status":   map[string]any{"addresses": []any{map[string]any{"type": "InternalIP", "address": "192.0.2.1"}}},
	}}}
	completed, err := completeManagedServicesInventory(resolved, nodes)
	if err != nil || len(completed) != 1 || completed[0].Hostname != "os-host" {
		t.Fatalf("mapped inventory: %v, %v", completed, err)
	}
	leases := map[string]any{"items": []any{map[string]any{
		"metadata": map[string]any{"name": "apiserver-a", "uid": "uid-a", "labels": map[string]any{
			"apiserver.kubernetes.io/identity": "kube-apiserver", "kubernetes.io/hostname": "os-host"}},
		"spec": map[string]any{"holderIdentity": "apiserver-a_boot"},
	}}}
	if _, err := buildManagedServicesAdmissionTopology(completed, leases, "system-uid", 0); err != nil {
		t.Fatal(err)
	}
}

func TestManagedServicesInventoryKeepsInternalIPWithPublicSSHAlias(t *testing.T) {
	nodes := []oidccutover.Node{{Name: "e2e-master-1", InternalIP: "10.77.0.110",
		Host: ports.SSHHost{Alias: "c08-e2e-master"}}}
	resolved, err := resolveManagedServicesOSHostnames(context.Background(), managedHostnameSSH{}, nodes)
	if err != nil || len(resolved) != 1 || resolved[0].IP != "10.77.0.110" || resolved[0].Hostname != "os-host" {
		t.Fatalf("public SSH route replaced the live Node InternalIP: inventory=%+v err=%v", resolved, err)
	}
}
