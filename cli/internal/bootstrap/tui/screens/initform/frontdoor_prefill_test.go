package initform

import (
	"strings"
	"testing"

	"github.com/shalb/kube-dc/cli/internal/bootstrap/clusterinit"
)

// The wizard must not launder the ambiguity the flag path refuses: opening it
// over a config that declares a VIP and no layer has to ASK, not answer.
func TestWizardDoesNotInventALayerForAnAmbiguousPrefill(t *testing.T) {
	o := &clusterinit.InitOptions{
		Yes: true, Name: "e2e", Domain: "e2e.example.com",
		NodeExternalIP: "203.0.113.52", Email: "ops@example.com",
		Mode: clusterinit.ModeInstall, Preset: clusterinit.PresetInternalOnly,
		Sets: map[string]string{
			"METALLB_FLOATING_IP":   "192.0.2.10",
			"METALLB_INTERFACE":     "br-ext-cloud",
			"EXT_NET_INTERFACE":     "enp1s0",
			"EXT_NET_VLAN_ID":       "0",
			"KUBE_OVN_MASTER_NODES": "10.77.0.22",
		},
	}
	st := initialState(o)
	if st.IngressAddressLayer != "" {
		t.Errorf("a prefill declaring a VIP but no layer must leave the field OPEN, got %q", st.IngressAddressLayer)
	}
	// And the panel must surface the actionable ambiguity error, not silence.
	m := NewPanelModel(st, "")
	m.st.OSMode = "rook-ceph-local"
	m.st.OSDNode, m.st.OSDSizeGB = "e2e-master-1", "40"
	joined := strings.Join(m.validationErrors(), " | ")
	if !strings.Contains(joined, "no address layer uses it") {
		t.Errorf("the wizard must show the ambiguity error, got: %s", joined)
	}
}

// A greenfield operator still gets the recommendation without being asked to
// research the question first.
func TestWizardGreenfieldGetsTheRecommendedLayer(t *testing.T) {
	st := initialState(&clusterinit.InitOptions{Sets: map[string]string{}})
	if st.IngressAddressLayer != clusterinit.AddressLayerMetalLBL2 {
		t.Errorf("greenfield default = %q, want %q", st.IngressAddressLayer, clusterinit.AddressLayerMetalLBL2)
	}
}
