// natgate.go — single-IP NAT topology detection + fleet wiring.
//
// E2E findings 17 + 17b (2026-07-05): when the control-plane node sits
// behind a 1:1 NAT (kube-dc FIP, EC2 elastic IP, OpenStack/Hetzner
// floating IP), the operator-declared "node external IP" — the public
// IP that wildcard DNS points at — is NEVER the destination address
// packets actually carry when they reach the node's interface. Two
// fleet-visible consequences:
//
//  1. NODE_EXTERNAL_IP feeds the envoy Gateway's spec.addresses and the
//     envoy Service's externalIPs. kube-proxy matches externalIP rules
//     against the ARRIVING dst — the internal IP — so with the public
//     IP configured, ports 80/443 silently RST and no ACME challenge
//     ever completes. The correct value is the arriving (internal) IP;
//     external kubectl still works because the kube-api TLSRoute
//     SNI-passthrough is the same path production uses.
//
//  2. With only ONE IP at the node, the shared Gateway's 6443
//     TLS-passthrough listener becomes a footgun: the envoy Service's
//     externalIP:6443 rule intercepts apiserver traffic AND envoy's
//     kube-api TLSRoute backend (the apiserver advertise address ==
//     the same IP) dials back into envoy itself — SNI re-match,
//     unbounded connection amplification, external kubectl locked
//     out. Production doesn't hit this because masters advertise a
//     private IP ≠ the public NODE_EXTERNAL_IP. On single-IP NAT the
//     listener must not exist; tenant-cluster kube-api SNI routing
//     needs a second node IP and is documented as unavailable.
//
// DetectArrivingIP answers "which IP does traffic arrive with?" over
// the SSH channel the init flow already has. WriteSingleIPNATPatch
// wires consequence 2 into the scaffolded cluster overlay.
package clusterinit

import (
	"context"
	"errors"
	"fmt"
	"io"
	"net"
	"os"
	"sort"

	"path/filepath"
	"strings"

	"github.com/shalb/kube-dc/cli/internal/bootstrap/ports"
)

// ArrivingIPOptions parameterizes DetectArrivingIP.
type ArrivingIPOptions struct {
	SSH  ports.SSHClient
	Host ports.SSHHost
	// PublicIP is the operator-declared node external IP (the wildcard
	// DNS target). Validated by the wizard/flag layer before this runs.
	PublicIP string
	Out      io.Writer
}

// DetectArrivingIP returns the destination IP with which external
// traffic reaches the node, and whether a single-IP NAT topology was
// detected (arriving != public).
//
//  1. If PublicIP is bound on any interface of the node → bare-metal /
//     multi-IP shape: arriving == public, nat == false.
//  2. Otherwise the node never sees the public IP as dst (1:1 NAT):
//     arriving == the node's primary source IP (route lookup), nat ==
//     true.
//
// Errors mean "could not determine" — the caller should warn and
// proceed with PublicIP (fail-open preserves pre-detection behavior;
// the operator sees the warning next to the substitution log line
// either way).
func DetectArrivingIP(ctx context.Context, opts ArrivingIPOptions) (string, bool, error) {
	if opts.SSH == nil {
		return "", false, fmt.Errorf("nat-detect: nil SSH client")
	}
	if net.ParseIP(opts.PublicIP) == nil {
		return "", false, fmt.Errorf("nat-detect: %q is not an IP", opts.PublicIP)
	}

	// `ip -o addr` — one line per address, easy to scan, present on
	// every distro RKE2 supports (iproute2).
	addrOut, err := opts.SSH.Run(ctx, opts.Host, "ip -o addr show")
	if err != nil {
		return "", false, fmt.Errorf("nat-detect: ip addr show: %w", err)
	}
	if hostHasIP(addrOut, opts.PublicIP) {
		return opts.PublicIP, false, nil
	}

	// Public IP is not bound anywhere → NAT. Derive the primary IP
	// from a route lookup. 192.0.2.1 (TEST-NET-1) never gets traffic —
	// `ip route get` is a pure FIB query.
	routeOut, err := opts.SSH.Run(ctx, opts.Host, "ip -4 route get 192.0.2.1")
	if err != nil {
		return "", false, fmt.Errorf("nat-detect: ip route get: %w", err)
	}
	src, err := parseRouteSrc(routeOut)
	if err != nil {
		return "", false, fmt.Errorf("nat-detect: %w", err)
	}
	return src, true, nil
}

