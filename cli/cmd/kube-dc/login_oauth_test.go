package main

import (
	"context"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/shalb/kube-dc/cli/internal/auth"
)

func TestLoginOAuthDeviceMode(t *testing.T) {
	for _, identity := range []struct{ realm, client string }{{"acme", "kube-dc"}, {adminRealm, adminClientID}} {
		t.Run(identity.realm, func(t *testing.T) {
			called := false
			srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				called = true
				if r.URL.Path != "/realms/"+identity.realm+"/protocol/openid-connect/auth/device" || r.FormValue("client_id") != identity.client {
					t.Errorf("wrong device endpoint or client: %s", r.URL.Path)
				}
				w.WriteHeader(http.StatusBadRequest)
				fmt.Fprint(w, `{"error":"access_denied"}`)
			}))
			defer srv.Close()
			_, err := loginOAuth(context.Background(), &auth.OAuthConfig{KeycloakURL: srv.URL, Realm: identity.realm, ClientID: identity.client}, true)
			if !called || err == nil {
				t.Fatalf("device login must reach Keycloak and propagate denial: called=%v error=%v", called, err)
			}
		})
	}
}

func TestAdminDeviceLoginPropagatesCommandCancellation(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	cmd := loginCmd()
	cmd.SetOut(io.Discard)
	cmd.SetErr(io.Discard)
	cmd.SetArgs([]string{"--domain", "example.invalid", "--admin", "--device-code"})
	if err := cmd.ExecuteContext(ctx); !errors.Is(err, context.Canceled) {
		t.Fatalf("device login must honor command cancellation before issuing a request, got %v", err)
	}
}
