package mock

import (
	"context"
	"crypto/sha1" //nolint:gosec // mock-only SHA-ish stand-in for git oids
	"crypto/sha256"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"sync"

	"github.com/shalb/kube-dc/cli/internal/bootstrap/ports"
)

// GitClient is an in-memory Git simulation good enough to exercise the
// atomic commit + rollback contract from agent rule 8. Each "dir" maps
// to a list of commits (SHA-ish derived from message + index) and a
// working-tree diff map; Head returns the latest SHA, ResetHard drops
// commits, CommitAndPush creates a new SHA + clears the diff.
type GitClient struct {
	scenario *Scenario

	mu           sync.Mutex
	repos        map[string]*mockRepo
	createdRepos map[string]bool           // owner/repo -> exists
	isolations   map[string]*mockIsolation // private checkout path -> state
}

type mockRepo struct {
	commits []string                  // SHA history; latest at end
	dirty   map[string]ports.FileDiff // working-tree changes
}

func NewGitClient(s *Scenario) *GitClient {
	c := &GitClient{
		scenario:     s,
		repos:        map[string]*mockRepo{},
		createdRepos: map[string]bool{},
	}
	// If the scenario declares a cloned fleet repo, seed it.
	if s != nil && s.Fleet != nil && s.Fleet.Cloned && s.Fleet.Path != "" {
		c.repos[s.Fleet.Path], c.repos[physical(s.Fleet.Path)] = aliased(&mockRepo{
			commits: []string{seedSHA(s.Fleet.Path)},
			dirty:   map[string]ports.FileDiff{},
		})
	}
	return c
}

// Clone seeds an in-memory repo at dir.
func (c *GitClient) Clone(ctx context.Context, repoURL, dir, token string) error {
	if err := ctx.Err(); err != nil {
		return err
	}
	c.mu.Lock()
	defer c.mu.Unlock()
	if c.known(dir) {
		return fmt.Errorf("mock: git Clone target %s already exists", dir)
	}
	c.repos[dir], c.repos[physical(dir)] = aliased(&mockRepo{
		commits: []string{seedSHA(repoURL)},
		dirty:   map[string]ports.FileDiff{},
	})
	return nil
}

// Init mirrors the adapter's idempotent contract: creates an empty
// mock repo (zero commits) unless dir is already tracked.
func (c *GitClient) Init(ctx context.Context, dir, branch string) error {
	if err := ctx.Err(); err != nil {
		return err
	}
	c.mu.Lock()
	defer c.mu.Unlock()
	if c.known(dir) {
		return nil
	}
	c.repos[dir], c.repos[physical(dir)] = aliased(&mockRepo{
		dirty: map[string]ports.FileDiff{},
	})
	return nil
}

func (c *GitClient) Pull(ctx context.Context, dir, token string) error {
	if err := ctx.Err(); err != nil {
		return err
	}
	c.mu.Lock()
	defer c.mu.Unlock()
	if _, ok := c.repos[dir]; !ok {
		return fmt.Errorf("mock: git Pull on unknown dir %s", dir)
	}
	return nil
}

func (c *GitClient) Diff(ctx context.Context, dir string) (ports.Diff, error) {
	if err := ctx.Err(); err != nil {
		return ports.Diff{}, err
	}
	c.mu.Lock()
	defer c.mu.Unlock()
	r, ok := c.repos[dir]
	if !ok {
		return ports.Diff{}, fmt.Errorf("mock: git Diff on unknown dir %s", dir)
	}
	out := make([]ports.FileDiff, 0, len(r.dirty))
	for _, f := range r.dirty {
		out = append(out, f)
	}
	sort.Slice(out, func(i, j int) bool { return out[i].Path < out[j].Path })
	return ports.Diff{Files: out}, nil
}

func (c *GitClient) Head(ctx context.Context, dir string) (string, error) {
	if err := ctx.Err(); err != nil {
		return "", err
	}
	c.mu.Lock()
	defer c.mu.Unlock()
	r, ok := c.repos[dir]
	if !ok {
		return "", fmt.Errorf("mock: git Head on unknown dir %s", dir)
	}
	if len(r.commits) == 0 {
		return "", fmt.Errorf("mock: git Head on empty repo %s", dir)
	}
	return r.commits[len(r.commits)-1], nil
}

