package gpuupgrade

import (
	"bytes"
	"fmt"
	"io"
	"os"
	"regexp"
	"sort"
	"strconv"
	"strings"
	"time"

	"gopkg.in/yaml.v3"
)

const (
	APIVersion     = "kube-dc.io/v1alpha1"
	Kind           = "GPUUpgradeQualification"
	MaxRecordBytes = 64 << 10
	DefaultMaxAge  = 30 * 24 * time.Hour
)

var (
	idPattern       = regexp.MustCompile(`^[a-z0-9](?:[a-z0-9.-]*[a-z0-9])?$`)
	revisionPattern = regexp.MustCompile(`^[0-9a-fA-F]{40}$`)
	pciPattern      = regexp.MustCompile(`^[0-9a-f]{4}:[0-9a-f]{4}/[0-9a-f]{4}:[0-9a-f]{4}$`)
	rke2Pattern     = regexp.MustCompile(`^v?1\.([0-9]+)\.([0-9]+)\+rke2r([0-9]+)$`)
)

// Target is the complete host/runtime tuple whose exact combination was
// qualified. Keeping the tuple indivisible prevents an independently proven
// kernel and driver from being combined without evidence for that combination.
type Target struct {
	Kernel       string `yaml:"kernel"`
	RKE2         string `yaml:"rke2"`
	Driver       string `yaml:"driver"`
	GPUOperator  string `yaml:"gpuOperator"`
	DCGMExporter string `yaml:"dcgmExporter"`
}

type Canary struct {
	CompletedAt      string `yaml:"completedAt"`
	AllocationPassed bool   `yaml:"allocationPassed"`
	MonitoringPassed bool   `yaml:"monitoringPassed"`
	RollbackPassed   bool   `yaml:"rollbackPassed"`
	Evidence         string `yaml:"evidence"`
}

type Qualification struct {
	APIVersion     string   `yaml:"apiVersion"`
	Kind           string   `yaml:"kind"`
	ID             string   `yaml:"id"`
	State          string   `yaml:"state"`
	ApprovedBy     string   `yaml:"approvedBy"`
	SourceRevision string   `yaml:"sourceRevision"`
	Hardware       []string `yaml:"hardware"`
	Target         Target   `yaml:"target"`
	Canary         Canary   `yaml:"canary"`
}

type Request struct {
	Current  Target
	Target   Target
	Hardware []string
	Now      time.Time
	MaxAge   time.Duration
}

type Result struct {
	QualificationID string
	CompletedAt     time.Time
	SourceRevision  string
	ApprovedBy      string
}

func Load(path string) (Qualification, error) {
	f, err := os.Open(path)
	if err != nil {
		return Qualification{}, fmt.Errorf("open qualification: %w", err)
	}
	defer f.Close()

	limited := io.LimitReader(f, MaxRecordBytes+1)
	body, err := io.ReadAll(limited)
	if err != nil {
		return Qualification{}, fmt.Errorf("read qualification: %w", err)
	}
	if len(body) > MaxRecordBytes {
		return Qualification{}, fmt.Errorf("qualification exceeds %d bytes", MaxRecordBytes)
	}

	var q Qualification
	dec := yaml.NewDecoder(bytes.NewReader(body))
	dec.KnownFields(true)
	if err := dec.Decode(&q); err != nil {
		return Qualification{}, fmt.Errorf("decode qualification: %w", err)
	}
	var trailing any
	if err := dec.Decode(&trailing); err != io.EOF {
		if err == nil {
			return Qualification{}, fmt.Errorf("decode qualification: multiple YAML documents are not allowed")
		}
		return Qualification{}, fmt.Errorf("decode qualification: %w", err)
	}
	return q, nil
}

