package setup

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/shalb/kube-dc/cli/internal/bootstrap/clusterinit"
	"github.com/shalb/kube-dc/cli/internal/bootstrap/ports"
)

type reviewFixtureRunner struct {
	shared map[string]string
	root   string
	calls  int
}

func (r *reviewFixtureRunner) WithSentinelCallback(ports.SentinelCallback) ports.ScriptRunner {
	return r
}
func (r *reviewFixtureRunner) Run(_ context.Context, _ ports.ScriptKind, _ map[string]string, args ...string) (<-chan ports.Line, error) {
	r.calls++
	if len(args) != 4 || args[3] != "/dev/null" {
		return nil, fmt.Errorf("generator used ambient Kubernetes discovery")
	}
	cluster := filepath.Join(r.root, "clusters", args[0])
	if err := os.MkdirAll(cluster, 0700); err != nil {
		return nil, err
	}
	for name, body := range map[string]string{
		"cluster-config.env":  "CLUSTER_NAME=demo\nDOMAIN=example.test\n",
		"infrastructure.yaml": "apiVersion: kustomize.toolkit.fluxcd.io/v1\nkind: Kustomization\nmetadata:\n  name: infra-core\n  namespace: flux-system\nspec:\n  path: ./infrastructure\n",
		"platform.yaml":       "apiVersion: kustomize.toolkit.fluxcd.io/v1\nkind: Kustomization\nmetadata:\n  name: platform\n  namespace: flux-system\nspec:\n  path: ./platform\n",
		"kustomization.yaml":  "apiVersion: kustomize.config.k8s.io/v1beta1\nkind: Kustomization\nresources:\n  - infrastructure.yaml\n  - platform.yaml\n  - secrets.enc.yaml\n",
		"secrets.enc.yaml":    "stringData:\n  PASSWORD: ENC[AES256_GCM,data:ciphertext,iv:xyz,type:str]\nsops:\n  age: []\n  mac: ENC[AES256_GCM,data:mac,iv:xyz,type:str]\n",
	} {
		if err := os.WriteFile(filepath.Join(cluster, name), []byte(body), 0600); err != nil {
			return nil, err
		}
	}
	for path, body := range r.shared {
		full := filepath.Join(r.root, path)
		if err := os.MkdirAll(filepath.Dir(full), 0700); err != nil {
			return nil, err
		}
		if err := os.WriteFile(full, []byte(body), 0600); err != nil {
			return nil, err
		}
	}
	channel := make(chan ports.Line, 1)
	channel <- ports.Line{Stream: ports.StreamExit, Text: "0"}
	close(channel)
	return channel, nil
}

func safetyReviewFixture(t *testing.T, shared ...map[string]string) (Compiled, SafetyReview, *evidenceHostSSHStub, string) {
	t.Helper()
	c, cache, cli, _ := artifactCacheFixture(t)
	starterProof, starterRef, source := starterBindingFixture(t)
	entries, err := os.ReadDir(filepath.Join(starterProof.CacheDirectory, "blobs", "sha256"))
	if err != nil {
		t.Fatal(err)
	}
	for _, entry := range entries {
		body, err := os.ReadFile(filepath.Join(starterProof.CacheDirectory, "blobs", "sha256", entry.Name()))
		if err != nil {
			t.Fatal(err)
		}
		cachedBlob(t, cache, body)
	}
	// The starter's config descriptor is required by the general OCI verifier.
	_, oldDigest, _ := strings.Cut(starterRef, "@sha256:")
	body, _ := os.ReadFile(filepath.Join(cache, "blobs", "sha256", oldDigest))
	var manifest map[string]any
	_ = json.Unmarshal(body, &manifest)
	config := []byte(`{"architecture":"amd64","os":"linux"}`)
	hash := cachedBlob(t, cache, config)
	manifest["config"] = map[string]any{"mediaType": "application/vnd.oci.image.config.v1+json", "digest": "sha256:" + hash, "size": len(config)}
	body, _ = json.Marshal(manifest)
	starterRef = "oci://example.test/starter@sha256:" + cachedBlob(t, cache, body)
	fleet, _ := gitBindingFixture(t)
	if err := os.CopyFS(fleet, os.DirFS(source)); err != nil {
		t.Fatal(err)
	}
	gitTest(t, fleet, "add", ".")
	gitTest(t, fleet, "commit", "-m", "starter")
	gitTest(t, fleet, "push")
	stub, host := networkSafetyFixture()
	host.SSHAlias = "admin@server-1"
	host.ManagementAddress = "192.0.2.10"
	stub.answers["server-1"][freeSpaceCommand] = "Filesystem 1B-blocks Used Available Use% Mounted on\n/dev/sda1 200000000000 20000000000 180000000000 10% /\n"
	spec := c.Spec
	spec.Target.Intent = clusterinit.ModeInstall
	spec.Release.StarterRef = starterRef
	spec.Hosts = []Host{host}
	spec.Execution.NoPush = true
	body, _ = os.ReadFile(spec.Release.RecordFile)
	var record InstallerReleaseRecord
	_ = json.Unmarshal(body, &record)
	record.Artifacts.StarterRef = starterRef
	writeReleaseTestRecord(t, spec, record)
	if err := os.WriteFile(spec.Platform.ConfigFile, []byte(baseConfig("demo")+"KUBE_DC_INIT_REPO="+fleet+"\nEXT_NET_INTERFACE=eth1\nEXT_NET_VLAN_ID=1103\n"), 0600); err != nil {
		t.Fatal(err)
	}
	c, err = Compile(spec)
	if err != nil {
		t.Fatal(err)
	}
	plan, err := clusterinit.BuildPlan(&c.Init, clusterinit.FleetState{})
	if err != nil {
		t.Fatal(err)
	}
	review, err := CreateSafetyReview(context.Background(), c, SafetyReviewOptions{ArtifactCache: cache, CLIPath: cli, Directory: filepath.Join(t.TempDir(), "prepared"), Scaffold: clusterinit.ScaffoldOptions{FleetRepo: fleet, Plan: plan}, SSH: stub, Runner: func(root string) ports.ScriptRunner {
		runner := &reviewFixtureRunner{root: root}
		if len(shared) > 0 {
			runner.shared = shared[0]
		}
		return runner
	}})
	if err != nil {
		t.Fatal(err)
	}
	return c, review, stub, cli
}

