// Package netplan is the real ports.NetplanClient adapter. It owns
// the snapshot/restore contract used by the v2 `bootstrap install`
// host-prep flow: take a copy of /etc/netplan/ before any change, then
// restore it as the rollback target if the operator loses SSH within
// the post-change watchdog window. In v1 the adapter only surfaces
// from M1-T02 host probes for the /etc/netplan/ inventory.
//
// All write paths require root; the adapter checks euid up front and
// returns ports.ErrNeedsSudo before touching the filesystem. Read
// paths (Snapshot's source-side enumeration) work as any user — the
// snapshot DESTINATION may be a user-owned tmp dir.
//
// Apply is intentionally a separate concern from Restore. Restore is
// the only path that calls `netplan apply` in v1 (it has to — the
// snapshot's job is to be live again). Adding standalone `Apply`
// would invite plain mutation outside the snapshot/restore guard rail.
package netplan

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"sort"
	"strings"

	"github.com/shalb/kube-dc/cli/internal/bootstrap/ports"
)

// netplanDir is the canonical netplan config directory on Ubuntu and
// derivatives. Constant rather than a struct field because the v1
// scope is Ubuntu only — see installer-prd.md §3.
const netplanDir = "/etc/netplan"

// Client implements ports.NetplanClient.
type Client struct {
	// netplanDir overrides the source directory. Production uses
	// /etc/netplan; tests point it at t.TempDir().
	netplanDir string

	// exec is the hook the adapter goes through for `netplan apply`.
	// Production uses runReal; tests inject a no-op.
	exec func(ctx context.Context, name string, args ...string) ([]byte, []byte, error)

	// geteuid is the euid hook. Production uses os.Geteuid; tests
	// inject 0 to exercise the success path or a non-zero value for
	// the sudo gate.
	geteuid func() int
}

// New returns a Client wired against the real /etc/netplan + the
// `netplan` binary.
func New() *Client {
	return &Client{
		netplanDir: netplanDir,
		exec:       runReal,
		geteuid:    os.Geteuid,
	}
}

// Snapshot copies every `*.yaml` under the source netplan dir into dst.
// dst is created (0700) if missing. Returns the list of files copied,
// relative to dst, sorted for deterministic operator output.
//
// Read-only at the source — does NOT require root.
func (c *Client) Snapshot(ctx context.Context, dst string) ([]string, error) {
	if dst == "" {
		return nil, fmt.Errorf("netplan: empty snapshot destination")
	}
	if err := os.MkdirAll(dst, 0o700); err != nil {
		return nil, fmt.Errorf("netplan: create %s: %w", dst, err)
	}

	entries, err := os.ReadDir(c.netplanDir)
	if err != nil {
		if os.IsNotExist(err) {
			// No netplan config (e.g. ifupdown-only host). Empty
			// snapshot is correct — Restore won't apply anything.
			return []string{}, nil
		}
		return nil, fmt.Errorf("netplan: read %s: %w", c.netplanDir, err)
	}

	var copied []string
	for _, e := range entries {
		if e.IsDir() {
			continue
		}
		name := e.Name()
		if !strings.HasSuffix(name, ".yaml") {
			continue
		}
		if err := copyFile(filepath.Join(c.netplanDir, name), filepath.Join(dst, name)); err != nil {
			return nil, fmt.Errorf("netplan: snapshot %s: %w", name, err)
		}
		copied = append(copied, name)
	}
	sort.Strings(copied)
	return copied, nil
}

// Restore replaces /etc/netplan/<file>.yaml with the snapshot copy and
// runs `netplan apply`. Refuses up front when the process isn't root.
//
// Restoration semantics: every `*.yaml` in `src` is copied to the
// netplan dir, overwriting any existing file with the same name. Files
// present in the netplan dir but absent from `src` are LEFT IN PLACE —
// the snapshot only captures what existed at snapshot time, and
// removing newer files (e.g. created by an operator between snapshot
// and restore) would be surprising. Callers needing a "match snapshot
// exactly" semantic should clear the dir before calling Restore.
func (c *Client) Restore(ctx context.Context, src string) error {
	if src == "" {
		return fmt.Errorf("netplan: empty snapshot source")
	}
	if c.geteuid() != 0 {
		return fmt.Errorf("netplan restore: %w", ports.ErrNeedsSudo)
	}

	entries, err := os.ReadDir(src)
	if err != nil {
		return fmt.Errorf("netplan: read snapshot %s: %w", src, err)
	}

	for _, e := range entries {
		if e.IsDir() || !strings.HasSuffix(e.Name(), ".yaml") {
			continue
		}
		if err := copyFile(filepath.Join(src, e.Name()), filepath.Join(c.netplanDir, e.Name())); err != nil {
			return fmt.Errorf("netplan: restore %s: %w", e.Name(), err)
		}
	}

	_, stderr, err := c.exec(ctx, "netplan", "apply")
	if err != nil {
		// Propagate stderr so the operator sees the actual netplan
		// reason (yaml parse error, conflicting renderer, ...).
		return fmt.Errorf("netplan apply: %w\nstderr: %s", err, strings.TrimSpace(string(stderr)))
	}
	return nil
}

// copyFile is a defensive io.Copy with file-mode preservation. We don't
// use os.Rename because src and dst may live on different filesystems
// (snapshot in /tmp vs /etc/netplan on /). 0o600 default keeps secrets
// out of world-readable mode regardless of the source's bits.
func copyFile(src, dst string) (retErr error) {
	in, err := os.Open(src)
	if err != nil {
		return err
	}
	defer in.Close()

	info, err := in.Stat()
	if err != nil {
		return err
	}

	// Preserve source mode but bound it to 0o600 minimum confidentiality.
	mode := info.Mode().Perm()
	if mode&0o077 != 0 {
		mode &= 0o700
	}
	if mode == 0 {
		mode = 0o600
	}

	out, err := os.OpenFile(dst, os.O_WRONLY|os.O_CREATE|os.O_TRUNC, mode)
	if err != nil {
		return err
	}
	defer func() {
		retErr = errors.Join(retErr, out.Close())
	}()

	if _, err := io.Copy(out, in); err != nil {
		return err
	}
	if err := out.Sync(); err != nil {
		return err
	}
	// `os.OpenFile`'s mode argument is only honoured when O_CREATE
	// actually creates the file. If dst already existed, its old
	// permission bits stick — restoring a 0o600 snapshot over an
	// existing 0o644 file would silently leave it world-readable.
	// Explicit Chmod closes that gap.
	return os.Chmod(dst, mode)
}

func runReal(ctx context.Context, name string, args ...string) ([]byte, []byte, error) {
	cmd := exec.CommandContext(ctx, name, args...)
	var stdout, stderr bytes.Buffer
	cmd.Stdout = &stdout
	cmd.Stderr = &stderr
	err := cmd.Run()
	return stdout.Bytes(), stderr.Bytes(), err
}
