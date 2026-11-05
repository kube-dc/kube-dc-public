package clusterinit

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"

	"github.com/shalb/kube-dc/cli/internal/bootstrap/config"
	"github.com/shalb/kube-dc/cli/internal/bootstrap/ports"
)

func preparedFixture(t *testing.T) (PreparedScaffold, string) {
	return preparedFixtureWithHooks(t, nil, nil)
}

func preparedFixtureWithHooks(t *testing.T, setup func(string), afterScript func(string, map[string]string) error, adjust ...func(*ScaffoldOptions)) (PreparedScaffold, string) {
	t.Helper()
	fleet := t.TempDir()
	writeStarterScaffoldSources(t, fleet)
	if setup != nil {
		setup(fleet)
	}
	directory := filepath.Join(t.TempDir(), "prepared")
	runner := func(root string) ports.ScriptRunner {
		var fake *fakeScriptRunner
		fake = &fakeScriptRunner{fleetRoot: root, lines: []ports.Line{{Stream: ports.StreamExit, Text: "0"}}, onRun: func(cluster string) error {
			if err := os.MkdirAll(cluster, 0700); err != nil {
				return err
			}
			for name, body := range map[string]string{
				"cluster-config.env":  "CLUSTER_NAME=demo\nDOMAIN=example.test\n",
				"infrastructure.yaml": "apiVersion: kustomize.toolkit.fluxcd.io/v1\nkind: Kustomization\nmetadata:\n  name: infra-cni\n  namespace: flux-system\n",
				"kustomization.yaml":  "apiVersion: kustomize.config.k8s.io/v1beta1\nkind: Kustomization\nresources:\n  - infrastructure.yaml\n",
				"secrets.enc.yaml":    "stringData:\n  PASSWORD: ENC[AES256_GCM,data:ciphertext,iv:xyz,type:str]\nsops:\n  age: []\n  mac: ENC[AES256_GCM,data:mac,iv:xyz,type:str]\n",
			} {
				if err := os.WriteFile(filepath.Join(cluster, name), []byte(body), 0600); err != nil {
					return err
				}
			}
			if afterScript != nil {
				return afterScript(root, fake.calls[len(fake.calls)-1].Env)
			}
			return nil
		}}
		return fake
	}
	opts := ScaffoldOptions{Sets: map[string]string{"EXT_NET_INTERFACE": "eth1", "EXT_NET_VLAN_ID": "1103"}, FleetRepo: fleet, NodeExternalIP: "192.0.2.10", Plan: &Plan{ClusterName: "demo", Domain: "example.test", Preset: PresetInternalOnly, IngressAddressLayer: AddressLayerNone}}
	for _, fn := range adjust {
		fn(&opts)
	}
	prepared, err := PrepareScaffold(context.Background(), opts, directory, runner, func(copy string) error { return nil })
	if err != nil {
		t.Fatal(err)
	}
	return prepared, fleet
}

func TestPreparedScaffoldPythonImportsLeaveSourceClean(t *testing.T) {
	if _, err := exec.LookPath("python3"); err != nil {
		t.Skip("python3 is unavailable")
	}
	prepared, _ := preparedFixtureWithHooks(t, func(fleet string) {
		path := filepath.Join(fleet, "scripts", "services_catalog_check.py")
		if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(path, []byte("VALUE = 1\n"), 0o644); err != nil {
			t.Fatal(err)
		}
	}, func(root string, env map[string]string) error {
		cmd := exec.Command("python3", "-c", "import services_catalog_check")
		cmd.Dir = filepath.Join(root, "scripts")
		cmd.Env = append(os.Environ(), "PYTHONDONTWRITEBYTECODE="+env["PYTHONDONTWRITEBYTECODE"])
		return cmd.Run()
	})
	if err := VerifyPreparedScaffold(prepared); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(filepath.Join(prepared.Directory, "scripts", "__pycache__")); !os.IsNotExist(err) {
		t.Fatalf("private preparation wrote Python bytecode: %v", err)
	}
}

