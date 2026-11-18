package clusterinit

// payg_managed_fees.go — `kube-dc bootstrap init --payg --payg-managed-fees`:
// opt-in managed-service fee evidence for a NEW installation.
//
// Fees for managed services are charged from a native census of the
// Services hub and runner, checked against a scope document Fleet pins. The
// scope names the installation's own cluster, namespace, data-plane and
// writer identities and the Services cell's public signing keys, so it can
// only be drafted once Services runs: the census image renders it
// (`payg-managed-fee-evidence -render-scope`), an operator reviews it and
// commits it to cluster-config.env. At bootstrap this writer therefore only
// prepares the layer:
//
//  1. clusters/<name>/payg-managed-fees.yaml — the Flux Kustomization over
//     ./platform/payg-managed-fee, SUSPENDED. A suspended Kustomization is
//     not built, so its PAYG_FEE_SCOPE_* references cannot fail Flux's strict
//     substitution before the scope exists. The census and the automatic
//     writer renewal run the independently pinned census image;
//  2. its entry in clusters/<name>/kustomization.yaml;
//  3. cluster-config.env — PAYG_FEE_CENSUS_IMAGE (release pins or --set,
//     digest-pinned) and PAYG_FEE_CENSUS_SUSPEND=false, so capture starts
//     when the operator unsuspends the layer.
//
// Nothing here bills. Fee hours need an operator-enrolled first period (the
// root) declared as PAYG_FEE_PAYABLE_ENROLLMENT; from then on Services
// releases renew the source by themselves (docs/platform/payg-managed-fees.md).

import (
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"reflect"
	"strings"

	"gopkg.in/yaml.v3"

	"github.com/shalb/kube-dc/cli/internal/bootstrap/config"
)

const (
	paygManagedFeesBundlePath    = "platform/payg-managed-fee"
	paygManagedFeesLayerFileName = "payg-managed-fees.yaml"

	PAYGFeeCensusImageKey   = "PAYG_FEE_CENSUS_IMAGE"
	paygFeeCensusSuspendKey = "PAYG_FEE_CENSUS_SUSPEND"
)

// paygFeeScopeKeys are rendered from the running installation after Services
// is up; a bootstrap value would describe an installation that does not
// exist yet.
var paygFeeScopeKeys = []string{
	"PAYG_FEE_SCOPE_JSON", "PAYG_FEE_SCOPE_REVISION", "PAYG_FEE_HUB_NAMESPACE",
	"PAYG_FEE_RUNNER_NAMESPACE", "PAYG_FEE_DATA_PLANE_NAME", "PAYG_FEE_API_CIDRS",
}

// ErrPAYGManagedFeesChangeOnResume fires when a resume is asked to add the
// managed-fee layer to an overlay without PAYG.
var ErrPAYGManagedFeesChangeOnResume = errors.New("init: --payg-managed-fees needs the PAYG wiring of --payg in the same overlay")

// paygManagedFeesLayerYAML is clusters/<name>/payg-managed-fees.yaml. The
// operator changes only `suspend` (to false) once the scope is committed.
func paygManagedFeesLayerYAML(suspend bool) string {
	return `# Managed-service fee evidence (opt-in; written by
# ` + "`kube-dc bootstrap init --payg --payg-managed-fees`" + `). Pulls in the shared
# bundle platform/payg-managed-fee: the native fee census, the payable fee
# hours and the automatic writer renewal. It starts SUSPENDED: render the
# census scope from the running installation, commit its PAYG_FEE_* lines to
# cluster-config.env, then set suspend: false (docs/platform/payg-managed-fees.md).
apiVersion: kustomize.toolkit.fluxcd.io/v1
kind: Kustomization
metadata:
  name: payg-managed-fees
  namespace: flux-system
spec:
  suspend: ` + fmt.Sprint(suspend) + `
  dependsOn:
    - name: payg
    - name: services-catalog
  interval: 10m
  retryInterval: 2m
  timeout: 10m
  path: ./` + paygManagedFeesBundlePath + `
  prune: true
  wait: true
  sourceRef:
    kind: GitRepository
    name: flux-system
  postBuild:
    substituteFrom:
      - kind: ConfigMap
        name: cluster-config
  patches:
    - target:
        group: batch
        version: v1
        kind: CronJob
        name: kube-dc-managed-fee-census
        namespace: monitoring
      patch: |-
        - op: replace
          path: /spec/jobTemplate/spec/template/spec/containers/0/image
          value: ${` + PAYGFeeCensusImageKey + `}
    - target:
        group: batch
        version: v1
        kind: CronJob
        name: kube-dc-managed-fee-writer-renewal
        namespace: monitoring
      patch: |-
        - op: replace
          path: /spec/jobTemplate/spec/template/spec/containers/0/image
          value: ${` + PAYGFeeCensusImageKey + `}
`
}

