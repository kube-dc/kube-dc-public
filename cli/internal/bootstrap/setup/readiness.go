package setup

import (
	"fmt"
	"sort"
	"time"
)

const ReadinessSchemaVersion = 1
const maxHostInventoryAge = 5 * time.Minute

// ToolFact is the selected operation's read-only workstation result.
type ToolFact struct {
	Name       string `json:"name"`
	Status     string `json:"status"`
	Reason     string `json:"reason"`
	Detail     string `json:"detail,omitempty"`
	NextAction string `json:"nextAction,omitempty"`
}

// ReadinessFinding is one actionable result from the existing checks.
type ReadinessFinding struct {
	ID         string    `json:"id"`
	Category   string    `json:"category"`
	Scope      string    `json:"scope,omitempty"`
	Resource   string    `json:"resource,omitempty"`
	Status     string    `json:"status"`
	Detail     string    `json:"detail"`
	NextAction string    `json:"nextAction,omitempty"`
	ObservedAt time.Time `json:"observedAt,omitempty"`
}

// ReadinessReport combines local and host observations for one compiled
// input. It is a review artifact; no combination authorizes apply.
type ReadinessReport struct {
	SchemaVersion int                `json:"schemaVersion"`
	State         string             `json:"state"`
	ReadyToApply  bool               `json:"readyToApply"`
	Cluster       string             `json:"cluster"`
	Profile       string             `json:"profile"`
	InputHash     string             `json:"inputHash"`
	PlanHash      string             `json:"planHash"`
	ReleaseSHA256 string             `json:"releaseSHA256"`
	ToolsChecked  bool               `json:"toolsChecked"`
	HostsChecked  bool               `json:"hostsChecked"`
	BlockedCount  int                `json:"blockedCount"`
	PendingCount  int                `json:"pendingCount"`
	PassedCount   int                `json:"passedCount"`
	Findings      []ReadinessFinding `json:"findings"`
	Unresolved    []string           `json:"unresolved"`
}

// BuildReadiness reduces existing probe reports. It checks input bindings so
// results from another plan cannot be mixed into one operator review.
func BuildReadiness(c Compiled, plan Preview, release ReleaseReport, tools []ToolFact, hosts *HostInventory) (ReadinessReport, error) {
	if err := checkCompiled(c); err != nil {
		return ReadinessReport{}, err
	}
	if plan.InputHash != c.InputHash || plan.PlanHash == "" || plan.ReleaseSHA256 != c.ReleaseSHA256 ||
		release.InputHash != c.InputHash || release.ReleaseSHA256 != c.ReleaseSHA256 ||
		(hosts != nil && hosts.InputHash != c.InputHash) {
		return ReadinessReport{}, fmt.Errorf("setup readiness inputs changed; run the checks again")
	}
	r := ReadinessReport{
		SchemaVersion: ReadinessSchemaVersion,
		State:         "action-required", Cluster: c.Spec.Name, Profile: c.Spec.Profile,
		InputHash: c.InputHash, PlanHash: plan.PlanHash, ReleaseSHA256: c.ReleaseSHA256,
		ToolsChecked: tools != nil, HostsChecked: hosts != nil,
		Findings: []ReadinessFinding{}, Unresolved: []string{},
	}
	for _, check := range release.Checks {
		r.Findings = append(r.Findings, ReadinessFinding{
			ID: "release." + check.ID, Category: "release", Scope: "release", Status: check.Status,
			Detail: check.Detail, NextAction: check.NextAction,
		})
	}
	for _, tool := range tools {
		status := "blocked"
		if tool.Status == "installed" || tool.Status == "managed" {
			status = "pass"
		}
		detail := tool.Reason + "; " + tool.Status
		if tool.Detail != "" {
			detail += "; " + tool.Detail
		}
		r.Findings = append(r.Findings, ReadinessFinding{
			ID: "tool." + tool.Name, Category: "workstation", Scope: "workstation", Resource: tool.Name,
			Status: status, Detail: detail,
			NextAction: tool.NextAction,
		})
	}
	var bindingFindings []ReadinessFinding
	if hosts != nil {
		bindingFindings = checkHostInventoryBinding(c.Spec.Hosts, *hosts, time.Now().UTC())
		r.Findings = append(r.Findings, bindingFindings...)
		for _, host := range hosts.Hosts {
			for _, check := range host.Checks {
				scope, resource := check.Scope, check.Resource
				if scope == "" {
					scope = "host"
				}
				if resource == "" {
					resource = host.ID
				}
				r.Findings = append(r.Findings, ReadinessFinding{
					ID: "host." + host.ID + "." + check.ID, Category: "host", Scope: scope,
					Resource: resource, Status: check.Status, Detail: check.Detail,
					NextAction: check.NextAction, ObservedAt: check.ObservedAt,
				})
			}
		}
		for _, check := range hosts.Checks {
			r.Findings = append(r.Findings, ReadinessFinding{
				ID: "hosts." + check.ID, Category: "hosts", Scope: check.Scope, Resource: check.Resource,
				Status: check.Status, Detail: check.Detail, NextAction: check.NextAction,
				ObservedAt: check.ObservedAt,
			})
		}
	}
	unresolved := map[string]bool{}
	for _, source := range [][]string{plan.Unresolved, release.Unresolved} {
		for _, id := range source {
			unresolved[id] = true
		}
	}
	if hosts == nil {
		unresolved["host-inventory-not-run"] = true
	} else {
		if len(bindingFindings) != 0 {
			unresolved["host-inventory-invalid-or-stale"] = true
		}
		for _, id := range hosts.Unresolved {
			unresolved[id] = true
		}
	}
	if tools == nil {
		unresolved["workstation-tools-not-run"] = true
	}
	for id := range unresolved {
		r.Unresolved = append(r.Unresolved, id)
	}
	sort.Strings(r.Unresolved)
	sort.SliceStable(r.Findings, func(i, j int) bool {
		a, b := r.Findings[i], r.Findings[j]
		if findingRank(a.Status) != findingRank(b.Status) {
			return findingRank(a.Status) < findingRank(b.Status)
		}
		if a.Category != b.Category {
			return a.Category < b.Category
		}
		return a.ID < b.ID
	})
	for _, finding := range r.Findings {
		switch finding.Status {
		case "blocked":
			r.BlockedCount++
		case "pass", "not-applicable":
			r.PassedCount++
		default:
			r.PendingCount++
		}
	}
	if r.BlockedCount != 0 {
		r.State = "blocked"
	}
	return r, nil
}

