// OS-2 — object-storage scaffold writer (installer-object-storage-
// scaffold.md §4.2). Runs as Scaffold step (7), after add-cluster.sh +
// env deltas + customInterfaces, before the commit. For the three
// rook-* modes it writes the full wiring a new cluster needs so the
// platform layer can converge (Mimir/Loki hard-require the system S3
// buckets — see the PRD §1.4):
//
//  1. clusters/<name>/object-storage/kustomization.yaml — composes the
//     shared fleet mode + bucket-provisioning (+ exposure unless
//     --no-s3-exposure), mirroring the live sibling overlays.
//  2. clusters/<name>/infra-object-storage.yaml — the Flux layer:
//     dependsOn infra-core, healthCheck CephCluster/rook-ceph (NEVER
//     CephObjectStore — it has no Ready condition Flux can poll),
//     prune:false + force:true (losing OSD/MON state = losing tenant
//     bucket data).
//  3. platform.yaml — dependsOn gains infra-object-storage (post-patch
//     in Go per the 2026-07-04 design call: no more bash conditional
//     surface after the add-cluster.sh heredoc footgun).
//  4. kustomization.yaml — resources gains infra-object-storage.yaml.
//  5. cluster-config.env — per-mode CEPH_* + S3_HOSTNAME env keys.
//
// `disabled` writes the DEGRADED wiring instead (OS-4 v2, PRD §6):
// the OBJECT_STORAGE_DISABLED=true env key (suspends Mimir + Loki
// fleet-side) plus a platform.yaml spec.patches block removing
// grafana-pg's backup path. It never touches the STORAGE wiring —
// no overlay, no Flux layer, and platform keeps dependsOn:
// [infra-core] only (a dangling dependsOn on a Kustomization that
// doesn't exist would block platform forever).
//
// Reconciliation order produced:
//
//	infra-cni → infra-core (Rook operator) → infra-object-storage
//	(data plane) → platform (OBC consumers) → addons
package clusterinit

import (
	"fmt"
	"io"
	"os"
	"path/filepath"
	"sort"
	"strings"

	"github.com/shalb/kube-dc/cli/internal/bootstrap/config"
)

// ObjectStorageSpec bundles the OS-1 InitOptions fields the scaffold
// writer consumes. Passed through ScaffoldOptions/ApplyOptions the
// same way Sets/NodeNICs are (the Plan carries the derived
// FilesToWrite preview; the raw values ride alongside and are
// hash-verified by the apply-plan input check).
type ObjectStorageSpec struct {
	Mode RookMode

	// rook-ceph-local
	OSDNode   string
	OSDSizeGB int
	OSDDevice string // optional; fleet template defaults to loop0

	// rook-ceph-multi-node — node → device; sorted keys map to
	// CEPH_NODE_{1..3} deterministically.
	CephNodes map[string]string

	// rook-ceph-pvc
	StorageClass    string
	OSDCount        int // 0 = fleet default
	OSDVolumeSizeGB int // 0 = fleet default

	// shared
	S3Hostname   string // empty = s3.<domain>
	NoS3Exposure bool
}

// ObjectStorage projects the spec out of InitOptions.
func (o *InitOptions) ObjectStorage() ObjectStorageSpec {
	return ObjectStorageSpec{
		Mode:            o.RookMode,
		OSDNode:         o.RookOSDNode,
		OSDSizeGB:       o.RookOSDSizeGB,
		OSDDevice:       o.RookOSDDevice,
		CephNodes:       o.CephNodes,
		StorageClass:    o.CephStorageClass,
		OSDCount:        o.CephOSDCount,
		OSDVolumeSizeGB: o.CephOSDVolumeSizeGB,
		S3Hostname:      o.S3Hostname,
		NoS3Exposure:    o.NoS3Exposure,
	}
}

// scaffoldsObjectStorage reports whether the mode gets the full
// object-storage wiring. Stub modes never reach the writer (Validate
// fails them closed), but the guard is repeated here for programmatic
// callers.
func scaffoldsObjectStorage(m RookMode) bool {
	switch m {
	case RookCephLocal, RookCephMultiNode, RookCephPVC:
		return true
	}
	return false
}

