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
	"regexp"
	"strings"
	"time"

	"github.com/shalb/kube-dc/cli/internal/bootstrap/clusterinit"
	"github.com/shalb/kube-dc/cli/internal/bootstrap/ports"
	"gopkg.in/yaml.v3"
)

var managedServicesSuspendLine = regexp.MustCompile(`(?m)^  suspend: (true|false)[ \t]*$`)

// prepareManagedServicesRebuild fences the old hub before a rebuild. It only
// modifies the two reviewed Kustomizations in an otherwise clean Fleet tree.
func prepareManagedServicesRebuild(ctx context.Context, repo, cluster, token string, git ports.GitClient, sops ports.SOPSClient) error {
	pins, err := managedServicesOverlayPins(repo, cluster)
	if err != nil {
		return err
	}
	if err := checkManagedServicesFleetDiff(ctx, git, repo); err != nil {
		return err
	}
	key, err := clusterinit.LoadManagedServicesCellKeyEscrow(ctx, repo, cluster, pins["SERVICES_CELL_ID"], sops)
	if err != nil {
		return err
	}
	clearManagedServicesKey(key)
	head, err := git.Head(ctx, repo)
	if err != nil {
		return err
	}
	base := filepath.Join(repo, "clusters", cluster)
	paths := []struct{ file, name string }{{"services.yaml", "services"}, {"services-catalog.yaml", "services-catalog"}}
	original := make(map[string][]byte, len(paths))
	changed := false
	for _, target := range paths {
		path := filepath.Join(base, target.file)
		current, err := readRegularRebuildFile(path)
		if err != nil {
			return rollbackManagedServicesRebuildFiles(original, err)
		}
		original[path] = current
		next, err := setManagedServicesKustomizationSuspend(current, target.name, true)
		if err != nil {
			return rollbackManagedServicesRebuildFiles(original, err)
		}
		if !bytes.Equal(current, next) {
			if err := writeRegularRebuildFile(path, next); err != nil {
				return rollbackManagedServicesRebuildFiles(original, err)
			}
			changed = true
		}
	}
	configPath := filepath.Join(base, "cluster-config.env")
	config, err := readRegularRebuildFile(configPath)
	if err != nil {
		return rollbackManagedServicesRebuildFiles(original, err)
	}
	original[configPath] = config
	nextConfig, err := setManagedServicesVisibility(config, false)
	if err != nil {
		return rollbackManagedServicesRebuildFiles(original, err)
	}
	if !bytes.Equal(config, nextConfig) {
		if err := writeRegularRebuildFile(configPath, nextConfig); err != nil {
			return rollbackManagedServicesRebuildFiles(original, err)
		}
		changed = true
	}
	if !changed {
		if err := git.Push(ctx, repo, token); err != nil {
			return fmt.Errorf("managed-services rebuild suspension is local but not pushed: %w", err)
		}
		return nil
	}
	if err := checkManagedServicesRebuildDiff(ctx, git, repo, cluster); err != nil {
		return rollbackManagedServicesRebuildFiles(original, err)
	}
	commit, err := git.CommitAndPush(ctx, repo, "Suspend managed services for rebuild of "+cluster, token)
	if err != nil {
		return rollbackOwnManagedServicesCommit(ctx, git, repo, head, commit, err)
	}
	return nil
}

func setManagedServicesVisibility(config []byte, visible bool) ([]byte, error) {
	const key = "KUBE_DC_UI_MANAGED_SERVICES_ALL_ORGANIZATIONS"
	lines := strings.Split(string(config), "\n")
	found := false
	for i, line := range lines {
		if !strings.HasPrefix(line, key+"=") {
			continue
		}
		if found || line != key+"=true" && line != key+"=false" {
			return nil, fmt.Errorf("managed-services console visibility flag is duplicated or invalid")
		}
		found = true
		if visible {
			lines[i] = key + "=true"
		} else {
			lines[i] = key + "=false"
		}
	}
	if !found {
		return nil, fmt.Errorf("managed-services console visibility flag is missing")
	}
	return []byte(strings.Join(lines, "\n")), nil
}

func readRegularRebuildFile(path string) ([]byte, error) {
	info, err := os.Lstat(path)
	if err != nil || !info.Mode().IsRegular() {
		return nil, fmt.Errorf("managed-services rebuild file %s is missing or not regular", path)
	}
	return os.ReadFile(path)
}

func rollbackManagedServicesRebuildFiles(original map[string][]byte, originalErr error) error {
	for path, body := range original {
		if err := writeRegularRebuildFile(path, body); err != nil {
			return fmt.Errorf("managed-services rebuild failed (%v) and local rollback failed: %w", originalErr, err)
		}
	}
	return originalErr
}

