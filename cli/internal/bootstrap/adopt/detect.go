// Package adopt powers `kube-dc bootstrap adopt` — the pre-install
// inventory for an EXISTING cluster. Before kube-dc's Flux installs its
// own kube-ovn / cert-manager / kubevirt / …, adopt detects which of
// those a cluster ALREADY has (by CRD, most reliably) and reports a
// per-component decision (adopt = keep it + exclude from kube-dc's Flux;
// or replace = let kube-dc manage), so the operator never silently
// clobbers existing infra.
//
// v1 is READ-ONLY: it detects + advises + prints the exact fleet-overlay
// edit for each decision. It does NOT rewrite the fleet overlay — that
// (the risky half: `resources:` rewriting / suspend / patches, frozen
// into the hashed Plan) is a deliberate follow-up so a reviewed advisory
// lands before any automated mutation.
package adopt

import (
	"context"
	"errors"
	"sort"

	"github.com/shalb/kube-dc/cli/internal/bootstrap/ports"
)

// Decision is the recommended action for a detected component.
type Decision string

const (
	// DecisionAdopt: keep the existing component, exclude it from
	// kube-dc's Flux (kube-dc won't manage or clobber it). The safe
	// default for a foreign pre-existing component.
	DecisionAdopt Decision = "adopt"
	// DecisionReplace: let kube-dc's Flux take over management. Riskier
	// (can disrupt live issuers/certs/webhooks mid-flight) — never the
	// default; the operator opts in.
	DecisionReplace Decision = "replace"
)

// Component is one thing kube-dc would install, with the signatures that
// prove it's already on the cluster + where kube-dc installs it.
type Component struct {
	Name string
	// CRDs — any present is a strong "already installed" signal.
	CRDs []string
	// Namespaces — a weaker fallback signal (only consulted when no CRD
	// signature matches).
	Namespaces []string
	// FleetPath is the fleet overlay entry that installs it (the line the
	// operator omits from clusters/<name>/{platform,infrastructure}/
	// kustomization.yaml to "adopt" = keep-and-exclude).
	FleetPath string
	// Note carries component-specific caveats (e.g. no ingress-nginx base).
	Note string
}

// catalog is the set of components kube-dc installs that a pre-existing
// cluster might already carry. CRD signatures are the primary detector;
// namespaces are a fallback. Mapping mirrors kube-dc-fleet's adoption
// table (gitops-migration-plan.md §2.2).
var catalog = []Component{
	{Name: "cert-manager", CRDs: []string{"certificates.cert-manager.io", "clusterissuers.cert-manager.io"}, Namespaces: []string{"cert-manager"}, FleetPath: "infrastructure/cert-manager"},
	{Name: "kube-ovn (CNI)", CRDs: []string{"subnets.kubeovn.io", "vpcs.kubeovn.io"}, FleetPath: "infrastructure/cni", Note: "kube-ovn is kube-dc's CNI — replacing a different CNI is a cluster-wide network change"},
	{Name: "envoy-gateway", CRDs: []string{"envoyproxies.gateway.envoyproxy.io"}, Namespaces: []string{"envoy-gateway-system"}, FleetPath: "infrastructure/envoy-gateway"},
	{Name: "kubevirt", CRDs: []string{"kubevirts.kubevirt.io"}, Namespaces: []string{"kubevirt"}, FleetPath: "platform/kubevirt"},
	{Name: "cdi", CRDs: []string{"datavolumes.cdi.kubevirt.io", "cdis.cdi.kubevirt.io"}, Namespaces: []string{"cdi"}, FleetPath: "platform/kubevirt", Note: "CDI is bundled under platform/kubevirt"},
	{Name: "kamaji", CRDs: []string{"tenantcontrolplanes.kamaji.clastix.io"}, Namespaces: []string{"kamaji-system"}, FleetPath: "platform/kamaji"},
	{Name: "rook-ceph", CRDs: []string{"cephclusters.ceph.rook.io"}, Namespaces: []string{"rook-ceph"}, FleetPath: "infrastructure/rook-ceph"},
	{Name: "monitoring (prometheus-operator)", CRDs: []string{"prometheuses.monitoring.coreos.com"}, Namespaces: []string{"monitoring"}, FleetPath: "platform/monitoring"},
	{Name: "cnpg", CRDs: []string{"clusters.postgresql.cnpg.io"}, Namespaces: []string{"cnpg-system"}, FleetPath: "infrastructure/cnpg"},
	{Name: "metallb", CRDs: []string{"ipaddresspools.metallb.io"}, Namespaces: []string{"metallb-system"}, FleetPath: "addons/metallb"},
	{Name: "keycloak", CRDs: []string{"keycloaks.k8s.keycloak.org"}, Namespaces: []string{"keycloak"}, FleetPath: "platform/keycloak"},
	{Name: "ingress-nginx", Namespaces: []string{"ingress-nginx"}, FleetPath: "(none)", Note: "kube-dc has NO ingress-nginx base — it uses envoy-gateway; keep yours (adopt) or migrate to envoy-gateway"},
}

