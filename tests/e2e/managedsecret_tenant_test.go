/*
Copyright Kube-DC 2025.

Licensed under the Apache License, Version 2.0 (the "License");
you may not use this file except in compliance with the License.
*/

// M1-T12b — tenant-side E2E for the Secrets Manager vertical. Where
// T12 first cut (managedsecret_test.go) covered the CONTROLLER half
// using cluster-admin RBAC, this file covers the TENANT half: the
// backend's authorisation contract, the OpenBao OIDC-role gating,
// the import saga, the consumer scanner, and the org-admin elevation
// flow — all via JWTs minted from a real Keycloak.
//
// E-numbering follows openbao-integration-development-scope.md §12:
//
//   E01 — cross-org isolation                      (deferred; needs Org B)
//   E02 — cross-project value reads denied          ✓ (helper Skips if no Project B in same org)
//   E03 — developer put + get in own project        ✓
//   E04 — viewer cannot reveal values               ✓
//   E05 — org-admin elevation                       ✓ (E05-a audit-stamp mode; E05-b gate mode is opt-in via env)
//   E07 — import existing K8s Secret                ✓
//   E13 — OrganizationGroup → policy propagation    ✓
//   E15 — used-by consumer scanner                  ✓
//
// Knobs (all have sensible defaults that match the cloud cluster):
//
//   KUBE_DC_E2E_ORG               default "shalb"     — target tenant realm
//   KUBE_DC_E2E_PROJECT           default "docs"      — primary test project (kept clean)
//   KUBE_DC_E2E_SECONDARY_PROJECT default "jumbolot"  — for E02 cross-project denial (Skip if absent)
//   KUBE_DC_E2E_DOMAIN            default "kube-dc.cloud" — backend hostname suffix
//   KUBE_DC_E2E_KEEP_USERS        default ""          — non-empty: skip AfterSuite user deletion (debugging)
//
// Provisioning paths (the suite tries each in order):
//
//   1. KUBE_DC_E2E_DEV_JWT / KUBE_DC_E2E_VIEWER_JWT / KUBE_DC_E2E_ORGADMIN_JWT —
//      pre-acquired tokens. Per-spec dependency: if KUBE_DC_E2E_DEV_JWT
//      is unset, the dev specs Skip(). Same for viewer + org-admin.
//      Operators acquire these via `kube-dc login --org … --username …`
//      and the credential cache under ~/.kube-dc/credentials/.
//   2. KUBE_DC_E2E_REALM_ADMIN_USER / KUBE_DC_E2E_REALM_ADMIN_PASSWORD —
//      override the realm-access Secret when the cluster's stored
//      password has drifted from Keycloak (real-world clusters
//      sometimes rotate realm-admin credentials out-of-band).
//   3. Read `<org>/realm-access` Secret — the default path. Fails
//      gracefully when the secret is absent or the password is stale.
//
// The suite creates two OrganizationGroup CRs on first run
// (`kube-dc-e2e-developer`, `kube-dc-e2e-viewer`) when provisioning
// is possible; AfterSuite deletes them. It reuses any pre-existing
// `org-admin` OrganizationGroup (the suite does NOT create one,
// because that already exists in production orgs and overwriting it
// would be intrusive).

package e2e

import (
	"context"
	"fmt"
	"os"
	"time"

	"github.com/Nerzal/gocloak/v13"
	. "github.com/onsi/ginkgo/v2"
	. "github.com/onsi/gomega"

	corev1 "k8s.io/api/core/v1"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/types"

	kubedccomv1 "github.com/shalb/kube-dc/api/kube-dc.com/v1"
	"github.com/shalb/kube-dc/tests/e2e/helpers"
)

// ---- env knobs + lazily-loaded fixture state ------------------------

