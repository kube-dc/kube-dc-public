package main

import (
	"bytes"
	"context"
	"encoding/base64"
	"encoding/json"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
	"time"

	"github.com/shalb/kube-dc/cli/internal/bootstrap/clusterinit"
)

// managedServicesReader keeps the pre-publication gate independent of the
// kubectl transport. Every read is against one explicitly named kubeconfig.
type managedServicesReader interface {
	Get(context.Context, string, string, string) (map[string]any, error)
	ProbeFamilyInstanceWrites(context.Context) error
}

type managedServicesKubectl struct {
	kubeconfig  string
	contextName string
}

func pinnedManagedServicesKubectl(ctx context.Context, kubeconfig string) (managedServicesKubectl, error) {
	callCtx, cancel := context.WithTimeout(ctx, 10*time.Second)
	defer cancel()
	cmd := exec.CommandContext(callCtx, "kubectl", "--kubeconfig", kubeconfig, "config", "current-context")
	output, err := cmd.Output()
	if err != nil {
		return managedServicesKubectl{}, fmt.Errorf("resolve managed-services kubeconfig context: %w", err)
	}
	name := strings.TrimSpace(string(output))
	if name == "" {
		return managedServicesKubectl{}, fmt.Errorf("managed-services kubeconfig has no current context")
	}
	return managedServicesKubectl{kubeconfig: kubeconfig, contextName: name}, nil
}

func (k managedServicesKubectl) kubectlArgs() []string {
	args := []string{"--kubeconfig", k.kubeconfig}
	if k.contextName != "" {
		args = append(args, "--context", k.contextName)
	}
	return args
}

func (k managedServicesKubectl) Get(ctx context.Context, namespace, resource, name string) (map[string]any, error) {
	callCtx, cancel := context.WithTimeout(ctx, 15*time.Second)
	defer cancel()
	args := append(k.kubectlArgs(), "--request-timeout=10s")
	if namespace != "" {
		args = append(args, "-n", namespace)
	}
	args = append(args, "get", resource)
	if name != "" {
		args = append(args, name)
	}
	args = append(args, "-o", "json")
	cmd := exec.CommandContext(callCtx, "kubectl", args...)
	var stderr bytes.Buffer
	cmd.Stderr = &stderr
	output, err := cmd.Output()
	if err != nil {
		return nil, fmt.Errorf("read %s/%s in %s: %w: %s", resource, name, namespace, err, strings.TrimSpace(stderr.String()))
	}
	var object map[string]any
	if err := json.Unmarshal(output, &object); err != nil {
		return nil, fmt.Errorf("decode %s/%s: %w", resource, name, err)
	}
	return object, nil
}

func (k managedServicesKubectl) ProbeFamilyInstanceWrites(ctx context.Context) error {
	callCtx, cancel := context.WithTimeout(ctx, 15*time.Second)
	defer cancel()
	args := append(k.kubectlArgs(), "--request-timeout=10s", "get", "--raw",
		"/api/v1/namespaces/kube-dc/services/https:kube-dc-manager-webhook:443/proxy/validate-family-instance-writes")
	cmd := exec.CommandContext(callCtx, "kubectl", args...)
	output, err := cmd.Output()
	if err != nil {
		return fmt.Errorf("managed-instance webhook handler is not reachable: %w", err)
	}
	var result map[string]any
	if err := json.Unmarshal(output, &result); err != nil {
		return fmt.Errorf("managed-instance webhook returned an invalid response: %w", err)
	}
	if nestedInt(result, "response", "status", "code") != 400 ||
		!strings.Contains(nestedString(result, "response", "status", "message"), "request body is empty") {
		return fmt.Errorf("managed-instance webhook did not answer with the installed handler")
	}
	return nil
}

type managedServicesPrepublish struct {
	CellKey          map[string][]byte
	CellID           string
	CatalogSuspended bool
}

