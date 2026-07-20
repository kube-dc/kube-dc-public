package git

import (
	"context"
	"crypto/ed25519"
	"crypto/rand"
	"encoding/pem"
	"net"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	gogit "github.com/go-git/go-git/v5"
	gogitconfig "github.com/go-git/go-git/v5/config"
	"github.com/go-git/go-git/v5/plumbing/object"
	githttp "github.com/go-git/go-git/v5/plumbing/transport/http"
	gitssh "github.com/go-git/go-git/v5/plumbing/transport/ssh"
	gossh "golang.org/x/crypto/ssh"
	"golang.org/x/crypto/ssh/agent"

	"github.com/shalb/kube-dc/cli/internal/bootstrap/ports"
)

// Compile-time assertion.
var _ ports.GitClient = (*Client)(nil)

// initLocalRepo creates a tmp git repo with one committed file. Used
// by tests that need a real go-git target without hitting the network.
func initLocalRepo(t *testing.T) (dir, headSHA string) {
	t.Helper()
	dir = t.TempDir()
	repo, err := gogit.PlainInit(dir, false)
	if err != nil {
		t.Fatalf("init: %v", err)
	}
	// Seed with one commit so HEAD resolves.
	if err := os.WriteFile(filepath.Join(dir, "README.md"), []byte("seed\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	wt, _ := repo.Worktree()
	_, _ = wt.Add("README.md")
	hash, err := wt.Commit("seed", &gogit.CommitOptions{
		Author: &object.Signature{Name: "test", Email: "test@example.com", When: time.Now()},
	})
	if err != nil {
		t.Fatalf("seed commit: %v", err)
	}
	return dir, hash.String()
}

func TestHead_AfterInit(t *testing.T) {
	dir, want := initLocalRepo(t)
	c := New()
	got, err := c.Head(context.Background(), dir)
	if err != nil {
		t.Fatalf("Head: %v", err)
	}
	if got != want {
		t.Errorf("Head=%q want %q", got, want)
	}
}

func TestDiff_CleanTree(t *testing.T) {
	dir, _ := initLocalRepo(t)
	c := New()
	d, err := c.Diff(context.Background(), dir)
	if err != nil {
		t.Fatalf("Diff: %v", err)
	}
	if len(d.Files) != 0 {
		t.Errorf("clean tree returned files: %v", d.Files)
	}
}

func TestDiff_UntrackedFile(t *testing.T) {
	dir, _ := initLocalRepo(t)
	if err := os.WriteFile(filepath.Join(dir, "new.txt"), []byte("hi"), 0o644); err != nil {
		t.Fatal(err)
	}
	c := New()
	d, err := c.Diff(context.Background(), dir)
	if err != nil {
		t.Fatalf("Diff: %v", err)
	}
	found := false
	for _, f := range d.Files {
		if f.Path == "new.txt" && f.Status == "??" {
			found = true
		}
	}
	if !found {
		t.Errorf("untracked file not surfaced: %v", d.Files)
	}
}

func TestCommitAndPush_NoRemote_StillCommits(t *testing.T) {
	// No remote configured → push fails, but the commit IS created.
	// Caller decides whether to ResetHard.
	dir, preSHA := initLocalRepo(t)
	if err := os.WriteFile(filepath.Join(dir, "f.txt"), []byte("x"), 0o644); err != nil {
		t.Fatal(err)
	}
	c := New()
	commitSHA, err := c.CommitAndPush(context.Background(), dir, "msg", "")
	if err == nil {
		t.Fatal("push should fail without a remote")
	}
	if commitSHA == "" || commitSHA == preSHA {
		t.Errorf("commit SHA missing or unchanged: pre=%q got=%q", preSHA, commitSHA)
	}
	// HEAD should now be commitSHA.
	head, _ := c.Head(context.Background(), dir)
	if head != commitSHA {
		t.Errorf("HEAD=%q want commitSHA=%q", head, commitSHA)
	}
}

func TestResetHard_RestoresPreCommitSHA(t *testing.T) {
	dir, preSHA := initLocalRepo(t)
	// Create + commit a change.
	if err := os.WriteFile(filepath.Join(dir, "f.txt"), []byte("x"), 0o644); err != nil {
		t.Fatal(err)
	}
	c := New()
	commitSHA, _ := c.CommitAndPush(context.Background(), dir, "to-be-rolled-back", "")
	if commitSHA == "" {
		t.Fatal("no commit created")
	}
	// Reset to the captured pre-commit SHA.
	if err := c.ResetHard(context.Background(), dir, preSHA); err != nil {
		t.Fatalf("ResetHard: %v", err)
	}
	head, _ := c.Head(context.Background(), dir)
	if head != preSHA {
		t.Errorf("after reset HEAD=%q want %q", head, preSHA)
	}
	// f.txt should be gone.
	if _, err := os.Stat(filepath.Join(dir, "f.txt")); !os.IsNotExist(err) {
		t.Errorf("f.txt should be removed: err=%v", err)
	}
}

func TestResetHard_RejectsNonAncestorSHA(t *testing.T) {
	dir, _ := initLocalRepo(t)
	c := New()
	if err := c.ResetHard(context.Background(), dir, "deadbeefdeadbeefdeadbeefdeadbeefdeadbeef"); err == nil {
		t.Fatal("non-ancestor SHA should be rejected")
	}
}

func TestResetHard_RejectsEmptyRef(t *testing.T) {
	dir, _ := initLocalRepo(t)
	c := New()
	if err := c.ResetHard(context.Background(), dir, ""); err == nil {
		t.Fatal("empty ref should be rejected")
	}
}

func TestClone_RefusesPopulatedDir(t *testing.T) {
	dst := t.TempDir()
	if err := os.WriteFile(filepath.Join(dst, "preexisting.txt"), []byte("x"), 0o644); err != nil {
		t.Fatal(err)
	}
	c := New()
	if err := c.Clone(context.Background(), "https://example.com/repo.git", dst, ""); err == nil {
		t.Fatal("clone should refuse a populated dir")
	}
}

func TestCreateRepo_201_NoError(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/orgs/acme/repos" {
			t.Errorf("path = %q", r.URL.Path)
		}
		if r.Header.Get("Authorization") != "Bearer test-token" {
			t.Errorf("token header missing")
		}
		w.WriteHeader(http.StatusCreated)
	}))
	defer srv.Close()
	c := New()
	c.githubAPIBase = srv.URL
	if err := c.CreateRepo(context.Background(), "acme", "fleet", true, "test-token"); err != nil {
		t.Errorf("201 should be success: %v", err)
	}
}

