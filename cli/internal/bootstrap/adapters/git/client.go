// Package git is the real ports.GitClient adapter. It uses go-git
// for in-process Git operations — no shell-out for the core flow.
// CreateRepo is the only path that talks to a non-Git API; it uses
// plain net/http against api.github.com.
//
// The adapter is shaped around the atomic-Git contract from agent
// rule 8: every mutation captures pre-commit SHA via Head, commits +
// pushes, and the caller rolls back via ResetHard on failure. The
// adapter does NOT auto-rollback because the operator may want to
// keep the local commit (e.g. manual conflict fix), and ports/git.go
// makes this explicit.
package git

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	nethttp "net/http"
	"os"
	"strings"
	"time"

	gogit "github.com/go-git/go-git/v5"
	"github.com/go-git/go-git/v5/plumbing"
	"github.com/go-git/go-git/v5/plumbing/object"
	githttp "github.com/go-git/go-git/v5/plumbing/transport/http"

	"github.com/shalb/kube-dc/cli/internal/bootstrap/ports"
)

// Client implements ports.GitClient.
type Client struct {
	// authorName + authorEmail are stamped on commits this adapter
	// creates. Default to a generic "kube-dc bootstrap" identity;
	// callers can override via NewWithIdentity (M4 may surface this
	// from the operator's git config).
	authorName  string
	authorEmail string

	// httpClient drives CreateRepo against api.github.com. Indirected
	// for tests; production uses a 30s-timeout client.
	httpClient *nethttp.Client

	// githubAPIBase lets tests point CreateRepo at a httptest server.
	// Empty falls back to https://api.github.com.
	githubAPIBase string
}

// New returns a Client with the default bootstrap-bot identity.
func New() *Client {
	return &Client{
		authorName:  "kube-dc bootstrap",
		authorEmail: "bootstrap@kube-dc.invalid",
		httpClient:  &nethttp.Client{Timeout: 30 * time.Second},
	}
}

// NewWithIdentity overrides the commit author for M4 plans that want
// the operator's real git identity on commits.
func NewWithIdentity(name, email string) *Client {
	c := New()
	if name != "" {
		c.authorName = name
	}
	if email != "" {
		c.authorEmail = email
	}
	return c
}

// ---------- ports.GitClient ----------

func (c *Client) Clone(ctx context.Context, repoURL, dir, token string) error {
	if dir == "" {
		return fmt.Errorf("git: empty dir")
	}
	if existsAndPopulated(dir) {
		return fmt.Errorf("git: %s is non-empty; refuse to clone into populated dir", dir)
	}
	_, err := gogit.PlainCloneContext(ctx, dir, false, &gogit.CloneOptions{
		URL:      repoURL,
		Auth:     basicAuth(token),
		Progress: io.Discard,
	})
	if err != nil {
		return fmt.Errorf("git: clone %s: %w", repoURL, err)
	}
	return nil
}

func (c *Client) Pull(ctx context.Context, dir, token string) error {
	repo, err := gogit.PlainOpen(dir)
	if err != nil {
		return fmt.Errorf("git: open %s: %w", dir, err)
	}
	wt, err := repo.Worktree()
	if err != nil {
		return fmt.Errorf("git: worktree %s: %w", dir, err)
	}
	err = wt.PullContext(ctx, &gogit.PullOptions{
		Auth:     basicAuth(token),
		Progress: io.Discard,
	})
	if err != nil && err != gogit.NoErrAlreadyUpToDate {
		return fmt.Errorf("git: pull %s: %w", dir, err)
	}
	return nil
}

func (c *Client) Diff(_ context.Context, dir string) (ports.Diff, error) {
	repo, err := gogit.PlainOpen(dir)
	if err != nil {
		return ports.Diff{}, fmt.Errorf("git: open %s: %w", dir, err)
	}
	wt, err := repo.Worktree()
	if err != nil {
		return ports.Diff{}, fmt.Errorf("git: worktree %s: %w", dir, err)
	}
	status, err := wt.Status()
	if err != nil {
		return ports.Diff{}, fmt.Errorf("git: status %s: %w", dir, err)
	}
	var out ports.Diff
	for path, st := range status {
		out.Files = append(out.Files, ports.FileDiff{
			Path:   path,
			Status: statusCode(st.Worktree, st.Staging),
		})
	}
	return out, nil
}

