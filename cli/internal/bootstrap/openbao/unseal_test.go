package openbao

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"sync"
	"testing"

	"github.com/shalb/kube-dc/cli/internal/bootstrap/ports"
)

// fakeOpenBao records every Unseal call + reports Sealed-state per
// pod. Lets us drive the threshold = 3 unseal loop deterministically.
type fakeOpenBao struct {
	mu sync.Mutex

	pods        []string
	sealed      map[string]bool // pod → currently-sealed
	initialized map[string]bool // pod → currently-initialized (defaults to true if absent)
	unsealCnt   map[string]int  // pod → how many times Unseal called
	raftJoinCnt map[string]int  // pod → how many times RaftJoin called
	statusErr   error
	unsealErr   error
	raftJoinErr error
}

func (b *fakeOpenBao) PodList(_ context.Context) ([]string, error) {
	return b.pods, nil
}

func (b *fakeOpenBao) Status(_ context.Context, pod string) (ports.BaoStatus, error) {
	if b.statusErr != nil {
		return ports.BaoStatus{}, b.statusErr
	}
	b.mu.Lock()
	defer b.mu.Unlock()
	// Default Initialized=true to keep pre-existing tests unchanged;
	// the F-bootstrap-2 specs set false explicitly to drive the
	// raft-join branch.
	init := true
	if v, ok := b.initialized[pod]; ok {
		init = v
	}
	s := ports.BaoStatus{Pod: pod, Initialized: init, Sealed: b.sealed[pod]}
	return s, nil
}

func (b *fakeOpenBao) Unseal(_ context.Context, pod string, share []byte) error {
	if b.unsealErr != nil {
		return b.unsealErr
	}
	b.mu.Lock()
	defer b.mu.Unlock()
	b.unsealCnt[pod]++
	if b.unsealCnt[pod] >= 3 {
		b.sealed[pod] = false
	}
	// Defensive scrub-check: the caller is supposed to scrub
	// `share` after we return. We can't assert that directly, but
	// the engine's regex extraction limits each share to a small
	// non-empty slice — we just verify it's non-empty here.
	if len(share) == 0 {
		return fmt.Errorf("empty share to %s", pod)
	}
	return nil
}

func (b *fakeOpenBao) RaftJoin(_ context.Context, pod, _ string) error {
	if b.raftJoinErr != nil {
		return b.raftJoinErr
	}
	b.mu.Lock()
	defer b.mu.Unlock()
	if b.raftJoinCnt == nil {
		b.raftJoinCnt = map[string]int{}
	}
	b.raftJoinCnt[pod]++
	if b.initialized == nil {
		b.initialized = map[string]bool{}
	}
	b.initialized[pod] = true
	return nil
}

func (b *fakeOpenBao) GenerateRoot(_ context.Context, _ [][]byte) ([]byte, error) { return nil, nil }
func (b *fakeOpenBao) RevokeSelf(_ context.Context, _ []byte) error               { return nil }
func (b *fakeOpenBao) ApplyPolicy(_ context.Context, _ []byte, _, _ string) error    { return nil }
func (b *fakeOpenBao) EnableAuthPath(_ context.Context, _ []byte, _, _ string) error { return nil }
func (b *fakeOpenBao) ConfigureKubernetesAuth(_ context.Context, _ []byte, _ string, _ ports.KubernetesAuthConfig) error {
	return nil
}
func (b *fakeOpenBao) SetAnnotations(_ context.Context, _ string, _ map[string]string) error {
	return nil
}
func (b *fakeOpenBao) WriteAuthRole(_ context.Context, _ []byte, _, _ string, _ map[string]string) error {
	return nil
}
func (b *fakeOpenBao) GetAnnotation(_ context.Context, _, _ string) (string, error) { return "", nil }
func (b *fakeOpenBao) SetAnnotation(_ context.Context, _, _, _ string) error        { return nil }

// fakeSOPSDecryptOnly returns a canned decrypted blob.
type fakeSOPSDecryptOnly struct {
	body []byte
	err  error
}

func (s *fakeSOPSDecryptOnly) Decrypt(_ context.Context, _ string) ([]byte, error) {
	if s.err != nil {
		return nil, s.err
	}
	return append([]byte(nil), s.body...), nil
}
func (s *fakeSOPSDecryptOnly) Encrypt(_ context.Context, _ string) error { return nil }
func (s *fakeSOPSDecryptOnly) SetStringData(_ context.Context, _, _ string, _ []byte) error {
	return nil
}
func (s *fakeSOPSDecryptOnly) Recipients(_ string) ([]string, error) { return nil, nil }
func (s *fakeSOPSDecryptOnly) DerivePubKey(_ string) (string, error)  { return "", nil }

