/*
Copyright Kube-DC 2025.

Licensed under the Apache License, Version 2.0 (the "License");
you may not use this file except in compliance with the License.
*/

// Package helpers provisions test users in a Kube-DC org's Keycloak
// realm and acquires JWTs for them so the M1-T12b tenant-side E2E
// specs can talk to the backend as a real user instead of going
// through the cluster-admin kubeconfig.
//
// Realm-admin credentials live in the Secret `<org>/realm-access`
// (same Secret organization_test.go uses); the E2E rig already runs
// under cluster-admin RBAC, so reading it at runtime is fine. The
// helpers DO NOT need master-realm admin access — every action is
// scoped to a single tenant realm.

package helpers

import (
	"context"
	"crypto/tls"
	"fmt"
	"net/http"
	"net/url"
	"strings"
	"time"

	"github.com/Nerzal/gocloak/v13"
	corev1 "k8s.io/api/core/v1"
	"k8s.io/apimachinery/pkg/types"
	"sigs.k8s.io/controller-runtime/pkg/client"
)

// e2eUserPassword is the fixed password we assign to every test user.
// It's intentionally unguessable but committed — these are throwaway
// accounts in a single test realm and never carry real permissions
// outside the spec under test. Cluster operators rotate it by
// editing this file if their security policy requires it.
const e2eUserPassword = "kube-dc-e2e-passw0rd-Tk7m4nQ" //nolint:gosec // test fixture

// KeycloakAdmin holds the realm-admin context for a single org.
// Every method operates inside that realm; cross-realm work needs a
// second instance.
type KeycloakAdmin struct {
	Realm string

	// Embedded gocloak client. Tests can reach in for ops we haven't
	// wrapped (group CRUD, role binding, etc.) without us re-exporting
	// every method.
	GoCloak     *gocloak.GoCloak
	BaseURL     string
	AccessToken string
}

// NewKeycloakAdminWithOverride is the same as NewKeycloakAdmin but
// honours caller-supplied user/password if both are non-empty. Used
// by the E2E suite to escape stale realm-access Secrets without
// editing the cluster.
func NewKeycloakAdminWithOverride(ctx context.Context, k8sClient client.Client, realm, overrideUser, overridePassword string) (*KeycloakAdmin, error) {
	if overrideUser != "" && overridePassword != "" {
		// We still read the Secret for the URL (the env vars only
		// override the credentials, not the endpoint). Clusters
		// without realm-access at all should provide a separate URL
		// env var if that becomes a real ask.
		sec := &corev1.Secret{}
		key := types.NamespacedName{Name: "realm-access", Namespace: realm}
		if err := k8sClient.Get(ctx, key, sec); err != nil {
			return nil, fmt.Errorf("get realm-access (for URL only): %w", err)
		}
		rawURL := string(sec.Data["url"])
		parsed, err := url.Parse(rawURL)
		if err != nil {
			return nil, fmt.Errorf("parse realm-access URL %q: %w", rawURL, err)
		}
		baseURL := fmt.Sprintf("%s://%s", parsed.Scheme, parsed.Host)
		gc := gocloak.NewClient(baseURL)
		gc.RestyClient().SetTLSClientConfig(&tls.Config{InsecureSkipVerify: true}) //nolint:gosec // tests only
		token, err := gc.LoginAdmin(ctx, overrideUser, overridePassword, realm)
		if err != nil {
			return nil, fmt.Errorf("keycloak realm-admin login (override) as %s in realm %s: %w", overrideUser, realm, err)
		}
		return &KeycloakAdmin{
			Realm:       realm,
			GoCloak:     gc,
			BaseURL:     baseURL,
			AccessToken: token.AccessToken,
		}, nil
	}
	return NewKeycloakAdmin(ctx, k8sClient, realm)
}

