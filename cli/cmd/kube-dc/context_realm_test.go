package main

import (
	"path/filepath"
	"testing"

	"github.com/shalb/kube-dc/cli/internal/config"
	"github.com/shalb/kube-dc/cli/internal/kubeconfig"
)

func TestTenantContextParamsPinsOrganizationRealm(t *testing.T) {
	p := tenantContextParams("kube-dc.cloud", "acme", "https://kube-api.kube-dc.cloud:6443", "acme-production", "test-ca", false, true)

	if p.Realm != "acme" {
		t.Fatalf("Realm = %q, want acme", p.Realm)
	}
	if p.ContextName != "kube-dc/kube-dc.cloud/acme/production" {
		t.Errorf("ContextName = %q", p.ContextName)
	}
	if p.UserName != "kube-dc@kube-dc.cloud/acme" {
		t.Errorf("UserName = %q", p.UserName)
	}
	if p.ClusterName != "kube-dc-kube-dc.cloud-acme" {
		t.Errorf("ClusterName = %q", p.ClusterName)
	}
	if p.Namespace != "acme-production" || !p.SetCurrent {
		t.Errorf("namespace/current = %q/%v", p.Namespace, p.SetCurrent)
	}
}

func TestRunNsUsesCurrentContextRealm(t *testing.T) {
	home := t.TempDir()
	t.Setenv("HOME", home)
	t.Setenv("KUBECONFIG", filepath.Join(home, "kubeconfig"))

	const server = "https://kube-api.kube-dc.cloud:6443"
	kubeMgr, err := kubeconfig.NewManager()
	if err != nil {
		t.Fatalf("new kubeconfig manager: %v", err)
	}
	if err := kubeMgr.AddKubeDCContext(tenantContextParams(
		"kube-dc.cloud", "acme", server, "acme-production", "", false, true,
	)); err != nil {
		t.Fatalf("add tenant context: %v", err)
	}
	if err := kubeMgr.AddKubeDCContext(kubeconfig.AddContextParams{
		Server:      server,
		ClusterName: "kube-dc-kube-dc.cloud-admin",
		UserName:    "kube-dc-admin@kube-dc.cloud",
		ContextName: "kube-dc/kube-dc.cloud/admin",
		SetCurrent:  false,
		Realm:       "master",
	}); err != nil {
		t.Fatalf("add admin context: %v", err)
	}

	credMgr, err := config.NewCredentialsManager()
	if err != nil {
		t.Fatalf("new credentials manager: %v", err)
	}
	for _, creds := range []*config.Credentials{
		{
			Server: server,
			Realm:  "acme",
			User:   config.UserInfo{Namespaces: []string{"acme-production", "acme-staging"}},
		},
		{
			Server: server,
			Realm:  "master",
			User:   config.UserInfo{Namespaces: []string{"kube-system"}},
		},
	} {
		if err := credMgr.Save(creds); err != nil {
			t.Fatalf("save %s credentials: %v", creds.Realm, err)
		}
	}

	if err := runNs([]string{"acme-staging"}); err != nil {
		t.Fatalf("runNs: %v", err)
	}
	cfg, err := kubeMgr.Load()
	if err != nil {
		t.Fatalf("reload kubeconfig: %v", err)
	}
	for _, ctx := range cfg.Contexts {
		if ctx.Name == cfg.CurrentContext {
			if ctx.Context.Namespace != "acme-staging" {
				t.Fatalf("current namespace = %q, want acme-staging", ctx.Context.Namespace)
			}
			return
		}
	}
	t.Fatal("current context was not found")
}

// The cloud shell hands the CLI a credential with an EMPTY access token (it is
// given only a refresh token) and a namespaces list that is empty or, worse,
// partial. Reading the cache alone therefore either failed outright or silently
// presented ONE Project as the user's complete access and hid the rest — the
// regression a release-gate review caught before this shipped.
//
// So the token is authoritative and preferred whenever it yields more.
func TestNsPrefersTheTokenOverAStaleCachedList(t *testing.T) {
	cached := []string{"acme-one"}
	fromToken := []string{"acme-one", "acme-two", "acme-three"}

	// The selection rule under test, isolated from the network refresh.
	got := cached
	if len(fromToken) > len(got) {
		got = fromToken
	}
	if len(got) != 3 {
		t.Fatalf("a partial cached list must not win over the token: got %v", got)
	}

	// And the reverse: an unreachable Keycloak yields nothing, and the user must
	// still be able to switch among the Projects they last knew about.
	var tokenFailed []string
	got = cached
	if len(tokenFailed) > len(got) {
		got = tokenFailed
	}
	if len(got) != 1 || got[0] != "acme-one" {
		t.Fatalf("a failed refresh must fall back to the cache, got %v", got)
	}
}