// checkPAYGManagedFeesLayer accepts exactly the rendered layer, suspended or
// not: the operator's only edit is unsuspending it.
func checkPAYGManagedFeesLayer(body []byte) error {
	var got any
	if err := decodeSingleYAMLDoc(body, &got); err != nil {
		return err
	}
	for _, suspend := range []bool{true, false} {
		var want any
		if err := yaml.Unmarshal([]byte(paygManagedFeesLayerYAML(suspend)), &want); err != nil {
			return fmt.Errorf("render the expected layer: %w", err)
		}
		if reflect.DeepEqual(got, want) {
			return nil
		}
	}
	return errors.New("it differs from the layer the CLI renders (only spec.suspend may change); restore it from `kube-dc bootstrap init --payg-managed-fees` output or docs/platform/payg-managed-fees.md")
}

// validatePAYGManagedFees is the cobra-time half: the layer needs PAYG and
// Services, and its scope is never a bootstrap input.
func validatePAYGManagedFees(o *InitOptions) []string {
	var errs []string
	for _, k := range paygFeeScopeKeys {
		if _, ok := o.Sets[k]; ok {
			errs = append(errs, "--set "+k+" is not accepted — the managed-fee scope is rendered from the running installation after Services is up (payg-managed-fee-evidence -render-scope), reviewed, and committed to cluster-config.env")
		}
	}
	if v, ok := o.Sets[PAYGFeeCensusImageKey]; ok && v != "" && !paygImageRegex.MatchString(v) {
		errs = append(errs, fmt.Sprintf("--set %s=%q must be a digest-pinned image reference (repo:tag@sha256:<64 hex>)", PAYGFeeCensusImageKey, v))
	}
	if !o.PAYGManagedFees {
		return errs
	}
	if !o.PAYG {
		errs = append(errs, "--payg-managed-fees requires --payg: managed-service fees are charged through the PAYG billing service and its metering database")
	}
	if services, err := ResolveManagedServices(o); err == nil && services.Mode != "on" {
		errs = append(errs, "--payg-managed-fees requires managed services (--managed-services=on): the fees are evidenced by the Services hub and runner")
	}
	return errs
}

