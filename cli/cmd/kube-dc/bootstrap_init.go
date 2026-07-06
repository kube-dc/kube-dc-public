package main

import (
	"context"
	"errors"
	"fmt"
	"io"
	"net/url"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"sort"
	"strings"
	"time"

	"github.com/charmbracelet/huh"
	"github.com/spf13/cobra"
	"github.com/spf13/pflag"

	"github.com/shalb/kube-dc/cli/internal/bootstrap"
	"github.com/shalb/kube-dc/cli/internal/bootstrap/adopt"
	"github.com/shalb/kube-dc/cli/internal/bootstrap/clusterinit"
	"github.com/shalb/kube-dc/cli/internal/bootstrap/discover"
	"github.com/shalb/kube-dc/cli/internal/bootstrap/fleetconfig"
	"github.com/shalb/kube-dc/cli/internal/bootstrap/tui/screens/initform"
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
		setFlags, nodeNICFlags, addonFlags, cephNodeFlags []string
		// --object-storage-mode is canonical (OS-1); --rook-mode is a
		// deprecated alias for one release. Bound to separate vars so
		// RunE can detect a conflicting double-set instead of silently
		// letting parse order win.
		objectStorageModeFlag, rookModeAliasFlag string
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
		Example: `  # Dry-run an existing-fleet add (atlantis-shape):
  kube-dc bootstrap init \
    --preset=cloud+public-vlan \
    --mode=install \
    --name=atlantis \
    --domain=kdc.atlantis.example.com \
    --node-external-ip=203.0.113.52 \
    --email=ops@atlantis.example.com \
    --fleet-mode=existing-fleet \
    --repo=~/projects/kube-dc-fleet \
    --object-storage-mode=rook-ceph-multi-node \
    --ceph-node=host5-a=sdb \
    --ceph-node=host6-a=sdb \
    --ceph-node=host7-a=sdb \
    --node-nic=host5-a=enp1s0 \
    --node-nic=host6-a=enp1s0 \
    --node-nic=host7-a=enp1s0 \
    --set=EXT_NET_VLAN_ID=1103 \
    --set=EXT_NET_INTERFACE=bond0 \
    --dry-run

  # CI apply:
  kube-dc bootstrap init --apply-plan=/tmp/atlantis-plan.json --no-tty`,
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
			if pairs, err := clusterinit.ParseSetPairs(cephNodeFlags); err != nil {
				return fmt.Errorf("--ceph-node: %w", err)
			} else if len(pairs) > 0 {
				o.CephNodes = pairs
			}
			o.Addons = addonFlags

			// Resolve the canonical/alias mode pair (OS-1).
			// --object-storage-mode wins; --rook-mode is honoured only
			// when the canonical flag is absent (pflag already printed
			// its deprecation notice). A conflicting double-set is an
			// error — silently preferring either would surprise.
			switch {
			case objectStorageModeFlag != "" && rookModeAliasFlag != "" &&
				objectStorageModeFlag != rookModeAliasFlag:
				return fmt.Errorf("init: --object-storage-mode=%s conflicts with --rook-mode=%s (drop the deprecated --rook-mode)",
					objectStorageModeFlag, rookModeAliasFlag)
			case objectStorageModeFlag != "":
				o.RookMode = clusterinit.RookMode(objectStorageModeFlag)
			case rookModeAliasFlag != "":
				o.RookMode = clusterinit.RookMode(rookModeAliasFlag)
			}

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

			// T6 wizard: `kube-dc bootstrap init` launched bare in a
			// TTY (no --name, no plan flags) runs the huh form to
			// populate the SAME InitOptions the flags would — then
			// the normal Validate → plan → confirm path continues
			// unchanged (thin-generator contract, OS-5 §7.1). A
			// cancelled form aborts cleanly with options untouched.
			wizardFlags := ""
			if bareInitInvocation(cmd) && !o.NoTTY {
				// Sibling-mode hint (OS-5 §7.3): computed against a
				// copy forced to existing-fleet — at this point the
				// operator hasn't picked a fleet mode yet, and the
				// hint helper soft-fails to "" on any miss.
				hintOpts := *o
				hintOpts.FleetMode = clusterinit.FleetExistingFleet
				eq, err := initform.Run(o, siblingObjectStorageModeHint(&hintOpts))
				if err != nil {
					return fmt.Errorf("init wizard: %w", err)
				}
				wizardFlags = eq
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
				// OS-1 design call: never inherit the mode, but when a
				// template sibling exists its mode is the best hint an
				// operator could get. Enrich the typed required error;
				// any sniff failure degrades to the plain error.
				if errors.Is(err, clusterinit.ErrObjectStorageModeRequired) {
					if hint := siblingObjectStorageModeHint(o); hint != "" {
						return fmt.Errorf("%w; %s", err, hint)
					}
				}
				return err
			}

			// M4-T05 GitLab fail-closed boundary RETIRED (properly
			// this time): the coordinated `kube-dc-fleet` PR
			// landing alongside this slice made `flux-install.sh`
			// provider-aware via `KUBE_DC_PROVIDER` env — dispatches
			// `flux bootstrap github` vs `flux bootstrap gitlab`
			// with matching token env-var handling. `add-cluster.sh`
			// was already provider-agnostic (writes Flux
			// Kustomizations that reference the auto-generated
			// `GitRepository name: flux-system` — Flux stores the
			// remote URL there). `runFluxInstall` now passes
			// `KUBE_DC_PROVIDER` + owner/repo/token env through
			// ApplyOptions. The sentinel becomes a deprecated
			// no-op; retained one release for backward-compat
			// `errors.Is` checks.

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
			//
			// **Dry-run must NOT mutate** — the age-key auto-generate
			// path (M4-T09 greenfield-generate close) runs
			// `bootstrap/generate-age-key.sh` which creates `age.key`
			// on disk. Pass `mutationsAllowed=!o.DryRun` into the
			// gate so plan previews stay side-effect-free. Also pass
			// `cmd.Context()` so operator Ctrl-C cancellation
			// interrupts a long-running generate script.
			if err := validateAgeKeyEnrollment(cmd.Context(), cmd.OutOrStdout(), o, !o.DryRun); err != nil {
				if o.DryRun && (errors.Is(err, clusterinit.ErrAgeKeyNotFound) ||
					errors.Is(err, clusterinit.ErrAgeKeyDryRunSkip)) {
					fmt.Fprintf(cmd.OutOrStdout(), "[sops] WARNING: %v\n", err)
				} else {
					return err
				}
			}

			// T6 wizard tail: render the plan for review, ask for an
			// explicit apply consent (the flag path's --yes
			// equivalent), and always print the equivalent flag
			// invocation so the interactive run is scriptable.
			if wizardFlags != "" {
				out := cmd.OutOrStdout()
				if err := runInitDryRun(out, o); err != nil {
					return err
				}
				// Adopt-mode wizard step: show the live drift preview and,
				// when there's writable drift, offer to pin versions inline
				// (so Flux adopts in place). Declining is fine — the
				// CheckAdoptPinned gate still runs during Apply.
				if o.Mode == clusterinit.ModeAdopt {
					if err := runInitAdoptWizardStep(cmd, out, o); err != nil {
						return err
					}
				}
				fmt.Fprintf(out, "\nEquivalent non-interactive invocation:\n\n%s\n\n", wizardFlags)
				applyNow := false
				if err := huh.NewConfirm().
					Title("Apply this plan now?").
					Description("scaffold + commit + push + flux-install against the fleet repo").
					Affirmative("Apply").Negative("No — just print the plan").
					Value(&applyNow).Run(); err != nil {
					return fmt.Errorf("init wizard: %w", err)
				}
				if !applyNow {
					return nil
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
		"Cluster FQDN (e.g. kdc.atlantis.example.com; required)")
	cmd.Flags().StringVar(&o.NodeExternalIP, "node-external-ip", "",
		"External IP of the cluster's wildcard target (one of the public IPs of any node; required)")
	cmd.Flags().StringVar(&o.Email, "email", "",
		"Operator email — used for cert-manager / LetsEncrypt registration (required)")

	// --- Fleet ---
	cmd.Flags().StringVar((*string)(&o.FleetMode), "fleet-mode", "",
		fmt.Sprintf("Fleet-repo relation (one of %s; required)", joinFleetModes(clusterinit.AllFleetModes)))
	cmd.Flags().StringVar((*string)(&o.Provider), "provider", "",
		"Remote-repo hosting provider for --fleet-mode=new-repo (`github` or `gitlab`; default github)")
	cmd.Flags().StringVar(&o.GitHubOwner, "github-owner", "",
		"Owner/group for --fleet-mode=new-repo (required in that mode; holds the GitLab group when --provider=gitlab)")
	cmd.Flags().StringVar(&o.GitHubRepo, "github-repo", "",
		"Repo name for --fleet-mode=new-repo (required in that mode)")
	cmd.Flags().StringVar(&o.GitHubToken, "github-token", "",
		"Provider PAT; leave unset to source via `gh auth token` / `glab auth login` state (never logged)")

	// --- Overrides ---
	cmd.Flags().StringSliceVar(&setFlags, "set", nil,
		"Override a cluster-config.env key (repeatable; KEY=VALUE; SCREAMING_SNAKE_CASE)")
	cmd.Flags().StringSliceVar(&nodeNICFlags, "node-nic", nil,
		"Per-node primary NIC (repeatable; NODE=IFACE; drives the customInterfaces patch)")

	// --- Object storage (OS-1) ---
	// REQUIRED, no default: the old silent default-to-disabled shipped
	// clusters whose monitoring layer could never converge. `disabled`
	// stays available as an explicit choice (loud plan warning).
	cmd.Flags().StringVar(&objectStorageModeFlag, "object-storage-mode", "",
		fmt.Sprintf("Object storage mode (REQUIRED; one of %s; external-* recognised but fail closed — fleet stubs)", joinRookModes(clusterinit.AllRookModes)))
	cmd.Flags().StringVar(&rookModeAliasFlag, "rook-mode", "",
		"Deprecated alias for --object-storage-mode")
	_ = cmd.Flags().MarkDeprecated("rook-mode", "use --object-storage-mode")
	cmd.Flags().StringVar(&o.RookOSDNode, "rook-osd-node", "",
		"Node hosting the OSD for --object-storage-mode=rook-ceph-local")
	cmd.Flags().IntVar(&o.RookOSDSizeGB, "rook-osd-size-gb", 0,
		"OSD size in GB for --object-storage-mode=rook-ceph-local")
	cmd.Flags().StringVar(&o.RookOSDDevice, "rook-osd-device", "",
		"OSD device for --object-storage-mode=rook-ceph-local (default: fleet template's loop0)")
	cmd.Flags().StringSliceVar(&cephNodeFlags, "ceph-node", nil,
		"OSD node for --object-storage-mode=rook-ceph-multi-node (repeatable; NODE=DEVICE; exactly 3)")
	cmd.Flags().StringVar(&o.CephStorageClass, "ceph-storage-class", "",
		"StorageClass backing OSD PVCs for --object-storage-mode=rook-ceph-pvc")
	cmd.Flags().IntVar(&o.CephOSDCount, "ceph-osd-count", 0,
		"OSD PVC count for --object-storage-mode=rook-ceph-pvc (0 = fleet default 2)")
	cmd.Flags().IntVar(&o.CephOSDVolumeSizeGB, "ceph-osd-volume-size-gb", 0,
		"OSD PVC size in GB for --object-storage-mode=rook-ceph-pvc (0 = fleet default 200)")
	cmd.Flags().StringVar(&o.S3Hostname, "s3-hostname", "",
		"S3 endpoint hostname for the exposure layer (default: s3.<domain>)")
	cmd.Flags().BoolVar(&o.NoS3Exposure, "no-s3-exposure", false,
		"Skip the S3 exposure layer (Certificate + HTTPRoute) — cluster-internal S3 only")

	// --- Addons ---
	// NOTE: not yet wired into apply — passing --addon currently fails
	// closed (ErrAddonsNotImplemented). Flag + registry retained so the
	// surface is stable for when the addon engine slice ships.
	cmd.Flags().StringSliceVar(&addonFlags, "addon", nil,
		"Enable an addon (RESERVED — not yet implemented; repeatable; one of: metallb, sso-google, stripe-billing, velero)")

	// --- Behaviour gates ---
	cmd.Flags().BoolVar(&o.AllowDNSNotReady, "allow-dns-not-ready", false,
		"Proceed even when M1-T03 DNS probes fail (certs will land Pending until DNS is wired)")
	cmd.Flags().BoolVar(&o.AllowNoKubevirtEligible, "allow-no-kubevirt-eligible", false,
		"Proceed even when the target cluster reports 0 NFD kubevirt-eligible nodes (VM workloads will fail to schedule until at least one node exposes /dev/kvm)")
	cmd.Flags().BoolVar(&o.AllowUnpinnedAdopt, "allow-unpinned-adopt", false,
		"With --mode=adopt: proceed even when pre-existing components aren't version-pinned to their live versions (RISKY — Flux's first reconcile may upgrade/restart them; run `bootstrap adopt --pin-versions` first instead)")
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

// layeredAdoptEnv presents the effective cluster-config.env to the
// adopt gate: `--set KEY=VALUE` overrides win, then the on-disk file.
// Satisfies adopt.EnvReader.
type layeredAdoptEnv struct {
	overlay map[string]string
	base    adopt.EnvReader // *config.Env, or nil when not scaffolded yet
}

func (l layeredAdoptEnv) GetOr(key, fallback string) string {
	if v, ok := l.overlay[key]; ok {
		return v
	}
	if l.base != nil {
		return l.base.GetOr(key, fallback)
	}
	return fallback
}

// effectiveAdoptEnv builds the env the adopt gate compares against the
// live cluster: the cluster's on-disk cluster-config.env (if it exists)
// with the operator's `--set` overrides layered on top — i.e. exactly
// what the apply will write. The bool return is whether a fleet overlay
// (clusters/<name>/cluster-config.env) exists; false routes the gate to
// the "scaffold first — adopt needs an existing overlay" boundary
// message instead of the circular "run adopt --pin-versions" (which
// would itself have nowhere to write).
func effectiveAdoptEnv(o *clusterinit.InitOptions, out io.Writer) (adopt.EnvReader, bool) {
	le := layeredAdoptEnv{overlay: o.Sets}
	env, _, err := loadClusterEnv(o.Repo, o.Name)
	switch {
	case err == nil:
		le.base = env
		return le, true
	case errors.Is(err, errClusterNotInFleet):
		// No overlay yet — the boundary case. The gate emits the
		// scaffold-first guidance; here just note the state.
		fmt.Fprintf(out, "[adopt] no fleet overlay for %q yet — treating all component versions as unpinned.\n", o.Name)
		return le, false
	default:
		// A different failure (repo unreadable, env parse error). Still
		// treat as unpinned but surface the underlying error.
		fmt.Fprintf(out, "[adopt] could not read cluster-config.env for %q (%v) — treating all component versions as unpinned.\n", o.Name, err)
		return le, false
	}
}

// runInitAdoptWizardStep runs the live adopt drift-preview during the
// interactive wizard (Mode==adopt) and — when there's writable drift —
// offers to write the version pins inline (the same commit/push
// transaction as `adopt --pin-versions --yes`) so Flux adopts each
// component in place. Declining is safe: the CheckAdoptPinned gate still
// runs during Apply and fails closed unless --allow-unpinned-adopt.
//
// Interactive (huh.Confirm) + a fleet mutation on "yes" — this glue is
// exercised via manual TTY validation; the pieces it composes are
// unit-tested (initform.BuildAdoptPreview, adopt.PinVersions,
// applyConfigSet's git transaction).
func runInitAdoptWizardStep(cmd *cobra.Command, out io.Writer, o *clusterinit.InitOptions) error {
	session, err := bootstrap.NewSession(bootstrap.Options{FleetRepoPath: o.Repo})
	if session != nil {
		defer session.Close()
	}
	if err != nil || session.K8s == nil {
		fmt.Fprintln(out, "[adopt] live preview skipped (no cluster client) — the adopt gate still runs during apply.")
		return nil
	}

	// On-disk overlay (tolerate not-found → the no-overlay boundary).
	env, repoRoot, lerr := loadClusterEnv(o.Repo, o.Name)
	overlayExists := lerr == nil
	envReader := layeredAdoptEnv{overlay: o.Sets}
	if overlayExists {
		envReader.base = env
	}

	res, err := adopt.PinVersions(cmd.Context(), session.K8s, envReader, adopt.PinOptions{})
	if err != nil {
		return fmt.Errorf("adopt preview: %w", err)
	}
	pv := initform.BuildAdoptPreview(o.Name, res, overlayExists)
	fmt.Fprintln(out, pv.Render())

	// Only offer to write when there's actionable, writable drift (overlay
	// exists + pending pins). No-overlay / undetected-only / already-safe
	// all short-circuit — the gate (or the preview text) covers them.
	if !pv.NeedsPinning() {
		return nil
	}

	writeNow := false
	if err := huh.NewConfirm().
		Title("Write these version pins now?").
		Description("commits + pushes clusters/" + o.Name + "/cluster-config.env so Flux adopts each component in place (no upgrade/restart)").
		Affirmative("Write pins").Negative("No — pin later / bypass").
		Value(&writeNow).Run(); err != nil {
		return fmt.Errorf("init wizard adopt: %w", err)
	}
	if !writeNow {
		fmt.Fprintln(out, "[adopt] pins not written — the adopt gate will fail closed at apply unless you pin first or pass --allow-unpinned-adopt.")
		return nil
	}

	kvs := make([]fleetconfig.KV, 0, len(res.Pins))
	for _, p := range res.Pins {
		kvs = append(kvs, fleetconfig.KV{Key: p.VersionKey, Value: p.Live})
	}
	changes, err := fleetconfig.Plan(env, kvs, true)
	if err != nil {
		return fmt.Errorf("init wizard adopt: %w", err)
	}
	token := ""
	if !o.NoPush {
		token = resolveGitHubToken(o, out)
	}
	return applyConfigSet(cmd, out, repoRoot, env, changes, o.Name, o.NoPush, token, string(o.Provider))
}

// runApplyEngine is the shared tail that both apply paths converge
// on. Builds a `bootstrap.Session` for the adapters, resolves the
// GitHub token if --github-token wasn't passed, then calls
// `clusterinit.Apply`.
func runApplyEngine(ctx context.Context, out io.Writer, o *clusterinit.InitOptions, plan *clusterinit.Plan) error {
	// GitLab fail-closed guard retired — the coordinated
	// `kube-dc-fleet` PR made `flux-install.sh` provider-aware.
	// Provider info now flows through `ApplyOptions` (see
	// `runFluxInstall`).

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

	// M4-T07 auto-install prereqs. Runs FIRST — before DNS / NFD /
	// create-repo / Apply — so a missing binary surfaces before we
	// commit + push. Gated on `!--no-install-prereqs`. Consent
	// piggybacks on `--yes` (interactive prompt in the middle of a
	// long apply flow would be a UX regression; if tools are
	// missing, the operator either passed --yes (auto-install) or
	// gets a clean error telling them to run `bootstrap
	// install-prereqs` first). No-op contract preserved — 0
	// missing → no ScriptRunner call, cheap.
	if !o.NoInstallPrereqs {
		if _, err := clusterinit.InstallPrereqs(ctx, clusterinit.InstallPrereqsOptions{
			Runner: session.Scripts,
			Assume: o.Yes,
			Out:    out,
		}); err != nil {
			// Consent-required maps to a helpful message rather than
			// a bare error. Structural failures bubble as-is.
			if errors.Is(err, clusterinit.ErrInstallPrereqsConsentRequired) {
				return fmt.Errorf("apply: %w; re-run with --yes to auto-install OR pre-install via `kube-dc bootstrap install-prereqs`", err)
			}
			return fmt.Errorf("apply: %w", err)
		}
	}

	// M4-T08: DNS verification gate. Runs BEFORE the token
	// resolution / Apply so a failure surfaces as fast as possible,
	// before any disk-mutating step or `gh auth token` subshell.
	// Reads the same DNSClient the doctor probes use. The gate
	// ALWAYS runs; `--allow-dns-not-ready` doesn't skip it — it
	// DOWNGRADES a failed probe to a warning (with the record-set
	// still printed so the operator can wire the records later)
	// and lets Apply proceed. Success (probes report Installed)
	// takes precedence over the flag either way. See
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

	// Item 5: --mode=adopt safety gate. Consumes the resolved adopt plan
	// (the pinned cluster-config.env) rather than silently re-detecting:
	// re-runs adopt.PinVersions against the live cluster + the effective
	// env and FAILS CLOSED when a pre-existing component would drift
	// (Flux's first reconcile would upgrade/restart it) or is undetected.
	// Read-only; only runs in adopt mode. Escape: --allow-unpinned-adopt.
	if o.Mode == clusterinit.ModeAdopt {
		adoptEnv, overlayExists := effectiveAdoptEnv(o, out)
		if err := clusterinit.CheckAdoptPinned(ctx, clusterinit.AdoptGateOptions{
			Inspector:      session.K8s,
			Env:            adoptEnv,
			Allow:          o.AllowUnpinnedAdopt,
			OverlayMissing: !overlayExists,
			ClusterName:    o.Name,
			Out:            out,
		}); err != nil {
			return err
		}
	}

	// Findings 17/17b (single-IP NAT): resolve the IP with which
	// external traffic actually ARRIVES at the node. Behind a 1:1
	// NAT (kube-dc FIP, EC2 elastic IP, floating IPs) the public IP
	// is never a local address — NODE_EXTERNAL_IP must be the node's
	// primary IP or the whole 80/443 front door silently RSTs, and
	// the 6443 passthrough listener must be dropped (see natgate.go).
	// Runs AFTER the DNS gate (which correctly probes the PUBLIC IP)
	// and before Apply (which scaffolds the arriving IP). Detection
	// failure fails OPEN with a warning — behavior without --ssh-host
	// is unchanged.
	applyNodeIP := o.NodeExternalIP
	singleIPNAT := false
	if o.SSHHost != "" && !o.NoSSH {
		if sshClient, err := bootstrap.NewSSHOnly(); err != nil {
			fmt.Fprintf(out, "[apply] WARNING: NAT-topology probe skipped (ssh adapter: %v)\n", err)
		} else if arriving, nat, err := clusterinit.DetectArrivingIP(ctx, clusterinit.ArrivingIPOptions{
			SSH:      sshClient,
			Host:     parseSSHHostArg(o.SSHHost),
			PublicIP: o.NodeExternalIP,
			Out:      out,
		}); err != nil {
			fmt.Fprintf(out, "[apply] WARNING: NAT-topology probe failed (%v) — keeping NODE_EXTERNAL_IP=%s\n", err, o.NodeExternalIP)
		} else if nat {
			applyNodeIP = arriving
			singleIPNAT = true
			fmt.Fprintf(out, "[apply] single-IP NAT detected: %s is not bound on the node; NODE_EXTERNAL_IP=%s (arriving IP), 6443 passthrough listener will be dropped\n",
				o.NodeExternalIP, arriving)
		}
	}

	// --no-push uses Git.Commit (no push), so a GitHub token would
	// be ignored. Skip the resolution entirely to avoid an
	// unnecessary `gh auth token` subshell + log noise (M4-T12
	// review-pass — P3).
	var token string
	if !o.NoPush {
		token = resolveGitHubToken(o, out)
	}

	// M4-T05: auto-create the remote GitHub repo when the operator
	// opted into new-repo mode. Runs BEFORE Apply so the remote
	// exists by the time the commit+push phase fires. Idempotent
	// (422 "already exists" is success), so re-running after a
	// downstream failure doesn't create a mess. `--no-create-repo`
	// is the escape hatch — operators whose org manages repo
	// creation externally skip this step entirely.
	//
	// Skipped on `--no-push` too: without a push, there's no reason
	// to have created the remote in the first place, and the token
	// isn't resolved on that path.
	if plan.FleetMode == clusterinit.FleetNewRepo && !o.NoCreateRepo && !o.NoPush {
		if err := clusterinit.CreateRemoteRepo(ctx, clusterinit.CreateRepoOptions{
			Provider: o.Provider,
			Owner:    o.GitHubOwner,
			Name:     o.GitHubRepo,
			Token:    token,
			Out:      out,
		}); err != nil {
			return err
		}
	}

	// E2E finding 5 + review P1 (2026-07-04): ensure `origin` points at
	// the canonical remote for EVERY new-repo apply — including
	// --no-create-repo (org pre-created the repo), which previously
	// skipped this entirely and died in commit-push with "remote not
	// found". A pre-existing origin pointing elsewhere (fleet-starter
	// template remote) is corrected with set-url, never silently kept:
	// pushing the bootstrap commit to the wrong remote is worse than
	// failing.
	if plan.FleetMode == clusterinit.FleetNewRepo && !o.NoPush {
		if err := ensureOriginRemote(ctx, out, o.Repo, o.Provider, o.GitHubOwner, o.GitHubRepo); err != nil {
			return err
		}
	}

	if err := clusterinit.Apply(ctx, clusterinit.ApplyOptions{
		Plan:           plan,
		FleetRepo:      o.Repo,
		NodeExternalIP: applyNodeIP,
		SingleIPNAT:    singleIPNAT,
		Sets:           o.Sets,
		NodeNICs:       o.NodeNICs,
		ObjectStorage:  o.ObjectStorage(),
		Runner:         session.Scripts,
		Git:            session.Git,
		GitHubToken:    token,
		Provider:       o.Provider,
		GitHubOwner:    o.GitHubOwner,
		GitHubRepo:     o.GitHubRepo,
		NoPush:         o.NoPush,
		Out:            out,
	}); err != nil {
		return err
	}

	// M4-T06 auto-fetch kubeconfig. Runs LAST — after flux-install
	// has succeeded — so the operator's kubeconfig points at a
	// cluster that's actually reconciling. Gated on `--ssh-host !=
	// "" && !--no-ssh` (the presence of --ssh-host is the operator's
	// explicit opt-in to the SSH auto-pull; --no-ssh is the escape
	// hatch for operators managing kubeconfig externally). Uses an
	// SSH-only session so a fresh laptop with no prior kubeconfig
	// works — the whole point of this step is to CREATE that
	// kubeconfig. Failure here is a WARNING not a hard error — the
	// cluster is up + reconciling; the operator can re-run
	// `bootstrap fetch-kubeconfig` manually if the auto-pull
	// couldn't reach the master.
	if o.SSHHost != "" && !o.NoSSH {
		if err := autoFetchKubeconfig(ctx, out, o); err != nil {
			fmt.Fprintf(out, "[apply] WARNING: auto-fetch kubeconfig failed: %v\n", err)
			fmt.Fprintf(out, "[apply]          re-run: kube-dc bootstrap fetch-kubeconfig %s --ssh-host %s --domain %s\n",
				o.Name, o.SSHHost, o.Domain)
		}
	}
	return nil
}

// autoFetchKubeconfig is the init-side wiring of the M4-T06
// engine. Kept as a small helper so the main runApplyEngine flow
// stays readable. Uses `bootstrap.NewSSHOnly()` (not the session's
// SSH port) so the SSH adapter is constructed even when the
// session had no kubeconfig at build time — matches the standalone
// `bootstrap fetch-kubeconfig` subcommand's shape.
func autoFetchKubeconfig(ctx context.Context, out io.Writer, o *clusterinit.InitOptions) error {
	sshClient, err := bootstrap.NewSSHOnly()
	if err != nil {
		return fmt.Errorf("build ssh adapter: %w", err)
	}
	cfg, err := clusterinit.FetchKubeconfig(ctx, clusterinit.FetchKubeconfigOptions{
		SSH:         sshClient,
		Host:        parseSSHHostArg(o.SSHHost),
		ClusterName: o.Name,
		Domain:      o.Domain,
		Out:         out,
	})
	if err != nil {
		return err
	}
	// Merge into the operator's local kubeconfig; setCurrent=true
	// on a fresh install so `kubectl` after `bootstrap init`
	// immediately targets the new cluster. Existing operators
	// re-running init would already have their context set;
	// setCurrent=true refreshes it, matching the "you just
	// installed this cluster" mental model.
	dest := clusterinit.DefaultKubeconfigPath()
	if err := clusterinit.MergeKubeconfig(dest, cfg, true); err != nil {
		return fmt.Errorf("merge kubeconfig into %s: %w", dest, err)
	}
	fmt.Fprintf(out, "[apply] kubeconfig merged into %s (current-context=%s)\n", dest, o.Name)
	return nil
}

// resolveGitHubToken returns the operator's PAT for the
// CommitAndPush step. Provider-aware (M4-T05 GitLab retire):
// dispatches to `gh auth token` for github, `glab auth status --show-token`
// for gitlab.
//
// Precedence (per provider):
//
//  1. `--github-token` flag value (explicit operator override).
//  2. Provider-specific CLI-auth subshell — picks up the operator's
//     existing `gh auth login` / `glab auth login` session.
//  3. Empty string — caller (Apply) surfaces the push-auth error
//     from the git adapter if a token was actually needed.
//
// Never logged. The fallback subshell output is captured into a
// local variable and trimmed; not echoed to the operator log.
//
// **Naming note**: the function is still `resolveGitHubToken` for
// backward-compat with the rest of the flow; the `--github-token`
// flag likewise. Both carry the token for the SELECTED provider,
// not necessarily GitHub. A future rename to `resolveRepoToken` /
// `--repo-token` may land in a bigger UX pass.
func resolveGitHubToken(o *clusterinit.InitOptions, out io.Writer) string {
	return resolveGitHubTokenWithExec(o, out, defaultTokenExec)
}

// tokenExecFn is the test seam over the provider CLI subshell.
// Production wires defaultTokenExec (exec.Command); tests inject a
// fake that returns canned stdout/err without forking.
type tokenExecFn func(binary string, args ...string) (stdout []byte, err error)

// defaultTokenExec runs the provider CLI + captures stdout;
// stderr is discarded (tokens must never appear in the operator's
// log; per-command messages come from the caller instead).
func defaultTokenExec(binary string, args ...string) ([]byte, error) {
	cmd := exec.Command(binary, args...)
	cmd.Stderr = io.Discard
	return cmd.Output()
}

// resolveGitHubTokenWithExec is the testable form of
// resolveGitHubToken — same logic, but the CLI subshell is
// invoked through `execFn` so tests can seed responses.
func resolveGitHubTokenWithExec(o *clusterinit.InitOptions, out io.Writer, execFn tokenExecFn) string {
	if o.GitHubToken != "" {
		fmt.Fprintln(out, "[apply] using --github-token flag value")
		return o.GitHubToken
	}

	// Provider dispatch. Default (empty) → github.
	provider := o.Provider
	if provider == "" {
		provider = clusterinit.ProviderGitHub
	}
	var (
		binary string
		args   []string
		label  string
	)
	switch provider {
	case clusterinit.ProviderGitLab:
		binary, args, label = "glab", []string{"auth", "status", "--show-token"}, "glab auth status --show-token"
	default:
		binary, args, label = "gh", []string{"auth", "token"}, "gh auth token"
	}

	stdout, err := execFn(binary, args...)
	if err != nil {
		fmt.Fprintf(out, "[apply] no token resolved (%s failed; push may fail if remote requires auth)\n", label)
		return ""
	}
	token := strings.TrimSpace(string(stdout))
	// glab's `--show-token` prints "Token: <value>" whereas gh's
	// `auth token` prints just the value. Extract the trailing
	// token portion when the shape matches "Token: xxx".
	if provider == clusterinit.ProviderGitLab {
		token = extractGlabToken(token)
	}
	if token == "" {
		fmt.Fprintf(out, "[apply] no token resolved (%s returned empty)\n", label)
		return ""
	}
	fmt.Fprintf(out, "[apply] resolved token via %s\n", label)
	return token
}

// extractGlabToken parses glab's `auth status --show-token` output
// to isolate the token value. glab emits a multi-line status block
// with the token on a `Token: <value>` line; this helper walks the
// lines and returns the token, or empty when not found.
func extractGlabToken(body string) string {
	for _, line := range strings.Split(body, "\n") {
		trimmed := strings.TrimSpace(line)
		if strings.HasPrefix(trimmed, "Token:") {
			return strings.TrimSpace(strings.TrimPrefix(trimmed, "Token:"))
		}
	}
	// Fallback: if the whole body looks like a bare token (glab
	// versions have varied), return as-is.
	if !strings.Contains(body, "\n") && body != "" {
		return body
	}
	return ""
}

// discoverFleetState returns a best-effort snapshot of the fleet repo
// for plan generation. M4-T13 wired the full sibling walk + version
// inheritance + domain-collision check.
//
// **Two-level walk**: `discover.ListClusters` handles the nested
// `clusters/eu/dc1/` shape (CloudSigma's multi-region grouping) so
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

	// SOPS recipient count for the plan's "sops encrypt" step note.
	// Best-effort: a missing/unreadable/recipient-less .sops.yaml
	// leaves SOPSRecipients at 0, which `sopsRecipientsNote` renders
	// as the safe "single recipient (operator's local age key)"
	// default. When the fleet DOES carry the canonical multi-recipient
	// `age: 'k1,k2,k3'` block, the plan now reports the real count so
	// the operator sees the secret will be readable by every enrolled
	// keyholder — not just themselves. (E2E shakedown 2026-07-04: this
	// was silently 0 because discoverFleetState never read .sops.yaml.)
	if body, rerr := os.ReadFile(filepath.Join(o.Repo, ".sops.yaml")); rerr == nil {
		st.SOPSRecipients = len(clusterinit.ParseSOPSConfigRecipients(body))
	}
	return st
}

// bareInitInvocation reports whether `init` was launched with no
// init-local flags at all — the ONLY shape that enters the T6 wizard
// (reviewer P2: gating on "missing --name" let partial invocations
// like `init --provider=gitlab --fleet-mode=new-repo` enter the form,
// which then OVERWROTE the flags the operator had already passed —
// the wizard preseeds from defaults, not from every InitOptions
// field). Partial invocations fall through to flag validation, whose
// errors name exactly what's missing.
//
// The parent's persistent `--repo` is deliberately exempt: it's
// fleet-repo CONTEXT (same role as the KUBE_DC_FLEET env var, which
// can't be detected as a flag anyway) and the wizard preseeds it.
func bareInitInvocation(cmd *cobra.Command) bool {
	bare := true
	cmd.Flags().Visit(func(f *pflag.Flag) {
		if f.Name != "repo" {
			bare = false
		}
	})
	return bare
}

// siblingObjectStorageModeHint returns a one-line hint naming the
// template sibling's object-storage mode, or "" when it can't be
// determined (greenfield, no repo, no template, no overlay, unreadable
// file — all silent: the hint is best-effort sugar on the typed
// ErrObjectStorageModeRequired, never a second failure mode).
//
// Design call (2026-07-04): existing-fleet runs never INHERIT the
// mode — an explicit operator decision is forced — but the error
// should say what the fleet already runs, e.g.
// "template sibling eu/dc1 uses rook-ceph-pvc".
func siblingObjectStorageModeHint(o *clusterinit.InitOptions) string {
	if o.FleetMode != clusterinit.FleetExistingFleet || o.Repo == "" {
		return ""
	}
	fleet := discoverFleetState(o)
	if fleet.InheritanceTemplate == "" {
		return ""
	}
	body, err := os.ReadFile(filepath.Join(o.Repo, "clusters", fleet.InheritanceTemplate, "object-storage", "kustomization.yaml"))
	if err != nil {
		return ""
	}
	m := objectStorageModeRefRegex.FindSubmatch(body)
	if m == nil {
		return ""
	}
	return fmt.Sprintf("template sibling %s uses %s", fleet.InheritanceTemplate, string(m[1]))
}

// objectStorageModeRefRegex extracts the mode name from an overlay's
// resource line, e.g. `- ../../../infrastructure/object-storage/modes/rook-ceph-pvc`.
var objectStorageModeRefRegex = regexp.MustCompile(`object-storage/modes/([a-z0-9-]+)`)

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
		provider := string(o.Provider)
		if provider == "" {
			provider = "github (default)"
		}
		fmt.Fprintf(out, "Remote:       provider=%s owner=%s repo=%s token=%s\n",
			provider, o.GitHubOwner, o.GitHubRepo, tok)
	}
	fmt.Fprintf(out, "Object store: mode=%s osd-node=%s osd-size=%dGB\n", o.RookMode, o.RookOSDNode, o.RookOSDSizeGB)
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
		"fleet-mode", "provider", "github-owner", "github-repo", "github-token",
		"set", "node-nic",
		"object-storage-mode", "rook-mode", // rook-mode = deprecated alias, one release
		"rook-osd-node", "rook-osd-size-gb", "rook-osd-device",
		"ceph-node", "ceph-storage-class", "ceph-osd-count", "ceph-osd-volume-size-gb",
		"s3-hostname", "no-s3-exposure",
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

