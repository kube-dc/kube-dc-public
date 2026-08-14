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
	"sort"
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

// RETIRED 2026-08-11: the single-IP-NAT :6443 listener-removal patch
// (natPlatformPatchesMarker / natPlatformPatchEntry) and its writer
// WriteSingleIPNATPatch are gone. Off-Envoy kube-api is the base default now —
// the shared gateway no longer ships a :6443 listener, so there is nothing to
// remove, and the index-coupled `test /spec/listeners/12` patch this emitted was
// a latent bomb (any base listener shift failed the whole Kustomization). New
// clusters get front-door/components/kube-api-off-envoy + KUBE_API_ARRIVAL_IP
// (see ResolveKubeAPIArrivalIP). Same for WriteExternalKubeAPIVIP's static
// EndpointSlice, replaced by the controller-managed off-Envoy component.

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
	gatewayVIPMarker,              // Gateway address = MetalLB VIP
	gatewayServiceVIPMarker,       // Envoy Service explicitly requests VIP
	byoWildcardTLSMarker,          // byo-wildcard ACME Certificate suppression
	dns01Route53Marker,            // ClusterIssuer solvers → Route53 DNS-01
}

// EgressGatewayProbeOptions parameterizes ProbeEgressGateway.
type EgressGatewayProbeOptions struct {
	SSH     ports.SSHClient
	Host    ports.SSHHost
	Gateway string // EXT_NET_GATEWAY — the tenant-egress next hop
	// ExtIface is EXT_NET_INTERFACE (the ext parent interface, e.g. bond0). When
	// set, a route that egresses via a DIFFERENT interface base is treated as
	// inconclusive rather than probed — an overlapping connected management route
	// on eth0 must not be mistaken for the (possibly down) tenant ext VLAN.
	ExtIface string
	// AnchorIPs is EXT_NET_ANCHOR_IPS (comma/space list). Used to sanity-check a
	// gateway that resolves to a local address: a local IP is only an expected
	// node-egress anchor when it is one of the CONFIGURED anchors — otherwise it
	// is very likely a fat-fingered EXT_NET_GATEWAY, which is surfaced as a warning.
	AnchorIPs string
	Out       io.Writer
}

// EgressGatewayResult reports what the ARP probe found.
type EgressGatewayResult struct {
	// Probed is false when the check could not run conclusively: the gateway is
	// the node's own anchor (node-egress), the ext network is not directly
	// configured on this node yet (pre-CNI), the route egresses via a different
	// interface, or the L2 probe tool was unavailable/ambiguous. Reachable is
	// only meaningful when Probed is true.
	Probed    bool
	Reachable bool
	// Warn asks the caller to surface Note as a WARNING rather than an
	// informational line: it marks a config smell (a local gateway that is not a
	// configured anchor) even though the ARP check itself could not run.
	Warn  bool
	Iface string
	Note  string
}