const canonicalDecryptedSecrets = `apiVersion: v1
kind: Secret
metadata:
    name: cluster-secrets
stringData:
    KEYCLOAK_ADMIN_PASSWORD: abc123
    OPENBAO_UNSEAL_KEY_1: share1AAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAA=
    OPENBAO_UNSEAL_KEY_2: share2BBBBBBBBBBBBBBBBBBBBBBBBBBBBBBBBBBBBBBBB=
    OPENBAO_UNSEAL_KEY_3: share3CCCCCCCCCCCCCCCCCCCCCCCCCCCCCCCCCCCCCCCC=
    OPENBAO_UNSEAL_KEY_4: share4DDDDDDDDDDDDDDDDDDDDDDDDDDDDDDDDDDDDDDDD=
    OPENBAO_UNSEAL_KEY_5: share5EEEEEEEEEEEEEEEEEEEEEEEEEEEEEEEEEEEEEEEE=
`

// --- happy path ---

func TestUnseal_HappyPath_UnsealsSealedPodsOnly(t *testing.T) {
	repo := setupFleet(t)
	// Override the seeded empty secrets.enc.yaml so the path
	// exists for the engine (real engine reads via SOPS, our
	// fake returns the canned body regardless of path content).
	_ = os.WriteFile(filepath.Join(repo, "clusters", "atlantis", "secrets.enc.yaml"),
		[]byte("stub-encrypted"), 0o644)

	sops := &fakeSOPSDecryptOnly{body: []byte(canonicalDecryptedSecrets)}
	bao := &fakeOpenBao{
		pods:      []string{"openbao-0", "openbao-1", "openbao-2"},
		sealed:    map[string]bool{"openbao-0": true, "openbao-1": false, "openbao-2": true},
		unsealCnt: map[string]int{},
	}

	var out bytes.Buffer
	err := Unseal(context.Background(), UnsealOptions{
		ClusterName: "atlantis",
		FleetRepo:   repo,
		SOPS:        sops,
		OpenBao:     bao,
		Out:         &out,
	})
	if err != nil {
		t.Fatalf("Unseal: %v\nout:\n%s", err, out.String())
	}

	// openbao-0 + openbao-2 each got 3 unseal calls; openbao-1
	// (already unsealed) got 0.
	if bao.unsealCnt["openbao-0"] != 3 {
		t.Errorf("openbao-0 unseal count = %d, want 3", bao.unsealCnt["openbao-0"])
	}
	if bao.unsealCnt["openbao-1"] != 0 {
		t.Errorf("openbao-1 (already unsealed) got %d unseal calls", bao.unsealCnt["openbao-1"])
	}
	if bao.unsealCnt["openbao-2"] != 3 {
		t.Errorf("openbao-2 unseal count = %d, want 3", bao.unsealCnt["openbao-2"])
	}
	// All pods now reporting unsealed.
	for _, p := range bao.pods {
		if bao.sealed[p] {
			t.Errorf("%s still sealed after Unseal", p)
		}
	}
	// Operator-facing log includes the skipped-pod marker.
	for _, want := range []string{
		"already unsealed (skipping)",
		"unsealing openbao-0",
		"unsealing openbao-2",
		"2 unsealed, 1 already-unsealed (total 3 pods)",
	} {
		if !bytes.Contains(out.Bytes(), []byte(want)) {
			t.Errorf("output missing %q\nFULL:\n%s", want, out.String())
		}
	}
}

// --- F-bootstrap-2: uninitialized followers get raft-joined first ---

