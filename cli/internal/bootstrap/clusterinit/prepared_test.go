package clusterinit

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"testing"

	"github.com/shalb/kube-dc/cli/internal/bootstrap/ports"
)

func preparedFixture(t *testing.T) (PreparedScaffold, string) {
	return preparedFixtureWithHooks(t, nil, nil)
}

func preparedFixtureWithHooks(t *testing.T, setup func(string), afterScript func(string, map[string]string) error) (PreparedScaffold, string) {
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
	prepared, err := PrepareScaffold(context.Background(), ScaffoldOptions{Sets: map[string]string{"EXT_NET_INTERFACE": "eth1", "EXT_NET_VLAN_ID": "1103"}, FleetRepo: fleet, NodeExternalIP: "192.0.2.10", Plan: &Plan{ClusterName: "demo", Domain: "example.test", Preset: PresetInternalOnly, IngressAddressLayer: AddressLayerNone}}, directory, runner, func(copy string) error { return nil })
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
