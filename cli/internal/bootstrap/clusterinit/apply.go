package clusterinit

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"io"
	"io/fs"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"

	"github.com/go-git/go-git/v5/plumbing/transport"
	"strings"

	"gopkg.in/yaml.v3"
	"k8s.io/client-go/tools/clientcmd"

	"github.com/shalb/kube-dc/cli/internal/bootstrap/ports"
)

// M4-T12 — apply.
//
// **What this slice does**: composes T10 Scaffold + T11
// customInterfaces (already chained from T10) with the atomic Git
// transaction + flux-install.sh script so a single `Apply(ctx,
// opts)` call drives the new cluster overlay from "nothing on disk"
// to "Flux reconciling against the cluster".
//
// **Apply-plan verbatim contract** (T04+T09+T13 review-pass): the
// cobra layer is responsible for calling Validate +
// VerifyApplyPlanInput before invoking Apply. Apply itself trusts
// its `ApplyOptions` — passing an un-verified plan + mismatched
// `Sets`/`NodeNICs` is a programmer error, not a runtime one.
// Cobra tests cover the gate ordering.
//
// **Atomic Git transaction** (memory entry "Push failure rolls back
// the local commit. Never use HEAD~1; use pre-commit SHA captured
// via Head()"): the 5-phase flow is:
//
//   1. **Capture the starting checkout** (branch and commit together). It
//      is what the run publishes onto and fast-forwards from. It is not a
//      rollback target: nothing is written to the operator's checkout
//      before the publish step, so there is nothing to roll back.
//   2. **Clean-tree check** via `git.Diff` — refuse if the operator
//      has uncommitted changes elsewhere in the fleet repo
//      (typed `ErrDirtyWorkingTree`). Without this gate the
//      subsequent `wt.Add(".")` in CommitAndPush would silently
//      slurp the operator's unrelated work into the cluster's
//      bootstrap commit.
//   3. **Scaffold** runs (T10 script + T11 customInterfaces patch) in a
//      PRIVATE CHECKOUT of the starting commit (ports.GitIsolatedClient),
//      not in the operator's checkout. This is the only step that writes
//      files, and a failure in it leaves the operator's checkout as it was.
//   4. **Commit + push**: the commit is made in the private checkout and
//      that exact commit is pushed to the starting branch. A failed push
//      needs no rollback. Then the operator's checkout is fast-forwarded
//      to the pushed commit with Git's own merge, which refuses when a
//      local change overlaps.
//   5. **flux-install.sh** via ScriptRunner. Failure here is NOT
//      auto-rolled-back — the remote already has the commit so
//      `git reset --hard` locally wouldn't undo the published
//      state. The operator fixes forward (or manually reverts).

// ApplyOptions is the parameter bundle for Apply. The cobra layer
// builds it from validated InitOptions + a verified Plan +
// adapter handles via bootstrap.Session.
type ApplyOptions struct {
	// Plan is the previously-validated init plan. Apply trusts
	// this — see "verbatim contract" in the package doc.
	Plan *Plan

	// FleetRepo is the absolute path of the fleet repo on disk.
	FleetRepo string

	// NodeExternalIP is forwarded to Scaffold for the
	// add-cluster.sh positional arg. When SingleIPNAT is true this
	// already carries the ARRIVING (internal) IP resolved by
	// DetectArrivingIP, not the operator-declared public IP.
	NodeExternalIP string

	// NodeCIDR is the node's LAN prefix, resolved over SSH before Apply and
	// forwarded to Scaffold so INFRA_ATTACHMENT_ROUTES can be written. Empty
	// disables dual-homing for this cluster rather than guessing a prefix.
	NodeCIDR string

	// SingleIPNAT forwards the findings-17/17b topology detection to
	// Scaffold (step 8: 6443-listener removal patch).
	SingleIPNAT bool

	// ControlPlaneNodes are the real control-plane node NAMES, resolved from
	// the live cluster when one is reachable. Empty means "unknown", which
	// makes the :6443 set-intersection derivation stand down in favour of the
	// address heuristic. It must never be filled with KUBE_OVN_GW_NODES: the
	// gateway set is not the control-plane set (the reference cluster has a
	// gateway node that is an ordinary worker), and substituting it inverts
	// the listener decision for exactly those nodes.
	ControlPlaneNodes []string

	// Sets + NodeNICs are the operator's resolved --set / --node-nic
	// maps. They must match what was in the plan when it was built
	// (VerifyApplyPlanInput enforces this for --apply-plan; the
	// default-flow path builds Plan + opts from the SAME InitOptions
	// in one go, so they're guaranteed-coherent there).
	// RequestedIngressLayer is the operator's EXPLICIT --ingress-address-layer,
	// empty when they passed none. Deliberately NOT Plan.IngressAddressLayer,
	// which is the RESOLVED layer and therefore never empty: comparing the
	// resolved value would refuse every ordinary resume of a VIP cluster,
	// because "flag absent" resolves to "none" and looks like a change request.
	RequestedIngressLayer string

	Sets     map[string]string
	NodeNICs map[string]string

	// ObjectStorage carries the OS-1 mode + companions for the OS-2
	// scaffold writer (Scaffold step 7).
	ObjectStorage ObjectStorageSpec

	// VMStorage carries the VM root-disk mode + golden subset for the
	// scaffold writer (Scaffold step 9). See vmstorage.go.
	VMStorage VMStorageSpec

	// ImageAccel wires the image-acceleration trio (Scaffold step 9b).
	ImageAccel ImageAccelSpec

	// WildcardTLS is the validated byo-wildcard material (Scaffold step 11).
	// DNS01Route53 is the validated dns01 solver config (step 11b).
	// nil = acme mode. Loaded by the cobra layer before Apply starts.
	WildcardTLS  *WildcardTLSMaterial
	DNS01Route53 *DNS01Route53Material
	// DNS01Cloudflare is the validated Cloudflare DNS-01 solver config (step
	// 11c). nil = not requested.
	DNS01Cloudflare *DNS01CloudflareMaterial

	// TrustedCA is validated public CA material (Scaffold step 12).
	TrustedCA *TrustedCAMaterial

	// PAYG is the operator's --payg (Scaffold step 13). Every resume checks
	// the PAYG pieces an overlay carries (identity, billing rule, login,
	// layer, role patch); --payg only authorizes completing a partial setup
	// (CheckPAYGOnResume).
	PAYG bool
	// PAYGManagedFees is the operator's --payg-managed-fees (Scaffold step
	// 13b). A resume completes a missing, suspended layer.
	PAYGManagedFees bool

	// GPU carries the validated, non-secret accelerator fleet contract.
	GPU GPUConfig

	// KubeconfigPath is forwarded to Scaffold so add-cluster.sh's discovery can
	// read the cluster. Set it only to a kubeconfig VERIFIED to target this
	// cluster; empty skips the pass-through (script falls back to its default).
	KubeconfigPath string

	// Runner executes scripts (add-cluster.sh, flux-install.sh).
	// Real flow uses the script adapter; tests use a fake.
	Runner ports.ScriptRunner

	// Git is the GitClient for the atomic commit + push transaction.
	Git ports.GitClient

	// GitHubToken is the PAT used by CommitAndPush + flux-install.sh.
	// Empty for --no-push paths or genuinely-public repos (rare in
	// this product). Never logged. Despite the name, carries the
	// token for the SELECTED Provider — see Provider field below.
	GitHubToken string

	// Provider selects the remote-repo hosting service for the
	// flux-install.sh dispatch. Empty defaults to GitHub. Feeds the
	// `KUBE_DC_PROVIDER` env var passed to the fleet script (which
	// dispatches `flux bootstrap github` vs `flux bootstrap gitlab`
	// accordingly).
	Provider     Provider
	ProviderHost string

	// GitHubOwner / GitHubRepo are the fleet-repo coordinates on
	// the selected provider — piped through as `GITHUB_OWNER` /
	// `GITHUB_REPO` env vars to `flux-install.sh` (env var names
	// kept for backward compat; the fleet script reads them
	// regardless of provider).
	GitHubOwner string
	GitHubRepo  string

	// SiteComponents attach Fleet-repository Kustomize Components to
	// generated Flux layers (KUBE_DC_INIT_SITE_COMPONENTS).
	SiteComponents []SiteComponent

	// GitHost is the Fleet remote's host. For a self-managed GitLab it reaches
	// flux-install.sh as GITLAB_HOST, which `flux bootstrap gitlab` needs.
	GitHost string

	// NoPush skips the push step. The local commit still lands;
	// flux-install is also skipped because Flux can't reconcile
	// from a non-pushed commit.
	NoPush bool

	// K8s, when set, lets Apply label the front-door ingress nodes before the overlay
	// is committed (StepIngressNodes). The generated overlay selects the host-bind
	// component, whose nodeSelector is INGRESS_NODE_LABEL, so without the label every
	// Envoy replica is unschedulable and a brand-new cluster has no front door while
	// every manifest is correct and Flux is green.
	//
	// Nil means the step is skipped — and it is deliberately skipped for --no-push,
	// which produces a local-only overlay and must not mutate a live cluster.
	K8s ports.K8sClient

	// Out is the operator-facing log writer. nil = ioutil.Discard.
	Out io.Writer

	// Reporter observes milestone lifecycle (Scaffold / commit-push /
	// flux-install). nil ⇒ NopReporter, in which case behavior is
	// identical to the pre-reporter engine (detail still goes to Out).
	// The install-run TUI supplies one to drive its live checklist.
	Reporter StepReporter

	// Prepared publishes the exact reviewed ciphertext instead of rerendering.
	// This path requires an abortable executor and retains ambiguous Git work.
	Prepared *PreparedScaffold
}

// --- Errors ---

