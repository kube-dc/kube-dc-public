// Package openbao is the real ports.OpenBaoClient adapter. It executes
// `bao` inside the active OpenBao pod via K8sClient.PodExec — no
// direct vault SDK dependency.
//
// **Secret transport: stdin, NOT argv.** Per the M0-T06 batch-2
// review:
//
//   - Unseal feeds the share via `bao operator unseal -` on stdin.
//   - GenerateRoot feeds each share via `bao operator generate-root -nonce <n> -` on stdin.
//   - RevokeSelf pipes the token through `sh -c 'read t; ...'` reading
//     from stdin — never expanded into the `-c` script.
//
// argv shows up in the exec request body, audit logs, and in the
// in-pod process listing; stdin is the only path that keeps the
// bytes out of those surfaces.
//
// **No Init method** (agent rule 7 + ports/openbao.go NB): the
// share-generation contract lives in `bootstrap/openbao-init.sh`,
// driven via ScriptRunner + a SentinelCallback. Adding a parallel
// Go init path would create two sources of truth.
//
// **Annotation markers** (M0-T06 batch-2 review): GetAnnotation /
// SetAnnotation delegate to K8sClient.GetServiceAnnotation /
// SetServiceAnnotation against the `openbao` Service in the
// `openbao` namespace. The two operational markers
// (kube-dc.com/openbao-bootstrap-finalized,
// kube-dc.com/openbao-controller-auth-installed) ride through this
// same path — production and mock parity restored.
package openbao

import (
	"bytes"
	"context"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"regexp"
	"sort"
	"strings"
	"sync"
	"time"

	"github.com/shalb/kube-dc/cli/internal/bootstrap/ports"
)

const openbaoNamespace = "openbao"

// openbaoServiceName matches the chart's Service name; GetAnnotation /
// SetAnnotation use this implicitly when callers pass `svc=""`. The
// shipping chart uses the same name as the namespace.
const openbaoServiceName = "openbao"

const activePodCacheTTL = 30 * time.Second

// Client implements ports.OpenBaoClient.
type Client struct {
	k8s ports.K8sClient

	mu             sync.Mutex
	activePod      string
	activePodFetch time.Time
}

// Compile-time assertion.
var _ ports.OpenBaoClient = (*Client)(nil)

// New constructs a Client that delegates PodExec calls to the supplied
// K8sClient. The k8s reference is shared (not owned); caller controls
// its lifecycle.
func New(k8s ports.K8sClient) *Client {
	return &Client{k8s: k8s}
}

// ---------- ports.OpenBaoClient ----------

func (c *Client) Status(ctx context.Context, pod string) (ports.BaoStatus, error) {
	// `bao status` exits 2 on a sealed server. Some exec transports lose
	// stdout on a non-zero remote exit, which previously converted the
	// authoritative JSON into a false "already unsealed" zero value.
	// Normalize only exit 2 inside the pod so the JSON reaches the parser;
	// all other command failures remain non-zero.
	statusCmd := []string{"/bin/sh", "-c", `bao status -format=json || [ "$?" -eq 2 ]`}
	out, err := c.execWithRetry(ctx, pod, statusCmd)
	if err != nil {
		var st ports.BaoStatus
		if parseErr := parseStatus(pod, out, &st); parseErr == nil {
			return st, nil
		}
		return ports.BaoStatus{Pod: pod}, fmt.Errorf("openbao: status %s: %w", pod, err)
	}
	var st ports.BaoStatus
	if err := parseStatus(pod, out, &st); err != nil {
		return ports.BaoStatus{Pod: pod}, fmt.Errorf("openbao: parse status %s: %w", pod, err)
	}
	return st, nil
}

// writeSentinel is the marker the privileged-write wrappers emit on
// success. Used to distinguish "WS drop, command never ran" (out is
// empty or doesn't contain the sentinel) from "WS delivered, command
// succeeded" (out contains the sentinel). This is necessary because
// the WebSocket exec on eu/dc1 kube-apiserver 2026-06-08 returns
// `err=nil, out=""` ~30% of the time when stdin drops — which is
// indistinguishable from a genuine empty-output success without an
// in-wrapper signal.
const writeSentinel = "KUBE_DC_WRITE_OK"

// execIdempotentWriteWithRetry runs an upsert-semantics privileged
// write (ApplyPolicy / EnableAuthPath / ConfigureKubernetesAuth /
// WriteAuthRole) with the F-bootstrap-3 retry policy + kubectl
// fallback.
//
// All call sites pass a wrapper that ends with `echo KUBE_DC_WRITE_OK`
// AFTER the bao write succeeds. We retry while the sentinel is
// absent — that covers both WS-drop signatures:
//   - err=nil + out="" (silent successful-empty)
//   - err="exit code N" + out="" (silent error)
//
// `isAlreadySuccess` is the per-method predicate that recognizes
// "real" idempotent-success outputs (e.g. "path is already in use"
// for EnableAuthPath — those don't emit the sentinel because bao
// exited non-zero, but we still treat them as success).
//
// Empirical WS reliability for sh-c-with-stdin against eu/dc1:
// ~65% per attempt. 6 attempts → all-fail probability ~0.18%.
//
// On all-WS-attempts-exhausted (still no sentinel, no real-error
// signal), falls back to kubectl exec via the K8s adapter's
// PodExecViaKubectl. The kubectl binary has a separate
// implementation of the exec protocol that empirically does not
// share the WS drop pattern. The fallback only fires when:
//   - WS exhausted maxAttempts with the WS-drop signature, AND
//   - kubectl is on $PATH (ErrKubectlNotFound surfaces the
//     original WS-drop error instead).
//
// Real bao errors (non-empty stderr returned during the WS phase)
// surface immediately without falling back — kubectl wouldn't
// change that outcome.
func (c *Client) execIdempotentWriteWithRetry(
	ctx context.Context,
	pod string,
	args []string,
	stdin []byte,
	isAlreadySuccess func(err error, out []byte) bool,
) ([]byte, error) {
	// Newline normalization (see normalizeExecStdin docstring).
	stdin, scrubNormalized := normalizeExecStdin(stdin)
	defer scrubNormalized()
	const maxAttempts = 6
	var (
		out []byte
		err error
	)
	for attempt := 1; attempt <= maxAttempts; attempt++ {
		out, err = c.k8s.PodExec(ctx, openbaoNamespace, pod, args, stdin)
		if err == nil && bytes.Contains(out, []byte(writeSentinel)) {
			return out, nil
		}
		if isAlreadySuccess != nil && isAlreadySuccess(err, out) {
			return out, nil
		}
		// Real error signal — exit non-zero with non-empty stderr
		// that's NOT just the sentinel-absent case. Surface
		// immediately; retry won't help. (Empty `out` is presumed
		// a WS drop and IS retried.)
		if err != nil && len(out) > 0 {
			return out, err
		}
		if attempt < maxAttempts {
			select {
			case <-ctx.Done():
				return out, ctx.Err()
			case <-time.After(100 * time.Millisecond):
			}
		}
	}

	// All WS attempts exhausted with the drop signature. Try the
	// kubectl fallback once. Same args + same stdin → same
	// idempotent server-side write (or no-op if a prior attempt
	// silently succeeded server-side, since the writes are
	// upsert-semantics).
	kOut, kErr := c.k8s.PodExecViaKubectl(ctx, openbaoNamespace, pod, args, stdin)
	if kErr == nil && bytes.Contains(kOut, []byte(writeSentinel)) {
		return kOut, nil
	}
	if isAlreadySuccess != nil && isAlreadySuccess(kErr, kOut) {
		return kOut, nil
	}
	// Real error from kubectl (non-empty stderr) — surface it
	// directly, it's the most actionable signal we have.
	if kErr != nil && len(kOut) > 0 {
		return kOut, fmt.Errorf("kubectl fallback: %w", kErr)
	}
	// Kubectl unavailable OR also dropped — fall through to the
	// original WS-drop error.
	if errors.Is(kErr, ports.ErrKubectlNotFound) {
		if err == nil {
			return out, fmt.Errorf("%w: wrapper never emitted %s (%d WS attempts, kubectl fallback unavailable)", errWSDrop, writeSentinel, maxAttempts)
		}
		return out, fmt.Errorf("%w (kubectl fallback unavailable)", err)
	}
	if err == nil {
		return out, fmt.Errorf("%w: wrapper never emitted %s (%d WS attempts + kubectl fallback also empty)", errWSDrop, writeSentinel, maxAttempts)
	}
	return out, fmt.Errorf("%w (kubectl fallback also failed: %v)", err, kErr)
}

