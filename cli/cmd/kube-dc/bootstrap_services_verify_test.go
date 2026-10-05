package main

import (
	"context"
	"encoding/base64"
	"fmt"
	"strings"
	"testing"
)

type fakeManagedServicesReader map[string]map[string]any

func (fakeManagedServicesReader) ProbeFamilyInstanceWrites(context.Context) error { return nil }

func (r fakeManagedServicesReader) Get(_ context.Context, namespace, resource, name string) (map[string]any, error) {
	key := namespace + "/" + resource + "/" + name
	if object, ok := r[key]; ok {
		return object, nil
	}
	return nil, fmt.Errorf("object %s is absent", key)
}

func readyManagedRelease(version, digest string) map[string]any {
	return map[string]any{
		"metadata": map[string]any{"generation": 2},
		"spec": map[string]any{"chart": map[string]any{"spec": map[string]any{"version": version}},
			"values": map[string]any{"image": map[string]any{"digest": digest}}},
		"status": map[string]any{"observedGeneration": 2, "conditions": []any{map[string]any{"type": "Ready", "status": "True"}}},
	}
}

func managedServicesPrepublishFixture() (fakeManagedServicesReader, map[string]string) {
	const digest = "sha256:0123456789abcdef0123456789abcdef0123456789abcdef0123456789abcdef"
	pins := map[string]string{"CLUSTER_NAME": "example", "SERVICES_CELL_ID": "cell-example", "SERVICES_CHART_VERSION": "v0.9.1", "SERVICES_HUB_DIGEST": digest}
	objects := fakeManagedServicesReader{
		"flux-system/configmap/cluster-config":          {"data": map[string]any{"CLUSTER_NAME": "example", "SERVICES_CELL_ID": "cell-example"}},
		"kube-dc-services/helmrelease/kube-dc-services": readyManagedRelease("v0.9.1", digest),
		"kube-dc/deployment/kube-dc-manager": {"metadata": map[string]any{"generation": 3},
			"spec": map[string]any{"replicas": 2}, "status": map[string]any{"observedGeneration": 3, "replicas": 2, "availableReplicas": 2, "readyReplicas": 2, "updatedReplicas": 2}},
		"/validatingwebhookconfigurations/": {"items": []any{map[string]any{"webhooks": []any{map[string]any{
			"clientConfig": map[string]any{"service": map[string]any{"namespace": "kube-dc", "name": "kube-dc-manager-webhook", "path": "/validate-family-instance-writes"}},
		}}}}},
		"kube-dc/endpoints/kube-dc-manager-webhook": {"subsets": []any{map[string]any{"addresses": []any{map[string]any{"ip": "10.0.0.1"}}}}},
		"/validatingadmissionpolicies/": {"items": []any{map[string]any{"metadata": map[string]any{"name": "protect-managed-services-in-projects"},
			"spec": map[string]any{"matchConditions": []any{map[string]any{"name": "exclude-cluster-admins", "expression": `!request.userInfo.groups.exists(g, g == 'platform:admin')`}}}}}},
		"flux-system/kustomization/services-catalog": {"spec": map[string]any{"suspend": true}},
		"kube-dc-services/secret/kube-dc-service-cell-signing-key": {"data": map[string]any{
			"cellID": base64.StdEncoding.EncodeToString([]byte("cell-example")),
			"keyID":  base64.StdEncoding.EncodeToString([]byte("ed25519-abcdef")),
			"public": base64.StdEncoding.EncodeToString([]byte("public")),
			"seed":   base64.StdEncoding.EncodeToString([]byte("seed")),
		}},
	}
	for _, operator := range [][2]string{{"valkey-system", "kube-dc-valkey-operator"}, {"strimzi-system", "strimzi"},
		{"mysql-operator", "mysql-operator"}, {"clickhouse-operator", "clickhouse-operator"}} {
		objects[operator[0]+"/helmrelease/"+operator[1]] = readyManagedRelease("v1", "")
	}
	for _, name := range []string{"protect-managed-service-markers-in-projects", "protect-managed-service-marker-introduction-in-projects", "protect-managed-service-pod-templates-in-projects", "protect-managed-services-scale-in-projects"} {
		objects["/validatingadmissionpolicies/"]["items"] = append(objects["/validatingadmissionpolicies/"]["items"].([]any), map[string]any{
			"metadata": map[string]any{"name": name}, "spec": map[string]any{"matchConditions": []any{map[string]any{
				"name": "exclude-cluster-admins", "expression": `!request.userInfo.groups.exists(g, g == 'platform:admin')`,
			}}},
		})
	}
	return objects, pins
}