// When a follower pod reports Initialized=false (the OrderedReady race
// after `bootstrap openbao init` brings up openbao-0 first and the
// StatefulSet only later creates openbao-1/2 with empty storage), the
// unseal engine must call RaftJoin on it before trying the shamir
// unseal — otherwise `bao operator unseal` exits 1 with "security
// barrier not initialized" because the follower doesn't yet know about
// the leader's shamir config.
//
// Verifies:
//   - RaftJoin runs exactly once per uninitialized follower
//   - RaftJoin does NOT run against the leader (openbao-0)
//   - RaftJoin does NOT run against followers already Initialized=true
//   - After join, the unseal proceeds normally (3 shares per pod)
func TestUnseal_AutoJoinsUninitializedFollowers(t *testing.T) {
	repo := setupFleet(t)
	_ = os.WriteFile(filepath.Join(repo, "clusters", "atlantis", "secrets.enc.yaml"),
		[]byte("stub"), 0o644)

	sops := &fakeSOPSDecryptOnly{body: []byte(canonicalDecryptedSecrets)}
	bao := &fakeOpenBao{
		pods:   []string{"openbao-0", "openbao-1", "openbao-2"},
		sealed: map[string]bool{"openbao-0": true, "openbao-1": true, "openbao-2": true},
		// openbao-0 is the leader (already initialized by init).
		// openbao-1 came up fresh (the OrderedReady race) — needs join.
		// openbao-2 has been previously joined (re-run of unseal) — no join needed.
		initialized: map[string]bool{
			"openbao-0": true,
			"openbao-1": false,
			"openbao-2": true,
		},
		unsealCnt:   map[string]int{},
		raftJoinCnt: map[string]int{},
	}

	var out bytes.Buffer
	err := Unseal(context.Background(), UnsealOptions{
		ClusterName: "atlantis",
		FleetRepo:   repo,
		SOPS:        sops,
		OpenBao:     bao,
		Out:         &out,
	})
	if err != nil {
		t.Fatalf("Unseal: %v\nout:\n%s", err, out.String())
	}

	// Exactly one RaftJoin call, against openbao-1.
	if got := bao.raftJoinCnt["openbao-1"]; got != 1 {
		t.Errorf("openbao-1 raft-join count = %d, want 1", got)
	}
	if got := bao.raftJoinCnt["openbao-0"]; got != 0 {
		t.Errorf("openbao-0 (leader) MUST NOT raft-join itself; got %d calls", got)
	}
	if got := bao.raftJoinCnt["openbao-2"]; got != 0 {
		t.Errorf("openbao-2 (already initialized) MUST NOT be re-joined; got %d calls", got)
	}

	// All three still got 3 unseal calls each.
	for _, p := range bao.pods {
		if bao.unsealCnt[p] != 3 {
			t.Errorf("%s unseal count = %d, want 3", p, bao.unsealCnt[p])
		}
	}

	// Operator log surfaces the join for openbao-1 and includes the
	// correct leader URL.
	for _, want := range []string{
		"openbao-1 uninitialized — raft-joining",
		"http://openbao-0.openbao-internal:8200",
		"3 unsealed, 0 already-unsealed (total 3 pods)",
	} {
		if !bytes.Contains(out.Bytes(), []byte(want)) {
			t.Errorf("output missing %q\nFULL:\n%s", want, out.String())
		}
	}
}

// RaftJoin errors must propagate (don't silently fall through to a
// doomed unseal that would log "security barrier not initialized").
func TestUnseal_RaftJoinErrorPropagates(t *testing.T) {
	repo := setupFleet(t)
	_ = os.WriteFile(filepath.Join(repo, "clusters", "atlantis", "secrets.enc.yaml"),
		[]byte("stub"), 0o644)

	sops := &fakeSOPSDecryptOnly{body: []byte(canonicalDecryptedSecrets)}
	bao := &fakeOpenBao{
		pods:        []string{"openbao-0", "openbao-1"},
		sealed:      map[string]bool{"openbao-0": true, "openbao-1": true},
		initialized: map[string]bool{"openbao-0": true, "openbao-1": false},
		unsealCnt:   map[string]int{},
		raftJoinCnt: map[string]int{},
		raftJoinErr: fmt.Errorf("simulated join transport failure"),
	}

	err := Unseal(context.Background(), UnsealOptions{
		ClusterName: "atlantis", FleetRepo: repo,
		SOPS: sops, OpenBao: bao,
	})
	if err == nil || !bytes.Contains([]byte(err.Error()), []byte("raft join openbao-1")) {
		t.Fatalf("expected raft-join error to propagate, got %v", err)
	}
	if bao.unsealCnt["openbao-1"] != 0 {
		t.Errorf("openbao-1 should NOT be unsealed after join failure; got %d unseal calls",
			bao.unsealCnt["openbao-1"])
	}
}

// --- share secrecy: log must not echo share values ---

