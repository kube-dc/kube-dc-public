package tui

import (
	"time"

	"github.com/shalb/kube-dc/cli/internal/bootstrap/discover"
)

// FleetLoadedMsg is delivered when the fleet repo has been re-enumerated.
type FleetLoadedMsg struct {
	Clusters []discover.Cluster
	At       time.Time
}

// FleetErrorMsg surfaces a fleet-enumeration error to the screen layer.
type FleetErrorMsg struct {
	Err error
}

// ClusterProbeMsg is one cluster's probe outcome — sent independently
// for each row so the fleet view fills in incrementally as probes
// complete (slow clusters don't block fast ones).
type ClusterProbeMsg struct {
	Name   string
	Result discover.ProbeResult
	At     time.Time
}

// TickMsg fires on the periodic refresh timer.
type TickMsg struct{}

// LoginDoneMsg is sent after a tea.ExecProcess login subprocess
// returns. Cluster is the row whose login was triggered (so the
// receiving screen can re-probe just that row instead of all of them).
// Err is non-nil when the subprocess exited with a non-zero status.
type LoginDoneMsg struct {
	Cluster string
	Admin   bool
	Err     error
}

// AuthTestDoneMsg carries the result of a one-shot HEAD /readyz call
// against the API server using the operator's cached creds. Used by the
// context-manager screen's `t` key to verify a context actually works
// before kubectl is invoked.
type AuthTestDoneMsg struct {
	Context string
	OK      bool
	Detail  string // human-readable: "200 OK", "401 Unauthorized: token expired", etc.
}
