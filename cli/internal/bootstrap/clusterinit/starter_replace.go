package clusterinit

import (
	"bufio"
	"context"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"slices"
	"sort"
	"strings"
)

// Changing the starter of a checkout that nothing has been installed from.
//
// A failed first run (for example a starter whose scripts did not run on the
// operator's workstation) leaves the old starter imported and committed in the
// checkout. A rerun with a corrected starter ref used to keep the old one
// ("fleet starter already present — skipping OCI pull"), and the only way out
// was a fresh clone. When no cluster overlay exists yet and the starter files
// are unmodified, init now replaces the starter instead. Once an overlay
// exists, a different starter is an upgrade, which init refuses with both refs
// named.

// ErrStarterChangeRefused is returned when the requested starter differs from
// the checkout's and replacing it is not safe.
var ErrStarterChangeRefused = errors.New("init: the checkout holds a different fleet starter")

// InstalledStarter is what .starter-version records about the imported starter.
type InstalledStarter struct {
	Ref    string
	Digest string
}

// ReadInstalledStarter returns the last installedRef/installedDigest recorded
// in dir/.starter-version.
func ReadInstalledStarter(dir string) (InstalledStarter, bool) {
	f, err := os.Open(filepath.Join(dir, ".starter-version"))
	if err != nil {
		return InstalledStarter{}, false
	}
	defer f.Close()
	var got InstalledStarter
	scanner := bufio.NewScanner(f)
	for scanner.Scan() {
		line := strings.TrimSpace(scanner.Text())
		if v, ok := strings.CutPrefix(line, "installedRef:"); ok {
			got = InstalledStarter{Ref: strings.TrimSpace(v)}
		} else if v, ok := strings.CutPrefix(line, "installedDigest:"); ok {
			got.Digest = strings.TrimSpace(v)
		}
	}
	if got.Ref == "" {
		return InstalledStarter{}, false
	}
	if got.Digest == "" {
		_, got.Digest = splitStarterRef(got.Ref)
	}
	return got, true
}

// splitStarterRef separates "oci://host/repo[:tag]@sha256:…" into the part
// before the digest and the digest.
func splitStarterRef(ref string) (base, digest string) {
	if b, d, ok := strings.Cut(ref, "@"); ok && strings.HasPrefix(d, "sha256:") {
		return b, d
	}
	return ref, ""
}

// StarterChangeRequested reports whether requested selects a different starter
// than the one dir was imported from. Digests decide when the request carries
// one; otherwise the ref without its digest decides, so a moved tag does not
// turn a resumed installation into a starter change.
func StarterChangeRequested(dir, requested string) (InstalledStarter, bool) {
	if strings.TrimSpace(requested) == "" {
		return InstalledStarter{}, false
	}
	installed, ok := ReadInstalledStarter(dir)
	if !ok {
		return InstalledStarter{}, false
	}
	reqBase, reqDigest := splitStarterRef(requested)
	instBase, _ := splitStarterRef(installed.Ref)
	if reqDigest != "" && installed.Digest != "" {
		return installed, reqDigest != installed.Digest
	}
	return installed, reqBase != instBase
}

// StarterOverlays lists the cluster overlays in dir/clusters: its
// directories. Files there (.gitkeep, .DS_Store) are not overlays.
func StarterOverlays(dir string) ([]string, error) {
	entries, err := os.ReadDir(filepath.Join(dir, "clusters"))
	if err != nil && !errors.Is(err, os.ErrNotExist) {
		return nil, fmt.Errorf("init: starter: read clusters/: %w", err)
	}
	var overlays []string
	for _, e := range entries {
		if e.IsDir() {
			overlays = append(overlays, "clusters/"+e.Name())
		}
	}
	sort.Strings(overlays)
	return overlays, nil
}

// CheckStarterReplacement refuses a starter change once a cluster overlay
// exists or the committed starter files differ from the starter's manifest.
func CheckStarterReplacement(dir string, installed InstalledStarter, requested string) error {
	overlays, err := StarterOverlays(dir)
	if err != nil {
		return err
	}
	if len(overlays) > 0 {
		return fmt.Errorf("%w: %s was imported from %s and already holds %s; the input selects %s. "+
			"Changing the starter of an existing installation is an upgrade, which init does not do. "+
			"Select the starter it was installed from, or start a new installation from a fresh clone",
			ErrStarterChangeRefused, dir, installed.Ref, strings.Join(overlays, ", "), requested)
	}
	// Untracked files in the starter trees (.DS_Store, __pycache__) are not
	// edits: they are removed with the trees. Untracked files Git does not
	// ignore are refused by CheckReplacementCheckout.
	if err := verifyStarterManifestExcept(dir, untrackedStarterFiles(dir)); err != nil {
		return fmt.Errorf("%w: %s was imported from %s and its starter files changed locally (%v); the input selects %s. "+
			"Restore or commit-revert those files, or start from a fresh clone",
			ErrStarterChangeRefused, dir, installed.Ref, err, requested)
	}
	return nil
}

