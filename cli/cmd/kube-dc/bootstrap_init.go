package main

import (
	"context"
	"errors"
	"fmt"
	"io"
	"os"
	"os/exec"
	"sort"
	"strings"
	"time"

	"github.com/spf13/cobra"
	"github.com/spf13/pflag"

	"github.com/shalb/kube-dc/cli/internal/bootstrap"
	"github.com/shalb/kube-dc/cli/internal/bootstrap/clusterinit"
	"github.com/shalb/kube-dc/cli/internal/bootstrap/discover"
)

// bootstrapInitCmd registers `kube-dc bootstrap init`. M4-T01 of the
// installer-agentic-implementation-plan: the cobra surface, flag
// binding, and typed `InitOptions` construction. The engine slices
// (M4-T02..T13) attach to this command as they ship.
//
// The flag count is high (≥25) but every flag corresponds to a
// cluster-config.env knob or a behaviour gate documented in
// installer-prd §4.1.3. The plan flagged a "split into --config file"
// option once flags exceed 30 — we're under that threshold; v1 stays
// flat-flag.
//
// **Three-mode RunE** (all live as of M4-T12):
//
//	--dry-run             : builds + renders the plan; writes
//	                        canonical JSON to --plan-file; consent
//	                        marker to ~/.kube-dc/init-state/.
//	--apply-plan <path>   : loads the plan, verifies inputs haven't
//	                        drifted, then runs the full apply
//	                        (Scaffold + commit + push + flux-install).
//	(default)             : builds the plan from current opts +
//	                        runs the full apply in one go.
//
// **Cobra cosmetics:** SilenceErrors + SilenceUsage are intentional —
// the validation error already renders the failure cleanly; cobra's
// default "Error: ... \n Usage: ..." block would just add noise on
// validation failure. Help output via `--help` still works.
func bootstrapInitCmd(fleetRepo *string) *cobra.Command {
	o := &clusterinit.InitOptions{}
	var (
		setFlags, nodeNICFlags, addonFlags []string
	)

	cmd := &cobra.Command{
		Use:           "init",
		Short:         "Bootstrap a new Kube-DC cluster overlay into the fleet repo (M4)",
		SilenceErrors: true,
		SilenceUsage:  true,
		Long: `Init scaffolds a new cluster overlay into the kube-dc-fleet repo
and drives the post-RKE2 install through to a Flux-reconciled,
OpenBao-initialised cluster. Per installer-prd §4.1.

Three operating modes:

  --dry-run            Print the plan; exit 0; no mutations.
  --apply-plan <path>  Apply a previously-written plan (hash-pinned).
  (default)            Walk the install flow against the live fleet.

CI-friendly apply gate: when --no-tty is set, one of --yes,
--apply-plan, or --dry-run is required so unattended runs never
mutate without an explicit operator vouch.

The flag surface mirrors cluster-config.env; --set KEY=VALUE applies
deltas on top of the preset's defaults (--set keys must be
SCREAMING_SNAKE_CASE per the cluster-config.env convention).`,
		Example: `  # Dry-run an existing-fleet add (cloudacropolis-shape):
  kube-dc bootstrap init \
    --preset=cloud+public-vlan \
    --mode=install \
    --name=cloudacropolis \
    --domain=kdc.acropolis.example.com \
    --node-external-ip=217.117.26.52 \
    --email=ops@acropolis.example.com \
    --fleet-mode=existing-fleet \
    --repo=~/projects/kube-dc-fleet \
    --rook-mode=rook-ceph-multi-node \
    --node-nic=SRV5-Kub1=enp1s0 \
    --node-nic=SRV6-Kub1=enp1s0 \
    --node-nic=SRV7-Kub1=enp1s0 \
    --set=EXT_NET_VLAN_ID=1103 \
    --set=EXT_NET_INTERFACE=bond0 \
    --dry-run

  # CI apply:
  kube-dc bootstrap init --apply-plan=/tmp/cloudacropolis-plan.json --no-tty`,
		RunE: func(cmd *cobra.Command, args []string) error {
			// Parse repeatable flags into the typed map shape before
			// validation runs. Surface parse errors with the flag name
			// so operators don't have to guess which --set was bad.
			if pairs, err := clusterinit.ParseSetPairs(setFlags); err != nil {
				return fmt.Errorf("--set: %w", err)
			} else {
				o.Sets = pairs
			}
			if pairs, err := clusterinit.ParseSetPairs(nodeNICFlags); err != nil {
				return fmt.Errorf("--node-nic: %w", err)
			} else {
				o.NodeNICs = pairs
			}
			o.Addons = addonFlags

			// --repo is the persistent flag from the parent bootstrap
			// command; reflect it into options so the engine doesn't
			// have to re-resolve it.
			if o.Repo == "" && fleetRepo != nil {
				o.Repo = *fleetRepo
			}

			// `--no-tty` auto-toggles when stdout is captured (tests,
			// pipelines). Mirror the doctor/status convention so the
			// apply gate doesn't trip on `kube-dc bootstrap init |
			// tee log.txt` (the consent here is the explicit `--yes`
			// or `--apply-plan`, not the writer shape).
			if !o.NoTTY && !isWriterTTY(cmd.OutOrStdout()) {
				o.NoTTY = true
			}

			// Resolve --mode=auto BEFORE Validate so the substituted
			// Mode reaches both Validate (which still refuses
			// ModeAuto as a safety net) and BuildPlan. Pass-through
			// for explicit --mode values is a no-op inside
			// ResolveMode. (M4-T03.)
			autoResolution, err := resolveAutoMode(cmd.Context(), cmd.OutOrStdout(), o)
			if err != nil {
				return err
			}

			if err := o.Validate(); err != nil {
				return err
			}

			// Preset's required-key check runs after structural
			// Validate so the operator sees one error per pre-flight
			// concern (structural typos first, then preset-shape
			// gaps). M4-T04 catches "missing EXT_PUBLIC_VLAN_ID for
			// cloud+public-vlan" before the plan is ever rendered.
			if err := clusterinit.ValidatePresetRequiredKeys(o); err != nil {
				return err
			}

			// M4-T13: domain-collision check against existing
			// siblings. Fails loudly if the operator's --domain is
			// already in use; otherwise no-op for non-existing-fleet
			// modes.
			if err := checkDomainCollision(o); err != nil {
				return err
			}

			// M4-T09: age-key enrollment check (existing-fleet branch).
			// Skipped on dry-run when there's no plausible age key on
			// disk — operators can still dry-run a plan without being
			// a keyholder yet; apply paths will refuse the engine
			// when M5-T01 needs the key.
			if err := validateAgeKeyEnrollment(cmd.OutOrStdout(), o); err != nil {
				if o.DryRun && (errors.Is(err, clusterinit.ErrAgeKeyNotFound) ||
					errors.Is(err, clusterinit.ErrAgeKeyGenerateNotImplemented)) {
					fmt.Fprintf(cmd.OutOrStdout(), "[sops] WARNING: %v\n", err)
				} else {
					return err
				}
			}

			return runInit(cmd.Context(), cmd.OutOrStdout(), o, autoResolution)
		},
	}

	// --- Topology ---
	cmd.Flags().StringVar((*string)(&o.Preset), "preset", "",
		fmt.Sprintf("Network topology preset (one of %s; required)", joinPresets(clusterinit.AllPresets)))
	cmd.Flags().StringVar((*string)(&o.Mode), "mode", "",
		fmt.Sprintf("Operating mode (one of %s; required until M4-T03 ships auto-detection)", joinModes(clusterinit.AllModes)))
	cmd.Flags().StringVar(&o.Name, "name", "",
		"Cluster name (lowercase, dashes, optionally nested with /; required)")
	cmd.Flags().StringVar(&o.Domain, "domain", "",
		"Cluster FQDN (e.g. kdc.acropolis.example.com; required)")
	cmd.Flags().StringVar(&o.NodeExternalIP, "node-external-ip", "",
		"External IP of the cluster's wildcard target (one of the public IPs of any node; required)")
	cmd.Flags().StringVar(&o.Email, "email", "",
		"Operator email — used for cert-manager / LetsEncrypt registration (required)")

	// --- Fleet ---
	cmd.Flags().StringVar((*string)(&o.FleetMode), "fleet-mode", "",
		fmt.Sprintf("Fleet-repo relation (one of %s; required)", joinFleetModes(clusterinit.AllFleetModes)))
	cmd.Flags().StringVar(&o.GitHubOwner, "github-owner", "",
		"GitHub owner for --fleet-mode=new-repo (required in that mode)")
	cmd.Flags().StringVar(&o.GitHubRepo, "github-repo", "",
		"GitHub repo name for --fleet-mode=new-repo (required in that mode)")
	cmd.Flags().StringVar(&o.GitHubToken, "github-token", "",
		"GitHub token; leave unset to source via `gh auth token` at engine time (never logged)")

	// --- Overrides ---
	cmd.Flags().StringSliceVar(&setFlags, "set", nil,
		"Override a cluster-config.env key (repeatable; KEY=VALUE; SCREAMING_SNAKE_CASE)")
	cmd.Flags().StringSliceVar(&nodeNICFlags, "node-nic", nil,
		"Per-node primary NIC (repeatable; NODE=IFACE; drives the customInterfaces patch)")

	// --- Rook ---
	cmd.Flags().StringVar((*string)(&o.RookMode), "rook-mode", string(clusterinit.RookDisabled),
		fmt.Sprintf("Storage strategy (one of %s)", joinRookModes(clusterinit.AllRookModes)))
	cmd.Flags().StringVar(&o.RookOSDNode, "rook-osd-node", "",
		"Node hosting the OSD for --rook-mode=rook-ceph-local")
	cmd.Flags().IntVar(&o.RookOSDSizeGB, "rook-osd-size-gb", 0,
		"OSD size in GB for --rook-mode=rook-ceph-local")

	// --- Addons ---
	cmd.Flags().StringSliceVar(&addonFlags, "addon", nil,
		"Enable an addon (repeatable; one of: metallb, sso-google, stripe-billing, velero)")

	// --- Behaviour gates ---
	cmd.Flags().BoolVar(&o.AllowDNSNotReady, "allow-dns-not-ready", false,
		"Proceed even when M1-T03 DNS probes fail (certs will land Pending until DNS is wired)")
	cmd.Flags().BoolVar(&o.AllowNoKubevirtEligible, "allow-no-kubevirt-eligible", false,
		"Proceed even when the target cluster reports 0 NFD kubevirt-eligible nodes (VM workloads will fail to schedule until at least one node exposes /dev/kvm)")
	cmd.Flags().StringVar(&o.SSHHost, "ssh-host", "",
		"SSH host for auto-kubeconfig-pull (M4-T06; deferred — operator must pass kubeconfig manually for v1)")
	cmd.Flags().BoolVar(&o.NoSSH, "no-ssh", false, "Skip the SSH kubeconfig-pull step")
	cmd.Flags().BoolVar(&o.NoInstallPrereqs, "no-install-prereqs", false,
		"Skip the auto-install-prerequisites step (M4-T07)")
	cmd.Flags().BoolVar(&o.NoCreateRepo, "no-create-repo", false,
		"Skip the GitHub repo creation step (M4-T05; existing-fleet mode skips this anyway)")
	cmd.Flags().StringVar(&o.MirrorRegistry, "mirror-registry", "",
		"Container image mirror registry (airgap installs)")
	cmd.Flags().StringVar(&o.BundlePullSecret, "bundle-pull-secret", "",
		"Path to a Docker pull-secret JSON (airgap installs)")
	cmd.Flags().StringVar(&o.OpenBaoSharesOut, "openbao-shares-out", "",
		"Operator-supplied write-only file for the 5 OpenBao shares (mode 0600; never under a git tree)")

	// --- Plan/apply flow ---
	cmd.Flags().BoolVar(&o.DryRun, "dry-run", false, "Print the plan; exit 0; no mutations")
	cmd.Flags().BoolVar(&o.DryRun, "print-plan", false, "Alias for --dry-run")
	cmd.Flags().StringVar(&o.PlanFile, "plan-file", "",
		"With --dry-run, write the plan JSON here; with --apply-plan, ignored unless it matches --apply-plan")
	cmd.Flags().StringVar(&o.ApplyPlan, "apply-plan", "",
		"Apply a previously-written plan JSON (hash-pinned; refuses on input drift)")
	cmd.Flags().BoolVar(&o.NoPush, "no-push", false, "Commit locally; do not push to the fleet remote")
	cmd.Flags().BoolVar(&o.NoTTY, "no-tty", false, "Plain stdout (required in CI)")
	cmd.Flags().BoolVarP(&o.Yes, "yes", "y", false, "Skip all interactive confirmations")

	// Mark --print-plan as deprecated-but-supported so help output
	// shows --dry-run as the canonical form. Cobra surfaces both.
	if f := cmd.Flags().Lookup("print-plan"); f != nil {
		f.Hidden = true
	}

	// Soft-validate the flag-set order at registration time. If a
	// future change forgets to wire a flag's parser, this is a fast
	// way to spot it.
	if err := assertRequiredFlagsRegistered(cmd.Flags()); err != nil {
		// Defensive panic — registration is static and bugs here are
		// build-time issues, not runtime.
		panic(fmt.Sprintf("bootstrap init: %v", err))
	}

	return cmd
}