// WriteObjectStorage performs steps 1–5 above for the rook-* modes.
// `disabled` writes the DEGRADED wiring instead (OS-4 v2, PRD §6):
// the OBJECT_STORAGE_DISABLED=true env key (suspends the Mimir + Loki
// HelmReleases fleet-side) plus a spec.patches block on platform.yaml
// removing grafana-pg's backup path — the Grafana DB runs unprotected
// rather than failing WAL archiving forever against a Pending OBC.
// Error for the stub modes (defense in depth — Validate refuses them
// long before this point).
func WriteObjectStorage(fleetRepo, clusterName, domain string, spec ObjectStorageSpec, out io.Writer) error {
	if out == nil {
		out = io.Discard
	}
	if spec.Mode == "" {
		return nil
	}
	clusterDir := filepath.Join(fleetRepo, "clusters", clusterName)
	if spec.Mode == RookDisabled {
		// Degraded mode: NO overlay, NO Flux layer, NO platform
		// dependsOn (a dangling dependsOn would block platform
		// forever) — just the suspend key + the grafana-pg patches.
		//
		// Fresh-scaffold only (design caveat 2026-07-04): platform
		// Kustomizations run prune:false, so flipping an EXISTING
		// enabled cluster to disabled does not clean up its
		// already-applied OBCs/ScheduledBackups — manual deletion.
		if err := patchFileLines(filepath.Join(clusterDir, "platform.yaml"), patchPlatformDisabledObjectStorage); err != nil {
			return fmt.Errorf("object-storage(disabled): patch platform.yaml: %w", err)
		}
		if err := writeObjectStorageEnv(filepath.Join(clusterDir, "cluster-config.env"), domain, spec); err != nil {
			return fmt.Errorf("object-storage(disabled): env keys: %w", err)
		}
		fmt.Fprintf(out, "[scaffold] object-storage DISABLED (degraded): Mimir+Loki suspended, grafana-pg backup path patched out\n")
		return nil
	}
	if !scaffoldsObjectStorage(spec.Mode) {
		return fmt.Errorf("object-storage: mode %q has no scaffold (fleet-side stub)", spec.Mode)
	}

	// (1) overlay
	overlayDir := filepath.Join(clusterDir, "object-storage")
	if err := os.MkdirAll(overlayDir, 0o755); err != nil {
		return fmt.Errorf("object-storage: mkdir %s: %w", overlayDir, err)
	}
	if err := os.WriteFile(filepath.Join(overlayDir, "kustomization.yaml"),
		[]byte(objectStorageOverlayYAML(clusterName, spec)), 0o644); err != nil {
		return fmt.Errorf("object-storage: write overlay: %w", err)
	}

	// (2) Flux layer
	if err := os.WriteFile(filepath.Join(clusterDir, "infra-object-storage.yaml"),
		[]byte(infraObjectStorageYAML(clusterName, spec)), 0o644); err != nil {
		return fmt.Errorf("object-storage: write infra-object-storage.yaml: %w", err)
	}

	// (3) platform.yaml dependsOn
	platformPath := filepath.Join(clusterDir, "platform.yaml")
	if err := patchFileLines(platformPath, patchPlatformDependsOn); err != nil {
		return fmt.Errorf("object-storage: patch platform.yaml: %w", err)
	}

	// (4) kustomization.yaml resources
	kustPath := filepath.Join(clusterDir, "kustomization.yaml")
	if err := patchFileLines(kustPath, patchKustomizationResources); err != nil {
		return fmt.Errorf("object-storage: patch kustomization.yaml: %w", err)
	}

	// (5) env keys
	if err := writeObjectStorageEnv(filepath.Join(clusterDir, "cluster-config.env"), domain, spec); err != nil {
		return fmt.Errorf("object-storage: env keys: %w", err)
	}

	fmt.Fprintf(out, "[scaffold] object-storage wired (mode=%s, exposure=%t)\n", spec.Mode, !spec.NoS3Exposure)
	return nil
}