func managedServicesOverlayPins(repo, cluster string) (map[string]string, error) {
	if !clusterinit.ValidManagedServicesClusterPath(cluster) {
		return nil, fmt.Errorf("invalid managed-services Fleet cluster path")
	}
	file := filepath.Join(repo, "clusters", cluster, "cluster-config.env")
	content, err := os.ReadFile(file)
	if err != nil {
		return nil, fmt.Errorf("read services overlay pins: %w", err)
	}
	pins := make(map[string]string)
	for _, line := range strings.Split(string(content), "\n") {
		line = strings.TrimSpace(line)
		if line == "" || strings.HasPrefix(line, "#") {
			continue
		}
		key, value, ok := strings.Cut(line, "=")
		if !ok || key == "" {
			return nil, fmt.Errorf("malformed services overlay pin")
		}
		if _, duplicate := pins[key]; duplicate {
			return nil, fmt.Errorf("duplicate services overlay pin %s", key)
		}
		pins[key] = value
	}
	for _, key := range []string{"CLUSTER_NAME", "SERVICES_CELL_ID", "SERVICES_DATAPLANE_NAME", "SERVICES_CHART_VERSION", "SERVICES_HUB_DIGEST"} {
		if pins[key] == "" {
			return nil, fmt.Errorf("services overlay has no %s", key)
		}
	}
	if pins["CLUSTER_NAME"] != cluster || !strings.HasPrefix(pins["SERVICES_HUB_DIGEST"], "sha256:") {
		return nil, fmt.Errorf("services overlay identity or hub digest is invalid")
	}
	return pins, nil
}

