package git

import (
	"bytes"
	"context"
	"crypto/sha256"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"sort"
	"strings"
	"sync"
	"time"

	gogit "github.com/go-git/go-git/v5"
	"github.com/go-git/go-git/v5/config"
	"github.com/go-git/go-git/v5/plumbing"
	"github.com/go-git/go-git/v5/plumbing/transport"
	gitssh "github.com/go-git/go-git/v5/plumbing/transport/ssh"

	"github.com/shalb/kube-dc/cli/internal/bootstrap/ports"
)

// This file is the adapter half of ports.GitIsolatedClient.
//
// WHY: generating into the operator's checkout made that checkout a scratch
// area. Publishing then needed `git add .` (which publishes whatever else is
// there) and a failed run needed `git reset --hard` + `git clean` (which
// discard whatever else is there). No amount of checking closes the races
// between such a check and such a command. So the run works in a private
// worktree instead, commits there, and the operator's checkout only ever
// receives a finished commit through Git's own fast-forward merge, which
// refuses when it would touch a local change.
//
// These operations use the git binary: linked worktrees, index.lock and ref
// lock files are Git's own protocols, and go-git implements none of them the
// way a concurrent git process expects.

// gitArgs are the options every git process of this flow runs with: no
// repository hook, and no filesystem-monitor hook either (core.fsmonitor names
// a program that index-reading commands would run).
func gitArgs(dir string, args ...string) []string {
	// --no-replace-objects: go-git, which pushes, follows the parents a commit
	// object records. `git replace` (and a graft made with it) would show
	// these commands a different history than the one that is published.
	return append([]string{"--no-replace-objects", "-C", dir, "-c", "core.hooksPath=/dev/null", "-c", "core.fsmonitor=false"}, args...)
}

// runGitRaw runs git in dir with no hook and no prompt, and returns stdout
// exactly as written (NUL-separated lists must not be trimmed: a path may
// start or end with a space). Literal pathspecs: no path is ever a pattern.
func runGitRaw(ctx context.Context, dir string, stdin []byte, env []string, args ...string) (string, error) {
	cmd := exec.CommandContext(ctx, "git", gitArgs(dir, args...)...)
	cmd.Env = append(append(gitEnv(), "GIT_TERMINAL_PROMPT=0", "GIT_LITERAL_PATHSPECS=1"), env...)
	if stdin != nil {
		cmd.Stdin = bytes.NewReader(stdin)
	}
	var stdout, stderr bytes.Buffer
	cmd.Stdout, cmd.Stderr = &stdout, &stderr
	if err := cmd.Run(); err != nil {
		return "", fmt.Errorf("git %s: %w: %s", args[0], err, strings.TrimSpace(stderr.String()))
	}
	return stdout.String(), nil
}

// runGit is runGitRaw for a single value: surrounding whitespace is trimmed.
func runGit(ctx context.Context, dir string, stdin []byte, env []string, args ...string) (string, error) {
	out, err := runGitRaw(ctx, dir, stdin, env, args...)
	return strings.TrimSpace(out), err
}

// nulList splits NUL-separated output into its entries.
func nulList(out string) []string {
	var items []string
	for _, item := range strings.Split(out, "\x00") {
		if item != "" {
			items = append(items, item)
		}
	}
	return items
}

// repoLocalEnv are the variables that point a git process at a repository,
// worktree, index or object store other than the one `-C <dir>` names (git
// rev-parse --local-env-vars). Inherited from the operator's shell, they would
// send "the private checkout's" commands to the operator's checkout.
var repoLocalEnv = map[string]bool{
	"GIT_DIR": true, "GIT_WORK_TREE": true, "GIT_INDEX_FILE": true, "GIT_COMMON_DIR": true,
	"GIT_OBJECT_DIRECTORY": true, "GIT_ALTERNATE_OBJECT_DIRECTORIES": true, "GIT_IMPLICIT_WORK_TREE": true,
	"GIT_GRAFT_FILE": true, "GIT_NO_REPLACE_OBJECTS": true, "GIT_REPLACE_REF_BASE": true, "GIT_PREFIX": true,
	"GIT_SHALLOW_FILE": true, "GIT_NAMESPACE": true, "GIT_CONFIG": true, "GIT_CONFIG_PARAMETERS": true,
	"GIT_CONFIG_COUNT": true, "GIT_LITERAL_PATHSPECS": true, "GIT_GLOB_PATHSPECS": true,
	"GIT_NOGLOB_PATHSPECS": true, "GIT_ICASE_PATHSPECS": true,
}

// gitEnv is the process environment without repoLocalEnv.
func gitEnv() []string {
	env := make([]string, 0, len(os.Environ()))
	for _, kv := range os.Environ() {
		name, _, _ := strings.Cut(kv, "=")
		if repoLocalEnv[name] || strings.HasPrefix(name, "GIT_CONFIG_KEY_") || strings.HasPrefix(name, "GIT_CONFIG_VALUE_") {
			continue
		}
		env = append(env, kv)
	}
	return env
}

func validSHA(sha string) error {
	if h := plumbing.NewHash(sha); h.IsZero() || h.String() != sha {
		return fmt.Errorf("invalid commit %q", sha)
	}
	return nil
}

func validCheckout(at ports.Checkout) (plumbing.ReferenceName, error) {
	branch := plumbing.ReferenceName(at.Branch)
	if !branch.IsBranch() {
		return "", fmt.Errorf("%q is not a branch ref", at.Branch)
	}
	return branch, validSHA(at.SHA)
}