// objectStorageOverlayYAML renders clusters/<name>/object-storage/
// kustomization.yaml. The relative depth back to the repo root
// depends on the cluster-name nesting (atlantis → ../../../,
// eu/dc1 → ../../../../) — mirrors the live sibling overlays.
func objectStorageOverlayYAML(clusterName string, spec ObjectStorageSpec) string {
	// clusters/ + name components + object-storage/ dirs deep.
	ups := strings.Repeat("../", 3+strings.Count(clusterName, "/"))
	var b strings.Builder
	b.WriteString("apiVersion: kustomize.config.k8s.io/v1beta1\n")
	b.WriteString("kind: Kustomization\n")
	b.WriteString("# Object-storage overlay for " + clusterName + " — generated by\n")
	b.WriteString("# `kube-dc bootstrap init` (mode=" + string(spec.Mode) + "). Composes the shared\n")
	b.WriteString("# fleet mode with the system-bucket OBCs (Mimir/Loki hard-require\n")
	b.WriteString("# them)")
	if spec.NoS3Exposure {
		b.WriteString(". S3 exposure omitted (--no-s3-exposure): cluster-internal\n# S3 only — add the exposure layer later if a public endpoint is needed.\n")
	} else {
		b.WriteString(" and the public S3 endpoint (Certificate + HTTPRoute for\n# https://${S3_HOSTNAME}).\n")
	}
	b.WriteString("resources:\n")
	b.WriteString("  - " + ups + "infrastructure/object-storage/modes/" + string(spec.Mode) + "\n")
	b.WriteString("  - " + ups + "infrastructure/object-storage/bucket-provisioning\n")
	if !spec.NoS3Exposure {
		b.WriteString("  - " + ups + "infrastructure/object-storage/exposure\n")
	}
	return b.String()
}

// infraObjectStorageYAML renders clusters/<name>/infra-object-storage.yaml
// — the Flux Kustomization. Shape mirrors the live siblings verbatim:
// dependsOn infra-core; healthCheck on CephCluster ONLY (CephObjectStore
// exposes no Ready condition Flux can poll — a healthCheck on it hangs
// the Kustomization forever even when phase=Ready); prune:false +
// force:true because losing OSD/MON state means losing tenant bucket
// data.
func infraObjectStorageYAML(clusterName string, spec ObjectStorageSpec) string {
	var b strings.Builder
	b.WriteString("# Layer 1d: Object storage (Rook Ceph) for " + clusterName + " — generated by\n")
	b.WriteString("# `kube-dc bootstrap init` (mode=" + string(spec.Mode) + ").\n")
	b.WriteString("#\n")
	b.WriteString("# Reconciliation order: infra-cni → infra-core → infra-object-storage → platform\n")
	b.WriteString(`apiVersion: kustomize.toolkit.fluxcd.io/v1
kind: Kustomization
metadata:
  name: infra-object-storage
  namespace: flux-system
spec:
  dependsOn:
    - name: infra-core
  interval: 10m
  retryInterval: 2m
  timeout: 20m
  path: ./clusters/` + clusterName + `/object-storage
  prune: false
  force: true
  sourceRef:
    kind: GitRepository
    name: flux-system
  decryption:
    provider: sops
    secretRef:
      name: sops-age
  postBuild:
    substituteFrom:
      - kind: ConfigMap
        name: cluster-config
      - kind: Secret
        name: cluster-secrets
        optional: true
  healthChecks:
    # CephCluster carries a Ready condition Flux can poll;
    # CephObjectStore does NOT (status.phase only) — never healthCheck
    # it or the Kustomization hangs forever. RGW comes up after the
    # cluster reports Ready.
    - apiVersion: ceph.rook.io/v1
      kind: CephCluster
      name: rook-ceph
      namespace: rook-ceph
`)
	return b.String()
}

// patchFileLines loads a file, applies a line-slice transform, and
// writes it back only when the transform changed something.
func patchFileLines(path string, patch func([]string) ([]string, bool, error)) error {
	body, err := os.ReadFile(path)
	if err != nil {
		return err
	}
	lines := strings.Split(string(body), "\n")
	patched, changed, err := patch(lines)
	if err != nil {
		return fmt.Errorf("%s: %w", filepath.Base(path), err)
	}
	if !changed {
		return nil
	}
	return os.WriteFile(path, []byte(strings.Join(patched, "\n")), 0o644)
}

