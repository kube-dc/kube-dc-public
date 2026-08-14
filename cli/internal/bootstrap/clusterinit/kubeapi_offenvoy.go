package clusterinit

import (
	"fmt"
	"io"
	"net"
	"os"
	"path/filepath"
	"strings"
)

// ResolveKubeAPIArrivalIP fills in cluster-config.env's KUBE_API_ARRIVAL_IP when
// the starter left it as the CHANGEME placeholder.
//
// Off-Envoy kube-api is the default management-API front door on every new
// cluster (front-door/components/kube-api-off-envoy): a selectorless
// ClusterIP + externalIPs Service on :6443 whose externalIP is
// ${KUBE_API_ARRIVAL_IP} — the address external kube-api arrives on at node
// PREROUTING. add-cluster.sh already sets that to the node's own address on a
// none/1:1-NAT layer, but on a MetalLB layer the arrival address is the
// announced VIP, which is operator-supplied (METALLB_FLOATING_IP starts as
// CHANGEME). This substitutes the resolved VIP so both keys carry one value.
//
// Never routes :6443 through Envoy — that collides with the apiserver on
// control-plane ingress nodes (production incident 2026-08-11), and the base
// gateway no longer ships a :6443 listener. If the arrival IP cannot be
// resolved (VIP still CHANGEME/empty), the placeholder is left in place and a
// warning is emitted: the render gate rejects externalIPs:[CHANGEME], so the
// misconfiguration fails loudly rather than shipping a dark kube-api.
func ResolveKubeAPIArrivalIP(fleetRepo, clusterName string, out io.Writer) error {
	if out == nil {
		out = io.Discard
	}
	path := filepath.Join(fleetRepo, "clusters", clusterName, "cluster-config.env")
	body, err := os.ReadFile(path)
	if err != nil {
		// No cluster-config.env (older starter / mock harness) — nothing to
		// resolve. The scaffold's other steps report the missing file.
		return nil
	}
	arrival := strings.TrimSpace(envValue(string(body), "KUBE_API_ARRIVAL_IP"))
	if net.ParseIP(arrival) != nil {
		return nil // already a real IP (none/1:1-NAT, or already resolved)
	}
	// The VIP fallback applies ONLY on a MetalLB address layer, where the arrival
	// address IS the announced VIP. On none/1:1-NAT the arrival address is the
	// node's own IP (add-cluster.sh writes it); if that is somehow not a real IP
	// we must NOT borrow METALLB_FLOATING_IP — on this layer it is unannounced and
	// may be a stale leftover, so pinning externalIPs to it would silently
	// black-hole kube-api. Warn and leave it for the operator (codex 2026-08-11).
	layer := strings.TrimSpace(envValue(string(body), "INGRESS_ADDRESS_LAYER"))
	if layer != "metallb-l2" && layer != "metallb-bgp" {
		fmt.Fprintf(out, "[scaffold] WARNING: KUBE_API_ARRIVAL_IP=%q is not a real IP on address layer %q — external kube-api (:6443) will be DARK until you set it to this cluster's front-door address in clusters/%s/cluster-config.env\n", arrival, layer, clusterName)
		return nil
	}
	vip := strings.TrimSpace(envValue(string(body), "METALLB_FLOATING_IP"))
	if net.ParseIP(vip) == nil {
		fmt.Fprintf(out, "[scaffold] WARNING: KUBE_API_ARRIVAL_IP=%q is not a real IP and METALLB_FLOATING_IP is unset — external kube-api (:6443) will be DARK until you set KUBE_API_ARRIVAL_IP to the front-door VIP in clusters/%s/cluster-config.env\n", arrival, clusterName)
		return nil
	}
	if err := patchFileLines(path, setEnvValueLine("KUBE_API_ARRIVAL_IP", vip)); err != nil {
		return fmt.Errorf("kube-api-arrival-ip: %w", err)
	}
	fmt.Fprintf(out, "[scaffold] external kube-api: off-Envoy on KUBE_API_ARRIVAL_IP=%s (= the front-door VIP); :6443 is never served through Envoy\n", vip)
	return nil
}

// setEnvValueLine returns a patchFileLines transform that rewrites the VALUE of
// an existing `KEY=...` line, preserving comments and surrounding lines. The
// starter always emits the key, so a missing key is a no-op (changed=false)
// rather than an append — an unexpected absence is caught by the render gate,
// not silently papered over here.
func setEnvValueLine(key, value string) func([]string) ([]string, bool, error) {
	prefix := key + "="
	return func(lines []string) ([]string, bool, error) {
		for i, l := range lines {
			if strings.HasPrefix(strings.TrimSpace(l), prefix) {
				want := key + "=" + value
				if l == want {
					return lines, false, nil
				}
				lines[i] = want
				return lines, true, nil
			}
		}
		return lines, false, nil
	}
}
