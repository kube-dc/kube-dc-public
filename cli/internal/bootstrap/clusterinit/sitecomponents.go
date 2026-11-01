package clusterinit

import (
	"fmt"
	"io"
	"os"
	"path"
	"path/filepath"
	"regexp"
	"sort"
	"strings"

	"gopkg.in/yaml.v3"
)

// SiteComponent attaches a Kustomize Component kept in the Fleet repository
// to one generated Flux Kustomization. Sites use it for configuration the
// starter cannot express, without editing the generated layer files by hand.
type SiteComponent struct {
	// Layer is the Flux Kustomization's metadata.name (for example platform).
	Layer string
	// Path is the component directory relative to the repository root.
	Path string
}

var siteLayerName = regexp.MustCompile(`^[a-z0-9]([-a-z0-9]*[a-z0-9])?$`)

// ParseSiteComponents reads KUBE_DC_INIT_SITE_COMPONENTS: comma-separated
// LAYER=PATH entries, applied in order (a later entry for the same layer is
// appended after the earlier ones).
func ParseSiteComponents(value string) ([]SiteComponent, error) {
	var out []SiteComponent
	seen := map[string]bool{}
	for _, raw := range strings.Split(value, ",") {
		raw = strings.TrimSpace(raw)
		if raw == "" {
			continue
		}
		layer, p, ok := strings.Cut(raw, "=")
		layer, p = strings.TrimSpace(layer), path.Clean(strings.TrimSpace(p))
		if !ok || !siteLayerName.MatchString(layer) || p == "." || path.IsAbs(p) ||
			p == ".." || strings.HasPrefix(p, "../") || strings.ContainsAny(p, " \t\"'\\$") {
			return nil, fmt.Errorf("site component %q must be LAYER=PATH with a repository-relative path", raw)
		}
		if seen[layer+"="+p] {
			continue
		}
		seen[layer+"="+p] = true
		out = append(out, SiteComponent{Layer: layer, Path: p})
	}
	return out, nil
}

// WriteSiteComponents appends each component to its layer's spec.components,
// relative to the layer's spec.path. Re-runs are idempotent. Each edited
// document is re-parsed and must differ only by the added entries.
func WriteSiteComponents(fleetRepo, clusterName string, comps []SiteComponent, out io.Writer) error {
	if len(comps) == 0 {
		return nil
	}
	if out == nil {
		out = io.Discard
	}
	clusterDir := filepath.Join(fleetRepo, "clusters", clusterName)
	for _, c := range comps {
		if err := requireComponentDir(fleetRepo, c.Path); err != nil {
			return err
		}
		file, err := findFluxLayerFile(clusterDir, c.Layer)
		if err != nil {
			return err
		}
		body, err := os.ReadFile(file)
		if err != nil {
			return fmt.Errorf("site component: read %s: %w", file, err)
		}
		updated, added, err := addLayerComponent(string(body), c.Layer, c.Path)
		if err != nil {
			return fmt.Errorf("site component %s=%s: %s: %w", c.Layer, c.Path, filepath.Base(file), err)
		}
		if !added {
			continue
		}
		if err := atomicWrite(file, []byte(updated), 0o644); err != nil {
			return fmt.Errorf("site component: write %s: %w", file, err)
		}
		fmt.Fprintf(out, "[scaffold] site component %s added to Flux layer %s\n", c.Path, c.Layer)
	}
	return nil
}

func requireComponentDir(fleetRepo, rel string) error {
	file := filepath.Join(fleetRepo, filepath.FromSlash(rel), "kustomization.yaml")
	body, err := os.ReadFile(file)
	if err != nil {
		return fmt.Errorf("site component %s: %w", rel, err)
	}
	var doc struct{ Kind string }
	if err := yaml.Unmarshal(body, &doc); err != nil || doc.Kind != "Component" {
		return fmt.Errorf("site component %s: kustomization.yaml must declare kind: Component", rel)
	}
	return nil
}

// findFluxLayerFile returns the overlay file declaring the named Flux
// Kustomization. Exactly one declaration is accepted.
func findFluxLayerFile(clusterDir, layer string) (string, error) {
	entries, err := os.ReadDir(clusterDir)
	if err != nil {
		return "", fmt.Errorf("site component: read %s: %w", clusterDir, err)
	}
	var found []string
	for _, e := range entries {
		if e.IsDir() || !strings.HasSuffix(e.Name(), ".yaml") || strings.HasSuffix(e.Name(), ".enc.yaml") {
			continue
		}
		file := filepath.Join(clusterDir, e.Name())
		body, err := os.ReadFile(file)
		if err != nil {
			return "", err
		}
		for _, d := range splitLayerDocs(string(body)) {
			if name, _, ok := fluxLayer(d.text); ok && name == layer {
				found = append(found, file)
			}
		}
	}
	sort.Strings(found)
	switch len(found) {
	case 1:
		return found[0], nil
	case 0:
		return "", fmt.Errorf("site component: no Flux Kustomization named %q in clusters/%s", layer, filepath.Base(clusterDir))
	default:
		return "", fmt.Errorf("site component: Flux Kustomization %q is declared more than once (%v)", layer, found)
	}
}

