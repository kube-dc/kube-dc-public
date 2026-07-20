// VM-STORAGE — VM root-disk storage scaffold writer (PRD
// docs/prd/vm-storage-mode.md). Runs as a Scaffold step after
// object-storage, before the commit. Mirrors objectstorage.go, but VM
// storage is an OPTIONAL VM-capability tier (not load-bearing): the
// default `local` writes nothing (VMs use local-path), and the
// `shared-rbd` modes require an object-storage mode that already
// provides the `rbd-pool` CephBlockPool (the rook-ceph-* modes do; the
// installer VERIFIES this — it never creates or owns the pool).
//
// For `shared-rbd` it writes, against the Phase-1 fleet split
// (platform/rbd-vm/{base,goldens-fs}):
//
//  1. clusters/<name>/rbd-vm-goldens/kustomization.yaml — the SELECTED
//     FS golden subset (one file per OS from the shared os/ catalog).
//     Explicit, never all-by-default: the 70 Gi windows-11-golden and
//     every other OS are opt-in via --vm-golden.
//  2. clusters/<name>/rbd-vm.yaml — two Flux Kustomizations: rbd-vm-base
//     (-> platform/rbd-vm/base) + rbd-vm-goldens (-> the subset overlay,
//     dependsOn rbd-vm-base, keeps postBuild for ${S3_HOSTNAME}).
//  3. kustomization.yaml — resources gains `- rbd-vm.yaml`.
//
// `shared-rbd-live-migration` FAILS CLOSED at scaffold time: its Block
// converter overlay carries a RUNTIME value (the live cluster's Envoy
// Gateway ClusterIP, for the converter's S3 egress) plus an unresolved
// Block-golden source catalog (PRD §3.2b) — neither is knowable during
// `bootstrap init`. Enable shared-rbd now; add live migration
// post-install (see clusters/<name>/rbd-vm-block/).
package clusterinit

import (
	"fmt"
	"io"
	"os"
	"path/filepath"
	"regexp"
	"sort"
	"strings"
)

// VMStorageMode selects the VM root-disk storage tier the installer
// scaffolds. OPTIONAL flag, default `local` (never fails when omitted).
type VMStorageMode string

const (
	// VMStorageLocal — node-local (local-path) VM disks. Default; the VM
	// default in every mode. No goldens, no snapshots, no live migration.
	VMStorageLocal VMStorageMode = "local"
	// VMStorageSharedRBD — Ceph-RBD (rbd-vm) VM disks + a selected FS
	// golden subset. Snapshots + fast prepared-image clones. Not
	// live-migratable.
	VMStorageSharedRBD VMStorageMode = "shared-rbd"
	// VMStorageSharedRBDLiveMigration — shared-rbd + the RWX-Block
	// converter for live migration. Installer-deferred (see file header).
	VMStorageSharedRBDLiveMigration VMStorageMode = "shared-rbd-live-migration"
)

// AllVMStorageModes lists the accepted --vm-storage-mode values.
var AllVMStorageModes = []VMStorageMode{
	VMStorageLocal, VMStorageSharedRBD, VMStorageSharedRBDLiveMigration,
}

func joinVMStorageModes(m []VMStorageMode) string {
	out := make([]string, len(m))
	for i, v := range m {
		out[i] = string(v)
	}
	return strings.Join(out, "|")
}

// vmGoldenNameRegex is the golden-OS slug shape (the goldens-fs/os/
// <slug>.yaml basenames: alpine-3.21, debian-12, windows-11-golden, …).
// Anchored whole-string so '=' / '$' / whitespace / path separators are
// structurally excluded (the value lands in a kustomization resource path).
var vmGoldenNameRegex = regexp.MustCompile(`^[a-z0-9](?:[a-z0-9.-]*[a-z0-9])?$`)

// defaultVMGolden is the minimal default when --vm-golden is unset: ONE
// containerdisk-source golden (no S3-mirror dependency, unlike the http/S3
// goldens), so a fresh capacity-constrained cluster imports ~one small OS
// rather than the whole ~200 Gi+ catalog. Windows is never a default.
const defaultVMGolden = "debian-12"

