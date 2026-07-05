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
	"fmt"
	"io"
	"net"

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
		// Only compose with a block we generated (OS-4 disabled).
		osFourSeen := false
		for _, l := range lines {
			if strings.TrimSpace(l) == disabledPlatformPatchesMarker {
				osFourSeen = true
				break
			}
		}
		if !osFourSeen {
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