// untrackedStarterFiles lists the untracked files, ignored ones included,
// under the starter trees of a Git checkout.
func untrackedStarterFiles(dir string) map[string]bool {
	args := append([]string{"-C", dir, "ls-files", "--others", "-z", "--"}, starterManifestTrees...)
	out, err := exec.Command("git", args...).Output()
	if err != nil {
		return nil
	}
	files := map[string]bool{}
	for _, f := range strings.Split(string(out), "\x00") {
		if f != "" {
			files[f] = true
		}
	}
	return files
}

// CheckReplacementCheckout refuses a replacement unless dir is a committed
// Git checkout whose only uncommitted change is the .sops.yaml that an
// interrupted key generation leaves behind. That file holds no secret; the
// import commit takes it and the age-key step completes it.
func CheckReplacementCheckout(dir string) error {
	if _, err := os.Stat(filepath.Join(dir, ".git")); err != nil || !gitHasCommit(dir) {
		return fmt.Errorf("%w: %s is not a committed Git checkout; start from a fresh clone",
			ErrStarterChangeRefused, dir)
	}
	dirty, err := gitDirtyPaths(dir)
	if err != nil {
		return fmt.Errorf("init: starter: read the Git status of %s: %w", dir, err)
	}
	if dirty = withoutSOPSLeftover(dirty); dirty != "" {
		return fmt.Errorf("%w: %s has uncommitted changes; commit them, remove them or list them in .gitignore "+
			"before the starter is replaced:\n%s",
			ErrStarterChangeRefused, dir, dirty)
	}
	// The replacement removes the starter trees whole. Ignored files there
	// other than editor and OS litter (a decrypted secret, a keys file) are
	// the operator's: refuse rather than delete them.
	var kept []string
	for f := range untrackedStarterFiles(dir) {
		if !starterTreeJunk(f) {
			kept = append(kept, f)
		}
	}
	if len(kept) > 0 {
		sort.Strings(kept)
		return fmt.Errorf("%w: %s holds ignored files inside the starter trees, which the replacement removes: %s; "+
			"move them out of the checkout first", ErrStarterChangeRefused, dir, strings.Join(kept, ", "))
	}
	return nil
}

// starterTreeJunk reports editor, OS and interpreter litter, which a
// replacement may delete with the starter trees.
func starterTreeJunk(path string) bool {
	base := filepath.Base(path)
	if strings.Contains("/"+filepath.ToSlash(path), "/__pycache__/") {
		return true
	}
	switch base {
	case ".DS_Store", "Thumbs.db":
		return true
	}
	for _, suffix := range []string{".pyc", ".pyo", ".swp", ".swo", ".bak", "~"} {
		if strings.HasSuffix(base, suffix) {
			return true
		}
	}
	return false
}

// withoutSOPSLeftover drops an untracked or unstaged .sops.yaml from
// `git status --porcelain` output: key generation writes that file before it
// enrolls the key, and init completes it after the starter step.
func withoutSOPSLeftover(porcelain string) string {
	var kept []string
	for _, line := range strings.Split(porcelain, "\n") {
		if line == "" || line == "?? .sops.yaml" || line == " M .sops.yaml" {
			continue
		}
		kept = append(kept, line)
	}
	return strings.Join(kept, "\n")
}

// withoutInterruptedReplacement also drops the staged deletions of
// starter-owned paths that a replacement stopped between `git rm` and its
// commit leaves: the import commit stages those paths again.
func withoutInterruptedReplacement(porcelain string) string {
	var kept []string
	for _, line := range strings.Split(withoutSOPSLeftover(porcelain), "\n") {
		if strings.HasPrefix(line, "D  ") && starterOwnedEntries[strings.SplitN(line[3:], "/", 2)[0]] {
			continue
		}
		if line != "" {
			kept = append(kept, line)
		}
	}
	return strings.Join(kept, "\n")
}