func (c *GitClient) CommitAndPush(ctx context.Context, dir, msg, token string) (string, error) {
	if err := ctx.Err(); err != nil {
		return "", err
	}
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.commitLocked(dir, msg)
}

// Push is a no-op in the mock (no real remote); it just honours ctx.
// Present so the mock satisfies ports.GitClient's resume-path Push.
func (c *GitClient) Push(ctx context.Context, dir, token string) error {
	return ctx.Err()
}

// Commit is the same as CommitAndPush minus the (already-noop) push
// step. Mock has no actual remote so the two are functionally
// identical; we keep them separate so callers that depend on the
// port's Commit-vs-CommitAndPush distinction can test both shapes.
func (c *GitClient) Commit(ctx context.Context, dir, msg string) (string, error) {
	if err := ctx.Err(); err != nil {
		return "", err
	}
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.commitLocked(dir, msg)
}

// commitLocked is the shared no-push commit primitive. Caller must
// hold c.mu.
func (c *GitClient) commitLocked(dir, msg string) (string, error) {
	r, ok := c.repos[dir]
	if !ok {
		return "", fmt.Errorf("mock: git commit on unknown dir %s", dir)
	}
	sha := commitSHA(msg, len(r.commits))
	r.commits = append(r.commits, sha)
	r.dirty = map[string]ports.FileDiff{} // committed → tree clean
	return sha, nil
}

// ResetHard drops commits until HEAD == ref. Refuses when ref is not
// in the commit history (matches the real-adapter rule: never forward-
// skip-reset).
func (c *GitClient) ResetHard(ctx context.Context, dir, ref string) error {
	if err := ctx.Err(); err != nil {
		return err
	}
	c.mu.Lock()
	defer c.mu.Unlock()
	r, ok := c.repos[dir]
	if !ok {
		return fmt.Errorf("mock: git ResetHard on unknown dir %s", dir)
	}
	for i, sha := range r.commits {
		if sha == ref {
			r.commits = r.commits[:i+1]
			r.dirty = map[string]ports.FileDiff{}
			return nil
		}
	}
	return fmt.Errorf("mock: git ResetHard ref %s not in history of %s (ports.ErrDirty-adjacent — review caller)", ref, dir)
}

func (c *GitClient) CreateRepo(ctx context.Context, owner, repo string, private bool, token string) error {
	if err := ctx.Err(); err != nil {
		return err
	}
	c.mu.Lock()
	defer c.mu.Unlock()
	key := owner + "/" + repo
	// Real adapter treats 201 + 422 (already exists) both as success.
	c.createdRepos[key] = true
	return nil
}

// MarkDirty is a test-only helper for setting working-tree changes
// without going through file I/O. Used by tests that need to exercise
// the clean-tree gate (agent rule 8).
func (c *GitClient) MarkDirty(dir, path, status string) {
	c.mu.Lock()
	defer c.mu.Unlock()
	if !c.known(dir) {
		c.repos[dir], c.repos[physical(dir)] = aliased(&mockRepo{
			commits: []string{seedSHA(dir)},
			dirty:   map[string]ports.FileDiff{},
		})
	}
	c.repos[dir].dirty[path] = ports.FileDiff{Path: path, Status: status}
}

// physical is the name clusterinit.Apply uses for an existing directory: the
// path with every link resolved. A repository is registered under both the
// name it was given and that one, so a fleet spelled "." or through a link is
// found either way.
func physical(dir string) string {
	resolved, err := filepath.EvalSymlinks(dir)
	if err != nil {
		return dir
	}
	if abs, err := filepath.Abs(resolved); err == nil {
		return abs
	}
	return dir
}

