package sops

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"gopkg.in/yaml.v3"

	"github.com/shalb/kube-dc/cli/internal/bootstrap/ports"
)

// Compile-time assertion.
var _ ports.SOPSClient = (*Client)(nil)

func TestEncrypt_BuildsExpectedArgs(t *testing.T) {
	var captured []string
	c := &Client{
		exec: func(_ context.Context, _ string, _ []byte, args ...string) ([]byte, []byte, error) {
			captured = args
			return nil, nil, nil
		},
	}
	if err := c.Encrypt(context.Background(), "/path/to/secret.enc.yaml"); err != nil {
		t.Fatalf("Encrypt: %v", err)
	}
	want := []string{"--encrypt", "--in-place", "/path/to/secret.enc.yaml"}
	if !equal(captured, want) {
		t.Errorf("args=%v want %v", captured, want)
	}
}

func TestEncrypt_EmptyPath_Rejected(t *testing.T) {
	c := New()
	if err := c.Encrypt(context.Background(), ""); err == nil {
		t.Fatal("empty path should fail")
	}
}

func TestEncrypt_SurfacesStderr(t *testing.T) {
	c := &Client{
		exec: func(context.Context, string, []byte, ...string) ([]byte, []byte, error) {
			return nil, []byte("Error: no recipients found in .sops.yaml"), errors.New("exit 1")
		},
	}
	err := c.Encrypt(context.Background(), "/tmp/foo")
	if err == nil {
		t.Fatal("expected error")
	}
	if !strings.Contains(err.Error(), "no recipients") {
		t.Errorf("stderr not propagated: %v", err)
	}
}

func TestDecrypt_ReturnsStdout(t *testing.T) {
	c := &Client{
		exec: func(_ context.Context, _ string, _ []byte, args ...string) ([]byte, []byte, error) {
			want := []string{"--decrypt", "/path/to/secret.enc.yaml"}
			if !equal(args, want) {
				t.Errorf("args=%v want %v", args, want)
			}
			return []byte("apiVersion: v1\nkind: Secret\n"), nil, nil
		},
	}
	out, err := c.Decrypt(context.Background(), "/path/to/secret.enc.yaml")
	if err != nil {
		t.Fatalf("Decrypt: %v", err)
	}
	if !strings.HasPrefix(string(out), "apiVersion: v1") {
		t.Errorf("Decrypt returned wrong content: %q", out)
	}
}

// Core review-pass test: SetStringData MUST pipe the new value via
// stdin and the encryption argv MUST NOT contain the plaintext.
func TestSetStringData_PlaintextRidesStdinNotArgv(t *testing.T) {
	// Prepare an encrypted-looking source file with sops recipients.
	dir := t.TempDir()
	path := filepath.Join(dir, "secret.enc.yaml")
	srcBody := `apiVersion: v1
kind: Secret
stringData:
    OPENBAO_UNSEAL_KEY_1: ENC[AES256_GCM,data:foo,iv:bar,tag:baz,type:str]
sops:
    age:
        - recipient: age10mskwx065akee5mw4txeqtnn90t724phdzx9k4jxnrgcp9cces6sqfkwvu
          enc: |
            -----BEGIN AGE ENCRYPTED FILE-----
            body
            -----END AGE ENCRYPTED FILE-----
`
	if err := os.WriteFile(path, []byte(srcBody), 0o600); err != nil {
		t.Fatal(err)
	}

	var (
		capturedEncryptArgs []string
		capturedStdin       []byte
	)
	const secretValue = "test-share-DO-NOT-LEAK-INTO-ARGV"

	c := &Client{
		exec: func(_ context.Context, _ string, stdin []byte, args ...string) ([]byte, []byte, error) {
			switch {
			case args[0] == "--decrypt":
				// First call (read current plaintext) and the final
				// round-trip verify both go through here. Return
				// content that contains the new value so verify
				// passes.
				return []byte("apiVersion: v1\nkind: Secret\nstringData:\n  OPENBAO_UNSEAL_KEY_1: " + secretValue + "\n"), nil, nil
			case args[0] == "--encrypt":
				capturedEncryptArgs = append([]string(nil), args...)
				capturedStdin = append([]byte(nil), stdin...)
				// Simulate sops writing the encrypted output.
				for i := 0; i < len(args)-1; i++ {
					if args[i] == "--output" {
						_ = os.WriteFile(args[i+1], []byte("encrypted-blob"), 0o644)
					}
				}
				return nil, nil, nil
			default:
				t.Errorf("unexpected sops args: %v", args)
				return nil, nil, nil
			}
		},
	}

	if err := c.SetStringData(context.Background(), path, "OPENBAO_UNSEAL_KEY_1", []byte(secretValue)); err != nil {
		t.Fatalf("SetStringData: %v", err)
	}

	// Plaintext MUST be in stdin.
	if !strings.Contains(string(capturedStdin), secretValue) {
		t.Errorf("plaintext not in stdin: %q", capturedStdin)
	}
	// Plaintext MUST NOT appear anywhere in argv.
	for i, a := range capturedEncryptArgs {
		if strings.Contains(a, "DO-NOT-LEAK") || strings.Contains(a, "test-share") {
			t.Errorf("plaintext leaked into argv[%d]=%q", i, a)
		}
	}
	// argv must end with /dev/stdin so sops reads from the pipe.
	if capturedEncryptArgs[len(capturedEncryptArgs)-1] != "/dev/stdin" {
		t.Errorf("argv does not target /dev/stdin: %v", capturedEncryptArgs)
	}
}

