package ports

import (
	"context"
	"time"
)

// FluxClient is the contract for everything Flux-CD-related the bootstrap
// engine needs: install Flux in a cluster, watch Kustomization /
// HelmRelease conditions, trigger a reconcile.
//
// The real adapter (M0-T06) wraps the `flux` CLI for `Bootstrap` (no
// in-process equivalent that's stable across releases) and client-go for
// the watch + reconcile paths. Mock adapter (M0-T02) replays event
// sequences from a scenario YAML.
//
// Watch methods return channels that close when ctx is done or the
// underlying watch ends. Callers MUST drain until close.
type FluxClient interface {
	// Bootstrap runs the equivalent of `flux bootstrap github` against
	// the configured kubeconfig (in opts). Synchronous; returns once
	// flux-system pods are Ready or ctx times out.
	//
	// Today this shells out to `flux` via ScriptRunner (matches v1
	// engineering plan §5 — flux is too feature-rich to wrap natively
	// in v1). v2 may swap to fluxcd/pkg/runtime if the surface stabilises.
	Bootstrap(ctx context.Context, opts FluxBootstrapOpts) error

	// WatchKustomizations streams condition changes for every
	// Kustomization in flux-system. Each event reflects the *current*
	// state of one Kustomization at the time of observation (not a
	// delta). The channel closes when ctx is cancelled or the watch
	// errors fatally; transient errors trigger a re-watch internally.
	WatchKustomizations(ctx context.Context) (<-chan KustomizationEvent, error)

	// WatchHelmReleases streams the same shape for HelmReleases (M5
	// status pane + M7 waterfall consume this).
	WatchHelmReleases(ctx context.Context) (<-chan HelmReleaseEvent, error)

	// Reconcile triggers a one-shot reconcile of the named resource.
	// `kind` is "Kustomization" or "HelmRelease". Equivalent to
	// `flux reconcile kustomization <name> --with-source`.
	Reconcile(ctx context.Context, kind, name string) error

	// PullArtifact downloads and extracts a Flux OCI artifact
	// (`flux pull artifact --url <url> --output <dir>`) into dir.
	// Used by `bootstrap init --fleet-mode=new-repo` to fetch the
	// fleet-starter bundle (oci://ghcr.io/kube-dc/fleet-starter:<ver>)
	// when the operator's --repo dir doesn't already carry the shared
	// fleet trees. Anonymous pull — the starter artifact is public;
	// no registry credentials are read or required.
	PullArtifact(ctx context.Context, url, dir string) error
}

// FluxBootstrapOpts carries everything `flux bootstrap github` needs.
// Field names match the flux CLI flags one-to-one where possible.
type FluxBootstrapOpts struct {
	// GitHubOwner + GitHubRepo identify the fleet repo on GitHub.
	GitHubOwner string
	GitHubRepo  string

	// Path is the cluster overlay path inside the repo
	// (e.g. "clusters/cloud").
	Path string

	// Token is the GitHub PAT. Required. The adapter never logs it.
	// In v1 this is sourced from `gh auth token` (per
	// flux-install.sh:51-61) — the CLI passes the resolved token here.
	Token string

	// Personal true → `flux bootstrap github --personal`. Use for
	// individual-account repos, omit for org-account repos.
	Personal bool

	// Branch defaults to "main" when empty.
	Branch string

	// Kubeconfig is the path to use during `flux bootstrap`. Transient
	// — set $KUBECONFIG for the duration of the call and never persist.
	Kubeconfig string
}

// KustomizationEvent reflects the current Ready/Reconciling condition of
// one flux-system Kustomization. The TUI's Phase 5 waterfall (M7 T7)
// builds a dependsOn tree from these.
type KustomizationEvent struct {
	Name        string
	Namespace   string
	DependsOn   []string  // names of Kustomizations this depends on
	Ready       bool      // condition Ready == True
	Reconciling bool      // condition Reconciling == True
	Reason      string    // last condition reason (e.g. "Reconciliation succeeded")
	Message     string    // last condition message
	Revision    string    // applied revision (commit SHA-ish)
	LastApplied time.Time // last successful apply
	LastAttempt time.Time // last reconcile attempt
}

// HelmReleaseEvent is the same shape for HelmReleases. The dependsOn
// graph for HRs is implicit (via the parent Kustomization), so HRs
// don't carry it explicitly here — the TUI groups HRs under their
// Kustomization in the waterfall view.
type HelmReleaseEvent struct {
	Name         string
	Namespace    string
	Ready        bool
	Reconciling  bool
	Reason       string
	Message      string
	ChartName    string
	ChartVersion string
	LastApplied  time.Time
	LastAttempt  time.Time
}
