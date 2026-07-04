package anchors

import (
	"context"
	"errors"
	"fmt"
	"sort"
	"strings"

	"github.com/shalb/kube-dc/cli/internal/bootstrap/ports"
)

// ApplyOptions parameterizes one Apply run.
//
// Anchors is the parsed map from ParseAnchorMap(EXT_NET_ANCHOR_IPS).
// Iface is EXT_NET_ANCHOR_INTERFACE (default "br-ext-cloud").
// DryRun=true makes Apply log the would-write actions without
// touching the network — used by `--dry-run` for operator preview.
// Resolver maps Kubernetes node names to SSHHost{Alias,Hostname}.
// nil falls back to the legacy alias-only behaviour, preserving
// compatibility with operators who use ~/.ssh/config aliases.
type ApplyOptions struct {
	Anchors  []Entry
	Iface    string
	DryRun   bool
	Resolver HostResolver
}

// NodeResult is the per-node outcome of one Apply pass.
//
// Wrote is true iff the script + unit landed AND `systemctl
// daemon-reload && systemctl enable --now` succeeded. Err captures
// the first failure (script Put / unit Put / daemon-reload / enable
// / verify) — partial state (e.g. script written but unit failed)
// shows up as Wrote=false + Err set; the operator re-runs after
// fixing.
type NodeResult struct {
	Host  string
	CIDR  string
	Wrote bool
	Err   error
}

// Result is the rolled-up Apply outcome. Failed is the count of
// nodes where Err != nil (zero on full success).
type Result struct {
	Nodes  []NodeResult
	Failed int
}

// Apply installs the anchor systemd unit on every node in
// opts.Anchors via ssh. Order is deterministic (ParseAnchorMap
// sorts by host); per-node failures don't short-circuit — the
// operator wants to see "srv5 ok, srv6 failed, srv7 ok" in one
// pass, not have to chase tail failures across re-runs.
//
// Returns a non-nil error only when the inputs themselves are bad
// (empty Iface, no anchors). Node-level failures live in
// Result.Nodes[i].Err with Result.Failed > 0; the caller decides
// whether to bubble or treat as partial success.
//
// **Idempotency**: each node receives the exact same script + unit
// every run; `install -m ...` is atomic + atomic-replace; `ip addr
// replace` is idempotent; `systemctl enable --now` is idempotent on
// an already-enabled-and-active unit. Re-running on a clean cluster
// is a near-no-op (writes are byte-identical, systemd notices and
// skips reload).
func Apply(ctx context.Context, ssh ports.SSHClient, opts ApplyOptions) (Result, error) {
	if ssh == nil {
		return Result{}, errors.New("anchors: Apply: nil SSH client")
	}
	if strings.TrimSpace(opts.Iface) == "" {
		return Result{}, errors.New("anchors: Apply: empty interface (set EXT_NET_ANCHOR_INTERFACE)")
	}
	if len(opts.Anchors) == 0 {
		return Result{}, errors.New("anchors: Apply: no anchor entries (EXT_NET_ANCHOR_IPS empty)")
	}

	// Defensive sort — callers should pass already-sorted slices from
	// ParseAnchorMap, but a hand-built slice from tests/scripts might
	// not be sorted. Determinism > caller trust.
	entries := make([]Entry, len(opts.Anchors))
	copy(entries, opts.Anchors)
	sort.Slice(entries, func(i, j int) bool { return entries[i].Host < entries[j].Host })

	script := []byte(RenderBindScript())
	resolver := opts.Resolver
	if resolver == nil {
		resolver = NewHostResolver(nil) // legacy alias-only
	}

	res := Result{Nodes: make([]NodeResult, 0, len(entries))}
	for _, e := range entries {
		nr := NodeResult{Host: e.Host, CIDR: e.CIDR}
		if err := applyOne(ctx, ssh, e, opts.Iface, script, opts.DryRun, resolver); err != nil {
			nr.Err = err
			res.Failed++
		} else {
			nr.Wrote = true
		}
		res.Nodes = append(res.Nodes, nr)
	}
	return res, nil
}

// applyOne is the per-node body of Apply. Extracted so the caller
// loop stays readable + each step's error surfaces with a clear
// "what failed on which node" message.
func applyOne(ctx context.Context, ssh ports.SSHClient, e Entry, iface string, script []byte, dryRun bool, resolver HostResolver) error {
	if dryRun {
		// Dry-run: skip every side-effecting call. The caller's
		// reporter logs the would-write actions.
		return nil
	}

	host := resolver(e.Host)

	if err := ssh.Put(ctx, host, RemoteScriptPath, script, ScriptMode); err != nil {
		return fmt.Errorf("put bind script: %w", err)
	}

	unit, err := RenderUnit(iface, e.CIDR)
	if err != nil {
		return fmt.Errorf("render unit: %w", err)
	}
	if err := ssh.Put(ctx, host, RemoteUnitPath, []byte(unit), UnitMode); err != nil {
		return fmt.Errorf("put systemd unit: %w", err)
	}

	// daemon-reload picks up the unit file change; enable --now both
	// enables persistence (symlink in multi-user.target.wants) AND
	// starts the unit (which binds the anchor immediately).
	if _, err := ssh.Run(ctx, host,
		fmt.Sprintf("systemctl daemon-reload && systemctl enable --now %s", UnitName)); err != nil {
		return fmt.Errorf("systemctl enable: %w", err)
	}

	// Verify: the unit reports active AND the CIDR is bound. Either
	// failure is a configuration drift we want to surface here.
	// Defense-in-depth shell-escaping: iface and CIDR are validated
	// upstream (validateNICName + net.ParseCIDR in preset.go), but
	// the verify command runs as root via SSH, so apply the same
	// shellQuote helper that probe.go already uses.
	if _, err := ssh.Run(ctx, host,
		fmt.Sprintf("systemctl is-active --quiet %s && ip -4 addr show %s | grep -wq %s",
			UnitName, shellQuote(iface), shellQuote(e.CIDR))); err != nil {
		return fmt.Errorf("verify: %w", err)
	}
	return nil
}

// shellQuote single-quotes a string for safe embedding in a remote
// shell command. Mirrors the helper in probe.go (kept duplicated so
// each file is self-contained for review). The standard POSIX
// escape: end the quote, insert `\'`, restart the quote.
func shellQuote(s string) string {
	return "'" + strings.ReplaceAll(s, "'", `'\''`) + "'"
}