// Atomic rename: the destination file at `path` must exist AND
// contain the (encrypted) output bytes from sops at the end of
// SetStringData. The intermediate tempfile must be cleaned up.
func TestSetStringData_AtomicRenameToDestination(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "secret.enc.yaml")
	if err := os.WriteFile(path, []byte("sops:\n  age:\n    - recipient: age1xxx\n"), 0o600); err != nil {
		t.Fatal(err)
	}

	const encBlob = "fresh-encrypted-output"

	c := &Client{
		exec: func(_ context.Context, _ string, _ []byte, args ...string) ([]byte, []byte, error) {
			if args[0] == "--decrypt" {
				return []byte("stringData:\n  KEY: value\n"), nil, nil
			}
			if args[0] == "--encrypt" {
				for i := 0; i < len(args)-1; i++ {
					if args[i] == "--output" {
						_ = os.WriteFile(args[i+1], []byte(encBlob), 0o644)
					}
				}
			}
			return nil, nil, nil
		},
	}

	if err := c.SetStringData(context.Background(), path, "KEY", []byte("value")); err != nil {
		t.Fatalf("SetStringData: %v", err)
	}

	body, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("dest missing after rename: %v", err)
	}
	if string(body) != encBlob {
		t.Errorf("dest content = %q, want %q", body, encBlob)
	}
	// Tempfile should be gone (cleaned up by either the rename or
	// the deferred remove).
	entries, _ := os.ReadDir(dir)
	for _, e := range entries {
		if strings.HasPrefix(e.Name(), ".sops-") {
			t.Errorf("tempfile leaked: %s", e.Name())
		}
	}
	// Final file mode should be 0o600 (chmod tightened before rename).
	info, _ := os.Stat(path)
	if info.Mode().Perm() != 0o600 {
		t.Errorf("dest mode = 0o%o, want 0o600", info.Mode().Perm())
	}
}