// hostHasIP reports whether `ip -o addr show` output carries ip as a
// configured address. Matches the `inet <ip>/` (and inet6) token shape
// so substrings of longer addresses can't false-positive.
func hostHasIP(out []byte, ip string) bool {
	for _, line := range strings.Split(string(out), "\n") {
		fields := strings.Fields(line)
		for i, f := range fields {
			if f != "inet" && f != "inet6" {
				continue
			}
			if i+1 >= len(fields) {
				continue
			}
			addr := fields[i+1]
			if cut := strings.IndexByte(addr, '/'); cut >= 0 {
				addr = addr[:cut]
			}
			if addr == ip {
				return true
			}
		}
	}
	return false
}

// parseRouteSrc extracts the `src <ip>` token from `ip route get`
// output.
func parseRouteSrc(out []byte) (string, error) {
	fields := strings.Fields(string(out))
	for i, f := range fields {
		if f == "src" && i+1 < len(fields) {
			ip := fields[i+1]
			if net.ParseIP(ip) == nil {
				return "", fmt.Errorf("route lookup returned invalid src %q", ip)
			}
			return ip, nil
		}
	}
	return "", fmt.Errorf("no `src` in route output %q", strings.TrimSpace(string(out)))
}

// --- consequence 2: drop the 6443 passthrough listener ---

// natPlatformPatchesMarker makes WriteSingleIPNATPatch idempotent and
// greppable. Kept distinct from the OS-4 marker — both blocks may
// coexist under one `patches:` key.
const natPlatformPatchesMarker = "# single-IP-NAT: no 6443 passthrough on this topology (do not duplicate)"

// natPlatformPatchEntry is the JSON6902 entry appended under
// spec.patches of clusters/<name>/platform.yaml. The `test` op fails
// the kustomize build loudly if platform/gateway-config ever reorders
// the listener list — silent removal of the WRONG listener would take
// down a front-door port.
const natPlatformPatchEntry = `    ` + natPlatformPatchesMarker + `
    # The envoy Service externalIP == the apiserver advertise IP here
    # (single-IP NAT, kube-dc E2E finding 17b): a 6443 listener makes
    # kube-proxy intercept apiserver traffic and envoy dial itself via
    # the kube-api TLSRoute. Tenant-cluster kube-api SNI passthrough is
    # unavailable on this topology (needs a second node IP).
    - target:
        kind: Gateway
        name: eg
        namespace: envoy-gateway-system
      patch: |
        - op: test
          path: /spec/listeners/12/name
          value: tls-passthrough
        - op: remove
          path: /spec/listeners/12`

