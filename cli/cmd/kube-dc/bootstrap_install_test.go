package main

import (
	"bytes"
	"strings"
	"testing"
)

func TestResolveInstallCIDRs_PresetDefaults(t *testing.T) {
	pod, svc, dns, err := resolveInstallCIDRs("internal-only", nil)
	if err != nil {
		t.Fatalf("internal-only: %v", err)
	}
	// The shared preset defaults (also what init writes).
	if pod != "10.100.0.0/16" || svc != "10.101.0.0/16" || dns != "10.101.0.11" {
		t.Errorf("preset defaults = %s/%s/%s", pod, svc, dns)
	}
	// cloud+public-vlan must resolve CIDRs WITHOUT demanding its
	// network-required keys (EXT_NET_*) that RKE2 doesn't care about.
	if _, _, _, err := resolveInstallCIDRs("cloud+public-vlan", nil); err != nil {
		t.Errorf("cloud+public-vlan should resolve CIDRs without EXT_NET_* sets: %v", err)
	}
}

func TestResolveInstallCIDRs_SetOverrides(t *testing.T) {
	pod, svc, dns, err := resolveInstallCIDRs("internal-only", []string{
		"POD_CIDR=10.200.0.0/16", "SVC_CIDR=10.201.0.0/16", "CLUSTER_DNS=10.201.0.11",
	})
	if err != nil {
		t.Fatal(err)
	}
	if pod != "10.200.0.0/16" || svc != "10.201.0.0/16" || dns != "10.201.0.11" {
		t.Errorf("--set overrides not applied: %s/%s/%s", pod, svc, dns)
	}
	// SVC_CIDR is the canonical service-CIDR key (matches bootstrap init).
	_, svc2, _, err := resolveInstallCIDRs("internal-only", []string{"SVC_CIDR=10.202.0.0/16"})
	if err != nil || svc2 != "10.202.0.0/16" {
		t.Errorf("SVC_CIDR: got %q err %v", svc2, err)
	}
}

func TestResolveInstallCIDRs_RejectsServiceCIDRAlias(t *testing.T) {
	// P1 drift guard: SERVICE_CIDR must be rejected — init only honors
	// SVC_CIDR, so accepting SERVICE_CIDR here would drift RKE2 from the
	// fleet's service CIDR.
	_, _, _, err := resolveInstallCIDRs("internal-only", []string{"SERVICE_CIDR=10.9.0.0/16"})
	if err == nil {
		t.Fatal("SERVICE_CIDR must be rejected")
	}
	if !strings.Contains(err.Error(), "SVC_CIDR") {
		t.Errorf("error should steer to SVC_CIDR: %v", err)
	}
}

func TestResolveInstallCIDRs_Errors(t *testing.T) {
	if _, _, _, err := resolveInstallCIDRs("nope", nil); err == nil {
		t.Error("unknown preset must error")
	}
	if _, _, _, err := resolveInstallCIDRs("internal-only", []string{"POD_CIDR"}); err == nil {
		t.Error("malformed --set must error")
	}
	// Invalid CIDR / DNS must fail before anything is written to a node.
	if _, _, _, err := resolveInstallCIDRs("internal-only", []string{"POD_CIDR=not-a-cidr"}); err == nil {
		t.Error("invalid POD_CIDR must error")
	}
	if _, _, _, err := resolveInstallCIDRs("internal-only", []string{"CLUSTER_DNS=999.999.0.1"}); err == nil {
		t.Error("invalid CLUSTER_DNS must error")
	}
}

func TestBootstrapInstall_RejectsBadDomainAndName(t *testing.T) {
	// Field validation must fire before any SSH.
	cases := [][]string{
		{"--ssh-host", "root@192.0.2.10", "--domain", "not a domain", "--name", "dc1"},
		{"--ssh-host", "root@192.0.2.10", "--domain", "example.com", "--name", "Bad_Name"},
		{"--ssh-host", "root@192.0.2.10", "--domain", "example.com", "--name", "dc1", "--node-ip", "999.1.1.1"},
	}
	for _, args := range cases {
		var buf bytes.Buffer
		repo := ""
		cmd := bootstrapInstallCmd(&repo)
		cmd.SetOut(&buf)
		cmd.SetErr(&buf)
		cmd.SetArgs(args)
		if err := cmd.Execute(); err == nil {
			t.Errorf("args %v should have failed validation", args)
		}
	}
}

func TestBootstrapInstall_RequiredFlags(t *testing.T) {
	// Missing --ssh-host / --domain / --name each fail before any SSH.
	for _, args := range [][]string{
		{"--domain", "example.com", "--name", "dc1"},                 // no ssh-host
		{"--ssh-host", "root@192.0.2.10", "--name", "dc1"},           // no domain
		{"--ssh-host", "root@192.0.2.10", "--domain", "example.com"}, // no name
	} {
		var buf bytes.Buffer
		repo := ""
		cmd := bootstrapInstallCmd(&repo)
		cmd.SetOut(&buf)
		cmd.SetErr(&buf)
		cmd.SetArgs(args)
		if err := cmd.Execute(); err == nil {
			t.Errorf("args %v should have failed on a required flag", args)
		} else if !strings.Contains(err.Error(), "required") {
			t.Errorf("args %v: want a 'required' error, got %v", args, err)
		}
	}
}