type layerDoc struct {
	start, end int // line range [start, end)
	text       string
}

func splitLayerDocs(body string) []layerDoc {
	lines := strings.Split(body, "\n")
	var docs []layerDoc
	start := 0
	for i := 0; i <= len(lines); i++ {
		if i == len(lines) || strings.TrimRight(lines[i], " ") == "---" {
			docs = append(docs, layerDoc{start, i, strings.Join(lines[start:i], "\n")})
			start = i + 1
		}
	}
	return docs
}

// fluxLayer reports a Flux Kustomization document's name and spec.path.
func fluxLayer(text string) (name, specPath string, ok bool) {
	var doc struct {
		APIVersion string `yaml:"apiVersion"`
		Kind       string
		Metadata   struct{ Name string }
		Spec       struct {
			Path       string
			Components []string
		}
	}
	if err := yaml.Unmarshal([]byte(text), &doc); err != nil {
		return "", "", false
	}
	if doc.Kind != "Kustomization" || !strings.HasPrefix(doc.APIVersion, "kustomize.toolkit.fluxcd.io/") {
		return "", "", false
	}
	return doc.Metadata.Name, doc.Spec.Path, true
}

func layerComponents(text string) []string {
	var doc struct{ Spec struct{ Components []string } }
	_ = yaml.Unmarshal([]byte(text), &doc)
	return doc.Spec.Components
}

// addLayerComponent inserts the component into the named layer's document,
// preserving every other line.
func addLayerComponent(body, layer, componentPath string) (string, bool, error) {
	lines := strings.Split(body, "\n")
	for _, d := range splitLayerDocs(body) {
		name, specPath, ok := fluxLayer(d.text)
		if !ok || name != layer {
			continue
		}
		if specPath == "" {
			return "", false, fmt.Errorf("layer has no spec.path")
		}
		rel, err := filepath.Rel(filepath.FromSlash(path.Clean(strings.TrimPrefix(specPath, "./"))), filepath.FromSlash(componentPath))
		if err != nil {
			return "", false, err
		}
		rel = filepath.ToSlash(rel)
		if !strings.HasPrefix(rel, ".") {
			rel = "./" + rel
		}
		before := layerComponents(d.text)
		for _, existing := range before {
			if path.Clean(existing) == path.Clean(rel) {
				return body, false, nil
			}
		}
		doc := lines[d.start:d.end]
		specIdx, keyIndent := -1, ""
		for i, l := range doc {
			if strings.TrimRight(l, " ") == "spec:" {
				specIdx = i
				continue
			}
			if specIdx >= 0 && keyIndent == "" && strings.TrimSpace(l) != "" && !strings.HasPrefix(strings.TrimSpace(l), "#") {
				keyIndent = l[:len(l)-len(strings.TrimLeft(l, " "))]
			}
		}
		if specIdx < 0 || keyIndent == "" {
			return "", false, fmt.Errorf("layer document has no top-level spec mapping")
		}
		var edited []string
		compIdx := -1
		for i, l := range doc {
			if i > specIdx && strings.TrimRight(l, " ") == keyIndent+"components:" {
				compIdx = i
			}
		}
		if compIdx >= 0 {
			// Append after the last list item, whichever indentation it uses.
			last := compIdx
			itemIndent := keyIndent + "  "
			for i := compIdx + 1; i < len(doc); i++ {
				t := strings.TrimLeft(doc[i], " ")
				ind := doc[i][:len(doc[i])-len(t)]
				if strings.HasPrefix(t, "- ") && (ind == keyIndent || ind == keyIndent+"  ") {
					last, itemIndent = i, ind
					continue
				}
				if strings.TrimSpace(doc[i]) == "" || strings.HasPrefix(t, "#") {
					continue
				}
				break
			}
			edited = append(edited, doc[:last+1]...)
			edited = append(edited, itemIndent+"- "+rel)
			edited = append(edited, doc[last+1:]...)
		} else {
			pathIdx := -1
			for i := specIdx + 1; i < len(doc); i++ {
				if strings.HasPrefix(doc[i], keyIndent+"path:") {
					pathIdx = i
					break
				}
			}
			if pathIdx < 0 {
				return "", false, fmt.Errorf("layer document has no spec.path line")
			}
			edited = append(edited, doc[:pathIdx+1]...)
			edited = append(edited, keyIndent+"components:", keyIndent+"  - "+rel)
			edited = append(edited, doc[pathIdx+1:]...)
		}
		after := layerComponents(strings.Join(edited, "\n"))
		if len(after) != len(before)+1 || after[len(after)-1] != rel {
			return "", false, fmt.Errorf("could not append %s to spec.components safely; edit the layer file by hand", rel)
		}
		if n, p, ok := fluxLayer(strings.Join(edited, "\n")); !ok || n != layer || p != specPath {
			return "", false, fmt.Errorf("edit changed the layer identity; edit the layer file by hand")
		}
		result := append(append(append([]string{}, lines[:d.start]...), edited...), lines[d.end:]...)
		return strings.Join(result, "\n"), true, nil
	}
	return "", false, fmt.Errorf("layer %q not found", layer)
}
