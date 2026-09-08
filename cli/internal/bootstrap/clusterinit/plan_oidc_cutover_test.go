package clusterinit

import "testing"

// The OIDC cutover runs automatically during finalize; --no-oidc-cutover opts
// out. That gate changes what the apply DOES, so it must participate in the
// input hash — otherwise an operator can review a plan that promises to wire
// the apiservers and then apply one that does not, and be handed a cluster
// that is green in every check and rejects every login.
//
// (The C4 re-entrance lint already forces the field into one of the two
// buckets; this test pins WHICH bucket.)
func TestInputHash_NoOIDCCutoverIsSubstantive(t *testing.T) {
	fleet := atlantisFleet()

	base, err := BuildPlan(atlantisOpts(), fleet)
	if err != nil {
		t.Fatalf("BuildPlan(base): %v", err)
	}

	opted := atlantisOpts()
	opted.NoOIDCCutover = true
	skipped, err := BuildPlan(opted, fleet)
	if err != nil {
		t.Fatalf("BuildPlan(--no-oidc-cutover): %v", err)
	}

	if base.InputHash == skipped.InputHash {
		t.Fatal("--no-oidc-cutover did not change the plan input hash; it must be a hashed input, not an apply-flow flag")
	}
}
