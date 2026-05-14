package auth

import (
	"context"
	"crypto/rand"
	"crypto/sha256"
	"crypto/tls"
	"crypto/x509"
	"encoding/base64"
	"encoding/json"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/url"
	"strings"
	"time"

	"github.com/pkg/browser"
)

// OAuthConfig holds the OAuth configuration for a Kube-DC server
type OAuthConfig struct {
	KeycloakURL string
	Realm       string
	ClientID    string
	RedirectURI string
	CACert      string // PEM-encoded CA certificate
	Insecure    bool   // Skip TLS verification
}

// TokenResponse represents the OAuth token response from Keycloak
type TokenResponse struct {
	AccessToken      string `json:"access_token"`
	RefreshToken     string `json:"refresh_token"`
	IDToken          string `json:"id_token"`
	ExpiresIn        int    `json:"expires_in"`
	RefreshExpiresIn int    `json:"refresh_expires_in"`
	TokenType        string `json:"token_type"`
	Scope            string `json:"scope"`
}

// OAuthFlow handles the browser-based OAuth authentication flow
type OAuthFlow struct {
	config       *OAuthConfig
	codeVerifier string
	state        string
	server       *http.Server
	resultCh     chan *TokenResponse
	errCh        chan error
}

// NewOAuthFlow creates a new OAuth flow handler
func NewOAuthFlow(config *OAuthConfig) *OAuthFlow {
	return &OAuthFlow{
		config:   config,
		resultCh: make(chan *TokenResponse, 1),
		errCh:    make(chan error, 1),
	}
}

// Login performs the browser-based OAuth login flow
func (f *OAuthFlow) Login(ctx context.Context) (*TokenResponse, error) {
	// Generate PKCE code verifier and challenge
	f.codeVerifier = generateCodeVerifier()
	codeChallenge := generateCodeChallenge(f.codeVerifier)
	f.state = generateState()

	// Find an available port for the callback server
	listener, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		return nil, fmt.Errorf("failed to start callback server: %w", err)
	}
	port := listener.Addr().(*net.TCPAddr).Port
	f.config.RedirectURI = fmt.Sprintf("http://localhost:%d/callback", port)

	// Start the callback server
	mux := http.NewServeMux()
	mux.HandleFunc("/callback", f.handleCallback)
	f.server = &http.Server{Handler: mux}

	go func() {
		if err := f.server.Serve(listener); err != nil && err != http.ErrServerClosed {
			f.errCh <- fmt.Errorf("callback server error: %w", err)
		}
	}()

	// Build authorization URL
	authURL := f.buildAuthorizationURL(codeChallenge)

	// Open browser
	fmt.Println("Opening browser for authentication...")
	fmt.Printf("If browser doesn't open, visit: %s\n\n", authURL)

	if err := browser.OpenURL(authURL); err != nil {
		fmt.Printf("Failed to open browser: %v\n", err)
		fmt.Printf("Please open this URL manually: %s\n", authURL)
	}

	fmt.Println("Waiting for authentication...")

	// Wait for result or timeout
	select {
	case token := <-f.resultCh:
		return token, nil
	case err := <-f.errCh:
		return nil, err
	case <-ctx.Done():
		return nil, ctx.Err()
	case <-time.After(5 * time.Minute):
		return nil, fmt.Errorf("authentication timeout")
	}
}

func (f *OAuthFlow) handleCallback(w http.ResponseWriter, r *http.Request) {
	// Verify state
	state := r.URL.Query().Get("state")
	if state != f.state {
		f.errCh <- fmt.Errorf("state mismatch")
		writeErrorPage(w, http.StatusBadRequest, "State mismatch", "The OAuth state parameter did not match. This usually means the login flow was interrupted or replayed. Please retry from your terminal.")
		return
	}

	// Check for error
	if errParam := r.URL.Query().Get("error"); errParam != "" {
		errDesc := r.URL.Query().Get("error_description")
		f.errCh <- fmt.Errorf("oauth error: %s - %s", errParam, errDesc)
		writeErrorPage(w, http.StatusBadRequest, "Authentication error", errDesc)
		return
	}

	// Get authorization code
	code := r.URL.Query().Get("code")
	if code == "" {
		f.errCh <- fmt.Errorf("no authorization code received")
		writeErrorPage(w, http.StatusBadRequest, "No authorization code", "Keycloak did not return an authorization code. Please retry from your terminal.")
		return
	}

	// Exchange code for tokens
	token, err := f.exchangeCode(code)
	if err != nil {
		f.errCh <- fmt.Errorf("token exchange failed: %w", err)
		writeErrorPage(w, http.StatusInternalServerError, "Token exchange failed", err.Error())
		return
	}

	// Send success response.
	// charset=utf-8 is critical: without it browsers on Cyrillic-locale
	// systems decoded the UTF-8 SVG/text as Windows-1251 and rendered
	// garbage like "вњ"" instead of "✓".
	w.Header().Set("Content-Type", "text/html; charset=utf-8")
	fmt.Fprint(w, successPageHTML)

	f.resultCh <- token

	// Shutdown server
	go func() {
		time.Sleep(time.Second)
		f.server.Shutdown(context.Background())
	}()
}

