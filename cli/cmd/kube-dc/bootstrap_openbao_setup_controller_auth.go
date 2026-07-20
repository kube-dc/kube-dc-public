package main

import (
	"fmt"
	"os"
	"path/filepath"

	"github.com/spf13/cobra"

	"github.com/shalb/kube-dc/cli/internal/bootstrap"
	"github.com/shalb/kube-dc/cli/internal/bootstrap/openbao"
	"github.com/shalb/kube-dc/cli/internal/bootstrap/secrets"
)

// bootstrapOpenBaoSetupControllerAuthCmd registers `kube-dc bootstrap
// openbao setup-controller-auth <cluster-name>` (M5-T08).
//
// Replaces hack/openbao-setup-controller-auth.sh. Idempotent setup of:
//   - auth/k8s-host (Kubernetes auth mount)
//   - kube-dc-controller-manager policy + role
//   - db-manager policy + role
//
// Operationally this is invoked:
//   - For partial-init recovery (init's Phase C bailed after share
//     custody but before annotate). Init's recovery message points here.
//   - For clusters already initialised before M5-T08 shipped (one-time
//     migration off the shell script).
//   - For policy + role refresh after a kube-dc chart upgrade extends
//     the HCL or rotates SA bindings (`--refresh-policy`).
//
// Token discipline: decrypts 3-of-5 shares, runs GenerateRoot in-memory,
// defers RevokeSelf immediately, then delegates to the same engine
// function (openbao.SetupControllerAuth) that init Phase C uses. Root
// token never touches disk on the operator's machine, never appears in
// argv, never logged.
func bootstrapOpenBaoSetupControllerAuthCmd(fleetRepo *string) *cobra.Command {
	var refreshPolicy bool
	cmd := &cobra.Command{
		Use:   "setup-controller-auth <cluster-name>",
		Short: "Provision OpenBao Kubernetes-auth policies and workload roles",
		Long: `Idempotent setup of OpenBao's Kubernetes-auth mount + the
controller, database-manager, and snapshot policies and roles. Replaces
hack/openbao-setup-controller-auth.sh.

Reads 3-of-5 Shamir shares from clusters/<name>/secrets.enc.yaml, runs
the OpenBao generate-root ceremony in-memory to obtain a single-use
root token, then writes:

  - kubernetes auth method at path k8s-host (configured against the
    in-cluster apiserver via the kube-root-ca.crt ConfigMap)
  - policy kube-dc-controller-manager (cross-Org admin)
  - role   kube-dc-controller-manager bound to kube-dc-manager SA
  - policy db-manager (M3-T05 envelope encryption + M4-T01 database engine)
  - role   db-manager bound to kube-dc-db-manager SA
  - annotation kube-dc.com/openbao-controller-auth-installed on svc/openbao
  - policy/role openbao-snapshot bound to openbao/openbao-snapshot SA
    (15-minute no-default-policy token; Raft snapshot read only)

The root token is deferred-revoked the moment it leaves the
generate-root call — never written to the operator's disk, never
logged, never on argv.

Use:
  - For partial-init recovery (init Phase C failed after share custody).
  - For clusters already initialised before this command shipped.
  - With --refresh-policy after a chart upgrade extends the HCL or
    rotates SA bindings (rewrites policies AND roles, skips
    auth-enable + auth-configure).`,
		Example: `  # First-time setup on a cluster already initialised by the legacy shell script
  kube-dc bootstrap openbao setup-controller-auth eu/dc1 --repo ~/projects/kube-dc-fleet

  # Refresh policies + roles after a chart upgrade
  kube-dc bootstrap openbao setup-controller-auth eu/dc1 --repo ~/projects/kube-dc-fleet --refresh-policy

  # Recovery after init's Phase C bailed (recovery message points here)
  kube-dc bootstrap openbao setup-controller-auth eu/dc1 --repo ~/projects/kube-dc-fleet`,
		Args:          cobra.ExactArgs(1),
		SilenceErrors: true,
		SilenceUsage:  true,
		RunE: func(cmd *cobra.Command, args []string) error {
			clusterName := args[0]
			repo := ""
			if fleetRepo != nil {
				repo = *fleetRepo
			}
			if repo == "" {
				return fmt.Errorf("openbao setup-controller-auth: --repo (or $KUBE_DC_FLEET) is required so the engine can find clusters/%s/secrets.enc.yaml", clusterName)
			}

			session, err := bootstrap.NewSession(bootstrap.Options{FleetRepoPath: repo})
			if err != nil {
				return fmt.Errorf("openbao setup-controller-auth: build session: %w", err)
			}
			if session != nil {
				defer session.Close()
			}
			out := cmd.OutOrStdout()

			// (1) Validate the fleet overlay exists.
			secretsPath := filepath.Join(repo, "clusters", clusterName, "secrets.enc.yaml")
			if _, err := os.Stat(secretsPath); err != nil {
				return fmt.Errorf("openbao setup-controller-auth: secrets.enc.yaml not found at %s: %w", secretsPath, err)
			}

			// (2) Decrypt the SOPS file + extract 3 threshold shares.
			//     Reuses the unseal engine's helper so we get identical
			//     parser tolerance (CR/LF, optional quotes, both flat
			//     and stringData layouts).
			decrypted, err := session.SOPS.Decrypt(cmd.Context(), secretsPath)
			if err != nil {
				return fmt.Errorf("openbao setup-controller-auth: decrypt %s: %w", secretsPath, err)
			}
			defer func() {
				for i := range decrypted {
					decrypted[i] = 0
				}
			}()
			shares, err := openbao.ExtractThresholdShares(decrypted)
			if err != nil {
				return fmt.Errorf("openbao setup-controller-auth: %w", err)
			}
			defer func() {
				for _, s := range shares {
					for i := range s {
						s[i] = 0
					}
				}
			}()

			// (3) Generate a temporary root token from the shares.
			//     Defer RevokeSelf IMMEDIATELY so any subsequent error
			//     unwinds with revocation still guaranteed.
			fmt.Fprintln(out, "[openbao] running generate-root ceremony (single-use token)")
			tok, err := session.OpenBao.GenerateRoot(cmd.Context(), shares)
			if err != nil {
				return fmt.Errorf("openbao setup-controller-auth: generate-root: %w", err)
			}
			buf := secrets.NewBuffer()
			defer buf.Scrub()
			// We hold tok in a buf so the outer Scrub() catches it on
			// any path including panics. The defer below revokes via
			// the adapter before Scrub() zeroes the bytes.
			buf.SetRootToken(tok)
			// Caller scrubs the slice the adapter returned us — drop
			// our local reference so only buf's copy is alive.
			for i := range tok {
				tok[i] = 0
			}
			defer func() {
				rtok, _ := buf.RootToken()
				if len(rtok) == 0 {
					return
				}
				if rerr := session.OpenBao.RevokeSelf(cmd.Context(), rtok); rerr != nil {
					fmt.Fprintf(out, "[openbao] WARNING: root token revoke failed: %v — manual remediation may be required\n", rerr)
				}
			}()

			// (4) Resolve refresh mode.
			mode := openbao.RefreshFull
			if refreshPolicy {
				mode = openbao.RefreshPolicy
			}

			// (5) Delegate to the shared engine (same path init Phase C uses).
			rtok, _ := buf.RootToken()
			if err := openbao.SetupControllerAuth(cmd.Context(), openbao.SetupControllerAuthOptions{
				Token:       rtok,
				RefreshMode: mode,
				OpenBao:     session.OpenBao,
				K8s:         session.K8s,
				Out:         out,
			}); err != nil {
				return fmt.Errorf("openbao setup-controller-auth: %w", err)
			}
			return nil
		},
	}
	cmd.Flags().BoolVar(&refreshPolicy, "refresh-policy", false,
		"Skip auth-enable + auth-configure; only rewrite policies AND roles. Use after a chart upgrade extends the HCL or rotates role params.")
	return cmd
}
