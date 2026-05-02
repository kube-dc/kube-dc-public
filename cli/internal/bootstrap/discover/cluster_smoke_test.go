package discover_test

import (
	"context"
	"os"
	"testing"
	"time"

	"github.com/shalb/kube-dc/cli/internal/bootstrap/discover"
)

// TestClusterProbe_LiveFleet runs the probe against every cluster in the
// developer's local kube-dc-fleet checkout. Skipped in CI. The test
// exercises the full Run() path including OIDC token minting; if the
// operator hasn't `kube-dc login`'d to a cluster, that probe surfaces
// Unreachable with a login hint, which is also a valid PASS for this
// smoke test (we're checking that the probe behaves sensibly, not that
// every cluster is reachable from the dev box).
func TestClusterProbe_LiveFleet(t *testing.T) {
	const liveFleet = "/home/voa/projects/kube-dc-fleet"
	if _, err := os.Stat(liveFleet); err != nil {
		t.Skipf("live fleet not present at %s — skipping", liveFleet)
	}

	clusters, err := discover.ListClusters(liveFleet)
	if err != nil {
		t.Fatalf("ListClusters: %v", err)
	}

	for _, c := range clusters {
		c := c
		t.Run(c.Name, func(t *testing.T) {
			if c.KubeAPIURL == "" {
				t.Skipf("%s has no KUBE_API_EXTERNAL_URL", c.Name)
			}
			ctx, cancel := context.WithTimeout(context.Background(), 8*time.Second)
			defer cancel()
			probe, err := discover.NewClusterProbe(ctx, c.KubeAPIURL, 3*time.Second)
			if err != nil {
				t.Fatalf("NewClusterProbe: %v", err)
			}
			res := probe.Run(ctx)
			t.Logf("%-12s status=%s detail=%q hint=%q reconcilers=%d",
				c.Name, res.Status, res.Detail, res.FixHint, len(res.Reconcilers))

			// Status must be one of the documented values — a typo or
			// missing branch in aggregate() would surface as "".
			switch res.Status {
			case discover.StatusReady,
				discover.StatusReconciling,
				discover.StatusDrifted,
				discover.StatusFailed,
				discover.StatusUnreachable,
				discover.StatusUnknown:
				// ok
			default:
				t.Errorf("unexpected status %q", res.Status)
			}
			// Probe must return a non-empty detail line for any
			// non-Ready outcome — that's what populates the right pane.
			if res.Status != discover.StatusReady && res.Detail == "" {
				t.Errorf("status=%s but Detail is empty (no human-readable reason)", res.Status)
			}
		})
	}
}