func TestSafetyReviewBindsGeneratorOutputWithoutAuthorizingApply(t *testing.T) {
	c, review, stub, cli := safetyReviewFixture(t)
	if review.ReadyToApply || review.Environment["NODE_CIDR"] != "192.0.2.0/24" || review.Environment["INGRESS_HOST_CIDR"] != "192.0.2.10/32" || len(review.Prepared.Files) == 0 || len(review.Unresolved) == 0 || len(review.HostEffects[0].ScriptBody) == 0 {
		t.Fatal("review lost generated effects or opened production")
	}
	path := filepath.Join(t.TempDir(), "review.json")
	if err := SaveSafetyReview(path, review); err != nil {
		t.Fatal(err)
	}
	loaded, err := LoadSafetyReview(path)
	if err != nil {
		t.Fatal(err)
	}
	if err := RecheckSafetyReview(context.Background(), c, loaded, stub, cli); err != nil {
		t.Fatal(err)
	}
	if stub.writeCalls != 0 || stub.uncappedRuns != 0 {
		t.Fatal("read-only review made host writes")
	}
	if gitTest(t, review.Git.Directory, "status", "--porcelain") != "" {
		t.Fatal("review changed Git checkout")
	}
}

func TestSafetyReviewRejectsNodeNetworkOutsideManagementLAN(t *testing.T) {
	c, review, stub, cli := safetyReviewFixture(t)
	body, err := os.ReadFile(c.Spec.Platform.ConfigFile)
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(c.Spec.Platform.ConfigFile, append(body, []byte(clusterinit.KeySpecVersion+"=1\nNODE_CIDR=198.51.100.0/24\n")...), 0600); err != nil {
		t.Fatal(err)
	}
	c, err = Compile(c.Spec)
	if err != nil {
		t.Fatal(err)
	}
	plan, err := clusterinit.BuildPlan(&c.Init, clusterinit.FleetState{})
	if err != nil {
		t.Fatal(err)
	}
	directory := filepath.Join(t.TempDir(), "prepared")
	generated := false
	_, err = CreateSafetyReview(context.Background(), c, SafetyReviewOptions{ArtifactCache: review.Artifacts.CacheDirectory, CLIPath: cli, Directory: directory, SSH: stub, Scaffold: clusterinit.ScaffoldOptions{FleetRepo: c.Init.Repo, Plan: plan}, Runner: func(root string) ports.ScriptRunner {
		generated = true
		return &reviewFixtureRunner{root: root}
	}})
	if err == nil || !strings.Contains(err.Error(), "management address must be usable in NODE_CIDR") {
		t.Fatalf("wrong node network was not rejected: %v", err)
	}
	if _, err := os.Stat(directory); !os.IsNotExist(err) || generated || stub.writeCalls != 0 {
		t.Fatal("invalid address plan generated files or wrote to a host")
	}
}

