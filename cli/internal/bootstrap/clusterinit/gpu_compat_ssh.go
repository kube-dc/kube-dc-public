package clusterinit

import (
	"context"
	"fmt"
	"sort"
	"strings"

	"github.com/shalb/kube-dc/cli/internal/bootstrap/discover"
	"github.com/shalb/kube-dc/cli/internal/bootstrap/ports"
)

// GPUHostResolver maps a Kubernetes node name from GPUConfig.NodeModes to the
// SSH endpoint used for read-only compatibility discovery. A nil resolver uses
// the node name as an ssh_config alias, matching the installer anchor workflow.
type GPUHostResolver func(node string) ports.SSHHost

// DiscoverAndValidateGPUHostsSSH gathers qualified facts from every enabled
// GPU node, then applies the same pure compatibility validator used by fixture
// tests. GPU-disabled and detect-only configurations remain network-free.
// Discovery is fail-closed for enabled products and performs no host writes.
func DiscoverAndValidateGPUHostsSSH(
	ctx context.Context,
	g GPUConfig,
	client ports.SSHClient,
	resolve GPUHostResolver,
) (map[string]discover.GPUHostInventory, error) {
	if g.Platform == "" || g.Platform == GPUPlatformDisabled || g.Platform == GPUPlatformDetectOnly {
		return map[string]discover.GPUHostInventory{}, nil
	}
	if client == nil {
		return nil, fmt.Errorf("%w: GPU host discovery requires an SSH client", ErrValidation)
	}
	if resolve == nil {
		resolve = func(node string) ports.SSHHost { return ports.SSHHost{Alias: node} }
	}

	nodes := make([]string, 0, len(g.NodeModes))
	for node, mode := range g.NodeModes {
		if mode != GPUNodeDisabled {
			nodes = append(nodes, node)
		}
	}
	sort.Strings(nodes)

	inventories := make(map[string]discover.GPUHostInventory, len(nodes))
	var discoveryErrs []error
	for _, node := range nodes {
		if err := ctx.Err(); err != nil {
			return nil, fmt.Errorf("GPU host discovery canceled: %w", err)
		}
		inv, err := discover.DiscoverGPUHostSSH(ctx, client, resolve(node))
		if err != nil {
			discoveryErrs = append(discoveryErrs, fmt.Errorf("node %s: %w", node, err))
			continue
		}
		inventories[node] = inv
	}
	if len(discoveryErrs) > 0 {
		return nil, fmt.Errorf("%w: %w", ErrValidation, gpuHostDiscoveryErrors(discoveryErrs))
	}
	if err := ValidateGPUHostCompatibility(g, inventories); err != nil {
		return nil, err
	}
	return inventories, nil
}

// gpuHostDiscoveryErrors preserves errors.Is for every node failure while
// keeping ErrValidation's operator-facing error on one deterministic line.
type gpuHostDiscoveryErrors []error

func (e gpuHostDiscoveryErrors) Error() string {
	parts := make([]string, 0, len(e))
	for _, err := range e {
		parts = append(parts, err.Error())
	}
	return strings.Join(parts, "; ")
}

func (e gpuHostDiscoveryErrors) Unwrap() []error { return []error(e) }