func TestManagedServicesPrepublishRequiresLiveIdentityAndReadiness(t *testing.T) {
	objects, pins := managedServicesPrepublishFixture()
	result, err := verifyManagedServicesPrepublish(context.Background(), objects, pins)
	if err != nil || !result.CatalogSuspended || result.CellID != "cell-example" || string(result.CellKey["seed"]) != "seed" {
		t.Fatalf("valid prepublish gate: %+v, %v", result, err)
	}
	for _, test := range []struct {
		name   string
		change func(fakeManagedServicesReader)
		want   string
	}{
		{"wrong cluster", func(r fakeManagedServicesReader) {
			r["flux-system/configmap/cluster-config"]["data"].(map[string]any)["CLUSTER_NAME"] = "other"
		}, "cluster identity"},
		{"wrong digest", func(r fakeManagedServicesReader) {
			r["kube-dc-services/helmrelease/kube-dc-services"]["spec"].(map[string]any)["values"].(map[string]any)["image"].(map[string]any)["digest"] = "sha256:other"
		}, "reviewed chart and hub digest"},
		{"stale operator", func(r fakeManagedServicesReader) {
			r["strimzi-system/helmrelease/strimzi"]["status"].(map[string]any)["observedGeneration"] = 1
		}, "strimzi"},
		{"manager unavailable", func(r fakeManagedServicesReader) {
			r["kube-dc/deployment/kube-dc-manager"]["status"].(map[string]any)["availableReplicas"] = 1
		}, "rollout is not complete"},
		{"old manager still terminating", func(r fakeManagedServicesReader) {
			r["kube-dc/deployment/kube-dc-manager"]["status"].(map[string]any)["replicas"] = 3
		}, "rollout is not complete"},
		{"webhook path absent", func(r fakeManagedServicesReader) {
			r["/validatingwebhookconfigurations/"]["items"] = []any{}
		}, "webhook path"},
		{"webhook endpoints absent", func(r fakeManagedServicesReader) {
			r["kube-dc/endpoints/kube-dc-manager-webhook"]["subsets"] = []any{}
		}, "no Ready endpoint"},
		{"admission excludes admin", func(r fakeManagedServicesReader) {
			r["/validatingadmissionpolicies/"]["items"].([]any)[0].(map[string]any)["spec"].(map[string]any)["matchConditions"].([]any)[0].(map[string]any)["expression"] = "true"
		}, "platform:admin"},
		{"policy missing", func(r fakeManagedServicesReader) {
			r["/validatingadmissionpolicies/"]["items"] = r["/validatingadmissionpolicies/"]["items"].([]any)[:4]
		}, "full managed-services admission policy set"},
		{"wrong signing cell", func(r fakeManagedServicesReader) {
			r["kube-dc-services/secret/kube-dc-service-cell-signing-key"]["data"].(map[string]any)["cellID"] = base64.StdEncoding.EncodeToString([]byte("cell-other"))
		}, "another cell"},
	} {
		t.Run(test.name, func(t *testing.T) {
			fixture, pins := managedServicesPrepublishFixture()
			test.change(fixture)
			if _, err := verifyManagedServicesPrepublish(context.Background(), fixture, pins); err == nil || !strings.Contains(err.Error(), test.want) {
				t.Fatalf("gate error = %v, want %q", err, test.want)
			}
		})
	}
}