// Finding is one detected component + how it was detected + the advice.
type Finding struct {
	Component Component
	Via       string // "CRD certificates.cert-manager.io" | "namespace cert-manager"
	Recommend Decision
}

// Result is the adopt inventory outcome.
type Result struct {
	FluxInstalled bool
	Findings      []Finding // detected components, sorted by name
}

// Inspector is the minimal cluster-read surface adopt needs. ports.K8sClient
// satisfies it (so tests get a tiny fake, not the full client).
type Inspector interface {
	ListCRDs(ctx context.Context) ([]string, error)
	ListNamespaces(ctx context.Context) ([]string, error)
	DiscoverFluxGraph(ctx context.Context) (ports.Graph, error)
}

// ErrMissingDependency is returned when no Inspector is supplied.
var ErrMissingDependency = errors.New("adopt: missing Inspector")

// Detect inventories the cluster: which catalog components are already
// present, and whether Flux is installed. CRD presence wins; namespace
// presence is the fallback signal. The recommendation is always Adopt in
// v1 (keep + exclude — the safe default); Replace is an operator opt-in
// surfaced by the command, not auto-recommended.
func Detect(ctx context.Context, insp Inspector) (*Result, error) {
	if insp == nil {
		return nil, ErrMissingDependency
	}
	crds, err := insp.ListCRDs(ctx)
	if err != nil {
		return nil, err
	}
	nss, err := insp.ListNamespaces(ctx)
	if err != nil {
		return nil, err
	}
	crdSet := toSet(crds)
	nsSet := toSet(nss)

	res := &Result{}
	// Flux presence (best-effort — a missing/absent graph just means
	// greenfield, not an error).
	if g, gerr := insp.DiscoverFluxGraph(ctx); gerr == nil && len(g.Nodes) > 0 {
		res.FluxInstalled = true
	}

	for _, comp := range catalog {
		if via, ok := detectOne(comp, crdSet, nsSet); ok {
			res.Findings = append(res.Findings, Finding{
				Component: comp,
				Via:       via,
				Recommend: DecisionAdopt,
			})
		}
	}
	sort.Slice(res.Findings, func(i, j int) bool {
		return res.Findings[i].Component.Name < res.Findings[j].Component.Name
	})
	return res, nil
}

// detectOne reports whether comp is present + how it was detected. A CRD
// match is preferred (strong); a namespace match is the fallback.
func detectOne(comp Component, crdSet, nsSet map[string]bool) (string, bool) {
	for _, crd := range comp.CRDs {
		if crdSet[crd] {
			return "CRD " + crd, true
		}
	}
	for _, ns := range comp.Namespaces {
		if nsSet[ns] {
			return "namespace " + ns, true
		}
	}
	return "", false
}

func toSet(items []string) map[string]bool {
	m := make(map[string]bool, len(items))
	for _, it := range items {
		m[it] = true
	}
	return m
}
