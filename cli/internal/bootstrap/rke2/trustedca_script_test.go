package rke2

import (
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
)

// The Go side validates what we AGREE to install; this exercises the bash that
// actually installs it. Both scripts carry the same function, and a divergence
// between them would give an air-gapped cluster servers that trust the registry
// and workers that do not — so both are run against the same table.
//
// Follows the registries_has_mirror pattern: lift the function out of the
// embedded script so the test cannot drift from what ships.

func extractShellFunc(t *testing.T, script []byte, name string) string {
	t.Helper()
	source := string(script)
	start := strings.Index(source, name+"() {")
	if start < 0 {
		t.Fatalf("cannot find %s in the embedded installer", name)
	}
	// The function body ends at the first line that is exactly "}".
	end := strings.Index(source[start:], "\n}\n")
	if end < 0 {
		t.Fatalf("cannot extract %s from the embedded installer", name)
	}
	return source[start : start+end+2]
}

func runTrustedCAFunc(t *testing.T, script []byte, arg string, anchorDir string) (string, error) {
	t.Helper()
	fn := extractShellFunc(t, script, "install_node_trusted_ca")
	dir := t.TempDir()
	runner := filepath.Join(dir, "run.sh")

	// Stub the logging helpers and the trust-store updater so the function can
	// run unprivileged, and point the anchor directory at a temp dir. The
	// updater records that it was called.
	harness := "#!/usr/bin/env bash\nset -uo pipefail\n" +
		"log_info() { echo \"[INFO] $*\"; }\n" +
		"log_warn() { echo \"[WARN] $*\"; }\n" +
		"log_error() { echo \"[ERROR] $*\" >&2; }\n" +
		"update-ca-certificates() { echo updated > " + filepath.Join(dir, "updated") + "; }\n" +
		"export -f update-ca-certificates 2>/dev/null || true\n" +
		fn + "\n" +
		"install_node_trusted_ca \"$1\"\n"
	if err := os.WriteFile(runner, []byte(harness), 0o700); err != nil {
		t.Fatal(err)
	}
	cmd := exec.Command("bash", runner, arg)
	// The function picks its anchor dir by probing well-known paths; give it a
	// writable one by prepending a fake root to PATH-independent checks is not
	// possible, so tests that need the happy path assert on refusals instead.
	out, err := cmd.CombinedOutput()
	return string(out), err
}

// Empty TRUSTED_CA_FILE means "no private CA" — the correct default on a
// connected, ACME-issued cluster. It must be a clean no-op, not a failure.
func TestScript_NoBundleIsANoOp(t *testing.T) {
	for name, script := range map[string][]byte{"server": installServerScript, "agent": installAgentScript} {
		t.Run(name, func(t *testing.T) {
			out, err := runTrustedCAFunc(t, script, "", "")
			if err != nil {
				t.Fatalf("an empty bundle path must succeed silently, got err=%v out=%s", err, out)
			}
		})
	}
}

// The installer is told a file exists; if it does not, continuing would produce
// a cluster that looks installed and cannot pull images.
func TestScript_MissingOrEmptyFileFails(t *testing.T) {
	dir := t.TempDir()
	empty := filepath.Join(dir, "empty.pem")
	if err := os.WriteFile(empty, nil, 0o600); err != nil {
		t.Fatal(err)
	}
	for name, script := range map[string][]byte{"server": installServerScript, "agent": installAgentScript} {
		t.Run(name+"/missing", func(t *testing.T) {
			if _, err := runTrustedCAFunc(t, script, filepath.Join(dir, "absent.pem"), ""); err == nil {
				t.Fatal("a missing bundle must fail the install, not be skipped")
			}
		})
		t.Run(name+"/empty", func(t *testing.T) {
			if _, err := runTrustedCAFunc(t, script, empty, ""); err == nil {
				t.Fatal("an empty bundle must fail the install")
			}
		})
	}
}