// VMStorageSpec bundles the InitOptions fields the scaffold writer
// consumes. Passed through ScaffoldOptions/ApplyOptions like
// ObjectStorageSpec; the Plan carries the derived FilesToWrite preview
// and the raw values ride alongside, hash-verified by the apply-plan
// input check.
type VMStorageSpec struct {
	Mode VMStorageMode
	// Goldens is the selected FS golden subset (validated against the
	// fleet's goldens-fs/os/ catalog at write time). Empty = the single
	// defaultVMGolden.
	Goldens []string
	// GoldensBlock is the selected Block (live-migration) golden set —
	// carried for forward-compat; live-migration is installer-deferred.
	GoldensBlock []string
}

// VMStorage projects the spec out of InitOptions.
func (o *InitOptions) VMStorage() VMStorageSpec {
	return VMStorageSpec{
		Mode:         o.VMStorageMode,
		Goldens:      o.VMGoldens,
		GoldensBlock: o.VMGoldensBlock,
	}
}

// scaffoldsVMStorage reports whether the mode writes rbd-vm wiring.
func scaffoldsVMStorage(m VMStorageMode) bool {
	switch m {
	case VMStorageSharedRBD, VMStorageSharedRBDLiveMigration:
		return true
	}
	return false
}

// ErrVMStorageLiveMigrationDeferred fails --vm-storage-mode=
// shared-rbd-live-migration closed at scaffold time (see file header).
var ErrVMStorageLiveMigrationDeferred = fmt.Errorf(
	"vm-storage: shared-rbd-live-migration is not yet installer-scaffoldable — its Block converter needs a RUNTIME egress target (the live cluster's Envoy Gateway ClusterIP) and a Block-golden source catalog (PRD docs/prd/vm-storage-mode.md §3.2b), neither known at install time. Install with --vm-storage-mode=shared-rbd now, then add live migration post-install per clusters/<name>/rbd-vm-block/")

// WriteVMStorage scaffolds the rbd-vm wiring for shared-rbd. `local`
// (and empty) write nothing. shared-rbd-live-migration fails closed.
func WriteVMStorage(fleetRepo, clusterName string, spec VMStorageSpec, out io.Writer) error {
	if out == nil {
		out = io.Discard
	}
	if spec.Mode == "" || spec.Mode == VMStorageLocal {
		return nil // VMs use local-path; nothing to scaffold
	}
	if spec.Mode == VMStorageSharedRBDLiveMigration {
		return ErrVMStorageLiveMigrationDeferred
	}
	if !scaffoldsVMStorage(spec.Mode) {
		return fmt.Errorf("vm-storage: mode %q has no scaffold", spec.Mode)
	}

	clusterDir := filepath.Join(fleetRepo, "clusters", clusterName)

	// Resolve + validate the golden subset against the fleet's Phase-1
	// per-OS catalog.
	goldens, err := resolveVMGoldens(fleetRepo, spec.Goldens)
	if err != nil {
		return err
	}

	// (1) subset overlay
	goldensDir := filepath.Join(clusterDir, "rbd-vm-goldens")
	if err := os.MkdirAll(goldensDir, 0o755); err != nil {
		return fmt.Errorf("vm-storage: mkdir %s: %w", goldensDir, err)
	}
	if err := os.WriteFile(filepath.Join(goldensDir, "kustomization.yaml"),
		[]byte(vmGoldensOverlayYAML(clusterName, goldens)), 0o644); err != nil {
		return fmt.Errorf("vm-storage: write goldens overlay: %w", err)
	}

	// (2) rbd-vm.yaml (base + goldens Flux Kustomizations)
	if err := os.WriteFile(filepath.Join(clusterDir, "rbd-vm.yaml"),
		[]byte(rbdVMYAML(clusterName)), 0o644); err != nil {
		return fmt.Errorf("vm-storage: write rbd-vm.yaml: %w", err)
	}

	// (3) kustomization.yaml resources
	if err := patchFileLines(filepath.Join(clusterDir, "kustomization.yaml"), patchKustomizationRBDVM); err != nil {
		return fmt.Errorf("vm-storage: patch kustomization.yaml: %w", err)
	}

	fmt.Fprintf(out, "[scaffold] vm-storage wired (mode=%s, goldens=%s)\n", spec.Mode, strings.Join(goldens, ","))
	return nil
}

// canonicalGoldens returns a golden list deduped + sorted, so two
// semantically identical selections (different --vm-golden order or dupes)
// hash, preview, and export IDENTICALLY. Nil-safe. Used by inputsForHash,
// the plan preview, and ExportMap (the writer canonicalizes separately in
// resolveVMGoldens).
func canonicalGoldens(in []string) []string {
	if len(in) == 0 {
		return nil
	}
	seen := map[string]bool{}
	out := make([]string, 0, len(in))
	for _, g := range in {
		g = strings.TrimSpace(g)
		if g == "" || seen[g] {
			continue
		}
		seen[g] = true
		out = append(out, g)
	}
	sort.Strings(out)
	return out
}

