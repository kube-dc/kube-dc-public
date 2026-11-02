package openbao

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"io"
	"path/filepath"
	"regexp"
	"time"

	"github.com/shalb/kube-dc/cli/internal/bootstrap/ports"
)

// M5-T02 — `kube-dc bootstrap openbao unseal`.
//
// Reads `OPENBAO_UNSEAL_KEY_{1..3}` from
// `clusters/<name>/secrets.enc.yaml` via SOPS.Decrypt, then runs
// OpenBaoClient.Unseal on every sealed pod. Idempotent: pods that
// are already unsealed are no-ops.
//
// **Threshold = 3** (matches openbao-init.sh's `-key-threshold=3`).
// We only need 3 of 5 shares to unseal; reading just those keeps
// the in-memory plaintext window smaller. The remaining 2 shares
// stay encrypted on disk.
//
// **Share scrub discipline**: shares are passed to
// OpenBaoClient.Unseal as `[]byte` per the port's plaintext
// discipline; the adapter scrubs after the bao exec returns. The
// engine here scrubs its local copies on every iteration.

// UnsealOptions is the parameter bundle for Unseal.
type UnsealOptions struct {
	// ClusterName is the fleet cluster name.
	ClusterName string
	// FleetRepo is the absolute path of the fleet repo.
	FleetRepo string
	// SOPS decrypts secrets.enc.yaml in memory.
	SOPS ports.SOPSClient
	// OpenBao runs `bao operator unseal` per pod.
	OpenBao ports.OpenBaoClient
	// K8s, when set, supplies the StatefulSet's replica count so Unseal also
	// waits for followers that the OrderedReady policy has not created yet.
	K8s ports.K8sClient
	// FollowerWait bounds that wait (default two minutes): a member that
	// cannot start for another reason must not hold the command for long.
	FollowerWait time.Duration
	// Out is the operator-facing log writer.
	Out io.Writer
}

// --- Errors ---

// ErrUnsealMissingShares is returned when secrets.enc.yaml lacks
// one or more of OPENBAO_UNSEAL_KEY_{1..3}. Indicates the cluster
// wasn't run through `bootstrap openbao init` yet, OR the
// encrypted file is corrupt.
var ErrUnsealMissingShares = errors.New("openbao: secrets.enc.yaml is missing one or more OPENBAO_UNSEAL_KEY_{1..3} entries — run `bootstrap openbao init` first")

// ErrUnsealMissingDependency surfaces on missing/nil adapter.
var ErrUnsealMissingDependency = errors.New("openbao: unseal missing required dependency")

// --- Engine ---

// Unseal walks every OpenBao pod, unseals the sealed ones with the
// first 3 shares from secrets.enc.yaml, and reports the result.
// Returns nil on success (including when no pods needed unsealing).
func Unseal(ctx context.Context, opts UnsealOptions) error {
	if err := validateUnsealOptions(opts); err != nil {
		return err
	}
	out := opts.Out
	if out == nil {
		out = io.Discard
	}

	// (1) Decrypt secrets.enc.yaml in memory, extract the 3
	// threshold shares.
	secretsPath := filepath.Join(opts.FleetRepo, "clusters", opts.ClusterName, "secrets.enc.yaml")
	decrypted, err := opts.SOPS.Decrypt(ctx, secretsPath)
	if err != nil {
		return fmt.Errorf("openbao: decrypt %s: %w", secretsPath, err)
	}
	// Scrub the decrypted blob when we're done with it — it
	// contains all 5 shares + any other cluster-scoped secrets.
	defer func() {
		for i := range decrypted {
			decrypted[i] = 0
		}
	}()

	shares, err := extractThresholdShares(decrypted)
	if err != nil {
		return err
	}
	// Scrub each share at the end of Unseal regardless of which
	// path returns. The Unseal port copies internally per share
	// per pod; our local references can drop after the pod loop.
	defer func() {
		for _, s := range shares {
			for i := range s {
				s[i] = 0
			}
		}
	}()

	// (2) Enumerate OpenBao pods.
	pods, err := opts.OpenBao.PodList(ctx)
	if err != nil {
		return fmt.Errorf("openbao: list pods: %w", err)
	}
	if len(pods) == 0 {
		return fmt.Errorf("openbao: no OpenBao pods found (HelmRelease not yet Ready?)")
	}
	wait := opts.FollowerWait
	if wait == 0 {
		wait = 2 * time.Minute
	}
	return unsealMembers(ctx, opts.OpenBao, opts.K8s, pods, shares, out, wait)
}

