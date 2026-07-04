package ports

import "context"

// NetplanClient is the contract for snapshotting and restoring
// `/etc/netplan/` configurations on the host. Used by M1-T02 host probes
// and (more importantly) by the v2 `bootstrap install` host-prep flow
// where a misapplied netplan change could lock the operator out of the
// node.
//
// Real adapter reads/writes `/etc/netplan/*.yaml` directly (requires
// sudo). Mock adapter returns a fixture snapshot.
//
// The v2 install flow takes a snapshot before any network change and
// keeps it as a rollback target — if the operator loses SSH connectivity
// to the node within 60s of `netplan apply`, the CLI restores the
// snapshot. This is a host-recovery contract, not a probe contract.
type NetplanClient interface {
	// Snapshot writes a copy of every `*.yaml` in `/etc/netplan/` to
	// `dst` (a directory; created if missing). Returns the list of
	// files snapshotted (relative paths within dst) so the caller can
	// inventory the rollback target.
	Snapshot(ctx context.Context, dst string) ([]string, error)

	// Restore restores files from `src` into `/etc/netplan/` and runs
	// `netplan apply`. Used as the rollback target by the v2 install
	// flow when post-change SSH reachability is lost.
	Restore(ctx context.Context, src string) error
}
