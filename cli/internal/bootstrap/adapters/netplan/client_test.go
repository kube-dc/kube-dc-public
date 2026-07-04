package netplan

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"reflect"
	"testing"

	"github.com/shalb/kube-dc/cli/internal/bootstrap/ports"
)

// makeNetplanDir builds a fake /etc/netplan with a couple of YAML
// files (and a non-YAML file the snapshot should skip).
func makeNetplanDir(t *testing.T) string {
	t.Helper()
	dir := t.TempDir()
	mustWrite(t, filepath.Join(dir, "00-installer.yaml"), "network: {version: 2}\n")
	mustWrite(t, filepath.Join(dir, "50-custom.yaml"), "network:\n  ethernets:\n    eno1:\n      dhcp4: true\n")
	mustWrite(t, filepath.Join(dir, "README"), "ignored")     // non-yaml: skip
	mustWrite(t, filepath.Join(dir, "01-dhcp.yml"), "ignored") // not .yaml: skip
	return dir
}

func mustWrite(t *testing.T, path, content string) {
	t.Helper()
	if err := os.WriteFile(path, []byte(content), 0o600); err != nil {
		t.Fatalf("write %s: %v", path, err)
	}
}

func TestSnapshot_CopiesYAMLOnly(t *testing.T) {
	src := makeNetplanDir(t)
	dst := filepath.Join(t.TempDir(), "snap")
	c := &Client{
		netplanDir: src,
		exec:       func(context.Context, string, ...string) ([]byte, []byte, error) { return nil, nil, nil },
		geteuid:    func() int { return 0 },
	}
	got, err := c.Snapshot(context.Background(), dst)
	if err != nil {
		t.Fatalf("Snapshot: %v", err)
	}
	want := []string{"00-installer.yaml", "50-custom.yaml"}
	if !reflect.DeepEqual(got, want) {
		t.Errorf("got %v, want %v", got, want)
	}
	// File contents preserved.
	body, err := os.ReadFile(filepath.Join(dst, "50-custom.yaml"))
	if err != nil {
		t.Fatal(err)
	}
	if string(body) == "" {
		t.Error("file was empty after snapshot")
	}
}

func TestSnapshot_MissingNetplanDir_EmptyResult(t *testing.T) {
	c := &Client{
		netplanDir: filepath.Join(t.TempDir(), "does-not-exist"),
		exec:       func(context.Context, string, ...string) ([]byte, []byte, error) { return nil, nil, nil },
		geteuid:    func() int { return 0 },
	}
	got, err := c.Snapshot(context.Background(), filepath.Join(t.TempDir(), "snap"))
	if err != nil {
		t.Fatalf("missing dir should be empty snapshot, got %v", err)
	}
	if len(got) != 0 {
		t.Errorf("want empty, got %v", got)
	}
}

func TestSnapshot_EmptyDest_Rejected(t *testing.T) {
	c := New()
	if _, err := c.Snapshot(context.Background(), ""); err == nil {
		t.Fatal("expected error on empty dst")
	}
}

func TestRestore_NonRoot_ErrNeedsSudo(t *testing.T) {
	src := makeNetplanDir(t)
	c := &Client{
		netplanDir: src,
		exec:       func(context.Context, string, ...string) ([]byte, []byte, error) { return nil, nil, nil },
		geteuid:    func() int { return 1000 },
	}
	err := c.Restore(context.Background(), src)
	if !errors.Is(err, ports.ErrNeedsSudo) {
		t.Fatalf("want ErrNeedsSudo, got %v", err)
	}
}

func TestRestore_Root_AppliesAndCalls_NetplanApply(t *testing.T) {
	src := makeNetplanDir(t)
	dstDir := t.TempDir() // simulated /etc/netplan
	called := false
	c := &Client{
		netplanDir: dstDir,
		exec: func(ctx context.Context, name string, args ...string) ([]byte, []byte, error) {
			called = true
			if name != "netplan" || len(args) != 1 || args[0] != "apply" {
				t.Errorf("unexpected exec: %s %v", name, args)
			}
			return nil, nil, nil
		},
		geteuid: func() int { return 0 },
	}
	if err := c.Restore(context.Background(), src); err != nil {
		t.Fatalf("Restore: %v", err)
	}
	if !called {
		t.Error("netplan apply not called")
	}
	// Files copied into the netplan dir.
	for _, f := range []string{"00-installer.yaml", "50-custom.yaml"} {
		if _, err := os.Stat(filepath.Join(dstDir, f)); err != nil {
			t.Errorf("expected %s in netplanDir: %v", f, err)
		}
	}
}

