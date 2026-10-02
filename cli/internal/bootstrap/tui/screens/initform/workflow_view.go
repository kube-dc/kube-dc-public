package initform

import (
	"fmt"
	"strings"
	"time"

	"github.com/charmbracelet/x/ansi"
	"github.com/shalb/kube-dc/cli/internal/bootstrap/setup"
)

func (m *PanelModel) discoveryBlocker() string {
	w := m.workflow
	if w == nil || !w.attempted {
		return ""
	}
	requests, err := m.resourceRequests()
	if err != nil {
		return err.Error()
	}
	for _, req := range requests {
		if !req.Host.Primary && req.Host.Disk == "" && req.Host.NIC == "" {
			continue
		}
		if req.Host.SSHAlias == "" {
			return "Enter an SSH target for host " + req.Host.ID + "."
		}
		snapshot, ok := w.hostSnapshots[req.Host.ID]
		if !ok || snapshot.Err != nil || fingerprint(snapshot.Request) != fingerprint(req) {
			return "Host evidence is missing or stale for " + req.Host.ID + ". Inspect all hosts again."
		}
		host := snapshot.Resources.Host
		age := time.Since(host.ObservedAt)
		if host.ObservedAt.IsZero() || age < 0 || age >= 5*time.Minute {
			return "Host evidence expired for " + req.Host.ID + ". Inspect all hosts again."
		}
		if host.Facts.HostKey == nil || host.Facts.MachineID == "" {
			return "Confirm the host key for " + req.Host.ID + " and inspect again."
		}
		for _, c := range host.Checks {
			if c.Status == "blocked" || (c.Status == "unknown" && (c.ID == "host-key" || strings.HasPrefix(c.ID, "selected-disk"))) {
				return "Host check blocked on " + req.Host.ID + ": " + c.Detail
			}
		}
	}
	return ""
}

func (m *PanelModel) finishBody(body string, cursor, width int) (string, int) {
	var lines []string
	adjusted := 0
	for i, line := range strings.Split(strings.TrimSuffix(body, "\n"), "\n") {
		if i == cursor {
			adjusted = len(lines)
		}
		lines = append(lines, strings.Split(ansi.Hardwrap(ansi.Wordwrap(line, width, ""), width, true), "\n")...)
	}
	if m.workflow != nil {
		for _, text := range m.workflowText() {
			if text == "" {
				lines = append(lines, "")
				continue
			}
			for _, line := range wrapPlain(text, width) {
				lines = append(lines, m.theme().text.Render(line))
			}
		}
	}
	return strings.Join(lines, "\n"), adjusted
}