// checkHostInventoryBinding detects partial, mixed, or expired reports before
// their individual checks are shown to the operator. A fresh read is required
// after a host selection or SSH identity changes.
func checkHostInventoryBinding(selected []Host, report HostInventory, now time.Time) []ReadinessFinding {
	var findings []ReadinessFinding
	add := func(id, resource, detail string) {
		findings = append(findings, ReadinessFinding{
			ID: "hosts.binding." + id, Category: "hosts", Scope: "host", Resource: resource,
			Status: "blocked", Detail: detail, NextAction: "Run the host inventory again with the current setup specification.",
		})
	}
	if report.SchemaVersion != HostInventorySchemaVersion {
		add("schema", "", "host inventory schema is missing or unsupported")
	}
	if report.State != "observed" && report.State != "blocked" {
		add("state", "", "host inventory state is missing or invalid")
	}
	blockedEvidence := false
	for _, check := range report.Checks {
		blockedEvidence = blockedEvidence || check.Status == "blocked"
	}
	want := make(map[string]Host, len(selected))
	for _, host := range selected {
		want[host.ID] = host
	}
	seen := make(map[string]bool, len(report.Hosts))
	for _, host := range report.Hosts {
		selectedHost, exists := want[host.ID]
		switch {
		case !exists:
			add("unexpected-"+host.ID, host.ID, "host was not selected in this setup")
		case seen[host.ID]:
			add("duplicate-"+host.ID, host.ID, "host appears more than once")
		default:
			seen[host.ID] = true
			if host.Role != selectedHost.Role || host.SSHAlias != selectedHost.SSHAlias {
				add("target-"+host.ID, host.ID, "host role or SSH target differs from the setup")
			}
			hostBlocked := false
			for _, check := range host.Checks {
				hostBlocked = hostBlocked || check.Status == "blocked"
			}
			blockedEvidence = blockedEvidence || hostBlocked
			if (host.State != "observed" && host.State != "blocked") || (host.State == "blocked" && !hostBlocked) || (host.State == "observed" && hostBlocked) {
				add("state-"+host.ID, host.ID, "host state does not match its checks")
			}
			if host.ObservedAt.IsZero() || host.ObservedAt.After(now.Add(time.Minute)) || now.Sub(host.ObservedAt) > maxHostInventoryAge {
				add("age-"+host.ID, host.ID, "host observation is missing, expired, or in the future")
			}
			if selectedHost.HostKeySHA256 != "" && (host.Facts.HostKey == nil || host.Facts.HostKey.FingerprintSHA256 != selectedHost.HostKeySHA256) {
				add("host-key-"+host.ID, host.ID, "verified host key evidence does not match the selected pin")
			}
		}
	}
	for _, host := range selected {
		if !seen[host.ID] {
			add("missing-"+host.ID, host.ID, "selected host is missing from the inventory")
		}
	}
	if (report.State == "blocked") != blockedEvidence {
		add("state-checks", "", "inventory state does not match the reported checks")
	}
	return findings
}

func findingRank(status string) int {
	switch status {
	case "blocked":
		return 0
	case "unknown", "pending":
		return 1
	case "pass":
		return 2
	default:
		return 3
	}
}