// known reports whether dir is a registered repository, under the name given
// or under its physical path. A repository first seen under one spelling and
// then named by another is the same one: the second name is registered for
// it, so its history is not replaced by a new, empty repository. The caller
// holds c.mu.
func (c *GitClient) known(dir string) bool {
	if _, ok := c.repos[dir]; ok {
		return true
	}
	if r, ok := c.repos[physical(dir)]; ok {
		c.repos[dir] = r
		return true
	}
	return false
}

// aliased returns r twice, for the two names it is registered under.
func aliased(r *mockRepo) (*mockRepo, *mockRepo) { return r, r }

// seedSHA derives a deterministic SHA-like string from a seed value so
// tests can assert on it without race-y timestamps.
func seedSHA(seed string) string {
	h := sha1.Sum([]byte("seed:" + seed)) //nolint:gosec
	return fmt.Sprintf("%x", h[:7])
}

// commitSHA derives a deterministic SHA-like string from the commit
// message + ordinal, so the same scenario produces the same SHAs on
// re-run.
func commitSHA(msg string, ordinal int) string {
	h := sha1.Sum([]byte(fmt.Sprintf("commit:%d:%s", ordinal, strings.TrimSpace(msg)))) //nolint:gosec
	return fmt.Sprintf("%x", h[:7])
}

// mockBranch is the one branch every mock repository is on.
const mockBranch = "refs/heads/main"

// mockIsolation is one private checkout of a mock repository.
type mockIsolation struct {
	fleet    string
	initial  map[string][32]byte // files when the checkout was made
	baseline map[string][32]byte // files at its last commit
	pending  []mockCommit        // commits made in it, not on the branch yet
	onDisk   bool                // work is a real copy of an existing directory
}

// mockCommit is one commit of a private checkout: what it changed against the
// state the checkout was made from, as recorded when it was committed (later
// edits of the private files are not part of it).
type mockCommit struct {
	sha     string
	files   map[string][]byte // added or modified: content at commit time
	deleted []string
}

// Checkout, Isolate, CommitIsolated, PushCommit and Advance make the mock a
// ports.GitIsolatedClient, which `init --apply` requires. The mock stores no
// file content of its own, but when the fleet directory exists on disk the
// private checkout is a real copy of it: generating there leaves the fleet
// directory untouched until Advance copies the committed changes back, a
// commit check reads the real files, and a discarded checkout takes its
// commits with it. The expected parent and branch are enforced.
func (c *GitClient) Checkout(ctx context.Context, dir string) (ports.Checkout, error) {
	sha, err := c.Head(ctx, dir)
	if err != nil {
		return ports.Checkout{}, err
	}
	return ports.Checkout{Branch: mockBranch, SHA: sha}, nil
}

func (c *GitClient) Isolate(ctx context.Context, dir, sha string) (string, func() error, error) {
	head, err := c.Head(ctx, dir)
	if err != nil {
		return "", nil, err
	}
	if head != sha {
		return "", nil, fmt.Errorf("mock: git Isolate at %s, but %s is at %s", sha, dir, head)
	}
	iso := &mockIsolation{fleet: dir}
	work := dir
	var parent string
	if fi, statErr := os.Stat(dir); statErr == nil && fi.IsDir() {
		if parent, err = os.MkdirTemp("", "kube-dc-mock-init-"); err != nil {
			return "", nil, err
		}
		work = filepath.Join(parent, "fleet")
		if err := copyTree(dir, work, nil); err != nil {
			_ = os.RemoveAll(parent)
			return "", nil, err
		}
		if iso.initial, err = hashTree(work); err != nil {
			_ = os.RemoveAll(parent)
			return "", nil, err
		}
		iso.baseline, iso.onDisk = iso.initial, true
	}
	c.mu.Lock()
	if c.isolations == nil {
		c.isolations = map[string]*mockIsolation{}
	}
	c.isolations[work] = iso
	c.mu.Unlock()
	discard := func() error {
		c.mu.Lock()
		delete(c.isolations, work)
		c.mu.Unlock()
		if parent != "" {
			return os.RemoveAll(parent)
		}
		return nil
	}
	return work, discard, nil
}