// NewKeycloakAdmin loads the `<realm>/realm-access` Secret via the
// in-test k8s client, parses the console URL to its base, and logs
// in as realm-admin. The token is short-lived (5 min default per
// Keycloak); callers should NOT cache the returned KeycloakAdmin
// across spec boundaries — call NewKeycloakAdmin afresh in
// BeforeSuite or wherever you need it.
func NewKeycloakAdmin(ctx context.Context, k8sClient client.Client, realm string) (*KeycloakAdmin, error) {
	sec := &corev1.Secret{}
	key := types.NamespacedName{Name: "realm-access", Namespace: realm}
	if err := k8sClient.Get(ctx, key, sec); err != nil {
		return nil, fmt.Errorf("get realm-access secret %s: %w", key, err)
	}
	rawURL := string(sec.Data["url"])
	username := string(sec.Data["user"])
	password := string(sec.Data["password"])
	if rawURL == "" || username == "" || password == "" {
		return nil, fmt.Errorf("realm-access secret %s missing url/user/password", key)
	}
	parsed, err := url.Parse(rawURL)
	if err != nil {
		return nil, fmt.Errorf("parse realm-access URL %q: %w", rawURL, err)
	}
	baseURL := fmt.Sprintf("%s://%s", parsed.Scheme, parsed.Host)

	gc := gocloak.NewClient(baseURL)
	// Some clusters serve Keycloak with the cluster's private CA. The
	// E2E rig already runs from inside the cluster or via the tunnel;
	// disable TLS verification on the gocloak http client to match
	// the rest of the suite's TLS-permissive defaults (kube-apiserver
	// access is also typically TLS-permissive in test mode).
	gc.RestyClient().SetTLSClientConfig(&tls.Config{InsecureSkipVerify: true}) //nolint:gosec // tests only
	token, err := gc.LoginAdmin(ctx, username, password, realm)
	if err != nil {
		return nil, fmt.Errorf("keycloak realm-admin login as %s in realm %s: %w", username, realm, err)
	}
	return &KeycloakAdmin{
		Realm:       realm,
		GoCloak:     gc,
		BaseURL:     baseURL,
		AccessToken: token.AccessToken,
	}, nil
}

// EnsureUser creates the user with the given username + email in this
// realm, sets the fixed e2e password, marks email verified + enabled,
// and adds them to the named groups. Idempotent: if a user with the
// same username already exists, we reuse it (the password is reset to
// e2eUserPassword and the group bindings are reconciled to exactly
// the desired set). Returns the Keycloak user ID.
//
// `groups` are group NAMES in this realm (e.g. "developers",
// "org-admin"). The groups must exist — the Kube-DC controller
// creates them when an OrganizationGroup CR is applied, so the
// caller is responsible for ensuring the relevant CR is present.
func (k *KeycloakAdmin) EnsureUser(ctx context.Context, username, email string, groups []string) (string, error) {
	existing, err := k.GoCloak.GetUsers(ctx, k.AccessToken, k.Realm, gocloak.GetUsersParams{
		Username: gocloak.StringP(username),
	})
	if err != nil {
		return "", fmt.Errorf("lookup user %q: %w", username, err)
	}
	var userID string
	if len(existing) > 0 && existing[0].ID != nil && *existing[0].Username == username {
		userID = *existing[0].ID
	} else {
		// Create.
		userID, err = k.GoCloak.CreateUser(ctx, k.AccessToken, k.Realm, gocloak.User{
			Username:      gocloak.StringP(username),
			Email:         gocloak.StringP(email),
			EmailVerified: gocloak.BoolP(true),
			Enabled:       gocloak.BoolP(true),
		})
		if err != nil {
			return "", fmt.Errorf("create user %q: %w", username, err)
		}
	}
	// Set the fixed password (NOT temporary, so the token endpoint
	// accepts it on the first login).
	if err := k.GoCloak.SetPassword(ctx, k.AccessToken, userID, k.Realm, e2eUserPassword, false); err != nil {
		return "", fmt.Errorf("set password for %q: %w", username, err)
	}
	// Reconcile groups: add the desired set, leave others alone.
	// (We intentionally don't strip un-listed groups — a user could
	// also be in default-roles-<realm>, which we shouldn't touch.)
	desired := map[string]bool{}
	for _, g := range groups {
		desired[g] = true
	}
	allGroups, err := k.GoCloak.GetGroups(ctx, k.AccessToken, k.Realm, gocloak.GetGroupsParams{})
	if err != nil {
		return "", fmt.Errorf("list groups: %w", err)
	}
	nameToID := map[string]string{}
	for _, g := range allGroups {
		if g.Name != nil && g.ID != nil {
			nameToID[*g.Name] = *g.ID
		}
	}
	for name := range desired {
		groupID, ok := nameToID[name]
		if !ok {
			return "", fmt.Errorf("group %q does not exist in realm %s (apply the matching OrganizationGroup CR first)", name, k.Realm)
		}
		if err := k.GoCloak.AddUserToGroup(ctx, k.AccessToken, k.Realm, userID, groupID); err != nil {
			return "", fmt.Errorf("add user %q to group %q: %w", username, name, err)
		}
	}
	return userID, nil
}

