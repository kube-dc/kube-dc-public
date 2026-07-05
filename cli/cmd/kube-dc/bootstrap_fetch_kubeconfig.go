package main

import (
	"fmt"
	"strings"

	"github.com/spf13/cobra"

	"github.com/shalb/kube-dc/cli/internal/bootstrap"
	"github.com/shalb/kube-dc/cli/internal/bootstrap/clusterinit"
	"github.com/shalb/kube-dc/cli/internal/bootstrap/ports"
)

// bootstrapFetchKubeconfigCmd registers `kube-dc bootstrap
// fetch-kubeconfig <cluster>` (M4-T06). Auto-pulls the RKE2 admin
// kubeconfig from a control-plane node over SSH, rewrites the
// server URL to the operator's public FQDN, and merges into the
// local kubeconfig.
//
// Distinct from `kube-dc bootstrap kubeconfig <cluster>`:
//
//   - `kubeconfig` generates an OIDC-exec-plugin template from the
//     fleet repo (per-operator identity, no shared creds).
//   - `fetch-kubeconfig` pulls the actual RKE2 admin kubeconfig
//     off the master node's disk (cluster-admin bearer token,
//     ONE identity, meant for platform operators finishing a
//     fresh install before OIDC is wired).
//
// Operators typically use `fetch-kubeconfig` ONCE right after
// `bootstrap init` to bootstrap kubectl access, then switch to
// `kubeconfig` + `kube-dc login` for day-2 operations.
func bootstrapFetchKubeconfigCmd(fleetRepo *string) *cobra.Command {
	var (
		sshHost    string
		domain     string
		remotePath string
		destPath   string
		setCurrent bool
	)
	cmd := &cobra.Command{
		Use:           "fetch-kubeconfig <cluster>",
		Short:         "Pull the RKE2 admin kubeconfig from a master node over SSH (M4-T06)",
		SilenceErrors: true,
		SilenceUsage:  true,
		Long: `Fetches /etc/rancher/rke2/rke2.yaml from the specified control-plane
host via SSH, rewrites the server URL from 127.0.0.1:6443 to
kube-api.<domain>:6443, renames the RKE2 "default" cluster/user/
context to <cluster>, and merges the result into the operator's
local kubeconfig.

Distinct from 'kube-dc bootstrap kubeconfig <cluster>' which
generates an OIDC-exec-plugin template — this command pulls the
actual bearer-token admin kubeconfig off the master's disk. Use
it ONCE right after 'bootstrap init' to bootstrap kubectl access;
switch to 'kubeconfig' + 'kube-dc login' for day-2.

Required flags:
  --ssh-host <endpoint>  Control-plane host: user@host or a
                         ~/.ssh/config Host alias.
  --domain <fqdn>        Cluster public FQDN — server URL is
                         rewritten to https://kube-api.<domain>:6443.

Kubeconfig destination:
  --kubeconfig <path>    Write to this path (default: $KUBECONFIG
                         first entry, else ~/.kube/config).
  --set-current          Also set current-context to <cluster>
                         after merging. Default: preserve the
                         operator's existing current-context.

Upsert semantics: entries with the same name as an existing one
in the destination kubeconfig are REPLACED (matches 'kubectl
config set-cluster' behaviour). Other entries preserved. Atomic
write via temp + rename so a mid-write crash leaves the file
intact.

SSH auth: ssh-agent first, then ~/.ssh/config IdentityFile. Never
accepts a --ssh-key flag — operators put keys in their ssh config.`,
		Example: `  # Standard flow after bootstrap init
  kube-dc bootstrap fetch-kubeconfig cloud \
    --ssh-host operator@master.cloud.example.com --domain cloud.example.com

  # Alternative: use a Host alias from ~/.ssh/config
  kube-dc bootstrap fetch-kubeconfig acme --ssh-host acme-master --domain acme.com

  # Write to a specific kubeconfig file (not ~/.kube/config)
  kube-dc bootstrap fetch-kubeconfig acme --ssh-host acme-master \
    --domain acme.com --kubeconfig ./acme-admin.yaml`,
		Args: cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			clusterName := args[0]
			if sshHost == "" {
				return fmt.Errorf("bootstrap fetch-kubeconfig: --ssh-host is required (operator@master or a ~/.ssh/config alias)")
			}
			if domain == "" {
				return fmt.Errorf("bootstrap fetch-kubeconfig: --domain is required (rewrites 127.0.0.1:6443 → kube-api.<domain>:6443)")
			}

			// SSH-only session — this subcommand's canonical use is
			// the fresh-laptop first-contact flow (no kubeconfig on
			// the operator's machine yet; the whole point of the
			// command is to CREATE it). NewSession's k8s.New()
			// would fail on that path; NewSSHOnly skips k8s
			// entirely. See wire.go for the factory.
			sshClient, err := bootstrap.NewSSHOnly()
			if err != nil {
				fmt.Fprintf(cmd.ErrOrStderr(), "bootstrap fetch-kubeconfig: build ssh adapter: %v\n", err)
				return &doctorExitCodeErr{code: 2}
			}

			// Resolve SSH host. The --ssh-host arg accepts either a
			// bare alias ("master") or a user@host form
			// ("operator@10.0.0.1"). Split the two shapes so the
			// port adapter sees clean fields.
			host := parseSSHHostArg(sshHost)

			// Resolve destination path.
			target := destPath
			if target == "" {
				target = clusterinit.DefaultKubeconfigPath()
			}
			if target == "" {
				return fmt.Errorf("bootstrap fetch-kubeconfig: could not resolve kubeconfig destination (pass --kubeconfig <path>)")
			}

			out := cmd.OutOrStdout()
			cfg, err := clusterinit.FetchKubeconfig(cmd.Context(), clusterinit.FetchKubeconfigOptions{
				SSH:         sshClient,
				Host:        host,
				ClusterName: clusterName,
				Domain:      domain,
				RemotePath:  remotePath,
				Out:         out,
			})
			if err != nil {
				return fmt.Errorf("bootstrap fetch-kubeconfig: %w", err)
			}
			if err := clusterinit.MergeKubeconfig(target, cfg, setCurrent); err != nil {
				return fmt.Errorf("bootstrap fetch-kubeconfig: %w", err)
			}
			fmt.Fprintf(out, "[fetch-kubeconfig] merged into %s\n", target)
			if setCurrent {
				fmt.Fprintf(out, "[fetch-kubeconfig] current-context set to %s\n", cfg.CurrentContext)
			} else {
				fmt.Fprintf(out, "[fetch-kubeconfig] existing current-context preserved; use `kubectl config use-context %s` to switch\n", clusterName)
			}
			return nil
		},
	}
	cmd.Flags().StringVar(&sshHost, "ssh-host", "",
		"SSH endpoint of a control-plane node — `user@host` or a ~/.ssh/config Host alias (required)")
	cmd.Flags().StringVar(&domain, "domain", "",
		"Cluster public FQDN suffix — server URL rewritten to https://kube-api.<domain>:6443 (required)")
	cmd.Flags().StringVar(&remotePath, "remote-path", "",
		"Override the default /etc/rancher/rke2/rke2.yaml path on the master (uncommon)")
	cmd.Flags().StringVar(&destPath, "kubeconfig", "",
		"Destination kubeconfig path (default: $KUBECONFIG first entry, else ~/.kube/config)")
	cmd.Flags().BoolVar(&setCurrent, "set-current", false,
		"Set current-context to <cluster> after merging (default: preserve existing)")
	return cmd
}

// parseSSHHostArg splits `user@host` into an SSHHost struct. When
// there's no `@`, the whole string becomes the Alias (which the
// port adapter resolves via ~/.ssh/config). Empty user defaults to
// the adapter's fallback (`root` per port docs).
func parseSSHHostArg(raw string) ports.SSHHost {
	if i := strings.Index(raw, "@"); i > 0 {
		return ports.SSHHost{
			User:     raw[:i],
			Hostname: raw[i+1:],
		}
	}
	return ports.SSHHost{Alias: raw}
}