// ErrDirtyWorkingTree is returned when the fleet repo has
// uncommitted changes at Apply time. The clean-tree gate prevents
// CommitAndPush's `wt.Add(".")` from accidentally including
// operator work-in-progress in the cluster's bootstrap commit.
var ErrDirtyWorkingTree = errors.New("init: fleet repo has uncommitted changes; commit or stash before --apply")

// ErrCheckoutMoved means the fleet checkout left the branch or commit this
// run started from: init does not publish onto it.
var ErrCheckoutMoved = errors.New("init: the fleet checkout changed during this run")

// ErrCheckoutNotAdvanced means the bootstrap commit is on the remote but the
// local checkout could not be fast-forwarded to it. Nothing local was changed.
var ErrCheckoutNotAdvanced = errors.New("init: the fleet checkout was not fast-forwarded to the pushed commit")

// ErrApplyFluxInstallFailed is returned when bootstrap/flux-install.sh
// exits non-zero. The commit has already been pushed at that point
// — operator fixes forward rather than auto-rolling-back the
// published state.
var ErrApplyFluxInstallFailed = errors.New("init: flux-install.sh failed AFTER successful commit+push")

// ErrApplyMissingDependency surfaces when Apply is called with a
// nil Plan / Runner / Git / empty FleetRepo / NodeExternalIP.
// Cobra wires every required adapter, so seeing this in production
// is a programmer error.
var ErrApplyMissingDependency = errors.New("init: apply missing required dependency")

// --- Engine ---