func Check(q Qualification, req Request) (Result, error) {
	var problems []string
	if q.APIVersion != APIVersion || q.Kind != Kind {
		problems = append(problems, fmt.Sprintf("record must be %s %s", APIVersion, Kind))
	}
	if !idPattern.MatchString(q.ID) {
		problems = append(problems, "record id must be a stable lowercase identifier")
	}
	if q.State != "qualified" {
		problems = append(problems, "record state must be qualified")
	}
	if strings.TrimSpace(q.ApprovedBy) == "" {
		problems = append(problems, "approvedBy is required")
	}
	if !revisionPattern.MatchString(q.SourceRevision) {
		problems = append(problems, "sourceRevision must be a full 40-character Git revision")
	}

	wantHardware, hardwareErrs := canonicalHardware(req.Hardware)
	gotHardware, recordHardwareErrs := canonicalHardware(q.Hardware)
	problems = append(problems, hardwareErrs...)
	for _, problem := range recordHardwareErrs {
		problems = append(problems, "record "+problem)
	}
	if len(wantHardware) > 0 && strings.Join(wantHardware, ",") != strings.Join(gotHardware, ",") {
		problems = append(problems, fmt.Sprintf("hardware tuple mismatch: requested %s, qualified %s", strings.Join(wantHardware, ","), strings.Join(gotHardware, ",")))
	}

	for _, field := range []struct{ name, current, target, qualified string }{
		{"kernel", req.Current.Kernel, req.Target.Kernel, q.Target.Kernel},
		{"rke2", req.Current.RKE2, req.Target.RKE2, q.Target.RKE2},
		{"driver", req.Current.Driver, req.Target.Driver, q.Target.Driver},
		{"gpuOperator", req.Current.GPUOperator, req.Target.GPUOperator, q.Target.GPUOperator},
		{"dcgmExporter", req.Current.DCGMExporter, req.Target.DCGMExporter, q.Target.DCGMExporter},
	} {
		if strings.TrimSpace(field.current) == "" || strings.TrimSpace(field.target) == "" {
			problems = append(problems, field.name+" current and target values are required")
		}
		if field.target != field.qualified {
			problems = append(problems, fmt.Sprintf("%s target %q is not the qualified value %q", field.name, field.target, field.qualified))
		}
	}
	if req.Current == req.Target {
		problems = append(problems, "at least one kernel, RKE2, driver, GPU Operator, or DCGM exporter target must change")
	}
	if err := validateRKE2Step(req.Current.RKE2, req.Target.RKE2); err != nil {
		problems = append(problems, err.Error())
	}

	completedAt, err := time.Parse(time.RFC3339, q.Canary.CompletedAt)
	if err != nil {
		problems = append(problems, "canary.completedAt must be RFC3339")
	} else {
		now := req.Now
		if now.IsZero() {
			now = time.Now()
		}
		maxAge := req.MaxAge
		if maxAge <= 0 {
			maxAge = DefaultMaxAge
		}
		if completedAt.After(now.Add(5 * time.Minute)) {
			problems = append(problems, "canary.completedAt is in the future")
		} else if now.Sub(completedAt) > maxAge {
			problems = append(problems, fmt.Sprintf("canary evidence is older than %s", maxAge))
		}
	}
	if !q.Canary.AllocationPassed || !q.Canary.MonitoringPassed || !q.Canary.RollbackPassed {
		problems = append(problems, "allocation, monitoring, and rollback canary checks must all pass")
	}
	if strings.TrimSpace(q.Canary.Evidence) == "" {
		problems = append(problems, "canary.evidence is required")
	}

	if len(problems) > 0 {
		return Result{}, fmt.Errorf("GPU upgrade blocked: %s", strings.Join(problems, "; "))
	}
	return Result{QualificationID: q.ID, CompletedAt: completedAt, SourceRevision: strings.ToLower(q.SourceRevision), ApprovedBy: q.ApprovedBy}, nil
}

func canonicalHardware(values []string) ([]string, []string) {
	seen := map[string]bool{}
	var out, problems []string
	for _, raw := range values {
		value := strings.ToLower(strings.TrimSpace(raw))
		if !pciPattern.MatchString(value) {
			problems = append(problems, fmt.Sprintf("hardware %q must be vendor:device/subvendor:subdevice", raw))
			continue
		}
		if seen[value] {
			problems = append(problems, fmt.Sprintf("hardware %q is duplicated", raw))
			continue
		}
		seen[value] = true
		out = append(out, value)
	}
	if len(out) == 0 {
		problems = append(problems, "at least one hardware identity is required")
	}
	sort.Strings(out)
	return out, problems
}

func validateRKE2Step(current, target string) error {
	c := rke2Pattern.FindStringSubmatch(current)
	t := rke2Pattern.FindStringSubmatch(target)
	if c == nil || t == nil {
		return fmt.Errorf("RKE2 versions must use v1.MINOR.PATCH+rke2rREV")
	}
	values := func(match []string) [3]int {
		var out [3]int
		for i := range out {
			out[i], _ = strconv.Atoi(match[i+1])
		}
		return out
	}
	from, to := values(c), values(t)
	if to[0] < from[0] || (to[0] == from[0] && to[1] < from[1]) || (to[0] == from[0] && to[1] == from[1] && to[2] < from[2]) {
		return fmt.Errorf("RKE2 downgrade is not allowed")
	}
	if to[0]-from[0] > 1 {
		return fmt.Errorf("RKE2 upgrade cannot skip a Kubernetes minor")
	}
	return nil
}
