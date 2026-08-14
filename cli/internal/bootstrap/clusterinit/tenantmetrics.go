package clusterinit

// tenantmetrics.go — A2: per-tenant metrics WRITE path, automatic for new
// installs.
//
// Without this, a fresh cluster brings up Mimir (long-term metrics store) but
// nothing ROUTES each Project's metrics into its own Mimir tenant, so every
// tenant Grafana Org shows logs but empty metrics dashboards — the KNOWN gap
// operators kept hitting and wiring by hand. This scaffolds the per-cluster
// Flux Kustomization that pulls in the shared write-path bundle
// (platform/monitoring-writepath = cortex-tenant + alloy-metrics), the same
// overlay the clusters that opt in already run, so it is on by default.
//
// WHY a per-cluster overlay and not the shared platform/monitoring root: the
// shared monitoring VALUES are sized for cloud's large dual-writer load and
// over-provision small clusters, so the write path is deliberately excluded
// from the common root (see kube-dc docs/prd/metrics-collection-consolidation.md).
// A cluster's main `platform` Kustomization can only PATCH rendered objects,
// not ADD kustomize resources — so the overlay is the only place to pull the
// bundle in.
//
// Gated on Mimir being present (any non-disabled object-storage mode). Graceful
// with older starters: if the bundle is absent the step warns and skips instead
// of failing the whole scaffold.

import (
	"fmt"
	"io"
	"os"
	"path/filepath"
)

// WriteTenantMetrics scaffolds clusters/<name>/monitoring-writepath.yaml and
// wires it into the cluster kustomization. No-op when disabled (object storage
// disabled ⇒ Mimir suspended ⇒ nothing to write to).
func WriteTenantMetrics(fleetRepo, clusterName string, enabled bool, out io.Writer) error {
	if out == nil {
		out = io.Discard
	}
	if !enabled {
		return nil
	}
	const bundle = "platform/monitoring-writepath"
	fi, err := os.Stat(filepath.Join(fleetRepo, bundle))
	if err != nil {
		if !os.IsNotExist(err) {
			return fmt.Errorf("tenant-metrics: stat %s: %w", bundle, err)
		}
		fmt.Fprintf(out, "[scaffold] tenant-metrics: skipped (%s not in this starter)\n", bundle)
		return nil
	}
	if !fi.IsDir() {
		return fmt.Errorf("tenant-metrics: %s exists but is not a directory", bundle)
	}
	clusterDir := filepath.Join(fleetRepo, "clusters", clusterName)
	file := filepath.Join(clusterDir, "monitoring-writepath.yaml")
	if err := os.WriteFile(file, []byte(monitoringWritepathYAML), 0o644); err != nil {
		return fmt.Errorf("tenant-metrics: write %s: %w", file, err)
	}
	if err := patchFileLines(filepath.Join(clusterDir, "kustomization.yaml"),
		patchKustomizationResource("monitoring-writepath.yaml")); err != nil {
		return fmt.Errorf("tenant-metrics: patch kustomization.yaml: %w", err)
	}
	fmt.Fprintf(out, "[scaffold] per-tenant metrics write path wired (cortex-tenant + alloy-metrics)\n")
	return nil
}

// monitoringWritepathYAML is the per-cluster Flux Kustomization that pulls in
// the shared write-path bundle. Modelled on the proven per-cluster overlays
// already in the fleet, with three deliberate differences for a FRESH-owned
// layer (codex review):
//   - prune:true + force:false — cortex-tenant/alloy-metrics are STATELESS
//     (a remote-write proxy + a metrics scraper, no persistent data), so this
//     cluster owns them from the start. prune:true garbage-collects them if the
//     overlay is removed (matches the stateless tenant-addons layer); force:true
//     is the object-storage/rbd-vm pattern for adopting stateful resources and
//     is an SSA-deletion footgun on a fresh layer, so it is off.
//   - healthChecks on both HelmReleases — without them the Kustomization reports
//     Ready as soon as the CRs apply, so a capacity-starved cluster would show
//     "Ready" with cortex/alloy Pending and empty dashboards. The health check
//     makes that failure LOUD instead of silent.
const monitoringWritepathYAML = `# Per-tenant metrics WRITE path (cortex-tenant + alloy-metrics).
#
# Routes every Project's metrics into its own Mimir tenant so tenant Grafana
# Orgs show metrics, not just logs. Kept as a dedicated Flux Kustomization: the
# shared platform/monitoring root deliberately excludes the write path (its
# cloud-sized values over-provision small clusters), and a cluster's main
# platform Kustomization can only PATCH rendered objects, not ADD kustomize
# resources. dependsOn platform — needs Mimir + the cortex-tenant/grafana
# HelmRepositories the bundle references.
apiVersion: kustomize.toolkit.fluxcd.io/v1
kind: Kustomization
metadata:
  name: monitoring-writepath
  namespace: flux-system
spec:
  dependsOn:
    - name: platform
  interval: 10m
  retryInterval: 2m
  timeout: 10m
  path: ./platform/monitoring-writepath
  prune: true
  force: false
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
  # Report NotReady until BOTH write-path HelmReleases are actually Ready, so a
  # cluster that cannot schedule them surfaces the failure instead of silently
  # serving empty tenant dashboards.
  healthChecks:
    - apiVersion: helm.toolkit.fluxcd.io/v2
      kind: HelmRelease
      name: cortex-tenant
      namespace: monitoring
    - apiVersion: helm.toolkit.fluxcd.io/v2
      kind: HelmRelease
      name: alloy-metrics
      namespace: monitoring
`
