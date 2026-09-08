package main

import (
	"context"
	"fmt"
	"io"
	"os"
	"strings"
	"time"

	"github.com/shalb/kube-dc/cli/internal/bootstrap"
	"github.com/shalb/kube-dc/cli/internal/bootstrap/breakglass"
	"github.com/shalb/kube-dc/cli/internal/bootstrap/clusterinit"
	"github.com/shalb/kube-dc/cli/internal/bootstrap/keycloak"
	"github.com/shalb/kube-dc/cli/internal/bootstrap/oidccutover"
	"github.com/shalb/kube-dc/cli/internal/bootstrap/openbao"
)

// runFinalizePhase drives the post-reconcile steps that used to be
// separate operator commands — OpenBao init/unseal/controller-auth and
// Keycloak OIDC client bootstrap — as live install milestones. It runs
// after Apply + flux-install + fetch-kubeconfig, i.e. once Flux is
// reconciling the platform.
//
// **Everything here is BEST-EFFORT.** The cluster is already up and
// reconciling by the time we get here; a finalize failure (OpenBao not
// up within budget, Keycloak still reconciling, a transient exec error)
// is reported as a deferred milestone with an exact re-run command, NOT
// a hard error that would mask a successful install. This matches the
// pre-full-flow world where these were manual post-install steps.
//
// Auth: the top-of-flow session (runApplyEngine) was built BEFORE
// fetch-kubeconfig and may have no cluster. We rebuild a session against
// the freshly-merged admin kubeconfig, which authenticates by client
// cert (the RKE2 admin kubeconfig) — NOT OIDC. That matters: on a fresh
// install no admin has `kube-dc login`'d yet and Keycloak isn't even up,
// so any OIDC-bearer path (e.g. discover.ClusterProbe) would report
// "not logged in". The openbao/keycloak drivers all go through the
// session's cert-authed adapters.
// fetchVerified is true only when THIS run's kubeconfig at
// clusterinit.DefaultKubeconfigPath() is known to have been freshly fetched
// FOR o.Name — i.e. bootstrap_init.go's sshEnabled && fetchOK. It is false
// on the --no-ssh / --ssh-host-less path, where runPostApply still runs
// (finalize is not gated on SSH) but that file is whatever the operator's
// kubeconfig already was — ssh_kubeconfig.go's cluster/user/context rename
// to o.Name never ran, so a context literally named o.Name may not exist
// there at all, or worse, may already exist and name a DIFFERENT cluster
// (e.g. a same-named cluster from an earlier install). Every place below
// that would otherwise assert "--kube-context o.Name is always valid" only
// does so when fetchVerified is true — see postApplyBreakGlassOptions.
func runPostApply(ctx context.Context, out io.Writer, o *clusterinit.InitOptions, rep clusterinit.StepReporter, fetchVerified bool) {
	fmt.Fprintln(out, "[post] Flux is reconciling — tracking convergence, then finalizing OpenBao + Keycloak")
	gpu := o.GPU()

	// Rebuild the session on the fetched admin kubeconfig (see doc).
	kubeconfig := clusterinit.DefaultKubeconfigPath()
	session, err := bootstrap.NewSession(bootstrap.Options{
		FleetRepoPath: o.Repo,
		Kubeconfig:    kubeconfig,
	})
	if err != nil {
		reason := fmt.Sprintf("build session on %s failed: %v", kubeconfig, err)
		rep.Skip(clusterinit.StepBreakGlass, reason)
		rep.Skip(clusterinit.StepReconcile, reason)
		skipGPUInstallSteps(rep, gpu, reason)
		rep.Skip(clusterinit.StepOpenBao, reason)
		rep.Skip(clusterinit.StepKeycloakOIDC, reason)
		rep.Skip(clusterinit.StepOIDCCutover, reason)
		finalizeHint(out, o, kubeconfig, fetchVerified)
		return
	}
	if session != nil {
		defer session.Close()
	}

	// Resolved once, up front: break-glass (below) and the Keycloak/OpenBao
	// steps further down both need it for their own commit+push.
	var token string
	if !o.NoPush {
		token = resolveGitHubToken(o, out)
	}

	// --- Break-glass: adopt (or refresh) the static-token cluster-admin
	// recovery kubeconfig. Runs FIRST among the finalize steps and does NOT
	// wait for the reconcile watch below — see StepBreakGlass: the
	// ServiceAccount + ClusterRoleBinding + token Secret it creates are core
	// API objects the apiserver already serves, so this works the moment
	// the session above was built, independent of Flux/infra-core ever
	// coming up. Closes the exact gap that left crk, jed and next without a
	// committed recovery kubeconfig: `bootstrap init` never ran `break-glass
	// adopt`, so the only way to get one was an operator remembering to run
	// it — and commit it — by hand (2026-09-04).
	bgErr := step(rep, clusterinit.StepBreakGlass, func() error {
		return breakglass.Adopt(ctx, postApplyBreakGlassOptions(o, session, kubeconfig, token, fetchVerified))
	})
	if bgErr != nil {
		fmt.Fprintf(out, "[finalize] break-glass recovery kubeconfig deferred (%v)\n", bgErr)
		// KUBECONFIG= pins the re-run to the same file the automatic attempt
		// above used. --kube-context additionally pins WHICH of that file's
		// contexts is targeted, since current-context can drift between now
		// and whenever the operator actually runs this — but it is only
		// printed when fetchVerified, i.e. only when fetch-kubeconfig
		// actually renamed the fetched entries to o.Name for THIS run (see
		// the fetchVerified doc above); otherwise a context named o.Name
		// might not exist, or might exist and name a DIFFERENT cluster.
		hint := fmt.Sprintf("KUBECONFIG=%s kube-dc bootstrap break-glass adopt %s --repo %s", shellQuote(kubeconfig), o.Name, shellQuote(o.Repo))
		if fetchVerified {
			hint += " --kube-context " + shellQuote(o.Name)
		}
		fmt.Fprintf(out, "[finalize]   re-run once ready: %s\n", hint)
	}

	// --- Reconcile watch (Feature: track Flux reconciliation). Best-
	// effort: a budget expiry surfaces as a ✗ milestone but the finalize
	// steps still run (they have their own readiness waits).
	reconcileErr := runReconcileWatchWithGPU(ctx, out, session.Flux, rep, gpu)
	if reconcileErr == nil && gpu.Platform == clusterinit.GPUPlatformEnabled {
		writeGPUInstallCompletion(out)
	}

	// --- OpenBao: wait for the pod to be Running (exec-able), then run
	// the full init chain. We wait for RUNNING, not READY: an
	// uninitialized OpenBao pod fails its readiness probe, and Init is
	// exactly what unseals it — waiting for Ready would deadlock.
	// Resumable: skip entirely if OpenBao is already finalized (Init is
	// non-idempotent), so a re-run of `init` doesn't error here.
	obErr := step(rep, clusterinit.StepOpenBao, func() error {
		if openBaoFinalized(ctx, session.K8s) {
			fmt.Fprintln(out, "[finalize] OpenBao already initialized — skipping (resume)")
			return nil
		}
		if err := waitPodRunning(ctx, out, session.K8s, openBaoNamespace, openBaoPod, finalizeReadyBudget); err != nil {
			return err
		}
		return openbao.Init(ctx, postApplyOpenBaoInitOptions(o, session, token, out))
	})
	if obErr != nil {
		fmt.Fprintf(out, "[finalize] OpenBao init deferred (%v)\n", obErr)
		fmt.Fprintf(out, "[finalize]   re-run once ready: kube-dc bootstrap openbao init %s --repo %s\n", o.Name, shellQuote(o.Repo))
	}

	// --- Keycloak: keycloak.Init self-polls the master-realm OIDC
	// discovery endpoint (10-min budget) before doing anything, so we
	// don't pre-wait here — an out-of-order call just times out
	// internally and we defer with a re-run hint. Idempotent, so a
	// re-run produces no diff.
	kcErr := step(rep, clusterinit.StepKeycloakOIDC, func() error {
		return keycloak.Init(ctx, keycloak.InitOptions{
			ClusterName: o.Name,
			FleetRepo:   o.Repo,
			Runner:      session.Scripts,
			Git:         session.Git,
			GitHubToken: token,
			NoPush:      o.NoPush,
			Out:         out,
		})
	})
	if kcErr != nil {
		fmt.Fprintf(out, "[finalize] Keycloak OIDC deferred (%v)\n", kcErr)
		fmt.Fprintf(out, "[finalize]   re-run once ready: kube-dc bootstrap keycloak init %s --repo %s\n", o.Name, shellQuote(o.Repo))
	}

	// --- Self-service sign-up (sso realm). Opt-in by presence: when the
	// operator exported SMTP credentials for the install, provision the
	// realm right here — otherwise installs ship with the console's
	// sign-up hidden (frontends gate it on SSO_ENABLED) and a hint. Not a
	// reporter milestone: it is optional day-2 surface, not an install
	// gate, and it must never turn a green install red. Runs after the
	// Keycloak OIDC ceremony because both need Keycloak answering, and
	// skips itself when that ceremony was deferred.
	smtpUser, smtpPass := os.Getenv("SMTP_USER"), os.Getenv("SMTP_PASSWORD")
	switch {
	case kcErr != nil:
		// Keycloak isn't ready; the sso ceremony would only time out too.
	case smtpUser != "" && smtpPass != "":
		ssoEnv := map[string]string{"SMTP_USER": smtpUser, "SMTP_PASSWORD": smtpPass}
		for _, k := range []string{"SMTP_HOST", "SMTP_PORT", "SMTP_FROM", "SMTP_FROM_NAME", "SMTP_SECURE", "GOOGLE_CLIENT_ID", "GOOGLE_CLIENT_SECRET"} {
			if v := os.Getenv(k); v != "" {
				ssoEnv[k] = v
			}
		}
		if ssoErr := keycloak.SSORealm(ctx, keycloak.SSOOptions{
			ClusterName: o.Name,
			FleetRepo:   o.Repo,
			Runner:      session.Scripts,
			Git:         session.Git,
			GitHubToken: token,
			NoPush:      o.NoPush,
			Out:         out,
			Env:         ssoEnv,
		}); ssoErr != nil {
			fmt.Fprintf(out, "[finalize] self-service sign-up deferred (%v)\n", ssoErr)
			fmt.Fprintf(out, "[finalize]   re-run once ready: SMTP_USER=... SMTP_PASSWORD=... kube-dc bootstrap keycloak sso %s --repo %s\n", o.Name, o.Repo)
		}
	default:
		fmt.Fprintf(out, "[finalize] self-service sign-up not enabled (no SMTP credentials in the environment) — enable later with: SMTP_USER=... SMTP_PASSWORD=... kube-dc bootstrap keycloak sso %s --repo %s --smtp-host <relay>\n", o.Name, o.Repo)
	}

	// --- Wire the apiservers to the OIDC webhook.
	//
	// Until this runs the cluster looks PERFECT and nobody can log in: nodes
	// Ready, Flux green, certificates issued, ingress serving — and every
	// Keycloak token rejected, so tenant kubectl, the console's organization
	// management and the platform operators all fail. Four clusters shipped
	// that way because it was a separate command an operator had to know to
	// run, and the symptom points at Keycloak rather than at the apiserver.
	//
	// It cannot be done earlier: kube-apiserver refuses to start when pointed
	// at a webhook kubeconfig that does not exist, and that file only appears
	// once Flux has brought up infra-core — which the reconcile watch above
	// has just waited for. This is the first moment it is possible, so it is
	// where it belongs.
	//
	// It deliberately does NOT gate on the reconcile watch succeeding. The
	// watch times out on slow clusters that go on to converge minutes later,
	// and gating on it would put us back where we started — the cutover
	// skipped, nobody told, the cluster unusable. The real gate is the
	// cutover's own preflight, which is strictly stronger: it requires the
	// authenticator to have pods AND the webhook kubeconfig to be present on
	// every control-plane node AND every node to answer SSH. A node that is
	// down, or a cluster where infra-core never came up, fails there and
	// nothing is restarted.
	if o.NoOIDCCutover {
		rep.Skip(clusterinit.StepOIDCCutover, "--no-oidc-cutover")
		fmt.Fprintf(out, "[finalize] OIDC cutover SKIPPED (--no-oidc-cutover) — until the apiservers are "+
			"wired by hand, every Keycloak login returns 401:\n")
		fmt.Fprintf(out, "[finalize]   %s\n", cutoverRerunCommand(o))
	}
	var cutErr error
	if !o.NoOIDCCutover {
		cutErr = step(rep, clusterinit.StepOIDCCutover, func() error {
			return runFinalizeOIDCCutover(ctx, out, o, session)
		})
	}
	if cutErr != nil {
		fmt.Fprintf(out, "[finalize] OIDC cutover deferred (%v)\n", cutErr)
		fmt.Fprintf(out, "[finalize]   REQUIRED — until this succeeds every Keycloak login returns 401:\n")
		fmt.Fprintf(out, "[finalize]     %s --dry-run\n", cutoverRerunCommand(o))
		fmt.Fprintf(out, "[finalize]     %s\n", cutoverRerunCommand(o))
		fmt.Fprintf(out, "[finalize]   (add --ssh-host user@host per node if this machine cannot reach their internal IPs)\n")
		fmt.Fprintf(out, "[finalize]   then confirm with: kube-dc bootstrap accept %s --domain %s\n", o.Name, o.Domain)
	}

	// --- Access summary (Feature: admin access + keycloak password +
	// SSO). Two outputs:
	//   1. A redaction-safe block (URLs + kubectl retrieval hint, NO
	//      password) → out. On the TUI path that's the log pane +
	//      transcript; on the plain/CI path that's stdout. Safe either way.
	//   2. On an INTERACTIVE terminal only (!NoTTY), the same block WITH
	//      the real Keycloak admin password → o.AccessSummary, which the
	//      cobra layer prints to the real terminal AFTER the alt-screen
	//      closes. Never on the plain/CI path (captured logs) and never
	//      in the redacted transcript.
	// Pass the SPECIFIC deferred steps so the block prints only the
	// rerun command(s) for what actually deferred — never telling the
	// operator to rerun a step that already succeeded.
	obDeferred, kcDeferred := obErr != nil, kcErr != nil
	fmt.Fprint(out, accessBlock(ctx, o, session.SOPS, false /*withPassword*/, obDeferred, kcDeferred))
	if !o.NoTTY {
		o.AccessSummary = accessBlock(ctx, o, session.SOPS, true /*withPassword*/, obDeferred, kcDeferred)
	}

	// The access block ends with credentials and URLs, which reads as "you are
	// done". If the cutover did not happen, none of those credentials work, so
	// this goes LAST — after the summary — rather than scrolling away above it.
	// An install that ends on a page of working-looking access details is
	// exactly how four clusters were handed over unusable.
	if cutErr != nil || o.NoOIDCCutover {
		writeCutoverOutstandingBanner(out, o)
		if !o.NoTTY {
			var b strings.Builder
			writeCutoverOutstandingBanner(&b, o)
			o.AccessSummary += b.String()
		}
	}
}