// Checkout reads HEAD's branch and commit together (ports.GitIsolatedClient).
func (c *Client) Checkout(_ context.Context, dir string) (ports.Checkout, error) {
	repo, err := openRepo(dir)
	if err != nil {
		return ports.Checkout{}, fmt.Errorf("git: open %s: %w", dir, err)
	}
	head, err := repo.Storer.Reference(plumbing.HEAD)
	if err != nil {
		return ports.Checkout{}, fmt.Errorf("git: head %s: %w", dir, err)
	}
	if head.Type() != plumbing.SymbolicReference {
		return ports.Checkout{SHA: head.Hash().String()}, nil
	}
	ref, err := repo.Reference(head.Target(), true)
	if err != nil {
		return ports.Checkout{}, fmt.Errorf("git: read %s in %s: %w", head.Target().Short(), dir, err)
	}
	return ports.Checkout{Branch: head.Target().String(), SHA: ref.Hash().String()}, nil
}

// Isolate adds a detached worktree of dir's repository at sha in a new
// directory only this user can enter (ports.GitIsolatedClient).
//
// The directory is inside the repository's own Git directory
// (<git-dir>/kube-dc-init/run-*), not under the system temp directory: the
// fleet scripts are executed from it, and a temp directory is often mounted
// noexec or is a small tmpfs. There it is on the fleet's own filesystem, the
// user can always write to it, and no checkout's `git status` ever sees it.
//
// A run that was killed cannot discard its checkout (which may hold a
// plaintext secret), so each run holds an exclusive lock on a file next to its
// directory for as long as it lives, and Isolate first removes the checkouts
// whose lock nobody holds.
func (c *Client) Isolate(ctx context.Context, dir, sha string) (string, func() error, error) {
	if err := validSHA(sha); err != nil {
		return "", nil, fmt.Errorf("git: isolate %s: %w", dir, err)
	}
	common, err := runGit(ctx, dir, nil, nil, "rev-parse", "--git-common-dir")
	if err != nil {
		return "", nil, fmt.Errorf("git: isolate %s: %w", dir, err)
	}
	if !filepath.IsAbs(common) {
		common = filepath.Join(dir, common)
	}
	if common, err = filepath.Abs(common); err != nil {
		return "", nil, fmt.Errorf("git: isolate %s: %w", dir, err)
	}
	base := filepath.Join(common, "kube-dc-init")
	if err := os.MkdirAll(base, 0o700); err != nil {
		return "", nil, fmt.Errorf("git: isolate %s: %w", dir, err)
	}
	sweepAbandonedRuns(common, base)
	work, err := os.MkdirTemp(base, "run-") // mode 0700, unique name
	if err != nil {
		return "", nil, fmt.Errorf("git: isolate %s: %w", dir, err)
	}
	owner := work + ".lock"
	held, locked := lockRun(owner)
	if !locked {
		_ = os.RemoveAll(work)
		return "", nil, fmt.Errorf("git: isolate %s: cannot lock %s", dir, owner)
	}
	// The worktree's registration is <git-dir>/worktrees/<name of work>: the
	// name is unique to this run, so removing it never touches another
	// worktree's registration (and `git worktree prune`, which removes the
	// registration of ANY worktree whose directory is absent right now, is
	// never used).
	registration := filepath.Join(common, "worktrees", filepath.Base(work))
	// unregister removes the checkout, its registration and, only once the
	// checkout is really gone, its lock file: while the lock file exists
	// (unlocked once this process is gone), a later run retries the removal.
	unregister := func() {
		removeTree(work)
		removeTree(work + ".tmp") // the run's scratch directory (RunTempDir)
		if registeredFor(registration, work) {
			_ = os.RemoveAll(registration)
		}
		if gone(work) && gone(work+".tmp") {
			_ = os.Remove(owner)
		}
		_ = held.Close()
		_ = os.Remove(base) // when no other run's checkout is in it
	}
	if _, err := runGit(ctx, dir, nil, nil, "worktree", "add", "--detach", "--quiet", work, sha); err != nil {
		unregister() // also when cancelled half-way: Git leaves a locked registration
		return "", nil, fmt.Errorf("git: isolate %s: %w", dir, err)
	}
	if err := os.Mkdir(work+".tmp", 0o700); err != nil {
		unregister()
		return "", nil, fmt.Errorf("git: isolate %s: %w", dir, err)
	}
	discard := func() error {
		// Not tied to ctx: a cancelled run must still remove its checkout.
		_, _ = runGit(context.Background(), dir, nil, nil, "worktree", "remove", "--force", work)
		unregister()
		for _, left := range []string{work, work + ".tmp"} {
			if _, err := os.Lstat(left); err == nil {
				return fmt.Errorf("git: %s could not be removed; it may hold generated secrets, remove it by hand", left)
			}
		}
		return nil
	}
	return work, discard, nil
}

// registeredFor reports whether registration is the worktree registration of
// the checkout at work (its gitdir file names work's .git file).
func registeredFor(registration, work string) bool {
	b, err := os.ReadFile(filepath.Join(registration, "gitdir"))
	if err != nil {
		// A registration cut short before gitdir was written still carries
		// this run's unique name.
		_, statErr := os.Stat(registration)
		return statErr == nil && errors.Is(err, os.ErrNotExist)
	}
	return filepath.Clean(strings.TrimSpace(string(b))) == filepath.Join(work, ".git")
}

// removeTree removes a directory tree, first making every directory in it
// writable: a writer may have left one that is not.
func removeTree(root string) {
	_ = filepath.WalkDir(root, func(path string, d os.DirEntry, err error) error {
		if err == nil && d.IsDir() {
			_ = os.Chmod(path, 0o700)
		}
		return nil
	})
	_ = os.RemoveAll(root)
}

