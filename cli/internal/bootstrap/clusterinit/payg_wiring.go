package clusterinit

// payg_wiring.go — structural checks of the PAYG wiring in a fleet overlay.
//
// Exact matching plus conservative refusal; no interpretation of what an
// arbitrary kustomize or JSON6902 patch does:
//
//   - the login Secret is exactly one fully encrypted Secret document;
//   - payg.yaml is, after parsing, identical to what the CLI renders;
//   - the CNPG role is wired only by a patch identical, after parsing, to the
//     one the CLI renders for the current base Cluster;
//   - any OTHER patch or replacement that might select the CNPG Cluster (in
//     platform.yaml or the shared tree) is refused, whatever it does;
//   - the cluster directory's own build is checked in payg_build.go.
//
// See payg_build.go for the threat model: these checks catch accidental
// misconfiguration, not someone with write access to the fleet repository.

import (
	"bytes"
	"errors"
	"fmt"
	"io"
	"io/fs"
	"os"
	"path/filepath"
	"reflect"
	"regexp"
	"strings"

	"gopkg.in/yaml.v3"
)

// paygSecretTopLevel are the only top-level keys a login Secret may carry.
// Anything else sits outside SOPS' encrypted_regex (data|stringData) and
// would be committed readable.
var paygSecretTopLevel = map[string]bool{
	"apiVersion": true, "kind": true, "metadata": true, "type": true,
	"data": true, "stringData": true, "immutable": true, "sops": true,
}

// decodeSingleYAMLDoc decodes body, which must hold exactly ONE document. A
// second document — even an empty one — is refused: an appended `---`
// Secret would otherwise be applied without ever being checked.
func decodeSingleYAMLDoc(body []byte, out any) error {
	dec := yaml.NewDecoder(bytes.NewReader(body))
	if err := dec.Decode(out); err != nil {
		return fmt.Errorf("not YAML: %w", err)
	}
	var extra yaml.Node
	if err := dec.Decode(&extra); !errors.Is(err, io.EOF) {
		if err != nil {
			return fmt.Errorf("trailing content is not YAML: %w", err)
		}
		return fmt.Errorf("more than one YAML document")
	}
	return nil
}

// checkPAYGDBSecret refuses anything but a single SOPS-encrypted
// kube-dc-metering-db Secret. Every entry of data AND of stringData is checked
// on its own — no merge in which one map could mask the other — a key may
// appear in only one of them, username/password/uri must all be present, and
// every value must be an ENC[...] ciphertext.
func checkPAYGDBSecret(body []byte) error {
	var raw map[string]any
	if err := decodeSingleYAMLDoc(body, &raw); err != nil {
		return err
	}
	for k := range raw {
		if !paygSecretTopLevel[k] {
			return fmt.Errorf("unexpected top-level key %q (outside SOPS' encrypted fields)", k)
		}
	}
	var doc struct {
		Kind     string `yaml:"kind"`
		Metadata struct {
			Name      string `yaml:"name"`
			Namespace string `yaml:"namespace"`
		} `yaml:"metadata"`
		StringData map[string]any `yaml:"stringData"`
		Data       map[string]any `yaml:"data"`
		Sops       map[string]any `yaml:"sops"`
	}
	if err := decodeSingleYAMLDoc(body, &doc); err != nil {
		return err
	}
	if doc.Kind != "Secret" || doc.Metadata.Name != paygDBSecretName || doc.Metadata.Namespace != paygNamespace {
		return fmt.Errorf("not the %s/%s Secret (kind=%q name=%q namespace=%q)", paygNamespace, paygDBSecretName, doc.Kind, doc.Metadata.Name, doc.Metadata.Namespace)
	}
	if _, ok := doc.Sops["mac"]; !ok {
		return fmt.Errorf("no SOPS metadata (sops.mac)")
	}
	for _, field := range []struct {
		name   string
		values map[string]any
	}{{"data", doc.Data}, {"stringData", doc.StringData}} {
		for k, v := range field.values {
			if s, ok := v.(string); !ok || !paygSOPSValueRegex.MatchString(s) {
				return fmt.Errorf("%s.%s is not SOPS-encrypted", field.name, k)
			}
		}
	}
	for k := range doc.Data {
		if _, both := doc.StringData[k]; both {
			return fmt.Errorf("%s is in both data and stringData", k)
		}
	}
	for _, k := range []string{"username", "password", "uri"} {
		_, inData := doc.Data[k]
		_, inString := doc.StringData[k]
		if !inData && !inString {
			return fmt.Errorf("no %s", k)
		}
	}
	return nil
}