// verifyManagedServicesPrepublish is the read-only gate before the first
// catalog commit. It verifies the target cluster identity, the running
// release and operators, admission identity, and the hub's signing key.
func verifyManagedServicesPrepublish(ctx context.Context, reader managedServicesReader, pins map[string]string) (managedServicesPrepublish, error) {
	var result managedServicesPrepublish
	config, err := reader.Get(ctx, "flux-system", "configmap", "cluster-config")
	if err != nil {
		return result, err
	}
	if nestedString(config, "data", "CLUSTER_NAME") != pins["CLUSTER_NAME"] ||
		nestedString(config, "data", "SERVICES_CELL_ID") != pins["SERVICES_CELL_ID"] {
		return result, fmt.Errorf("live cluster identity differs from the reviewed services overlay")
	}
	service, err := reader.Get(ctx, "kube-dc-services", "helmrelease", "kube-dc-services")
	if err != nil {
		return result, err
	}
	if !resourceReady(service) || nestedString(service, "spec", "chart", "spec", "version") != pins["SERVICES_CHART_VERSION"] ||
		nestedString(service, "spec", "values", "image", "digest") != pins["SERVICES_HUB_DIGEST"] {
		return result, fmt.Errorf("managed-services HelmRelease is not Ready on the reviewed chart and hub digest")
	}
	for _, operator := range [][2]string{{"valkey-system", "kube-dc-valkey-operator"}, {"strimzi-system", "strimzi"},
		{"mysql-operator", "mysql-operator"}, {"clickhouse-operator", "clickhouse-operator"}} {
		release, err := reader.Get(ctx, operator[0], "helmrelease", operator[1])
		if err != nil {
			return result, err
		}
		if !resourceReady(release) {
			return result, fmt.Errorf("operator HelmRelease %s/%s is not Ready", operator[0], operator[1])
		}
	}
	manager, err := reader.Get(ctx, "kube-dc", "deployment", "kube-dc-manager")
	if err != nil {
		return result, err
	}
	replicas := max(1, nestedInt(manager, "spec", "replicas"))
	if nestedInt(manager, "status", "observedGeneration") < nestedInt(manager, "metadata", "generation") ||
		nestedInt(manager, "status", "replicas") != replicas ||
		nestedInt(manager, "status", "availableReplicas") < replicas ||
		nestedInt(manager, "status", "readyReplicas") < replicas ||
		nestedInt(manager, "status", "updatedReplicas") < replicas {
		return result, fmt.Errorf("kube-dc-manager rollout is not complete")
	}
	webhookConfigs, err := reader.Get(ctx, "", "validatingwebhookconfigurations", "")
	if err != nil {
		return result, err
	}
	servesInstanceWrites := false
	for _, item := range nestedSlice(webhookConfigs, "items") {
		configuration, _ := item.(map[string]any)
		for _, raw := range nestedSlice(configuration, "webhooks") {
			webhook, _ := raw.(map[string]any)
			if nestedString(webhook, "clientConfig", "service", "namespace") == "kube-dc" &&
				nestedString(webhook, "clientConfig", "service", "name") == "kube-dc-manager-webhook" &&
				nestedString(webhook, "clientConfig", "service", "path") == "/validate-family-instance-writes" {
				servesInstanceWrites = true
			}
		}
	}
	if !servesInstanceWrites {
		return result, fmt.Errorf("manager does not publish the managed-instance write webhook path")
	}
	webhookEndpoints, err := reader.Get(ctx, "kube-dc", "endpoints", "kube-dc-manager-webhook")
	if err != nil {
		return result, err
	}
	availableEndpoint := false
	for _, raw := range nestedSlice(webhookEndpoints, "subsets") {
		subset, _ := raw.(map[string]any)
		availableEndpoint = availableEndpoint || len(nestedSlice(subset, "addresses")) > 0
	}
	if !availableEndpoint {
		return result, fmt.Errorf("manager write webhook has no Ready endpoint")
	}
	if err := reader.ProbeFamilyInstanceWrites(ctx); err != nil {
		return result, err
	}
	policies, err := reader.Get(ctx, "", "validatingadmissionpolicies", "")
	if err != nil {
		return result, err
	}
	expectedPolicies := map[string]bool{
		"protect-managed-services-in-projects":                    true,
		"protect-managed-service-markers-in-projects":             true,
		"protect-managed-service-marker-introduction-in-projects": true,
		"protect-managed-service-pod-templates-in-projects":       true,
		"protect-managed-services-scale-in-projects":              true,
	}
	for _, raw := range nestedSlice(policies, "items") {
		policy, ok := raw.(map[string]any)
		name := nestedString(policy, "metadata", "name")
		if !ok || !expectedPolicies[name] {
			continue
		}
		matched := false
		for _, condition := range nestedSlice(policy, "spec", "matchConditions") {
			entry, _ := condition.(map[string]any)
			if nestedString(entry, "name") == "exclude-cluster-admins" && strings.Contains(nestedString(entry, "expression"), "platform:admin") {
				matched = true
			}
		}
		if !matched {
			return result, fmt.Errorf("managed-services admission policy %s does not admit platform:admin", nestedString(policy, "metadata", "name"))
		}
		delete(expectedPolicies, name)
	}
	if len(expectedPolicies) != 0 {
		return result, fmt.Errorf("the full managed-services admission policy set is not live")
	}
	catalog, err := reader.Get(ctx, "flux-system", "kustomization", "services-catalog")
	if err != nil {
		return result, err
	}
	result.CatalogSuspended = nestedBool(catalog, "spec", "suspend")
	secret, err := reader.Get(ctx, "kube-dc-services", "secret", "kube-dc-service-cell-signing-key")
	if err != nil {
		return result, err
	}
	result.CellID = pins["SERVICES_CELL_ID"]
	result.CellKey = make(map[string][]byte, 4)
	for _, key := range []string{"seed", "public", "keyID", "cellID"} {
		encoded := nestedString(secret, "data", key)
		value, err := base64.StdEncoding.DecodeString(encoded)
		if err != nil || len(value) == 0 {
			return managedServicesPrepublish{}, fmt.Errorf("managed-services signing Secret has invalid %s data", key)
		}
		result.CellKey[key] = value
	}
	if string(result.CellKey["cellID"]) != result.CellID {
		return managedServicesPrepublish{}, fmt.Errorf("managed-services signing Secret belongs to another cell")
	}
	return result, nil
}

