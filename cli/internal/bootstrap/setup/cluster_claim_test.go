package setup

import (
	"context"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/shalb/kube-dc/cli/internal/bootstrap/rke2"
	coordinationv1 "k8s.io/api/coordination/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/apimachinery/pkg/types"
	"k8s.io/client-go/kubernetes"
	"k8s.io/client-go/kubernetes/fake"
	ktesting "k8s.io/client-go/testing"
)

func clusterClaimClient(t *testing.T) *fake.Clientset {
	t.Helper()
	client := fake.NewClientset()
	client.PrependReactor("create", "leases", func(a ktesting.Action) (bool, runtime.Object, error) {
		lease := a.(ktesting.CreateAction).GetObject().(*coordinationv1.Lease)
		lease.UID, lease.ResourceVersion = "lease-instance-1", "opaque-api-version"
		return false, nil, nil
	})
	return client
}

func clusterReservationFixture(t *testing.T, prepare ...func(*SessionCoordinator) []byte) (*SessionCoordinator, *fixtureClaims, *fake.Clientset) {
	t.Helper()
	c, hosts := coordinatorFixture(t)
	if _, err := c.Acquire(context.Background(), false); err != nil {
		t.Fatal(err)
	}
	for id, claim := range hosts.remote {
		claim.State = "started"
		hosts.remote[id] = claim
	}
	path := filepath.Join(c.writer.directory, "child-bootstrap.kubeconfig")
	body := []byte("protected child config")
	if len(prepare) != 0 {
		body = prepare[0](c)
	}
	if err := os.WriteFile(path, body, 0600); err != nil {
		t.Fatal(err)
	}
	primary := primaryServer(c.compiled.Spec.Hosts)
	attempt, err := c.writer.Begin("fetch-kubeconfig")
	if err != nil {
		t.Fatal(err)
	}
	node := rke2.VerifiedNode{Name: primary.ID, UID: "verified-primary-uid", Role: "server", InternalIP: primary.ManagementAddress}
	hash, err := c.writer.saveStageEvidence("fetch-kubeconfig", attempt, primary, node, path)
	if err != nil {
		t.Fatal(err)
	}
	if err := c.writer.completeVerified("fetch-kubeconfig", attempt, hash); err != nil {
		t.Fatal(err)
	}
	client := clusterClaimClient(t)
	c.clusterClient = func(_ context.Context, compiled Compiled, gotPath string, gotNode rke2.VerifiedNode) (kubernetes.Interface, error) {
		if gotPath != path || gotNode != node || compiled.InputHash != c.compiled.InputHash {
			t.Fatal("child client did not receive the durable handoff")
		}
		return client, nil
	}
	return c, hosts, client
}

func TestCoordinatorReservesChildAfterDurableHandoffAndRetainsHosts(t *testing.T) {
	c, hosts, client := clusterReservationFixture(t)
	client.PrependReactor("create", "leases", func(a ktesting.Action) (bool, runtime.Object, error) {
		record, err := ReadSession(c.writer.directory)
		if err != nil {
			t.Fatal(err)
		}
		event, ok := lastSessionEvent(record, clusterClaimStage)
		if !ok || event.State != "running" {
			t.Fatal("API mutation preceded durable intent")
		}
		if _, err := verifiedSessionHandoff(c.writer.root, record); err != nil {
			t.Fatal("API mutation preceded verified handoff")
		}
		return false, nil, nil
	})
	before := hosts.writes
	if err := c.reserveChildCluster(context.Background()); err != nil {
		t.Fatal(err)
	}
	record, err := ReadSession(c.writer.directory)
	if err != nil {
		t.Fatal(err)
	}
	event, _ := lastSessionEvent(record, clusterClaimStage)
	proof, err := loadStoredStageProof(c.writer.root, record, event)
	if err != nil || proof.Cluster == nil || proof.Cluster.UID != "lease-instance-1" || proof.Cluster.RunID != c.runID {
		t.Fatalf("cluster proof is not bound to actual API instance: %+v %v", proof, err)
	}
	if hosts.writes != before {
		t.Fatal("cluster reservation released or rewrote host claims")
	}
	for _, claim := range hosts.remote {
		if claim.State != "started" {
			t.Fatal("exclusion gap in host-to-cluster transition")
		}
	}
	status, err := c.Status(context.Background())
	if err != nil || status.ReadyToApply || status.Cluster.State != "held" || !status.Cluster.CurrentRun || !status.Cluster.MatchesProof {
		t.Fatalf("unexpected inspection: %+v %v", status.Cluster, err)
	}
	for _, a := range client.Actions() {
		if a.GetVerb() != "create" && a.GetVerb() != "get" {
			t.Fatalf("unreviewed cluster action: %s", a.GetVerb())
		}
	}
	if err := c.reserveChildCluster(context.Background()); err == nil {
		t.Fatal("completed reservation was retried")
	}
}