func TestCreateRepo_422_AlreadyExists_NoError(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusUnprocessableEntity)
		_, _ = w.Write([]byte(`{"message":"name already exists on this account"}`))
	}))
	defer srv.Close()
	c := New()
	c.githubAPIBase = srv.URL
	if err := c.CreateRepo(context.Background(), "acme", "fleet", true, "tok"); err != nil {
		t.Errorf("422 should be operational success: %v", err)
	}
}

// Review-pass: a generic 422 (not name-uniqueness) must NOT be
// swallowed as success.
func TestCreateRepo_422_GenericValidation_Surfaces(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusUnprocessableEntity)
		_, _ = w.Write([]byte(`{"message":"Validation Failed","errors":[{"resource":"Repository","field":"license_template","code":"invalid"}]}`))
	}))
	defer srv.Close()
	c := New()
	c.githubAPIBase = srv.URL
	err := c.CreateRepo(context.Background(), "acme", "fleet", true, "tok")
	if err == nil {
		t.Fatal("generic 422 should not be swallowed")
	}
	if !strings.Contains(err.Error(), "422") {
		t.Errorf("error should mention 422: %v", err)
	}
}

func TestCreateRepo_500_Surfaces(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusInternalServerError)
		_, _ = w.Write([]byte(`{"message":"server error"}`))
	}))
	defer srv.Close()
	c := New()
	c.githubAPIBase = srv.URL
	if err := c.CreateRepo(context.Background(), "acme", "fleet", true, "tok"); err == nil {
		t.Error("500 should surface")
	}
}

