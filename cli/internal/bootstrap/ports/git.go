package ports

import (
	"context"
	"errors"
)

// TreeFileReader returns the content of a regular file at path in one Git tree
// object and whether the tree has it. Trees are content-addressed, so the bytes
// are exactly what any commit recording that tree contains.
type TreeFileReader func(path string) (data []byte, present bool, err error)

// CommitTrees are the two trees a commit check decides on.
type CommitTrees struct {
	// Parent is the tree of the commit's parent.
	Parent TreeFileReader
	// Candidate is the tree the new commit records.
	Candidate TreeFileReader
}

// Checkout is a checkout's branch and commit, read together.
type Checkout struct {
	// Branch is the full ref HEAD names (refs/heads/<name>); "" when detached.
	Branch string
	// SHA is the commit HEAD resolves to.
	SHA string
}

// RunTempDir is the scratch directory that belongs to the private checkout
// work: created and removed with it, and outside every checkout. Subprocesses
// of the run get it as TMPDIR, so what a killed one leaves behind is removed
// with the run (or by the next run) and never lands in the operator's
// checkout, wherever the operator's own TMPDIR points.
func RunTempDir(work string) string { return work + ".tmp" }

// ErrNothingToCommit is returned by CommitIsolated when the private checkout
// holds no change against the parent commit.
var ErrNothingToCommit = errors.New("git: nothing to commit")

// ErrNotRegularFile is returned by a TreeFileReader, with present=true, for a
// path that is in the tree but is not a regular file (a directory, a symlink,
// a submodule).
var ErrNotRegularFile = errors.New("git: not a regular file in the tree")

// ErrPartialCheckout is returned by Advance when Git started writing the new
// commit's files and then failed (a full disk, a directory it may not write
// to). The branch is back at the old commit, but the checkout is no longer as
// it was: the error names the files that were written.
var ErrPartialCheckout = errors.New("git: the checkout failed part-way")

// ErrPublishRefused wraps the error of a PublishCheck: nothing was pushed.
var ErrPublishRefused = errors.New("git: the publication check refused the push")

// PublishCheck inspects what a push would newly publish, right before it is
// sent.
//
// Check receives every version of every file under Roots whose path Want
// accepts, in every commit the push would newly publish: the commits
// reachable from the pushed commit (through every parent of a merge, by the
// parents the commit objects themselves record) that are not reachable from a
// branch the push destination advertises at that moment. A stale local
// picture of the remote is not trusted; when the destination cannot be asked,
// nothing counts as published. Files are read from each commit's tree, not
// from a checkout (which may be sparse), and a file is found under each of its
// paths even when another path has the same content.
type PublishCheck struct {
	// Destination, when set, judges the one URL the push will go to (the
	// URL Git itself resolves for a push to origin) before anything else.
	Destination func(pushURL string) error
	Want        func(path string) bool
	Roots       []string
	Check       func(files []UnpublishedFile) error
}

// ErrPushUnconfirmed means a push reported no error but the destination could
// not be asked afterwards whether it has the commit: it may or may not be
// published.
var ErrPushUnconfirmed = errors.New("git: the push could not be confirmed at the destination")

// UnpublishedFile is one version of a file in what a push publishes: in the
// tip itself (inspected always) or in a commit the destination lacks.
type UnpublishedFile struct {
	Path string
	Blob string // object ID
	Data []byte
}

// GitIsolatedClient lets a run generate and commit in a private checkout, so
// the operator's checkout is never a scratch area: nothing is written, staged
// or committed there until the finished commit is fast-forwarded in.
//
// The run captures the starting checkout (Checkout), works in a private
// worktree at that commit (Isolate), commits there (CommitIsolated), pushes
// that exact commit to the starting branch (PushCommit), and only then moves
// the operator's branch, index and worktree forward (Advance). A failure
// before Advance leaves the operator's checkout exactly as it was.
type GitIsolatedClient interface {
	// Checkout reads HEAD's branch and commit in one step.
	Checkout(ctx context.Context, dir string) (Checkout, error)

	// Isolate creates a private worktree of dir's repository at commit sha
	// (detached, in a directory only the current user can enter, on the
	// repository's own filesystem and outside every checkout) and returns
	// its path. discard removes it; it is safe to call after any outcome.
	// No hook of the repository runs. Next to it, RunTempDir(work) is a
	// scratch directory of the run, with the same lifetime.
	Isolate(ctx context.Context, dir, sha string) (work string, discard func() error, err error)

	// CommitIsolated records everything in the private worktree work (made
	// by Isolate for dir) as a commit on parent (work's current commit)
	// and returns its ID. Ignore rules apply as with `git add -A` in dir:
	// a path dir's own ignore policy excludes is never recorded. check
	// (when non-nil) runs against the commit's tree and the parent's before
	// the commit exists; its error aborts. Nothing changed returns
	// ErrNothingToCommit. No hook runs and no ref outside work moves.
	CommitIsolated(ctx context.Context, dir, work, msg, parent string, check func(CommitTrees) error) (string, error)

	// TreeOf reads files of the commit sha in dir's repository.
	TreeOf(ctx context.Context, dir, sha string) (TreeFileReader, error)

	// PushCommit pushes exactly at.SHA to at.Branch on the remote, whatever
	// any checkout's HEAD names. When the transport reports an error but
	// the remote branch is at at.SHA, the commit is published and no error
	// is returned.
	//
	// The push destination (URL and credentials) is resolved once. With a
	// non-nil check, that same destination is first asked for its branch
	// tips, check sees what the push would newly publish to it
	// (PublishCheck), and a check error (wrapped in ErrPublishRefused)
	// stops before anything is sent. A push destination that changed in the
	// configuration meanwhile is refused as well.
	PushCommit(ctx context.Context, dir, token string, at Checkout, check *PublishCheck) error

	// ResumeAdvance finishes an Advance that a killed process left between
	// moving the branch and updating index and worktree: the branch already
	// names the new commit while the checkout still holds the old one. It
	// reports whether there was one. Local changes that overlap are
	// refused, as in Advance, and nothing is overwritten.
	ResumeAdvance(ctx context.Context, dir string) (resumed bool, err error)

	// Advance fast-forwards dir from from.SHA to the descendant commit to.
	// HEAD must name from.Branch at from.SHA. The branch is moved first, by
	// compare-and-swap, so nothing else can commit onto the old tip; then
	// index and worktree move with Git's own two-tree merge, which refuses
	// (changing nothing) when a path the new commit touches has a local
	// change, a staged change, a local deletion or a file in the way. On a
	// refusal the branch is put back. Unrelated local work is left as it
	// is. The index is never rewound: there is no reverse merge.
	Advance(ctx context.Context, dir string, from Checkout, to string) error
}

// GitProviderBinder returns a client restricted to one reviewed provider host.
// Production adapters implement this without changing ambient Git settings.
type GitProviderBinder interface {
	ForProvider(context.Context, string, string) (GitClient, error)
}

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

	// Init creates an empty git repository in dir with `branch` as the
	// initial HEAD branch. Idempotent: an existing repository is left
	// untouched (nil error). Used by the fleet-starter extraction — a
	// freshly extracted starter must be a repo on a deterministic
	// branch BEFORE the engine's commit+push and flux bootstrap steps
	// (branch detection, installer-bugs B5).
	Init(ctx context.Context, dir, branch string) error

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