// checkPAYGLayer requires payg.yaml to be exactly what the CLI renders,
// compared after parsing (comments and key order do not matter, content
// does). Any addition — a postBuild.substitute, a second substituteFrom, a
// changed path — could override the installation identity or the metering
// image that cluster-config.env pins, so there is nothing to customise here.
func checkPAYGLayer(body []byte) error {
	var got, want any
	if err := decodeSingleYAMLDoc(body, &got); err != nil {
		return err
	}
	if err := yaml.Unmarshal([]byte(paygLayerYAML), &want); err != nil {
		return fmt.Errorf("render the expected layer: %w", err)
	}
	if !reflect.DeepEqual(got, want) {
		return errors.New("it differs from the layer the CLI renders (no postBuild.substitute, exactly one substituteFrom — ConfigMap cluster-config — and no other changes); restore it with `kube-dc bootstrap init --payg` output or by hand from platform/payg/README.md")
	}
	return nil
}

// paygKustomizationHas reports whether a kustomization.yaml lists res as a
// resource (parsed, not grepped).
func paygKustomizationHas(body []byte, res string) bool {
	var k struct {
		Resources []string `yaml:"resources"`
	}
	if yaml.Unmarshal(body, &k) != nil {
		return false
	}
	for _, r := range k.Resources {
		if strings.TrimPrefix(r, "./") == res {
			return true
		}
	}
	return false
}

// paygObject identifies one object the PAYG wiring depends on.
type paygObject struct {
	group, version, kind, name, namespace string
}

func (o paygObject) String() string {
	return fmt.Sprintf("%s %s/%s", o.kind, o.namespace, o.name)
}

// The objects whose content decides what PAYG runs with.
func paygCNPGCluster(dbCluster string) paygObject {
	return paygObject{"postgresql.cnpg.io", "v1", "Cluster", dbCluster, paygNamespace}
}

var (
	paygLayerObject  = paygObject{"kustomize.toolkit.fluxcd.io", "v1", "Kustomization", "payg", "flux-system"}
	paygSecretObject = paygObject{"", "v1", "Secret", paygDBSecretName, paygNamespace}
	paygConfigObject = paygObject{"", "v1", "ConfigMap", "cluster-config", "flux-system"}
)

// paygPatternMatches follows kustomize's selector semantics: every field is a
// regular expression over the whole value; an empty pattern matches anything.
// A pattern that does not compile is treated as matching (conservative).
func paygPatternMatches(pattern, value string) bool {
	if pattern == "" {
		return true
	}
	re, err := regexp.Compile("^(?:" + pattern + ")$")
	if err != nil {
		return true
	}
	return re.MatchString(value)
}

// mightSelect reports whether a kustomize/Flux target selector COULD select
// o. No attempt is made to understand what the patch does: a selector that
// might pick the object is enough. Label and annotation selectors cannot be
// evaluated against a manifest here, and unknown selector fields are not
// understood, so both count as "might select".
func (o paygObject) mightSelect(target map[string]any) bool {
	for k, v := range target {
		s, _ := v.(string)
		switch k {
		case "group":
			if !paygPatternMatches(s, o.group) {
				return false
			}
		case "version":
			if !paygPatternMatches(s, o.version) {
				return false
			}
		case "kind":
			if !paygPatternMatches(s, o.kind) {
				return false
			}
		case "name":
			if !paygPatternMatches(s, o.name) {
				return false
			}
		case "namespace":
			if !paygPatternMatches(s, o.namespace) {
				return false
			}
		}
	}
	return true // including labelSelector / annotationSelector / unknown keys
}

