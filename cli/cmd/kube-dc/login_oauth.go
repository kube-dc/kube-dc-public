package main

import (
	"context"
	"os"
	"time"

	"github.com/shalb/kube-dc/cli/internal/auth"
)

func loginOAuth(ctx context.Context, cfg *auth.OAuthConfig, deviceCode bool) (*auth.TokenResponse, error) {
	flow := auth.NewOAuthFlow(cfg)
	if deviceCode {
		// Keycloak supplies the code lifetime, which may exceed the browser
		// flow's five-minute callback timeout.
		return flow.LoginDevice(ctx, os.Stdout)
	}
	ctx, cancel := context.WithTimeout(ctx, 5*time.Minute)
	defer cancel()
	return flow.Login(ctx)
}
