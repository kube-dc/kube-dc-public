package clusterinit

import (
	"context"
	"crypto/sha256"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"

	"github.com/shalb/kube-dc/cli/internal/bootstrap/ports"
)

// fakeGit as a ports.GitIsolatedClient, for directories that are not Git
// repositories: the private checkout is a copy of the directory, a commit is
// "the copy differs from when it was last committed", and Advance copies the
// result back. So a failed run leaves the fleet directory untouched, as with
// the real adapter.

type fakeIsolation struct {
	work     string
	initial  map[string][32]byte // files when the private checkout was made
	baseline map[string][32]byte // files at its last commit
	advanced []string            // commits the fleet directory was fast-forwarded to
	pushedAt []ports.Checkout
}

func (g *fakeGit) Checkout(ctx context.Context, dir string) (ports.Checkout, error) {
	sha, err := g.Head(ctx, dir)
	g.mu.Lock()
	defer g.mu.Unlock()
	if n := len(g.iso.advanced); n > 0 {
		sha = g.iso.advanced[n-1] // the branch was fast-forwarded
	}
	return ports.Checkout{Branch: "refs/heads/main", SHA: sha}, err
}

func (g *fakeGit) Isolate(_ context.Context, dir, _ string) (string, func() error, error) {
	parent, err := os.MkdirTemp("", "fake-private-")
	if err != nil {
		return "", nil, err
	}
	work := filepath.Join(parent, "fleet")
	if err := copyTree(dir, work); err != nil {
		return "", nil, err
	}
	baseline, err := hashTree(work)
	if err != nil {
		return "", nil, err
	}
	g.mu.Lock()
	g.fleetDir = dir
	g.iso = fakeIsolation{work: work, initial: baseline, baseline: baseline}
	g.mu.Unlock()
	return work, func() error {
		if err := os.RemoveAll(parent); err != nil {
			return err
		}
		return g.discardErr
	}, nil
}

func (g *fakeGit) CommitIsolated(_ context.Context, _, work, _, _ string, check func(ports.CommitTrees) error) (string, error) {
	g.mu.Lock()
	defer g.mu.Unlock()
	now, err := hashTree(work)
	if err != nil {
		return "", err
	}
	if sameHashes(now, g.iso.baseline) {
		return "", ports.ErrNothingToCommit
	}
	g.commitCall++
	if g.commitErr != nil {
		return "", g.commitErr
	}
	if check != nil {
		reader := func(root string) ports.TreeFileReader {
			return func(path string) ([]byte, bool, error) {
				b, err := os.ReadFile(filepath.Join(root, filepath.FromSlash(path)))
				if os.IsNotExist(err) {
					return nil, false, nil
				}
				return b, err == nil, err
			}
		}
		if err := check(ports.CommitTrees{Parent: reader(g.fleetDir), Candidate: reader(work)}); err != nil {
			return "", err
		}
	}
	g.iso.baseline = now
	return g.commitSHA, nil
}

func (g *fakeGit) PushCommit(_ context.Context, _, _ string, at ports.Checkout, check *ports.PublishCheck) error {
	if check != nil {
		// No history to scan here; the check still runs, as it does right
		// before the real push.
		if err := check.Check(nil); err != nil {
			return fmt.Errorf("%w: %w", ports.ErrPublishRefused, err)
		}
	}
	g.mu.Lock()
	defer g.mu.Unlock()
	if g.pushErr != nil {
		g.pushAtCommit = true
		return g.pushErr
	}
	g.pushed = true
	g.iso.pushedAt = append(g.iso.pushedAt, at)
	return nil
}

func (g *fakeGit) Advance(_ context.Context, dir string, _ ports.Checkout, to string) error {
	g.mu.Lock()
	defer g.mu.Unlock()
	if g.advanceErr != nil {
		return g.advanceErr
	}
	g.iso.advanced = append(g.iso.advanced, to)
	// Only what the private checkout committed: an unrelated file changed
	// in the fleet directory meanwhile must not be copied over.
	return filepath.WalkDir(g.iso.work, func(path string, d fs.DirEntry, err error) error {
		if err != nil || d.IsDir() {
			return err
		}
		rel, _ := filepath.Rel(g.iso.work, path)
		if was, ok := g.iso.initial[rel]; ok && was == g.iso.baseline[rel] {
			return nil
		}
		b, err := os.ReadFile(path)
		if err != nil {
			return err
		}
		target := filepath.Join(dir, rel)
		if err := os.MkdirAll(filepath.Dir(target), 0o755); err != nil {
			return err
		}
		return os.WriteFile(target, b, 0o644)
	})
}

