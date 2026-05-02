package screens

import (
	"testing"

	"github.com/shalb/kube-dc/cli/internal/kubeconfig"
)

// TestClassifyContext is the load-bearing test for §16.6: an admin
// row is admin, a tenant row is tenant, an external row never
// surfaces as a kube-dc identity even when its name superficially
// resembles one. Failing this means the destructive `d` action could
// hit a context the operator manages.
func TestClassifyContext(t *testing.T) {
	execKubeDC := func(args ...string) *kubeconfig.User {
		return &kubeconfig.User{
			Exec: &kubeconfig.ExecConfig{Command: "kube-dc", Args: append([]string{"credential"}, args...)},
		}
	}
	execOther := &kubeconfig.User{
		Exec: &kubeconfig.ExecConfig{Command: "aws", Args: []string{"eks", "get-token"}},
	}
	staticUser := kubeconfig.User{} // no exec, no token (model only inspects Exec)

	cases := []struct {
		name     string
		ctxName  string
		cluster  kubeconfig.Cluster
		user     kubeconfig.User
		wantID   Identity
		wantRealm string
	}{
		{
			name:    "admin via --realm master",
			ctxName: "kube-dc/kube-dc.cloud/admin",
			cluster: kubeconfig.Cluster{Server: "https://kube-api.kube-dc.cloud:6443"},
			user:    *execKubeDC("--server", "https://kube-api.kube-dc.cloud:6443", "--realm", "master"),
			wantID:  IdentityAdmin, wantRealm: "master",
		},
		{
			name:    "admin via /admin name suffix (legacy fallback)",
			ctxName: "kube-dc/example.com/admin",
			cluster: kubeconfig.Cluster{Server: "https://kube-api.example.com:6443"},
			user:    *execKubeDC("--server", "https://kube-api.example.com:6443"),
			wantID:  IdentityAdmin, wantRealm: "master",
		},
		{
			name:    "tenant pre-realm-aware login",
			ctxName: "kube-dc/stage.kube-dc.com/shalb/dev",
			cluster: kubeconfig.Cluster{Server: "https://kube-api.stage.kube-dc.com:6443"},
			user:    *execKubeDC("--server", "https://kube-api.stage.kube-dc.com:6443"),
			wantID:  IdentityTenant, wantRealm: "shalb",
		},
		{
			name:    "tenant new realm-aware login",
			ctxName: "kube-dc/stage.kube-dc.com/shalb/dev",
			cluster: kubeconfig.Cluster{Server: "https://kube-api.stage.kube-dc.com:6443"},
			user:    *execKubeDC("--server", "https://kube-api.stage.kube-dc.com:6443", "--realm", "shalb"),
			wantID:  IdentityTenant, wantRealm: "shalb",
		},
		{
			name:    "break-glass: static-token kubeconfig pointing at kube-api",
			ctxName: "cloud-break-glass",
			cluster: kubeconfig.Cluster{Server: "https://kube-api.kube-dc.cloud:6443"},
			user:    staticUser,
			wantID:  IdentityBreakGlass,
		},
		{
			name:    "external: foreign exec plugin (aws-eks)",
			ctxName: "my-eks-cluster",
			cluster: kubeconfig.Cluster{Server: "https://hash.gr7.us-east-1.eks.amazonaws.com"},
			user:    *execOther,
			wantID:  IdentityExternal,
		},
		{
			name:    "external: kubectx-style local-ish context",
			ctxName: "stage-admin",
			cluster: kubeconfig.Cluster{Server: "https://192.168.1.10:6443"},
			user:    staticUser,
			wantID:  IdentityExternal,
		},
		// Security boundary: a custom context name that LOOKS kube-dc-ish
		// but doesn't have the kube-dc exec plugin AND doesn't point at
		// a kube-api.* server must NOT classify as admin/tenant. Otherwise
		// the destructive `d` action could hit a context the operator owns.
		{
			name:    "spoofed name without exec plugin stays external",
			ctxName: "kube-dc/example.com/admin",
			cluster: kubeconfig.Cluster{Server: "https://malicious.example.com:6443"},
			user:    staticUser,
			wantID:  IdentityExternal,
		},
		// Edge case: spoofed name pointing at a *real* kube-api server
		// without our exec plugin — caught by the BREAK-GLASS heuristic.
		// This is acceptable because BREAK-GLASS is non-destructive
		// (deleteSelected refuses to delete it).
		{
			name:    "spoofed name pointing at kube-api becomes break-glass (non-destructive)",
			ctxName: "kube-dc/example.com/admin",
			cluster: kubeconfig.Cluster{Server: "https://kube-api.example.com:6443"},
			user:    staticUser,
			wantID:  IdentityBreakGlass,
		},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			gotID, gotRealm := classifyContext(tc.ctxName, tc.cluster, tc.user)
			if gotID != tc.wantID {
				t.Errorf("Identity = %q, want %q", gotID, tc.wantID)
			}
			if tc.wantRealm != "" && gotRealm != tc.wantRealm {
				t.Errorf("Realm = %q, want %q", gotRealm, tc.wantRealm)
			}
		})
	}
}

func TestTenantRealmFromName(t *testing.T) {
	cases := map[string]string{
		"kube-dc/stage.kube-dc.com/shalb/dev":   "shalb",
		"kube-dc/kube-dc.cloud/shalb/jumbolot":  "shalb",
		"kube-dc/kube-dc.cloud/admin":           "admin",
		"kube-dc/foo":                           "",
		"kube-dc":                               "",
		"":                                      "",
		"some-other-context":                    "",
	}
	for in, want := range cases {
		if got := tenantRealmFromName(in); got != want {
			t.Errorf("tenantRealmFromName(%q) = %q, want %q", in, got, want)
		}
	}
}
