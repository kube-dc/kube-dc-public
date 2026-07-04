// Package openbao — embedded HCL policy files for the controller-auth
// setup ceremony. The .hcl files in ./policies/ are the source of
// truth; they're embedded into the CLI binary at build time so a
// fleet checkout doesn't need to track the HCL alongside
// secrets.enc.yaml (avoids HCL-vs-CLI version drift).
//
// Loaded by SetupControllerAuth (engine) and by init Phase C.
package openbao

import _ "embed"

// ManagerPolicyHCL is the cross-Org admin policy for kube-dc-controller-manager.
// Embedded from policies/kube-dc-controller-manager.hcl — see that file for
// the path-by-path rationale.
//
//go:embed policies/kube-dc-controller-manager.hcl
var ManagerPolicyHCL string

// DBManagerPolicyHCL is the M3-T05 / M4-T01 policy for kube-dc-db-manager.
// Embedded from policies/db-manager.hcl. Tight scope: per-Org Transit
// encrypt/decrypt + Database engine mount/config; NEVER rotates or
// destroys keys (that surface stays on the controller-manager).
//
//go:embed policies/db-manager.hcl
var DBManagerPolicyHCL string

// Hardcoded SA + role parameters for the two roles. These are
// chart-shape invariants — changing them is a chart-coordinated
// breaking change that warrants a separate PR, NOT a flag here.
const (
	ManagerPolicyName = "kube-dc-controller-manager"
	ManagerRoleName   = "kube-dc-controller-manager"
	ManagerSAName     = "kube-dc-manager"
	ManagerSAns       = "kube-dc"

	DBManagerPolicyName = "db-manager"
	DBManagerRoleName   = "db-manager"
	DBManagerSAName     = "kube-dc-db-manager"
	DBManagerSAns       = "kube-dc"

	// Auth mount path under the root namespace. Hardcoded to match
	// the manager-side login at internal/openbao/auth.go.
	KubernetesAuthPath = "k8s-host"

	// Token lifetime on the issued client tokens (auth role). 1h
	// initial TTL, 24h cap — same as the shell script. The controller
	// renews on a sliding window via the auth/token/renew-self grant
	// embedded in both policies.
	TokenTTL    = "1h"
	TokenMaxTTL = "24h"

	// Annotations stamped on svc/openbao for idempotence detection.
	AnnotationBootstrapFinalized       = "kube-dc.com/openbao-bootstrap-finalized"
	AnnotationControllerAuthInstalled  = "kube-dc.com/openbao-controller-auth-installed"
)

// ManagerRoleParams returns the bao-write key=value pairs for the
// controller-manager role under auth/k8s-host/role/<role>. Caller
// passes these straight to OpenBaoClient.WriteAuthRole.
func ManagerRoleParams() map[string]string {
	return map[string]string{
		"bound_service_account_names":      ManagerSAName,
		"bound_service_account_namespaces": ManagerSAns,
		"policies":                         ManagerPolicyName,
		"token_ttl":                        TokenTTL,
		"token_max_ttl":                    TokenMaxTTL,
	}
}

// DBManagerRoleParams returns the bao-write key=value pairs for the
// db-manager role.
func DBManagerRoleParams() map[string]string {
	return map[string]string{
		"bound_service_account_names":      DBManagerSAName,
		"bound_service_account_namespaces": DBManagerSAns,
		"policies":                         DBManagerPolicyName,
		"token_ttl":                        TokenTTL,
		"token_max_ttl":                    TokenMaxTTL,
	}
}
