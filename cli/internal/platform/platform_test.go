package platform

import (
	"context"
	"errors"
	"fmt"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"
)

func cache(t *testing.T) (*Cache, *time.Time) {
	now := time.Date(2026, 10, 24, 12, 0, 0, 0, time.UTC)
	return &Cache{Dir: t.TempDir(), Now: func() time.Time { return now }}, &now
}

func TestContractCheck(t *testing.T) {
	c := &Contract{}
	c.Platform.Version = "v0.9.12"
	c.CLI.MinimumVersion = "v0.9.10"
	c.CLI.RecommendedVersion = "0.9.12"
	for _, x := range []struct {
		cli    string
		tooOld bool
		notice bool
	}{
		{"0.9.9", true, false}, {"v0.9.10", false, true}, {"0.9.11", false, true}, {"v0.9.12", false, false},
		{"0.10.0", false, false}, {"dev", false, false}, {"v0.9.9-3-gabc", false, false},
	} {
		notice, err := c.Check(x.cli)
		var old *ErrCLITooOld
		if errors.As(err, &old) != x.tooOld || (notice != "") != x.notice {
			t.Errorf("%s: notice %q err %v", x.cli, notice, err)
		}
	}
	// No policy, no contract: nothing is enforced.
	if n, err := (&Contract{}).Check("0.0.1"); n != "" || err != nil {
		t.Fatalf("empty contract: %q %v", n, err)
	}
	var none *Contract
	if n, err := none.Check("0.0.1"); n != "" || err != nil {
		t.Fatalf("nil contract: %q %v", n, err)
	}
	// Malformed policy values are ignored.
	bad := &Contract{}
	bad.CLI.MinimumVersion = "latest"
	if _, err := bad.Check("0.0.1"); err != nil {
		t.Fatal(err)
	}
}

func TestFetchContractIsCachedForAnHour(t *testing.T) {
	hits := 0
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		hits++
		fmt.Fprint(w, `{"apiVersion":"v1","apiServer":"https://k","platform":{"version":"v0.9.12"},"cli":{"minimumVersion":"v0.9.10"}}`)
	}))
	defer srv.Close()
	c, now := cache(t)
	for i := 0; i < 3; i++ {
		got, err := FetchContract(context.Background(), srv.Client(), c, srv.URL)
		if err != nil || got.Platform.Version != "v0.9.12" || got.CLI.MinimumVersion != "v0.9.10" {
			t.Fatalf("%+v %v", got, err)
		}
	}
	if hits != 1 {
		t.Fatalf("fetched %d times", hits)
	}
	*now = now.Add(CacheTTL)
	_, _ = FetchContract(context.Background(), srv.Client(), c, srv.URL)
	if hits != 2 {
		t.Fatalf("not refreshed after an hour: %d", hits)
	}
}

func TestAnInstallationWithoutAContractIsNotAnError(t *testing.T) {
	srv := httptest.NewServer(http.NotFoundHandler())
	defer srv.Close()
	c, _ := cache(t)
	got, err := FetchContract(context.Background(), srv.Client(), c, srv.URL)
	if got != nil || err != nil {
		t.Fatalf("%+v %v", got, err)
	}
}

type lister struct {
	served map[string][]string
	calls  int
	fail   bool
}

func (l *lister) ServedResources(_ context.Context, gv string) ([]string, error) {
	l.calls++
	if l.fail {
		return nil, errors.New("connection refused")
	}
	return l.served[gv], nil
}

func TestCapabilities(t *testing.T) {
	c, _ := cache(t)
	l := &lister{served: map[string][]string{"security.kube-dc.com/v1alpha1": {"kmskeys", "managedsecrets", "managedsecrets/status"}}}
	caps := NewCapabilities("https://kube-api.example:6443", l, c)
	ctx := context.Background()
	if !caps.Serves(ctx, "security.kube-dc.com/v1alpha1", "kmskeys") || caps.Serves(ctx, "security.kube-dc.com/v1alpha1", "managedcertificates") {
		t.Fatal("served set wrong")
	}
	// A group version the cluster does not serve (older installation).
	if caps.Serves(ctx, "platform.kube-dc.com/v1alpha1", "secretvalues") {
		t.Fatal("unserved group answered true")
	}
	if l.calls != 2 {
		t.Fatalf("discovery calls %d", l.calls)
	}
	// Another process within the hour reuses the cache.
	l2 := &lister{}
	again := NewCapabilities("https://kube-api.example:6443", l2, c)
	if !again.Serves(ctx, "security.kube-dc.com/v1alpha1", "managedsecrets") || l2.calls != 0 {
		t.Fatalf("cache not used: %d", l2.calls)
	}
	// Discovery failing answers false and is not cached.
	l3 := &lister{fail: true}
	broken := NewCapabilities("https://other:6443", l3, c)
	if broken.Serves(ctx, "security.kube-dc.com/v1alpha1", "kmskeys") {
		t.Fatal("failure answered true")
	}
	l3.fail, l3.served = false, l.served
	if !broken.Serves(ctx, "security.kube-dc.com/v1alpha1", "kmskeys") {
		t.Fatal("failure was cached")
	}
}

func TestAudits(t *testing.T) {
	c := &Contract{}
	c.Audit.KubernetesGroups = []string{"security.kube-dc.com"}
	var none *Contract
	if !c.Audits("security.kube-dc.com") || c.Audits("services.kube-dc.com") || none.Audits("security.kube-dc.com") {
		t.Fatal("Audits")
	}
}