// errWSDrop wraps the all-attempts-empty failure mode so the caller
// can distinguish it from a true bao error.
var errWSDrop = fmt.Errorf("openbao: WebSocket exec drop (no sentinel)")

// ErrFileStorageBackend is returned by GenerateRoot when OpenBao is running
// the `file` storage backend, on which `sys/generate-root/attempt` returns
// 405 — so the generate-root ceremony (and everything downstream: controller-
// auth, per-org transit, KMSKey, managed-K8s etcd encryption) can never
// succeed. This turns a cryptic mid-install 405 into an actionable failure.
// The platform standard is single-node RAFT (OPENBAO_HA_ENABLED=true +
// OPENBAO_REPLICAS=1) — never file. (single-node file-backend cluster, 2026-07-09)
var ErrFileStorageBackend = fmt.Errorf("openbao: file storage backend does not support generate-root " +
	"(set OPENBAO_HA_ENABLED=true for raft in the cluster's cluster-config.env and reconcile, then re-run)")

// execWithRetry runs a stdin-less, read-only bao command with the
// F-bootstrap-3 retry policy: bounded to 6 attempts because
// WebSocket exec drops captured stdout intermittently against busy
// apiservers. Empirical reliability per attempt is ~70-90% per pod;
// 6 attempts brings the all-fail probability below 10^-4. ~100ms
// jitter between attempts avoids retry storms tripping the same
// transient apiserver-side condition.
//
// Use ONLY for read-only operations (no server-side side effects).
// For write operations the caller must implement a recovery query
// (e.g. -status after a share submission) because retry would
// duplicate the side effect.
//
// After exhausting retries on the "empty stdout, nil error" path,
// returns errWSDrop-wrapped so isRetryableCeremonyError(via errors.Is)
// classifies the failure correctly and the outer ceremony retry kicks
// in. Without this, all-empty exhaustion would surface to the caller
// as a downstream semantic error (e.g. "returned empty OTP") that the
// retry predicate can't match on without fragile message-text checks.
func (c *Client) execWithRetry(ctx context.Context, pod string, cmd []string) ([]byte, error) {
	const maxAttempts = 6
	var (
		out []byte
		err error
	)
	for attempt := 1; attempt <= maxAttempts; attempt++ {
		out, err = c.k8s.PodExec(ctx, openbaoNamespace, pod, cmd, nil)
		if err != nil {
			return out, err
		}
		if len(out) > 0 {
			return out, nil
		}
		if attempt < maxAttempts {
			select {
			case <-ctx.Done():
				return out, ctx.Err()
			case <-time.After(100 * time.Millisecond):
			}
		}
	}
	return out, fmt.Errorf("%w: read-only command returned empty output after %d attempts", errWSDrop, maxAttempts)
}

func (c *Client) PodList(ctx context.Context) ([]string, error) {
	// List actual StatefulSet members. Probing guessed ordinals through exec
	// produced phantom openbao-1/2 members on a live one-replica cluster.
	const selector = "app.kubernetes.io/instance=openbao,app.kubernetes.io/name=openbao,component=server"
	pods, err := c.k8s.ListPodNames(ctx, openbaoNamespace, selector)
	if err != nil {
		return nil, fmt.Errorf("openbao: list server pods: %w", err)
	}
	if len(pods) == 0 {
		return nil, fmt.Errorf("openbao: no server pods found in %s", openbaoNamespace)
	}
	return pods, nil
}

// Unseal feeds the share to OpenBao's `/v1/sys/unseal` HTTP API via
// `wget --post-file=-`, which reads the JSON body from stdin. Argv
// stays clean of secret material.
//
// **Why not `bao operator unseal -`** (atlantis bring-up
// 2026-05-26): OpenBao 2.5.3 does NOT honor `-` as a stdin marker
// the way HashiCorp Vault does — bao treats `-` as a literal key
// value and rejects it as "must be a valid hex or base64 string".
// Plain `bao operator unseal` (no args) refuses non-TTY stdin
// outright ("file descriptor 0 is not a terminal"). The shipped
// openbao container has wget but no curl; we use wget's
// --post-file=- to keep the share off argv while talking to the
// local unseal API directly.
//
// The JSON body buffer is scrubbed before return.
func (c *Client) Unseal(ctx context.Context, pod string, share []byte) error {
	if len(share) == 0 {
		return fmt.Errorf("openbao: Unseal called with empty share")
	}
	trimmed := bytes.TrimSpace(share)
	// Build {"key":"<share>"} — share is already a string from the
	// caller (base64 of the Shamir share). json.Marshal handles any
	// escaping the share might require.
	body, err := json.Marshal(map[string]string{"key": string(trimmed)})
	if err != nil {
		return fmt.Errorf("openbao: marshal unseal body: %w", err)
	}
	defer func() {
		for i := range body {
			body[i] = 0
		}
	}()
	cmd := []string{
		"wget", "-q", "-O-",
		"--header=Content-Type: application/json",
		"--post-file=-",
		"http://127.0.0.1:8200/v1/sys/unseal",
	}
	out, err := c.k8s.PodExec(ctx, openbaoNamespace, pod, cmd, body)
	// Scrub the response — it contains the unseal progress + nonce,
	// not secrets, but it does echo state we don't need around. The
	// already-unsealed check is text-only on the bytes.
	defer func() {
		for i := range out {
			out[i] = 0
		}
	}()
	if err != nil {
		if isAlreadyUnsealed(err) {
			return nil
		}
		if bytes.Contains(out, []byte("already unsealed")) {
			return nil
		}
		return fmt.Errorf("openbao: unseal %s: %w", pod, err)
	}
	// wget exits 0 even on HTTP 400 if we didn't pass --server-response
	// + error-handling flags. Inspect the body for the API error.
	if bytes.Contains(out, []byte("\"errors\"")) {
		// Surface only the error structure — never the body content.
		return fmt.Errorf("openbao: unseal %s: API returned errors (response redacted)", pod)
	}
	return nil
}

