package main

import (
	"context"
	"os"
	"path/filepath"
	"testing"

	"github.com/shalb/kube-dc/cli/internal/bootstrap/clusterinit"
	"k8s.io/client-go/tools/clientcmd"
	clientcmdapi "k8s.io/client-go/tools/clientcmd/api"
)

func TestBootstrapSiteInputsKeepsPrivateSiteShapeAndRepoPrecedence(t *testing.T) {
	path := filepath.Join("..", "..", "internal", "bootstrap", "clusterinit", "testdata", "variants", "ten-host-bond.env")
	for _, explicit := range []bool{false, true} {
		o, err := bootstrapSiteInputs(path, "/tmp/explicit-fleet", explicit)
		if err != nil {
			t.Fatal(err)
		}
		wantRepo := "/tmp/example-fleet"
		if explicit {
			wantRepo = "/tmp/explicit-fleet"
		}
		if o.Repo != wantRepo || len(o.CephNodes) != 10 || o.ProviderHost != "gitlab.example.test" || o.Sets["CEPH_REPLICATION_SIZE"] != "3" || o.Sets["EXT_NET_NODE_EGRESS_ENABLED"] != "false" || o.SiteComponents == "" {
			t.Fatal("site inputs changed while opening the root form")
		}
	}
	path = filepath.Join(t.TempDir(), "ambiguous.env")
	if err := os.WriteFile(path, []byte("CEPH_NODE_1=one\nCEPH_NODE_1_DEVICE=sdb\nCEPH_NODE_2=one\nCEPH_NODE_2_DEVICE=sdc\n"), 0600); err != nil {
		t.Fatal(err)
	}
	if _, err := bootstrapSiteInputs(path, "", false); err == nil {
		t.Fatal("root import silently dropped a disk")
	}
}

func TestProviderInspectorRefusesUnrelatedOrUnverifiedContext(t *testing.T) {
	for _, variant := range []string{"unrelated", "insecure", "http", "userinfo"} {
		path := filepath.Join(t.TempDir(), "config")
		raw := clientcmdapi.NewConfig()
		raw.CurrentContext = "other"
		raw.Contexts["other"] = &clientcmdapi.Context{Cluster: "cluster"}
		raw.Clusters["cluster"] = &clientcmdapi.Cluster{Server: "https://unrelated.example.test:6443", InsecureSkipTLSVerify: variant == "insecure"}
		if variant == "insecure" {
			raw.Clusters["cluster"].Server = "https://kube-api.example.test:6443"
		}
		if variant == "http" {
			raw.Clusters["cluster"].Server = "http://kube-api.example.test:6443"
		}
		if variant == "userinfo" {
			raw.Clusters["cluster"].Server = "https://user@kube-api.example.test:6443"
		}
		if err := clientcmd.WriteToFile(*raw, path); err != nil {
			t.Fatal(err)
		}
		t.Setenv("KUBECONFIG", path)
		_, err := inspectPanelProviderStorage(context.Background(), clusterinit.InitOptions{Name: "site", Domain: "example.test", CephStorageClass: "class"})
		if err == nil {
			t.Fatal("inspection accepted an unrelated or unverified context")
		}
	}
}
