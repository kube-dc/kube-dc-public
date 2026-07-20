package discover

import (
	"context"

	"github.com/shalb/kube-dc/cli/internal/bootstrap/ports"
)

// InfraSubnetProvider is the narrow slice of K8sClient this probe needs.
type InfraSubnetProvider interface {
	GetResourceFieldFirst(ctx context.Context, group, version, resource, namespace, name string, fields ...string) (string, error)
}

// InfraAttachProbe reports whether the Tenant Networking v2 infra Subnet exists.
//
// Worth a dedicated doctor row because its absence is invisible until it is
// expensive. The subnet is what a dual-homed pod's eth0 attaches to, and the pod
// webhook's readiness check fails CLOSED without it — so on a cluster whose
// namespaces are labelled, a missing subnet means every pod CREATE in them is
// rejected, with the only clue buried in an admission error.
//
// The manager guards against the ordering (it will not label a namespace until
// the subnet is Ready), so the realistic failure this catches is the other one:
// a cluster installed with dual-homing configured but the subnet never applied —
// Flux stuck on tenant-net-v2, or an operator who enabled the feature by hand
// without adding the Kustomization. That cluster looks healthy and silently
// never dual-homes.
type InfraAttachProbe struct {
	k8s        InfraSubnetProvider
	subnetName string
}

// NewInfraAttachProbe constructs the probe. subnetName defaults to infra-net,
// matching INFRA_ATTACHMENT_SUBNET's universal default.
func NewInfraAttachProbe(k8s InfraSubnetProvider, subnetName string) *InfraAttachProbe {
	if subnetName == "" {
		subnetName = "infra-net"
	}
	return &InfraAttachProbe{k8s: k8s, subnetName: subnetName}
}

var _ ports.Probe = (*InfraAttachProbe)(nil)

func (p *InfraAttachProbe) Name() string { return "tenant-net-v2" }

// Run reads the Subnet's CIDR as an existence signal.
//
// Severity ladder:
//   - not configured  -> Missing/Warn (internal wiring)
//   - lookup error    -> Missing/Warn, never Blocker: doctor must not fail the
//     whole run because one CR read failed
//   - absent          -> Missing/Warn. Warn rather than Blocker because a
//     cluster deliberately installed without dual-homing is legitimate; the
//     detail says what it means so the operator can tell the two apart.
//   - present         -> Installed/Info, detail carries the CIDR
func (p *InfraAttachProbe) Run(ctx context.Context) ports.Result {
	if p.k8s == nil {
		return ports.Result{
			Status:   ports.StatusMissing,
			Severity: ports.SeverityWarn,
			Detail:   "cluster client not configured (internal wiring bug)",
		}
	}
	cidr, err := p.k8s.GetResourceFieldFirst(ctx,
		"kubeovn.io", "v1", "subnets", "", p.subnetName, "spec.cidrBlock")
	if err != nil {
		return ports.Result{
			Status:   ports.StatusMissing,
			Severity: ports.SeverityWarn,
			Detail:   "could not read Subnet/" + p.subnetName + ": " + err.Error(),
		}
	}
	if cidr == "" {
		return ports.Result{
			Status:   ports.StatusMissing,
			Severity: ports.SeverityWarn,
			Detail: "Subnet/" + p.subnetName + " absent — Tenant Networking v2 is not active. " +
				"Projects fall back to the legacy -ext alias path. If this cluster is meant to be " +
				"dual-homed, check the tenant-net-v2 Kustomization reconciled.",
		}
	}
	return ports.Result{
		Status:   ports.StatusInstalled,
		Severity: ports.SeverityInfo,
		Detail:   "Subnet/" + p.subnetName + " present (" + cidr + ") — dual-homing active",
	}
}
