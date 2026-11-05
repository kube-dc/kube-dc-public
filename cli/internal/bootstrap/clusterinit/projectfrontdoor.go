package clusterinit

import (
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"

	"gopkg.in/yaml.v3"
)

// KeyProjectFrontDoorDNS records that init gave Project Pods an internal front
// door; WriteProductComponents then answers the installation's zone with it
// in Project DNS.
const KeyProjectFrontDoorDNS = "PROJECT_FRONT_DOOR_DNS"

const projectFrontDoorDNSDir = "vpc-dns-front-door"

// projectFrontDoorBlocked reports the dedicated MetalLB front-door VIP when it
// lies inside INFRA_ATTACHMENT_ROUTES on an installation with infra attachment
// on (Class D, docs/platform/internal-platform-endpoints.md). Dual-homed
// Project Pods dial such an address over the infra NIC, whose SecurityGroup
// drops new connections to it: every platform hostname (S3, Keycloak, the
// console) times out from Projects although VMs and platform Pods reach it.
// A node's own address (layer none) is never reported: a /32 for it would also
// carry kubelet probe replies.
func projectFrontDoorBlocked(get func(string) string) string {
	if !strings.EqualFold(strings.TrimSpace(get("INFRA_ATTACHMENT_ENABLED")), "true") {
		return ""
	}
	if platformEndpointOverlayEnabled(get) || strings.TrimSpace(get("ENVOY_GATEWAY_INTERNAL_VIP")) != "" {
		return "" // an internal front door is already configured
	}
	// MetalLB L2 only: the internal endpoint inherits the advertisement mode,
	// and a BGP-advertised VIP on the on-link external subnet gets no ARP.
	if strings.TrimSpace(get("INGRESS_ADDRESS_LAYER")) != "metallb-l2" {
		return ""
	}
	vip := canonicalIPv4(get("METALLB_FLOATING_IP"))
	if vip == "" || !routeCoversIP(get("INFRA_ATTACHMENT_ROUTES"), vip) {
		return ""
	}
	return vip
}

// projectFrontDoorBlockedIn is projectFrontDoorBlocked for an env file.
func projectFrontDoorBlockedIn(path string) string {
	content, err := os.ReadFile(path)
	if err != nil {
		return ""
	}
	return projectFrontDoorBlocked(func(k string) string { return envValue(string(content), k) })
}

// ResolveProjectFrontDoor gives Project Pods an internal front door on the
// external network when the public one is blocked for them (see
// projectFrontDoorBlocked): the same Envoy platform endpoint, VIP, anchors,
// IPAM reservation and global allowlists as the 1:1-NAT path, reached over
// the Project NIC. It reports whether it did.
func ResolveProjectFrontDoor(fleetRepo, clusterName string, ingressNodes, controlPlaneNodes []string, extraExcludes string, out io.Writer) (bool, error) {
	if out == nil {
		out = io.Discard
	}
	path := filepath.Join(fleetRepo, "clusters", clusterName, "cluster-config.env")
	content, err := os.ReadFile(path)
	if err != nil {
		return false, fmt.Errorf("project front door: read %s: %w", path, err)
	}
	env := string(content)
	front := projectFrontDoorBlocked(func(k string) string { return envValue(env, k) })
	if front == "" {
		return false, nil
	}
	fmt.Fprintf(out, "[scaffold] front door %s is inside the infra routes, where Project Pods cannot open connections to it\n", front)
	// The chart's internal endpoint selects control-plane nodes as Envoy
	// backends; with Envoy on dedicated ingress workers it would have none.
	if stray := notIn(ingressNodes, controlPlaneNodes); len(controlPlaneNodes) == 0 || len(stray) > 0 {
		fmt.Fprintf(out, "[scaffold] WARNING: not configuring the internal front door: Envoy runs on %v, which are not all control-plane nodes %v; configure it by hand with a matching backend selector (docs/platform/internal-platform-endpoints.md, Class D)\n", ingressNodes, controlPlaneNodes)
		return false, nil
	}
	if err := resolveInternalGateway(fleetRepo, clusterName, ingressNodes, true, extraExcludes, out); err != nil {
		return false, err
	}
	return true, nil
}

// notIn returns the entries of a that are not in b.
func notIn(a, b []string) []string {
	set := map[string]bool{}
	for _, v := range b {
		set[strings.TrimSpace(v)] = true
	}
	var out []string
	for _, v := range a {
		if v = strings.TrimSpace(v); v != "" && !set[v] {
			out = append(out, v)
		}
	}
	return out
}

// writeProjectFrontDoorDNS writes a Component that makes every Project
// resolver answer A queries for the installation's zone with the internal
// front door. The whole zone, not a list: managed registries and exposed
// Services get generated hostnames. It replaces the starter's Corefile with
// two zone blocks in front of the starter's own server block, which is kept
// verbatim.
// dnsOpts carries what the Corefile must keep or cover besides the zone:
// an operator-configured internal API VIP for kube-api, and an S3 hostname
// outside the installation domain.
type frontDoorDNSOptions struct {
	KubeAPIInternal bool   // PLATFORM_ENDPOINT_KUBE_API_ENABLED with KUBE_API_INTERNAL_VIP
	ExtraS3Zone     string // S3_HOSTNAME when it is not under DOMAIN
}