// IngressCollidesWithAPIServer reports whether Envoy will share a node with
// kube-apiserver, which is what decides the :6443 TLS-passthrough listener's
// fate under the v2 host-bind data plane.
//
// This SUPERSEDES address-based guessing for clusters that declare their
// ingress nodes. Envoy binds the listener ports on the nodes carrying the
// ingress label, so the question is set intersection — "is any ingress node a
// control-plane node?" — not "does NODE_EXTERNAL_IP happen to equal a
// control-plane address" (codex 2026-08-07, P1). Getting it wrong is
// bidirectional harm: keep the listener on a control-plane ingress node and it
// silently never binds (the apiserver owns the port) while the Gateway still
// reports Programmed=True; remove it on a dedicated-worker ingress set and
// kube-api.<domain>:6443 loses its route.
//
// What that listener actually carries, verified live 2026-08-08: ONE TLSRoute,
// kube-api.<domain> -> the `kubernetes` Service — i.e. the MANAGEMENT cluster's
// own API. Managed (Kamaji) tenant clusters do NOT use :6443: their kubeconfigs
// point at https://<cluster>-cp-<namespace>.<domain>:443 and are served by the
// separate `tls-passthrough-wildcard` listener on :443. So removing the :6443
// listener does not touch managed clusters, and is safe whenever the front-door
// address belongs to a node running an apiserver (which then serves :6443
// itself). It is NOT safe when the front door is a VIP that may land on a node
// without an apiserver.
//
// ingressNodes empty = the operator did not declare a set, so we cannot
// derive; the caller falls back to the address heuristic.
func IngressCollidesWithAPIServer(ingressNodes, controlPlaneNodes []string) (collides, derivable bool) {
	if len(ingressNodes) == 0 {
		return false, false
	}
	cp := make(map[string]bool, len(controlPlaneNodes))
	for _, n := range controlPlaneNodes {
		if n = strings.TrimSpace(n); n != "" {
			cp[n] = true
		}
	}
	if len(cp) == 0 {
		return false, false // no control-plane knowledge → cannot derive
	}
	var onCP, offCP []string
	for _, n := range ingressNodes {
		if n = strings.TrimSpace(n); n == "" {
			continue
		}
		if cp[n] {
			onCP = append(onCP, n)
		} else {
			offCP = append(offCP, n)
		}
	}
	return len(onCP) > 0, true
}

// ErrMixedIngressPlacement fires when the ingress set straddles control-plane
// and non-control-plane nodes.
//
// The :6443 listener is a GLOBAL Gateway listener, but whether Envoy can bind it
// is a PER-NODE fact — the apiserver owns the port on a control-plane node and
// does not elsewhere. So a mixed set has no correct global answer:
//
//   - Keep the listener and it silently never binds on the control-plane
//     members, while the Gateway still reports Programmed=True.
//   - Remove it and the non-control-plane members lose a listener they could
//     have served, so kube-api.<domain>:6443 has no route through them.
//
// Removing it globally on a single intersection — which is what this code used
// to do — takes the listener away from the nodes that were fine. Refuse the
// topology instead of picking a wrong answer for half the fleet.
//
// This is not the same question as managed tenant clusters, which are on :443
// via the wildcard passthrough listener and are unaffected either way.
var ErrMixedIngressPlacement = errors.New(
	"init: ingress nodes straddle control-plane and worker nodes, which has no correct :6443 answer")

// ClassifyIngressPlacement splits the ingress set by control-plane membership so
// the caller can act per topology instead of on a single boolean.
//
// Returns onCP and offCP sorted; derivable is false when the control-plane set is
// unknown (the caller then falls back to the address heuristic).
func ClassifyIngressPlacement(ingressNodes, controlPlaneNodes []string) (onCP, offCP []string, derivable bool) {
	if len(ingressNodes) == 0 || len(controlPlaneNodes) == 0 {
		return nil, nil, false
	}
	cp := make(map[string]bool, len(controlPlaneNodes))
	for _, n := range controlPlaneNodes {
		if n = strings.TrimSpace(n); n != "" {
			cp[n] = true
		}
	}
	if len(cp) == 0 {
		return nil, nil, false
	}
	for _, n := range ingressNodes {
		if n = strings.TrimSpace(n); n == "" {
			continue
		}
		if cp[n] {
			onCP = append(onCP, n)
		} else {
			offCP = append(offCP, n)
		}
	}
	sort.Strings(onCP)
	sort.Strings(offCP)
	return onCP, offCP, true
}