func (m *PanelModel) workflowText() []string {
	w := m.workflow
	section := m.currentSection()
	if section == "Continue" && w.services.Session != nil {
		return m.sessionText()
	}
	var lines []string
	add := func(s string) { lines = append(lines, s) }
	if section == "Network" {
		lines = append(lines, m.networkSummary()...)
		if !w.networkDetails {
			return lines
		}
	}
	if section == "Storage" {
		lines = append(lines, m.storageSummary()...)
		if !w.storageDetails {
			return lines
		}
	}
	if section == "Hosts" {
		add("")
		add("SSH target: " + nonEmpty(m.request().Host.SSHAlias, "set SSH host in Basics or Other SSH targets"))
		if w.services.Demo != nil {
			add("SIMULATED · Scenario: " + w.scenario)
		}
		add("Discovery starts only when you select Discover host resources. Opening the form or changing tabs makes no SSH requests.")
	}
	if section == "Hosts" || section == "Network" || section == "Storage" || (section == "Readiness" && w.services.Demo == nil) {
		add("")
		if w.busy {
			add("Reading host facts… Ctrl+C cancels.")
			return lines
		}
		if w.resourceErr != nil {
			add("Discovery failed: " + w.resourceErr.Error())
			return lines
		}
		if !m.resourcesCurrent() {
			add("No current host evidence. Open Hosts and select Discover host resources.")
			return lines
		}
		h := w.resources.Host
		f := h.Facts
		add("Observed: " + h.ObservedAt.UTC().Format(time.RFC3339) + " · " + h.SSHAlias)
		if f.HostKey != nil {
			add("Verified SSH fingerprint: " + f.HostKey.FingerprintSHA256)
		}
		if f.MachineID != "" {
			add("Machine: " + f.MachineID)
			add(fmt.Sprintf("%s %s · %s · %d CPUs · %.1f GiB RAM · %.1f GiB free", f.OSID, f.OSVersion, f.Architecture, f.CPUs, float64(f.MemoryKiB)/(1<<20), float64(f.FreeBytes)/(1<<30)))
		}
		if section == "Hosts" || section == "Network" {
			add("")
			add("Interfaces (no automatic role assignment)")
			for _, n := range f.NICs {
				route := ""
				for _, name := range w.resources.DefaultRouteNICs {
					if name == n.Name {
						route = " · default route"
					}
				}
				add(fmt.Sprintf("%s · MTU %d · %s · %s%s", n.Name, n.MTU, n.MAC, strings.Join(n.Addresses, ", "), route))
			}
			if section == "Network" {
				for _, c := range setup.NetworkCandidates(w.resources, time.Now().UTC()) {
					add(fmt.Sprintf("%s · %s · MTU %d · %s", c.Name, c.Kind, c.MTU, c.Reason))
				}
				if !w.resources.Network.Complete {
					add("Detailed link or route inventory is incomplete. No interface suggestions.")
				}
			}
		}
		if section == "Hosts" || section == "Storage" {
			add("")
			add("Disks on " + h.ID + " (select explicitly)")
			for _, d := range w.resources.Disks {
				state := "CHECK INCOMPLETE · " + d.Reason
				if d.Checked {
					state = "AVAILABLE · identity and usage checked"
				}
				if d.InUse {
					state = "RESERVED · " + d.Reason
				}
				add(fmt.Sprintf("%s · %.1f GiB · %s · %s · %s", d.Path, float64(d.SizeBytes)/(1<<30), d.Media, d.Model, state))
				add(fmt.Sprintf("  WWN %s · serial %s", nonEmpty(d.WWN, "unknown"), nonEmpty(d.Serial, "unknown")))
			}
			if d := f.SelectedDisk; d != nil {
				add("Selected disk: " + d.SelectedPath + " → " + d.ResolvedPath)
				add(fmt.Sprintf("Stable ID: %s · WWN %s · serial %s · holders %d · signatures %d", d.StableID, d.WWN, d.Serial, d.HolderCount, d.SignatureCount))
			}
			add("Loop-backed and PVC storage do not use a discovered raw-disk candidate.")
		}
		add("")
		add("Checks")
		for _, c := range h.Checks {
			add(strings.ToUpper(c.Status) + " · " + c.ID + ": " + c.Detail)
			if c.NextAction != "" && (c.Status == "blocked" || c.Status == "unknown") {
				add("Next: " + c.NextAction)
			}
		}
		add("")
		add("These are read-only observations, not installation approval. A qualified release and full setup checks remain required for the guided installer.")
	}
	if section == "Readiness" && w.services.Demo != nil {
		add("")
		add("SIMULATED readiness · shared setup compiler")
		if w.busy {
			add("Checking current settings…")
			return lines
		}
		if w.resourceErr != nil {
			add(w.resourceErr.Error())
			return lines
		}
		if w.reviewHash != m.stateHash() {
			add("Run Check readiness to check the current inputs.")
			return lines
		}
		r := w.review.Readiness
		add(fmt.Sprintf("%d blocked · %d unknown · %d passed", r.BlockedCount, r.PendingCount, r.PassedCount))
		for _, c := range r.Findings {
			add(strings.ToUpper(c.Status) + " · " + c.ID + ": " + c.Detail)
			if c.NextAction != "" && (c.Status == "blocked" || c.Status == "unknown") {
				add("Next: " + c.NextAction)
			}
		}
	}
	if section == "Review" && w.services.Demo != nil {
		add("")
		add("SIMULATION REVIEW · Production apply unavailable")
		if w.reviewHash != m.stateHash() || !w.review.CanRun {
			add("Run readiness checks and resolve findings before simulation.")
			return lines
		}
		add("Plan: " + w.review.Plan.PlanHash)
		for i, s := range w.review.Plan.Stages {
			add(fmt.Sprintf("%02d  %s", i+1, s.Title))
		}
		add("Open production gates: " + strings.Join(w.review.Readiness.Unresolved, ", "))
	}
	if w.services.Demo != nil && (section == "Install" || section == "Verify" || section == "Result" || section == "Continue") {
		add("")
		add("SIMULATED · No host or cluster was changed.")
		if w.resultErr != nil {
			add("Error: " + w.resultErr.Error())
		} else {
			add(w.result)
		}
		if section == "Continue" {
			add("Your settings remain available in the same sections. Edit them, check again, then review a new simulation.")
		}
	}
	return lines
}

func (m *PanelModel) primaryBlocker() string {
	if m.editing {
		return "Finish or cancel the current field edit."
	}
	if m.Busy() {
		return "Reading facts. Ctrl+C cancels the operation."
	}
	w := m.workflow
	if w == nil {
		return ""
	}
	switch m.currentSection() {
	case "Readiness", "Review":
		if w.services.Demo != nil && (w.reviewHash != m.stateHash() || w.resourceErr != nil || !w.review.CanRun) {
			return "Resolve blocked or unknown checks, then check readiness again."
		}
		if w.services.Demo == nil && m.currentSection() == "Review" {
			if len(m.validationErrors()) > 0 {
				return "Correct the settings before installation review."
			}
			return m.discoveryBlocker()
		}
	}
	return ""
}

func (m *PanelModel) workflowBadge(section string) (string, bool) {
	w := m.workflow
	if w == nil {
		return "", false
	}
	status := "pending"
	switch section {
	case "Hosts":
		if m.resourcesCurrent() {
			status = "pass"
			for _, c := range w.resources.Host.Checks {
				if c.Status == "blocked" || c.Status == "unknown" {
					status = "blocked"
					break
				}
			}
		} else if w.resourceErr != nil {
			status = "blocked"
		}
	case "Readiness", "Review":
		if w.services.Demo != nil {
			if w.reviewHash == m.stateHash() {
				status = "pass"
				if !w.review.CanRun || w.resourceErr != nil {
					status = "blocked"
				}
			}
		} else if w.attempted {
			status = "pass"
			if m.discoveryBlocker() != "" {
				status = "blocked"
			}
			for _, c := range w.resources.Host.Checks {
				if c.Status == "unknown" {
					status = "blocked"
				}
			}
		}
	default:
		return "", false
	}
	switch status {
	case "pass":
		return " " + m.theme().good.Render("✓"), true
	case "blocked":
		return " " + m.theme().warn.Render("!"), true
	default:
		return " " + m.theme().muted.Render("·"), true
	}
}
