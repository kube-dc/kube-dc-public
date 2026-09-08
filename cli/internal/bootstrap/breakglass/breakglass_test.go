package breakglass

import (
	"context"
	"errors"
	"strings"
	"testing"

	"github.com/shalb/kube-dc/cli/internal/bootstrap/ports"
)

// TestAssertSecretsEncrypted covers the fail-closed guard that prevents a
// cleartext credential from ever reaching the committed break-glass file
// (regression guard for the kube-dc E2E 2026-07-08 P1 leak, where the fleet
// .sops.yaml `^(data|stringData)$` regex left the kubeconfig token in plain).
func TestAssertSecretsEncrypted(t *testing.T) {
	const token = "eyJhbGciOiJSUzI1NiIsImtpZCI6IkFCQ0RFRkdISUpLIn0.payloadpayloadpayload.sigsigsig"

	tests := []struct {
		name      string
		plain     string
		encrypted string
		wantErr   bool
	}{
		{
			name:      "token left in cleartext → error",
			plain:     "users:\n- user:\n    token: " + token + "\n",
			encrypted: "users:\n- user:\n    token: " + token + "\n", // sops no-op'd the field
			wantErr:   true,
		},
		{
			name:      "token encrypted → ok",
			plain:     "users:\n- user:\n    token: " + token + "\n",
			encrypted: "users:\n- user:\n    token: ENC[AES256_GCM,data:ZmFrZQ==,iv:x,tag:y,type:str]\n",
			wantErr:   false,
		},
		{
			name:      "public CA data left plaintext is fine (not a guarded key)",
			plain:     "clusters:\n- cluster:\n    certificate-authority-data: LS0tLS1CRUdJTkNFUlQ=\n",
			encrypted: "clusters:\n- cluster:\n    certificate-authority-data: LS0tLS1CRUdJTkNFUlQ=\n",
			wantErr:   false,
		},
		{
			name:      "client-key-data left in cleartext → error",
			plain:     "users:\n- user:\n    client-key-data: LS0tLS1CRUdJTlBSSVZBVEVLRVk=\n",
			encrypted: "users:\n- user:\n    client-key-data: LS0tLS1CRUdJTlBSSVZBVEVLRVk=\n",
			wantErr:   true,
		},
		{
			name:      "empty/trivial token value is skipped",
			plain:     "users:\n- user:\n    token: \"\"\n",
			encrypted: "users:\n- user:\n    token: \"\"\n",
			wantErr:   false,
		},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			err := assertSecretsEncrypted([]byte(tc.plain), []byte(tc.encrypted))
			if tc.wantErr && err == nil {
				t.Fatalf("expected error (plaintext secret should be detected), got nil")
			}
			if !tc.wantErr && err != nil {
				t.Fatalf("expected no error, got %v", err)
			}
		})
	}
}

// fakeGit is a minimal ports.GitClient double for exercising
// commitBreakGlass's decision tree without a real git checkout. Only
// Commit/CommitAndPush are meaningfully instrumented; every other method
// is unreachable from commitBreakGlass and just satisfies the interface.
type fakeGit struct {
	commitErr        error
	commitAndPushErr error

	commitCalls        []string // dir+"|"+msg
	commitAndPushCalls []string // dir+"|"+msg+"|"+token
}

func (f *fakeGit) Commit(_ context.Context, dir, msg string) (string, error) {
	f.commitCalls = append(f.commitCalls, dir+"|"+msg)
	if f.commitErr != nil {
		return "", f.commitErr
	}
	return "deadbeef", nil
}

func (f *fakeGit) CommitAndPush(_ context.Context, dir, msg, token string) (string, error) {
	f.commitAndPushCalls = append(f.commitAndPushCalls, dir+"|"+msg+"|"+token)
	if f.commitAndPushErr != nil {
		return "", f.commitAndPushErr
	}
	return "deadbeef", nil
}

func (f *fakeGit) Clone(context.Context, string, string, string) error { return nil }
func (f *fakeGit) Init(context.Context, string, string) error          { return nil }
func (f *fakeGit) Pull(context.Context, string, string) error          { return nil }
func (f *fakeGit) Diff(context.Context, string) (ports.Diff, error)    { return ports.Diff{}, nil }
func (f *fakeGit) Head(context.Context, string) (string, error)        { return "", nil }
func (f *fakeGit) Push(context.Context, string, string) error          { return nil }
func (f *fakeGit) ResetHard(context.Context, string, string) error     { return nil }
func (f *fakeGit) CreateRepo(context.Context, string, string, bool, string) error {
	return nil
}