// sweepAbandonedRuns removes the private checkouts whose lock nobody holds:
// their process is gone. Best effort: a failure here must not block a new run.
func sweepAbandonedRuns(common, base string) {
	entries, _ := os.ReadDir(base) // not Glob: the path may contain pattern characters
	for _, entry := range entries {
		if !strings.HasPrefix(entry.Name(), "run-") || !strings.HasSuffix(entry.Name(), ".lock") {
			continue
		}
		owner := filepath.Join(base, entry.Name())
		held, abandoned := lockRun(owner)
		if !abandoned {
			continue // a live run holds it
		}
		work := strings.TrimSuffix(owner, ".lock")
		removeTree(work)
		removeTree(work + ".tmp")
		if registration := filepath.Join(common, "worktrees", filepath.Base(work)); registeredFor(registration, work) {
			_ = os.RemoveAll(registration)
		}
		if gone(work) && gone(work+".tmp") {
			_ = os.Remove(owner)
		}
		_ = held.Close()
	}
}

// gone reports whether nothing is left at path. The lock file of a run is
// what the next sweep finds the run's directories by, so it is removed only
// when both of them (the private checkout and its scratch directory, which
// can hold a copy of a kubeconfig) are gone.
func gone(path string) bool {
	_, err := os.Lstat(path)
	return errors.Is(err, os.ErrNotExist)
}

// CommitIsolated commits everything in the private worktree onto parent
// (ports.GitIsolatedClient). It stages into the worktree's own index, so no
// other checkout's index is involved, and builds the commit with commit-tree,
// so neither a hook nor the operator's commit settings take part.
//
// Ignore policy is dir's, not the private checkout's: the two can differ
// (configuration can depend on the branch, and the private checkout is
// detached; the environment can supply configuration). See startIgnored.
func (c *Client) CommitIsolated(ctx context.Context, dir, work, msg, parent string, check func(ports.CommitTrees) error) (string, error) {
	if err := validSHA(parent); err != nil {
		return "", fmt.Errorf("git: commit in %s: %w", work, err)
	}
	top, err := runGit(ctx, work, nil, nil, "rev-parse", "--show-toplevel")
	if err != nil {
		return "", fmt.Errorf("git: commit in %s: %w", work, err)
	}
	if !samePath(top, work) {
		return "", fmt.Errorf("git: commit in %s: git resolves it to the worktree %s", work, top)
	}
	head, err := runGit(ctx, work, nil, nil, "rev-parse", "--verify", "HEAD^{commit}")
	if err != nil {
		return "", fmt.Errorf("git: commit in %s: %w", work, err)
	}
	if head != parent {
		return "", fmt.Errorf("git: commit in %s: the private checkout is at %s, not the expected parent %s", work, head, parent)
	}
	// What to stage is decided BEFORE anything is written to the object
	// store: every change to a tracked file, and every untracked file that
	// the STARTING checkout's ignore rules do not exclude (startIgnored).
	// Those paths are then staged by name, with -f: the private checkout's
	// own view of the ignore rules plays no part.
	tracked, err := runGitRaw(ctx, work, nil, nil, "ls-files", "-z", "--modified", "--deleted")
	if err != nil {
		return "", fmt.Errorf("git: changed files in %s: %w", work, err)
	}
	untracked, err := runGitRaw(ctx, work, nil, nil, "ls-files", "-z", "--others")
	if err != nil {
		return "", fmt.Errorf("git: new files in %s: %w", work, err)
	}
	ignored, err := startIgnored(ctx, dir, nulList(untracked))
	if err != nil {
		return "", err
	}
	// Only files are ever named to `git add`: a directory would be staged
	// with everything below it, ignored files included. A tracked path that
	// is gone, or is a directory now (its files are in the untracked list),
	// is removed from the index instead.
	remove, add := map[string]bool{}, map[string]bool{}
	for _, p := range nulList(tracked) {
		if fi, err := os.Lstat(filepath.Join(work, filepath.FromSlash(p))); err != nil || fi.IsDir() {
			remove[p] = true
		} else {
			add[p] = true
		}
	}
	for _, p := range nulList(untracked) {
		if ignored[p] || strings.HasSuffix(p, "/") { // "dir/": a nested repository
			continue
		}
		if fi, err := os.Lstat(filepath.Join(work, filepath.FromSlash(p))); err == nil && !fi.IsDir() {
			add[p] = true
		}
	}
	nul := func(set map[string]bool) []byte {
		var list strings.Builder
		for p := range set {
			list.WriteString(p)
			list.WriteByte(0)
		}
		return []byte(list.String())
	}
	if len(remove) > 0 {
		if _, err := runGit(ctx, work, nul(remove), nil, "update-index", "--force-remove", "-z", "--stdin"); err != nil {
			return "", fmt.Errorf("git: stage removals in %s: %w", work, err)
		}
	}
	if len(add) > 0 {
		if _, err := runGit(ctx, work, nul(add), nil, "add", "-f", "--pathspec-from-file=-", "--pathspec-file-nul"); err != nil {
			return "", fmt.Errorf("git: stage %s: %w", work, err)
		}
	}
	tree, err := runGit(ctx, work, nil, nil, "write-tree")
	if err != nil {
		return "", fmt.Errorf("git: write tree %s: %w", work, err)
	}
	parentTree, err := runGit(ctx, work, nil, nil, "rev-parse", "--verify", parent+"^{tree}")
	if err != nil {
		return "", fmt.Errorf("git: parent tree %s: %w", work, err)
	}
	if tree == parentTree {
		return "", ports.ErrNothingToCommit
	}
	if check != nil {
		if err := check(ports.CommitTrees{Parent: treeFileReader(ctx, work, parentTree), Candidate: treeFileReader(ctx, work, tree)}); err != nil {
			return "", fmt.Errorf("git: commit check in %s: %w", work, err)
		}
	}
	identity := []string{
		"GIT_AUTHOR_NAME=" + c.authorName, "GIT_AUTHOR_EMAIL=" + c.authorEmail,
		"GIT_COMMITTER_NAME=" + c.authorName, "GIT_COMMITTER_EMAIL=" + c.authorEmail,
	}
	sha, err := runGit(ctx, work, []byte(msg+"\n"), identity, "commit-tree", tree, "-p", parent, "-F", "-")
	if err != nil {
		return "", fmt.Errorf("git: commit in %s: %w", work, err)
	}
	// Move the private checkout's detached HEAD, so a following commit
	// stacks on this one. No branch moves.
	if _, err := runGit(ctx, work, nil, nil, "update-ref", "--no-deref", "HEAD", sha, parent); err != nil {
		return "", fmt.Errorf("git: commit in %s: %w", work, err)
	}
	return sha, nil
}

