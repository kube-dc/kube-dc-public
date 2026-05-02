package discover_test

import (
	"os"
	"testing"

	"github.com/shalb/kube-dc/cli/internal/bootstrap/discover"
)

// TestListClusters_LiveFleet runs against the developer's local checkout
// at /home/voa/projects/kube-dc-fleet when present. Skipped in CI where
// the path won't exist. This is a smoke test, not a unit test — its job
// is to prove the enumerator finds all three real clusters with their
// real cluster-config.env values.
func TestListClusters_LiveFleet(t *testing.T) {
	const liveFleet = "/home/voa/projects/kube-dc-fleet"
	if _, err := os.Stat(liveFleet); err != nil {
		t.Skipf("live fleet not present at %s — skipping", liveFleet)
	}

	clusters, err := discover.ListClusters(liveFleet)
	if err != nil {
		t.Fatalf("ListClusters: %v", err)
	}

	want := map[string]struct {
		domain string
		extNet string
	}{
		"cloud":  {domain: "kube-dc.cloud", extNet: "ext-cloud"},
		"stage":  {},
		"cs/zrh": {},
	}

	got := map[string]discover.Cluster{}
	for _, c := range clusters {
		got[c.Name] = c
	}

	for name, w := range want {
		c, ok := got[name]
		if !ok {
			t.Errorf("expected cluster %q in fleet, not found. Got: %v", name, names(clusters))
			continue
		}
		if w.domain != "" && c.Domain != w.domain {
			t.Errorf("cluster %s: DOMAIN = %q, want %q", name, c.Domain, w.domain)
		}
		if w.extNet != "" && c.ExtNetName != w.extNet {
			t.Errorf("cluster %s: EXT_NET_NAME = %q, want %q", name, c.ExtNetName, w.extNet)
		}
		if c.Env == nil {
			t.Errorf("cluster %s: Env not parsed", name)
		} else if len(c.Env.Keys()) == 0 {
			t.Errorf("cluster %s: zero keys parsed from cluster-config.env", name)
		}
		// Fleet convention: no kubeconfigs in tree.
		if c.HasInTreeKubeconfig {
			t.Errorf("cluster %s: HasInTreeKubeconfig=true (kubeconfig leaked into Git)", name)
		}
	}

	t.Logf("found %d clusters: %v", len(clusters), names(clusters))
	for _, c := range clusters {
		t.Logf("  %-12s domain=%-25s api=%-35s ext=%s", c.Name, c.Domain, c.KubeAPIURL, c.ExtNetName)
	}
}

func names(cs []discover.Cluster) []string {
	out := make([]string, len(cs))
	for i, c := range cs {
		out[i] = c.Name
	}
	return out
}
