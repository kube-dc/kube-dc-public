package clusterinit

import (
	"bytes"
	"errors"
	"fmt"
	"io"
	"os"
	"sort"

	"gopkg.in/yaml.v3"
)

// M4-T11 — customInterfaces patch.
//
// Per installer-prd §10.M4-T11: "takes --node-nic map; emits the
// inline Kustomize patch shape from clusters/cloud/infrastructure.yaml:67-89.
// Pure YAML emission."
//
// The patch adds a `spec.customInterfaces` field to the
// `kubeovn.io/v1/ProviderNetwork` named `${EXT_NET_NAME}` so each
// listed node uses a non-default NIC for the external network.
// Without this, kube-ovn assigns the default NIC inferred from the
// chart's values — which only works when every node has the same
// NIC name. For heterogeneous fleets (cloud uses
// enp94s0f0np0 / enp8s0f0 / eno1 across blade types) this patch is
// mandatory.
//
// Canonical shape (from clusters/cloud/infrastructure.yaml):
//
//	patches:
//	  - target:
//	      group: kubeovn.io
//	      version: v1
//	      kind: ProviderNetwork
//	      name: \$\{EXT_NET_NAME\}
//	    patch: |-
//	      - op: add
//	        path: /spec/customInterfaces
//	        value:
//	          - interface: enp8s0f0
//	            nodes:
//	              - node-d4
//	          - interface: eno1
//	            nodes:
//	              - node-a1
//
// Nodes sharing an interface are grouped (single `interface:` entry
// with multiple `nodes:`). The output is sorted deterministically so
// two consecutive scaffold runs against the same `--node-nic` set
// produce byte-identical files — critical for clean diff reviews.

// customInterfaceEntry is one element of the patch's `value:` list.
type customInterfaceEntry struct {
	Interface string   `yaml:"interface"`
	Nodes     []string `yaml:"nodes"`
}

// BuildCustomInterfacesPatch returns the YAML text for the
// `patches:` block to insert into the infra-core Kustomization's
// spec. Empty `nodeNICs` returns ("", nil) — caller decides whether
// to omit the patches block entirely.
//
// Determinism: nodes within an interface are sorted alphabetically;
// interface groups are sorted by interface name. Two runs of the
// same map produce identical output regardless of Go map iteration
// order.
func BuildCustomInterfacesPatch(nodeNICs map[string]string) (string, error) {
	if len(nodeNICs) == 0 {
		return "", nil
	}

	// Group nodes by interface.
	groups := make(map[string][]string)
	for node, iface := range nodeNICs {
		groups[iface] = append(groups[iface], node)
	}
	// Sort each group's node list + the group keys for stable
	// output.
	ifaces := make([]string, 0, len(groups))
	for iface := range groups {
		ifaces = append(ifaces, iface)
		sort.Strings(groups[iface])
	}
	sort.Strings(ifaces)

	entries := make([]customInterfaceEntry, 0, len(ifaces))
	for _, iface := range ifaces {
		entries = append(entries, customInterfaceEntry{
			Interface: iface,
			Nodes:     groups[iface],
		})
	}

	// The patch's `patch:` field is itself a YAML string (a JSON-
	// patch op), so we marshal twice: once for the inner op list,
	// then embed that string in the outer Kustomization patch.
	innerOp := []map[string]any{{
		"op":    "add",
		"path":  "/spec/customInterfaces",
		"value": entries,
	}}
	var innerBuf bytes.Buffer
	enc := yaml.NewEncoder(&innerBuf)
	enc.SetIndent(2)
	if err := enc.Encode(innerOp); err != nil {
		return "", fmt.Errorf("marshal customInterfaces inner op: %w", err)
	}
	if err := enc.Close(); err != nil {
		return "", fmt.Errorf("close inner encoder: %w", err)
	}
	innerYAML := innerBuf.String()

	// Build the outer Kustomize patches block. The target's
	// `name:` field needs the literal `${EXT_NET_NAME}` placeholder
	// (Flux's postBuild.substituteFrom replaces it at apply time);
	// the `\$\{…\}` escaping shown in clusters/cloud isn't actually
	// in the file content — that's docs-only escaping. The raw
	// file has `name: ${EXT_NET_NAME}`.
	outer := map[string]any{
		"patches": []map[string]any{{
			"target": map[string]any{
				"group":   "kubeovn.io",
				"version": "v1",
				"kind":    "ProviderNetwork",
				"name":    "${EXT_NET_NAME}",
			},
			"patch": innerYAML,
		}},
	}

	var outerBuf bytes.Buffer
	outEnc := yaml.NewEncoder(&outerBuf)
	outEnc.SetIndent(2)
	if err := outEnc.Encode(outer); err != nil {
		return "", fmt.Errorf("marshal patches block: %w", err)
	}
	if err := outEnc.Close(); err != nil {
		return "", fmt.Errorf("close outer encoder: %w", err)
	}
	return outerBuf.String(), nil
}

