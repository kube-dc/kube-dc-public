package clusterinit

import (
	"crypto/ed25519"
	"crypto/rand"
	"strings"
	"testing"

	"golang.org/x/crypto/ssh"
)

func TestServerPublicKeyLineNormalizesToHandshakeFingerprint(t *testing.T) {
	public, _, err := ed25519.GenerateKey(rand.Reader)
	if err != nil {
		t.Fatal(err)
	}
	key, err := ssh.NewPublicKey(public)
	if err != nil {
		t.Fatal(err)
	}
	line := strings.TrimSpace(string(ssh.MarshalAuthorizedKey(key))) + " host@example"
	want := ssh.FingerprintSHA256(key)
	got, err := NormalizeSSHHostKeyInput(line)
	if err != nil || got != want {
		t.Fatalf("server public key did not produce SSH handshake fingerprint: %q %v", got, err)
	}
	if got, err := NormalizeSSHHostKeyInput(want); err != nil || got != want {
		t.Fatalf("fingerprint input changed: %q %v", got, err)
	}
	for _, bad := range []string{"ssh-ed25519 AAAA sample", line + "\n" + line, "command=foo " + line} {
		if _, err := NormalizeSSHHostKeyInput(bad); err == nil {
			t.Fatalf("accepted malformed or multi-line host key")
		}
	}
}