func TestCreateRepo_RequiresToken(t *testing.T) {
	c := New()
	if err := c.CreateRepo(context.Background(), "acme", "fleet", true, ""); err == nil {
		t.Error("empty token should be rejected")
	}
}

// --- SSH-vs-HTTPS auth selection (regression: cs/next 2026-07) ---
//
// The adapter used to hand githttp.BasicAuth to every transport, so a
// fleet cloned over SSH failed with go-git's opaque "invalid auth
// method" — and it failed *after* `bootstrap openbao init` had already
// initialized the vault, which then rolled the commit back and left the
// Shamir shares out of the fleet. These tests pin the URL classification
// and the auth type chosen per transport.

func TestIsSSHURL_Classification(t *testing.T) {
	ssh := []string{
		"git@github.com:kube-dc/kube-dc-fleet.git", // scp-like (what `git remote add` writes)
		"ssh://git@github.com/kube-dc/kube-dc-fleet.git",
		"ssh://git@gitlab.example.com:2222/org/repo.git",
		"deploy@host.internal:/srv/git/repo.git", // non-git user
		"host.internal:/srv/git/repo.git",        // userless scp-like (P2)
		"host.internal:srv/repo.git",             // userless scp-like, relative path
		"ssh://host.internal:2222/srv/repo.git",  // userless with port
	}
	for _, u := range ssh {
		if !isSSHURL(u) {
			t.Errorf("isSSHURL(%q) = false, want true", u)
		}
	}
	notSSH := []string{
		"https://github.com/kube-dc/kube-dc-fleet.git",
		"http://internal.example.com/repo.git",
		"git://github.com/org/repo.git",
		"file:///srv/git/repo.git",
		"", // no remote configured
	}
	for _, u := range notSSH {
		if isSSHURL(u) {
			t.Errorf("isSSHURL(%q) = true, want false", u)
		}
	}
}

// sshUser returns the explicit login, or "" when the URL omits one.
//
// Empty is required, NOT a fallback to "git": OpenSSH and go-git both
// connect as the current OS user for a userless ssh://host/path, and
// go-git's NewSSHAgentAuth("") resolves that. Hard-coding "git" broke
// self-hosted remotes that rely on the local login. Hosted forges always
// spell the user out, so they keep working.
func TestSSHUser_EmptyWhenURLOmitsUser(t *testing.T) {
	cases := map[string]string{
		"git@github.com:org/repo.git":             "git",
		"ssh://git@github.com/org/repo.git":       "git",
		"deploy@host.internal:/srv/repo.git":      "deploy",
		"ssh://deploy@host.internal/srv/repo.git": "deploy",
		"ssh://host.internal/srv/repo.git":        "", // -> current OS user
		"ssh://host.internal:2222/srv/repo.git":   "", // port, still no user
		"host.internal:/srv/repo.git":             "", // userless scp-like (P2)
	}
	for in, want := range cases {
		if got := sshUser(in); got != want {
			t.Errorf("sshUser(%q) = %q, want %q", in, got, want)
		}
	}
}

// HTTPS remotes must keep using the token — the historical behaviour.
func TestAuthFor_HTTPS_UsesToken(t *testing.T) {
	c := New()
	auth, err := c.authFor("https://github.com/org/repo.git", "ghp_example")
	if err != nil {
		t.Fatalf("authFor https: %v", err)
	}
	ba, ok := auth.(*githttp.BasicAuth)
	if !ok {
		t.Fatalf("auth = %T, want *githttp.BasicAuth", auth)
	}
	if ba.Password != "ghp_example" {
		t.Errorf("token not passed through: got %q", ba.Password)
	}
}

