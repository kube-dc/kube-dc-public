// Package systemctl is the real ports.SystemctlClient adapter. It
// shells out to `systemctl` via os/exec. The adapter is used by M1-T02
// host probes (`systemctl is-active rke2-server` on the operator's
// laptop or directly on a node) and — sparingly — by M4-T07 prereq
// installation when a unit must be restarted.
//
// **Privilege model**: read paths (`is-active`) work as any user.
// Mutation paths (`restart`) require root. The adapter checks `geteuid`
// up front and surfaces `ports.ErrNeedsSudo` before invoking systemctl
// — that way the CLI can present a clean "this step needs sudo" prompt
// instead of letting systemctl prompt for a polkit password from a
// non-TTY context (which would hang or fail opaquely).
package systemctl

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"strings"

	"github.com/shalb/kube-dc/cli/internal/bootstrap/ports"
)

// Client implements ports.SystemctlClient.
type Client struct {
	// exec is the hook the adapter goes through for every systemctl
	// invocation. Production uses runReal; tests inject canned outputs.
	exec func(ctx context.Context, args ...string) ([]byte, []byte, error)

	// geteuid is the euid check hook. Production uses os.Geteuid; tests
	// inject a constant so the privilege-gate test runs identically as
	// root and as a regular user.
	geteuid func() int
}

// New returns a Client wired against the real systemctl binary.
func New() *Client {
	return &Client{
		exec:    runReal,
		geteuid: os.Geteuid,
	}
}

// IsActive runs `systemctl is-active <unit>` and applies the contract
// from ports.SystemctlClient: active means exit 0 AND stdout starts
// with "active". Any other output (inactive / failed / activating /
// unknown) returns (false, <sub-state>, nil). Errors come back only
// for transport-level failures (binary missing, ctx cancelled).
func (c *Client) IsActive(ctx context.Context, unit string) (bool, string, error) {
	if unit == "" {
		return false, "", fmt.Errorf("systemctl: empty unit name")
	}

	stdout, _, err := c.exec(ctx, "is-active", unit)
	state := strings.TrimSpace(string(stdout))

	if err != nil {
		// systemctl exits non-zero for any non-"active" state. That's
		// not a transport error — it's a normal "inactive"/"failed"
		// signal. ExitError → swallow; everything else → surface.
		var ee *exec.ExitError
		if !errors.As(err, &ee) {
			return false, state, fmt.Errorf("systemctl is-active %s: %w", unit, err)
		}
	}

	active := state == "active"
	return active, state, nil
}

// Restart runs `systemctl restart <unit>`. Refuses up front when the
// process isn't root, surfacing ports.ErrNeedsSudo. Real failures
// (unit not found, dependency cycle, ...) come back wrapped with the
// systemctl stderr.
func (c *Client) Restart(ctx context.Context, unit string) error {
	if unit == "" {
		return fmt.Errorf("systemctl: empty unit name")
	}
	if c.geteuid() != 0 {
		return fmt.Errorf("systemctl restart %s: %w", unit, ports.ErrNeedsSudo)
	}

	_, stderr, err := c.exec(ctx, "restart", unit)
	if err != nil {
		// Pass stderr through so the operator sees the actual systemd
		// reason (e.g. "Unit not found.", "Failed to start ...").
		return fmt.Errorf("systemctl restart %s: %w\nstderr: %s", unit, err, strings.TrimSpace(string(stderr)))
	}
	return nil
}

// runReal is the production exec hook. Separated so tests can swap it
// without touching the public Client API.
func runReal(ctx context.Context, args ...string) ([]byte, []byte, error) {
	cmd := exec.CommandContext(ctx, "systemctl", args...)
	var stdout, stderr bytes.Buffer
	cmd.Stdout = &stdout
	cmd.Stderr = &stderr
	err := cmd.Run()
	return stdout.Bytes(), stderr.Bytes(), err
}

