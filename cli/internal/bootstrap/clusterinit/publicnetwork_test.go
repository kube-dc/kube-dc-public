package clusterinit

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// The cloud+public-vlan preset advertises infra-public-network (plan text,
// PresetKustomizations, preset_test.go) but nothing wrote it: infrastructure.yaml
// comes from add-cluster.sh, which has no preset awareness. The EXT_PUBLIC_*
// keys were scaffolded and consumed by nothing, so ext-public never existed.
func TestWritePublicNetwork(t *testing.T) {
	newRepo := func(t *testing.T) (string, string) {
		t.Helper()
		repo := t.TempDir()
		source := filepath.Join(repo, "infrastructure", "kube-ovn-network-public")
		if err := os.MkdirAll(source, 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(filepath.Join(source, "kustomization.yaml"), []byte("apiVersion: kustomize.config.k8s.io/v1beta1\nkind: Kustomization\n"), 0o644); err != nil {
			t.Fatal(err)
		}
		dir := filepath.Join(repo, "clusters", "dc1")
		if err := os.MkdirAll(dir, 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(filepath.Join(dir, "infrastructure.yaml"),
			[]byte("apiVersion: kustomize.toolkit.fluxcd.io/v1\nkind: Kustomization\nmetadata:\n  name: infra-cni\n"), 0o644); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(filepath.Join(dir, "cluster-config.env"),
			[]byte("CLUSTER_NAME=dc1\n"), 0o644); err != nil {
			t.Fatal(err)
		}
		return repo, dir
	}

	t.Run("public-vlan preset gets the layer", func(t *testing.T) {
		repo, dir := newRepo(t)
		if err := WritePublicNetwork(repo, "dc1", PresetCloudPublicVLAN, nil); err != nil {
			t.Fatalf("WritePublicNetwork: %v", err)
		}
		infra, _ := os.ReadFile(filepath.Join(dir, "infrastructure.yaml"))
		if !strings.Contains(string(infra), "name: infra-public-network") {
			t.Error("infrastructure.yaml missing the infra-public-network Kustomization")
		}
		if !strings.Contains(string(infra), "path: ./infrastructure/kube-ovn-network-public") {
			t.Error("infra-public-network points at the wrong path")
		}
		if !strings.Contains(string(infra), "- name: infra-core") {
			t.Error("infra-public-network must dependsOn infra-core (needs the kubeovn CRDs + VPC)")
		}
		// Deliberately NOT asserting VPC_EXTRA_EXTERNAL_SUBNETS: nothing in the
		// fleet consumes that key, ext-public attaches without it, and setting
		// it on the DEFAULT VPC would leave the external router port unmanaged
		// (see infrastructure/kube-ovn-network/vpc-config.yaml).
		env, _ := os.ReadFile(filepath.Join(dir, "cluster-config.env"))
		if strings.Contains(string(env), "VPC_EXTRA_EXTERNAL_SUBNETS") {
			t.Error("the public-network layer must not write VPC_EXTRA_EXTERNAL_SUBNETS")
		}
	})

	t.Run("presets without a public VLAN are untouched", func(t *testing.T) {
		for _, p := range []Preset{PresetCloudVLAN, PresetInternalOnly} {
			repo, dir := newRepo(t)
			if err := WritePublicNetwork(repo, "dc1", p, nil); err != nil {
				t.Fatalf("WritePublicNetwork(%s): %v", p, err)
			}
			infra, _ := os.ReadFile(filepath.Join(dir, "infrastructure.yaml"))
			if strings.Contains(string(infra), "infra-public-network") {
				t.Errorf("preset %s must not get infra-public-network", p)
			}
		}
	})

	t.Run("idempotent", func(t *testing.T) {
		repo, dir := newRepo(t)
		for i := 0; i < 3; i++ {
			if err := WritePublicNetwork(repo, "dc1", PresetCloudPublicVLAN, nil); err != nil {
				t.Fatalf("run %d: %v", i, err)
			}
		}
		infra, _ := os.ReadFile(filepath.Join(dir, "infrastructure.yaml"))
		if n := strings.Count(string(infra), "name: infra-public-network"); n != 1 {
			t.Errorf("layer written %d times, want exactly 1", n)
		}
	})
}

// A public-VLAN preset with no infrastructure.yaml cannot be wired; init must
// fail rather than silently omit a promised layer.
func TestWritePublicNetwork_MissingInfrastructureIsFatal(t *testing.T) {
	repo := t.TempDir()
	source := filepath.Join(repo, "infrastructure", "kube-ovn-network-public")
	if err := os.MkdirAll(source, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(source, "kustomization.yaml"), []byte("apiVersion: kustomize.config.k8s.io/v1beta1\nkind: Kustomization\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.MkdirAll(filepath.Join(repo, "clusters", "dc1"), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := WritePublicNetwork(repo, "dc1", PresetCloudPublicVLAN, nil); err == nil {
		t.Fatal("expected an error when infrastructure.yaml is absent")
	}
}

func TestWritePublicNetwork_MissingStarterSourceIsFatal(t *testing.T) {
	repo := t.TempDir()
	dir := filepath.Join(repo, "clusters", "dc1")
	if err := os.MkdirAll(dir, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, "infrastructure.yaml"), []byte("kind: Kustomization\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	err := WritePublicNetwork(repo, "dc1", PresetCloudPublicVLAN, nil)
	if err == nil || !strings.Contains(err.Error(), "starter is missing") {
		t.Fatalf("expected actionable starter-version error, got %v", err)
	}
}
