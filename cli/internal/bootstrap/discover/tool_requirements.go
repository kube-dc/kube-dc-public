package discover

import (
	"context"
	"fmt"
	"sort"

	"github.com/shalb/kube-dc/cli/internal/bootstrap/ports"
)

// ToolOperation identifies work that can require programs on the operator's
// machine. A profile can add requirements when qualified release metadata is
// available; this registry covers the commands used by the current scripts.
type ToolOperation string

const (
	ToolInit    ToolOperation = "init"
	ToolPrepare ToolOperation = "install-prerequisites"
	ToolVendor  ToolOperation = "vendor-manifests"
	ToolRender  ToolOperation = "render"
)

type ToolSelection struct {
	Operation           ToolOperation
	Push                bool
	CreateRepo          bool
	Provider            string
	SSH                 bool
	StandaloneKustomize bool
	Component           string
}

type ToolRequirement struct {
	Name        string
	Reason      string
	AutoInstall bool
}

// These are the binaries installed by the published Fleet prerequisite
// script. A missing program outside this list needs manual preparation until
// the release ships a verified installer for it.
var fleetScriptInstallable = map[string]bool{
	"kubectl": true, "flux": true, "sops": true, "age": true,
	"age-keygen": true, "helm": true, "kustomize": true,
	"yq": true, "clusterctl": true,
}

// ToolRequirements returns only programs invoked by the selected operation.
// In-process OCI acquisition and go-git need no external flux or git binary.
func ToolRequirements(s ToolSelection) ([]ToolRequirement, error) {
	required := map[string]string{}
	add := func(name, reason string) { required[name] = reason }
	switch s.Operation {
	case ToolInit:
		add("kubectl", "Fleet scripts and cluster checks")
		add("sops", "encrypt platform credentials")
		add("age", "create the SOPS age key")
		add("age-keygen", "generate the SOPS age keypair")
		if s.Push {
			add("flux", "bootstrap Flux from the Fleet repository")
			add("git", "Fleet bootstrap Git operations")
			add("helm", "install the first cluster charts")
		}
		if s.CreateRepo {
			if !s.Push {
				return nil, fmt.Errorf("create-repo requires push")
			}
			switch s.Provider {
			case "", "github":
				add("gh", "create a GitHub Fleet repository")
			case "gitlab":
				add("glab", "create a GitLab Fleet repository")
			default:
				return nil, fmt.Errorf("unknown repository provider %q", s.Provider)
			}
		}
		if s.SSH {
			add("ssh", "reach an SSH Git remote")
		}
	case ToolPrepare:
		add("curl", "download prerequisite packages")
		add("sudo", "install prerequisite packages")
	case ToolVendor:
		component := s.Component
		if component == "" {
			component = "all"
		}
		switch component {
		case "all":
			add("curl", "download upstream manifests")
			add("clusterctl", "render Cluster API providers")
			add("helm", "render chart resources")
			add("yq", "select rendered YAML resources")
		case "capi":
			add("curl", "download upstream manifests")
			add("clusterctl", "render Cluster API providers")
		case "crds":
			add("helm", "render chart resources")
			add("yq", "select rendered YAML resources")
		case "kubevirt", "kyverno", "sveltos", "local-path", "multus":
			add("curl", "download upstream manifests")
		default:
			return nil, fmt.Errorf("unknown vendor component %q", component)
		}
	case ToolRender:
		if s.StandaloneKustomize {
			add("kustomize", "render Fleet overlays without kubectl")
		} else {
			add("kubectl", "render Fleet Kustomize overlays")
		}
	default:
		return nil, fmt.Errorf("unknown tool operation %q", s.Operation)
	}
	names := make([]string, 0, len(required))
	for name := range required {
		names = append(names, name)
	}
	sort.Strings(names)
	out := make([]ToolRequirement, 0, len(names))
	for _, name := range names {
		out = append(out, ToolRequirement{Name: name, Reason: required[name], AutoInstall: fleetScriptInstallable[name]})
	}
	return out, nil
}

// ToolProbesFor selects the same probes for read-only readiness and for
// InstallPrereqs. The caller still decides whether to install anything.
func ToolProbesFor(s ToolSelection) ([]ports.Probe, error) {
	reqs, err := ToolRequirements(s)
	if err != nil {
		return nil, err
	}
	probes := make([]ports.Probe, 0, len(reqs))
	for _, req := range reqs {
		probe, ok := toolProbeByName(req.Name)
		if !ok {
			return nil, fmt.Errorf("no probe for required tool %q", req.Name)
		}
		probes = append(probes, probe)
	}
	return probes, nil
}

type ToolCheck struct {
	Requirement ToolRequirement
	Result      ports.Result
}

func CheckRequiredTools(ctx context.Context, s ToolSelection) ([]ToolCheck, error) {
	reqs, err := ToolRequirements(s)
	if err != nil {
		return nil, err
	}
	probes, err := ToolProbesFor(s)
	if err != nil {
		return nil, err
	}
	checks := make([]ToolCheck, 0, len(probes))
	for i, probe := range probes {
		if err := ctx.Err(); err != nil {
			return nil, err
		}
		checks = append(checks, ToolCheck{Requirement: reqs[i], Result: probe.Run(ctx)})
	}
	return checks, nil
}

func toolProbeByName(name string) (ports.Probe, bool) {
	switch name {
	case "kubectl":
		return newKubectlProbe(realExec), true
	case "flux":
		return newFluxProbe(realExec), true
	case "sops":
		return newSOPSProbe(realExec), true
	case "age":
		return newAgeProbe(realExec), true
	case "age-keygen":
		return newSimpleVersionProbe("age-keygen", realExec, "-version"), true
	case "git":
		return newGitProbe(realExec), true
	case "gh":
		// Repo creation can use an explicit token. The generic doctor's
		// gh-auth check is a separate credential check, not a binary need.
		return newSimpleVersionProbe("gh", realExec, "--version"), true
	case "ssh":
		return newSSHProbe(realExec), true
	case "helm":
		return newSimpleVersionProbe("helm", realExec, "version", "--short"), true
	case "glab":
		return newSimpleVersionProbe("glab", realExec, "version"), true
	case "yq":
		return newSimpleVersionProbe("yq", realExec, "--version"), true
	case "clusterctl":
		return newSimpleVersionProbe("clusterctl", realExec, "version"), true
	case "curl":
		return newSimpleVersionProbe("curl", realExec, "--version"), true
	case "sudo":
		return newSimpleVersionProbe("sudo", realExec, "--version"), true
	case "kustomize":
		return newSimpleVersionProbe("kustomize", realExec, "version"), true
	default:
		return nil, false
	}
}
