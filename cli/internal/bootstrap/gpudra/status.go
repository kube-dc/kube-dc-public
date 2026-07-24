// Package gpudra evaluates the fail-closed DRA support and migration gates.
// Collection is a separate read-only adapter so these rules stay unit-testable.
package gpudra

import (
	"fmt"
	"regexp"
	"sort"
	"strconv"
	"strings"
	"time"

	"github.com/shalb/kube-dc/cli/internal/bootstrap/ports"
)

const DefaultDriverDigest = "sha256:ae7e844acbd099a109bfa84296905ea941eed292a7991c06e2eeee443457ede3"

type Check struct {
	Name    string `json:"name"`
	Pass    bool   `json:"pass"`
	Message string `json:"message"`
}

type Report struct {
	Ready  bool    `json:"ready"`
	Checks []Check `json:"checks"`
}

// DriverRecoveryPlan is read-only. It identifies one exact stale
// DaemonSet-owned kubelet-plugin Pod and the checks that make a serialized
// manual delete safe; it never deletes the Pod itself.
type DriverRecoveryPlan struct {
	Eligible  bool                   `json:"eligible"`
	Checks    []Check                `json:"checks"`
	Candidate *ports.GPUDRADriverPod `json:"candidate,omitempty"`
}

type Options struct {
	Now            time.Time
	MaxCanaryAge   time.Duration
	RequiredDigest string
	MigrationPlan  bool
	RollbackPlan   bool
}

func Evaluate(status ports.GPUDRAStatus, options Options) Report {
	if options.Now.IsZero() {
		options.Now = time.Now()
	}
	if options.MaxCanaryAge <= 0 {
		options.MaxCanaryAge = 10 * time.Minute
	}
	checks := []Check{
		check("kubernetes-version", kubernetesAtLeast136(status.ServerVersion), fmt.Sprintf("server=%s; require Kubernetes 1.36+", status.ServerVersion)),
		check("stable-dra-api", status.StableAPI, "resource.k8s.io/v1 is discoverable"),
		check("device-class", status.DeviceClassPresent, fmt.Sprintf("DeviceClass=%s", status.DeviceClass)),
		check("resource-slices", status.ResourceSlices > 0 && status.Devices > 0, fmt.Sprintf("slices=%d devices=%d", status.ResourceSlices, status.Devices)),
		check("consumable-capacity", status.ConsumableCapacity && status.ShareableDevices == status.Devices && status.Devices > 0, fmt.Sprintf("shareable=%d/%d", status.ShareableDevices, status.Devices)),
		check("driver-ready", status.DriverDesired > 0 && status.DriverReady == status.DriverDesired, fmt.Sprintf("ready=%d desired=%d", status.DriverReady, status.DriverDesired)),
		check("driver-digest", imagesPinned(status.DriverImages, options.RequiredDigest), strings.Join(status.DriverImages, ",")),
		check("single-allocator-owner", len(status.AllocatorOwners) == 1 && strings.Contains(status.AllocatorOwners[0], "hami-dra"), strings.Join(status.AllocatorOwners, ",")),
		check("dra-node-ownership", len(status.DRANodes) > 0 && len(status.WrongModeNodes) == 0, fmt.Sprintf("nodes=%s wrong-mode=%s", strings.Join(status.DRANodes, ","), strings.Join(status.WrongModeNodes, ","))),
		canaryCheck(status, options),
	}
	if options.MigrationPlan || options.RollbackPlan {
		checks = append(checks,
			check("creation-gates-closed", !status.SharedCreationEnabled && !status.VMCreationEnabled, fmt.Sprintf("shared=%t vm=%t", status.SharedCreationEnabled, status.VMCreationEnabled)),
		)
	}
	if options.MigrationPlan {
		checks = append(checks,
			check("zero-legacy-holders", len(status.LegacyHolders) == 0, holderMessage(status.LegacyHolders)),
		)
	}
	if options.RollbackPlan {
		checks = append(checks,
			check("zero-dra-holders", len(status.DRAHolders) == 0 && status.GPUClaims == 0, fmt.Sprintf("holders=%s gpuClaims=%d", holderMessage(status.DRAHolders), status.GPUClaims)),
		)
	}
	report := Report{Ready: true, Checks: checks}
	for _, item := range checks {
		if !item.Pass {
			report.Ready = false
		}
	}
	return report
}