func TestSetStringData_RoundTripFailureSurfaces(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "secret.enc.yaml")
	if err := os.WriteFile(path, []byte("sops:\n  age:\n    - recipient: age1xxx\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	decryptCalls := 0
	c := &Client{
		exec: func(_ context.Context, _ string, _ []byte, args ...string) ([]byte, []byte, error) {
			if args[0] == "--decrypt" {
				decryptCalls++
				if decryptCalls == 1 {
					return []byte("stringData:\n  KEY: old\n"), nil, nil
				}
				// Round-trip verify returns content WITHOUT the new value.
				return []byte("stringData:\n  KEY: stale-old\n"), nil, nil
			}
			if args[0] == "--encrypt" {
				for i := 0; i < len(args)-1; i++ {
					if args[i] == "--output" {
						_ = os.WriteFile(args[i+1], []byte("blob"), 0o644)
					}
				}
			}
			return nil, nil, nil
		},
	}
	err := c.SetStringData(context.Background(), path, "KEY", []byte("expected-value"))
	if err == nil {
		t.Fatal("round-trip mismatch should be caught")
	}
	if !strings.Contains(err.Error(), "round-trip verify failed") {
		t.Errorf("error doesn't surface round-trip: %v", err)
	}
}

func TestSetStringData_EmptyValueRejected(t *testing.T) {
	c := New()
	if err := c.SetStringData(context.Background(), "/x", "K", nil); err == nil {
		t.Fatal("empty value should be rejected")
	}
}

func TestDerivePubKey_FromAgeFile(t *testing.T) {
	tmp := filepath.Join(t.TempDir(), "age.key")
	body := `# created: 2026-05-26T10:00:00+02:00
# public key: age10mskwx065akee5mw4txeqtnn90t724phdzx9k4jxnrgcp9cces6sqfkwvu
AGE-SECRET-KEY-1AAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAA
`
	if err := os.WriteFile(tmp, []byte(body), 0o600); err != nil {
		t.Fatal(err)
	}
	c := New()
	pub, err := c.DerivePubKey(tmp)
	if err != nil {
		t.Fatalf("DerivePubKey: %v", err)
	}
	if pub != "age10mskwx065akee5mw4txeqtnn90t724phdzx9k4jxnrgcp9cces6sqfkwvu" {
		t.Errorf("pub=%q", pub)
	}
}

func TestDerivePubKey_MissingComment_Errors(t *testing.T) {
	tmp := filepath.Join(t.TempDir(), "age.key")
	if err := os.WriteFile(tmp, []byte("AGE-SECRET-KEY-1AAA\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	c := New()
	if _, err := c.DerivePubKey(tmp); err == nil {
		t.Fatal("expected error on missing pubkey comment")
	}
}

func TestRecipients_ParsesCanonicalSOPSYAML(t *testing.T) {
	tmp := filepath.Join(t.TempDir(), "secret.enc.yaml")
	body := `apiVersion: v1
kind: Secret
sops:
  age:
    - recipient: age10mskwx065akee5mw4txeqtnn90t724phdzx9k4jxnrgcp9cces6sqfkwvu
      enc: |
        -----BEGIN AGE ENCRYPTED FILE-----
        body
        -----END AGE ENCRYPTED FILE-----
    - recipient: age1fugkeh0yhf56d6t2qm8gwqdl3kx7963wv2qq5jax2kewldsfeqgq6p67pm
`
	if err := os.WriteFile(tmp, []byte(body), 0o600); err != nil {
		t.Fatal(err)
	}
	c := New()
	got, err := c.Recipients(tmp)
	if err != nil {
		t.Fatalf("Recipients: %v", err)
	}
	if len(got) != 2 {
		t.Errorf("got %d recipients, want 2: %v", len(got), got)
	}
}

func TestSetStringDataNode_PreservesOtherKeys(t *testing.T) {
	// Direct unit test of the in-memory YAML mutation — ensures we
	// don't clobber unrelated stringData entries when setting one.
	src := []byte(`apiVersion: v1
kind: Secret
stringData:
  KEEP_ME: keepvalue
  OPENBAO_UNSEAL_KEY_1: oldvalue
`)
	var doc yaml.Node
	if err := yaml.Unmarshal(src, &doc); err != nil {
		t.Fatal(err)
	}
	if err := setStringDataNode(&doc, "OPENBAO_UNSEAL_KEY_1", "newvalue"); err != nil {
		t.Fatal(err)
	}
	out, err := yaml.Marshal(&doc)
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(out), "KEEP_ME: keepvalue") {
		t.Errorf("unrelated key dropped: %s", out)
	}
	if !strings.Contains(string(out), "OPENBAO_UNSEAL_KEY_1: newvalue") {
		t.Errorf("new value missing: %s", out)
	}
}

func TestSetStringDataNode_AppendsWhenStringDataAbsent(t *testing.T) {
	src := []byte("apiVersion: v1\nkind: Secret\nmetadata:\n  name: x\n")
	var doc yaml.Node
	if err := yaml.Unmarshal(src, &doc); err != nil {
		t.Fatal(err)
	}
	if err := setStringDataNode(&doc, "KEY", "value"); err != nil {
		t.Fatal(err)
	}
	out, _ := yaml.Marshal(&doc)
	if !strings.Contains(string(out), "stringData:") {
		t.Errorf("stringData not appended: %s", out)
	}
	if !strings.Contains(string(out), "KEY: value") {
		t.Errorf("key/value missing: %s", out)
	}
}

func equal(a, b []string) bool {
	if len(a) != len(b) {
		return false
	}
	for i := range a {
		if a[i] != b[i] {
			return false
		}
	}
	return true
}