func TestManagedServicesPublishedRequiresAllPlansAndBundles(t *testing.T) {
	objects := fakeManagedServicesReader{
		"flux-system/kustomization/services-catalog": readyManagedRelease("", ""),
		"flux-system/configmap/cluster-config": {"data": map[string]any{
			"CLUSTER_NAME": "sample", "KUBE_DC_UI_MANAGED_SERVICES_ALL_ORGANIZATIONS": "true"}},
		"/servicedataplane/sample-platform": {"metadata": map[string]any{"generation": 1}, "status": map[string]any{
			"ready": true,
			"conditions": []any{
				map[string]any{"type": "EgressReady", "status": "True", "observedGeneration": 1},
				map[string]any{"type": "BundleReady", "status": "True", "observedGeneration": 1},
				map[string]any{"type": "AcceptingPlacements", "status": "True", "observedGeneration": 1},
			},
		}},
	}
	plane := objects["/servicedataplane/sample-platform"]["status"].(map[string]any)
	for _, family := range greenfieldFamilies {
		plane["bundles"] = append(nestedSlice(objects["/servicedataplane/sample-platform"], "status", "bundles"),
			map[string]any{"family": family, "ready": true})
	}
	var plans []any
	for _, family := range greenfieldFamilies {
		for _, tier := range []string{"development", "production"} {
			plans = append(plans, map[string]any{"metadata": map[string]any{"name": family + "-" + tier, "generation": 1},
				"status": map[string]any{"conditions": []any{map[string]any{"type": "Verified", "status": "True", "observedGeneration": 1}}}})
		}
	}
	for _, name := range legacyServicePlans {
		plans = append(plans, map[string]any{"metadata": map[string]any{"name": name}, "spec": map[string]any{"disabled": true}})
	}
	objects["/managedserviceplans/"] = map[string]any{"items": plans}
	pins := map[string]string{"CLUSTER_NAME": "sample", "SERVICES_DATAPLANE_NAME": "sample-platform"}
	if err := checkManagedServicesPublished(context.Background(), objects, pins); err != nil {
		t.Fatal(err)
	}
	objects["/servicedataplane/sample-platform"]["status"].(map[string]any)["bundles"].([]any)[0].(map[string]any)["ready"] = false
	if err := checkManagedServicesPublished(context.Background(), objects, pins); err == nil || !strings.Contains(err.Error(), "bundle") {
		t.Fatalf("unready bundle accepted: %v", err)
	}
	objects["/servicedataplane/sample-platform"]["status"].(map[string]any)["bundles"].([]any)[0].(map[string]any)["ready"] = true
	plans[0].(map[string]any)["status"].(map[string]any)["conditions"].([]any)[0].(map[string]any)["status"] = "False"
	if err := checkManagedServicesPublished(context.Background(), objects, pins); err == nil || !strings.Contains(err.Error(), "not Verified") {
		t.Fatalf("unverified plan accepted: %v", err)
	}
	condition := plans[0].(map[string]any)["status"].(map[string]any)["conditions"].([]any)[0].(map[string]any)
	condition["status"] = "True"
	condition["observedGeneration"] = 0
	if err := checkManagedServicesPublished(context.Background(), objects, pins); err == nil || !strings.Contains(err.Error(), "not Verified") {
		t.Fatalf("stale plan accepted: %v", err)
	}
	condition["observedGeneration"] = 1
	plans[0].(map[string]any)["spec"] = map[string]any{"disabled": true}
	if err := checkManagedServicesPublished(context.Background(), objects, pins); err == nil || !strings.Contains(err.Error(), "is disabled") {
		t.Fatalf("disabled Development plan accepted: %v", err)
	}
	plans[0].(map[string]any)["spec"] = map[string]any{"disabled": false}
	objects["flux-system/kustomization/services-catalog"]["spec"].(map[string]any)["components"] = []any{"components/development-only"}
	for _, raw := range plans {
		plan := raw.(map[string]any)
		name := nestedString(plan, "metadata", "name")
		if strings.HasSuffix(name, "-production") {
			plan["metadata"].(map[string]any)["annotations"] = map[string]any{"services.kube-dc.com/console": "disabled"}
		}
	}
	if err := checkManagedServicesPublished(context.Background(), objects, pins); err != nil {
		t.Fatalf("development-only catalog refused: %v", err)
	}
}