// modeResolution is the value `resolveAutoMode` returns: the
// detected (or explicit) Mode plus the human-readable reason
// rendered as the plan-header "Auto-detected: …" line.
type modeResolution struct {
	Mode   clusterinit.Mode
	Reason string
	// AutoDetected is true when ModeAuto was passed and a probe ran.
	// false for explicit --mode flags (we still record the reason
	// so the renderer can echo "explicit --mode=X" uniformly).
	AutoDetected bool
}

// resolveAutoMode handles `--mode=auto` by probing the current
// kubeconfig and substituting the detected mode into `o.Mode`. For
// any other --mode value this is a pure pass-through.
//
// Probe failures (no kubeconfig, apiserver unreachable, RBAC denied
// on the canary namespace) surface as typed errors via
// clusterinit.ErrK8sUnreachable + a wrap from the prober's
// constructor. The cobra layer prints these directly — operators
// fix the kubeconfig or pass --mode=install|adopt|resume explicitly.
//
// The 5-second probe timeout matches M1 doctor's per-probe budget;
// long enough for a healthy cluster, short enough that a hung
// kubeconfig doesn't block `init`.
func resolveAutoMode(ctx context.Context, out io.Writer, o *clusterinit.InitOptions) (modeResolution, error) {
	if o.Mode != clusterinit.ModeAuto {
		// Explicit --mode — no probe needed. Surface the choice for
		// the plan header uniformly.
		mode, reason, _ := clusterinit.ResolveMode(ctx, o, nil)
		return modeResolution{Mode: mode, Reason: reason, AutoDetected: false}, nil
	}

	if ctx == nil {
		ctx = context.Background()
	}
	probeCtx, cancel := context.WithTimeout(ctx, modeProbeTimeout)
	defer cancel()

	prober, err := newRealModeProber("")
	if err != nil {
		return modeResolution{}, fmt.Errorf("%w (pass --mode=install|adopt|resume explicitly)", err)
	}

	mode, reason, err := clusterinit.ResolveMode(probeCtx, o, prober)
	if err != nil {
		return modeResolution{}, err
	}
	fmt.Fprintf(out, "Auto-detected mode: %s — %s\n\n", mode, reason)
	return modeResolution{Mode: mode, Reason: reason, AutoDetected: true}, nil
}

