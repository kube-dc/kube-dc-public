package mock

import (
	"context"
	"os"
	"path/filepath"
	"testing"
)

// A fleet directory given through a link is found under its resolved path
// too: that is the name the apply engine uses for it.
func TestGitClient_RepositoryFoundUnderItsPhysicalPath(t *testing.T) {
	realDir := t.TempDir()
	link := filepath.Join(t.TempDir(), "fleet")
	if err := os.Symlink(realDir, link); err != nil {
		t.Skipf("no symlinks here: %v", err)
	}
	c := NewGitClient(nil)
	c.MarkDirty(link, "x", "M")
	resolved, err := filepath.EvalSymlinks(link)
	if err != nil {
		t.Fatal(err)
	}
	for _, dir := range []string{link, resolved} {
		if _, err := c.Head(context.Background(), dir); err != nil {
			t.Fatalf("%s: %v", dir, err)
		}
	}
}

// Naming a known repository by its other spelling does not replace it with a
// new, empty one.
func TestGitClient_SecondSpellingKeepsTheHistory(t *testing.T) {
	realDir := t.TempDir()
	link := filepath.Join(t.TempDir(), "fleet")
	if err := os.Symlink(realDir, link); err != nil {
		t.Skipf("no symlinks here: %v", err)
	}
	ctx := context.Background()
	c := NewGitClient(nil)
	if err := c.Clone(ctx, "https://example.invalid/acme/fleet.git", realDir, ""); err != nil {
		t.Fatal(err)
	}
	before, err := c.Head(ctx, realDir)
	if err != nil {
		t.Fatal(err)
	}
	if err := c.Init(ctx, link, "main"); err != nil {
		t.Fatal(err)
	}
	for _, dir := range []string{realDir, link} {
		if got, err := c.Head(ctx, dir); err != nil || got != before {
			t.Fatalf("%s: head %q (%v), want %q: the second spelling replaced the repository", dir, got, err, before)
		}
	}
	if err := c.Clone(ctx, "https://example.invalid/acme/other.git", link, ""); err == nil {
		t.Fatal("a clone onto a known repository, named by its other spelling, was accepted")
	}
}