// ErrCustomIfacesNoInfraCore is returned by WriteCustomInterfacesPatch
// when the supplied infrastructure.yaml has no Kustomization named
// `infra-core` — indicates a malformed file (add-cluster.sh always
// writes infra-core).
var ErrCustomIfacesNoInfraCore = errors.New("init: infrastructure.yaml has no infra-core Kustomization to patch")

// WriteCustomInterfacesPatch parses `infraPath` as a multi-document
// YAML stream, finds the infra-core Kustomization, and appends a
// `spec.patches` block carrying the customInterfaces JSON-patch
// for the supplied node→NIC map.
//
// Empty `nodeNICs` is a no-op (returns nil; file unchanged).
//
// **YAML preservation note**: yaml.v3 Node manipulation preserves
// per-key comments AND order — the infrastructure.yaml the script
// writes has a header comment block per Kustomization, which is
// useful to keep so the diff reads naturally. We re-marshal the
// whole document via the Node tree to keep formatting consistent
// across keys; minor whitespace drift (one blank line vs two) is
// acceptable since the file is regenerated wholesale by T10's
// scaffold step + this T11 patch.
func WriteCustomInterfacesPatch(infraPath string, nodeNICs map[string]string) error {
	if len(nodeNICs) == 0 {
		return nil
	}

	body, err := os.ReadFile(infraPath)
	if err != nil {
		return fmt.Errorf("read %s: %w", infraPath, err)
	}

	// Decode every YAML doc in the stream.
	var docs []*yaml.Node
	dec := yaml.NewDecoder(bytes.NewReader(body))
	for {
		var n yaml.Node
		if err := dec.Decode(&n); err != nil {
			if errors.Is(err, io.EOF) {
				break
			}
			return fmt.Errorf("parse %s: %w", infraPath, err)
		}
		docs = append(docs, &n)
	}

	// Find the infra-core Kustomization + append the patches block.
	patched := false
	for _, doc := range docs {
		if !isInfraCoreKustomization(doc) {
			continue
		}
		if err := appendCustomInterfacesPatch(doc, nodeNICs); err != nil {
			return fmt.Errorf("append patches: %w", err)
		}
		patched = true
		break
	}
	if !patched {
		return ErrCustomIfacesNoInfraCore
	}

	// Re-emit the stream. yaml.v3 Encoder writes `---` separators
	// automatically between consecutive Encode calls. Atomic write
	// via temp + rename matches config.Env.Write semantics.
	var out bytes.Buffer
	enc := yaml.NewEncoder(&out)
	enc.SetIndent(2)
	for _, doc := range docs {
		if err := enc.Encode(doc); err != nil {
			return fmt.Errorf("re-encode %s: %w", infraPath, err)
		}
	}
	if err := enc.Close(); err != nil {
		return fmt.Errorf("close encoder: %w", err)
	}

	return atomicWrite(infraPath, out.Bytes(), 0o644)
}

// isInfraCoreKustomization reports whether `doc`'s root mapping is
// a Flux Kustomization with metadata.name = "infra-core".
func isInfraCoreKustomization(doc *yaml.Node) bool {
	if doc == nil || doc.Kind != yaml.DocumentNode || len(doc.Content) == 0 {
		return false
	}
	root := doc.Content[0]
	if root.Kind != yaml.MappingNode {
		return false
	}
	kind := mappingGet(root, "kind")
	if kind == nil || kind.Value != "Kustomization" {
		return false
	}
	meta := mappingGet(root, "metadata")
	if meta == nil || meta.Kind != yaml.MappingNode {
		return false
	}
	name := mappingGet(meta, "name")
	return name != nil && name.Value == "infra-core"
}

