package clusterinit

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// --- fixtures: the add-cluster.sh-generated shapes the patchers target ---

const genPlatformYAML = `apiVersion: kustomize.toolkit.fluxcd.io/v1
kind: Kustomization
metadata:
  name: platform
  namespace: flux-system
spec:
  dependsOn:
    - name: infra-core
  interval: 10m
  retryInterval: 2m
  timeout: 15m
  path: ./platform
  prune: false
  force: true
`

const genKustomizationYAML = `apiVersion: kustomize.config.k8s.io/v1beta1
kind: Kustomization
resources:
  - infrastructure.yaml
  - platform.yaml
  - secrets.enc.yaml
configMapGenerator:
  - name: cluster-config
    namespace: flux-system
    envs:
      - cluster-config.env
generatorOptions:
  disableNameSuffixHash: true
`

func localSpec() ObjectStorageSpec {
	return ObjectStorageSpec{
		Mode:      RookCephLocal,
		OSDNode:   "host6-a",
		OSDSizeGB: 500,
	}
}

// --- overlay rendering ---

func TestObjectStorageOverlay_FlatName(t *testing.T) {
	body := objectStorageOverlayYAML("atlantis", localSpec())
	for _, want := range []string{
		"- ../../../infrastructure/object-storage/modes/rook-ceph-local",
		"- ../../../infrastructure/object-storage/bucket-provisioning",
		"- ../../../infrastructure/object-storage/exposure",
	} {
		if !strings.Contains(body, want) {
			t.Errorf("overlay missing %q\nFULL:\n%s", want, body)
		}
	}
	if strings.Contains(body, "../../../../") {
		t.Errorf("flat name must use exactly 3 ups\nFULL:\n%s", body)
	}
}

func TestObjectStorageOverlay_NestedNameDepth(t *testing.T) {
	// clusters/eu/dc1/object-storage is one level deeper — the live
	// nested siblings use 4 ups.
	body := objectStorageOverlayYAML("eu/dc1", localSpec())
	if !strings.Contains(body, "- ../../../../infrastructure/object-storage/modes/rook-ceph-local") {
		t.Errorf("nested name must use 4 ups\nFULL:\n%s", body)
	}
}

func TestObjectStorageOverlay_NoExposure(t *testing.T) {
	spec := localSpec()
	spec.NoS3Exposure = true
	body := objectStorageOverlayYAML("atlantis", spec)
	if strings.Contains(body, "exposure") && strings.Contains(body, "- ../../../infrastructure/object-storage/exposure") {
		t.Errorf("--no-s3-exposure must omit the exposure resource\nFULL:\n%s", body)
	}
	if !strings.Contains(body, "bucket-provisioning") {
		t.Errorf("bucket-provisioning must stay (Mimir/Loki OBCs)\nFULL:\n%s", body)
	}
}

// --- Flux layer rendering ---

func TestInfraObjectStorageYAML_Shape(t *testing.T) {
	body := infraObjectStorageYAML("eu/dc1", ObjectStorageSpec{Mode: RookCephPVC})
	for _, want := range []string{
		"name: infra-object-storage",
		"- name: infra-core",
		"path: ./clusters/eu/dc1/object-storage",
		"prune: false",
		"force: true",
		"kind: CephCluster",
		"name: rook-ceph",
		"namespace: rook-ceph",
		"name: sops-age",
		"name: cluster-config",
	} {
		if !strings.Contains(body, want) {
			t.Errorf("Flux layer missing %q\nFULL:\n%s", want, body)
		}
	}
	// The CephObjectStore anti-pattern must never sneak in as a
	// healthCheck (no Ready condition — hangs the Kustomization).
	if strings.Contains(body, "kind: CephObjectStore") {
		t.Errorf("CephObjectStore must not be a healthCheck\nFULL:\n%s", body)
	}
}

// --- patchers ---

func TestPatchPlatformDependsOn_GeneratedShape(t *testing.T) {
	lines := strings.Split(genPlatformYAML, "\n")
	out, changed, err := patchPlatformDependsOn(lines)
	if err != nil || !changed {
		t.Fatalf("patch: changed=%v err=%v", changed, err)
	}
	body := strings.Join(out, "\n")
	want := "  dependsOn:\n    - name: infra-core\n    - name: infra-object-storage\n  interval: 10m"
	if !strings.Contains(body, want) {
		t.Errorf("dependsOn insertion wrong:\n%s", body)
	}
}

