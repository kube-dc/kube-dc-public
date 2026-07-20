package ports

import "context"

// GitClient is the contract for Git operations against the fleet repo.
// Real adapter uses go-git (no shell-out for the core flow); mock adapter
// simulates an in-memory repo.
//
// The interface is shaped around the atomic-Git contract from agent rule
// 8: every mutation is bracketed by clean-tree check, capture pre-commit
// SHA via Head, commit + push, rollback-via-pre-commit-SHA on failure.
// See M4-T12 for the canonical usage pattern:
//
//	pre, _ := g.Head(ctx, dir)              // capture rollback target
//	diff, _ := g.Diff(ctx, dir)
//	if len(diff.Files) > 0 && !allowDirty { return ErrDirty }
//	sha, err := g.CommitAndPush(ctx, dir, msg, tok)
//	if err != nil { _ = g.ResetHard(ctx, dir, pre); return err }
//
// **Auth**: GitClient methods that talk to a remote (Clone, Pull,
// CommitAndPush, CreateRepo) take an explicit `token` argument. The
// adapter never reads the operator's `$GITHUB_TOKEN` directly — wire
// layer passes it in. This keeps the auth surface visible at the call
// site and makes mocking straightforward (mock ignores token).
type GitClient interface {
	// Clone clones repoURL into dir. dir MUST be empty or non-existent
	// (the adapter refuses to clone into a populated directory to avoid
	// merging with an unrelated checkout).
	Clone(ctx context.Context, repoURL, dir, token string) error

	// Pull is `git pull --rebase` against the configured upstream of
	// `dir`. Used to refresh the fleet repo cache before reading it.
	// `token` is the GitHub PAT used to authenticate against private
	// repos — every customer fleet repo we touch is private, so this
	// is always required in practice. Empty `token` works only for
	// genuinely-public repos (rare in this product).
	Pull(ctx context.Context, dir, token string) error

	// Diff returns the working-tree diff. Empty Diff.Files means clean.
	// Used by agent rule 8 to gate mutations on a clean working tree.
	Diff(ctx context.Context, dir string) (Diff, error)

	// Head returns the SHA of the current HEAD commit in `dir`. Used
	// by callers performing an atomic mutation to capture the rollback
	// target BEFORE invoking CommitAndPush — agent rule 8 mandates
	// `git reset --hard <pre-commit-sha>` (never `HEAD~1`) when push
	// fails. See the example flow in the interface godoc above.
	Head(ctx context.Context, dir string) (string, error)

	// CommitAndPush stages all changes in `dir`, creates a commit with
	// the supplied message, and pushes to the configured remote.
	// Returns the newly-created commit SHA on success; the caller
	// records this for diagnostic logging (rollback uses the *pre*-
	// commit SHA captured via Head before this call).
	//
	// On push failure, the local commit IS NOT automatically rolled
	// back — the caller calls ResetHard with the pre-commit SHA they
	// captured BEFORE invoking CommitAndPush. This split is deliberate:
	// the rollback decision is the caller's, not the adapter's, because
	// some workflows (e.g. operator manually fixing the push conflict)
	// want to keep the local commit.
	CommitAndPush(ctx context.Context, dir, msg, token string) (commitSHA string, err error)

	// Commit is CommitAndPush minus the push — stages all changes in
	// `dir` + creates a local commit + returns its SHA. Used by
	// M4-T12's apply path when the operator passed `--no-push`
	// (debug / CI / local-testing flow). For production installs,
	// prefer CommitAndPush since Flux can only reconcile from a
	// pushed commit.
	Commit(ctx context.Context, dir, msg string) (commitSHA string, err error)

	// Push pushes the current HEAD of `dir` to its upstream. It creates
	// no commit (unlike CommitAndPush) — it's the push half on its own.
	// Idempotent: a no-op (nil error) when the remote is already up to
	// date. Used by the resume path (M4): when a prior apply committed
	// the cluster overlay locally but the push didn't land (--no-push
	// then re-run, or a failed push left HEAD ahead of upstream), a
	// resume must push HEAD so the branch Flux reconciles actually
	// contains the overlay — skipping the push would point flux-install
	// at a remote missing the cluster.
	Push(ctx context.Context, dir, token string) error

	// ResetHard is `git reset --hard <ref>` against `dir`. Used for
	// commit rollback per agent rule 8. Refuses to run when the named
	// `ref` is not an ancestor of HEAD (prevents accidental forward-
	// skip resets). Pass the SHA returned by a prior Head() call as
	// `ref` — never `HEAD~1` (a foreign commit on top of yours would
	// be silently discarded otherwise).
	ResetHard(ctx context.Context, dir, ref string) error

	// CreateRepo creates a new empty GitHub repo via the GitHub API.
	// Used by M4-T05 when --fleet-mode=new-repo + the named owner/repo
	// doesn't exist yet. Returns nil on 201 Created AND on 422 (already
	// exists) — both are operationally success.
	CreateRepo(ctx context.Context, owner, repo string, private bool, token string) error
}

// Diff is the working-tree state of a git checkout. Empty Files means
// "clean tree, safe to mutate" per agent rule 8.
type Diff struct {
	// Files lists every path with uncommitted changes. Includes
	// untracked files (matching `git status --porcelain` behaviour)
	// because an untracked file in `clusters/<name>/` would also be
	// included by `git add` and we want the operator to acknowledge it.
	Files []FileDiff
}

// FileDiff is one entry from the working-tree diff.
type FileDiff struct {
	Path   string
	Status string // "M" modified | "A" added | "D" deleted | "??" untracked | "R" renamed
}