// Apply orchestrates the full bootstrap apply: scaffold + commit +
// push + flux-install. See the package doc for the canonical
// 5-phase flow + rollback semantics.
func Apply(ctx context.Context, opts ApplyOptions) error {
	if err := validateApplyOptions(opts); err != nil {
		return err
	}
	if opts.Prepared != nil {
		return applyPrepared(ctx, opts)
	}
	if _, guarded := opts.Reporter.(StepExecutor); guarded {
		return fmt.Errorf("guarded apply requires the exact prepared scaffold")
	}
	// One fleet directory for everything that follows, the provider review
	// of the checkout included. The path is resolved once, the way the kernel
	// resolves it; the result has no link and no ".." left in it, so no
	// later reader (Git itself, the Git library and its provider binding,
	// the script runner, the kubeconfig check) can take it for another
	// directory by reading the spelling its own way.
	if physical, err := physicalDir(opts.FleetRepo); err != nil {
		return fmt.Errorf("apply: resolve the fleet repository %s: %w", opts.FleetRepo, err)
	} else if physical != opts.FleetRepo {
		if rebase, ok := opts.Runner.(ports.FleetScriptRunner); ok {
			opts.Runner = rebase.WithFleetRepo(physical)
		}
		opts.FleetRepo = physical
	}
	if !opts.NoPush {
		if err := CheckProviderHostSupport(opts.FleetRepo, opts.Provider, opts.ProviderHost); err != nil {
			return err
		}
		if binder, ok := opts.Git.(ports.GitProviderBinder); ok {
			host, err := applyProviderHost(ctx, opts)
			if err != nil {
				return err
			}
			opts.Git, err = binder.ForProvider(ctx, opts.FleetRepo, host)
			if err != nil {
				return err
			}
		}
	}
	out := opts.Out
	if out == nil {
		out = io.Discard
	}
	rep := reporterOrNop(opts.Reporter)

	// Phase 1: capture the starting checkout: branch and commit together.
	// The run generates and commits in a private checkout of that commit
	// (Phase 3), publishes only to that branch, and fast-forwards this
	// checkout only while it is still on that branch at that commit.
	bound, ok := opts.Git.(ports.GitIsolatedClient)
	if !ok {
		return fmt.Errorf("apply: the git client cannot work in a private checkout of the fleet repository")
	}
	fleet := opts.FleetRepo
	// A run killed in the middle of its fast-forward left the branch at the
	// new commit and the files at the old one: finish that first, or the
	// clean-tree gate below would report the run's own files as changes.
	if resumed, rerr := bound.ResumeAdvance(ctx, fleet); rerr != nil {
		return fmt.Errorf("apply: %w", rerr)
	} else if resumed {
		fmt.Fprintf(out, "[apply] finished the fast-forward an interrupted run left half-way\n")
	}
	start, err := bound.Checkout(ctx, fleet)
	if err != nil {
		return fmt.Errorf("apply: capture pre-commit SHA: %w", err)
	}
	if start.Branch == "" {
		return fmt.Errorf("apply: %s has a detached HEAD; check out the branch Flux reconciles, then re-run", fleet)
	}
	preSHA := start.SHA
	// onStart refuses a checkout that left the starting branch or commit.
	onStart := func() error {
		cur, err := bound.Checkout(ctx, fleet)
		if err != nil {
			return fmt.Errorf("%w: read the checkout: %v", ErrCheckoutMoved, err)
		}
		if cur.Branch != start.Branch {
			return fmt.Errorf("%w: it switched from %s to %s", ErrCheckoutMoved, branchLabel(start.Branch), branchLabel(cur.Branch))
		}
		if cur.SHA != preSHA {
			return fmt.Errorf("%w: %s moved from %s to %s", ErrCheckoutMoved, branchLabel(cur.Branch), preSHA, cur.SHA)
		}
		return nil
	}

	// Phase 2: clean-tree gate.
	diff, err := opts.Git.Diff(ctx, opts.FleetRepo)
	if err != nil {
		return fmt.Errorf("apply: diff %s: %w", opts.FleetRepo, err)
	}
	if len(diff.Files) > 0 {
		// Surface the dirty file paths so the operator knows what
		// to stash/commit before re-running. ports.Diff.Files
		// includes untracked files (per port godoc) so this also
		// catches stray .swp / editor backup files.
		paths := make([]string, 0, len(diff.Files))
		for _, f := range diff.Files {
			paths = append(paths, f.Path)
		}
		return fmt.Errorf("%w (dirty paths: %v)", ErrDirtyWorkingTree, paths)
	}

	// Phase 2b: label the front-door ingress nodes.
	//
	// Deliberately BEFORE scaffold/commit: the overlay this run is about to write
	// selects the host-bind component, which places Envoy by INGRESS_NODE_LABEL. If the
	// label cannot be established, committing that overlay hands Flux a front door that
	// can never schedule. Failing here leaves the fleet repo untouched.
	//
	// Skipped for --no-push (local-only overlay must not mutate a cluster) and when no
	// K8s client was supplied.
	if opts.K8s == nil {
		rep.Skip(StepIngressNodes, "no cluster connection")
	} else if opts.NoPush {
		rep.Skip(StepIngressNodes, "--no-push: not mutating a live cluster")
	} else {
		rep.Start(StepIngressNodes)
		ingressNodes := opts.Plan.IngressNodes
		if err := EnsureIngressNodeLabels(ctx, opts.K8s, ingressNodes, opts.Plan.IngressNodeLabel, out); err != nil {
			rep.Done(StepIngressNodes, err)
			return fmt.Errorf("apply: label ingress nodes: %w", err)
		}
		// Same step, same node set: derive the prefix the platform NetworkPolicies must
		// admit. Envoy is hostNetwork, so it reaches OpenBao and the Flux UI with a NODE
		// address that no namespaceSelector can match; without this value those upstreams
		// are denied. Nothing in the installer computed it, and the scaffold could only
		// discover it when handed a kubeconfig it is not normally given — so a normal
		// `bootstrap init` left it empty.
		//
		// An explicit --set INGRESS_HOST_CIDR always wins: an operator who knows their
		// nodes source from a different NIC than their InternalIP must be able to say so.
		if _, pinned := opts.Sets["INGRESS_HOST_CIDR"]; pinned {
			fmt.Fprintf(out, "  ingress host CIDR: %s (operator-supplied, not derived)\n",
				opts.Sets["INGRESS_HOST_CIDR"])
		} else if cidr, addrs, err := IngressHostCIDRFor(ctx, opts.K8s, ingressNodes); err != nil {
			// Do NOT fail the install: the value is also validated by the fleet render
			// gate and by frontdoor-check.sh preflight, and refusing here would block an
			// install for a cluster whose addressing simply cannot be expressed as one
			// prefix (dual-stack). Say exactly what to do instead.
			fmt.Fprintf(out, "  WARNING: could not derive INGRESS_HOST_CIDR: %v\n", err)
			fmt.Fprintf(out, "           Set it explicitly (--set INGRESS_HOST_CIDR=...) to the prefix\n")
			fmt.Fprintf(out, "           carrying the ingress nodes' upstream source addresses, or the\n")
			fmt.Fprintf(out, "           platform NetworkPolicies will DENY the host-bound Envoy and\n")
			fmt.Fprintf(out, "           OpenBao and the Flux UI will answer 503.\n")
		} else {
			opts.Plan.IngressHostCIDR = cidr
			fmt.Fprintf(out, "  ingress host CIDR: %s (derived from %d ingress node address(es): %s)\n",
				cidr, len(addrs), strings.Join(addrs, ", "))
			if cidrIsWiderThanNeeded(cidr, len(addrs)) {
				fmt.Fprintf(out, "           note: that prefix is wider than those addresses need — it is the\n")
				fmt.Fprintf(out, "           smallest SINGLE prefix covering them, and the policies render one\n")
				fmt.Fprintf(out, "           ipBlock. Override with --set INGRESS_HOST_CIDR=... to narrow it.\n")
			}
		}
		rep.Done(StepIngressNodes, nil)
	}

	// Phase 3: scaffold (T10 script + T11 customInterfaces).
	//
	// PRIVATE CHECKOUT: everything from here to the publish step (Phase 4)
	// runs in a private worktree of the starting commit, not in the
	// operator's checkout. A failure anywhere in between leaves the
	// operator's checkout exactly as it was, with nothing to roll back; the
	// private checkout is simply discarded. That is also what makes a retry
	// after a failed init work in the same checkout.
	work, discard, err := bound.Isolate(ctx, fleet, preSHA)
	if err != nil {
		return fmt.Errorf("apply: %w", err)
	}
	// KNOWN LIMITS of "a failure before the push leaves the checkout as it
	// was". The guarantee is about the fleet repository's own files. It does
	// not extend to files that other programs are configured to write and
	// that the operator placed inside the checkout (git-ignored, or the
	// clean-tree gate would have refused them):
	//   - a credential cache: with HOME inside the checkout, an exec
	//     credential plugin started by the scripts' kubectl (the
	//     `kube-dc credential` plugin, for one) refreshes
	//     $HOME/.kube-dc/credentials there;
	//   - a Git trace file: GIT_TRACE*/GIT_TRACE2* naming a file inside the
	//     checkout is honoured by every Git command of the run.
	// Both are the operator's own settings doing what they say.
	//
	// Cleanup is registered before anything else can fail: from here on,
	// every return discards the private checkout and puts the process
	// environment back.
	var restoreEnv []func()
	discarded := false
	discardWork := func() error {
		if discarded {
			return nil
		}
		discarded = true
		for i := len(restoreEnv) - 1; i >= 0; i-- {
			restoreEnv[i]()
		}
		derr := discard()
		if derr != nil {
			fmt.Fprintf(out, "[apply] WARNING: %v\n", derr)
		}
		return derr
	}
	defer func() { _ = discardWork() }()
	setEnv := func(name, value string) {
		old, had := os.LookupEnv(name)
		_ = os.Setenv(name, value)
		restoreEnv = append(restoreEnv, func() {
			if had {
				_ = os.Setenv(name, old)
			} else {
				_ = os.Unsetenv(name)
			}
		})
	}
	// While the private checkout exists, TMPDIR is the run's own scratch
	// directory: the fleet scripts and sops create temp files there, and a
	// killed subprocess leaves them there, not wherever the operator's
	// TMPDIR points (which may be inside the fleet checkout).
	kubeconfig := opts.KubeconfigPath
	if work != fleet {
		runTemp := ports.RunTempDir(work)
		if err := os.MkdirAll(runTemp, 0o700); err != nil {
			return fmt.Errorf("apply: the run's scratch directory: %w", err)
		}
		setEnv("TMPDIR", runTemp)
		// kubectl writes to the kubeconfig it is given (a refreshed OIDC
		// token, for one). A kubeconfig that lives inside the fleet checkout
		// must not be changed by a run that fails, so the scripts do not
		// read one from there.
		discovery, env, kerr := isolateKubeconfigs(fleet, runTemp, opts.Plan.ClusterName, opts.KubeconfigPath, os.Getenv("KUBECONFIG"))
		if kerr != nil {
			return fmt.Errorf("apply: %w", kerr)
		}
		kubeconfig = discovery
		setEnv("KUBECONFIG", env)
	}
	runner := opts.Runner
	if work != fleet {
		// A tracked symlink that leads out of the repository would carry a
		// writer out of the private checkout, possibly into the operator's.
		if err := refuseEscapingSymlinks(work); err != nil {
			return fmt.Errorf("apply: %w", err)
		}
		// And none at all where init writes and checks by path.
		if err := refuseManagedLinks(work, opts.Plan.ClusterName); err != nil {
			return fmt.Errorf("apply: %w", err)
		}
		rebase, ok := runner.(ports.FleetScriptRunner)
		if !ok {
			return fmt.Errorf("apply: the script runner cannot run the fleet scripts in a private checkout")
		}
		runner = rebase.WithFleetRepo(work)
		// Scripts and sops read the fleet's age key through its absolute
		// path. It is NOT linked or copied into the private checkout: a
		// path there that a tracked symlink points at would be written
		// through.
		defer exportFleetAgeKey(fleet)() // until Apply returns: flux-install reads it too
	}
	opts.FleetRepo = work // until Phase 4, the fleet repository IS the private checkout
	// tip is the last commit this run made in the private checkout.
	tip := preSHA
	commit := func(msg string, check func(ports.CommitTrees) error) (string, error) {
		// Before anything is staged: a private key that Git does not ignore
		// must not even reach the object store.
		if err := refuseUnignoredAgeKey(fleet, work); err != nil {
			return "", err
		}
		sha, err := bound.CommitIsolated(ctx, fleet, work, msg, tip, func(trees ports.CommitTrees) error {
			if err := ageKeyTreeCheck(trees.Candidate); err != nil {
				return err
			}
			if check != nil {
				return check(trees)
			}
			return nil
		})
		if err == nil {
			tip = sha
		}
		return sha, err
	}
	// commitStep commits a resume step's changes; none is not an error.
	commitStep := func(msg string) (string, bool, error) {
		sha, err := commit(msg, nil)
		if errors.Is(err, ports.ErrNothingToCommit) {
			return "", false, nil
		}
		return sha, err == nil, err
	}
	clusterDir := filepath.Join(opts.FleetRepo, "clusters", opts.Plan.ClusterName)
	resuming := false
	completePAYG := false // resume found a partial PAYG scaffold to finish
	var systemDNSIPs []string
	if opts.Plan.ManagedServicesMode == "on" && opts.SingleIPNAT {
		if opts.K8s == nil {
			return fmt.Errorf("apply: managed-services internal gateway requires a live cluster connection to resolve system DNS ingress nodes")
		}
		_, addresses, err := IngressHostCIDRFor(ctx, opts.K8s, opts.Plan.IngressNodes)
		if err != nil {
			return fmt.Errorf("apply: managed-services system DNS: %w", err)
		}
		systemDNSIPs = addresses
	}
	rep.Start(StepScaffold)
	// Set by the scaffold: the overlay it wrote references the registry-depot
	// credential, so the bootstrap commit must carry it (overlayCommitCheck).
	var credentialWired bool
	if err := Scaffold(ctx, ScaffoldOptions{
		Plan:                    opts.Plan,
		FleetRepo:               opts.FleetRepo,
		NodeExternalIP:          opts.NodeExternalIP,
		NodeCIDR:                opts.NodeCIDR,
		Sets:                    opts.Sets,
		NodeNICs:                opts.NodeNICs,
		ObjectStorage:           opts.ObjectStorage,
		VMStorage:               opts.VMStorage,
		ImageAccel:              opts.ImageAccel,
		WildcardTLS:             opts.WildcardTLS,
		DNS01Route53:            opts.DNS01Route53,
		DNS01Cloudflare:         opts.DNS01Cloudflare,
		TrustedCA:               opts.TrustedCA,
		PAYG:                    opts.PAYG,
		PAYGManagedFees:         opts.PAYGManagedFees,
		GPU:                     opts.GPU,
		SingleIPNAT:             opts.SingleIPNAT,
		SystemDNSIPs:            systemDNSIPs,
		ControlPlaneNodes:       opts.ControlPlaneNodes,
		KubeconfigPath:          kubeconfig,
		Runner:                  runner,
		Out:                     out,
		RegistryCredentialWired: &credentialWired,
	}); err != nil {
		if errors.Is(err, ErrScaffoldTargetExists) {
			// Before declaring a resume: a resume writes NOTHING, so an
			// operator asking for a different front door must be told, not
			// quietly ignored.
			if fdErr := CheckFrontDoorMatchesOverlay(opts.FleetRepo, opts.Plan.ClusterName, opts.RequestedIngressLayer); fdErr != nil {
				rep.Done(StepScaffold, fdErr)
				return fdErr
			}
			complete, pErr := CheckPAYGOnResume(opts.FleetRepo, opts.Plan.ClusterName, opts.Plan.Domain, opts.PAYG)
			if pErr != nil {
				rep.Done(StepScaffold, pErr)
				return pErr
			}
			completePAYG = complete
			// RESUME (retry/restart): the overlay already exists from a
			// prior apply. Rather than fail, skip re-scaffolding and
			// continue — the Phase-2 clean-tree gate already confirmed
			// nothing is uncommitted, so there's nothing new to commit
			// and flux-install is idempotent. This is what makes a
			// re-run of `init` resume a partially-completed install
			// instead of erroring. To force a fresh scaffold, edit or
			// remove clusters/<name>/ first.
			fmt.Fprintf(out, "[apply] cluster overlay clusters/%s already exists — resuming (skipping scaffold + commit; flux-install is idempotent)\n", opts.Plan.ClusterName)
			rep.Done(StepScaffold, nil)
			resuming = true
		} else {
			rep.Done(StepScaffold, err)
			// The partial scaffold (possibly a plaintext secret, if the SOPS
			// fallback fired) is in the private checkout, which is discarded
			// on return. Nothing was written to the operator's checkout.
			fmt.Fprintf(out, "[apply] scaffold failed; nothing was written to %s\n", fleet)
			return fmt.Errorf("apply: scaffold: %w", err)
		}
	} else {
		rep.Done(StepScaffold, nil)
	}

	// Phase 3b: TLS material against a RESUMED overlay. Skipping scaffold
	// wholesale made the DOCUMENTED rotation flow ("re-run init with the new
	// material") a silent no-op: apply reported success while the old
	// credential/certificate stayed in Git (codex review 2026-08-06, P1).
	// When the operator supplied TLS material, run just those writers — they
	// are content-aware idempotent (unchanged material → no writes, so a
	// plain resume stays a plain resume) — and commit whatever changed; the
	// resume push below carries it.
	rotated := false
	if resuming && (opts.WildcardTLS != nil || opts.DNS01Route53 != nil || opts.DNS01Cloudflare != nil) {
		if err := WriteWildcardTLS(opts.FleetRepo, opts.Plan.ClusterName, opts.Plan.Domain, opts.WildcardTLS, out); err != nil {
			return fmt.Errorf("apply: rotate wildcard-tls on resumed overlay: %w", err)
		}
		if err := WriteDNS01Route53(opts.FleetRepo, opts.Plan.ClusterName, opts.DNS01Route53, out); err != nil {
			return fmt.Errorf("apply: rotate dns01-route53 on resumed overlay: %w", err)
		}
		if err := WriteDNS01Cloudflare(opts.FleetRepo, opts.Plan.ClusterName, opts.Plan.Domain, opts.DNS01Cloudflare, out); err != nil {
			return fmt.Errorf("apply: rotate dns01-cloudflare on resumed overlay: %w", err)
		}
		{
			msg := fmt.Sprintf("chore(%s): rotate platform TLS material via kube-dc CLI", opts.Plan.ClusterName)
			sha, committed, cerr := commitStep(msg)
			if cerr != nil {
				return fmt.Errorf("apply: commit TLS rotation: %w", cerr)
			}
			if committed {
				rotated = true
				fmt.Fprintf(out, "[apply] TLS material rotated on existing overlay — commit=%s\n", sha)
			}
		}
	}

	// Phase 3c: a PAYG scaffold that was cut short (some of its pieces are in
	// the committed overlay, some are not). CheckPAYGOnResume already refused
	// a foreign identity or an overlay with no PAYG at all; here the missing
	// pieces are written, the whole contract is re-checked, and the result is
	// committed so the resume push carries it.
	if resuming && completePAYG {
		changed, err := CompletePAYGOnResume(opts.FleetRepo, opts.Plan.ClusterName, opts.Plan.Domain, out)
		if err != nil {
			return fmt.Errorf("apply: complete PAYG on resumed overlay: %w", err)
		}
		if changed {
			msg := fmt.Sprintf("chore(%s): complete PAYG wiring via kube-dc CLI", opts.Plan.ClusterName)
			sha, _, cerr := commitStep(msg)
			if cerr != nil {
				return fmt.Errorf("apply: commit PAYG completion: %w", cerr)
			}
			fmt.Fprintf(out, "[apply] PAYG wiring completed on existing overlay — commit=%s\n", sha)
			rotated = true
		}
	}
	// Phase 3d: the managed-fee layer asked of a resumed overlay. It needs the
	// overlay's PAYG wiring and is written suspended, so it cannot start a
	// collector or charge anything; the resume push carries the commit.
	if resuming && opts.PAYGManagedFees {
		changed, err := WritePAYGManagedFees(opts.FleetRepo, opts.Plan.ClusterName, true, out)
		if err != nil {
			return fmt.Errorf("apply: managed-fee layer on resumed overlay: %w", err)
		}
		if changed {
			msg := fmt.Sprintf("chore(%s): prepare managed-service fee evidence via kube-dc CLI", opts.Plan.ClusterName)
			sha, _, cerr := commitStep(msg)
			if cerr != nil {
				return fmt.Errorf("apply: commit the managed-fee layer: %w", cerr)
			}
			fmt.Fprintf(out, "[apply] managed-service fee layer prepared on existing overlay — commit=%s\n", sha)
			rotated = true
		}
	}

	// Phase 3d: site components. Written on a fresh scaffold (committed in
	// Phase 4) and re-applied idempotently on resume, where a change is
	// committed here so the resume push carries it.
	// A failure here needs no rollback: the files are in the private checkout.
	if !resuming {
		if err := WriteProductComponents(ctx, ProductComponentOptions{
			FleetRepo: opts.FleetRepo, ClusterName: opts.Plan.ClusterName,
			Sets: opts.Sets, TrustedCA: opts.TrustedCA, K8s: nodeLabelsOf(opts.K8s),
			PlannedManagers: opts.Plan.ManagerNodes, WildcardCertPEM: wildcardCertPEM(opts.WildcardTLS),
		}, out); err != nil {
			return fmt.Errorf("apply: product components: %w", err)
		}
	} else if strings.TrimSpace(opts.Sets[ExtraExcludeKey]) != "" {
		fmt.Fprintf(out, "[apply] resume: %s is applied only when the overlay is created; change clusters/%s/%s by hand\n",
			ExtraExcludeKey, opts.Plan.ClusterName, extNetExcludesDir)
	}
	if len(opts.SiteComponents) > 0 {
		if err := WriteSiteComponents(opts.FleetRepo, opts.Plan.ClusterName, opts.SiteComponents, out); err != nil {
			return fmt.Errorf("apply: %w", err)
		}
		if resuming {
			msg := fmt.Sprintf("chore(%s): attach site components via kube-dc CLI", opts.Plan.ClusterName)
			sha, committed, cerr := commitStep(msg)
			if cerr != nil {
				return fmt.Errorf("apply: commit site components: %w", cerr)
			}
			if committed {
				fmt.Fprintf(out, "[apply] site components attached to the existing overlay — commit=%s\n", sha)
				rotated = true
			}
		}
	}

	// Every generated overlay file must parse before it is committed: several
	// writers edit the same files line by line, and invalid YAML would only
	// surface later as a root Flux layer that never applies.
	if err := validateOverlayYAML(clusterDir); err != nil {
		return fmt.Errorf("apply: %w", err)
	}

	// Phase 4: publish. Until here nothing touched the operator's checkout.
	//
	//  1. Commit in the private checkout (a resume's commits already exist).
	//  2. Push exactly that commit to the starting branch. A failed push
	//     leaves the operator's checkout as it was: there is nothing to roll
	//     back, and a re-run starts clean.
	//  3. Fast-forward the operator's checkout to the pushed commit with
	//     Git's own merge, which refuses when a local change overlaps.
	//
	// On resume the overlay is already committed, but a prior --no-push run
	// or a failed push can leave the branch ahead of the remote Flux
	// reconciles, so the starting commit (or this run's resume commits) is
	// pushed in any case: a clean tree does not prove the remote has it.
	opts.FleetRepo = fleet
	if !resuming {
		rep.Start(StepCommitPush)
		msg := fmt.Sprintf("feat(%s): initial bootstrap via kube-dc CLI", opts.Plan.ClusterName)
		overlayCheck := overlayCommitCheck(opts.Plan.ClusterName, credentialWired)
		if _, cerr := commit(msg, func(trees ports.CommitTrees) error {
			if err := overlayCheck(trees); err != nil {
				return err
			}
			return encryptedFilesCheck(work, opts.Plan.ClusterName, trees.Candidate)
		}); cerr != nil {
			rep.Done(StepCommitPush, cerr)
			return fmt.Errorf("apply: commit-push: %w (nothing was changed in %s)", cerr, fleet)
		}
	} else if rotated || !opts.NoPush {
		rep.Start(StepCommitPush)
		// A resume publishes a commit this run did not check as a
		// candidate (or only checked for the key): check the exact tip now.
		tree, terr := bound.TreeOf(ctx, fleet, tip)
		if terr == nil {
			terr = ageKeyTreeCheck(tree)
		}
		if terr == nil {
			terr = overlayTreeCheck(opts.Plan.ClusterName, tree)
		}
		if terr == nil {
			// Every managed secret file of the tip, whether or not the
			// remote already has the commit (the history scan of the push
			// covers only what the remote lacks).
			terr = encryptedFilesCheck(work, opts.Plan.ClusterName, tree)
		}
		if terr != nil {
			rep.Done(StepCommitPush, terr)
			return fmt.Errorf("apply: the commit in %s must not be published: %w (nothing was pushed)", fleet, terr)
		}
	}
	if resuming && opts.NoPush && !rotated {
		rep.Skip(StepCommitPush, "overlay already committed; --no-push (local-only)")
		fmt.Fprintln(out, "[apply] resuming: overlay already committed (--no-push, local-only)")
	} else {
		at := ports.Checkout{Branch: start.Branch, SHA: tip}
		// Do not publish a commit this checkout could no longer fast-forward to.
		if merr := onStart(); merr != nil {
			rep.Done(StepCommitPush, merr)
			return fmt.Errorf("apply: %w; nothing was pushed, and nothing was changed in %s", merr, fleet)
		}
		if !opts.NoPush {
			// A push publishes every commit the remote does not have yet, not
			// only the tip's tree. So what is checked is exactly what this
			// push would newly publish to the destination it goes to
			// (publishCheck), and the adapter resolves that destination once
			// for the check and the push.
			check := publishCheck(opts.Plan.ClusterName)
			// flux-install is told which repository Flux reconciles
			// (GITHUB_OWNER / GITHUB_REPO). The push must go to that one: a
			// pushInsteadOf rule can send it to a fork.
			check.Destination = func(pushURL string) error {
				if err := pushDestinationMatches(pushURL, opts.GitHubOwner, opts.GitHubRepo); err != nil {
					return err
				}
				// An SSH URL with the right host name and path can still
				// reach another instance (another port): the provider's API
				// says which SSH address the project really has.
				if _, isSSH := parseSSHRemote(pushURL); !isSSH {
					return nil
				}
				host, herr := applyProviderHost(ctx, opts)
				if herr != nil {
					return herr
				}
				return sshDestinationIsProviderProject(ctx, opts.Provider, host, opts.GitHubOwner, opts.GitHubRepo, opts.GitHubToken, pushURL)
			}
			inspect := check.Check
			check.Check = func(files []ports.UnpublishedFile) error {
				if err := inspect(files); err != nil {
					return err
				}
				// Asking the remote took time: the checkout must still be
				// where the run started, right before anything is sent.
				return onStart()
			}
			if perr := bound.PushCommit(ctx, fleet, opts.GitHubToken, at, check); perr != nil {
				if errors.Is(perr, ports.ErrPublishRefused) {
					rep.Done(StepCommitPush, perr)
					return fmt.Errorf("apply: %w; nothing was pushed", perr)
				}
				if errors.Is(perr, ports.ErrPushUnconfirmed) {
					rep.Done(StepCommitPush, perr)
					return fmt.Errorf("apply: %w. Nothing was changed in %s. Before re-running init, check whether %s on the remote is at %s: if it is, run `git pull --ff-only` first, so the re-run resumes instead of generating a second commit",
						perr, fleet, branchLabel(start.Branch), tip)
				}
				rep.Done(StepCommitPush, perr)
				if resuming {
					return fmt.Errorf("apply: resume push (cannot confirm the remote Flux reconciles has the overlay; nothing was changed in %s): %w", fleet, perr)
				}
				return fmt.Errorf("apply: commit-push: %w (nothing was changed in %s). If the connection dropped rather than the remote refusing, the remote may have the commit: check whether %s there is at %s, and if so run `git pull --ff-only` before re-running init, so it resumes instead of generating a second commit; otherwise re-run init to retry",
					perr, fleet, branchLabel(start.Branch), tip)
			}
		}
		if tip != preSHA {
			if aerr := bound.Advance(ctx, fleet, start, tip); aerr != nil {
				rep.Done(StepCommitPush, aerr)
				if errors.Is(aerr, ports.ErrPartialCheckout) {
					return fmt.Errorf("apply: %w. The branch is back at %s; commit %s%s holds the complete files. Remove or restore the files named, then re-run init",
						aerr, preSHA, tip, pushedSuffix(opts.NoPush))
				}
				if opts.NoPush {
					return fmt.Errorf("apply: commit: %w (nothing was changed in %s)", aerr, fleet)
				}
				return fmt.Errorf("%w: %s is pushed to %s, but %s was left as it is: %v. Fix what is named, run `git pull --ff-only`, then re-run init to continue",
					ErrCheckoutNotAdvanced, tip, branchLabel(start.Branch), fleet, aerr)
			}
		}
		rep.Done(StepCommitPush, nil)
		if resuming {
			fmt.Fprintf(out, "[apply] resuming: overlay already committed — %s%s\n", tip, pushedSuffix(opts.NoPush))
		} else {
			fmt.Fprintf(out, "[apply] commit=%s%s\n", tip, pushedSuffix(opts.NoPush))
		}
	}

	// The private checkout (it may hold generated secrets) is not kept
	// around while Flux installs, and a run that cannot remove it stops here.
	if derr := discardWork(); derr != nil {
		return fmt.Errorf("apply: %w. The overlay is committed%s; remove that directory, then re-run init to continue", derr, pushedSuffix(opts.NoPush))
	}

	// Phase 5: flux-install.sh (skipped when NoPush — Flux needs a
	// pushed commit to reconcile from).
	if opts.NoPush {
		rep.Skip(StepFluxInstall, "--no-push: cluster overlay is local-only")
		fmt.Fprintln(out, "[apply] --no-push set; skipping flux-install (cluster overlay exists locally only)")
		return nil
	}
	rep.Start(StepFluxInstall)
	// flux-install.sh works in this checkout and reads its branch: it must be
	// the starting branch at the commit that was published, on every path
	// (a plain resume pushes without fast-forwarding anything).
	if cur, cerr := bound.Checkout(ctx, fleet); cerr != nil || cur.Branch != start.Branch || cur.SHA != tip {
		err := fmt.Errorf("%w: %s is published on %s, but this checkout is %s at %s (%v); check out %s at that commit, then re-run init to continue",
			ErrCheckoutMoved, tip, branchLabel(start.Branch), branchLabel(cur.Branch), cur.SHA, cerr, branchLabel(start.Branch))
		rep.Done(StepFluxInstall, err)
		return err
	}
	if err := runFluxInstall(ctx, opts, out); err != nil {
		rep.Done(StepFluxInstall, err)
		return fmt.Errorf("%w: %v", ErrApplyFluxInstallFailed, err)
	}
	rep.Done(StepFluxInstall, nil)
	fmt.Fprintf(out, "[apply] complete — Flux is reconciling cluster %q\n", opts.Plan.ClusterName)
	return nil
}

