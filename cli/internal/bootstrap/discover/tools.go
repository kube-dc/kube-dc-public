package discover

// M1-T01 local-tooling probes. Each probe shells out to a CLI's
// version command and emits a ports.Result describing presence +
// version-floor compliance. Probes are read-only and ctx-honouring
// per ports.Probe contract; missing tools return StatusMissing /
// SeverityBlocker with a FixHint pointing at scripts/install-
// prerequisites.sh.
//
// **Per-tool parsing** rather than a single regex: each upstream CLI
// emits its version in a slightly different shape (kubectl wants
// `--output=json`, sops prints "sops 3.10.2 (latest)", ssh writes
// `OpenSSH_9.6p1` to stderr — etc.). One parser per tool is shorter
// than one mega-regex that hedges against all of them.
//
// **gh extra check**: `gh auth status` must report repo + workflow
// scopes; missing scopes return StatusPartial pointing at
// `gh auth refresh --scopes repo,workflow` (per installer-ux §4.2).
//
// Version comparison handles `v1.32.1+rke2r1` → (1, 32, 1) by
// stripping the leading `v` and the `+...` build suffix.

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os/exec"
	"regexp"
	"strconv"
	"strings"

	"github.com/shalb/kube-dc/cli/internal/bootstrap/ports"
)

// InstallPrereqsHint is the canonical FixHint for any missing tool.
// The script lives in the kube-dc-fleet repo (NOT the kube-dc repo),
// so the doctor printer + M1-T06 cobra command must resolve it
// against the fleet-repo root (the wire-layer's --repo flag value).
const InstallPrereqsHint = "Install via scripts/install-prerequisites.sh (in the kube-dc-fleet repo)"

// MinVersions are the floors from installer-ux §4.2. Tools below the
// floor return StatusPartial + SeverityWarn (operational risk; not a
// hard blocker because some shipped versions of these tools are
// version-stamped oddly).
var MinVersions = map[string]Semver{
	"kubectl": {1, 28, 0},
	"flux":    {2, 4, 0},
	"sops":    {3, 9, 0},
	"age":     {1, 1, 0},
	"git":     {2, 30, 0},
	"gh":      {2, 40, 0},
	// `ssh` and `bao` have no floor in the contract — we just check
	// presence.
}

// execHook signature lets tests inject canned (stdout, stderr) pairs
// per command without spawning real processes.
type execHook func(ctx context.Context, name string, args ...string) (stdout, stderr []byte, err error)

func realExec(ctx context.Context, name string, args ...string) ([]byte, []byte, error) {
	cmd := exec.CommandContext(ctx, name, args...)
	var out, errb bytes.Buffer
	cmd.Stdout = &out
	cmd.Stderr = &errb
	err := cmd.Run()
	return out.Bytes(), errb.Bytes(), err
}

// ToolProbe implements ports.Probe for a single local CLI.
type ToolProbe struct {
	name      string
	versionFn func(ctx context.Context, exec execHook) (Semver, string, error)
	extraFn   func(ctx context.Context, exec execHook) *extraResult
	exec      execHook
}

// extraResult is the `gh auth status`-style post-version check.
// Returning a non-nil pointer overrides Severity/Status/FixHint.
type extraResult struct {
	status   ports.Status
	severity ports.Severity
	detail   string
	fixHint  ports.FixHint
}

// Compile-time assertion.
var _ ports.Probe = (*ToolProbe)(nil)

func (p *ToolProbe) Name() string { return p.name }