func samePath(a, b string) bool {
	ra, errA := filepath.EvalSymlinks(a)
	rb, errB := filepath.EvalSymlinks(b)
	return errA == nil && errB == nil && ra == rb
}

// startIgnored returns which of paths the STARTING checkout's ignore rules
// exclude, whether or not anything tracks them (--no-index).
//
// The rules are evaluated in dir itself, as the operator's own `git add`
// there would apply them: with the operator's configuration, including what
// the environment supplies (GIT_CONFIG_COUNT and friends: core.excludesFile,
// core.ignoreCase) and what depends on the branch (includeIf onbranch), and
// with a relative excludes file resolved there. Only what could point git at
// ANOTHER repository is taken away: the repository-selecting variables are
// stripped and the Git directory and worktree are named explicitly, which
// also outranks a core.worktree from any configuration source.
func startIgnored(ctx context.Context, dir string, paths []string) (map[string]bool, error) {
	ignored := map[string]bool{}
	if len(paths) == 0 {
		return ignored, nil
	}
	top, err := runGit(ctx, dir, nil, nil, "rev-parse", "--show-toplevel")
	if err != nil {
		return nil, fmt.Errorf("git: check ignore rules in %s: %w", dir, err)
	}
	gitDir, err := runGit(ctx, dir, nil, nil, "rev-parse", "--absolute-git-dir")
	if err != nil {
		return nil, fmt.Errorf("git: check ignore rules in %s: %w", dir, err)
	}
	// Not runGit: check-ignore takes plain path names and rejects the
	// literal-pathspec setting.
	cmd := exec.CommandContext(ctx, "git", "--git-dir="+gitDir, "--work-tree="+top, "-C", top,
		"-c", "core.hooksPath=/dev/null", "-c", "core.fsmonitor=false",
		"check-ignore", "--no-index", "-z", "--stdin")
	cmd.Env = append(gitEnv(), "GIT_TERMINAL_PROMPT=0")
	for _, kv := range os.Environ() {
		if name, _, _ := strings.Cut(kv, "="); configEnv(name) {
			cmd.Env = append(cmd.Env, kv)
		}
	}
	cmd.Stdin = strings.NewReader(strings.Join(paths, "\x00") + "\x00")
	out, err := cmd.Output()
	var exit *exec.ExitError
	if errors.As(err, &exit) && exit.ExitCode() == 1 {
		return ignored, nil // none ignored
	}
	if err != nil {
		return nil, fmt.Errorf("git: check ignore rules in %s: %w", dir, err)
	}
	for _, p := range nulList(string(out)) {
		ignored[p] = true
	}
	return ignored, nil
}

func configEnv(name string) bool {
	return name == "GIT_CONFIG_COUNT" || name == "GIT_CONFIG_PARAMETERS" ||
		strings.HasPrefix(name, "GIT_CONFIG_KEY_") || strings.HasPrefix(name, "GIT_CONFIG_VALUE_")
}

// TreeOf reads files of the commit sha (ports.GitIsolatedClient).
func (c *Client) TreeOf(ctx context.Context, dir, sha string) (ports.TreeFileReader, error) {
	if err := validSHA(sha); err != nil {
		return nil, fmt.Errorf("git: read %s: %w", dir, err)
	}
	tree, err := runGit(ctx, dir, nil, nil, "rev-parse", "--verify", sha+"^{tree}")
	if err != nil {
		return nil, fmt.Errorf("git: read %s: %w", dir, err)
	}
	return treeFileReader(ctx, dir, tree), nil
}

// treeFileReader reads regular files from one (immutable) tree object.
func treeFileReader(ctx context.Context, dir, tree string) ports.TreeFileReader {
	return func(path string) ([]byte, bool, error) {
		entry, err := runGitRaw(ctx, dir, nil, nil, "ls-tree", "-z", tree, "--", path)
		if err != nil {
			return nil, false, err
		}
		entry = strings.TrimRight(entry, "\x00")
		if entry == "" {
			return nil, false, nil
		}
		meta, name, ok := strings.Cut(entry, "\t")
		fields := strings.Fields(meta)
		if !ok || len(fields) != 3 || name != path {
			return nil, false, fmt.Errorf("unexpected tree entry %q for %s", entry, path)
		}
		if fields[1] != "blob" || fields[0] != "100644" && fields[0] != "100755" {
			return nil, true, fmt.Errorf("%w: %s (mode %s %s)", ports.ErrNotRegularFile, path, fields[0], fields[1])
		}
		cmd := exec.CommandContext(ctx, "git", gitArgs(dir, "cat-file", "blob", fields[2])...)
		cmd.Env = gitEnv()
		data, err := cmd.Output()
		return data, true, err
	}
}

