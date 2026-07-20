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
	"errors"
	"fmt"
	"io"
	nethttp "net/http"
	"os"
	osuser "os/user"
	"path/filepath"
	"strings"
	"time"

	gogit "github.com/go-git/go-git/v5"
	"github.com/go-git/go-git/v5/config"
	"github.com/go-git/go-git/v5/plumbing"
	"github.com/go-git/go-git/v5/plumbing/object"
	"github.com/go-git/go-git/v5/plumbing/transport"
	githttp "github.com/go-git/go-git/v5/plumbing/transport/http"
	gitssh "github.com/go-git/go-git/v5/plumbing/transport/ssh"
	"golang.org/x/crypto/ssh"

	"github.com/shalb/kube-dc/cli/internal/bootstrap/ports"
)

// errNoSSHCredentials is returned when a remote needs SSH but neither an
// agent identity nor an on-disk key is usable. Wrapped with the remote
// URL by authFor so the operator sees which remote failed.
var errNoSSHCredentials = errors.New(
	"uses SSH but no usable SSH credentials were found (start an ssh-agent " +
		"holding the key, or add an unencrypted ~/.ssh/id_ed25519); " +
		"alternatively point the remote at https:// so the token can be used")

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
	auth, err := c.authFor(repoURL, token)
	if err != nil {
		return err
	}
	_, err = gogit.PlainCloneContext(ctx, dir, false, &gogit.CloneOptions{
		URL:      repoURL,
		Auth:     auth,
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
	auth, err := c.authForDirection(repo, dirFetch, token)
	if err != nil {
		return err
	}
	err = wt.PullContext(ctx, &gogit.PullOptions{
		Auth:     auth,
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
	if pushErr := c.Push(ctx, dir, token); pushErr != nil {
		// Per ports/git.go: the local commit is NOT rolled back here.
		// Return the commit SHA so the caller can choose to keep or
		// discard it via ResetHard.
		return sha, pushErr
	}
	return sha, nil
}

// Push pushes the current HEAD to upstream without creating a commit.
// A no-op (nil) when already up to date. See ports/git.go.
func (c *Client) Push(ctx context.Context, dir, token string) error {
	repo, err := gogit.PlainOpen(dir)
	if err != nil {
		return fmt.Errorf("git: open %s: %w", dir, err)
	}
	head, err := repo.Head()
	if err != nil {
		return fmt.Errorf("git: head %s: %w", dir, err)
	}
	if !head.Name().IsBranch() {
		return fmt.Errorf("git: cannot push detached HEAD in %s", dir)
	}
	// Explicit refspec for the current branch so the push does NOT depend
	// on branch upstream tracking being configured. A freshly `git init`'d
	// fleet whose origin was added but never `--set-upstream` otherwise
	// fails with an empty, unhelpful go-git error — the resume path hit
	// exactly that (e2e r6).
	ref := head.Name() // refs/heads/<branch>
	spec := config.RefSpec(fmt.Sprintf("%s:%s", ref, ref))
	// Push auth MUST come from the push URL, not the fetch URL.
	auth, err := c.authForDirection(repo, dirPush, token)
	if err != nil {
		return err
	}
	if pushErr := repo.PushContext(ctx, &gogit.PushOptions{
		RefSpecs: []config.RefSpec{spec},
		Auth:     auth,
		Progress: io.Discard,
	}); pushErr != nil && pushErr != gogit.NoErrAlreadyUpToDate {
		return fmt.Errorf("git: push %s (%s): %w", dir, ref.Short(), pushErr)
	}
	return nil
}

// Commit is CommitAndPush minus the push step (see ports/git.go).
// Used by M4-T12's apply path when `--no-push` is set. Returns the
// new commit SHA.
// Init creates an empty repository in dir with `branch` as HEAD.
// Idempotent — an existing repo is left untouched so a re-run of the
// starter extraction (or an operator-prepared checkout) never loses
// history.
func (c *Client) Init(_ context.Context, dir, branch string) error {
	if branch == "" {
		branch = "main"
	}
	_, err := gogit.PlainInitWithOptions(dir, &gogit.PlainInitOptions{
		InitOptions: gogit.InitOptions{
			DefaultBranch: plumbing.NewBranchReferenceName(branch),
		},
	})
	if errors.Is(err, gogit.ErrRepositoryAlreadyExists) {
		return nil
	}
	if err != nil {
		return fmt.Errorf("git: init %s: %w", dir, err)
	}
	return nil
}

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

// isSSHURL reports whether remote uses the SSH transport.
//
// Delegated to go-git's own parser rather than pattern-matched here: a
// hand-rolled check must re-implement scp-like detection and gets it
// subtly wrong. The previous version required an "@", so userless
// scp-like remotes (`host:path` — which git and go-git both accept,
// resolving the login to the current OS user) were classified as
// non-SSH and handed HTTP credentials. transport.NewEndpoint is the
// same function go-git uses to choose a transport, so our
// classification cannot drift from the one that actually dials.
func isSSHURL(remote string) bool {
	ep, err := transport.NewEndpoint(remote)
	if err != nil {
		return false
	}
	return ep.Protocol == "ssh"
}

// sshUser extracts the login from an SSH remote, or "" when the URL
// omits one.
//
// Empty is deliberate, not a fallback to "git": for a userless
// `ssh://host/path`, OpenSSH and go-git both connect as the current OS
// user, and go-git's NewSSHAgentAuth("") resolves that for us. Forcing
// "git" would break self-hosted remotes that rely on the local login.
// Hosted forges (github/gitlab/…) always spell the user explicitly, so
// they are unaffected.
func sshUser(remote string) string {
	ep, err := transport.NewEndpoint(remote)
	if err != nil {
		return ""
	}
	return ep.User
}

// defaultKeyFiles lists the conventional private keys, strongest first.
func defaultKeyFiles() []string {
	home, err := os.UserHomeDir()
	if err != nil {
		return nil
	}
	var out []string
	for _, name := range []string{"id_ed25519", "id_ecdsa", "id_rsa"} {
		p := filepath.Join(home, ".ssh", name)
		if _, statErr := os.Stat(p); statErr == nil {
			out = append(out, p)
		}
	}
	return out
}

// sshAuthFor builds an SSH auth method that offers agent identities AND
// on-disk keys within a SINGLE handshake.
//
// Why combined rather than "agent, else files": go-git's
// NewSSHAgentAuth only dials SSH_AUTH_SOCK — it never checks that the
// agent actually holds a usable signer. A reachable but empty (or
// wrong-key) agent therefore won the selection and ~/.ssh/id_ed25519
// was never tried, turning a recoverable situation into an opaque
// transport failure that also bypassed the actionable error below.
// Offering both sets lets the server pick whichever key it accepts.
func sshAuthFor(user string) (transport.AuthMethod, error) {
	// agentSigners is nil when no agent is reachable.
	var agentSigners func() ([]ssh.Signer, error)
	if os.Getenv("SSH_AUTH_SOCK") != "" {
		if a, err := gitssh.NewSSHAgentAuth(user); err == nil {
			agentSigners = a.Callback
			// NewSSHAgentAuth resolves "" to the current OS user; adopt
			// that so the key-file signers below use the same login.
			user = a.User
		}
	}
	keyFiles := defaultKeyFiles()
	if agentSigners == nil && len(keyFiles) == 0 {
		return nil, errNoSSHCredentials
	}
	if user == "" {
		// No agent resolved it for us — do it ourselves so the auth
		// method carries a concrete login.
		if u, err := osuser.Current(); err == nil {
			user = u.Username
		}
	}

	return &gitssh.PublicKeysCallback{
		User: user,
		Callback: func() ([]ssh.Signer, error) {
			var signers []ssh.Signer
			if agentSigners != nil {
				// An empty/broken agent must not abort the handshake —
				// fall through to the key files.
				if s, err := agentSigners(); err == nil {
					signers = append(signers, s...)
				}
			}
			for _, p := range keyFiles {
				pk, err := gitssh.NewPublicKeysFromFile(user, p, "")
				if err != nil {
					// Passphrase-protected key with no passphrase
					// available — skip it, try the next.
					continue
				}
				signers = append(signers, pk.Signer)
			}
			if len(signers) == 0 {
				return nil, errNoSSHCredentials
			}
			return signers, nil
		},
	}, nil
}

// authFor picks the auth method that matches the remote's transport.
//
// Why this exists: this adapter used to hand githttp.BasicAuth to every
// operation, which only works for https:// remotes. A fleet cloned over
// SSH (`git@github.com:org/repo.git` — what `gh repo clone` and most
// operators produce) failed with go-git's opaque "invalid auth method".
// That surfaced mid-ceremony: `bootstrap openbao init` initialized the
// vault, encrypted the Shamir shares, then could not push and rolled the
// commit back — leaving an initialized OpenBao with no shares in the
// fleet (cs/next, 2026-07). Transport and credential type must agree.
func (c *Client) authFor(remote, token string) (transport.AuthMethod, error) {
	if !isSSHURL(remote) {
		// Includes the empty-remote case: preserve the historical
		// behaviour of handing over the token (nil when unset).
		return basicAuth(token), nil
	}
	auth, err := sshAuthFor(sshUser(remote))
	if err != nil {
		return nil, fmt.Errorf("git: remote %q: %w", remote, err)
	}
	return auth, nil
}

// gitDirection distinguishes the two transport directions, which can
// have different URLs (and therefore different credential types).
type gitDirection int

const (
	dirFetch gitDirection = iota
	dirPush
)

// authForDirection resolves the auth method for one direction of a
// repo's origin remote. Extracted so the Pull→fetch / Push→push wiring
// is directly testable: previously remoteURLsOf() and authFor() were
// each covered, but nothing asserted that Push actually consults the
// PUSH url — mutating the Push call site back to the fetch url still
// passed the suite.
func (c *Client) authForDirection(repo *gogit.Repository, d gitDirection, token string) (transport.AuthMethod, error) {
	fetchURL, pushURL := remoteURLsOf(repo, "")
	url := fetchURL
	if d == dirPush {
		url = pushURL
	}
	return c.authFor(url, token)
}

// remoteURLsOf returns the fetch and push URLs configured for the named
// remote ("origin" when name is empty). Both are "" when the remote
// cannot be resolved — in which case authFor falls back to token auth.
//
// Fetch and push can differ: git appends `pushurl` entries to the same
// remote, and go-git mirrors that split — remote.go uses URLs[0] for
// fetch but URLs[len-1] for push ("Fetch will always use the first URL,
// while push will use all of them"). Selecting auth from URLs[0] alone
// therefore hands HTTP credentials to an SSH pushurl (or vice-versa) and
// reproduces the exact "invalid auth method" this adapter set out to fix.
func remoteURLsOf(repo *gogit.Repository, name string) (fetchURL, pushURL string) {
	if name == "" {
		name = gogit.DefaultRemoteName
	}
	rem, err := repo.Remote(name)
	if err != nil || rem == nil {
		return "", ""
	}
	urls := rem.Config().URLs
	if len(urls) == 0 {
		return "", ""
	}
	return urls[0], urls[len(urls)-1]
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