// validateApplyOptions is a defensive guard against programmatic
// callers (e.g. tests) constructing ApplyOptions with missing
// dependencies. Returns ErrApplyMissingDependency wrapping a
// specific cause.
func validateApplyOptions(opts ApplyOptions) error {
	if opts.Plan == nil {
		return fmt.Errorf("%w: Plan", ErrApplyMissingDependency)
	}
	if opts.FleetRepo == "" {
		return fmt.Errorf("%w: FleetRepo", ErrApplyMissingDependency)
	}
	if opts.NodeExternalIP == "" {
		return fmt.Errorf("%w: NodeExternalIP", ErrApplyMissingDependency)
	}
	if opts.Runner == nil {
		return fmt.Errorf("%w: Runner", ErrApplyMissingDependency)
	}
	if opts.Git == nil {
		return fmt.Errorf("%w: Git", ErrApplyMissingDependency)
	}
	return nil
}

// commitAndMaybePush picks between Git.Commit (NoPush) and
// Git.CommitAndPush. Centralised so the rollback path in Apply
// handles both shapes via the same error wrapper.
func commitAndMaybePush(ctx context.Context, opts ApplyOptions, msg string) (string, error) {
	if opts.NoPush {
		return opts.Git.Commit(ctx, opts.FleetRepo, msg)
	}
	return opts.Git.CommitAndPush(ctx, opts.FleetRepo, msg, opts.GitHubToken)
}