// UnpublishedFiles lists the versions of the wanted files in every commit a
// push of sha would newly publish (ports.GitIsolatedClient).
//
// Which commits are new is decided by the push destination itself: the branch
// tips it advertises now (the same URL and credentials the push uses), not
// refs/remotes/origin/*, which describe whatever origin pointed at when they
// were last fetched. Each new commit's tree is then listed, so a file is seen
// under each of its own paths (an object listing names a blob once, by
// whichever path it met first).
func (c *Client) UnpublishedFiles(ctx context.Context, dir, token, sha string, want func(path string) bool, roots ...string) ([]ports.UnpublishedFile, error) {
	repo, err := openRepo(dir)
	if err != nil {
		return nil, fmt.Errorf("git: open %s: %w", dir, err)
	}
	pushURL, auth, err := c.requestForDirection(repo, dirPush, token)
	if err != nil {
		return nil, err
	}
	return c.unpublishedFiles(ctx, dir, sha, c.advertisedBranches(ctx, dir, repo, pushURL, auth), want, roots)
}

// unpublishedFiles is UnpublishedFiles for a known set of published commits.
func (c *Client) unpublishedFiles(ctx context.Context, dir, sha string, published []string, want func(path string) bool, roots []string) ([]ports.UnpublishedFile, error) {
	if err := validSHA(sha); err != nil {
		return nil, fmt.Errorf("git: unpublished history of %s: %w", dir, err)
	}
	// A legacy grafts file rewrites parents for git but not for the push.
	if common, err := runGit(ctx, dir, nil, nil, "rev-parse", "--git-common-dir"); err == nil {
		if !filepath.IsAbs(common) {
			common = filepath.Join(dir, common)
		}
		if b, err := os.ReadFile(filepath.Join(common, "info", "grafts")); err == nil && strings.TrimSpace(string(b)) != "" {
			return nil, fmt.Errorf("git: %s uses a grafts file (%s): the history a push publishes differs from the one git shows; remove it before running init", dir, filepath.Join(common, "info", "grafts"))
		}
	}
	args := []string{"rev-list", sha}
	if len(published) > 0 {
		args = append(args, "--not")
		args = append(args, published...)
	}
	commits, err := runGit(ctx, dir, nil, nil, args...)
	if err != nil {
		return nil, fmt.Errorf("git: unpublished history of %s: %w", dir, err)
	}
	var files []ports.UnpublishedFile
	seen := map[string]bool{} // path + blob: read each version of each path once
	// The tip itself is always inspected, also when the destination already
	// has it (a resume): its managed files are read from its Git tree, so
	// what the working tree happens to hold (a sparse checkout) plays no part.
	for _, commit := range append([]string{sha}, strings.Fields(commits)...) {
		listing, err := runGitRaw(ctx, dir, nil, nil, append([]string{"ls-tree", "-r", "-z", commit, "--"}, roots...)...)
		if err != nil {
			return nil, fmt.Errorf("git: list %s in %s: %w", commit, dir, err)
		}
		for _, entry := range nulList(listing) {
			meta, path, ok := strings.Cut(entry, "\t")
			fields := strings.Fields(meta)
			if !ok || len(fields) != 3 || fields[1] != "blob" || !want(path) || seen[path+"\x00"+fields[2]] {
				continue
			}
			seen[path+"\x00"+fields[2]] = true
			cmd := exec.CommandContext(ctx, "git", gitArgs(dir, "cat-file", "blob", fields[2])...)
			cmd.Env = gitEnv()
			data, err := cmd.Output()
			if err != nil {
				return nil, fmt.Errorf("git: read %s (%s) in %s: %w", path, fields[2], dir, err)
			}
			files = append(files, ports.UnpublishedFile{Path: path, Blob: fields[2], Data: data})
		}
	}
	return files, nil
}

// advertisedBranches asks the given push destination for its branch tips and returns
// those this repository has as commits. Empty when the destination cannot be
// asked: then no history counts as published.
func (c *Client) advertisedBranches(ctx context.Context, dir string, repo *gogit.Repository, pushURL string, auth transport.AuthMethod) []string {
	if pushURL == "" {
		return nil
	}
	listCtx, cancel := context.WithTimeout(ctx, 30*time.Second)
	defer cancel()
	remote := gogit.NewRemote(repo.Storer, &config.RemoteConfig{Name: "kube-dc-list", URLs: []string{pushURL}})
	refs, err := remote.ListContext(listCtx, &gogit.ListOptions{Auth: auth})
	if err != nil {
		return nil
	}
	var tips []string
	for _, ref := range refs {
		if !ref.Name().IsBranch() || ref.Hash().IsZero() {
			continue
		}
		if _, err := runGit(ctx, dir, nil, nil, "cat-file", "-e", ref.Hash().String()+"^{commit}"); err == nil {
			tips = append(tips, ref.Hash().String())
		}
	}
	return tips
}

// literalSSHDial serialises the pushes that switch the SSH transport to the
// address written in the URL (see PushCommit).
var literalSSHDial sync.Mutex

