package adopt

import (
	"context"
	"errors"
	"testing"

	"github.com/shalb/kube-dc/cli/internal/bootstrap/ports"
)

type fakeInspector struct {
	crds     []string
	nss      []string
	graph    ports.Graph
	charts   map[string]string
	crFields map[string]string // CR name → version (GetResourceFieldFirst)
	crErr    error
	crdErr   error
	graphErr error
	chartErr error
}

func (f fakeInspector) ListCRDs(context.Context) ([]string, error) {
	return f.crds, f.crdErr
}
func (f fakeInspector) ListNamespaces(context.Context) ([]string, error) {
	return f.nss, nil
}
func (f fakeInspector) DiscoverFluxGraph(context.Context) (ports.Graph, error) {
	return f.graph, f.graphErr
}
func (f fakeInspector) HelmReleaseChartVersions(context.Context) (map[string]string, error) {
	return f.charts, f.chartErr
}
func (f fakeInspector) GetResourceFieldFirst(_ context.Context, _, _, _, _, name string, _ ...string) (string, error) {
	return f.crFields[name], f.crErr
}

func findingFor(res *Result, name string) *Finding {
	for i := range res.Findings {
		if res.Findings[i].Component.Name == name {
			return &res.Findings[i]
		}
	}
	return nil
}

func TestDetect_ViaCRD(t *testing.T) {
	res, err := Detect(context.Background(), fakeInspector{
		crds: []string{"certificates.cert-manager.io", "kubevirts.kubevirt.io"},
	})
	if err != nil {
		t.Fatal(err)
	}
	cm := findingFor(res, "cert-manager")
	if cm == nil || cm.Via != "CRD certificates.cert-manager.io" {
		t.Errorf("cert-manager should be detected via CRD: %+v", cm)
	}
	if cm.Recommend != DecisionAdopt {
		t.Errorf("default recommendation should be adopt, got %q", cm.Recommend)
	}
	if findingFor(res, "kubevirt") == nil {
		t.Error("kubevirt should be detected via CRD")
	}
}

func TestDetect_NamespaceFallback(t *testing.T) {
	// ingress-nginx has no CRD signature — detected by namespace only.
	res, err := Detect(context.Background(), fakeInspector{nss: []string{"ingress-nginx"}})
	if err != nil {
		t.Fatal(err)
	}
	ing := findingFor(res, "ingress-nginx")
	if ing == nil || ing.Via != "namespace ingress-nginx" {
		t.Errorf("ingress-nginx should be detected via namespace: %+v", ing)
	}
	if ing.Component.Note == "" {
		t.Error("ingress-nginx should carry the envoy-gateway note")
	}
	// P2b: a component kube-dc has NO base for (FleetPath "(none)") must
	// recommend skip, not adopt.
	if ing.Recommend != DecisionSkip {
		t.Errorf("ingress-nginx (no base) should recommend skip, got %q", ing.Recommend)
	}
}

func TestDetect_CRDPreferredOverNamespace(t *testing.T) {
	res, err := Detect(context.Background(), fakeInspector{
		crds: []string{"certificates.cert-manager.io"},
		nss:  []string{"cert-manager"},
	})
	if err != nil {
		t.Fatal(err)
	}
	cm := findingFor(res, "cert-manager")
	if cm == nil || cm.Via[:3] != "CRD" {
		t.Errorf("CRD signal should win over namespace: %+v", cm)
	}
}

func TestDetect_Greenfield(t *testing.T) {
	res, err := Detect(context.Background(), fakeInspector{})
	if err != nil {
		t.Fatal(err)
	}
	if len(res.Findings) != 0 {
		t.Errorf("greenfield should detect nothing, got %+v", res.Findings)
	}
	if res.FluxInstalled {
		t.Error("greenfield: FluxInstalled should be false")
	}
}

func TestDetect_FluxInstalledFlag(t *testing.T) {
	res, err := Detect(context.Background(), fakeInspector{
		graph: ports.Graph{Nodes: []ports.GraphNode{{Name: "infrastructure"}}},
	})
	if err != nil {
		t.Fatal(err)
	}
	if !res.FluxInstalled {
		t.Error("a non-empty Flux graph should set FluxInstalled")
	}
	// A Flux-graph error must NOT fail detection (greenfield is fine).
	if _, err := Detect(context.Background(), fakeInspector{graphErr: errors.New("no flux")}); err != nil {
		t.Errorf("flux-graph error should be tolerated, got %v", err)
	}
}

func TestDetect_SortedByName(t *testing.T) {
	res, err := Detect(context.Background(), fakeInspector{
		crds: []string{"kubevirts.kubevirt.io", "certificates.cert-manager.io", "cephclusters.ceph.rook.io"},
	})
	if err != nil {
		t.Fatal(err)
	}
	for i := 1; i < len(res.Findings); i++ {
		if res.Findings[i-1].Component.Name > res.Findings[i].Component.Name {
			t.Errorf("findings not sorted: %s before %s", res.Findings[i-1].Component.Name, res.Findings[i].Component.Name)
		}
	}
}

func TestDetect_Errors(t *testing.T) {
	if _, err := Detect(context.Background(), nil); !errors.Is(err, ErrMissingDependency) {
		t.Errorf("nil inspector → ErrMissingDependency, got %v", err)
	}
	if _, err := Detect(context.Background(), fakeInspector{crdErr: errors.New("rbac denied")}); err == nil {
		t.Error("ListCRDs error must propagate")
	}
}
