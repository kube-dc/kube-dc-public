/*
Copyright Kube-DC 2025.

Licensed under the Apache License, Version 2.0 (the "License");
you may not use this file except in compliance with the License.
*/

package helpers

import (
	"bytes"
	"context"
	"crypto/tls"
	"encoding/base64"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"strings"
	"time"
)

// BackendClient is a tiny purpose-built HTTP client for the
// /api/secrets/* and /api/orgs/* + /api/audit/* surfaces. We don't
// reuse cli/internal/backend because the CLI lives in a sibling Go
// module (cli/go.mod) — pulling it across the module boundary would
// either need a replace directive or a vendor tree, both heavier than
// recreating ~50 lines of HTTP-with-JWT here.
//
// All requests carry `Authorization: Bearer <jwt>`. TLS is permissive
// by default (matches the rest of the suite's `httpsAgent` defaults)
// since the cluster's backend.<domain> may be served behind the
// Envoy Gateway's LE cert OR a private CA.
type BackendClient struct {
	BaseURL string // e.g. https://backend.kube-dc.cloud
	JWT     string

	http *http.Client
}

// NewBackendClient builds a client targeting `backend.<domain>`
// (kube-dc's standard envoy-gateway hostname).
func NewBackendClient(domain, jwt string) *BackendClient {
	domain = strings.TrimPrefix(domain, "https://")
	domain = strings.TrimSuffix(domain, "/")
	return &BackendClient{
		BaseURL: fmt.Sprintf("https://backend.%s", domain),
		JWT:     jwt,
		http: &http.Client{
			Timeout: 60 * time.Second,
			Transport: &http.Transport{
				TLSClientConfig: &tls.Config{InsecureSkipVerify: true}, //nolint:gosec // tests only
			},
		},
	}
}

// Do issues a request and decodes the JSON response. `out` is
// optional — pass nil for fire-and-forget calls. Returns the HTTP
// status code AND the raw body (string) for assertion convenience
// even when JSON decoding fails (404s often return HTML).
func (c *BackendClient) Do(ctx context.Context, method, path string, body any, out any) (int, string, error) {
	var bodyReader io.Reader
	if body != nil {
		raw, err := json.Marshal(body)
		if err != nil {
			return 0, "", fmt.Errorf("marshal body: %w", err)
		}
		bodyReader = bytes.NewReader(raw)
	}
	req, err := http.NewRequestWithContext(ctx, method, c.BaseURL+path, bodyReader)
	if err != nil {
		return 0, "", fmt.Errorf("build request: %w", err)
	}
	req.Header.Set("Accept", "application/json")
	if body != nil {
		req.Header.Set("Content-Type", "application/json")
	}
	req.Header.Set("Authorization", "Bearer "+c.JWT)
	resp, err := c.http.Do(req)
	if err != nil {
		return 0, "", fmt.Errorf("%s %s: %w", method, path, err)
	}
	defer resp.Body.Close()
	raw, _ := io.ReadAll(resp.Body)
	bodyStr := string(raw)
	if out != nil && len(raw) > 0 && resp.StatusCode >= 200 && resp.StatusCode < 300 {
		if err := json.Unmarshal(raw, out); err != nil {
			return resp.StatusCode, bodyStr, fmt.Errorf("decode response: %w (body: %s)", err, bodyStr)
		}
	}
	return resp.StatusCode, bodyStr, nil
}

// decodeJSONBody is a tiny helper shared by the Keycloak token
// endpoint and any future raw-HTTP probes the helpers need.
func decodeJSONBody(resp *http.Response, into any) error {
	raw, err := io.ReadAll(resp.Body)
	if err != nil {
		return err
	}
	return json.Unmarshal(raw, into)
}

// DecodeJWT base64-decodes the JWT payload and returns it as a map.
// Mirrors what the backend's util/openbao.decodeJwt does so test
// assertions on claims (groups, org, preferred_username) read the
// same values the backend will read.
func DecodeJWT(jwt string) (map[string]any, error) {
	parts := strings.Split(jwt, ".")
	if len(parts) != 3 {
		return nil, fmt.Errorf("jwt: want 3 segments, got %d", len(parts))
	}
	// Convert URL-safe base64 to standard and add padding.
	b64 := strings.ReplaceAll(parts[1], "-", "+")
	b64 = strings.ReplaceAll(b64, "_", "/")
	switch len(b64) % 4 {
	case 2:
		b64 += "=="
	case 3:
		b64 += "="
	}
	raw, err := base64.StdEncoding.DecodeString(b64)
	if err != nil {
		return nil, fmt.Errorf("jwt: decode payload: %w", err)
	}
	var out map[string]any
	if err := json.Unmarshal(raw, &out); err != nil {
		return nil, fmt.Errorf("jwt: parse payload: %w", err)
	}
	return out, nil
}
