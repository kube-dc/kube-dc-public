package main

import (
	"fmt"
	"path/filepath"

	"github.com/shalb/kube-dc/cli/internal/bootstrap/clusterinit"
	"github.com/shalb/kube-dc/cli/internal/bootstrap/ports"
)

// validateRawOSDApplyInputs runs before the apply engine can modify a host or
// Fleet checkout. An existing cluster keeps its selected disks, which are
// necessarily occupied; a new cluster must recheck empty disks over SSH.
func validateRawOSDApplyInputs(o *clusterinit.InitOptions, sshEnabled bool) error {
	rawDevices := o.ObjectStorage().RawOSDDevices()
	switch o.Mode {
	case clusterinit.ModeInstall:
		if len(rawDevices) == 0 {
			return nil
		}
		if !sshEnabled {
			return fmt.Errorf("raw Ceph devices require verified SSH access for the device safety check")
		}
		if o.RookMode == clusterinit.RookCephMultiNode && o.PrimaryNode == "" {
			return fmt.Errorf("multi-node raw Ceph devices require --primary-node to map the primary SSH host")
		}
		for _, disk := range rawDevices {
			if _, err := rawOSDTarget(o, disk[0]); err != nil {
				return err
			}
		}
	case clusterinit.ModeAdopt, clusterinit.ModeResume:
		if err := clusterinit.RequireUnchangedRawOSDSelection(
			filepath.Join(o.Repo, "clusters", o.Name, "cluster-config.env"), o.ObjectStorage()); err != nil {
			return fmt.Errorf("raw Ceph device review: %w", err)
		}
	default:
		if len(rawDevices) > 0 {
			return fmt.Errorf("raw Ceph devices require a resolved install, adopt, or resume mode")
		}
	}
	return nil
}

// rawOSDTarget binds a selected Kubernetes node to the SSH endpoint reviewed
// for that node. The local one-disk mode has only one possible primary node;
// multi-node installs name it explicitly or supply per-node targets.
func rawOSDTarget(o *clusterinit.InitOptions, node string) (ports.SSHHost, error) {
	primary := o.PrimaryNode
	if primary == "" && o.RookMode == clusterinit.RookCephLocal {
		primary = o.RookOSDNode
	}
	if primary != "" && node == primary {
		if mapped := o.NodeSSHHosts[node]; mapped != "" && mapped != o.SSHHost {
			return ports.SSHHost{}, fmt.Errorf("primary node %s has two different SSH targets", node)
		}
		if o.SSHHost == "" {
			return ports.SSHHost{}, fmt.Errorf("primary node %s has no SSH target", node)
		}
		if pin := o.NodeSSHHostKeys[node]; pin != "" && pin != o.SSHHostKeySHA256 {
			return ports.SSHHost{}, fmt.Errorf("primary node %s has two different SSH host-key pins", node)
		}
		return initSSHHost(o), nil
	}
	alias := o.NodeSSHHosts[node]
	if alias == "" {
		return ports.SSHHost{}, fmt.Errorf("raw disk node %s needs --node-ssh-host %s=user@host", node, node)
	}
	target := parseSSHHostArg(alias)
	target.ExpectedHostKeySHA256 = o.NodeSSHHostKeys[node]
	return target, nil
}