// RaftJoin runs `bao operator raft join <leaderURL>` inside the named
// pod. Idempotent: if the pod is already a voter in the leader's
// cluster the bao command reports `Joined: true` again (or one of the
// "already a voter / duplicate" phrases captured by isAlreadyJoined)
// and we treat that as success.
//
// `leaderURL` is the in-cluster headless-service form, typically
// "http://openbao-0.openbao-internal:8200". The follower opens a TCP
// connection to that URL and the leader pushes the cluster's TLS-encoded
// gossip + raft config; no secrets traverse the call from this side, so
// argv carries the URL safely.
func (c *Client) RaftJoin(ctx context.Context, pod, leaderURL string) error {
	out, err := c.k8s.PodExec(ctx, openbaoNamespace, pod, []string{"bao", "operator", "raft", "join", leaderURL}, nil)
	if err != nil {
		if isAlreadyJoined(err) || bytes.Contains(out, []byte("already a voter")) {
			return nil
		}
		return fmt.Errorf("openbao: raft join %s to %s: %w (%s)", pod, leaderURL, err, out)
	}
	return nil
}

// GenerateRoot walks the `bao operator generate-root` ceremony with
// each share fed via stdin (the `-` argument). The OTP returned from
// the init step is held briefly to decode the final encoded token;
// the OTP is NOT secret material (it's the one-time pad the caller
// can pair with the encoded token to recover the root token — only
// the combined pair is sensitive).
//
// F-bootstrap-3 hardening: the whole ceremony is wrapped in an outer
// retry loop. The unrecoverable edge case is the threshold share's
// response getting dropped — the OpenBao server-side state
// auto-clears after delivering the encoded_token, so a lost
// response means we have to re-run the whole ceremony from -cancel
// + -init to recover. Bounded to `outerAttempts` iterations (see
// the const below).
func (c *Client) GenerateRoot(ctx context.Context, shares [][]byte) ([]byte, error) {
	if len(shares) < 3 {
		return nil, fmt.Errorf("openbao: GenerateRoot needs at least 3 shares, got %d", len(shares))
	}
	pod, err := c.activePodCached(ctx)
	if err != nil {
		return nil, fmt.Errorf("openbao: locate active pod: %w", err)
	}
	// Fail fast on the file storage backend: generate-root 405s there, so
	// the ceremony below would burn its retries and surface a cryptic
	// "405 unsupported operation". Surface the actionable cause instead.
	if st, sErr := c.Status(ctx, pod); sErr == nil && st.StorageType == "file" {
		return nil, ErrFileStorageBackend
	}
	const outerAttempts = 5
	var (
		tok     []byte
		lastErr error
	)
	for outerAttempt := 1; outerAttempt <= outerAttempts; outerAttempt++ {
		tok, lastErr = c.generateRootOnce(ctx, pod, shares)
		if lastErr == nil {
			return tok, nil
		}
		// Only retry if the failure mode might be the WS-drop edge
		// case — "did not land after N attempts" (intermediate share
		// dropped beyond our recovery budget) or "auto-cleared"
		// (threshold-share response lost). Other errors (validation,
		// auth) are not retried.
		if !isRetryableCeremonyError(lastErr) {
			return nil, lastErr
		}
		// Make sure prior state is fully cleared before the next attempt.
		_, _ = c.k8s.PodExec(ctx, openbaoNamespace, pod,
			[]string{"bao", "operator", "generate-root", "-cancel"}, nil)
		if outerAttempt < outerAttempts {
			select {
			case <-ctx.Done():
				return nil, ctx.Err()
			case <-time.After(250 * time.Millisecond):
			}
		}
	}
	return nil, fmt.Errorf("openbao: generate-root failed after %d ceremony attempts: %w", outerAttempts, lastErr)
}

// isRetryableCeremonyError matches the WS-drop edge cases we know
// are safe to retry by re-running the whole generate-root ceremony.
// Validation errors with non-empty stderr surface immediately.
//
// Classification priority:
//  1. errors.Is(err, errWSDrop) — sentinel-wrapped explicit drops
//     from execWithRetry / execIdempotentWriteWithRetry exhaustion.
//     This is the preferred path (no string matching).
//  2. String fallbacks — for failure shapes that originate at the
//     engine layer (share-3 auto-clear, raw exec exit codes with
//     empty body). These wrap errors with task-specific phrasing
//     that doesn't carry errWSDrop, so we match on the wrapper's
//     message.
//
// Deterministic ceremony inputs (shares + opts) make every
// retryable case safe to re-run.
func isRetryableCeremonyError(err error) bool {
	if err == nil {
		return false
	}
	if errors.Is(err, errWSDrop) {
		return true
	}
	msg := err.Error()
	switch {
	case strings.Contains(msg, "did not land after"):
		// Share submission exhausted its inner retry budget — outer
		// ceremony retry resubmits with fresh state.
		return true
	case strings.Contains(msg, "auto-cleared"):
		// Threshold share's response was lost and OpenBao
		// auto-cleared the ceremony — retry from -cancel + -init.
		return true
	case strings.Contains(msg, "exited") && strings.Contains(msg, "()"):
		// Raw exec exit with empty body — WS-drop signature before
		// it reaches a wrapper that can classify with errWSDrop.
		return true
	}
	return false
}

