package auth

import (
	"bytes"
	"context"
	"crypto/tls"
	"crypto/x509"
	"encoding/base64"
	"encoding/json"
	"encoding/pem"
	"errors"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/url"
	"strings"
	"time"
)

// ValidateCABundle accepts certificate-only CA bundles. In particular, a
// certificate followed by a private key must never be persisted in kubeconfig.
func ValidateCABundle(bundle string) error {
	_, err := certificateBundle(bundle, true)
	return err
}

// NormalizeTrustBundle accepts explicit operator trust, including annotated
// PEM, complete chains and pinned server certificates. Only certificates are
// returned: other PEM blocks (especially private keys) are rejected.
func NormalizeTrustBundle(bundle string) (string, error) {
	certificates, err := certificateBundle(bundle, false)
	if err != nil {
		return "", &invalidTrustBundleError{err}
	}
	return certificates, nil
}

type invalidTrustBundleError struct{ error }

// IsCertificateTrustError distinguishes failures that CA discovery can address
// from connection, proxy, timeout and configuration failures.
func IsCertificateTrustError(err error) bool {
	// net/http distinguishes the proxy's dial/TLS failure from the API's
	// TLS handshake inside an established tunnel. API CA discovery cannot
	// repair trust in the proxy that is needed to reach the API.
	var proxyError *net.OpError
	if errors.As(err, &proxyError) && proxyError.Op == "proxyconnect" {
		return false
	}
	var verification *tls.CertificateVerificationError
	var bundle *invalidTrustBundleError
	return errors.As(err, &verification) || errors.As(err, &bundle)
}

func certificateBundle(bundle string, strict bool) (string, error) {
	rest := bytes.TrimSpace([]byte(bundle))
	var certificates bytes.Buffer
	for len(rest) > 0 {
		if !strict {
			start := bytes.Index(rest, []byte("-----BEGIN "))
			if start < 0 {
				break // Ignore annotations; they are never persisted.
			}
			rest = rest[start:]
		}
		if !bytes.HasPrefix(rest, []byte("-----BEGIN CERTIFICATE-----")) {
			return "", fmt.Errorf("CA bundle must contain only PEM certificates")
		}
		const endMarker = "-----END CERTIFICATE-----"
		end := bytes.Index(rest, []byte(endMarker))
		if end < 0 {
			return "", fmt.Errorf("invalid PEM certificate in CA bundle")
		}
		end += len(endMarker)
		block, trailing := pem.Decode(rest[:end])
		if block == nil || len(bytes.TrimSpace(trailing)) != 0 || block.Type != "CERTIFICATE" || len(block.Headers) != 0 ||
			bytes.Count(rest[:end], []byte("-----BEGIN ")) != 1 {
			return "", fmt.Errorf("invalid PEM certificate in CA bundle")
		}
		cert, err := x509.ParseCertificate(block.Bytes)
		if err != nil || (strict && !cert.IsCA) {
			return "", fmt.Errorf("CA bundle contains an invalid CA certificate")
		}
		certificates.Write(pem.EncodeToMemory(&pem.Block{Type: "CERTIFICATE", Bytes: cert.Raw}))
		rest = bytes.TrimSpace(rest[end:])
	}
	if certificates.Len() == 0 {
		return "", fmt.Errorf("CA bundle contains no certificates")
	}
	return certificates.String(), nil
}

// DiscoverAPICA retrieves public CA metadata over verified HTTPS. The response
// may configure only the expected Kubernetes API, not OAuth or backend trust.
func DiscoverAPICA(ctx context.Context, discoveryURL, expectedServer string) (string, error) {
	client := CreateHTTPClient("", false)
	defer client.CloseIdleConnections()
	return discoverAPICA(ctx, client, discoveryURL, expectedServer)
}

func discoverAPICA(ctx context.Context, client *http.Client, discoveryURL, expectedServer string) (string, error) {
	u, err := url.Parse(discoveryURL)
	if err != nil || u.Scheme != "https" || u.Host == "" || u.User != nil {
		return "", fmt.Errorf("CA discovery requires an HTTPS URL")
	}
	// Copy so callers' clients are not mutated. Redirects can cross the trust
	// boundary (including an HTTPS-to-HTTP downgrade), so none are followed.
	verifiedClient := *client
	verifiedClient.Timeout = 10 * time.Second
	verifiedClient.CheckRedirect = func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse }
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, discoveryURL, nil)
	if err != nil {
		return "", err
	}
	req.Header.Set("Accept", "application/json")
	response, err := verifiedClient.Do(req)
	if err != nil {
		return "", fmt.Errorf("fetch API CA from %s: %w", u.Host, err)
	}
	defer response.Body.Close()
	if response.StatusCode != http.StatusOK {
		return "", fmt.Errorf("API CA discovery at %s returned HTTP %d", u.Host, response.StatusCode)
	}
	return decodeAPICA(response.Body, expectedServer)
}