// PushCommit pushes exactly at.SHA to at.Branch (ports.GitIsolatedClient).
//
// The destination is resolved ONCE: the URL and credentials that answer "what
// does the remote already have" for the publication check are the ones the
// push then uses. Resolving twice would let the check look at one remote and
// the push send to another (a pushurl removed in between).
func (c *Client) PushCommit(ctx context.Context, dir, token string, at ports.Checkout, check *ports.PublishCheck) error {
	if err := c.checkProviderDestination(ctx, dir); err != nil {
		return err
	}
	branch, err := validCheckout(at)
	if err != nil {
		return fmt.Errorf("git: push from %s: %w", dir, err)
	}
	// After a push, the remote's fetch refspecs decide which local refs are
	// updated as "tracking" refs. One that maps into refs/heads/ would move
	// a local branch behind its checkout's back.
	if specs, err := runGit(ctx, dir, nil, nil, "config", "--get-all", "remote.origin.fetch"); err == nil {
		for _, spec := range strings.Fields(specs) {
			// Expanded for the branch being pushed: +refs/*:refs/* maps it
			// to a local branch too.
			refspec := config.RefSpec(spec)
			if refspec.Validate() != nil || !refspec.Match(branch) {
				continue
			}
			if dst := refspec.Dst(branch); dst.IsBranch() {
				return fmt.Errorf("git: push from %s: remote.origin.fetch %q writes into local branches; a push would move one without updating its checkout. Remove that mapping, then re-run", dir, spec)
			}
		}
	}
	repo, err := openRepo(dir)
	if err != nil {
		return fmt.Errorf("git: open %s: %w", dir, err)
	}
	pushURL, err := effectivePushURL(ctx, dir)
	if err != nil {
		return err
	}
	// An SSH push dials exactly the host and port its URL names. go-git
	// would otherwise apply the operator's SSH configuration its own way (a
	// HostName entry also switches the Port, even when the URL names one),
	// so no check of the URL could say where the listing, the push and the
	// confirmation really connect. With the configuration ignored, the
	// address that Destination judges is the address that is dialled.
	// (The setting is the transport's package variable, hence the lock.)
	if ep, err := transport.NewEndpoint(pushURL); err == nil && ep.Protocol == "ssh" {
		literalSSHDial.Lock()
		saved := gitssh.DefaultSSHConfig
		gitssh.DefaultSSHConfig = nil
		defer func() {
			gitssh.DefaultSSHConfig = saved
			literalSSHDial.Unlock()
		}()
	}
	// The destination is judged before a credential is chosen for it.
	if check != nil && check.Destination != nil {
		if err := check.Destination(pushURL); err != nil {
			return fmt.Errorf("%w: %w", ports.ErrPublishRefused, err)
		}
	}
	auth, err := c.authFor(pushURL, token)
	if err != nil {
		return err
	}
	if c.afterRemoteSelection != nil {
		c.afterRemoteSelection()
	}
	if check != nil {
		files, err := c.unpublishedFiles(ctx, dir, at.SHA, c.advertisedBranches(ctx, dir, repo, pushURL, auth), check.Want, check.Roots)
		if err == nil {
			err = check.Check(files)
		}
		if err != nil {
			return fmt.Errorf("%w: %w", ports.ErrPublishRefused, err)
		}
	}
	// The configuration may have changed while the check ran.
	if now, err := effectivePushURL(ctx, dir); err != nil || now != pushURL {
		return fmt.Errorf("%w: the push destination of %s changed while the push was being checked (%v)", ports.ErrPublishRefused, dir, err)
	}
	return c.pushTo(ctx, dir, repo, pushURL, auth, at.SHA, branch)
}

// effectivePushURL is where `git push origin` would send from dir: the one URL
// Git itself resolves, with pushurl and url.<base>.pushInsteadOf applied and
// the operator's configuration (environment-supplied included) in effect.
// go-git reads only the repository's own configuration and would push to the
// literal remote URL, which a rewrite rule may have redirected to a fork.
func effectivePushURL(ctx context.Context, dir string) (string, error) {
	cmd := exec.CommandContext(ctx, "git", gitArgs(dir, "remote", "get-url", "--push", "--all", "origin")...)
	cmd.Env = append(gitEnv(), "GIT_TERMINAL_PROMPT=0")
	for _, kv := range os.Environ() {
		if name, _, _ := strings.Cut(kv, "="); configEnv(name) {
			cmd.Env = append(cmd.Env, kv)
		}
	}
	out, err := cmd.Output()
	if err != nil {
		return "", fmt.Errorf("git: push from %s: no push destination for origin: %w", dir, err)
	}
	urls := strings.Fields(string(out))
	if len(urls) != 1 {
		return "", fmt.Errorf("git: push from %s: origin has %d push URLs; init publishes to exactly one", dir, len(urls))
	}
	return urls[0], nil
}

// Advance fast-forwards dir from from.SHA to the descendant commit to
// (ports.GitIsolatedClient).
//
//  1. Refuse up front what Git itself would let through: a file (ignored or
//     not) where the new commit adds one, and a tracked file deleted locally
//     that the new commit changes or removes (Git would silently bring it
//     back).
//  2. Record the intent (advanceMarker), so a process killed between the next
//     two steps can be finished by ResumeAdvance.
//  3. Move the branch with `git update-ref <branch> <to> <from>`. This
//     compare-and-swap reserves the old tip: nothing else can commit onto it
//     from here on.
//  4. Move index and worktree with `git read-tree -m -u <from> <to>`: under
//     the index lock, Git checks every path the new commit touches and
//     changes nothing when one has a local modification or a staged change.
//     Unrelated local work is not looked at.
//  5. On a refusal, put the branch back the same way.
//
// There is no reverse merge: once step 4 has succeeded the index is the
// operator's again, and an entry they staged with the same content as the new
// commit must not be rewound.
func (c *Client) Advance(ctx context.Context, dir string, from ports.Checkout, to string) error {
	branch, err := validCheckout(from)
	if err == nil {
		err = validSHA(to)
	}
	if err != nil {
		return fmt.Errorf("git: fast-forward %s: %w", dir, err)
	}
	cur, err := c.Checkout(ctx, dir)
	if err != nil {
		return err
	}
	if cur.Branch != branch.String() || cur.SHA != from.SHA {
		return fmt.Errorf("git: fast-forward %s: the checkout is %s at %s, not %s at %s", dir, cur.Branch, cur.SHA, branch, from.SHA)
	}
	if _, err := runGit(ctx, dir, nil, nil, "merge-base", "--is-ancestor", from.SHA, to); err != nil {
		return fmt.Errorf("git: fast-forward %s: %s does not descend from %s: %w", dir, to, from.SHA, err)
	}
	added, err := mergeBlockers(ctx, dir, from.SHA, to)
	if err != nil {
		return err
	}
	marker, err := advanceMarker(ctx, dir)
	if err != nil {
		return fmt.Errorf("git: fast-forward %s: %w", dir, err)
	}
	// The index's checksum, taken now: ResumeAdvance replays the merge only
	// onto exactly this index.
	if err := os.WriteFile(marker, []byte(branch.String()+"\n"+from.SHA+"\n"+to+"\n"+indexChecksum(ctx, dir)+"\n"), 0o600); err != nil {
		return fmt.Errorf("git: fast-forward %s: %w", dir, err)
	}
	if _, err := runGit(ctx, dir, nil, nil, "update-ref", "-m", "kube-dc init: fast-forward", branch.String(), to, from.SHA); err != nil {
		_ = os.Remove(marker)
		return fmt.Errorf("git: fast-forward %s: %w", dir, err)
	}
	if mergeErr := mergeCheckout(ctx, dir, from.SHA, to, added); mergeErr != nil {
		// Not tied to ctx: the branch must go back.
		_, backErr := runGit(context.Background(), dir, nil, nil, "update-ref", "-m", "kube-dc init: fast-forward refused", branch.String(), from.SHA, to)
		if backErr != nil {
			// The marker stays: the next run finishes the fast-forward.
			return fmt.Errorf("%w; and %s could not be put back to %s (%v): it is at %s with the old files, re-run init to finish", mergeErr, branch.Short(), from.SHA, backErr, to)
		}
		_ = os.Remove(marker)
		return mergeErr
	}
	_ = os.Remove(marker)
	return nil
}

