package initform

import (
	"strings"
	"testing"
)

func probeFacts() []ProbeNodeFact {
	return []ProbeNodeFact{
		{Name: "master-2", InternalIP: "192.0.2.2", ControlPlane: true},
		{Name: "master-1", InternalIP: "192.0.2.1", ControlPlane: true},
		{Name: "worker-1", InternalIP: "192.0.2.10", ControlPlane: false},
	}
}

func TestBuildProbePrefill_SortsAndFilters(t *testing.T) {
	p := BuildProbePrefill(probeFacts())
	if got := strings.Join(p.ControlPlaneIPs, ","); got != "192.0.2.1,192.0.2.2" {
		t.Errorf("ControlPlaneIPs = %q (want sorted CP-only)", got)
	}
	if got := strings.Join(p.AllNodeNames, ","); got != "master-1,master-2,worker-1" {
		t.Errorf("AllNodeNames = %q (want sorted all)", got)
	}
}

func TestBuildProbePrefill_SkipsIPlessNodes(t *testing.T) {
	// A half-registered CP node without an InternalIP must not produce
	// an empty element in the comma-joined MASTER_NODES value.
	p := BuildProbePrefill([]ProbeNodeFact{
		{Name: "master-1", InternalIP: "192.0.2.1", ControlPlane: true},
		{Name: "master-2", InternalIP: "", ControlPlane: true},
	})
	if got := strings.Join(p.ControlPlaneIPs, ","); got != "192.0.2.1" {
		t.Errorf("ControlPlaneIPs = %q (IP-less node must be skipped)", got)
	}
	if len(p.AllNodeNames) != 2 {
		t.Errorf("AllNodeNames should keep the IP-less node's NAME: %v", p.AllNodeNames)
	}
}

func TestProbePrefill_FillsOnlyEmptyFields(t *testing.T) {
	p := BuildProbePrefill(probeFacts())

	// Empty state → both fields filled, two notes.
	st := &State{}
	notes := p.ApplyTo(st)
	if st.KubeOVNMasterNodes != "192.0.2.1,192.0.2.2" {
		t.Errorf("KubeOVNMasterNodes = %q", st.KubeOVNMasterNodes)
	}
	if st.GWNodes != "master-1,master-2,worker-1" {
		t.Errorf("GWNodes = %q", st.GWNodes)
	}
	if len(notes) != 2 {
		t.Errorf("want 2 notes, got %v", notes)
	}

	// Operator-supplied values (config/env/flags landed via FromOptions)
	// must NEVER be clobbered — probe is the lowest-precedence source.
	st = &State{KubeOVNMasterNodes: "10.0.0.1", GWNodes: "gw-a"}
	notes = p.ApplyTo(st)
	if st.KubeOVNMasterNodes != "10.0.0.1" || st.GWNodes != "gw-a" {
		t.Errorf("probe clobbered operator values: %+v", st)
	}
	if len(notes) != 0 {
		t.Errorf("no notes expected when nothing filled, got %v", notes)
	}
}

func TestProbePrefill_EmptyPrefillNoOp(t *testing.T) {
	var p ProbePrefill
	st := &State{}
	if notes := p.ApplyTo(st); len(notes) != 0 || st.KubeOVNMasterNodes != "" || st.GWNodes != "" {
		t.Errorf("empty prefill must be a no-op (notes=%v st=%+v)", notes, st)
	}
	// nil state must not panic.
	if notes := p.ApplyTo(nil); notes != nil {
		t.Errorf("nil state: want nil notes, got %v", notes)
	}
}
