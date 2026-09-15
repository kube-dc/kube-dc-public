package main

import (
	"context"
	"fmt"
	"os"

	"github.com/shalb/kube-dc/cli/internal/auth"
	"github.com/shalb/kube-dc/cli/internal/kubeconfig"
)

// An empty result preserves existing trust, including CA file references.
// A non-empty bundle replaces it; UseSystemCA explicitly clears old trust.
type apiTrust struct {
	CACert      string
	UseSystemCA bool
}

func readLoginCA(path string) (string, error) {
	data, err := os.ReadFile(path)
	if err != nil {
		return "", fmt.Errorf("failed to read CA certificate: %w", err)
	}
	ca, err := auth.NormalizeTrustBundle(string(data))
	if err != nil {
		return "", fmt.Errorf("invalid --ca-cert: %w", err)
	}
	return ca, nil
}

func resolveLoginAPICA(ctx context.Context, domain, clusterName, server, providedCA string, insecure bool) (apiTrust, error) {
	if err := ctx.Err(); err != nil {
		return apiTrust{}, err
	}
	if insecure {
		return apiTrust{}, nil
	}
	mgr, err := kubeconfig.NewManager()
	if err != nil {
		return apiTrust{}, err
	}
	cluster, err := mgr.ClusterConnection(clusterName, server)
	if err != nil {
		return apiTrust{}, err
	}
	var existingCA string
	if providedCA == "" {
		existingCA, err = mgr.ClusterCA(clusterName, server)
		if err != nil {
			return apiTrust{}, err
		}
	}
	connection := auth.APIConnection{TLSServerName: cluster.TLSServerName, ProxyURL: cluster.ProxyURL}
	return resolveAPICA(ctx, server, providedCA, existingCA, connection, func(ctx context.Context) (string, error) {
		return auth.DiscoverAPICA(ctx, "https://backend."+domain+"/.well-known/kube-dc-config", server)
	})
}

func resolveAPICA(ctx context.Context, server, providedCA, existingCA string, connection auth.APIConnection, discover func(context.Context) (string, error)) (apiTrust, error) {
	verify := func(ca string) error { return auth.VerifyAPIConnection(ctx, server, ca, connection) }
	// Explicit operator trust is authoritative; never replace it by discovery.
	if providedCA != "" {
		ca, err := auth.NormalizeTrustBundle(providedCA)
		if err != nil {
			return apiTrust{}, fmt.Errorf("invalid --ca-cert: %w", err)
		}
		if err := verify(ca); err != nil {
			return apiTrust{}, fmt.Errorf("verify API server with --ca-cert: %w", err)
		}
		return apiTrust{CACert: ca}, nil
	}
	// Reuse valid configured trust without converting CA files to snapshots.
	initialErr := verify(existingCA)
	if initialErr == nil {
		return apiTrust{UseSystemCA: existingCA == ""}, nil
	}
	if !auth.IsCertificateTrustError(initialErr) {
		return apiTrust{}, fmt.Errorf("cannot verify API server %s: %w", server, initialErr)
	}
	// The API may have migrated from a private CA to system-trusted TLS.
	if existingCA != "" {
		if err := verify(""); err == nil {
			return apiTrust{UseSystemCA: true}, nil
		} else if !auth.IsCertificateTrustError(err) {
			return apiTrust{}, fmt.Errorf("cannot verify API server %s: %w", server, err)
		}
	}
	if err := ctx.Err(); err != nil {
		return apiTrust{}, err
	}
	ca, err := discover(ctx)
	if err != nil {
		return apiTrust{}, fmt.Errorf("cannot establish API server trust (%v): %w; obtain the trusted API CA bundle from your platform administrator and pass --ca-cert", initialErr, err)
	}
	// Discovered metadata has a stricter contract than explicit operator trust.
	if err := auth.ValidateCABundle(ca); err != nil {
		return apiTrust{}, fmt.Errorf("invalid discovered API CA: %w", err)
	}
	if err := verify(ca); err != nil {
		return apiTrust{}, fmt.Errorf("discovered CA does not verify API server %s: %w", server, err)
	}
	return apiTrust{CACert: ca}, nil
}