func (g *fakeGit) TreeOf(_ context.Context, dir, _ string) (ports.TreeFileReader, error) {
	// The tip being read is the private checkout's state when one exists.
	g.mu.Lock()
	if _, err := os.Stat(g.iso.work); g.iso.work != "" && err == nil {
		dir = g.iso.work
	}
	g.mu.Unlock()
	return func(path string) ([]byte, bool, error) {
		b, err := os.ReadFile(filepath.Join(dir, filepath.FromSlash(path)))
		if os.IsNotExist(err) {
			return nil, false, nil
		}
		return b, err == nil, err
	}, nil
}

func (g *fakeGit) ResumeAdvance(context.Context, string) (bool, error) { return false, nil }

var _ ports.GitIsolatedClient = (*fakeGit)(nil)

func copyTree(src, dst string) error {
	return filepath.WalkDir(src, func(path string, d fs.DirEntry, err error) error {
		if err != nil {
			return err
		}
		rel, _ := filepath.Rel(src, path)
		target := filepath.Join(dst, rel)
		if d.IsDir() {
			return os.MkdirAll(target, 0o755)
		}
		info, err := d.Info()
		if err != nil {
			return err
		}
		if info.Mode()&fs.ModeSymlink != 0 {
			link, err := os.Readlink(path)
			if err != nil {
				return err
			}
			_ = os.Remove(target)
			return os.Symlink(link, target)
		}
		b, err := os.ReadFile(path)
		if err != nil {
			return err
		}
		return os.WriteFile(target, b, info.Mode().Perm())
	})
}

func hashTree(root string) (map[string][32]byte, error) {
	out := map[string][32]byte{}
	err := filepath.WalkDir(root, func(path string, d fs.DirEntry, err error) error {
		if err != nil || d.IsDir() {
			return err
		}
		rel, _ := filepath.Rel(root, path)
		if d.Type()&fs.ModeSymlink != 0 {
			link, _ := os.Readlink(path)
			out[rel] = sha256.Sum256([]byte("link:" + link))
			return nil
		}
		b, err := os.ReadFile(path)
		if err != nil {
			return err
		}
		out[rel] = sha256.Sum256(b)
		return nil
	})
	return out, err
}

func sameHashes(a, b map[string][32]byte) bool {
	if len(a) != len(b) {
		return false
	}
	for k, v := range a {
		if w, ok := b[k]; !ok || w != v {
			return false
		}
	}
	return true
}

// WithFleetRepo makes the fake script runner write its "script output" into
// another copy of the fleet (ports.FleetScriptRunner). Calls are still
// recorded on the original.
func (f *fakeScriptRunner) WithFleetRepo(dir string) ports.ScriptRunner {
	return rebasedFakeRunner{f, dir}
}

type rebasedFakeRunner struct {
	*fakeScriptRunner
	root string
}

func (r rebasedFakeRunner) Run(ctx context.Context, name ports.ScriptKind, env map[string]string, args ...string) (<-chan ports.Line, error) {
	saved := r.fakeScriptRunner.fleetRoot
	if saved != "" {
		r.fakeScriptRunner.fleetRoot = r.root
	}
	defer func() { r.fakeScriptRunner.fleetRoot = saved }()
	return r.fakeScriptRunner.Run(ctx, name, env, args...)
}

func (f *flippingScriptRunner) WithFleetRepo(dir string) ports.ScriptRunner {
	return rebasedFlippingRunner{f, dir}
}

type rebasedFlippingRunner struct {
	*flippingScriptRunner
	root string
}

func (r rebasedFlippingRunner) Run(ctx context.Context, name ports.ScriptKind, env map[string]string, args ...string) (<-chan ports.Line, error) {
	saved := r.wrapped.fleetRoot
	if saved != "" {
		r.wrapped.fleetRoot = r.root
	}
	defer func() { r.wrapped.fleetRoot = saved }()
	return r.flippingScriptRunner.Run(ctx, name, env, args...)
}