func TestSafetyReviewPathsCannotChangeFleetCheckout(t *testing.T) {
	c, review, stub, cli := safetyReviewFixture(t)
	alias := filepath.Join(t.TempDir(), "fleet-alias")
	if err := os.Symlink(review.Git.Directory, alias); err != nil {
		t.Fatal(err)
	}
	plan, err := clusterinit.BuildPlan(&c.Init, clusterinit.FleetState{})
	if err != nil {
		t.Fatal(err)
	}
	for _, parent := range []string{review.Git.Directory, alias} {
		if err := SaveSafetyReview(filepath.Join(parent, "review.json"), review); err == nil {
			t.Fatal("review file inside checkout was accepted")
		}
		_, err := CreateSafetyReview(context.Background(), c, SafetyReviewOptions{ArtifactCache: review.Artifacts.CacheDirectory, CLIPath: cli, Directory: filepath.Join(parent, "prepared"), SSH: stub, Scaffold: clusterinit.ScaffoldOptions{FleetRepo: c.Init.Repo, Plan: plan}, Runner: func(string) ports.ScriptRunner {
			t.Fatal("unsafe destination reached generator")
			return nil
		}})
		if err == nil || !strings.Contains(err.Error(), "outside the selected Git checkout") {
			t.Fatalf("unsafe preparation path was not rejected: %v", err)
		}
	}
	if gitTest(t, review.Git.Directory, "status", "--porcelain") != "" || stub.writeCalls != 0 {
		t.Fatal("review path rejection changed installation targets")
	}
}

func TestSafetyReviewDriftCausesNoInstallationWrites(t *testing.T) {
	for _, variant := range []string{"expired", "future", "target", "network", "address", "release", "git", "prepared"} {
		t.Run(variant, func(t *testing.T) {
			c, review, stub, cli := safetyReviewFixture(t)
			switch variant {
			case "expired":
				review.CreatedAt = time.Now().Add(-6 * time.Minute)
				review.Hash = safetyReviewHash(review)
			case "future":
				review.CreatedAt = time.Now().Add(time.Minute)
				review.Hash = safetyReviewHash(review)
			case "target":
				stub.answers["server-1"]["cat /etc/machine-id"] = strings.Repeat("b", 32)
			case "network":
				stub.answers["server-1"][networkConfigurationCommand] = strings.Repeat("b", 64) + "  -\n"
			case "address":
				stub.answers["server-1"]["ip -j addr show"] = strings.ReplaceAll(stub.answers["server-1"]["ip -j addr show"], "192.0.2.10", "192.0.2.11")
			case "release":
				_ = os.WriteFile(c.Spec.Release.RecordFile, []byte(`{"changed":true}`), 0600)
			case "git":
				_ = os.WriteFile(filepath.Join(review.Git.Directory, "foreign.txt"), []byte("operator work"), 0600)
			case "prepared":
				_ = os.WriteFile(filepath.Join(review.Prepared.Directory, review.Prepared.Files[0].Path), []byte("changed"), 0600)
			}
			if err := RecheckSafetyReview(context.Background(), c, review, stub, cli); err == nil {
				t.Fatal("changed review accepted")
			}
			if stub.writeCalls != 0 || stub.uncappedRuns != 0 {
				t.Fatal("review failure wrote to host")
			}
			if _, err := os.Stat(filepath.Join(review.Git.Directory, "clusters", "demo")); !os.IsNotExist(err) {
				t.Fatal("review failure wrote installation files")
			}
		})
	}
}

const testRegistryCredential = "apiVersion: v1\nkind: Secret\nmetadata:\n  name: registry-depot-auth\n  namespace: kube-dc\nstringData:\n  htpasswd: ENC[AES256_GCM,data:fixture,iv:xyz,type:str]\nsops:\n  mac: ENC[AES256_GCM,data:mac,iv:xyz,type:str]\n"

func TestReviewedSharedCredentialSurvivesNextClusterReview(t *testing.T) {
	c, review, stub, cli := safetyReviewFixture(t, map[string]string{"platform/registry-depot/secret.enc.yaml": testRegistryCredential})
	if err := clusterinit.PublishPreparedScaffold(context.Background(), review.Prepared, review.Git.Directory, func(ctx context.Context) error { return RecheckSafetyReview(ctx, c, review, stub, cli) }); err != nil {
		t.Fatal(err)
	}
	gitTest(t, review.Git.Directory, "add", ".")
	gitTest(t, review.Git.Directory, "commit", "-m", "first cluster")
	gitTest(t, review.Git.Directory, "push")
	if err := BindStarterSource(review.Artifacts, c.Spec.Release.StarterRef, review.Git.Directory); err != nil {
		t.Fatal(err)
	}
	spec := c.Spec
	spec.Name = "next"
	if err := os.WriteFile(spec.Platform.ConfigFile, []byte(baseConfig("next")+"KUBE_DC_INIT_REPO="+review.Git.Directory+"\nEXT_NET_INTERFACE=eth1\nEXT_NET_VLAN_ID=1103\n"), 0600); err != nil {
		t.Fatal(err)
	}
	c, err := Compile(spec)
	if err != nil {
		t.Fatal(err)
	}
	plan, err := clusterinit.BuildPlan(&c.Init, clusterinit.FleetState{})
	if err != nil {
		t.Fatal(err)
	}
	second, err := CreateSafetyReview(context.Background(), c, SafetyReviewOptions{ArtifactCache: review.Artifacts.CacheDirectory, CLIPath: cli, Directory: filepath.Join(t.TempDir(), "prepared"), Scaffold: clusterinit.ScaffoldOptions{FleetRepo: review.Git.Directory, Plan: plan}, SSH: stub, Runner: func(root string) ports.ScriptRunner { return &reviewFixtureRunner{root: root} }})
	if err != nil {
		t.Fatal(err)
	}
	for _, file := range second.Prepared.Files {
		if file.Path == "platform/registry-depot/secret.enc.yaml" {
			t.Fatal("second cluster tries to overwrite shared credential")
		}
	}
}