// RemoveUserFromGroup unbinds a user from a single group by name.
// Used by the E13 propagation spec to verify that permission removal
// flips access from allowed → denied within the documented 30s window.
func (k *KeycloakAdmin) RemoveUserFromGroup(ctx context.Context, userID, groupName string) error {
	allGroups, err := k.GoCloak.GetGroups(ctx, k.AccessToken, k.Realm, gocloak.GetGroupsParams{})
	if err != nil {
		return fmt.Errorf("list groups: %w", err)
	}
	for _, g := range allGroups {
		if g.Name != nil && *g.Name == groupName && g.ID != nil {
			return k.GoCloak.DeleteUserFromGroup(ctx, k.AccessToken, k.Realm, userID, *g.ID)
		}
	}
	return fmt.Errorf("group %q not found in realm %s", groupName, k.Realm)
}

// AddUserToGroup is the AddUserToGroup counterpart of
// RemoveUserFromGroup; same name-resolution semantics. Used by the
// E13 propagation spec when verifying that newly-granted permissions
// take effect within 30s.
func (k *KeycloakAdmin) AddUserToGroup(ctx context.Context, userID, groupName string) error {
	allGroups, err := k.GoCloak.GetGroups(ctx, k.AccessToken, k.Realm, gocloak.GetGroupsParams{})
	if err != nil {
		return fmt.Errorf("list groups: %w", err)
	}
	for _, g := range allGroups {
		if g.Name != nil && *g.Name == groupName && g.ID != nil {
			return k.GoCloak.AddUserToGroup(ctx, k.AccessToken, k.Realm, userID, *g.ID)
		}
	}
	return fmt.Errorf("group %q not found in realm %s", groupName, k.Realm)
}

// DeleteUser removes the user from the realm. Idempotent: a 404 from
// Keycloak is treated as success so AfterSuite cleanup doesn't fail
// when an earlier spec already deleted the user.
func (k *KeycloakAdmin) DeleteUser(ctx context.Context, username string) error {
	users, err := k.GoCloak.GetUsers(ctx, k.AccessToken, k.Realm, gocloak.GetUsersParams{
		Username: gocloak.StringP(username),
	})
	if err != nil {
		return fmt.Errorf("lookup user %q: %w", username, err)
	}
	for _, u := range users {
		if u.ID == nil || u.Username == nil || *u.Username != username {
			continue
		}
		if err := k.GoCloak.DeleteUser(ctx, k.AccessToken, k.Realm, *u.ID); err != nil {
			// gocloak surfaces 404 as a wrapped error; the easiest
			// "is it not-found?" check is the message substring.
			if strings.Contains(err.Error(), "404") {
				return nil
			}
			return fmt.Errorf("delete user %q: %w", username, err)
		}
		return nil
	}
	return nil // not present == idempotent success
}

// LoginAsUser returns a fresh access token for (username, password)
// using Keycloak's direct password grant against the kube-dc client
// (client_id=kube-dc, no client secret in the public flow). The token
// claims will include `org`, `preferred_username`, `email`, and
// `groups` — the same shape the backend's decodeJwt expects.
//
// The OIDC pass-through model in OpenBao verifies this token at
// `/v1/<org>/auth/oidc-keycloak/login`, so the password grant works
// without any further setup as long as the realm's `kube-dc` client
// is public (no client secret) — which is how the chart provisions it.
func (k *KeycloakAdmin) LoginAsUser(ctx context.Context, username string) (string, error) {
	httpClient := &http.Client{
		Timeout: 30 * time.Second,
		Transport: &http.Transport{
			TLSClientConfig: &tls.Config{InsecureSkipVerify: true}, //nolint:gosec // tests only
		},
	}
	tokenURL := fmt.Sprintf("%s/realms/%s/protocol/openid-connect/token", k.BaseURL, k.Realm)
	form := url.Values{}
	form.Set("client_id", "kube-dc")
	form.Set("grant_type", "password")
	form.Set("username", username)
	form.Set("password", e2eUserPassword)
	form.Set("scope", "openid")
	req, err := http.NewRequestWithContext(ctx, "POST", tokenURL, strings.NewReader(form.Encode()))
	if err != nil {
		return "", fmt.Errorf("build token request: %w", err)
	}
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	resp, err := httpClient.Do(req)
	if err != nil {
		return "", fmt.Errorf("token request: %w", err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != 200 {
		return "", fmt.Errorf("token endpoint returned %d for user %s in realm %s", resp.StatusCode, username, k.Realm)
	}
	var body struct {
		AccessToken string `json:"access_token"`
	}
	if err := decodeJSONBody(resp, &body); err != nil {
		return "", fmt.Errorf("decode token response: %w", err)
	}
	if body.AccessToken == "" {
		return "", fmt.Errorf("token endpoint returned empty access_token for %s", username)
	}
	return body.AccessToken, nil
}

// E2EUserPassword is the constant test-user password. Exported so a
// spec can write it on a manually-provisioned user if it skips
// EnsureUser (rare; mostly for debugging).
func E2EUserPassword() string { return e2eUserPassword }