func pushedSuffix(noPush bool) string {
	if noPush {
		return " (--no-push set; not pushed to remote)"
	}
	return " pushed"
}

// runFluxInstall executes bootstrap/flux-install.sh via the
// supplied ScriptRunner. Args: `<cluster-name> --new-cluster`.
// Passes provider + owner/repo/token via env — the fleet script
// dispatches `flux bootstrap github` vs `flux bootstrap gitlab`
// based on `KUBE_DC_PROVIDER`. No secret-redaction step
// (flux-install doesn't echo passwords; add-cluster.sh's redactor
// is specific to that script's output).
func runFluxInstall(ctx context.Context, opts ApplyOptions, out io.Writer) error {
	fmt.Fprintln(out, "[apply] running flux-install.sh")

	// Provider dispatch — default (empty) → github for backward
	// compat with pre-multi-provider callers. The env var name is
	// `KUBE_DC_PROVIDER`; matches the fleet script's expectation.
	provider := opts.Provider
	if provider == "" {
		provider = ProviderGitHub
	}
	env := map[string]string{
		"KUBE_DC_PROVIDER": string(provider),
	}
	host, err := applyProviderHost(ctx, opts)
	if err != nil {
		return err
	}
	if err := CheckProviderHostSupport(opts.FleetRepo, provider, opts.ProviderHost); err != nil {
		return err
	}
	env["KUBE_DC_PROVIDER_HOST"] = host
	// A verified child-cluster kubeconfig must also govern the Flux script.
	// The runner otherwise inherits the workstation's KUBECONFIG, which may
	// point to an unrelated cluster even though Scaffold received this path.
	//
	// The script runs in the fleet repository, not in the directory the
	// operator started in, so a relative spelling (of this path or of an
	// entry of $KUBECONFIG) is handed over as an absolute one: there it
	// would name another file. An absolute path is passed exactly as given
	// (a verified session pins it).
	if opts.KubeconfigPath != "" {
		env["KUBECONFIG"] = fromAnywhere(opts.KubeconfigPath)
	} else if list := os.Getenv("KUBECONFIG"); list != "" {
		entries := filepath.SplitList(list)
		changed := false
		for i, entry := range entries {
			if entry != "" && !filepath.IsAbs(entry) {
				entries[i], changed = fromAnywhere(entry), true
			}
		}
		if changed {
			env["KUBECONFIG"] = strings.Join(entries, string(os.PathListSeparator))
		}
	}
	if opts.GitHubOwner != "" {
		env["GITHUB_OWNER"] = opts.GitHubOwner
	}
	if opts.GitHubRepo != "" {
		env["GITHUB_REPO"] = opts.GitHubRepo
	}
	if provider == ProviderGitLab {
		env["GITLAB_HOST"] = host
	}
	// Token env var name differs per provider. Set the appropriate
	// one so the fleet script's auth branch reads it directly; the
	// script also has a CLI-auth fallback (`gh`/`glab`) but env is
	// authoritative when we resolved a token upstream.
	if opts.GitHubToken != "" {
		if provider == ProviderGitLab {
			env["GITLAB_TOKEN"] = opts.GitHubToken
		} else {
			env["GITHUB_TOKEN"] = opts.GitHubToken
		}
	}

	lines, err := opts.Runner.Run(ctx, ports.ScriptFluxInstall, env, opts.Plan.ClusterName, "--new-cluster")
	if err != nil {
		return fmt.Errorf("start flux-install.sh: %w", err)
	}
	// ports.Drain enforces the ScriptRunner contract: a stream that ends
	// without a terminal exit record is a failure, not a silent success.
	// This loop previously defaulted exitCode to 0, so a killed
	// flux-install left the CLI reporting Flux as installed.
	exitCode, err := ports.Drain(lines, func(ln ports.Line) {
		streamTag := "stdout"
		if ln.Stream == ports.StreamStderr {
			streamTag = "stderr"
		}
		fmt.Fprintf(out, "[flux-install %s] %s\n", streamTag, ln.Text)
	})
	if err != nil {
		return fmt.Errorf("flux-install: %w", err)
	}
	if exitCode != 0 {
		return fmt.Errorf("flux-install.sh exit=%d", exitCode)
	}
	return nil
}

func branchLabel(ref string) string {
	if ref == "" {
		return "a detached HEAD"
	}
	return strings.TrimPrefix(ref, "refs/heads/")
}