// runInit dispatches on the three operating modes documented in the
// cobra Long help: --dry-run / --apply-plan / default-apply. After
// M4-T12 the default-apply and --apply-plan paths both invoke the
// real Apply engine (Scaffold + commit + push + flux-install); only
// flag-validation gating differs.
func runInit(ctx context.Context, out io.Writer, o *clusterinit.InitOptions, modeRes modeResolution) error {
	switch {
	case o.DryRun:
		return runInitDryRun(out, o)

	case o.ApplyPlan != "":
		return runInitApplyPlan(ctx, out, o)

	default:
		// Default-flow apply: build the plan from validated current
		// inputs, then hand to Apply directly (no LoadPlan or
		// VerifyApplyPlanInput — those gates are specific to the
		// --apply-plan path where the plan was built in a prior
		// session).
		_ = modeRes
		return runInitDefaultApply(ctx, out, o)
	}
}

// runInitDryRun derives the Plan from `o`, renders it for human
// review, optionally writes the canonical JSON to --plan-file, and
// writes the local consent marker so `--no-tty` apply paths can verify
// "yes, this exact plan was reviewed in this session".
//
// **FleetState in T02**: stubbed — M4-T13 will populate from disk
// (clusters/ listing + sibling cluster-config.env parse). For now,
// fleet-mode=existing-fleet plans surface placeholder prior-cluster
// info derived from the cobra layer's resolveFleetRepo + a directory
// scan if --repo is set; otherwise empty.
func runInitDryRun(out io.Writer, o *clusterinit.InitOptions) error {
	fleet := discoverFleetState(o)
	plan, err := clusterinit.BuildPlan(o, fleet)
	if err != nil {
		return fmt.Errorf("build plan: %w", err)
	}

	fmt.Fprintln(out, "=== kube-dc bootstrap init — DRY RUN ===")
	fmt.Fprintln(out)
	plan.Render(out)
	fmt.Fprintln(out)
	fmt.Fprintln(out, "No mutations performed. To apply this plan, re-run with --apply-plan <plan-file> --yes (or drop --dry-run + add --yes for the default-apply path).")

	if o.PlanFile != "" {
		if err := clusterinit.WritePlanFile(plan, o.PlanFile); err != nil {
			return fmt.Errorf("write plan-file: %w", err)
		}
		fmt.Fprintf(out, "Plan written to %s (mode 0644, atomic temp+rename).\n", o.PlanFile)
	}

	// Write the local consent marker so a subsequent `--no-tty`
	// apply (without --yes / --apply-plan) can verify the operator
	// just reviewed this exact plan. Marker is a per-cluster file
	// under ~/.kube-dc/init-state/ — non-fatal if it fails.
	if err := clusterinit.WriteConsentMarker(plan); err != nil {
		fmt.Fprintf(out, "Note: could not write consent marker (%v); --no-tty apply without --yes/--apply-plan will refuse.\n", err)
	}
	return nil
}