// appendCustomInterfacesPatch walks doc.spec, appending (or
// replacing) the `patches:` field with a JSON-patch op that
// targets the ProviderNetwork named ${EXT_NET_NAME}.
//
// If `spec.patches` already exists, it's REPLACED — operator
// re-running scaffold with a different --node-nic set shouldn't
// accumulate stale entries. (T11 is the sole owner of that key;
// adopting an existing infrastructure.yaml outside the
// add-cluster.sh path is a future concern.)
func appendCustomInterfacesPatch(doc *yaml.Node, nodeNICs map[string]string) error {
	root := doc.Content[0]
	spec := mappingGet(root, "spec")
	if spec == nil || spec.Kind != yaml.MappingNode {
		return fmt.Errorf("infra-core has no `spec` mapping")
	}

	// Build the inner JSON-patch op as a YAML Node tree so we
	// can attach it as a single key inside spec.
	entries := buildSortedCustomInterfaceEntries(nodeNICs)
	innerOpsNode, err := jsonPatchOpsNode(entries)
	if err != nil {
		return err
	}
	// The Kustomize patches[].patch field is a string (JSON-patch
	// payload), so we marshal innerOpsNode to text + wrap with a
	// literal-block scalar Node so the |- shape is preserved in
	// the output.
	var innerBuf bytes.Buffer
	enc := yaml.NewEncoder(&innerBuf)
	enc.SetIndent(2)
	if err := enc.Encode(innerOpsNode); err != nil {
		return fmt.Errorf("marshal inner ops: %w", err)
	}
	if err := enc.Close(); err != nil {
		return err
	}
	innerStr := innerBuf.String()

	patchesNode := &yaml.Node{
		Kind: yaml.SequenceNode,
		Tag:  "!!seq",
		Content: []*yaml.Node{
			{
				Kind: yaml.MappingNode,
				Tag:  "!!map",
				Content: []*yaml.Node{
					{Kind: yaml.ScalarNode, Value: "target"},
					{
						Kind: yaml.MappingNode,
						Tag:  "!!map",
						Content: []*yaml.Node{
							{Kind: yaml.ScalarNode, Value: "group"}, {Kind: yaml.ScalarNode, Value: "kubeovn.io"},
							{Kind: yaml.ScalarNode, Value: "version"}, {Kind: yaml.ScalarNode, Value: "v1"},
							{Kind: yaml.ScalarNode, Value: "kind"}, {Kind: yaml.ScalarNode, Value: "ProviderNetwork"},
							{Kind: yaml.ScalarNode, Value: "name"}, {Kind: yaml.ScalarNode, Value: "${EXT_NET_NAME}"},
						},
					},
					{Kind: yaml.ScalarNode, Value: "patch"},
					{Kind: yaml.ScalarNode, Tag: "!!str", Style: yaml.LiteralStyle, Value: innerStr},
				},
			},
		},
	}

	// Upsert spec.patches.
	mappingSet(spec, "patches", patchesNode)
	return nil
}

// buildSortedCustomInterfaceEntries grouped + sorted (shared with
// BuildCustomInterfacesPatch but extracted so we can reuse it for
// the Node-tree construction).
func buildSortedCustomInterfaceEntries(nodeNICs map[string]string) []customInterfaceEntry {
	groups := make(map[string][]string)
	for node, iface := range nodeNICs {
		groups[iface] = append(groups[iface], node)
	}
	ifaces := make([]string, 0, len(groups))
	for iface := range groups {
		ifaces = append(ifaces, iface)
		sort.Strings(groups[iface])
	}
	sort.Strings(ifaces)
	out := make([]customInterfaceEntry, 0, len(ifaces))
	for _, iface := range ifaces {
		out = append(out, customInterfaceEntry{Interface: iface, Nodes: groups[iface]})
	}
	return out
}

