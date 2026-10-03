package clusterinit

// payg_build.go — the cluster directory's own build, checked for anything
// that could reshape the PAYG objects after the file-level checks passed.
//
// THREAT MODEL. These checks guard the CLI's own scaffold against accidental
// misconfiguration: a copied overlay, a re-run, a hand edit that shadows the
// installation identity, the metering image or the database login. They are
// NOT a security boundary against someone with write access to the fleet
// repository — that person can change the deployed objects directly, and no
// check in an installer can stop it. Installation identity uniqueness is
// ultimately an operator invariant; the billing service is the second line,
// refusing a data plane whose installation UID is not its own.
//
// Within that model the rules are simple refusals, never interpretation:
//
//   - the cluster root kustomization, and every kustomization its resources
//     reach, may use only the fields the scaffold uses (resources, the
//     generators, patches, labels); components, transformers, validators,
//     replacements, generators, helm charts, namespace and name prefixes or
//     suffixes are refused outright;
//   - cluster-config is produced only by the scaffold's exact generator entry
//     in the root; any other generator of that name, or of the login Secret's
//     name, anywhere in the build, is refused;
//   - no patch in that build may even possibly select the payg layer, the
//     platform Kustomization, the login Secret or cluster-config;
//   - no other object in the cluster directory may be a Flux Kustomization
//     that builds ./platform/payg (or below), or carry the name of one of
//     those objects.

import (
	"bytes"
	"errors"
	"fmt"
	"io"
	"io/fs"
	"os"
	"path"
	"path/filepath"
	"reflect"
	"strings"

	"gopkg.in/yaml.v3"
)

// paygPlatformObject is the Flux Kustomization whose patches carry the role.
var paygPlatformObject = paygObject{"kustomize.toolkit.fluxcd.io", "v1", "Kustomization", "platform", "flux-system"}

// paygBuildAllowedKeys are the kustomization fields the scaffold's cluster
// build uses (or that cannot reshape an object's content). Everything else is
// refused.
var paygBuildAllowedKeys = map[string]bool{
	"apiVersion": true, "kind": true, "metadata": true, "resources": true,
	"configMapGenerator": true, "secretGenerator": true, "generatorOptions": true,
	"patches": true, "patchesStrategicMerge": true, "patchesJson6902": true,
	"labels": true, "commonLabels": true, "commonAnnotations": true,
	"buildMetadata": true, "sortOptions": true,
}

// paygScaffoldGenerator and paygScaffoldGeneratorOptions are what
// add-cluster.sh writes into every cluster root, parsed.
const (
	paygScaffoldGenerator        = "name: cluster-config\nnamespace: flux-system\nenvs:\n  - cluster-config.env\n"
	paygScaffoldGeneratorOptions = "disableNameSuffixHash: true\n"
)