var _ ports.GitClient = (*fakeGit)(nil)

// TestCommitBreakGlass covers the four paths bootstrap init's automated
// finalize step depends on: the standalone-CLI contract (Git nil → no
// commit, just the manual-next-step reminder), the --no-push finalize
// path (local commit only, no token used), the default finalize path
// (commit AND push, with the resolved GitHub token), and a push failure
// surfacing as an error that names the file so the operator can recover
// by hand rather than silently losing the write.
func TestCommitBreakGlass(t *testing.T) {
	const target = "/fleet/clusters/jed/break-glass-kubeconfig.enc.yaml"

	t.Run("nil Git — manual-CLI contract, no commit attempted", func(t *testing.T) {
		err := commitBreakGlass(context.Background(), AdoptOpts{
			FleetRoot:   "/fleet",
			ClusterName: "jed",
		}, target, "adopt break-glass recovery kubeconfig")
		if err != nil {
			t.Fatalf("expected nil (no Git configured is not an error), got %v", err)
		}
	})

	t.Run("NoPush commits locally without pushing", func(t *testing.T) {
		g := &fakeGit{}
		err := commitBreakGlass(context.Background(), AdoptOpts{
			FleetRoot:   "/fleet",
			ClusterName: "jed",
			Git:         g,
			NoPush:      true,
			GitHubToken: "should-not-be-used",
		}, target, "adopt break-glass recovery kubeconfig")
		if err != nil {
			t.Fatalf("unexpected error: %v", err)
		}
		if len(g.commitAndPushCalls) != 0 {
			t.Fatalf("NoPush must not push: got CommitAndPush calls %v", g.commitAndPushCalls)
		}
		want := "/fleet|jed: adopt break-glass recovery kubeconfig"
		if len(g.commitCalls) != 1 || g.commitCalls[0] != want {
			t.Fatalf("Commit call = %v, want [%q]", g.commitCalls, want)
		}
	})

	t.Run("default path commits and pushes with the resolved token", func(t *testing.T) {
		g := &fakeGit{}
		err := commitBreakGlass(context.Background(), AdoptOpts{
			FleetRoot:   "/fleet",
			ClusterName: "jed",
			Git:         g,
			GitHubToken: "ghp_xxx",
		}, target, "adopt break-glass recovery kubeconfig")
		if err != nil {
			t.Fatalf("unexpected error: %v", err)
		}
		if len(g.commitCalls) != 0 {
			t.Fatalf("push path must not use the local-only Commit: got %v", g.commitCalls)
		}
		want := "/fleet|jed: adopt break-glass recovery kubeconfig|ghp_xxx"
		if len(g.commitAndPushCalls) != 1 || g.commitAndPushCalls[0] != want {
			t.Fatalf("CommitAndPush call = %v, want [%q]", g.commitAndPushCalls, want)
		}
	})

	t.Run("push failure surfaces the encrypted file's path for manual recovery", func(t *testing.T) {
		g := &fakeGit{commitAndPushErr: errors.New("remote rejected")}
		err := commitBreakGlass(context.Background(), AdoptOpts{
			FleetRoot:   "/fleet",
			ClusterName: "jed",
			Git:         g,
		}, target, "adopt break-glass recovery kubeconfig")
		if err == nil {
			t.Fatal("expected an error from the failed push")
		}
		if !errors.Is(err, g.commitAndPushErr) {
			t.Fatalf("error does not wrap the underlying push error: %v", err)
		}
	})

	t.Run("dirty file OTHER than the target blocks the commit (closes the post-check-pre-commit window)", func(t *testing.T) {
		g := &fakeGitDiff{diff: ports.Diff{Files: []ports.FileDiff{
			{Path: "clusters/jed/some-other-file.yaml", Status: "M"},
		}}}
		err := commitBreakGlass(context.Background(), AdoptOpts{
			FleetRoot:   "/fleet",
			ClusterName: "jed",
			Git:         g,
		}, target, "adopt break-glass recovery kubeconfig")
		if !errors.Is(err, ErrDirtyWorkingTree) {
			t.Fatalf("expected ErrDirtyWorkingTree, got %v", err)
		}
		if len(g.commitAndPushCalls) != 0 {
			t.Fatalf("must not commit when an unrelated file is dirty: got %v", g.commitAndPushCalls)
		}
	})

	t.Run("the target file's own diff does not self-block the commit", func(t *testing.T) {
		g := &fakeGitDiff{diff: ports.Diff{Files: []ports.FileDiff{
			{Path: "clusters/jed/break-glass-kubeconfig.enc.yaml", Status: "M"},
		}}}
		err := commitBreakGlass(context.Background(), AdoptOpts{
			FleetRoot:   "/fleet",
			ClusterName: "jed",
			Git:         g,
		}, target, "adopt break-glass recovery kubeconfig")
		if err != nil {
			t.Fatalf("the file this call is about to commit must be excluded from the dirty check: %v", err)
		}
		if len(g.commitAndPushCalls) != 1 {
			t.Fatalf("expected exactly one CommitAndPush call, got %v", g.commitAndPushCalls)
		}
	})
}

