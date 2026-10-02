package openbao

import (
	"bytes"
	"context"
	"crypto/rand"
	"encoding/json"
	"fmt"
	"math/big"
	"time"
)

// OpenBao 2.6's bao CLI calls an authenticated generate-root endpoint.
// Bootstrap cannot use it because the ceremony is how it obtains a token.
// The legacy endpoint must be explicitly enabled on the loopback listener;
// this fallback sends requests only to that listener in the active pod.
func (c *Client) generateRootLegacy(ctx context.Context, pod string, shares [][]byte) ([]byte, error) {
	var state legacyRootState
	if err := c.legacyRootJSON(ctx, pod, "GET", "attempt", nil, &state); err != nil {
		return nil, fmt.Errorf("openbao: legacy root status: %w", err)
	}
	if state.OTPLength < 20 || state.OTPLength > 64 {
		return nil, fmt.Errorf("openbao: legacy root OTP length is invalid")
	}
	const alphabet = "0123456789ABCDEFGHIJKLMNOPQRSTUVWXYZabcdefghijklmnopqrstuvwxyz"
	otpBytes := make([]byte, state.OTPLength)
	for i := range otpBytes {
		n, err := rand.Int(rand.Reader, big.NewInt(int64(len(alphabet))))
		if err != nil {
			return nil, fmt.Errorf("openbao: generate-root legacy OTP: %w", err)
		}
		otpBytes[i] = alphabet[n.Int64()]
	}
	otp := string(otpBytes)
	defer zeroBytes(otpBytes)
	if _, err := c.legacyRootRequest(ctx, pod, "DELETE", "attempt", nil); err != nil {
		return nil, fmt.Errorf("openbao: cancel legacy root ceremony: %w", err)
	}
	state = legacyRootState{}
	if err := c.legacyRootJSON(ctx, pod, "POST", "attempt", map[string]string{"otp": otp}, &state); err != nil {
		return nil, fmt.Errorf("openbao: start legacy root ceremony: %w", err)
	}
	if !state.Started || state.Nonce == "" {
		return nil, fmt.Errorf("openbao: legacy root ceremony did not start")
	}
	for i, share := range shares {
		// A response loss after the final share is not retried: it may
		// contain the only copy of the encoded token.
		payload := map[string]string{"key": string(share), "nonce": state.Nonce}
		var next legacyRootState
		err := c.legacyRootJSON(ctx, pod, "POST", "update", payload, &next)
		if err != nil {
			return nil, fmt.Errorf("openbao: legacy root share %d: %w", i+1, err)
		}
		if !next.Complete && next.Progress < i+1 {
			return nil, fmt.Errorf("openbao: legacy root share %d did not advance", i+1)
		}
		state = next
		if next.Complete {
			break
		}
	}
	if !state.Complete || !isBase64ish(state.EncodedToken) {
		return nil, fmt.Errorf("openbao: legacy root ceremony did not return an encoded token")
	}
	tok, err := decodeRootToken(state.EncodedToken, otp)
	if err != nil {
		return nil, fmt.Errorf("openbao: decode legacy root token: %w", err)
	}
	return tok, nil
}

type legacyRootState struct {
	Started      bool   `json:"started"`
	Nonce        string `json:"nonce"`
	Progress     int    `json:"progress"`
	Complete     bool   `json:"complete"`
	EncodedToken string `json:"encoded_token"`
	OTPLength    int    `json:"otp_length"`
}

func (c *Client) legacyRootJSON(ctx context.Context, pod, method, path string, payload any, dst *legacyRootState) error {
	var body []byte
	if payload != nil {
		var err error
		body, err = json.Marshal(payload)
		if err != nil {
			return err
		}
		defer zeroBytes(body)
	}
	out, err := c.legacyRootRequest(ctx, pod, method, path, body)
	if err != nil {
		return err
	}
	defer zeroBytes(out)
	return json.Unmarshal(out, dst)
}

func (c *Client) legacyRootRequest(ctx context.Context, pod, method, path string, body []byte) ([]byte, error) {
	if (method != "GET" && method != "POST" && method != "DELETE") || (path != "attempt" && path != "update") {
		return nil, fmt.Errorf("openbao: invalid legacy root request")
	}
	// BusyBox wget cannot DELETE. BusyBox nc speaks only to 127.0.0.1;
	// body arrives on stdin and is never put in an exec argument.
	// BusyBox nc closes the socket as soon as its stdin writer reaches EOF.
	// Keep POST's writer open briefly so OpenBao can consume the body and
	// respond; otherwise it sporadically reports "context canceled".
	const script = `body=$(cat)
	{ printf '%s /v1/sys/generate-root/%s HTTP/1.1\r\nHost: localhost\r\nContent-Type: application/json\r\nContent-Length: %s\r\nConnection: close\r\n\r\n%s' "$1" "$2" "${#body}" "$body"; if [ "$1" = POST ]; then sleep 30; fi; } | nc -w 40 127.0.0.1 8200`
	// Kubernetes exec occasionally returns an empty stdout after nc completed.
	// Only replay GET and DELETE: POST can advance a one-use ceremony.
	attempts := 1
	if method != "POST" {
		attempts = 3
	}
	for attempt := 0; attempt < attempts; attempt++ {
		out, err := c.k8s.PodExec(ctx, openbaoNamespace, pod, []string{"sh", "-c", script, "_", method, path}, body)
		if err != nil {
			return nil, fmt.Errorf("openbao: legacy root transport: %w", err)
		}
		lineEnd := bytes.Index(out, []byte("\r\n"))
		headerEnd := bytes.Index(out, []byte("\r\n\r\n"))
		if lineEnd >= 0 && headerEnd >= 0 {
			if !bytes.HasPrefix(out, []byte("HTTP/1.1 2")) {
				return nil, fmt.Errorf("openbao: legacy root API rejected request: %s", out[:lineEnd])
			}
			return append([]byte(nil), out[headerEnd+4:]...), nil
		}
		if attempt+1 < attempts {
			select {
			case <-ctx.Done():
				return nil, ctx.Err()
			case <-time.After(200 * time.Millisecond):
			}
		}
	}
	return nil, fmt.Errorf("openbao: legacy root API returned no HTTP status after %d attempts", attempts)
}