func TestManagedServicesReleaseVerification(t *testing.T) {
	pins := map[string]string{"CLUSTER_NAME": "sample", "SERVICES_CELL_ID": "cell-sample", "SERVICES_CHART_VERSION": "release", "SERVICES_HUB_DIGEST": "sha256:hub", "SERVICES_RUNNER_IMAGE": "runner@sha256:runner"}
	objects := fakeManagedServicesReader{
		"flux-system/configmap/cluster-config":                     {"data": map[string]any{"CLUSTER_NAME": "sample", "SERVICES_CELL_ID": "cell-sample"}},
		"kube-dc-services/helmrelease/kube-dc-services":            readyManagedRelease("release", "sha256:hub"),
		"kube-dc-service-runner/deployment/kube-dc-service-runner": {"metadata": map[string]any{"generation": 2}, "spec": map[string]any{"replicas": 1, "template": map[string]any{"spec": map[string]any{"containers": []any{map[string]any{"image": "runner@sha256:runner"}}}}}, "status": map[string]any{"observedGeneration": 2, "replicas": 1, "updatedReplicas": 1, "availableReplicas": 1}},
	}
	var classes []any
	for _, name := range append(append([]string{}, greenfieldFamilies...), "valkey-ha") {
		classes = append(classes, map[string]any{"metadata": map[string]any{"name": name, "generation": 2}, "status": map[string]any{"conditions": []any{map[string]any{"type": "Verified", "status": "True", "observedGeneration": 2}}}})
	}
	objects["/managedserviceclasses/"] = map[string]any{"items": classes}
	objects["kube-dc-services/deployment/kube-dc-services-hub"] = map[string]any{
		"metadata": map[string]any{"generation": 2},
		"spec":     map[string]any{"replicas": 1, "template": map[string]any{"spec": map[string]any{"containers": []any{map[string]any{"name": "hub", "image": "hub@sha256:hub"}}}}},
		"status":   map[string]any{"observedGeneration": 2, "replicas": 1, "updatedReplicas": 1, "availableReplicas": 1},
	}
	config := objects["flux-system/configmap/cluster-config"]["data"].(map[string]any)
	for key, value := range pins {
		config[key] = value
	}
	objects["/servicedataplane/"] = map[string]any{}
	if err := verifyManagedServicesRelease(context.Background(), objects, pins); err != nil {
		t.Fatal(err)
	}
	// MariaDB's chart and executable pins are independent. A patched image must
	// match its catalog attestation without accepting stale live Fleet pins.
	pins["MARIADB_OPERATOR_VERSION"] = "26.3.0"
	pins["MARIADB_OPERATOR_IMAGE_REPOSITORY"] = "docker.io/shalb/kube-dc-mariadb-operator"
	pins["MARIADB_OPERATOR_IMAGE_TAG"] = "26.3.0-shared-startup.20261005.6"
	pins["MARIADB_OPERATOR_IMAGE_DIGEST"] = "sha256:patched"
	for key, value := range pins {
		config[key] = value
	}
	mariaBundle := map[string]any{"family": "mariadb", "operatorVersion": pins["MARIADB_OPERATOR_IMAGE_TAG"]}
	objects["/servicedataplane/"]["status"] = map[string]any{"bundles": []any{mariaBundle}}
	if err := verifyManagedServicesRelease(context.Background(), objects, pins); err != nil {
		t.Fatalf("independently pinned MariaDB image refused: %v", err)
	}
	mariaBundle["operatorVersion"] = pins["MARIADB_OPERATOR_VERSION"]
	if err := verifyManagedServicesRelease(context.Background(), objects, pins); err == nil || !strings.Contains(err.Error(), "mariadb operator attestation") {
		t.Fatalf("old MariaDB executable attestation accepted: %v", err)
	}
	delete(pins, "MARIADB_OPERATOR_IMAGE_TAG")
	if err := verifyManagedServicesRelease(context.Background(), objects, pins); err != nil {
		t.Fatalf("default MariaDB image version refused: %v", err)
	}
	pins["MARIADB_OPERATOR_IMAGE_TAG"] = "26.3.0-shared-startup.20261005.6"
	mariaBundle["operatorVersion"] = pins["MARIADB_OPERATOR_IMAGE_TAG"]
	for _, key := range []string{"MARIADB_OPERATOR_VERSION", "MARIADB_OPERATOR_IMAGE_REPOSITORY", "MARIADB_OPERATOR_IMAGE_TAG", "MARIADB_OPERATOR_IMAGE_DIGEST"} {
		config[key] = "stale"
		if err := verifyManagedServicesRelease(context.Background(), objects, pins); err == nil || !strings.Contains(err.Error(), key+" differs from Fleet") {
			t.Fatalf("stale %s accepted: %v", key, err)
		}
		config[key] = pins[key]
	}
	pins["STRIMZI_OPERATOR_CHART_VERSION"] = "1.2.0"
	pins["STRIMZI_OPERATOR_VERSION"] = "1.2.0-kubedc.20261005.1"
	strimziKeys := []string{"STRIMZI_OPERATOR_VERSION"}
	for _, prefix := range []string{"STRIMZI_OPERATOR_IMAGE_", "STRIMZI_TOPIC_OPERATOR_IMAGE_", "STRIMZI_USER_OPERATOR_IMAGE_"} {
		for key, value := range map[string]string{"REGISTRY": "docker.io", "REPOSITORY": "shalb", "NAME": "kube-dc-strimzi-operator", "TAG": "1.2.0-kubedc.20261005.1@sha256:patched"} {
			pins[prefix+key] = value
			strimziKeys = append(strimziKeys, prefix+key)
		}
	}
	for key, value := range pins {
		config[key] = value
	}
	kafkaBundle := map[string]any{"family": "kafka", "operatorVersion": pins["STRIMZI_OPERATOR_VERSION"]}
	objects["/servicedataplane/"]["status"].(map[string]any)["bundles"] = []any{mariaBundle, kafkaBundle}
	if err := verifyManagedServicesRelease(context.Background(), objects, pins); err != nil {
		t.Fatalf("independently pinned Kafka operator refused: %v", err)
	}
	kafkaBundle["operatorVersion"] = pins["STRIMZI_OPERATOR_CHART_VERSION"]
	if err := verifyManagedServicesRelease(context.Background(), objects, pins); err == nil || !strings.Contains(err.Error(), "kafka operator attestation") {
		t.Fatalf("old Kafka executable attestation accepted: %v", err)
	}
	delete(pins, "STRIMZI_OPERATOR_VERSION")
	if err := verifyManagedServicesRelease(context.Background(), objects, pins); err != nil {
		t.Fatalf("default Kafka operator version refused: %v", err)
	}
	pins["STRIMZI_OPERATOR_VERSION"] = "1.2.0-kubedc.20261005.1"
	kafkaBundle["operatorVersion"] = pins["STRIMZI_OPERATOR_VERSION"]
	for _, key := range strimziKeys {
		config[key] = "stale"
		if err := verifyManagedServicesRelease(context.Background(), objects, pins); err == nil || !strings.Contains(err.Error(), key+" differs from Fleet") {
			t.Fatalf("stale %s accepted: %v", key, err)
		}
		config[key] = pins[key]
	}
	config["SERVICES_CELL_ID"] = "foreign-cell"
	if err := verifyManagedServicesRelease(context.Background(), objects, pins); err == nil || !strings.Contains(err.Error(), "identity") {
		t.Fatalf("foreign cell accepted: %v", err)
	}
	config["SERVICES_CELL_ID"] = "cell-sample"
	container := objects["kube-dc-service-runner/deployment/kube-dc-service-runner"]["spec"].(map[string]any)["template"].(map[string]any)["spec"].(map[string]any)["containers"].([]any)[0].(map[string]any)
	container["image"] = "runner@sha256:other"
	if err := verifyManagedServicesRelease(context.Background(), objects, pins); err == nil || !strings.Contains(err.Error(), "runner image") {
		t.Fatalf("wrong runner accepted: %v", err)
	}
	container["image"] = "runner@sha256:runner"
	hubContainer := objects["kube-dc-services/deployment/kube-dc-services-hub"]["spec"].(map[string]any)["template"].(map[string]any)["spec"].(map[string]any)["containers"].([]any)[0].(map[string]any)
	hubContainer["image"] = "hub@sha256:old"
	if err := verifyManagedServicesRelease(context.Background(), objects, pins); err == nil || !strings.Contains(err.Error(), "hub image") {
		t.Fatalf("old hub accepted: %v", err)
	}
	hubContainer["image"] = "hub@sha256:hub"
	pins["CLICKHOUSE_OPERATOR_CHART_VERSION"] = "new"
	config["CLICKHOUSE_OPERATOR_CHART_VERSION"] = "old"
	if err := verifyManagedServicesRelease(context.Background(), objects, pins); err == nil || !strings.Contains(err.Error(), "pin CLICKHOUSE") {
		t.Fatalf("unreconciled Fleet pin accepted: %v", err)
	}
	delete(pins, "CLICKHOUSE_OPERATOR_CHART_VERSION")
	condition := classes[0].(map[string]any)["status"].(map[string]any)["conditions"].([]any)[0].(map[string]any)
	condition["observedGeneration"] = 1
	if err := verifyManagedServicesRelease(context.Background(), objects, pins); err == nil || !strings.Contains(err.Error(), "current generation") {
		t.Fatalf("stale class accepted: %v", err)
	}
	condition["observedGeneration"] = 2
	pins["CLICKHOUSE_OPERATOR_VERSION"] = "0.27.4-patched"
	config["CLICKHOUSE_OPERATOR_VERSION"] = pins["CLICKHOUSE_OPERATOR_VERSION"]
	bundle := map[string]any{"family": "clickhouse", "operatorVersion": "0.27.4"}
	objects["/servicedataplane/"] = map[string]any{"status": map[string]any{"bundles": []any{bundle}}}
	if err := verifyManagedServicesRelease(context.Background(), objects, pins); err == nil || !strings.Contains(err.Error(), "operator attestation") {
		t.Fatalf("chart version accepted as patched runtime attestation: %v", err)
	}
	bundle["operatorVersion"] = pins["CLICKHOUSE_OPERATOR_VERSION"]
	if err := verifyManagedServicesRelease(context.Background(), objects, pins); err != nil {
		t.Fatalf("independent runtime version refused: %v", err)
	}
}

