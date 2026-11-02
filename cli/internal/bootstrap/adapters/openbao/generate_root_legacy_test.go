package openbao

import (
	"context"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"testing"
)

// legacyRootServer models OpenBao's unauthenticated generate-root API behind
// the exec transport. dropUpdate lists the update calls (1-based) whose
// response is lost after the server has applied them.
type legacyRootServer struct {
	token      string
	required   int
	otp, nonce string
	progress   int
	started    bool
	updates    int
	ceremonies int
	dropUpdate map[int]bool
	// lostUpdate lists update calls that never reach the server.
	lostUpdate map[int]bool
	// errUpdate lists update calls applied but answered with an exec error.
	errUpdate map[int]bool
}

func (s *legacyRootServer) exec(_ context.Context, _, _ string, cmd []string, stdin []byte) ([]byte, error) {
	method, path := cmd[len(cmd)-2], cmd[len(cmd)-1]
	reply := func(v any) ([]byte, error) {
		b, _ := json.Marshal(v)
		return append([]byte("HTTP/1.1 200 OK\r\nContent-Type: application/json\r\n\r\n"), b...), nil
	}
	status := map[string]any{"started": s.started, "nonce": s.nonce, "progress": s.progress, "required": s.required, "otp_length": len(s.token)}
	switch method + " " + path {
	case "GET attempt":
		return reply(status)
	case "DELETE attempt":
		s.started, s.progress, s.nonce = false, 0, ""
		return reply(map[string]any{})
	case "POST attempt":
		var body map[string]string
		_ = json.Unmarshal(stdin, &body)
		s.ceremonies++
		s.otp, s.started, s.progress, s.nonce = body["otp"], true, 0, fmt.Sprintf("nonce-%d", s.ceremonies)
		status["started"], status["nonce"] = true, s.nonce
		return reply(status)
	case "POST update":
		var body map[string]string
		_ = json.Unmarshal(stdin, &body)
		s.updates++
		if s.lostUpdate[s.updates] {
			return nil, nil
		}
		if !s.started || body["nonce"] != s.nonce {
			return []byte("HTTP/1.1 400 Bad Request\r\n\r\n{}"), nil
		}
		s.progress++
		resp := map[string]any{"started": true, "nonce": s.nonce, "progress": s.progress, "required": s.required}
		if s.progress == s.required {
			enc := make([]byte, len(s.token))
			for i := range enc {
				enc[i] = s.token[i] ^ s.otp[i]
			}
			resp = map[string]any{"complete": true, "encoded_token": base64.RawStdEncoding.EncodeToString(enc)}
			s.started, s.progress = false, 0
		}
		if s.dropUpdate[s.updates] {
			return nil, nil // Kubernetes exec returned an empty stdout
		}
		if s.errUpdate[s.updates] {
			return nil, fmt.Errorf("websocket: close 1006 (abnormal closure)")
		}
		return reply(resp)
	}
	return nil, fmt.Errorf("unexpected %s %s", method, path)
}

func legacyShares() [][]byte { return [][]byte{[]byte("s1"), []byte("s2"), []byte("s3")} }

func runLegacy(t *testing.T, srv *legacyRootServer) ([]byte, error) {
	t.Helper()
	c := New(&fakeK8s{exec: srv.exec})
	return c.generateRootLegacy(context.Background(), "openbao-0", legacyShares())
}

// A lost response for a share that landed continues with the next share
// instead of failing (second installation rehearsal: share 1 landed, the exec
// returned nothing, and the ceremony was abandoned).
func TestLegacyRootLostResponseForLandedShare(t *testing.T) {
	srv := &legacyRootServer{token: "s.0123456789abcdefghijklmn", required: 3, dropUpdate: map[int]bool{1: true}}
	tok, err := runLegacy(t, srv)
	if err != nil || string(tok) != srv.token || srv.updates != 3 || srv.ceremonies != 1 {
		t.Fatalf("token=%q err=%v updates=%d ceremonies=%d", tok, err, srv.updates, srv.ceremonies)
	}
}

// The final share's response carries the only copy of the encoded token; when
// it is lost the ceremony is run once more.
func TestLegacyRootLostFinalResponseRestartsOnce(t *testing.T) {
	srv := &legacyRootServer{token: "s.0123456789abcdefghijklmn", required: 3, dropUpdate: map[int]bool{3: true}}
	tok, err := runLegacy(t, srv)
	if err != nil || string(tok) != srv.token || srv.ceremonies != 2 {
		t.Fatalf("token=%q err=%v ceremonies=%d", tok, err, srv.ceremonies)
	}
	srv = &legacyRootServer{token: "s.0123456789abcdefghijklmn", required: 3, dropUpdate: map[int]bool{3: true, 6: true}}
	if _, err := runLegacy(t, srv); !errors.Is(err, errLegacyRestart) || srv.ceremonies != 2 {
		t.Fatalf("a second lost token must stop: err=%v ceremonies=%d", err, srv.ceremonies)
	}
}

// A share that never reached OpenBao is sent once more.
func TestLegacyRootShareThatDidNotLandIsResent(t *testing.T) {
	srv := &legacyRootServer{token: "s.0123456789abcdefghijklmn", required: 3, lostUpdate: map[int]bool{2: true}}
	tok, err := runLegacy(t, srv)
	if err != nil || string(tok) != srv.token || srv.updates != 4 || srv.ceremonies != 1 {
		t.Fatalf("token=%q err=%v updates=%d ceremonies=%d", tok, err, srv.updates, srv.ceremonies)
	}
}

// An exec error after the share landed is resolved like an empty stdout.
func TestLegacyRootExecErrorAfterLandedShare(t *testing.T) {
	srv := &legacyRootServer{token: "s.0123456789abcdefghijklmn", required: 3, errUpdate: map[int]bool{2: true}}
	tok, err := runLegacy(t, srv)
	if err != nil || string(tok) != srv.token || srv.updates != 3 || srv.ceremonies != 1 {
		t.Fatalf("token=%q err=%v updates=%d ceremonies=%d", tok, err, srv.updates, srv.ceremonies)
	}
}

// A resend whose own response is lost is resolved by the status read too.
func TestLegacyRootResendResponseLost(t *testing.T) {
	srv := &legacyRootServer{token: "s.0123456789abcdefghijklmn", required: 3,
		lostUpdate: map[int]bool{2: true}, dropUpdate: map[int]bool{3: true}}
	tok, err := runLegacy(t, srv)
	if err != nil || string(tok) != srv.token || srv.ceremonies != 1 {
		t.Fatalf("token=%q err=%v updates=%d ceremonies=%d", tok, err, srv.updates, srv.ceremonies)
	}
}