// Belt to the Go validator's brace: even if something upstream changed, the
// script itself must never install a private key as a node trust anchor.
func TestScript_RefusesAPrivateKey(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "mixed.pem")
	body := "-----BEGIN CERTIFICATE-----\nMIIB\n-----END CERTIFICATE-----\n" +
		"-----BEGIN PRIVATE KEY-----\nMIIB\n-----END PRIVATE KEY-----\n"
	if err := os.WriteFile(path, []byte(body), 0o600); err != nil {
		t.Fatal(err)
	}
	for name, script := range map[string][]byte{"server": installServerScript, "agent": installAgentScript} {
		t.Run(name, func(t *testing.T) {
			out, err := runTrustedCAFunc(t, script, path, "")
			if err == nil {
				t.Fatalf("a PRIVATE KEY must be refused by the script too; out=%s", out)
			}
			if !strings.Contains(out, "PRIVATE KEY") {
				t.Fatalf("the refusal must name the reason, got: %s", out)
			}
		})
	}
}

func TestScript_RefusesAFileWithNoCertificate(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "junk.pem")
	if err := os.WriteFile(path, []byte("just some text\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	for name, script := range map[string][]byte{"server": installServerScript, "agent": installAgentScript} {
		t.Run(name, func(t *testing.T) {
			if _, err := runTrustedCAFunc(t, script, path, ""); err == nil {
				t.Fatal("a file with no PEM certificate must be refused")
			}
		})
	}
}

// A failure here must stop the install. Continuing would hand the operator a
// cluster that comes up and then cannot pull a single image — a far more
// expensive failure to diagnose than an install that stops with a reason.
func TestScript_FailureAbortsTheInstall(t *testing.T) {
	for name, script := range map[string][]byte{"server": installServerScript, "agent": installAgentScript} {
		t.Run(name, func(t *testing.T) {
			source := string(script)
			if !strings.Contains(source, `if ! install_node_trusted_ca "${TRUSTED_CA_FILE:-}"; then`) {
				t.Fatal("the installer must call install_node_trusted_ca and check its result")
			}
			if !strings.Contains(source, "Node trust setup failed; refusing to continue.") {
				t.Fatal("a node-trust failure must abort the install with an explicit reason")
			}
		})
	}
}

// Ordering is the whole point: trust has to exist before rke2 starts, or the
// first image pull races it.
func TestScript_RunsBeforeRKE2Starts(t *testing.T) {
	for name, script := range map[string][]byte{"server": installServerScript, "agent": installAgentScript} {
		t.Run(name, func(t *testing.T) {
			source := string(script)
			trust := strings.Index(source, `if ! install_node_trusted_ca "${TRUSTED_CA_FILE:-}"; then`)
			if trust < 0 {
				t.Fatal("install_node_trusted_ca is never invoked")
			}
			start := strings.Index(source, "systemctl start")
			if start >= 0 && trust > start {
				t.Fatal("node trust must be installed BEFORE rke2 is started, or the first image pull races it")
			}
		})
	}
}

// Debian's update-ca-certificates ignores any file that is not *.crt — it exits
// 0 and silently never trusts the CA. Getting this suffix wrong is the single
// most likely way for this to appear to work and not.
func TestScript_UsesTheCrtSuffixOnDebian(t *testing.T) {
	for name, script := range map[string][]byte{"server": installServerScript, "agent": installAgentScript} {
		t.Run(name, func(t *testing.T) {
			fn := extractShellFunc(t, script, "install_node_trusted_ca")
			// The path is assembled from anchor_dir + filename, so assert on the
			// two halves rather than a literal that never appears in the source.
			if !strings.Contains(fn, "anchor_dir=/usr/local/share/ca-certificates") {
				t.Fatal("the Debian anchor directory must be handled")
			}
			if !strings.Contains(fn, `dest="${anchor_dir}/kube-dc-platform-ca-${i}.${suffix}"`) {
				t.Fatal("anchors must be written one certificate per numbered file")
			}
			if !strings.Contains(fn, "suffix=crt") {
				t.Fatal("the Debian anchor must end in .crt — update-ca-certificates exits 0 and " +
					"silently ignores any other suffix, so the CA is never actually trusted")
			}
			if !strings.Contains(fn, "anchor_dir=/etc/pki/ca-trust/source/anchors") {
				t.Fatal("the RHEL anchor directory must also be handled")
			}
		})
	}
}

// ---- Regressions found by running the script on REAL distros ----
//
// The tests above stub update-ca-certificates, which is why they could not
// catch either of these. Both were found by executing the shipped function in
// debian:12 and rockylinux:9 containers against the real trust tooling.