func decodeAPICA(body io.Reader, expectedServer string) (string, error) {
	const maxBody = 256 * 1024
	data, err := io.ReadAll(io.LimitReader(body, maxBody+1))
	if err != nil {
		return "", err
	}
	if len(data) > maxBody {
		return "", fmt.Errorf("API CA discovery response is too large")
	}
	var metadata struct {
		APIVersion               string `json:"apiVersion"`
		APIServer                string `json:"apiServer"`
		CertificateAuthorityData string `json:"certificateAuthorityData"`
	}
	if err := json.Unmarshal(data, &metadata); err != nil {
		return "", fmt.Errorf("invalid API CA discovery JSON")
	}
	if metadata.APIVersion != "v1" || metadata.APIServer != strings.TrimRight(expectedServer, "/") {
		return "", fmt.Errorf("API CA discovery returned an unsupported version or a different API server")
	}
	ca, err := base64.StdEncoding.DecodeString(metadata.CertificateAuthorityData)
	if err != nil {
		return "", fmt.Errorf("API CA discovery returned invalid certificate encoding")
	}
	if err := ValidateCABundle(string(ca)); err != nil {
		return "", err
	}
	return string(ca), nil
}

// APIConnection contains only transport settings, never user credentials.
type APIConnection struct {
	TLSServerName string
	ProxyURL      string
}

// VerifyAPIServer proves API identity without sending a bearer token.
func VerifyAPIServer(ctx context.Context, server, ca string) error {
	return VerifyAPIConnection(ctx, server, ca, APIConnection{})
}

// VerifyAPIConnection uses the same HTTP proxy and TLS behavior as kubectl.
// Any HTTP response (including 401/403) proves TLS succeeded; redirects are
// never followed. The request has no Kubernetes credentials or cookies.
func VerifyAPIConnection(ctx context.Context, server, ca string, connection APIConnection) error {
	u, err := url.Parse(server)
	if err != nil || u.Scheme != "https" || u.Host == "" || u.User != nil ||
		(u.Path != "" && u.Path != "/") || u.RawQuery != "" || u.Fragment != "" {
		return fmt.Errorf("invalid HTTPS API server URL")
	}
	if ca != "" {
		ca, err = NormalizeTrustBundle(ca)
		if err != nil {
			return err
		}
	}
	// Match kubeconfig/client-go exactly: an explicit CA bundle replaces
	// system roots for the API. OAuth's HTTP client intentionally adds roots
	// instead, because Keycloak may have a publicly issued certificate.
	tlsConfig := &tls.Config{ServerName: connection.TLSServerName}
	if ca != "" {
		tlsConfig.RootCAs = x509.NewCertPool()
		tlsConfig.RootCAs.AppendCertsFromPEM([]byte(ca))
	}
	proxy := http.ProxyFromEnvironment
	if connection.ProxyURL != "" {
		proxyURL, err := url.Parse(connection.ProxyURL)
		if err != nil || proxyURL.Host == "" ||
			(proxyURL.Scheme != "http" && proxyURL.Scheme != "https" && proxyURL.Scheme != "socks5" && proxyURL.Scheme != "socks5h") {
			return fmt.Errorf("invalid kubeconfig proxy-url")
		}
		proxy = http.ProxyURL(proxyURL)
	}
	transport := &http.Transport{
		Proxy: proxy, TLSClientConfig: tlsConfig,
		DialContext:         (&net.Dialer{Timeout: 8 * time.Second}).DialContext,
		TLSHandshakeTimeout: 8 * time.Second,
	}
	defer transport.CloseIdleConnections()
	client := &http.Client{Transport: transport, Timeout: 8 * time.Second,
		CheckRedirect: func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse }}
	u.Path = "/version"
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, u.String(), nil)
	if err != nil {
		return err
	}
	response, err := client.Do(req)
	if err != nil {
		return err
	}
	response.Body.Close()
	return nil
}