func TestPatchPlatformDependsOn_Idempotent(t *testing.T) {
	lines := strings.Split(genPlatformYAML, "\n")
	once, _, _ := patchPlatformDependsOn(lines)
	twice, changed, err := patchPlatformDependsOn(once)
	if err != nil {
		t.Fatalf("second patch: %v", err)
	}
	if changed {
		t.Error("second patch must be a no-op")
	}
	if strings.Count(strings.Join(twice, "\n"), "infra-object-storage") != 1 {
		t.Errorf("duplicate insertion:\n%s", strings.Join(twice, "\n"))
	}
}

func TestPatchPlatformDependsOn_MissingBlockErrors(t *testing.T) {
	lines := []string{"spec:", "  interval: 10m"}
	if _, _, err := patchPlatformDependsOn(lines); err == nil {
		t.Fatal("missing dependsOn must error (silent skip = platform races the OBC provisioner)")
	}
}

func TestPatchPlatformDependsOn_CommentDoesNotFalsePositive(t *testing.T) {
	// Reviewer P3 2026-07-04: a COMMENT mentioning the layer must not
	// trip the idempotence check — the actual dependsOn entry is what
	// matters. Silent skip here would ship the platform/OBC race.
	commented := "# platform waits for infra-object-storage (see PRD)\n" + genPlatformYAML
	out, changed, err := patchPlatformDependsOn(strings.Split(commented, "\n"))
	if err != nil || !changed {
		t.Fatalf("comment must not suppress patching: changed=%v err=%v", changed, err)
	}
	if !strings.Contains(strings.Join(out, "\n"), "    - name: infra-object-storage") {
		t.Errorf("entry not inserted despite comment:\n%s", strings.Join(out, "\n"))
	}
}

func TestPatchKustomizationResources_CommentDoesNotFalsePositive(t *testing.T) {
	commented := "# resources include infra-object-storage.yaml eventually\n" + genKustomizationYAML
	out, changed, err := patchKustomizationResources(strings.Split(commented, "\n"))
	if err != nil || !changed {
		t.Fatalf("comment must not suppress patching: changed=%v err=%v", changed, err)
	}
	if !strings.Contains(strings.Join(out, "\n"), "  - infra-object-storage.yaml") {
		t.Errorf("entry not inserted despite comment:\n%s", strings.Join(out, "\n"))
	}
}

func TestPatchKustomizationResources_GeneratedShape(t *testing.T) {
	lines := strings.Split(genKustomizationYAML, "\n")
	out, changed, err := patchKustomizationResources(lines)
	if err != nil || !changed {
		t.Fatalf("patch: changed=%v err=%v", changed, err)
	}
	body := strings.Join(out, "\n")
	want := "  - infrastructure.yaml\n  - infra-object-storage.yaml\n  - platform.yaml"
	if !strings.Contains(body, want) {
		t.Errorf("resources insertion wrong:\n%s", body)
	}
}

func TestPatchKustomizationResources_IdempotentAndMissing(t *testing.T) {
	once, _, _ := patchKustomizationResources(strings.Split(genKustomizationYAML, "\n"))
	_, changed, err := patchKustomizationResources(once)
	if err != nil || changed {
		t.Fatalf("second patch must no-op cleanly: changed=%v err=%v", changed, err)
	}
	if _, _, err := patchKustomizationResources([]string{"resources:", "  - platform.yaml"}); err == nil {
		t.Fatal("missing infrastructure.yaml entry must error")
	}
}

// --- env keys ---

