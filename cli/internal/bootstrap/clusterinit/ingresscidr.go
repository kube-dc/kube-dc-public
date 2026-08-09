package clusterinit

import (
	"context"
	"fmt"
	"math/big"
	"net"
	"sort"
	"strings"

	"github.com/shalb/kube-dc/cli/internal/bootstrap/ports"
)

// nodeAddresser is the narrow slice of ports.K8sClient this step needs.
type nodeAddresser interface {
	NodeInternalIPs(ctx context.Context) (map[string]string, error)
}

// DeriveIngressHostCIDR returns the smallest single prefix covering every supplied
// address, which is what INGRESS_HOST_CIDR must be.
//
// WHY THIS EXISTS
//
//	The host-bind front door runs Envoy with hostNetwork, so traffic it proxies to an
//	upstream arrives with a NODE address. The platform NetworkPolicies admit that by
//	ipBlock and nothing else can: a namespaceSelector cannot map a node IP back to a pod
//	namespace, and the pinned Kube-OVN skips hostNetwork pods when building selected
//	ports, so a podSelector never matches either.
//
//	Nothing in the installer set this value. It previously defaulted to 127.0.0.1/32 —
//	syntactically valid, semantically a deny — and a cluster that adopted host-bind
//	without setting it silently 503'd OpenBao and the Flux UI. On a production cluster
//	that broke a tenant's KMS plugin, which failed the apiserver's kms-providers
//	readiness check, which left a managed cluster NotReady for ~44h with healthy workers.
//	The default has since been removed, which converts the silent denial into a loud
//	failure — but a fresh install still needs the right value, computed.
//
// WHY InternalIP AND NOT THE DISCOVERED NODE_CIDR
//
//	They are not the same network. On at least one real cluster NODE_CIDR is a public
//	/26 discovered from an external NIC while the nodes' InternalIPs are private, so
//	deriving from NODE_CIDR would admit an unrelated network and still deny Envoy. The
//	address that matters is the one the node sources from when it reaches an upstream,
//	and for same-LAN upstreams that is the InternalIP.
//
// It is a single prefix because the policies render a single ipBlock.cidr; a
// comma-separated value is one malformed CIDR, not two subnets. The result can therefore
// be wider than the addresses strictly need, which the caller surfaces rather than hides,
// because widening an allow-list is a decision an operator should see.
func DeriveIngressHostCIDR(addrs []string) (string, error) {
	if len(addrs) == 0 {
		return "", fmt.Errorf("derive INGRESS_HOST_CIDR: no ingress node addresses")
	}
	var v4, v6 []net.IP
	for _, a := range addrs {
		ip := net.ParseIP(strings.TrimSpace(a))
		if ip == nil {
			return "", fmt.Errorf("derive INGRESS_HOST_CIDR: %q is not an IP address", a)
		}
		if ip4 := ip.To4(); ip4 != nil {
			v4 = append(v4, ip4)
		} else {
			v6 = append(v6, ip)
		}
	}
	// Mixed families cannot be expressed as one prefix. Refuse rather than silently
	// admitting one family and denying the other.
	if len(v4) > 0 && len(v6) > 0 {
		return "", fmt.Errorf("derive INGRESS_HOST_CIDR: ingress nodes are dual-stack "+
			"(%d IPv4, %d IPv6) but the NetworkPolicies render ONE ipBlock — set "+
			"INGRESS_HOST_CIDR explicitly for the family the upstreams use", len(v4), len(v6))
	}
	ips := v4
	bits := 32
	if len(v6) > 0 {
		ips, bits = v6, 128
	}

	// Longest common prefix across all addresses.
	prefix := bits
	for prefix > 0 {
		masked := make(map[string]struct{}, len(ips))
		mask := net.CIDRMask(prefix, bits)
		for _, ip := range ips {
			masked[ip.Mask(mask).String()] = struct{}{}
		}
		if len(masked) == 1 {
			break
		}
		prefix--
	}
	mask := net.CIDRMask(prefix, bits)
	base := ips[0].Mask(mask)
	return fmt.Sprintf("%s/%d", base.String(), prefix), nil
}

// IngressHostCIDRFor resolves the ingress nodes' InternalIPs and derives the prefix.
// Returns the prefix, the addresses it was derived from (for the operator-facing line),
// and an error that names the missing node rather than guessing.
func IngressHostCIDRFor(ctx context.Context, k8s nodeAddresser, nodes []string) (string, []string, error) {
	if k8s == nil {
		return "", nil, fmt.Errorf("derive INGRESS_HOST_CIDR: no cluster connection")
	}
	if len(nodes) == 0 {
		return "", nil, fmt.Errorf("derive INGRESS_HOST_CIDR: no ingress nodes planned")
	}
	all, err := k8s.NodeInternalIPs(ctx)
	if err != nil {
		return "", nil, fmt.Errorf("derive INGRESS_HOST_CIDR: %w", err)
	}
	var addrs, missing []string
	for _, n := range nodes {
		ip, ok := all[n]
		if !ok || ip == "" {
			missing = append(missing, n)
			continue
		}
		addrs = append(addrs, ip)
	}
	if len(missing) > 0 {
		sort.Strings(missing)
		return "", nil, fmt.Errorf("derive INGRESS_HOST_CIDR: no InternalIP for ingress "+
			"node(s) %s", strings.Join(missing, ", "))
	}
	sort.Strings(addrs)
	cidr, err := DeriveIngressHostCIDR(addrs)
	if err != nil {
		return "", addrs, err
	}
	return cidr, addrs, nil
}

// cidrIsWiderThanNeeded reports whether the derived prefix spans far more addresses than
// the nodes occupy, so the caller can say so instead of quietly widening an allow-list.
func cidrIsWiderThanNeeded(cidr string, nodeCount int) bool {
	_, net_, err := net.ParseCIDR(cidr)
	if err != nil || nodeCount <= 0 {
		return false
	}
	ones, bits := net_.Mask.Size()
	size := new(big.Int).Lsh(big.NewInt(1), uint(bits-ones))
	return size.Cmp(big.NewInt(int64(4*nodeCount))) > 0
}

var _ nodeAddresser = ports.K8sClient(nil)