// An empty remote (no origin) must not error — fall back to token auth
// so `CommitAndPush` on a remote-less repo still commits locally.
func TestAuthFor_EmptyRemote_FallsBackToToken(t *testing.T) {
	c := New()
	auth, err := c.authFor("", "tok")
	if err != nil {
		t.Fatalf("authFor empty remote: %v", err)
	}
	if _, ok := auth.(*githttp.BasicAuth); !ok {
		t.Fatalf("auth = %T, want *githttp.BasicAuth", auth)
	}
}

// The core regression: an SSH remote must NOT be handed BasicAuth. It
// either resolves real SSH credentials or returns an actionable error —
// never the silent HTTP mismatch that produced "invalid auth method".
func TestAuthFor_SSH_NeverBasicAuth(t *testing.T) {
	c := New()
	auth, err := c.authFor("git@github.com:org/repo.git", "ghp_example")
	if err != nil {
		// No agent and no key in this environment: the message must
		// tell the operator how to fix it.
		for _, want := range []string{"SSH", "ssh-agent", "https://"} {
			if !strings.Contains(err.Error(), want) {
				t.Errorf("error %q missing guidance %q", err, want)
			}
		}
		return
	}
	if _, isBasic := auth.(*githttp.BasicAuth); isBasic {
		t.Fatal("SSH remote was given HTTP BasicAuth — the cs/next bug")
	}
}

// remoteURLsOf must split fetch vs push. go-git uses URLs[0] for fetch
// and URLs[len-1] for push (remote.go: "Fetch will always use the first
// URL, while push will use all of them"), because `git remote set-url
// --push` APPENDS. Selecting auth from URLs[0] alone hands HTTP creds to
// an SSH pushurl and reproduces "invalid auth method".
func TestRemoteURLsOf_SplitsFetchAndPush(t *testing.T) {
	dir, _ := initLocalRepo(t)
	repo, err := gogit.PlainOpen(dir)
	if err != nil {
		t.Fatal(err)
	}
	if f, p := remoteURLsOf(repo, ""); f != "" || p != "" {
		t.Errorf("no remote: got fetch=%q push=%q, want empty", f, p)
	}

	const fetchURL = "https://github.com/kube-dc/kube-dc-fleet.git"
	const pushURL = "git@github.com:kube-dc/kube-dc-fleet.git"
	if _, err := repo.CreateRemote(&gogitconfig.RemoteConfig{
		Name: gogit.DefaultRemoteName,
		URLs: []string{fetchURL, pushURL}, // pushurl appended, as git does
	}); err != nil {
		t.Fatal(err)
	}
	f, p := remoteURLsOf(repo, "")
	if f != fetchURL {
		t.Errorf("fetch = %q, want %q", f, fetchURL)
	}
	if p != pushURL {
		t.Errorf("push = %q, want %q (must be the LAST url)", p, pushURL)
	}

	// Single-URL remotes: fetch and push are the same.
	dir2, _ := initLocalRepo(t)
	repo2, _ := gogit.PlainOpen(dir2)
	if _, err := repo2.CreateRemote(&gogitconfig.RemoteConfig{
		Name: gogit.DefaultRemoteName,
		URLs: []string{pushURL},
	}); err != nil {
		t.Fatal(err)
	}
	if f, p := remoteURLsOf(repo2, ""); f != pushURL || p != pushURL {
		t.Errorf("single url: fetch=%q push=%q, want both %q", f, p, pushURL)
	}
}