func writeRegularRebuildFile(path string, body []byte) error {
	info, err := os.Lstat(path)
	if err != nil || !info.Mode().IsRegular() {
		return fmt.Errorf("managed-services rebuild file %s is missing or not regular", path)
	}
	temp, err := os.CreateTemp(filepath.Dir(path), ".managed-services-rebuild-*")
	if err != nil {
		return err
	}
	defer os.Remove(temp.Name())
	defer temp.Close()
	if err := temp.Chmod(info.Mode().Perm()); err != nil {
		return err
	}
	if _, err := temp.Write(body); err != nil {
		return err
	}
	if err := temp.Sync(); err != nil {
		return err
	}
	if err := temp.Close(); err != nil {
		return err
	}
	return os.Rename(temp.Name(), path)
}

func setManagedServicesKustomizationSuspend(body []byte, name string, suspend bool) ([]byte, error) {
	var document struct {
		APIVersion string                           `yaml:"apiVersion"`
		Kind       string                           `yaml:"kind"`
		Metadata   struct{ Name, Namespace string } `yaml:"metadata"`
		Spec       struct {
			Suspend *bool  `yaml:"suspend"`
			Path    string `yaml:"path"`
		} `yaml:"spec"`
	}
	if err := yaml.Unmarshal(body, &document); err != nil {
		return nil, err
	}
	if document.APIVersion != "kustomize.toolkit.fluxcd.io/v1" || document.Kind != "Kustomization" ||
		document.Metadata.Name != name || document.Metadata.Namespace != "flux-system" ||
		document.Spec.Path != "./platform/kube-dc-services" && document.Spec.Path != "./platform/kube-dc-services-catalog" {
		return nil, fmt.Errorf("managed-services %s Kustomization has an unexpected identity or path", name)
	}
	if name == "services" && document.Spec.Path != "./platform/kube-dc-services" ||
		name == "services-catalog" && document.Spec.Path != "./platform/kube-dc-services-catalog" {
		return nil, fmt.Errorf("managed-services %s Kustomization has the wrong component path", name)
	}
	line := []byte("  suspend: false")
	if suspend {
		line = []byte("  suspend: true")
	}
	matches := managedServicesSuspendLine.FindAllIndex(body, -1)
	if len(matches) > 1 {
		return nil, fmt.Errorf("managed-services %s Kustomization has duplicate suspend fields", name)
	}
	if len(matches) == 1 {
		return managedServicesSuspendLine.ReplaceAll(body, line), nil
	}
	if document.Spec.Suspend != nil || bytes.Count(body, []byte("\nspec:\n")) != 1 {
		return nil, fmt.Errorf("managed-services %s Kustomization has an unexpected spec", name)
	}
	return bytes.Replace(body, []byte("\nspec:\n"), append([]byte("\nspec:\n"), append(line, '\n')...), 1), nil
}

func checkManagedServicesRebuildDiff(ctx context.Context, git ports.GitClient, repo, cluster string) error {
	diff, err := git.Diff(ctx, repo)
	if err != nil {
		return err
	}
	allowed := map[string]bool{
		filepath.ToSlash(filepath.Join("clusters", cluster, "services.yaml")):         true,
		filepath.ToSlash(filepath.Join("clusters", cluster, "services-catalog.yaml")): true,
		filepath.ToSlash(filepath.Join("clusters", cluster, "cluster-config.env")):    true,
	}
	if len(diff.Files) == 0 {
		return fmt.Errorf("managed-services rebuild produced no Git change")
	}
	for _, file := range diff.Files {
		if !allowed[filepath.ToSlash(file.Path)] || strings.Contains(file.Status, "D") {
			return fmt.Errorf("managed-services rebuild has an unrelated Git change %q", file.Path)
		}
	}
	return nil
}

func clearManagedServicesKey(key map[string][]byte) {
	for _, value := range key {
		for i := range value {
			value[i] = 0
		}
	}
}

// restoreManagedServicesCellKey must run while the services Kustomization is
// suspended, before the replacement hub can mint a new signing identity.
type managedServicesRebuildReader interface {
	Get(context.Context, string, string, string) (map[string]any, error)
	ApplyRebuildObject(context.Context, map[string]any) error
}