// generateRootOnce performs a single ceremony pass (cancel → init →
// shares → decode). Called by GenerateRoot's outer retry loop.
func (c *Client) generateRootOnce(ctx context.Context, pod string, shares [][]byte) ([]byte, error) {
	var err error

	// Step 1a: pre-generate the OTP locally (OpenBao 2.5.x divergence
	// from Vault — `generate-root -init` no longer accepts no-args
	// and returns an empty body, so the script's pattern of feeding
	// `-otp=<local-otp>` to -init is the only thing that works).
	// Retried via execWithRetry because -generate-otp is read-only
	// (it doesn't mutate server state) so the WebSocket-dropped-
	// stdout race can be safely re-tried.
	otpOut, err := c.execWithRetry(ctx, pod, []string{"bao", "operator", "generate-root", "-generate-otp"})
	if err != nil {
		return nil, fmt.Errorf("openbao: generate-root -generate-otp: %w (%s)", err, otpOut)
	}
	localOTP := strings.TrimSpace(string(otpOut))
	if localOTP == "" {
		return nil, fmt.Errorf("openbao: generate-root -generate-otp returned empty OTP after retries")
	}
	// Step 1b: init the ceremony with the OTP. The init call returns
	// the nonce we feed to the share-supply step.
	//
	// Cancel any prior in-progress ceremony FIRST, then loop on
	// -status until we observe it cleared. `generate-root -cancel`
	// is documented as idempotent ("Success! Root token generation
	// canceled (if it was started)"), but its response body is
	// frequently eaten by the WebSocket-exec race so we can't rely
	// on its return value — instead verify via the read-only
	// -status query (which execWithRetry can safely retry).
	//
	// If -status shows started=true after cancel, sleep + retry up
	// to 3 times before giving up. In practice one cancel + one
	// status check is enough.
	_, _ = c.k8s.PodExec(ctx, openbaoNamespace, pod, []string{"bao", "operator", "generate-root", "-cancel"}, nil)
	{
		var pre struct {
			Started bool `json:"started"`
		}
		for i := 0; i < 3; i++ {
			statusOut, statusErr := c.execWithRetry(ctx, pod, []string{"bao", "operator", "generate-root", "-status", "-format=json"})
			if statusErr == nil && json.Unmarshal(statusOut, &pre) == nil && !pre.Started {
				break
			}
			// Re-cancel + brief pause before re-checking.
			_, _ = c.k8s.PodExec(ctx, openbaoNamespace, pod, []string{"bao", "operator", "generate-root", "-cancel"}, nil)
			select {
			case <-ctx.Done():
				return nil, ctx.Err()
			case <-time.After(150 * time.Millisecond):
			}
		}
		if pre.Started {
			return nil, fmt.Errorf("openbao: generate-root -cancel did not clear in-progress ceremony after 3 attempts")
		}
	}

	// Best-effort init (response often dropped by WS). Use -status
	// as the source of truth for the nonce afterwards — that's a
	// read-only call we can retry liberally.
	initArgs := []string{"bao", "operator", "generate-root", "-init", "-otp=" + localOTP, "-format=json"}
	_, _ = c.k8s.PodExec(ctx, openbaoNamespace, pod, initArgs, nil)

	// Read the nonce back from -status. Loop with retries — if init
	// didn't take, we'll see started=false and try once more.
	var initResp struct {
		Nonce string `json:"nonce"`
		OTP   string // populated below
	}
	{
		const initVerifyAttempts = 4
		var statusResp struct {
			Started bool   `json:"started"`
			Nonce   string `json:"nonce"`
		}
		for attempt := 1; attempt <= initVerifyAttempts; attempt++ {
			statusOut, statusErr := c.execWithRetry(ctx, pod,
				[]string{"bao", "operator", "generate-root", "-status", "-format=json"})
			if statusErr == nil && json.Unmarshal(statusOut, &statusResp) == nil && statusResp.Started && statusResp.Nonce != "" {
				initResp.Nonce = statusResp.Nonce
				break
			}
			// Not started yet — re-issue init and pause briefly.
			_, _ = c.k8s.PodExec(ctx, openbaoNamespace, pod, initArgs, nil)
			if attempt < initVerifyAttempts {
				select {
				case <-ctx.Done():
					return nil, ctx.Err()
				case <-time.After(150 * time.Millisecond):
				}
			}
		}
		if initResp.Nonce == "" {
			return nil, fmt.Errorf("openbao: generate-root init never produced a nonce (verified via -status)")
		}
	}
	// OpenBao 2.5.x: init doesn't echo back the OTP; use the local one.
	initResp.OTP = localOTP

	// Step 2: supply shares one by one — each on stdin.
	//
	// Drop-resilient submission: WebSocket exec can drop the stdin
	// (share never reaches OpenBao), the stdout (response lost but
	// share landed), or both. Each share is submitted in a retry
	// loop guarded by `-status` checks:
	//
	//   1. Submit share via stdin
	//   2. Parse response → if progress == i+1, done
	//   3. If empty response OR error: query -status
	//   4. If -status shows progress >= i+1: share landed, accept
	//   5. If -status shows progress < i+1: re-submit (bounded retry)
	//
	// The "already provided" error from a same-key re-submission
	// (when stdout was dropped but the share DID land) is also
	// handled by step 4 — we check -status FIRST before re-submitting.
	var encoded string
	type shareResp struct {
		Progress     int    `json:"progress"`
		Required     int    `json:"required"`
		Complete     bool   `json:"complete"`
		EncodedToken string `json:"encoded_token"`
	}
	for i, share := range shares {
		const maxAttempts = 6
		var resp shareResp
		accepted := false
		for attempt := 1; attempt <= maxAttempts && !accepted; attempt++ {
			// Pre-check: if -status already shows progress >= i+1,
			// share landed from a previous attempt (or by some other
			// path); skip submission entirely.
			if attempt > 1 {
				statusOut, statusErr := c.execWithRetry(ctx, pod,
					[]string{"bao", "operator", "generate-root", "-status", "-format=json"})
				if statusErr == nil && json.Unmarshal(statusOut, &resp) == nil && resp.Progress >= i+1 {
					accepted = true
					break
				}
			}

			cmd := []string{
				"bao", "operator", "generate-root",
				"-nonce", initResp.Nonce,
				"-format=json",
				"-",
			}
			out, err := c.k8s.PodExec(ctx, openbaoNamespace, pod, cmd, share)
			if err != nil {
				// "already provided" → check -status; the share may
				// have landed on a prior attempt whose stdout we lost.
				if bytes.Contains(out, []byte("already been provided")) {
					continue // loop will re-check status at the top
				}
				if attempt < maxAttempts {
					select {
					case <-ctx.Done():
						return nil, ctx.Err()
					case <-time.After(100 * time.Millisecond):
					}
					continue
				}
				return nil, fmt.Errorf("openbao: generate-root share %d: %w (%s)", i+1, err, out)
			}
			if len(out) > 0 {
				if jerr := json.Unmarshal(out, &resp); jerr == nil {
					// Accept on progress matching OR Complete=true (the
					// final share returns complete + encoded_token; some
					// bao versions omit progress in that response).
					if resp.Progress >= i+1 || resp.Complete {
						accepted = true
						break
					}
				}
			}
			// Empty response or didn't advance — verify via -status.
			statusOut, statusErr := c.execWithRetry(ctx, pod,
				[]string{"bao", "operator", "generate-root", "-status", "-format=json"})
			if statusErr == nil && json.Unmarshal(statusOut, &resp) == nil {
				if resp.Progress >= i+1 {
					accepted = true
					break
				}
				// Threshold-share auto-clear: started=false + progress=0
				// AFTER we submitted the LAST share means OpenBao
				// completed the ceremony, delivered the encoded_token
				// in a response we lost, then auto-cleared. The
				// encoded_token is gone — caller must restart the
				// whole ceremony.
				var statusResp struct {
					Started bool `json:"started"`
				}
				_ = json.Unmarshal(statusOut, &statusResp)
				if i == len(shares)-1 && !statusResp.Started && resp.Progress == 0 {
					return nil, fmt.Errorf("openbao: generate-root share %d auto-cleared (threshold response lost); needs outer-loop retry", i+1)
				}
			}
			// Brief pause before re-submitting.
			if attempt < maxAttempts {
				select {
				case <-ctx.Done():
					return nil, ctx.Err()
				case <-time.After(100 * time.Millisecond):
				}
			}
		}
		if !accepted {
			return nil, fmt.Errorf("openbao: generate-root share %d: did not land after %d attempts (last progress=%d/required=%d)", i+1, maxAttempts, resp.Progress, resp.Required)
		}
		if resp.Complete {
			encoded = resp.EncodedToken
			break
		}
	}
	if encoded == "" {
		return nil, fmt.Errorf("openbao: generate-root never completed")
	}

	// Step 3: decode — done LOCALLY in Go, NOT via an in-pod bao call.
	//
	// The OpenBao/Vault root-token decode operation is purely
	// computational: base64-decode the encoded_token to bytes, XOR
	// each byte with the corresponding byte of the OTP string. The
	// result is the printable root token (e.g. "s.Z9dEi...").
	//
	// Verified against `bao operator generate-root -decode -otp` on
	// OpenBao 2.5.3 with a full live ceremony: same input pair
	// produces identical output. The XOR-with-OTP-as-bytes pattern
	// matches the OpenBao source (xor.XORBytes against the OTP
	// treated as a byte string, not base64-decoded — OpenBao 2.5.x
	// emits OTPs as printable ASCII whose byte length already
	// matches the decoded encoded_token length).
	//
	// Doing this locally is STRICTLY better than the in-pod CLI for
	// THIS step:
	//   - The encoded_token NEVER reaches the openbao pod. It came
	//     to us in the threshold-share response and we decode it
	//     here in operator memory; the pod sees nothing more of it.
	//   - The encoded+OTP PAIR (which IS root-token-equivalent) is
	//     never reintroduced to the pod for decode. Prior in-pod
	//     wrapper approaches all leaked one or both halves inside
	//     the openbao pod (argv visible in /proc/<pid>/cmdline,
	//     mktemp file briefly readable, stdin pipe).
	//   - The decode step itself has no WS-drop exposure: no
	//     PodExec call, no shell wrapper to retry.
	//
	// NOTE: the OTP alone WAS supplied to the openbao pod earlier
	// via `-init -otp=<localOTP>` argv (see Step 1b above). That's
	// a separate, intentional decision: OTP alone is NOT root-
	// token-equivalent (it XORs with the encoded_token, which the
	// pod never sees the operator-side copy of) — anyone reading
	// init's argv has the OTP but cannot derive the root token.
	// Calling out the scope so a future reader doesn't conclude
	// "OTP never touches the pod" from this comment.
	//
	// (Older code piped the pair to an in-pod `sh -c` wrapper that
	// invoked `bao operator generate-root -decode=... -otp=...`.
	// Two attempts at file-based indirection were tried and reverted:
	// `bao write sys/generate-root/decode -` was removed in
	// OpenBao 2.5.x, and `@file` syntax on -decode/-otp is unsupported
	// — it's treated as a literal string. See the source-history
	// search at b94a4f0e for the dead-end paths.)
	if !isBase64ish(encoded) {
		return nil, fmt.Errorf("openbao: generate-root encoded_token has unexpected shape")
	}
	if !isBase64ish(initResp.OTP) {
		return nil, fmt.Errorf("openbao: generate-root otp has unexpected shape")
	}
	tok, err := decodeRootToken(encoded, initResp.OTP)
	if err != nil {
		return nil, fmt.Errorf("openbao: generate-root decode: %w", err)
	}
	if len(tok) == 0 {
		return nil, fmt.Errorf("openbao: generate-root decode produced empty token")
	}
	return tok, nil
}

