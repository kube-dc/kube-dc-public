package gputransition

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

const nodeFeatureFixture = `# generated
apiVersion: nfd.k8s-sigs.io/v1alpha1
kind: NodeFeatureRule
spec:
  rules:
    - name: gpu-a-pod-hami
      labels:
        kube-dc.com/gpu.workload-mode: pod-hami
        kube-dc.com/gpu.expected-workload-mode: pod-hami
        nvidia.com/gpu.workload.config: container
      matchFeatures:
        - feature: system.name
          matchExpressions:
            nodename:
              op: In
              value: ["gpu-a"]
        - feature: pci.device
          matchExpressions:
            vendor: {op: In, value: ["10de"]}
    - name: gpu-b-pod-hami
      labels:
        kube-dc.com/gpu.workload-mode: pod-hami
        kube-dc.com/gpu.expected-workload-mode: pod-hami
        nvidia.com/gpu.workload.config: container
      matchFeatures:
        - feature: system.name
          matchExpressions:
            nodename:
              op: In
              value: ["gpu-b"]
`

func TestWriteNodeModeChangesOnlyExactNodeAndRoundTrips(t *testing.T) {
	path := filepath.Join(t.TempDir(), "nodefeaturerule.yaml")
	if err := os.WriteFile(path, []byte(nodeFeatureFixture), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := WriteNodeMode(path, "gpu-a", ModePodHAMi, ModeVMPassthrough); err != nil {
		t.Fatal(err)
	}
	mode, err := ReadNodeMode(path, "gpu-a")
	if err != nil || mode != ModeVMPassthrough {
		t.Fatalf("mode=%q err=%v", mode, err)
	}
	body, _ := os.ReadFile(path)
	if !strings.Contains(string(body), "gpu-a-vm-passthrough") || !strings.Contains(string(body), "nvidia.com/gpu.workload.config: vm-passthrough") {
		t.Fatalf("target block not updated:\n%s", body)
	}
	if strings.Count(string(body), "gpu-b-pod-hami") != 1 || strings.Count(string(body), "nvidia.com/gpu.workload.config: container") != 1 {
		t.Fatalf("other node changed:\n%s", body)
	}
	if err := WriteNodeMode(path, "gpu-a", ModeVMPassthrough, ModePodHAMi); err != nil {
		t.Fatal(err)
	}
	mode, err = ReadNodeMode(path, "gpu-a")
	if err != nil || mode != ModePodHAMi {
		t.Fatalf("round-trip mode=%q err=%v", mode, err)
	}
}

func TestNodeModeEditorFailsClosedOnDriftAbsentAndAmbiguous(t *testing.T) {
	tests := []struct{ name, body, node, want string }{
		{"from drift", nodeFeatureFixture, "gpu-a", "drift"},
		{"absent", nodeFeatureFixture, "gpu-c", "absent"},
		{"ambiguous", nodeFeatureFixture + nodeFeatureFixture, "gpu-a", "ambiguous"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			path := filepath.Join(t.TempDir(), "rule.yaml")
			if err := os.WriteFile(path, []byte(tt.body), 0o644); err != nil {
				t.Fatal(err)
			}
			from := ModePodHAMi
			if tt.name == "from drift" {
				from = ModeVMPassthrough
			}
			err := WriteNodeMode(path, tt.node, from, ModeVMPassthrough)
			if err == nil || !strings.Contains(err.Error(), tt.want) {
				t.Fatalf("want %q error, got %v", tt.want, err)
			}
		})
	}
}