// runInitApplyPlan is the `--apply-plan <path>` flow per the
// reviewer's pre-T10 gate ordering: Validate (already run upstream
// in cobra RunE) → LoadPlan → VerifyApplyPlanInput → Apply.
//
// The plan loaded from disk is the source of truth for the
// cluster's identity (Name, Domain, Preset, Mode,
// InheritedDefaults); the operator's current `--set` /
// `--node-nic` values must hash-match what was in the plan
// (`VerifyApplyPlanInput`) — that's how we ensure the operator
// applies exactly what they reviewed at dry-run time.
//
// The apply-plan verbatim contract from plan.go is enforced here
// by SHAPE: Apply receives `plan` (loaded) + `o.Sets` / `o.NodeNICs`
// (verified to match the plan's inputs via the hash). The engine
// never calls `discoverFleetState` or `InheritFromSiblings` on this
// path — fleet-state changes between dry-run and apply do not
// influence what gets written.
func runInitApplyPlan(ctx context.Context, out io.Writer, o *clusterinit.InitOptions) error {
	fmt.Fprintln(out, "=== kube-dc bootstrap init — APPLY-PLAN ===")
	fmt.Fprintf(out, "Plan source: %s\n", o.ApplyPlan)

	plan, err := clusterinit.LoadPlan(o.ApplyPlan)
	if err != nil {
		return fmt.Errorf("load plan: %w", err)
	}
	if err := clusterinit.VerifyApplyPlanInput(plan, o); err != nil {
		return fmt.Errorf("verify plan inputs: %w", err)
	}

	fmt.Fprintf(out, "Loaded plan for cluster %q (planHash=%s, %d scripts).\n",
		plan.ClusterName, plan.PlanHash, len(plan.ScriptsToRun))
	fmt.Fprintln(out, "Inputs verified — applying plan verbatim.")
	return runApplyEngine(ctx, out, o, plan)
}

