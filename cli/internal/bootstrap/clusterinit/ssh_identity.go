package clusterinit

import (
	"crypto/sha256"
	"encoding/base64"
	"fmt"
	"strings"

	"golang.org/x/crypto/ssh"
)

func ValidateSSHHostKeyField(value string) error {
	if value == "" {
		return nil
	}
	encoded, ok := strings.CutPrefix(value, "SHA256:")
	raw, err := base64.RawStdEncoding.DecodeString(encoded)
	if !ok || err != nil || len(raw) != sha256.Size || base64.RawStdEncoding.EncodeToString(raw) != encoded {
		return fmt.Errorf("SSH host fingerprint must be a SHA256: fingerprint, not a public-key line")
	}
	return nil
}

// NormalizeSSHHostKeyInput accepts a fingerprint or an SSH server public-key
// line copied through a trusted console. The durable option remains a SHA-256
// fingerprint, which the SSH adapter checks during the target handshake.
func NormalizeSSHHostKeyInput(value string) (string, error) {
	value = strings.TrimSpace(value)
	if value == "" || strings.HasPrefix(value, "SHA256:") {
		return value, ValidateSSHHostKeyField(value)
	}
	key, comment, options, rest, err := ssh.ParseAuthorizedKey([]byte(value))
	if err != nil || len(options) != 0 || len(rest) != 0 || strings.ContainsAny(comment, "\r\n") {
		return "", fmt.Errorf("enter a SHA256: fingerprint or one SSH server public-key line")
	}
	return ssh.FingerprintSHA256(key), nil
}
