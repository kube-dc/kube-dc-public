package auth

import (
	"bytes"
	"context"
	"encoding/json"
	"encoding/pem"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"reflect"
	"strings"
	"testing"
	"testing/synctest"
	"time"
)

func TestLoginDevice(t *testing.T) {
	for _, mode := range []string{"tenant", "admin", "custom CA", "insecure"} {
		t.Run(mode, func(t *testing.T) {
			t.Parallel()
			realm, clientID := "acme", "kube-dc"
			if mode == "admin" {
				realm, clientID = "master", "kube-dc-admin"
			}
			want := TokenResponse{AccessToken: "private-access", RefreshToken: "private-refresh", IDToken: "private-id", ExpiresIn: 300, RefreshExpiresIn: 1800, TokenType: "Bearer"}
			var paths []string
			srv := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				paths = append(paths, r.URL.Path)
				if r.Method != http.MethodPost || r.Header.Get("Content-Type") != "application/x-www-form-urlencoded" {
					t.Errorf("unexpected request: %s %v", r.Method, r.Header)
				}
				if err := r.ParseForm(); err != nil {
					t.Error(err)
				}
				if r.Form.Get("client_id") != clientID || r.Form.Has("client_secret") || r.Form.Has("redirect_uri") {
					t.Errorf("wrong client authentication parameters: %v", r.Form)
				}
				w.Header().Set("Content-Type", "application/json")
				switch r.URL.Path {
				case "/realms/" + realm + "/protocol/openid-connect/auth/device":
					if r.Form.Get("scope") != "openid offline_access" {
						t.Errorf("offline tokens must be requested: %v", r.Form)
					}
					json.NewEncoder(w).Encode(deviceAuthorization{DeviceCode: "private-device", UserCode: "ABCD-EFGH", VerificationURI: "https://login.example/realms/" + realm + "/device", ExpiresIn: 600, Interval: 1})
				case "/realms/" + realm + "/protocol/openid-connect/token":
					if r.Form.Get("grant_type") != deviceGrantType || r.Form.Get("device_code") != "private-device" {
						t.Errorf("wrong token request: %v", r.Form)
					}
					json.NewEncoder(w).Encode(want)
				default:
					t.Errorf("unexpected endpoint: %s", r.URL.Path)
					w.WriteHeader(http.StatusNotFound)
				}
			}))
			defer srv.Close()
			cfg := &OAuthConfig{KeycloakURL: srv.URL + "/", Realm: realm, ClientID: clientID}
			if mode == "insecure" {
				cfg.Insecure = true
			} else {
				cfg.CACert = string(pem.EncodeToMemory(&pem.Block{Type: "CERTIFICATE", Bytes: srv.Certificate().Raw}))
			}
			var out bytes.Buffer
			ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
			defer cancel()
			got, err := NewOAuthFlow(cfg).LoginDevice(ctx, &out)
			if err != nil {
				t.Fatal(err)
			}
			if *got != want || len(paths) != 2 {
				t.Fatalf("unexpected tokens or requests: token match=%v, paths=%v", *got == want, paths)
			}
			for _, wantText := range []string{"https://login.example/realms/" + realm + "/device", "ABCD-EFGH"} {
				if !strings.Contains(out.String(), wantText) {
					t.Errorf("missing instructions %q", wantText)
				}
			}
			if strings.Contains(out.String(), "private-") {
				t.Error("instructions disclosed a credential")
			}
		})
	}
}

type deviceRoundTripper func(*http.Request) (*http.Response, error)

func (f deviceRoundTripper) RoundTrip(r *http.Request) (*http.Response, error) { return f(r) }

func deviceResponse(status int, body string) *http.Response {
	return &http.Response{StatusCode: status, Header: make(http.Header), Body: io.NopCloser(strings.NewReader(body))}
}