// writeCutoverOutstandingBanner is the last thing an install prints when the
// apiservers were not wired. It states the consequence in the operator's terms
// — every credential just printed is refused — because "oidc-cutover deferred"
// means nothing to someone installing kube-dc for the first time.
func writeCutoverOutstandingBanner(out io.Writer, o *clusterinit.InitOptions) {
	fmt.Fprintf(out, "\n!! THIS CLUSTER CANNOT BE LOGGED INTO YET\n")
	fmt.Fprintf(out, "   The apiservers are not wired to the OIDC webhook, so every credential above is\n")
	fmt.Fprintf(out, "   rejected: the console, tenant kubectl and the platform operators all get 401.\n")
	fmt.Fprintf(out, "   Nothing else reports this — the cluster is Ready and Flux is green.\n\n")
	fmt.Fprintf(out, "   Finish it:  %s\n", cutoverRerunCommand(o))
	fmt.Fprintf(out, "   Confirm:    kube-dc bootstrap accept %s --domain %s   (identity/oidc-cutover)\n", o.Name, o.Domain)
}

// postApplyOpenBaoInitOptions is kept as a small, testable wiring boundary.
// The automatic full-install path must honor every custody option exposed by
// bootstrap init just like the standalone openbao init command does.
func postApplyOpenBaoInitOptions(o *clusterinit.InitOptions, session *bootstrap.Session, token string, out io.Writer) openbao.InitOptions {
	return openbao.InitOptions{
		ClusterName:   o.Name,
		FleetRepo:     o.Repo,
		Runner:        session.Scripts,
		SOPS:          session.SOPS,
		Git:           session.Git,
		OpenBao:       session.OpenBao,
		K8s:           session.K8s,
		GitHubToken:   token,
		NoPush:        o.NoPush,
		SharesOutPath: o.OpenBaoSharesOut,
		Out:           out,
	}
}