type tenantFixture struct {
	org              string
	project          string
	secondaryProject string // optional; "" if not provisioned
	projectNS        string // <org>-<project>
	domain           string

	admin *helpers.KeycloakAdmin

	// Per-role test users + their authed backend clients. Populated
	// in BeforeSuite; cleared in AfterSuite.
	devUser    testIdentity
	viewerUser testIdentity
	adminUser  testIdentity

	keepUsers bool
}

type testIdentity struct {
	Username string
	Email    string
	UserID   string // Keycloak user id (for group reconciliation)
	JWT      string
	Backend  *helpers.BackendClient
}

func envOrDefault(k, dflt string) string {
	if v := os.Getenv(k); v != "" {
		return v
	}
	return dflt
}

// devGroupName + viewerGroupName are the OrganizationGroup names this
// suite creates. Both go into <org>/<groupName> so they don't collide
// with tenant fixtures.
const (
	devGroupName    = "kube-dc-e2e-developer"
	viewerGroupName = "kube-dc-e2e-viewer"

	// User identities — emails align with kube-dc.cloud since that's
	// the realm-display-domain on every cluster the suite targets.
	devUsername    = "kube-dc-e2e-developer"
	viewerUsername = "kube-dc-e2e-viewer"
	adminUsername  = "kube-dc-e2e-orgadmin"

	// Backend gate enforcement requires a non-default env var; the
	// E05-b gate spec opts in on this knob.
	envElevationEnforce = "KUBE_DC_E2E_ELEVATION_ENFORCE"

	// Pre-acquired JWT fallbacks (used when realm-admin provisioning
	// isn't possible — e.g. stale realm-access password). Setting one
	// of these makes the corresponding identity available even if
	// dynamic provisioning fails.
	envDevJWT      = "KUBE_DC_E2E_DEV_JWT"
	envViewerJWT   = "KUBE_DC_E2E_VIEWER_JWT"
	envOrgAdminJWT = "KUBE_DC_E2E_ORGADMIN_JWT"

	// Realm-admin override (used when the realm-access Secret's
	// password has drifted from Keycloak).
	envRealmAdminUser     = "KUBE_DC_E2E_REALM_ADMIN_USER"
	envRealmAdminPassword = "KUBE_DC_E2E_REALM_ADMIN_PASSWORD"
)

var tenant *tenantFixture

// tenantSkipReason is set when BeforeSuite can't fully bring up the
// tenant fixture (e.g. realm-access Secret missing on a fresh cluster);
// every spec checks it and Skip()s with the recorded reason rather
// than failing.
var tenantSkipReason string

// ---- BeforeSuite / AfterSuite scaffolding ---------------------------
//
// Ginkgo allows exactly one BeforeSuite per binary. The existing one
// in e2e_suite_test.go bootstraps the K8s client + scheme; this file
// wires a small DeferCleanup-style hook into it via setupT12bTenant /
// teardownT12bTenant which the main BeforeSuite calls. Splitting like
// this keeps the T12 + T12b lifecycle decoupled from the (much older)
// Organization/Project/Workload suites.