func TestPreparedCatalogLockReplacesOnlyReviewedBaseline(t *testing.T) {
	const path = "platform/kube-dc-services-catalog/revisions.lock"
	fleet := t.TempDir()
	if err := os.MkdirAll(filepath.Join(fleet, filepath.Dir(path)), 0o755); err != nil {
		t.Fatal(err)
	}
	old := []byte("old lock\n")
	newBody := []byte("old lock\nnew cluster\n")
	if err := os.WriteFile(filepath.Join(fleet, path), old, 0o644); err != nil {
		t.Fatal(err)
	}
	root, err := os.OpenRoot(fleet)
	if err != nil {
		t.Fatal(err)
	}
	defer root.Close()
	oldHash := sha256.Sum256(old)
	newHash := sha256.Sum256(newBody)
	before := []PreparedFile{{Path: path, SHA256: hex.EncodeToString(oldHash[:]), Mode: 0o644, Size: int64(len(old))}}
	after := PreparedFile{Path: path, SHA256: hex.EncodeToString(newHash[:]), Mode: 0o644, Size: int64(len(newBody))}
	changed, err := reviewedSourceAdditions(before, []PreparedFile{after})
	if err != nil || len(changed) != 1 || changed[0].Path != path {
		t.Fatalf("catalog lock delta not reviewable: %+v, %v", changed, err)
	}
	if err := publishPreparedCatalogLock(root, fleet, after, newBody, before); err != nil {
		t.Fatal(err)
	}
	if got, err := os.ReadFile(filepath.Join(fleet, path)); err != nil || string(got) != string(newBody) {
		t.Fatalf("published lock = %q, %v", got, err)
	}
	if err := publishPreparedCatalogLock(root, fleet, after, []byte("bad"), before); err == nil {
		t.Fatal("stale baseline replacement was accepted")
	}
}

func TestPreparedScaffoldPublishesExactGeneratorBytes(t *testing.T) {
	prepared, fleet := preparedFixture(t)
	if _, err := os.Stat(filepath.Join(fleet, "clusters", "demo")); !os.IsNotExist(err) {
		t.Fatal("preparation wrote the selected checkout")
	}
	if err := PublishPreparedScaffold(context.Background(), prepared, fleet, func(context.Context) error { return nil }); err != nil {
		t.Fatal(err)
	}
	for _, file := range prepared.Files {
		want, err := os.ReadFile(filepath.Join(prepared.Directory, file.Path))
		if err != nil {
			t.Fatal(err)
		}
		got, err := os.ReadFile(filepath.Join(fleet, file.Path))
		if err != nil || string(got) != string(want) {
			t.Fatalf("publication changed reviewed bytes: %s", file.Path)
		}
	}
}

func TestPreparedScaffoldRefusesDriftBeforeDestinationWrites(t *testing.T) {
	for _, variant := range []string{"output", "source", "guard", "symlink", "file-scope", "destination"} {
		t.Run(variant, func(t *testing.T) {
			p, fleet := preparedFixture(t)
			guard := func(context.Context) error { return nil }
			switch variant {
			case "output":
				_ = os.WriteFile(filepath.Join(p.Directory, p.Files[0].Path), []byte("changed"), 0600)
			case "source":
				_ = os.WriteFile(filepath.Join(fleet, "addons", "metallb", "kustomization.yaml"), []byte("changed"), 0600)
			case "guard":
				guard = func(context.Context) error { return fmt.Errorf("target changed") }
			case "symlink":
				outside := t.TempDir()
				if err := os.Symlink(outside, filepath.Join(fleet, "clusters")); err != nil {
					t.Fatal(err)
				}
			case "destination":
				p.Destination = t.TempDir()
				p.Hash = preparedHash(p)
			case "file-scope":
				p.Files[0].Path = "foreign.txt"
				p.Hash = preparedHash(p)
			}
			if err := PublishPreparedScaffold(context.Background(), p, fleet, guard); err == nil {
				t.Fatal("drift reached publication")
			}
			if _, err := os.Stat(filepath.Join(fleet, "clusters", "demo")); !os.IsNotExist(err) {
				t.Fatal("rejected review created installation files")
			}
		})
	}
}

func TestPreparedSourceGuardRunsBeforeGenerator(t *testing.T) {
	fleet := t.TempDir()
	writeStarterScaffoldSources(t, fleet)
	called := false
	_, err := PrepareScaffold(context.Background(), ScaffoldOptions{FleetRepo: fleet, Plan: &Plan{ClusterName: "demo"}}, filepath.Join(t.TempDir(), "prepared"), func(string) ports.ScriptRunner { called = true; return &fakeScriptRunner{} }, func(string) error { return fmt.Errorf("release baseline mismatch") })
	if err == nil || called {
		t.Fatal("generator ran before copied source was verified")
	}
}

