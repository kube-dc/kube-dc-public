package openbao

import (
	"bytes"
	"context"
	"encoding/base64"
	"errors"
	"fmt"
	"strings"
	"sync/atomic"
	"testing"

	"github.com/shalb/kube-dc/cli/internal/bootstrap/ports"
)

// fakeK8s is a stub ports.K8sClient that returns canned PodExec output.
// All other methods are unused by most openbao tests and panic to
// surface accidental dependencies; the annotation tests overlay their
// own getAnn/setAnn hooks.
type fakeK8s struct {
	exec        func(ctx context.Context, ns, pod string, cmd []string, stdin []byte) ([]byte, error)
	kubectlExec func(ctx context.Context, ns, pod string, cmd []string, stdin []byte) ([]byte, error)
	getAnn      func(ctx context.Context, ns, svc, key string) (string, error)
	setAnn      func(ctx context.Context, ns, svc, key, value string) error
}

func (f *fakeK8s) PodExec(ctx context.Context, ns, pod string, cmd []string, stdin []byte) ([]byte, error) {
	return f.exec(ctx, ns, pod, cmd, stdin)
}

// PodExecViaKubectl falls through to the WebSocket path when the
// test didn't configure a separate kubectl stub. Most existing
// tests don't exercise the fallback — they're set up so the WS
// path always reports success. Tests that DO want to model "WS
// drops + kubectl succeeds" set kubectlExec explicitly.
func (f *fakeK8s) PodExecViaKubectl(ctx context.Context, ns, pod string, cmd []string, stdin []byte) ([]byte, error) {
	if f.kubectlExec != nil {
		return f.kubectlExec(ctx, ns, pod, cmd, stdin)
	}
	return f.exec(ctx, ns, pod, cmd, stdin)
}
func (f *fakeK8s) DiscoverFluxGraph(context.Context) (ports.Graph, error) {
	panic("fakeK8s: DiscoverFluxGraph not stubbed")
}
func (f *fakeK8s) NodeLabels(context.Context) (map[string]map[string]string, error) {
	panic("fakeK8s: NodeLabels not stubbed")
}
func (f *fakeK8s) DeploymentImages(context.Context, string) (map[string]string, error) {
	panic("fakeK8s: DeploymentImages not stubbed")
}
func (f *fakeK8s) ListNamespaces(context.Context) ([]string, error) {
	panic("fakeK8s: ListNamespaces not stubbed")
}
func (f *fakeK8s) ListCRDs(context.Context) ([]string, error) {
	panic("fakeK8s: ListCRDs not stubbed")
}
func (f *fakeK8s) GetServiceAnnotation(ctx context.Context, ns, svc, key string) (string, error) {
	if f.getAnn == nil {
		panic("fakeK8s: GetServiceAnnotation not stubbed")
	}
	return f.getAnn(ctx, ns, svc, key)
}
func (f *fakeK8s) SetServiceAnnotation(ctx context.Context, ns, svc, key, value string) error {
	if f.setAnn == nil {
		panic("fakeK8s: SetServiceAnnotation not stubbed")
	}
	return f.setAnn(ctx, ns, svc, key, value)
}
func (f *fakeK8s) SetServiceAnnotations(ctx context.Context, ns, svc string, kv map[string]string) error {
	if f.setAnn == nil {
		panic("fakeK8s: SetServiceAnnotations not stubbed (reuse setAnn for the batch path)")
	}
	for k, v := range kv {
		if err := f.setAnn(ctx, ns, svc, k, v); err != nil {
			return err
		}
	}
	return nil
}
func (f *fakeK8s) GetConfigMapData(_ context.Context, _, _, _ string) (string, error) {
	panic("fakeK8s: GetConfigMapData not stubbed")
}

// Compile-time assertion that the openbao adapter implements its port.
var _ ports.OpenBaoClient = (*Client)(nil)

func TestStatus_ParsesActivePodOutput(t *testing.T) {
	f := &fakeK8s{
		exec: func(_ context.Context, _, pod string, cmd []string, _ []byte) ([]byte, error) {
			if pod != "openbao-2" {
				t.Errorf("unexpected pod %q", pod)
			}
			if len(cmd) < 2 || cmd[0] != "bao" || cmd[1] != "status" {
				t.Errorf("unexpected cmd %v", cmd)
			}
			return []byte(`{"initialized":true,"sealed":false,"version":"2.5.3","ha_mode":"active","raft_index":14721,"active_node_id":"node-2"}`), nil
		},
	}
	c := New(f)
	st, err := c.Status(context.Background(), "openbao-2")
	if err != nil {
		t.Fatalf("Status: %v", err)
	}
	if !st.Initialized || st.Sealed || st.HAMode != "active" || st.RaftIndex != 14721 || st.Version != "2.5.3" {
		t.Errorf("Status parse mismatch: %+v", st)
	}
}

func TestStatus_ParsesSealedPodOutputDespiteExitError(t *testing.T) {
	// `bao status` exits 2 on a sealed pod but the JSON shape is
	// authoritative. Adapter must prefer parsed output over the err.
	f := &fakeK8s{
		exec: func(_ context.Context, _, pod string, _ []string, _ []byte) ([]byte, error) {
			return []byte(`{"initialized":true,"sealed":true,"version":"2.5.3"}`), errors.New("exit status 2")
		},
	}
	c := New(f)
	st, err := c.Status(context.Background(), "openbao-0")
	if err != nil {
		t.Fatalf("sealed pod should not surface err when output parses: %v", err)
	}
	if !st.Sealed {
		t.Error("Sealed=false on a sealed-pod output")
	}
}

