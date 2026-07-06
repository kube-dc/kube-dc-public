package tui

import (
	"testing"
	"time"

	tea "charm.land/bubbletea/v2"

	"github.com/shalb/kube-dc/cli/internal/alerts"
)

// sampleAlerts is a fixed 3-alert set (≥2 so a list-cursor move is
// observable) used to populate a model without a network client.
func sampleAlerts() []alerts.Alert {
	now := time.Now()
	return []alerts.Alert{
		{Fingerprint: "fp1", AlertName: "HighCPU", Severity: "critical", State: "active",
			StartsAt:    now.Add(-10 * time.Minute),
			Labels:      map[string]string{"alertname": "HighCPU", "severity": "critical", "namespace": "monitoring"},
			Annotations: map[string]string{"summary": "cpu is high"}},
		{Fingerprint: "fp2", AlertName: "DiskWarn", Severity: "warning", State: "active",
			StartsAt:    now.Add(-5 * time.Minute),
			Labels:      map[string]string{"alertname": "DiskWarn", "severity": "warning", "namespace": "storage"},
			Annotations: map[string]string{"summary": "disk filling"}},
		{Fingerprint: "fp3", AlertName: "InfoPing", Severity: "info", State: "active",
			StartsAt:    now.Add(-1 * time.Minute),
			Labels:      map[string]string{"alertname": "InfoPing", "severity": "info"},
			Annotations: map[string]string{"summary": "fyi"}},
	}
}

// loadedModel returns a sized model pre-populated with sampleAlerts via a
// loadedMsg — no client, no network (Init is never run, so loadCmd never
// touches the nil client).
func loadedModel(t *testing.T) *Model {
	t.Helper()
	m := NewModel(nil, "test", nil)
	m.Update(tea.WindowSizeMsg{Width: 120, Height: 40})
	m.Update(loadedMsg{alerts: sampleAlerts(), at: time.Now()})
	return m
}

// TestFocusRouting_DetailsFocusDoesNotMoveSelection is the P2 regression
// guard: after Space toggles focus to the details pane, ↑/↓ must scroll
// the details viewport, NOT move the alert-list selection. Before the fix
// the routing sent plain up/down to the list regardless of focus, so
// "details focus" was visual-only and arrows still moved the selection.
func TestFocusRouting_DetailsFocusDoesNotMoveSelection(t *testing.T) {
	m := loadedModel(t)

	// Sanity: with the LIST focused (default), Down moves the selection.
	if got := m.list.Index(); got != 0 {
		t.Fatalf("expected initial selection 0, got %d", got)
	}
	m.Update(tea.KeyPressMsg{Code: tea.KeyDown})
	if got := m.list.Index(); got != 1 {
		t.Fatalf("list-focused Down should move selection 0→1, got %d", got)
	}

	// Space focuses the details pane…
	m.Update(tea.KeyPressMsg{Code: tea.KeySpace})
	if m.focus != focusDetails {
		t.Fatalf("Space should focus the details pane, focus=%v", m.focus)
	}
	// …and now Down must NOT move the alert selection (it scrolls details).
	before := m.list.Index()
	m.Update(tea.KeyPressMsg{Code: tea.KeyDown})
	if got := m.list.Index(); got != before {
		t.Errorf("details-focused Down must NOT move the alert selection (was %d, now %d)", before, got)
	}
}

// TestEnterFocusesDetails is the P3 guard: the keymap advertises Enter as
// "details", but before the fix handleKey had no Enter case (a no-op).
// Enter now focuses the details pane (Esc returns to the list).
func TestEnterFocusesDetails(t *testing.T) {
	m := loadedModel(t)
	if m.focus != focusList {
		t.Fatalf("expected initial focus on the list, got %v", m.focus)
	}
	m.Update(tea.KeyPressMsg{Code: tea.KeyEnter})
	if m.focus != focusDetails {
		t.Errorf("Enter should focus the details pane, focus=%v", m.focus)
	}
	// Esc returns focus to the list (the documented inverse).
	m.Update(tea.KeyPressMsg{Code: tea.KeyEsc})
	if m.focus != focusList {
		t.Errorf("Esc should return focus to the list, focus=%v", m.focus)
	}
}
