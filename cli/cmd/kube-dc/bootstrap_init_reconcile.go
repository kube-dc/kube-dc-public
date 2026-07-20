package main

import (
	"context"
	"encoding/base64"
	"fmt"
	"io"
	"sort"
	"strings"
	"time"

	"gopkg.in/yaml.v3"

	"github.com/shalb/kube-dc/cli/internal/bootstrap/clusterinit"
	"github.com/shalb/kube-dc/cli/internal/bootstrap/openbao"
	"github.com/shalb/kube-dc/cli/internal/bootstrap/ports"
)

// Reconcile-tracking + access-summary + resume helpers for the
// post-apply phase. Features: "track reconciliation with flux",
// "access to admin + keycloak password + SSO", "restart/retry step".

// reconcile tunables (vars so tests can shrink them).
var (
	reconcileBudget      = 20 * time.Minute
	reconcileReportEvery = 15 * time.Second
	// reconcileTarget is the flux-system Kustomization whose Ready
	// condition means the platform has converged (it dependsOn the
	// infra layers + health-checks the platform HelmReleases).
	reconcileTarget = "platform"
)

// fluxWatcher is the narrow slice of ports.FluxClient the reconcile watch
// needs (kept small so tests use a tiny fake).
type fluxWatcher interface {
	WatchKustomizations(ctx context.Context) (<-chan ports.KustomizationEvent, error)
	WatchHelmReleases(ctx context.Context) (<-chan ports.HelmReleaseEvent, error)
}

// sopsDecrypter is the one method the access summary needs from
// ports.SOPSClient (kept narrow so tests fake a single method).
type sopsDecrypter interface {
	Decrypt(ctx context.Context, path string) ([]byte, error)
}

// runReconcileWatch is the StepReconcile milestone: it watches Flux
// converge the platform, emitting a live "N/M HelmReleases Ready | <per-
// Kustomization>" tally to the log pane, and returns once the target
// Kustomization is Ready. On budget expiry it returns a soft error
// (milestone ✗, "still reconciling") — the flow continues to the finalize
// steps regardless, since the cluster is up and Flux keeps reconciling.
func runReconcileWatch(ctx context.Context, out io.Writer, flux fluxWatcher, rep clusterinit.StepReporter) {
	_ = runReconcileWatchWithGPU(ctx, out, flux, rep, clusterinit.GPUConfig{})
}

type gpuReconcileTarget struct {
	name string
	step clusterinit.StepID
}

func runReconcileWatchWithGPU(ctx context.Context, out io.Writer, flux fluxWatcher, rep clusterinit.StepReporter, gpu clusterinit.GPUConfig) error {
	targets := gpuReconcileTargets(gpu)
	completed := make(map[clusterinit.StepID]bool, len(targets))
	rep.Start(clusterinit.StepReconcile)
	for _, target := range targets {
		rep.Start(target.step)
	}
	required := make([]string, 0, len(targets))
	onReady := func(name string) {
		for _, target := range targets {
			if target.name == name && !completed[target.step] {
				completed[target.step] = true
				rep.Done(target.step, nil)
			}
		}
	}
	for _, target := range targets {
		required = append(required, target.name)
	}
	err := watchReconcileTargets(ctx, out, flux, reconcileBudget, required, onReady)
	rep.Done(clusterinit.StepReconcile, err)
	for _, target := range targets {
		if completed[target.step] {
			continue
		}
		terminalErr := err
		if terminalErr == nil {
			terminalErr = fmt.Errorf("GPU readiness target %s was not observed", target.name)
		}
		rep.Done(target.step, terminalErr)
	}
	return err
}

func gpuReconcileTargets(gpu clusterinit.GPUConfig) []gpuReconcileTarget {
	if gpu.Platform != clusterinit.GPUPlatformEnabled {
		return nil
	}
	targets := []gpuReconcileTarget{
		{name: "gpu-node-mode", step: clusterinit.StepGPUInventory},
		{name: "gpu-operator", step: clusterinit.StepGPUOperator},
	}
	if gpu.HAMiEnabled {
		targets = append(targets, gpuReconcileTarget{name: "hami", step: clusterinit.StepGPUHAMi})
	}
	return append(targets, gpuReconcileTarget{name: reconcileTarget, step: clusterinit.StepGPUProduct})
}

func watchReconcile(ctx context.Context, out io.Writer, flux fluxWatcher, budget time.Duration) error {
	return watchReconcileTargets(ctx, out, flux, budget, nil, nil)
}