func TestObjectStorageEnvKeys_PerMode(t *testing.T) {
	cases := []struct {
		name string
		spec ObjectStorageSpec
		want []string // "KEY=VALUE" expectations, in order
	}{
		{
			"local with device",
			ObjectStorageSpec{Mode: RookCephLocal, OSDNode: "host6-a", OSDSizeGB: 500, OSDDevice: "sdb"},
			[]string{"S3_HOSTNAME=s3.atlantis.example.com", "CEPH_LOCAL_OSD_NODE=host6-a", "CEPH_LOCAL_OSD_SIZE_GB=500", "CEPH_LOCAL_OSD_DEVICE=sdb"},
		},
		{
			"local default device omitted",
			ObjectStorageSpec{Mode: RookCephLocal, OSDNode: "host6-a", OSDSizeGB: 500},
			[]string{"S3_HOSTNAME=s3.atlantis.example.com", "CEPH_LOCAL_OSD_NODE=host6-a", "CEPH_LOCAL_OSD_SIZE_GB=500"},
		},
		{
			"multi-node sorted slots",
			ObjectStorageSpec{Mode: RookCephMultiNode, CephNodes: map[string]string{
				"host7-a": "sdc", "host5-a": "sdb", "host6-a": "sdb",
			}},
			[]string{
				"S3_HOSTNAME=s3.atlantis.example.com",
				"CEPH_NODE_1=host5-a", "CEPH_NODE_1_DEVICE=sdb",
				"CEPH_NODE_2=host6-a", "CEPH_NODE_2_DEVICE=sdb",
				"CEPH_NODE_3=host7-a", "CEPH_NODE_3_DEVICE=sdc",
			},
		},
		{
			"pvc with defaults elided",
			ObjectStorageSpec{Mode: RookCephPVC, StorageClass: "fast-ssd"},
			[]string{"S3_HOSTNAME=s3.atlantis.example.com", "CEPH_OSD_STORAGE_CLASS=fast-ssd"},
		},
		{
			"pvc explicit counts",
			ObjectStorageSpec{Mode: RookCephPVC, StorageClass: "fast-ssd", OSDCount: 3, OSDVolumeSizeGB: 400},
			[]string{"S3_HOSTNAME=s3.atlantis.example.com", "CEPH_OSD_STORAGE_CLASS=fast-ssd", "CEPH_OSD_COUNT=3", "CEPH_OSD_VOLUME_SIZE_GB=400"},
		},
		{
			"explicit s3 hostname wins",
			ObjectStorageSpec{Mode: RookCephPVC, StorageClass: "fast-ssd", S3Hostname: "objects.example.net"},
			[]string{"S3_HOSTNAME=objects.example.net", "CEPH_OSD_STORAGE_CLASS=fast-ssd"},
		},
		{
			// OS-4: disabled carries exactly the suspend gate — no
			// S3_HOSTNAME (there is no S3 endpoint to name).
			"disabled → suspend key only",
			ObjectStorageSpec{Mode: RookDisabled},
			[]string{"OBJECT_STORAGE_DISABLED=true"},
		},
	}
	for _, tc := range cases {
		tc := tc
		t.Run(tc.name, func(t *testing.T) {
			kv := objectStorageEnvKeys("atlantis.example.com", tc.spec)
			var got []string
			for _, p := range kv {
				got = append(got, p[0]+"="+p[1])
			}
			if strings.Join(got, ";") != strings.Join(tc.want, ";") {
				t.Errorf("env keys mismatch\n got: %v\nwant: %v", got, tc.want)
			}
		})
	}
}

// --- end-to-end writer ---

