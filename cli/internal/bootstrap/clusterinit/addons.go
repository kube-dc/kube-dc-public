package clusterinit

import (
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"
)

// MetalLB is the DEFAULT ingress path (INGRESS_MODE=metallb-lb, see
// preset.go) and nothing installed it.
//
// add-cluster.sh scaffolds cluster-config.env with INGRESS_MODE,
// METALLB_MODE, METALLB_FLOATING_IP, METALLB_INTERFACE and METALLB_VERSION,
// and preset.go validates them carefully -- but the addons/addons-config
// Flux layers that consume those keys were never emitted. add-cluster.sh
// only mentions them in comments; the long-lived clusters that do have the
// layers had them added by hand, and others have none at all.
//
// The failure is silent and plausible-looking. With no IPAddressPool the
// Envoy Service never gets a LoadBalancer address, so the Gateway falls back
// to a node IP and the front door *appears* to work -- on the on-prem
// install `kubectl get gateway` showed Programmed=True at 203.0.113.11 (a
// control-plane node) while METALLB_FLOATING_IP=198.51.100.2 sat unused and
// `kubectl -n metallb-system get ipaddresspool` returned nothing, because
// the namespace did not exist. The operator's configured VIP is ignored and
// ingress has no failover: lose that one node and the front door goes with
// it.
//
// Two Flux layers, not one, and the ordering is load-bearing: applying the
// MetalLB CRs in the same Kustomization as the operator fails SSA dry-run on
// the first reconcile with `no matches for kind "L2Advertisement" in version
// metallb.io/v1beta1` and wedges the whole layer. addons installs the
// operator; addons-config applies the CRs and health-gates on
// metallb-controller so it only runs once the CRDs are registered and the
// webhook is live.

// ingressModeMetalLB is the INGRESS_MODE value that requires the layers.
// `hostnetwork` binds Envoy to the host and needs no LoadBalancer.
const ingressModeMetalLB = "metallb-lb"

// The shared addon trees. metallb-config vs metallb-config-bgp is chosen by
// METALLB_MODE: L2 announces via ARP on a shared segment, BGP advertises
// each VIP as a /32 to a peer router.
const (
	addonPathMetalLB      = "addons/metallb"
	addonPathConfigL2     = "addons/metallb-config"
	addonPathConfigBGP    = "addons/metallb-config-bgp"
	metallbControllerName = "metallb-controller"
	metallbNamespace      = "metallb-system"
)

func addonsLayerYAML(clusterName string) string {
	return `---
# MetalLB operator (INGRESS_MODE=metallb-lb). The MetalLB CRs live in a
# SEPARATE Kustomization (addons-config.yaml) so the operator's CRDs are
# registered before any CR is dry-run.
apiVersion: kustomize.toolkit.fluxcd.io/v1
kind: Kustomization
metadata:
  name: addons
  namespace: flux-system
spec:
  dependsOn:
    - name: platform
  interval: 10m
  retryInterval: 2m
  timeout: 15m
  path: ./clusters/` + clusterName + `/addons
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
  # The gate belongs HERE, on the layer that installs the operator.
  # A Kustomization's healthChecks run after ITS OWN apply, so putting the
  # only check on addons-config cannot prevent the CRD race it exists to
  # stop: addons-config would already have dry-run its IPAddressPool by the
  # time the check ran. Gating addons means it is not Ready until
  # metallb-controller is, and addons-config.dependsOn waits for Ready.
  healthChecks:
    - apiVersion: apps/v1
      kind: Deployment
      name: ` + metallbControllerName + `
      namespace: ` + metallbNamespace + `
`
}

func addonsConfigLayerYAML(clusterName string) string {
	return `---
# MetalLB CR instances (IPAddressPool + L2Advertisement/BGPPeer), applied
# only after ./addons has installed the operator. The healthCheck is what
# makes that ordering real: without it this layer can still race the CRD
# registration and fail its first SSA dry-run.
apiVersion: kustomize.toolkit.fluxcd.io/v1
kind: Kustomization
metadata:
  name: addons-config
  namespace: flux-system
spec:
  dependsOn:
    - name: addons
  interval: 10m
  retryInterval: 2m
  timeout: 15m
  path: ./clusters/` + clusterName + `/addons-config
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
    - apiVersion: apps/v1
      kind: Deployment
      name: ` + metallbControllerName + `
      namespace: ` + metallbNamespace + `
`
}