// postApplyBreakGlassOptions is kept as a small, testable wiring boundary,
// same reasoning as postApplyOpenBaoInitOptions above. kubeconfigPath is
// threaded through explicitly (rather than relying on ambient $KUBECONFIG
// matching) so this targets the cluster that was just installed regardless
// of what the operator's shell environment happens to point at.
//
// KubectlContext is pinned to o.Name too — NOT left to "whatever is
// current-context in that file" — but only when fetchVerified: fetch-
// kubeconfig (ssh_kubeconfig.go) renames the fetched cluster/user/context
// entries to ClusterName, so o.Name is then a valid, addressable context
// inside kubeconfigPath regardless of what current-context has drifted to.
// That guarantee holds ONLY on the path where the fetch actually ran FOR
// THIS cluster (bootstrap_init.go's sshEnabled && fetchOK — see the
// fetchVerified doc on runPostApply). On --no-ssh, runPostApply still
// executes against whatever kubeconfig the operator already had, where a
// context literally named o.Name may not exist, or worse, may already
// exist and address a DIFFERENT cluster (e.g. an earlier install reusing
// the name) — asserting it there would be worse than the plain
// current-context trust every OTHER finalize step already relies on for
// that same untrusted case, not better. So KubectlContext is left empty
// there, matching that existing, narrower trust level exactly rather than
// overclaiming a guarantee this call cannot actually back up.
func postApplyBreakGlassOptions(o *clusterinit.InitOptions, session *bootstrap.Session, kubeconfigPath, token string, fetchVerified bool) breakglass.AdoptOpts {
	opts := breakglass.AdoptOpts{
		FleetRoot:      o.Repo,
		ClusterName:    o.Name,
		KubeconfigPath: kubeconfigPath,
		Git:            session.Git,
		GitHubToken:    token,
		NoPush:         o.NoPush,
	}
	if fetchVerified {
		opts.KubectlContext = o.Name
	}
	return opts
}