func TestDevicePollingIntervals(t *testing.T) {
	// Virtual time lets us assert the actual wait durations without sleeping
	// through a real authorization window or replacing the production timer.
	for _, tc := range []struct {
		name     string
		interval int64
		errors   []string
		want     []time.Duration
	}{
		{"default", 0, []string{"authorization_pending", ""}, []time.Duration{5 * time.Second, 5 * time.Second}},
		{"slow down persists", 2, []string{"slow_down", "authorization_pending", "slow_down", ""}, []time.Duration{2 * time.Second, 7 * time.Second, 7 * time.Second, 12 * time.Second}},
		{"timeout backs off", 1, []string{"timeout", "authorization_pending", ""}, []time.Duration{time.Second, 2 * time.Second, 2 * time.Second}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			synctest.Test(t, func(t *testing.T) {
				previous := time.Now()
				var waits []time.Duration
				client := &http.Client{Transport: deviceRoundTripper(func(r *http.Request) (*http.Response, error) {
					waits = append(waits, time.Since(previous))
					time.Sleep(3 * time.Second) // A slow response must not consume the next wait.
					previous = time.Now()
					code := tc.errors[len(waits)-1]
					if code == "timeout" {
						return nil, context.DeadlineExceeded
					}
					if code != "" {
						return deviceResponse(400, fmt.Sprintf(`{"error":%q}`, code)), nil
					}
					return deviceResponse(200, `{"access_token":"access"}`), nil
				})}
				ctx, cancel := context.WithTimeout(context.Background(), time.Minute)
				defer cancel()
				_, err := pollDeviceToken(ctx, client, "https://keycloak/token", "kube-dc", deviceAuthorization{DeviceCode: "device", Interval: tc.interval})
				if err != nil {
					t.Fatal(err)
				}
				if !reflect.DeepEqual(waits, tc.want) {
					t.Errorf("poll waits=%v, want %v", waits, tc.want)
				}
			})
		})
	}
}

func TestDevicePollingStops(t *testing.T) {
	for _, tc := range []struct {
		name, body, want string
		status           int
	}{
		{"denied", `{"error":"access_denied"}`, "denied", 400},
		{"expired", `{"error":"expired_token"}`, "expired", 400},
		{"disabled", `{"error":"unauthorized_client"}`, "enable OAuth 2.0 Device Authorization Grant", 400},
		{"invalid client", `{"error":"invalid_client"}`, "enable OAuth 2.0 Device Authorization Grant", 401},
		{"unsupported", `{"error":"unsupported_grant_type"}`, "enable OAuth 2.0 Device Authorization Grant", 400},
		{"unknown error", `{"error":"private-secret","error_description":"private-secret"}`, "HTTP 400", 400},
		{"invalid JSON", `<html>private-secret</html>`, "invalid JSON", 502},
		{"empty token", `{}`, "without an access token", 200},
	} {
		t.Run(tc.name, func(t *testing.T) {
			synctest.Test(t, func(t *testing.T) {
				calls := 0
				client := &http.Client{Transport: deviceRoundTripper(func(r *http.Request) (*http.Response, error) {
					calls++
					return deviceResponse(tc.status, tc.body), nil
				})}
				ctx, cancel := context.WithTimeout(context.Background(), time.Minute)
				defer cancel()
				_, err := pollDeviceToken(ctx, client, "https://keycloak/token", "kube-dc", deviceAuthorization{DeviceCode: "device"})
				err = NewOAuthFlow(&OAuthConfig{Realm: "acme", ClientID: "kube-dc"}).deviceLoginError(err)
				if err == nil || !strings.Contains(err.Error(), tc.want) || strings.Contains(err.Error(), "private-secret") || calls != 1 {
					t.Fatalf("error=%v, calls=%d; want %q and one call", err, calls, tc.want)
				}
			})
		})
	}
}

func TestDevicePollingDeadlineAndCancellation(t *testing.T) {
	for _, cancelEarly := range []bool{false, true} {
		synctest.Test(t, func(t *testing.T) {
			ctx, cancel := context.WithTimeout(context.Background(), 12*time.Second)
			defer cancel()
			calls := 0
			client := &http.Client{Transport: deviceRoundTripper(func(r *http.Request) (*http.Response, error) {
				calls++
				if cancelEarly {
					cancel()
				}
				return deviceResponse(400, `{"error":"authorization_pending"}`), nil
			})}
			_, err := pollDeviceToken(ctx, client, "https://keycloak/token", "kube-dc", deviceAuthorization{DeviceCode: "device"})
			wantErr, wantCalls := context.DeadlineExceeded, 2
			if cancelEarly {
				wantErr, wantCalls = context.Canceled, 1
			}
			if !errors.Is(err, wantErr) || calls != wantCalls {
				t.Fatalf("error=%v calls=%d, want %v calls=%d", err, calls, wantErr, wantCalls)
			}
		})
	}
}

