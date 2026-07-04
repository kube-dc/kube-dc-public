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
//   - Git commit with a descriptive message.
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

	// Out is where redacted stdout + status lines go. nil = ioutil.Discard.
	Out io.Writer
}

// ErrMissingDependency surfaces on missing/nil adapter or empty
// option. Matches the [[port-validation]] pattern used by openbao
// + clusterinit engines.
var ErrMissingDependency = errors.New("keycloak: init missing required dependency")

// Init invokes bootstrap/setup-keycloak-oidc.sh via the script
// runner. The script is fully idempotent — re-running on a cluster
// where all OIDC clients are already configured produces no Git
// diff. Returns nil on script-exit-zero, a wrapped error otherwise.
func Init(ctx context.Context, opts InitOptions) error {
	if err := validate(opts); err != nil {
		return err
	}
	out := opts.Out
	if out == nil {
		out = io.Discard
	}

	fmt.Fprintf(out, "[keycloak] running bootstrap/setup-keycloak-oidc.sh for %s\n", opts.ClusterName)

	// No env or sentinel callback — the script's stdout is purely
	// human-readable progress + a `git commit` at the end. Secrets
	// are surfaced via SOPS-encrypted file mutations, not stdout.
	lines, err := opts.Runner.Run(ctx, ports.ScriptSetupKeycloakOIDC, nil, opts.ClusterName)
	if err != nil {
		return fmt.Errorf("keycloak init: start setup-keycloak-oidc.sh: %w", err)
	}

	exit := 0
	for ln := range lines {
		if ln.Stream == ports.StreamExit {
			n, perr := parseExitCode(ln.Text)
			if perr != nil {
				return fmt.Errorf("keycloak init: parse exit code %q: %w", ln.Text, perr)
			}
			exit = n
			continue
		}
		// Forward non-exit lines through to the caller's Out. The
		// script doesn't echo secret material to stdout — only the
		// names of resources it touches + the SOPS commit hash.
		fmt.Fprintf(out, "[keycloak-init %s] %s\n", ln.Stream, ln.Text)
	}
	if exit != 0 {
		return fmt.Errorf("keycloak init: setup-keycloak-oidc.sh exit=%d", exit)
	}
	fmt.Fprintf(out, "[keycloak] init complete — flux-web + grafana + kube-dc-admin clients configured, secrets committed.\n")
	fmt.Fprintf(out, "[keycloak] next: flux reconcile kustomization platform --with-source && kubectl rollout restart deploy/{flux-operator,prom-operator-grafana}\n")
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
	return nil
}

// parseExitCode pulls the integer exit code out of a StreamExit
// line's Text. Mirrors the pattern in openbao/init.go.
func parseExitCode(s string) (int, error) {
	n := 0
	for _, c := range s {
		if c < '0' || c > '9' {
			return 0, fmt.Errorf("not a number")
		}
		n = n*10 + int(c-'0')
	}
	return n, nil
}
