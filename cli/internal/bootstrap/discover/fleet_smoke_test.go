package discover_test

import (
	"os"
	"testing"

	"github.com/shalb/kube-dc/cli/internal/bootstrap/discover"
)

// TestListClusters_LiveFleet is an opt-in smoke test: it runs against
// a real fleet checkout ONLY when the operator points at one via the
// KUBE_DC_FLEET_SMOKE env var (skipped otherwise — CI, fresh clones).
//
//	KUBE_DC_FLEET_SMOKE=~/projects/my-fleet go test ./internal/bootstrap/discover/ -run LiveFleet -v
//
// Assertions are STRUCTURAL only — at least one cluster enumerates,
// every cluster's cluster-config.env parses with keys, and the fleet
// convention (no kubeconfigs in tree) holds. It deliberately does NOT
// assert specific cluster names/domains: this package ships to the
// public mirror, and real infrastructure identifiers must never be
// hardcoded here (2026-07-04 sweep).
func TestListClusters_LiveFleet(t *testing.T) {
	liveFleet := os.Getenv("KUBE_DC_FLEET_SMOKE")
	if liveFleet == "" {
		t.Skip("KUBE_DC_FLEET_SMOKE not set — skipping live-fleet smoke")
	}
	if _, err := os.Stat(liveFleet); err != nil {
		t.Skipf("fleet not present at %s — skipping", liveFleet)
	}

	clusters, err := discover.ListClusters(liveFleet)
	if err != nil {
		t.Fatalf("ListClusters: %v", err)
	}
	if len(clusters) == 0 {
		t.Fatal("expected at least one cluster in the live fleet")
	}

	for _, c := range clusters {
		if c.Name == "" {
			t.Error("cluster with empty name enumerated")
		}
		if c.Env == nil {
			t.Errorf("cluster %s: Env not parsed", c.Name)
		} else if len(c.Env.Keys()) == 0 {
			t.Errorf("cluster %s: zero keys parsed from cluster-config.env", c.Name)
		}
		// Fleet convention: no kubeconfigs in tree.
		if c.HasInTreeKubeconfig {
			t.Errorf("cluster %s: HasInTreeKubeconfig=true (kubeconfig leaked into Git)", c.Name)
		}
	}

	t.Logf("found %d clusters: %v", len(clusters), names(clusters))
}

func names(cs []discover.Cluster) []string {
	out := make([]string, len(cs))
	for i, c := range cs {
		out[i] = c.Name
	}
	return out
}