func waitManagedServicesPrepublish(ctx context.Context, reader managedServicesReader, pins map[string]string) (managedServicesPrepublish, error) {
	// Identity errors are not startup delays: fail before entering the wait.
	config, err := reader.Get(ctx, "flux-system", "configmap", "cluster-config")
	if err != nil {
		return managedServicesPrepublish{}, err
	}
	if nestedString(config, "data", "CLUSTER_NAME") != pins["CLUSTER_NAME"] || nestedString(config, "data", "SERVICES_CELL_ID") != pins["SERVICES_CELL_ID"] {
		return managedServicesPrepublish{}, fmt.Errorf("live cluster identity differs from the reviewed services overlay")
	}
	deadline, cancel := context.WithTimeout(ctx, 15*time.Minute)
	defer cancel()
	var last error
	for {
		ready, err := verifyManagedServicesPrepublish(deadline, reader, pins)
		if err == nil {
			return ready, nil
		}
		last = err
		if stallReader, ok := reader.(managedServicesStallReader); ok {
			if reset, resetErr := resetStalledManagedServicesReleases(deadline, stallReader, true); resetErr != nil {
				return managedServicesPrepublish{}, resetErr
			} else if reset {
				last = fmt.Errorf("%w; first-install HelmRelease reset requested", err)
			}
		}
		select {
		case <-deadline.Done():
			return managedServicesPrepublish{}, fmt.Errorf("managed-services prepublication did not converge: %w", last)
		case <-time.After(5 * time.Second):
		}
	}
}

func nestedValue(object map[string]any, keys ...string) any {
	var value any = object
	for _, key := range keys {
		mapping, ok := value.(map[string]any)
		if !ok {
			return nil
		}
		value = mapping[key]
	}
	return value
}

func nestedString(object map[string]any, keys ...string) string {
	value, _ := nestedValue(object, keys...).(string)
	return value
}

func nestedInt(object map[string]any, keys ...string) int {
	switch value := nestedValue(object, keys...).(type) {
	case float64:
		return int(value)
	case int:
		return value
	case json.Number:
		count, _ := strconv.Atoi(string(value))
		return count
	default:
		return 0
	}
}

func nestedBool(object map[string]any, keys ...string) bool {
	value, _ := nestedValue(object, keys...).(bool)
	return value
}

func nestedSlice(object map[string]any, keys ...string) []any {
	value, _ := nestedValue(object, keys...).([]any)
	return value
}

func resourceReady(object map[string]any) bool {
	if nestedInt(object, "status", "observedGeneration") < nestedInt(object, "metadata", "generation") {
		return false
	}
	for _, raw := range nestedSlice(object, "status", "conditions") {
		condition, ok := raw.(map[string]any)
		if ok && nestedString(condition, "type") == "Ready" && nestedString(condition, "status") == "True" {
			return true
		}
	}
	return false
}

var greenfieldFamilies = []string{"postgresql", "valkey", "kafka", "mysql", "mariadb", "clickhouse"}

var legacyServicePlans = []string{
	"postgresql-platform-ha", "postgresql-shared", "valkey-basic", "valkey-durable",
	"valkey-ha", "valkey-ha-durable", "kafka-dev", "kafka-tenant-ha", "kafka-legacy",
}

// checkManagedServicesPublished is the post-push acceptance gate. It checks
// the actual API objects, including plans hidden from the console on a
// single-node development installation.
func checkManagedServicesPublished(ctx context.Context, reader managedServicesReader, pins map[string]string) error {
	return checkManagedServicesCatalog(ctx, reader, pins, true)
}