// GatewayCollidesWithAPIServer reports whether the Envoy Service's external
// address is one of the cluster's OWN node IPs, read from the scaffolded
// cluster-config.env.
//
// The 6443 listener is unsafe whenever `envoy externalIP == an apiserver
// address`, and single-IP NAT is only ONE way to arrive there. The other —
// and the common one on an internal deployment — is simply setting
// NODE_EXTERNAL_IP to a control-plane node's own address. There is no NAT
// involved, so DetectArrivingIP finds nothing and the patch never fires,
// yet the consequence is identical: kube-proxy programs the externalIP for
// the Envoy Service on EVERY node, so any node dialling <that IP>:6443
// reaches Envoy instead of the apiserver. Envoy sees an unmatched SNI and
// resets.
//
// Observed on a fresh 3+3 install with NODE_EXTERNAL_IP = master01's IP:
// from a worker, master01:6443 returned errno=104 with no certificate while
// master02:6443 returned CN=kube-apiserver. Nodes fell to NotReady one at a
// time as each rke2 agent's local load balancer happened to rotate onto
// master01 — a slow, confusing failure that looks like a flaky worker.
//
// This check needs no SSH, so it also covers --no-ssh runs, where the probe
// is skipped entirely and the collision would otherwise ship unnoticed.
func GatewayCollidesWithAPIServer(envBody string) bool {
	ip := strings.TrimSpace(envValue(envBody, "NODE_EXTERNAL_IP"))
	if ip == "" {
		return false
	}
	// Both keys list control-plane addresses; either is enough, and they are
	// normally identical. Checking both means a cluster that set only one
	// still gets the protection.
	for _, key := range []string{"KUBE_OVN_MASTER_NODES", "OVN_DB_IPS"} {
		for _, candidate := range strings.Split(envValue(envBody, key), ",") {
			if controlPlaneCandidateHost(candidate) == ip {
				return true
			}
		}
	}
	return false
}

func controlPlaneCandidateHost(candidate string) string {
	candidate = strings.TrimSpace(candidate)
	scheme, address, hasScheme := strings.Cut(candidate, ":")
	if !hasScheme || (scheme != "tcp" && scheme != "ssl") {
		return candidate
	}
	host, _, err := net.SplitHostPort(address)
	if err != nil {
		return candidate
	}
	return strings.Trim(host, "[]")
}

// WriteExternalKubeAPIVIP scaffolds an OFF-ENVOY external route for the
// management kube-api on the front-door MetalLB VIP.
//
// PRODUCTIZED FROM A PRODUCTION INCIDENT (2026-08-11). The
// :6443 Envoy tls-passthrough listener is removed whenever Envoy shares a node
// with an apiserver (IngressCollidesWithAPIServer / single-IP-NAT /
// GatewayCollidesWithAPIServer). Removing it is self-sufficient ONLY when the
// front-door address is a node's OWN address — that node's apiserver then serves
// :6443 itself. On a MetalLB VIP front door the VIP can land on a node WITHOUT an
// apiserver, so removing the listener silently kills external kube-api with no
// replacement — exactly the incident (external kubectl dead, the cluster
// "unusable" from an operator's laptop while console/login stayed up).
//
// The fix serves kube-api on the SAME VIP but NOT through Envoy: a selectorless
// ClusterIP Service with externalIPs=<VIP> on :6443 + a static EndpointSlice to
// the control-plane host IPs. kube-proxy DNATs <VIP>:6443 -> an apiserver on
// whichever node MetalLB lands the VIP on; Envoy keeps :80/:443 and never binds
// :6443, so the collision cannot recur. HA across all control-plane apiservers.
//
// Why externalIPs and not a second LoadBalancer Service sharing the VIP: MetalLB
// will not co-locate a second Service on an already-allocated VIP (the Envoy
// allocation carries no sharing key and the etp Local/Cluster backends differ),
// and the kube-dc service_lb controller intercepts every type=LoadBalancer
// Service. A ClusterIP + externalIPs Service sidesteps both.
//
// The VIP is written LITERALLY (not a ${METALLB_FLOATING_IP} substitution): an
// empty substitution would render externalIPs: [""], a known front-door footgun.
// Skips writing if the manifest already exists (operator may have customised it).
func WriteExternalKubeAPIVIP(fleetRepo, clusterName, vip string, cpHostIPs []string, out io.Writer) error {
	if out == nil {
		out = io.Discard
	}
	if net.ParseIP(vip) == nil {
		return fmt.Errorf("external-kube-api: VIP %q is not an IP", vip)
	}
	eps := make([]string, 0, len(cpHostIPs))
	for _, ip := range cpHostIPs {
		ip = strings.TrimSpace(ip)
		if ip == "" {
			continue
		}
		if net.ParseIP(ip) == nil {
			return fmt.Errorf("external-kube-api: control-plane host %q is not an IP", ip)
		}
		eps = append(eps, fmt.Sprintf("  - addresses: [%q]", ip))
	}
	if len(eps) == 0 {
		return fmt.Errorf("external-kube-api: no control-plane host IPs (KUBE_OVN_MASTER_NODES) to back the endpoint")
	}
	manifest := fmt.Sprintf(externalKubeAPIManifest, vip, strings.Join(eps, "\n"))

	dir := filepath.Join(fleetRepo, "clusters", clusterName, "addons-config")
	path := filepath.Join(dir, "kube-api-external.yaml")
	if _, err := os.Stat(path); err == nil {
		return nil // already scaffolded / operator-customised — never clobber
	}
	if err := os.MkdirAll(dir, 0o755); err != nil {
		return fmt.Errorf("external-kube-api: mkdir %s: %w", dir, err)
	}
	if err := atomicWrite(path, []byte(manifest), 0o644); err != nil {
		return fmt.Errorf("external-kube-api: write %s: %w", path, err)
	}
	kpath := filepath.Join(dir, "kustomization.yaml")
	if err := patchFileLines(kpath, appendKustomizeResource("kube-api-external.yaml")); err != nil {
		return fmt.Errorf("external-kube-api: wire kustomization: %w", err)
	}
	fmt.Fprintf(out, "[scaffold] external kube-api: VIP:6443 served OFF-Envoy via a ClusterIP+externalIPs Service -> %d control-plane apiservers (the :6443 Envoy listener was removed for this VIP+host-bind topology)\n", len(eps))
	return nil
}