// runInitDefaultApply is the no-flags-apply path: BuildPlan from
// current inputs in this session, then call Apply. No drift check
// is needed because the plan and the engine's `Sets`/`NodeNICs`
// come from the same InitOptions instance — they're coherent by
// construction.
func runInitDefaultApply(ctx context.Context, out io.Writer, o *clusterinit.InitOptions) error {
	fmt.Fprintln(out, "=== kube-dc bootstrap init — APPLY ===")
	fleet := discoverFleetState(o)
	plan, err := clusterinit.BuildPlan(o, fleet)
	if err != nil {
		return fmt.Errorf("build plan: %w", err)
	}
	fmt.Fprintf(out, "Built plan for cluster %q (planHash=%s, %d scripts).\n",
		plan.ClusterName, plan.PlanHash, len(plan.ScriptsToRun))
	return runApplyEngine(ctx, out, o, plan)
}

// runApplyEngine is the shared tail that both apply paths converge
// on. Builds a `bootstrap.Session` for the adapters, resolves the
// GitHub token if --github-token wasn't passed, then calls
// `clusterinit.Apply`.
func runApplyEngine(ctx context.Context, out io.Writer, o *clusterinit.InitOptions, plan *clusterinit.Plan) error {
	session, err := bootstrap.NewSession(bootstrap.Options{
		FleetRepoPath: o.Repo,
	})
	if err != nil {
		// Mock-mode + non-kubeconfig real flows both return errors
		// here; the engine NEEDS a real session for the apply
		// path, so surface the failure directly. (Mock-mode apply
		// is a future concern — the M5 OpenBao mock can run end-to-
		// end against scenario fixtures, but T12 doesn't gate
		// behind that yet.)
		return fmt.Errorf("apply: build session: %w", err)
	}
	if session != nil {
		defer session.Close()
	}

	// M4-T08: DNS verification gate. Runs BEFORE the token
	// resolution / Apply so a failure surfaces as fast as possible,
	// before any disk-mutating step or `gh auth token` subshell.
	// Reads the same DNSClient the doctor probes use; skipped only
	// when --allow-dns-not-ready is explicitly set (operator opts
	// in to a "certs land Pending" install shape). See
	// clusterinit/dnsgate.go for the wildcard + explicit-FQDN
	// fallback + record-set printer.
	if err := clusterinit.CheckDNSReady(ctx, clusterinit.DNSGateOptions{
		Domain:           o.Domain,
		NodeExternalIP:   o.NodeExternalIP,
		AllowDNSNotReady: o.AllowDNSNotReady,
		DNS:              session.DNS,
		Out:              out,
	}); err != nil {
		return err
	}

	// M6-T05: NFD-derived KubeVirt-eligibility gate. Runs after the
	// DNS gate (both are read-only pre-Apply preflights; DNS first
	// because it also gates non-VM install shapes) and before Apply.
	// Soft-skips on any of: no K8sClient, NFDDetect error, NFD not
	// installed yet (fresh-RKE2 pre-Apply — Apply is what will
	// install it). Blocks only when NFD IS installed and reports 0
	// kubevirt-eligible nodes (a demonstrable hardware/config
	// misconfig). Escape hatch: --allow-no-kubevirt-eligible.
	if err := clusterinit.CheckKubeVirtEligibility(ctx, clusterinit.NFDGateOptions{
		K8s:                     session.K8s,
		AllowNoKubevirtEligible: o.AllowNoKubevirtEligible,
		Out:                     out,
	}); err != nil {
		return err
	}

	// --no-push uses Git.Commit (no push), so a GitHub token would
	// be ignored. Skip the resolution entirely to avoid an
	// unnecessary `gh auth token` subshell + log noise (M4-T12
	// review-pass — P3).
	var token string
	if !o.NoPush {
		token = resolveGitHubToken(o, out)
	}
	return clusterinit.Apply(ctx, clusterinit.ApplyOptions{
		Plan:           plan,
		FleetRepo:      o.Repo,
		NodeExternalIP: o.NodeExternalIP,
		Sets:           o.Sets,
		NodeNICs:       o.NodeNICs,
		Runner:         session.Scripts,
		Git:            session.Git,
		GitHubToken:    token,
		NoPush:         o.NoPush,
		Out:            out,
	})
}