// Unseal feeds the share via stdin (NEVER argv) by posting a JSON
// body to OpenBao's /v1/sys/unseal HTTP endpoint via `wget
// --post-file=-`. This is the M0-T06 batch-2 review's P1 fix
// (argv exposure of OpenBao shares is unacceptable), updated for
// OpenBao 2.5.3 which doesn't support `bao operator unseal -`
// as a stdin form (atlantis bring-up 2026-05-26 — bao treats
// `-` as a literal key and rejects it).
func TestUnseal_FeedsShareViaStdinNotArgv(t *testing.T) {
	var capturedArgv []string
	var capturedStdin []byte
	f := &fakeK8s{
		exec: func(_ context.Context, _, _ string, cmd []string, stdin []byte) ([]byte, error) {
			capturedArgv = append([]string(nil), cmd...)
			capturedStdin = append([]byte(nil), stdin...)
			return nil, nil
		},
	}
	c := New(f)
	share := []byte("test-share-bytes-DO-NOT-LEAK")
	if err := c.Unseal(context.Background(), "openbao-0", share); err != nil {
		t.Fatalf("Unseal: %v", err)
	}

	// Share MUST be in stdin (as part of the JSON body).
	if !strings.Contains(string(capturedStdin), "test-share-bytes-DO-NOT-LEAK") {
		t.Errorf("stdin %q does not include share bytes", capturedStdin)
	}
	// Share MUST NOT appear anywhere in argv.
	for i, a := range capturedArgv {
		if strings.Contains(a, "test-share-bytes") || strings.Contains(a, "DO-NOT-LEAK") {
			t.Errorf("share leaked into argv[%d] = %q", i, a)
		}
	}
	// argv MUST end in the unseal endpoint URL — the share is in
	// the wget POST body, fed via stdin.
	if len(capturedArgv) == 0 || !strings.HasSuffix(capturedArgv[len(capturedArgv)-1], "/v1/sys/unseal") {
		t.Errorf("argv does not target the unseal endpoint: %v", capturedArgv)
	}
	// And argv MUST contain --post-file=- so stdin is the body.
	gotPostFile := false
	for _, a := range capturedArgv {
		if a == "--post-file=-" {
			gotPostFile = true
		}
	}
	if !gotPostFile {
		t.Errorf("argv missing --post-file=- (stdin-body marker): %v", capturedArgv)
	}
}

func TestUnseal_EmptyShareRejected(t *testing.T) {
	c := New(&fakeK8s{})
	if err := c.Unseal(context.Background(), "openbao-0", nil); err == nil {
		t.Fatal("empty share should be rejected")
	}
}

func TestUnseal_AlreadyUnsealed_Idempotent(t *testing.T) {
	f := &fakeK8s{
		exec: func(_ context.Context, _, _ string, _ []string, _ []byte) ([]byte, error) {
			return nil, errors.New("Vault is already unsealed.")
		},
	}
	c := New(f)
	if err := c.Unseal(context.Background(), "openbao-0", []byte("share")); err != nil {
		t.Errorf("already-unsealed should be idempotent: %v", err)
	}
}

// RevokeSelf MUST NOT put the token in argv. The wrapper reads from
// stdin via `read -r tok` then sets VAULT_TOKEN as a process env var
// (kernel-only, not argv) before invoking bao.
func TestRevokeSelf_FeedsTokenViaStdinNotArgv(t *testing.T) {
	var capturedArgv []string
	var capturedStdin []byte
	f := &fakeK8s{
		exec: func(_ context.Context, _, _ string, cmd []string, stdin []byte) ([]byte, error) {
			capturedArgv = append([]string(nil), cmd...)
			capturedStdin = append([]byte(nil), stdin...)
			return nil, nil
		},
		// activePodCached calls PodList + Status under the hood,
		// driven through `exec` above. We need a deterministic
		// active-pod resolution for the test to reach RevokeSelf's
		// exec; stub the Status JSON-return path.
	}
	// Make exec deterministic: probe (`true`) returns OK; `bao status`
	// returns active for openbao-0; revoke wrapper records.
	f.exec = func(_ context.Context, _, pod string, cmd []string, stdin []byte) ([]byte, error) {
		if len(cmd) > 0 && cmd[0] == "true" {
			return nil, nil
		}
		if len(cmd) > 1 && cmd[0] == "bao" && cmd[1] == "status" {
			if pod == "openbao-0" {
				return []byte(`{"initialized":true,"sealed":false,"ha_mode":"active"}`), nil
			}
			return []byte(`{"initialized":true,"sealed":false,"ha_mode":"standby"}`), nil
		}
		// revoke wrapper — return sentinel for the new retry helper.
		capturedArgv = append([]string(nil), cmd...)
		capturedStdin = append([]byte(nil), stdin...)
		return []byte("KUBE_DC_WRITE_OK\n"), nil
	}

	c := New(f)
	token := []byte("s.rootABC-DO-NOT-LEAK")
	if err := c.RevokeSelf(context.Background(), token); err != nil {
		t.Fatalf("RevokeSelf: %v", err)
	}
	// Stdin gets the normalizeExecStdin treatment (newline appended)
	// before reaching the in-pod shell wrapper. The test asserts the
	// token bytes are present without caring about exact whitespace.
	if !strings.Contains(string(capturedStdin), "s.rootABC-DO-NOT-LEAK") {
		t.Errorf("stdin = %q, want to contain token bytes", capturedStdin)
	}
	for i, a := range capturedArgv {
		if strings.Contains(a, "s.rootABC") || strings.Contains(a, "DO-NOT-LEAK") {
			t.Errorf("token leaked into argv[%d] = %q", i, a)
		}
	}
}