func TestManagedServicesClickHouseRuntimePins(t *testing.T) {
	pins := map[string]string{"CLICKHOUSE_OPERATOR_CHART_VERSION": "0.27.4", "CLICKHOUSE_OPERATOR_IMAGE_TAG": "0.27.4@sha256:operator", "CLICKHOUSE_METRICS_IMAGE_TAG": "0.27.4@sha256:metrics"}
	release := readyManagedRelease("0.27.4", "")
	release["spec"].(map[string]any)["values"] = map[string]any{"operator": map[string]any{"image": map[string]any{"tag": pins["CLICKHOUSE_OPERATOR_IMAGE_TAG"]}}, "metrics": map[string]any{"image": map[string]any{"tag": pins["CLICKHOUSE_METRICS_IMAGE_TAG"]}}}
	operator := map[string]any{"name": "altinity-clickhouse-operator", "image": "altinity/clickhouse-operator:" + pins["CLICKHOUSE_OPERATOR_IMAGE_TAG"]}
	metrics := map[string]any{"name": "metrics-exporter", "image": "altinity/metrics-exporter:" + pins["CLICKHOUSE_METRICS_IMAGE_TAG"]}
	status := map[string]any{"observedGeneration": 2, "replicas": 1, "updatedReplicas": 1, "availableReplicas": 1}
	deployment := map[string]any{"metadata": map[string]any{"generation": 2}, "spec": map[string]any{"replicas": 1, "template": map[string]any{"spec": map[string]any{"containers": []any{operator, metrics}}}}, "status": status}
	objects := fakeManagedServicesReader{"clickhouse-operator/helmrelease/clickhouse-operator": release, "clickhouse-operator/deployment/clickhouse-operator-altinity-clickhouse-operator": deployment}
	if err := verifyManagedServicesClickHouse(context.Background(), objects, pins); err != nil {
		t.Fatal(err)
	}
	status["updatedReplicas"] = 0
	if err := verifyManagedServicesClickHouse(context.Background(), objects, pins); err == nil || !strings.Contains(err.Error(), "rollout") {
		t.Fatalf("incomplete upgrade accepted: %v", err)
	}
	status["updatedReplicas"] = 1
	operator["image"] = "altinity/clickhouse-operator:0.27.4@sha256:wrong"
	if err := verifyManagedServicesClickHouse(context.Background(), objects, pins); err == nil || !strings.Contains(err.Error(), "runtime image") {
		t.Fatalf("same tag wrong operator digest accepted: %v", err)
	}
	operator["image"] = "altinity/clickhouse-operator:" + pins["CLICKHOUSE_OPERATOR_IMAGE_TAG"]
	pins["CLICKHOUSE_OPERATOR_IMAGE_REPOSITORY"] = "shalb/kube-dc-clickhouse-operator"
	operator["image"] = pins["CLICKHOUSE_OPERATOR_IMAGE_REPOSITORY"] + ":" + pins["CLICKHOUSE_OPERATOR_IMAGE_TAG"]
	operatorValues := release["spec"].(map[string]any)["values"].(map[string]any)["operator"].(map[string]any)["image"].(map[string]any)
	operatorValues["repository"] = pins["CLICKHOUSE_OPERATOR_IMAGE_REPOSITORY"]
	if err := verifyManagedServicesClickHouse(context.Background(), objects, pins); err != nil {
		t.Fatalf("pinned fork repository refused: %v", err)
	}
	operatorValues["repository"] = "foreign.example/operator"
	if err := verifyManagedServicesClickHouse(context.Background(), objects, pins); err == nil || !strings.Contains(err.Error(), "repository") {
		t.Fatalf("wrong Helm repository accepted: %v", err)
	}
	operatorValues["repository"] = pins["CLICKHOUSE_OPERATOR_IMAGE_REPOSITORY"]
	operator["image"] = "altinity/clickhouse-operator:" + pins["CLICKHOUSE_OPERATOR_IMAGE_TAG"]
	if err := verifyManagedServicesClickHouse(context.Background(), objects, pins); err == nil || !strings.Contains(err.Error(), "runtime image") {
		t.Fatalf("wrong runtime repository accepted: %v", err)
	}
	operator["image"] = pins["CLICKHOUSE_OPERATOR_IMAGE_REPOSITORY"] + ":" + pins["CLICKHOUSE_OPERATOR_IMAGE_TAG"]
	metrics["image"] = "altinity/metrics-exporter:0.27.3@sha256:old"
	if err := verifyManagedServicesClickHouse(context.Background(), objects, pins); err == nil || !strings.Contains(err.Error(), "metrics-exporter") {
		t.Fatalf("old metrics image accepted: %v", err)
	}
}