// externalKubeAPIManifest is the Service + EndpointSlice, %s = VIP, %s = the
// joined "  - addresses: [\"ip\"]" endpoint lines.
const externalKubeAPIManifest = `# kube-api-external — external management kube-api on the front-door VIP, served
# OFF-Envoy. Scaffolded by ` + "`kube-dc bootstrap init`" + ` because this cluster's front
# door is a MetalLB VIP AND Envoy is host-bind on apiserver node(s), so the
# Gateway :6443 tls-passthrough listener was removed (it would snipe the
# apiserver's own :6443 — production incident 2026-08-11). See natgate.go
# WriteExternalKubeAPIVIP for the full rationale.
#
# kube-proxy DNATs <VIP>:6443 -> a control-plane apiserver on whichever node
# MetalLB lands the VIP on; Envoy keeps :80/:443 and never binds :6443. If a
# control-plane node's address changes, update the EndpointSlice below.
apiVersion: v1
kind: Service
metadata:
  name: kube-api-external
  namespace: kube-system
  labels:
    app.kubernetes.io/managed-by: kube-dc
    kube-dc.com/component: front-door-kube-api
spec:
  type: ClusterIP
  externalIPs:
    - %q
  ports:
    - name: https
      port: 6443
      targetPort: 6443
      protocol: TCP
---
apiVersion: discovery.k8s.io/v1
kind: EndpointSlice
metadata:
  name: kube-api-external-1
  namespace: kube-system
  labels:
    kubernetes.io/service-name: kube-api-external
    app.kubernetes.io/managed-by: kube-dc
addressType: IPv4
ports:
  - name: https
    port: 6443
    protocol: TCP
endpoints:
%s
`