// splitCSVList parses a comma-joined golden list from a prefill value:
// split, trim, drop empties. Order is preserved as written (hashing +
// export canonicalize separately via canonicalGoldens).
func splitCSVList(v string) []string {
	parts := strings.Split(v, ",")
	out := make([]string, 0, len(parts))
	for _, p := range parts {
		if p = strings.TrimSpace(p); p != "" {
			out = append(out, p)
		}
	}
	if len(out) == 0 {
		return nil
	}
	return out
}

// resolveVMGoldens validates the requested golden subset against the
// fleet's platform/rbd-vm/goldens-fs/os/ catalog (the Phase-1 split) and
// returns a deterministic, de-duplicated list. Empty request → the single
// defaultVMGolden. The catalog must exist (Phase 1) — a missing dir is a
// hard error, not a silent empty subset.
func resolveVMGoldens(fleetRepo string, requested []string) ([]string, error) {
	osDir := filepath.Join(fleetRepo, "platform", "rbd-vm", "goldens-fs", "os")
	entries, err := os.ReadDir(osDir)
	if err != nil {
		return nil, fmt.Errorf("vm-storage: read golden catalog %s: %w (the Phase-1 rbd-vm split must be present in the fleet)", osDir, err)
	}
	avail := map[string]bool{}
	var availList []string
	for _, e := range entries {
		n := e.Name()
		if e.IsDir() || !strings.HasSuffix(n, ".yaml") {
			continue
		}
		slug := strings.TrimSuffix(n, ".yaml")
		avail[slug] = true
		availList = append(availList, slug)
	}
	sort.Strings(availList)
	if len(availList) == 0 {
		return nil, fmt.Errorf("vm-storage: golden catalog %s is empty (Phase-1 split incomplete)", osDir)
	}

	if len(requested) == 0 {
		if !avail[defaultVMGolden] {
			return nil, fmt.Errorf("vm-storage: default golden %q not in the catalog (have: %s) — pass --vm-golden explicitly", defaultVMGolden, strings.Join(availList, ","))
		}
		return []string{defaultVMGolden}, nil
	}

	seen := map[string]bool{}
	var sel []string
	for _, g := range requested {
		if !avail[g] {
			return nil, fmt.Errorf("vm-storage: --vm-golden %q is not in the catalog (have: %s)", g, strings.Join(availList, ","))
		}
		if !seen[g] {
			seen[g] = true
			sel = append(sel, g)
		}
	}
	sort.Strings(sel)
	return sel, nil
}

// vmGoldensOverlayYAML renders clusters/<name>/rbd-vm-goldens/
// kustomization.yaml — the selected FS goldens by relative path into the
// shared per-OS catalog. Repo-root depth mirrors objectStorageOverlayYAML.
func vmGoldensOverlayYAML(clusterName string, goldens []string) string {
	ups := strings.Repeat("../", 3+strings.Count(clusterName, "/"))
	var b strings.Builder
	b.WriteString("apiVersion: kustomize.config.k8s.io/v1beta1\n")
	b.WriteString("kind: Kustomization\n")
	b.WriteString("# rbd-vm FS golden subset for " + clusterName + " — generated by\n")
	b.WriteString("# `kube-dc bootstrap init` (--vm-storage-mode=shared-rbd). Selects ONLY the\n")
	b.WriteString("# named goldens from the shared per-OS catalog (platform/rbd-vm/goldens-fs/os/).\n")
	b.WriteString("# The 70Gi windows-11-golden + every other OS are opt-in — add a line here (or\n")
	b.WriteString("# re-run with --vm-golden). Referenced by the rbd-vm-goldens Flux Kustomization\n")
	b.WriteString("# in ../rbd-vm.yaml.\n")
	b.WriteString("resources:\n")
	for _, g := range goldens {
		b.WriteString("  - " + ups + "platform/rbd-vm/goldens-fs/os/" + g + ".yaml\n")
	}
	return b.String()
}

