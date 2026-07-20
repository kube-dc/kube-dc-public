package git

import (
	"context"
	"net"
	"net/url"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"

	gogit "github.com/go-git/go-git/v5"
	"github.com/go-git/go-git/v5/config"
	"golang.org/x/crypto/ssh"
	"golang.org/x/crypto/ssh/agent"
)

// End-to-end SSH coverage for Clone / Pull / Push against a REAL sshd.
//
// The unit tests in client_test.go assert how the adapter *chooses* auth
// (transport classification, direction, signer composition). They cannot
// prove the choice actually authenticates, because nothing dials. These
// specs close that gap: a throwaway sshd + ssh-agent, two bare repos,
// and the adapter's own Clone/Pull/Push doing real network I/O.
//
// Skipped unless KUBEDC_SSH_E2E is set, because they need a running
// sshd. hack/ssh-e2e-harness.sh stands the whole thing up:
//
//	eval "$(hack/ssh-e2e-harness.sh up)"
//	go -C cli test -count=1 -v -run TestSSHE2E ./internal/bootstrap/adapters/git/
//	hack/ssh-e2e-harness.sh down
//
// Nothing here touches a fleet repo, a real remote, or a cluster.
func sshE2EEnv(t *testing.T) (repoA, repoB string) {
	t.Helper()
	if os.Getenv("KUBEDC_SSH_E2E") == "" {
		t.Skip("set KUBEDC_SSH_E2E=1 (needs a throwaway sshd) to run SSH end-to-end specs")
	}
	repoA, repoB = os.Getenv("KUBEDC_SSH_E2E_REPO_A"), os.Getenv("KUBEDC_SSH_E2E_REPO_B")
	if repoA == "" || repoB == "" {
		t.Fatal("KUBEDC_SSH_E2E_REPO_A and _REPO_B must be set")
	}
	// HOME drives go-git's known_hosts lookup AND the default key-file
	// signers. Pointing it at the harness dir keeps the developer's real
	// ~/.ssh out of the test entirely.
	if h := os.Getenv("KUBEDC_SSH_E2E_HOME"); h != "" {
		t.Setenv("HOME", h)
	}
	return repoA, repoB
}

// uniqueContent keeps every write distinct across runs. The bare repos
// persist between invocations of the harness, so writing fixed bytes
// makes a re-run a no-op ("nothing to commit") and the spec fails for a
// reason unrelated to what it is testing.
func uniqueContent(tag string) []byte {
	return []byte(tag + " " + time.Now().Format(time.RFC3339Nano) + "\n")
}

func headOf(t *testing.T, gitDir string) string {
	t.Helper()
	out, err := exec.Command("git", "--git-dir="+gitDir, "rev-parse", "main").Output()
	if err != nil {
		t.Fatalf("rev-parse %s: %v", gitDir, err)
	}
	return strings.TrimSpace(string(out))
}

// bareDirOf returns the server-side path of a harness repo so a spec can
// inspect it directly.
//
// The harness exports these paths explicitly rather than having the test
// carve them out of the ssh:// URL. Deriving them by string-matching
// "127.0.0.1:2222" silently hardcoded the default port, so the harness's
// documented SSH_E2E_PORT knob authenticated and cloned fine and then
// died on "unexpected harness URL".
func bareDirOf(t *testing.T, sshURL string) string {
	t.Helper()
	for _, env := range []struct{ url, bare string }{
		{os.Getenv("KUBEDC_SSH_E2E_REPO_A"), os.Getenv("KUBEDC_SSH_E2E_BARE_A")},
		{os.Getenv("KUBEDC_SSH_E2E_REPO_B"), os.Getenv("KUBEDC_SSH_E2E_BARE_B")},
	} {
		if env.url != "" && env.url == sshURL && env.bare != "" {
			return env.bare
		}
	}
	// Fall back to parsing, so the specs still work against a harness
	// that predates the exported paths. url.Parse handles any host:port.
	u, err := url.Parse(sshURL)
	if err != nil {
		t.Fatalf("cannot derive a bare path from %q: %v", sshURL, err)
	}
	if u.Path == "" {
		t.Fatalf("no path component in harness URL %q", sshURL)
	}
	return u.Path
}

// sshMode reports which credential source the harness made usable, so a
// spec can assert the handshake was carried by the source under test
// rather than by a second one that happened to also be present.
func sshMode() string {
	if m := os.Getenv("KUBEDC_SSH_E2E_MODE"); m != "" {
		return m
	}
	return "both"
}