// resolveGitHubToken returns the operator's GitHub PAT for the
// CommitAndPush step. Precedence:
//
//  1. `--github-token` flag value (explicit operator override).
//  2. `gh auth token` subshell — picks up the operator's existing
//     `gh auth login` session.
//  3. Empty string — caller (Apply) surfaces the push-auth error
//     from the git adapter if a token was actually needed.
//
// Never logged. The fallback subshell output is captured into a
// local variable and trimmed; not echoed to the operator log.
func resolveGitHubToken(o *clusterinit.InitOptions, out io.Writer) string {
	if o.GitHubToken != "" {
		fmt.Fprintln(out, "[apply] using --github-token flag value")
		return o.GitHubToken
	}
	cmd := exec.Command("gh", "auth", "token")
	cmd.Stderr = io.Discard
	stdout, err := cmd.Output()
	if err != nil {
		fmt.Fprintln(out, "[apply] no GitHub token resolved (gh auth token failed; push may fail if remote requires auth)")
		return ""
	}
	token := strings.TrimSpace(string(stdout))
	if token == "" {
		fmt.Fprintln(out, "[apply] no GitHub token resolved (gh auth token returned empty)")
		return ""
	}
	fmt.Fprintln(out, "[apply] resolved GitHub token via gh auth token")
	return token
}

