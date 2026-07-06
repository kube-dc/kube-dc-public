package main

import (
	"fmt"
	"io"
	"strings"

	"github.com/spf13/cobra"

	"github.com/shalb/kube-dc/cli/internal/bootstrap"
	"github.com/shalb/kube-dc/cli/internal/bootstrap/adopt"
	"github.com/shalb/kube-dc/cli/internal/bootstrap/clusterinit"
	"github.com/shalb/kube-dc/cli/internal/bootstrap/fleetconfig"
)

// bootstrapAdoptCmd registers `kube-dc bootstrap adopt` (V2) — the
// pre-install inventory + version-pin adoption for an EXISTING cluster.
//
// In this fleet "adopt" = let kube-dc's Flux take a component over IN
// PLACE (the Kustomizations run prune:false + force:true, so Flux adopts
// the running Helm release rather than deleting it). The safe-adoption
// action is to PIN cluster-config.env to the component's LIVE version so
// Flux's first reconcile doesn't upgrade/restart it.
//
//   - no flag:        advisory inventory (read-only) — what's already here.
//   - --pin-versions: detect live chart versions + pin them into
//     cluster-config.env (diff → commit → push, via the config engine).
func bootstrapAdoptCmd(fleetRepo *string) *cobra.Command {
	var (
		kubeconfig      string
		pinVersions     bool
		yes             bool
		noPush          bool
		githubToken     string
		provider        string
		skipComponents  []string
		manualPins      []string
		allowUndetected bool
	)
	cmd := &cobra.Command{
		Use:           "adopt [cluster]",
		Short:         "Inventory + version-pin pre-existing components for in-place adoption",
		SilenceErrors: true,
		SilenceUsage:  true,
		Long: `Detects components already on the target cluster that kube-dc would
otherwise install (cert-manager, kube-ovn, envoy-gateway, kubevirt,
kamaji, rook-ceph, monitoring, cnpg, metallb, keycloak, …), by CRD (most
reliable) then namespace.

In this fleet, ADOPT = Flux takes the component over IN PLACE: the
Kustomizations run prune:false + force:true, so Flux adopts the running
Helm release instead of deleting it. The safe-adoption action is to PIN
cluster-config.env to the component's LIVE chart version so Flux's first
reconcile does NOT upgrade/restart it.

  (no flag)        advisory inventory — read-only; what's already present.
  --pin-versions   read each component's live chart version and pin it
                   into clusters/<cluster>/cluster-config.env (diff →
                   commit → push, via the same engine as 'config set').

Point it at the TARGET cluster with --kubeconfig (or KUBECONFIG).
--pin-versions also needs the fleet repo (--repo) + the [cluster] name.`,
		Example: `  # Advisory inventory
  kube-dc bootstrap adopt --kubeconfig ./target.kubeconfig

  # Pin live versions so Flux adopts in place (preview, then --yes)
  kube-dc bootstrap adopt acme --kubeconfig ./acme.kubeconfig --pin-versions
  kube-dc bootstrap adopt acme --kubeconfig ./acme.kubeconfig --pin-versions --yes`,
		Args: cobra.MaximumNArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			clusterName := ""
			if len(args) == 1 {
				clusterName = args[0]
			}
			out := cmd.OutOrStdout()

			if provider != "" && provider != string(clusterinit.ProviderGitHub) && provider != string(clusterinit.ProviderGitLab) {
				return fmt.Errorf("bootstrap adopt: --provider must be github or gitlab (got %q)", provider)
			}

			session, err := bootstrap.NewSession(bootstrap.Options{
				FleetRepoPath: *fleetRepo,
				Kubeconfig:    kubeconfig,
			})
			if session != nil {
				defer session.Close()
			}
			// adopt needs a live cluster — a missing kubeconfig is fatal
			// (there's nothing to inventory).
			if err != nil {
				return fmt.Errorf("bootstrap adopt: need a target cluster (set --kubeconfig / KUBECONFIG): %w", err)
			}
			k8s := k8sFromSession(session)
			if k8s == nil {
				return fmt.Errorf("bootstrap adopt: no k8s client (set --kubeconfig / KUBECONFIG)")
			}

			if pinVersions {
				if clusterName == "" {
					return fmt.Errorf("bootstrap adopt --pin-versions: a <cluster> arg is required (to locate clusters/<cluster>/cluster-config.env)")
				}
				opts, err := parseAdoptPinOpts(skipComponents, manualPins)
				if err != nil {
					return fmt.Errorf("bootstrap adopt: %w", err)
				}
				return runAdoptPinVersions(cmd, out, k8s, *fleetRepo, clusterName, opts, adoptApply{yes: yes, noPush: noPush, allowUndetected: allowUndetected, githubToken: githubToken, provider: provider})
			}

			label := clusterName
			if label == "" {
				label = "<cluster>"
			}
			res, err := adopt.Detect(cmd.Context(), k8s)
			if err != nil {
				return fmt.Errorf("bootstrap adopt: %w", err)
			}
			renderAdopt(out, res, label)
			return nil
		},
	}
	cmd.Flags().StringVar(&kubeconfig, "kubeconfig", "", "Kubeconfig for the TARGET cluster to inventory (default: $KUBECONFIG, then ~/.kube/config)")
	cmd.Flags().BoolVar(&pinVersions, "pin-versions", false, "Pin cluster-config.env to each component's LIVE chart version (adopt in place; needs --repo + [cluster])")
	cmd.Flags().BoolVar(&yes, "yes", false, "With --pin-versions: apply (write + commit + push). Without it, preview the diff")
	cmd.Flags().BoolVar(&noPush, "no-push", false, "With --pin-versions --yes: commit locally, don't push")
	cmd.Flags().StringVar(&githubToken, "github-token", "", "Token for the push (default: `gh auth token`, or `glab auth status --show-token` with --provider gitlab)")
	cmd.Flags().StringVar(&provider, "provider", "", "Git host for the push credential: github (default) or gitlab")
	cmd.Flags().StringArrayVar(&skipComponents, "skip-component", nil, "Exclude a detected component from --pin-versions (keep/handle it yourself). Repeatable, by name e.g. ingress-nginx")
	cmd.Flags().StringArrayVar(&manualPins, "manual-pin", nil, "Supply a version for a component whose live version isn't readable, e.g. KUBEVIRT_VERSION=v1.8.1. Repeatable")
	cmd.Flags().BoolVar(&allowUndetected, "allow-undetected-version", false, "Proceed with --pin-versions even if some detected components are unresolved (lab/dev; production should skip/manual-pin them)")
	return cmd
}