// CommitIsolated records a commit in the private checkout; Advance later
// moves the branch to it.
func (c *GitClient) CommitIsolated(ctx context.Context, dir, work, msg, parent string, check func(ports.CommitTrees) error) (string, error) {
	if err := ctx.Err(); err != nil {
		return "", err
	}
	c.mu.Lock()
	defer c.mu.Unlock()
	r, ok := c.repos[dir]
	iso := c.isolations[work]
	if !ok || iso == nil || iso.fleet != dir {
		return "", fmt.Errorf("mock: git commit in %s, which is not a private checkout of %s", work, dir)
	}
	tip := ""
	if n := len(iso.pending); n > 0 {
		tip = iso.pending[n-1].sha
	} else if len(r.commits) > 0 {
		tip = r.commits[len(r.commits)-1]
	}
	if tip != parent {
		return "", fmt.Errorf("mock: git commit in %s on %s, but the private checkout is at %s", work, parent, tip)
	}
	if iso.onDisk {
		now, err := hashTree(work)
		if err != nil {
			return "", err
		}
		if sameHashes(now, iso.baseline) {
			return "", ports.ErrNothingToCommit
		}
		if check != nil {
			if err := check(ports.CommitTrees{Parent: dirReader(dir), Candidate: dirReader(work)}); err != nil {
				return "", fmt.Errorf("mock: git commit check in %s: %w", work, err)
			}
		}
		iso.baseline = now
	}
	commit := mockCommit{sha: commitSHA(msg, len(r.commits)+len(iso.pending)), files: map[string][]byte{}}
	if iso.onDisk {
		for rel, sum := range iso.baseline {
			if was, ok := iso.initial[rel]; ok && was == sum {
				continue
			}
			b, err := os.ReadFile(filepath.Join(work, filepath.FromSlash(rel)))
			if err != nil {
				return "", err
			}
			commit.files[rel] = b
		}
		for rel := range iso.initial {
			if _, ok := iso.baseline[rel]; !ok {
				commit.deleted = append(commit.deleted, rel)
			}
		}
	}
	iso.pending = append(iso.pending, commit)
	return commit.sha, nil
}

// PushCommit is a no-op (no real remote) once the commit is known.
func (c *GitClient) PushCommit(ctx context.Context, dir, token string, at ports.Checkout, check *ports.PublishCheck) error {
	if err := ctx.Err(); err != nil {
		return err
	}
	if check != nil {
		// The mock has no remote and keeps no history of file content.
		if err := check.Check(nil); err != nil {
			return fmt.Errorf("%w: %w", ports.ErrPublishRefused, err)
		}
	}
	c.mu.Lock()
	defer c.mu.Unlock()
	r, ok := c.repos[dir]
	if !ok {
		return fmt.Errorf("mock: git PushCommit on unknown dir %s", dir)
	}
	if at.Branch != mockBranch {
		return fmt.Errorf("mock: git PushCommit to %q, the mock is on %s", at.Branch, mockBranch)
	}
	known := append([]string{}, r.commits...)
	for _, iso := range c.isolations {
		if iso.fleet == dir {
			for _, commit := range iso.pending {
				known = append(known, commit.sha)
			}
		}
	}
	for _, sha := range known {
		if sha == at.SHA {
			return nil
		}
	}
	return fmt.Errorf("mock: git PushCommit of unknown commit %s in %s", at.SHA, dir)
}

