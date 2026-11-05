package clusterinit

import (
	"os"
	"path/filepath"
	"testing"
)

func TestValidateInputSpecRefusesCredentialContentsInFileKeys(t *testing.T) {
	for _, v := range []string{"-----BEGIN PRIVATE KEY-----", "abcdefghijklmnopqrstuvwxyz0123456789ABCD", "Bearer abc", "Ab/cd+efgh/ij"} {
		for k := range credentialFileKeys {
			if err := ValidateInputSpec(map[string]string{k: v}); err == nil {
				t.Fatalf("%s accepted credential contents", k)
			}
		}
	}
	existing := filepath.Join(t.TempDir(), "Alice", "Route53SecretKey2026")
	if err := os.MkdirAll(filepath.Dir(existing), 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(existing, []byte("x"), 0o600); err != nil {
		t.Fatal(err)
	}
	for _, v := range []string{"/secure/ca.pem", "certs/ca.pem", "cf.token", "/home/Alice/secrets/route53-secret", existing, ""} {
		if err := ValidateInputSpec(map[string]string{InitPrefix + "TRUSTED_CA_BUNDLE": v}); err != nil {
			t.Fatalf("refused a file name %q: %v", v, err)
		}
	}
}