// appendKustomizeResource returns a patchFileLines patch that idempotently adds
// `- <resource>` under a kustomization's resources: key.
func appendKustomizeResource(resource string) func([]string) ([]string, bool, error) {
	want := "- " + resource
	return func(lines []string) ([]string, bool, error) {
		hasResources := false
		for _, l := range lines {
			t := strings.TrimSpace(l)
			if t == want {
				return lines, false, nil // already present
			}
			if t == "resources:" {
				hasResources = true
			}
		}
		end := len(lines)
		for end > 0 && strings.TrimSpace(lines[end-1]) == "" {
			end--
		}
		out := append([]string{}, lines[:end]...)
		if !hasResources {
			out = append(out, "resources:")
		}
		out = append(out, "  "+want)
		return out, true, nil
	}
}

// WriteSingleIPNATPatch appends the 6443-listener removal to the
// scaffolded platform.yaml. Composes with the OS-4 disabled block:
// when a `patches:` key already exists it must be ours (OS-4 marker) —
// entries are appended under it; a hand-edited patches block is
// refused, mirroring OS-4's own semantics. MUST run after
// WriteObjectStorage in the scaffold sequence (OS-4's writer refuses
// pre-existing patches: keys, including ours).
func WriteSingleIPNATPatch(fleetRepo, clusterName string, out io.Writer) error {
	if out == nil {
		out = io.Discard
	}
	path := filepath.Join(fleetRepo, "clusters", clusterName, "platform.yaml")
	if err := patchFileLines(path, patchPlatformSingleIPNAT); err != nil {
		return fmt.Errorf("single-ip-nat: patch platform.yaml: %w", err)
	}
	fmt.Fprintf(out, "[scaffold] single-IP NAT: 6443 passthrough listener removed for this cluster (tenant kube-api SNI routing unavailable — needs a second node IP)\n")
	return nil
}

func patchPlatformSingleIPNAT(lines []string) ([]string, bool, error) {
	hasPatchesKey := false
	for _, l := range lines {
		switch strings.TrimSpace(l) {
		case natPlatformPatchesMarker:
			return lines, false, nil // already wired
		case "patches:":
			hasPatchesKey = true
		}
	}
	if hasPatchesKey {
		// Compose with any block WE generated; refuse only a hand-edited one.
		//
		// This used to accept the OS-4 marker alone, which broke a normal
		// install: WriteAddons now writes the Gateway VIP patch earlier in the
		// scaffold sequence, so on a cluster with
		//   METALLB_FLOATING_IP != NODE_EXTERNAL_IP,
		//   NODE_EXTERNAL_IP a control-plane address (or single-IP NAT), and
		//   object storage ENABLED (so the OS-4 marker is absent)
		// the `patches:` key exists, no OS-4 marker is found, and init aborts.
		// Every kube-dc-owned marker has to be recognised here.
		ours := false
		for _, l := range lines {
			t := strings.TrimSpace(l)
			for _, m := range ownedPlatformPatchMarkers {
				if t == m || strings.Contains(t, m) {
					ours = true
					break
				}
			}
			if ours {
				break
			}
		}
		if !ours {
			return nil, false, fmt.Errorf("platform.yaml already has a patches: block (hand-edited?) — add the single-IP-NAT 6443-listener removal manually (marker: %q)", natPlatformPatchesMarker)
		}
	}
	end := len(lines)
	for end > 0 && strings.TrimSpace(lines[end-1]) == "" {
		end--
	}
	block := natPlatformPatchEntry
	if !hasPatchesKey {
		block = "  patches:\n" + block
	}
	out := make([]string, 0, end+24)
	out = append(out, lines[:end]...)
	out = append(out, strings.Split(block, "\n")...)
	out = append(out, "")
	return out, true, nil
}

