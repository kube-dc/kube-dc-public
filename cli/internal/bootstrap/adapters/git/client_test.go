package git

import (
	"context"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	gogit "github.com/go-git/go-git/v5"
	"github.com/go-git/go-git/v5/plumbing/object"

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
