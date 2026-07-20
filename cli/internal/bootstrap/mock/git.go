package mock

import (
	"context"
	"crypto/sha1" //nolint:gosec // mock-only SHA-ish stand-in for git oids
	"fmt"
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
	createdRepos map[string]bool // owner/repo -> exists
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
		c.repos[s.Fleet.Path] = &mockRepo{
			commits: []string{seedSHA(s.Fleet.Path)},
			dirty:   map[string]ports.FileDiff{},
		}
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
	if _, exists := c.repos[dir]; exists {
		return fmt.Errorf("mock: git Clone target %s already exists", dir)
	}
	c.repos[dir] = &mockRepo{
		commits: []string{seedSHA(repoURL)},
		dirty:   map[string]ports.FileDiff{},
	}
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
	if _, exists := c.repos[dir]; exists {
		return nil
	}
	c.repos[dir] = &mockRepo{
		dirty: map[string]ports.FileDiff{},
	}
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
	if _, ok := c.repos[dir]; !ok {
		c.repos[dir] = &mockRepo{
			commits: []string{seedSHA(dir)},
			dirty:   map[string]ports.FileDiff{},
		}
	}
	c.repos[dir].dirty[path] = ports.FileDiff{Path: path, Status: status}
}

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
