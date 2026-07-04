package main

import (
	"fmt"
	"io"
	"text/tabwriter"

	"github.com/spf13/cobra"

	"github.com/shalb/kube-dc/cli/internal/bootstrap/topology"
)

// bootstrapDoctorTopologyCmd registers `kube-dc bootstrap doctor
// topology` — classifies the target cluster's external-networking
// topology into Class A/B/C and recommends whether the Internal
// Platform Endpoints feature should be enabled.
//
// Lives as a sibling subcommand under `doctor` (rather than folded
// into the unified `bootstrap doctor` probe set) because the
// topology probes need to talk to a specific cluster, while the
// rest of doctor runs against the operator's local environment.
// Mirrors the `anchors` shape — separate cobra command, dedicated
// package under cli/internal/bootstrap/, single-purpose output.
//
// Exit code is 0 in all classification cases (including
// VerdictAmbiguous — that's a successful "we couldn't tell" answer,
// not a probe failure). A non-zero exit is reserved for cases where
// the probe itself couldn't run (no kubeconfig, etc.).
func bootstrapDoctorTopologyCmd() *cobra.Command {
	var kubeconfig string

	cmd := &cobra.Command{
		Use:   "topology",
		Short: "Classify cluster topology and recommend internal-platform-endpoints setting",
		Long: `Classify the current cluster's external-networking topology
into one of three classes and recommend whether the Internal Platform
Endpoints feature should be enabled.

Classes:

  A — 1:1-NAT hairpin (cloudacropolis-class)          → REQUIRES the feature
  B — Flat-L2 with per-node externalIPs (cloud-class) → does NOT need it
  C — Cloud-provider LoadBalancer (AWS/GCP/Azure)     → does NOT need it

The classifier is a heuristic: probes look at cluster-side
configuration (cloud-provider integration, EnvoyProxy CR shape,
Fork E Service presence) and combine the per-probe hints into a
single verdict with a confidence label. For clusters where the
shape doesn't match any of the three known classes, the verdict is
"ambiguous" and you should fall back to the 5-line manual decision
aid in docs/platform/internal-platform-endpoints.md §"Do you need
this?".

Probes run against the current kubeconfig. Use KUBECONFIG env var or
--kubeconfig to target a specific cluster.`,
		Example: `  # Classify the cluster reachable via current kubeconfig
  kube-dc bootstrap doctor topology

  # Target a specific cluster
  KUBECONFIG=/path/to/cloudacropolis_config kube-dc bootstrap doctor topology

  # Or via flag
  kube-dc bootstrap doctor topology --kubeconfig /path/to/cluster_config`,
		SilenceErrors: true,
		SilenceUsage:  true,
		RunE: func(cmd *cobra.Command, _ []string) error {
			ctx := cmd.Context()
			r, err := topology.Probe(ctx, kubeconfig)
			if err != nil {
				return err
			}
			printTopologyResult(cmd.OutOrStdout(), r)
			return nil
		},
	}
	cmd.Flags().StringVar(&kubeconfig, "kubeconfig", "", "Path to kubeconfig file (defaults to KUBECONFIG env var or ~/.kube/config)")
	return cmd
}

// printTopologyResult writes the probe table + verdict + reasoning +
// recommendation in a consistent format. Three blocks:
//
//   1. PROBE table — one row per probe, with the class hint each
//      signal points at (or "—" for purely informational rows)
//   2. Classification line — Class + confidence + Verdict
//   3. Reasoning + Next + Reference
//
// The format matches the anchors doctor output shape (tabwriter
// table + footer) so operators get a consistent UX across the
// doctor subcommands.
func printTopologyResult(w io.Writer, r topology.Result) {
	tw := tabwriter.NewWriter(w, 0, 0, 2, ' ', 0)
	fmt.Fprintln(tw, "PROBE\tDETAIL\tCLASS HINT\tCONFIDENCE")
	for _, s := range r.Signals {
		hint := string(s.Hint)
		if hint == "" {
			hint = "—"
		}
		conf := s.Confidence
		if conf == "" {
			conf = "—"
		}
		fmt.Fprintf(tw, "%s\t%s\t%s\t%s\n", s.Probe, s.Detail, hint, conf)
	}
	tw.Flush()
	fmt.Fprintln(w)
	fmt.Fprintf(w, "Classification: Class %s  (confidence: %s)\n", r.Class, r.Confidence)
	fmt.Fprintf(w, "Internal Platform Endpoints: %s\n", r.Verdict)
	fmt.Fprintln(w)
	fmt.Fprintln(w, r.Reasoning)
	fmt.Fprintln(w)
	fmt.Fprintln(w, "Next:")
	fmt.Fprintln(w, "  "+r.Recommendation)
	fmt.Fprintln(w)
	fmt.Fprintln(w, "Reference: docs/platform/internal-platform-endpoints.md")
}
