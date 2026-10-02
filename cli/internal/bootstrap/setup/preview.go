package setup

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"sort"

	"github.com/shalb/kube-dc/cli/internal/bootstrap/clusterinit"
)

const PreviewSchemaVersion = 1

// StagePreview describes an operation in the proposed guided sequence.
// Scopes name places the operation can read or change. PotentialWrite marks
// possible changes; a condition means live discovery can skip the operation.
type StagePreview struct {
	ID             string   `json:"id"`
	Title          string   `json:"title"`
	Scopes         []string `json:"scopes"`
	Target         string   `json:"target,omitempty"`
	PotentialWrite bool     `json:"potentialWrite"`
	Condition      string   `json:"condition,omitempty"`
}

// Preview is a stable local review artifact. It is not an apply authorization:
// target identity, host readiness, release qualification, and exact diffs need
// live checks that the local compiler cannot perform.
type Preview struct {
	SchemaVersion int            `json:"schemaVersion"`
	Operation     string         `json:"operation"`
	State         string         `json:"state"`
	ReadyToApply  bool           `json:"readyToApply"`
	Cluster       string         `json:"cluster"`
	Profile       string         `json:"profile"`
	Target        Target         `json:"target"`
	StarterRef    string         `json:"starterRef"`
	RKE2Version   string         `json:"rke2Version"`
	ReleaseSHA256 string         `json:"releaseSHA256"`
	InputHash     string         `json:"inputHash"`
	Stages        []StagePreview `json:"stages"`
	Unresolved    []string       `json:"unresolved"`
	PlanHash      string         `json:"planHash"`
}

// BuildPreview derives host and platform stages from one compiled spec. It
// reads no live state and does not contact hosts, a registry, or a cluster.
func BuildPreview(c Compiled) (Preview, error) {
	if err := checkCompiled(c); err != nil {
		return Preview{}, err
	}
	spec, o := c.Spec, c.Init
	p := Preview{
		SchemaVersion: PreviewSchemaVersion,
		Operation:     "plan",
		State:         "planned",
		ReadyToApply:  false,
		Cluster:       spec.Name,
		Profile:       spec.Profile,
		Target:        spec.Target,
		StarterRef:    spec.Release.StarterRef,
		RKE2Version:   spec.Release.RKE2Version,
		ReleaseSHA256: c.ReleaseSHA256,
		InputHash:     c.InputHash,
		Unresolved: []string{
			"qualified-release-and-profile",
			"live-target-identity",
			"git-remote-and-branch",
			"host-readiness-and-device-identity",
			"exact-fleet-and-cluster-diff",
			"verification-resource-ownership",
		},
	}

	// Stage construction follows the proposed guided sequence below.
	return buildPreviewStages(p, spec, o)
}

func checkCompiled(c Compiled) error {
	if c.InputHash == "" || c.ReleaseSHA256 == "" {
		return fmt.Errorf("compile the setup specification before planning")
	}
	seal, err := sealCompiled(c)
	if err != nil {
		return err
	}
	if c.seal == "" || seal != c.seal {
		return fmt.Errorf("compiled setup inputs changed; validate the specification again")
	}
	return nil
}

