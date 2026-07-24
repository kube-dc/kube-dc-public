package initform

import (
	"fmt"
	"sort"
	"strings"
)

// Probe-driven prefill (the T6 deferred polish item, OS-5 §7 direction).
//
// When `bootstrap init` opens the panel with a reachable cluster on the
// ambient kubeconfig, the two topology fields every install needs —
// KUBE_OVN_MASTER_NODES (control-plane InternalIPs; the #1 "always
// --set it" key) and KUBE_OVN_GW_NODES (gateway node names) — can be
// read off the live cluster instead of hand-typed. The cobra layer
// gathers facts best-effort (short timeout, any error → no prefill);
// this file is the PURE application of those facts onto the panel State.
//
// Precedence contract: probe values fill ONLY fields that are still
// empty after FromOptions overlaid --config / KUBE_DC_INIT_* / flags —
// i.e. defaults < probe < config < env < flags < TUI edits. A probe
// must never clobber anything the operator said explicitly.

// ProbeNodeFact mirrors adapters/k8s.NodeNetFact without importing the
// adapter (the TUI stays adapter-free; cobra maps between the types).
type ProbeNodeFact struct {
	Name         string
	InternalIP   string
	ControlPlane bool
}

// ProbePrefill is the panel-facing digest of the node facts.
type ProbePrefill struct {
	// ControlPlaneIPs are the InternalIPs of control-plane nodes,
	// sorted — the KUBE_OVN_MASTER_NODES prefill.
	ControlPlaneIPs []string
	// AllNodeNames are every node's name, sorted — the
	// KUBE_OVN_GW_NODES prefill (small clusters run gateways on all
	// nodes; larger topologies edit the field down).
	AllNodeNames []string
}

// BuildProbePrefill digests raw node facts. Nodes without an InternalIP
// are skipped for the IP list (a half-registered node must not produce
// an empty element in KUBE_OVN_MASTER_NODES — the env value is split on
// commas by the fleet templates and an empty element breaks kube-ovn's
// MASTER_NODES flag).
func BuildProbePrefill(facts []ProbeNodeFact) ProbePrefill {
	var p ProbePrefill
	for _, f := range facts {
		if f.Name != "" {
			p.AllNodeNames = append(p.AllNodeNames, f.Name)
		}
		if f.ControlPlane && f.InternalIP != "" {
			p.ControlPlaneIPs = append(p.ControlPlaneIPs, f.InternalIP)
		}
	}
	sort.Strings(p.ControlPlaneIPs)
	sort.Strings(p.AllNodeNames)
	return p
}

// ApplyTo fills the State's still-empty topology fields and returns
// one human-readable note per field it filled (rendered in the panel
// hint line so the operator knows the values came from the live
// cluster, not from their own input). Empty prefill = no-op.
func (p ProbePrefill) ApplyTo(st *State) []string {
	if st == nil {
		return nil
	}
	var notes []string
	if strings.TrimSpace(st.KubeOVNMasterNodes) == "" && len(p.ControlPlaneIPs) > 0 {
		st.KubeOVNMasterNodes = strings.Join(p.ControlPlaneIPs, ",")
		notes = append(notes, fmt.Sprintf(
			"masters=%s (%d control-plane node(s) probed)",
			st.KubeOVNMasterNodes, len(p.ControlPlaneIPs)))
	}
	if strings.TrimSpace(st.GWNodes) == "" && len(p.AllNodeNames) > 0 {
		st.GWNodes = strings.Join(p.AllNodeNames, ",")
		notes = append(notes, fmt.Sprintf(
			"gw-nodes=%s (all probed nodes; trim for larger topologies)",
			st.GWNodes))
	}
	return notes
}