// discoverFleetState returns a best-effort snapshot of the fleet repo
// for plan generation. M4-T13 wired the full sibling walk + version
// inheritance + domain-collision check.
//
// **Two-level walk**: `discover.ListClusters` handles the nested
// `clusters/cs/zrh/` shape (CloudSigma's multi-region grouping) so
// prior clusters list correctly without re-rolling the walker.
//
// **Inheritance (M4-T13)**: for each non-self sibling, load
// cluster-config.env + the file's ModTime, hand to
// `clusterinit.InheritFromSiblings` which picks the
// most-recently-modified sibling as the template and merges version
// pins (KUBE_DC_VERSION, *_CHART_VERSION, *_TAG suffixes). The
// resulting Defaults seed `Plan.InheritedDefaults`.
//
// **Domain-collision check**: deferred to the cobra RunE
// (`checkDomainCollision` below) so the error surfaces BEFORE
// BuildPlan rather than as a downstream symptom.
func discoverFleetState(o *clusterinit.InitOptions) clusterinit.FleetState {
	if o.FleetMode != clusterinit.FleetExistingFleet || o.Repo == "" {
		return clusterinit.FleetState{}
	}
	st := clusterinit.FleetState{}
	clusters, err := discover.ListClusters(o.Repo)
	if err != nil {
		// Don't fail the whole dry-run — surface "no priors" silently
		// and let the operator notice the empty list. The hard
		// "existing-fleet requires --repo" check has already passed
		// at this point (Validate), so a ListClusters failure here is
		// usually a perms / sub-directory shape issue worth seeing
		// rather than dying on.
		return st
	}

	// Build the sibling list (excluding self if the operator's new
	// cluster name accidentally matches a sibling — the engine's
	// scaffold step rejects that loudly; here we just skip).
	siblings := make([]clusterinit.SiblingCluster, 0, len(clusters))
	for _, c := range clusters {
		if c.Name == o.Name {
			continue
		}
		st.PriorClusters = append(st.PriorClusters, c.Name)
		// ModTime of cluster-config.env drives the "most-recently-
		// modified template" pick. Failing the stat is non-fatal —
		// such a sibling falls to the alphabetic tiebreak.
		var modTime time.Time
		if info, ierr := os.Stat(c.EnvPath); ierr == nil {
			modTime = info.ModTime()
		}
		// Convert the parsed Env to the flat map InheritFromSiblings
		// expects. Iterating Keys preserves the operator's intent
		// without coupling clusterinit to config.Env's internals.
		envMap := make(map[string]string, len(c.Env.Keys()))
		for _, k := range c.Env.Keys() {
			envMap[k], _ = c.Env.Get(k)
		}
		siblings = append(siblings, clusterinit.SiblingCluster{
			Name:    c.Name,
			Domain:  c.Domain,
			Env:     envMap,
			ModTime: modTime,
		})
	}

	st.Siblings = siblings
	inheritance := clusterinit.InheritFromSiblings(siblings)
	st.InheritedDefaults = inheritance.Defaults
	st.InheritanceTemplate = inheritance.TemplateName
	return st
}

// checkDomainCollision runs the M4-T13 domain-collision check
// before BuildPlan. Returns the typed ErrDomainCollision wrapped
// with the sibling list so the cobra layer can surface a clean
// error. No-op for non-existing-fleet modes.
func checkDomainCollision(o *clusterinit.InitOptions) error {
	if o.FleetMode != clusterinit.FleetExistingFleet || o.Repo == "" {
		return nil
	}
	clusters, err := discover.ListClusters(o.Repo)
	if err != nil {
		return nil // ListClusters failures already surface in discoverFleetState
	}
	siblings := make([]clusterinit.SiblingCluster, 0, len(clusters))
	for _, c := range clusters {
		if c.Name == o.Name {
			continue
		}
		siblings = append(siblings, clusterinit.SiblingCluster{
			Name:   c.Name,
			Domain: c.Domain,
		})
	}
	return clusterinit.CheckDomainCollision(o.Domain, siblings)
}

