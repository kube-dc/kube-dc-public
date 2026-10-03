package main

import (
	"bytes"
	"context"
	"crypto/ed25519"
	"crypto/sha256"
	"encoding/base64"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/shalb/kube-dc/cli/internal/bootstrap/ports"
)

type fakeRebuildSOPS struct {
	ports.SOPSClient
	plain []byte
}

type fakeRebuildGit struct {
	ports.GitClient
	repo      string
	committed bool
	pushed    bool
}

func (f *fakeRebuildGit) Diff(_ context.Context, repo string) (ports.Diff, error) {
	if repo != f.repo {
		return ports.Diff{}, fmt.Errorf("wrong Fleet repo")
	}
	if f.committed {
		return ports.Diff{}, nil
	}
	service, _ := os.ReadFile(filepath.Join(repo, "clusters", "example", "services.yaml"))
	if !bytes.Contains(service, []byte("  suspend: true\n")) {
		return ports.Diff{}, nil
	}
	return ports.Diff{Files: []ports.FileDiff{
		{Path: "clusters/example/services.yaml", Status: "M"},
		{Path: "clusters/example/services-catalog.yaml", Status: "M"},
	}}, nil
}

func (*fakeRebuildGit) Head(context.Context, string) (string, error) { return "prior", nil }
func (f *fakeRebuildGit) CommitAndPush(context.Context, string, string, string) (string, error) {
	f.committed = true
	return "new", nil
}
func (f *fakeRebuildGit) Push(context.Context, string, string) error {
	f.pushed = true
	return nil
}

func (f fakeRebuildSOPS) Decrypt(context.Context, string) ([]byte, error) {
	return bytes.Clone(f.plain), nil
}

type fakeRebuildReader struct {
	objects map[string]map[string]any
	applied []string
}

func (f *fakeRebuildReader) Get(_ context.Context, namespace, resource, name string) (map[string]any, error) {
	object, ok := f.objects[namespace+"/"+resource+"/"+name]
	if !ok {
		return nil, fmt.Errorf("NotFound")
	}
	return object, nil
}

func (f *fakeRebuildReader) ApplyRebuildObject(_ context.Context, object map[string]any) error {
	kind := object["kind"].(string)
	f.applied = append(f.applied, kind)
	if kind == "Secret" {
		raw, err := json.Marshal(object)
		if err != nil {
			return err
		}
		var normalized map[string]any
		if err := json.Unmarshal(raw, &normalized); err != nil {
			return err
		}
		f.objects["kube-dc-services/secret/kube-dc-service-cell-signing-key"] = normalized
	}
	return nil
}

func rebuildFixture(t *testing.T) (string, fakeRebuildSOPS, map[string]string) {
	t.Helper()
	repo := t.TempDir()
	dir := filepath.Join(repo, "clusters", "example")
	if err := os.MkdirAll(filepath.Join(dir, "escrow"), 0o700); err != nil {
		t.Fatal(err)
	}
	pins := "CLUSTER_NAME=example\nSERVICES_CELL_ID=example-cell\nSERVICES_DATAPLANE_NAME=example-platform\nSERVICES_CHART_VERSION=0.9.1\nSERVICES_HUB_DIGEST=sha256:1234\nKUBE_DC_UI_MANAGED_SERVICES_ALL_ORGANIZATIONS=true\n"
	for file, value := range map[string]string{
		"cluster-config.env":    pins,
		"services.yaml":         "apiVersion: kustomize.toolkit.fluxcd.io/v1\nkind: Kustomization\nmetadata:\n  name: services\n  namespace: flux-system\nspec:\n  path: ./platform/kube-dc-services\n",
		"services-catalog.yaml": "apiVersion: kustomize.toolkit.fluxcd.io/v1\nkind: Kustomization\nmetadata:\n  name: services-catalog\n  namespace: flux-system\nspec:\n  suspend: false\n  path: ./platform/kube-dc-services-catalog\n",
	} {
		if err := os.WriteFile(filepath.Join(dir, file), []byte(value), 0o600); err != nil {
			t.Fatal(err)
		}
	}
	seed := bytes.Repeat([]byte{9}, ed25519.SeedSize)
	public := ed25519.NewKeyFromSeed(seed).Public().(ed25519.PublicKey)
	hash := sha256.Sum256(public)
	fields := map[string]string{
		"seed":   base64.StdEncoding.EncodeToString([]byte(base64.StdEncoding.EncodeToString(seed))),
		"public": base64.StdEncoding.EncodeToString([]byte(base64.StdEncoding.EncodeToString(public))),
		"keyID":  base64.StdEncoding.EncodeToString([]byte("ed25519-" + hex.EncodeToString(hash[:6]))),
		"cellID": base64.StdEncoding.EncodeToString([]byte("example-cell")),
	}
	keyID := "ed25519-" + hex.EncodeToString(hash[:6])
	header := "apiVersion: v1\nkind: Secret\nmetadata:\n  name: kube-dc-service-cell-signing-key\n  namespace: kube-dc-services\n  annotations:\n    services.kube-dc.com/cell-id: example-cell\n    services.kube-dc.com/key-id: " + keyID + "\ntype: Opaque\ndata:\n"
	var plain strings.Builder
	plain.WriteString(header)
	for _, field := range []string{"seed", "public", "keyID", "cellID"} {
		plain.WriteString("  " + field + ": " + fields[field] + "\n")
	}
	encrypted := header + "  seed: ENC[redacted]\nsops:\n  version: '3.8'\n"
	if err := os.WriteFile(filepath.Join(dir, "escrow", "services-cell-signing-key.enc.yaml"), []byte(encrypted), 0o600); err != nil {
		t.Fatal(err)
	}
	return repo, fakeRebuildSOPS{plain: []byte(plain.String())}, fields
}