// Clone over ssh:// must authenticate via the agent and produce a
// working tree. Token is empty — an SSH remote must never be handed
// HTTP BasicAuth (the original P1 bug).
func TestSSHE2E_CloneOverSSH(t *testing.T) {
	repoA, _ := sshE2EEnv(t)
	dir := t.TempDir()
	if err := New().Clone(context.Background(), repoA, dir, ""); err != nil {
		t.Fatalf("clone over ssh failed: %v", err)
	}
	if _, err := os.Stat(filepath.Join(dir, "file.txt")); err != nil {
		t.Fatalf("clone produced no working tree: %v", err)
	}
}

// A userless ssh:// URL must fall back to the current OS user rather
// than being forced to "git" (the P2 bug). The harness sshd only
// accepts the invoking user, so a wrong fallback fails to authenticate.
func TestSSHE2E_CloneUserlessSSHURL(t *testing.T) {
	repoA, _ := sshE2EEnv(t)
	userless := repoA
	if at := strings.Index(userless, "@"); at >= 0 {
		userless = "ssh://" + userless[at+1:]
	}
	if strings.Contains(userless, "@") {
		t.Fatalf("failed to strip user from %q", repoA)
	}
	dir := t.TempDir()
	if err := New().Clone(context.Background(), userless, dir, ""); err != nil {
		t.Fatalf("clone of userless URL %q failed (forced to the wrong user?): %v", userless, err)
	}
}

// Pull must fetch new upstream commits over SSH.
func TestSSHE2E_PullOverSSH(t *testing.T) {
	repoA, _ := sshE2EEnv(t)
	dir := t.TempDir()
	c := New()
	if err := c.Clone(context.Background(), repoA, dir, ""); err != nil {
		t.Fatalf("clone: %v", err)
	}
	before := headOf(t, filepath.Join(dir, ".git"))

	// Advance the server side out-of-band.
	pushNewCommitTo(t, bareDirOf(t, repoA), "upstream change")

	if err := c.Pull(context.Background(), dir, ""); err != nil {
		t.Fatalf("pull over ssh failed: %v", err)
	}
	if after := headOf(t, filepath.Join(dir, ".git")); after == before {
		t.Errorf("pull did not advance HEAD (still %s)", before)
	}
}

// Push must send a local commit over SSH and land it on the remote.
func TestSSHE2E_PushOverSSH(t *testing.T) {
	repoA, _ := sshE2EEnv(t)
	dir := t.TempDir()
	c := NewWithIdentity("Test", "t@example.invalid")
	if err := c.Clone(context.Background(), repoA, dir, ""); err != nil {
		t.Fatalf("clone: %v", err)
	}
	if err := os.WriteFile(filepath.Join(dir, "pushed.txt"), uniqueContent("from push test"), 0o644); err != nil {
		t.Fatal(err)
	}
	sha, err := c.CommitAndPush(context.Background(), dir, "e2e push", "")
	if err != nil {
		t.Fatalf("CommitAndPush over ssh failed: %v", err)
	}
	if got := headOf(t, bareDirOf(t, repoA)); got != sha {
		t.Errorf("remote main = %s, want the pushed commit %s", got, sha)
	}
}