// decodeRootToken reverses the OpenBao generate-root encoding:
//
//	encoded = base64(rootToken XOR otp_bytes)
//
// where otp_bytes is the OTP string's raw UTF-8 bytes (NOT
// base64-decoded — OpenBao 2.5.x emits OTPs as printable ASCII
// whose byte length already matches the decoded encoded_token).
//
// Verified end-to-end against the live `bao operator generate-root
// -decode -otp` CLI on OpenBao 2.5.3: identical inputs produce
// identical outputs. Caller MUST scrub the returned slice.
func decodeRootToken(encoded, otp string) ([]byte, error) {
	encBytes, err := base64.RawStdEncoding.DecodeString(encoded)
	if err != nil {
		// Fall back to standard (padded) encoding — bao 2.5.x emits
		// unpadded RawStdEncoding but older versions may not.
		encBytes, err = base64.StdEncoding.DecodeString(encoded)
		if err != nil {
			return nil, fmt.Errorf("base64-decode encoded_token: %w", err)
		}
	}
	otpBytes := []byte(otp)
	if len(encBytes) != len(otpBytes) {
		// Scrub the partial-decoded buffer before returning the error
		// so the caller doesn't have to think about it.
		zeroBytes(encBytes)
		return nil, fmt.Errorf("encoded_token (%d bytes) and otp (%d bytes) length mismatch", len(encBytes), len(otpBytes))
	}
	tok := make([]byte, len(encBytes))
	for i := range encBytes {
		tok[i] = encBytes[i] ^ otpBytes[i]
	}
	// Scrub the intermediate base64-decoded buffer — it's
	// (rootToken XOR otp) so still sensitive (revealed in
	// combination with otp).
	zeroBytes(encBytes)
	return tok, nil
}