// patchMightSelect reports whether a patch entry could apply to o. With a
// target, the target decides. Without one, a strategic-merge document names
// its object itself; only a document that plainly names another kind or
// another name is ruled out — anything else (a JSON6902 list without a
// target, an unparsable body) counts as selecting.
func (o paygObject) patchMightSelect(target map[string]any, body string) bool {
	if target != nil {
		return o.mightSelect(target)
	}
	dec := yaml.NewDecoder(strings.NewReader(body))
	sawDoc := false
	for {
		var doc any
		err := dec.Decode(&doc)
		if errors.Is(err, io.EOF) {
			return !sawDoc
		}
		if err != nil {
			return true
		}
		sawDoc = true
		m, ok := doc.(map[string]any)
		if !ok {
			return true
		}
		kind, _ := m["kind"].(string)
		meta, _ := m["metadata"].(map[string]any)
		name, _ := meta["name"].(string)
		if (kind == "" || kind == o.kind) && (name == "" || name == o.name) {
			return true
		}
	}
}

// paygRolePatch is the platform.yaml patch entry the CLI renders, parsed.
type paygRolePatch struct {
	target map[string]any
	ops    any
}

// paygExpectedRolePatch parses the entry renderPAYGRolePatch writes for this
// Cluster, connection limit and base shape. Only an entry identical to it
// counts as the collector's role.
func paygExpectedRolePatch(dbCluster string, connectionLimit int, shape paygManagedShape) (paygRolePatch, error) {
	var doc struct {
		Patches []struct {
			Target map[string]any `yaml:"target"`
			Patch  string         `yaml:"patch"`
		} `yaml:"patches"`
	}
	if err := yaml.Unmarshal([]byte("patches:\n"+renderPAYGRolePatch(dbCluster, connectionLimit, shape)), &doc); err != nil || len(doc.Patches) != 1 {
		return paygRolePatch{}, fmt.Errorf("render the expected role patch: %v", err)
	}
	var ops any
	if err := yaml.Unmarshal([]byte(doc.Patches[0].Patch), &ops); err != nil {
		return paygRolePatch{}, fmt.Errorf("render the expected role patch: %w", err)
	}
	return paygRolePatch{target: doc.Patches[0].Target, ops: ops}, nil
}

// paygRoleWiring reads platform.yaml's spec.patches — the patches of the Flux
// Kustomization that renders the Cluster. The collector's role is wired only
// by an entry IDENTICAL, after parsing, to the one the CLI renders for the
// current base: exact target (group, kind, name, namespace, no selectors),
// exact op list — and so a role with login true, its passwordSecret and
// ensure: present. Every other entry that might select the Cluster is
// refused, whatever it does; the kube-dc marker without its patch is a
// broken overlay.
func paygRoleWiring(platformBody []byte, dbCluster string, want paygRolePatch) (bool, error) {
	var k struct {
		Spec struct {
			Patches []map[string]any `yaml:"patches"`
		} `yaml:"spec"`
	}
	if err := yaml.Unmarshal(platformBody, &k); err != nil {
		return false, fmt.Errorf("platform.yaml is not YAML: %w", err)
	}
	cluster := paygCNPGCluster(dbCluster)
	wired := false
	for i, entry := range k.Spec.Patches {
		target, _ := entry["target"].(map[string]any)
		body, _ := entry["patch"].(string)
		if _, hasPath := entry["path"]; hasPath {
			return false, fmt.Errorf("platform.yaml patch %d refers to a file, which a Flux Kustomization does not support — fix platform.yaml by hand", i)
		}
		if len(entry) == 2 && target != nil && body != "" && reflect.DeepEqual(target, want.target) {
			var ops any
			if yaml.Unmarshal([]byte(body), &ops) == nil && reflect.DeepEqual(ops, want.ops) {
				if wired {
					return false, fmt.Errorf("platform.yaml adds %s more than once", paygDBRole)
				}
				wired = true
				continue
			}
		}
		if cluster.patchMightSelect(target, body) {
			return false, fmt.Errorf("platform.yaml patch %d might select the CNPG %s, and any such patch could change its spec.managed roles; only the exact patch the CLI renders is accepted — remove it or merge the %s role into it by hand (platform/payg/README.md step 2)", i, cluster, paygDBRole)
		}
	}
	if !wired && bytes.Contains(platformBody, []byte(paygDBRoleMarker)) {
		return false, fmt.Errorf("platform.yaml carries the %q marker but no patch that adds %s the way the CLI renders it — restore the patch or remove the marker by hand", paygDBRoleMarker, paygDBRole)
	}
	return wired, nil
}

