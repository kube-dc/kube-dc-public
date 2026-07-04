// Package lint holds tree-wide hygiene checks for everything that
// ships to the PUBLIC kube-dc-public mirror.
//
// The sync workflow (.github/workflows/sync_to_public_repo.yaml)
// rsyncs charts/, examples/, docs/cloud/, docs/platform/, docs-ui/,
// installer/, hack/, tests/, cli/, the root README.md and the
// deploy-docs workflow to the public repo on every push to main.
// Real infrastructure identifiers (customer cluster names, public
// IPs, node hostnames, operator identities, bastion FQDNs, operator
// home paths) must therefore never be hardcoded ANYWHERE on that
// surface — not in code, tests, fixtures, comments, or docs examples.
//
// TestNoRealInfraReferences enforces that with a banned-pattern scan
// over the full mirror surface (it lives in the cli module so it runs
// under the standard `go -C cli test ./...` gate, but it walks the
// REPO root — reviewer catch 2026-07-04: a cli-only scan missed
// installer/ + tests/ leaks). Use neutral placeholders instead:
//
//   - IPs:        RFC 5737 documentation ranges (203.0.113.x,
//     198.51.100.x, 192.0.2.x)
//   - domains:    *.example.com / *.example.net
//   - clusters:   fictional names (atlantis, prod1, eu/dc1, …)
//   - nodes:      HOST5-A / host5-a / node-a1 shapes
//   - identities: alice / ops@example.com / user@example.com
//
// (2026-07-04 sweep: ~530 real-infra references were scrubbed; this
// test keeps them out.)
package lint

import (
	"bytes"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"runtime"
	"strconv"
	"strings"
	"testing"
)

// mirrorSurface is the exact rsync set from
// .github/workflows/sync_to_public_repo.yaml (dirs + single files).
// Entries missing on disk are skipped silently so a standalone cli/
// checkout (the public repo layout keeps the same relative shape)
// still runs the scan over whatever is present.
var mirrorSurface = []string{
	"charts",
	"examples",
	"docs/cloud",
	"docs/platform",
	"docs-ui",
	"installer",
	"hack",
	"tests",
	"cli",
	"README.md",
	".github/workflows/deploy-docs.yml",
}

// bannedPatterns are case-insensitive regexes over file contents.
// Each entry documents WHAT real thing it guards against so a match
// is self-explanatory, and may carry a line-level `allow` regexp for
// documented product-public exceptions. Keep patterns tight — false
// positives erode trust in the gate.
var bannedPatterns = []struct {
	re     *regexp.Regexp
	allow  *regexp.Regexp // a matching LINE is permitted (documented exception)
	reason string
}{
	{re: regexp.MustCompile(`217\.117\.26\.`), reason: "real public IP block of a production cluster — use RFC 5737 (203.0.113.x)"},
	{re: regexp.MustCompile(`88\.99\.29\.`), reason: "real bare-metal public IP — use RFC 5737 (198.51.100.x)"},
	{re: regexp.MustCompile(`(?i)acropolis`), reason: "real customer/cluster name — use a fictional cluster name"},
	{re: regexp.MustCompile(`(?i)srv[0-9]+-kub`), reason: "real node hostname scheme — use HOST5-A / host5-a shapes"},
	{
		re: regexp.MustCompile(`(?i)zrh`),
		// The KdcCluster CRD enumerates CloudSigma's PUBLIC region
		// codes (zrh, lvs, sjc, tyo) — product API surface like AWS
		// us-east-1, not customer data.
		allow:  regexp.MustCompile(`CloudSigma region`),
		reason: "real region/cluster token (CloudSigma Zurich) — use eu/dc1 shapes",
	},
	{re: regexp.MustCompile(`cloudsigma-dssd`),
		// The CloudSigma CSI integration's canonical StorageClass name
		// is functional product configuration, not test data. Allowed
		// where it defines/references the actual StorageClass object
		// (the CSI configmap's `name: cloudsigma-dssd`).
		allow:  regexp.MustCompile(`(?i)storageclass|storage_class|name: cloudsigma-dssd`),
		reason: "real StorageClass name in non-functional context — use fast-ssd"},
	{re: regexp.MustCompile(`(?i)ams1-blade`), reason: "real production node names — use node-a1 shapes"},
	{re: regexp.MustCompile(`/home/voa\b`), reason: "operator home path — use ~/, repo-relative paths, or env-var-driven paths"},
	{re: regexp.MustCompile(`(?i)(voa|vtsap|tsap)@`), reason: "operator identity — use ops@example.com"},
	{re: regexp.MustCompile(`@shalb\.com`), reason: "employee email domain — use example.com identities"},
	{re: regexp.MustCompile(`\bcs\.shalb\.com`), reason: "real bastion domain — use bastion.example.com"},
}

// skipDirs are never scanned. Mirrors the rsync excludes for docs-ui
// (node_modules, build, .docusaurus, .cache-loader) plus the usual
// non-source dirs.
var skipDirs = map[string]bool{
	".git": true, "bin": true, "node_modules": true, "vendor": true,
	"build": true, ".docusaurus": true, ".cache-loader": true,
}

