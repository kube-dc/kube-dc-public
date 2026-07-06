package clusterinit

import (
	"bytes"
	"context"
	"errors"
	"strings"
	"testing"

	"github.com/shalb/kube-dc/cli/internal/bootstrap/ports"
)

// gateInspector is a minimal adopt.Inspector for the adopt-gate tests.
type gateInspector struct {
	crds     []string
	charts   map[string]string
	crFields map[string]string
	err      error
}

func (g gateInspector) ListCRDs(context.Context) ([]string, error) { return g.crds, g.err }
func (g gateInspector) ListNamespaces(context.Context) ([]string, error) {
	return nil, nil
}
func (g gateInspector) DiscoverFluxGraph(context.Context) (ports.Graph, error) {
	return ports.Graph{}, nil
}
func (g gateInspector) HelmReleaseChartVersions(context.Context) (map[string]string, error) {
	return g.charts, nil
}
func (g gateInspector) GetResourceFieldFirst(_ context.Context, _, _, _, _, name string, _ ...string) (string, error) {
	return g.crFields[name], nil
}

// gateEnv is an adopt.EnvReader backed by a plain map.
type gateEnv map[string]string

func (e gateEnv) GetOr(k, fb string) string {
	if v, ok := e[k]; ok {
		return v
	}
	return fb
}

func TestCheckAdoptPinned_AllPinnedPasses(t *testing.T) {
	var buf bytes.Buffer
	err := CheckAdoptPinned(context.Background(), AdoptGateOptions{
		Inspector: gateInspector{
			crds:   []string{"certificates.cert-manager.io", "subnets.kubeovn.io"},
			charts: map[string]string{"cert-manager/cert-manager": "v1.14.4", "kube-system/kube-ovn": "v1.15.0"},
		},
		Env:         gateEnv{"CERT_MANAGER_VERSION": "v1.14.4", "KUBE_OVN_VERSION": "v1.15.0"},
		ClusterName: "acme",
		Out:         &buf,
	})
	if err != nil {
		t.Fatalf("all components pinned to live → gate should pass, got %v", err)
	}
	if !strings.Contains(buf.String(), "already pinned") {
		t.Errorf("expected adopt-safe note, got %q", buf.String())
	}
}

func TestCheckAdoptPinned_DriftFailsClosed(t *testing.T) {
	var buf bytes.Buffer
	err := CheckAdoptPinned(context.Background(), AdoptGateOptions{
		Inspector: gateInspector{
			crds:   []string{"certificates.cert-manager.io"},
			charts: map[string]string{"cert-manager/cert-manager": "v1.14.4"},
		},
		Env:         gateEnv{"CERT_MANAGER_VERSION": "v1.20.0"}, // drift → would upgrade
		ClusterName: "acme",
		Out:         &buf,
	})
	if err == nil {
		t.Fatal("a drifting pin must fail closed")
	}
	if !strings.Contains(err.Error(), "cert-manager") || !strings.Contains(err.Error(), "adopt acme --pin-versions") {
		t.Errorf("error should name the component + remediation: %v", err)
	}
}

func TestCheckAdoptPinned_UndetectedFailsClosed(t *testing.T) {
	// rook-ceph detected via CRD but no Helm release + no CR → undetected.
	err := CheckAdoptPinned(context.Background(), AdoptGateOptions{
		Inspector:   gateInspector{crds: []string{"cephclusters.ceph.rook.io"}},
		Env:         gateEnv{},
		ClusterName: "acme",
		Out:         &bytes.Buffer{},
	})
	if err == nil {
		t.Fatal("an undetected pre-existing component must fail closed")
	}
	if !strings.Contains(err.Error(), "rook-ceph") {
		t.Errorf("error should name rook-ceph: %v", err)
	}
}

func TestCheckAdoptPinned_AllowDowngradesToWarning(t *testing.T) {
	var buf bytes.Buffer
	err := CheckAdoptPinned(context.Background(), AdoptGateOptions{
		Inspector:   gateInspector{crds: []string{"cephclusters.ceph.rook.io"}},
		Env:         gateEnv{},
		Allow:       true,
		ClusterName: "acme",
		Out:         &buf,
	})
	if err != nil {
		t.Fatalf("--allow-unpinned-adopt should downgrade to a warning, got %v", err)
	}
	if !strings.Contains(buf.String(), "RISKY") {
		t.Errorf("expected a RISKY warning, got %q", buf.String())
	}
}

func TestCheckAdoptPinned_GreenfieldPasses(t *testing.T) {
	// No pre-existing components at all → nothing to gate.
	err := CheckAdoptPinned(context.Background(), AdoptGateOptions{
		Inspector: gateInspector{},
		Env:       gateEnv{},
		Out:       &bytes.Buffer{},
	})
	if err != nil {
		t.Fatalf("greenfield adopt → gate should pass, got %v", err)
	}
}

func TestCheckAdoptPinned_KubeVirtViaCRPasses(t *testing.T) {
	// KubeVirt/CDI resolve via their operator CR (item 4) — pinned when
	// the env matches the CR version, so the gate must pass.
	err := CheckAdoptPinned(context.Background(), AdoptGateOptions{
		Inspector: gateInspector{
			crds:     []string{"kubevirts.kubevirt.io", "datavolumes.cdi.kubevirt.io"},
			crFields: map[string]string{"kubevirt": "v1.8.1", "cdi": "v1.65.0"},
		},
		Env:         gateEnv{"KUBEVIRT_VERSION": "v1.8.1", "KUBEVIRT_CDI_VERSION": "v1.65.0"},
		ClusterName: "acme",
		Out:         &bytes.Buffer{},
	})
	if err != nil {
		t.Fatalf("kubevirt+cdi pinned to their CR versions → gate should pass, got %v", err)
	}
}

func TestCheckAdoptPinned_SoftSkipWithoutInputs(t *testing.T) {
	// No inspector or no env → can't evaluate → soft-skip (unchanged behavior).
	if err := CheckAdoptPinned(context.Background(), AdoptGateOptions{Env: gateEnv{}, Out: &bytes.Buffer{}}); err != nil {
		t.Errorf("nil inspector should soft-skip, got %v", err)
	}
	if err := CheckAdoptPinned(context.Background(), AdoptGateOptions{Inspector: gateInspector{}, Out: &bytes.Buffer{}}); err != nil {
		t.Errorf("nil env should soft-skip, got %v", err)
	}
}

func TestCheckAdoptPinned_InspectorErrorPropagates(t *testing.T) {
	err := CheckAdoptPinned(context.Background(), AdoptGateOptions{
		Inspector: gateInspector{err: errors.New("rbac denied")},
		Env:       gateEnv{},
		Out:       &bytes.Buffer{},
	})
	if err == nil || !strings.Contains(err.Error(), "adopt preflight") {
		t.Errorf("a cluster-read error must propagate as an adopt-preflight error, got %v", err)
	}
}