func TestPreparedScaffoldRetainsProductAndCustomSiteComponents(t *testing.T) {
	const component = "apiVersion: kustomize.config.k8s.io/v1alpha1\nkind: Component\n"
	prepared, fleet := preparedFixtureWithHooks(t, func(root string) {
		for _, path := range []string{"install/site-component", "platform/components/platform-state-on-managers", "platform/components/openbao-on-managers"} {
			full := filepath.Join(root, path)
			if err := os.MkdirAll(full, 0700); err != nil {
				t.Fatal(err)
			}
			if err := os.WriteFile(filepath.Join(full, "kustomization.yaml"), []byte(component), 0600); err != nil {
				t.Fatal(err)
			}
		}
	}, func(root string, _ map[string]string) error {
		cluster := filepath.Join(root, "clusters/demo")
		if err := os.WriteFile(filepath.Join(cluster, "platform.yaml"), []byte("apiVersion: kustomize.toolkit.fluxcd.io/v1\nkind: Kustomization\nmetadata:\n  name: platform\nspec:\n  path: ./platform\n"), 0600); err != nil {
			return err
		}
		return os.WriteFile(filepath.Join(cluster, "cluster-config.env"), []byte("CLUSTER_NAME=demo\nDOMAIN=example.test\nOPENBAO_REPLICAS=3\n"), 0600)
	}, func(opts *ScaffoldOptions) {
		opts.Plan.ManagerNodes = 3
		opts.ManagerNodeNames = []string{"server-1", "server-2", "server-3"}
		opts.SiteComponents = []SiteComponent{{Layer: "platform", Path: "install/site-component"}}
	})
	body, err := os.ReadFile(filepath.Join(prepared.Directory, "clusters/demo/platform.yaml"))
	if err != nil {
		t.Fatal(err)
	}
	for _, want := range []string{"platform-state-on-managers", "openbao-on-managers", "install/site-component"} {
		if !strings.Contains(string(body), want) {
			t.Fatalf("prepared output omitted %s", want)
		}
	}
	if err := PublishPreparedScaffold(context.Background(), prepared, fleet, func(context.Context) error { return nil }); err != nil {
		t.Fatal(err)
	}
	if err := VerifyPreparedPublication(prepared); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(fleet, "install/site-component/kustomization.yaml"), []byte(component+"# changed\n"), 0600); err != nil {
		t.Fatal(err)
	}
	if err := VerifyPreparedPublication(prepared); err == nil {
		t.Fatal("unreviewed site component change accepted")
	}
}

func TestPreparedComponentsRejectUncapturedSiblingAndRemoteResources(t *testing.T) {
	for _, reference := range []string{"../shared/resource.yaml", "https://example.test/base?ref=main"} {
		t.Run(reference, func(t *testing.T) {
			root := t.TempDir()
			component := filepath.Join(root, "sites/component")
			if err := os.MkdirAll(component, 0700); err != nil {
				t.Fatal(err)
			}
			body := "apiVersion: kustomize.config.k8s.io/v1alpha1\nkind: Component\nresources:\n  - " + reference + "\n"
			if err := os.WriteFile(filepath.Join(component, "kustomization.yaml"), []byte(body), 0600); err != nil {
				t.Fatal(err)
			}
			if _, err := SnapshotScaffoldSource(root, SiteComponent{Layer: "platform", Path: "sites/component"}); err == nil {
				t.Fatal("uncaptured dependency entered the review baseline")
			}
		})
	}
}

func TestPreparedComponentsRejectLegacyUncapturedSources(t *testing.T) {
	for _, extra := range []string{
		"configMapGenerator:\n  - name: local-config\n    env: ../shared/input.env\n",
		"secretGenerator:\n  - name: local-secret\n    env: ../shared/input.env\n",
		"helmChartInflationGenerator:\n  - chartName: remote-chart\n    chartRepoUrl: https://example.test/charts\n",
	} {
		root := t.TempDir()
		component := filepath.Join(root, "sites/component")
		if err := os.MkdirAll(component, 0700); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(filepath.Join(component, "kustomization.yaml"), []byte("apiVersion: kustomize.config.k8s.io/v1alpha1\nkind: Component\n"+extra), 0600); err != nil {
			t.Fatal(err)
		}
		if _, err := SnapshotScaffoldSource(root, SiteComponent{Layer: "platform", Path: "sites/component"}); err == nil {
			t.Fatal("legacy source bypassed preparation policy")
		}
	}
}