// paygClusterBuildConflicts runs every cluster-build rule for clusterDir.
func paygClusterBuildConflicts(fleetRepo, clusterDir string) error {
	var wantGen, wantOpts any
	if err := yaml.Unmarshal([]byte(paygScaffoldGenerator), &wantGen); err != nil {
		return err
	}
	if err := yaml.Unmarshal([]byte(paygScaffoldGeneratorOptions), &wantOpts); err != nil {
		return err
	}
	protected := []paygObject{paygLayerObject, paygPlatformObject, paygSecretObject, paygConfigObject}
	rel := func(p string) string {
		r, err := filepath.Rel(fleetRepo, p)
		if err != nil {
			return p
		}
		return r
	}

	seen := map[string]bool{}
	scaffoldGenerators := 0
	var files []string // resource files reached by the root build
	var walk func(dir string, root bool) error
	walk = func(dir string, root bool) error {
		kfile := ""
		for _, n := range []string{"kustomization.yaml", "kustomization.yml", "Kustomization"} {
			if _, err := os.Stat(filepath.Join(dir, n)); err == nil {
				kfile = filepath.Join(dir, n)
				break
			}
		}
		if kfile == "" {
			if root {
				return fmt.Errorf("%s has no kustomization", rel(dir))
			}
			return nil
		}
		if seen[kfile] {
			return nil
		}
		seen[kfile] = true
		body, err := os.ReadFile(kfile)
		if err != nil {
			return err
		}
		var k map[string]any
		if err := yaml.Unmarshal(body, &k); err != nil {
			return fmt.Errorf("%s is not YAML: %w", rel(kfile), err)
		}
		for key := range k {
			if !paygBuildAllowedKeys[key] {
				return fmt.Errorf("%s uses %q, which the scaffold does not — in a PAYG cluster it could reshape the PAYG objects without a patch, so it is refused", rel(kfile), key)
			}
		}
		for _, field := range []string{"configMapGenerator", "secretGenerator"} {
			entries, _ := k[field].([]any)
			for _, e := range entries {
				m, _ := e.(map[string]any)
				name, _ := m["name"].(string)
				if name != paygConfigObject.name && name != paygSecretObject.name {
					continue
				}
				if root && field == "configMapGenerator" && reflect.DeepEqual(e, wantGen) {
					scaffoldGenerators++
					continue
				}
				return fmt.Errorf("%s has a %s entry for %q other than the scaffold's (%s) — it could replace or merge into what PAYG reads, so it is refused", rel(kfile), field, name, strings.ReplaceAll(strings.TrimSpace(paygScaffoldGenerator), "\n", "; "))
			}
		}
		if root {
			if opts, ok := k["generatorOptions"]; !ok || !reflect.DeepEqual(opts, wantOpts) {
				return fmt.Errorf("%s: generatorOptions must be exactly the scaffold's (%s)", rel(kfile), strings.TrimSpace(paygScaffoldGeneratorOptions))
			}
		}
		if err := paygKustomizationConflicts(dir, body, protected); err != nil {
			return fmt.Errorf("%s: %w", rel(kfile), err)
		}
		resources, _ := k["resources"].([]any)
		for _, r := range resources {
			s, _ := r.(string)
			if s == "" || strings.Contains(s, "://") || strings.HasPrefix(s, "github.com/") || strings.HasPrefix(s, "git@") {
				return fmt.Errorf("%s includes a remote or empty resource %q, which cannot be checked", rel(kfile), s)
			}
			p := filepath.Clean(filepath.Join(dir, s))
			bundle := filepath.Join(fleetRepo, paygBundlePath)
			if p == bundle || strings.HasPrefix(p, bundle+string(filepath.Separator)) {
				return fmt.Errorf("%s includes %s directly — only payg.yaml may build the PAYG bundle", rel(kfile), rel(p))
			}
			if fi, err := os.Stat(p); err == nil && fi.IsDir() {
				if err := walk(p, false); err != nil {
					return err
				}
			} else if err == nil {
				files = append(files, p)
			}
		}
		return nil
	}
	if err := walk(clusterDir, true); err != nil {
		return err
	}
	if scaffoldGenerators != 1 {
		return fmt.Errorf("%s must generate cluster-config exactly once, with the scaffold's entry", rel(filepath.Join(clusterDir, "kustomization.yaml")))
	}

	// Every object in the cluster directory (nested clusters excluded) and
	// every resource file the root build reaches.
	designated := map[string]string{
		paygLayerObject.name + "/" + paygLayerObject.namespace:       filepath.Join(clusterDir, paygLayerFileName),
		paygPlatformObject.name + "/" + paygPlatformObject.namespace: filepath.Join(clusterDir, "platform.yaml"),
		paygSecretObject.name + "/" + paygSecretObject.namespace:     filepath.Join(clusterDir, paygDBSecretsFileName),
	}
	// checkObject applies the object rules to one manifest, expanding List
	// wrappers (v1 List, or any *List kind with items) so an exported list
	// cannot hide a second reconciler.
	var checkObject func(p string, obj map[string]any) error
	checkObject = func(p string, obj map[string]any) error {
		str := func(m map[string]any, k string) string {
			v, _ := m[k].(string)
			return v
		}
		apiVersion, kind := str(obj, "apiVersion"), str(obj, "kind")
		if items, ok := obj["items"].([]any); ok && strings.HasSuffix(kind, "List") {
			for _, it := range items {
				m, ok := it.(map[string]any)
				if !ok {
					continue
				}
				if err := checkObject(p, m); err != nil {
					return err
				}
			}
			return nil
		}
		if strings.HasPrefix(apiVersion, "kustomize.config.k8s.io/") {
			return nil // a kustomize build file, not an object
		}
		meta, _ := obj["metadata"].(map[string]any)
		name, ns := str(meta, "name"), str(meta, "namespace")
		spec, _ := obj["spec"].(map[string]any)
		if strings.HasPrefix(apiVersion, "kustomize.toolkit.fluxcd.io/") && kind == "Kustomization" && p != designated["payg/flux-system"] {
			specPath := str(spec, "path")
			target := path.Clean(strings.TrimPrefix(strings.TrimSpace(specPath), "./"))
			if target == paygBundlePath || strings.HasPrefix(target, paygBundlePath+"/") {
				return fmt.Errorf("%s: Flux Kustomization %q builds %s — only payg.yaml may, so its inline settings cannot shadow the reviewed ones", rel(p), name, specPath)
			}
		}
		for _, o := range protected {
			if name != o.name || (ns != "" && ns != o.namespace) {
				continue
			}
			if o == paygConfigObject && kind == "ConfigMap" {
				return fmt.Errorf("%s declares ConfigMap %s by hand — it is generated from cluster-config.env only", rel(p), o.name)
			}
			if want, ok := designated[o.name+"/"+o.namespace]; ok && p != want && (kind == o.kind || o.kind == "Kustomization") {
				return fmt.Errorf("%s declares a second %s named %s/%s (the only one belongs in %s)", rel(p), kind, o.namespace, o.name, rel(want))
			}
		}
		return nil
	}
	// inspected records the files checkFile really read, so the reachable
	// resources below are skipped only when they were actually inspected.
	inspected := map[string]bool{}
	checkFile := func(p string) error {
		if inspected[p] {
			return nil
		}
		inspected[p] = true
		body, err := os.ReadFile(p)
		if err != nil {
			return fmt.Errorf("%s is part of the build but cannot be read: %w", rel(p), err)
		}
		dec := yaml.NewDecoder(bytes.NewReader(body))
		for {
			var doc any
			err := dec.Decode(&doc)
			if errors.Is(err, io.EOF) {
				return nil
			}
			if err != nil {
				return nil // not plain YAML/JSON (a template); nothing to name
			}
			obj, ok := doc.(map[string]any)
			if !ok {
				continue
			}
			if err := checkObject(p, obj); err != nil {
				return err
			}
		}
	}
	// The incidental scan: every manifest in the cluster directory, nested
	// clusters excluded (their objects are theirs).
	walkErr := filepath.WalkDir(clusterDir, func(p string, d fs.DirEntry, err error) error {
		if err != nil {
			return err
		}
		if d.IsDir() {
			if p != clusterDir {
				if _, err := os.Stat(filepath.Join(p, "cluster-config.env")); err == nil {
					return fs.SkipDir // another cluster nested below this one
				}
			}
			return nil
		}
		if strings.HasSuffix(p, ".yaml") || strings.HasSuffix(p, ".yml") || strings.HasSuffix(p, ".json") {
			return checkFile(p)
		}
		return nil
	})
	if walkErr != nil {
		return walkErr
	}
	// Every resource file the build actually reaches — whatever its
	// extension, wherever it lives (a nested directory included) — unless the
	// scan above really read it.
	for _, f := range files {
		if err := checkFile(f); err != nil {
			return err
		}
	}
	return nil
}