// rbdVMYAML renders clusters/<name>/rbd-vm.yaml — the two Flux
// Kustomizations. rbd-vm-goldens points at the CLUSTER subset overlay
// (not the full platform/rbd-vm/goldens-fs), so a fresh cluster gets only
// its selected goldens. Shape matches the shipped fleet contract (platform/rbd-vm split).
func rbdVMYAML(clusterName string) string {
	var b strings.Builder
	b.WriteString("# rbd-vm (VM root-disk storage) for " + clusterName + " — generated by\n")
	b.WriteString("# `kube-dc bootstrap init` (--vm-storage-mode=shared-rbd). Two Kustomizations:\n")
	b.WriteString("#   rbd-vm-base    -> platform/rbd-vm/base (StorageClass rbd-vm +\n")
	b.WriteString("#                     VolumeSnapshotClass + golden-images ns/RBAC). No goldens,\n")
	b.WriteString("#                     no Ceph capacity. rbd-vm is NOT the default StorageClass.\n")
	b.WriteString("#   rbd-vm-goldens -> clusters/" + clusterName + "/rbd-vm-goldens (the SELECTED FS\n")
	b.WriteString("#                     golden subset — edit that overlay to add/remove OSes).\n")
	b.WriteString("# Both dependsOn infra-object-storage (Rook + the mode's rbd-pool CephBlockPool)\n")
	b.WriteString("# + platform (CDI/KubeVirt). prune:false so a regroup can't delete a live\n")
	b.WriteString("# golden. To disable: remove `- rbd-vm.yaml` from\n")
	b.WriteString("# clusters/" + clusterName + "/kustomization.yaml resources.\n")
	b.WriteString(`apiVersion: kustomize.toolkit.fluxcd.io/v1
kind: Kustomization
metadata:
  name: rbd-vm-base
  namespace: flux-system
spec:
  dependsOn:
    - name: infra-object-storage
    - name: platform
  interval: 10m
  retryInterval: 2m
  timeout: 10m
  path: ./platform/rbd-vm/base
  prune: false
  force: true
  sourceRef:
    kind: GitRepository
    name: flux-system
---
apiVersion: kustomize.toolkit.fluxcd.io/v1
kind: Kustomization
metadata:
  name: rbd-vm-goldens
  namespace: flux-system
spec:
  dependsOn:
    - name: rbd-vm-base
  interval: 10m
  retryInterval: 2m
  timeout: 10m
  path: ./clusters/` + clusterName + `/rbd-vm-goldens
  prune: false
  force: true
  sourceRef:
    kind: GitRepository
    name: flux-system
  postBuild:
    substituteFrom:
      - kind: ConfigMap
        name: cluster-config
      - kind: Secret
        name: cluster-secrets
        optional: true
`)
	return b.String()
}

// patchKustomizationRBDVM inserts `- rbd-vm.yaml` into the cluster
// overlay kustomization's resources list, after the `- platform.yaml`
// entry (rbd-vm dependsOn platform, so it reads after it). Idempotent;
// errors when the resources block shape drifted.
func patchKustomizationRBDVM(lines []string) ([]string, bool, error) {
	for _, l := range lines {
		if strings.TrimSpace(l) == "- rbd-vm.yaml" {
			return lines, false, nil // already patched
		}
	}
	for i, l := range lines {
		if strings.TrimSpace(l) == "- platform.yaml" {
			indent := l[:len(l)-len(strings.TrimLeft(l, " "))]
			out := make([]string, 0, len(lines)+1)
			out = append(out, lines[:i+1]...)
			out = append(out, indent+"- rbd-vm.yaml")
			out = append(out, lines[i+1:]...)
			return out, true, nil
		}
	}
	return nil, false, fmt.Errorf("no `- platform.yaml` resources entry found (file shape drifted from add-cluster.sh output)")
}

// vmStorageFiles returns the cluster-relative paths WriteVMStorage
// creates/patches for a mode, for the Plan's dry-run preview. It does NOT
// resolve the golden subset against the fleet (dry-run may run without a
// scaffolded fleet); the golden names are validated at write time.
func vmStorageFiles(clusterName string, spec VMStorageSpec) []string {
	if !scaffoldsVMStorage(spec.Mode) || spec.Mode == VMStorageSharedRBDLiveMigration {
		return nil
	}
	base := filepath.Join("clusters", clusterName)
	return []string{
		filepath.Join(base, "rbd-vm-goldens", "kustomization.yaml"),
		filepath.Join(base, "rbd-vm.yaml"),
		filepath.Join(base, "kustomization.yaml") + " (+ rbd-vm.yaml resource)",
	}
}