// Mixed transports must resolve auth INDEPENDENTLY per direction — in
// both orderings.
func TestAuthFor_MixedTransports_BothDirections(t *testing.T) {
	c := New()

	// https fetch + ssh push (the common `set-url --push` case)
	if auth, err := c.authFor("https://github.com/o/r.git", "tok"); err != nil {
		t.Fatalf("https fetch: %v", err)
	} else if _, ok := auth.(*githttp.BasicAuth); !ok {
		t.Errorf("https fetch auth = %T, want *githttp.BasicAuth", auth)
	}
	if auth, err := c.authFor("git@github.com:o/r.git", "tok"); err == nil {
		if _, isBasic := auth.(*githttp.BasicAuth); isBasic {
			t.Error("ssh push got BasicAuth — wrong transport credentials")
		}
	}

	// ssh fetch + https push (the reverse)
	if auth, err := c.authFor("ssh://git@github.com/o/r.git", "tok"); err == nil {
		if _, isBasic := auth.(*githttp.BasicAuth); isBasic {
			t.Error("ssh fetch got BasicAuth — wrong transport credentials")
		}
	}
	if auth, err := c.authFor("https://github.com/o/r.git", "tok"); err != nil {
		t.Fatalf("https push: %v", err)
	} else if _, ok := auth.(*githttp.BasicAuth); !ok {
		t.Errorf("https push auth = %T, want *githttp.BasicAuth", auth)
	}
}

// startEmptyAgent runs a REAL ssh-agent (in-memory keyring holding no
// identities) on a unix socket and returns its path. A dead socket is
// not good enough: NewSSHAgentAuth fails to dial it, so the buggy
// "agent wins on reachability" path is never taken and the test passes
// vacuously. Only a reachable-but-empty agent exercises P1b.
func startEmptyAgent(t *testing.T) string {
	t.Helper()
	// Keep the path short — unix socket paths cap near 104 bytes.
	dir, err := os.MkdirTemp("", "kdcagt")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = os.RemoveAll(dir) })
	sock := filepath.Join(dir, "a.sock")
	ln, err := net.Listen("unix", sock)
	if err != nil {
		t.Skipf("unix socket unavailable: %v", err)
	}
	keyring := agent.NewKeyring() // deliberately empty
	go func() {
		for {
			conn, aerr := ln.Accept()
			if aerr != nil {
				return
			}
			go func() { _ = agent.ServeAgent(keyring, conn) }()
		}
	}()
	t.Cleanup(func() { _ = ln.Close() })
	return sock
}

// P1b: a REACHABLE agent that holds no usable signer must not suppress
// the on-disk key fallback. go-git's NewSSHAgentAuth only dials the
// socket — it never checks for signers — so the old "agent, else files"
// selection returned an empty agent and ~/.ssh/id_ed25519 was never
// offered, turning a recoverable case into an opaque transport failure.
// This drives the signer callback, which is what the handshake calls.
func TestSSHAuthFor_ReachableEmptyAgent_StillOffersKeyFiles(t *testing.T) {
	home := t.TempDir()
	t.Setenv("HOME", home)
	t.Setenv("SSH_AUTH_SOCK", startEmptyAgent(t))

	keyDir := filepath.Join(home, ".ssh")
	if err := os.MkdirAll(keyDir, 0o700); err != nil {
		t.Fatal(err)
	}
	writeTestEd25519Key(t, filepath.Join(keyDir, "id_ed25519"))

	auth, err := sshAuthFor("git")
	if err != nil {
		t.Fatalf("sshAuthFor: %v", err)
	}
	cb, ok := auth.(*gitssh.PublicKeysCallback)
	if !ok {
		t.Fatalf("auth = %T, want *gitssh.PublicKeysCallback", auth)
	}
	signers, err := cb.Callback()
	if err != nil {
		t.Fatalf("signer callback: %v", err)
	}
	if len(signers) == 0 {
		t.Fatal("empty agent suppressed the key-file fallback (the P1b bug)")
	}
}

// A dead agent socket must likewise fall back to key files.
func TestSSHAuthFor_DeadAgentSocket_StillOffersKeyFiles(t *testing.T) {
	home := t.TempDir()
	t.Setenv("HOME", home)
	t.Setenv("SSH_AUTH_SOCK", filepath.Join(home, "nonexistent.sock"))

	keyDir := filepath.Join(home, ".ssh")
	if err := os.MkdirAll(keyDir, 0o700); err != nil {
		t.Fatal(err)
	}
	writeTestEd25519Key(t, filepath.Join(keyDir, "id_ed25519"))

	auth, err := sshAuthFor("git")
	if err != nil {
		t.Fatalf("sshAuthFor: %v", err)
	}
	cb := auth.(*gitssh.PublicKeysCallback)
	signers, err := cb.Callback()
	if err != nil || len(signers) == 0 {
		t.Fatalf("key-file fallback failed: signers=%d err=%v", len(signers), err)
	}
}