// GetAnnotation delegates to K8sClient.GetServiceAnnotation against
// the openbao Service in the openbao namespace.
func TestGetAnnotation_DelegatesToK8s(t *testing.T) {
	var capturedNS, capturedSvc, capturedKey string
	f := &fakeK8s{
		getAnn: func(_ context.Context, ns, svc, key string) (string, error) {
			capturedNS, capturedSvc, capturedKey = ns, svc, key
			return "2026-05-26T10:00:00Z", nil
		},
	}
	c := New(f)
	got, err := c.GetAnnotation(context.Background(), "", "kube-dc.com/openbao-bootstrap-finalized")
	if err != nil {
		t.Fatalf("GetAnnotation: %v", err)
	}
	if got != "2026-05-26T10:00:00Z" {
		t.Errorf("value = %q", got)
	}
	if capturedNS != "openbao" || capturedSvc != "openbao" || capturedKey != "kube-dc.com/openbao-bootstrap-finalized" {
		t.Errorf("delegated args wrong: ns=%q svc=%q key=%q", capturedNS, capturedSvc, capturedKey)
	}
}

// GenerateRoot's decode step must NOT put the encoded_token or OTP
// anywhere reachable from the openbao pod — both together are
// root-token-equivalent material. The decoder runs LOCALLY in the
// operator's Go process (base64-decode encoded XOR otp_bytes); no
// shell wrapper, no in-pod bao call, no argv exposure, no stdin/file
// path for the pair.
//
// This test forces a known root token through a synthetic ceremony:
// the mock returns encoded=base64(root_token XOR otp) as the
// threshold-share response, then asserts GenerateRoot returns the
// original root token AND that no `sh -c` exec ever happened (i.e.
// the decode step shelled out for NOTHING).
func TestGenerateRoot_DecodeIsLocal_NoInPodCall(t *testing.T) {
	// Pick an OTP and root token of the same length so XOR-base64
	// round-trips cleanly. The bao CLI emits OTPs as printable
	// ASCII whose byte length matches the decoded encoded_token.
	const (
		otp        = "B64otpVALUE-OPENBAO-2-5-3-LIVE-OK" // 33 bytes
		finalToken = "s.LiveDecodeVerifiedAgainstDC1123" // 33 bytes (same length)
	)

	// Compute the encoded form the mock should return — base64 of
	// (root XOR otp). Match the adapter's local decode logic so the
	// test catches algorithmic drift in either direction.
	xored := make([]byte, len(finalToken))
	for i := range finalToken {
		xored[i] = finalToken[i] ^ otp[i]
	}
	encoded := base64.RawStdEncoding.EncodeToString(xored)

	var (
		shCInvocations int
		// Track generate-root state machine in the mock:
		started  bool
		progress int
	)
	f := &fakeK8s{
		exec: func(_ context.Context, _, pod string, cmd []string, _ []byte) ([]byte, error) {
			switch {
			case len(cmd) > 0 && cmd[0] == "true":
				return nil, nil
			case len(cmd) > 1 && cmd[0] == "bao" && cmd[1] == "status":
				if pod == "openbao-0" {
					return []byte(`{"initialized":true,"sealed":false,"ha_mode":"active"}`), nil
				}
				return []byte(`{"initialized":true,"sealed":false,"ha_mode":"standby"}`), nil
			case len(cmd) > 2 && cmd[0] == "bao" && cmd[2] == "generate-root" && containsString(cmd, "-cancel"):
				started = false
				progress = 0
				return []byte("Success! Root token generation canceled (if it was started)\n"), nil
			case len(cmd) > 2 && cmd[0] == "bao" && cmd[2] == "generate-root" && containsString(cmd, "-status"):
				return []byte(fmt.Sprintf(`{"started":%v,"progress":%d,"required":3,"complete":false,"encoded_token":"","nonce":"n-123"}`, started, progress)), nil
			case len(cmd) > 2 && cmd[0] == "bao" && cmd[2] == "generate-root" && containsString(cmd, "-generate-otp"):
				return []byte(otp + "\n"), nil
			case len(cmd) > 2 && cmd[0] == "bao" && cmd[2] == "generate-root" && containsString(cmd, "-init"):
				if !containsString(cmd, "-otp="+otp) {
					return nil, fmt.Errorf("expected -init to carry -otp=<localotp>; got argv=%v", cmd)
				}
				started = true
				return []byte(`{"nonce":"n-123","otp":"","required":3}`), nil
			case len(cmd) > 2 && cmd[0] == "bao" && cmd[2] == "generate-root" && containsString(cmd, "-nonce"):
				progress++
				if progress >= 3 {
					complete := fmt.Sprintf(`{"complete":true,"encoded_token":%q,"progress":3,"required":3}`, encoded)
					started = false
					progress = 0
					return []byte(complete), nil
				}
				return []byte(fmt.Sprintf(`{"complete":false,"encoded_token":"","progress":%d,"required":3}`, progress)), nil
			case len(cmd) >= 2 && cmd[0] == "sh" && cmd[1] == "-c":
				// CRITICAL: no `sh -c` invocation should ever happen
				// for the decode step. The only legitimate `sh -c`
				// callers in GenerateRoot's path used to be the decode
				// wrapper — which is gone now.
				shCInvocations++
				for _, a := range cmd {
					if strings.Contains(a, "generate-root") && (strings.Contains(a, "-decode") || strings.Contains(a, "-otp")) {
						t.Errorf("decode shell wrapper executed (should be local Go now): %v", cmd)
					}
				}
				return nil, fmt.Errorf("unexpected sh -c invocation: %v", cmd)
			default:
				t.Errorf("unexpected exec cmd: %v", cmd)
				return nil, nil
			}
		},
	}

	c := New(f)
	shares := [][]byte{[]byte("share1"), []byte("share2"), []byte("share3")}
	tok, err := c.GenerateRoot(context.Background(), shares)
	if err != nil {
		t.Fatalf("GenerateRoot: %v", err)
	}
	if string(tok) != finalToken {
		t.Errorf("token = %q want %q (local decode failed to round-trip)", tok, finalToken)
	}
	if shCInvocations != 0 {
		t.Errorf("decode triggered %d sh -c invocations; want 0 (decode should be local)", shCInvocations)
	}
}