func (f *OAuthFlow) buildAuthorizationURL(codeChallenge string) string {
	params := url.Values{
		"client_id":             {f.config.ClientID},
		"redirect_uri":          {f.config.RedirectURI},
		"response_type":         {"code"},
		"scope":                 {"openid offline_access"},
		"state":                 {f.state},
		"code_challenge":        {codeChallenge},
		"code_challenge_method": {"S256"},
	}

	return fmt.Sprintf("%s/realms/%s/protocol/openid-connect/auth?%s",
		f.config.KeycloakURL, f.config.Realm, params.Encode())
}

func (f *OAuthFlow) exchangeCode(code string) (*TokenResponse, error) {
	tokenURL := fmt.Sprintf("%s/realms/%s/protocol/openid-connect/token",
		f.config.KeycloakURL, f.config.Realm)

	data := url.Values{
		"grant_type":    {"authorization_code"},
		"client_id":     {f.config.ClientID},
		"code":          {code},
		"redirect_uri":  {f.config.RedirectURI},
		"code_verifier": {f.codeVerifier},
	}

	client := f.getHTTPClient()
	resp, err := client.Post(tokenURL, "application/x-www-form-urlencoded", strings.NewReader(data.Encode()))
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()

	body, err := io.ReadAll(resp.Body)
	if err != nil {
		return nil, err
	}

	if resp.StatusCode != http.StatusOK {
		return nil, fmt.Errorf("token exchange failed: %s", string(body))
	}

	var token TokenResponse
	if err := json.Unmarshal(body, &token); err != nil {
		return nil, err
	}

	return &token, nil
}

// getHTTPClient returns an HTTP client configured with CA cert or insecure mode
func (f *OAuthFlow) getHTTPClient() *http.Client {
	return CreateHTTPClient(f.config.CACert, f.config.Insecure)
}

// CreateHTTPClient creates an HTTP client with optional CA cert or insecure mode
func CreateHTTPClient(caCert string, insecure bool) *http.Client {
	tlsConfig := &tls.Config{}

	if caCert != "" {
		certPool := x509.NewCertPool()
		if ok := certPool.AppendCertsFromPEM([]byte(caCert)); ok {
			tlsConfig.RootCAs = certPool
		}
	}

	if insecure {
		tlsConfig.InsecureSkipVerify = true
	}

	return &http.Client{
		Transport: &http.Transport{
			TLSClientConfig: tlsConfig,
		},
		Timeout: 30 * time.Second,
	}
}

// RefreshToken uses a refresh token to get new access tokens
func RefreshToken(keycloakURL, realm, clientID, refreshToken, caCert string, insecure bool) (*TokenResponse, error) {
	tokenURL := fmt.Sprintf("%s/realms/%s/protocol/openid-connect/token", keycloakURL, realm)

	data := url.Values{
		"grant_type":    {"refresh_token"},
		"client_id":     {clientID},
		"refresh_token": {refreshToken},
	}

	client := CreateHTTPClient(caCert, insecure)
	resp, err := client.Post(tokenURL, "application/x-www-form-urlencoded", strings.NewReader(data.Encode()))
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()

	body, err := io.ReadAll(resp.Body)
	if err != nil {
		return nil, err
	}

	if resp.StatusCode != http.StatusOK {
		return nil, fmt.Errorf("token refresh failed: %s", string(body))
	}

	var token TokenResponse
	if err := json.Unmarshal(body, &token); err != nil {
		return nil, err
	}

	return &token, nil
}