func verifyManagedServicesRelease(ctx context.Context, reader managedServicesReader, pins map[string]string) error {
	config, err := reader.Get(ctx, "flux-system", "configmap", "cluster-config")
	if err != nil {
		return err
	}
	if nestedString(config, "data", "CLUSTER_NAME") != pins["CLUSTER_NAME"] || nestedString(config, "data", "SERVICES_CELL_ID") != pins["SERVICES_CELL_ID"] {
		return fmt.Errorf("live cluster identity differs from the Fleet overlay")
	}
	for _, key := range []string{"SERVICES_CHART_VERSION", "SERVICES_HUB_DIGEST", "SERVICES_RUNNER_IMAGE", "SERVICES_PG_OPERATOR_VERSION", "MYSQL_OPERATOR_CHART_VERSION", "MYSQL_OPERATOR_VERSION", "MYSQL_OPERATOR_IMAGE_DIGEST", "MARIADB_OPERATOR_VERSION", "STRIMZI_OPERATOR_CHART_VERSION", "VALKEY_OPERATOR_CHART_VERSION", "CLICKHOUSE_OPERATOR_CHART_VERSION", "CLICKHOUSE_OPERATOR_IMAGE_TAG", "CLICKHOUSE_METRICS_IMAGE_TAG"} {
		if pins[key] != "" && nestedString(config, "data", key) != pins[key] {
			return fmt.Errorf("live managed-services pin %s differs from Fleet; wait for the target overlay to reconcile", key)
		}
	}
	release, err := reader.Get(ctx, "kube-dc-services", "helmrelease", "kube-dc-services")
	if err != nil {
		return err
	}
	if !resourceReady(release) || nestedString(release, "spec", "chart", "spec", "version") != pins["SERVICES_CHART_VERSION"] || nestedString(release, "spec", "values", "image", "digest") != pins["SERVICES_HUB_DIGEST"] {
		return fmt.Errorf("managed-services HelmRelease does not match the Ready Fleet chart and hub digest")
	}
	hub, err := reader.Get(ctx, "kube-dc-services", "deployment", "kube-dc-services-hub")
	if err != nil {
		return err
	}
	if !managedServicesDeploymentRolledOut(hub) {
		return fmt.Errorf("managed-services hub has not completed its pinned rollout")
	}
	hubMatched := false
	for _, raw := range nestedSlice(hub, "spec", "template", "spec", "containers") {
		container, _ := raw.(map[string]any)
		if nestedString(container, "name") == "hub" && strings.HasSuffix(nestedString(container, "image"), "@"+pins["SERVICES_HUB_DIGEST"]) {
			hubMatched = true
		}
	}
	if !hubMatched {
		return fmt.Errorf("managed-services hub image differs from the Fleet digest")
	}
	deployment, err := reader.Get(ctx, "kube-dc-service-runner", "deployment", "kube-dc-service-runner")
	if err != nil {
		return err
	}
	if pins["SERVICES_RUNNER_IMAGE"] == "" || !managedServicesDeploymentRolledOut(deployment) {
		return fmt.Errorf("managed-services runner has not completed its pinned rollout")
	}
	matched := false
	for _, raw := range nestedSlice(deployment, "spec", "template", "spec", "containers") {
		container, _ := raw.(map[string]any)
		if nestedString(container, "image") == pins["SERVICES_RUNNER_IMAGE"] {
			matched = true
		}
	}
	if !matched {
		return fmt.Errorf("managed-services runner image differs from the Fleet pin")
	}
	classes, err := reader.Get(ctx, "", "managedserviceclasses", "")
	if err != nil {
		return err
	}
	byName := make(map[string]map[string]any)
	for _, raw := range nestedSlice(classes, "items") {
		class, _ := raw.(map[string]any)
		byName[nestedString(class, "metadata", "name")] = class
	}
	for _, name := range append(append([]string{}, greenfieldFamilies...), "valkey-ha") {
		if !conditionCurrentTrue(byName[name], "Verified") {
			return fmt.Errorf("managed-services class %s is not Verified at its current generation", name)
		}
	}
	plane, err := reader.Get(ctx, "", "servicedataplane", pins["SERVICES_DATAPLANE_NAME"])
	if err != nil {
		return err
	}
	expectedOperators := map[string]string{"postgresql": pins["SERVICES_PG_OPERATOR_VERSION"], "mysql": pins["MYSQL_OPERATOR_VERSION"], "mariadb": pins["MARIADB_OPERATOR_VERSION"], "kafka": pins["STRIMZI_OPERATOR_CHART_VERSION"], "clickhouse": pins["CLICKHOUSE_OPERATOR_CHART_VERSION"]}
	for _, raw := range nestedSlice(plane, "status", "bundles") {
		bundle, _ := raw.(map[string]any)
		family := nestedString(bundle, "family")
		if expectedOperators[family] != "" && nestedString(bundle, "operatorVersion") != expectedOperators[family] {
			return fmt.Errorf("managed-services %s operator attestation differs from Fleet", family)
		}
	}
	if pins["CLICKHOUSE_OPERATOR_CHART_VERSION"] != "" {
		if err := verifyManagedServicesClickHouse(ctx, reader, pins); err != nil {
			return err
		}
	}
	return nil
}

