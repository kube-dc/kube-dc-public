package rke2

import (
	"crypto/sha256"
	"encoding/hex"
	"strings"
	"testing"
)

// auditPolicySHA256 is the sha256 of the reviewed kube-apiserver audit policy
// the installer writes inline. Changing the policy is a reviewed change: update
// this embedded installer, the fleet's bootstrap/rke2/install-server.sh and this
// digest together.
const auditPolicySHA256 = "def8b1292c9ccc03627dc599642937c1e7740a535955e4c9d5f054f49ab141ee"

const auditPolicyKey = "audit-policy-file: ${RANCHER_DIR}/kube-dc-audit-policy.yaml"

// TestEmbeddedInstaller_AuditPolicy pins the audit policy every new control
// plane gets. The file must exist before kube-apiserver starts, so it is written
// before config.yaml; both config templates (first server and joining server)
// name it.
func TestEmbeddedInstaller_AuditPolicy(t *testing.T) {
	script := string(installServerScript)
	lines := strings.Split(script, "\n")
	start, end, generate := -1, -1, -1
	for i, line := range lines {
		switch {
		case start < 0 && line == `cat > "${RANCHER_DIR}/kube-dc-audit-policy.yaml" <<'AUDIT_POLICY_EOF'`:
			start = i
		case start >= 0 && end < 0 && line == "AUDIT_POLICY_EOF":
			end = i
		case line == "# Generate config.yaml":
			generate = i
		}
	}
	if start < 0 || end < 0 {
		t.Fatal("installer no longer writes the audit policy through a quoted AUDIT_POLICY_EOF heredoc")
	}
	if generate < 0 || end > generate {
		t.Fatal("installer must write the audit policy before it generates config.yaml")
	}
	policy := strings.Join(lines[start+1:end], "\n") + "\n"
	sum := sha256.Sum256([]byte(policy))
	if got := hex.EncodeToString(sum[:]); got != auditPolicySHA256 {
		t.Errorf("embedded audit policy sha256 is %s, want the reviewed %s", got, auditPolicySHA256)
	}
	if !strings.Contains(script, `chmod 0600 "${RANCHER_DIR}/kube-dc-audit-policy.yaml"`) {
		t.Error("installer no longer restricts the audit policy file to root")
	}
	if n := strings.Count(script, "\n"+auditPolicyKey+"\n"); n != 2 {
		t.Errorf("found %d config templates naming the audit policy, want 2 (first server and joining server)", n)
	}
}
