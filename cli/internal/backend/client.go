// Package backend is the typed HTTP client for the kube-dc backend
// API consumed by the CLI. Wraps `https://backend.<domain>/api/...`
// with the cached Keycloak JWT.
//
// Scope: M1-T07 covers /api/secrets/* (and /api/audit/* for the
// follow-on audit-query CLI). Each endpoint family lives in its own
// file under this package so subcommands can import a single typed
// surface rather than handcrafting requests.

package backend

import (
	"bytes"
	"context"
	"crypto/tls"
	"crypto/x509"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"strings"
	"time"
)

// Client is the typed backend API client. Construct via New().
type Client struct {
	BaseURL     string // e.g. https://backend.kube-dc.cloud
	AccessToken string // Keycloak JWT
	http        *http.Client
}

// New builds a Client from a server domain (e.g. kube-dc.cloud)
// and an access token. caCert is the PEM bundle for the cluster's
// gateway certificate, or empty to use the system trust store.
// insecure skips TLS verification entirely (use only with --insecure
// at the CLI level — never as a default).
//
// `domain` is normalised: any http:// or https:// prefix and trailing
// slashes are stripped before we prepend `backend.` (M1-T07
// first-review-pass P3). Pure-host strings like `kube-dc.cloud`
// are the canonical input.
func New(domain, accessToken, caCert string, insecure bool) (*Client, error) {
	d := normaliseDomain(domain)
	if d == "" {
		return nil, fmt.Errorf("backend client: domain is required")
	}
	if strings.ContainsAny(d, "/?#") {
		return nil, fmt.Errorf("backend client: domain %q must be a bare host (got path-like input)", domain)
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
			return nil, fmt.Errorf("backend client: could not parse CA cert PEM")
		}
		tlsCfg.RootCAs = pool
	}
	return &Client{
		BaseURL:     "https://backend." + d,
		AccessToken: accessToken,
		http: &http.Client{
			Timeout:   30 * time.Second,
			Transport: &http.Transport{TLSClientConfig: tlsCfg},
		},
	}, nil
}

// normaliseDomain strips scheme + trailing slashes from a domain
// string. Exported via the package-private helper for unit tests.
func normaliseDomain(domain string) string {
	d := strings.TrimSpace(domain)
	d = strings.TrimPrefix(d, "https://")
	d = strings.TrimPrefix(d, "http://")
	return strings.TrimRight(d, "/")
}

// APIError is the shape of an HTTP non-2xx response from the backend.
// It carries the HTTP status + the parsed error/details JSON when the
// body decoded; otherwise the raw body for debugging.
type APIError struct {
	Status  int
	Message string         // top-level `error` field
	Details map[string]any // remaining JSON keys + parsed `details`
	Raw     string         // unparsed body
}

func (e *APIError) Error() string {
	if e.Message != "" {
		return fmt.Sprintf("backend %d: %s", e.Status, e.Message)
	}
	if e.Raw != "" {
		return fmt.Sprintf("backend %d: %s", e.Status, truncate(e.Raw, 200))
	}
	return fmt.Sprintf("backend %d", e.Status)
}

func truncate(s string, n int) string {
	if len(s) <= n {
		return s
	}
	return s[:n] + "…"
}

// do sends an authenticated request and JSON-decodes the 2xx body
// into `out`. Non-2xx responses are returned as *APIError.
func (c *Client) do(ctx context.Context, method, path string, body any, out any) error {
	var bodyReader io.Reader
	if body != nil {
		b, err := json.Marshal(body)
		if err != nil {
			return fmt.Errorf("encode body: %w", err)
		}
		bodyReader = bytes.NewReader(b)
	}
	u := c.BaseURL + path
	req, err := http.NewRequestWithContext(ctx, method, u, bodyReader)
	if err != nil {
		return fmt.Errorf("build request: %w", err)
	}
	req.Header.Set("Accept", "application/json")
	if body != nil {
		req.Header.Set("Content-Type", "application/json")
	}
	if c.AccessToken != "" {
		req.Header.Set("Authorization", "Bearer "+c.AccessToken)
	}
	resp, err := c.http.Do(req)
	if err != nil {
		return fmt.Errorf("%s %s: %w", method, u, err)
	}
	defer resp.Body.Close()
	raw, _ := io.ReadAll(resp.Body)
	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		apiErr := &APIError{Status: resp.StatusCode, Raw: string(raw)}
		var parsed map[string]any
		if err := json.Unmarshal(raw, &parsed); err == nil {
			if m, ok := parsed["error"].(string); ok {
				apiErr.Message = m
				delete(parsed, "error")
			}
			apiErr.Details = parsed
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

// doRaw is like do() but returns the raw body for non-JSON
// responses (CSV exports, etc.).
func (c *Client) doRaw(ctx context.Context, method, path string, body any) ([]byte, error) {
	var bodyReader io.Reader
	if body != nil {
		b, err := json.Marshal(body)
		if err != nil {
			return nil, fmt.Errorf("encode body: %w", err)
		}
		bodyReader = bytes.NewReader(b)
	}
	u := c.BaseURL + path
	req, err := http.NewRequestWithContext(ctx, method, u, bodyReader)
	if err != nil {
		return nil, fmt.Errorf("build request: %w", err)
	}
	req.Header.Set("Accept", "*/*")
	if body != nil {
		req.Header.Set("Content-Type", "application/json")
	}
	if c.AccessToken != "" {
		req.Header.Set("Authorization", "Bearer "+c.AccessToken)
	}
	resp, err := c.http.Do(req)
	if err != nil {
		return nil, fmt.Errorf("%s %s: %w", method, u, err)
	}
	defer resp.Body.Close()
	raw, _ := io.ReadAll(resp.Body)
	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		return nil, &APIError{Status: resp.StatusCode, Raw: string(raw)}
	}
	return raw, nil
}

// pathEscape escapes a single path segment.
func pathEscape(s string) string {
	return url.PathEscape(s)
}