// isolateKubeconfigs decides which kubeconfigs the scripts of the private
// checkout read, so that nothing their kubectl rewrites is inside the
// operator's fleet checkout. There are two inputs.
//
// $KUBECONFIG (or, when it is unset, ~/.kube/config): what any kubectl call
// without an explicit file reads. The scripts always get a run-owned snapshot
// of it: env is one file in runTemp holding the merged view of the whole
// list, read here, in the operator's working directory. Nothing is decided
// about where the originals are, so no spelling of a path (relative, through
// a link, a second name of the same file) can be decided wrongly.
//
// The file add-cluster.sh hands to kubectl: the explicit path, or, when there
// is none, the script's own default ~/.kube/<cluster>_config. A verified
// session pins the explicit path, so it is passed on as it is whenever that
// is safe, and replaced by a copy only when the file it opens is inside the
// checkout or has another name that could be (a hard link). discovery is the
// path to pass to the script ("" = none, the script then reads its default,
// which was found to be safe). A relative spelling is passed on as an
// absolute one: the script runs in another directory.
//
// Scope: this covers what Apply starts (the scripts and their kubectl). The
// CLI's own Kubernetes client is built by the caller, from the operator's
// kubeconfig, and has authenticated before Apply begins (preflight), so a
// token it refreshes there is not something a failed Apply could undo.
func isolateKubeconfigs(fleet, runTemp, cluster, explicit, envList string) (discovery, env string, err error) {
	root, err := filepath.EvalSymlinks(fromAnywhere(fleet))
	if err != nil {
		return "", "", fmt.Errorf("resolve the fleet checkout %s: %w", fleet, err)
	}
	script, isDefault := explicit, false
	if script == "" && cluster != "" {
		if home, herr := os.UserHomeDir(); herr == nil {
			script, isDefault = filepath.Join(home, ".kube", cluster+"_config"), true
		}
	}
	if script != "" {
		opened, copyIt, perr := placeKubeconfig(root, script)
		switch {
		case perr != nil:
			return "", "", perr
		case copyIt:
			discovery = filepath.Join(runTemp, "kubeconfig-discovery")
			if err := snapshotKubeconfig(discovery, []string{opened}); err != nil {
				return "", "", err
			}
		case !isDefault:
			discovery = opened
		}
	}
	files := filepath.SplitList(envList)
	if len(files) == 0 {
		files = []string{clientcmd.RecommendedHomeFile}
	}
	env = filepath.Join(runTemp, "kubeconfig-env")
	if err := snapshotKubeconfig(env, files); err != nil {
		return "", "", err
	}
	return discovery, env, nil
}

// fromAnywhere returns a spelling of path that opens the same file from any
// working directory. An absolute path is returned as it is. A relative one is
// joined to the working directory as written, NOT cleaned: "link/../file"
// names the file next to the link's target, and cleaning it as text would
// name another one.
func fromAnywhere(path string) string {
	if path == "" || filepath.IsAbs(path) {
		return path
	}
	cwd, err := os.Getwd()
	if err != nil {
		return path
	}
	return cwd + string(os.PathSeparator) + path
}

// physicalDir resolves a directory path the way the kernel does, component by
// component (a ".." leaves the directory a link led to, not the one the link
// is in), and returns the absolute result. A path that does not exist is
// returned as given: there is nothing to resolve, and whoever opens it next
// reports that.
func physicalDir(path string) (string, error) {
	resolved, err := filepath.EvalSymlinks(fromAnywhere(path))
	if errors.Is(err, fs.ErrNotExist) {
		return path, nil
	}
	if err != nil {
		return "", err
	}
	return resolved, nil
}

// placeKubeconfig says what path names. opened is a spelling that opens the
// same file from any directory. copyIt is true when the file it opens is
// inside root (the fleet checkout, absolute and resolved) or has more than one
// name. A path that opens nothing is not copied: there is nothing there for
// kubectl to rewrite.
func placeKubeconfig(root, path string) (opened string, copyIt bool, err error) {
	opened = fromAnywhere(path)
	if !filepath.IsAbs(opened) {
		return "", false, fmt.Errorf("resolve the kubeconfig path %s: the working directory is not known", path)
	}
	// EvalSymlinks walks the components the way the kernel does (a ".."
	// leaves the directory a link led to, not the one the link is in).
	resolved, err := filepath.EvalSymlinks(opened)
	if errors.Is(err, fs.ErrNotExist) {
		return opened, false, nil
	}
	if err != nil {
		return "", false, fmt.Errorf("resolve the kubeconfig path %s: %w", path, err)
	}
	// Both are absolute and resolved: a Rel error means another volume.
	if rel, rerr := filepath.Rel(root, resolved); rerr == nil && !escapes(rel) {
		return opened, true, nil
	}
	return opened, hasOtherNames(resolved), nil
}

// snapshotKubeconfig writes private, mode 0600: the merged view of files, as
// kubectl would compute it from that list. Files that do not exist are
// skipped, as kubectl skips them. What the originals refer to by relative
// path (a CA or key file, a token file, an exec plugin given by path) is
// resolved against the file that said it, so the copy works from where it is.
// Nothing is inlined: a file an unrelated context names and that is gone does
// not make the copy fail.
func snapshotKubeconfig(private string, files []string) error {
	rules := &clientcmd.ClientConfigLoadingRules{Precedence: files}
	config, err := rules.Load()
	if err != nil {
		return fmt.Errorf("read the kubeconfig %s: %w", strings.Join(files, string(os.PathListSeparator)), err)
	}
	if err := os.MkdirAll(filepath.Dir(private), 0o700); err != nil {
		return err
	}
	if err := clientcmd.WriteToFile(*config, private); err != nil {
		return fmt.Errorf("write a private copy of the kubeconfig: %w", err)
	}
	return os.Chmod(private, 0o600)
}

// exportFleetAgeKey gives sops (the CLI's and the scripts') the fleet's age
// key by absolute path when the operator selected none. `init` normally did
// this already, after verifying the key. The returned function takes the
// setting back: another Apply in this process, for another fleet, must not
// inherit this fleet's key.
func exportFleetAgeKey(fleet string) (restore func()) {
	restore = func() {}
	old, had := os.LookupEnv("SOPS_AGE_KEY_FILE")
	if old != "" || os.Getenv("SOPS_AGE_KEY") != "" {
		return restore
	}
	key, err := filepath.Abs(filepath.Join(fleet, "age.key"))
	if err != nil {
		return restore
	}
	if fi, err := os.Lstat(key); err == nil && fi.Mode().IsRegular() {
		_ = os.Setenv("SOPS_AGE_KEY_FILE", key)
		return func() {
			switch {
			case os.Getenv("SOPS_AGE_KEY_FILE") != key:
				// someone else set it meanwhile: theirs
			case had:
				_ = os.Setenv("SOPS_AGE_KEY_FILE", old) // it was set, and empty
			default:
				_ = os.Unsetenv("SOPS_AGE_KEY_FILE")
			}
		}
	}
	return restore
}

// refuseEscapingSymlinks refuses a private checkout that contains a symlink
// leading out of it. Writers and scripts follow directory symlinks, so one
// that the repository tracks and that leads to the operator's own checkout
// (or anywhere else) would send their output there.
//
// Each link is resolved the way the filesystem resolves it, one component at
// a time (resolveLink), not by cleaning the path first: "a/../x" where "a" is
// itself a link is not "x".
func refuseEscapingSymlinks(work string) error {
	root, err := filepath.EvalSymlinks(work)
	if err != nil {
		return err
	}
	// Walking the resolved root: WalkDir does not follow links, so every
	// directory it visits is a real one.
	return filepath.WalkDir(root, func(path string, d fs.DirEntry, err error) error {
		if err != nil {
			return err
		}
		if d.IsDir() && d.Name() == ".git" && filepath.Dir(path) == root {
			return filepath.SkipDir
		}
		if d.Type()&fs.ModeSymlink == 0 {
			return nil
		}
		rel, _ := filepath.Rel(root, path)
		target, err := resolveLink(path)
		if err != nil {
			return fmt.Errorf("the fleet repository tracks the symlink %s, which cannot be resolved (%v); init does not generate through it", filepath.ToSlash(rel), err)
		}
		if inside, _ := filepath.Rel(root, target); escapes(inside) {
			return fmt.Errorf("the fleet repository tracks the symlink %s, which leads outside the repository (%s); init does not generate through it", filepath.ToSlash(rel), target)
		}
		return nil
	})
}

// refuseManagedLinks refuses a symlink anywhere init writes for the cluster
// or checks by path: clusters/<name> and platform, with everything below
// them, and the directories leading there. A link inside the repository is
// refused too:
//
//   - a writer follows it, so the file init "generates" for this cluster
//     would be another cluster's (or a shared) file, replaced and published;
//   - Git stores a file under the link's target path, so a check that asks
//     the commit for platform/registry-depot/secret.enc.yaml would find
//     nothing there and pass, while the secret is published elsewhere.
func refuseManagedLinks(work, clusterName string) error {
	refuse := func(rel string) error {
		return fmt.Errorf("%s is a symlink in the fleet repository; init writes and checks the files of the cluster by their own paths and does not work through a link there. Replace it with the real file or directory", filepath.ToSlash(rel))
	}
	// Every directory on the way down: a cluster name can be nested
	// (clusters/eu/east1), and a link at clusters/eu redirects as well.
	leading := []string{"platform"}
	for dir := filepath.Join("clusters", filepath.FromSlash(clusterName)); dir != "." && dir != string(filepath.Separator); dir = filepath.Dir(dir) {
		leading = append(leading, dir)
	}
	for _, dir := range leading {
		fi, err := os.Lstat(filepath.Join(work, dir))
		if errors.Is(err, fs.ErrNotExist) {
			continue
		}
		if err != nil {
			return err
		}
		if fi.Mode()&fs.ModeSymlink != 0 {
			return refuse(dir)
		}
	}
	for _, dir := range []string{filepath.Join("clusters", filepath.FromSlash(clusterName)), "platform"} {
		err := filepath.WalkDir(filepath.Join(work, dir), func(path string, d fs.DirEntry, err error) error {
			if errors.Is(err, fs.ErrNotExist) {
				return nil
			}
			if err != nil {
				return err
			}
			if d.Type()&fs.ModeSymlink != 0 {
				rel, _ := filepath.Rel(work, path)
				return refuse(rel)
			}
			return nil
		})
		if err != nil {
			return err
		}
	}
	return nil
}