// RevokeSelf reads the token from stdin inside the pod and pipes it
// into `bao token revoke -self`. The token never appears in argv,
// the `-c` script body, or an environment variable — it lives in
// the kernel pipe only for the duration of the exec.
//
// The shell snippet is intentionally minimal: read one line from
// stdin into $tok, then run bao with VAULT_TOKEN set in its env
// (process-private env, NOT argv). `read` is a builtin so it's the
// same `sh` process; no fork into a third process that could see the
// arg.
func (c *Client) RevokeSelf(ctx context.Context, token []byte) error {
	if len(token) == 0 {
		return nil // idempotent: nothing to revoke
	}
	pod, err := c.activePodCached(ctx)
	if err != nil {
		return fmt.Errorf("openbao: locate active pod: %w", err)
	}
	// RevokeSelf shares the same F-bootstrap-3 transport concerns as
	// the privileged-write helpers: stdin-bearing `sh -c` wrapper, WS
	// drops, POSIX-read-needs-newline. The cost of a silent revoke
	// failure is high — a captured root token stays valid until its
	// natural TTL — so route through execIdempotentWriteWithRetry
	// (the upsert-semantics retry budget) + the isAlreadyRevoked
	// predicate, AND emit a write-sentinel after a successful revoke
	// so we can distinguish "WS drop, never ran" from "bao revoked OK
	// but stdout was eaten".
	wrapper := `read -r tok
VAULT_TOKEN="$tok" bao token revoke -self >/dev/null
echo ` + writeSentinel
	_, err = c.execIdempotentWriteWithRetry(ctx, pod,
		[]string{"sh", "-c", wrapper}, token,
		func(err error, _ []byte) bool { return isAlreadyRevoked(err) })
	if err != nil {
		return fmt.Errorf("openbao: revoke-self: %w", err)
	}
	return nil
}

// ---- Privileged controller-auth setup methods (M5-T08 / v2) -----------
//
// All four below take a short-lived root token as the first arg after
// ctx. The caller MUST defer RevokeSelf on that token BEFORE the first
// privileged call — the engine guarantees this. Token transport into
// the pod uses a `read -r tok` shell wrapper + `VAULT_TOKEN=$tok env`
// (process-private env), never argv. Mirrors RevokeSelf's pattern.
//
// Idempotent: "already enabled" / "already exists" surface as the
// shell-script-equivalent `|| true` — caller treats as success.

// EnableAuthPath enables an auth method at the named path (e.g.
// EnableAuthPath(token, "kubernetes", "k8s-host") corresponds to
// `bao auth enable -path=k8s-host kubernetes`). Idempotent: an
// already-enabled mount returns nil.
func (c *Client) EnableAuthPath(ctx context.Context, token []byte, mountType, path string) error {
	if len(token) == 0 {
		return fmt.Errorf("openbao.EnableAuthPath: empty token")
	}
	if mountType == "" || path == "" {
		return fmt.Errorf("openbao.EnableAuthPath: mountType + path required")
	}
	pod, err := c.activePodCached(ctx)
	if err != nil {
		return fmt.Errorf("openbao.EnableAuthPath: locate active pod: %w", err)
	}
	wrapper := `read -r tok
VAULT_TOKEN="$tok" bao auth enable -path="$1" "$2" >/dev/null
echo ` + writeSentinel
	args := []string{"sh", "-c", wrapper, "_", path, mountType}
	out, err := c.execIdempotentWriteWithRetry(ctx, pod, args, token, isAlreadyEnabled)
	if err != nil {
		return fmt.Errorf("openbao.EnableAuthPath: %w (%s)", err, sanitizeAuthOutput(out))
	}
	return nil
}

// ConfigureKubernetesAuth writes the config sub-path of an enabled
// Kubernetes auth mount. The CA bundle is multi-line PEM — it can't
// ride argv, so the adapter pipes (token \n CA) over stdin: line 1
// goes into VAULT_TOKEN, the rest into a mktemp'd file that `bao`
// reads via the `@<path>` indirection. `trap rm -f $tmp EXIT` cleans
// up on any exit path including SIGINT mid-exec.
func (c *Client) ConfigureKubernetesAuth(ctx context.Context, token []byte, path string, opts ports.KubernetesAuthConfig) error {
	if len(token) == 0 {
		return fmt.Errorf("openbao.ConfigureKubernetesAuth: empty token")
	}
	if path == "" {
		return fmt.Errorf("openbao.ConfigureKubernetesAuth: path required")
	}
	if opts.KubernetesHost == "" || opts.KubernetesCACert == "" {
		return fmt.Errorf("openbao.ConfigureKubernetesAuth: KubernetesHost + KubernetesCACert required")
	}
	pod, err := c.activePodCached(ctx)
	if err != nil {
		return fmt.Errorf("openbao.ConfigureKubernetesAuth: locate active pod: %w", err)
	}
	disableISS := "false"
	if opts.DisableISSValidation {
		disableISS = "true"
	}
	// stdin layout: line 1 = token, lines 2..N = CA PEM
	stdin := append([]byte(nil), token...)
	stdin = append(stdin, '\n')
	stdin = append(stdin, []byte(opts.KubernetesCACert)...)
	// The shell wrapper:
	//   - read one line for the token (kernel pipe -> $tok)
	//   - mktemp + trap for CA file lifecycle (non-secret but
	//     stale-file hygiene matters)
	//   - rest of stdin -> $tmp via `cat >`
	//   - VAULT_TOKEN=$tok bao write auth/<path>/config \
	//       kubernetes_host=<host> kubernetes_ca_cert=@$tmp \
	//       disable_iss_validation=<bool>
	wrapper := `read -r tok
tmp="$(mktemp /tmp/kube-dc-ca.XXXXXX)"
trap 'rm -f "$tmp"' EXIT
cat > "$tmp"
VAULT_TOKEN="$tok" bao write "auth/$1/config" \
    "kubernetes_host=$2" \
    "kubernetes_ca_cert=@$tmp" \
    "disable_iss_validation=$3" >/dev/null
echo ` + writeSentinel
	args := []string{"sh", "-c", wrapper, "_", path, opts.KubernetesHost, disableISS}
	out, err := c.execIdempotentWriteWithRetry(ctx, pod, args, stdin, nil)
	// Scrub stdin (contains token bytes).
	for i := range stdin {
		stdin[i] = 0
	}
	if err != nil {
		return fmt.Errorf("openbao.ConfigureKubernetesAuth: %w (%s)", err, sanitizeAuthOutput(out))
	}
	return nil
}