func TestManagedServicesRebuildSuspensionAndKeyRestore(t *testing.T) {
	repo, sops, fields := rebuildFixture(t)
	dir := filepath.Join(repo, "clusters", "example")
	serviceFile := filepath.Join(dir, "services.yaml")
	catalogFile := filepath.Join(dir, "services-catalog.yaml")
	service, _ := os.ReadFile(serviceFile)
	suspended, err := setManagedServicesKustomizationSuspend(service, "services", true)
	if err != nil || !bytes.Contains(suspended, []byte("  suspend: true\n")) {
		t.Fatalf("service suspension: %v", err)
	}
	if err := writeRegularRebuildFile(serviceFile, suspended); err != nil {
		t.Fatal(err)
	}
	catalog, _ := os.ReadFile(catalogFile)
	suspended, err = setManagedServicesKustomizationSuspend(catalog, "services-catalog", true)
	if err != nil {
		t.Fatal(err)
	}
	if err := writeRegularRebuildFile(catalogFile, suspended); err != nil {
		t.Fatal(err)
	}
	configPath := filepath.Join(dir, "cluster-config.env")
	config, _ := os.ReadFile(configPath)
	hidden, err := setManagedServicesVisibility(config, false)
	if err != nil {
		t.Fatal(err)
	}
	if err := writeRegularRebuildFile(configPath, hidden); err != nil {
		t.Fatal(err)
	}
	reader := &fakeRebuildReader{objects: map[string]map[string]any{
		"flux-system/configmap/cluster-config": {"data": map[string]any{"CLUSTER_NAME": "example", "SERVICES_CELL_ID": "example-cell", "KUBE_DC_UI_MANAGED_SERVICES_ALL_ORGANIZATIONS": "false"}},
		"flux-system/kustomization/services": {"metadata": map[string]any{"name": "services", "namespace": "flux-system"},
			"spec": map[string]any{"suspend": true, "path": "./platform/kube-dc-services"}},
		"flux-system/kustomization/services-catalog": {"metadata": map[string]any{"name": "services-catalog", "namespace": "flux-system"},
			"spec": map[string]any{"suspend": true, "path": "./platform/kube-dc-services-catalog"}},
	}}
	if err := restoreManagedServicesCellKey(context.Background(), reader, repo, "example", sops); err != nil {
		t.Fatal(err)
	}
	if strings.Join(reader.applied, ",") != "Namespace,Secret" {
		t.Fatalf("restore applied %v", reader.applied)
	}
	secret := reader.objects["kube-dc-services/secret/kube-dc-service-cell-signing-key"]
	for field, value := range fields {
		if secret["data"].(map[string]any)[field] != value {
			t.Fatalf("restored %s differs", field)
		}
	}
	if err := restoreManagedServicesCellKey(context.Background(), reader, repo, "example", sops); err != nil {
		t.Fatalf("same key was not idempotent: %v", err)
	}
	secret["data"].(map[string]any)["seed"] = "different"
	if err := restoreManagedServicesCellKey(context.Background(), reader, repo, "example", sops); err == nil {
		t.Fatal("different existing key was replaced")
	}
	delete(reader.objects, "kube-dc-services/secret/kube-dc-service-cell-signing-key")
	reader.objects["kube-dc-services/deployment/kube-dc-services"] = map[string]any{}
	if err := restoreManagedServicesCellKey(context.Background(), reader, repo, "example", sops); err == nil {
		t.Fatal("missing key was restored while an old hub Deployment existed")
	}
}