// TestDecodeRootToken_RoundTrip verifies the local decode function
// against synthetic inputs across multiple lengths + encodings. The
// adapter's decode is verified against live bao on eu/dc1 in
// docs/internal/openbao-runbook.md.
func TestDecodeRootToken_RoundTrip(t *testing.T) {
	cases := []struct {
		name string
		root string
		otp  string
	}{
		// Both pairs are crafted so len(root) == len(otp) — bao's
		// encode operation requires equal-length input.
		{"len-32", "s.OpenBao-decode-roundtrip-32-by", "OTP-printable-ASCII-32-bytes-XXX"},
		{"len-26", "s.shorter-root-token-26-by", "OTP-26-bytes-printableXXXX"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if len(tc.root) != len(tc.otp) {
				t.Fatalf("test data bug: root and otp must be the same byte length; got root=%d otp=%d", len(tc.root), len(tc.otp))
			}
			xored := make([]byte, len(tc.root))
			for i := range tc.root {
				xored[i] = tc.root[i] ^ tc.otp[i]
			}
			encoded := base64.RawStdEncoding.EncodeToString(xored)
			got, err := decodeRootToken(encoded, tc.otp)
			if err != nil {
				t.Fatalf("decodeRootToken: %v", err)
			}
			if string(got) != tc.root {
				t.Errorf("round-trip failed: got %q want %q", got, tc.root)
			}
		})
	}
}

// TestDecodeRootToken_LengthMismatch verifies the decoder rejects
// encoded + otp pairs where decoded encoded length != otp byte length.
func TestDecodeRootToken_LengthMismatch(t *testing.T) {
	enc := base64.RawStdEncoding.EncodeToString([]byte("aaaaaaaaaa")) // 10 bytes
	otp := "B"                                                        // 1 byte
	_, err := decodeRootToken(enc, otp)
	if err == nil || !strings.Contains(err.Error(), "length mismatch") {
		t.Errorf("expected length mismatch error, got %v", err)
	}
}

// Defense-in-depth: the adapter rejects encoded_token / OTP shapes
// that don't look like base64 before passing them to decodeRootToken.
// This guard originally protected an in-pod JSON heredoc from quote-
// injection; kept now to catch obviously-malformed values from a
// hostile/buggy OpenBao server before they reach the XOR step.
// `isBase64ish` is the pre-validation pivot.
//
// Tested at the isBase64ish layer directly (the guards in
// GenerateRoot delegate to it). A separate end-to-end variant below
// confirms that a malformed encoded_token threading all the way
// through the ceremony surfaces as the expected "unexpected shape"
// error, not a confusing downstream parse failure.
func TestIsBase64ish_RejectsMalformedShapes(t *testing.T) {
	cases := []struct {
		name   string
		value  string
		wantOK bool
	}{
		{"empty", "", false},
		{"plain base64", "Zm9vYmFyMTIzNDU2", true},
		{"unpadded base64", "Zm9vYmFy", true},
		{"with quote", `"injected"`, false},
		{"with backslash", `back\slash`, false},
		{"with newline", "line1\nline2", false},
		{"with space", "has space", false},
		{"with single quote", "has'quote", false},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			got := isBase64ish(tc.value)
			if got != tc.wantOK {
				t.Errorf("isBase64ish(%q) = %v, want %v", tc.value, got, tc.wantOK)
			}
		})
	}
}

// End-to-end variant: a hostile OpenBao response with a malformed
// encoded_token must surface as the explicit "unexpected shape"
// error from the isBase64ish guard in GenerateRoot — NOT as a
// downstream "illegal base64 data" panic out of decodeRootToken.
// This proves the guard runs BEFORE the XOR step.
func TestGenerateRoot_MalformedEncodedToken_RejectedByGuard(t *testing.T) {
	const otp = "OTP-printable-ASCII-32-bytes-XXX" // 32 bytes
	const malformed = "this has a space"           // not base64-shaped

	var (
		started  bool
		progress int
	)
	f := &fakeK8s{
		exec: func(_ context.Context, _, pod string, cmd []string, _ []byte) ([]byte, error) {
			switch {
			case len(cmd) > 0 && cmd[0] == "true":
				return nil, nil
			case len(cmd) > 1 && cmd[0] == "bao" && cmd[1] == "status":
				if pod == "openbao-0" {
					return []byte(`{"initialized":true,"sealed":false,"ha_mode":"active"}`), nil
				}
				return []byte(`{"initialized":true,"sealed":false,"ha_mode":"standby"}`), nil
			case len(cmd) > 2 && cmd[0] == "bao" && cmd[2] == "generate-root" && containsString(cmd, "-cancel"):
				started = false
				progress = 0
				return []byte("Success! Root token generation canceled (if it was started)\n"), nil
			case len(cmd) > 2 && cmd[0] == "bao" && cmd[2] == "generate-root" && containsString(cmd, "-status"):
				return []byte(fmt.Sprintf(`{"started":%v,"progress":%d,"required":3,"complete":false,"encoded_token":"","nonce":"n-123"}`, started, progress)), nil
			case len(cmd) > 2 && cmd[0] == "bao" && cmd[2] == "generate-root" && containsString(cmd, "-generate-otp"):
				return []byte(otp + "\n"), nil
			case len(cmd) > 2 && cmd[0] == "bao" && cmd[2] == "generate-root" && containsString(cmd, "-init"):
				started = true
				return []byte(`{"nonce":"n-123","otp":"","required":3}`), nil
			case len(cmd) > 2 && cmd[0] == "bao" && cmd[2] == "generate-root" && containsString(cmd, "-nonce"):
				progress++
				if progress >= 3 {
					return []byte(fmt.Sprintf(`{"complete":true,"encoded_token":%q,"progress":3,"required":3}`, malformed)), nil
				}
				return []byte(fmt.Sprintf(`{"complete":false,"encoded_token":"","progress":%d,"required":3}`, progress)), nil
			default:
				return nil, fmt.Errorf("unexpected exec cmd: %v", cmd)
			}
		},
	}
	c := New(f)
	_, err := c.GenerateRoot(context.Background(), [][]byte{[]byte("s1"), []byte("s2"), []byte("s3")})
	if err == nil {
		t.Fatal("malformed encoded_token should be rejected")
	}
	// Specifically: must come from the isBase64ish guard, not a
	// downstream base64-decode error inside decodeRootToken.
	if !strings.Contains(err.Error(), "encoded_token has unexpected shape") {
		t.Errorf("expected isBase64ish guard to fire with 'encoded_token has unexpected shape'; got: %v", err)
	}
}