func restoreManagedServicesCellKey(ctx context.Context, reader managedServicesRebuildReader, repo, cluster string, sops ports.SOPSClient) error {
	pins, err := managedServicesOverlayPins(repo, cluster)
	if err != nil {
		return err
	}
	if pins["KUBE_DC_UI_MANAGED_SERVICES_ALL_ORGANIZATIONS"] != "false" {
		return fmt.Errorf("Fleet managed-services console visibility must be off during key restore")
	}
	for _, target := range []struct{ file, name string }{{"services.yaml", "services"}, {"services-catalog.yaml", "services-catalog"}} {
		body, err := readRegularRebuildFile(filepath.Join(repo, "clusters", cluster, target.file))
		if err != nil {
			return err
		}
		suspended, err := setManagedServicesKustomizationSuspend(body, target.name, true)
		if err != nil || !bytes.Equal(body, suspended) {
			return fmt.Errorf("Fleet %s Kustomization must be suspended before restoring the signing key", target.name)
		}
	}
	key, err := clusterinit.LoadManagedServicesCellKeyEscrow(ctx, repo, cluster, pins["SERVICES_CELL_ID"], sops)
	if err != nil {
		return err
	}
	defer clearManagedServicesKey(key)
	config, err := reader.Get(ctx, "flux-system", "configmap", "cluster-config")
	if err != nil {
		return err
	}
	if nestedString(config, "data", "CLUSTER_NAME") != cluster ||
		nestedString(config, "data", "SERVICES_CELL_ID") != pins["SERVICES_CELL_ID"] ||
		nestedString(config, "data", "KUBE_DC_UI_MANAGED_SERVICES_ALL_ORGANIZATIONS") != "false" {
		return fmt.Errorf("live cluster identity differs from the key escrow")
	}
	for _, name := range []string{"services", "services-catalog"} {
		object, err := reader.Get(ctx, "flux-system", "kustomization", name)
		if err != nil {
			return err
		}
		expectedPath := "./platform/kube-dc-services"
		if name == "services-catalog" {
			expectedPath += "-catalog"
		}
		if nestedString(object, "metadata", "name") != name ||
			nestedString(object, "metadata", "namespace") != "flux-system" ||
			nestedString(object, "spec", "path") != expectedPath ||
			!nestedBool(object, "spec", "suspend") {
			return fmt.Errorf("%s Kustomization must be suspended before restoring the signing key", name)
		}
	}
	secret, err := reader.Get(ctx, "kube-dc-services", "secret", "kube-dc-service-cell-signing-key")
	if err == nil {
		existingData, ok := nestedValue(secret, "data").(map[string]any)
		if !ok || nestedString(secret, "type") != "Opaque" || len(existingData) != len(key) {
			return fmt.Errorf("existing managed-services signing Secret has unexpected data")
		}
		if nestedString(secret, "metadata", "annotations", "services.kube-dc.com/cell-id") != pins["SERVICES_CELL_ID"] ||
			nestedString(secret, "metadata", "annotations", "services.kube-dc.com/key-id") != string(key["keyID"]) {
			return fmt.Errorf("existing managed-services signing Secret has a different identity")
		}
		for field, value := range key {
			if nestedString(secret, "data", field) != base64.StdEncoding.EncodeToString(value) {
				return fmt.Errorf("a different managed-services signing key already exists; refusing to replace it")
			}
		}
		return nil
	}
	if !strings.Contains(err.Error(), "NotFound") && !strings.Contains(err.Error(), "not found") {
		return err
	}
	if _, err := reader.Get(ctx, "kube-dc-services", "deployment", "kube-dc-services"); err == nil {
		return fmt.Errorf("hub Deployment is still present; stop it before restoring a missing signing key")
	} else if !strings.Contains(err.Error(), "NotFound") && !strings.Contains(err.Error(), "not found") {
		return err
	}
	namespace := map[string]any{"apiVersion": "v1", "kind": "Namespace", "metadata": map[string]any{
		"name": "kube-dc-services", "annotations": map[string]string{"kustomize.toolkit.fluxcd.io/prune": "disabled"},
	}}
	if err := reader.ApplyRebuildObject(ctx, namespace); err != nil {
		return err
	}
	data := make(map[string]string, len(key))
	for field, value := range key {
		data[field] = base64.StdEncoding.EncodeToString(value)
	}
	object := map[string]any{"apiVersion": "v1", "kind": "Secret", "type": "Opaque", "metadata": map[string]any{
		"name": "kube-dc-service-cell-signing-key", "namespace": "kube-dc-services",
		"annotations": map[string]string{"services.kube-dc.com/cell-id": pins["SERVICES_CELL_ID"], "services.kube-dc.com/key-id": string(key["keyID"])},
	}, "data": data}
	if err := reader.ApplyRebuildObject(ctx, object); err != nil {
		return err
	}
	verified, err := reader.Get(ctx, "kube-dc-services", "secret", "kube-dc-service-cell-signing-key")
	if err != nil {
		return err
	}
	for field, value := range data {
		if nestedString(verified, "data", field) != value {
			return fmt.Errorf("restored signing Secret did not match the escrow")
		}
	}
	return nil
}

func (k managedServicesKubectl) ApplyRebuildObject(ctx context.Context, object map[string]any) error {
	body, err := json.Marshal(object)
	if err != nil {
		return err
	}
	defer func() {
		for i := range body {
			body[i] = 0
		}
	}()
	callCtx, cancel := context.WithTimeout(ctx, 30*time.Second)
	defer cancel()
	args := append(k.kubectlArgs(), "--request-timeout=20s")
	if object["kind"] == "Secret" {
		// Create is conditional on absence. A hub racing the restore must not
		// let server-side apply overwrite its newly minted signing key.
		args = append(args, "create", "-f", "-")
	} else {
		args = append(args, "apply", "--server-side", "--field-manager=kube-dc-cli", "-f", "-")
	}
	cmd := exec.CommandContext(callCtx, "kubectl", args...)
	cmd.Stdin = bytes.NewReader(body)
	output, err := cmd.CombinedOutput()
	if err != nil {
		if object["kind"] == "Secret" {
			return fmt.Errorf("create managed-services signing Secret: %w", err)
		}
		return fmt.Errorf("apply managed-services rebuild object: %w: %s", err, strings.TrimSpace(string(output)))
	}
	return nil
}