// skipFiles are lockfiles/checksum files whose base64 blobs
// false-positive on short tokens like "zrh".
var skipFiles = map[string]bool{
	"package-lock.json": true, "yarn.lock": true, "pnpm-lock.yaml": true,
	"go.sum": true,
}

func TestNoRealInfraReferences(t *testing.T) {
	_, self, _, ok := runtime.Caller(0)
	if !ok {
		t.Fatal("runtime.Caller failed")
	}
	// self = <repo>/cli/internal/lint/no_real_infra_test.go →
	// repo root is three levels up from the containing directory.
	repoRoot := filepath.Clean(filepath.Join(filepath.Dir(self), "..", "..", ".."))

	var violations []string
	scan := func(path string, body []byte) {
		rel, _ := filepath.Rel(repoRoot, path)
		for _, b := range bannedPatterns {
			for _, m := range b.re.FindAllIndex(body, 5) {
				lineStart := bytes.LastIndexByte(body[:m[0]], '\n') + 1
				lineEnd := bytes.IndexByte(body[m[1]:], '\n')
				if lineEnd < 0 {
					lineEnd = len(body)
				} else {
					lineEnd += m[1]
				}
				if b.allow != nil && b.allow.Match(body[lineStart:lineEnd]) {
					continue
				}
				line := 1 + bytes.Count(body[:m[0]], []byte("\n"))
				violations = append(violations,
					rel+":"+strconv.Itoa(line)+": "+string(body[m[0]:m[1]])+" — "+b.reason)
			}
		}
	}

	// Preferred enumeration: `git ls-files` over the surface — TRACKED
	// files are exactly what the CI checkout rsyncs to the mirror, so
	// untracked local state (.cluster.dev/ terraform cache, captured
	// e2e debug logs, editor litter) can't false-positive. Falls back
	// to a filesystem walk for git-less checkouts (release tarballs).
	files := gitTrackedFiles(repoRoot, mirrorSurface)
	if files == nil {
		files = walkedFiles(t, repoRoot, mirrorSurface)
	}
	for _, path := range files {
		if path == self || skipFiles[filepath.Base(path)] {
			continue // the ban list itself names the banned tokens
		}
		info, err := os.Stat(path)
		if err != nil || info.Size() > 4<<20 {
			continue // unreadable / gone / too big to be source
		}
		body, err := os.ReadFile(path)
		if err != nil {
			continue
		}
		// Skip binaries (compiled artifacts, images).
		if bytes.IndexByte(body[:min(len(body), 8192)], 0) != -1 {
			continue
		}
		scan(path, body)
	}

	if len(violations) > 0 {
		t.Errorf("real-infrastructure references found on the PUBLIC mirror surface:\n  %s",
			strings.Join(violations, "\n  "))
	}
}

// gitTrackedFiles returns absolute paths of files under the surface
// entries that would reach the mirror: tracked PLUS untracked-but-
// not-gitignored (`--others --exclude-standard`), so a leak in a
// freshly-written file fails the suite BEFORE it's ever committed,
// while gitignored local state (.cluster.dev/ cache, captured e2e
// debug logs) stays excluded. Returns nil when git is unavailable
// (caller falls back to a filesystem walk).
func gitTrackedFiles(repoRoot string, surface []string) []string {
	args := append([]string{"-C", repoRoot, "ls-files", "-z", "--cached", "--others", "--exclude-standard", "--"}, surface...)
	out, err := exec.Command("git", args...).Output()
	if err != nil {
		return nil
	}
	var files []string
	for _, rel := range strings.Split(string(out), "\x00") {
		if rel == "" {
			continue
		}
		files = append(files, filepath.Join(repoRoot, rel))
	}
	return files
}

// walkedFiles is the git-less fallback: filesystem walk over the
// surface with hidden-dir + junk-dir skips (approximates a fresh
// checkout; less precise than git ls-files but better than nothing).
func walkedFiles(t *testing.T, repoRoot string, surface []string) []string {
	t.Helper()
	var files []string
	for _, entry := range surface {
		root := filepath.Join(repoRoot, entry)
		info, err := os.Stat(root)
		if err != nil {
			continue // absent in this checkout (e.g. standalone cli clone)
		}
		if !info.IsDir() {
			files = append(files, root)
			continue
		}
		werr := filepath.WalkDir(root, func(path string, d os.DirEntry, err error) error {
			if err != nil {
				return err
			}
			if d.IsDir() {
				if skipDirs[d.Name()] || strings.HasPrefix(d.Name(), ".") {
					return filepath.SkipDir
				}
				return nil
			}
			files = append(files, path)
			return nil
		})
		if werr != nil {
			t.Fatalf("walk %s: %v", entry, werr)
		}
	}
	return files
}

func min(a, b int) int {
	if a < b {
		return a
	}
	return b
}
