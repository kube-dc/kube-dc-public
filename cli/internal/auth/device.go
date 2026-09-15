package auth

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/url"
	"strings"
	"time"
)

const deviceGrantType = "urn:ietf:params:oauth:grant-type:device_code"

type deviceAuthorization struct {
	DeviceCode      string `json:"device_code"`
	UserCode        string `json:"user_code"`
	VerificationURI string `json:"verification_uri"`
	ExpiresIn       int64  `json:"expires_in"`
	Interval        int64  `json:"interval"`
}

type deviceOAuthError struct {
	Code   string `json:"error"`
	Status int    `json:"-"`
}

func (e *deviceOAuthError) Error() string {
	// Do not include response bodies or error_description: they may echo
	// device codes, tokens, or other credentials.
	return fmt.Sprintf("Keycloak device authorization failed (HTTP %d)", e.Status)
}

// LoginDevice performs RFC 8628 device authorization. The user approves the
// displayed code in a browser on another device; no local callback is needed.
// Tokens use the same cache and refresh path as browser login.
func (f *OAuthFlow) LoginDevice(ctx context.Context, out io.Writer) (*TokenResponse, error) {
	client := f.getHTTPClient()
	// Neither grant endpoint needs redirects. A 307/308 could otherwise
	// forward the device code to another origin or downgrade to HTTP.
	client.CheckRedirect = func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse }
	defer client.CloseIdleConnections()
	baseURL := strings.TrimRight(f.config.KeycloakURL, "/") + "/realms/" +
		url.PathEscape(f.config.Realm) + "/protocol/openid-connect"

	started := time.Now()
	var grant deviceAuthorization
	err := postDeviceForm(ctx, client, baseURL+"/auth/device", url.Values{
		"client_id": {f.config.ClientID},
		"scope":     {"openid offline_access"},
	}, &grant)
	if err != nil {
		return nil, f.deviceLoginError(err)
	}
	// Bound seconds before converting to time.Duration to prevent overflow.
	const maxSeconds = int64((1<<63 - 1) / time.Second)
	verificationURL, urlErr := url.Parse(grant.VerificationURI)
	if grant.DeviceCode == "" || grant.UserCode == "" || urlErr != nil ||
		verificationURL.Host == "" || (verificationURL.Scheme != "https" && verificationURL.Scheme != "http") ||
		grant.ExpiresIn <= 0 || grant.ExpiresIn > maxSeconds || grant.Interval < 0 || grant.Interval > maxSeconds {
		return nil, fmt.Errorf("Keycloak returned an invalid device authorization response")
	}
	ctx, cancel := context.WithDeadline(ctx, started.Add(time.Duration(grant.ExpiresIn)*time.Second))
	defer cancel()
	if err := ctx.Err(); err != nil {
		return nil, f.deviceLoginError(err)
	}
	if _, err := fmt.Fprintf(out, "Open this URL in a browser on another device:\n  %s\n\nEnter code: %s\n\nWaiting for approval (code expires in %s)...\n",
		grant.VerificationURI, grant.UserCode, time.Duration(grant.ExpiresIn)*time.Second); err != nil {
		return nil, fmt.Errorf("display device authorization instructions: %w", err)
	}

	token, err := pollDeviceToken(ctx, client, baseURL+"/token", f.config.ClientID, grant)
	if err != nil {
		return nil, f.deviceLoginError(err)
	}
	return token, nil
}

func pollDeviceToken(ctx context.Context, client *http.Client, endpoint, clientID string, grant deviceAuthorization) (*TokenResponse, error) {
	interval := time.Duration(grant.Interval) * time.Second
	if interval == 0 {
		interval = 5 * time.Second
	}
	data := url.Values{
		"grant_type":  {deviceGrantType},
		"client_id":   {clientID},
		"device_code": {grant.DeviceCode},
	}
	for {
		// Wait before the first request and after each response. A ticker can
		// accumulate ticks during a slow request and poll again too soon.
		timer := time.NewTimer(interval)
		select {
		case <-ctx.Done():
			timer.Stop()
			return nil, ctx.Err()
		case <-timer.C:
		}
		if err := ctx.Err(); err != nil {
			return nil, err
		}
		var token TokenResponse
		err := postDeviceForm(ctx, client, endpoint, data, &token)
		if err == nil {
			if token.AccessToken == "" {
				return nil, fmt.Errorf("Keycloak returned a token response without an access token")
			}
			return &token, nil
		}
		if ctx.Err() != nil {
			return nil, ctx.Err()
		}
		var oauthErr *deviceOAuthError
		var netErr net.Error
		switch {
		case errors.As(err, &oauthErr):
			switch oauthErr.Code {
			case "authorization_pending":
			case "slow_down":
				interval = increaseDeviceInterval(interval, 5*time.Second)
			default:
				return nil, err
			}
		case errors.As(err, &netErr) && netErr.Timeout():
			// RFC 8628 requires reduced polling frequency after timeouts.
			interval = increaseDeviceInterval(interval, interval)
		default:
			return nil, err
		}
	}
}

func increaseDeviceInterval(interval, increase time.Duration) time.Duration {
	const maxDuration = time.Duration(1<<63 - 1)
	if increase > maxDuration-interval {
		return maxDuration
	}
	return interval + increase
}

func postDeviceForm(ctx context.Context, client *http.Client, endpoint string, data url.Values, result any) error {
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, endpoint, strings.NewReader(data.Encode()))
	if err != nil {
		return err
	}
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	req.Header.Set("Accept", "application/json")
	resp, err := client.Do(req)
	if err != nil {
		return err
	}
	defer resp.Body.Close()
	const maxBody = 1 << 20
	body, err := io.ReadAll(io.LimitReader(resp.Body, maxBody+1))
	if err != nil {
		return err
	}
	if len(body) > maxBody {
		return fmt.Errorf("Keycloak device authorization response is too large")
	}
	var oauthErr deviceOAuthError
	if err := json.Unmarshal(body, &oauthErr); err != nil {
		return fmt.Errorf("Keycloak returned invalid JSON (HTTP %d)", resp.StatusCode)
	}
	if resp.StatusCode != http.StatusOK || oauthErr.Code != "" {
		oauthErr.Status = resp.StatusCode
		return &oauthErr
	}
	if err := json.Unmarshal(body, result); err != nil {
		return fmt.Errorf("Keycloak returned an invalid device authorization response")
	}
	return nil
}

func (f *OAuthFlow) deviceLoginError(err error) error {
	var oauthErr *deviceOAuthError
	if errors.As(err, &oauthErr) {
		switch oauthErr.Code {
		case "access_denied":
			return fmt.Errorf("device login was denied; run kube-dc login --device-code again to retry")
		case "expired_token":
			return fmt.Errorf("device login code expired; run kube-dc login --device-code again to retry")
		case "unauthorized_client", "invalid_client", "unsupported_grant_type":
			return fmt.Errorf("Keycloak rejected device login for client %q in realm %q; ask a Keycloak administrator to enable OAuth 2.0 Device Authorization Grant for this client: %w",
				f.config.ClientID, f.config.Realm, err)
		}
	}
	if errors.Is(err, context.DeadlineExceeded) {
		return fmt.Errorf("device login timed out or the code expired; run kube-dc login --device-code again to retry: %w", err)
	}
	return err
}