// ProbeEgressGateway checks whether EXT_NET_GATEWAY actually answers ARP from
// the node — the difference between "in-CIDR, looks fine" and "tenant internet
// egress silently black-holes" (the green-install/dead-egress trap). It runs
// PRE-CNI, so it is best-effort and fails OPEN: it verifies only when the
// gateway is directly connected on a configured interface, and reports
// Probed=false (skip, not fail) for the node-egress-anchor and
// not-yet-configured cases rather than blocking a valid install.
func ProbeEgressGateway(ctx context.Context, opts EgressGatewayProbeOptions) (EgressGatewayResult, error) {
	if opts.SSH == nil {
		return EgressGatewayResult{}, fmt.Errorf("egress-gw probe: nil SSH client")
	}
	if net.ParseIP(opts.Gateway) == nil {
		return EgressGatewayResult{}, fmt.Errorf("egress-gw probe: %q is not an IP", opts.Gateway)
	}
	routeOut, err := opts.SSH.Run(ctx, opts.Host, fmt.Sprintf("ip -4 route get %s", opts.Gateway))
	if err != nil {
		return EgressGatewayResult{}, fmt.Errorf("egress-gw probe: ip route get: %w", err)
	}
	iface, direct, isLocal := parseEgressRoute(string(routeOut))
	if isLocal {
		// `local` proves only that the gateway is an address of THIS host — not
		// that node-egress is configured. It is the expected node-egress anchor
		// pattern ONLY when the gateway is one of the configured anchors; a local
		// IP that is not a known anchor is almost always a fat-fingered gateway.
		if opts.AnchorIPs != "" && !ipInList(opts.Gateway, opts.AnchorIPs) {
			return EgressGatewayResult{Probed: false, Warn: true, Iface: "lo",
				Note: "gateway resolves to a local address on this node that is NOT a configured ext anchor (EXT_NET_ANCHOR_IPS) — likely a misconfigured EXT_NET_GATEWAY"}, nil
		}
		return EgressGatewayResult{Probed: false, Iface: "lo", Note: "gateway is this node's own address (node-egress anchor) — ARP check N/A; verify upstream forwarding after install"}, nil
	}
	if !direct || iface == "" {
		return EgressGatewayResult{Probed: false, Note: "ext-network interface not directly configured on this node yet — cannot verify pre-CNI"}, nil
	}
	// A route that egresses via a different interface than the configured ext
	// parent is very likely an overlapping connected management route, NOT the
	// tenant ext VLAN (which may simply be down pre-CNI). Don't probe it — a
	// reply there would be a false "egress reachable".
	if opts.ExtIface != "" && !ifaceMatches(iface, opts.ExtIface) {
		return EgressGatewayResult{Probed: false, Iface: iface,
			Note: fmt.Sprintf("gateway resolves via %s, not the configured ext interface %s — the ext VLAN may not be up yet; cannot verify pre-CNI", iface, opts.ExtIface)}, nil
	}
	// Defense-in-depth: the interface name is interpolated into a root SSH
	// command. Kernel names are normally tame, but reject anything outside a
	// safe grammar rather than trust `ip route get` output.
	if !isSafeIfaceName(iface) {
		return EgressGatewayResult{Probed: false, Iface: iface, Note: "route interface name failed validation — skipping ARP probe"}, nil
	}
	// arping is the L2 authority: rc 0 = replied, rc 1 = ran and got zero
	// replies (a DEFINITIVE negative), rc >=2 = could not run (no CAP_NET_RAW,
	// bad args). Without arping, only a ping REPLY is trustworthy (a ping
	// non-reply may just be ICMP filtering), so that path yields REPLIED or
	// INCONCLUSIVE — never a false NOREPLY. Inputs are pre-validated (Gateway is
	// an IP, iface passed isSafeIfaceName), and single-quoted for good measure.
	probeCmd := fmt.Sprintf(
		"if command -v arping >/dev/null 2>&1; then "+
			"arping -c2 -w3 -I '%[1]s' '%[2]s' >/dev/null 2>&1; rc=$?; "+
			"if [ \"$rc\" -eq 0 ]; then echo REPLIED; "+
			"elif [ \"$rc\" -eq 1 ]; then echo NOREPLY; "+
			"else echo INCONCLUSIVE; fi; "+
			"elif ping -c1 -W2 '%[2]s' >/dev/null 2>&1; then echo REPLIED; "+
			"else echo INCONCLUSIVE; fi",
		iface, opts.Gateway)
	out, rerr := opts.SSH.Run(ctx, opts.Host, probeCmd)
	if rerr != nil {
		return EgressGatewayResult{Probed: false, Iface: iface, Note: fmt.Sprintf("ARP probe could not run (%v)", rerr)}, nil
	}
	switch s := string(out); {
	case strings.Contains(s, "REPLIED"):
		return EgressGatewayResult{Probed: true, Reachable: true, Iface: iface}, nil
	case strings.Contains(s, "NOREPLY"):
		return EgressGatewayResult{Probed: true, Reachable: false, Iface: iface}, nil
	default: // INCONCLUSIVE / no arping+ICMP-filtered / empty
		return EgressGatewayResult{Probed: false, Iface: iface, Note: "L2 probe inconclusive (arping unavailable/unprivileged or ICMP filtered) — verify egress by hand"}, nil
	}
}

// isSafeIfaceName accepts only interface names within a conservative grammar
// (Linux IFNAMSIZ is 16, so 15 usable chars). It exists to keep a name lifted
// from `ip route get` output out of a shell command if it ever contained a
// metacharacter such as `;` or `&`.
func isSafeIfaceName(s string) bool {
	if s == "" || len(s) > 15 {
		return false
	}
	for _, r := range s {
		switch {
		case r >= 'a' && r <= 'z', r >= 'A' && r <= 'Z', r >= '0' && r <= '9':
		case r == '.' || r == '_' || r == '-' || r == ':' || r == '@':
		default:
			return false
		}
	}
	return true
}

// ifaceBase strips a VLAN sub-interface suffix (bond0.163 → bond0) so a route
// via a VLAN of the configured ext parent still matches.
func ifaceBase(s string) string { return strings.SplitN(s, ".", 2)[0] }

// ifaceMatches reports whether a route interface belongs to the configured ext
// parent (same base name), tolerating a VLAN sub-interface on either side.
func ifaceMatches(routeIface, extIface string) bool {
	return ifaceBase(routeIface) == ifaceBase(extIface)
}

// ipInList reports whether ip (normalized) appears in a comma/space separated
// list of IPs.
func ipInList(ip, list string) bool {
	want := net.ParseIP(ip)
	if want == nil {
		return false
	}
	for _, tok := range strings.FieldsFunc(list, func(r rune) bool { return r == ',' || r == ' ' || r == '\t' || r == '\n' }) {
		if got := net.ParseIP(strings.TrimSpace(tok)); got != nil && got.Equal(want) {
			return true
		}
	}
	return false
}

// parseEgressRoute classifies `ip -4 route get <gw>` output:
//   - direct=true: gw is directly connected on iface (`... dev <iface>` with no
//     `via`) → arpable.
//   - isLocal=true: gw is one of THIS node's own addresses (`local <gw> dev lo`)
//     — the node-egress anchor pattern (gateway == node anchor IP).
//   - both false: gw is reached via a next hop (`... via ...`) — ext network not
//     directly configured on this node yet.
func parseEgressRoute(routeOut string) (iface string, direct, isLocal bool) {
	line := strings.TrimSpace(routeOut)
	if line == "" {
		return "", false, false
	}
	fields := strings.Fields(line)
	if len(fields) > 0 && fields[0] == "local" {
		return "lo", false, true
	}
	// Token scan (not a raw " via " substring) so field order can't fool it.
	for _, f := range fields {
		if f == "via" {
			return devFromRouteFields(fields), false, false
		}
	}
	return devFromRouteFields(fields), true, false
}

func devFromRouteFields(fields []string) string {
	for i, f := range fields {
		if f == "dev" && i+1 < len(fields) {
			return fields[i+1]
		}
	}
	return ""
}