func skipGPUInstallSteps(rep clusterinit.StepReporter, gpu clusterinit.GPUConfig, reason string) {
	if gpu.Platform != clusterinit.GPUPlatformEnabled {
		return
	}
	for _, id := range clusterinit.GPUInstallStepIDs(gpu.HAMiEnabled) {
		rep.Skip(id, reason)
	}
}

func writeGPUInstallCompletion(out io.Writer) {
	fmt.Fprintln(out, "GPU platform installation is ready. Bootstrap granted no billable tenant GPU quota.")
	fmt.Fprintln(out, "Next:")
	fmt.Fprintln(out, "  1. Add a GPU add-on grant in Admin → Billing.")
	fmt.Fprintln(out, "  2. Assign the add-on to one controlled organization.")
	fmt.Fprintln(out, "  3. Optionally cap GPU use per project.")
	fmt.Fprintln(out, "  4. Run `kube-dc bootstrap doctor` and inspect Accelerators; use `bootstrap status` for node readiness.")
}

// finalize tunables (vars, not consts, so tests can shrink them).
var (
	openBaoNamespace     = "openbao"
	openBaoPod           = "openbao-0"
	finalizeReadyBudget  = 15 * time.Minute
	finalizePollInterval = 15 * time.Second
)

// waitPodRunning polls a pod's status.phase until it is "Running" (i.e.
// the container is up and `kubectl exec` will succeed) or the budget
// elapses / ctx cancels. Emits a heartbeat line every poll so the log
// pane shows progress during the (potentially many-minute) platform
// reconcile. A missing pod (HR not reconciled yet) is treated as
// not-ready-yet, not a hard error.
func waitPodRunning(ctx context.Context, out io.Writer, k8s interface {
	GetResourceFieldFirst(ctx context.Context, group, version, resource, namespace, name string, fields ...string) (string, error)
}, namespace, pod string, budget time.Duration) error {
	deadline := timeNow().Add(budget)
	for {
		if err := ctx.Err(); err != nil {
			return err
		}
		phase, err := k8s.GetResourceFieldFirst(ctx, "", "v1", "pods", namespace, pod, "status.phase")
		switch {
		case err == nil && phase == "Running":
			fmt.Fprintf(out, "[finalize] %s/%s is Running\n", namespace, pod)
			return nil
		case err != nil:
			fmt.Fprintf(out, "[finalize] waiting for %s/%s (not created yet)…\n", namespace, pod)
		default:
			fmt.Fprintf(out, "[finalize] waiting for %s/%s (phase=%s)…\n", namespace, pod, phase)
		}
		if timeNow().After(deadline) {
			return fmt.Errorf("timed out after %s waiting for %s/%s to be Running", budget, namespace, pod)
		}
		select {
		case <-ctx.Done():
			return ctx.Err()
		case <-timeAfter(finalizePollInterval):
		}
	}
}