// findPAYGBaseCluster reads the CNPG Cluster the role is added to from the
// fleet's shared platform/ tree and reports its spec.managed shape. Run on
// EVERY PAYG write and resume, wired or not: it refuses a Cluster the fleet
// does not ship, a Cluster declared more than once, a base that already
// declares the role, and any kustomization of the shared tree with a patch or
// replacement that might select that Cluster.
func findPAYGBaseCluster(fleetRepo, dbCluster string) (paygManagedShape, error) {
	root := filepath.Join(fleetRepo, "platform")
	cluster := paygCNPGCluster(dbCluster)
	var bases []string
	baseNamespace := ""
	shape := paygNoManaged
	var conflict error
	walkErr := filepath.WalkDir(root, func(path string, d fs.DirEntry, err error) error {
		if err != nil {
			return err
		}
		if d.IsDir() || !(strings.HasSuffix(path, ".yaml") || strings.HasSuffix(path, ".yml")) || strings.HasSuffix(path, ".enc.yaml") {
			return nil
		}
		body, err := os.ReadFile(path)
		if err != nil {
			return nil
		}
		base := filepath.Base(path)
		if base == "kustomization.yaml" || base == "kustomization.yml" {
			if err := paygKustomizationConflicts(filepath.Dir(path), body, []paygObject{cluster}); err != nil && conflict == nil {
				conflict = fmt.Errorf("%s: %w", path, err)
			}
			return nil
		}
		if !bytes.Contains(body, []byte(dbCluster)) {
			return nil
		}
		dec := yaml.NewDecoder(bytes.NewReader(body))
		for {
			var doc struct {
				APIVersion string `yaml:"apiVersion"`
				Kind       string `yaml:"kind"`
				Metadata   struct {
					Name      string `yaml:"name"`
					Namespace string `yaml:"namespace"`
				} `yaml:"metadata"`
				Spec struct {
					Managed *struct {
						Roles []struct {
							Name string `yaml:"name"`
						} `yaml:"roles"`
					} `yaml:"managed"`
				} `yaml:"spec"`
			}
			if err := dec.Decode(&doc); err != nil {
				return nil // end of file, or a template that is not plain YAML
			}
			if doc.Kind != "Cluster" || doc.Metadata.Name != dbCluster ||
				!(doc.APIVersion == "" || strings.HasPrefix(doc.APIVersion, "postgresql.cnpg.io/")) {
				continue
			}
			bases = append(bases, path)
			baseNamespace = doc.Metadata.Namespace
			switch {
			case doc.Spec.Managed == nil:
				shape = paygNoManaged
			case doc.Spec.Managed.Roles == nil:
				shape = paygManagedNoRoles
			default:
				shape = paygManagedRoles
				for _, r := range doc.Spec.Managed.Roles {
					if r.Name == paygDBRole && conflict == nil {
						conflict = fmt.Errorf("%s already declares the managed role %s", path, paygDBRole)
					}
				}
			}
		}
	})
	if walkErr != nil {
		return 0, fmt.Errorf("read %s: %w", root, walkErr)
	}
	switch {
	case len(bases) == 0:
		return 0, fmt.Errorf("the CNPG Cluster %s/%s is not in this fleet checkout's platform/ tree — declare the %s managed role by hand (platform/payg/README.md step 2)", paygNamespace, dbCluster, paygDBRole)
	case conflict != nil:
		return 0, conflict
	case len(bases) > 1:
		// A second document of the same Cluster is a strategic-merge patch
		// file (or a copy), and which one is the base cannot be told apart.
		return 0, fmt.Errorf("the CNPG Cluster %s/%s is declared more than once in platform/ (%s) — add the %s role by hand (platform/payg/README.md step 2)", paygNamespace, dbCluster, strings.Join(bases, ", "), paygDBRole)
	case baseNamespace != paygNamespace:
		return 0, fmt.Errorf("the CNPG Cluster %s in %s is not in namespace %s — declare the %s role by hand (platform/payg/README.md step 2)", dbCluster, bases[0], paygNamespace, paygDBRole)
	}
	return shape, nil
}