// patchPlatformDependsOn inserts `- name: infra-object-storage` after
// the LAST item of platform.yaml's dependsOn block. Idempotent: a file
// already naming infra-object-storage is returned unchanged. Errors
// when no dependsOn block exists (add-cluster.sh always emits one —
// a missing block means the file shape changed and silent skipping
// would ship a race: platform reconciling before the OBCs exist).
func patchPlatformDependsOn(lines []string) ([]string, bool, error) {
	for _, l := range lines {
		// Exact structural match — a COMMENT mentioning the layer
		// must not false-positive the no-op check and silently skip
		// the required wiring (reviewer P3 2026-07-04).
		if strings.TrimSpace(l) == "- name: infra-object-storage" {
			return lines, false, nil // already patched
		}
	}
	depIdx := -1
	for i, l := range lines {
		if strings.TrimSpace(l) == "dependsOn:" {
			depIdx = i
			break
		}
	}
	if depIdx == -1 {
		return nil, false, fmt.Errorf("no dependsOn block found (file shape drifted from add-cluster.sh output)")
	}
	// Walk the list items following dependsOn; keep the item indent.
	last := depIdx
	indent := "    "
	for i := depIdx + 1; i < len(lines); i++ {
		t := strings.TrimSpace(lines[i])
		if strings.HasPrefix(t, "- name:") || strings.HasPrefix(t, "#") && last > depIdx {
			if strings.HasPrefix(t, "- name:") {
				indent = lines[i][:len(lines[i])-len(strings.TrimLeft(lines[i], " "))]
			}
			last = i
			continue
		}
		break
	}
	if last == depIdx {
		return nil, false, fmt.Errorf("dependsOn block has no items (unexpected add-cluster.sh output)")
	}
	out := make([]string, 0, len(lines)+1)
	out = append(out, lines[:last+1]...)
	out = append(out, indent+"- name: infra-object-storage")
	out = append(out, lines[last+1:]...)
	return out, true, nil
}

// patchKustomizationResources inserts `- infra-object-storage.yaml`
// into the cluster overlay kustomization's resources list, after the
// infrastructure.yaml entry (reading order: infra layers before
// platform). Idempotent; errors when the resources block is missing.
func patchKustomizationResources(lines []string) ([]string, bool, error) {
	for _, l := range lines {
		// Exact structural match — see patchPlatformDependsOn.
		if strings.TrimSpace(l) == "- infra-object-storage.yaml" {
			return lines, false, nil // already patched
		}
	}
	for i, l := range lines {
		if strings.TrimSpace(l) == "- infrastructure.yaml" {
			indent := l[:len(l)-len(strings.TrimLeft(l, " "))]
			out := make([]string, 0, len(lines)+1)
			out = append(out, lines[:i+1]...)
			out = append(out, indent+"- infra-object-storage.yaml")
			out = append(out, lines[i+1:]...)
			return out, true, nil
		}
	}
	return nil, false, fmt.Errorf("no `- infrastructure.yaml` resources entry found (file shape drifted from add-cluster.sh output)")
}

// disabledPlatformPatchesMarker is the exact-match idempotence line
// for patchPlatformDisabledObjectStorage (trimmed-line equality — a
// comment merely MENTIONING it elsewhere can't false-positive).
const disabledPlatformPatchesMarker = "# OS-4 object-storage-disabled patches (do not duplicate this block)"

// disabledPlatformPatches is the spec.patches block appended to the
// generated platform.yaml in disabled mode. Shape mirrors the live
// fleet's $patch:delete usage (target-selected, name in the patch
// body unused for deletes). Patch targets are shared-tree object
// names — the fleet-side modes/disabled/kustomization.yaml comment
// lists them, and Flux fails LOUDLY on a missing patch target, so a
// fleet-side rename surfaces immediately rather than silently.
const disabledPlatformPatches = `  ` + disabledPlatformPatchesMarker + `
  # (kube-dc installer-object-storage-scaffold PRD section 6): this
  # cluster runs --object-storage-mode=disabled. Remove grafana-pg's
  # backup path so WAL archiving + daily backups don't fail forever
  # against a Pending bucket claim — the Grafana DB runs UNPROTECTED.
  # Mimir + Loki are suspended separately via OBJECT_STORAGE_DISABLED
  # in cluster-config.env. Fresh-scaffold only: flipping an ENABLED
  # cluster to disabled needs manual cleanup (prune:false).
  patches:
    - patch: |
        - op: remove
          path: /spec/backup
      target:
        group: postgresql.cnpg.io
        kind: Cluster
        name: grafana-pg
    - patch: |
        apiVersion: objectbucket.io/v1alpha1
        kind: ObjectBucketClaim
        metadata:
          name: grafana-pg-backups
          namespace: monitoring
        $patch: delete
      target:
        kind: ObjectBucketClaim
        name: grafana-pg-backups
    - patch: |
        apiVersion: postgresql.cnpg.io/v1
        kind: ScheduledBackup
        metadata:
          name: grafana-pg-daily
          namespace: monitoring
        $patch: delete
      target:
        kind: ScheduledBackup
        name: grafana-pg-daily`

