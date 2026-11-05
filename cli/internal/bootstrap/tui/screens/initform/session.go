package initform

import (
	"context"
	"fmt"
	"strings"
	"time"

	tea "charm.land/bubbletea/v2"
	"github.com/shalb/kube-dc/cli/internal/bootstrap/setup"
)

// SessionControl is the same coordinator used by command-line inspection.
// It supplies ownership operations, never arbitrary shell commands or Apply.
type SessionControl interface {
	Status(context.Context) (setup.CoordinatorStatus, error)
	Acquire(context.Context, bool) (setup.CoordinatorStatus, error)
	Release(context.Context) (setup.CoordinatorStatus, error)
}

type executorHandoffControl interface {
	PrepareExecutorHandoff(context.Context) (setup.SessionHandoff, error)
	AcceptExecutorHandoff(context.Context, string) (setup.CoordinatorStatus, error)
}

type PlatformServices struct {
	Refresh func(context.Context) (setup.PlatformReview, error)
	Inspect func(context.Context, bool) (setup.PlatformRecoveryReport, error)
}

func (m *PanelModel) sessionFields() []panelField {
	action := func(label, id, desc string) panelField {
		return panelField{Section: "Continue", Label: label, Action: id, Kind: panelAction, Desc: desc}
	}
	fields := []panelField{
		action("Inspect session and targets", "session-inspect", "Read saved evidence, host claims, and the verified child cluster reservation. No installation changes."),
		action("Reserve reviewed hosts", "session-claim", "Save ownership intent, recheck reviewed inputs, and reserve all hosts for 30 minutes. Installation stays blocked."),
		action("Take over expired unused reservations", "session-takeover", "Explicit takeover checks every host again. Started or active claims cannot be taken over."),
		action("Release unused reservations", "session-release", "Release only this run's reservations that have admitted no effects."),
		action("Show installation gates", "session-gates", "Full platform effects, release qualification, and recovery must pass before installation can run."),
	}
	if m.workflow != nil {
		if _, ok := m.workflow.services.Session.(executorHandoffControl); ok {
			fields = append(fields, action("Prepare workstation handoff", "session-handoff", "At the verified host boundary, stop this run's write access and retain a private handoff. No platform operation can have started."), action("Accept prepared handoff", "session-accept-handoff", "Check the designated recipient key, transfer parked controls, and verify every host and the cluster. Requires a fresh invocation."))
		}
	}
	if m.workflow != nil && m.workflow.services.Platform != nil {
		fields = append(fields, action("Refresh execution review", "session-refresh-platform", "Inspect the verified child and receipt-bound Fleet state. Keep the original safety review and earlier proofs."), action("Inspect interrupted effects", "session-inspect-effects", "Check live postconditions without retrying an effect."), action("Record verified outcomes", "session-record-effects", "Repeat live checks and save only confirmed completion evidence. This does not restore write authority."))
	}
	return fields
}
func (m *PanelModel) startSession(action string) tea.Cmd {
	w := m.workflow
	if w == nil || w.services.Session == nil || w.busy {
		return nil
	}
	m.openSection("Continue")
	if action == "session-gates" {
		m.notice = "Installation is blocked: release qualification, full platform effects, cluster ownership, and live recovery remain open."
		return nil
	}
	if (action == "session-claim" || action == "session-takeover" || action == "session-refresh-platform" || action == "session-record-effects" || action == "session-handoff" || action == "session-accept-handoff") && w.sessionInputHash != m.stateHash() {
		m.notice = "Settings changed. Prepare a new safety review and session before reserving these hosts."
		return nil
	}
	m.Close()
	w.busy = true
	// A multi-host review is serial at the ownership boundary. The coordinator
	// bounds each SSH operation; cancellation preserves ambiguous claims.
	ctx, cancel := context.WithTimeout(w.services.Context, 5*time.Minute)
	w.cancel = cancel
	session := w.services.Session
	handoffRef := ""
	if w.sessionStatus.Handoff != nil {
		handoffRef = w.sessionStatus.Handoff.CertificateSHA256
	}
	e := PanelEvent{Owner: m, generation: w.generation, kind: "session", hash: m.stateHash()}
	return func() tea.Msg {
		switch action {
		case "session-handoff":
			if h, ok := session.(executorHandoffControl); ok {
				_, e.err = h.PrepareExecutorHandoff(ctx)
				if e.err == nil {
					e.sessionStatus, e.err = session.Status(ctx)
				}
			} else {
				e.err = fmt.Errorf("executor handoff is unavailable")
			}
		case "session-accept-handoff":
			if h, ok := session.(executorHandoffControl); ok && handoffRef != "" {
				e.sessionStatus, e.err = h.AcceptExecutorHandoff(ctx, handoffRef)
			} else {
				e.err = fmt.Errorf("inspect the session to find a prepared handoff")
			}
		case "session-refresh-platform":
			e.kind = "session-platform-review"
			if p := w.services.Platform; p != nil && p.Refresh != nil {
				e.platformReview, e.err = p.Refresh(ctx)
			} else {
				e.err = fmt.Errorf("platform review is unavailable")
			}
		case "session-inspect-effects", "session-record-effects":
			e.kind = "session-platform-inspect"
			if p := w.services.Platform; p != nil && p.Inspect != nil {
				e.platformRecovery, e.err = p.Inspect(ctx, action == "session-record-effects")
			} else {
				e.err = fmt.Errorf("platform inspection is unavailable")
			}
		case "session-inspect":
			e.sessionStatus, e.err = session.Status(ctx)
		case "session-claim":
			e.sessionStatus, e.err = session.Acquire(ctx, false)
		case "session-takeover":
			e.sessionStatus, e.err = session.Acquire(ctx, true)
		case "session-release":
			e.sessionStatus, e.err = session.Release(ctx)
		default:
			e.err = fmt.Errorf("unknown session action")
		}
		return e
	}
}
func (m *PanelModel) sessionText() []string {
	w := m.workflow
	lines := []string{"Saved session · " + safeText(w.services.SessionDirectory)}
	if w.platformReview.Hash != "" {
		lines = append(lines, fmt.Sprintf("Execution review: %.16s · %d stages", w.platformReview.Hash, len(w.platformReview.Effects)))
	}
	for i, finding := range w.platformRecovery.Findings {
		if i >= 3 {
			lines = append(lines, "More findings are available from session inspect-effects.")
			break
		}
		lines = append(lines, safeText(finding.Stage)+": "+safeText(finding.State))
	}
	if w.sessionInputHash != m.stateHash() {
		lines = append(lines, "Settings changed. Reserve is blocked until you prepare a new review.")
	}
	if !w.sessionInspected {
		return append(lines, "Inspect the session to read saved evidence and current host claims.")
	}
	status := w.sessionStatus
	if h := status.Handoff; h != nil {
		lines = append(lines, fmt.Sprintf("Workstation handoff: %s · %.16s", safeText(h.Phase), h.CertificateSHA256))
		if h.Phase == "preparing" {
			lines = append(lines, "The previous run is sealed. Transfer the private session and its bound files, then accept from a new invocation.")
		}
	}
	if status.ExecutorUncertain {
		lines = append(lines, "Executor state is uncertain. Verified outcomes do not restore write authority.")
	}
	phase := status.Session.OwnershipPhase
	if phase == "" {
		phase = "unclaimed"
	}
	lines = append(lines, "Ownership: "+phase+" · verified stages: "+fmt.Sprint(status.Session.CompletedStages))
	for _, target := range status.Targets {
		state := target.State
		if state == "reserved" || state == "started" {
			owner := "another run"
			if target.CurrentRun {
				owner = "this run"
			}
			state += " · " + owner
			if target.BootChanged {
				state += " · host restarted"
			} else if target.SecondsRemaining == 0 {
				state += " · expired"
			} else {
				state += fmt.Sprintf(" · %dm left", (target.SecondsRemaining+59)/60)
			}
		}
		if target.State == "unknown" {
			state = "unknown · inspect host access and ownership"
		}
		lines = append(lines, safeText(target.HostID)+"  "+state)
	}
	if status.Cluster.State != "" {
		cluster := "Cluster: " + safeText(status.Cluster.State)
		if status.Cluster.State == "held" {
			if status.Cluster.CurrentRun {
				cluster += " · this run"
			} else {
				cluster += " · inspection only"
			}
			if !status.Cluster.MatchesProof {
				cluster += " · no completion proof"
			}
		}
		lines = append(lines, cluster)
		if status.Cluster.State == "unknown" || status.Cluster.State == "conflict" || status.Cluster.State == "absent" {
			lines = append(lines, safeText(status.Cluster.Detail))
		}
	}
	if status.Session.State == "inspection-required" && len(status.Session.NeedsInspection) > 0 {
		ids := []string{}
		for _, event := range status.Session.NeedsInspection {
			ids = append(ids, event.Stage)
		}
		lines = append(lines, "Inspect interrupted stages: "+strings.Join(ids, ", "))
	}
	return append(lines, "Started claims stay protected after expiry. Installation remains blocked.")
}

// OpenContinue selects the existing form's continuation section.
func (m *PanelModel) OpenContinue() { m.openSection("Continue") }