func writeProjectFrontDoorDNS(fleetRepo, clusterName string, dnsOpts frontDoorDNSOptions) (string, error) {
	base, err := os.ReadFile(filepath.Join(fleetRepo, "infrastructure", "kube-ovn-network", "vpc-dns.yaml"))
	if err != nil {
		return "", fmt.Errorf("project front door DNS: read the starter's vpc-dns.yaml: %w", err)
	}
	corefile, err := vpcDNSCorefile(string(base))
	if err != nil {
		return "", err
	}
	// The new zone blocks answer before the starter's block, so anything the
	// starter does for names under the domain would be bypassed. Accept only
	// the plain layout (Kubernetes records, upstream forwarding through
	// /etc/resolv.conf, the usual housekeeping) and refuse anything else
	// rather than override it silently.
	if err := plainVPCDNSCorefile(corefile); err != nil {
		return "", err
	}
	// The most specific zone wins in CoreDNS. kube-api keeps resolving
	// upstream: the internal endpoint serves 443 only, not the API's 6443.
	// Every other A query under the domain gets the internal front door;
	// other types and anything else go upstream as before.
	kubeAPI := ""
	if dnsOpts.KubeAPIInternal {
		// Keep the operator's internal API endpoint for kube-api.
		kubeAPI = "    hosts {\n      ${KUBE_API_INTERNAL_VIP} kube-api.${DOMAIN}\n      fallthrough\n    }\n"
	}
	s3Zone := ""
	if dnsOpts.ExtraS3Zone != "" {
		s3Zone = dnsOpts.ExtraS3Zone + `:53 {
    errors
    template IN A ` + dnsOpts.ExtraS3Zone + ` {
      answer "{{ .Name }} 60 IN A ${ENVOY_GATEWAY_INTERNAL_VIP}"
    }
    forward . /etc/resolv.conf {
      prefer_udp
    }
    cache 30
}
`
	}
	corefile = `kube-api.${DOMAIN}:53 {
    errors
` + kubeAPI + `    forward . /etc/resolv.conf {
      prefer_udp
    }
    cache 30
}
` + s3Zone + `${DOMAIN}:53 {
    errors
    template IN A ${DOMAIN} {
      answer "{{ .Name }} 60 IN A ${ENVOY_GATEWAY_INTERNAL_VIP}"
    }
    forward . /etc/resolv.conf {
      prefer_udp
    }
    cache 30
}
` + corefile
	var b strings.Builder
	b.WriteString(`# Generated by kube-dc bootstrap init: Project Pods are dual-homed and the
# front door is inside the infra routes, so Project DNS answers the
# installation's zone with the internal front door on the external network.
apiVersion: kustomize.config.k8s.io/v1alpha1
kind: Component
patches:
  - target:
      kind: ConfigMap
      name: vpc-dns-corefile
      namespace: kube-system
    patch: |-
      - op: replace
        path: /data/Corefile
        value: |-
`)
	for _, line := range strings.Split(strings.TrimRight(corefile, "\n"), "\n") {
		if line == "" {
			b.WriteString("\n")
			continue
		}
		b.WriteString("          " + line + "\n")
	}
	dir := filepath.Join(fleetRepo, "clusters", clusterName, projectFrontDoorDNSDir)
	if err := os.MkdirAll(dir, 0o755); err != nil {
		return "", err
	}
	if err := atomicWrite(filepath.Join(dir, "kustomization.yaml"), []byte(b.String()), 0o644); err != nil {
		return "", err
	}
	return filepath.ToSlash(filepath.Join("clusters", clusterName, projectFrontDoorDNSDir)), nil
}

// plainVPCDNSCorefile accepts the starter's default Project resolver only.
func plainVPCDNSCorefile(corefile string) error {
	allowed := map[string]bool{
		".:53": true, "errors": true, "health": true, "lameduck": true, "ready": true,
		"kubernetes": true, "pods": true, "fallthrough": true, "ttl": true, "prometheus": true,
		"forward": true, "prefer_udp": true, "cache": true, "loop": true, "reload": true,
		"loadbalance": true, "}": true,
	}
	refuse := func(what string) error {
		return fmt.Errorf("project front door DNS: the starter's vpc-dns Corefile %s; configure Project DNS by hand (docs/platform/internal-platform-endpoints.md, Class D)", what)
	}
	servers := 0
	for _, line := range strings.Split(corefile, "\n") {
		f := strings.Fields(line)
		if len(f) == 0 || strings.HasPrefix(f[0], "#") {
			continue
		}
		if !allowed[f[0]] {
			return refuse(fmt.Sprintf("uses %q", f[0]))
		}
		switch f[0] {
		case ".:53":
			// One server block for the root zone only.
			if servers++; servers > 1 || strings.Join(f, " ") != ".:53 {" {
				return refuse(fmt.Sprintf("declares the server %q", strings.Join(f, " ")))
			}
		case "forward":
			// Exactly the host resolvers, as the generated blocks do.
			if dest := strings.Join(f[1:], " "); dest != ". /etc/resolv.conf" && dest != ". /etc/resolv.conf {" {
				return refuse(fmt.Sprintf("forwards %q", dest))
			}
		}
	}
	if servers != 1 {
		return refuse("has no single .:53 server block")
	}
	return nil
}

// vpcDNSCorefile extracts the Corefile of the vpc-dns-corefile ConfigMap from
// the starter's multi-document vpc-dns.yaml.
func vpcDNSCorefile(manifest string) (string, error) {
	for _, doc := range strings.Split(manifest, "\n---") {
		if !strings.Contains(doc, "name: vpc-dns-corefile") {
			continue
		}
		var cm struct {
			Data map[string]string `yaml:"data"`
		}
		if err := yaml.Unmarshal([]byte(doc), &cm); err != nil {
			return "", fmt.Errorf("project front door DNS: parse vpc-dns-corefile: %w", err)
		}
		if cm.Data["Corefile"] == "" {
			break
		}
		return cm.Data["Corefile"], nil
	}
	return "", fmt.Errorf("project front door DNS: the starter's vpc-dns.yaml has no vpc-dns-corefile Corefile")
}