// With neither agent nor key files, the caller must get the actionable
// error rather than a downstream transport failure.
func TestSSHAuthFor_NoCredentials_ActionableError(t *testing.T) {
	home := t.TempDir()
	t.Setenv("HOME", home)
	t.Setenv("SSH_AUTH_SOCK", "")

	_, err := sshAuthFor("git")
	if err == nil {
		t.Fatal("want error when no agent and no key files")
	}
	for _, want := range []string{"ssh-agent", "id_ed25519", "https://"} {
		if !strings.Contains(err.Error(), want) {
			t.Errorf("error %q missing guidance %q", err, want)
		}
	}
}

// writeTestEd25519Key writes a throwaway unencrypted ed25519 key in
// OpenSSH format so NewPublicKeysFromFile can parse it.
func writeTestEd25519Key(t *testing.T, path string) {
	t.Helper()
	_, priv, err := ed25519.GenerateKey(rand.Reader)
	if err != nil {
		t.Fatal(err)
	}
	blk, err := gossh.MarshalPrivateKey(priv, "")
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, pem.EncodeToMemory(blk), 0o600); err != nil {
		t.Fatal(err)
	}
}

// Directly pins the Pull→fetch / Push→push WIRING. The earlier tests
// covered remoteURLsOf() and authFor() independently, so mutating the
// Push call site back to the fetch URL still passed. This asserts the
// direction actually consulted, using a remote whose two URLs need
// different credential types.
func TestAuthForDirection_PullUsesFetch_PushUsesPush(t *testing.T) {
	c := New()

	// fetch = https (token), push = ssh (keys)
	dir, _ := initLocalRepo(t)
	repo, err := gogit.PlainOpen(dir)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := repo.CreateRemote(&gogitconfig.RemoteConfig{
		Name: gogit.DefaultRemoteName,
		URLs: []string{"https://github.com/o/r.git", "git@github.com:o/r.git"},
	}); err != nil {
		t.Fatal(err)
	}
	fetchAuth, err := c.authForDirection(repo, dirFetch, "tok")
	if err != nil {
		t.Fatalf("fetch direction: %v", err)
	}
	if _, ok := fetchAuth.(*githttp.BasicAuth); !ok {
		t.Errorf("fetch auth = %T, want *githttp.BasicAuth (https fetch url)", fetchAuth)
	}
	if pushAuth, err := c.authForDirection(repo, dirPush, "tok"); err == nil {
		if _, isBasic := pushAuth.(*githttp.BasicAuth); isBasic {
			t.Error("push direction used the FETCH url — push must use the LAST url")
		}
	}

	// Reversed: fetch = ssh, push = https.
	dir2, _ := initLocalRepo(t)
	repo2, _ := gogit.PlainOpen(dir2)
	if _, err := repo2.CreateRemote(&gogitconfig.RemoteConfig{
		Name: gogit.DefaultRemoteName,
		URLs: []string{"git@github.com:o/r.git", "https://github.com/o/r.git"},
	}); err != nil {
		t.Fatal(err)
	}
	pushAuth2, err := c.authForDirection(repo2, dirPush, "tok")
	if err != nil {
		t.Fatalf("push direction: %v", err)
	}
	if _, ok := pushAuth2.(*githttp.BasicAuth); !ok {
		t.Errorf("push auth = %T, want *githttp.BasicAuth (https push url)", pushAuth2)
	}
	if fetchAuth2, err := c.authForDirection(repo2, dirFetch, "tok"); err == nil {
		if _, isBasic := fetchAuth2.(*githttp.BasicAuth); isBasic {
			t.Error("fetch direction used the PUSH url — fetch must use the FIRST url")
		}
	}
}