// Advance moves HEAD from from.SHA to the commit `to` of a private checkout
// and copies the files that checkout changed into the fleet directory. A
// file changed in the fleet directory meanwhile is in the way.
func (c *GitClient) Advance(ctx context.Context, dir string, from ports.Checkout, to string) error {
	if err := ctx.Err(); err != nil {
		return err
	}
	c.mu.Lock()
	defer c.mu.Unlock()
	r, ok := c.repos[dir]
	if !ok {
		return fmt.Errorf("mock: git Advance on unknown dir %s", dir)
	}
	if len(r.commits) == 0 || from.Branch != mockBranch || r.commits[len(r.commits)-1] != from.SHA {
		return fmt.Errorf("mock: git Advance from %s at %s, which is not HEAD of %s", from.Branch, from.SHA, dir)
	}
	for _, iso := range c.isolations {
		if iso.fleet != dir {
			continue
		}
		for i, commit := range iso.pending {
			if commit.sha != to {
				continue
			}
			if iso.onDisk {
				current, err := hashTree(dir)
				if err != nil {
					return err
				}
				touched := append([]string{}, commit.deleted...)
				for rel := range commit.files {
					touched = append(touched, rel)
				}
				for _, rel := range touched {
					was, existed := iso.initial[rel]
					if now, exists := current[rel]; exists != existed || exists && now != was {
						return fmt.Errorf("mock: git Advance: %s changed in %s and is in the way", rel, dir)
					}
				}
				for rel, body := range commit.files {
					target := filepath.Join(dir, filepath.FromSlash(rel))
					if err := os.MkdirAll(filepath.Dir(target), 0o755); err != nil {
						return err
					}
					if err := os.WriteFile(target, body, 0o644); err != nil {
						return err
					}
				}
				for _, rel := range commit.deleted {
					_ = os.Remove(filepath.Join(dir, filepath.FromSlash(rel)))
				}
			}
			for _, done := range iso.pending[:i+1] {
				r.commits = append(r.commits, done.sha)
			}
			iso.pending = iso.pending[i+1:]
			r.dirty = map[string]ports.FileDiff{}
			return nil
		}
	}
	return fmt.Errorf("mock: git Advance to unknown commit %s in %s", to, dir)
}

// TreeOf reads the files of commit sha: for a commit still in a private
// checkout, what that commit recorded over the fleet directory; for a commit
// on the branch, the fleet directory (the mock keeps no older content).
func (c *GitClient) TreeOf(ctx context.Context, dir, sha string) (ports.TreeFileReader, error) {
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	c.mu.Lock()
	defer c.mu.Unlock()
	base := dirReader(dir)
	for _, iso := range c.isolations {
		if iso.fleet != dir {
			continue
		}
		for _, commit := range iso.pending {
			if commit.sha != sha {
				continue
			}
			files, deleted := commit.files, map[string]bool{}
			for _, rel := range commit.deleted {
				deleted[rel] = true
			}
			return func(path string) ([]byte, bool, error) {
				if body, ok := files[path]; ok {
					return body, true, nil
				}
				if deleted[path] {
					return nil, false, nil
				}
				return base(path)
			}, nil
		}
	}
	return base, nil
}

// ResumeAdvance: the mock's Advance cannot be interrupted.
func (c *GitClient) ResumeAdvance(ctx context.Context, dir string) (bool, error) {
	return false, ctx.Err()
}

var _ ports.GitIsolatedClient = (*GitClient)(nil)

func dirReader(root string) ports.TreeFileReader {
	return func(path string) ([]byte, bool, error) {
		b, err := os.ReadFile(filepath.Join(root, filepath.FromSlash(path)))
		if os.IsNotExist(err) {
			return nil, false, nil
		}
		return b, err == nil, err
	}
}

// copyTree copies src to dst; with only set, just those relative paths.
func copyTree(src, dst string, only map[string]bool) error {
	return filepath.WalkDir(src, func(path string, d fs.DirEntry, err error) error {
		if err != nil {
			return err
		}
		rel, _ := filepath.Rel(src, path)
		target := filepath.Join(dst, rel)
		if d.IsDir() {
			if only != nil {
				return nil
			}
			return os.MkdirAll(target, 0o755)
		}
		if only != nil && !only[filepath.ToSlash(rel)] {
			return nil
		}
		if err := os.MkdirAll(filepath.Dir(target), 0o755); err != nil {
			return err
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
		rel = filepath.ToSlash(rel)
		if d.Type()&fs.ModeSymlink != 0 {
			link, _ := os.Readlink(path)
			out[rel] = sha256.Sum256([]byte("link:" + link))
			return nil
		}
		b, err := os.ReadFile(path)
		if err != nil {
			return err
		}
		out[rel] = sha256.Sum256(append([]byte("file:"), b...))
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