// writeOptionsSummary prints a stable, redaction-safe view of the
// resolved InitOptions. GitHubToken is always shown as `<set>` or
// `<unset>` — never the value — so this output is safe to commit to
// CI logs.
func writeOptionsSummary(out io.Writer, o *clusterinit.InitOptions) {
	tok := "<unset>"
	if o.GitHubToken != "" {
		tok = "<set>"
	}
	pull := "<unset>"
	if o.BundlePullSecret != "" {
		pull = o.BundlePullSecret
	}
	sharesOut := "<unset>"
	if o.OpenBaoSharesOut != "" {
		sharesOut = o.OpenBaoSharesOut
	}
	fmt.Fprintf(out, "Cluster:      %s (preset=%s mode=%s)\n", o.Name, o.Preset, o.Mode)
	fmt.Fprintf(out, "Domain:       %s\n", o.Domain)
	fmt.Fprintf(out, "Node IP:      %s\n", o.NodeExternalIP)
	fmt.Fprintf(out, "Email:        %s\n", o.Email)
	fmt.Fprintf(out, "Fleet:        mode=%s repo=%s\n", o.FleetMode, o.Repo)
	if o.FleetMode == clusterinit.FleetNewRepo {
		fmt.Fprintf(out, "GitHub:       owner=%s repo=%s token=%s\n", o.GitHubOwner, o.GitHubRepo, tok)
	}
	fmt.Fprintf(out, "Rook:         mode=%s osd-node=%s osd-size=%dGB\n", o.RookMode, o.RookOSDNode, o.RookOSDSizeGB)
	if len(o.Addons) > 0 {
		fmt.Fprintf(out, "Addons:       %v\n", o.Addons)
	}
	if len(o.Sets) > 0 {
		fmt.Fprintln(out, "Overrides:")
		// Sort keys so the summary output is deterministic for
		// snapshot tests + log-diffing (M4-T01+T02 review-pass — P3).
		keys := make([]string, 0, len(o.Sets))
		for k := range o.Sets {
			keys = append(keys, k)
		}
		sort.Strings(keys)
		for _, k := range keys {
			fmt.Fprintf(out, "  %s=%s\n", k, o.Sets[k])
		}
	}
	if len(o.NodeNICs) > 0 {
		fmt.Fprintln(out, "Node NICs:")
		keys := make([]string, 0, len(o.NodeNICs))
		for k := range o.NodeNICs {
			keys = append(keys, k)
		}
		sort.Strings(keys)
		for _, k := range keys {
			fmt.Fprintf(out, "  %s=%s\n", k, o.NodeNICs[k])
		}
	}
	fmt.Fprintln(out, "Gates:")
	fmt.Fprintf(out, "  allow-dns-not-ready=%t  allow-no-kubevirt-eligible=%t  no-ssh=%t  no-install-prereqs=%t  no-create-repo=%t  no-push=%t  no-tty=%t  yes=%t\n",
		o.AllowDNSNotReady, o.AllowNoKubevirtEligible, o.NoSSH, o.NoInstallPrereqs, o.NoCreateRepo, o.NoPush, o.NoTTY, o.Yes)
	fmt.Fprintf(out, "Files:        mirror-registry=%s  bundle-pull-secret=%s  openbao-shares-out=%s\n",
		emptyAsNone(o.MirrorRegistry), pull, sharesOut)
}

func emptyAsNone(s string) string {
	if s == "" {
		return "<unset>"
	}
	return s
}

// joinPresets / joinModes / joinFleetModes / joinRookModes are
// thin wrappers so the cobra layer doesn't import strings just to
// pretty-print a slice. They mirror the in-package helpers in
// clusterinit/options.go but operate on the public types.
func joinPresets(p []clusterinit.Preset) string { return joinStringers(p) }
func joinModes(m []clusterinit.Mode) string     { return joinStringers(m) }
func joinFleetModes(f []clusterinit.FleetMode) string {
	return joinStringers(f)
}
func joinRookModes(r []clusterinit.RookMode) string { return joinStringers(r) }

// joinStringers takes any string-aliased slice and pipe-joins it.
// Generics keep the four wrappers above to one-line each without
// reflection.
func joinStringers[T ~string](in []T) string {
	out := ""
	for i, v := range in {
		if i > 0 {
			out += "|"
		}
		out += string(v)
	}
	return out
}

// assertRequiredFlagsRegistered is a defensive build-time check that
// every flag in the §4.1.3 contract is registered. If a future commit
// forgets to wire one in (or renames it), the panic at registration
// surfaces it in tests immediately instead of producing a silent
// validation gap.
func assertRequiredFlagsRegistered(fs *pflag.FlagSet) error {
	required := []string{
		"preset", "mode", "name", "domain", "node-external-ip", "email",
		"fleet-mode", "github-owner", "github-repo", "github-token",
		"set", "node-nic",
		"rook-mode", "rook-osd-node", "rook-osd-size-gb",
		"addon",
		"allow-dns-not-ready", "ssh-host", "no-ssh", "no-install-prereqs", "no-create-repo",
		"mirror-registry", "bundle-pull-secret", "openbao-shares-out",
		"dry-run", "plan-file", "apply-plan", "no-push", "no-tty", "yes",
	}
	for _, name := range required {
		if fs.Lookup(name) == nil {
			return fmt.Errorf("flag %q missing from init cobra surface", name)
		}
	}
	return nil
}