// ensureOriginRemote points the fleet repo's `origin` at the canonical
// provider URL. Missing → added; present but DIFFERENT → set-url with a
// visible notice (a stale fleet-starter/template remote must never
// receive the bootstrap push); matching → no-op. Kept independent of
// the create-repo step (review P1): --no-create-repo skips repo
// creation but still needs a correct origin before CommitAndPush.
func ensureOriginRemote(ctx context.Context, out io.Writer, repo string, provider clusterinit.Provider, owner, name string) error {
	host := "github.com"
	if provider == clusterinit.ProviderGitLab {
		host = "gitlab.com"
	}
	want := fmt.Sprintf("https://%s/%s/%s.git", host, owner, name)

	get := exec.CommandContext(ctx, "git", "-C", repo, "remote", "get-url", "origin")
	cur, err := get.Output()
	switch {
	case err != nil: // no origin configured
		add := exec.CommandContext(ctx, "git", "-C", repo, "remote", "add", "origin", want)
		if outB, aerr := add.CombinedOutput(); aerr != nil {
			return fmt.Errorf("init: add origin remote: %w (%s)", aerr, strings.TrimSpace(string(outB)))
		}
		fmt.Fprintf(out, "[apply] origin remote set to %s\n", want)
	case strings.TrimSpace(string(cur)) != want:
		// A global insteadOf rewrite or an embedded credential can
		// decorate what git reports, so don't compare raw strings —
		// compare the provider HOST plus the EXACT owner/repo path
		// (credentials + port + a trailing .git stripped). Review P1
		// (2026-07-05): the old `strings.Contains(cur, "/"+owner+"/"+name)`
		// ignored the host (a github.com remote satisfied a gitlab
		// apply) and false-positived on path prefixes (`/acme/fleet`
		// matched `/acme/fleet-starter`). Only a genuine host-or-repo
		// mismatch corrects; pushing the bootstrap commit to the wrong
		// remote is worse than failing.
		if remoteMatches(strings.TrimSpace(string(cur)), host, owner, name) {
			return nil
		}
		set := exec.CommandContext(ctx, "git", "-C", repo, "remote", "set-url", "origin", want)
		if outB, serr := set.CombinedOutput(); serr != nil {
			return fmt.Errorf("init: correct origin remote: %w (%s)", serr, strings.TrimSpace(string(outB)))
		}
		fmt.Fprintf(out, "[apply] origin remote CORRECTED %s → %s\n", strings.TrimSpace(string(cur)), want)
	}
	return nil
}