// The post-install check exists to catch update-ca-certificates exiting 0 while
// silently skipping the anchor. It was written as a fingerprint compare that
// piped a whole CA bundle into `openssl x509` — which reads only the FIRST
// certificate in a stream, so it never matched. Measured on debian:12: a
// perfectly successful install printed "could not confirm the CA in the system
// bundle", i.e. the guard against the most likely silent failure cried wolf on
// every single run and was therefore useless.
func TestScript_VerifiesTrustWithOpensslVerify(t *testing.T) {
	for name, script := range map[string][]byte{"server": installServerScript, "agent": installAgentScript} {
		t.Run(name, func(t *testing.T) {
			fn := extractShellFunc(t, script, "install_node_trusted_ca")
			if !strings.Contains(fn, "openssl verify -no_check_time -partial_chain -CAfile") {
				t.Fatal("trust must be confirmed with `openssl verify -no_check_time -partial_chain -CAfile`, " +
					"measured correct on debian:12 and rockylinux:9 for present/absent/wrong-suffix")
			}
			if strings.Contains(fn, "crl2pkcs7") {
				t.Fatal("the crl2pkcs7|pkcs7|x509 fingerprint pipeline reads only the FIRST certificate " +
					"of the bundle, so it never matches and warns on every successful install")
			}
		})
	}
}

// A failed confirmation must FAIL the install, not warn. Warning hands the
// operator a cluster that comes up and then cannot pull a single image — the
// exact failure this whole code path exists to prevent.
func TestScript_UnconfirmedTrustFailsTheInstall(t *testing.T) {
	for name, script := range map[string][]byte{"server": installServerScript, "agent": installAgentScript} {
		t.Run(name, func(t *testing.T) {
			fn := extractShellFunc(t, script, "install_node_trusted_ca")
			if !strings.Contains(fn, "reported success but the CA is NOT trusted via") {
				t.Fatal("an unconfirmed trust store must be reported as an error")
			}
			idx := strings.Index(fn, "reported success but the CA is NOT trusted via")
			if !strings.Contains(fn[idx:], "return 1") {
				t.Fatal("an unconfirmed trust store must return non-zero so the install aborts")
			}
		})
	}
}

// `cmp` lives in diffutils, which a minimal RHEL/Rocky host does not ship.
// Measured on rockylinux:9: "cmp: command not found", and because a missing
// binary is indistinguishable from "the files differ", every re-run silently
// reinstalled the anchor and rebuilt the whole trust store.
func TestScript_IdempotencyDoesNotDependOnDiffutils(t *testing.T) {
	for name, script := range map[string][]byte{"server": installServerScript, "agent": installAgentScript} {
		t.Run(name, func(t *testing.T) {
			fn := extractShellFunc(t, script, "install_node_trusted_ca")
			if strings.Contains(fn, "cmp -s") {
				t.Fatal("cmp is in diffutils and is absent on minimal RHEL/Rocky; " +
					"compare by checksum instead")
			}
			if !strings.Contains(fn, "sha256sum") {
				t.Fatal("the unchanged-bundle check must use a coreutils checksum")
			}
		})
	}
}

// ---- Hardening from the node-layer Codex review, each measured on real distros ----

// An expired root in a rotation bundle is LEGITIMATE — the outgoing CA rides
// alongside the incoming one, and that overlap is what makes rotation safe. The
// documented policy is that expiry warns and never refuses, but `openssl verify`
// checks anchor validity periods, so the verification step silently reintroduced
// a hard refusal. Measured on debian:12: without -no_check_time a correct
// rotation bundle aborts the install.
func TestScript_ExpiredRootInARotationBundleDoesNotAbort(t *testing.T) {
	for name, script := range map[string][]byte{"server": installServerScript, "agent": installAgentScript} {
		t.Run(name, func(t *testing.T) {
			fn := extractShellFunc(t, script, "install_node_trusted_ca")
			// Asserted on the INVOCATION, not on bare flag presence: both flags
			// are named in the comment that explains them, so a looser check
			// passes even when they are stripped from the command. Verified by
			// mutation — that is exactly how this test failed first time round.
			const invocation = "openssl verify -no_check_time -partial_chain -CAfile"
			if !strings.Contains(fn, invocation) {
				t.Fatalf("verification must run %q.\n"+
					"-no_check_time: an expired OUTGOING root is normal in a rotation bundle, and "+
					"enforcing validity here reintroduces the hard refusal the warn-only policy exists to avoid.\n"+
					"-partial_chain: an intermediate-only bundle must verify against the installed "+
					"intermediate rather than demanding a root we were never given.", invocation)
			}
		})
	}
}