// WriteAddons emits the addons + addons-config layers when the cluster's
// INGRESS_MODE calls for MetalLB. It is a no-op for hostnetwork, and
// idempotent: every file and every kustomization entry is checked
// independently so a partial run converges on re-execution rather than
// leaving a half-wired cluster that the "already done" short-circuit would
// then skip forever.
func WriteAddons(fleetRepo, clusterName string, out io.Writer) error {
	clusterDir := filepath.Join(fleetRepo, "clusters", clusterName)

	envPath := filepath.Join(clusterDir, "cluster-config.env")
	envBody, err := os.ReadFile(envPath)
	if err != nil {
		return fmt.Errorf("addons: read cluster-config.env: %w", err)
	}

	// Absent INGRESS_MODE means the scaffold default, which is MetalLB —
	// treating "unset" as "no ingress" would reproduce the very silence
	// this function exists to remove.
	mode := envValue(string(envBody), "INGRESS_MODE")
	if mode == "" {
		mode = ingressModeMetalLB
	}
	if mode != ingressModeMetalLB {
		return nil
	}

	configPath := addonPathConfigL2
	if strings.EqualFold(envValue(string(envBody), "METALLB_MODE"), "bgp") {
		configPath = addonPathConfigBGP
	}

	// An existing fleet checkout may predate these starter trees. Fail here
	// with the exact missing path rather than commit Flux Kustomizations that
	// can only become NotReady later.
	for _, source := range []string{addonPathMetalLB, configPath} {
		if _, err := os.Stat(filepath.Join(fleetRepo, source, "kustomization.yaml")); err != nil {
			return fmt.Errorf("addons: starter is missing %s/kustomization.yaml (update the fleet-starter/checkout to the CLI release before init): %w", source, err)
		}
	}
	operatorRel, err := filepath.Rel(filepath.Join(clusterDir, "addons"), filepath.Join(fleetRepo, addonPathMetalLB))
	if err != nil {
		return fmt.Errorf("addons: resolve operator source path: %w", err)
	}
	configRel, err := filepath.Rel(filepath.Join(clusterDir, "addons-config"), filepath.Join(fleetRepo, configPath))
	if err != nil {
		return fmt.Errorf("addons: resolve config source path: %w", err)
	}
	operatorRel = filepath.ToSlash(operatorRel)
	configRel = filepath.ToSlash(configRel)

	wrote := false
	write := func(rel, content string) error {
		p := filepath.Join(clusterDir, rel)
		if err := os.MkdirAll(filepath.Dir(p), 0o755); err != nil {
			return fmt.Errorf("addons: mkdir %s: %w", filepath.Dir(p), err)
		}
		if _, err := os.Stat(p); err == nil {
			return nil // operator may have customised it; never clobber
		}
		if err := atomicWrite(p, []byte(content), 0o644); err != nil {
			return fmt.Errorf("addons: write %s: %w", p, err)
		}
		wrote = true
		return nil
	}

	if err := write("addons.yaml", addonsLayerYAML(clusterName)); err != nil {
		return err
	}
	if err := write("addons-config.yaml", addonsConfigLayerYAML(clusterName)); err != nil {
		return err
	}
	if err := write("addons/kustomization.yaml",
		"apiVersion: kustomize.config.k8s.io/v1beta1\nkind: Kustomization\n"+
			"# MetalLB operator only — CRs are applied by ../addons-config.\nresources:\n  - "+
			operatorRel+"\n"); err != nil {
		return err
	}
	if err := write("addons-config/kustomization.yaml",
		"apiVersion: kustomize.config.k8s.io/v1beta1\nkind: Kustomization\n"+
			"# MetalLB CRs — applied after ../addons has registered the CRDs.\nresources:\n  - "+
			configRel+"\n"); err != nil {
		return err
	}

	// Root kustomization must reference both layers or Flux never sees them.
	// Checked per-entry: a cluster that already lists addons.yaml (say from
	// an older scaffold) must still gain addons-config.yaml.
	rootPath := filepath.Join(clusterDir, "kustomization.yaml")
	rootBody, err := os.ReadFile(rootPath)
	if err != nil {
		return fmt.Errorf("addons: read kustomization.yaml: %w", err)
	}
	root := string(rootBody)
	for _, entry := range []string{"addons.yaml", "addons-config.yaml"} {
		if hasResourceEntry(root, entry) {
			continue
		}
		root, err = appendResourceEntry(root, entry)
		if err != nil {
			return fmt.Errorf("addons: %w", err)
		}
		wrote = true
	}
	if root != string(rootBody) {
		if err := atomicWrite(rootPath, []byte(root), 0o644); err != nil {
			return fmt.Errorf("addons: write kustomization.yaml: %w", err)
		}
	}

	if wrote && out != nil {
		fmt.Fprintf(out, "[scaffold] addons wired (metallb operator + %s)\n", filepath.Base(configPath))
	}

	return writeGatewayVIPPatch(clusterDir, string(envBody), out)
}