// resolveLink returns where the symlink at link (whose directory is a real
// path) leads, following every link on the way. Components that do not exist
// are kept as written, as long as the rest of the target only descends.
func resolveLink(link string) (string, error) {
	cur := filepath.Dir(link)
	rest := []string{filepath.Base(link)}
	for hops := 0; len(rest) > 0; {
		part := rest[0]
		rest = rest[1:]
		switch part {
		case "", ".":
			continue
		case "..":
			cur = filepath.Dir(cur)
			continue
		}
		next := filepath.Join(cur, part)
		fi, err := os.Lstat(next)
		if errors.Is(err, fs.ErrNotExist) {
			// Nothing below a missing directory can be a link NOW. But a
			// ".." further on would climb back out of it once a writer has
			// created it, to a place where the next component may be a link:
			// where such a target ends up cannot be established.
			for _, p := range append([]string{part}, rest...) {
				switch p {
				case "", ".":
				case "..":
					return "", fmt.Errorf("its target goes through a directory that does not exist yet and back out of it")
				default:
					cur = filepath.Join(cur, p)
				}
			}
			return cur, nil
		}
		if err != nil {
			return "", err
		}
		if fi.Mode()&fs.ModeSymlink == 0 {
			cur = next
			continue
		}
		if hops++; hops > 64 {
			return "", fmt.Errorf("too many levels of symbolic links")
		}
		target, err := os.Readlink(next)
		if err != nil {
			return "", err
		}
		if filepath.IsAbs(target) {
			cur = filepath.VolumeName(target) + string(filepath.Separator)
		}
		rest = append(strings.Split(filepath.ToSlash(target), "/"), rest...)
	}
	return cur, nil
}

func escapes(rel string) bool {
	return rel == "" || rel == ".." || strings.HasPrefix(rel, ".."+string(filepath.Separator)) || filepath.IsAbs(rel)
}

// refuseUnignoredAgeKey refuses to stage when the private checkout holds an
// age.key that Git would record: staging alone stores its content in the
// repository's object store. "Would record" is judged by the rules the staging
// uses: the starting checkout's (its ignore policy can depend on the branch)
// and the private checkout's own; and a key Git already tracks is refused
// outright.
func refuseUnignoredAgeKey(fleet, work string) error {
	if _, err := os.Lstat(filepath.Join(work, "age.key")); err != nil {
		return nil
	}
	if cleanGit(work, "ls-files", "--error-unmatch", "--", "age.key").Run() == nil {
		return fmt.Errorf("age.key is tracked by Git in this repository: remove it from Git and rotate the key before running init")
	}
	for _, dir := range []string{fleet, work} {
		if cleanGit(dir, "check-ignore", "--no-index", "-q", "--", "age.key").Run() != nil {
			return fmt.Errorf("age.key is in the checkout and Git does not ignore it: add it to .gitignore before running init")
		}
	}
	return nil
}

// cleanGit is a git command in dir that neither runs repository hooks nor
// honours a repository selected through the environment.
func cleanGit(dir string, args ...string) *exec.Cmd {
	cmd := exec.Command("git", append([]string{"-C", dir, "-c", "core.hooksPath=/dev/null", "-c", "core.fsmonitor=false"}, args...)...)
	for _, kv := range os.Environ() {
		switch name, _, _ := strings.Cut(kv, "="); name {
		case "GIT_DIR", "GIT_WORK_TREE", "GIT_INDEX_FILE", "GIT_COMMON_DIR", "GIT_OBJECT_DIRECTORY", "GIT_NAMESPACE", "GIT_PREFIX":
		default:
			cmd.Env = append(cmd.Env, kv)
		}
	}
	return cmd
}

// ageKeyTreeCheck refuses a tree that contains the age private key at the
// repository root: init neither commits it nor publishes a commit that has it.
func ageKeyTreeCheck(tree ports.TreeFileReader) error {
	if _, present, err := tree("age.key"); err != nil || present {
		return fmt.Errorf("the commit contains age.key, the fleet's private key: remove it from Git and rotate the key")
	}
	return nil
}

// overlayCommitCheck decides on the exact tree the bootstrap commit records:
//   - it is a complete overlay (overlayTreeCheck);
//   - when the scaffold wired the registry-depot overlay, it carries the
//     credential that overlay references as a regular file, unchanged if the
//     starting commit already publishes it.
func overlayCommitCheck(clusterName string, credentialWired bool) func(ports.CommitTrees) error {
	return func(trees ports.CommitTrees) error {
		if err := overlayTreeCheck(clusterName, trees.Candidate); err != nil {
			return err
		}
		if !credentialWired {
			return nil
		}
		data, present, err := trees.Candidate(registryDepotCredentialPath)
		if err != nil {
			return fmt.Errorf("read %s in the commit tree: %w", registryDepotCredentialPath, err)
		}
		if !present {
			return fmt.Errorf("the commit wires registry-depot but does not contain %s (is it ignored by Git?)", registryDepotCredentialPath)
		}
		published, tracked, err := trees.Parent(registryDepotCredentialPath)
		if err != nil {
			return fmt.Errorf("read %s in the starting commit: %w", registryDepotCredentialPath, err)
		}
		if tracked && !bytes.Equal(data, published) {
			return fmt.Errorf("the commit changes %s, which the starting commit already publishes", registryDepotCredentialPath)
		}
		return nil
	}
}

// overlayTreeCheck refuses a tree whose cluster overlay must not be published:
//
//   - a file every overlay consists of, or a YAML file its kustomization.yaml
//     lists as a resource, is not in the tree. That happens when Git ignores
//     the file: the generated copy (the cluster's encrypted secrets, for one)
//     is then left out of the commit and discarded with the private checkout,
//     and Flux is handed a kustomization naming a file the repository lacks;
//   - secrets.enc.yaml holds a value in the clear (secretsEncrypted).
//
// Resources that are not YAML files of this overlay are not judged: a
// directory (flux-system is created by `flux bootstrap` later), a remote URL,
// a path outside the overlay.
func overlayTreeCheck(clusterName string, tree ports.TreeFileReader) error {
	overlay := "clusters/" + clusterName + "/"
	content := map[string][]byte{}
	for _, name := range []string{"cluster-config.env", "kustomization.yaml", "secrets.enc.yaml"} {
		data, present, err := tree(overlay + name)
		if err != nil {
			return fmt.Errorf("read %s in the commit tree: %w", overlay+name, err)
		}
		if !present {
			return fmt.Errorf("the commit does not contain %s, which the overlay needs (is it ignored by Git?)", overlay+name)
		}
		content[name] = data
	}
	if err := secretsEncrypted(content["secrets.enc.yaml"]); err != nil {
		return fmt.Errorf("%ssecrets.enc.yaml in the commit: %w", overlay, err)
	}
	var doc struct {
		Resources []string `yaml:"resources"`
	}
	if err := yaml.Unmarshal(content["kustomization.yaml"], &doc); err != nil {
		return fmt.Errorf("%skustomization.yaml in the commit tree: %w", overlay, err)
	}
	for _, res := range doc.Resources {
		if !(strings.HasSuffix(res, ".yaml") || strings.HasSuffix(res, ".yml")) || strings.Contains(res, "://") || strings.HasPrefix(res, "/") {
			continue
		}
		outside := false
		for _, segment := range strings.Split(res, "/") {
			outside = outside || segment == ".."
		}
		if outside {
			continue
		}
		// In the tree but not a regular file (a link, a directory) still
		// counts as there; any other reader error is a failure to check.
		_, present, err := tree(overlay + strings.TrimPrefix(res, "./"))
		if err != nil && !errors.Is(err, ports.ErrNotRegularFile) {
			return fmt.Errorf("read %s%s in the commit tree: %w", overlay, res, err)
		}
		if !present {
			return fmt.Errorf("%skustomization.yaml lists %s, which the commit does not contain (is it ignored by Git?)", overlay, res)
		}
	}
	return nil
}

// encryptedFilesCheck applies secretsEncrypted to every SOPS-managed file of
// the tree that init or its scripts manage: each *.enc.yaml under the cluster
// overlay (cluster secrets, wildcard TLS, DNS-01 and other credentials) and
// the registry-depot credential. work is a checkout of that tree; it only
// supplies the file names, the content checked is the tree's.
func encryptedFilesCheck(work, clusterName string, tree ports.TreeFileReader) error {
	paths := []string{registryDepotCredentialPath}
	overlay := filepath.Join(work, "clusters", clusterName)
	err := filepath.WalkDir(overlay, func(path string, d fs.DirEntry, err error) error {
		if errors.Is(err, fs.ErrNotExist) {
			return nil
		}
		if err != nil || d.IsDir() {
			return err
		}
		if name := d.Name(); strings.HasSuffix(name, ".enc.yaml") || strings.HasSuffix(name, ".enc.yml") {
			rel, rerr := filepath.Rel(work, path)
			if rerr != nil {
				return rerr
			}
			paths = append(paths, filepath.ToSlash(rel))
		}
		return nil
	})
	if err != nil {
		return fmt.Errorf("list the overlay's encrypted files: %w", err)
	}
	for _, p := range paths {
		data, present, err := tree(p)
		if errors.Is(err, ports.ErrNotRegularFile) || err == nil && !present {
			continue // not a file of this tree
		}
		if err != nil {
			return fmt.Errorf("read %s in the commit tree: %w", p, err)
		}
		if err := secretsEncrypted(data); err != nil {
			return fmt.Errorf("%s in the commit: %w", p, err)
		}
	}
	return nil
}