// initFollowerWait bounds how long init waits for StatefulSet members that
// do not exist yet: each starts only after the previous one is unsealed.
const initFollowerWait = 10 * time.Minute

// followerPoll is the interval between member checks while waiting.
var followerPoll = 5 * time.Second

// raftJoinWait bounds the retries of one follower's Raft join. A follower
// that starts with the leader (parallel pod management) asks to join while
// the just-unsealed leader is not active yet, and the leader answers
// "failed to get raft challenge" until it is. A follower's status read is
// retried for the same window, so one poll of the follower wait can take up
// to twice raftJoinWait per follower beyond the follower wait itself.
var raftJoinWait = 2 * time.Minute

// unsealMembers unseals the current members, then the remaining StatefulSet
// members as they appear: the chart's OrderedReady policy creates each
// follower only after the previous member is Ready, which needs an unseal.
func unsealMembers(ctx context.Context, client ports.OpenBaoClient, k8s ports.K8sClient, pods []string, shares [][]byte, out io.Writer, followerWait time.Duration) error {
	if err := unsealPods(ctx, client, pods, shares, out); err != nil {
		return err
	}
	want := openBaoReplicas(ctx, k8s)
	if len(pods) >= want {
		return nil
	}
	fmt.Fprintf(out, "[openbao] waiting for %d more member(s) to start (ordered StatefulSet)\n", want-len(pods))
	deadline := time.Now().Add(followerWait)
	var lastErr error
	for {
		select {
		case <-ctx.Done():
			return ctx.Err()
		case <-time.After(followerPoll):
		}
		current, err := client.PodList(ctx)
		if err == nil {
			err = unsealPods(ctx, client, current, shares, io.Discard)
			if err == nil && len(current) >= want {
				fmt.Fprintf(out, "[openbao] all %d member(s) unsealed\n", len(current))
				return nil
			}
		}
		if err != nil {
			lastErr = err // a starting container cannot answer yet; keep waiting
		}
		if time.Now().After(deadline) {
			if lastErr != nil {
				return fmt.Errorf("openbao: members did not all unseal within %s: %w", followerWait, lastErr)
			}
			return fmt.Errorf("openbao: only %d of %d members exist after %s", len(current), want, followerWait)
		}
	}
}

// openBaoReplicas reads spec.replicas of openbao/openbao. Zero means unknown,
// in which case the caller does not wait for further members.
func openBaoReplicas(ctx context.Context, k8s ports.K8sClient) int {
	if k8s == nil {
		return 0
	}
	objs, err := k8s.ListResourceObjects(ctx, "apps", "v1", "statefulsets", "openbao")
	if err != nil {
		return 0
	}
	for _, obj := range objs {
		meta, _ := obj["metadata"].(map[string]any)
		spec, _ := obj["spec"].(map[string]any)
		if meta == nil || spec == nil || meta["name"] != "openbao" {
			continue
		}
		switch n := spec["replicas"].(type) {
		case int64:
			return int(n)
		case float64:
			return int(n)
		case int:
			return n
		}
	}
	return 0
}