func buildPreviewStages(p Preview, spec Spec, o clusterinit.InitOptions) (Preview, error) {
	hostStages := func() {
		if spec.Target.Intent != clusterinit.ModeInstall && spec.Target.Intent != clusterinit.ModeAuto {
			return
		}
		condition := ""
		if spec.Target.Intent == clusterinit.ModeAuto {
			condition = "if the live target is a new cluster"
		}
		primary, servers, workers := orderedHostPhase(spec.Hosts)
		p.Stages = append(p.Stages, StagePreview{ID: "rke2-first-server", Title: "Install the first RKE2 server", Scopes: []string{"host"}, Target: primary.ID, PotentialWrite: true, Condition: condition})
		for _, h := range append(servers, workers...) {
			title := "Join an RKE2 worker"
			if h.Role == "server" {
				title = "Join an RKE2 server"
			}
			p.Stages = append(p.Stages, StagePreview{ID: "rke2-join-" + h.ID, Title: title, Scopes: []string{"host"}, Target: h.ID, PotentialWrite: true, Condition: condition})
		}
	}
	ssh := o.SSHHost != "" && !o.NoSSH
	gpu := o.GPU()
	steps := clusterinit.InstallSteps(clusterinit.InstallStepInputs{
		NoInstallPrereqs: o.NoInstallPrereqs,
		Starter:          o.FleetMode != clusterinit.FleetExistingFleet,
		Adopt:            o.Mode == clusterinit.ModeAdopt || o.Mode == clusterinit.ModeAuto,
		SSH:              ssh,
		StorageDevCheck:  ssh && (o.Mode == clusterinit.ModeInstall || o.Mode == clusterinit.ModeAuto) && len(o.ObjectStorage().RawOSDDevices()) > 0,
		NewRepoCreate:    o.FleetMode == clusterinit.FleetNewRepo && !o.NoCreateRepo && !o.NoPush,
		NewRepoRemote:    o.FleetMode == clusterinit.FleetNewRepo && !o.NoPush,
		NoPush:           o.NoPush,
		Finalize:         !o.NoPush,
		GPUEnabled:       gpu.Platform == clusterinit.GPUPlatformEnabled,
		HAMiEnabled:      gpu.HAMiEnabled,
	})
	for _, step := range steps {
		if step.ID == clusterinit.StepDNS {
			// The guided coordinator prepares the workstation, installs or
			// verifies hosts, and gets cluster access before platform checks.
			hostStages()
			if ssh {
				p.Stages = append(p.Stages, StagePreview{ID: string(clusterinit.StepFetchKubeconfig), Title: "Fetch kubeconfig", Scopes: []string{"workstation"}, Target: spec.Name, PotentialWrite: true})
			}
		}
		if step.ID == clusterinit.StepFetchKubeconfig {
			continue
		}
		scopes, writes, err := previewStepEffect(step.ID)
		if err != nil {
			return Preview{}, err
		}
		stage := StagePreview{ID: string(step.ID), Title: step.Title, Scopes: scopes, PotentialWrite: writes}
		if step.ID == clusterinit.StepStarter || step.ID == clusterinit.StepScaffold || step.ID == clusterinit.StepRemote {
			stage.Target = o.Repo
		} else if hasScope(scopes, "cluster") || hasScope(scopes, "identity") {
			stage.Target = spec.Name
		}
		switch step.ID {
		case clusterinit.StepStarter:
			stage.Condition = "if shared Fleet files are absent, or their Git checkout needs repair"
		case clusterinit.StepInstallPrereqs:
			stage.Condition = "if selected tools are missing"
		case clusterinit.StepCreateRepo:
			stage.Condition = "if the remote repository is absent"
		case clusterinit.StepIngressNodes:
			if o.NoPush {
				stage.PotentialWrite = false
				stage.Condition = "skipped because push is disabled"
			}
		case clusterinit.StepCommitPush:
			if o.NoPush {
				stage.Scopes = []string{"fleet", "git-local"}
			}
		case clusterinit.StepAdoptGate:
			if spec.Target.Intent == clusterinit.ModeAuto {
				stage.Condition = "if the live target is adopted"
			}
		case clusterinit.StepStorageDev:
			if spec.Target.Intent == clusterinit.ModeAuto {
				stage.Condition = "if the live target is a new cluster"
			}
		}
		p.Stages = append(p.Stages, stage)
	}
	capabilities := append([]string(nil), spec.Verification.Capabilities...)
	sort.Strings(capabilities)
	for _, capability := range capabilities {
		p.Stages = append(p.Stages, StagePreview{
			ID: "verify-" + capability, Title: "Verify " + capability + " and clean up temporary resources",
			Scopes: []string{"cluster", "verification"}, Target: spec.Verification.Namespace, PotentialWrite: true,
		})
	}
	data, err := json.Marshal(p)
	if err != nil {
		return Preview{}, err
	}
	digest := sha256.Sum256(data)
	p.PlanHash = hex.EncodeToString(digest[:])
	return p, nil
}

func hasScope(scopes []string, want string) bool {
	for _, scope := range scopes {
		if scope == want {
			return true
		}
	}
	return false
}

func previewStepEffect(id clusterinit.StepID) (scopes []string, writes bool, err error) {
	switch id {
	case clusterinit.StepPrepare:
		return []string{"workstation"}, false, nil
	case clusterinit.StepDNS:
		return []string{"network"}, false, nil
	case clusterinit.StepKubeVirt, clusterinit.StepReconcile,
		clusterinit.StepGPUOperator, clusterinit.StepGPUHAMi,
		clusterinit.StepGPUProduct:
		return []string{"cluster"}, false, nil
	case clusterinit.StepAdoptGate:
		return []string{"cluster", "fleet"}, false, nil
	case clusterinit.StepNATProbe, clusterinit.StepEgressGW, clusterinit.StepStorageDev:
		return []string{"host", "network"}, false, nil
	case clusterinit.StepStarter:
		return []string{"workstation", "fleet", "git-local"}, true, nil
	case clusterinit.StepInstallPrereqs:
		return []string{"workstation"}, true, nil
	case clusterinit.StepRemote:
		return []string{"fleet", "git-local"}, true, nil
	case clusterinit.StepScaffold:
		return []string{"fleet"}, true, nil
	case clusterinit.StepFetchKubeconfig:
		return []string{"workstation"}, true, nil
	case clusterinit.StepCreateRepo:
		return []string{"git-provider"}, true, nil
	case clusterinit.StepCommitPush:
		return []string{"fleet", "git-local", "git-provider"}, true, nil
	case clusterinit.StepIngressNodes, clusterinit.StepGPUInventory:
		return []string{"cluster"}, true, nil
	case clusterinit.StepFluxInstall:
		return []string{"cluster", "fleet", "git-local", "git-provider"}, true, nil
	case clusterinit.StepBreakGlass, clusterinit.StepOpenBao:
		return []string{"cluster", "workstation", "fleet", "git-local", "git-provider"}, true, nil
	case clusterinit.StepKeycloakOIDC:
		return []string{"identity", "cluster", "fleet", "git-local", "git-provider"}, true, nil
	case clusterinit.StepOIDCCutover:
		return []string{"host"}, true, nil
	default:
		return nil, false, fmt.Errorf("setup preview has no effect classification for step %q", id)
	}
}