func TestPrepareManagedServicesRebuildSuspendsBothKustomizations(t *testing.T) {
	repo, sops, _ := rebuildFixture(t)
	git := &fakeRebuildGit{repo: repo}
	if err := prepareManagedServicesRebuild(context.Background(), repo, "example", "token", git, sops); err != nil {
		t.Fatal(err)
	}
	if !git.committed {
		t.Fatal("suspension was not committed")
	}
	for _, file := range []string{"services.yaml", "services-catalog.yaml"} {
		body, err := os.ReadFile(filepath.Join(repo, "clusters", "example", file))
		if err != nil || !bytes.Contains(body, []byte("  suspend: true\n")) {
			t.Fatalf("%s was not suspended: %v", file, err)
		}
	}
	config, err := os.ReadFile(filepath.Join(repo, "clusters", "example", "cluster-config.env"))
	if err != nil || !bytes.Contains(config, []byte("KUBE_DC_UI_MANAGED_SERVICES_ALL_ORGANIZATIONS=false")) {
		t.Fatalf("console visibility was not disabled: %v", err)
	}
	if err := prepareManagedServicesRebuild(context.Background(), repo, "example", "token", git, sops); err != nil {
		t.Fatalf("repeated suspension was not idempotent: %v", err)
	}
	if !git.pushed {
		t.Fatal("already-suspended state was not pushed")
	}
}

func TestManagedServicesRebuildPushTargetMatchesLiveFlux(t *testing.T) {
	repo := initManagedServicesRollbackRepo(t)
	managedServicesTestCommit(t, repo, "initial")
	if output, err := execGit(t, repo, "remote", "add", "origin", "https://github.com/example/fleet.git"); err != nil {
		t.Fatalf("add remote: %s: %v", output, err)
	}
	reader := &fakeRebuildReader{objects: map[string]map[string]any{
		"flux-system/configmap/cluster-config": {"data": map[string]any{"CLUSTER_NAME": "example"}},
		"flux-system/gitrepository/flux-system": {
			"metadata": map[string]any{"name": "flux-system", "namespace": "flux-system"},
			"spec":     map[string]any{"ref": map[string]any{"branch": "main"}, "url": "ssh://git@github.com/example/fleet"},
		},
		"flux-system/kustomization/flux-system": {
			"spec": map[string]any{"path": "./clusters/example", "sourceRef": map[string]any{"kind": "GitRepository", "name": "flux-system"}},
		},
	}}
	if err := verifyManagedServicesFleetPushTarget(context.Background(), repo, "example", reader); err != nil {
		t.Fatal(err)
	}
	if output, err := execGit(t, repo, "remote", "set-url", "origin", "https://github.com/other/fetch-mirror.git"); err != nil {
		t.Fatalf("set fetch remote: %s: %v", output, err)
	}
	if output, err := execGit(t, repo, "config", "remote.origin.pushurl", "https://github.com/example/fleet.git"); err != nil {
		t.Fatalf("set push remote: %s: %v", output, err)
	}
	if err := verifyManagedServicesFleetPushTarget(context.Background(), repo, "example", reader); err != nil {
		t.Fatalf("matching pushurl with a different fetch URL was refused: %v", err)
	}
	sync := reader.objects["flux-system/kustomization/flux-system"]
	sync["spec"].(map[string]any)["sourceRef"].(map[string]any)["namespace"] = "other"
	if err := verifyManagedServicesFleetPushTarget(context.Background(), repo, "example", reader); err == nil {
		t.Fatal("cross-namespace Flux source was accepted")
	}
	delete(sync["spec"].(map[string]any)["sourceRef"].(map[string]any), "namespace")
	flux := reader.objects["flux-system/gitrepository/flux-system"]
	flux["spec"].(map[string]any)["ref"].(map[string]any)["branch"] = "release"
	if err := verifyManagedServicesFleetPushTarget(context.Background(), repo, "example", reader); err == nil {
		t.Fatal("feature branch would have been pushed while Flux follows another branch")
	}
	flux["spec"].(map[string]any)["ref"].(map[string]any)["branch"] = "main"
	flux["spec"].(map[string]any)["ref"].(map[string]any)["tag"] = "stale"
	if err := verifyManagedServicesFleetPushTarget(context.Background(), repo, "example", reader); err == nil {
		t.Fatal("Flux tag selector overriding the branch was accepted")
	}
	delete(flux["spec"].(map[string]any)["ref"].(map[string]any), "tag")
	flux["spec"].(map[string]any)["url"] = "ssh://git@github.com/other/fleet"
	if err := verifyManagedServicesFleetPushTarget(context.Background(), repo, "example", reader); err == nil {
		t.Fatal("push to a different repository was accepted")
	}
	flux["spec"].(map[string]any)["url"] = "ssh://git@github.com/example/fleet"
	reader.objects["flux-system/configmap/cluster-config"]["data"].(map[string]any)["CLUSTER_NAME"] = "other"
	if err := verifyManagedServicesFleetPushTarget(context.Background(), repo, "example", reader); err == nil {
		t.Fatal("wrong live cluster was accepted")
	}
}