func TestClusterReservationAmbiguousCreateDoesNotRetry(t *testing.T) {
	for _, kind := range []string{"response-lost", "cancel-after-create", "proof-sync", "readback-failed", "replaced-before-readback"} {
		t.Run(kind, func(t *testing.T) {
			c, hosts, client := clusterReservationFixture(t)
			ctx, cancel := context.WithCancel(context.Background())
			defer cancel()
			client.PrependReactor("create", "leases", func(a ktesting.Action) (bool, runtime.Object, error) {
				lease := a.(ktesting.CreateAction).GetObject().(*coordinationv1.Lease).DeepCopy()
				lease.UID, lease.ResourceVersion = "lease-instance-1", "1"
				if err := client.Tracker().Create(coordinationv1.SchemeGroupVersion.WithResource("leases"), lease, clusterClaimNamespace); err != nil {
					t.Fatal(err)
				}
				switch kind {
				case "response-lost":
					return true, nil, errors.New("transport lost; untrusted response body")
				case "cancel-after-create":
					cancel()
				case "proof-sync":
					c.writer.syncDirectory = func(*os.Root) error { return errors.New("full disk") }
				case "replaced-before-readback":
					replacement := lease.DeepCopy()
					replacement.UID = "another-instance"
					if err := client.Tracker().Update(coordinationv1.SchemeGroupVersion.WithResource("leases"), replacement, clusterClaimNamespace); err != nil {
						t.Fatal(err)
					}
				}
				return true, lease, nil
			})
			if kind == "readback-failed" {
				client.PrependReactor("get", "leases", func(ktesting.Action) (bool, runtime.Object, error) { return true, nil, errors.New("transport lost") })
			}
			if err := c.reserveChildCluster(ctx); err == nil || strings.Contains(err.Error(), "untrusted response body") {
				t.Fatalf("ambiguous response accepted or body leaked: %v", err)
			}
			if err := c.writer.Close(); err != nil {
				t.Fatal(err)
			}
			reopened, err := OpenSession(c.writer.directory)
			if err != nil {
				t.Fatal(err)
			}
			defer reopened.Close()
			record, _ := reopened.Snapshot()
			event, _ := lastSessionEvent(record, clusterClaimStage)
			if event.State != "uncertain" || record.Ownership.Phase != "uncertain" || len(hosts.remote) == 0 {
				t.Fatalf("ambiguity lost on reopen: %+v %+v", event, record.Ownership)
			}
			if err := reopened.Match(c.compiled, c.review); err != nil {
				t.Fatal(err)
			}
			if _, err := reopened.Begin(clusterClaimStage); err == nil {
				t.Fatal("uncertain Create allowed automatic retry")
			}
			if _, err := client.Tracker().Get(coordinationv1.SchemeGroupVersion.WithResource("leases"), clusterClaimNamespace, clusterClaimName); err != nil {
				t.Fatal("remote claim was erased after ambiguity")
			}
		})
	}
}

