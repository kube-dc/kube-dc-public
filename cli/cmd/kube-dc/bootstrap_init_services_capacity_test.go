package main

import (
	"context"
	"fmt"
	"strings"
	"testing"

	"github.com/shalb/kube-dc/cli/internal/bootstrap/clusterinit"
	"github.com/shalb/kube-dc/cli/internal/bootstrap/ports"
)

type capacitySSH struct{ responses map[string]string }

func (s capacitySSH) Run(_ context.Context, _ ports.SSHHost, command string) ([]byte, error) {
	for key, answer := range s.responses {
		if strings.Contains(command, key) {
			return []byte(answer), nil
		}
	}
	return nil, fmt.Errorf("unexpected capacity command: %s", command)
}
func (capacitySSH) Fetch(context.Context, ports.SSHHost, string) ([]byte, error) {
	return nil, fmt.Errorf("not used")
}
func (capacitySSH) Put(context.Context, ports.SSHHost, string, []byte, uint32) error {
	return fmt.Errorf("not used")
}

func TestProbeServicesBudgetBoundsSparseLoopByFreeSpace(t *testing.T) {
	o := &clusterinit.InitOptions{RookMode: clusterinit.RookCephLocal, RookOSDNode: "node-1", RookOSDDevice: "loop0", RookOSDSizeGB: 500, SSHHost: "root@node-1"}
	got, err := probeServicesBudget(context.Background(), o, capacitySSH{responses: map[string]string{"df -B1": "Avail\n107374182400\n"}})
	if err != nil || got != "25Gi" {
		t.Fatalf("sparse loop budget = %q, %v; want 25Gi", got, err)
	}
}

func TestProbeServicesBudgetUsesSelectedRawDisksAndCopies(t *testing.T) {
	o := &clusterinit.InitOptions{RookMode: clusterinit.RookCephMultiNode, PrimaryNode: "node-1", SSHHost: "root@node-1", NodeSSHHosts: map[string]string{"node-2": "root@node-2"}, CephNodes: map[string]string{"node-1": "sdb", "node-2": "sdc"}}
	got, err := probeServicesBudget(context.Background(), o, capacitySSH{responses: map[string]string{"/dev/sdb": "107374182400\n", "/dev/sdc": "107374182400\n"}})
	if err != nil || got != "25Gi" {
		t.Fatalf("two-disk replicated budget = %q, %v; want 25Gi", got, err)
	}
}

func TestProbeServicesBudgetSingleRawDiskUnits(t *testing.T) {
	for _, tc := range []struct {
		name  string
		bytes string
		want  string
	}{
		{name: "150 GiB", bytes: "161061273600\n", want: "37Gi"},
		{name: "150 GB", bytes: "150000000000\n", want: "34Gi"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			o := &clusterinit.InitOptions{RookMode: clusterinit.RookCephLocal, RookOSDNode: "node-1", RookOSDDevice: "sdb", SSHHost: "root@node-1"}
			got, err := probeServicesBudget(context.Background(), o, capacitySSH{responses: map[string]string{"/dev/sdb": tc.bytes}})
			if err != nil || got != tc.want {
				t.Fatalf("single-disk budget = %q, %v; want %s", got, err, tc.want)
			}
		})
	}
}