// jsonPatchOpsNode builds the yaml.Node representing the JSON-patch
// `- op: add / path: /spec/customInterfaces / value: [entries]`
// list. Used by appendCustomInterfacesPatch.
func jsonPatchOpsNode(entries []customInterfaceEntry) (*yaml.Node, error) {
	// Build the `value` sequence first.
	valueSeq := &yaml.Node{Kind: yaml.SequenceNode, Tag: "!!seq"}
	for _, e := range entries {
		entryMap := &yaml.Node{
			Kind: yaml.MappingNode,
			Tag:  "!!map",
			Content: []*yaml.Node{
				{Kind: yaml.ScalarNode, Value: "interface"},
				{Kind: yaml.ScalarNode, Value: e.Interface},
				{Kind: yaml.ScalarNode, Value: "nodes"},
			},
		}
		nodesSeq := &yaml.Node{Kind: yaml.SequenceNode, Tag: "!!seq"}
		for _, n := range e.Nodes {
			nodesSeq.Content = append(nodesSeq.Content, &yaml.Node{Kind: yaml.ScalarNode, Value: n})
		}
		entryMap.Content = append(entryMap.Content, nodesSeq)
		valueSeq.Content = append(valueSeq.Content, entryMap)
	}

	opMap := &yaml.Node{
		Kind: yaml.MappingNode,
		Tag:  "!!map",
		Content: []*yaml.Node{
			{Kind: yaml.ScalarNode, Value: "op"}, {Kind: yaml.ScalarNode, Value: "add"},
			{Kind: yaml.ScalarNode, Value: "path"}, {Kind: yaml.ScalarNode, Value: "/spec/customInterfaces"},
			{Kind: yaml.ScalarNode, Value: "value"},
			valueSeq,
		},
	}
	ops := &yaml.Node{Kind: yaml.SequenceNode, Tag: "!!seq", Content: []*yaml.Node{opMap}}
	return ops, nil
}

// --- yaml.Node helpers ---

// mappingGet returns the value Node for `key` in a mapping `m`, or
// nil if absent. yaml.v3 stores mapping content as alternating
// key/value pairs in `m.Content`; this helper hides that detail
// from callers.
func mappingGet(m *yaml.Node, key string) *yaml.Node {
	if m == nil || m.Kind != yaml.MappingNode {
		return nil
	}
	for i := 0; i+1 < len(m.Content); i += 2 {
		if m.Content[i].Value == key {
			return m.Content[i+1]
		}
	}
	return nil
}

// mappingSet upserts a key/value pair in mapping `m`. Replaces an
// existing value when the key is already present; appends
// otherwise.
func mappingSet(m *yaml.Node, key string, value *yaml.Node) {
	if m == nil || m.Kind != yaml.MappingNode {
		return
	}
	for i := 0; i+1 < len(m.Content); i += 2 {
		if m.Content[i].Value == key {
			m.Content[i+1] = value
			return
		}
	}
	m.Content = append(m.Content,
		&yaml.Node{Kind: yaml.ScalarNode, Value: key},
		value,
	)
}

// --- atomic write helper ---

// atomicWrite is the engine-local atomic-file-write helper. Mirrors
// config.Env.Write semantics (temp + chmod + rename) so T11 + T10
// + T13 use the same write pattern. Kept in clusterinit/ rather
// than a shared package because it's a 20-line helper and the
// engine doesn't have a generic "file ops" sub-package yet.
func atomicWrite(path string, data []byte, perm os.FileMode) error {
	dir := dirOf(path)
	tmp, err := os.CreateTemp(dir, baseOf(path)+".*.tmp")
	if err != nil {
		return fmt.Errorf("create temp in %s: %w", dir, err)
	}
	tmpPath := tmp.Name()
	if _, werr := tmp.Write(data); werr != nil {
		_ = tmp.Close()
		_ = os.Remove(tmpPath)
		return fmt.Errorf("write %s: %w", tmpPath, werr)
	}
	if err := tmp.Chmod(perm); err != nil {
		_ = tmp.Close()
		_ = os.Remove(tmpPath)
		return fmt.Errorf("chmod %s: %w", tmpPath, err)
	}
	if err := tmp.Close(); err != nil {
		_ = os.Remove(tmpPath)
		return fmt.Errorf("close %s: %w", tmpPath, err)
	}
	if err := os.Rename(tmpPath, path); err != nil {
		_ = os.Remove(tmpPath)
		return fmt.Errorf("rename %s -> %s: %w", tmpPath, path, err)
	}
	return nil
}

func dirOf(path string) string {
	for i := len(path) - 1; i >= 0; i-- {
		if path[i] == '/' {
			if i == 0 {
				return "/"
			}
			return path[:i]
		}
	}
	return "."
}

func baseOf(path string) string {
	for i := len(path) - 1; i >= 0; i-- {
		if path[i] == '/' {
			return path[i+1:]
		}
	}
	return path
}