func (c *Client) Head(_ context.Context, dir string) (string, error) {
	repo, err := gogit.PlainOpen(dir)
	if err != nil {
		return "", fmt.Errorf("git: open %s: %w", dir, err)
	}
	ref, err := repo.Head()
	if err != nil {
		return "", fmt.Errorf("git: head %s: %w", dir, err)
	}
	return ref.Hash().String(), nil
}

func (c *Client) CommitAndPush(ctx context.Context, dir, msg, token string) (string, error) {
	sha, err := c.commitLocal(dir, msg)
	if err != nil {
		return "", err
	}
	repo, err := gogit.PlainOpen(dir)
	if err != nil {
		return sha, fmt.Errorf("git: open %s: %w", dir, err)
	}
	if pushErr := repo.PushContext(ctx, &gogit.PushOptions{
		Auth:     basicAuth(token),
		Progress: io.Discard,
	}); pushErr != nil && pushErr != gogit.NoErrAlreadyUpToDate {
		// Per ports/git.go: the local commit is NOT rolled back here.
		// Return the commit SHA so the caller can choose to keep or
		// discard it via ResetHard.
		return sha, fmt.Errorf("git: push %s: %w", dir, pushErr)
	}
	return sha, nil
}

// Commit is CommitAndPush minus the push step (see ports/git.go).
// Used by M4-T12's apply path when `--no-push` is set. Returns the
// new commit SHA.
func (c *Client) Commit(_ context.Context, dir, msg string) (string, error) {
	return c.commitLocal(dir, msg)
}

// commitLocal is the shared local-only commit primitive used by both
// CommitAndPush + Commit. Stages every change in the working tree
// (the agent-rule "stage only what you want" contract is enforced by
// the caller's clean-tree pre-check, not here).
func (c *Client) commitLocal(dir, msg string) (string, error) {
	repo, err := gogit.PlainOpen(dir)
	if err != nil {
		return "", fmt.Errorf("git: open %s: %w", dir, err)
	}
	wt, err := repo.Worktree()
	if err != nil {
		return "", fmt.Errorf("git: worktree %s: %w", dir, err)
	}
	if _, err := wt.Add("."); err != nil {
		return "", fmt.Errorf("git: add %s: %w", dir, err)
	}
	hash, err := wt.Commit(msg, &gogit.CommitOptions{
		Author: &object.Signature{
			Name:  c.authorName,
			Email: c.authorEmail,
			When:  time.Now(),
		},
	})
	if err != nil {
		return "", fmt.Errorf("git: commit %s: %w", dir, err)
	}
	return hash.String(), nil
}

func (c *Client) ResetHard(_ context.Context, dir, ref string) error {
	repo, err := gogit.PlainOpen(dir)
	if err != nil {
		return fmt.Errorf("git: open %s: %w", dir, err)
	}
	wt, err := repo.Worktree()
	if err != nil {
		return fmt.Errorf("git: worktree %s: %w", dir, err)
	}
	target := plumbing.NewHash(ref)
	if target.IsZero() {
		return fmt.Errorf("git: ResetHard invalid ref %q", ref)
	}
	// Ancestor check: refuse to reset to a commit that isn't reachable
	// from HEAD. Prevents the "foreign commit silently discarded"
	// footgun mentioned in ports/git.go.
	if err := assertAncestorOfHEAD(repo, target); err != nil {
		return fmt.Errorf("git: ResetHard %s: %w", ref, err)
	}
	if err := wt.Reset(&gogit.ResetOptions{Mode: gogit.HardReset, Commit: target}); err != nil {
		return fmt.Errorf("git: reset %s: %w", ref, err)
	}
	return nil
}