func TestUnseal_ShareValuesNeverInOutput(t *testing.T) {
	repo := setupFleet(t)
	canary := `stringData:
    OPENBAO_UNSEAL_KEY_1: CANARY-SHARE-1-MUST-NOT-LEAK
    OPENBAO_UNSEAL_KEY_2: CANARY-SHARE-2-MUST-NOT-LEAK
    OPENBAO_UNSEAL_KEY_3: CANARY-SHARE-3-MUST-NOT-LEAK
    OPENBAO_UNSEAL_KEY_4: CANARY-SHARE-4-MUST-NOT-LEAK
    OPENBAO_UNSEAL_KEY_5: CANARY-SHARE-5-MUST-NOT-LEAK
`
	sops := &fakeSOPSDecryptOnly{body: []byte(canary)}
	bao := &fakeOpenBao{
		pods:      []string{"openbao-0"},
		sealed:    map[string]bool{"openbao-0": true},
		unsealCnt: map[string]int{},
	}

	var out bytes.Buffer
	if err := Unseal(context.Background(), UnsealOptions{
		ClusterName: "atlantis",
		FleetRepo:   repo,
		SOPS:        sops,
		OpenBao:     bao,
		Out:         &out,
	}); err != nil {
		t.Fatalf("Unseal: %v", err)
	}
	if bytes.Contains(out.Bytes(), []byte("CANARY-SHARE")) {
		t.Fatalf("share value leaked to operator output:\n%s", out.String())
	}
}

// --- missing threshold shares ---

func TestUnseal_RefusesMissingThresholdShares(t *testing.T) {
	repo := setupFleet(t)
	// Decrypted body has only shares 1 + 2 (no 3) — unseal
	// threshold is 3, so this must refuse.
	body := `stringData:
    OPENBAO_UNSEAL_KEY_1: x
    OPENBAO_UNSEAL_KEY_2: y
`
	sops := &fakeSOPSDecryptOnly{body: []byte(body)}
	bao := &fakeOpenBao{pods: []string{"openbao-0"}, sealed: map[string]bool{"openbao-0": true}, unsealCnt: map[string]int{}}
	err := Unseal(context.Background(), UnsealOptions{
		ClusterName: "atlantis", FleetRepo: repo,
		SOPS: sops, OpenBao: bao,
	})
	if !errors.Is(err, ErrUnsealMissingShares) {
		t.Fatalf("expected ErrUnsealMissingShares, got %v", err)
	}
	if !bytes.Contains([]byte(err.Error()), []byte("3")) {
		t.Errorf("error should name the missing key index 3: %v", err)
	}
	// No pod was touched.
	if bao.unsealCnt["openbao-0"] != 0 {
		t.Errorf("pod touched despite missing shares")
	}
}

// --- empty pod list ---

func TestUnseal_RefusesEmptyPodList(t *testing.T) {
	repo := setupFleet(t)
	_ = os.WriteFile(filepath.Join(repo, "clusters", "atlantis", "secrets.enc.yaml"),
		[]byte("stub"), 0o644)
	sops := &fakeSOPSDecryptOnly{body: []byte(canonicalDecryptedSecrets)}
	bao := &fakeOpenBao{pods: []string{}, sealed: map[string]bool{}, unsealCnt: map[string]int{}}
	err := Unseal(context.Background(), UnsealOptions{
		ClusterName: "atlantis", FleetRepo: repo,
		SOPS: sops, OpenBao: bao,
	})
	if err == nil || !bytes.Contains([]byte(err.Error()), []byte("no OpenBao pods found")) {
		t.Fatalf("expected 'no OpenBao pods found' error, got %v", err)
	}
}

// --- already-unsealed idempotence ---

func TestUnseal_AllAlreadyUnsealed_NoOp(t *testing.T) {
	repo := setupFleet(t)
	sops := &fakeSOPSDecryptOnly{body: []byte(canonicalDecryptedSecrets)}
	bao := &fakeOpenBao{
		pods:      []string{"openbao-0", "openbao-1"},
		sealed:    map[string]bool{"openbao-0": false, "openbao-1": false},
		unsealCnt: map[string]int{},
	}
	var out bytes.Buffer
	err := Unseal(context.Background(), UnsealOptions{
		ClusterName: "atlantis", FleetRepo: repo,
		SOPS: sops, OpenBao: bao, Out: &out,
	})
	if err != nil {
		t.Fatalf("Unseal: %v", err)
	}
	for _, p := range bao.pods {
		if bao.unsealCnt[p] != 0 {
			t.Errorf("%s touched despite Sealed=false", p)
		}
	}
	if !bytes.Contains(out.Bytes(), []byte("0 unsealed, 2 already-unsealed")) {
		t.Errorf("summary should report 0 unsealed:\n%s", out.String())
	}
}