// generateCodeVerifier generates a random PKCE code verifier
func generateCodeVerifier() string {
	b := make([]byte, 32)
	rand.Read(b)
	return base64.RawURLEncoding.EncodeToString(b)
}

// generateCodeChallenge generates the PKCE code challenge from the verifier
func generateCodeChallenge(verifier string) string {
	hash := sha256.Sum256([]byte(verifier))
	return base64.RawURLEncoding.EncodeToString(hash[:])
}

// generateState generates a random state parameter
func generateState() string {
	b := make([]byte, 16)
	rand.Read(b)
	return base64.RawURLEncoding.EncodeToString(b)
}

// writeErrorPage renders a styled error page matching the success page
// (utf-8, kube-dc card styling). Replaces http.Error which produces
// bare "text/plain" output that doesn't tell the user what to do next.
// title is the short headline, detail is the underlying error message
// (HTML-escaped before render).
func writeErrorPage(w http.ResponseWriter, status int, title, detail string) {
	w.Header().Set("Content-Type", "text/html; charset=utf-8")
	w.WriteHeader(status)
	page := strings.NewReplacer(
		"{{title}}", htmlEscape(title),
		"{{detail}}", htmlEscape(detail),
	).Replace(errorPageHTML)
	fmt.Fprint(w, page)
}

func htmlEscape(s string) string {
	r := strings.NewReplacer(
		"&", "&amp;",
		"<", "&lt;",
		">", "&gt;",
		`"`, "&quot;",
		"'", "&#39;",
	)
	return r.Replace(s)
}

// successPageHTML is rendered to the user's browser after the CLI's
// OAuth callback successfully exchanges the authorization code for a
// token. Styled to match the kube-dc console (#0066CC primary).
// The check mark is an inline SVG so it can't be misrendered by
// browsers that ignore the charset header.
const successPageHTML = `<!DOCTYPE html>
<html lang="en">
<head>
<meta charset="utf-8">
<meta name="viewport" content="width=device-width,initial-scale=1">
<title>Kube-DC — Authentication Successful</title>
<style>
  :root { --kube-dc-primary: #0066CC; --kube-dc-success: #3E8635; }
  * { box-sizing: border-box; }
  html, body { margin: 0; padding: 0; height: 100%; }
  body {
    font-family: "RedHatText", -apple-system, BlinkMacSystemFont, "Segoe UI",
                 Roboto, "Helvetica Neue", Arial, sans-serif;
    background: #f0f3f7;
    color: #151515;
    display: flex;
    align-items: center;
    justify-content: center;
    padding: 24px;
  }
  .card {
    background: #ffffff;
    max-width: 460px;
    width: 100%;
    padding: 40px 32px 32px;
    border-radius: 8px;
    box-shadow: 0 4px 16px rgba(0, 0, 0, 0.08);
    text-align: center;
    border-top: 4px solid var(--kube-dc-primary);
  }
  .check-circle {
    width: 72px; height: 72px;
    border-radius: 50%;
    background: var(--kube-dc-success);
    display: inline-flex;
    align-items: center;
    justify-content: center;
    margin-bottom: 24px;
  }
  h1 {
    font-size: 24px;
    margin: 0 0 12px;
    font-weight: 600;
    color: #151515;
  }
  p {
    margin: 0 0 8px;
    color: #6a6e73;
    font-size: 15px;
    line-height: 1.5;
  }
  .countdown {
    margin-top: 24px;
    font-size: 13px;
    color: #6a6e73;
  }
  .brand {
    margin-top: 32px;
    padding-top: 16px;
    border-top: 1px solid #f0f0f0;
    font-size: 12px;
    color: #6a6e73;
    letter-spacing: 0.5px;
    text-transform: uppercase;
  }
  .brand strong { color: var(--kube-dc-primary); font-weight: 700; }
</style>
</head>
<body>
  <main class="card" role="status" aria-live="polite">
    <div class="check-circle" aria-hidden="true">
      <svg xmlns="http://www.w3.org/2000/svg" width="36" height="36" viewBox="0 0 24 24" fill="none" stroke="#ffffff" stroke-width="3" stroke-linecap="round" stroke-linejoin="round">
        <polyline points="20 6 9 17 4 12"></polyline>
      </svg>
    </div>
    <h1>Authentication successful</h1>
    <p>You're signed in to Kube-DC.</p>
    <p>You can close this window and return to your terminal.</p>
    <p class="countdown" id="countdown">This window will close automatically in 3 seconds…</p>
    <div class="brand"><strong>Kube-DC</strong> CLI</div>
  </main>
<script>
  (function () {
    var seconds = 3;
    var el = document.getElementById('countdown');
    var t = setInterval(function () {
      seconds -= 1;
      if (seconds <= 0) {
        clearInterval(t);
        window.close();
        if (el) el.textContent = 'You can close this window now.';
      } else if (el) {
        el.textContent = 'This window will close automatically in ' + seconds + ' second' + (seconds === 1 ? '' : 's') + '…';
      }
    }, 1000);
  })();
</script>
</body>
</html>`