func verifyManagedServicesClickHouse(ctx context.Context, reader managedServicesReader, pins map[string]string) error {
	release, err := reader.Get(ctx, "clickhouse-operator", "helmrelease", "clickhouse-operator")
	if err != nil {
		return err
	}
	if !resourceReady(release) || nestedString(release, "spec", "chart", "spec", "version") != pins["CLICKHOUSE_OPERATOR_CHART_VERSION"] {
		return fmt.Errorf("ClickHouse operator HelmRelease has not completed the Fleet chart upgrade")
	}
	deployment, err := reader.Get(ctx, "clickhouse-operator", "deployment", "clickhouse-operator-altinity-clickhouse-operator")
	if err != nil {
		return err
	}
	if !managedServicesDeploymentRolledOut(deployment) {
		return fmt.Errorf("ClickHouse operator has not completed its rollout")
	}
	expected := map[string]string{}
	for _, image := range []struct{ container, key, section, repository, fallback string }{
		{"altinity-clickhouse-operator", "CLICKHOUSE_OPERATOR_IMAGE_TAG", "operator", "altinity/clickhouse-operator", "0.27.3@sha256:251c4fda281854c3adba6710098c637d076361282bf6ea4fc6a402dd93ef0e08"},
		{"metrics-exporter", "CLICKHOUSE_METRICS_IMAGE_TAG", "metrics", "altinity/metrics-exporter", "0.27.3@sha256:6831ce244b81b9639740941a0fe8f0cfc534c4860b22ff0eb668307b6c1a3116"},
	} {
		tag := pins[image.key]
		if tag == "" && pins["CLICKHOUSE_OPERATOR_CHART_VERSION"] == "0.27.3" {
			tag = image.fallback
		}
		if tag == "" || nestedString(release, "spec", "values", image.section, "image", "tag") != tag {
			return fmt.Errorf("ClickHouse %s image tag/digest does not match Fleet", image.section)
		}
		expected[image.container] = image.repository + ":" + tag
	}
	for _, raw := range nestedSlice(deployment, "spec", "template", "spec", "containers") {
		container, _ := raw.(map[string]any)
		name := nestedString(container, "name")
		if want, ok := expected[name]; ok {
			if nestedString(container, "image") != want {
				return fmt.Errorf("ClickHouse %s runtime image does not match the Fleet tag/digest", name)
			}
			delete(expected, name)
		}
	}
	if len(expected) != 0 {
		return fmt.Errorf("ClickHouse operator or metrics container is absent")
	}
	return nil
}

func managedServicesDeploymentRolledOut(deployment map[string]any) bool {
	replicas := max(1, nestedInt(deployment, "spec", "replicas"))
	return nestedInt(deployment, "status", "observedGeneration") >= nestedInt(deployment, "metadata", "generation") && nestedInt(deployment, "status", "replicas") == replicas && nestedInt(deployment, "status", "updatedReplicas") == replicas && nestedInt(deployment, "status", "availableReplicas") >= replicas
}