func containsString(haystack []string, needle string) bool {
	for _, s := range haystack {
		if s == needle {
			return true
		}
	}
	return false
}

func TestSetAnnotation_DelegatesToK8s(t *testing.T) {
	var capturedValue string
	f := &fakeK8s{
		setAnn: func(_ context.Context, _, _, _, value string) error {
			capturedValue = value
			return nil
		},
	}
	c := New(f)
	if err := c.SetAnnotation(context.Background(), "", "kube-dc.com/openbao-controller-auth-installed", "2026-05-26T10:05:00Z"); err != nil {
		t.Fatalf("SetAnnotation: %v", err)
	}
	if capturedValue != "2026-05-26T10:05:00Z" {
		t.Errorf("value = %q", capturedValue)
	}
}

func TestActivePodCache_TTL(t *testing.T) {
	var calls int32
	f := &fakeK8s{
		exec: func(_ context.Context, _, pod string, cmd []string, _ []byte) ([]byte, error) {
			atomic.AddInt32(&calls, 1)
			if cmd[0] == "true" {
				// Probe; respond OK for openbao-0..2.
				return nil, nil
			}
			// bao status — only openbao-0 reports active.
			if pod == "openbao-0" {
				return []byte(`{"initialized":true,"sealed":false,"ha_mode":"active"}`), nil
			}
			return []byte(`{"initialized":true,"sealed":false,"ha_mode":"standby"}`), nil
		},
	}
	c := New(f)
	first, err := c.activePodCached(context.Background())
	if err != nil {
		t.Fatalf("first call: %v", err)
	}
	if first != "openbao-0" {
		t.Errorf("active = %q want openbao-0", first)
	}
	callsAfterFirst := atomic.LoadInt32(&calls)

	// Second call within TTL: cache hit, no new exec.
	second, _ := c.activePodCached(context.Background())
	if second != first {
		t.Errorf("cache returned %q; first=%q", second, first)
	}
	if atomic.LoadInt32(&calls) != callsAfterFirst {
		t.Errorf("cache miss: calls went from %d to %d", callsAfterFirst, atomic.LoadInt32(&calls))
	}
}

// TestUnimplementedV1_ReturnErrNotImplemented was removed when the
// three stubs (ApplyPolicy / EnableAuthPath / WriteAuthRole) shipped
// real implementations as part of the M5-T08 controller-auth
// automation. See policies.go + setup_controller_auth.go.

func TestIsAlreadyUnsealed(t *testing.T) {
	if !isAlreadyUnsealed(errors.New("Error: Vault is already unsealed.")) {
		t.Error("missed canonical 'already unsealed' phrasing")
	}
	if isAlreadyUnsealed(errors.New("connection refused")) {
		t.Error("false positive on unrelated error")
	}
}

func TestIsAlreadyRevoked(t *testing.T) {
	cases := map[string]bool{
		"token not found":      true,
		"403 Forbidden":        true,
		"no token to revoke":   true,
		"connection refused":   false,
		"vault is not running": false,
	}
	for in, want := range cases {
		if got := isAlreadyRevoked(errors.New(in)); got != want {
			t.Errorf("%q: got %v want %v", in, got, want)
		}
	}
}

func TestParseStatus_BadJSON(t *testing.T) {
	var st ports.BaoStatus
	if err := parseStatus("openbao-0", []byte("not json"), &st); err == nil {
		t.Error("expected parse error on garbage")
	}
}

// E2E finding 23: a single-node file-storage deployment reports
// ha_enabled=false and the zero active_time FOREVER — the 2.5 shim
// must not synthesize "standby" from that, or activePodCached hides
// the only working pod and every post-unseal step fails with
// "no active (unsealed) pod found".
func TestParseStatus_SingleNodeFileStorageIsNotStandby(t *testing.T) {
	body := []byte(`{"type":"shamir","initialized":true,"sealed":false,
		"version":"2.5.3","storage_type":"file","ha_enabled":false,
		"active_time":"0001-01-01T00:00:00Z"}`)
	var st ports.BaoStatus
	if err := parseStatus("openbao-0", body, &st); err != nil {
		t.Fatalf("parse: %v", err)
	}
	if st.Sealed {
		t.Error("sealed should be false")
	}
	if st.HAMode != "" {
		t.Errorf("HAMode = %q, want \"\" (single-node arm must match)", st.HAMode)
	}
}

// The HA-enabled paths keep working: zero active_time → standby,
// real timestamp → active.
func TestParseStatus_HAEnabledShim(t *testing.T) {
	standby := []byte(`{"initialized":true,"sealed":false,"ha_enabled":true,
		"active_time":"0001-01-01T00:00:00Z"}`)
	active := []byte(`{"initialized":true,"sealed":false,"ha_enabled":true,
		"active_time":"2026-07-05T00:00:00Z"}`)
	var st ports.BaoStatus
	if err := parseStatus("openbao-0", standby, &st); err != nil || st.HAMode != "standby" {
		t.Errorf("standby case: HAMode=%q err=%v", st.HAMode, err)
	}
	st = ports.BaoStatus{}
	if err := parseStatus("openbao-1", active, &st); err != nil || st.HAMode != "active" {
		t.Errorf("active case: HAMode=%q err=%v", st.HAMode, err)
	}
}