// seedScaffold builds a temp fleet with the add-cluster.sh-generated
// file shapes the writer patches.
func seedScaffold(t *testing.T, clusterName string) string {
	t.Helper()
	repo := t.TempDir()
	dir := filepath.Join(repo, "clusters", clusterName)
	if err := os.MkdirAll(dir, 0o755); err != nil {
		t.Fatal(err)
	}
	write := func(name, body string) {
		t.Helper()
		if err := os.WriteFile(filepath.Join(dir, name), []byte(body), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	write("platform.yaml", genPlatformYAML)
	write("kustomization.yaml", genKustomizationYAML)
	write("cluster-config.env", "CLUSTER_NAME="+clusterName+"\nDOMAIN=atlantis.example.com\n")
	return repo
}

func TestWriteObjectStorage_EndToEnd(t *testing.T) {
	repo := seedScaffold(t, "atlantis")
	spec := ObjectStorageSpec{Mode: RookCephMultiNode, CephNodes: map[string]string{
		"host5-a": "sdb", "host6-a": "sdb", "host7-a": "sdc",
	}}
	if err := WriteObjectStorage(repo, "atlantis", "atlantis.example.com", spec, nil); err != nil {
		t.Fatalf("WriteObjectStorage: %v", err)
	}
	dir := filepath.Join(repo, "clusters", "atlantis")

	overlay, err := os.ReadFile(filepath.Join(dir, "object-storage", "kustomization.yaml"))
	if err != nil {
		t.Fatalf("overlay not written: %v", err)
	}
	if !strings.Contains(string(overlay), "modes/rook-ceph-multi-node") {
		t.Errorf("overlay wrong mode:\n%s", overlay)
	}

	flux, err := os.ReadFile(filepath.Join(dir, "infra-object-storage.yaml"))
	if err != nil {
		t.Fatalf("Flux layer not written: %v", err)
	}
	if !strings.Contains(string(flux), "path: ./clusters/atlantis/object-storage") {
		t.Errorf("Flux layer wrong path:\n%s", flux)
	}

	platform, _ := os.ReadFile(filepath.Join(dir, "platform.yaml"))
	if !strings.Contains(string(platform), "- name: infra-object-storage") {
		t.Errorf("platform dependsOn not patched:\n%s", platform)
	}

	kust, _ := os.ReadFile(filepath.Join(dir, "kustomization.yaml"))
	if !strings.Contains(string(kust), "- infra-object-storage.yaml") {
		t.Errorf("kustomization resources not patched:\n%s", kust)
	}

	env, _ := os.ReadFile(filepath.Join(dir, "cluster-config.env"))
	for _, want := range []string{
		"S3_HOSTNAME=s3.atlantis.example.com",
		"CEPH_NODE_1=host5-a", "CEPH_NODE_3_DEVICE=sdc",
	} {
		if !strings.Contains(string(env), want) {
			t.Errorf("env missing %q\nFULL:\n%s", want, env)
		}
	}
}

func TestWriteObjectStorage_DisabledDegradedWiring(t *testing.T) {
	// OS-4 v2: disabled writes the DEGRADED wiring — the suspend env
	// key + the grafana-pg backup-path patches — but never the
	// storage wiring (no overlay, no Flux layer, no dependsOn: a
	// dangling dependsOn would block platform forever).
	repo := seedScaffold(t, "atlantis")
	if err := WriteObjectStorage(repo, "atlantis", "atlantis.example.com",
		ObjectStorageSpec{Mode: RookDisabled}, nil); err != nil {
		t.Fatalf("disabled wiring: %v", err)
	}
	dir := filepath.Join(repo, "clusters", "atlantis")

	// Storage wiring absent.
	if _, err := os.Stat(filepath.Join(dir, "object-storage")); !os.IsNotExist(err) {
		t.Error("disabled must not create the overlay dir")
	}
	if _, err := os.Stat(filepath.Join(dir, "infra-object-storage.yaml")); !os.IsNotExist(err) {
		t.Error("disabled must not write the Flux layer")
	}
	platform, _ := os.ReadFile(filepath.Join(dir, "platform.yaml"))
	if strings.Contains(string(platform), "- name: infra-object-storage") {
		t.Error("disabled must not patch platform dependsOn (dangling dependsOn blocks platform forever)")
	}

	// Degraded wiring present: suspend key + the three patch targets.
	env, _ := os.ReadFile(filepath.Join(dir, "cluster-config.env"))
	if !strings.Contains(string(env), "OBJECT_STORAGE_DISABLED=true") {
		t.Errorf("suspend key missing from env:\n%s", env)
	}
	for _, want := range []string{
		"patches:",
		"path: /spec/backup",
		"name: grafana-pg",
		"name: grafana-pg-backups",
		"name: grafana-pg-daily",
		"$patch: delete",
	} {
		if !strings.Contains(string(platform), want) {
			t.Errorf("platform.yaml missing degraded patch fragment %q\nFULL:\n%s", want, platform)
		}
	}

	// Idempotent: second run changes nothing (exact marker match).
	before := string(platform)
	if err := WriteObjectStorage(repo, "atlantis", "atlantis.example.com",
		ObjectStorageSpec{Mode: RookDisabled}, nil); err != nil {
		t.Fatalf("second disabled run: %v", err)
	}
	after, _ := os.ReadFile(filepath.Join(dir, "platform.yaml"))
	if before != string(after) {
		t.Error("second run must be a no-op (duplicate patches: block would be invalid YAML)")
	}
}

func TestPatchPlatformDisabled_ExistingPatchesBlockErrors(t *testing.T) {
	// A hand-edited platform.yaml that already carries a patches:
	// block must ERROR — appending a second `patches:` key would be
	// duplicate-key YAML. The operator merges manually.
	lines := strings.Split(genPlatformYAML+"  patches:\n    - patch: |\n        {}\n", "\n")
	if _, _, err := patchPlatformDisabledObjectStorage(lines); err == nil {
		t.Fatal("existing patches: block must error, not append a duplicate key")
	}
}

func TestWriteObjectStorage_StubModeErrors(t *testing.T) {
	repo := seedScaffold(t, "atlantis")
	err := WriteObjectStorage(repo, "atlantis", "atlantis.example.com",
		ObjectStorageSpec{Mode: RookExternalS3}, nil)
	if err == nil {
		t.Fatal("stub mode must error (defense in depth — Validate normally refuses it upstream)")
	}
}

func TestWriteObjectStorage_NestedName(t *testing.T) {
	repo := seedScaffold(t, "eu/dc1")
	spec := ObjectStorageSpec{Mode: RookCephPVC, StorageClass: "fast-ssd"}
	if err := WriteObjectStorage(repo, "eu/dc1", "eu-dc1.example.net", spec, nil); err != nil {
		t.Fatalf("WriteObjectStorage nested: %v", err)
	}
	overlay, err := os.ReadFile(filepath.Join(repo, "clusters", "eu", "dc1", "object-storage", "kustomization.yaml"))
	if err != nil {
		t.Fatalf("nested overlay not written: %v", err)
	}
	if !strings.Contains(string(overlay), "../../../../infrastructure/object-storage/modes/rook-ceph-pvc") {
		t.Errorf("nested overlay depth wrong:\n%s", overlay)
	}
}