// ResumeAdvance deals with a fast-forward that a killed process left between
// moving the branch and updating index and worktree (ports.GitIsolatedClient).
//
// Time may have passed, and the operator may have worked in the checkout
// since. So the merge is replayed only when nothing can have happened: the
// index file is byte for byte the one Advance recorded before it moved the
// branch. When the index already holds the new commit's entries for every
// path that commit touched, the fast-forward had in fact completed, and only
// the marker is removed. In every other case the state is the operator's to
// judge: nothing is changed, and the error says what happened.
func (c *Client) ResumeAdvance(ctx context.Context, dir string) (bool, error) {
	marker, err := advanceMarker(ctx, dir)
	if err != nil {
		return false, nil // not a repository this flow can work in; Apply reports that itself
	}
	b, err := os.ReadFile(marker)
	if err != nil {
		return false, nil
	}
	lines := strings.Split(strings.TrimSpace(string(b)), "\n")
	if len(lines) != 4 || validSHA(lines[1]) != nil || validSHA(lines[2]) != nil {
		_ = os.Remove(marker)
		return false, nil
	}
	branch, from, to, recorded := lines[0], lines[1], lines[2], lines[3]
	cur, err := c.Checkout(ctx, dir)
	if err != nil {
		return false, err
	}
	if cur.Branch != branch || cur.SHA != to {
		// The branch never moved, or has moved on since: nothing to finish.
		_ = os.Remove(marker)
		return false, nil
	}
	changes, err := runGitRaw(ctx, dir, nil, nil, "diff-tree", "-r", "-z", "--no-renames", "--name-only", from, to)
	if err != nil {
		return false, fmt.Errorf("git: resume fast-forward %s: %w", dir, err)
	}
	touched := nulList(changes)
	// Already applied: the index agrees with the new commit on every path it
	// touched (the process died after the merge, before removing the marker).
	applied := len(touched) > 0
	if applied {
		_, derr := runGit(ctx, dir, nil, nil, append([]string{"diff-index", "--cached", "--quiet", to, "--"}, touched...)...)
		applied = derr == nil
	}
	if applied {
		_ = os.Remove(marker)
		return false, nil
	}
	short := strings.TrimPrefix(branch, "refs/heads/")
	if sum := indexChecksum(ctx, dir); recorded == "" || sum == "" || sum != recorded {
		_ = os.Remove(marker)
		return true, fmt.Errorf("an interrupted run moved %s from %s to %s without updating this checkout, and the index has changed since, so init will not touch it. `git status` shows that commit's changes as if they were undone here. To take the commit's files, run `git read-tree -m -u %s %s`; then re-run init", short, from, to, from, to)
	}
	added, err := mergeBlockers(ctx, dir, from, to)
	if err == nil {
		err = mergeCheckout(ctx, dir, from, to, added)
	}
	if err != nil {
		return true, fmt.Errorf("an interrupted run moved %s to %s without updating this checkout, and finishing that now failed: %w. Fix what is named, then re-run init", short, to, err)
	}
	_ = os.Remove(marker)
	return true, nil
}

// indexChecksum identifies the exact index file of the checkout at dir ("" when
// it cannot be read).
func indexChecksum(ctx context.Context, dir string) string {
	path, err := runGit(ctx, dir, nil, nil, "rev-parse", "--git-path", "index")
	if err != nil {
		return ""
	}
	if !filepath.IsAbs(path) {
		path = filepath.Join(dir, path)
	}
	b, err := os.ReadFile(path)
	if err != nil {
		return ""
	}
	return fmt.Sprintf("%x", sha256.Sum256(b))
}

// advanceMarker is where Advance records a fast-forward in progress: in the
// checkout's own Git directory (a linked worktree has its own).
func advanceMarker(ctx context.Context, dir string) (string, error) {
	gitDir, err := runGit(ctx, dir, nil, nil, "rev-parse", "--absolute-git-dir")
	if err != nil {
		return "", err
	}
	return filepath.Join(gitDir, "kube-dc-init-advance"), nil
}

