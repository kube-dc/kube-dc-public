package clusterinit

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestResolveManagedServicesInternalNetwork(t *testing.T) {
	repo := t.TempDir()
	dir := filepath.Join(repo, "clusters", "fixture")
	if err := os.MkdirAll(dir, 0o755); err != nil {
		t.Fatal(err)
	}
	path := filepath.Join(dir, "cluster-config.env")
	seed := strings.Join([]string{
		"SERVICES_INTERNAL_GATEWAY_REQUIRED=true",
		"EXT_NET_CIDR=100.65.0.0/16", "EXT_NET_GATEWAY=100.65.0.1",
		"EXT_NET_EXCLUDE_IPS=100.65.0.1..100.65.0.2",
		"EXT_NET_MGMT_SNAT_IP=100.65.0.2", "KUBE_OVN_GW_NODES=",
		"ENVOY_GATEWAY_INTERNAL_VIP=", "PLATFORM_ENDPOINT_ENVOY_GATEWAY_ENABLED=false",
		"INGRESS_GLOBAL_ALLOWLIST=[\"10.0.0.1\"]", "EGRESS_GLOBAL_ALLOWLIST=[]",
		"EXT_NET_ANCHOR_IPS=", "EXT_NET_ANCHOR_REQUIRED=false", "",
		"INFRA_ATTACHMENT_ROUTES=10.77.0.0/16,172.30.0.0/22",
	}, "\n")
	if err := os.WriteFile(path, []byte(seed), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := ResolveManagedServicesInternalNetwork(repo, "fixture", []string{"node-b", "node-a"}, nil); err != nil {
		t.Fatal(err)
	}
	body, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	for _, want := range []string{
		"KUBE_OVN_GW_NODES=node-a,node-b", "EXT_NET_EXCLUDE_IPS=100.65.0.1..100.65.0.5",
		"ENVOY_GATEWAY_INTERNAL_VIP=100.65.0.3", "PLATFORM_ENDPOINT_ENVOY_GATEWAY_ENABLED=true",
		"INGRESS_GLOBAL_ALLOWLIST=[\"10.0.0.1\",\"100.65.0.3\"]",
		"EGRESS_GLOBAL_ALLOWLIST=[\"100.65.0.3\"]",
		"EXT_NET_ANCHOR_IPS=node-a=100.65.0.4/16,node-b=100.65.0.5/16",
		"EXT_NET_ANCHOR_REQUIRED=true",
		"INFRA_ATTACHMENT_ROUTES=10.77.0.0/16,172.30.0.0/22,100.65.0.3/32",
		"INFRA_ATTACHMENT_PLATFORM_INGRESS_VIP=100.65.0.3",
		"INFRA_ATTACHMENT_PLATFORM_INGRESS_WORKLOAD_EGRESS=true",
	} {
		if !strings.Contains(string(body), want+"\n") {
			t.Errorf("missing %q in %s", want, body)
		}
	}
}

func TestResolveManagedServicesInternalNetworkInsertsMissingStarterKeys(t *testing.T) {
	repo := t.TempDir()
	dir := filepath.Join(repo, "clusters", "fixture")
	if err := os.MkdirAll(dir, 0o755); err != nil {
		t.Fatal(err)
	}
	path := filepath.Join(dir, "cluster-config.env")
	seed := strings.Join([]string{
		"SERVICES_INTERNAL_GATEWAY_REQUIRED=true",
		"EXT_NET_CIDR=100.65.0.0/16", "EXT_NET_GATEWAY=100.65.0.1",
		"EXT_NET_EXCLUDE_IPS=100.65.0.1..100.65.0.2",
		"EXT_NET_MGMT_SNAT_IP=100.65.0.2",
		"ENVOY_GATEWAY_INTERNAL_VIP=", "PLATFORM_ENDPOINT_ENVOY_GATEWAY_ENABLED=false",
		"EXT_NET_ANCHOR_IPS=", "EXT_NET_ANCHOR_REQUIRED=false", "",
		"INFRA_ATTACHMENT_ROUTES=10.77.0.0/16",
	}, "\n")
	if err := os.WriteFile(path, []byte(seed), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := ResolveManagedServicesInternalNetwork(repo, "fixture", []string{"node-a"}, nil); err != nil {
		t.Fatal(err)
	}
	body, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	for _, want := range []string{
		"KUBE_OVN_GW_NODES=node-a",
		"INGRESS_GLOBAL_ALLOWLIST=[\"100.65.0.3\"]",
		"EGRESS_GLOBAL_ALLOWLIST=[\"100.65.0.3\"]",
		"INFRA_ATTACHMENT_ROUTES=10.77.0.0/16,100.65.0.3/32",
		"INFRA_ATTACHMENT_PLATFORM_INGRESS_VIP=100.65.0.3",
		"INFRA_ATTACHMENT_PLATFORM_INGRESS_WORKLOAD_EGRESS=true",
	} {
		if strings.Count(string(body), "\n"+want+"\n") != 1 {
			t.Errorf("want one %q in %s", want, body)
		}
	}
}

func TestResolveManagedServicesInternalNetworkRefusesOverlap(t *testing.T) {
	repo := t.TempDir()
	dir := filepath.Join(repo, "clusters", "fixture")
	if err := os.MkdirAll(dir, 0o755); err != nil {
		t.Fatal(err)
	}
	path := filepath.Join(dir, "cluster-config.env")
	seed := "SERVICES_INTERNAL_GATEWAY_REQUIRED=true\nEXT_NET_CIDR=100.65.0.0/29\nEXT_NET_GATEWAY=100.65.0.1\nEXT_NET_EXCLUDE_IPS=100.65.0.1..100.65.0.2\nMETALLB_FLOATING_IP=100.65.0.3\n"
	if err := os.WriteFile(path, []byte(seed), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := ResolveManagedServicesInternalNetwork(repo, "fixture", []string{"node-a"}, nil); err == nil || !strings.Contains(err.Error(), "overlaps") {
		t.Fatalf("expected overlap refusal, got %v", err)
	}
	got, _ := os.ReadFile(path)
	if string(got) != seed {
		t.Fatal("refusal modified the overlay")
	}
}