// paygKustomizationConflicts refuses a kustomization with any patch or
// replacement that might select one of objs — patches, patchesJson6902,
// patchesStrategicMerge, replacements; inline or in a file. What the patch
// does is not examined.
func paygKustomizationConflicts(dir string, body []byte, objs []paygObject) error {
	var k struct {
		Patches               []map[string]any `yaml:"patches"`
		PatchesJSON6902       []map[string]any `yaml:"patchesJson6902"`
		PatchesStrategicMerge []string         `yaml:"patchesStrategicMerge"`
		Replacements          []map[string]any `yaml:"replacements"`
	}
	if yaml.Unmarshal(body, &k) != nil {
		return nil // not a kustomization this check can read
	}
	read := func(file string) (string, bool) {
		b, err := os.ReadFile(filepath.Join(dir, file))
		return string(b), err == nil
	}
	check := func(what string, target map[string]any, inline, file string) error {
		content := inline
		if file != "" {
			c, ok := read(file)
			if !ok && target == nil {
				return fmt.Errorf("%s %s cannot be read, so it might select a PAYG object", what, file)
			}
			content = c
		}
		for _, o := range objs {
			if o.patchMightSelect(target, content) {
				return fmt.Errorf("%s (%s) might select the %s the PAYG wiring depends on — only unrelated patches are allowed there; move it or apply the PAYG pieces by hand (platform/payg/README.md)", what, firstNonEmpty(file, "inline"), o)
			}
		}
		return nil
	}
	for _, p := range append(k.Patches, k.PatchesJSON6902...) {
		target, _ := p["target"].(map[string]any)
		inline, _ := p["patch"].(string)
		file, _ := p["path"].(string)
		if err := check("a patch", target, inline, file); err != nil {
			return err
		}
	}
	for _, p := range k.PatchesStrategicMerge {
		if strings.Contains(p, "\n") || strings.Contains(p, "kind:") {
			if err := check("a strategic-merge patch", nil, p, ""); err != nil {
				return err
			}
		} else if err := check("a strategic-merge patch", nil, "", p); err != nil {
			return err
		}
	}
	for _, r := range k.Replacements {
		if file, ok := r["path"].(string); ok {
			c, readOK := read(file)
			var rs []map[string]any
			if !readOK || yaml.Unmarshal([]byte(c), &rs) != nil {
				return fmt.Errorf("replacements file %s cannot be read, so it might select a PAYG object", file)
			}
			for _, inner := range rs {
				if err := paygReplacementConflicts(inner, objs); err != nil {
					return err
				}
			}
			continue
		}
		if err := paygReplacementConflicts(r, objs); err != nil {
			return err
		}
	}
	return nil
}

// paygReplacementConflicts refuses a replacement whose targets might select
// one of objs.
func paygReplacementConflicts(r map[string]any, objs []paygObject) error {
	targets, _ := r["targets"].([]any)
	for _, t := range targets {
		tm, _ := t.(map[string]any)
		sel, _ := tm["select"].(map[string]any)
		for _, o := range objs {
			if sel == nil || o.mightSelect(sel) {
				return fmt.Errorf("a replacement might select the %s the PAYG wiring depends on — apply the PAYG pieces by hand (platform/payg/README.md)", o)
			}
		}
	}
	return nil
}

func firstNonEmpty(a, b string) string {
	if a != "" {
		return a
	}
	return b
}