func TestManagedServicesGitDestinationNormalizesDefaultPorts(t *testing.T) {
	for _, tc := range []struct {
		a, b string
		want bool
	}{
		{"git@github.com:example/fleet.git", "https://github.com/example/fleet", true},
		{"ssh://git@github.com/example/fleet", "https://github.com/example/fleet.git", true},
		{"ssh://git@github.com:2222/example/fleet", "https://github.com/example/fleet", false},
		{"https://github.com/other/fleet", "ssh://git@github.com/example/fleet", false},
	} {
		if got := sameManagedServicesGitDestination(tc.a, tc.b); got != tc.want {
			t.Errorf("destination equality for %q and %q = %t; want %t", tc.a, tc.b, got, tc.want)
		}
	}
}

func TestManagedServicesRebuildRequiresSuspendedGitAndLive(t *testing.T) {
	repo, sops, _ := rebuildFixture(t)
	reader := &fakeRebuildReader{objects: map[string]map[string]any{
		"flux-system/configmap/cluster-config": {"data": map[string]any{"CLUSTER_NAME": "example", "SERVICES_CELL_ID": "example-cell", "KUBE_DC_UI_MANAGED_SERVICES_ALL_ORGANIZATIONS": "false"}},
		"flux-system/kustomization/services": {"metadata": map[string]any{"name": "services", "namespace": "flux-system"},
			"spec": map[string]any{"suspend": false, "path": "./platform/kube-dc-services"}},
		"flux-system/kustomization/services-catalog": {"metadata": map[string]any{"name": "services-catalog", "namespace": "flux-system"},
			"spec": map[string]any{"suspend": true, "path": "./platform/kube-dc-services-catalog"}},
	}}
	if err := restoreManagedServicesCellKey(context.Background(), reader, repo, "example", sops); err == nil {
		t.Fatal("unsuspended Git was accepted")
	}
	paths := []struct{ file, name string }{{"services.yaml", "services"}, {"services-catalog.yaml", "services-catalog"}}
	for _, target := range paths {
		path := filepath.Join(repo, "clusters", "example", target.file)
		current, _ := os.ReadFile(path)
		next, err := setManagedServicesKustomizationSuspend(current, target.name, true)
		if err != nil {
			t.Fatal(err)
		}
		if err := writeRegularRebuildFile(path, next); err != nil {
			t.Fatal(err)
		}
	}
	configPath := filepath.Join(repo, "clusters", "example", "cluster-config.env")
	config, _ := os.ReadFile(configPath)
	hidden, err := setManagedServicesVisibility(config, false)
	if err != nil {
		t.Fatal(err)
	}
	if err := writeRegularRebuildFile(configPath, hidden); err != nil {
		t.Fatal(err)
	}
	if err := restoreManagedServicesCellKey(context.Background(), reader, repo, "example", sops); err == nil {
		t.Fatal("unsuspended live Kustomization was accepted")
	}
}
