package clusterinit

import (
	"bytes"
	"context"
	"errors"
	"strings"
	"testing"
)

// fakeNodeLabels is a hermetic discover.NodeLabelsProvider. Tests
// wire the labels map directly against node names. The err field is
// returned verbatim when non-nil so tests can exercise the
// "target-cluster-not-reachable" soft-skip path.
type fakeNodeLabels struct {
	labels map[string]map[string]string
	err    error
}

func (f *fakeNodeLabels) NodeLabels(_ context.Context) (map[string]map[string]string, error) {
	if f.err != nil {
		return nil, f.err
	}
	return f.labels, nil
}

// TestCheckKubeVirtEligibility_NilK8s — soft-skip when the session
// hasn't wired a K8sClient (fresh-RKE2 pre-Apply / no kubeconfig).
// The gate must NOT return an error here — Apply is what will land
// the cluster in the first place.
func TestCheckKubeVirtEligibility_NilK8s(t *testing.T) {
	var out bytes.Buffer
	err := CheckKubeVirtEligibility(context.Background(), NFDGateOptions{
		K8s: nil,
		Out: &out,
	})
	if err != nil {
		t.Fatalf("nil K8s should soft-skip, got %v", err)
	}
	if !strings.Contains(out.String(), "no K8sClient") {
		t.Errorf("expected 'no K8sClient' skip note, got:\n%s", out.String())
	}
}

// TestCheckKubeVirtEligibility_ProbeError — soft-skip when NFDDetect
// errors (target cluster unreachable at Apply-time). The gate is a
// preflight, not a hard integration check; the operator will see the
// real cluster-up error from Apply itself.
func TestCheckKubeVirtEligibility_ProbeError(t *testing.T) {
	var out bytes.Buffer
	err := CheckKubeVirtEligibility(context.Background(), NFDGateOptions{
		K8s: &fakeNodeLabels{err: errors.New("connection refused")},
		Out: &out,
	})
	if err != nil {
		t.Fatalf("probe error should soft-skip, got %v", err)
	}
	if !strings.Contains(out.String(), "connection refused") {
		t.Errorf("expected skip note to include the probe error, got:\n%s", out.String())
	}
}

// TestCheckKubeVirtEligibility_NFDAbsent — soft-skip on a cluster
// with nodes but no NFD labels (pre-infra-core Apply state). This is
// the expected pre-Apply state on a fresh install — Apply installs
// NFD via M6-T01 — so refusing here would deadlock the install.
func TestCheckKubeVirtEligibility_NFDAbsent(t *testing.T) {
	var out bytes.Buffer
	err := CheckKubeVirtEligibility(context.Background(), NFDGateOptions{
		K8s: &fakeNodeLabels{labels: map[string]map[string]string{
			"srv1": {"node-role.kubernetes.io/control-plane": "true"},
			"srv2": {},
		}},
		Out: &out,
	})
	if err != nil {
		t.Fatalf("NFD-absent should soft-skip, got %v", err)
	}
	if !strings.Contains(out.String(), "NFD not installed yet") {
		t.Errorf("expected 'NFD not installed yet' skip note, got:\n%s", out.String())
	}
}

// TestCheckKubeVirtEligibility_HappyPath — canonical PASS. NFD +
// composites both installed, at least one node kubevirt-eligible.
func TestCheckKubeVirtEligibility_HappyPath(t *testing.T) {
	var out bytes.Buffer
	err := CheckKubeVirtEligibility(context.Background(), NFDGateOptions{
		K8s: &fakeNodeLabels{labels: map[string]map[string]string{
			"srv1": {"kube-dc.com/kubevirt-eligible": "true"},
			"srv2": {"kube-dc.com/kubevirt-eligible": "true"},
			"srv3": {"kube-dc.com/gpu.nvidia": "true"},
		}},
		Out: &out,
	})
	if err != nil {
		t.Fatalf("happy path should pass, got %v", err)
	}
	body := out.String()
	if !strings.Contains(body, "NFD gate: PASS") {
		t.Errorf("expected PASS in log, got:\n%s", body)
	}
	if !strings.Contains(body, "2/3 nodes KubeVirt-eligible") {
		t.Errorf("expected '2/3 nodes KubeVirt-eligible' count, got:\n%s", body)
	}
	if !strings.Contains(body, "M6-T02 composites") {
		t.Errorf("expected 'M6-T02 composites' source, got:\n%s", body)
	}
}