// Sanity that nothing in the adapter retains share bytes — we can't
// inspect goroutine memory, but we can verify the share bytes don't
// turn up in any visible struct field. Defense-in-depth doc-test.
func TestUnseal_DoesNotRetainShare(t *testing.T) {
	f := &fakeK8s{exec: func(context.Context, string, string, []string, []byte) ([]byte, error) { return nil, nil }}
	c := New(f)
	share := []byte("secret-share-DO-NOT-LEAK")
	if err := c.Unseal(context.Background(), "openbao-0", share); err != nil {
		t.Fatal(err)
	}
	// scrub caller-side
	for i := range share {
		share[i] = 0
	}
	// Re-inspect adapter state — no field should still have the slug.
	repr := fmtClientState(c)
	if strings.Contains(repr, "secret-share") || strings.Contains(repr, "DO-NOT-LEAK") {
		t.Errorf("adapter retained share material: %s", repr)
	}
}

func fmtClientState(c *Client) string {
	return c.activePod + "|" + c.activePodFetch.String()
}

// =====================================================================
// F-bootstrap-3 kubectl-exec fallback tests (execIdempotentWriteWithRetry)
//
// The fallback contract:
//   1. WS path succeeds with sentinel → return success, no fallback
//   2. WS path returns real error (non-empty stderr) → surface
//      immediately, no fallback
//   3. WS path returns idempotent-success signal (e.g. "already
//      exists") → return success via isAlreadySuccess predicate, no
//      fallback
//   4. WS path exhausts maxAttempts with WS-drop signature
//      (empty out + nil err, OR empty out + exit-N err) → kubectl
//      fallback fires
//   5. kubectl fallback succeeds with sentinel → return success
//   6. kubectl returns ErrKubectlNotFound → surface original
//      WS-drop error with note that fallback is unavailable
//   7. Stdin payload reaches the fallback unchanged
//   8. Secret material never appears in fallback argv (same
//      stdin-not-argv contract as the WS path)
// =====================================================================

// Helper: build a fakeK8s whose WS exec always returns the
// WS-drop signature (empty stdout, no error) and whose kubectl
// fallback echoes the sentinel.
func newWSAlwaysDropsK8s(kubectlReply []byte, kubectlErr error) (*fakeK8s, *int, *int, *[]byte) {
	var (
		wsCalls      int
		kubectlCalls int
		kubectlStdin []byte
	)
	f := &fakeK8s{
		exec: func(_ context.Context, _, _ string, _ []string, _ []byte) ([]byte, error) {
			wsCalls++
			return nil, nil // WS-drop signature
		},
		kubectlExec: func(_ context.Context, _, _ string, _ []string, stdin []byte) ([]byte, error) {
			kubectlCalls++
			kubectlStdin = append([]byte(nil), stdin...)
			return kubectlReply, kubectlErr
		},
	}
	return f, &wsCalls, &kubectlCalls, &kubectlStdin
}

func TestExecIdempotentWrite_WS_DropExhausts_FallsBackToKubectl(t *testing.T) {
	const sentinel = "KUBE_DC_WRITE_OK"
	f, wsCalls, kubectlCalls, kubectlStdin := newWSAlwaysDropsK8s(
		[]byte(sentinel+"\n"), nil)
	c := New(f)

	args := []string{"sh", "-c", "fake-wrapper", "_", "arg1"}
	stdin := []byte("SECRET-TOKEN-XYZ\n")
	out, err := c.execIdempotentWriteWithRetry(
		context.Background(), "openbao-0", args, stdin, nil)
	if err != nil {
		t.Fatalf("expected fallback to succeed; got %v", err)
	}
	if !strings.Contains(string(out), sentinel) {
		t.Errorf("fallback output missing sentinel: %q", out)
	}
	if *wsCalls < 6 {
		t.Errorf("WS attempts = %d, expected 6 before fallback", *wsCalls)
	}
	if *kubectlCalls != 1 {
		t.Errorf("kubectl fallback fired %d times, expected exactly 1", *kubectlCalls)
	}
	// Stdin transparency check.
	if string(*kubectlStdin) != string(stdin) {
		t.Errorf("kubectl stdin = %q, want %q (transparency broken)", *kubectlStdin, stdin)
	}
}

func TestExecIdempotentWrite_WS_RealError_NoFallback(t *testing.T) {
	var (
		wsCalls      int
		kubectlCalls int
	)
	f := &fakeK8s{
		exec: func(_ context.Context, _, _ string, _ []string, _ []byte) ([]byte, error) {
			wsCalls++
			// Real bao error: non-empty stderr + exit code.
			return []byte("Error writing data to auth/k8s-host/role/manager: permission denied\n"),
				fmt.Errorf("exit code 1")
		},
		kubectlExec: func(_ context.Context, _, _ string, _ []string, _ []byte) ([]byte, error) {
			kubectlCalls++
			return nil, nil
		},
	}
	c := New(f)
	_, err := c.execIdempotentWriteWithRetry(
		context.Background(), "openbao-0",
		[]string{"sh", "-c", "fake"}, []byte("TOK"), nil)
	if err == nil {
		t.Fatal("expected real bao error to surface")
	}
	if wsCalls != 1 {
		t.Errorf("WS should not have retried a real error; got %d calls", wsCalls)
	}
	if kubectlCalls != 0 {
		t.Errorf("real bao error must NOT trigger kubectl fallback; got %d", kubectlCalls)
	}
}