func (p *ToolProbe) Run(ctx context.Context) ports.Result {
	if err := ctxCanceled(ctx); err != nil {
		return *err
	}
	ver, raw, err := p.versionFn(ctx, p.exec)
	if err != nil {
		// Distinguish "binary missing" (exec.ErrNotFound) from
		// "binary present but version-probe failed" so the FixHint
		// is precise.
		if isNotFound(err) {
			return ports.Result{
				Status:   ports.StatusMissing,
				Severity: ports.SeverityBlocker,
				Detail:   fmt.Sprintf("%s not found in PATH", p.name),
				FixHint:  ports.FixHint{Text: InstallPrereqsHint},
			}
		}
		return ports.Result{
			Status:   ports.StatusPartial,
			Severity: ports.SeverityWarn,
			Detail:   fmt.Sprintf("%s found but version probe failed: %v", p.name, err),
		}
	}

	minVer, hasFloor := MinVersions[p.name]
	if hasFloor && ver.Less(minVer) {
		return ports.Result{
			Status:   ports.StatusPartial,
			Severity: ports.SeverityWarn,
			Version:  raw,
			Detail:   fmt.Sprintf("%s %s < required %s", p.name, raw, minVer.String()),
			FixHint:  ports.FixHint{Text: fmt.Sprintf("Upgrade %s to ≥%s via %s", p.name, minVer.String(), InstallPrereqsHint)},
		}
	}

	// Extra check (currently only gh's scope probe). Re-check ctx
	// before the second exec so a cancelled run doesn't fire
	// `gh auth status` after `gh --version` returned.
	if p.extraFn != nil {
		if err := ctxCanceled(ctx); err != nil {
			return *err
		}
		if x := p.extraFn(ctx, p.exec); x != nil {
			return ports.Result{
				Status:   x.status,
				Severity: x.severity,
				Version:  raw,
				Detail:   x.detail,
				FixHint:  x.fixHint,
			}
		}
	}

	return ports.Result{
		Status:   ports.StatusInstalled,
		Severity: ports.SeverityInfo,
		Version:  raw,
		Detail:   fmt.Sprintf("%s %s", p.name, raw),
	}
}

// AllToolProbes returns the canonical set of 8 probes wired to the
// real os/exec. Tests build their own slice with custom execHooks.
func AllToolProbes() []ports.Probe {
	return []ports.Probe{
		newKubectlProbe(realExec),
		newFluxProbe(realExec),
		newSOPSProbe(realExec),
		newAgeProbe(realExec),
		newGitProbe(realExec),
		newGHProbe(realExec),
		newSSHProbe(realExec),
		newBaoProbe(realExec),
	}
}

// ---------- per-tool constructors ----------

func newKubectlProbe(e execHook) *ToolProbe {
	return &ToolProbe{
		name: "kubectl",
		exec: e,
		versionFn: func(ctx context.Context, e execHook) (Semver, string, error) {
			// `kubectl version --client --output=json` is more robust
			// than text parsing across upstream versions.
			stdout, _, err := e(ctx, "kubectl", "version", "--client", "--output=json")
			if err != nil {
				return Semver{}, "", err
			}
			var v struct {
				ClientVersion struct {
					GitVersion string `json:"gitVersion"`
				} `json:"clientVersion"`
			}
			if err := json.Unmarshal(stdout, &v); err != nil {
				return Semver{}, "", fmt.Errorf("parse kubectl json: %w", err)
			}
			s, err := ParseSemver(v.ClientVersion.GitVersion)
			return s, v.ClientVersion.GitVersion, err
		},
	}
}

func newFluxProbe(e execHook) *ToolProbe {
	return &ToolProbe{
		name: "flux",
		exec: e,
		versionFn: func(ctx context.Context, e execHook) (Semver, string, error) {
			// `flux --version` -> "flux version v2.4.0"
			stdout, stderr, err := e(ctx, "flux", "--version")
			if err != nil {
				return Semver{}, "", err
			}
			combined := strings.TrimSpace(string(stdout) + string(stderr))
			fields := strings.Fields(combined)
			if len(fields) < 3 {
				return Semver{}, "", fmt.Errorf("unexpected flux output: %q", combined)
			}
			raw := fields[len(fields)-1]
			s, err := ParseSemver(raw)
			return s, raw, err
		},
	}
}

func newSOPSProbe(e execHook) *ToolProbe {
	return &ToolProbe{
		name: "sops",
		exec: e,
		versionFn: func(ctx context.Context, e execHook) (Semver, string, error) {
			// `sops --version` -> "sops 3.10.2 (latest)" / "sops 3.10.2"
			stdout, stderr, err := e(ctx, "sops", "--version")
			if err != nil {
				return Semver{}, "", err
			}
			combined := strings.TrimSpace(string(stdout) + string(stderr))
			fields := strings.Fields(combined)
			if len(fields) < 2 {
				return Semver{}, "", fmt.Errorf("unexpected sops output: %q", combined)
			}
			s, err := ParseSemver(fields[1])
			return s, fields[1], err
		},
	}
}

