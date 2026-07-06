package screens

import (
	"fmt"
	"sort"
	"strings"

	"github.com/shalb/kube-dc/cli/internal/bootstrap/ports"
)

// waterfall.go is the T7 view core: a pure renderer that turns a Flux
// reconcile Graph (ports.Graph, from DiscoverFluxGraph) into a
// dependency-ordered "waterfall" — each Kustomization indented under the
// depth of its longest dependency chain, so an operator watching an
// install sees the canonical
// flux-system → infra-cni → infra-core → infra-object-storage → platform
// cascade converge top-to-bottom.
//
// FleetModel already shows a FLAT per-cluster Kustomization list; the
// waterfall adds the dependsOn structure it lacks. Kept pure (Graph in,
// string out) so it's headless-testable and the live tab is a thin shell
// over it.

// WaterfallGlyph is the status marker for a node, exported so the tab
// and tests share one source of truth.
func WaterfallGlyph(n ports.GraphNode) string {
	switch {
	case n.Ready:
		return "✓"
	case n.Reconciling:
		return "◑"
	default:
		return "·"
	}
}

// waterfallRow is one rendered line, pre-layout.
type waterfallRow struct {
	depth int
	node  ports.GraphNode
}

// RenderWaterfall renders g as a depth-indented reconcile tree. Ordering
// is deterministic: by (depth, name). Nodes in a dependency CYCLE (or
// depending on a missing node) are still shown — depth falls back to 0
// and a cycle note is appended — so a malformed graph never hides rows
// or hangs. Returns a friendly line for an empty graph.
func RenderWaterfall(g ports.Graph) string {
	if len(g.Nodes) == 0 {
		return "no Flux Kustomizations found (cluster not bootstrapped, or flux-system absent)"
	}

	byName := make(map[string]ports.GraphNode, len(g.Nodes))
	for _, n := range g.Nodes {
		byName[n.Name] = n
	}
	depths, cyclic := waterfallDepths(g.Nodes, byName)

	rows := make([]waterfallRow, 0, len(g.Nodes))
	for _, n := range g.Nodes {
		rows = append(rows, waterfallRow{depth: depths[n.Name], node: n})
	}
	sort.SliceStable(rows, func(i, j int) bool {
		if rows[i].depth != rows[j].depth {
			return rows[i].depth < rows[j].depth
		}
		return rows[i].node.Name < rows[j].node.Name
	})

	var b strings.Builder
	var ready, reconciling, pending int
	for _, r := range rows {
		indent := strings.Repeat("  ", r.depth)
		fmt.Fprintf(&b, "%s%s %s", indent, WaterfallGlyph(r.node), r.node.Name)
		if r.node.Reconciling {
			b.WriteString("  reconciling")
		} else if !r.node.Ready && r.node.Reason != "" {
			fmt.Fprintf(&b, "  %s", r.node.Reason)
		}
		b.WriteString("\n")
		switch {
		case r.node.Ready:
			ready++
		case r.node.Reconciling:
			reconciling++
		default:
			pending++
		}
	}
	fmt.Fprintf(&b, "\n%d ready · %d reconciling · %d pending", ready, reconciling, pending)
	if len(cyclic) > 0 {
		sort.Strings(cyclic)
		fmt.Fprintf(&b, "\n⚠ dependency cycle or missing dependsOn target involving: %s", strings.Join(cyclic, ", "))
	}
	return b.String()
}

// waterfallDepths computes each node's depth = longest dependency chain
// to a root (a node with no in-graph dependencies). Memoized DFS with a
// visiting-set so a cycle can't recurse forever: any node on a cycle (or
// pointing at a name not in the graph) resolves to depth 0 and is
// reported in `cyclic`. A dependsOn target absent from the graph is
// treated as an external/satisfied root (depth contribution 0) but the
// depending node is flagged so the operator notices the dangling ref.
func waterfallDepths(nodes []ports.GraphNode, byName map[string]ports.GraphNode) (map[string]int, []string) {
	depth := make(map[string]int, len(nodes))
	state := make(map[string]int) // 0 unvisited, 1 visiting, 2 done
	cyclicSet := map[string]bool{}

	var visit func(name string) int
	visit = func(name string) int {
		switch state[name] {
		case 2:
			return depth[name]
		case 1:
			// On the current DFS stack → cycle. Break it.
			cyclicSet[name] = true
			return 0
		}
		node, ok := byName[name]
		if !ok {
			// Dangling dependsOn target — treat as an external root.
			return 0
		}
		state[name] = 1
		best := 0
		for _, dep := range node.DependsOn {
			if _, present := byName[dep]; !present {
				// depending on something outside the graph — note it.
				cyclicSet[name] = true
				continue
			}
			if d := visit(dep) + 1; d > best {
				best = d
			}
		}
		depth[name] = best
		state[name] = 2
		return best
	}

	for _, n := range nodes {
		visit(n.Name)
	}
	cyclic := make([]string, 0, len(cyclicSet))
	for name := range cyclicSet {
		cyclic = append(cyclic, name)
	}
	return depth, cyclic
}
