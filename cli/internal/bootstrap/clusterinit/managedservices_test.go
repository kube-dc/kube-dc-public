package clusterinit

import (
	"strings"
	"testing"
)

func TestResolveManagedServicesStorageQualification(t *testing.T) {
	for _, tc := range []struct {
		name, kind, mode, storage       string
		count, size                     int
		wantMode, wantClass, wantBudget string
		wantError                       string
	}{
		{"pvc default", "kube-dc", "auto", string(RookCephPVC), 0, 0, "on", "ceph-block", "50Gi", ""},
		{"pvc custom", "kube-dc", "on", string(RookCephPVC), 4, 240, "on", "ceph-block", "120Gi", ""},
		{"no bucket", "kube-dc", "auto", string(RookDisabled), 0, 0, "off", "", "", ""},
		{"explicit without bucket", "kube-dc", "on", string(RookDisabled), 0, 0, "", "", "", "needs a rook-ceph bucket"},
		{"cloudsigma", "cloudsigma", "auto", string(RookCephPVC), 0, 0, "off", "", "", ""},
		{"cloudsigma explicit", "cloudsigma", "on", string(RookCephPVC), 0, 0, "", "", "", "not qualified"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			got, err := ResolveManagedServices(&InitOptions{InstallationKind: tc.kind, ManagedServicesMode: tc.mode, RookMode: RookMode(tc.storage), CephOSDCount: tc.count, CephOSDVolumeSizeGB: tc.size})
			if tc.wantError != "" {
				if err == nil || !strings.Contains(err.Error(), tc.wantError) {
					t.Fatalf("got %v, want error containing %q", err, tc.wantError)
				}
				return
			}
			if err != nil || got.Mode != tc.wantMode || got.DatabaseClass != tc.wantClass || got.StorageBudget != tc.wantBudget {
				t.Fatalf("got %+v, %v; want mode=%s class=%s budget=%s", got, err, tc.wantMode, tc.wantClass, tc.wantBudget)
			}
		})
	}
}

func TestResolveManagedServicesRejectsInvalidEgressAndClass(t *testing.T) {
	base := InitOptions{InstallationKind: "kube-dc", ManagedServicesMode: "on", RookMode: RookCephPVC}
	for _, tc := range []struct {
		name   string
		mutate func(*InitOptions)
	}{
		{"credentials in URL", func(o *InitOptions) { o.ServicesEgressProbeURLs = []string{"https://user:pass@example.com/"} }},
		{"insecure URL", func(o *InitOptions) { o.ServicesEgressProbeURLs = []string{"http://example.com/"} }},
		{"cloudsigma probe", func(o *InitOptions) { o.ServicesEgressProbeURLs = []string{"https://s3.cloudsigma.com/"} }},
		{"invalid class", func(o *InitOptions) { o.ServicesDatabaseClass = "Bad_Name" }},
	} {
		t.Run(tc.name, func(t *testing.T) {
			o := base
			tc.mutate(&o)
			if _, err := ResolveManagedServices(&o); err == nil {
				t.Fatal("invalid managed-services input was accepted")
			}
		})
	}
}

func TestResolveManagedServicesExplicitClassNeedsLiveExpansionProof(t *testing.T) {
	o := &InitOptions{InstallationKind: "kube-dc", RookMode: RookCephPVC, ServicesDatabaseClass: "fast-db", ManagedServicesMode: "auto"}
	selection, err := ResolveManagedServices(o)
	if err != nil || selection.Mode != "off" {
		t.Fatalf("unverified class should leave auto off: %+v, %v", selection, err)
	}
	o.ManagedServicesMode = "on"
	if _, err := ResolveManagedServices(o); err == nil {
		t.Fatal("explicit on accepted an unverified StorageClass")
	}
	o.ServicesClassExpansionVerified = true
	selection, err = ResolveManagedServices(o)
	if err != nil || selection.Mode != "on" || !selection.NeedsClassExpansionCheck {
		t.Fatalf("verified class should qualify: %+v, %v", selection, err)
	}
}

func TestManagedServicesPlanCarriesReviewedManagerInventory(t *testing.T) {
	o := validBase()
	o.InstallationKind = "kube-dc"
	o.ManagedServicesMode = "on"
	o.RookMode = RookCephPVC
	o.CephStorageClass = "ceph-block"
	for _, tc := range []struct {
		masters string
		want    int
	}{
		{"192.0.2.1", 1},
		{"192.0.2.1,192.0.2.2,192.0.2.3", 3},
	} {
		o.Sets = map[string]string{"KUBE_OVN_MASTER_NODES": tc.masters}
		plan, err := BuildPlan(&o, FleetState{})
		if err != nil {
			t.Fatal(err)
		}
		if plan.ManagerNodes != tc.want {
			t.Fatalf("%s: manager nodes = %d, want %d", tc.masters, plan.ManagerNodes, tc.want)
		}
	}
}

func TestLocalHAStoragePlanIsExplicitHashedAndHostBound(t *testing.T) {
	o := validBase()
	o.InstallationKind = "kube-dc"
	o.ManagedServicesMode = "on"
	o.RookMode = RookCephPVC
	o.CephStorageClass = "ceph-block"
	o.ServicesHAStorageClass = "local-path"
	o.ServicesHAStorageBudget = "128Gi"
	o.Sets = map[string]string{"KUBE_OVN_MASTER_NODES": "192.0.2.1,192.0.2.2,192.0.2.3"}
	plan, err := BuildPlan(&o, FleetState{})
	if err != nil {
		t.Fatal(err)
	}
	if plan.ServicesHAStorageClass != "local-path" || plan.ServicesHAStorageBudget != "128Gi" || plan.ServicesDatabaseClass != "ceph-block" {
		t.Fatalf("lost separate storage roles: %+v", plan)
	}
	hash, err := ComputeInputHash(&o)
	if err != nil {
		t.Fatal(err)
	}
	o.ServicesHAStorageBudget = "256Gi"
	next, _ := ComputeInputHash(&o)
	if hash == next {
		t.Fatal("logical budget missing from reviewed input hash")
	}
	o.Sets["KUBE_OVN_MASTER_NODES"] = "192.0.2.1"
	if _, err := BuildPlan(&o, FleetState{}); err == nil {
		t.Fatal("one-host local HA accepted")
	}
	for _, tc := range []struct{ class, budget, mode string }{{"local-path", "", "on"}, {"", "128Gi", "on"}, {"Bad_Name", "128Gi", "on"}, {"local-path", "0Gi", "on"}, {"ceph-block", "128Gi", "on"}, {"local-path", "128Gi", "off"}} {
		o.ServicesHAStorageClass = tc.class
		o.ServicesHAStorageBudget = tc.budget
		o.ManagedServicesMode = tc.mode
		if _, err := ResolveManagedServices(&o); err == nil {
			t.Fatalf("invalid HA selection accepted: %+v", tc)
		}
	}
}
