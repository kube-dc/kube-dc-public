package clusterinit

import (
	"fmt"
	"os"
	"path"
	"strings"

	"gopkg.in/yaml.v3"
)

// Prepared components can consume only captured local inputs. Escaping sibling
// resources and remote bases need a separate reviewed source contract.
func checkPreparedComponentDependencies(directory string, components []SiteComponent, entries []string) error {
	if len(components) == 0 {
		return nil
	}
	root, err := os.OpenRoot(directory)
	if err != nil {
		return err
	}
	defer root.Close()
	seen := map[string]bool{}
	var visit func(string) error
	visit = func(relative string) error {
		if seen[relative] {
			return nil
		}
		if len(seen) >= 1024 {
			return fmt.Errorf("site component dependency limit exceeded")
		}
		if !safePreparedPath(relative) {
			return fmt.Errorf("site component dependency must stay inside captured local sources")
		}
		covered := false
		for _, entry := range entries {
			covered = covered || relative == entry || strings.HasPrefix(relative, entry+"/")
		}
		if !covered {
			return fmt.Errorf("site component dependency is outside captured source roots; keep resources inside the component or shared Fleet trees")
		}
		seen[relative] = true
		info, err := root.Lstat(relative)
		if err != nil {
			return fmt.Errorf("site component dependency is missing")
		}
		if info.Mode()&os.ModeSymlink != 0 {
			return fmt.Errorf("site component dependency cannot be a symlink")
		}
		if !info.IsDir() {
			_, _, err := readPreparedFile(root, relative)
			return err
		}
		var body []byte
		for _, name := range []string{"kustomization.yaml", "kustomization.yml", "Kustomization"} {
			body, _, err = readPreparedFile(root, path.Join(relative, name))
			if err == nil {
				break
			}
		}
		if err != nil {
			return fmt.Errorf("site component dependency has no bounded kustomization")
		}
		var doc map[string]any
		if err := yaml.Unmarshal(body, &doc); err != nil {
			return fmt.Errorf("invalid site component kustomization")
		}
		ref := func(value string) error {
			if value == "" || strings.ContainsAny(value, "\r\n\x00?\\") || strings.Contains(value, ":") || path.IsAbs(value) {
				return fmt.Errorf("site component dependency must be a reviewed local path")
			}
			return visit(path.Join(relative, value))
		}
		for _, key := range []string{"resources", "bases", "components", "crds", "configurations", "transformers", "generators", "patchesStrategicMerge"} {
			list, _ := doc[key].([]any)
			for _, raw := range list {
				value, ok := raw.(string)
				if !ok {
					return fmt.Errorf("unsupported site component dependency shape")
				}
				if key == "patchesStrategicMerge" && strings.Contains(value, "\n") {
					continue
				}
				if err := ref(value); err != nil {
					return err
				}
			}
		}
		for _, key := range []string{"patches", "patchesJson6902", "replacements"} {
			list, _ := doc[key].([]any)
			for _, raw := range list {
				entry, _ := raw.(map[string]any)
				if value, _ := entry["path"].(string); value != "" {
					if err := ref(value); err != nil {
						return err
					}
				}
			}
		}
		if openapi, ok := doc["openapi"].(map[string]any); ok {
			if value, _ := openapi["path"].(string); value != "" {
				if err := ref(value); err != nil {
					return err
				}
			}
		}
		for _, key := range []string{"configMapGenerator", "secretGenerator"} {
			list, _ := doc[key].([]any)
			for _, raw := range list {
				entry, _ := raw.(map[string]any)
				if value, _ := entry["env"].(string); value != "" {
					if err := ref(value); err != nil {
						return err
					}
				}
				for _, field := range []string{"files", "envs"} {
					values, _ := entry[field].([]any)
					for _, raw := range values {
						value, _ := raw.(string)
						if field == "files" {
							if _, p, ok := strings.Cut(value, "="); ok {
								value = p
							}
						}
						if err := ref(value); err != nil {
							return err
						}
					}
				}
			}
		}
		for _, key := range []string{"helmCharts", "helmChartInflationGenerator"} {
			if lenValue, ok := doc[key].([]any); ok && len(lenValue) > 0 {
				return fmt.Errorf("prepared site components cannot acquire Helm chart sources; use pinned Fleet HelmReleases")
			}
		}
		return nil
	}
	for _, component := range components {
		if err := visit(component.Path); err != nil {
			return err
		}
	}
	return nil
}
