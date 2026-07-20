package breakglass

import "testing"

// TestAssertSecretsEncrypted covers the fail-closed guard that prevents a
// cleartext credential from ever reaching the committed break-glass file
// (regression guard for the kube-dc E2E 2026-07-08 P1 leak, where the fleet
// .sops.yaml `^(data|stringData)$` regex left the kubeconfig token in plain).
func TestAssertSecretsEncrypted(t *testing.T) {
	const token = "eyJhbGciOiJSUzI1NiIsImtpZCI6IkFCQ0RFRkdISUpLIn0.payloadpayloadpayload.sigsigsig"

	tests := []struct {
		name      string
		plain     string
		encrypted string
		wantErr   bool
	}{
		{
			name:      "token left in cleartext → error",
			plain:     "users:\n- user:\n    token: " + token + "\n",
			encrypted: "users:\n- user:\n    token: " + token + "\n", // sops no-op'd the field
			wantErr:   true,
		},
		{
			name:      "token encrypted → ok",
			plain:     "users:\n- user:\n    token: " + token + "\n",
			encrypted: "users:\n- user:\n    token: ENC[AES256_GCM,data:ZmFrZQ==,iv:x,tag:y,type:str]\n",
			wantErr:   false,
		},
		{
			name:      "public CA data left plaintext is fine (not a guarded key)",
			plain:     "clusters:\n- cluster:\n    certificate-authority-data: LS0tLS1CRUdJTkNFUlQ=\n",
			encrypted: "clusters:\n- cluster:\n    certificate-authority-data: LS0tLS1CRUdJTkNFUlQ=\n",
			wantErr:   false,
		},
		{
			name:      "client-key-data left in cleartext → error",
			plain:     "users:\n- user:\n    client-key-data: LS0tLS1CRUdJTlBSSVZBVEVLRVk=\n",
			encrypted: "users:\n- user:\n    client-key-data: LS0tLS1CRUdJTlBSSVZBVEVLRVk=\n",
			wantErr:   true,
		},
		{
			name:      "empty/trivial token value is skipped",
			plain:     "users:\n- user:\n    token: \"\"\n",
			encrypted: "users:\n- user:\n    token: \"\"\n",
			wantErr:   false,
		},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			err := assertSecretsEncrypted([]byte(tc.plain), []byte(tc.encrypted))
			if tc.wantErr && err == nil {
				t.Fatalf("expected error (plaintext secret should be detected), got nil")
			}
			if !tc.wantErr && err != nil {
				t.Fatalf("expected no error, got %v", err)
			}
		})
	}
}
