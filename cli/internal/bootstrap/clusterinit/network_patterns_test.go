package clusterinit

import (
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"reflect"
	"regexp"
	"strings"
	"testing"

	"gopkg.in/yaml.v3"
)

// Render the exact shared Fleet manifests without contacting a cluster or
// changing the checkout. This catches patch targets that match only after
// env substitution, which Flux performs after Kustomize.
func TestNetworkPatternsRenderAgainstFleet(t *testing.T) {
	fleet := os.Getenv("KUBE_DC_TEST_FLEET")
	if fleet == "" {
		t.Skip("set KUBE_DC_TEST_FLEET for the source render matrix")
	}
	kustomize, err := exec.LookPath("kustomize")
	if err != nil {
		t.Fatal(err)
	}
	for _, tc := range []struct {
		name               string
		preset             Preset
		vlan, iface, layer string
	}{
		{"dedicated-untagged", PresetCloudVLAN, "0", "ens5", "none"},
		{"tagged-cloud", PresetCloudVLAN, "200", "bond0", "metallb-l2"},
		{"shared-public", PresetCloudPublicVLAN, "200", "eno1", "none"},
		{"bond-public-l2", PresetCloudPublicVLAN, "200", "bond0", "metallb-l2"},
		{"public-bgp-vip", PresetCloudPublicVLAN, "200", "bond0", "metallb-bgp"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			root := t.TempDir()
			for _, dir := range []string{"kube-ovn-network", "kube-ovn-network-public"} {
				if err := os.CopyFS(filepath.Join(root, dir), os.DirFS(filepath.Join(fleet, "infrastructure", dir))); err != nil {
					t.Fatal(err)
				}
			}
			o := validBase()
			o.Preset, o.IngressAddressLayer = tc.preset, tc.layer
			o.Sets = map[string]string{"EXT_NET_INTERFACE": tc.iface, "EXT_NET_VLAN_ID": tc.vlan, "KUBE_OVN_MASTER_NODES": "192.0.2.10", "KUBE_OVN_GW_NODES": "server-1,server-2", "EXT_NET_EXCLUDE_IPS": "100.65.0.1..100.65.0.20"}
			if tc.preset == PresetCloudPublicVLAN {
				for k, v := range map[string]string{"EXT_PUBLIC_VLAN_ID": "300", "EXT_PUBLIC_CIDR": "198.51.100.0/24", "EXT_PUBLIC_GATEWAY": "198.51.100.1", "EXT_PUBLIC_EXCLUDE_IPS_1": "198.51.100.1..198.51.100.12", "EXT_PUBLIC_EXCLUDE_IPS_2": "198.51.100.13"} {
					o.Sets[k] = v
				}
			}
			if tc.layer != "none" {
				o.Sets["METALLB_FLOATING_IP"] = "198.51.100.10"
			}
			if tc.layer == "metallb-l2" {
				o.Sets["METALLB_INTERFACE"] = "ext-pub-anchor"
				if tc.preset == PresetCloudVLAN {
					o.Sets["METALLB_INTERFACE"] = "eno2"
				}
			}
			if tc.layer == "metallb-bgp" {
				o.Sets["METALLB_BGP_LOCAL_ASN"], o.Sets["METALLB_BGP_PEER_ASN"], o.Sets["METALLB_BGP_PEER_ADDRESS"] = "64512", "64513", "192.0.2.1"
			}
			if err := o.Validate(); err != nil {
				t.Fatal(err)
			}
			env, err := ResolvedEnvFor(&o)
			if err != nil {
				t.Fatal(err)
			}
			patch, err := BuildCustomInterfacesPatch(map[string]string{"server-2": "ens4", "worker-1": "ens3"})
			if err != nil {
				t.Fatal(err)
			}
			infraPath := filepath.Join(root, "infrastructure.yaml")
			if err := os.WriteFile(infraPath, []byte("apiVersion: kustomize.toolkit.fluxcd.io/v1\nkind: Kustomization\nmetadata: {name: infra-core}\nspec: {path: ./infrastructure/core}\n"), 0600); err != nil {
				t.Fatal(err)
			}
			if err := WriteCustomInterfacesPatch(infraPath, map[string]string{"server-2": "ens4", "worker-1": "ens3"}); err != nil {
				t.Fatal(err)
			}
			written, err := os.ReadFile(infraPath)
			if err != nil {
				t.Fatal(err)
			}
			var actual, helper map[string]any
			if err := yaml.Unmarshal(written, &actual); err != nil {
				t.Fatal(err)
			}
			if err := yaml.Unmarshal([]byte(patch), &helper); err != nil {
				t.Fatal(err)
			}
			if !reflect.DeepEqual(actual["spec"].(map[string]any)["patches"], helper["patches"]) {
				t.Fatal("scaffold writer and patch helper diverged")
			}
			body := "apiVersion: kustomize.config.k8s.io/v1beta1\nkind: Kustomization\nresources:\n  - kube-ovn-network\n"
			if PresetHasPublicNetwork(tc.preset) {
				body += "  - kube-ovn-network-public\n"
			}
			body += patch
			if err := os.WriteFile(filepath.Join(root, "kustomization.yaml"), []byte(body), 0600); err != nil {
				t.Fatal(err)
			}
			out, err := exec.Command(kustomize, "build", root).CombinedOutput()
			if err != nil {
				t.Fatalf("render: %v\n%s", err, out)
			}
			re := regexp.MustCompile(`\$\{([A-Z0-9_]+)(?::[-=]([^}]*))?\}`)
			rendered := re.ReplaceAllStringFunc(string(out), func(s string) string {
				parts := re.FindStringSubmatch(s)
				if value, ok := env[parts[1]]; ok {
					return value
				}
				return parts[2]
			})
			objects := map[string]map[string]any{}
			dec := yaml.NewDecoder(strings.NewReader(rendered))
			for {
				var obj map[string]any
				err := dec.Decode(&obj)
				if err == io.EOF {
					break
				}
				if err != nil {
					t.Fatal(err)
				}
				meta := obj["metadata"].(map[string]any)
				objects[obj["kind"].(string)+"/"+meta["name"].(string)] = obj
			}
			pn := objects["ProviderNetwork/ext-cloud"]["spec"].(map[string]any)
			mappings, _ := pn["customInterfaces"].([]any)
			if pn["defaultInterface"] != tc.iface || len(mappings) != 2 {
				t.Fatal("heterogeneous NIC mappings did not reach ProviderNetwork")
			}
			vlan := objects["Vlan/vlan"+tc.vlan]["spec"].(map[string]any)
			if vlan["provider"] != "ext-cloud" {
				t.Fatal("wrong VLAN provider")
			}
			_, public := objects["Subnet/ext-public"]
			if public != PresetHasPublicNetwork(tc.preset) {
				t.Fatal("wrong public network composition")
			}
			if tc.layer == "none" && env["ENVOY_SERVICE_TYPE"] != "ClusterIP" || tc.layer != "none" && env["ENVOY_SERVICE_TYPE"] != "LoadBalancer" {
				t.Fatal("wrong independent platform access shape")
			}
		})
	}
}