func watchReconcileTargets(ctx context.Context, out io.Writer, flux fluxWatcher, budget time.Duration, required []string, onReady func(string)) error {
	wctx, cancel := context.WithTimeout(ctx, budget)
	defer cancel()

	ksCh, err := flux.WatchKustomizations(wctx)
	if err != nil {
		return fmt.Errorf("watch kustomizations: %w", err)
	}
	hrCh, err := flux.WatchHelmReleases(wctx)
	if err != nil {
		return fmt.Errorf("watch helmreleases: %w", err)
	}

	ks := map[string]bool{} // name -> Ready
	hr := map[string]bool{} // ns/name -> Ready
	ticker := time.NewTicker(reconcileReportEvery)
	defer ticker.Stop()

	lastReport := ""
	report := func() {
		total, ready := len(hr), 0
		for _, r := range hr {
			if r {
				ready++
			}
		}
		names := make([]string, 0, len(ks))
		for n := range ks {
			names = append(names, n)
		}
		sort.Strings(names)
		parts := make([]string, 0, len(ks))
		for _, n := range names {
			mark := "…"
			if ks[n] {
				mark = "✓"
			}
			parts = append(parts, n+mark)
		}
		line := fmt.Sprintf("HelmReleases %d/%d Ready | %s", ready, total, strings.Join(parts, " "))
		if line != lastReport {
			fmt.Fprintf(out, "[reconcile] %s\n", line)
			lastReport = line
		}
	}
	converged := func() bool {
		if !ks[reconcileTarget] {
			return false
		}
		for _, name := range required {
			if !ks[name] {
				return false
			}
		}
		return true
	}

	for {
		select {
		case <-wctx.Done():
			report()
			return checkConverged(converged, budget)
		case ev, ok := <-ksCh:
			if !ok {
				ksCh = nil
				if hrCh == nil {
					return checkConverged(converged, budget)
				}
				continue
			}
			ks[ev.Name] = ev.Ready
			if ev.Ready && onReady != nil {
				onReady(ev.Name)
			}
			report()
			if converged() {
				fmt.Fprintf(out, "[reconcile] platform converged — %q Kustomization Ready\n", reconcileTarget)
				return nil
			}
		case ev, ok := <-hrCh:
			if !ok {
				hrCh = nil
				if ksCh == nil {
					return checkConverged(converged, budget)
				}
				continue
			}
			hr[ev.Namespace+"/"+ev.Name] = ev.Ready
		case <-ticker.C:
			report()
			if converged() {
				return nil
			}
		}
	}
}

func checkConverged(converged func() bool, budget time.Duration) error {
	if converged() {
		return nil
	}
	return fmt.Errorf("platform still reconciling after %s (Flux continues in the background; check `kube-dc bootstrap status`)", budget)
}

// openBaoFinalized reports whether OpenBao is already initialized on this
// cluster (the openbao Service carries the bootstrap-finalized
// annotation). Makes the finalize phase resumable: openbao.Init is
// deliberately non-idempotent ("running twice is an error"), so on a
// re-run we SKIP it rather than fail. Any read error → not-finalized
// (safer to attempt than to wrongly skip on a fresh cluster).
func openBaoFinalized(ctx context.Context, k8s interface {
	GetServiceAnnotation(ctx context.Context, ns, svc, key string) (string, error)
}) bool {
	v, err := k8s.GetServiceAnnotation(ctx, "openbao", "openbao", openbao.AnnotationBootstrapFinalized)
	return err == nil && strings.TrimSpace(v) != ""
}

// --- access summary (Feature: admin access + keycloak password + SSO) ---