// --- bao.Unseal error propagates ---

func TestUnseal_UnsealErrorPropagates(t *testing.T) {
	repo := setupFleet(t)
	sops := &fakeSOPSDecryptOnly{body: []byte(canonicalDecryptedSecrets)}
	bao := &fakeOpenBao{
		pods:      []string{"openbao-0"},
		sealed:    map[string]bool{"openbao-0": true},
		unsealCnt: map[string]int{},
		unsealErr: errors.New("bao unseal: SPDY connection lost"),
	}
	err := Unseal(context.Background(), UnsealOptions{
		ClusterName: "atlantis", FleetRepo: repo,
		SOPS: sops, OpenBao: bao,
	})
	if err == nil {
		t.Fatal("expected bao unseal error to surface")
	}
	if !bytes.Contains([]byte(err.Error()), []byte("openbao-0")) {
		t.Errorf("error should name the failed pod: %v", err)
	}
}

// --- SOPS decrypt error ---

func TestUnseal_SOPSDecryptError(t *testing.T) {
	repo := setupFleet(t)
	sops := &fakeSOPSDecryptOnly{err: errors.New("sops: no recipient matched")}
	bao := &fakeOpenBao{pods: []string{"openbao-0"}, sealed: map[string]bool{"openbao-0": true}, unsealCnt: map[string]int{}}
	err := Unseal(context.Background(), UnsealOptions{
		ClusterName: "atlantis", FleetRepo: repo,
		SOPS: sops, OpenBao: bao,
	})
	if err == nil || !bytes.Contains([]byte(err.Error()), []byte("decrypt")) {
		t.Fatalf("expected SOPS decrypt error, got %v", err)
	}
}

// --- missing dependencies ---

func TestUnseal_MissingDependencies(t *testing.T) {
	base := UnsealOptions{
		ClusterName: "atlantis",
		FleetRepo:   "/tmp",
		SOPS:        &fakeSOPSDecryptOnly{body: []byte(canonicalDecryptedSecrets)},
		OpenBao:     &fakeOpenBao{pods: []string{"openbao-0"}, sealed: map[string]bool{"openbao-0": true}, unsealCnt: map[string]int{}},
	}
	cases := []struct {
		name   string
		mutate func(*UnsealOptions)
		wantIn string
	}{
		{"empty ClusterName", func(o *UnsealOptions) { o.ClusterName = "" }, "ClusterName"},
		{"empty FleetRepo", func(o *UnsealOptions) { o.FleetRepo = "" }, "FleetRepo"},
		{"nil SOPS", func(o *UnsealOptions) { o.SOPS = nil }, "SOPS"},
		{"nil OpenBao", func(o *UnsealOptions) { o.OpenBao = nil }, "OpenBao"},
	}
	for _, tc := range cases {
		tc := tc
		t.Run(tc.name, func(t *testing.T) {
			o := base
			tc.mutate(&o)
			err := Unseal(context.Background(), o)
			if !errors.Is(err, ErrUnsealMissingDependency) {
				t.Fatalf("expected ErrUnsealMissingDependency, got %v", err)
			}
			if !bytes.Contains([]byte(err.Error()), []byte(tc.wantIn)) {
				t.Errorf("error should name %q: %v", tc.wantIn, err)
			}
		})
	}
}

// --- regex extraction unit ---

func TestExtractThresholdShares(t *testing.T) {
	body := []byte(canonicalDecryptedSecrets)
	shares, err := extractThresholdShares(body)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if len(shares) != 3 {
		t.Fatalf("share count = %d, want 3", len(shares))
	}
	wantPrefix := []string{"share1AAAA", "share2BBBB", "share3CCCC"}
	for i, want := range wantPrefix {
		if !bytes.HasPrefix(shares[i], []byte(want)) {
			t.Errorf("share[%d] = %q, want prefix %q", i, shares[i], want)
		}
	}
}

func TestExtractThresholdShares_MissingMiddle(t *testing.T) {
	body := []byte(`stringData:
    OPENBAO_UNSEAL_KEY_1: a
    OPENBAO_UNSEAL_KEY_3: c
`)
	_, err := extractThresholdShares(body)
	if !errors.Is(err, ErrUnsealMissingShares) {
		t.Fatalf("expected ErrUnsealMissingShares, got %v", err)
	}
}