// unsealPods joins and unseals every sealed member with the threshold shares.
// Init uses it too, so a fresh HA install leaves no member sealed.
func unsealPods(ctx context.Context, client ports.OpenBaoClient, pods []string, shares [][]byte, out io.Writer) error {

	// (3) Per-pod unseal. Skip pods reporting Sealed=false.
	//
	// Uninitialized followers (Sealed=true + Initialized=false, as
	// reported by `bao status` on a fresh Raft pod) cannot be
	// unsealed directly — the shamir barrier hasn't been written to
	// their local storage yet. They must be joined to the leader's
	// Raft cluster first, then unsealed with the leader's shares.
	// This is the M5-T02 follow-up that handles the OrderedReady
	// race observed during eu/dc1 bring-up: the StatefulSet creates
	// openbao-1/2 only AFTER openbao-0 becomes Ready, so the first
	// time `unseal` runs against a fresh HA install, openbao-1/2
	// come up with empty storage and need the join before the
	// unseal makes any progress.
	leader := pods[0]
	leaderURL := fmt.Sprintf("http://%s.openbao-internal:8200", leader)
	unsealed := 0
	alreadyUnsealed := 0
	for _, pod := range pods {
		st, err := client.Status(ctx, pod)
		if err != nil && pod != leader {
			// A follower started with the leader may not listen yet.
			st, err = statusWithRetry(ctx, client, pod)
		}
		if err != nil {
			return fmt.Errorf("openbao: status %s: %w", pod, err)
		}
		if !st.Sealed {
			fmt.Fprintf(out, "[openbao] %s already unsealed (skipping)\n", pod)
			alreadyUnsealed++
			continue
		}
		// Auto-join followers that haven't seen the cluster yet. We
		// only touch pods other than the leader — the leader was
		// initialized by `bootstrap openbao init` and is the
		// authoritative source. A non-leader pod reporting
		// Initialized=false here is the OrderedReady race; raft
		// join is idempotent so doing it on an already-joined
		// follower is safe (the adapter swallows the duplicate-join
		// error).
		if !st.Initialized && pod != leader {
			fmt.Fprintf(out, "[openbao] %s uninitialized — raft-joining %s\n", pod, leaderURL)
			if err := raftJoinWithRetry(ctx, client, pod, leaderURL); err != nil {
				return fmt.Errorf("openbao: raft join %s to %s: %w", pod, leaderURL, err)
			}
		}
		fmt.Fprintf(out, "[openbao] unsealing %s\n", pod)
		// Apply 3 distinct shares; the bao operator unseal command
		// is incremental — after 3 it transitions from sealed to
		// unsealed.
		for i, share := range shares {
			if err := client.Unseal(ctx, pod, share); err != nil {
				return fmt.Errorf("openbao: unseal %s with share %d: %w", pod, i+1, err)
			}
		}
		unsealed++
	}
	fmt.Fprintf(out, "[openbao] unseal complete — %d unsealed, %d already-unsealed (total %d pods)\n",
		unsealed, alreadyUnsealed, len(pods))
	return nil
}

// statusWithRetry reads a follower's status until raftJoinWait passes.
func statusWithRetry(ctx context.Context, client ports.OpenBaoClient, pod string) (ports.BaoStatus, error) {
	deadline := time.Now().Add(raftJoinWait)
	for {
		st, err := client.Status(ctx, pod)
		if err == nil || !time.Now().Before(deadline) {
			return st, err
		}
		select {
		case <-ctx.Done():
			return st, err
		case <-time.After(followerPoll):
		}
	}
}

// raftJoinWithRetry joins pod to the leader, retrying until raftJoinWait
// passes; the last error is returned.
func raftJoinWithRetry(ctx context.Context, client ports.OpenBaoClient, pod, leaderURL string) error {
	deadline := time.Now().Add(raftJoinWait)
	for {
		err := client.RaftJoin(ctx, pod, leaderURL)
		if err == nil || !time.Now().Before(deadline) {
			return err
		}
		select {
		case <-ctx.Done():
			return err
		case <-time.After(followerPoll):
		}
	}
}