// gatewayVIPMarker identifies the patch entry so re-runs are no-ops and a
// hand-edited patches: block can be told apart from ours.
const gatewayVIPMarker = "# kube-dc: gateway address = MetalLB VIP"

// gatewayServiceVIPMarker identifies the EnvoyProxy Service annotation that
// explicitly requests the VIP. The starter pools deliberately set
// autoAssign: false, so changing only Gateway.spec.address leaves the Service
// Pending: MetalLB will not allocate the address unless the Service asks for
// it through this annotation.
const gatewayServiceVIPMarker = "# kube-dc: Envoy Service requests MetalLB VIP"

// writeGatewayVIPPatch points the Envoy Gateway at the MetalLB VIP.
//
// platform/gateway-config/gateway.yaml hardcodes
//
//	addresses: [{type: IPAddress, value: "${NODE_EXTERNAL_IP}"}]
//
// Envoy Gateway turns that into the Service's requested LoadBalancer
// address. When the MetalLB pool holds a DIFFERENT address — which is the
// normal case as soon as the node LAN and the ingress VLAN are separate
// segments — MetalLB cannot satisfy the request and assigns nothing: the
// Service sits at <pending> forever with a correctly-configured pool right
// next to it. Observed on a fresh install where the pool was 198.51.100.2/32
// on the routed public VLAN and the Gateway asked for a node address on the
// management VLAN.
//
// No existing cluster had hit this, which is why it survived: they either
// leave METALLB_FLOATING_IP empty or run Envoy on hostNetwork, so the
// default ingress path was never actually exercised end-to-end.
//
// The patch is emitted per-cluster rather than by changing the shared
// gateway.yaml, because that file is reconciled by every cluster and an
// unresolved variable there would empty the Gateway address on clusters
// that are working today.
//
// The resolved literal is written, not "${METALLB_FLOATING_IP}": platform.yaml
// is itself substituted by the parent Kustomization, so a variable here would
// depend on a second substitution pass that may not happen.
func writeGatewayVIPPatch(clusterDir, envBody string, out io.Writer) error {
	vip := envValue(envBody, "METALLB_FLOATING_IP")
	node := envValue(envBody, "NODE_EXTERNAL_IP")
	if vip == "" || strings.HasPrefix(vip, "CHANGEME") {
		return nil
	}

	serviceEntry := "    - target:\n" +
		"        group: gateway.envoyproxy.io\n" +
		"        version: v1alpha1\n" +
		"        kind: EnvoyProxy\n" +
		"        name: custom-proxy-config\n" +
		"        namespace: envoy-gateway-system\n" +
		"      patch: |\n" +
		"        " + gatewayServiceVIPMarker + "\n" +
		"        - op: add\n" +
		"          path: /spec/provider/kubernetes/envoyService/patch/value/metadata\n" +
		"          value:\n" +
		"            annotations:\n" +
		"              metallb.universe.tf/loadBalancerIPs: \"" + vip + "\"\n"

	gatewayEntry := "    - target:\n" +
		"        kind: Gateway\n" +
		"        name: eg\n" +
		"      patch: |\n" +
		"        " + gatewayVIPMarker + "\n" +
		"        - op: replace\n" +
		"          path: /spec/addresses/0/value\n" +
		"          value: \"" + vip + "\"\n"

	path := filepath.Join(clusterDir, "platform.yaml")
	err := patchFileLines(path, func(lines []string) ([]string, bool, error) {
		hasPatches := false
		servicePresent := false
		gatewayPresent := false
		changed := false
		for i, l := range lines {
			marker := ""
			valuePrefix := ""
			wantIndent := ""
			switch {
			case strings.Contains(l, gatewayServiceVIPMarker):
				servicePresent = true
				marker = gatewayServiceVIPMarker
				valuePrefix = "metallb.universe.tf/loadBalancerIPs:"
				wantIndent = "              "
			case strings.Contains(l, gatewayVIPMarker):
				gatewayPresent = true
				marker = gatewayVIPMarker
				valuePrefix = "value:"
				wantIndent = "          "
			default:
				continue
			}
			// Managed literals must track cluster-config.env on a resumed init.
			// Otherwise the pool moves while Envoy keeps requesting the old VIP.
			foundValue := false
			for j := i + 1; j < len(lines) && j < i+16; j++ {
				t := strings.TrimSpace(lines[j])
				if !strings.HasPrefix(t, valuePrefix) {
					continue
				}
				foundValue = true
				want := wantIndent + valuePrefix + ` "` + vip + `"`
				if lines[j] != want {
					lines[j] = want
					changed = true
				}
				break
			}
			if !foundValue {
				return lines, false, fmt.Errorf("managed patch %q has no value", marker)
			}
		}
		for _, l := range lines {
			if strings.TrimSpace(l) == "patches:" {
				hasPatches = true
				break
			}
		}
		entries := ""
		if !servicePresent {
			entries += serviceEntry
		}
		if vip != node && !gatewayPresent {
			entries += gatewayEntry
		}
		if entries == "" {
			return lines, changed, nil
		}

		end := len(lines)
		for end > 0 && strings.TrimSpace(lines[end-1]) == "" {
			end--
		}
		block := entries
		if !hasPatches {
			block = "  patches:\n" + entries
		}
		res := append([]string{}, lines[:end]...)
		res = append(res, strings.Split(strings.TrimRight(block, "\n"), "\n")...)
		res = append(res, "")
		return res, true, nil
	})
	if err != nil {
		return fmt.Errorf("addons: patch platform.yaml with gateway VIP: %w", err)
	}
	if out != nil {
		fmt.Fprintf(out, "[scaffold] Envoy Service requests the MetalLB VIP %s (pool auto-assignment is disabled)\n", vip)
		if vip != node {
			fmt.Fprintf(out, "[scaffold] gateway address pinned to the MetalLB VIP %s (was %s — MetalLB cannot serve an address outside its pool)\n", vip, node)
		}
	}
	return nil
}

