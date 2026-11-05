package clusterinit

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"gopkg.in/yaml.v3"
)

func classDEnv(overrides ...string) string {
	lines := []string{
		"INFRA_ATTACHMENT_ENABLED=true",
		"INFRA_ATTACHMENT_ROUTES=192.0.2.0/24,172.30.0.0/22,10.100.0.0/16",
		"INFRA_ATTACHMENT_PLATFORM_INGRESS_VIP=192.0.2.30",
		"INGRESS_ADDRESS_LAYER=metallb-l2", "METALLB_FLOATING_IP=192.0.2.30",
		"EXT_NET_CIDR=198.51.100.0/24", "EXT_NET_GATEWAY=198.51.100.1",
		"EXT_NET_EXCLUDE_IPS=198.51.100.1..198.51.100.64",
		"EXT_NET_MGMT_SNAT_IP=198.51.100.64", "KUBE_OVN_GW_NODES=node-a,node-b,node-c",
		"ENVOY_GATEWAY_INTERNAL_VIP=", "PLATFORM_ENDPOINT_ENVOY_GATEWAY_ENABLED=false",
		"EXT_NET_ANCHOR_IPS=", "EXT_NET_ANCHOR_REQUIRED=false", "",
	}
	env := strings.Join(lines, "\n")
	for _, o := range overrides {
		k, _, _ := strings.Cut(o, "=")
		lines := strings.Split(env, "\n")
		for i, l := range lines {
			if strings.HasPrefix(l, k+"=") {
				lines[i] = o
			}
		}
		env = strings.Join(lines, "\n")
	}
	return env
}

// A dedicated MetalLB VIP inside the infra routes is Class D; a node address,
// a VIP outside the routes, no infra attachment or a configured internal
// front door is not.
func TestProjectFrontDoorBlocked(t *testing.T) {
	for _, tc := range []struct {
		name string
		env  string
		want string
	}{
		{"class D", classDEnv(), "192.0.2.30"},
		{"VIP outside the routes", classDEnv("METALLB_FLOATING_IP=203.0.113.10"), ""},
		{"node address front door", classDEnv("INGRESS_ADDRESS_LAYER=none"), ""},
		{"BGP-advertised VIP", classDEnv("INGRESS_ADDRESS_LAYER=metallb-bgp"), ""},
		{"no infra attachment", classDEnv("INFRA_ATTACHMENT_ENABLED=false"), ""},
		{"internal front door already on", classDEnv("PLATFORM_ENDPOINT_ENVOY_GATEWAY_ENABLED=true"), ""},
		{"internal VIP already chosen", classDEnv("ENVOY_GATEWAY_INTERNAL_VIP=198.51.100.70"), ""},
	} {
		t.Run(tc.name, func(t *testing.T) {
			if got := projectFrontDoorBlocked(func(k string) string { return envValue(tc.env, k) }); got != tc.want {
				t.Fatalf("got %q want %q", got, tc.want)
			}
		})
	}
}

// The Class D path reserves the internal VIP and anchors like the 1:1-NAT
// path but keeps them off the infra attachment: routes, the control-plane
// front-door grant and the workload grant are unchanged.
func TestResolveProjectFrontDoor(t *testing.T) {
	repo := t.TempDir()
	dir := filepath.Join(repo, "clusters", "fixture")
	if err := os.MkdirAll(dir, 0o755); err != nil {
		t.Fatal(err)
	}
	path := filepath.Join(dir, "cluster-config.env")
	if err := os.WriteFile(path, []byte(classDEnv()), 0o644); err != nil {
		t.Fatal(err)
	}
	var out strings.Builder
	cp := []string{"node-a", "node-b", "node-c"}
	did, err := ResolveProjectFrontDoor(repo, "fixture", cp, cp, "198.51.100.240..198.51.100.254", &out)
	if err != nil || !did {
		t.Fatalf("did=%v err=%v", did, err)
	}
	body, _ := os.ReadFile(path)
	for _, want := range []string{
		"EXT_NET_EXCLUDE_IPS=198.51.100.1..198.51.100.68",
		"ENVOY_GATEWAY_INTERNAL_VIP=198.51.100.65", "PLATFORM_ENDPOINT_ENVOY_GATEWAY_ENABLED=true",
		`INGRESS_GLOBAL_ALLOWLIST=["198.51.100.65"]`, `EGRESS_GLOBAL_ALLOWLIST=["198.51.100.65"]`,
		"EXT_NET_ANCHOR_IPS=node-a=198.51.100.66/24,node-b=198.51.100.67/24,node-c=198.51.100.68/24",
		"EXT_NET_ANCHOR_REQUIRED=true", "PROJECT_FRONT_DOOR_DNS=zone", "SERVICES_INTERNAL_GATEWAY_REQUIRED=true",
		"INFRA_ATTACHMENT_ROUTES=192.0.2.0/24,172.30.0.0/22,10.100.0.0/16",
		"INFRA_ATTACHMENT_PLATFORM_INGRESS_VIP=192.0.2.30",
	} {
		if !strings.Contains(string(body), want+"\n") {
			t.Errorf("missing %q in %s", want, body)
		}
	}
	if strings.Contains(string(body), "WORKLOAD_EGRESS") {
		t.Errorf("the workload grant must not be set: %s", body)
	}
	if !strings.Contains(out.String(), "inside the infra routes") {
		t.Errorf("output %q", out.String())
	}
	// Not Class D: nothing is written.
	other := classDEnv("METALLB_FLOATING_IP=203.0.113.10")
	if err := os.WriteFile(path, []byte(other), 0o644); err != nil {
		t.Fatal(err)
	}
	if did, err := ResolveProjectFrontDoor(repo, "fixture", cp, cp, "", nil); did || err != nil {
		t.Fatalf("did=%v err=%v", did, err)
	}
	if body, _ := os.ReadFile(path); string(body) != other {
		t.Fatalf("env changed: %s", body)
	}
}