func TestExecIdempotentWrite_WS_AlreadyExists_NoFallback(t *testing.T) {
	var (
		wsCalls      int
		kubectlCalls int
	)
	f := &fakeK8s{
		exec: func(_ context.Context, _, _ string, _ []string, _ []byte) ([]byte, error) {
			wsCalls++
			return []byte("Error enabling auth method: path is already in use at k8s-host/\n"),
				fmt.Errorf("exit code 2")
		},
		kubectlExec: func(_ context.Context, _, _ string, _ []string, _ []byte) ([]byte, error) {
			kubectlCalls++
			return nil, nil
		},
	}
	c := New(f)
	out, err := c.execIdempotentWriteWithRetry(
		context.Background(), "openbao-0",
		[]string{"sh", "-c", "fake"}, []byte("TOK"), isAlreadyEnabled)
	if err != nil {
		t.Fatalf("idempotent-success predicate should swallow the error: %v", err)
	}
	if !strings.Contains(string(out), "already in use") {
		t.Errorf("output not preserved: %q", out)
	}
	if kubectlCalls != 0 {
		t.Errorf("'already exists' must NOT trigger kubectl fallback; got %d", kubectlCalls)
	}
	_ = wsCalls
}

func TestExecIdempotentWrite_WS_FirstAttemptSucceeds_NoFallback(t *testing.T) {
	const sentinel = "KUBE_DC_WRITE_OK"
	var (
		wsCalls      int
		kubectlCalls int
	)
	f := &fakeK8s{
		exec: func(_ context.Context, _, _ string, _ []string, _ []byte) ([]byte, error) {
			wsCalls++
			return []byte(sentinel + "\n"), nil
		},
		kubectlExec: func(_ context.Context, _, _ string, _ []string, _ []byte) ([]byte, error) {
			kubectlCalls++
			return nil, nil
		},
	}
	c := New(f)
	_, err := c.execIdempotentWriteWithRetry(
		context.Background(), "openbao-0",
		[]string{"sh", "-c", "fake"}, []byte("TOK"), nil)
	if err != nil {
		t.Fatalf("WS success path failed: %v", err)
	}
	if wsCalls != 1 {
		t.Errorf("WS path should succeed on first attempt; got %d", wsCalls)
	}
	if kubectlCalls != 0 {
		t.Errorf("first-attempt success must NOT trigger fallback; got %d", kubectlCalls)
	}
}

func TestExecIdempotentWrite_KubectlMissing_PreservesWSDropError(t *testing.T) {
	f, _, _, _ := newWSAlwaysDropsK8s(nil, ports.ErrKubectlNotFound)
	c := New(f)
	_, err := c.execIdempotentWriteWithRetry(
		context.Background(), "openbao-0",
		[]string{"sh", "-c", "fake"}, []byte("TOK"), nil)
	if err == nil {
		t.Fatal("expected error when WS drops AND kubectl missing")
	}
	// Must surface errWSDrop classification — the outer ceremony
	// retry depends on it.
	if !errors.Is(err, errWSDrop) {
		t.Errorf("expected errWSDrop classification; got %v", err)
	}
	if !strings.Contains(err.Error(), "kubectl fallback unavailable") {
		t.Errorf("error should mention kubectl unavailability for triage; got %v", err)
	}
}

// Secrets-discipline regression: the args slice passed to PodExec
// (and PodExecViaKubectl) MUST NOT carry the stdin payload. This is
// the same M0-T06 batch-2 contract — extended to cover the new
// fallback method.
func TestExecIdempotentWrite_Fallback_RespectsArgvDiscipline(t *testing.T) {
	const secret = "SECRET-TOKEN-MUST-NOT-LEAK"
	var (
		kubectlArgv []string
	)
	f := &fakeK8s{
		exec: func(_ context.Context, _, _ string, _ []string, _ []byte) ([]byte, error) {
			return nil, nil // force fallback
		},
		kubectlExec: func(_ context.Context, _, _ string, cmd []string, _ []byte) ([]byte, error) {
			kubectlArgv = append([]string(nil), cmd...)
			return []byte("KUBE_DC_WRITE_OK\n"), nil
		},
	}
	c := New(f)
	args := []string{"sh", "-c", "fake-wrapper", "_", "publicarg"}
	stdin := []byte(secret + "\n")
	if _, err := c.execIdempotentWriteWithRetry(
		context.Background(), "openbao-0", args, stdin, nil); err != nil {
		t.Fatalf("fallback path failed: %v", err)
	}
	for i, a := range kubectlArgv {
		if strings.Contains(a, secret) || strings.Contains(a, "MUST-NOT-LEAK") {
			t.Errorf("secret leaked into kubectl argv[%d]: %q", i, a)
		}
	}
}

// =====================================================================
// normalizeExecStdin — POSIX-read newline contract + secret scrub
// =====================================================================

func TestNormalizeExecStdin_AppendsNewline_AllocatesOwnedCopy(t *testing.T) {
	// Caller passes a token-shaped buffer WITHOUT a trailing newline.
	// The helper must:
	//   - return a buffer ending in '\n'
	//   - NOT mutate the caller's input
	//   - return a scrub closure that zeroes the helper-owned copy
	orig := []byte("s.fake-root-token-no-newline")
	origCopy := append([]byte(nil), orig...)

	out, scrub := normalizeExecStdin(orig)
	if &out[0] == &orig[0] {
		t.Fatal("expected helper to allocate a NEW backing array (caller can't scrub the helper-owned copy otherwise)")
	}
	if out[len(out)-1] != '\n' {
		t.Errorf("expected trailing newline; got %q", out)
	}
	if string(orig) != string(origCopy) {
		t.Errorf("caller input was mutated: %q -> %q", origCopy, orig)
	}

	// Scrub must zero the helper-owned buffer.
	scrub()
	for i, b := range out {
		if b != 0 {
			t.Errorf("helper-owned buffer[%d] = %d, expected 0 after scrub", i, b)
		}
	}
}