// ApplyPolicy writes (creates or updates) a policy under
// `sys/policies/acl/<name>`. The HCL goes via stdin so it never
// hits argv (the policy is non-secret but argv buffers are noisy);
// the token rides VAULT_TOKEN env via the same pattern as RevokeSelf.
//
// Stdin layout: line 1 = token, lines 2..N = HCL.
func (c *Client) ApplyPolicy(ctx context.Context, token []byte, name, hcl string) error {
	if len(token) == 0 {
		return fmt.Errorf("openbao.ApplyPolicy: empty token")
	}
	if name == "" || hcl == "" {
		return fmt.Errorf("openbao.ApplyPolicy: name + hcl required")
	}
	pod, err := c.activePodCached(ctx)
	if err != nil {
		return fmt.Errorf("openbao.ApplyPolicy: locate active pod: %w", err)
	}
	stdin := append([]byte(nil), token...)
	stdin = append(stdin, '\n')
	stdin = append(stdin, []byte(hcl)...)
	// `bao policy write NAME -` reads policy HCL from stdin. We use a
	// here-doc-style wrapper that reads the token first, then sends
	// the remaining stdin to the inner `bao policy write` via a
	// process substitution would be simpler but `sh` may not support
	// it portably — use `cat | bao` instead.
	wrapper := `read -r tok
cat | VAULT_TOKEN="$tok" bao policy write "$1" - >/dev/null
echo ` + writeSentinel
	args := []string{"sh", "-c", wrapper, "_", name}
	out, err := c.execIdempotentWriteWithRetry(ctx, pod, args, stdin, nil)
	for i := range stdin {
		stdin[i] = 0
	}
	if err != nil {
		return fmt.Errorf("openbao.ApplyPolicy(%s): %w (%s)", name, err, sanitizeAuthOutput(out))
	}
	return nil
}

// WriteAuthRole writes a role under auth/<mount>/role/<role> using
// the supplied params as k=v argv elements. Role params (SA names,
// TTLs, comma-separated namespaces) aren't secret so argv is fine;
// the token still rides stdin + VAULT_TOKEN env.
//
// Idempotent: an existing role gets overwritten (bao write semantics).
// "node already exists" / "already a voter" don't apply here — those
// belong to RaftJoin.
func (c *Client) WriteAuthRole(ctx context.Context, token []byte, mount, role string, params map[string]string) error {
	if len(token) == 0 {
		return fmt.Errorf("openbao.WriteAuthRole: empty token")
	}
	if mount == "" || role == "" {
		return fmt.Errorf("openbao.WriteAuthRole: mount + role required")
	}
	pod, err := c.activePodCached(ctx)
	if err != nil {
		return fmt.Errorf("openbao.WriteAuthRole: locate active pod: %w", err)
	}
	// Sort keys for deterministic argv order — easier test assertions
	// and easier audit-log reading.
	keys := make([]string, 0, len(params))
	for k := range params {
		keys = append(keys, k)
	}
	sort.Strings(keys)

	// Build argv suffix: "k1=v1" "k2=v2" ... POSIX `shift` is used
	// in the wrapper instead of bash's ${@:3} because the openbao
	// pod's `sh` is busybox/distroless sh, not bash.
	wrapperArgs := []string{"sh", "-c",
		`set -eu
read -r tok
mount=$1
role=$2
shift 2
VAULT_TOKEN="$tok" bao write "auth/$mount/role/$role" "$@"
echo ` + writeSentinel,
		"_", mount, role,
	}
	for _, k := range keys {
		wrapperArgs = append(wrapperArgs, fmt.Sprintf("%s=%s", k, params[k]))
	}
	out, err := c.execIdempotentWriteWithRetry(ctx, pod, wrapperArgs, token, nil)
	if err != nil {
		return fmt.Errorf("openbao.WriteAuthRole(%s/%s): %w (%s)", mount, role, err, sanitizeAuthOutput(out))
	}
	return nil
}

// isAlreadyEnabled detects the OpenBao "path is already in use"
// response that `bao auth enable` returns when the mount exists. We
// also catch the rarer "already exists" wording from older builds.
func isAlreadyEnabled(err error, out []byte) bool {
	if err == nil {
		return false
	}
	msg := strings.ToLower(err.Error()) + " " + strings.ToLower(string(out))
	return strings.Contains(msg, "path is already in use") ||
		strings.Contains(msg, "already exists")
}

// sanitizeAuthOutput trims bao's banner / progress lines off the
// captured exec output so wrapped errors stay readable. Cheap
// best-effort — never logs the input token (no stdin was ever
// echoed back by bao in any of these paths).
func sanitizeAuthOutput(out []byte) string {
	s := strings.TrimSpace(string(out))
	if len(s) > 400 {
		s = s[:400] + "…"
	}
	return s
}

// GetAnnotation reads the named annotation from the OpenBao Service.
// Delegates to K8sClient.GetServiceAnnotation; the empty string +
// nil error is a well-formed "annotation absent" signal.
func (c *Client) GetAnnotation(ctx context.Context, svc, key string) (string, error) {
	if svc == "" {
		svc = openbaoServiceName
	}
	return c.k8s.GetServiceAnnotation(ctx, openbaoNamespace, svc, key)
}

// SetAnnotation writes (or replaces) the named annotation on the
// OpenBao Service. Used for the bootstrap-finalized + controller-
// auth-installed markers M5 maintains.
func (c *Client) SetAnnotation(ctx context.Context, svc, key, value string) error {
	if svc == "" {
		svc = openbaoServiceName
	}
	return c.k8s.SetServiceAnnotation(ctx, openbaoNamespace, svc, key, value)
}

// SetAnnotations writes a batch of annotations on the OpenBao Service
// in a single merge-patch. Used by init Phase C to stamp both
// finalized + controller-auth-installed markers atomically.
func (c *Client) SetAnnotations(ctx context.Context, svc string, kv map[string]string) error {
	if svc == "" {
		svc = openbaoServiceName
	}
	return c.k8s.SetServiceAnnotations(ctx, openbaoNamespace, svc, kv)
}

// ---------- helpers ----------

// activePodCached returns the name of the active OpenBao pod, using
// the cached value when fresh.
func (c *Client) activePodCached(ctx context.Context) (string, error) {
	c.mu.Lock()
	if c.activePod != "" && time.Since(c.activePodFetch) < activePodCacheTTL {
		pod := c.activePod
		c.mu.Unlock()
		return pod, nil
	}
	c.mu.Unlock()

	pods, err := c.PodList(ctx)
	if err != nil {
		return "", err
	}
	for _, pod := range pods {
		st, err := c.Status(ctx, pod)
		if err != nil {
			continue
		}
		if st.HAMode == "active" || (!st.Sealed && st.HAMode == "") {
			c.mu.Lock()
			c.activePod = pod
			c.activePodFetch = time.Now()
			c.mu.Unlock()
			return pod, nil
		}
	}
	return "", fmt.Errorf("openbao: no active (unsealed) pod found among %v", pods)
}