func PlanDriverRecovery(status ports.GPUDRAStatus, now time.Time, minUnreadyAge time.Duration) DriverRecoveryPlan {
	if now.IsZero() {
		now = time.Now()
	}
	if minUnreadyAge <= 0 {
		minUnreadyAge = 10 * time.Minute
	}
	candidates := make([]ports.GPUDRADriverPod, 0, len(status.DriverPods))
	for _, pod := range status.DriverPods {
		age := now.Sub(pod.ReadyLastTransitionTime)
		if !pod.Ready && !pod.Deleting && pod.NodeReady && !pod.ReadyLastTransitionTime.IsZero() && age >= minUnreadyAge {
			candidates = append(candidates, pod)
		}
	}
	sort.Slice(candidates, func(i, j int) bool {
		if candidates[i].ReadyLastTransitionTime.Equal(candidates[j].ReadyLastTransitionTime) {
			return candidates[i].Namespace+"/"+candidates[i].Name < candidates[j].Namespace+"/"+candidates[j].Name
		}
		return candidates[i].ReadyLastTransitionTime.Before(candidates[j].ReadyLastTransitionTime)
	})
	checks := []Check{
		check("driver-degraded", status.DriverDesired > 0 && status.DriverReady < status.DriverDesired,
			fmt.Sprintf("ready=%d desired=%d", status.DriverReady, status.DriverDesired)),
		check("inventory-empty", status.ResourceSlices == 0 && status.Devices == 0,
			fmt.Sprintf("slices=%d devices=%d", status.ResourceSlices, status.Devices)),
		check("stale-driver-pod", len(candidates) > 0,
			fmt.Sprintf("eligible=%d observed=%d minAge=%s", len(candidates), len(status.DriverPods), minUnreadyAge)),
	}
	plan := DriverRecoveryPlan{Eligible: true, Checks: checks}
	for _, item := range checks {
		if !item.Pass {
			plan.Eligible = false
		}
	}
	if plan.Eligible {
		candidate := candidates[0]
		plan.Candidate = &candidate
	}
	return plan
}

func check(name string, pass bool, message string) Check {
	if strings.TrimSpace(message) == "" {
		message = "none"
	}
	return Check{Name: name, Pass: pass, Message: message}
}

func canaryCheck(status ports.GPUDRAStatus, options Options) Check {
	age := time.Duration(0)
	fresh := false
	if status.CanaryLastSuccessfulTime != nil {
		age = options.Now.Sub(*status.CanaryLastSuccessfulTime)
		fresh = age >= 0 && age <= options.MaxCanaryAge
	}
	return check("allocation-canary", status.CanaryPresent && !status.CanarySuspended && fresh,
		fmt.Sprintf("present=%t suspended=%t age=%s max=%s", status.CanaryPresent, status.CanarySuspended, age.Round(time.Second), options.MaxCanaryAge))
}

func imagesPinned(images []string, requiredDigest string) bool {
	if len(images) == 0 {
		return false
	}
	requiredDigest = strings.TrimSpace(requiredDigest)
	for _, image := range images {
		parts := strings.Split(image, "@")
		if len(parts) != 2 || !strings.HasPrefix(parts[1], "sha256:") {
			return false
		}
		if requiredDigest != "" && parts[1] != requiredDigest {
			return false
		}
	}
	return true
}

var kubernetesVersion = regexp.MustCompile(`v?(\d+)\.(\d+)`)

func kubernetesAtLeast136(raw string) bool {
	match := kubernetesVersion.FindStringSubmatch(raw)
	if len(match) != 3 {
		return false
	}
	major, majorErr := strconv.Atoi(match[1])
	minor, minorErr := strconv.Atoi(match[2])
	return majorErr == nil && minorErr == nil && (major > 1 || (major == 1 && minor >= 36))
}

func holderMessage(holders []ports.GPUHolder) string {
	values := make([]string, 0, len(holders))
	for _, holder := range holders {
		values = append(values, holder.Namespace+"/"+holder.Kind+"/"+holder.Name)
	}
	sort.Strings(values)
	if len(values) == 0 {
		return "none"
	}
	return strings.Join(values, ",")
}
