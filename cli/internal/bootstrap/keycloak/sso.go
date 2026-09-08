package keycloak

import (
	"context"
	"fmt"
	"io"

	"github.com/shalb/kube-dc/cli/internal/bootstrap/ports"
)

// SSOOptions drives SSORealm — the self-service sign-up ceremony. Same
// engine shape as InitOptions: the canonical bootstrap/setup-sso-realm.sh
// owns the Keycloak REST dance, the SOPS mutations, the ordering invariant
// (realm first, SSO_ENABLED=true only after every realm step succeeded)
// and the commit; this engine owns the push and the streaming.
type SSOOptions struct {
	ClusterName string
	FleetRepo   string
	Runner      ports.ScriptRunner
	Git         ports.GitClient
	GitHubToken string
	NoPush      bool
	Out         io.Writer

	// Env carries the script's inputs. SECRETS (SMTP_USER, SMTP_PASSWORD,
	// GOOGLE_CLIENT_SECRET, SSO_BROKER_SECRET) ride here and only here —
	// never argv, never flags — mirroring the DNS-01 token discipline.
	// The ScriptRunner redacts secret-matching keys from its logs.
	Env map[string]string
}

// SSORealm provisions the central `sso` realm (email self-registration +
// verification; Google IdP only when the GOOGLE_* pair is present in Env)
// and persists the resulting config in the fleet overlay. Idempotent:
// re-running on a provisioned cluster converges with no Git diff, and the
// stored SSO_BROKER_SECRET is never rotated implicitly.
func SSORealm(ctx context.Context, opts SSOOptions) error {
	if err := validate(InitOptions{
		ClusterName: opts.ClusterName,
		FleetRepo:   opts.FleetRepo,
		Runner:      opts.Runner,
		Git:         opts.Git,
	}); err != nil {
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
			return fmt.Errorf("keycloak sso: read fleet HEAD before ceremony: %w", err)
		}
	}

	fmt.Fprintf(out, "[keycloak] running bootstrap/setup-sso-realm.sh for %s\n", opts.ClusterName)

	lines, err := opts.Runner.Run(ctx, ports.ScriptSetupSSORealm, opts.Env, opts.ClusterName)
	if err != nil {
		return fmt.Errorf("keycloak sso: start setup-sso-realm.sh: %w", err)
	}
	exit, err := ports.Drain(lines, func(ln ports.Line) {
		fmt.Fprintf(out, "[keycloak-sso %s] %s\n", ln.Stream, ln.Text)
	})
	if err != nil {
		return fmt.Errorf("keycloak sso: %w", err)
	}
	if exit != 0 {
		return fmt.Errorf("keycloak sso: setup-sso-realm.sh exit=%d", exit)
	}
	if opts.NoPush {
		fmt.Fprintln(out, "[keycloak] sso realm complete — fleet config committed locally (--no-push)")
	} else {
		postSHA, err := opts.Git.Head(ctx, opts.FleetRepo)
		if err != nil {
			return fmt.Errorf("keycloak sso: read fleet HEAD after ceremony (local commit may need manual push): %w", err)
		}
		if postSHA == preSHA {
			fmt.Fprintln(out, "[keycloak] sso realm complete — already converged; no new commit to push")
		} else {
			if err := opts.Git.Push(ctx, opts.FleetRepo, opts.GitHubToken); err != nil {
				return fmt.Errorf("keycloak sso: push fleet config (local commit preserved): %w", err)
			}
			fmt.Fprintln(out, "[keycloak] sso realm complete — realm live, SMTP + broker secret SOPS-committed and pushed")
		}
	}
	fmt.Fprintln(out, "[keycloak] next: flux reconcile kustomization flux-system --with-source && flux reconcile kustomization platform")
	fmt.Fprintln(out, "[keycloak]       (rolls the manager/backend with SSO_ENABLED=true; the console then offers email sign-up)")
	return nil
}