// adoptApply bundles the --pin-versions apply-side flags.
type adoptApply struct {
	yes             bool
	noPush          bool
	allowUndetected bool
	githubToken     string
	provider        string
}

// parseAdoptPinOpts turns --skip-component / --manual-pin flags into
// adopt.PinOptions. Manual pins reuse the config key+value validation.
func parseAdoptPinOpts(skip, manual []string) (adopt.PinOptions, error) {
	opts := adopt.PinOptions{Skip: map[string]bool{}, Manual: map[string]string{}}
	for _, s := range skip {
		opts.Skip[s] = true
	}
	kvs, err := fleetconfig.ParseAssignments(manual)
	if err != nil {
		return opts, fmt.Errorf("--manual-pin: %w", err)
	}
	for _, kv := range kvs {
		opts.Manual[kv.Key] = kv.Value
	}
	return opts, nil
}

// runAdoptPinVersions detects live chart versions and pins them into the
// cluster's cluster-config.env so Flux adopts each component in place.
// Reuses the config engine's write+commit transaction (loadClusterEnv →
// fleetconfig.Plan → applyConfigSet).
func runAdoptPinVersions(cmd *cobra.Command, out io.Writer, k8s adopt.Inspector, fleetRepoFlag, clusterName string, opts adopt.PinOptions, ap adoptApply) error {
	env, repoRoot, err := loadClusterEnv(fleetRepoFlag, clusterName)
	if err != nil {
		return fmt.Errorf("bootstrap adopt --pin-versions: %w", err)
	}
	res, err := adopt.PinVersions(cmd.Context(), k8s, env, opts)
	if err != nil {
		return fmt.Errorf("bootstrap adopt --pin-versions: %w", err)
	}
	renderPinPlan(out, clusterName, res)

	// FAIL CLOSED on unresolved components: a detected component with no
	// live/manual version must not be silently left unpinned (adoption
	// would look complete while a high-risk component like KubeVirt stays
	// unpinned). The operator resolves each via --manual-pin KEY=VER or
	// --skip-component NAME, or accepts the risk with
	// --allow-undetected-version (lab/dev).
	if res.HasUnresolved() && !ap.allowUndetected {
		return fmt.Errorf("bootstrap adopt --pin-versions: %d component(s) unresolved (%s) — pin each with --manual-pin KEY=VERSION, exclude with --skip-component NAME, or (lab/dev only) pass --allow-undetected-version",
			len(res.Undetected), strings.Join(res.Undetected, ", "))
	}

	if len(res.Pins) == 0 {
		fmt.Fprintln(out, "[adopt] no version pins needed — nothing to change.")
		return nil
	}
	if !ap.yes {
		fmt.Fprintln(out, "\n[adopt] preview — re-run with --yes to write the pins (commit"+pushSuffix(ap.noPush)+").")
		return nil
	}

	// Enact via the config engine's write+commit transaction. allowAdd:
	// a version key may be absent on a fresh/foreign fleet overlay.
	kvs := make([]fleetconfig.KV, 0, len(res.Pins))
	for _, p := range res.Pins {
		kvs = append(kvs, fleetconfig.KV{Key: p.VersionKey, Value: p.Live})
	}
	changes, err := fleetconfig.Plan(env, kvs, true)
	if err != nil {
		return fmt.Errorf("bootstrap adopt --pin-versions: %w", err)
	}
	return applyConfigSet(cmd, out, repoRoot, env, changes, clusterName, ap.noPush, ap.githubToken, ap.provider)
}