func newAgeProbe(e execHook) *ToolProbe {
	return &ToolProbe{
		name: "age",
		exec: e,
		versionFn: func(ctx context.Context, e execHook) (Semver, string, error) {
			// `age --version` -> "v1.2.1" or "1.2.1"
			stdout, stderr, err := e(ctx, "age", "--version")
			if err != nil {
				return Semver{}, "", err
			}
			raw := strings.TrimSpace(string(stdout) + string(stderr))
			s, err := ParseSemver(raw)
			return s, raw, err
		},
	}
}

func newGitProbe(e execHook) *ToolProbe {
	return &ToolProbe{
		name: "git",
		exec: e,
		versionFn: func(ctx context.Context, e execHook) (Semver, string, error) {
			// `git --version` -> "git version 2.43.0"
			stdout, _, err := e(ctx, "git", "--version")
			if err != nil {
				return Semver{}, "", err
			}
			fields := strings.Fields(string(stdout))
			if len(fields) < 3 {
				return Semver{}, "", fmt.Errorf("unexpected git output: %q", stdout)
			}
			raw := fields[2]
			s, err := ParseSemver(raw)
			return s, raw, err
		},
	}
}

func newGHProbe(e execHook) *ToolProbe {
	return &ToolProbe{
		name: "gh",
		exec: e,
		versionFn: func(ctx context.Context, e execHook) (Semver, string, error) {
			// `gh --version` -> "gh version 2.51.0 (2024-04-15)"
			stdout, _, err := e(ctx, "gh", "--version")
			if err != nil {
				return Semver{}, "", err
			}
			fields := strings.Fields(string(stdout))
			if len(fields) < 3 {
				return Semver{}, "", fmt.Errorf("unexpected gh output: %q", stdout)
			}
			raw := fields[2]
			s, err := ParseSemver(raw)
			return s, raw, err
		},
		extraFn: func(ctx context.Context, e execHook) *extraResult {
			// `gh auth status` writes to stderr and exits 0 when
			// authenticated. Parse for `repo` + `workflow` scopes.
			_, stderr, err := e(ctx, "gh", "auth", "status")
			if err != nil {
				return &extraResult{
					status:   ports.StatusPartial,
					severity: ports.SeverityWarn,
					detail:   "gh installed but not authenticated",
					fixHint:  ports.FixHint{Text: "Run `gh auth login` and grant repo + workflow scopes"},
				}
			}
			// Look for "Token scopes: 'repo', 'workflow', ..." line.
			body := string(stderr)
			scopes := parseGHScopes(body)
			missing := RequiredGHScopes(scopes)
			if len(missing) == 0 {
				return nil // happy path — fall through to default StatusInstalled
			}
			return &extraResult{
				status:   ports.StatusPartial,
				severity: ports.SeverityWarn,
				detail:   fmt.Sprintf("gh missing scopes: %s", strings.Join(missing, ", ")),
				fixHint:  ports.FixHint{Text: "Run `gh auth refresh --scopes repo,workflow` to grant missing scopes"},
			}
		},
	}
}

func newSSHProbe(e execHook) *ToolProbe {
	return &ToolProbe{
		name: "ssh",
		exec: e,
		versionFn: func(ctx context.Context, e execHook) (Semver, string, error) {
			// `ssh -V` writes to STDERR, not stdout. e.g. "OpenSSH_9.6p1 ..."
			_, stderr, err := e(ctx, "ssh", "-V")
			if err != nil {
				return Semver{}, "", err
			}
			raw := strings.TrimSpace(string(stderr))
			// "OpenSSH_9.6p1" → "9.6" — Semver is loose for ssh
			// since there's no patch number. Treat p1 as patch.
			s, err := parseOpenSSHVersion(raw)
			return s, raw, err
		},
	}
}