// buildAccessSummary renders the end-of-install access block: the
// Keycloak admin console + credentials and the SSO app URLs. The
// Keycloak admin password is read by SOPS-decrypting the cluster
// overlay's secrets.enc.yaml (no cluster call). This string is printed
// to the operator's TERMINAL after the install screen closes — NOT to
// the redacted transcript file (the password must not be persisted).
// accessBlock renders the end-of-install access block. Two axes:
//
//   - withPassword: include the ACTUAL Keycloak admin password (only
//     ever true on an interactive terminal — never on the plain/CI path,
//     where it would land in captured logs). When false, prints the
//     kubectl retrieval hint instead of the password.
//   - obDeferred / kcDeferred: whether OpenBao / Keycloak specifically
//     didn't complete in this run. When set, the header says SSO isn't
//     live yet and prints ONLY the rerun command(s) for the step(s) that
//     actually deferred — never telling the operator to rerun a step
//     that already succeeded.
func accessBlock(ctx context.Context, o *clusterinit.InitOptions, sops sopsDecrypter, withPassword, obDeferred, kcDeferred bool) string {
	d := o.Domain
	var b strings.Builder
	b.WriteString("\n══════════════════════════════ ACCESS ══════════════════════════════\n")
	if obDeferred || kcDeferred {
		b.WriteString(fmt.Sprintf("Cluster %q is installed, but a finalize step is still completing —\n", o.Name))
		b.WriteString("SSO below becomes available once it finishes. Re-run:\n")
		if obDeferred {
			b.WriteString(fmt.Sprintf("    kube-dc bootstrap openbao init %s --repo %s\n", o.Name, o.Repo))
		}
		if kcDeferred {
			b.WriteString(fmt.Sprintf("    kube-dc bootstrap keycloak init %s --repo %s\n", o.Name, o.Repo))
		}
		b.WriteString("\n")
	} else {
		b.WriteString(fmt.Sprintf("Cluster %q is installed. Authenticate through Keycloak SSO:\n\n", o.Name))
	}
	b.WriteString("  Keycloak admin console\n")
	b.WriteString(fmt.Sprintf("    URL:      https://login.%s/admin/master/console/\n", d))
	b.WriteString("    Realm:    master\n")
	b.WriteString("    User:     admin\n")
	if withPassword {
		pw, pwErr := readKeycloakAdminPassword(ctx, o, sops)
		if pw != "" {
			b.WriteString(fmt.Sprintf("    Password: %s\n", pw))
		} else {
			b.WriteString(fmt.Sprintf("    Password: (unavailable: %v)\n", pwErr))
			b.WriteString("              kubectl -n keycloak get secret keycloak -o jsonpath='{.data.admin-password}' | base64 -d\n")
		}
	} else {
		// Plain/CI path: never emit the password to captured output —
		// print the retrieval command instead.
		b.WriteString("    Password: kubectl -n keycloak get secret keycloak -o jsonpath='{.data.admin-password}' | base64 -d\n")
	}
	b.WriteString("\n  Then the same Keycloak identity (add your user to the 'admin' group) signs in to:\n")
	b.WriteString(fmt.Sprintf("    Console (kube-dc): https://console.%s\n", d))
	b.WriteString(fmt.Sprintf("    Grafana:           https://grafana.%s\n", d))
	b.WriteString(fmt.Sprintf("    Flux Web:          https://flux.%s\n", d))
	b.WriteString(fmt.Sprintf("    Admin console:     https://admin.%s\n", d))
	b.WriteString("═════════════════════════════════════════════════════════════════════\n")
	return b.String()
}

// readKeycloakAdminPassword decrypts clusters/<name>/secrets.enc.yaml and
// extracts KEYCLOAK_ADMIN_PASSWORD from stringData (plaintext) or data
// (base64). Returns ("", err) on any failure so the caller falls back to
// the kubectl hint.
func readKeycloakAdminPassword(ctx context.Context, o *clusterinit.InitOptions, sops sopsDecrypter) (string, error) {
	const key = "KEYCLOAK_ADMIN_PASSWORD"
	if o.Repo == "" {
		return "", fmt.Errorf("no fleet repo path")
	}
	path := fmt.Sprintf("%s/clusters/%s/secrets.enc.yaml", strings.TrimRight(o.Repo, "/"), o.Name)
	raw, err := sops.Decrypt(ctx, path)
	if err != nil {
		return "", fmt.Errorf("decrypt %s: %w", path, err)
	}
	var doc struct {
		Data       map[string]string `yaml:"data"`
		StringData map[string]string `yaml:"stringData"`
	}
	if err := yaml.Unmarshal(raw, &doc); err != nil {
		return "", fmt.Errorf("parse secret: %w", err)
	}
	if v, ok := doc.StringData[key]; ok && v != "" {
		return v, nil
	}
	if v, ok := doc.Data[key]; ok && v != "" {
		// Kubernetes Secret `data` MUST be base64 — invalid base64 is a
		// malformed secret, not a password. Surface the error so the
		// caller shows the kubectl retrieval hint instead of a garbage
		// "password".
		dec, derr := base64.StdEncoding.DecodeString(v)
		if derr != nil {
			return "", fmt.Errorf("data.%s is not valid base64: %w", key, derr)
		}
		return string(dec), nil
	}
	return "", fmt.Errorf("%s not found in %s", key, path)
}