// THE directional spec. A remote with two URLs (fetch=A, push=B) must
// read from A and write to B. This is the wiring the unit tests can only
// assert indirectly: here a wrong choice actually writes to the wrong
// server, which the assertions catch by inspecting both bare repos.
func TestSSHE2E_MixedFetchPushURLs(t *testing.T) {
	repoA, repoB := sshE2EEnv(t)
	// The other specs in this file advance A, so A and B drift apart
	// and a push to B would fail as non-fast-forward for reasons that
	// have nothing to do with URL direction. Level them first, so a
	// failure here can only mean the direction was chosen wrongly.
	syncBare(t, bareDirOf(t, repoA), bareDirOf(t, repoB))
	dir := t.TempDir()
	c := NewWithIdentity("Test", "t@example.invalid")
	if err := c.Clone(context.Background(), repoA, dir, ""); err != nil {
		t.Fatalf("clone: %v", err)
	}

	// Rewrite origin: fetch from A, push to B.
	repo, err := gogit.PlainOpen(dir)
	if err != nil {
		t.Fatal(err)
	}
	if err := repo.DeleteRemote("origin"); err != nil {
		t.Fatal(err)
	}
	if _, err := repo.CreateRemote(&config.RemoteConfig{
		Name: "origin",
		URLs: []string{repoA, repoB}, // [0]=fetch, [last]=push
	}); err != nil {
		t.Fatal(err)
	}

	beforeA := headOf(t, bareDirOf(t, repoA))
	beforeB := headOf(t, bareDirOf(t, repoB))

	if err := os.WriteFile(filepath.Join(dir, "directional.txt"), uniqueContent("push me to B"), 0o644); err != nil {
		t.Fatal(err)
	}
	sha, err := c.CommitAndPush(context.Background(), dir, "directional push", "")
	if err != nil {
		t.Fatalf("push to the push-URL failed: %v", err)
	}

	if got := headOf(t, bareDirOf(t, repoB)); got != sha {
		t.Errorf("push target B = %s, want %s — push used the wrong URL", got, sha)
	}
	if got := headOf(t, bareDirOf(t, repoA)); got != beforeA {
		t.Errorf("fetch-only remote A moved to %s (was %s) — push wrote to the FETCH url", got, beforeA)
	}
	if beforeB == sha {
		t.Fatal("harness error: B already had the commit before the push")
	}

	// And the fetch direction still reads from A. This needs a FRESH
	// clone: `dir` has just pushed a commit to B that A does not have,
	// so pulling A into it is a genuine divergence and would fail for
	// reasons unrelated to URL direction.
	dir2 := t.TempDir()
	if err := c.Clone(context.Background(), repoA, dir2, ""); err != nil {
		t.Fatalf("clone for the fetch-direction check: %v", err)
	}
	repo2, err := gogit.PlainOpen(dir2)
	if err != nil {
		t.Fatal(err)
	}
	if err := repo2.DeleteRemote("origin"); err != nil {
		t.Fatal(err)
	}
	if _, err := repo2.CreateRemote(&config.RemoteConfig{
		Name: "origin",
		URLs: []string{repoA, repoB},
	}); err != nil {
		t.Fatal(err)
	}
	beforePull := headOf(t, filepath.Join(dir2, ".git"))

	// Advance A only. B is deliberately left behind, so a pull that
	// wrongly read the push URL would find nothing new.
	pushNewCommitTo(t, bareDirOf(t, repoA), "A-side change")
	wantA := headOf(t, bareDirOf(t, repoA))

	if err := c.Pull(context.Background(), dir2, ""); err != nil {
		t.Fatalf("pull from the fetch-URL failed: %v", err)
	}
	afterPull := headOf(t, filepath.Join(dir2, ".git"))
	if afterPull == beforePull {
		t.Error("pull did not advance — fetch never reached A")
	}
	if afterPull != wantA {
		t.Errorf("pull landed on %s, want A's head %s — fetch used the wrong URL", afterPull, wantA)
	}
}

// pushNewCommitTo advances a bare repo's main by one commit, using the
// plain git CLI over the local filesystem (no SSH) so the helper never
// masks a transport failure in the code under test.
func pushNewCommitTo(t *testing.T, bareDir, msg string) {
	t.Helper()
	w := t.TempDir()
	run := func(args ...string) {
		t.Helper()
		cmd := exec.Command("git", args...)
		cmd.Dir = w
		if out, err := cmd.CombinedOutput(); err != nil {
			t.Fatalf("git %s: %v\n%s", strings.Join(args, " "), err, out)
		}
	}
	run("clone", bareDir, ".")
	run("config", "user.email", "t@example.invalid")
	run("config", "user.name", "Test")
	if err := os.WriteFile(filepath.Join(w, "upstream.txt"), uniqueContent(msg), 0o644); err != nil {
		t.Fatal(err)
	}
	run("add", ".")
	run("commit", "-m", msg)
	run("push", "origin", "main")
}

// syncBare force-updates dst's main to match src's, over the local
// filesystem (never SSH), so test ordering cannot leave the two bare
// repos diverged and turn a directional assertion into a
// non-fast-forward error.
func syncBare(t *testing.T, srcBare, dstBare string) {
	t.Helper()
	w := t.TempDir()
	run := func(dir string, args ...string) {
		t.Helper()
		cmd := exec.Command("git", args...)
		cmd.Dir = dir
		if out, err := cmd.CombinedOutput(); err != nil {
			t.Fatalf("git %s: %v\n%s", strings.Join(args, " "), err, out)
		}
	}
	run(w, "clone", srcBare, ".")
	run(w, "push", "--force", dstBare, "main")
}