// commitStarterPaths commits the named paths alone, with the git CLI: in a
// checkout with history, staging the whole tree (the Git adapter) could take
// a file the clean-tree check cannot see, because go-git applies neither
// .git/info/exclude nor every .gitignore rule as Git does. A named path that
// Git ignores is left out, as the adapter would skip it; starter-owned paths
// tracked at HEAD are committed too, so a deletion staged by an interrupted
// replacement does not stay behind.
func commitStarterPaths(ctx context.Context, dir string, names []string, message string) error {
	var paths []string
	seen := map[string]bool{}
	for _, name := range names {
		if seen[name] {
			continue
		}
		seen[name] = true
		if _, err := os.Lstat(filepath.Join(dir, name)); err != nil || gitIgnoresPath(dir, name) {
			continue
		}
		paths = append(paths, name)
	}
	sort.Strings(paths)
	if _, err := gitCLI(ctx, dir, append([]string{"-c", "core.excludesFile=/dev/null", "add", "-A", "--"}, paths...)...); err != nil {
		return err
	}
	// Only names the index now holds: an empty directory, or one holding only
	// ignored files, stages nothing and would fail `git commit --`.
	var commitPaths []string
	for _, name := range paths {
		if gitTracksPath(dir, name) {
			commitPaths = append(commitPaths, name)
		}
	}
	if tracked, err := gitCLI(ctx, dir, append([]string{"ls-tree", "--name-only", "HEAD", "--"}, starterOwnedNames()...)...); err == nil {
		for _, name := range strings.Fields(tracked) {
			if !slices.Contains(paths, name) {
				commitPaths = append(commitPaths, name)
			}
		}
	}
	if err := beforeStarterCommit(dir); err != nil {
		return err
	}
	_, err := gitCLI(ctx, dir, append(append(append([]string{}, localCommitArgs...), "-m", message, "--"), commitPaths...)...)
	return err
}

// beforeStarterCommit is a test seam between staging and committing.
var beforeStarterCommit = func(string) error { return nil }

// gitCLI runs git in dir and folds its output into the error.
func gitCLI(ctx context.Context, dir string, args ...string) (string, error) {
	// No hooks for any of init's Git calls (post-checkout, prepare-commit-msg).
	cmd := exec.CommandContext(ctx, "git", append([]string{"-C", dir, "-c", "core.hooksPath=/dev/null"}, args...)...)
	out, err := cmd.CombinedOutput()
	if err != nil {
		return "", fmt.Errorf("init: starter: git %s: %w (%s)", strings.Join(args, " "), err, strings.TrimSpace(string(out)))
	}
	return strings.TrimSpace(string(out)), nil
}

// localCommitArgs commit as the Git adapter does: its identity, no hooks, no
// signing.
var localCommitArgs = []string{"-c", "user.name=kube-dc bootstrap", "-c", "user.email=bootstrap@kube-dc.invalid",
	"-c", "commit.gpgsign=false", "-c", "core.hooksPath=/dev/null", "commit", "--no-verify", "-q"}

func starterOwnedNames() []string {
	names := make([]string, 0, len(starterOwnedEntries))
	for name := range starterOwnedEntries {
		names = append(names, name)
	}
	sort.Strings(names)
	return names
}

// removeStarterForReplacement removes the starter-owned top-level entries and
// commits only their removal. Operator content (README, docs, clusters/,
// .gitignore, .sops.yaml, the age key) is kept.
func removeStarterForReplacement(ctx context.Context, dir, message string) error {
	names := starterOwnedNames()
	if _, err := gitCLI(ctx, dir, append([]string{"rm", "-r", "-q", "--ignore-unmatch", "--"}, names...)...); err != nil {
		return err
	}
	// Untracked or ignored files inside the starter trees (caches) go too.
	for _, name := range names {
		if err := os.RemoveAll(filepath.Join(dir, name)); err != nil {
			return fmt.Errorf("init: starter: remove %s: %w", name, err)
		}
	}
	// The index holds only these removals: CheckReplacementCheckout refused
	// anything staged, and the .sops.yaml it allows is unstaged.
	_, err := gitCLI(ctx, dir, append(append([]string{}, localCommitArgs...), "-m", message)...)
	return err
}

// restoreStarter puts back the starter of commit head after a replacement
// failed: HEAD and the index return to head, the starter-owned entries and the
// other top-level entries this run added (moved) are replaced by head's, and
// .gitignore is restored. Every other file in the working tree is left as it
// is (.sops.yaml with its edits, the age key).
func restoreStarter(ctx context.Context, dir, head string, moved []string) error {
	names := append(starterOwnedNames(), moved...)
	for _, name := range names {
		if err := os.RemoveAll(filepath.Join(dir, name)); err != nil {
			return fmt.Errorf("init: starter: remove %s: %w", name, err)
		}
	}
	if _, err := gitCLI(ctx, dir, "reset", "-q", head); err != nil {
		return err
	}
	tracked, err := gitCLI(ctx, dir, append([]string{"ls-tree", "--name-only", head, "--", ".gitignore"}, names...)...)
	if err != nil {
		return err
	}
	if names := strings.Fields(tracked); len(names) > 0 {
		_, err = gitCLI(ctx, dir, append([]string{"checkout", "-q", head, "--"}, names...)...)
	}
	return err
}