// finalizeHint prints the manual commands for both finalize steps when
// the phase can't run at all (e.g. session build failed).
//
// kubeconfigPath, when non-empty, is prefixed onto the break-glass line as
// KUBECONFIG=<path> so a re-run reads the same file the automatic attempt
// used rather than whatever the operator's ambient $KUBECONFIG happens to
// point at. --kube-context <o.Name> is appended ADDITIONALLY, but only
// when fetchVerified — see the doc on runPostApply and
// postApplyBreakGlassOptions for exactly why: it is only guaranteed valid
// on the path where fetch-kubeconfig actually renamed the fetched entries
// to o.Name FOR THIS RUN. Printing it unconditionally would be worse than
// omitting it: on --no-ssh, a context named o.Name might not exist, or
// worse, might already exist and address a DIFFERENT cluster, silently
// applying cluster-admin RBAC there instead. Callers that reach here
// because the fresh admin kubeconfig fetch itself failed pass
// kubeconfigPath="" (fetchVerified is necessarily false too in that case)
// — matching this whole code path's existing "refuse to guess from the
// current context" philosophy — so the printed command carries an
// explicit reminder to point kubectl at the right cluster by hand instead
// of silently omitting the risk.
func finalizeHint(out io.Writer, o *clusterinit.InitOptions, kubeconfigPath string, fetchVerified bool) {
	fmt.Fprintf(out, "[finalize] run these once the platform reconciles:\n")
	switch {
	case kubeconfigPath != "" && fetchVerified:
		fmt.Fprintf(out, "[finalize]   KUBECONFIG=%s kube-dc bootstrap break-glass adopt %s --repo %s --kube-context %s\n",
			shellQuote(kubeconfigPath), o.Name, shellQuote(o.Repo), shellQuote(o.Name))
	case kubeconfigPath != "":
		fmt.Fprintf(out, "[finalize]   KUBECONFIG=%s kube-dc bootstrap break-glass adopt %s --repo %s\n",
			shellQuote(kubeconfigPath), o.Name, shellQuote(o.Repo))
	default:
		fmt.Fprintf(out, "[finalize]   kube-dc bootstrap break-glass adopt %s --repo %s   # point --kube-context (or $KUBECONFIG) at %s first — the fetch that would have targeted it for you failed\n", o.Name, shellQuote(o.Repo), o.Name)
	}
	fmt.Fprintf(out, "[finalize]   kube-dc bootstrap openbao init %s --repo %s\n", o.Name, shellQuote(o.Repo))
	fmt.Fprintf(out, "[finalize]   kube-dc bootstrap keycloak init %s --repo %s\n", o.Name, shellQuote(o.Repo))
	if !o.NoOIDCCutover {
		// Listed last because it is last in the finalize order, but it is the
		// one an operator must not skip: the other two failing is visible, this
		// one failing looks like a healthy cluster nobody can log in to.
		fmt.Fprintf(out, "[finalize]   %s   # REQUIRED for any Keycloak login to work\n", cutoverRerunCommand(o))
	}
}