// setupT12bTenant brings up the tenant fixture. It first checks the
// pre-acquired-JWT env vars and uses them if set. For any role that
// doesn't have a JWT pre-set, it tries to provision a user via the
// realm-admin token (either the env-override credentials or the
// realm-access Secret). Errors are recorded in tenantSkipReason so
// the specs Skip() with a clear reason instead of failing inside
// helper code.
func setupT12bTenant() {
	if k8sClient == nil {
		tenantSkipReason = "k8s client not initialised in BeforeSuite"
		return
	}
	ctxLocal, cancel := context.WithTimeout(context.Background(), 2*time.Minute)
	defer cancel()

	tenant = &tenantFixture{
		org:              envOrDefault("KUBE_DC_E2E_ORG", "shalb"),
		project:          envOrDefault("KUBE_DC_E2E_PROJECT", "docs"),
		secondaryProject: envOrDefault("KUBE_DC_E2E_SECONDARY_PROJECT", "jumbolot"),
		domain:           envOrDefault("KUBE_DC_E2E_DOMAIN", "kube-dc.cloud"),
		keepUsers:        os.Getenv("KUBE_DC_E2E_KEEP_USERS") != "",
	}
	tenant.projectNS = tenant.org + "-" + tenant.project

	// Verify the target project namespace exists; otherwise every
	// spec will fail with a confusing error.
	probe := &corev1.Namespace{}
	if err := k8sClient.Get(ctxLocal, types.NamespacedName{Name: tenant.projectNS}, probe); err != nil {
		tenantSkipReason = fmt.Sprintf("project namespace %q not present; set KUBE_DC_E2E_ORG/PROJECT to a real project", tenant.projectNS)
		return
	}

	// Pre-acquired JWT fallback: take any env-supplied tokens first.
	// Specs that have a JWT can run even if realm-admin provisioning
	// fails downstream.
	if jwt := os.Getenv(envDevJWT); jwt != "" {
		tenant.devUser = identityFromJWT(devUsername, jwt, tenant.domain)
		Logf("T12b: using pre-acquired developer JWT from %s", envDevJWT)
	}
	if jwt := os.Getenv(envViewerJWT); jwt != "" {
		tenant.viewerUser = identityFromJWT(viewerUsername, jwt, tenant.domain)
		Logf("T12b: using pre-acquired viewer JWT from %s", envViewerJWT)
	}
	if jwt := os.Getenv(envOrgAdminJWT); jwt != "" {
		tenant.adminUser = identityFromJWT(adminUsername, jwt, tenant.domain)
		Logf("T12b: using pre-acquired org-admin JWT from %s", envOrgAdminJWT)
	}

	// If every identity is satisfied by env vars, skip the
	// realm-admin login path entirely — useful when the realm-access
	// password has drifted on the target cluster.
	if tenant.devUser.JWT != "" && tenant.viewerUser.JWT != "" && tenant.adminUser.JWT != "" {
		Logf("T12b: all identities supplied via env vars; skipping Keycloak provisioning")
		return
	}

	By("T12b: ensuring OrganizationGroup fixtures for developer + viewer roles")
	for _, og := range []*securityFixtureGroup{
		{name: devGroupName, project: tenant.project, role: "developer"},
		{name: viewerGroupName, project: tenant.project, role: "user"},
	} {
		if err := ensureOrgGroup(ctxLocal, og, tenant.org); err != nil {
			tenantSkipReason = fmt.Sprintf("ensure OrganizationGroup %s: %v", og.name, err)
			return
		}
	}

	By("T12b: authenticating as realm-admin to provision test users")
	admin, err := newKeycloakAdminWithOverride(ctxLocal, tenant.org)
	if err != nil {
		Logf("T12b: realm-admin login failed (%v) — specs using unprovisioned identities will Skip", err)
		// Don't set tenantSkipReason: some specs may still run via
		// env-supplied JWTs already populated above.
		return
	}
	tenant.admin = admin

	// Wait briefly for Keycloak group sync. The OrganizationGroup
	// controller creates the group on the next reconcile (sub-30s
	// typically). We poll until both names resolve.
	By("T12b: waiting for Keycloak groups to materialise (Org→KC sync)")
	Eventually(func() error {
		groups, err := admin.GoCloak.GetGroups(ctxLocal, admin.AccessToken, tenant.org, gocloakNameQuery(devGroupName))
		if err != nil {
			return err
		}
		if len(groups) == 0 {
			return fmt.Errorf("developer group not yet present in Keycloak")
		}
		groups, _ = admin.GoCloak.GetGroups(ctxLocal, admin.AccessToken, tenant.org, gocloakNameQuery(viewerGroupName))
		if len(groups) == 0 {
			return fmt.Errorf("viewer group not yet present in Keycloak")
		}
		return nil
	}, 60*time.Second, 3*time.Second).Should(Succeed())

	// Provision identities that aren't already satisfied by env JWTs.
	By("T12b: provisioning missing test identities via Keycloak")
	if tenant.devUser.JWT == "" {
		tenant.devUser = provisionIdentity(ctxLocal, admin, tenant.org, devUsername, []string{devGroupName}, tenant.domain)
	}
	if tenant.viewerUser.JWT == "" {
		tenant.viewerUser = provisionIdentity(ctxLocal, admin, tenant.org, viewerUsername, []string{viewerGroupName}, tenant.domain)
	}
	if tenant.adminUser.JWT == "" && hasOrgGroup(ctxLocal, tenant.org, "org-admin") {
		tenant.adminUser = provisionIdentity(ctxLocal, admin, tenant.org, adminUsername, []string{"org-admin"}, tenant.domain)
	} else if tenant.adminUser.JWT == "" {
		Logf("T12b: no `org-admin` OrganizationGroup in %s — E05 org-admin specs will Skip", tenant.org)
	}
}