func validateUnsealOptions(opts UnsealOptions) error {
	if opts.ClusterName == "" {
		return fmt.Errorf("%w: ClusterName", ErrUnsealMissingDependency)
	}
	if opts.FleetRepo == "" {
		return fmt.Errorf("%w: FleetRepo", ErrUnsealMissingDependency)
	}
	if opts.SOPS == nil {
		return fmt.Errorf("%w: SOPS", ErrUnsealMissingDependency)
	}
	if opts.OpenBao == nil {
		return fmt.Errorf("%w: OpenBao", ErrUnsealMissingDependency)
	}
	return nil
}

// shareValueRegex matches the OPENBAO_UNSEAL_KEY_<N>: <value> entry
// inside the decrypted secrets.enc.yaml. The value is the canonical
// `unseal_keys_b64` element from `bao operator init` — base64-ish
// (A-Za-z0-9+/= and the `_-` variants).
//
// Multiline mode + start-of-line anchor so we don't match
// substrings inside another key's value. The capture group is the
// value.
var shareValueRegex = regexp.MustCompile(`(?m)^\s*OPENBAO_UNSEAL_KEY_(\d+):\s*([A-Za-z0-9+/=_-]+)\s*$`)

// extractThresholdShares pulls OPENBAO_UNSEAL_KEY_{1..3} out of the
// decrypted YAML. Returns 3 freshly-allocated byte slices the caller
// scrubs.
//
// **Why string parsing instead of yaml.Unmarshal**: the decrypted
// secrets.enc.yaml is a Kubernetes Secret with `stringData:` —
// unmarshaling pulls every key into a map, expanding the plaintext
// surface. Regex-grep on the specific 3 keys we need keeps the
// plaintext window narrow.
func extractThresholdShares(decrypted []byte) ([][]byte, error) {
	matches := shareValueRegex.FindAllSubmatchIndex(decrypted, -1)

	// Build a 1-indexed slot map keyed by the share number; we
	// populate slots 1-5 if present, then return slots 1-3 for the
	// threshold.
	found := make(map[int][]byte, 5)
	for _, m := range matches {
		// m[2:4] is the share-number capture; m[4:6] is the value.
		numBytes := decrypted[m[2]:m[3]]
		valBytes := decrypted[m[4]:m[5]]
		// Atoi without strconv to avoid the alloc — the regex
		// already restricted to digits.
		n := 0
		for _, c := range numBytes {
			n = n*10 + int(c-'0')
		}
		if n < 1 || n > 5 {
			continue
		}
		// Defensive copy — we'll scrub these independently of the
		// shared decrypted blob.
		dup := make([]byte, len(valBytes))
		copy(dup, valBytes)
		found[n] = dup
	}

	shares := make([][]byte, 0, 3)
	missing := []int{}
	for i := 1; i <= 3; i++ {
		v, ok := found[i]
		if !ok || len(v) == 0 {
			missing = append(missing, i)
			continue
		}
		shares = append(shares, v)
	}
	if len(missing) > 0 {
		// Best-effort scrub of any shares we managed to collect
		// before returning the error.
		for _, s := range shares {
			for i := range s {
				s[i] = 0
			}
		}
		// Also scrub the 4 + 5 shares we may have pulled in `found`
		// since they're irrelevant to the threshold path.
		for k, v := range found {
			if k <= 3 {
				continue
			}
			for i := range v {
				v[i] = 0
			}
		}
		return nil, fmt.Errorf("%w (missing keys: %v)", ErrUnsealMissingShares, missing)
	}
	// Scrub the unused shares 4 + 5.
	for k, v := range found {
		if k <= 3 {
			continue
		}
		for i := range v {
			v[i] = 0
		}
	}
	return shares, nil
}

// Compile-time guard: bytes.Contains stays in scope for unseal's
// internals if a future caveat needs it. Keeps the import set
// stable across init.go + unseal.go.
var _ = bytes.Contains