// Refusals: Envoy off the control plane (the endpoint would have no
// backends), a site reservation overlapping the VIP or anchors, and an infra
// route that would carry the internal VIP.
func TestResolveProjectFrontDoorRefusals(t *testing.T) {
	write := func(t *testing.T, env string) (string, string) {
		repo := t.TempDir()
		dir := filepath.Join(repo, "clusters", "fixture")
		if err := os.MkdirAll(dir, 0o755); err != nil {
			t.Fatal(err)
		}
		path := filepath.Join(dir, "cluster-config.env")
		if err := os.WriteFile(path, []byte(env), 0o644); err != nil {
			t.Fatal(err)
		}
		return repo, path
	}
	cp := []string{"node-a", "node-b", "node-c"}
	t.Run("ingress on workers", func(t *testing.T) {
		repo, path := write(t, classDEnv())
		var out strings.Builder
		did, err := ResolveProjectFrontDoor(repo, "fixture", []string{"node-a", "worker-1"}, cp, "", &out)
		if did || err != nil || !strings.Contains(out.String(), "WARNING: not configuring") {
			t.Fatalf("did=%v err=%v out=%q", did, err, out.String())
		}
		if body, _ := os.ReadFile(path); string(body) != classDEnv() {
			t.Fatalf("env changed")
		}
	})
	t.Run("control plane unknown", func(t *testing.T) {
		repo, _ := write(t, classDEnv())
		if did, err := ResolveProjectFrontDoor(repo, "fixture", cp, nil, "", nil); did || err != nil {
			t.Fatalf("did=%v err=%v", did, err)
		}
	})
	t.Run("site reservation overlaps", func(t *testing.T) {
		repo, _ := write(t, classDEnv())
		_, err := ResolveProjectFrontDoor(repo, "fixture", cp, cp, "198.51.100.66", nil)
		if err == nil || !strings.Contains(err.Error(), "overlaps") {
			t.Fatalf("err=%v", err)
		}
	})
	t.Run("infra route covers the internal VIP", func(t *testing.T) {
		repo, _ := write(t, classDEnv("INFRA_ATTACHMENT_ROUTES=192.0.2.0/24,198.51.100.0/24"))
		_, err := ResolveProjectFrontDoor(repo, "fixture", cp, cp, "", nil)
		if err == nil || !strings.Contains(err.Error(), "inside INFRA_ATTACHMENT_ROUTES") {
			t.Fatalf("err=%v", err)
		}
	})
}