// WritePAYGManagedFees prepares the managed-fee layer in an overlay that
// already carries the PAYG wiring. Idempotent: an existing layer (suspended
// or not), its kustomization entry and the census pins are kept.
func WritePAYGManagedFees(fleetRepo, clusterName string, enabled bool, out io.Writer) (bool, error) {
	if out == nil {
		out = io.Discard
	}
	if !enabled {
		return false, nil
	}
	clusterDir := filepath.Join(fleetRepo, "clusters", clusterName)
	if _, err := os.Stat(filepath.Join(fleetRepo, paygManagedFeesBundlePath, "kustomization.yaml")); err != nil {
		return false, fmt.Errorf("payg-managed-fees: the fleet checkout has no %s/kustomization.yaml — --payg-managed-fees needs a fleet-starter that ships the managed-fee bundle (update the starter/checkout to the CLI release): %w", paygManagedFeesBundlePath, err)
	}
	envPath := filepath.Join(clusterDir, "cluster-config.env")
	env, err := config.LoadEnv(envPath)
	if err != nil {
		return false, fmt.Errorf("payg-managed-fees: %w", err)
	}
	if !strings.EqualFold(paygEnvValue(env, PAYGEnabledKey), "true") || paygEnvValue(env, PAYGInstallationUIDKey) == "" {
		return false, fmt.Errorf("payg-managed-fees: %w (clusters/%s/cluster-config.env has no PAYG_ENABLED=true and installation identity)", ErrPAYGManagedFeesChangeOnResume, clusterName)
	}
	if reason := paygBillingRefusal(paygEnvValue(env, billingProviderKey), env.Keys()); reason != "" {
		return false, fmt.Errorf("payg-managed-fees: %w: %s", ErrPAYGRefused, reason)
	}
	image := paygEnvValue(env, PAYGFeeCensusImageKey)
	if image == "" {
		return false, fmt.Errorf("payg-managed-fees: %s is not pinned — this starter's release pins predate the managed-fee census; pass --set %s=shalb/kube-dc-fee-census:<tag>@sha256:<digest> (check the digest on Docker Hub first)", PAYGFeeCensusImageKey, PAYGFeeCensusImageKey)
	}
	if !paygImageRegex.MatchString(image) {
		return false, fmt.Errorf("payg-managed-fees: %s=%q is not digest-pinned (want repo:tag@sha256:<64 hex>)", PAYGFeeCensusImageKey, image)
	}

	kustPath := filepath.Join(clusterDir, "kustomization.yaml")
	kustBody, err := os.ReadFile(kustPath)
	if err != nil {
		return false, fmt.Errorf("payg-managed-fees: %w", err)
	}
	kustLines, kustChanged, err := patchKustomizationResource(paygManagedFeesLayerFileName)(strings.Split(string(kustBody), "\n"))
	if err != nil {
		return false, fmt.Errorf("payg-managed-fees: kustomization.yaml: %w", err)
	}
	layerPath := filepath.Join(clusterDir, paygManagedFeesLayerFileName)
	writeLayer := false
	if body, err := os.ReadFile(layerPath); os.IsNotExist(err) {
		writeLayer = true
	} else if err != nil {
		return false, fmt.Errorf("payg-managed-fees: read %s: %w", layerPath, err)
	} else if err := checkPAYGManagedFeesLayer(body); err != nil {
		return false, fmt.Errorf("payg-managed-fees: %s is not the managed-fee layer: %v", layerPath, err)
	}
	envChanged := paygEnvValue(env, paygFeeCensusSuspendKey) == ""

	// Re-read before writing, as the other scaffold writers do: a target that
	// changed meanwhile surfaces instead of being overwritten.
	if writeLayer {
		if _, err := os.Stat(layerPath); err == nil {
			return false, fmt.Errorf("payg-managed-fees: %s appeared while init was running — re-run init against the current state", paygManagedFeesLayerFileName)
		}
		if err := os.WriteFile(layerPath, []byte(paygManagedFeesLayerYAML(true)), 0o644); err != nil {
			return false, fmt.Errorf("payg-managed-fees: write %s: %w", layerPath, err)
		}
	}
	if kustChanged {
		current, err := os.ReadFile(kustPath)
		if err != nil || string(current) != string(kustBody) {
			return false, fmt.Errorf("payg-managed-fees: kustomization.yaml changed while init was running — re-run init against the current state")
		}
		if err := os.WriteFile(kustPath, []byte(strings.Join(kustLines, "\n")), 0o644); err != nil {
			return false, fmt.Errorf("payg-managed-fees: write %s: %w", kustPath, err)
		}
	}
	if envChanged {
		env.Set(paygFeeCensusSuspendKey, "false")
		if err := env.Write(""); err != nil {
			return false, fmt.Errorf("payg-managed-fees: write cluster-config.env: %w", err)
		}
	}
	changed := writeLayer || kustChanged || envChanged
	if !changed {
		return false, nil
	}
	fmt.Fprintf(out, "[scaffold] managed-service fee layer prepared: %s (./%s, SUSPENDED), %s=%s, %s=false\n",
		paygManagedFeesLayerFileName, paygManagedFeesBundlePath, PAYGFeeCensusImageKey, image, paygFeeCensusSuspendKey)
	fmt.Fprintf(out, "[scaffold] managed fees next: once Services runs, render the scope (payg-managed-fee-evidence -render-scope), commit its PAYG_FEE_* lines, unsuspend %s, then enroll the first fee period (docs/platform/payg-managed-fees.md)\n", paygManagedFeesLayerFileName)
	return true, nil
}