func TestReviewRefusesUnexpectedOrPlaintextSharedWrites(t *testing.T) {
	for _, variant := range []string{"unexpected", "plaintext", "trailing-resource", "trailing-plaintext", "trailing-empty"} {
		t.Run(variant, func(t *testing.T) {
			c, review, stub, cli := safetyReviewFixture(t)
			shared := map[string]string{"platform/foreign.yaml": "foreign change\n"}
			switch variant {
			case "plaintext":
				shared = map[string]string{"platform/registry-depot/secret.enc.yaml": strings.ReplaceAll(testRegistryCredential, "ENC[AES256_GCM,data:fixture,iv:xyz,type:str]", "not-a-secret")}
			case "trailing-resource":
				shared = map[string]string{"platform/registry-depot/secret.enc.yaml": testRegistryCredential + "---\napiVersion: v1\nkind: ConfigMap\nmetadata:\n  name: foreign\ndata:\n  key: value\n"}
			case "trailing-plaintext":
				shared = map[string]string{"platform/registry-depot/secret.enc.yaml": testRegistryCredential + "---\napiVersion: v1\nkind: Secret\nmetadata:\n  name: foreign\nstringData:\n  password: plaintext\n"}
			case "trailing-empty":
				shared = map[string]string{"platform/registry-depot/secret.enc.yaml": testRegistryCredential + "---\n"}
			}
			plan, err := clusterinit.BuildPlan(&c.Init, clusterinit.FleetState{})
			if err != nil {
				t.Fatal(err)
			}
			directory := filepath.Join(t.TempDir(), "rejected")
			_, err = CreateSafetyReview(context.Background(), c, SafetyReviewOptions{ArtifactCache: review.Artifacts.CacheDirectory, CLIPath: cli, Directory: directory, Scaffold: clusterinit.ScaffoldOptions{FleetRepo: review.Git.Directory, Plan: plan}, SSH: stub, Runner: func(root string) ports.ScriptRunner { return &reviewFixtureRunner{root: root, shared: shared} }})
			if err == nil {
				t.Fatal("unreviewed shared change accepted")
			}
			if _, err := os.Stat(directory); !os.IsNotExist(err) {
				t.Fatal("failed private preparation was not removed")
			}
			if gitTest(t, review.Git.Directory, "status", "--porcelain") != "" || stub.writeCalls != 0 {
				t.Fatal("failed preparation changed installation targets")
			}
		})
	}
}

func TestSafetyReviewFileRejectsUnknownFieldsTamperingAndPublicModes(t *testing.T) {
	_, review, _, _ := safetyReviewFixture(t)
	for _, variant := range []string{"unknown", "tamper", "public", "trailing"} {
		t.Run(variant, func(t *testing.T) {
			path := filepath.Join(t.TempDir(), "review.json")
			if err := SaveSafetyReview(path, review); err != nil {
				t.Fatal(err)
			}
			body, err := os.ReadFile(path)
			if err != nil {
				t.Fatal(err)
			}
			switch variant {
			case "unknown":
				body = append([]byte(`{"unknown":true,`), body[1:]...)
			case "tamper":
				body = []byte(strings.Replace(string(body), `"readyToApply": false`, `"readyToApply": true`, 1))
			case "trailing":
				body = append(body, []byte("\n{}\n")...)
			case "public":
				if err := os.Chmod(path, 0644); err != nil {
					t.Fatal(err)
				}
			}
			if err := os.WriteFile(path, body, 0600); err != nil {
				t.Fatal(err)
			}
			if _, err := LoadSafetyReview(path); err == nil {
				t.Fatal("invalid review file accepted")
			}
		})
	}
}