// pushDestinationMatches refuses a push URL that does not name the repository
// Flux is being installed for (owner/repo on the provider). Every URL that
// leaves this machine is judged, whatever its path looks like: one whose path
// cannot be read as that owner/repo is refused, not let through. Only a local
// path (or file://) is not judged: it has no owner.
func pushDestinationMatches(pushURL, owner, repo string) error {
	if owner == "" || repo == "" {
		return nil
	}
	path, hosted := hostedRepoPath(pushURL)
	if !hosted {
		return nil
	}
	want := owner + "/" + repo
	if path == "" {
		return fmt.Errorf("pushes from this checkout go to a remote address that names no owner/repository, but Flux is being installed for %s: they must be the same repository. Check url.*.pushInsteadOf and remote.origin.pushurl", want)
	}
	if !strings.EqualFold(path, want) {
		// The repository paths only: a push URL can carry a credential.
		return fmt.Errorf("pushes from this checkout go to the repository %s, but Flux is being installed for %s: they must be the same repository. Check url.*.pushInsteadOf and remote.origin.pushurl", path, want)
	}
	return nil
}

// hostedRepoPath classifies a remote the way the Git transport does and
// returns its repository path (owner/repo, or group/.../repo, "" when it has
// none). hosted is false only for a local repository. The transport is decided
// first and independently of the path: `deploy@host:.git` is an SSH remote
// with no project path, not a local one.
func hostedRepoPath(remote string) (path string, hosted bool) {
	ep, err := transport.NewEndpoint(remote)
	if err != nil {
		return "", true // unreadable: nothing says it is local
	}
	if ep.Protocol == "file" {
		return "", false
	}
	path = strings.Trim(ep.Path, "/")
	path = strings.Trim(strings.TrimSuffix(path, ".git"), "/")
	return path, true
}

// managedSecretPath reports whether path is a file init treats as holding
// secrets for this cluster: the age private key, the registry-depot
// credential, and every *.enc.yaml of the cluster's overlay.
func managedSecretPath(clusterName, path string) bool {
	if path == "age.key" || path == registryDepotCredentialPath {
		return true
	}
	return strings.HasPrefix(path, "clusters/"+clusterName+"/") &&
		(strings.HasSuffix(path, ".enc.yaml") || strings.HasSuffix(path, ".enc.yml"))
}

// publishCheck refuses a push that would newly publish a secret in the clear.
// It looks at every version of every managed secret file (managedSecretPath)
// in the commits the push destination does not have yet: the tip's, and those
// of every commit below it, through every parent of a merge. A key or a
// decrypted credential that was committed locally and removed or re-encrypted
// in a later commit is still in what the push sends.
//
// SCOPE: these are the files init and its scripts write for this cluster. It
// is not a general secret scanner: a secret an operator commits under another
// path or in another format is theirs to keep out of Git.
func publishCheck(clusterName string) *ports.PublishCheck {
	return &ports.PublishCheck{
		Want:  func(path string) bool { return managedSecretPath(clusterName, path) },
		Roots: []string{"age.key", registryDepotCredentialPath, "clusters/" + clusterName},
		Check: func(files []ports.UnpublishedFile) error {
			for _, f := range files {
				if f.Path == "age.key" {
					return fmt.Errorf("the branch to publish (its tip, or a commit not yet on the remote) contains age.key, the fleet's private key (blob %s): rewrite it out of the branch and rotate the key", f.Blob)
				}
				if err := secretsEncrypted(f.Data); err != nil {
					return fmt.Errorf("the branch to publish (its tip, or a commit not yet on the remote) contains %s (blob %s) not fully encrypted: %w. Rewrite it out of the branch and rotate what it held", f.Path, f.Blob, err)
				}
			}
			return nil
		},
	}
}

// sopsValue is one complete SOPS ciphertext value, nothing before or after.
var sopsValue = regexp.MustCompile(`^ENC\[AES256_GCM,data:[A-Za-z0-9+/=]*,iv:[A-Za-z0-9+/=]+,tag:[A-Za-z0-9+/=]+,type:[a-z]+\]$`)

// secretsEncrypted refuses a SOPS-managed manifest that is not fully
// encrypted:
//   - every document must be a mapping that carries SOPS metadata;
//   - its data and stringData sections (a Kubernetes Secret), when present,
//     must be mappings whose every value is one complete SOPS ciphertext;
//   - the credentials of a kubeconfig (users[].user.token, client-key-data,
//     password, ...) must be SOPS ciphertext as well: a kubeconfig encrypted
//     with the Secret rule has metadata and a bearer token in the clear;
//   - YAML merge keys and aliases are refused: they give a mapping values that
//     are not written under its own keys.
//
// A file where one password is encrypted and another is still in the clear
// looks encrypted at a glance. The documents are read as YAML nodes, not into
// Go maps: a section with a non-string key decodes into a different map type,
// and a lenient type assertion would skip that whole section.
func secretsEncrypted(manifest []byte) error {
	dec := yaml.NewDecoder(bytes.NewReader(manifest))
	docs := 0
	for {
		var doc yaml.Node
		err := dec.Decode(&doc)
		if errors.Is(err, io.EOF) {
			break
		}
		if err != nil {
			return fmt.Errorf("not valid YAML: %w", err)
		}
		root := &doc
		if root.Kind == yaml.DocumentNode {
			if len(root.Content) == 0 {
				continue
			}
			root = root.Content[0]
		}
		if root.Kind == yaml.ScalarNode && root.Tag == "!!null" {
			continue // an empty document
		}
		if root.Kind != yaml.MappingNode {
			return fmt.Errorf("a document is not a mapping")
		}
		if err := refuseIndirection(root); err != nil {
			return err
		}
		docs++
		hasSOPS := false
		for i := 0; i+1 < len(root.Content); i += 2 {
			key, value := root.Content[i], root.Content[i+1]
			switch key.Value {
			case "sops":
				hasSOPS = value.Kind == yaml.MappingNode && len(value.Content) > 0
			case "data", "stringData":
				if value.Kind != yaml.MappingNode {
					return fmt.Errorf("%s is not a mapping", key.Value)
				}
				for j := 0; j+1 < len(value.Content); j += 2 {
					name, v := value.Content[j], value.Content[j+1]
					if v.Kind != yaml.ScalarNode || !sopsValue.MatchString(v.Value) {
						return fmt.Errorf("%s.%s is not SOPS ciphertext: the value is in the clear", key.Value, name.Value)
					}
				}
			case "users":
				if err := kubeconfigUsersEncrypted(value); err != nil {
					return err
				}
			case "metadata":
				if err := refuseLastApplied(value); err != nil {
					return err
				}
			}
		}
		if !hasSOPS {
			return fmt.Errorf("a document has no SOPS metadata: it is not encrypted")
		}
	}
	if docs == 0 {
		return fmt.Errorf("the file is empty")
	}
	return nil
}

// lastAppliedAnnotation is where `kubectl apply` and `--save-config` keep a
// full copy of the object, a Secret's data included. SOPS encrypts data and
// stringData and leaves metadata alone, so that copy stays readable.
const lastAppliedAnnotation = "kubectl.kubernetes.io/last-applied-configuration"

// refuseLastApplied refuses a manifest whose metadata carries that annotation.
func refuseLastApplied(metadata *yaml.Node) error {
	if metadata.Kind != yaml.MappingNode {
		return nil
	}
	for i := 0; i+1 < len(metadata.Content); i += 2 {
		if metadata.Content[i].Value != "annotations" || metadata.Content[i+1].Kind != yaml.MappingNode {
			continue
		}
		annotations := metadata.Content[i+1]
		for j := 0; j+1 < len(annotations.Content); j += 2 {
			if annotations.Content[j].Value == lastAppliedAnnotation {
				return fmt.Errorf("metadata.annotations has %s: kubectl keeps a copy of the whole object there, in the clear. Remove the annotation", lastAppliedAnnotation)
			}
		}
	}
	return nil
}

// refuseIndirection refuses merge keys and aliases anywhere in a document.
func refuseIndirection(n *yaml.Node) error {
	if n.Kind == yaml.AliasNode {
		return fmt.Errorf("it uses a YAML alias, which init does not accept in an encrypted manifest")
	}
	if n.Kind == yaml.MappingNode {
		for i := 0; i+1 < len(n.Content); i += 2 {
			if key := n.Content[i]; key.Tag == "!!merge" || key.Value == "<<" {
				return fmt.Errorf("it uses a YAML merge key (<<), which init does not accept in an encrypted manifest")
			}
		}
	}
	for _, child := range n.Content {
		if err := refuseIndirection(child); err != nil {
			return err
		}
	}
	return nil
}

// kubeconfigCredentials are the fields of a kubeconfig user that hold a
// credential.
var kubeconfigCredentials = map[string]bool{
	"token": true, "client-key-data": true, "password": true, "auth-provider": true, "exec": true,
}

// kubeconfigUsersEncrypted checks users[].user of a kubeconfig: every scalar
// credential must be SOPS ciphertext, and a structured one (an exec or
// auth-provider block) must not be there in the clear at all.
func kubeconfigUsersEncrypted(users *yaml.Node) error {
	if users.Kind != yaml.SequenceNode {
		return nil // not a kubeconfig's users list
	}
	for _, entry := range users.Content {
		if entry.Kind != yaml.MappingNode {
			continue
		}
		for i := 0; i+1 < len(entry.Content); i += 2 {
			if entry.Content[i].Value != "user" || entry.Content[i+1].Kind != yaml.MappingNode {
				continue
			}
			user := entry.Content[i+1]
			for j := 0; j+1 < len(user.Content); j += 2 {
				name, v := user.Content[j], user.Content[j+1]
				if !kubeconfigCredentials[name.Value] {
					continue
				}
				if v.Kind != yaml.ScalarNode || !sopsValue.MatchString(v.Value) {
					return fmt.Errorf("users[].user.%s is not SOPS ciphertext: the credential is in the clear", name.Value)
				}
			}
		}
	}
	return nil
}