// hasResourceEntry reports whether the kustomization already lists name as a
// resource. Matches the list item specifically, so a mention inside a
// comment or a longer path does not count as wired.
func hasResourceEntry(body, name string) bool {
	for _, line := range strings.Split(body, "\n") {
		t := strings.TrimSpace(line)
		if strings.HasPrefix(t, "#") {
			continue
		}
		if t == "- "+name || t == "- ./"+name {
			return true
		}
	}
	return false
}

// appendResourceEntry inserts name at the end of the top-level `resources:`
// list, preserving the indentation already in use.
func appendResourceEntry(body, name string) (string, error) {
	lines := strings.Split(body, "\n")
	start := -1
	for i, l := range lines {
		if strings.TrimSpace(l) == "resources:" && !strings.HasPrefix(l, " ") {
			start = i
			break
		}
	}
	if start < 0 {
		return "", fmt.Errorf("no top-level `resources:` list in kustomization.yaml")
	}
	last, indent := start, "  "
	for i := start + 1; i < len(lines); i++ {
		t := strings.TrimSpace(lines[i])
		if strings.HasPrefix(t, "- ") {
			last = i
			indent = lines[i][:len(lines[i])-len(strings.TrimLeft(lines[i], " "))]
			continue
		}
		if t == "" || strings.HasPrefix(t, "#") {
			continue // blank lines / comments may sit inside the list
		}
		break // a new top-level key ends the list
	}
	out := append([]string{}, lines[:last+1]...)
	out = append(out, indent+"- "+name)
	out = append(out, lines[last+1:]...)
	return strings.Join(out, "\n"), nil
}
