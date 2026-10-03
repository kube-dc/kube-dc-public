package main

import (
	"context"

	sshadapter "github.com/shalb/kube-dc/cli/internal/bootstrap/adapters/ssh"
	"github.com/shalb/kube-dc/cli/internal/bootstrap/clusterinit"
	"github.com/shalb/kube-dc/cli/internal/bootstrap/ports"
	"github.com/shalb/kube-dc/cli/internal/bootstrap/setup"
	"github.com/shalb/kube-dc/cli/internal/bootstrap/tui/screens/initform"
)

func bootstrapPanelServices(ctx context.Context) initform.PanelServices {
	return initform.PanelServices{Context: ctx, Discover: func(ctx context.Context, request setup.ResourceRequest) (setup.HostResources, error) {
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
	if _, matches := clusterinit.KubeconfigTargetsCluster(o.Domain, o.NodeExternalIP); !matches {
		return nil
	}
	return gatherPanelProbe(ctx)
}