// identityFromJWT builds a testIdentity from a raw JWT (env-supplied
// fallback path). The UserID is left empty because the spec can't
// rebind groups on a user we didn't provision (E13 specifically
// requires a provisioned UserID; it Skip()s when missing).
func identityFromJWT(username, jwt, domain string) testIdentity {
	return testIdentity{
		Username: username,
		JWT:      jwt,
		Backend:  helpers.NewBackendClient(domain, jwt),
	}
}

// newKeycloakAdminWithOverride tries the env-var override credentials
// first (KUBE_DC_E2E_REALM_ADMIN_USER/PASSWORD), falling back to the
// realm-access Secret. This is the escape hatch for clusters whose
// stored password has drifted from Keycloak.
func newKeycloakAdminWithOverride(ctx context.Context, realm string) (*helpers.KeycloakAdmin, error) {
	return helpers.NewKeycloakAdminWithOverride(ctx, k8sClient, realm,
		os.Getenv(envRealmAdminUser),
		os.Getenv(envRealmAdminPassword),
	)
}

// teardownT12bTenant cleans up the Keycloak users + OrganizationGroup
// CRs the suite created. Called from e2e_suite_test.go's AfterSuite.
// Idempotent — safe if setup partially completed before erroring.
func teardownT12bTenant() {
	if tenant == nil || tenant.admin == nil {
		return
	}
	if tenant.keepUsers {
		Logf("T12b: KUBE_DC_E2E_KEEP_USERS set; leaving test users behind")
		return
	}
	ctxLocal, cancel := context.WithTimeout(context.Background(), 1*time.Minute)
	defer cancel()
	for _, u := range []string{devUsername, viewerUsername, adminUsername} {
		if err := tenant.admin.DeleteUser(ctxLocal, u); err != nil {
			Logf("T12b cleanup: delete user %s: %v (non-fatal)", u, err)
		}
	}
	// OrganizationGroup CRs cleanup (cluster-admin RBAC, K8s client).
	for _, name := range []string{devGroupName, viewerGroupName} {
		og := &kubedccomv1.OrganizationGroup{
			ObjectMeta: metav1.ObjectMeta{Name: name, Namespace: tenant.org},
		}
		if err := k8sClient.Delete(ctxLocal, og); err != nil && !apierrors.IsNotFound(err) {
			Logf("T12b cleanup: delete OrganizationGroup %s: %v (non-fatal)", name, err)
		}
	}
}

// ---- shared helpers --------------------------------------------------

type securityFixtureGroup struct {
	name    string
	project string
	role    string // "developer" | "user" | "admin" | "project-manager"
}

