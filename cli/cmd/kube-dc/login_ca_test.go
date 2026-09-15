package main

import (
	"context"
	"encoding/base64"
	"encoding/pem"
	"errors"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"strings"
	"testing"

	"github.com/shalb/kube-dc/cli/internal/auth"
	"github.com/shalb/kube-dc/cli/internal/kubeconfig"
)

func TestFreshLoginDiscoversAndEmbedsAPICA(t *testing.T) {
	for _, realm := range []string{"acme", "master"} {
		t.Run(realm, func(t *testing.T) {
			t.Setenv("KUBECONFIG", filepath.Join(t.TempDir(), "config"))
			api := httptest.NewTLSServer(http.HandlerFunc(func(http.ResponseWriter, *http.Request) {}))
			defer api.Close()
			ca := string(pem.EncodeToMemory(&pem.Block{Type: "CERTIFICATE", Bytes: api.Certificate().Raw}))
			calls := 0
			resolved, err := resolveAPICA(context.Background(), api.URL, "", "", auth.APIConnection{}, func(context.Context) (string, error) {
				calls++
				return ca, nil
			})
			if err != nil || calls != 1 || resolved.CACert != ca || resolved.UseSystemCA {
				t.Fatalf("fresh trust resolution failed: calls=%d err=%v", calls, err)
			}
			mgr, err := kubeconfig.NewManager()
			if err != nil {
				t.Fatal(err)
			}
			if err := mgr.AddKubeDCContext(kubeconfig.AddContextParams{
				Server: api.URL, ClusterName: "cluster", ContextName: realm, UserName: realm, Realm: realm, CACert: resolved.CACert, UseSystemCA: resolved.UseSystemCA,
			}); err != nil {
				t.Fatal(err)
			}
			cfg, err := mgr.Load()
			if err != nil {
				t.Fatal(err)
			}
			cluster := cfg.Clusters[0].Cluster
			if cluster.InsecureSkipTLSVerify {
				t.Fatal("fresh kubeconfig disabled TLS verification")
			}
			bundle, err := base64.StdEncoding.DecodeString(cluster.CertificateAuthorityData)
			if err != nil {
				t.Fatal(err)
			}
			if err := auth.VerifyAPIServer(context.Background(), cluster.Server, string(bundle)); err != nil {
				t.Fatal(err)
			}
			args := strings.Join(cfg.Users[0].User.Exec.Args, " ")
			if !strings.Contains(args, "--realm "+realm) {
				t.Fatalf("wrong exec identity: %s", args)
			}
		})
	}
}

func TestLoginAPICATrustPolicy(t *testing.T) {
	api := httptest.NewTLSServer(http.HandlerFunc(func(http.ResponseWriter, *http.Request) {}))
	defer api.Close()
	ca := string(pem.EncodeToMemory(&pem.Block{Type: "CERTIFICATE", Bytes: api.Certificate().Raw}))
	for _, tc := range []struct {
		name, provided, existing, discovered string
		discoveryErr                         error
		wantCalls                            int
		wantErr                              bool
	}{
		{name: "configured trust needs no backend", existing: ca},
		{name: "explicit trust needs no backend", provided: ca},
		{name: "explicit trust cannot be overridden", provided: "invalid", discovered: ca, wantErr: true},
		{name: "stale trust refreshed", existing: "invalid", discovered: ca, wantCalls: 1},
		{name: "missing discovery fails closed", discoveryErr: errors.New("HTTP 404"), wantCalls: 1, wantErr: true},
		{name: "invalid discovery fails closed", discovered: "invalid", wantCalls: 1, wantErr: true},
		{name: "empty discovery fails closed", wantCalls: 1, wantErr: true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			calls := 0
			got, err := resolveAPICA(context.Background(), api.URL, tc.provided, tc.existing, auth.APIConnection{}, func(context.Context) (string, error) { calls++; return tc.discovered, tc.discoveryErr })
			if (err != nil) != tc.wantErr || calls != tc.wantCalls {
				t.Fatalf("calls=%d error=%v", calls, err)
			}
			wantCA := ca
			if tc.name == "configured trust needs no backend" {
				wantCA = "" // Reuse trust without converting a file reference to data.
			}
			if !tc.wantErr && (got.CACert != wantCA || got.UseSystemCA) {
				t.Fatal("wrong CA selected")
			}
		})
	}
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	_, err := resolveAPICA(ctx, api.URL, "", "", auth.APIConnection{}, func(context.Context) (string, error) { t.Fatal("discovery called after cancellation"); return "", nil })
	if !errors.Is(err, context.Canceled) {
		t.Fatalf("expected cancellation, got %v", err)
	}
}