func TestRestore_PropagatesNetplanApplyStderr(t *testing.T) {
	src := makeNetplanDir(t)
	dstDir := t.TempDir()
	c := &Client{
		netplanDir: dstDir,
		exec: func(ctx context.Context, name string, args ...string) ([]byte, []byte, error) {
			return nil, []byte("netplan: invalid YAML in /etc/netplan/50-custom.yaml line 3"), errors.New("exit status 78")
		},
		geteuid: func() int { return 0 },
	}
	err := c.Restore(context.Background(), src)
	if err == nil {
		t.Fatal("expected error")
	}
	if !contains(err.Error(), "invalid YAML") {
		t.Errorf("stderr not propagated: %v", err)
	}
}

func TestRestore_PreservesNewerFilesNotInSnapshot(t *testing.T) {
	src := makeNetplanDir(t)
	dstDir := t.TempDir()
	// Pre-populate dstDir with a NEWER file not in the snapshot.
	mustWrite(t, filepath.Join(dstDir, "99-operator.yaml"), "operator-added-after-snapshot")

	c := &Client{
		netplanDir: dstDir,
		exec:       func(context.Context, string, ...string) ([]byte, []byte, error) { return nil, nil, nil },
		geteuid:    func() int { return 0 },
	}
	if err := c.Restore(context.Background(), src); err != nil {
		t.Fatalf("Restore: %v", err)
	}
	// Newer file MUST still be there (contract is "overwrite snapshot
	// files only, leave others alone").
	if _, err := os.Stat(filepath.Join(dstDir, "99-operator.yaml")); err != nil {
		t.Errorf("newer file got deleted on restore: %v", err)
	}
}

func TestRestore_EmptySrc_Rejected(t *testing.T) {
	c := New()
	if err := c.Restore(context.Background(), ""); err == nil {
		t.Fatal("expected error on empty src")
	}
}

// Restore must tighten existing-file permissions. os.OpenFile only
// applies its mode arg on creation — without a follow-up os.Chmod,
// a 0o600 snapshot restored over an existing 0o644 file would leave
// it world-readable. Verifies the review-pass fix on
// adapters/netplan/client.go copyFile.
func TestRestore_TightensExistingFileMode(t *testing.T) {
	src := t.TempDir()
	dstDir := t.TempDir()

	// Snapshot file at 0o600.
	snapshotFile := filepath.Join(src, "10-secret.yaml")
	if err := os.WriteFile(snapshotFile, []byte("snapshot content"), 0o600); err != nil {
		t.Fatal(err)
	}

	// Pre-existing dst file at 0o644 (world-readable).
	dstFile := filepath.Join(dstDir, "10-secret.yaml")
	if err := os.WriteFile(dstFile, []byte("old content"), 0o644); err != nil {
		t.Fatal(err)
	}

	c := &Client{
		netplanDir: dstDir,
		exec:       func(context.Context, string, ...string) ([]byte, []byte, error) { return nil, nil, nil },
		geteuid:    func() int { return 0 },
	}
	if err := c.Restore(context.Background(), src); err != nil {
		t.Fatalf("Restore: %v", err)
	}

	info, err := os.Stat(dstFile)
	if err != nil {
		t.Fatal(err)
	}
	got := info.Mode().Perm()
	if got != 0o600 {
		t.Errorf("restored mode = 0o%o, want 0o600 (review-pass: Restore must chmod existing files)", got)
	}
	// Content was overwritten too — sanity check.
	body, _ := os.ReadFile(dstFile)
	if string(body) != "snapshot content" {
		t.Errorf("content not restored: %q", body)
	}
}

func contains(s, sub string) bool {
	for i := 0; i+len(sub) <= len(s); i++ {
		if s[i:i+len(sub)] == sub {
			return true
		}
	}
	return false
}