// P1a end-to-end: push auth must be derived from the PUSH url.
//
// TestSSHE2E_MixedFetchPushURLs cannot prove this on its own. There both
// URLs are the same host and user, so choosing auth from the fetch URL
// yields byte-identical credentials and the bug is invisible — reverting
// the fix does not fail that spec. Here the two URLs demand DIFFERENT
// auth types: fetch is https:// (which resolves to HTTP BasicAuth from
// the token) and push is ssh:// (which must resolve to a public-key
// method). Selecting from the wrong URL hands BasicAuth to an SSH
// transport, which cannot authenticate.
func TestSSHE2E_PushAuthComesFromPushURL(t *testing.T) {
	repoA, repoB := sshE2EEnv(t)
	syncBare(t, bareDirOf(t, repoA), bareDirOf(t, repoB))

	dir := t.TempDir()
	c := NewWithIdentity("Test", "t@example.invalid")
	if err := c.Clone(context.Background(), repoA, dir, ""); err != nil {
		t.Fatalf("clone: %v", err)
	}
	repo, err := gogit.PlainOpen(dir)
	if err != nil {
		t.Fatal(err)
	}
	if err := repo.DeleteRemote("origin"); err != nil {
		t.Fatal(err)
	}
	// [0] = fetch over HTTPS (never contacted here), [last] = push over SSH.
	if _, err := repo.CreateRemote(&config.RemoteConfig{
		Name: "origin",
		URLs: []string{"https://example.invalid/unused.git", repoB},
	}); err != nil {
		t.Fatal(err)
	}

	if err := os.WriteFile(filepath.Join(dir, "auth-direction.txt"),
		uniqueContent("push auth from push url"), 0o644); err != nil {
		t.Fatal(err)
	}
	// A non-empty token makes the https URL resolve to BasicAuth, so a
	// wrong selection produces credentials rather than nothing.
	sha, err := c.CommitAndPush(context.Background(), dir, "push-auth direction", "fake-token")
	if err != nil {
		t.Fatalf("push failed — auth was probably taken from the https FETCH url: %v", err)
	}
	if got := headOf(t, bareDirOf(t, repoB)); got != sha {
		t.Errorf("push target = %s, want %s", got, sha)
	}
}

