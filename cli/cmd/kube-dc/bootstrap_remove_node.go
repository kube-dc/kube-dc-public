package main

import (
	"bytes"
	"context"
	"fmt"
	"os"
	"os/exec"

	"github.com/spf13/cobra"

	"github.com/shalb/kube-dc/cli/internal/bootstrap"
	sshadapter "github.com/shalb/kube-dc/cli/internal/bootstrap/adapters/ssh"
	"github.com/shalb/kube-dc/cli/internal/bootstrap/noderemove"
	"github.com/shalb/kube-dc/cli/internal/bootstrap/ports"
)

// bootstrapRemoveNodeCmd registers `kube-dc bootstrap remove-node` — the
// safe teardown counterpart to `install --join-server`. It removes a node
// in the etcd-quorum-safe order (member remove FIRST, then cordon/drain/
// delete, then node-side rke2 teardown over SSH).
func bootstrapRemoveNodeCmd(fleetRepo *string) *cobra.Command {
	var (
		sshHost      string
		kubeconfig   string
		drainTimeout string
		uninstall    bool
		skipDrain    bool
		force        bool
		dryRun       bool
		yes          bool
		sshJump      string
		sshAcceptNew bool
	)
	cmd := &cobra.Command{
		Use:           "remove-node <node-name>",
		Short:         "Safely remove a node from a kube-dc cluster (etcd-quorum-aware)",
		SilenceErrors: true,
		SilenceUsage:  true,
		Long: `Removes a node from a live kube-dc cluster in the order that protects
etcd quorum:

  1. (control-plane/etcd node) etcd member remove  — FIRST, while healthy
  2. cordon + drain  (kubectl's own PDB-aware eviction)
  3. delete the node object
  4. node-side rke2 teardown over SSH (rke2-killall.sh, or
     rke2-uninstall.sh with --uninstall)

The ordering matters: deleting a control-plane node/VM WITHOUT first
removing its etcd member strands the member and, on a 2-member cluster,
breaks quorum the moment the node stops. remove-node encodes the safe
order so you can't trip it. It refuses to remove the last
control-plane/etcd node.

Cluster access uses your kubectl (KUBECONFIG / --kubeconfig). The
node-side teardown needs --ssh-host (skipped if omitted — run
rke2-killall.sh on the host yourself). This does NOT delete the VM/host —
that is your infrastructure to remove.

Without --yes this only prints the plan (a forced dry-run); re-run with
--yes to apply.`,
		Example: `  # Preview (prints the plan, changes nothing)
  kube-dc bootstrap remove-node worker-3 --ssh-host root@203.0.113.23

  # Remove a worker, then tear down rke2 on the host
  kube-dc bootstrap remove-node worker-3 --ssh-host root@203.0.113.23 --yes

  # Remove an additional control-plane (etcd member removed first)
  kube-dc bootstrap remove-node master-3 --ssh-host root@203.0.113.13 --yes

  # Through a bastion, full uninstall on the host
  kube-dc bootstrap remove-node worker-3 --ssh-host root@10.0.0.23 \
    --ssh-jump root@bastion.example.com --uninstall --yes`,
		Args: cobra.MaximumNArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			name := ""
			if len(args) == 1 {
				name = args[0]
			}
			if name == "" {
				return fmt.Errorf("bootstrap remove-node: a <node-name> argument is required")
			}
			kbin, err := exec.LookPath("kubectl")
			if err != nil {
				return fmt.Errorf("bootstrap remove-node: kubectl not found on $PATH (required to talk to the cluster): %w", err)
			}

			// Node-side teardown SSH is optional.
			var sshClient ports.SSHClient
			var node ports.SSHHost
			if sshHost != "" {
				var sshOpts []sshadapter.Option
				if sshAcceptNew {
					sshOpts = append(sshOpts, sshadapter.WithAcceptNewHostKeys())
				}
				sshClient, err = bootstrap.NewSSHOnly(sshOpts...)
				if err != nil {
					return fmt.Errorf("bootstrap remove-node: build ssh adapter: %w", err)
				}
				node = parseSSHHostArg(sshHost)
				if sshJump != "" {
					node.ProxyJump = sshJump
				}
			}

			out := cmd.OutOrStdout()
			// Without --yes, force a dry-run: print the plan and stop.
			effectiveDryRun := dryRun || !yes
			err = noderemove.Remove(cmd.Context(), noderemove.Options{
				Kubectl:      &kubectlRunner{bin: kbin, kubeconfig: kubeconfig},
				SSH:          sshClient,
				Node:         node,
				NodeName:     name,
				Uninstall:    uninstall,
				SkipDrain:    skipDrain,
				DrainTimeout: drainTimeout,
				Force:        force,
				DryRun:       effectiveDryRun,
				Out:          out,
			})
			if err != nil {
				return err
			}
			if effectiveDryRun && !dryRun {
				fmt.Fprintln(out, "\n[remove-node] this was a preview — re-run with --yes to apply.")
			}
			return nil
		},
	}
	cmd.Flags().StringVar(&sshHost, "ssh-host", "", "SSH endpoint of the node being removed (for rke2 teardown): user@host or a ~/.ssh/config alias. Omit to skip node-side teardown")
	cmd.Flags().StringVar(&kubeconfig, "kubeconfig", "", "Kubeconfig for the target cluster (default: $KUBECONFIG, then kubectl's default)")
	cmd.Flags().BoolVar(&uninstall, "uninstall", false, "Node-side teardown runs rke2-uninstall.sh (full removal) instead of rke2-killall.sh (stop only)")
	cmd.Flags().BoolVar(&skipDrain, "skip-drain", false, "Skip cordon + drain (delete the node object directly)")
	cmd.Flags().StringVar(&drainTimeout, "drain-timeout", "120s", "Timeout passed to kubectl drain")
	cmd.Flags().BoolVar(&force, "force", false, "Pass --force/--disable-eviction to drain and continue past a drain failure (node-side teardown failure is always non-fatal)")
	cmd.Flags().BoolVar(&dryRun, "dry-run", false, "Resolve + print the plan; change nothing")
	cmd.Flags().BoolVar(&yes, "yes", false, "Apply the removal (without this, remove-node only previews the plan)")
	cmd.Flags().StringVar(&sshJump, "ssh-jump", "", "SSH jump chain to reach the node: `[user@]host[:port][,...]` (like ssh -J). Empty honours ~/.ssh/config ProxyJump")
	cmd.Flags().BoolVar(&sshAcceptNew, "ssh-accept-new-host-keys", false, "Trust-on-first-use for the node's SSH host key (a MISMATCH is still refused)")
	_ = fleetRepo // remove-node is fleet-independent; flag accepted for parity
	return cmd
}

// kubectlRunner shells the operator's kubectl for the cluster-side steps
// (get/cordon/drain/delete/exec). Reusing kubectl keeps `drain` on
// kubectl's own PDB-aware eviction logic. Kubeconfig is passed through so
// the child binary targets the same cluster.
type kubectlRunner struct {
	bin        string
	kubeconfig string
}

func (k *kubectlRunner) Run(ctx context.Context, args ...string) ([]byte, error) {
	child := exec.CommandContext(ctx, k.bin, args...)
	env := os.Environ()
	if k.kubeconfig != "" {
		filtered := env[:0]
		for _, kv := range env {
			if len(kv) >= len("KUBECONFIG=") && kv[:len("KUBECONFIG=")] == "KUBECONFIG=" {
				continue
			}
			filtered = append(filtered, kv)
		}
		filtered = append(filtered, "KUBECONFIG="+k.kubeconfig)
		child.Env = filtered
	} else {
		child.Env = env
	}
	var buf bytes.Buffer
	child.Stdout = &buf
	child.Stderr = &buf
	err := child.Run()
	return buf.Bytes(), err
}