func renderPinPlan(out io.Writer, clusterName string, res *adopt.PinResult) {
	fmt.Fprintf(out, "== adopt --pin-versions — %s (%d pin(s)) ==\n", clusterName, len(res.Pins))
	for _, p := range res.Pins {
		cur := p.Current
		if cur == "" {
			cur = "(unset)"
		}
		src := "pin to LIVE"
		if p.Manual {
			src = "pin to --manual-pin value"
		}
		fmt.Fprintf(out, "  ~ %s: %s → %s   (%s, %s)\n", p.VersionKey, cur, p.Live, p.Component, src)
	}
	for _, a := range res.AlreadyPinned {
		fmt.Fprintf(out, "    already pinned: %s\n", a)
	}
	for _, s := range res.Skipped {
		fmt.Fprintf(out, "    skipped (--skip-component): %s\n", s)
	}
	for _, u := range res.Undetected {
		fmt.Fprintf(out, "  ✗ %s: live version not readable (no Helm release) — resolve with --manual-pin or --skip-component\n", u)
	}
	for _, k := range res.UnusedManual {
		fmt.Fprintf(out, "  ⚠ --manual-pin %s matched no detected component (ignored)\n", k)
	}
	if len(res.Pins) > 0 {
		fmt.Fprintln(out, "\n  Pinning to LIVE means Flux adopts each in place (prune:false + force:true)")
		fmt.Fprintln(out, "  with no upgrade/restart. Bump the pins later, deliberately, per component.")
		fmt.Fprintln(out, "  REVIEW each pin: the value is the chart's metadata.version, which may")
		fmt.Fprintln(out, "  differ in FORMAT from the fleet's key (e.g. a `v` prefix, or chart-vs-app")
		fmt.Fprintln(out, "  version) — confirm it's what the fleet's HelmRelease consumes before --yes.")
	}
}

func renderAdopt(out io.Writer, res *adopt.Result, clusterName string) {
	fmt.Fprintf(out, "== adopt inventory — target cluster (%d pre-existing component(s)) ==\n", len(res.Findings))
	if res.FluxInstalled {
		fmt.Fprintln(out, "  note: Flux is already installed here — this may already be a kube-dc-managed cluster.")
	}
	if len(res.Findings) == 0 {
		fmt.Fprintln(out, "  ✓ greenfield — none of kube-dc's components are already present; nothing to adopt.")
		fmt.Fprintln(out, "  Proceed with `kube-dc bootstrap init`.")
		return
	}

	for _, f := range res.Findings {
		fmt.Fprintf(out, "\n  • %s  (detected via %s)\n", f.Component.Name, f.Via)
		fmt.Fprintf(out, "      kube-dc installs it at: %s\n", f.Component.FleetPath)
		if f.Component.VersionKey != "" {
			fmt.Fprintf(out, "      version pin: %s\n", f.Component.VersionKey)
		}
		if f.Component.Note != "" {
			fmt.Fprintf(out, "      note: %s\n", f.Component.Note)
		}
		fmt.Fprintf(out, "      recommended: %s\n", f.Recommend)
	}

	fmt.Fprintln(out, "\n  ADOPT (default): let Flux take the component over IN PLACE — the fleet's")
	fmt.Fprintln(out, "  Kustomizations run prune:false + force:true, so nothing you own is deleted.")
	fmt.Fprintf(out, "  Make it safe by pinning cluster-config.env to the LIVE versions first:\n")
	fmt.Fprintf(out, "      kube-dc bootstrap adopt %s --kubeconfig <target> --pin-versions --yes\n", clusterName)
	fmt.Fprintln(out, "  SKIP (keep your own, don't let kube-dc manage it): omit the component from")
	fmt.Fprintf(out, "  clusters/%s/<layer> — manual overlay edit; not automated here.\n", clusterName)
}