// The DNS component keeps the starter's Corefile and adds the zone answer.
func TestWriteProjectFrontDoorDNS(t *testing.T) {
	repo := t.TempDir()
	starter := filepath.Join(repo, "infrastructure", "kube-ovn-network")
	if err := os.MkdirAll(starter, 0o755); err != nil {
		t.Fatal(err)
	}
	vpcDNS := `apiVersion: v1
kind: ServiceAccount
metadata:
  name: vpc-dns
---
apiVersion: v1
kind: ConfigMap
metadata:
  name: vpc-dns-corefile
  namespace: kube-system
data:
  Corefile: |-
    .:53 {
        errors
        ready
        kubernetes cluster.local in-addr.arpa ip6.arpa {
          pods insecure
        }
        forward . /etc/resolv.conf {
          prefer_udp
        }
        cache 30
    }
`
	if err := os.WriteFile(filepath.Join(starter, "vpc-dns.yaml"), []byte(vpcDNS), 0o644); err != nil {
		t.Fatal(err)
	}
	rel, err := writeProjectFrontDoorDNS(repo, "fixture", frontDoorDNSOptions{})
	if err != nil {
		t.Fatal(err)
	}
	body, err := os.ReadFile(filepath.Join(repo, rel, "kustomization.yaml"))
	if err != nil {
		t.Fatal(err)
	}
	var comp struct {
		Kind    string `yaml:"kind"`
		Patches []struct {
			Patch string `yaml:"patch"`
		} `yaml:"patches"`
	}
	if err := yaml.Unmarshal(body, &comp); err != nil || comp.Kind != "Component" || len(comp.Patches) != 1 {
		t.Fatalf("component %s: %v", body, err)
	}
	var ops []struct {
		Value string `yaml:"value"`
	}
	if err := yaml.Unmarshal([]byte(comp.Patches[0].Patch), &ops); err != nil || len(ops) != 1 {
		t.Fatalf("patch: %v", err)
	}
	core := ops[0].Value
	for _, want := range []string{
		"kube-api.${DOMAIN}:53 {\n    errors\n    forward . /etc/resolv.conf {",
		"${DOMAIN}:53 {\n    errors\n    template IN A ${DOMAIN} {\n      answer \"{{ .Name }} 60 IN A ${ENVOY_GATEWAY_INTERNAL_VIP}\"\n    }\n    forward . /etc/resolv.conf {",
		".:53 {\n    errors\n    ready\n", "cache 30",
	} {
		if !strings.Contains(core, want) {
			t.Errorf("Corefile lacks %q:\n%s", want, core)
		}
	}
}

// An operator's internal API VIP stays the kube-api answer, and an S3
// hostname outside the domain gets the internal front door too.
func TestWriteProjectFrontDoorDNSKeepsAPIAndExternalS3(t *testing.T) {
	repo := t.TempDir()
	dir := filepath.Join(repo, "infrastructure", "kube-ovn-network")
	if err := os.MkdirAll(dir, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, "vpc-dns.yaml"), []byte("apiVersion: v1\nkind: ConfigMap\nmetadata:\n  name: vpc-dns-corefile\ndata:\n  Corefile: |-\n    .:53 {\n        errors\n        ready\n    }\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	rel, err := writeProjectFrontDoorDNS(repo, "fixture", frontDoorDNSOptions{KubeAPIInternal: true, ExtraS3Zone: "objects.example.net"})
	if err != nil {
		t.Fatal(err)
	}
	body, _ := os.ReadFile(filepath.Join(repo, rel, "kustomization.yaml"))
	for _, want := range []string{"${KUBE_API_INTERNAL_VIP} kube-api.${DOMAIN}", "objects.example.net:53 {", "template IN A objects.example.net {"} {
		if !strings.Contains(string(body), want) {
			t.Errorf("missing %q in %s", want, body)
		}
	}
}

// A starter Corefile that already maps names is refused, not overridden.
func TestWriteProjectFrontDoorDNSRefusesMappedCorefile(t *testing.T) {
	repo := t.TempDir()
	dir := filepath.Join(repo, "infrastructure", "kube-ovn-network")
	if err := os.MkdirAll(dir, 0o755); err != nil {
		t.Fatal(err)
	}
	cm := "apiVersion: v1\nkind: ConfigMap\nmetadata:\n  name: vpc-dns-corefile\ndata:\n  Corefile: |-\n    .:53 {\n        ready\n        hosts {\n          192.0.2.9 kube-api.example.com\n          fallthrough\n        }\n    }\n"
	if err := os.WriteFile(filepath.Join(dir, "vpc-dns.yaml"), []byte(cm), 0o644); err != nil {
		t.Fatal(err)
	}
	if _, err := writeProjectFrontDoorDNS(repo, "fixture", frontDoorDNSOptions{}); err == nil || !strings.Contains(err.Error(), `uses "hosts"`) {
		t.Fatalf("err=%v", err)
	}
}

func TestPlainVPCDNSCorefile(t *testing.T) {
	plain := ".:53 {\n    errors\n    ready\n    forward . /etc/resolv.conf {\n      prefer_udp\n    }\n    cache 30\n}"
	if err := plainVPCDNSCorefile(plain); err != nil {
		t.Fatalf("plain refused: %v", err)
	}
	for _, bad := range []string{
		strings.Replace(plain, "    ready\n", "    ready\n    forward kube-api.example.com 10.0.0.53\n", 1),
		strings.Replace(plain, "forward . /etc/resolv.conf", "forward . 10.0.0.53", 1),
		strings.Replace(plain, "    ready\n", "    ready\n    rewrite name a b\n", 1),
		"example.com:53 {\n    file db\n}\n" + plain,
		strings.Replace(plain, ".:53 {", ".:53 kube-api.example.com:53 {", 1),
		strings.Replace(plain, "forward . /etc/resolv.conf {", "forward . /etc/resolv.conf 10.0.0.53 {", 1),
		plain + "\n" + plain,
	} {
		if err := plainVPCDNSCorefile(bad); err == nil {
			t.Errorf("accepted:\n%s", bad)
		}
	}
}
