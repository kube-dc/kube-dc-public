package main

import (
	"context"
	"fmt"
	"io"
	"time"

	"github.com/shalb/kube-dc/cli/internal/bootstrap"
	"github.com/shalb/kube-dc/cli/internal/bootstrap/clusterinit"
	"github.com/shalb/kube-dc/cli/internal/bootstrap/keycloak"
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
func runPostApply(ctx context.Context, out io.Writer, o *clusterinit.InitOptions, rep clusterinit.StepReporter) {
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
		rep.Skip(clusterinit.StepReconcile, reason)
		skipGPUInstallSteps(rep, gpu, reason)
		rep.Skip(clusterinit.StepOpenBao, reason)
		rep.Skip(clusterinit.StepKeycloakOIDC, reason)
		finalizeHint(out, o)
		return
	}
	if session != nil {
		defer session.Close()
	}

	// --- Reconcile watch (Feature: track Flux reconciliation). Best-
	// effort: a budget expiry surfaces as a ✗ milestone but the finalize
	// steps still run (they have their own readiness waits).
	reconcileErr := runReconcileWatchWithGPU(ctx, out, session.Flux, rep, gpu)
	if reconcileErr == nil && gpu.Platform == clusterinit.GPUPlatformEnabled {
		writeGPUInstallCompletion(out)
	}

	var token string
	if !o.NoPush {
		token = resolveGitHubToken(o, out)
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
		return openbao.Init(ctx, openbao.InitOptions{
			ClusterName: o.Name,
			FleetRepo:   o.Repo,
			Runner:      session.Scripts,
			SOPS:        session.SOPS,
			Git:         session.Git,
			OpenBao:     session.OpenBao,
			K8s:         session.K8s,
			GitHubToken: token,
			NoPush:      o.NoPush,
			Out:         out,
		})
	})
	if obErr != nil {
		fmt.Fprintf(out, "[finalize] OpenBao init deferred (%v)\n", obErr)
		fmt.Fprintf(out, "[finalize]   re-run once ready: kube-dc bootstrap openbao init %s --repo %s\n", o.Name, o.Repo)
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
			Out:         out,
		})
	})
	if kcErr != nil {
		fmt.Fprintf(out, "[finalize] Keycloak OIDC deferred (%v)\n", kcErr)
		fmt.Fprintf(out, "[finalize]   re-run once ready: kube-dc bootstrap keycloak init %s --repo %s\n", o.Name, o.Repo)
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
func finalizeHint(out io.Writer, o *clusterinit.InitOptions) {
	fmt.Fprintf(out, "[finalize] run these once the platform reconciles:\n")
	fmt.Fprintf(out, "[finalize]   kube-dc bootstrap openbao init %s --repo %s\n", o.Name, o.Repo)
	fmt.Fprintf(out, "[finalize]   kube-dc bootstrap keycloak init %s --repo %s\n", o.Name, o.Repo)
}

// timeNow / timeAfter are indirections so finalize tests don't sleep in
// real time.
var (
	timeNow   = time.Now
	timeAfter = time.After
)