// NodeCIDRFromAddrOutput derives the node's LAN prefix — the network its
// primary address sits on — from `ip -o addr show` output.
//
// This is the one value Tenant Networking v2 cannot get anywhere else during a
// greenfield bootstrap. INFRA_ATTACHMENT_ROUTES must carry the node subnet so a
// dual-homed pod answers kubelet probes over its infra NIC; omit it and replies
// take the wrong path, so pods never reach Ready while nothing reports an error.
//
// It cannot be inferred from the arguments the installer already has. The node's
// EXTERNAL IP is a different network, and a bare internal IP carries no mask —
// only the `inet <addr>/<prefix>` token does, which is why the prefix is read
// from this output rather than assumed to be /24.
//
// nodeIP selects which address to use, so the caller decides (normally the same
// address DetectArrivingIP resolved). Returns the masked network, e.g.
// 192.168.110.11/24 -> 192.168.110.0/24.
//
// Fails CLOSED: any doubt returns an error and the caller must degrade rather
// than guess, because a wrong-but-plausible CIDR is worse than none — it
// installs cleanly and misroutes silently.
func NodeCIDRFromAddrOutput(addrOut []byte, nodeIP string) (string, error) {
	ip := net.ParseIP(nodeIP)
	if ip == nil {
		return "", fmt.Errorf("node-cidr: %q is not an IP", nodeIP)
	}
	for _, line := range strings.Split(string(addrOut), "\n") {
		fields := strings.Fields(line)
		for i, f := range fields {
			// Only IPv4: the injected routes are IPv4 and mixing families here
			// would silently produce an unusable route.
			if f != "inet" || i+1 >= len(fields) {
				continue
			}
			addr, ipnet, err := net.ParseCIDR(fields[i+1])
			if err != nil || addr == nil || ipnet == nil {
				continue
			}
			if addr.Equal(ip) {
				return ipnet.String(), nil
			}
		}
	}
	return "", fmt.Errorf("node-cidr: no inet address matching %s in `ip -o addr show` output", nodeIP)
}

// DetectNodeCIDR resolves the node's LAN prefix over SSH — the value
// INFRA_ATTACHMENT_ROUTES needs so a dual-homed pod answers kubelet probes over
// its infra NIC.
//
// It resolves the node's PRIMARY address by route lookup rather than trusting
// the address the operator passed on the command line: that argument is the
// node's external/public IP, which on a NAT'd host is not bound to any interface
// at all, and even when it is bound it may not be the address kubelet registers
// as InternalIP. The route-source address is the one the node actually uses.
//
// Deliberately separate from DetectArrivingIP. That probe answers "is this host
// behind 1:1 NAT" and fails OPEN, because guessing wrong there only costs a
// passthrough listener. This one feeds a routing table, where a wrong answer
// misroutes silently, so it fails CLOSED and the caller must degrade rather than
// substitute a guess.
func DetectNodeCIDR(ctx context.Context, opts ArrivingIPOptions) (string, error) {
	if opts.SSH == nil {
		return "", fmt.Errorf("node-cidr: nil SSH client")
	}
	routeOut, err := opts.SSH.Run(ctx, opts.Host, "ip -4 route get 192.0.2.1")
	if err != nil {
		return "", fmt.Errorf("node-cidr: ip route get: %w", err)
	}
	src, err := parseRouteSrc(routeOut)
	if err != nil {
		return "", fmt.Errorf("node-cidr: %w", err)
	}
	addrOut, err := opts.SSH.Run(ctx, opts.Host, "ip -o addr show")
	if err != nil {
		return "", fmt.Errorf("node-cidr: ip addr show: %w", err)
	}
	return NodeCIDRFromAddrOutput(addrOut, src)
}

// ownedPlatformPatchMarkers lists every marker kube-dc writes into
// clusters/<name>/platform.yaml `patches:`. Any writer that needs to compose
// with an existing block must accept ALL of them — recognising only a subset
// turns a normal combination of features into a hard install failure.
//
// Keep this in sync when adding a new managed patch.
var ownedPlatformPatchMarkers = []string{
	disabledPlatformPatchesMarker, // object-storage disabled
	natPlatformPatchesMarker,      // single-IP NAT / 6443 listener removal
	gatewayVIPMarker,              // Gateway address = MetalLB VIP
	gatewayServiceVIPMarker,       // Envoy Service explicitly requests VIP
	byoWildcardTLSMarker,          // byo-wildcard ACME Certificate suppression
	dns01Route53Marker,            // ClusterIssuer solvers → Route53 DNS-01
}