// timeNow / timeAfter are indirections so finalize tests don't sleep in
// real time.
var (
	timeNow   = time.Now
	timeAfter = time.After
)

// runFinalizeOIDCCutover wires every control-plane apiserver to the OIDC
// webhook as part of `init`, so a cluster is not handed over in the state
// where it looks healthy and nobody can log in.
//
// It is deliberately conservative about WHICH nodes it touches: the cutover
// refuses a partial set, because a half-wired cluster gives intermittent 401s
// as kubectl load-balances across apiservers — a symptom that reads like a
// clock or Keycloak fault and costs far more than the un-wired case. Node
// discovery therefore comes from the live cluster, not from whatever single
// --ssh-host init happened to use.
//
// Re-running is safe: the cutover is idempotent and snapshots each node's
// config before touching it.
func runFinalizeOIDCCutover(ctx context.Context, out io.Writer, o *clusterinit.InitOptions, session *bootstrap.Session) error {
	if o.NoSSH {
		return fmt.Errorf("--no-ssh: cannot reach the control-plane nodes to wire them")
	}
	if session == nil || session.SSH == nil {
		return fmt.Errorf("no SSH client available in this session")
	}

	// Discover every control-plane node from the cluster. sshUser "" means the
	// resolver's default (root), matching `bootstrap oidc-cutover` with no
	// --ssh-user, so the automatic path and the manual one agree.
	nodes, err := resolveCutoverNodes(ctx, clusterinit.DefaultKubeconfigPath(), nil, oidcCutoverSSHUser(o), false)
	if err != nil {
		return err
	}
	fmt.Fprintf(out, "[finalize] wiring %d control-plane node(s) to the OIDC webhook\n", len(nodes))

	res, err := oidccutover.Run(ctx, oidccutover.Options{
		SSH:          session.SSH,
		K8s:          session.K8s,
		Nodes:        nodes,
		ReadyTimeout: finalizeCutoverReadyTimeout,
		Out:          out,
	})
	// Run is fail-fast and sequential, so an error can leave EARLIER nodes
	// wired. Say so: a half-wired control plane returns intermittent 401s
	// (kubectl load-balances across apiservers), which reads as a Keycloak or
	// clock fault and is far more expensive to diagnose than "none wired".
	// The generic deferred hint above would otherwise imply nothing happened.
	if wired := len(res.Wired) + len(res.AlreadyWired); err != nil && wired > 0 {
		fmt.Fprintf(out, "[finalize] WARNING: %d of %d control-plane node(s) are already wired — "+
			"this cluster is PARTIALLY cut over and will return intermittent 401s until the rest are done\n",
			wired, len(nodes))
	}
	if err != nil {
		return err
	}
	fmt.Fprintf(out, "[finalize] OIDC cutover: %d node(s) wired, %d already wired\n",
		len(res.Wired), len(res.AlreadyWired))
	return nil
}