func checkManagedServicesCatalog(ctx context.Context, reader managedServicesReader, pins map[string]string, greenfield bool) error {
	catalog, err := reader.Get(ctx, "flux-system", "kustomization", "services-catalog")
	if err != nil {
		return err
	}
	if nestedBool(catalog, "spec", "suspend") || !resourceReady(catalog) {
		return fmt.Errorf("managed-services catalog is not published and Ready")
	}
	config, err := reader.Get(ctx, "flux-system", "configmap", "cluster-config")
	if err != nil {
		return err
	}
	if nestedString(config, "data", "CLUSTER_NAME") != pins["CLUSTER_NAME"] ||
		nestedString(config, "data", "KUBE_DC_UI_MANAGED_SERVICES_ALL_ORGANIZATIONS") != "true" {
		return fmt.Errorf("managed-services console is not enabled on the expected cluster")
	}
	plane, err := reader.Get(ctx, "", "servicedataplane", pins["SERVICES_DATAPLANE_NAME"])
	if err != nil {
		return err
	}
	if !nestedBool(plane, "status", "ready") || !conditionCurrentTrue(plane, "EgressReady") ||
		!conditionCurrentTrue(plane, "BundleReady") || !conditionCurrentTrue(plane, "AcceptingPlacements") {
		return fmt.Errorf("managed-services data plane is not Ready with egress")
	}
	bundles := make(map[string]bool)
	for _, raw := range nestedSlice(plane, "status", "bundles") {
		bundle, ok := raw.(map[string]any)
		if ok {
			bundles[nestedString(bundle, "family")] = nestedBool(bundle, "ready")
		}
	}
	for _, family := range greenfieldFamilies {
		if !bundles[family] {
			return fmt.Errorf("managed-services %s bundle is not Ready", family)
		}
	}
	plans, err := reader.Get(ctx, "", "managedserviceplans", "")
	if err != nil {
		return err
	}
	byName := make(map[string]map[string]any)
	for _, raw := range nestedSlice(plans, "items") {
		if plan, ok := raw.(map[string]any); ok {
			byName[nestedString(plan, "metadata", "name")] = plan
		}
	}
	developmentOnly := false
	for _, raw := range nestedSlice(catalog, "spec", "components") {
		if raw == "components/development-only" {
			developmentOnly = true
		}
	}
	for _, family := range greenfieldFamilies {
		for _, tier := range []string{"development", "production"} {
			name := family + "-" + tier
			plan := byName[name]
			if plan == nil || !conditionCurrentTrue(plan, "Verified") {
				return fmt.Errorf("managed-services plan %s is not Verified", name)
			}
			if nestedBool(plan, "spec", "disabled") {
				return fmt.Errorf("managed-services plan %s is disabled", name)
			}
			consoleDisabled := nestedString(plan, "metadata", "annotations", "services.kube-dc.com/console") == "disabled"
			if consoleDisabled != (tier == "production" && developmentOnly) {
				return fmt.Errorf("managed-services plan %s has unexpected console visibility", name)
			}
		}
	}
	if greenfield {
		for _, name := range legacyServicePlans {
			plan := byName[name]
			if plan == nil || !nestedBool(plan, "spec", "disabled") {
				return fmt.Errorf("legacy managed-services plan %s is not disabled", name)
			}
		}
	}
	return nil
}

func conditionCurrentTrue(object map[string]any, kind string) bool {
	generation := nestedInt(object, "metadata", "generation")
	if generation < 1 {
		return false
	}
	for _, raw := range nestedSlice(object, "status", "conditions") {
		condition, ok := raw.(map[string]any)
		if ok && nestedString(condition, "type") == kind && nestedString(condition, "status") == "True" &&
			nestedInt(condition, "observedGeneration") >= generation {
			return true
		}
	}
	return false
}

func waitManagedServicesPublished(ctx context.Context, reader managedServicesReader, pins map[string]string) error {
	deadline, cancel := context.WithTimeout(ctx, 15*time.Minute)
	defer cancel()
	var last error
	for {
		if err := checkManagedServicesPublished(deadline, reader, pins); err == nil {
			return nil
		} else {
			last = err
		}
		select {
		case <-deadline.Done():
			return fmt.Errorf("managed-services publication did not converge: %w", last)
		case <-time.After(5 * time.Second):
		}
	}
}
