package main

import (
	"context"
	"encoding/json"
	"fmt"
	"testing"

	"github.com/shalb/kube-dc/cli/internal/bootstrap/clusterinit"
)

func TestLocalHAClassAndReadyHostValidation(t *testing.T) {
	sc := []byte(`{"provisioner":"rancher.io/local-path","volumeBindingMode":"WaitForFirstConsumer"}`)
	nodes := []map[string]any{}
	for i := 0; i < 3; i++ {
		nodes = append(nodes, map[string]any{"metadata": map[string]any{"uid": fmt.Sprint(i), "labels": map[string]string{"kubernetes.io/hostname": fmt.Sprint(i)}}, "status": map[string]any{"conditions": []map[string]string{{"type": "Ready", "status": "True"}}}})
	}
	raw, _ := json.Marshal(map[string]any{"items": nodes})
	if err := verifyServicesHAStorageObjects(sc, raw); err != nil {
		t.Fatal(err)
	}
	for _, bad := range []string{`{"provisioner":"ebs.csi.aws.com","volumeBindingMode":"WaitForFirstConsumer"}`, `{"provisioner":"rancher.io/local-path","volumeBindingMode":"Immediate"}`} {
		if verifyServicesHAStorageObjects([]byte(bad), raw) == nil {
			t.Fatal("unqualified HA storage accepted")
		}
	}
	for _, fault := range []string{"unschedulable", "tainted", "notReady", "sameHost"} {
		var v map[string]any
		json.Unmarshal(raw, &v)
		items := v["items"].([]any)
		node := items[2].(map[string]any)
		switch fault {
		case "unschedulable":
			node["spec"] = map[string]any{"unschedulable": true}
		case "tainted":
			node["spec"] = map[string]any{"taints": []map[string]string{{"effect": "NoSchedule"}}}
		case "notReady":
			node["status"] = map[string]any{}
		case "sameHost":
			node["metadata"].(map[string]any)["labels"] = map[string]string{"kubernetes.io/hostname": "0"}
		}
		bad, _ := json.Marshal(v)
		if verifyServicesHAStorageObjects(sc, bad) == nil {
			t.Fatalf("%s host counted", fault)
		}
	}
}

func TestGreenfieldLocalHAStorageDefersLivePrerequisites(t *testing.T) {
	o := &clusterinit.InitOptions{Mode: clusterinit.ModeInstall, ServicesHAStorageClass: "local-path"}
	if err := verifyServicesHAStorage(context.Background(), o); err != nil {
		t.Fatal("greenfield pre-CNI/storage installation was blocked", err)
	}
	o.ServicesHAStorageClass = "custom"
	if verifyServicesHAStorage(context.Background(), o) == nil {
		t.Fatal("uninstalled custom class accepted")
	}
}

func TestLocalHAStoragePublicationRequiresLiveClassAndHosts(t *testing.T) {
	ctx := context.Background()
	pins := map[string]string{"SERVICES_STORAGE_ROLES": `{"database":"ceph-block","database-ha":"local-path"}`}
	live := map[string]any{"data": map[string]any{"SERVICES_STORAGE_ROLES": pins["SERVICES_STORAGE_ROLES"]}}
	nodes := []any{}
	for i := 0; i < 3; i++ {
		nodes = append(nodes, map[string]any{"metadata": map[string]any{"uid": fmt.Sprint(i), "labels": map[string]any{"kubernetes.io/hostname": fmt.Sprint(i)}}, "status": map[string]any{"conditions": []any{map[string]any{"type": "Ready", "status": "True"}}}})
	}
	reader := fakeManagedServicesReader{"/storageclass/local-path": {"provisioner": "rancher.io/local-path", "volumeBindingMode": "WaitForFirstConsumer"}, "/nodes/": {"items": nodes}}
	for _, roles := range []string{pins["SERVICES_STORAGE_ROLES"], "{database: ceph-block, database-ha: local-path}"} {
		pins["SERVICES_STORAGE_ROLES"] = roles
		live["data"].(map[string]any)["SERVICES_STORAGE_ROLES"] = roles
		if err := verifyManagedServicesHAStorage(ctx, reader, pins, live); err != nil {
			t.Fatal("valid YAML/JSON role mapping rejected", err)
		}
	}
	if err := verifyManagedServicesHAStorage(ctx, reader, map[string]string{"SERVICES_STORAGE_ROLES": "{database: ceph-block}"}, live); err != nil {
		t.Fatal("existing non-HA YAML roles rejected", err)
	}
	delete(reader, "/storageclass/local-path")
	if verifyManagedServicesHAStorage(ctx, reader, pins, live) == nil {
		t.Fatal("missing class passed publication")
	}
	reader["/storageclass/local-path"] = map[string]any{"provisioner": "rancher.io/local-path", "volumeBindingMode": "WaitForFirstConsumer"}
	reader["/nodes/"]["items"] = nodes[:2]
	if verifyManagedServicesHAStorage(ctx, reader, pins, live) == nil {
		t.Fatal("two-host target passed publication")
	}
}
