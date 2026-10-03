package main

import (
	"testing"

	"github.com/shalb/kube-dc/cli/internal/bootstrap/anchors"
	"github.com/shalb/kube-dc/cli/internal/bootstrap/clusterinit"
	"github.com/shalb/kube-dc/cli/internal/bootstrap/oidccutover"
	"github.com/shalb/kube-dc/cli/internal/bootstrap/ports"
)

func TestManagedServicesAnchorResolverUsesStandaloneAndInstallTargets(t *testing.T) {
	fleet := anchors.NewHostResolver(map[string]string{"master-2": "192.0.2.22", "master-3": "192.0.2.23"})
	standalone := managedServicesAnchorResolver(&clusterinit.InitOptions{}, []oidccutover.Node{
		{Name: "master-1", Host: ports.SSHHost{Alias: "203.0.113.11", User: "ubuntu"}},
		{Name: "master-2", Host: ports.SSHHost{Alias: "203.0.113.12", User: "ubuntu"}},
	}, fleet, 3)
	if got := standalone("master-2"); got.Alias != "203.0.113.12" || got.User != "ubuntu" {
		t.Fatalf("standalone SSH target ignored: %+v", got)
	}
	if got := standalone("master-3"); got.Hostname != "192.0.2.23" {
		t.Fatalf("Fleet SSH mapping ignored: %+v", got)
	}
	install := managedServicesAnchorResolver(&clusterinit.InitOptions{
		PrimaryNode: "master-1", SSHHost: "root@203.0.113.11",
		NodeSSHHosts: map[string]string{"master-2": "ubuntu@203.0.113.12"},
	}, nil, fleet, 3)
	if got := install("master-1"); got.Alias != "203.0.113.11" || got.User != "root" {
		t.Fatalf("install primary SSH target ignored: %+v", got)
	}
	if got := install("master-2"); got.Alias != "203.0.113.12" || got.User != "ubuntu" {
		t.Fatalf("install per-node SSH target ignored: %+v", got)
	}
}