// Debian's update-ca-certificates only creates CApath hash symlinks for
// SINGLE-certificate files. Measured on debian:12: two certs in one .crt gave
// ZERO new links; the same two in two files gave two. The multi-cert file still
// reaches ca-certificates.crt, so Go (and therefore containerd) is fine, but an
// SSL_CERT_DIR-only consumer would silently not trust it.
func TestScript_WritesOneCertificatePerAnchorFile(t *testing.T) {
	for name, script := range map[string][]byte{"server": installServerScript, "agent": installAgentScript} {
		t.Run(name, func(t *testing.T) {
			fn := extractShellFunc(t, script, "install_node_trusted_ca")
			if !strings.Contains(fn, "cert-\" n \".pem") && !strings.Contains(fn, "cert-${i}.pem") {
				t.Fatal("the bundle must be split into one certificate per anchor file, or CApath " +
					"consumers silently do not trust it")
			}
			if !strings.Contains(fn, "n > total") {
				t.Fatal("anchors left by a LARGER previous bundle must be removed, or a retired CA " +
					"stays trusted on the node for ever")
			}
		})
	}
}

// The Go validator enforces certificates-only, CA-only and a size bound; the
// script can only look for PEM markers. Between them the bundle sits at a
// predictable path in a world-writable directory, so without binding the two the
// weaker parser decides what becomes a trust anchor on every node.
func TestScript_RefusesABundleThatChangedAfterValidation(t *testing.T) {
	for name, script := range map[string][]byte{"server": installServerScript, "agent": installAgentScript} {
		t.Run(name, func(t *testing.T) {
			fn := extractShellFunc(t, script, "install_node_trusted_ca")
			if !strings.Contains(fn, "TRUSTED_CA_SHA256") {
				t.Fatal("the script must verify the fingerprint the CLI validated")
			}
			if !strings.Contains(fn, "does NOT match the bundle the installer validated") {
				t.Fatal("a fingerprint mismatch must be refused by name, not warned about")
			}
		})
	}
}

// errexit does NOT apply inside a function invoked as `if ! fn`, so an unchecked
// install(1) failure would sail on to the trust-store rebuild and the probe —
// and the probe can pass on a PREVIOUSLY trusted anchor, reporting success for a
// bundle that was never written.
func TestScript_ChecksTheAnchorCopySucceeded(t *testing.T) {
	for name, script := range map[string][]byte{"server": installServerScript, "agent": installAgentScript} {
		t.Run(name, func(t *testing.T) {
			fn := extractShellFunc(t, script, "install_node_trusted_ca")
			if !strings.Contains(fn, `if ! install -m 0644`) {
				t.Fatal("the anchor copy must be checked; errexit is disabled for a function called from `if !`")
			}
			if !strings.Contains(fn, "rolling back to the previous anchors") {
				t.Fatal("a failed trust-store rebuild must restore the previous anchors rather than " +
					"leaving the node with neither the old nor the new trust configuration")
			}
		})
	}
}

// The staged bundle is at a predictable path in a world-writable directory.
// Leaving it behind is a collision target for the next run.
func TestScript_RemovesTheStagedBundle(t *testing.T) {
	for name, script := range map[string][]byte{"server": installServerScript, "agent": installAgentScript} {
		t.Run(name, func(t *testing.T) {
			if !strings.Contains(string(script), `rm -f "${TRUSTED_CA_FILE}"`) {
				t.Fatal("the staged bundle must be removed after install, on success and on failure")
			}
		})
	}
}

// Anchors from an earlier install are deliberately NOT removed — silently
// untrusting a CA could cut a running node off from its registry. But a re-run
// with no bundle must not imply "public CAs only" while a private CA is still
// trusted.
func TestScript_ReportsAnExistingAnchorWhenNoBundleIsGiven(t *testing.T) {
	for name, script := range map[string][]byte{"server": installServerScript, "agent": installAgentScript} {
		t.Run(name, func(t *testing.T) {
			fn := extractShellFunc(t, script, "install_node_trusted_ca")
			if !strings.Contains(fn, "STILL TRUSTS a kube-dc CA from an earlier install") {
				t.Fatal("a node that still trusts a private CA must say so when none was requested")
			}
		})
	}
}
