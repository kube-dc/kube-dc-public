package main

import (
	"context"
	"fmt"
	"k8s.io/client-go/kubernetes"
	"k8s.io/client-go/tools/clientcmd"
	"net/url"
	"strings"
	"time"

	sshadapter "github.com/shalb/kube-dc/cli/internal/bootstrap/adapters/ssh"
	"github.com/shalb/kube-dc/cli/internal/bootstrap/clusterinit"
	"github.com/shalb/kube-dc/cli/internal/bootstrap/ports"
	"github.com/shalb/kube-dc/cli/internal/bootstrap/setup"
	"github.com/shalb/kube-dc/cli/internal/bootstrap/tui/screens/initform"
)

func bootstrapPanelServices(ctx context.Context) initform.PanelServices {
	return initform.PanelServices{Context: ctx, InspectProviderStorage: inspectPanelProviderStorage, Discover: func(ctx context.Context, request setup.ResourceRequest) (setup.HostResources, error) {
		// Strict known_hosts, no key enrollment. Construction and IO are deferred
		// until the operator explicitly requests discovery from the form.
		return setup.DiscoverResources(ctx, request, sshadapter.New())
	}}
}

func initSSHHost(o *clusterinit.InitOptions) ports.SSHHost {
	h := parseSSHHostArg(o.SSHHost)
	h.ExpectedHostKeySHA256 = o.SSHHostKeySHA256
	return h
}

// Existing-cluster suggestions must refer to the selected cluster. Never use
// an unrelated current kubeconfig to fill a new cluster's network fields.
func panelProbeForTarget(ctx context.Context, o *clusterinit.InitOptions) *initform.ProbePrefill {
	if _, matches := clusterinit.KubeconfigTargetsNamedCluster(o.Name, o.Domain, o.NodeExternalIP, controlPlaneAddrs(o)); !matches {
		return nil
	}
	return gatherPanelProbe(ctx)
}

// Inspect one loaded context only after the operator requests the read. Class
// configuration is advisory; this does not qualify raw-block provisioning.
func inspectPanelProviderStorage(ctx context.Context, o clusterinit.InitOptions) (setup.ProviderStorageObservation, error) {
	var empty setup.ProviderStorageObservation
	raw, err := clientcmd.NewDefaultClientConfigLoadingRules().Load()
	if err != nil {
		return empty, fmt.Errorf("cannot load the selected target kubeconfig")
	}
	entry := raw.Contexts[raw.CurrentContext]
	if entry == nil || raw.Clusters[entry.Cluster] == nil {
		return empty, fmt.Errorf("select the target kubeconfig context first")
	}
	cluster := raw.Clusters[entry.Cluster]
	endpoint, parseErr := url.Parse(cluster.Server)
	if parseErr != nil || endpoint.Scheme != "https" || endpoint.User != nil || cluster.InsecureSkipTLSVerify || !clusterinit.KubeconfigServerMatchesTarget(cluster.Server, raw.CurrentContext, o.Name, o.Domain, o.NodeExternalIP, strings.Split(o.Sets["KUBE_OVN_MASTER_NODES"], ",")) {
		return empty, fmt.Errorf("StorageClass inspection needs a verified TLS context for the selected target")
	}
	cfg, err := clientcmd.NewNonInteractiveClientConfig(*raw, raw.CurrentContext, &clientcmd.ConfigOverrides{}, nil).ClientConfig()
	if err != nil {
		return empty, fmt.Errorf("cannot construct the selected target client")
	}
	cfg.Timeout = 5 * time.Second
	client, err := kubernetes.NewForConfig(cfg)
	if err != nil {
		return empty, fmt.Errorf("cannot construct the selected target client")
	}
	return setup.InspectProviderStorage(ctx, client, o.CephStorageClass)
}