func TestClusterReservationBlocksBeforeWritesForLostHostOrHandoff(t *testing.T) {
	for _, kind := range []string{"host-owner", "host-state", "host-expiry", "handoff-bytes", "missing-handoff", "canceled", "journal-sync", "wrong-api"} {
		t.Run(kind, func(t *testing.T) {
			c, hosts, client := clusterReservationFixture(t)
			ctx, cancel := context.WithCancel(context.Background())
			defer cancel()
			primary := primaryServer(c.compiled.Spec.Hosts)
			claim := hosts.remote[primary.ID]
			switch kind {
			case "host-owner":
				claim.RunID = strings.Repeat("b", 64)
			case "host-state":
				claim.State = "reserved"
			case "host-expiry":
				claim.ExpiresTick = 0
			case "handoff-bytes":
				if err := os.WriteFile(filepath.Join(c.writer.directory, "child-bootstrap.kubeconfig"), []byte("replacement"), 0600); err != nil {
					t.Fatal(err)
				}
			case "missing-handoff":
				if err := os.Remove(filepath.Join(c.writer.directory, "child-bootstrap.kubeconfig")); err != nil {
					t.Fatal(err)
				}
			case "canceled":
				cancel()
			case "journal-sync":
				c.writer.syncDirectory = func(*os.Root) error { return errors.New("full disk") }
			case "wrong-api":
				c.clusterClient = func(context.Context, Compiled, string, rke2.VerifiedNode) (kubernetes.Interface, error) {
					return nil, errors.New("wrong UID or TLS target")
				}
			}
			hosts.remote[primary.ID] = claim
			if err := c.reserveChildCluster(ctx); err == nil {
				t.Fatal("unsafe reservation accepted")
			}
			if len(client.Actions()) != 0 {
				t.Fatalf("API mutation after failed guard: %+v", client.Actions())
			}
		})
	}
}

func TestClusterReservationInspectsProofAndNeverRestoresRunAuthority(t *testing.T) {
	c, _, client := clusterReservationFixture(t)
	if err := c.reserveChildCluster(context.Background()); err != nil {
		t.Fatal(err)
	}
	record, _ := c.writer.Snapshot()
	for _, kind := range []string{"same", "renewed", "recreated", "foreign", "malformed", "expired", "absent", "unavailable"} {
		t.Run(kind, func(t *testing.T) {
			lease, err := client.CoordinationV1().Leases(clusterClaimNamespace).Get(context.Background(), clusterClaimName, metav1.GetOptions{})
			if err != nil {
				t.Fatal(err)
			}
			original := lease.DeepCopy()
			defer func() {
				_ = client.Tracker().Update(coordinationv1.SchemeGroupVersion.WithResource("leases"), original, clusterClaimNamespace)
			}()
			switch kind {
			case "renewed":
				lease.ResourceVersion = "different-opaque-version"
			case "recreated":
				lease.UID = "new-instance-with-same-owner"
			case "foreign":
				lease.Annotations[clusterClaimPrefix+"session"] = strings.Repeat("e", 64)
			case "malformed":
				lease.Spec.HolderIdentity = nil
			case "expired":
				one := int32(1)
				old := metav1.NewMicroTime(time.Now().Add(-time.Hour))
				lease.Spec.LeaseDurationSeconds, lease.Spec.RenewTime = &one, &old
			}
			if err := client.Tracker().Update(coordinationv1.SchemeGroupVersion.WithResource("leases"), lease, clusterClaimNamespace); err != nil {
				t.Fatal(err)
			}
			if kind == "absent" {
				if err := client.Tracker().Delete(coordinationv1.SchemeGroupVersion.WithResource("leases"), clusterClaimNamespace, clusterClaimName); err != nil {
					t.Fatal(err)
				}
				defer func() {
					_ = client.Tracker().Create(coordinationv1.SchemeGroupVersion.WithResource("leases"), original, clusterClaimNamespace)
				}()
			}
			factory := c.clusterClient
			if kind == "unavailable" {
				factory = func(context.Context, Compiled, string, rke2.VerifiedNode) (kubernetes.Interface, error) {
					return nil, errors.New("offline")
				}
			}
			status := inspectSessionCluster(context.Background(), c.writer.directory, record, c.compiled, "", factory)
			want := "held"
			if kind == "recreated" || kind == "foreign" || kind == "malformed" {
				want = "conflict"
			}
			if kind == "absent" {
				want = "absent"
			}
			if kind == "unavailable" {
				want = "unknown"
			}
			if status.State != want || status.CurrentRun {
				t.Fatalf("%+v", status)
			}
		})
	}
	if err := c.writer.Close(); err != nil {
		t.Fatal(err)
	}
	writer, err := OpenSession(c.writer.directory)
	if err != nil {
		t.Fatal(err)
	}
	defer writer.Close()
	if err := writer.Match(c.compiled, c.review); err != nil {
		t.Fatal(err)
	}
	c.writer, c.runID = writer, strings.Repeat("f", 64)
	status, err := c.Status(context.Background())
	if err != nil || status.Cluster.CurrentRun || !status.Cluster.MatchesProof || status.Cluster.State != "held" {
		t.Fatalf("%+v %v", status.Cluster, err)
	}
	before := len(client.Actions())
	if err := c.reserveChildCluster(context.Background()); err == nil || len(client.Actions()) != before {
		t.Fatal("reopen restored previous-run write authority")
	}
}