// patchPlatformDisabledObjectStorage appends the disabled-mode
// spec.patches block to the generated platform.yaml. The generated
// file ends inside `spec:` (postBuild is its last key), so a
// two-space-indented `patches:` appended at EOF lands under spec.
// Idempotent via the exact marker line; errors if the file already
// carries a DIFFERENT patches: block (hand-edited — appending a
// second one would produce duplicate YAML keys).
func patchPlatformDisabledObjectStorage(lines []string) ([]string, bool, error) {
	for _, l := range lines {
		if strings.TrimSpace(l) == disabledPlatformPatchesMarker {
			return lines, false, nil // already patched
		}
	}
	for _, l := range lines {
		if strings.TrimSpace(l) == "patches:" {
			return nil, false, fmt.Errorf("platform.yaml already has a patches: block (hand-edited?) — merge the OS-4 disabled patches manually, see installer-object-storage-scaffold PRD section 6")
		}
	}
	// Drop trailing blank lines so the block lands directly after the
	// last real line; re-add a single trailing newline via the join.
	end := len(lines)
	for end > 0 && strings.TrimSpace(lines[end-1]) == "" {
		end--
	}
	out := make([]string, 0, end+40)
	out = append(out, lines[:end]...)
	out = append(out, strings.Split(disabledPlatformPatches, "\n")...)
	out = append(out, "")
	return out, true, nil
}

// objectStorageEnvKeys returns the mode's cluster-config.env keys in
// deterministic order. Shared between the writer and the plan's
// files-to-write description so dry-run and apply can't diverge.
//
// Disabled mode carries exactly one key: the OS-4 suspend gate for
// the Mimir + Loki HelmReleases (fleet-side
// `suspend: ${OBJECT_STORAGE_DISABLED:=false}`).
func objectStorageEnvKeys(domain string, spec ObjectStorageSpec) [][2]string {
	if spec.Mode == RookDisabled {
		return [][2]string{{"OBJECT_STORAGE_DISABLED", "true"}}
	}
	var kv [][2]string
	host := spec.S3Hostname
	if host == "" {
		host = "s3." + domain
	}
	kv = append(kv, [2]string{"S3_HOSTNAME", host})

	switch spec.Mode {
	case RookCephLocal:
		kv = append(kv, [2]string{"CEPH_LOCAL_OSD_NODE", spec.OSDNode})
		kv = append(kv, [2]string{"CEPH_LOCAL_OSD_SIZE_GB", fmt.Sprintf("%d", spec.OSDSizeGB)})
		if spec.OSDDevice != "" {
			kv = append(kv, [2]string{"CEPH_LOCAL_OSD_DEVICE", spec.OSDDevice})
		}
	case RookCephMultiNode:
		// Sorted node names → slots 1..3, deterministically.
		nodes := make([]string, 0, len(spec.CephNodes))
		for n := range spec.CephNodes {
			nodes = append(nodes, n)
		}
		sort.Strings(nodes)
		for i, n := range nodes {
			slot := fmt.Sprintf("%d", i+1)
			kv = append(kv, [2]string{"CEPH_NODE_" + slot, n})
			kv = append(kv, [2]string{"CEPH_NODE_" + slot + "_DEVICE", spec.CephNodes[n]})
		}
	case RookCephPVC:
		kv = append(kv, [2]string{"CEPH_OSD_STORAGE_CLASS", spec.StorageClass})
		if spec.OSDCount > 0 {
			kv = append(kv, [2]string{"CEPH_OSD_COUNT", fmt.Sprintf("%d", spec.OSDCount)})
		}
		if spec.OSDVolumeSizeGB > 0 {
			kv = append(kv, [2]string{"CEPH_OSD_VOLUME_SIZE_GB", fmt.Sprintf("%d", spec.OSDVolumeSizeGB)})
		}
	}
	return kv
}

// writeObjectStorageEnv appends/updates the mode's env keys in
// cluster-config.env, preserving existing order + comments (config.Env
// in-place Set — same machinery as the --set deltas).
func writeObjectStorageEnv(envPath, domain string, spec ObjectStorageSpec) error {
	env, err := config.LoadEnv(envPath)
	if err != nil {
		return err
	}
	for _, kv := range objectStorageEnvKeys(domain, spec) {
		env.Set(kv[0], kv[1])
	}
	return env.Write("")
}