func TestLoginDeviceInvalidAuthorization(t *testing.T) {
	for _, body := range []string{
		`{}`, `null`, `{"error":"unauthorized_client","error_description":"private-secret"}`,
		`{"device_code":"private-secret","user_code":"CODE","verification_uri":"https://keycloak/device","expires_in":0}`,
		`{"device_code":"private-secret","user_code":"CODE","verification_uri":"https://keycloak/device","expires_in":9223372036854775807}`,
		`{"device_code":"private-secret","user_code":"CODE","verification_uri":"https://keycloak/device","expires_in":600,"interval":-1}`,
		`{"device_code":"private-secret","user_code":"CODE","verification_uri":"file:///device","expires_in":600}`,
	} {
		t.Run(body, func(t *testing.T) {
			srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				if !strings.HasSuffix(r.URL.Path, "/auth/device") {
					t.Error("invalid grant must not be polled")
				}
				fmt.Fprint(w, body)
			}))
			defer srv.Close()
			var out bytes.Buffer
			_, err := NewOAuthFlow(&OAuthConfig{KeycloakURL: srv.URL, Realm: "acme", ClientID: "kube-dc"}).LoginDevice(context.Background(), &out)
			if err == nil || strings.Contains(err.Error(), "private-secret") || out.Len() != 0 {
				t.Fatalf("expected sanitized failure before instructions, error=%v output=%q", err, out.String())
			}
		})
	}
}

func TestLoginDeviceCancelsAuthorizationRequest(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		io.Copy(io.Discard, r.Body)
		cancel()
		<-r.Context().Done()
	}))
	defer srv.Close()
	_, err := NewOAuthFlow(&OAuthConfig{KeycloakURL: srv.URL, Realm: "acme", ClientID: "kube-dc"}).LoginDevice(ctx, io.Discard)
	if !errors.Is(err, context.Canceled) {
		t.Fatalf("expected request cancellation, got %v", err)
	}
}

func TestLoginDeviceHonorsCodeExpiry(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if !strings.HasSuffix(r.URL.Path, "/auth/device") {
			t.Error("must not poll after code expiry")
		}
		fmt.Fprint(w, `{"device_code":"device","user_code":"CODE","verification_uri":"https://keycloak/device","expires_in":1,"interval":2}`)
	}))
	defer srv.Close()
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	_, err := NewOAuthFlow(&OAuthConfig{KeycloakURL: srv.URL, Realm: "acme", ClientID: "kube-dc"}).LoginDevice(ctx, io.Discard)
	if !errors.Is(err, context.DeadlineExceeded) || ctx.Err() != nil {
		t.Fatalf("expected code expiry before parent deadline, got %v (parent=%v)", err, ctx.Err())
	}
}

func TestLoginDeviceRejectsUntrustedTLS(t *testing.T) {
	srv := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		t.Error("must not send the request over an untrusted TLS connection")
	}))
	defer srv.Close()
	_, err := NewOAuthFlow(&OAuthConfig{KeycloakURL: srv.URL, Realm: "acme", ClientID: "kube-dc"}).LoginDevice(context.Background(), io.Discard)
	if err == nil || !strings.Contains(err.Error(), "certificate") {
		t.Fatalf("expected TLS verification failure, got %v", err)
	}
}

func TestDeviceLoginDoesNotFollowRedirects(t *testing.T) {
	for _, atToken := range []bool{false, true} {
		t.Run(fmt.Sprintf("token=%v", atToken), func(t *testing.T) {
			called := false
			target := httptest.NewServer(http.HandlerFunc(func(http.ResponseWriter, *http.Request) { called = true }))
			defer target.Close()
			srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				if atToken && strings.HasSuffix(r.URL.Path, "/auth/device") {
					fmt.Fprint(w, `{"device_code":"private-device","user_code":"CODE","verification_uri":"https://keycloak/device","expires_in":60,"interval":1}`)
					return
				}
				w.Header().Set("Location", target.URL)
				w.WriteHeader(http.StatusTemporaryRedirect)
			}))
			defer srv.Close()
			_, err := NewOAuthFlow(&OAuthConfig{KeycloakURL: srv.URL, Realm: "acme", ClientID: "kube-dc"}).LoginDevice(context.Background(), io.Discard)
			if err == nil || called {
				t.Fatalf("redirect accepted or credential forwarded: %v", err)
			}
		})
	}
}