// parseStatus consumes `bao status -format=json` output and fills st.
//
// **Tolerance**: the K8sClient combines stdout+stderr into one
// buffer, and in practice (atlantis bring-up 2026-05-26 + eu/dc1
// 2026-06-08 on WebSocket exec) the apiserver remotecommand stream
// can prepend or append a stray byte from the error channel —
// enough to make json.Unmarshal reject the whole payload. Locate
// the first '{' and feed json.Decoder, which stops after the first
// complete object. Anything after the JSON is ignored.
func parseStatus(pod string, out []byte, st *ports.BaoStatus) error {
	st.Pod = pod
	var raw struct {
		Initialized  bool   `json:"initialized"`
		Sealed       bool   `json:"sealed"`
		Version      string `json:"version"`
		HAMode       string `json:"ha_mode"` // legacy Vault field
		RaftIndex    uint64 `json:"raft_index"`
		ActiveNodeID string `json:"active_node_id"`

		// OpenBao 2.5.x dropped ha_mode in favour of these two:
		//   - active_time != "0001-01-01T00:00:00Z" → this pod is the leader
		//   - is_self     == true && active_time set → confirmation
		// We populate HAMode from these so downstream activePodCached
		// logic (which checks HAMode == "active") keeps working
		// against OpenBao 2.5+.
		ActiveTime string `json:"active_time"`
		IsSelf     bool   `json:"is_self"`

		// Gate for the active_time shim (E2E finding 23): on a
		// single-node file-storage deployment ha_enabled=false and
		// active_time is ALWAYS the zero value — deriving "standby"
		// from it marked the only (unsealed, working) pod as
		// standby and activePodCached found no active pod at all.
		HAEnabled bool `json:"ha_enabled"`

		// storage_type distinguishes raft (required) from file (broken:
		// 405s generate-root). Surfaced so the generate-root ceremony can
		// fail fast with an actionable message instead of a cryptic 405.
		StorageType string `json:"storage_type"`
	}
	start := bytes.IndexByte(out, '{')
	if start < 0 {
		return fmt.Errorf("no JSON object in bao-status output (%d bytes)", len(out))
	}
	dec := json.NewDecoder(bytes.NewReader(out[start:]))
	if err := dec.Decode(&raw); err != nil {
		return err
	}
	st.Initialized = raw.Initialized
	st.Sealed = raw.Sealed
	st.Version = raw.Version
	st.HAMode = raw.HAMode
	st.RaftIndex = raw.RaftIndex
	st.ActiveNodeID = raw.ActiveNodeID
	st.StorageType = raw.StorageType
	// OpenBao 2.5+ compatibility: derive HAMode from active_time
	// when the legacy ha_mode field is absent. active_time is the
	// zero RFC3339 value ("0001-01-01T00:00:00Z") for standbys; any
	// other timestamp means this pod is the active leader. ONLY
	// meaningful when HA is enabled — a single-node file-storage
	// deployment (ha_enabled=false) always reports the zero
	// active_time, and synthesizing "standby" there hid the only
	// working pod from activePodCached (E2E finding 23). With HA
	// disabled we leave HAMode "" so the single-node arm
	// (!Sealed && HAMode == "") matches.
	if st.HAMode == "" && raw.HAEnabled {
		if raw.ActiveTime != "" && raw.ActiveTime != "0001-01-01T00:00:00Z" {
			st.HAMode = "active"
		} else if raw.ActiveTime == "0001-01-01T00:00:00Z" {
			st.HAMode = "standby"
		}
	}
	return nil
}

func isPodNotFound(err error) bool {
	msg := strings.ToLower(err.Error())
	return strings.Contains(msg, "not found") && strings.Contains(msg, "pod")
}

// isPodNotScheduled matches the kubelet/apiserver error a `kubectl exec`
// returns when a pod exists in the API but hasn't been scheduled onto
// a node yet. For StatefulSet enumeration this is functionally the same
// as a 404: we should skip past it and treat the cluster as ending at
// the previous pod. The exact phrase comes from the apiserver's
// streaming-attach handler — observed verbatim during the eu/dc1
// bring-up where openbao-1 was created mid-Init.
func isPodNotScheduled(err error) bool {
	return strings.Contains(strings.ToLower(err.Error()), "does not have a host assigned")
}

func isAlreadyUnsealed(err error) bool {
	return strings.Contains(strings.ToLower(err.Error()), "already unsealed")
}

// isAlreadyJoined matches the bao CLI's responses for an idempotent
// raft join — when a pod is re-asked to join the same leader, bao
// returns one of these phrases instead of mutating cluster state.
func isAlreadyJoined(err error) bool {
	msg := strings.ToLower(err.Error())
	return strings.Contains(msg, "already a voter") ||
		strings.Contains(msg, "duplicate") ||
		strings.Contains(msg, "node already exists")
}

func isAlreadyRevoked(err error) bool {
	if err == nil {
		return false
	}
	msg := strings.ToLower(err.Error())
	return strings.Contains(msg, "token not found") || strings.Contains(msg, "no token") || strings.Contains(msg, "403")
}

// base64ishRegex matches bao's generate-root output shape (URL-safe-
// or-standard base64 with optional padding). Used as a defensive
// pre-validation guard before passing the encoded_token + OTP into
// decodeRootToken — catches obviously-malformed values from a
// hostile/buggy OpenBao server before they reach the base64 +
// XOR step.
var base64ishRegex = regexp.MustCompile(`^[A-Za-z0-9+/_=-]+$`)

func isBase64ish(s string) bool {
	return s != "" && base64ishRegex.MatchString(s)
}

// zeroBytes overwrites b. Used to scrub buffers that briefly hold
// secret material (root tokens, shares, decoded-XOR intermediates).
func zeroBytes(b []byte) {
	for i := range b {
		b[i] = 0
	}
}

// normalizeExecStdin guarantees the input ends with a trailing newline
// so POSIX `read -r tok` inside the in-pod shell wrapper succeeds.
//
// The kubectl exec transport is strictly POSIX: a stdin payload that
// doesn't end with '\n' causes `read` to return 1 + the wrapper exits
// 1 under `set -e` (the WS streamer historically rode past this
// because client-go closed the stdin half cleanly enough that read
// returned 0 with the partial line — the kubectl fallback exposed
// the latent bug).
//
// Security discipline: when the input is already newline-terminated
// (or empty), we return it unchanged + a no-op scrub. When we need
// to add the terminator, we allocate a FRESH owned buffer, copy the
// secret bytes in, append '\n', and return a scrub closure the
// caller MUST defer. Naive `append(stdin, '\n')` would silently grow
// the caller's backing array (or allocate a new one the caller
// can't see) — either way, neither the original caller's scrub nor
// this helper would zero the bytes consistently. Owning the copy
// makes the scrub responsibility explicit.
//
// Caller contract: ALWAYS `defer scrub()` immediately after the
// return — no exceptions. The scrub is idempotent (zeroes empty
// slices as a no-op).
func normalizeExecStdin(stdin []byte) (out []byte, scrub func()) {
	if len(stdin) == 0 || stdin[len(stdin)-1] == '\n' {
		return stdin, func() {}
	}
	owned := make([]byte, len(stdin)+1)
	copy(owned, stdin)
	owned[len(stdin)] = '\n'
	return owned, func() { zeroBytes(owned) }
}
