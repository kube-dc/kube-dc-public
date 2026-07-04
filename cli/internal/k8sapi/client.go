// Package k8sapi is a thin direct-to-kube-apiserver client used by
// CLI subcommands that prefer kubectl-style semantics over the
// kube-dc backend abstraction.
//
// Design choice (M1-T07): the CLI is hybrid. CRD reads / patches /
// soft deletes go via this package straight to the kube-apiserver
// using the user's Keycloak JWT (the OIDC webhook validates it the
// same way kubectl does). Value-plane writes, import sagas, and the
// consumer scanner go via cli/internal/backend because those
// operations need orchestration the backend already owns.
//
// Why a hand-rolled client instead of client-go? client-go would
// pull in ~80 MiB of transitive dependencies for what amounts to
// five JSON-over-HTTPS calls. The cli/internal/bootstrap/discover
// package already uses raw http.Client against /apis/... for the
// same reason; this matches that pattern.

package k8sapi

import (
	"bytes"
	"context"
	"crypto/tls"
	"crypto/x509"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"strings"
	"time"
)

// Client wraps the kube-apiserver URL + the cached Keycloak JWT.
type Client struct {
	APIServerURL string // e.g. https://kube-api.kube-dc.cloud:6443
	AccessToken  string
	http         *http.Client
}

// New builds a Client from a kube-apiserver URL + access token. caCert
// is the cluster's CA bundle (PEM); insecure skips verification.
func New(apiServerURL, accessToken, caCert string, insecure bool) (*Client, error) {
	if apiServerURL == "" {
		return nil, fmt.Errorf("k8sapi: API server URL is required")
	}
	tlsCfg := &tls.Config{}
	if insecure {
		tlsCfg.InsecureSkipVerify = true
	} else if caCert != "" {
		pool, _ := x509.SystemCertPool()
		if pool == nil {
			pool = x509.NewCertPool()
		}
		if !pool.AppendCertsFromPEM([]byte(caCert)) {
			return nil, fmt.Errorf("k8sapi: could not parse CA cert PEM")
		}
		tlsCfg.RootCAs = pool
	}
	return &Client{
		APIServerURL: strings.TrimRight(apiServerURL, "/"),
		AccessToken:  accessToken,
		http: &http.Client{
			Timeout:   30 * time.Second,
			Transport: &http.Transport{TLSClientConfig: tlsCfg},
		},
	}, nil
}

// APIError surfaces an HTTP non-2xx from the kube-apiserver. The
// kube-apiserver returns metav1.Status JSON on error; we extract the
// `message` field when present.
type APIError struct {
	Status  int
	Message string
	Raw     string
}

func (e *APIError) Error() string {
	if e.Message != "" {
		return fmt.Sprintf("kube-apiserver %d: %s", e.Status, e.Message)
	}
	return fmt.Sprintf("kube-apiserver %d: %s", e.Status, truncate(e.Raw, 200))
}

func truncate(s string, n int) string {
	if len(s) <= n {
		return s
	}
	return s[:n] + "…"
}

// do is the common request path. Sends `body` JSON when non-nil and
// decodes a 2xx JSON response into `out`. `contentType` lets the
// caller override the default `application/json` (e.g. for merge
// patches that require `application/merge-patch+json`).
func (c *Client) do(ctx context.Context, method, path string, body any, out any, contentType string) error {
	var bodyReader io.Reader
	if body != nil {
		b, err := json.Marshal(body)
		if err != nil {
			return fmt.Errorf("encode body: %w", err)
		}
		bodyReader = bytes.NewReader(b)
	}
	url := c.APIServerURL + path
	req, err := http.NewRequestWithContext(ctx, method, url, bodyReader)
	if err != nil {
		return fmt.Errorf("build request: %w", err)
	}
	req.Header.Set("Accept", "application/json")
	if body != nil {
		if contentType == "" {
			contentType = "application/json"
		}
		req.Header.Set("Content-Type", contentType)
	}
	if c.AccessToken != "" {
		req.Header.Set("Authorization", "Bearer "+c.AccessToken)
	}
	resp, err := c.http.Do(req)
	if err != nil {
		return fmt.Errorf("%s %s: %w", method, url, err)
	}
	defer resp.Body.Close()
	raw, _ := io.ReadAll(resp.Body)
	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		apiErr := &APIError{Status: resp.StatusCode, Raw: string(raw)}
		var status struct {
			Message string `json:"message"`
		}
		if err := json.Unmarshal(raw, &status); err == nil && status.Message != "" {
			apiErr.Message = status.Message
		}
		return apiErr
	}
	if out == nil || len(raw) == 0 {
		return nil
	}
	if err := json.Unmarshal(raw, out); err != nil {
		return fmt.Errorf("decode response: %w (body: %s)", err, truncate(string(raw), 200))
	}
	return nil
}
