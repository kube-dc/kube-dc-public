package screens

import (
	"strings"
	"testing"

	"github.com/shalb/kube-dc/cli/internal/bootstrap/ports"
)

func TestRenderWaterfall_Empty(t *testing.T) {
	out := RenderWaterfall(ports.Graph{})
	if !strings.Contains(out, "no Flux Kustomizations") {
		t.Errorf("empty graph should render a friendly note, got: %q", out)
	}
}

func TestRenderWaterfall_DependencyOrderAndIndent(t *testing.T) {
	// Canonical fleet cascade, deliberately given OUT of order to prove
	// the renderer orders by dependency depth, not input order.
	g := ports.Graph{Nodes: []ports.GraphNode{
		{Name: "platform", DependsOn: []string{"infra-object-storage"}, Reconciling: true},
		{Name: "infra-cni", DependsOn: []string{"flux-system"}, Ready: true},
		{Name: "flux-system", Ready: true},
		{Name: "infra-object-storage", DependsOn: []string{"infra-core"}},
		{Name: "infra-core", DependsOn: []string{"infra-cni"}, Ready: true},
	}}
	out := RenderWaterfall(g)
	lines := strings.Split(out, "\n")

	// Depth order: flux-system(0) < infra-cni(1) < infra-core(2) <
	// infra-object-storage(3) < platform(4).
	order := []string{"flux-system", "infra-cni", "infra-core", "infra-object-storage", "platform"}
	var seen []string
	for _, l := range lines {
		for _, name := range order {
			if strings.Contains(l, name) {
				seen = append(seen, name)
			}
		}
	}
	for i := range order {
		if i >= len(seen) || seen[i] != order[i] {
			t.Fatalf("waterfall not in dependency order:\nwant %v\ngot  %v\n---\n%s", order, seen, out)
		}
	}

	// Indentation grows with depth: platform (depth 4) indented deeper
	// than flux-system (depth 0).
	fluxIndent := leadingSpaces(lineWith(lines, "flux-system"))
	platIndent := leadingSpaces(lineWith(lines, "platform"))
	if platIndent <= fluxIndent {
		t.Errorf("deeper node should be indented more: flux=%d platform=%d", fluxIndent, platIndent)
	}

	// Glyphs + summary.
	if !strings.Contains(out, "✓ flux-system") {
		t.Errorf("ready node should carry ✓:\n%s", out)
	}
	if !strings.Contains(out, "◑ platform") || !strings.Contains(out, "reconciling") {
		t.Errorf("reconciling node should carry ◑ + label:\n%s", out)
	}
	if !strings.Contains(out, "3 ready · 1 reconciling · 1 pending") {
		t.Errorf("summary counts wrong:\n%s", out)
	}
}

func TestRenderWaterfall_CycleDoesNotHang(t *testing.T) {
	// a → b → a is a cycle; must terminate + flag, not recurse forever.
	g := ports.Graph{Nodes: []ports.GraphNode{
		{Name: "a", DependsOn: []string{"b"}},
		{Name: "b", DependsOn: []string{"a"}},
		{Name: "c", Ready: true}, // independent, must still render
	}}
	out := RenderWaterfall(g)
	if !strings.Contains(out, "dependency cycle") {
		t.Errorf("cycle should be flagged:\n%s", out)
	}
	for _, name := range []string{"a", "b", "c"} {
		if !strings.Contains(out, name) {
			t.Errorf("cycle must not hide node %q:\n%s", name, out)
		}
	}
}

func TestRenderWaterfall_DanglingDependsOnFlagged(t *testing.T) {
	// depends on a name not in the graph → treated as external root, but
	// the depending node is flagged so a typo'd dependsOn is visible.
	g := ports.Graph{Nodes: []ports.GraphNode{
		{Name: "platform", DependsOn: []string{"ghost"}, Ready: true},
	}}
	out := RenderWaterfall(g)
	if !strings.Contains(out, "platform") {
		t.Fatalf("node must render:\n%s", out)
	}
	if !strings.Contains(out, "missing dependsOn") {
		t.Errorf("dangling dependsOn should be flagged:\n%s", out)
	}
}

func lineWith(lines []string, sub string) string {
	for _, l := range lines {
		if strings.Contains(l, sub) {
			return l
		}
	}
	return ""
}

func leadingSpaces(s string) int {
	return len(s) - len(strings.TrimLeft(s, " "))
}
