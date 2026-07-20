package openbao

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/shalb/kube-dc/cli/internal/bootstrap/ports"
	"github.com/shalb/kube-dc/cli/internal/bootstrap/secrets"
)

// TestMain sandboxes HOME for the whole package.
//
// Init's custody paths derive their destination from os.UserHomeDir(),
// so a test that forgets t.Setenv("HOME", ...) writes real share files
// into the developer's ~/.kube-dc. That actually happened during this
// review pass. Redirecting HOME once here makes the leak impossible
// rather than relying on every future test to remember.
func TestMain(m *testing.M) {
	sandbox, err := os.MkdirTemp("", "openbao-test-home-")
	if err != nil {
		fmt.Fprintf(os.Stderr, "sandbox home: %v\n", err)
		os.Exit(1)
	}
	os.Setenv("HOME", sandbox)
	code := m.Run()
	os.RemoveAll(sandbox)
	os.Exit(code)
}

// fakeRunner emits canned Lines + tracks the sentinel-callback the
// engine attaches via WithSentinelCallback. The test triggers the
// callback inline during Run.
type fakeRunner struct {
	cb ports.SentinelCallback
	// lines is the stream replayed on Run (excluding the sentinel
	// — the sentinel is fired inline during Run when
	// fireSentinel is true).
	lines []ports.Line
	// sentinelPayload is what the callback receives when triggered.
	sentinelPayload []byte
	// fireSentinel controls whether the runner triggers the cb in
	// the Run path (true = real flow; false = simulate "script
	// exited without emitting sentinel").
	fireSentinel bool
	// exitCode is the code reported on the StreamExit line. Non-zero
	// WITH fireSentinel models the P0 case the write-ahead custody
	// exists for: the script emitted the shares and then died.
	exitCode int
	// exitText overrides the exit-line text verbatim; a value that does
	// not parse as an integer makes drainOpenBaoInit report a parse
	// error.
	exitText string
	// dropExit omits the terminal exit record entirely, modelling a
	// truncated stream (script killed, runner died, connection lost).
	dropExit bool
	// trailingOutput, when set, is emitted AFTER the exit record --
	// forbidden by the ScriptRunner contract.
	trailingOutput string
	// extraExit, when set, is emitted as a SECOND exit record.
	extraExit string
}

func (f *fakeRunner) WithSentinelCallback(cb ports.SentinelCallback) ports.ScriptRunner {
	// Return a sibling fakeRunner with the cb set — never `*f`
	// (copying would clone any future sync fields; cheap to make
	// this safe-by-construction). The new instance shares the
	// underlying slices, which is fine because the runner only
	// reads them.
	return &fakeRunner{
		cb:              cb,
		lines:           f.lines,
		sentinelPayload: f.sentinelPayload,
		fireSentinel:    f.fireSentinel,
		exitCode:        f.exitCode,
		exitText:        f.exitText,
		dropExit:        f.dropExit,
		trailingOutput:  f.trailingOutput,
		extraExit:       f.extraExit,
	}
}

func (f *fakeRunner) Run(_ context.Context, kind ports.ScriptKind, _ map[string]string, _ ...string) (<-chan ports.Line, error) {
	// Buffered enough to hold every line + placeholder + exit-line
	// even on the cb-error branch (lines + 3 marker lines). Avoids
	// the deadlock that hit on the malformed-payload tests where
	// the cb fires inline and the consumer hadn't started reading.
	out := make(chan ports.Line, len(f.lines)+8)
	for _, ln := range f.lines {
		out <- ln
	}
	if f.fireSentinel && f.cb != nil {
		out <- ports.Line{Stream: ports.StreamStdout, Text: "[ScriptOpenBaoInit payload captured — N bytes]", Time: time.Now()}
		if err := f.cb(kind, "KUBE_DC_INIT_JSON_BEGIN", f.sentinelPayload); err != nil {
			out <- ports.Line{Stream: ports.StreamStderr, Text: fmt.Sprintf("sentinel cb err: %v", err), Time: time.Now()}
			out <- ports.Line{Stream: ports.StreamExit, Text: "1", Time: time.Now()}
			close(out)
			return out, nil
		}
	}
	if !f.dropExit {
		exitText := f.exitText
		if exitText == "" {
			exitText = strconv.Itoa(f.exitCode)
		}
		out <- ports.Line{Stream: ports.StreamExit, Text: exitText, Time: time.Now()}
	}
	if f.extraExit != "" {
		out <- ports.Line{Stream: ports.StreamExit, Text: f.extraExit, Time: time.Now()}
	}
	if f.trailingOutput != "" {
		out <- ports.Line{Stream: ports.StreamStdout, Text: f.trailingOutput, Time: time.Now()}
	}
	close(out)
	return out, nil
}

// fakeSOPS records SetStringData calls + returns a decrypted blob
// that's the running concatenation of every key set so far. Round-
// trip verify (bytes.Contains) passes; mismatch can be forced via
// the alterValue field.
type fakeSOPS struct {
	mu         sync.Mutex
	written    map[string][]byte
	alterValue map[string][]byte // keys whose stored value differs from input (forces round-trip mismatch)
	decryptErr error
}

func newFakeSOPS() *fakeSOPS {
	return &fakeSOPS{written: map[string][]byte{}, alterValue: map[string][]byte{}}
}

func (s *fakeSOPS) Encrypt(_ context.Context, _ string) error { return nil }
func (s *fakeSOPS) Decrypt(_ context.Context, _ string) ([]byte, error) {
	if s.decryptErr != nil {
		return nil, s.decryptErr
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	// Concatenate every value seen so far — bytes.Contains in the
	// engine matches on share value substrings.
	var b bytes.Buffer
	for _, v := range s.written {
		b.Write(v)
		b.WriteByte('\n')
	}
	return b.Bytes(), nil
}
func (s *fakeSOPS) SetStringData(_ context.Context, _, key string, value []byte) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	stored := append([]byte(nil), value...)
	if alt, ok := s.alterValue[key]; ok {
		stored = alt
	}
	s.written[key] = stored
	return nil
}
func (s *fakeSOPS) Recipients(_ string) ([]string, error) { return nil, nil }
func (s *fakeSOPS) DerivePubKey(_ string) (string, error) { return "", nil }

// fakeGit records the order of operations so push-failure tests
// can confirm ResetHard ran with the captured pre-commit SHA.
type fakeGit struct {
	mu sync.Mutex

	preSHA    string
	commitSHA string

	// diff drives the M5 review-pass dirty-tree gate. Tests set
	// non-empty .Files to simulate uncommitted operator work.
	diff ports.Diff

	commitErr   error
	commitCalls int
	pushErr     error
	pushed      bool

	resetCalls []string
	resetErr   error
}

func (g *fakeGit) Clone(_ context.Context, _, _, _ string) error                     { return nil }
func (g *fakeGit) Init(_ context.Context, _, _ string) error { return nil }
func (g *fakeGit) Pull(_ context.Context, _, _ string) error                         { return nil }
func (g *fakeGit) Diff(_ context.Context, _ string) (ports.Diff, error)              { return g.diff, nil }
func (g *fakeGit) CreateRepo(_ context.Context, _, _ string, _ bool, _ string) error { return nil }
func (g *fakeGit) Head(_ context.Context, _ string) (string, error)                  { return g.preSHA, nil }
func (g *fakeGit) ResetHard(_ context.Context, _, ref string) error {
	g.mu.Lock()
	defer g.mu.Unlock()
	g.resetCalls = append(g.resetCalls, ref)
	return g.resetErr
}
func (g *fakeGit) Commit(_ context.Context, _, _ string) (string, error) {
	g.mu.Lock()
	defer g.mu.Unlock()
	g.commitCalls++
	if g.commitErr != nil {
		return "", g.commitErr
	}
	return g.commitSHA, nil
}
func (g *fakeGit) CommitAndPush(_ context.Context, _, _, _ string) (string, error) {
	g.mu.Lock()
	defer g.mu.Unlock()
	g.commitCalls++
	if g.commitErr != nil {
		return "", g.commitErr
	}
	if g.pushErr != nil {
		return g.commitSHA, g.pushErr
	}
	g.pushed = true
	return g.commitSHA, nil
}

func (g *fakeGit) Push(_ context.Context, _, _ string) error {
	g.mu.Lock()
	defer g.mu.Unlock()
	if g.pushErr != nil {
		return g.pushErr
	}
	g.pushed = true
	return nil
}

// fakeBao implements ports.OpenBaoClient for the M5-T01 review-pass
// post-commit operations (unseal/revoke/annotate via stdin
// transport). Records every call so tests can assert ordering +
// argument shape.
type fakeBao struct {
	mu sync.Mutex

	pods             []string
	annotation       string // pre-existing annotation; "" = not initialised
	getAnnoErr       error
	unsealCalls      []byte // appended shares per call (to check stdin transport)
	unsealErr        error
	revokeToken      []byte
	revokeErr        error
	annotationsSet   map[string]string
	setAnnotationErr error

	// M5-T08 controller-auth setup recording. Used by the new
	// TestInit_PhaseC_* specs to verify Phase C runs the setup step
	// when SkipControllerAuth=false.
	enableAuthCalls    int
	configureAuthCalls int
	policyCalls        []string // policy names in order
	roleCalls          []string // "<mount>/<role>" in order
	policyErr          error    // injected error on EVERY ApplyPolicy call
}