// remoteMatches reports whether git remote URL `raw` already points at
// host/owner/name — used by ensureOriginRemote to decide a decorated
// but-equivalent remote is a no-op. Host comparison is case-insensitive
// (hostnames are); path is compared exactly to owner/name.
func remoteMatches(raw, host, owner, name string) bool {
	rHost, rPath, ok := remoteIdentity(raw)
	if !ok {
		return false
	}
	return strings.EqualFold(rHost, host) && rPath == owner+"/"+name
}

// remoteIdentity extracts (host, "owner/repo") from a git remote URL,
// stripping embedded credentials, port, and a trailing ".git". Handles
// both the URL form (`scheme://[user[:pass]@]host[:port]/path`) and the
// scp-like SSH shorthand (`[user@]host:path`). ok=false when the URL
// can't be parsed into a host plus a non-empty path — callers treat
// that as "not a match" and correct the remote.
func remoteIdentity(raw string) (host, path string, ok bool) {
	raw = strings.TrimSpace(raw)
	if raw == "" {
		return "", "", false
	}
	if strings.Contains(raw, "://") {
		u, err := url.Parse(raw)
		if err != nil || u.Hostname() == "" {
			return "", "", false
		}
		host = u.Hostname() // drops userinfo + port
		path = u.Path
	} else {
		// scp-like: [user@]host:owner/repo(.git) — the first colon
		// separates host from path (git scp paths carry no colon).
		colon := strings.Index(raw, ":")
		if colon < 0 {
			return "", "", false
		}
		hostAndUser := raw[:colon]
		path = raw[colon+1:]
		if at := strings.LastIndex(hostAndUser, "@"); at >= 0 {
			host = hostAndUser[at+1:]
		} else {
			host = hostAndUser
		}
	}
	path = strings.Trim(path, "/")
	path = strings.TrimSuffix(path, ".git")
	if host == "" || path == "" {
		return "", "", false
	}
	return host, path, true
}