// --- credential-source coverage ---
//
// The default harness setup installs the accepted key BOTH in the agent
// and on disk, which is the realistic developer configuration but proves
// nothing about which source carried the handshake: with both present,
// an implementation that ignores key files entirely still passes. The
// harness's SSH_E2E_MODE narrows it to one usable source per run, and
// this spec asserts the run is actually exercising what its mode claims.
//
// Modes, and what a failure means:
//
//	agent       key ONLY in the agent      -> agent path is broken
//	keyfile     key ONLY on disk, no agent -> key-file path is broken
//	wrongagent  agent holds a REJECTED key -> the agent suppressed the
//	            plus the good key on disk     key-file fallback (P1b)
//	emptyagent  agent reachable but empty  -> an empty agent aborted the
//	            plus the good key on disk     handshake (P1b)
func TestSSHE2E_CredentialSourceMatchesMode(t *testing.T) {
	repoA, _ := sshE2EEnv(t)
	mode := sshMode()

	home := os.Getenv("KUBEDC_SSH_E2E_HOME")
	keyFile := filepath.Join(home, ".ssh", "id_ed25519")
	_, keyErr := os.Stat(keyFile)
	hasKeyFile := keyErr == nil
	hasAgent := os.Getenv("SSH_AUTH_SOCK") != ""

	// Inspect the agent for real. Checking that SSH_AUTH_SOCK is merely
	// SET proves nothing about what the agent holds: a "wrongagent" run
	// whose ssh-add silently failed would look identical to a correct
	// one and would pass while testing nothing. Dial the socket and
	// compare the identities it offers against the harness fixtures.
	accepted := fingerprintOfPubFile(t, os.Getenv("KUBEDC_SSH_E2E_ACCEPTED_PUB"))
	rejected := fingerprintOfPubFile(t, os.Getenv("KUBEDC_SSH_E2E_REJECTED_PUB"))
	agentKeys := agentFingerprints(t)
	has := func(fp string) bool {
		for _, k := range agentKeys {
			if k == fp {
				return true
			}
		}
		return false
	}

	// Verify the harness really set up what the mode advertises —
	// otherwise a mis-provisioned run would silently prove nothing.
	switch mode {
	case "agent":
		if hasKeyFile {
			t.Fatalf("mode=agent but a key file exists at %s; the agent path is not isolated", keyFile)
		}
		if !hasAgent {
			t.Fatal("mode=agent but SSH_AUTH_SOCK is unset")
		}
		if !has(accepted) {
			t.Fatalf("mode=agent but the agent does not hold the accepted key (holds %d: %v)", len(agentKeys), agentKeys)
		}
	case "keyfile":
		if hasAgent {
			t.Fatal("mode=keyfile but SSH_AUTH_SOCK is set; the key-file path is not isolated")
		}
		if !hasKeyFile {
			t.Fatalf("mode=keyfile but no key file at %s", keyFile)
		}
	case "wrongagent":
		if !hasAgent {
			t.Fatal("mode=wrongagent but SSH_AUTH_SOCK is unset; the fallback is not being exercised")
		}
		if !hasKeyFile {
			t.Fatalf("mode=wrongagent but no key file at %s to fall back to", keyFile)
		}
		// The whole point of the mode: the agent is reachable and
		// non-empty, but useless to this server.
		if has(accepted) {
			t.Fatal("mode=wrongagent but the agent holds the ACCEPTED key; the handshake could succeed via the agent and the key-file fallback would go untested")
		}
		if !has(rejected) {
			t.Fatalf("mode=wrongagent but the agent does not hold the decoy key (holds %d: %v); an accidentally-empty agent is a different scenario", len(agentKeys), agentKeys)
		}
	case "emptyagent":
		if !hasAgent {
			t.Fatal("mode=emptyagent but SSH_AUTH_SOCK is unset; the fallback is not being exercised")
		}
		if !hasKeyFile {
			t.Fatalf("mode=emptyagent but no key file at %s to fall back to", keyFile)
		}
		if len(agentKeys) != 0 {
			t.Fatalf("mode=emptyagent but the agent holds %d identities: %v", len(agentKeys), agentKeys)
		}
	case "both":
		if !has(accepted) {
			t.Fatalf("mode=both but the agent does not hold the accepted key (holds %d: %v)", len(agentKeys), agentKeys)
		}
	default:
		t.Fatalf("unknown harness mode %q", mode)
	}

	// With the setup confirmed, a successful clone means THIS mode's
	// credential source carried a real handshake.
	dir := t.TempDir()
	if err := New().Clone(context.Background(), repoA, dir, ""); err != nil {
		t.Fatalf("clone failed in mode=%s (agent=%v keyfile=%v): %v", mode, hasAgent, hasKeyFile, err)
	}
	if _, err := os.Stat(filepath.Join(dir, "file.txt")); err != nil {
		t.Fatalf("clone produced no working tree in mode=%s: %v", mode, err)
	}
}

// agentFingerprints dials SSH_AUTH_SOCK and returns the SHA256
// fingerprints of every identity the agent offers. Returns nil when no
// agent is configured. A configured-but-unreachable socket is fatal:
// silently treating it as empty would let a broken harness masquerade as
// the "no agent" scenario.
func agentFingerprints(t *testing.T) []string {
	t.Helper()
	sock := os.Getenv("SSH_AUTH_SOCK")
	if sock == "" {
		return nil
	}
	conn, err := net.Dial("unix", sock)
	if err != nil {
		t.Fatalf("SSH_AUTH_SOCK=%s is set but not reachable: %v", sock, err)
	}
	defer conn.Close()
	keys, err := agent.NewClient(conn).List()
	if err != nil {
		t.Fatalf("listing agent identities: %v", err)
	}
	fps := make([]string, 0, len(keys))
	for _, k := range keys {
		fps = append(fps, ssh.FingerprintSHA256(k))
	}
	return fps
}

// fingerprintOfPubFile reads an OpenSSH .pub fixture and returns its
// SHA256 fingerprint, so agent contents can be compared against the
// harness's accepted/rejected keys rather than assumed.
func fingerprintOfPubFile(t *testing.T, path string) string {
	t.Helper()
	if path == "" {
		t.Skip("harness predates KUBEDC_SSH_E2E_ACCEPTED_PUB/_REJECTED_PUB; re-run hack/ssh-e2e-harness.sh")
	}
	raw, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("read pub fixture %s: %v", path, err)
	}
	pk, _, _, _, err := ssh.ParseAuthorizedKey(raw)
	if err != nil {
		t.Fatalf("parse pub fixture %s: %v", path, err)
	}
	return ssh.FingerprintSHA256(pk)
}