// errorPageHTML is rendered when the OAuth callback fails. Shares the
// success-page styling (utf-8, kube-dc card) but with a red accent and
// no auto-close — the user needs to read the message and retry from
// the terminal. {{title}} and {{detail}} are replaced before serving.
const errorPageHTML = `<!DOCTYPE html>
<html lang="en">
<head>
<meta charset="utf-8">
<meta name="viewport" content="width=device-width,initial-scale=1">
<title>Kube-DC — Authentication failed</title>
<style>
  :root { --kube-dc-primary: #0066CC; --kube-dc-danger: #C9190B; }
  * { box-sizing: border-box; }
  html, body { margin: 0; padding: 0; height: 100%; }
  body {
    font-family: "RedHatText", -apple-system, BlinkMacSystemFont, "Segoe UI",
                 Roboto, "Helvetica Neue", Arial, sans-serif;
    background: #f0f3f7;
    color: #151515;
    display: flex;
    align-items: center;
    justify-content: center;
    padding: 24px;
  }
  .card {
    background: #ffffff;
    max-width: 520px;
    width: 100%;
    padding: 40px 32px 32px;
    border-radius: 8px;
    box-shadow: 0 4px 16px rgba(0, 0, 0, 0.08);
    text-align: center;
    border-top: 4px solid var(--kube-dc-danger);
  }
  .x-circle {
    width: 72px; height: 72px;
    border-radius: 50%;
    background: var(--kube-dc-danger);
    display: inline-flex;
    align-items: center;
    justify-content: center;
    margin-bottom: 24px;
  }
  h1 {
    font-size: 22px;
    margin: 0 0 12px;
    font-weight: 600;
    color: #151515;
  }
  p {
    margin: 0 0 12px;
    color: #6a6e73;
    font-size: 15px;
    line-height: 1.5;
  }
  .detail {
    margin-top: 20px;
    padding: 12px 16px;
    background: #faf2f2;
    border-left: 3px solid var(--kube-dc-danger);
    border-radius: 4px;
    text-align: left;
    font-family: "RedHatMono", "SFMono-Regular", Menlo, Consolas, monospace;
    font-size: 13px;
    color: #3c0000;
    word-break: break-word;
  }
  .brand {
    margin-top: 32px;
    padding-top: 16px;
    border-top: 1px solid #f0f0f0;
    font-size: 12px;
    color: #6a6e73;
    letter-spacing: 0.5px;
    text-transform: uppercase;
  }
  .brand strong { color: var(--kube-dc-primary); font-weight: 700; }
</style>
</head>
<body>
  <main class="card" role="alert" aria-live="assertive">
    <div class="x-circle" aria-hidden="true">
      <svg xmlns="http://www.w3.org/2000/svg" width="36" height="36" viewBox="0 0 24 24" fill="none" stroke="#ffffff" stroke-width="3" stroke-linecap="round" stroke-linejoin="round">
        <line x1="18" y1="6" x2="6" y2="18"></line>
        <line x1="6" y1="6" x2="18" y2="18"></line>
      </svg>
    </div>
    <h1>{{title}}</h1>
    <p>You can close this window and retry the login from your terminal.</p>
    <div class="detail">{{detail}}</div>
    <div class="brand"><strong>Kube-DC</strong> CLI</div>
  </main>
</body>
</html>`