// fakeGitDiff extends fakeGit with a scriptable Diff, for
// checkCleanWorkingTree coverage.
type fakeGitDiff struct {
	fakeGit
	diff    ports.Diff
	diffErr error
}

func (f *fakeGitDiff) Diff(_ context.Context, _ string) (ports.Diff, error) {
	return f.diff, f.diffErr
}

// TestCheckCleanWorkingTree covers the guard that stops Adopt/Rotate's
// automated commit from sweeping in unrelated operator work-in-progress:
// the real Git adapter's Commit(AndPush) always `git add`s the WHOLE
// working tree (see commitLocal in the adapter), so this must run BEFORE
// any mutation and refuse when the tree isn't clean — mirroring
// openbao.Init's identical pre-flight gate against the same adapter.
func TestCheckCleanWorkingTree(t *testing.T) {
	t.Run("nil Git — standalone CLI path never checks", func(t *testing.T) {
		if err := checkCleanWorkingTree(context.Background(), AdoptOpts{FleetRoot: "/fleet"}, ""); err != nil {
			t.Fatalf("expected nil (no Git configured), got %v", err)
		}
	})

	t.Run("clean tree passes", func(t *testing.T) {
		g := &fakeGitDiff{}
		err := checkCleanWorkingTree(context.Background(), AdoptOpts{FleetRoot: "/fleet", Git: g}, "")
		if err != nil {
			t.Fatalf("unexpected error: %v", err)
		}
	})

	t.Run("dirty tree is refused, naming the offending paths", func(t *testing.T) {
		g := &fakeGitDiff{diff: ports.Diff{Files: []ports.FileDiff{
			{Path: "clusters/jed/some-other-file.yaml", Status: "M"},
		}}}
		err := checkCleanWorkingTree(context.Background(), AdoptOpts{FleetRoot: "/fleet", Git: g}, "")
		if !errors.Is(err, ErrDirtyWorkingTree) {
			t.Fatalf("expected ErrDirtyWorkingTree, got %v", err)
		}
		if !strings.Contains(err.Error(), "some-other-file.yaml") {
			t.Fatalf("error should name the dirty path for diagnosis, got: %v", err)
		}
	})

	t.Run("allow excludes exactly one path, everything else still blocks", func(t *testing.T) {
		g := &fakeGitDiff{diff: ports.Diff{Files: []ports.FileDiff{
			{Path: "clusters/jed/break-glass-kubeconfig.enc.yaml", Status: "M"},
			{Path: "clusters/jed/some-other-file.yaml", Status: "M"},
		}}}
		err := checkCleanWorkingTree(context.Background(), AdoptOpts{FleetRoot: "/fleet", Git: g},
			"clusters/jed/break-glass-kubeconfig.enc.yaml")
		if !errors.Is(err, ErrDirtyWorkingTree) {
			t.Fatalf("the OTHER dirty file must still block even with an allow path set, got %v", err)
		}
		if strings.Contains(err.Error(), "break-glass-kubeconfig.enc.yaml") {
			t.Fatalf("the allowed path must not appear in the dirty-paths list: %v", err)
		}
	})

	t.Run("Diff error propagates instead of being treated as clean", func(t *testing.T) {
		g := &fakeGitDiff{diffErr: errors.New("repo not found")}
		err := checkCleanWorkingTree(context.Background(), AdoptOpts{FleetRoot: "/fleet", Git: g}, "")
		if err == nil {
			t.Fatal("expected the Diff error to surface, got nil")
		}
		if errors.Is(err, ErrDirtyWorkingTree) {
			t.Fatalf("a Diff failure is not the same as a dirty tree, got: %v", err)
		}
	})
}