// cutoverRerunCommand renders the command that finishes the job by hand.
//
// It deliberately carries NO cluster name: `bootstrap oidc-cutover` takes no
// positional argument and acts on whatever the current kubeconfig points at.
// Printing a name would be silently ignored by cobra while telling the operator
// the opposite — and this command edits apiserver configs, so "which cluster"
// is exactly the thing not to be wrong about. The --ssh-user is carried through
// because that IS load-bearing: without it every node is probed as root.
func cutoverRerunCommand(o *clusterinit.InitOptions) string {
	cmd := "kube-dc bootstrap oidc-cutover"
	if user := oidcCutoverSSHUser(o); user != "" {
		cmd += " --ssh-user " + user
	}
	return cmd
}

// oidcCutoverSSHUser reuses the user from --ssh-host when it carries one, so a
// cluster whose nodes are reached as `ubuntu@…` is not silently probed as root.
func oidcCutoverSSHUser(o *clusterinit.InitOptions) string {
	if o == nil {
		return ""
	}
	if user, _, found := strings.Cut(o.SSHHost, "@"); found && user != "" {
		return user
	}
	return ""
}

// finalizeCutoverReadyTimeout is how long each apiserver gets to come back
// healthy after its manifest is rewritten. It matches the standalone
// `bootstrap oidc-cutover --ready-timeout` default; the cutover restores the
// snapshot and aborts rather than moving to the next node if a node exceeds it,
// so a slow node costs an aborted run, never a half-wired control plane.
const finalizeCutoverReadyTimeout = 5 * time.Minute

// shellQuote single-quotes s for safe interpolation into a printed shell
// command line — same idiom already used independently in several
// internal/bootstrap packages (oidccutover, rke2, anchors, discover,
// initform). Without it, a kubeconfig path containing a space breaks the
// printed break-glass re-run hint, and one containing an unescaped `$`
// (e.g. a literal `$USER` component) would shell-expand on copy-paste —
// silently pointing the re-run at a DIFFERENT path than the one that was
// actually used, defeating the whole point of printing it explicitly.
func shellQuote(s string) string {
	return "'" + strings.ReplaceAll(s, "'", `'"'"'`) + "'"
}