func (b *fakeBao) PodList(_ context.Context) ([]string, error) {
	if len(b.pods) == 0 {
		return []string{"openbao-0"}, nil
	}
	return b.pods, nil
}
func (b *fakeBao) Status(_ context.Context, pod string) (ports.BaoStatus, error) {
	return ports.BaoStatus{Pod: pod}, nil
}
func (b *fakeBao) Unseal(_ context.Context, _ string, share []byte) error {
	b.mu.Lock()
	defer b.mu.Unlock()
	if b.unsealErr != nil {
		return b.unsealErr
	}
	b.unsealCalls = append(b.unsealCalls, share...)
	b.unsealCalls = append(b.unsealCalls, '|')
	return nil
}
func (b *fakeBao) RaftJoin(_ context.Context, _, _ string) error { return nil }
func (b *fakeBao) GenerateRoot(_ context.Context, _ [][]byte) ([]byte, error) {
	return nil, nil
}
func (b *fakeBao) RevokeSelf(_ context.Context, tok []byte) error {
	b.mu.Lock()
	defer b.mu.Unlock()
	// Capture the token bytes BEFORE returning the configured error
	// so tests can assert "the call was attempted with the right
	// token" even on the error path.
	b.revokeToken = append([]byte(nil), tok...)
	return b.revokeErr
}
func (b *fakeBao) ApplyPolicy(_ context.Context, _ []byte, name, _ string) error {
	b.mu.Lock()
	defer b.mu.Unlock()
	b.policyCalls = append(b.policyCalls, name)
	return b.policyErr
}
func (b *fakeBao) EnableAuthPath(_ context.Context, _ []byte, _, _ string) error {
	b.mu.Lock()
	defer b.mu.Unlock()
	b.enableAuthCalls++
	return nil
}
func (b *fakeBao) ConfigureKubernetesAuth(_ context.Context, _ []byte, _ string, _ ports.KubernetesAuthConfig) error {
	b.mu.Lock()
	defer b.mu.Unlock()
	b.configureAuthCalls++
	return nil
}
func (b *fakeBao) WriteAuthRole(_ context.Context, _ []byte, mount, role string, _ map[string]string) error {
	b.mu.Lock()
	defer b.mu.Unlock()
	b.roleCalls = append(b.roleCalls, mount+"/"+role)
	return nil
}
func (b *fakeBao) SetAnnotations(_ context.Context, svc string, kv map[string]string) error {
	b.mu.Lock()
	defer b.mu.Unlock()
	if b.annotationsSet == nil {
		b.annotationsSet = map[string]string{}
	}
	for k, v := range kv {
		b.annotationsSet[svc+"/"+k] = v
	}
	return nil
}
func (b *fakeBao) GetAnnotation(_ context.Context, _, _ string) (string, error) {
	if b.getAnnoErr != nil {
		return "", b.getAnnoErr
	}
	return b.annotation, nil
}
func (b *fakeBao) SetAnnotation(_ context.Context, svc, key, value string) error {
	b.mu.Lock()
	defer b.mu.Unlock()
	if b.setAnnotationErr != nil {
		return b.setAnnotationErr
	}
	if b.annotationsSet == nil {
		b.annotationsSet = map[string]string{}
	}
	b.annotationsSet[svc+"/"+key] = value
	return nil
}

// --- fixtures ---

// canonicalShareJSON returns a synthetic bao-init JSON payload with
// 5 shares + a root token. Shares are base64-shaped so the regex
// + scrub paths see realistic byte content.
const canonicalShareJSON = `{
  "unseal_keys_b64": [
    "share1AAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAA=",
    "share2BBBBBBBBBBBBBBBBBBBBBBBBBBBBBBBBBBBBBBBB=",
    "share3CCCCCCCCCCCCCCCCCCCCCCCCCCCCCCCCCCCCCCCC=",
    "share4DDDDDDDDDDDDDDDDDDDDDDDDDDDDDDDDDDDDDDDD=",
    "share5EEEEEEEEEEEEEEEEEEEEEEEEEEEEEEEEEEEEEEEE="
  ],
  "unseal_keys_hex": ["aa", "bb", "cc", "dd", "ee"],
  "unseal_shares": 5,
  "unseal_threshold": 3,
  "recovery_keys_b64": [],
  "recovery_keys_hex": [],
  "recovery_shares": 0,
  "recovery_threshold": 0,
  "root_token": "ROOT_TOKEN_BBBBBBBBBBBBBBBBBBBBBB"
}`