// mergeBlockers refuses what `git read-tree -m -u` would let through, and
// returns the paths the new commit adds.
func mergeBlockers(ctx context.Context, dir, from, to string) ([]string, error) {
	changes, err := runGitRaw(ctx, dir, nil, nil, "diff-tree", "-r", "-z", "--no-renames", "--name-status", from, to)
	if err != nil {
		return nil, fmt.Errorf("git: fast-forward %s: %w", dir, err)
	}
	var added []string
	touched, removed := map[string]bool{}, map[string]bool{}
	fields := strings.Split(changes, "\x00")
	for i := 0; i+1 < len(fields); i += 2 {
		switch fields[i] {
		case "A":
			added = append(added, fields[i+1])
		case "D":
			removed[fields[i+1]] = true
			touched[fields[i+1]] = true
		default:
			touched[fields[i+1]] = true
		}
	}
	for _, p := range added {
		// A tracked file the same commit removes (it becomes a directory),
		// or a directory holding nothing but files that commit removes (it
		// becomes a file), is not in the way: Git replaces it, and refuses
		// if anything in it was changed.
		if blocker := pathInTheWay(dir, p); blocker != "" && !removed[blocker] && !onlyRemoved(dir, blocker, removed) {
			return nil, fmt.Errorf("git: fast-forward %s: %s is in the way of %s, which the new commit adds", dir, blocker, p)
		}
	}
	// A staged change on a path the new commit touches, even one with the
	// very content the commit brings: Git would accept that one, and after
	// the branch moved the operator's staged change would simply be gone
	// from `git diff --cached`.
	staged, err := runGitRaw(ctx, dir, nil, nil, "diff-index", "--cached", "--no-renames", "--name-only", "-z", from)
	if err != nil {
		return nil, fmt.Errorf("git: fast-forward %s: %w", dir, err)
	}
	incoming := map[string]bool{}
	for _, p := range added {
		incoming[p] = true
	}
	for _, p := range nulList(staged) {
		if touched[p] || incoming[p] {
			return nil, fmt.Errorf("git: fast-forward %s: %s has a staged change, and the new commit changes it too", dir, p)
		}
	}
	deleted, err := runGitRaw(ctx, dir, nil, nil, "ls-files", "--deleted", "-z")
	if err != nil {
		return nil, fmt.Errorf("git: fast-forward %s: %w", dir, err)
	}
	for _, p := range nulList(deleted) {
		if touched[p] {
			return nil, fmt.Errorf("git: fast-forward %s: %s is deleted locally, and the new commit changes it", dir, p)
		}
	}
	return added, nil
}

// mergeCheckout moves index and worktree from `from` to `to`. A refusal
// changes nothing. A checkout that fails part-way is reported as
// ports.ErrPartialCheckout with every file it wrote: the added files that
// exist now, and the tracked files that differ from the index now and did not
// before.
func mergeCheckout(ctx context.Context, dir, from, to string, added []string) error {
	modified := func() map[string]bool {
		out, _ := runGitRaw(context.Background(), dir, nil, nil, "diff-files", "--name-only", "-z")
		set := map[string]bool{}
		for _, p := range nulList(out) {
			set[p] = true
		}
		return set
	}
	before := modified()
	// What is at each added path now: a directory the commit turns into a
	// file is there already, and is not something the checkout wrote.
	kind := func(p string) string {
		fi, err := os.Lstat(filepath.Join(dir, filepath.FromSlash(p)))
		if err != nil {
			return "absent"
		}
		return fi.Mode().Type().String()
	}
	was := map[string]string{}
	for _, p := range added {
		was[p] = kind(p)
	}
	_, mergeErr := runGit(ctx, dir, nil, nil, "read-tree", "-m", "-u", from, to)
	if mergeErr == nil {
		return nil
	}
	var written []string
	for _, p := range added {
		if now := kind(p); now != "absent" && now != was[p] {
			written = append(written, p)
		}
	}
	for p := range modified() {
		if !before[p] {
			written = append(written, p)
		}
	}
	if len(written) > 0 {
		sort.Strings(written)
		return fmt.Errorf("%w in %s (%v): these files of commit %s were written and no longer match the branch: %s",
			ports.ErrPartialCheckout, dir, mergeErr, to, strings.Join(written, ", "))
	}
	return fmt.Errorf("git: fast-forward %s: local changes overlap the new commit: %w", dir, mergeErr)
}

// onlyRemoved reports whether rel is a directory in which every file is one the
// new commit removes.
func onlyRemoved(dir, rel string, removed map[string]bool) bool {
	root := filepath.Join(dir, filepath.FromSlash(rel))
	if fi, err := os.Lstat(root); err != nil || !fi.IsDir() {
		return false
	}
	only := true
	_ = filepath.WalkDir(root, func(path string, d os.DirEntry, err error) error {
		if err != nil {
			only = false
			return filepath.SkipAll
		}
		if d.IsDir() {
			return nil
		}
		if sub, rerr := filepath.Rel(dir, path); rerr != nil || !removed[filepath.ToSlash(sub)] {
			only = false
			return filepath.SkipAll
		}
		return nil
	})
	return only
}

// pathInTheWay returns what exists on disk where the new commit adds rel: rel
// itself, or a non-directory at one of its parent directories.
func pathInTheWay(dir, rel string) string {
	parts := strings.Split(rel, "/")
	for i := 1; i <= len(parts); i++ {
		prefix := strings.Join(parts[:i], "/")
		fi, err := os.Lstat(filepath.Join(dir, filepath.FromSlash(prefix)))
		if errors.Is(err, os.ErrNotExist) {
			return ""
		}
		if err != nil {
			return prefix
		}
		if i == len(parts) || !fi.IsDir() {
			return prefix
		}
	}
	return ""
}

var _ ports.GitIsolatedClient = (*Client)(nil)
