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

func (m *PanelModel) sessionFields() []panelField {
	action := func(label, id, desc string) panelField {
		return panelField{Section: "Continue", Label: label, Action: id, Kind: panelAction, Desc: desc}
	}
	return []panelField{
		action("Inspect session and targets", "session-inspect", "Read saved evidence and remote ownership. No installation changes."),
		action("Reserve reviewed hosts", "session-claim", "Save ownership intent, recheck reviewed inputs, and reserve all hosts for 30 minutes. Installation stays blocked."),
		action("Take over expired unused reservations", "session-takeover", "Explicit takeover checks every host again. Started or active claims cannot be taken over."),
		action("Release unused reservations", "session-release", "Release only this run's reservations that have admitted no effects."),
		action("Show installation gates", "session-gates", "Full platform effects, release qualification, and recovery must pass before installation can run."),
	}
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
	if (action == "session-claim" || action == "session-takeover") && w.sessionInputHash != m.stateHash() {
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
	e := PanelEvent{Owner: m, generation: w.generation, kind: "session", hash: m.stateHash()}
	return func() tea.Msg {
		switch action {
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
	if w.sessionInputHash != m.stateHash() {
		lines = append(lines, "Settings changed. Reserve is blocked until you prepare a new review.")
	}
	if !w.sessionInspected {
		return append(lines, "Inspect the session to read saved evidence and current host claims.")
	}
	status := w.sessionStatus
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