func TestClusterCreateExcludesAnotherWorkstationEvenWhenExistingLeaseExpired(t *testing.T) {
	c, _, client := clusterReservationFixture(t)
	record, _ := c.writer.Snapshot()
	base := expectedClusterClaim(record, c.runID, firstServerEvidence("uid"))
	var wg sync.WaitGroup
	results := make(chan error, 2)
	for _, run := range []string{strings.Repeat("a", 64), strings.Repeat("b", 64)} {
		wg.Add(1)
		go func(run string) {
			defer wg.Done()
			expected := base
			expected.RunID = run
			_, err := acquireClusterClaim(context.Background(), client.CoordinationV1().Leases(clusterClaimNamespace), expected)
			results <- err
		}(run)
	}
	wg.Wait()
	close(results)
	passed := 0
	for err := range results {
		if err == nil {
			passed++
		}
	}
	if passed != 1 {
		t.Fatalf("%d workstations acquired one reservation", passed)
	}
	lease, err := client.CoordinationV1().Leases(clusterClaimNamespace).Get(context.Background(), clusterClaimName, metav1.GetOptions{})
	if err != nil {
		t.Fatal(err)
	}
	one, old := int32(1), metav1.NewMicroTime(time.Now().Add(-time.Hour))
	lease.Spec.LeaseDurationSeconds, lease.Spec.RenewTime = &one, &old
	if _, err := client.CoordinationV1().Leases(clusterClaimNamespace).Update(context.Background(), lease, metav1.UpdateOptions{}); err != nil {
		t.Fatal(err)
	}
	before := len(client.Actions())
	if _, err := acquireClusterClaim(context.Background(), client.CoordinationV1().Leases(clusterClaimNamespace), base); err == nil {
		t.Fatal("expired Lease was taken over")
	}
	actions := client.Actions()[before:]
	if len(actions) != 1 || actions[0].GetVerb() != "create" {
		t.Fatalf("conflicting Lease modified: %+v", actions)
	}
}

func TestClusterClaimRejectsUnknownIdentityWithoutRawMetadata(t *testing.T) {
	bad := &coordinationv1.Lease{ObjectMeta: metav1.ObjectMeta{Name: clusterClaimName, Namespace: clusterClaimNamespace, UID: types.UID("secret\nvalue")}}
	if _, err := parseClusterClaim(bad); err == nil || strings.Contains(fmt.Sprint(err), "secret") {
		t.Fatal("malformed metadata accepted or leaked")
	}
}

func TestClusterReservationChecksSecondaryHostOwnership(t *testing.T) {
	c, hosts, _ := clusterReservationFixture(t)
	record, _ := c.writer.Snapshot()
	primary := primaryServer(c.compiled.Spec.Hosts)
	secondary := primary
	secondary.ID = "server-2"
	second := hosts.remote[primary.ID]
	second.HostID, second.MachineID = secondary.ID, strings.Repeat("b", 32)
	hosts.remote[secondary.ID] = second
	record.Targets = append(record.Targets, SessionTarget{ID: secondary.ID, MachineID: second.MachineID, HostKeySHA256: secondary.HostKeySHA256})
	selected := []Host{primary, secondary}
	if err := checkStartedHostClaims(context.Background(), selected, record, c.runID, hosts); err != nil {
		t.Fatal(err)
	}
	for _, kind := range []string{"owner", "expiry", "machine", "boot"} {
		t.Run(kind, func(t *testing.T) {
			changed := second
			switch kind {
			case "owner":
				changed.RunID = strings.Repeat("c", 64)
			case "expiry":
				changed.ExpiresTick = 0
			case "machine":
				changed.MachineID = strings.Repeat("d", 32)
			case "boot":
				changed.ObservedBootID = "22222222-2222-2222-2222-222222222222"
			}
			hosts.remote[secondary.ID] = changed
			if err := checkStartedHostClaims(context.Background(), selected, record, c.runID, hosts); err == nil || !strings.Contains(err.Error(), secondary.ID) {
				t.Fatal("primary ownership hid lost secondary")
			}
		})
	}
}