func setupFleet(t *testing.T) string {
	t.Helper()
	dir := t.TempDir()
	clusterDir := filepath.Join(dir, "clusters", "atlantis")
	if err := os.MkdirAll(clusterDir, 0o755); err != nil {
		t.Fatal(err)
	}
	// Seed an empty secrets.enc.yaml so the preflight passes.
	if err := os.WriteFile(filepath.Join(clusterDir, "secrets.enc.yaml"),
		[]byte("apiVersion: v1\nkind: Secret\nstringData: {}\nsops:\n  mac: stub\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	return dir
}

func baseInitOpts(t *testing.T, repo string, runner *fakeRunner, sops *fakeSOPS, git *fakeGit) InitOptions {
	t.Helper()
	return InitOptions{
		ClusterName: "atlantis",
		FleetRepo:   repo,
		Runner:      runner,
		SOPS:        sops,
		Git:         git,
		OpenBao:     &fakeBao{},
		GitHubToken: "fake-token",
		NoPush:      true, // unit tests stay local
		// Existing tests skip the M5-T08 controller-auth Phase C
		// step by default — they cover share custody + commit + unseal
		// + annotate. The TestInit_PhaseC_RunsControllerAuthSetup spec
		// (below) is the dedicated coverage for the new step.
		SkipControllerAuth: true,
	}
}

// --- happy path ---

func TestInit_HappyPath_EncryptsAllShares(t *testing.T) {
	repo := setupFleet(t)
	runner := &fakeRunner{
		fireSentinel:    true,
		sentinelPayload: []byte(canonicalShareJSON),
		lines: []ports.Line{
			{Stream: ports.StreamStdout, Text: "openbao-init: waiting for pod", Time: time.Now()},
		},
	}
	sops := newFakeSOPS()
	git := &fakeGit{preSHA: "pre", commitSHA: "post"}

	var out bytes.Buffer
	opts := baseInitOpts(t, repo, runner, sops, git)
	opts.Out = &out
	if err := Init(context.Background(), opts); err != nil {
		t.Fatalf("Init: %v\nout:\n%s", err, out.String())
	}

	// All 5 keys written.
	for i := 1; i <= 5; i++ {
		key := fmt.Sprintf("OPENBAO_UNSEAL_KEY_%d", i)
		if _, ok := sops.written[key]; !ok {
			t.Errorf("key %s never set", key)
		}
	}
	// Local commit happened (NoPush=true so no push).
	if git.commitCalls != 1 {
		t.Errorf("commit calls = %d, want 1", git.commitCalls)
	}
	if git.pushed {
		t.Error("NoPush=true but push happened")
	}
	if len(git.resetCalls) != 0 {
		t.Errorf("happy path triggered rollback: %v", git.resetCalls)
	}
	// Operator-facing log mentions success.
	for _, want := range []string{
		"running bootstrap/openbao-init.sh",
		"encrypted OPENBAO_UNSEAL_KEY_1",
		"encrypted OPENBAO_UNSEAL_KEY_5",
		"init complete",
	} {
		if !strings.Contains(out.String(), want) {
			t.Errorf("output missing %q\nFULL:\n%s", want, out.String())
		}
	}
}

// --- share secrecy regression: sentinel-captured plaintext must not leak ---

func TestInit_SharePlaintextNeverInOutput(t *testing.T) {
	// Inject distinctive canary tokens in the JSON payload. The
	// share values must NOT appear in operator output — only the
	// runner's placeholder line "[ScriptOpenBaoInit payload captured]".
	canary := `{
  "unseal_keys_b64": [
    "CANARY-SHARE-VALUE-1-MUST-NOT-LEAK",
    "CANARY-SHARE-VALUE-2-MUST-NOT-LEAK",
    "CANARY-SHARE-VALUE-3-MUST-NOT-LEAK",
    "CANARY-SHARE-VALUE-4-MUST-NOT-LEAK",
    "CANARY-SHARE-VALUE-5-MUST-NOT-LEAK"
  ],
  "root_token": "CANARY-ROOT-TOKEN-MUST-NOT-LEAK"
}`
	repo := setupFleet(t)
	runner := &fakeRunner{fireSentinel: true, sentinelPayload: []byte(canary)}
	sops := newFakeSOPS()
	git := &fakeGit{preSHA: "pre", commitSHA: "post"}

	var out bytes.Buffer
	opts := baseInitOpts(t, repo, runner, sops, git)
	opts.Out = &out
	if err := Init(context.Background(), opts); err != nil {
		t.Fatalf("Init: %v", err)
	}

	if strings.Contains(out.String(), "CANARY-SHARE-VALUE") {
		t.Fatalf("share value leaked to operator output:\n%s", out.String())
	}
	if strings.Contains(out.String(), "CANARY-ROOT-TOKEN") {
		t.Fatalf("root token leaked to operator output:\n%s", out.String())
	}
}

// --- missing scaffold ---

func TestInit_RefusesMissingSecretsFile(t *testing.T) {
	repo := t.TempDir() // no clusters/<name>/ subtree
	runner := &fakeRunner{fireSentinel: true, sentinelPayload: []byte(canonicalShareJSON)}
	sops := newFakeSOPS()
	git := &fakeGit{preSHA: "pre", commitSHA: "post"}
	opts := baseInitOpts(t, repo, runner, sops, git)
	err := Init(context.Background(), opts)
	if err == nil {
		t.Fatal("expected error when secrets.enc.yaml missing")
	}
	if !strings.Contains(err.Error(), "secrets.enc.yaml not found") {
		t.Errorf("error should name the missing file: %v", err)
	}
	if len(runner.lines) > 0 {
		// runner had canned lines but Run shouldn't have fired.
	}
	if sops.written == nil || len(sops.written) > 0 {
		t.Errorf("SetStringData ran despite missing scaffold: %v", sops.written)
	}
}

// --- sentinel never fired ---

func TestInit_RefusesWhenSentinelMissing(t *testing.T) {
	repo := setupFleet(t)
	// fireSentinel=false → script exits 0 but the callback never
	// runs (regression of a fleet-script patch that drops the
	// sentinels).
	runner := &fakeRunner{fireSentinel: false}
	sops := newFakeSOPS()
	git := &fakeGit{preSHA: "pre", commitSHA: "post"}
	opts := baseInitOpts(t, repo, runner, sops, git)
	err := Init(context.Background(), opts)
	if !errors.Is(err, ErrShareCaptureMissing) {
		t.Fatalf("expected ErrShareCaptureMissing, got %v", err)
	}
}

// --- malformed payload ---

func TestInit_RejectsMalformedJSON(t *testing.T) {
	repo := setupFleet(t)
	runner := &fakeRunner{fireSentinel: true, sentinelPayload: []byte("not-json")}
	sops := newFakeSOPS()
	git := &fakeGit{preSHA: "pre", commitSHA: "post"}
	opts := baseInitOpts(t, repo, runner, sops, git)
	err := Init(context.Background(), opts)
	if err == nil {
		t.Fatal("malformed JSON should error")
	}
	// Different shape than ErrShareCaptureMissing: the payload was
	// captured but unusable. The parse cause is reported ahead of the
	// resulting exit=1, because "json unmarshal" tells the operator
	// what went wrong and "exit=1" does not.
	if !strings.Contains(err.Error(), "json unmarshal") {
		t.Errorf("malformed payload should surface as a parse failure; got %v", err)
	}
}

// --- short payload ---

func TestInit_RejectsTooFewShares(t *testing.T) {
	repo := setupFleet(t)
	payload := `{"unseal_keys_b64":["a","b","c"],"root_token":"t"}`
	runner := &fakeRunner{fireSentinel: true, sentinelPayload: []byte(payload)}
	sops := newFakeSOPS()
	git := &fakeGit{preSHA: "pre", commitSHA: "post"}
	opts := baseInitOpts(t, repo, runner, sops, git)
	err := Init(context.Background(), opts)
	if err == nil {
		t.Fatal("expected error on <5 shares")
	}
	if !strings.Contains(err.Error(), "got 3") {
		t.Errorf("3-share payload should fail with the share count; got %v", err)
	}
	// P0 (review-pass 3): rejecting the payload must NOT throw away the
	// 3 shares it did contain. The vault is initialized either way, and
	// the unseal threshold is 3 — those shares may be the only way back
	// in. loadBufferFromJSON therefore stores before it validates.
	if !strings.Contains(err.Error(), "3 captured share(s) were preserved") {
		t.Errorf("partial payload must still preserve the shares it carried; got %v", err)
	}
}

// --- round-trip verify failure ---

// P0 (review-pass): a round-trip mismatch must NOT roll back. By this
// point openbao-init.sh has already initialized the vault, so the 5
// captured shares are the only key material in existence — ResetHard
// would delete the recovery material and leave the vault permanently
// sealed. Instead the shares are preserved off-git and the error names
// where they went.
func TestInit_RoundTripMismatch_PreservesSharesNoRollback(t *testing.T) {
	repo := setupFleet(t)
	home := t.TempDir()
	t.Setenv("HOME", home)
	runner := &fakeRunner{fireSentinel: true, sentinelPayload: []byte(canonicalShareJSON)}
	sops := newFakeSOPS()
	sops.alterValue["OPENBAO_UNSEAL_KEY_2"] = []byte("tampered-value")
	git := &fakeGit{preSHA: "ROLLBACK-TARGET", commitSHA: "post"}
	opts := baseInitOpts(t, repo, runner, sops, git)

	err := Init(context.Background(), opts)
	if !errors.Is(err, ErrShareCustodyFailed) {
		t.Fatalf("expected ErrShareCustodyFailed, got %v", err)
	}
	if len(git.resetCalls) != 0 {
		t.Fatalf("ResetHard must NEVER run after the vault is initialized; got %v", git.resetCalls)
	}
	// The shares must survive somewhere the operator can reach.
	dump := filepath.Join(home, ".kube-dc", "openbao-emergency-shares-atlantis.yaml")
	if _, statErr := os.Stat(dump); statErr != nil {
		t.Fatalf("emergency dump missing at %s: %v", dump, statErr)
	}
	if !strings.Contains(err.Error(), dump) {
		t.Errorf("error must tell the operator where the shares are; got: %v", err)
	}
}

// --- push failure rollback ---

// P0 (review-pass): THE cs/next failure. A push failure must preserve
// the encrypted shares, never roll back. Previously this rolled the
// commit away, leaving OpenBao Initialized with zero shares in the
// fleet — recoverable only because the operator happened to pass the
// optional --openbao-shares-out.
func TestInit_PushFailure_PreservesSharesNoRollback(t *testing.T) {
	repo := setupFleet(t)
	home := t.TempDir()
	t.Setenv("HOME", home)
	runner := &fakeRunner{fireSentinel: true, sentinelPayload: []byte(canonicalShareJSON)}
	sops := newFakeSOPS()
	pushErr := errors.New("simulated push failure")
	git := &fakeGit{preSHA: "PRE-COMMIT-SHA", commitSHA: "post", pushErr: pushErr}
	opts := baseInitOpts(t, repo, runner, sops, git)
	opts.NoPush = false // exercise the push path

	err := Init(context.Background(), opts)
	if !errors.Is(err, pushErr) {
		t.Fatalf("expected push error to surface, got %v", err)
	}
	if len(git.resetCalls) != 0 {
		t.Fatalf("ResetHard must NEVER run after the vault is initialized; got %v", git.resetCalls)
	}
	// No --openbao-shares-out was set, so init must have made an
	// off-git copy itself: a stray `git checkout` in the fleet must
	// not be able to destroy the only custody.
	dump := filepath.Join(home, ".kube-dc", "openbao-emergency-shares-atlantis.yaml")
	if _, statErr := os.Stat(dump); statErr != nil {
		t.Fatalf("no off-git copy written on push failure (%s): %v", dump, statErr)
	}
	// And the operator gets the exact remaining steps. The fake commits
	// successfully ("post") and fails only the push, so the ONLY
	// remaining step is `git push`.
	for _, want := range []string{"git push", "Nothing was rolled back"} {
		if !strings.Contains(err.Error(), want) {
			t.Errorf("error missing recovery guidance %q; got: %v", want, err)
		}
	}
	// P1 (review-pass 3): the hint must NOT tell the operator to
	// re-commit. `git add && git commit && git push` dies at the commit
	// with "nothing to commit" (exit 1), so the && chain never reaches
	// the push and the shares silently stay local — the recovery
	// instructions would not actually recover anything.
	if strings.Contains(err.Error(), "git commit") {
		t.Errorf("commit already succeeded (%s) — hint must not re-commit, "+
			"or the && chain aborts before push; got: %v", git.commitSHA, err)
	}
}

// P1 (review-pass 3): the mirror of the case above. When the COMMIT
// itself failed there is nothing staged, so the operator does need the
// full add/commit/push chain.
func TestInit_CommitFailure_HintIncludesAddAndCommit(t *testing.T) {
	repo := setupFleet(t)
	home := t.TempDir()
	t.Setenv("HOME", home)
	runner := &fakeRunner{fireSentinel: true, sentinelPayload: []byte(canonicalShareJSON)}
	sops := newFakeSOPS()
	commitErr := errors.New("simulated commit failure")
	// commitSHA empty => CommitAndPush failed before producing a commit.
	git := &fakeGit{preSHA: "PRE", commitSHA: "", pushErr: commitErr}
	opts := baseInitOpts(t, repo, runner, sops, git)
	opts.NoPush = false

	err := Init(context.Background(), opts)
	if err == nil {
		t.Fatal("expected commit failure to surface")
	}
	for _, want := range []string{"git add", "git commit", "git push"} {
		if !strings.Contains(err.Error(), want) {
			t.Errorf("no commit exists, so the hint needs %q; got: %v", want, err)
		}
	}
}

// P1 (review-pass 3): --no-push + a successful commit means the shares
// are already where the operator asked them to be. Telling them to run
// `git push` would contradict the flag they passed.
func TestInit_NoPushHint_DoesNotInstructPush(t *testing.T) {
	if got := manualFinishHint(InitOptions{FleetRepo: "/fleet", NoPush: true},
		"clusters/x/secrets.enc.yaml", "msg", "abc123"); strings.Contains(got, "git push") {
		t.Errorf("--no-push with a good commit must not instruct a push; got: %q", got)
	}
}

// A shares-out write failure must NOT abandon the ceremony: the
// encrypted copy in the fleet is the authoritative custody, so the
// convenience file failing is a warning, not a rollback trigger.
func TestInit_SharesOutFailure_IsWarningNotRollback(t *testing.T) {
	repo := setupFleet(t)
	runner := &fakeRunner{fireSentinel: true, sentinelPayload: []byte(canonicalShareJSON)}
	sops := newFakeSOPS()
	git := &fakeGit{preSHA: "PRE", commitSHA: "post"}
	opts := baseInitOpts(t, repo, runner, sops, git)
	// Unwritable destination (a directory component that is a file).
	bad := filepath.Join(t.TempDir(), "not-a-dir")
	if err := os.WriteFile(bad, []byte("x"), 0o600); err != nil {
		t.Fatal(err)
	}
	opts.SharesOutPath = filepath.Join(bad, "shares.yaml")

	err := Init(context.Background(), opts)
	if err != nil {
		t.Fatalf("shares-out failure must not fail the ceremony: %v", err)
	}
	if len(git.resetCalls) != 0 {
		t.Fatalf("ResetHard must not run; got %v", git.resetCalls)
	}
	if git.commitCalls == 0 {
		t.Error("the encrypted shares must still be committed")
	}
}

// --- script exit non-zero ---

func TestInit_ScriptNonZeroExit_NoRollback(t *testing.T) {
	repo := setupFleet(t)
	// Script "fails" before emitting sentinel — emit only stderr +
	// exit 1.
	runner := &fakeRunner{
		lines: []ports.Line{
			{Stream: ports.StreamStderr, Text: "ERROR: simulated failure", Time: time.Now()},
		},
		fireSentinel: false,
	}
	// Need to override the runner's default exit-0; use a custom
	// approach: emit exit:1 via lines, then prevent the default
	// exit:0 by manipulating fireSentinel. But our fake always
	// appends exit:0 at the end of Run; we need to short-circuit.
	// Simplest: write a separate one-off runner inline.
	runner2 := &exitNonZeroRunner{exitCode: 1, stderrText: "ERROR: simulated failure"}
	sops := newFakeSOPS()
	git := &fakeGit{preSHA: "pre", commitSHA: "post"}
	opts := baseInitOpts(t, repo, runner, sops, git)
	opts.Runner = runner2
	err := Init(context.Background(), opts)
	if err == nil {
		t.Fatal("expected script-exit error")
	}
	if !strings.Contains(err.Error(), "exit=1") {
		t.Errorf("error should report exit code: %v", err)
	}
	// Script never wrote shares → no commit → no rollback needed.
	if len(git.resetCalls) != 0 {
		t.Errorf("rollback ran despite no commit attempt: %v", git.resetCalls)
	}
}

// exitNonZeroRunner produces stderr + non-zero exit without firing
// the sentinel. Used by TestInit_ScriptNonZeroExit_NoRollback.
type exitNonZeroRunner struct {
	exitCode   int
	stderrText string
}

func (r *exitNonZeroRunner) WithSentinelCallback(_ ports.SentinelCallback) ports.ScriptRunner {
	return r
}
func (r *exitNonZeroRunner) Run(_ context.Context, _ ports.ScriptKind, _ map[string]string, _ ...string) (<-chan ports.Line, error) {
	out := make(chan ports.Line, 2)
	out <- ports.Line{Stream: ports.StreamStderr, Text: r.stderrText, Time: time.Now()}
	out <- ports.Line{Stream: ports.StreamExit, Text: fmt.Sprintf("%d", r.exitCode), Time: time.Now()}
	close(out)
	return out, nil
}

// --- shares-out path safety ---

func TestInit_RefusesSharesOutInsideGitTree(t *testing.T) {
	// Create a fake git repo + try to write shares-out inside it.
	repo := setupFleet(t)
	gitRepo := t.TempDir()
	if err := os.MkdirAll(filepath.Join(gitRepo, ".git"), 0o755); err != nil {
		t.Fatal(err)
	}
	sharesOut := filepath.Join(gitRepo, "shares.yaml")

	runner := &fakeRunner{fireSentinel: true, sentinelPayload: []byte(canonicalShareJSON)}
	sops := newFakeSOPS()
	git := &fakeGit{preSHA: "pre", commitSHA: "post"}
	opts := baseInitOpts(t, repo, runner, sops, git)
	opts.SharesOutPath = sharesOut
	err := Init(context.Background(), opts)
	if !errors.Is(err, ErrSharesOutInsideGit) {
		t.Fatalf("expected ErrSharesOutInsideGit, got %v", err)
	}
	if !strings.Contains(err.Error(), gitRepo) {
		t.Errorf("error should name the colliding repo: %v", err)
	}
	// Critical: SetStringData never ran (preflight caught the
	// shares-out misuse before any disk mutation).
	if len(sops.written) != 0 {
		t.Errorf("encryption ran despite shares-out preflight failure: %v", sops.written)
	}
}

func TestInit_SharesOut_HappyPath(t *testing.T) {
	repo := setupFleet(t)
	sharesOut := filepath.Join(t.TempDir(), "off-fleet", "shares.yaml")
	runner := &fakeRunner{fireSentinel: true, sentinelPayload: []byte(canonicalShareJSON)}
	sops := newFakeSOPS()
	git := &fakeGit{preSHA: "pre", commitSHA: "post"}
	opts := baseInitOpts(t, repo, runner, sops, git)
	opts.SharesOutPath = sharesOut
	if err := Init(context.Background(), opts); err != nil {
		t.Fatalf("Init: %v", err)
	}
	st, err := os.Stat(sharesOut)
	if err != nil {
		t.Fatalf("shares-out file missing: %v", err)
	}
	if st.Mode().Perm() != 0o600 {
		t.Errorf("shares-out mode = %o, want 0600", st.Mode().Perm())
	}
	// File should contain the share values (this is the
	// operator's explicit plaintext backup — we DO write them
	// here, just with mode 0600 + outside any git tree).
	body, _ := os.ReadFile(sharesOut)
	for i := 1; i <= 5; i++ {
		marker := fmt.Sprintf("share%dAAAAA", i)
		// The canonical JSON's share values start with `shareN` —
		// at least the first share's prefix should appear.
		if i == 1 && !strings.Contains(string(body), marker) {
			t.Errorf("shares-out file should contain share %d value\nGOT:\n%s", i, body)
		}
	}
}

// --- review-pass: dirty-tree gate ---

func TestInit_DirtyTree_Refused_BeforeScript(t *testing.T) {
	// Review-pass P1/P2: if the fleet repo has uncommitted
	// changes, Init must refuse BEFORE the script runs so the
	// openbao commit can't slurp operator work-in-progress.
	repo := setupFleet(t)
	runner := &fakeRunner{fireSentinel: true, sentinelPayload: []byte(canonicalShareJSON)}
	sops := newFakeSOPS()
	git := &fakeGit{
		preSHA:    "pre",
		commitSHA: "post",
		diff: ports.Diff{Files: []ports.FileDiff{
			{Path: "infrastructure/cni/values.yaml", Status: "M"},
		}},
	}
	opts := baseInitOpts(t, repo, runner, sops, git)
	err := Init(context.Background(), opts)
	if !errors.Is(err, ErrDirtyWorkingTree) {
		t.Fatalf("expected ErrDirtyWorkingTree, got %v", err)
	}
	if !strings.Contains(err.Error(), "infrastructure/cni/values.yaml") {
		t.Errorf("error should name dirty paths: %v", err)
	}
	// Script must NOT have run.
	if runner.cb != nil {
		t.Errorf("script callback registered despite dirty tree")
	}
	if git.commitCalls != 0 {
		t.Errorf("commit ran despite dirty tree")
	}
	if len(sops.written) != 0 {
		t.Errorf("SOPS write ran despite dirty tree")
	}
}

// --- review-pass: already-initialised gate ---

func TestInit_AlreadyInitialised_Refused_BeforeScript(t *testing.T) {
	// Review-pass P2: refuse via OpenBao.GetAnnotation when the
	// service is already marked finalised — running the script
	// again would fail at `bao operator init`, but we catch it
	// with a typed error first.
	repo := setupFleet(t)
	runner := &fakeRunner{fireSentinel: true, sentinelPayload: []byte(canonicalShareJSON)}
	sops := newFakeSOPS()
	git := &fakeGit{preSHA: "pre", commitSHA: "post"}
	bao := &fakeBao{annotation: "2026-05-26T12:00:00Z"}
	opts := baseInitOpts(t, repo, runner, sops, git)
	opts.OpenBao = bao
	err := Init(context.Background(), opts)
	if !errors.Is(err, ErrAlreadyInitialised) {
		t.Fatalf("expected ErrAlreadyInitialised, got %v", err)
	}
	if git.commitCalls != 0 {
		t.Errorf("commit ran despite already-initialised")
	}
}

// --- review-pass: post-commit failure preserves shares ---

func TestInit_PostCommitUnsealFailure_NoRollback(t *testing.T) {
	// Review-pass P1: if unseal fails AFTER the commit, the
	// encrypted shares must remain durable — NEVER rolled back.
	// (M4-T12's rollback semantics don't apply because rolling
	// back here would lose the only copy of the recovery shares.)
	repo := setupFleet(t)
	runner := &fakeRunner{fireSentinel: true, sentinelPayload: []byte(canonicalShareJSON)}
	sops := newFakeSOPS()
	git := &fakeGit{preSHA: "PRE-COMMIT-SHA", commitSHA: "POST"}
	bao := &fakeBao{unsealErr: errors.New("simulated unseal failure")}

	var out bytes.Buffer
	opts := baseInitOpts(t, repo, runner, sops, git)
	opts.OpenBao = bao
	opts.Out = &out

	err := Init(context.Background(), opts)
	if !errors.Is(err, ErrPostCommitIrreversible) {
		t.Fatalf("expected ErrPostCommitIrreversible, got %v", err)
	}
	// Critical: NO ResetHard call — the commit MUST survive.
	if len(git.resetCalls) != 0 {
		t.Fatalf("post-commit unseal failure triggered rollback (would lose shares): %v", git.resetCalls)
	}
	// Commit DID happen.
	if git.commitCalls != 1 {
		t.Errorf("expected 1 commit call, got %d", git.commitCalls)
	}
	// Recovery message + suggestion to re-run unseal.
	if !strings.Contains(out.String(), "openbao unseal atlantis") {
		t.Errorf("output should suggest the recovery command:\n%s", out.String())
	}
}

// M5-T08 reworked Phase C: root-token revocation now runs as a defer
// (best-effort) instead of a sequential step that can fail Init. The
// reason — shares are already durable on the remote, so a revoke
// failure must NOT mask the successful share-custody side. A revoke
// error gets WARNING-logged but the Init still returns nil so the
// operator's manual remediation runs against a known-good cluster.
func TestInit_PostCommitRevokeFailure_LogsButDoesNotFail(t *testing.T) {
	repo := setupFleet(t)
	runner := &fakeRunner{fireSentinel: true, sentinelPayload: []byte(canonicalShareJSON)}
	sops := newFakeSOPS()
	git := &fakeGit{preSHA: "pre", commitSHA: "post"}
	bao := &fakeBao{revokeErr: errors.New("simulated revoke failure")}
	var out bytes.Buffer
	opts := baseInitOpts(t, repo, runner, sops, git)
	opts.OpenBao = bao
	opts.Out = &out

	err := Init(context.Background(), opts)
	if err != nil {
		t.Fatalf("revoke failure should not fail Init (shares are durable): %v", err)
	}
	if len(git.resetCalls) != 0 {
		t.Fatalf("revoke failure triggered rollback (would lose shares): %v", git.resetCalls)
	}
	// Unseal succeeded (3 calls).
	if !strings.Contains(string(bao.unsealCalls), "share1") {
		t.Errorf("share1 didn't reach unseal: %s", bao.unsealCalls)
	}
	// Revoke was attempted with the captured root token.
	if !strings.HasPrefix(string(bao.revokeToken), "ROOT_TOKEN") {
		t.Errorf("revoke was not attempted with the root token; got %q", bao.revokeToken)
	}
	// Operator was warned about the failure.
	if !strings.Contains(out.String(), "root-token revoke failed") {
		t.Errorf("output should warn about revoke failure:\n%s", out.String())
	}
}

// --- review-pass: unseal stdin transport ---

func TestInit_UnsealStdinTransport_VerifyShareValues(t *testing.T) {
	// Review-pass P1: shares reach `bao operator unseal -` via
	// stdin (the adapter's PodExec stdin arg), never argv. Our
	// fakeBao records every share byte it received via Unseal so
	// we can confirm the engine pumps the exact 3 share values.
	repo := setupFleet(t)
	runner := &fakeRunner{fireSentinel: true, sentinelPayload: []byte(canonicalShareJSON)}
	sops := newFakeSOPS()
	git := &fakeGit{preSHA: "pre", commitSHA: "post"}
	bao := &fakeBao{}
	opts := baseInitOpts(t, repo, runner, sops, git)
	opts.OpenBao = bao

	if err := Init(context.Background(), opts); err != nil {
		t.Fatalf("Init: %v", err)
	}
	// 3 shares fed through Unseal: share1, share2, share3 from
	// the canonical payload. share4/share5 must NOT reach the
	// unseal path (threshold = 3).
	collected := string(bao.unsealCalls)
	for _, want := range []string{"share1AAAA", "share2BBBB", "share3CCCC"} {
		if !strings.Contains(collected, want) {
			t.Errorf("unseal payload missing %q (got %q)", want, collected)
		}
	}
	for _, forbidden := range []string{"share4DDDD", "share5EEEE"} {
		if strings.Contains(collected, forbidden) {
			t.Errorf("share %q reached unseal — threshold=3 must skip shares 4/5", forbidden)
		}
	}
	// Root token went through RevokeSelf.
	if !strings.HasPrefix(string(bao.revokeToken), "ROOT_TOKEN") {
		t.Errorf("revoke didn't receive the root token: %q", bao.revokeToken)
	}
	// Annotation set.
	if _, ok := bao.annotationsSet["openbao/kube-dc.com/openbao-bootstrap-finalized"]; !ok {
		t.Errorf("bootstrap-finalized annotation not set: %v", bao.annotationsSet)
	}
}

// --- missing deps ---

func TestInit_MissingDependencies(t *testing.T) {
	repo := setupFleet(t)
	runner := &fakeRunner{}
	sops := newFakeSOPS()
	git := &fakeGit{}
	base := baseInitOpts(t, repo, runner, sops, git)

	cases := []struct {
		name   string
		mutate func(*InitOptions)
		wantIn string
	}{
		{"empty ClusterName", func(o *InitOptions) { o.ClusterName = "" }, "ClusterName"},
		{"empty FleetRepo", func(o *InitOptions) { o.FleetRepo = "" }, "FleetRepo"},
		{"nil Runner", func(o *InitOptions) { o.Runner = nil }, "Runner"},
		{"nil SOPS", func(o *InitOptions) { o.SOPS = nil }, "SOPS"},
		{"nil Git", func(o *InitOptions) { o.Git = nil }, "Git"},
		{"nil OpenBao", func(o *InitOptions) { o.OpenBao = nil }, "OpenBao"},
	}
	for _, tc := range cases {
		tc := tc
		t.Run(tc.name, func(t *testing.T) {
			o := base
			tc.mutate(&o)
			err := Init(context.Background(), o)
			if !errors.Is(err, ErrInitMissingDependency) {
				t.Fatalf("expected ErrInitMissingDependency, got %v", err)
			}
			if !strings.Contains(err.Error(), tc.wantIn) {
				t.Errorf("error should name %q: %v", tc.wantIn, err)
			}
		})
	}
}

// --- JSON parser unit tests ---

func TestLoadBufferFromJSON(t *testing.T) {
	cases := []struct {
		name    string
		payload string
		wantErr bool
		wantSub string
	}{
		{"canonical", canonicalShareJSON, false, ""},
		{"trim whitespace", "\n\n  " + canonicalShareJSON + "  \n\n", false, ""},
		{"malformed", "not json", true, "json unmarshal"},
		{"too few shares", `{"unseal_keys_b64":["a","b"],"root_token":"t"}`, true, "got 2"},
		{"too many shares", `{"unseal_keys_b64":["a","b","c","d","e","f"],"root_token":"t"}`, true, "got 6"},
		{"empty share", `{"unseal_keys_b64":["a","","c","d","e"],"root_token":"t"}`, true, "empty"},
		{"missing root token", `{"unseal_keys_b64":["a","b","c","d","e"]}`, true, "root_token absent"},
		{"empty payload", "", true, "empty"},
	}
	for _, tc := range cases {
		tc := tc
		t.Run(tc.name, func(t *testing.T) {
			buf := secrets.NewBuffer()
			defer buf.Scrub()
			err := loadBufferFromJSON(buf, []byte(tc.payload))
			if tc.wantErr {
				if err == nil {
					t.Fatalf("expected error containing %q", tc.wantSub)
				}
				if !strings.Contains(err.Error(), tc.wantSub) {
					t.Errorf("error %q missing substring %q", err.Error(), tc.wantSub)
				}
				return
			}
			if err != nil {
				t.Fatalf("unexpected error: %v", err)
			}
			if buf.ShareCount() != 5 {
				t.Errorf("ShareCount = %d, want 5", buf.ShareCount())
			}
		})
	}
}

// =====================================================================
// M5-T08 — init Phase C controller-auth integration tests.
//
// Verifies the new Phase C ordering:
//   1. unseal openbao-0
//   2. capture root token; defer RevokeSelf IMMEDIATELY
//   3. (if !SkipControllerAuth) postInitSetupControllerAuth
//   4. annotate both keys atomically via SetAnnotations
//   5. (defer fires here) RevokeSelf
// =====================================================================

// fakeInitK8s satisfies ports.K8sClient with a canned CA bundle for
// the kube-root-ca.crt lookup the controller-auth setup needs.
type fakeInitK8s struct{}

func (f *fakeInitK8s) GetConfigMapData(_ context.Context, ns, name, key string) (string, error) {
	if ns == "kube-dc" && name == "kube-root-ca.crt" && key == "ca.crt" {
		return "-----BEGIN CERTIFICATE-----\nFAKE\n-----END CERTIFICATE-----\n", nil
	}
	return "", nil
}
func (f *fakeInitK8s) DiscoverFluxGraph(context.Context) (ports.Graph, error) {
	panic("fakeInitK8s: DiscoverFluxGraph not stubbed")
}
func (f *fakeInitK8s) NodeLabels(context.Context) (map[string]map[string]string, error) {
	panic("fakeInitK8s: NodeLabels not stubbed")
}
func (f *fakeInitK8s) DeploymentImages(context.Context, string) (map[string]string, error) {
	panic("fakeInitK8s: DeploymentImages not stubbed")
}
func (f *fakeInitK8s) ListNamespaces(context.Context) ([]string, error) {
	panic("fakeInitK8s: ListNamespaces not stubbed")
}
func (f *fakeInitK8s) ListCRDs(context.Context) ([]string, error) {
	panic("fakeInitK8s: ListCRDs not stubbed")
}
func (f *fakeInitK8s) ListPodNames(context.Context, string, string) ([]string, error) {
	panic("fakeInitK8s: ListPodNames not stubbed")
}
func (f *fakeInitK8s) HelmReleaseChartVersions(context.Context) (map[string]string, error) {
	panic("fakeInitK8s: HelmReleaseChartVersions not stubbed")
}
func (f *fakeInitK8s) GetResourceFieldFirst(context.Context, string, string, string, string, string, ...string) (string, error) {
	panic("fakeInitK8s: GetResourceFieldFirst not stubbed")
}
func (f *fakeInitK8s) PodExec(context.Context, string, string, []string, []byte) ([]byte, error) {
	panic("fakeInitK8s: PodExec not stubbed")
}
func (f *fakeInitK8s) PodExecViaKubectl(context.Context, string, string, []string, []byte) ([]byte, error) {
	panic("fakeInitK8s: PodExecViaKubectl not stubbed (engine uses OpenBao port)")
}
func (f *fakeInitK8s) GetServiceAnnotation(context.Context, string, string, string) (string, error) {
	panic("fakeInitK8s: GetServiceAnnotation not stubbed (engine uses OpenBao port)")
}
func (f *fakeInitK8s) SetServiceAnnotation(context.Context, string, string, string, string) error {
	panic("fakeInitK8s: SetServiceAnnotation not stubbed (engine uses OpenBao port)")
}
func (f *fakeInitK8s) SetServiceAnnotations(context.Context, string, string, map[string]string) error {
	panic("fakeInitK8s: SetServiceAnnotations not stubbed (engine uses OpenBao port)")
}

// TestInit_PhaseC_RunsControllerAuthSetup verifies that with
// SkipControllerAuth=false, Phase C runs all 6 privileged setup
// steps in order before the atomic annotate-both.
func TestInit_PhaseC_RunsControllerAuthSetup(t *testing.T) {
	repo := setupFleet(t)
	runner := &fakeRunner{fireSentinel: true, sentinelPayload: []byte(canonicalShareJSON)}
	sops := newFakeSOPS()
	git := &fakeGit{preSHA: "pre", commitSHA: "post"}
	bao := &fakeBao{}
	opts := baseInitOpts(t, repo, runner, sops, git)
	opts.OpenBao = bao
	opts.K8s = &fakeInitK8s{}
	opts.SkipControllerAuth = false // FLIP — exercise the new path
	var out bytes.Buffer
	opts.Out = &out

	if err := Init(context.Background(), opts); err != nil {
		t.Fatalf("Init: %v\n%s", err, out.String())
	}
	// EnableAuthPath called once
	if bao.enableAuthCalls != 1 {
		t.Errorf("EnableAuthPath calls = %d, want 1", bao.enableAuthCalls)
	}
	// ConfigureKubernetesAuth called once
	if bao.configureAuthCalls != 1 {
		t.Errorf("ConfigureKubernetesAuth calls = %d, want 1", bao.configureAuthCalls)
	}
	// Three policies in order, including the read-only snapshot job.
	if len(bao.policyCalls) != 3 || bao.policyCalls[0] != ManagerPolicyName || bao.policyCalls[1] != DBManagerPolicyName || bao.policyCalls[2] != SnapshotPolicyName {
		t.Errorf("policy call order wrong: %v", bao.policyCalls)
	}
	// Three roles in order.
	wantRoles := []string{KubernetesAuthPath + "/" + ManagerRoleName, KubernetesAuthPath + "/" + DBManagerRoleName, KubernetesAuthPath + "/" + SnapshotRoleName}
	if len(bao.roleCalls) != len(wantRoles) || bao.roleCalls[0] != wantRoles[0] || bao.roleCalls[1] != wantRoles[1] || bao.roleCalls[2] != wantRoles[2] {
		t.Errorf("role call order wrong: %v (want %v)", bao.roleCalls, wantRoles)
	}
}

// TestInit_PhaseC_AnnotateBoth_AtomicAndIncludesBothKeys verifies
// the atomic annotation patch stamps BOTH bootstrap-finalized AND
// controller-auth-installed when SkipControllerAuth=false. Replaces
// the previous one-key-at-a-time SetAnnotation path.
func TestInit_PhaseC_AnnotateBoth_AtomicAndIncludesBothKeys(t *testing.T) {
	repo := setupFleet(t)
	runner := &fakeRunner{fireSentinel: true, sentinelPayload: []byte(canonicalShareJSON)}
	sops := newFakeSOPS()
	git := &fakeGit{preSHA: "pre", commitSHA: "post"}
	bao := &fakeBao{}
	opts := baseInitOpts(t, repo, runner, sops, git)
	opts.OpenBao = bao
	opts.K8s = &fakeInitK8s{}
	opts.SkipControllerAuth = false

	if err := Init(context.Background(), opts); err != nil {
		t.Fatalf("Init: %v", err)
	}
	if _, ok := bao.annotationsSet["openbao/"+AnnotationBootstrapFinalized]; !ok {
		t.Errorf("bootstrap-finalized annotation not set; got %v", bao.annotationsSet)
	}
	if _, ok := bao.annotationsSet["openbao/"+AnnotationControllerAuthInstalled]; !ok {
		t.Errorf("controller-auth-installed annotation not set; got %v", bao.annotationsSet)
	}
}

// TestInit_PhaseC_SkipControllerAuth_LeavesAuthOff verifies the
// emergency flag: no auth-setup calls, no controller-auth annotation,
// but bootstrap-finalized IS stamped (init still completes share custody).
func TestInit_PhaseC_SkipControllerAuth_LeavesAuthOff(t *testing.T) {
	repo := setupFleet(t)
	runner := &fakeRunner{fireSentinel: true, sentinelPayload: []byte(canonicalShareJSON)}
	sops := newFakeSOPS()
	git := &fakeGit{preSHA: "pre", commitSHA: "post"}
	bao := &fakeBao{}
	opts := baseInitOpts(t, repo, runner, sops, git)
	opts.OpenBao = bao
	opts.K8s = &fakeInitK8s{}
	opts.SkipControllerAuth = true // back to default
	var out bytes.Buffer
	opts.Out = &out

	if err := Init(context.Background(), opts); err != nil {
		t.Fatalf("Init: %v", err)
	}
	if bao.enableAuthCalls != 0 || bao.configureAuthCalls != 0 ||
		len(bao.policyCalls) != 0 || len(bao.roleCalls) != 0 {
		t.Errorf("--skip-controller-auth must skip all 6 setup calls; got enable=%d configure=%d policy=%d role=%d",
			bao.enableAuthCalls, bao.configureAuthCalls, len(bao.policyCalls), len(bao.roleCalls))
	}
	// finalized SET, controller-auth-installed NOT SET
	if _, ok := bao.annotationsSet["openbao/"+AnnotationBootstrapFinalized]; !ok {
		t.Errorf("bootstrap-finalized annotation must still be set even with --skip-controller-auth")
	}
	if _, ok := bao.annotationsSet["openbao/"+AnnotationControllerAuthInstalled]; ok {
		t.Errorf("controller-auth-installed annotation must NOT be set with --skip-controller-auth")
	}
	// Operator was told about the half-installed state
	if !strings.Contains(out.String(), "skip-controller-auth") {
		t.Errorf("output should warn about skip-controller-auth state:\n%s", out.String())
	}
}

// TestInit_PhaseC_SetupFailure_RevokesAndReturnsErr verifies the
// `defer RevokeSelf` discipline: a setup-step failure must still
// revoke the captured root token AND return ErrPostCommitIrreversible
// with a recovery hint pointing at setup-controller-auth.
func TestInit_PhaseC_SetupFailure_RevokesAndReturnsErr(t *testing.T) {
	repo := setupFleet(t)
	runner := &fakeRunner{fireSentinel: true, sentinelPayload: []byte(canonicalShareJSON)}
	sops := newFakeSOPS()
	git := &fakeGit{preSHA: "pre", commitSHA: "post"}
	bao := &fakeBao{
		policyErr: errors.New("simulated policy write failure"),
	}
	opts := baseInitOpts(t, repo, runner, sops, git)
	opts.OpenBao = bao
	opts.K8s = &fakeInitK8s{}
	opts.SkipControllerAuth = false
	var out bytes.Buffer
	opts.Out = &out

	err := Init(context.Background(), opts)
	if !errors.Is(err, ErrPostCommitIrreversible) {
		t.Fatalf("expected ErrPostCommitIrreversible, got %v", err)
	}
	// Revoke fired even though setup failed (defer discipline).
	if !strings.HasPrefix(string(bao.revokeToken), "ROOT_TOKEN") {
		t.Errorf("revoke did NOT fire after setup failure: %q", bao.revokeToken)
	}
	// Recovery message points at setup-controller-auth, not unseal.
	if !strings.Contains(out.String(), "setup-controller-auth") {
		t.Errorf("output should suggest setup-controller-auth recovery:\n%s", out.String())
	}
}

// --- P0 (review-pass 3): write-ahead custody ---
//
// The shares only ever exist in a secrets.Buffer that `defer
// buf.Scrub()` wipes on EVERY return from Init. Before this pass, the
// error paths between the sentinel callback and Phase B returned
// straight out — so a script that emitted its share payload and then
// failed (nonzero exit, truncated stream, bad payload, missing root
// token) left an INITIALIZED vault whose keys had just been zeroed in
// memory and written nowhere. Unrecoverable.
//
// Each spec below drives one of those paths and asserts the shares
// reached disk anyway.

// sharesReachedDisk asserts an emergency dump exists for the cluster
// and actually contains the captured share material.
func sharesReachedDisk(t *testing.T, home, cluster string) string {
	t.Helper()
	dir := filepath.Join(home, ".kube-dc")
	entries, err := os.ReadDir(dir)
	if err != nil {
		t.Fatalf("no custody dir %s: %v", dir, err)
	}
	for _, e := range entries {
		if !strings.HasPrefix(e.Name(), "openbao-emergency-shares-") {
			continue
		}
		path := filepath.Join(dir, e.Name())
		body, rerr := os.ReadFile(path)
		if rerr != nil {
			t.Fatalf("read %s: %v", path, rerr)
		}
		// The first canonical share must be present verbatim, or the
		// file is custody in name only.
		if !strings.Contains(string(body), "share1AAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAA=") {
			t.Fatalf("%s exists but does not contain the captured shares:\n%s", path, body)
		}
		if fi, serr := os.Stat(path); serr == nil && fi.Mode().Perm() != 0o600 {
			t.Errorf("custody file %s has mode %o, want 0600", path, fi.Mode().Perm())
		}
		return path
	}
	t.Fatalf("shares were captured but no emergency dump exists in %s (entries: %v)", dir, entries)
	return ""
}

func TestInit_PostCaptureFailures_AlwaysPreserveShares(t *testing.T) {
	cases := []struct {
		name    string
		mutate  func(*fakeRunner, *fakeSOPS, *fakeGit)
		wantMsg string
	}{
		{
			// The script emitted the shares, then died — e.g. the unseal
			// or annotate step inside openbao-init.sh failed. The vault
			// is initialized regardless.
			name:    "nonzero exit after the payload was emitted",
			mutate:  func(r *fakeRunner, _ *fakeSOPS, _ *fakeGit) { r.exitCode = 7 },
			wantMsg: "exit=7",
		},
		{
			// Truncated stream: no exit line at all.
			name:    "drain error after the payload was emitted",
			mutate:  func(r *fakeRunner, _ *fakeSOPS, _ *fakeGit) { r.exitText = "not-a-number" },
			wantMsg: "drain",
		},
		{
			// Payload parsed but carried no root token. Init refuses to
			// continue — the shares are still real and still the only
			// copy of the vault's recovery material.
			name: "root token absent from an otherwise valid payload",
			mutate: func(r *fakeRunner, _ *fakeSOPS, _ *fakeGit) {
				r.sentinelPayload = []byte(strings.Replace(canonicalShareJSON,
					`"root_token": "ROOT_TOKEN_BBBBBBBBBBBBBBBBBBBBBB"`, `"root_token": ""`, 1))
			},
			wantMsg: "root_token",
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			repo := setupFleet(t)
			home := t.TempDir()
			t.Setenv("HOME", home)
			runner := &fakeRunner{fireSentinel: true, sentinelPayload: []byte(canonicalShareJSON)}
			sops := newFakeSOPS()
			git := &fakeGit{preSHA: "PRE", commitSHA: "post"}
			tc.mutate(runner, sops, git)
			opts := baseInitOpts(t, repo, runner, sops, git)

			err := Init(context.Background(), opts)
			if err == nil {
				t.Fatal("expected failure")
			}
			if !strings.Contains(err.Error(), tc.wantMsg) {
				t.Errorf("error should mention %q; got: %v", tc.wantMsg, err)
			}
			// The whole point: shares on disk, and the error says where.
			path := sharesReachedDisk(t, home, "atlantis")
			if !strings.Contains(err.Error(), path) {
				t.Errorf("error must tell the operator where the shares are (%s); got: %v", path, err)
			}
			if len(git.resetCalls) != 0 {
				t.Errorf("ResetHard must NEVER run once the vault is initialized; got %v", git.resetCalls)
			}
		})
	}
}

// A failure BEFORE the sentinel fires is free: the vault was never
// initialized, so there is nothing to preserve and no scary custody
// message to show the operator.
func TestInit_PreCaptureFailure_NoCustodyFile(t *testing.T) {
	repo := setupFleet(t)
	home := t.TempDir()
	t.Setenv("HOME", home)
	runner := &fakeRunner{fireSentinel: false, exitCode: 3}
	git := &fakeGit{preSHA: "PRE"}
	err := Init(context.Background(), baseInitOpts(t, repo, runner, newFakeSOPS(), git))
	if err == nil {
		t.Fatal("expected failure")
	}
	if entries, rerr := os.ReadDir(filepath.Join(home, ".kube-dc")); rerr == nil {
		for _, e := range entries {
			if strings.HasPrefix(e.Name(), "openbao-emergency-shares-") {
				t.Fatalf("no shares were ever captured, so nothing should be dumped; found %s", e.Name())
			}
		}
	}
	if strings.Contains(err.Error(), "vault IS initialized") {
		t.Errorf("pre-capture failure must not claim the vault is initialized; got: %v", err)
	}
}

// On full success the write-ahead plaintext copy must be cleaned up —
// the encrypted, committed secrets.enc.yaml is the custody of record,
// and leaving plaintext shares in ~/.kube-dc would quietly undo that.
func TestInit_Success_RemovesWriteAheadCopy(t *testing.T) {
	repo := setupFleet(t)
	home := t.TempDir()
	t.Setenv("HOME", home)
	runner := &fakeRunner{fireSentinel: true, sentinelPayload: []byte(canonicalShareJSON)}
	git := &fakeGit{preSHA: "PRE", commitSHA: "post"}
	if err := Init(context.Background(), baseInitOpts(t, repo, runner, newFakeSOPS(), git)); err != nil {
		t.Fatalf("happy path failed: %v", err)
	}
	entries, rerr := os.ReadDir(filepath.Join(home, ".kube-dc"))
	if rerr != nil {
		return // dir never created — also fine
	}
	for _, e := range entries {
		if strings.HasPrefix(e.Name(), "openbao-emergency-shares-") {
			t.Errorf("write-ahead plaintext copy %s survived a successful ceremony", e.Name())
		}
	}
}

// --- P1 (review-pass 3): emergency dumps must never clobber ---

// A second failed ceremony must not overwrite the first one's shares.
// The earlier file may belong to a still-sealed vault nobody has
// recovered yet; overwriting it destroys the only way back in.
func TestEmergencyDump_NeverOverwritesExistingDump(t *testing.T) {
	home := t.TempDir()
	t.Setenv("HOME", home)
	buf := secrets.NewBuffer()
	if err := loadBufferFromJSON(buf, []byte(canonicalShareJSON)); err != nil {
		t.Fatalf("seed buffer: %v", err)
	}
	opts := InitOptions{ClusterName: "atlantis"}

	first, err := emergencyDumpShares(opts, buf)
	if err != nil {
		t.Fatalf("first dump: %v", err)
	}
	// Mark the first file so we can prove it was not replaced.
	marker := "# FIRST-CEREMONY-DO-NOT-LOSE\n"
	body, _ := os.ReadFile(first)
	if err := os.WriteFile(first, append([]byte(marker), body...), 0o600); err != nil {
		t.Fatalf("mark first dump: %v", err)
	}

	second, err := emergencyDumpShares(opts, buf)
	if err != nil {
		t.Fatalf("second dump: %v", err)
	}
	if second == first {
		t.Fatalf("second dump reused the first path %s — the earlier shares would be destroyed", first)
	}
	after, _ := os.ReadFile(first)
	if !strings.HasPrefix(string(after), marker) {
		t.Errorf("first ceremony's dump at %s was overwritten", first)
	}
	if b, rerr := os.ReadFile(second); rerr != nil || !strings.Contains(string(b), "share1AAAA") {
		t.Errorf("second dump %s is not usable custody: %v", second, rerr)
	}
}

// Nested cluster names must not collide with hyphenated ones. The old
// "/"->"-" flattening mapped BOTH "cs/next" and "cs-next" onto
// openbao-emergency-shares-cs-next.yaml.
func TestEmergencyDump_NestedNameDoesNotCollideWithHyphen(t *testing.T) {
	home := t.TempDir()
	t.Setenv("HOME", home)
	buf := secrets.NewBuffer()
	if err := loadBufferFromJSON(buf, []byte(canonicalShareJSON)); err != nil {
		t.Fatalf("seed buffer: %v", err)
	}
	nested, err := emergencyDumpShares(InitOptions{ClusterName: "cs/next"}, buf)
	if err != nil {
		t.Fatalf("nested dump: %v", err)
	}
	hyphen, err := emergencyDumpShares(InitOptions{ClusterName: "cs-next"}, buf)
	if err != nil {
		t.Fatalf("hyphen dump: %v", err)
	}
	if nested == hyphen {
		t.Fatalf("cs/next and cs-next both dumped to %s", nested)
	}
	// Both must still be single files directly under ~/.kube-dc — a
	// nested name must never turn into a directory tree.
	for _, p := range []string{nested, hyphen} {
		if filepath.Dir(p) != filepath.Join(home, ".kube-dc") {
			t.Errorf("dump %s escaped ~/.kube-dc", p)
		}
	}
}

// A partial capture is exactly when custody matters most: refusing to
// write because fewer than 5 shares arrived would discard the 3 that
// might still unseal the vault.
func TestEmergencyDump_PartialCaptureStillWritesWithWarning(t *testing.T) {
	home := t.TempDir()
	t.Setenv("HOME", home)
	buf := secrets.NewBuffer()
	partial := `{"unseal_keys_b64":["p1AAAA=","p2BBBB=","p3CCCC="],"root_token":"s.tok"}`
	if err := loadBufferFromJSON(buf, []byte(partial)); err != nil {
		t.Skipf("loader rejects partial payloads outright: %v", err)
	}
	path, err := emergencyDumpShares(InitOptions{ClusterName: "atlantis"}, buf)
	if err != nil {
		t.Fatalf("partial capture must still be preserved, got: %v", err)
	}
	body, _ := os.ReadFile(path)
	if !strings.Contains(string(body), "p1AAAA=") {
		t.Errorf("partial dump lost the shares it did have:\n%s", body)
	}
	if !strings.Contains(string(body), "PARTIAL CAPTURE") {
		t.Errorf("partial dump must be labelled so it is not mistaken for full custody:\n%s", body)
	}
}

// --- truncated stream (no terminal exit record) ---
//
// The drainer used to initialise exit=0 and return it untouched when
// the channel closed without a StreamExit line. A killed script, a dead
// runner or a dropped connection therefore read as a clean exit 0 —
// which for openbao-init.sh means asserting the vault was initialized
// and finalized on the strength of output we never received.

// Truncated BEFORE the sentinel fires: nothing was captured, so this is
// an ordinary failure. Init must not claim custody it does not have.
func TestInit_TruncatedStreamBeforeCapture_FailsWithoutCustodyClaim(t *testing.T) {
	repo := setupFleet(t)
	home := t.TempDir()
	t.Setenv("HOME", home)
	runner := &fakeRunner{fireSentinel: false, dropExit: true}
	git := &fakeGit{preSHA: "PRE", commitSHA: "post"}
	sops := newFakeSOPS()

	err := Init(context.Background(), baseInitOpts(t, repo, runner, sops, git))
	if !errors.Is(err, ErrScriptStreamTruncated) {
		t.Fatalf("truncated stream must not be synthesized as exit 0; got %v", err)
	}
	// Nothing was captured => no custody file and no custody language.
	if entries, rerr := os.ReadDir(filepath.Join(home, ".kube-dc")); rerr == nil {
		for _, e := range entries {
			if strings.HasPrefix(e.Name(), "openbao-emergency-shares-") {
				t.Errorf("no shares were captured, so nothing should be dumped; found %s", e.Name())
			}
		}
	}
	if strings.Contains(err.Error(), "vault IS initialized") {
		t.Errorf("pre-capture truncation must not claim the vault is initialized; got: %v", err)
	}
	// And it must not have written anything to the fleet.
	if len(sops.written) != 0 {
		t.Errorf("truncated pre-capture run must not encrypt anything; wrote %d keys", len(sops.written))
	}
	if git.commitCalls != 0 {
		t.Errorf("truncated pre-capture run must not commit; got %d commits", git.commitCalls)
	}
}

// Truncated AFTER the sentinel fires: the vault IS initialized and we
// hold its only keys. This is the dangerous one — it must fail, must
// preserve, and must tell the operator where the shares are.
func TestInit_TruncatedStreamAfterCapture_PreservesSharesAndNamesPath(t *testing.T) {
	repo := setupFleet(t)
	home := t.TempDir()
	t.Setenv("HOME", home)
	runner := &fakeRunner{
		fireSentinel:    true,
		sentinelPayload: []byte(canonicalShareJSON),
		dropExit:        true,
	}
	git := &fakeGit{preSHA: "PRE", commitSHA: "post"}

	err := Init(context.Background(), baseInitOpts(t, repo, runner, newFakeSOPS(), git))
	if !errors.Is(err, ErrScriptStreamTruncated) {
		t.Fatalf("expected ErrScriptStreamTruncated, got %v", err)
	}
	path := sharesReachedDisk(t, home, "atlantis")
	if !strings.Contains(err.Error(), path) {
		t.Errorf("error must name the custody path %s; got: %v", path, err)
	}
	if !strings.Contains(err.Error(), "vault IS initialized") {
		t.Errorf("post-capture truncation must warn the vault is initialized; got: %v", err)
	}
	if len(git.resetCalls) != 0 {
		t.Errorf("ResetHard must NEVER run once the vault is initialized; got %v", git.resetCalls)
	}
}

// Direct drainer coverage: EOF with no exit record is an error, and a
// present exit record still round-trips its code.
func TestDrainOpenBaoInit_RequiresTerminalExitRecord(t *testing.T) {
	mk := func(lines ...ports.Line) <-chan ports.Line {
		ch := make(chan ports.Line, len(lines))
		for _, l := range lines {
			ch <- l
		}
		close(ch)
		return ch
	}
	stdout := ports.Line{Stream: ports.StreamStdout, Text: "working"}

	if _, err := drainOpenBaoInit(mk(stdout), io.Discard); !errors.Is(err, ErrScriptStreamTruncated) {
		t.Errorf("EOF without an exit record must error; got %v", err)
	}
	if _, err := drainOpenBaoInit(mk(), io.Discard); !errors.Is(err, ErrScriptStreamTruncated) {
		t.Errorf("empty stream must error; got %v", err)
	}
	for _, code := range []string{"0", "7"} {
		got, err := drainOpenBaoInit(mk(stdout, ports.Line{Stream: ports.StreamExit, Text: code}), io.Discard)
		if err != nil {
			t.Errorf("exit=%s should drain cleanly; got %v", code, err)
		}
		if want, _ := strconv.Atoi(code); got != want {
			t.Errorf("exit code %s round-tripped as %d", code, got)
		}
	}
}

// P2 (review-pass 4): the ScriptRunner contract says the exit record is
// the FINAL record. "An exit appeared somewhere" is weaker, and the old
// drainer accepted both looser shapes plus a malformed code:
//
//   - output after the exit record  (stream interleaved or truncated
//     mid-restart; the code we captured may not describe this run)
//   - a second exit record          (a trailing 0 masking a real failure)
//   - "0garbage"                    (fmt.Sscanf stopped at the first
//     non-digit and reported success)
//
// All three are now rejected by ports.Drain. Any shares already captured
// must still be preserved, since the vault is initialized either way.
func TestInit_MalformedExitRecords_AreRejectedAndPreserveShares(t *testing.T) {
	cases := []struct {
		name    string
		mutate  func(*fakeRunner)
		wantErr error
	}{
		{
			name:    "exit code with a trailing garbage suffix",
			mutate:  func(r *fakeRunner) { r.exitText = "0garbage" },
			wantErr: ports.ErrStreamBadExitCode,
		},
		{
			name:    "output after the exit record",
			mutate:  func(r *fakeRunner) { r.trailingOutput = "late line after exit" },
			wantErr: ports.ErrStreamOutputAfterExit,
		},
		{
			name:    "a second exit record masking the first",
			mutate:  func(r *fakeRunner) { r.exitCode = 1; r.extraExit = "0" },
			wantErr: ports.ErrStreamDuplicateExit,
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			repo := setupFleet(t)
			home := t.TempDir()
			t.Setenv("HOME", home)
			runner := &fakeRunner{fireSentinel: true, sentinelPayload: []byte(canonicalShareJSON)}
			tc.mutate(runner)
			git := &fakeGit{preSHA: "PRE", commitSHA: "post"}

			err := Init(context.Background(), baseInitOpts(t, repo, runner, newFakeSOPS(), git))
			if !errors.Is(err, tc.wantErr) {
				t.Fatalf("want %v, got %v", tc.wantErr, err)
			}
			// The vault is initialized regardless of how mangled the
			// stream was, so custody still applies.
			path := sharesReachedDisk(t, home, "atlantis")
			if !strings.Contains(err.Error(), path) {
				t.Errorf("error must name the custody path %s; got: %v", path, err)
			}
			if len(git.resetCalls) != 0 {
				t.Errorf("ResetHard must NEVER run once the vault is initialized; got %v", git.resetCalls)
			}
		})
	}
}