func newBaoProbe(e execHook) *ToolProbe {
	return &ToolProbe{
		name: "bao",
		exec: e,
		versionFn: func(ctx context.Context, e execHook) (Semver, string, error) {
			// `bao --version` -> "OpenBao v2.5.3 ('xxx')"
			stdout, stderr, err := e(ctx, "bao", "--version")
			if err != nil {
				return Semver{}, "", err
			}
			combined := strings.TrimSpace(string(stdout) + string(stderr))
			fields := strings.Fields(combined)
			if len(fields) < 2 {
				return Semver{}, "", fmt.Errorf("unexpected bao output: %q", combined)
			}
			raw := fields[1]
			s, err := ParseSemver(raw)
			return s, raw, err
		},
	}
}

// ---------- helpers ----------

type Semver struct {
	major, minor, patch int
}

func (s Semver) String() string { return fmt.Sprintf("%d.%d.%d", s.major, s.minor, s.patch) }

func (s Semver) Less(o Semver) bool {
	if s.major != o.major {
		return s.major < o.major
	}
	if s.minor != o.minor {
		return s.minor < o.minor
	}
	return s.patch < o.patch
}

// semverRegex captures major.minor.patch from strings like
// "v1.32.1+rke2r1", "1.32.1", "v2.4.0-beta.1". Trailing pre-release
// / build metadata is ignored.
var semverRegex = regexp.MustCompile(`v?(\d+)\.(\d+)\.(\d+)`)

func ParseSemver(raw string) (Semver, error) {
	m := semverRegex.FindStringSubmatch(raw)
	if len(m) != 4 {
		return Semver{}, fmt.Errorf("no Semver in %q", raw)
	}
	maj, _ := strconv.Atoi(m[1])
	min, _ := strconv.Atoi(m[2])
	pat, _ := strconv.Atoi(m[3])
	return Semver{maj, min, pat}, nil
}

// opensshRegex captures "OpenSSH_9.6p1" / "OpenSSH_8.9".
var opensshRegex = regexp.MustCompile(`OpenSSH_(\d+)\.(\d+)(?:p(\d+))?`)

func parseOpenSSHVersion(raw string) (Semver, error) {
	m := opensshRegex.FindStringSubmatch(raw)
	if len(m) < 3 {
		return Semver{}, fmt.Errorf("not an OpenSSH version string: %q", raw)
	}
	maj, _ := strconv.Atoi(m[1])
	min, _ := strconv.Atoi(m[2])
	pat := 0
	if len(m) > 3 && m[3] != "" {
		pat, _ = strconv.Atoi(m[3])
	}
	return Semver{maj, min, pat}, nil
}

// parseGHScopes pulls the comma-separated scopes from a `gh auth
// status` output. Returns nil when no scopes line is found.
//
// The gh CLI prefixes scope lines with a "✓" check-mark glyph
// followed by whitespace, e.g. "  ✓ Token scopes: 'repo', 'workflow'".
// We use Contains rather than HasPrefix so we don't have to track
// gh's UI glyph evolution across versions.
func parseGHScopes(body string) []string {
	const marker = "Token scopes:"
	for _, line := range strings.Split(body, "\n") {
		idx := strings.Index(line, marker)
		if idx < 0 {
			continue
		}
		raw := strings.TrimSpace(line[idx+len(marker):])
		// Scopes are comma-separated, each quoted: 'repo', 'workflow', 'gist'
		var scopes []string
		for _, p := range strings.Split(raw, ",") {
			s := strings.TrimSpace(p)
			s = strings.Trim(s, `'"`)
			if s != "" {
				scopes = append(scopes, s)
			}
		}
		return scopes
	}
	return nil
}

// RequiredGHScopes returns the missing scopes from the required set.
// Empty slice means "all present".
func RequiredGHScopes(have []string) []string {
	required := []string{"repo", "workflow"}
	hashave := map[string]bool{}
	for _, s := range have {
		hashave[s] = true
	}
	var missing []string
	for _, r := range required {
		if !hashave[r] {
			missing = append(missing, r)
		}
	}
	return missing
}

func isNotFound(err error) bool {
	if err == nil {
		return false
	}
	if errors.Is(err, exec.ErrNotFound) {
		return true
	}
	msg := strings.ToLower(err.Error())
	return strings.Contains(msg, "executable file not found") || strings.Contains(msg, "no such file or directory")
}
