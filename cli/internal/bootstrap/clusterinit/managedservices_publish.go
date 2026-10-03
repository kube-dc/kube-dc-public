package clusterinit

import (
	"fmt"
	"os"
	"path/filepath"
	"regexp"
	"strings"

	"gopkg.in/yaml.v3"
)

var catalogSuspendLine = regexp.MustCompile(`(?m)^  suspend: (true|false)[ \t]*$`)

// PublishManagedServicesFiles prepares the two Fleet changes that expose a
// verified catalog. The caller must verify the live cluster and escrow the
// signing key first, then commit and push these files together with escrow.
func PublishManagedServicesFiles(repo, cluster string) (bool, error) {
	if repo == "" || !ValidManagedServicesClusterPath(cluster) {
		return false, fmt.Errorf("managed-services publication needs a Fleet repo and DNS-label cluster")
	}
	dir := filepath.Join(repo, "clusters", cluster)
	catalogPath := filepath.Join(dir, "services-catalog.yaml")
	configPath := filepath.Join(dir, "cluster-config.env")
	catalog, err := readRegularManagedServicesFile(catalogPath)
	if err != nil {
		return false, err
	}
	config, err := readRegularManagedServicesFile(configPath)
	if err != nil {
		return false, err
	}
	var obj struct {
		APIVersion string                           `yaml:"apiVersion"`
		Kind       string                           `yaml:"kind"`
		Metadata   struct{ Name, Namespace string } `yaml:"metadata"`
		Spec       struct {
			Suspend *bool `yaml:"suspend"`
		} `yaml:"spec"`
	}
	if err := yaml.Unmarshal(catalog, &obj); err != nil {
		return false, fmt.Errorf("parse managed-services catalog: %w", err)
	}
	if obj.APIVersion != "kustomize.toolkit.fluxcd.io/v1" || obj.Kind != "Kustomization" ||
		obj.Metadata.Name != "services-catalog" || obj.Metadata.Namespace != "flux-system" || obj.Spec.Suspend == nil {
		return false, fmt.Errorf("managed-services catalog has unexpected identity or no suspend field")
	}
	lines := catalogSuspendLine.FindAllIndex(catalog, -1)
	if len(lines) != 1 {
		return false, fmt.Errorf("managed-services catalog must have exactly one plain suspend field")
	}
	const flag = "KUBE_DC_UI_MANAGED_SERVICES_ALL_ORGANIZATIONS"
	configLines := strings.Split(string(config), "\n")
	flagAt := -1
	for i, line := range configLines {
		if strings.HasPrefix(line, flag+"=") {
			if flagAt >= 0 || (line != flag+"=true" && line != flag+"=false") {
				return false, fmt.Errorf("managed-services console flag is duplicated or invalid")
			}
			flagAt = i
		}
	}
	if flagAt < 0 {
		return false, fmt.Errorf("managed-services console flag is missing")
	}
	if !*obj.Spec.Suspend && configLines[flagAt] == flag+"=true" {
		return false, nil
	}
	if !*obj.Spec.Suspend || configLines[flagAt] != flag+"=false" {
		return false, fmt.Errorf("managed-services catalog and console flag are partly published; inspect Fleet before retrying")
	}
	newCatalog := catalogSuspendLine.ReplaceAll(catalog, []byte("  suspend: false"))
	configLines[flagAt] = flag + "=true"
	newConfig := []byte(strings.Join(configLines, "\n"))
	if err := writeManagedServicesFile(catalogPath, newCatalog); err != nil {
		return false, err
	}
	if err := writeManagedServicesFile(configPath, newConfig); err != nil {
		if restoreErr := writeManagedServicesFile(catalogPath, catalog); restoreErr != nil {
			return false, fmt.Errorf("write console flag: %v; catalog rollback failed: %w", err, restoreErr)
		}
		return false, err
	}
	return true, nil
}

func readRegularManagedServicesFile(path string) ([]byte, error) {
	info, err := os.Lstat(path)
	if err != nil || !info.Mode().IsRegular() {
		return nil, fmt.Errorf("managed-services Fleet file %s is absent or not regular: %v", path, err)
	}
	return os.ReadFile(path)
}

func writeManagedServicesFile(path string, data []byte) error {
	info, err := os.Lstat(path)
	if err != nil || !info.Mode().IsRegular() {
		return fmt.Errorf("managed-services Fleet file %s is absent or not regular: %v", path, err)
	}
	temp, err := os.CreateTemp(filepath.Dir(path), ".managed-services-*")
	if err != nil {
		return err
	}
	defer os.Remove(temp.Name())
	defer temp.Close()
	if err := temp.Chmod(info.Mode().Perm()); err != nil {
		return err
	}
	if _, err := temp.Write(data); err != nil {
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
