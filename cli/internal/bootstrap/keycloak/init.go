// Package keycloak owns the D'-1 ceremony: register the master-realm
// OIDC clients (flux-web, grafana, kube-dc-admin) and SOPS-encrypt
// their client secrets into the cluster overlay's secrets.enc.yaml.
//
// **Current implementation** wraps the canonical
// kube-dc-fleet/bootstrap/setup-keycloak-oidc.sh shell script via
// ports.ScriptRunner. The script already owns:
//
//   - Idempotent client create-or-update with the right shape
//     (confidential vs PKCE, redirect URIs, default scopes).
//   - 'groups' client scope + group-membership mapper.
//   - Surgical `sops --set` updates so only the rotated keys get a
//     new IV/tag — every other ENC token stays byte-identical and
//     the diff is reviewable.
//   - Git commit with a descriptive message (the Go engine pushes it).
//
// **Future work** ports the logic to a pure-Go REST client + the
// SOPS adapter (same shape as the OpenBao engine), gaining the
// same testing surface as clusterinit. The shell-out is the
// shortest path that gives operators a unified
// `kube-dc bootstrap keycloak init <cluster>` UX today.
package keycloak

import (
	"context"
	"errors"
	"fmt"
	"io"

	"github.com/shalb/kube-dc/cli/internal/bootstrap/ports"
)

// InitOptions is the parameter bundle for Init. Mirrors the shape
// of openbao.InitOptions for consistency across bootstrap engines.
type InitOptions struct {
	// ClusterName is the per-cluster overlay directory under
	// clusters/<name>/ in the fleet repo. The script uses this to
	// locate secrets.enc.yaml + cluster-config.env.
	ClusterName string

	// FleetRepo is the absolute path to the fleet checkout. The
	// script reads cluster-config.env from clusters/<name>/ inside.
	FleetRepo string

	// Runner is the ports.ScriptRunner adapter; production wires
	// this to the script-adapter, tests wire to a fake.
	Runner ports.ScriptRunner

	// Git pushes the script-created commit to the remote Flux watches.
	Git ports.GitClient

	// GitHubToken is passed to the transport adapter; empty is valid for SSH.
	GitHubToken string

	// NoPush leaves the script-created commit local for explicit test workflows.
	NoPush bool

	// Out is where redacted stdout + status lines go. nil = io.Discard.
	Out io.Writer
}

// ErrMissingDependency surfaces on missing/nil adapter or empty
// option. Matches the [[port-validation]] pattern used by openbao
// + clusterinit engines.
var ErrMissingDependency = errors.New("keycloak: init missing required dependency")

// Init invokes bootstrap/setup-keycloak-oidc.sh via the script
// runner. The script is fully idempotent — re-running on a cluster
// where all OIDC clients are already configured produces no Git
// diff. The engine snapshots HEAD before the script and pushes only when the
// script created a new commit, unless NoPush is set; failures preserve the
// local commit for explicit recovery.
func Init(ctx context.Context, opts InitOptions) error {
	if err := validate(opts); err != nil {
		return err
	}
	out := opts.Out
	if out == nil {
		out = io.Discard
	}
	var preSHA string
	if !opts.NoPush {
		var err error
		preSHA, err = opts.Git.Head(ctx, opts.FleetRepo)
		if err != nil {
			return fmt.Errorf("keycloak init: read fleet HEAD before ceremony: %w", err)
		}
	}

	fmt.Fprintf(out, "[keycloak] running bootstrap/setup-keycloak-oidc.sh for %s\n", opts.ClusterName)

	// No env or sentinel callback — the script's stdout is purely
	// human-readable progress + a `git commit` at the end. Secrets
	// are surfaced via SOPS-encrypted file mutations, not stdout.
	lines, err := opts.Runner.Run(ctx, ports.ScriptSetupKeycloakOIDC, nil, opts.ClusterName)
	if err != nil {
		return fmt.Errorf("keycloak init: start setup-keycloak-oidc.sh: %w", err)
	}

	// Forward non-exit lines through to the caller's Out. The script
	// doesn't echo secret material to stdout — only the names of
	// resources it touches + the SOPS commit hash.
	//
	// ports.Drain enforces the ScriptRunner contract. This loop used to
	// default exit to 0, so a stream that ended without an exit record
	// printed "init complete" and reported the OIDC clients configured
	// and the secrets committed on the strength of output we never got.
	exit, err := ports.Drain(lines, func(ln ports.Line) {
		fmt.Fprintf(out, "[keycloak-init %s] %s\n", ln.Stream, ln.Text)
	})
	if err != nil {
		return fmt.Errorf("keycloak init: %w", err)
	}
	if exit != 0 {
		return fmt.Errorf("keycloak init: setup-keycloak-oidc.sh exit=%d", exit)
	}
	if opts.NoPush {
		fmt.Fprintln(out, "[keycloak] init complete — secrets committed locally (--no-push)")
	} else {
		postSHA, err := opts.Git.Head(ctx, opts.FleetRepo)
		if err != nil {
			return fmt.Errorf("keycloak init: read fleet HEAD after ceremony (local commit may need manual push): %w", err)
		}
		if postSHA == preSHA {
			fmt.Fprintln(out, "[keycloak] init complete — clients already converged; no new commit to push")
		} else {
			if err := opts.Git.Push(ctx, opts.FleetRepo, opts.GitHubToken); err != nil {
				return fmt.Errorf("keycloak init: push durable OIDC config (local commit preserved; live backend Secret already materialized): %w", err)
			}
			fmt.Fprintln(out, "[keycloak] init complete — clients configured; encrypted secrets + chart wiring committed and pushed")
		}
	}
	fmt.Fprintln(out, "[keycloak] next: flux reconcile kustomization flux-system --with-source && flux reconcile kustomization addons && flux reconcile kustomization platform")
	return nil
}

func validate(opts InitOptions) error {
	if opts.ClusterName == "" {
		return fmt.Errorf("%w: ClusterName", ErrMissingDependency)
	}
	if opts.FleetRepo == "" {
		return fmt.Errorf("%w: FleetRepo", ErrMissingDependency)
	}
	if opts.Runner == nil {
		return fmt.Errorf("%w: Runner", ErrMissingDependency)
	}
	if !opts.NoPush && opts.Git == nil {
		return fmt.Errorf("%w: Git", ErrMissingDependency)
	}
	return nil
}