func ensureOrgGroup(ctx context.Context, fx *securityFixtureGroup, orgNS string) error {
	og := &kubedccomv1.OrganizationGroup{}
	key := types.NamespacedName{Name: fx.name, Namespace: orgNS}
	err := k8sClient.Get(ctx, key, og)
	if err == nil {
		// Already exists — assume the spec matches. We deliberately
		// don't reconcile the spec to avoid clobbering operator-edited
		// fixtures.
		return nil
	}
	if !apierrors.IsNotFound(err) {
		return err
	}
	og = &kubedccomv1.OrganizationGroup{
		ObjectMeta: metav1.ObjectMeta{
			Name:      fx.name,
			Namespace: orgNS,
			Annotations: map[string]string{
				"kube-dc.com/description":  "T12b E2E fixture — auto-cleaned on suite teardown",
				"kube-dc.com/display-name": fx.name,
			},
		},
		Spec: kubedccomv1.OrganizationGroupSpec{
			Permissions: []kubedccomv1.OrganizationRoleRef{
				{Project: fx.project, Roles: []string{fx.role}},
			},
		},
	}
	return k8sClient.Create(ctx, og)
}

func hasOrgGroup(ctx context.Context, orgNS, name string) bool {
	og := &kubedccomv1.OrganizationGroup{}
	return k8sClient.Get(ctx, types.NamespacedName{Name: name, Namespace: orgNS}, og) == nil
}

func gocloakNameQuery(name string) gocloakGroupsParams {
	return gocloakGroupsParams{Search: &name}
}

// gocloakGroupsParams aliases gocloak.GetGroupsParams to avoid pulling
// the gocloak import into every helper call site; defined inline so
// the import-block stays tight.
type gocloakGroupsParams = gocloak.GetGroupsParams

func provisionIdentity(ctx context.Context, admin *helpers.KeycloakAdmin, org, username string, groups []string, domain string) testIdentity {
	email := username + "@" + domain
	userID, err := admin.EnsureUser(ctx, username, email, groups)
	Expect(err).NotTo(HaveOccurred(), "ensure test user %s in realm %s", username, org)
	jwt, err := admin.LoginAsUser(ctx, username)
	Expect(err).NotTo(HaveOccurred(), "login as test user %s in realm %s", username, org)
	return testIdentity{
		Username: username,
		Email:    email,
		UserID:   userID,
		JWT:      jwt,
		Backend:  helpers.NewBackendClient(domain, jwt),
	}
}

// requireTenant Skip()s the current spec when BeforeSuite couldn't
// finish provisioning (missing realm-access, project namespace
// absent, …). Every spec should call this first so a misconfigured
// environment fails fast with a clear reason rather than a confusing
// nil-deref deep in the helper code.
func requireTenant() {
	if tenantSkipReason != "" {
		Skip(tenantSkipReason)
	}
}

// requireIdentity Skip()s the spec when a specific role's identity
// isn't available (neither env-supplied nor provisioned). Each role
// has its own opt-in env var that operators can set per-cluster.
func requireDev() {
	requireTenant()
	if tenant.devUser.JWT == "" {
		Skip(fmt.Sprintf("developer identity unavailable; set %s or fix realm-admin provisioning", envDevJWT))
	}
}

func requireViewer() {
	requireTenant()
	if tenant.viewerUser.JWT == "" {
		Skip(fmt.Sprintf("viewer identity unavailable; set %s or fix realm-admin provisioning", envViewerJWT))
	}
}

func requireOrgAdmin() {
	requireTenant()
	if tenant.adminUser.JWT == "" {
		Skip(fmt.Sprintf("org-admin identity unavailable; set %s or apply an `org-admin` OrganizationGroup CR", envOrgAdminJWT))
	}
}

// uniqueName builds a deterministic-but-collision-resistant resource
// name. The nanosecond suffix means consecutive specs in a single
// suite run get distinct names even if they run inside the same
// millisecond on a fast machine.
func uniqueName(prefix string) string {
	return fmt.Sprintf("%s-%d", prefix, time.Now().UnixNano())
}

// ---- gocloak import shim ---------------------------------------------
//
// We type-alias gocloak.GetGroupsParams above so the helpers/keycloak.go
// public API stays gocloak-free. The actual gocloak import lives in
// this small bottom block so the rest of the spec file doesn't need
// to know about it.

// Note: gocloak is already a dependency (see organization_test.go),
// so this adds zero new module weight.
