package clusterinit

import (
	"context"
	"fmt"
	"io"
	"sort"
	"strings"

	"github.com/shalb/kube-dc/cli/internal/bootstrap/ports"
)

// DefaultIngressNodeLabel is the label the host-bind front-door component selects on.
// It must match INGRESS_NODE_LABEL in the generated cluster-config.env and the
// nodeSelector in platform/gateway-config/components/host-bind.
const DefaultIngressNodeLabel = "kube-dc.com/ingress"

// nodeLabeler is the narrow slice of ports.K8sClient this step needs. Declared so the
// logic can be tested without standing up the whole scenario-driven mock.
type nodeLabeler interface {
	NodeLabels(ctx context.Context) (map[string]map[string]string, error)
	SetNodeLabel(ctx context.Context, node, key, value string) error
}

// EnsureIngressNodeLabels applies the ingress label to exactly the planned ingress
// nodes, and refuses rather than guessing when the live cluster does not match the plan.
//
// WHY THIS EXISTS
//
//	The front door is a host-bound Envoy placed by nodeSelector on this label, with
//	REQUIRED pod anti-affinity and replicas == the number of labelled nodes. Nothing in
//	the installer ever applied the label, so a freshly bootstrapped cluster rendered N
//	Envoy replicas that were all unschedulable: every manifest correct, Flux green, and
//	no front door at all on day one. Printing the intended node set is not enough — the
//	label has to exist before the platform Kustomization is allowed to reconcile.
//
// FAIL-CLOSED RULES, each for a failure seen during the 2026-08-08 fleet migration:
//
//   - Empty node set: refuse. An unlabelled cluster is a dark front door, and silently
//     doing nothing is how that shipped in the first place.
//   - A planned node that does not exist: refuse before writing ANY label, so a typo
//     cannot half-label a cluster. Readiness and cordon state are deliberately NOT
//     checked here: the label is a placement precondition, not a health gate, and the
//     port exposes only labels. scripts/frontdoor-check.sh preflight is where liveness
//     of the resulting placement is asserted.
//   - A node OUTSIDE the planned set already carrying the label: refuse. That label may
//     be live on a serving cluster; removing it would take that node out of the front
//     door, and adding ours alongside changes the replica arithmetic silently. This is
//     a human decision.
//   - After writing, re-read and verify. A merge patch that appears to succeed but does
//     not land leaves the same dark front door, and the whole point of this step is that
//     nothing downstream notices.
//
// EnsureIngressNodeLabels is the production entry point; the logic lives in
// ensureIngressNodeLabelsFor against the narrow interface above.
func EnsureIngressNodeLabels(
	ctx context.Context,
	k8s ports.K8sClient,
	nodes []string,
	label string,
	out io.Writer,
) error {
	return ensureIngressNodeLabelsFor(ctx, k8s, nodes, label, out)
}

func ensureIngressNodeLabelsFor(
	ctx context.Context,
	k8s nodeLabeler,
	nodes []string,
	label string,
	out io.Writer,
) error {
	if label == "" {
		label = DefaultIngressNodeLabel
	}
	if len(nodes) == 0 {
		return fmt.Errorf(
			"ingress node set is empty: the host-bind front door selects nodes by %s, "+
				"so with none labelled every Envoy replica is unschedulable and the "+
				"cluster comes up with no front door", label)
	}

	live, err := k8s.NodeLabels(ctx)
	if err != nil {
		return fmt.Errorf("read node labels: %w", err)
	}

	want := make(map[string]bool, len(nodes))
	for _, n := range nodes {
		want[n] = true
	}

	// Validate the whole plan BEFORE writing anything.
	var missing []string
	for _, n := range nodes {
		if _, ok := live[n]; !ok {
			missing = append(missing, n)
		}
	}
	if len(missing) > 0 {
		sort.Strings(missing)
		known := make([]string, 0, len(live))
		for n := range live {
			known = append(known, n)
		}
		sort.Strings(known)
		return fmt.Errorf(
			"ingress nodes not found in the cluster: %s (nodes present: %s)",
			strings.Join(missing, ", "), strings.Join(known, ", "))
	}

	// A node outside the plan already carrying the label is not ours to remove.
	var foreign []string
	for node, labels := range live {
		if labels[label] == "true" && !want[node] {
			foreign = append(foreign, node)
		}
	}
	if len(foreign) > 0 {
		sort.Strings(foreign)
		return fmt.Errorf(
			"node(s) outside the planned ingress set already carry %s=true: %s. "+
				"Refusing to continue: that label may be serving traffic on this "+
				"cluster, removing it would drop those nodes out of the front door, and "+
				"adding ours alongside changes the replica arithmetic silently. Resolve "+
				"by hand, then re-run",
			label, strings.Join(foreign, ", "))
	}

	var applied, already []string
	for _, n := range nodes {
		if live[n][label] == "true" {
			already = append(already, n)
			continue
		}
		if err := k8s.SetNodeLabel(ctx, n, label, "true"); err != nil {
			return fmt.Errorf("label ingress node %s: %w", n, err)
		}
		applied = append(applied, n)
	}

	// Verify after writing. The label existing is the whole precondition; trusting the
	// patch response is exactly the kind of assumption that produced a dark front door.
	after, err := k8s.NodeLabels(ctx)
	if err != nil {
		return fmt.Errorf("re-read node labels to verify: %w", err)
	}
	var unverified []string
	for _, n := range nodes {
		if after[n][label] != "true" {
			unverified = append(unverified, n)
		}
	}
	if len(unverified) > 0 {
		sort.Strings(unverified)
		return fmt.Errorf(
			"labelled %s=true but a re-read does not show it on: %s",
			label, strings.Join(unverified, ", "))
	}

	if out != nil {
		sort.Strings(applied)
		sort.Strings(already)
		if len(applied) > 0 {
			fmt.Fprintf(out, "  labelled %s=true: %s\n", label, strings.Join(applied, ", "))
		}
		if len(already) > 0 {
			fmt.Fprintf(out, "  already %s=true: %s\n", label, strings.Join(already, ", "))
		}
		fmt.Fprintf(out, "  ingress set is %d node(s); ENVOY_REPLICAS must equal that\n", len(nodes))
	}
	return nil
}