func (c *Client) CreateRepo(ctx context.Context, owner, repo string, private bool, token string) error {
	if owner == "" || repo == "" {
		return fmt.Errorf("git: CreateRepo needs owner + repo")
	}
	if token == "" {
		return fmt.Errorf("git: CreateRepo needs a GitHub PAT")
	}
	body, _ := json.Marshal(map[string]interface{}{
		"name":    repo,
		"private": private,
	})
	base := c.githubAPIBase
	if base == "" {
		base = "https://api.github.com"
	}

	// Resolve owner: empty target (a /user/repos call) creates under
	// the authenticated user; non-empty owner uses /orgs/<o>/repos.
	// The CLI surface always passes a non-empty owner today; we
	// honour the org path.
	url := fmt.Sprintf("%s/orgs/%s/repos", strings.TrimRight(base, "/"), owner)

	req, err := nethttp.NewRequestWithContext(ctx, "POST", url, bytes.NewReader(body))
	if err != nil {
		return fmt.Errorf("git: build CreateRepo request: %w", err)
	}
	req.Header.Set("Authorization", "Bearer "+token)
	req.Header.Set("Accept", "application/vnd.github+json")
	req.Header.Set("Content-Type", "application/json")

	resp, err := c.httpClient.Do(req)
	if err != nil {
		return fmt.Errorf("git: CreateRepo HTTP: %w", err)
	}
	defer resp.Body.Close()

	switch resp.StatusCode {
	case nethttp.StatusCreated:
		return nil
	case nethttp.StatusUnprocessableEntity:
		// 422 is GitHub's grab-bag for "validation failed" — includes
		// "name already exists on this account" (operationally success
		// per the ports contract) AND legitimate validation failures
		// (bad name, license mismatch, ...). Narrow to the
		// already-exists case; surface anything else as a real error.
		buf, _ := io.ReadAll(resp.Body)
		body := string(buf)
		if isAlreadyExists(body) {
			return nil
		}
		return fmt.Errorf("git: CreateRepo HTTP 422 (validation, not already-exists): %s", strings.TrimSpace(body))
	default:
		buf, _ := io.ReadAll(resp.Body)
		return fmt.Errorf("git: CreateRepo HTTP %d: %s", resp.StatusCode, bytes.TrimSpace(buf))
	}
}

// isAlreadyExists detects the GitHub 422 shape that means "repo with
// that name exists already". The string match is intentionally
// narrow — GitHub uses several phrasings ("name already exists",
// "must be unique") and we accept any of them, but NOT generic
// "validation failed" without a name/uniqueness signal.
func isAlreadyExists(body string) bool {
	low := strings.ToLower(body)
	return strings.Contains(low, "name already exists") ||
		strings.Contains(low, "must be unique") ||
		strings.Contains(low, "already exists on this account")
}

// ---------- helpers ----------

func basicAuth(token string) *githttp.BasicAuth {
	if token == "" {
		return nil
	}
	return &githttp.BasicAuth{
		// GitHub accepts any non-empty username with PAT in password.
		Username: "kube-dc",
		Password: token,
	}
}

// statusCode maps go-git's (worktree, staging) status codes into the
// `git status --porcelain` two-letter shorthand the port contract
// describes.
func statusCode(wt, staging gogit.StatusCode) string {
	// Priority: untracked > worktree change > staged change.
	switch wt {
	case gogit.Untracked:
		return "??"
	case gogit.Deleted:
		return "D"
	case gogit.Modified:
		return "M"
	case gogit.Renamed:
		return "R"
	case gogit.Added:
		return "A"
	}
	switch staging {
	case gogit.Modified:
		return "M"
	case gogit.Added:
		return "A"
	case gogit.Deleted:
		return "D"
	case gogit.Renamed:
		return "R"
	}
	return "?"
}

// existsAndPopulated returns true when dir exists and contains at
// least one entry. Empty dir or missing dir → false.
func existsAndPopulated(dir string) bool {
	entries, err := os.ReadDir(dir)
	if err != nil {
		return false
	}
	return len(entries) > 0
}

// assertAncestorOfHEAD walks back from HEAD looking for target. The
// max-walk cap (1024 commits) bounds the work on pathological repos;
// real fleet repos have dozens of commits per cluster.
func assertAncestorOfHEAD(repo *gogit.Repository, target plumbing.Hash) error {
	headRef, err := repo.Head()
	if err != nil {
		return err
	}
	iter, err := repo.Log(&gogit.LogOptions{From: headRef.Hash()})
	if err != nil {
		return err
	}
	defer iter.Close()
	for i := 0; i < 1024; i++ {
		c, err := iter.Next()
		if err != nil {
			break
		}
		if c.Hash == target {
			return nil
		}
	}
	return fmt.Errorf("ref is not an ancestor of HEAD")
}

