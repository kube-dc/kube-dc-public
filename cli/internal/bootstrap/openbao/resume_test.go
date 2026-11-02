package openbao

import (
	"context"
	"os"
	"path/filepath"
	"testing"

	"github.com/shalb/kube-dc/cli/internal/bootstrap/ports"
)

// fakeResumeBao unseals like fakeOpenBao and records the controller-auth and
// annotation calls like fakeSetupBao.
type fakeResumeBao struct {
	*fakeSetupBao
	members  *fakeOpenBao
	roots    int
	revoked  int
	rootSeen []byte
}

func (f *fakeResumeBao) Status(ctx context.Context, pod string) (ports.BaoStatus, error) {
	return f.members.Status(ctx, pod)
}
func (f *fakeResumeBao) PodList(ctx context.Context) ([]string, error) { return f.members.PodList(ctx) }
func (f *fakeResumeBao) Unseal(ctx context.Context, pod string, share []byte) error {
	return f.members.Unseal(ctx, pod, share)
}
func (f *fakeResumeBao) RaftJoin(ctx context.Context, pod, leader string) error {
	return f.members.RaftJoin(ctx, pod, leader)
}
func (f *fakeResumeBao) GenerateRoot(_ context.Context, shares [][]byte) ([]byte, error) {
	f.roots++
	return []byte("single-use-root"), nil
}
func (f *fakeResumeBao) RevokeSelf(_ context.Context, token []byte) error {
	f.revoked++
	f.rootSeen = append([]byte(nil), token...)
	return nil
}

func resumeFixture(t *testing.T, authInstalled bool) (*fakeResumeBao, ResumeOptions) {
	t.Helper()
	repo := setupFleet(t)
	_ = os.WriteFile(filepath.Join(repo, "clusters", "atlantis", "secrets.enc.yaml"), []byte("stub"), 0o644)
	bao := &fakeResumeBao{
		fakeSetupBao: &fakeSetupBao{},
		members: &fakeOpenBao{
			pods:        []string{"openbao-0", "openbao-1", "openbao-2"},
			sealed:      map[string]bool{"openbao-1": true, "openbao-2": true},
			initialized: map[string]bool{"openbao-1": false, "openbao-2": false},
			unsealCnt:   map[string]int{},
			raftJoinCnt: map[string]int{},
		},
	}
	return bao, ResumeOptions{
		Unseal: UnsealOptions{
			ClusterName: "atlantis", FleetRepo: repo,
			SOPS:    &fakeSOPSDecryptOnly{body: []byte(canonicalDecryptedSecrets)},
			OpenBao: bao,
			K8s:     &fakeSetupK8s{caCert: "-----BEGIN CERTIFICATE-----\nFAKE\n-----END CERTIFICATE-----\n"},
		},
		ControllerAuthInstalled: authInstalled,
	}
}

// An init that committed its shares but stopped before Phase C finished is
// completed from Git: followers join and unseal, controller auth is installed
// with a revoked single-use root token, and the finalized marker is stamped.
func TestResumeCommittedFinishesPhaseC(t *testing.T) {
	bao, opts := resumeFixture(t, false)
	if err := ResumeCommitted(context.Background(), opts); err != nil {
		t.Fatal(err)
	}
	for _, pod := range []string{"openbao-1", "openbao-2"} {
		if bao.members.sealed[pod] || bao.members.raftJoinCnt[pod] != 1 {
			t.Fatalf("%s sealed=%v joins=%d", pod, bao.members.sealed[pod], bao.members.raftJoinCnt[pod])
		}
	}
	if bao.roots != 1 || bao.revoked != 1 || string(bao.rootSeen) != "single-use-root" {
		t.Fatalf("root ceremonies=%d revokes=%d", bao.roots, bao.revoked)
	}
	if len(bao.enableCalls) == 0 {
		t.Fatal("controller auth was not installed")
	}
	last := bao.annotateCalls[len(bao.annotateCalls)-1]
	if last[AnnotationBootstrapFinalized] == "" {
		t.Fatalf("finalized marker not stamped: %v", bao.annotateCalls)
	}
}

// Controller auth already installed (for example by the standalone command):
// it is not redone, but the shares still prove custody with a revoked
// single-use root token before the finalized marker is stamped.
func TestResumeCommittedKeepsInstalledControllerAuth(t *testing.T) {
	bao, opts := resumeFixture(t, true)
	if err := ResumeCommitted(context.Background(), opts); err != nil {
		t.Fatal(err)
	}
	if bao.roots != 1 || bao.revoked != 1 || len(bao.enableCalls) != 0 {
		t.Fatalf("root ceremonies=%d revokes=%d enable calls=%d", bao.roots, bao.revoked, len(bao.enableCalls))
	}
	if len(bao.annotateCalls) != 1 || bao.annotateCalls[0][AnnotationBootstrapFinalized] == "" {
		t.Fatalf("annotations %v", bao.annotateCalls)
	}
}
