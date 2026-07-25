package clusterinit

import (
	"bufio"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"
)

// The `cloud+public-vlan` preset promises an `infra-public-network` Flux
// Kustomization -- the plan prints it, PresetKustomizations lists it, and
// preset_test.go asserts it. Nothing wrote it: `infrastructure.yaml` comes
// from bootstrap/add-cluster.sh, which has no preset awareness and only ever
// emits infra-cni + infra-core.
//
// The result was silent and expensive: the EXT_PUBLIC_* keys landed in
// cluster-config.env but were consumed by NOTHING, so the public VLAN
// (tenant EIP/FIP pool; it may also carry an ingress VIP only when speaker nodes have a real host-facing interface on that VLAN)
// simply did not exist. Discovered on the on-prem install, where
// `kubectl get subnet` showed ext-cloud but no ext-public.
//
// This mirrors the object-storage wiring: the CLI appends the layer
// in-process after add-cluster.sh has produced the base files.

// publicSubnetName is fixed by the fleet template
// (infrastructure/kube-ovn-network-public/subnet-public.yaml hardcodes
// `name: ext-public`), unlike the cloud subnet whose name comes from
// ${EXT_NET_NAME}.
const publicSubnetName = "ext-public"

// defaultCloudSubnetName matches add-cluster.sh's EXT_NET_NAME default.
const defaultCloudSubnetName = "ext-cloud"

// publicNetworkLayerYAML is the Flux Kustomization for
// infrastructure/kube-ovn-network-public (Vlan + Subnet for the routed
// public VLAN). dependsOn infra-core because the kubeovn.io CRDs and the
// ovn-cluster VPC must exist first.
func publicNetworkLayerYAML() string {
	return `---
# Public/provider VLAN — tenant public EIP/FIP pool. This creates the
# Kube-OVN logical network; it does not create a host-facing MetalLB interface. Emitted for presets whose spec
# includes infra-public-network; requires EXT_PUBLIC_* in cluster-config.env.
# The Subnet manifest itself attaches ext-public to the ovn-cluster VPC.
apiVersion: kustomize.toolkit.fluxcd.io/v1
kind: Kustomization
metadata:
  name: infra-public-network
  namespace: flux-system
spec:
  dependsOn:
    - name: infra-core
  interval: 10m
  retryInterval: 2m
  timeout: 10m
  path: ./infrastructure/kube-ovn-network-public
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
`
}

// PresetHasPublicNetwork reports whether the preset's Kustomization list
// includes infra-public-network.
func PresetHasPublicNetwork(p Preset) bool {
	ks, ok := PresetKustomizations(p)
	if !ok {
		return false
	}
	for _, k := range ks {
		if k == "infra-public-network" {
			return true
		}
	}
	return false
}

// envValue returns the value of key in a cluster-config.env body, or "".
// Only a real assignment counts: a commented-out line is not a definition.
func envValue(body, key string) string {
	sc := bufio.NewScanner(strings.NewReader(body))
	val := ""
	for sc.Scan() {
		line := strings.TrimSpace(sc.Text())
		if line == "" || strings.HasPrefix(line, "#") {
			continue
		}
		name, v, ok := strings.Cut(line, "=")
		if !ok || strings.TrimSpace(name) != key {
			continue
		}
		val = strings.TrimSpace(v) // last assignment wins, as with env files
	}
	return val
}

// WritePublicNetwork appends the infra-public-network Flux Kustomization to
// clusters/<name>/infrastructure.yaml.
//
// Re-running converges from any partial state: the layer is detected by its
// own marker rather than by a "did we run before" flag.
func WritePublicNetwork(fleetRepo, clusterName string, p Preset, out io.Writer) error {
	if !PresetHasPublicNetwork(p) {
		return nil
	}
	source := filepath.Join(fleetRepo, "infrastructure", "kube-ovn-network-public", "kustomization.yaml")
	if _, err := os.Stat(source); err != nil {
		return fmt.Errorf("public-network: starter is missing infrastructure/kube-ovn-network-public/kustomization.yaml (update the fleet-starter/checkout to the CLI release before init): %w", err)
	}

	clusterDir := filepath.Join(fleetRepo, "clusters", clusterName)
	wroteSomething := false

	// ---- (1) the Flux layer -------------------------------------------
	infraPath := filepath.Join(clusterDir, "infrastructure.yaml")
	infraBody, err := os.ReadFile(infraPath)
	if err != nil {
		// A public-VLAN preset without infrastructure.yaml means the layer
		// can never be wired, so `init` must NOT report success: that is
		// precisely the silent-omission failure this function exists to stop.
		return fmt.Errorf("public-network: %s is required to wire infra-public-network for preset %s: %w", infraPath, p, err)
	}
	if !strings.Contains(string(infraBody), "name: infra-public-network") {
		body := string(infraBody)
		if !strings.HasSuffix(body, "\n") {
			body += "\n"
		}
		if err := atomicWrite(infraPath, []byte(body+publicNetworkLayerYAML()), 0o644); err != nil {
			return fmt.Errorf("public-network: write infrastructure.yaml: %w", err)
		}
		wroteSomething = true
	}

	// NOTE: this deliberately does NOT write VPC_EXTRA_EXTERNAL_SUBNETS.
	//
	// An earlier version did, claiming the key made the ovn-cluster VPC attach
	// ext-public. It does not: nothing in the fleet consumes the key, and the
	// subnet attaches without it (verified on a live cluster — ext-public had
	// spec.vpc=ovn-cluster while ovn-cluster.spec.extraExternalSubnets was
	// empty).
	//
	// Worse, setting it would be actively harmful. infrastructure/
	// kube-ovn-network/vpc-config.yaml documents that the DEFAULT VPC must not
	// set extraExternalSubnets: with enable-eip-snat kube-ovn early-returns
	// from handleUpdateVpcExternal, and the legacy external-gw handler
	// early-returns when extraExternalSubnets is non-nil, leaving the
	// ovn-cluster-ext-cloud logical router port unmanaged.
	//
	// The Flux layer above is the whole job.
	if wroteSomething && out != nil {
		fmt.Fprintf(out, "[scaffold] public-network wired (infra-public-network)\n")
	}
	return nil
}