// TestCheckKubeVirtEligibility_ZeroEligibleBlocks — the whole point
// of the gate. Composites are applied but say NO nodes can host VMs
// (e.g., an operator installed kube-dc onto a bare-metal cluster
// where every node's /dev/kvm isn't exposed). Gate MUST refuse.
func TestCheckKubeVirtEligibility_ZeroEligibleBlocks(t *testing.T) {
	var out bytes.Buffer
	err := CheckKubeVirtEligibility(context.Background(), NFDGateOptions{
		K8s: &fakeNodeLabels{labels: map[string]map[string]string{
			"srv1": {"kube-dc.com/gpu.nvidia": "true"},
			"srv2": {"kube-dc.com/gpu.amd": "true"},
		}},
		Out: &out,
	})
	if err == nil {
		t.Fatal("want ErrNoKubeVirtEligibleNodes, got nil")
	}
	if !errors.Is(err, ErrNoKubeVirtEligibleNodes) {
		t.Errorf("want ErrNoKubeVirtEligibleNodes, got: %v", err)
	}
	msg := err.Error()
	if !strings.Contains(msg, "0 of 2 nodes") {
		t.Errorf("error should quote the count, got: %v", err)
	}
	if !strings.Contains(msg, "--allow-no-kubevirt-eligible") {
		t.Errorf("error should mention the escape-hatch flag, got: %v", err)
	}
}

// TestCheckKubeVirtEligibility_ZeroEligibleButAllowFlag — same
// failing state, but the operator opted in via
// --allow-no-kubevirt-eligible. Gate MUST pass and log the
// "workloads will fail" warning.
func TestCheckKubeVirtEligibility_ZeroEligibleButAllowFlag(t *testing.T) {
	var out bytes.Buffer
	err := CheckKubeVirtEligibility(context.Background(), NFDGateOptions{
		K8s: &fakeNodeLabels{labels: map[string]map[string]string{
			"srv1": {"kube-dc.com/gpu.nvidia": "true"},
		}},
		AllowNoKubevirtEligible: true,
		Out:                     &out,
	})
	if err != nil {
		t.Fatalf("allow-flag should override the block, got %v", err)
	}
	body := out.String()
	if !strings.Contains(body, "--allow-no-kubevirt-eligible set") {
		t.Errorf("expected escape-hatch log, got:\n%s", body)
	}
	if !strings.Contains(body, "will fail to schedule") {
		t.Errorf("expected 'will fail to schedule' warning, got:\n%s", body)
	}
}

// TestCheckKubeVirtEligibility_RawFallback — NFD is installed
// (feature.node.kubernetes.io/* labels present) but the M6-T02
// composites are not. Gate uses the raw-fallback derivation and
// still enforces the >=1 rule. Source is named so the operator sees
// they're running an M6-T02-less install shape.
func TestCheckKubeVirtEligibility_RawFallback(t *testing.T) {
	var out bytes.Buffer
	err := CheckKubeVirtEligibility(context.Background(), NFDGateOptions{
		K8s: &fakeNodeLabels{labels: map[string]map[string]string{
			"srv1": {
				"feature.node.kubernetes.io/cpu-cpuid.VMX":           "true",
				"feature.node.kubernetes.io/kernel-loadedmodule.kvm": "true",
			},
			"srv2": {
				"feature.node.kubernetes.io/cpu-model.family": "6",
			},
		}},
		Out: &out,
	})
	if err != nil {
		t.Fatalf("raw-fallback with 1 eligible node should pass, got %v", err)
	}
	body := out.String()
	if !strings.Contains(body, "NFD gate: PASS") {
		t.Errorf("expected PASS in log, got:\n%s", body)
	}
	if !strings.Contains(body, "raw NFD labels (composites absent") {
		t.Errorf("expected raw-fallback source note, got:\n%s", body)
	}
	if !strings.Contains(body, "1/2 nodes KubeVirt-eligible") {
		t.Errorf("expected '1/2 nodes KubeVirt-eligible' count, got:\n%s", body)
	}
}

// TestCheckKubeVirtEligibility_NilOut — nil Out must not crash. Gate
// falls back to io.Discard.
func TestCheckKubeVirtEligibility_NilOut(t *testing.T) {
	err := CheckKubeVirtEligibility(context.Background(), NFDGateOptions{
		K8s: &fakeNodeLabels{labels: map[string]map[string]string{
			"srv1": {"kube-dc.com/kubevirt-eligible": "true"},
		}},
		Out: nil,
	})
	if err != nil {
		t.Fatalf("nil Out should not error, got %v", err)
	}
}