func TestNormalizeExecStdin_AlreadyTerminated_NoOp(t *testing.T) {
	orig := []byte("s.fake-root-token-with-newline\n")
	origPtr := &orig[0]

	out, scrub := normalizeExecStdin(orig)
	// No allocation — same backing array.
	if &out[0] != origPtr {
		t.Errorf("expected no-op pass-through; helper allocated unnecessarily")
	}
	// Scrub is a no-op (doesn't zero the caller's input).
	scrub()
	if string(orig) != "s.fake-root-token-with-newline\n" {
		t.Errorf("no-op scrub mutated caller input: %q", orig)
	}
}

func TestNormalizeExecStdin_Empty_NoOp(t *testing.T) {
	out, scrub := normalizeExecStdin(nil)
	if out != nil {
		t.Errorf("nil input should return nil; got %v", out)
	}
	scrub() // must not panic
}

// Regression: when execIdempotentWriteWithRetry receives a stdin
// payload without a trailing newline, the bytes that hit PodExec
// MUST be the normalized newline-terminated form. Catches accidental
// removal of the newline-normalization step.
func TestExecIdempotentWrite_NormalizesNewlineForWrapper(t *testing.T) {
	var capturedStdin []byte
	f := &fakeK8s{
		exec: func(_ context.Context, _, _ string, _ []string, stdin []byte) ([]byte, error) {
			capturedStdin = append([]byte(nil), stdin...)
			return []byte("KUBE_DC_WRITE_OK\n"), nil
		},
	}
	c := New(f)
	// Caller passes a token WITHOUT a trailing newline (matches how
	// generate-root decode returns the token bytes).
	tokenWithoutNewline := []byte("s.root-token-no-newline")
	_, err := c.execIdempotentWriteWithRetry(
		context.Background(), "openbao-0",
		[]string{"sh", "-c", "fake"}, tokenWithoutNewline, nil)
	if err != nil {
		t.Fatalf("execIdempotentWriteWithRetry: %v", err)
	}
	if len(capturedStdin) == 0 || capturedStdin[len(capturedStdin)-1] != '\n' {
		t.Errorf("expected normalized stdin to end with '\\n'; got %q", capturedStdin)
	}
	if !bytes.Equal(capturedStdin[:len(capturedStdin)-1], tokenWithoutNewline) {
		t.Errorf("expected normalized stdin to contain the caller bytes + newline; got %q", capturedStdin)
	}
}

// =====================================================================
// RevokeSelf — now uses retry + kubectl fallback (P2 follow-up to 6c08e500)
// =====================================================================

func TestRevokeSelf_WS_DropExhausts_FallsBackToKubectl(t *testing.T) {
	const sentinel = "KUBE_DC_WRITE_OK"
	var (
		wsCalls      int
		kubectlCalls int
	)
	f := &fakeK8s{
		exec: func(_ context.Context, _, pod string, cmd []string, _ []byte) ([]byte, error) {
			// First call: bao status (active-pod detection).
			if len(cmd) > 1 && cmd[0] == "bao" && cmd[1] == "status" {
				if pod == "openbao-0" {
					return []byte(`{"initialized":true,"sealed":false,"ha_mode":"active"}`), nil
				}
				return []byte(`{"initialized":true,"sealed":false,"ha_mode":"standby"}`), nil
			}
			if len(cmd) > 0 && cmd[0] == "true" {
				return nil, nil
			}
			// Revoke wrapper: simulate WS drop (empty out, nil err).
			wsCalls++
			return nil, nil
		},
		kubectlExec: func(_ context.Context, _, _ string, _ []string, _ []byte) ([]byte, error) {
			kubectlCalls++
			return []byte(sentinel + "\n"), nil
		},
	}
	c := New(f)
	if err := c.RevokeSelf(context.Background(), []byte("s.root-token-XYZ")); err != nil {
		t.Fatalf("RevokeSelf with fallback should succeed: %v", err)
	}
	if wsCalls < 6 {
		t.Errorf("WS attempts = %d, expected 6 before fallback", wsCalls)
	}
	if kubectlCalls != 1 {
		t.Errorf("kubectl fallback fired %d times, expected 1", kubectlCalls)
	}
}

func TestRevokeSelf_AlreadyRevoked_NoFallback(t *testing.T) {
	var kubectlCalls int
	f := &fakeK8s{
		exec: func(_ context.Context, _, pod string, cmd []string, _ []byte) ([]byte, error) {
			if len(cmd) > 1 && cmd[0] == "bao" && cmd[1] == "status" {
				if pod == "openbao-0" {
					return []byte(`{"initialized":true,"sealed":false,"ha_mode":"active"}`), nil
				}
				return []byte(`{"initialized":true,"sealed":false,"ha_mode":"standby"}`), nil
			}
			if len(cmd) > 0 && cmd[0] == "true" {
				return nil, nil
			}
			// Revoke wrapper: simulate "Vault is sealed" / already-revoked.
			return []byte("Error revoking token: permission denied\n"),
				errors.New("permission denied")
		},
		kubectlExec: func(_ context.Context, _, _ string, _ []string, _ []byte) ([]byte, error) {
			kubectlCalls++
			return nil, nil
		},
	}
	c := New(f)
	err := c.RevokeSelf(context.Background(), []byte("s.expired-token"))
	// permission denied is treated as not-already-revoked — surfaces as a real error.
	if err == nil {
		t.Fatal("expected permission-denied to surface as error")
	}
	if kubectlCalls != 0 {
		t.Errorf("real bao error must NOT trigger fallback; got %d calls", kubectlCalls)
	}
}

// realKubectlStreamer-specific tests live in the K8s adapter package
// where `resolveKubectl` and the argv-construction logic are
// accessible — see internal/bootstrap/adapters/k8s/client_test.go
// (TestResolveKubectl* + TestRealKubectlStreamer_BuildsArgv*).