// Prepared Class D generation must keep the same internal VIP, anchors and
// Project DNS component as ordinary initialization.
func TestPreparedClassDMatchesOrdinaryFrontDoor(t *testing.T) {
	cp := []string{"node-a", "node-b", "node-c"}
	prepared, _ := preparedFixtureWithHooks(t, func(fleet string) {
		files := map[string]string{
			"scripts/services-catalog-render-test.sh":      "#!/bin/sh\nexit 0\n",
			"bootstrap/add-cluster.sh":                     "# scaffold-capability: managed-services-v1\n",
			"infrastructure/kube-ovn-network/vpc-dns.yaml": "apiVersion: v1\nkind: ConfigMap\nmetadata:\n  name: vpc-dns-corefile\ndata:\n  Corefile: |-\n    .:53 {\n      errors\n      ready\n      forward . /etc/resolv.conf\n      cache 30\n    }\n",
		}
		for name, body := range files {
			path := filepath.Join(fleet, name)
			if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
				t.Fatal(err)
			}
			if err := os.WriteFile(path, []byte(body), 0o600); err != nil {
				t.Fatal(err)
			}
		}
	}, func(root string, _ map[string]string) error {
		dir := filepath.Join(root, "clusters", "demo")
		files := map[string]string{
			"cluster-config.env":  "CLUSTER_NAME=demo\nDOMAIN=example.test\n" + classDEnv(),
			"platform.yaml":       "apiVersion: kustomize.toolkit.fluxcd.io/v1\nkind: Kustomization\nmetadata:\n  name: platform\n  namespace: flux-system\nspec:\n  path: ./platform\n",
			"infrastructure.yaml": "apiVersion: kustomize.toolkit.fluxcd.io/v1\nkind: Kustomization\nmetadata:\n  name: infra-core\n  namespace: flux-system\nspec:\n  path: ./infrastructure\n",
		}
		for name, body := range files {
			if err := os.WriteFile(filepath.Join(dir, name), []byte(body), 0o600); err != nil {
				return err
			}
		}
		return nil
	}, func(opts *ScaffoldOptions) {
		opts.ControlPlaneNodes, opts.ManagerNodeNames = cp, cp
		opts.Plan.ManagerNodes, opts.Plan.ManagedServicesMode = 3, "on"
		opts.Plan.IngressNodes, opts.Plan.IngressAddressLayer = cp, AddressLayerMetalLBL2
		for _, line := range strings.Split(classDEnv(), "\n") {
			if k, v, ok := strings.Cut(line, "="); ok {
				opts.Sets[k] = v
			}
		}
	})
	path := filepath.Join(prepared.Directory, "clusters", "demo", "cluster-config.env")
	env, err := config.LoadEnv(path)
	if err != nil {
		t.Fatal(err)
	}
	want := env.AsMap()
	if want["PROJECT_FRONT_DOOR_DNS"] != "zone" || want["ENVOY_GATEWAY_INTERNAL_VIP"] != "198.51.100.65" {
		t.Fatal("prepared front door missing")
	}
	ordinary := t.TempDir()
	if err := os.CopyFS(ordinary, os.DirFS(prepared.Directory)); err != nil {
		t.Fatal(err)
	}
	for _, kv := range []string{"ENVOY_GATEWAY_INTERNAL_VIP=", "PLATFORM_ENDPOINT_ENVOY_GATEWAY_ENABLED=false", "EXT_NET_ANCHOR_IPS=", "EXT_NET_ANCHOR_REQUIRED=false", "PROJECT_FRONT_DOOR_DNS=", "EXT_NET_EXCLUDE_IPS=198.51.100.1..198.51.100.64"} {
		k, v, _ := strings.Cut(kv, "=")
		env.Set(k, v)
	}
	path = filepath.Join(ordinary, "clusters", "demo", "cluster-config.env")
	if err := env.Write(path); err != nil {
		t.Fatal(err)
	}
	if did, err := ResolveProjectFrontDoor(ordinary, "demo", cp, cp, "", nil); err != nil || !did {
		t.Fatalf("ordinary front door: %v", err)
	}
	if err := WriteProductComponents(context.Background(), ProductComponentOptions{FleetRepo: ordinary, ClusterName: "demo"}, nil); err != nil {
		t.Fatal(err)
	}
	env, err = config.LoadEnv(path)
	if err != nil {
		t.Fatal(err)
	}
	for _, key := range []string{"ENVOY_GATEWAY_INTERNAL_VIP", "EXT_NET_ANCHOR_IPS", "EXT_NET_EXCLUDE_IPS", "PROJECT_FRONT_DOOR_DNS", "INGRESS_GLOBAL_ALLOWLIST", "EGRESS_GLOBAL_ALLOWLIST"} {
		if env.AsMap()[key] != want[key] {
			t.Errorf("ordinary and reviewed %s differ", key)
		}
	}
	rel := filepath.Join("clusters", "demo", "vpc-dns-front-door", "kustomization.yaml")
	expected, err := os.ReadFile(filepath.Join(prepared.Directory, rel))
	if err != nil {
		t.Fatal(err)
	}
	actual, err := os.ReadFile(filepath.Join(ordinary, rel))
	if err != nil || string(actual) != string(expected) {
		t.Fatal("ordinary and prepared DNS differ", err)
	}
}
