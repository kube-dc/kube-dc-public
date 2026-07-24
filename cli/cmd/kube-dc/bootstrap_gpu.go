package main

import (
	"context"
	"encoding/json"
	"fmt"
	"path/filepath"
	"strings"
	"time"

	"github.com/spf13/cobra"

	"github.com/shalb/kube-dc/cli/internal/bootstrap"
	"github.com/shalb/kube-dc/cli/internal/bootstrap/clusterinit"
	"github.com/shalb/kube-dc/cli/internal/bootstrap/config"
	"github.com/shalb/kube-dc/cli/internal/bootstrap/gpudra"
	"github.com/shalb/kube-dc/cli/internal/bootstrap/gputransition"
	"github.com/shalb/kube-dc/cli/internal/bootstrap/gpuupgrade"
	"github.com/shalb/kube-dc/cli/internal/bootstrap/ports"
)

func bootstrapGPUCmd(fleetRepo *string) *cobra.Command {
	cmd := &cobra.Command{Use: "gpu", Short: "Validate and maintain accelerator platform state"}
	cmd.AddCommand(newGPUUpgradeCheckCmd(time.Now))
	cmd.AddCommand(bootstrapGPUTransitionCmd(fleetRepo))
	cmd.AddCommand(bootstrapGPUDRACmd(time.Now))
	return cmd
}

func bootstrapGPUDRACmd(now func() time.Time) *cobra.Command {
	var kubeconfig, driver, deviceClass, digest, output string
	var maxCanaryAge, minDriverUnreadyAge time.Duration
	cmd := &cobra.Command{
		Use:   "dra",
		Short: "Detect and validate the DRA shared-GPU backend",
		Long: `Read-only DRA operator workflow. Status/detect show the live snapshot;
doctor enforces the Kubernetes, digest, inventory, ownership, and canary support
gate; plan additionally requires creation gates closed and zero legacy holders;
rollback-plan requires creation gates closed and zero DRA holders/claims.
postflight reruns the full doctor gate after each upgraded GPU node and includes
the exact current-DaemonSet-owned driver Pod state in its diagnostic output.

recovery-plan is read-only and emits one exact kubelet-plugin Pod deletion only
when the DaemonSet is degraded, its DRA inventory is empty, and an old unready
Pod remains on a Ready node. The operator must execute that command separately.

Apply is performed by bootstrap init with --gpu-shared-allocator=dra (or auto)
after a saved plan passes this command. No subcommand silently falls back to the
legacy device-plugin allocator.`,
	}
	read := func(ctx context.Context) (ports.GPUDRAStatus, error) {
		client, err := bootstrap.NewGPUDRAOnly(kubeconfig)
		if err != nil {
			return ports.GPUDRAStatus{}, err
		}
		return client.GPUDRAStatus(ctx, driver, deviceClass)
	}
	renderStatus := func(command *cobra.Command, status ports.GPUDRAStatus) error {
		if output == "json" {
			body, err := json.MarshalIndent(status, "", "  ")
			if err != nil {
				return err
			}
			fmt.Fprintln(command.OutOrStdout(), string(body))
			return nil
		}
		fmt.Fprintf(command.OutOrStdout(), "Kubernetes: %s (stable DRA=%t, consumable capacity=%t)\n", status.ServerVersion, status.StableAPI, status.ConsumableCapacity)
		fmt.Fprintf(command.OutOrStdout(), "DeviceClass: %s (present=%t)\n", status.DeviceClass, status.DeviceClassPresent)
		fmt.Fprintf(command.OutOrStdout(), "Inventory: slices=%d devices=%d shareable=%d\n", status.ResourceSlices, status.Devices, status.ShareableDevices)
		fmt.Fprintf(command.OutOrStdout(), "Driver: ready=%d/%d images=%s owners=%s\n", status.DriverReady, status.DriverDesired, strings.Join(status.DriverImages, ","), strings.Join(status.AllocatorOwners, ","))
		for _, pod := range status.DriverPods {
			fmt.Fprintf(command.OutOrStdout(), "Driver Pod: %s/%s node=%s phase=%s ready=%t nodeReady=%t deleting=%t created=%s readyTransition=%s\n",
				pod.Namespace, pod.Name, pod.Node, pod.Phase, pod.Ready, pod.NodeReady, pod.Deleting, pod.CreationTimestamp.UTC().Format(time.RFC3339), pod.ReadyLastTransitionTime.UTC().Format(time.RFC3339))
		}
		fmt.Fprintf(command.OutOrStdout(), "Claims: total=%d allocated=%d pending=%d holders=%d legacy-holders=%d\n", status.Claims, status.AllocatedClaims, status.PendingClaims, len(status.DRAHolders), len(status.LegacyHolders))
		fmt.Fprintf(command.OutOrStdout(), "Creation gates: shared=%t vm=%t; DRA nodes=%s; wrong-mode=%s\n", status.SharedCreationEnabled, status.VMCreationEnabled, strings.Join(status.DRANodes, ","), strings.Join(status.WrongModeNodes, ","))
		return nil
	}
	renderReport := func(command *cobra.Command, report gpudra.Report, status *ports.GPUDRAStatus) error {
		if output == "json" {
			var value any = report
			if status != nil {
				value = struct {
					Status ports.GPUDRAStatus `json:"status"`
					Report gpudra.Report      `json:"report"`
				}{Status: *status, Report: report}
			}
			body, err := json.MarshalIndent(value, "", "  ")
			if err != nil {
				return err
			}
			fmt.Fprintln(command.OutOrStdout(), string(body))
		} else {
			if status != nil {
				if err := renderStatus(command, *status); err != nil {
					return err
				}
			}
			for _, item := range report.Checks {
				state := "PASS"
				if !item.Pass {
					state = "FAIL"
				}
				fmt.Fprintf(command.OutOrStdout(), "[%s] %s: %s\n", state, item.Name, item.Message)
			}
		}
		if !report.Ready {
			return fmt.Errorf("DRA support gate failed; no allocator change was made")
		}
		return nil
	}
	for _, name := range []string{"status", "detect"} {
		name := name
		cmd.AddCommand(&cobra.Command{
			Use: name, Short: "Show the live DRA operator snapshot", SilenceUsage: true,
			RunE: func(command *cobra.Command, _ []string) error {
				status, err := read(command.Context())
				if err != nil {
					return err
				}
				return renderStatus(command, status)
			},
		})
	}
	addGate := func(name, short string, migration, rollback, includeStatus bool) {
		cmd.AddCommand(&cobra.Command{
			Use: name, Short: short, SilenceUsage: true,
			RunE: func(command *cobra.Command, _ []string) error {
				status, err := read(command.Context())
				if err != nil {
					return err
				}
				report := gpudra.Evaluate(status, gpudra.Options{
					Now: now(), MaxCanaryAge: maxCanaryAge, RequiredDigest: digest,
					MigrationPlan: migration, RollbackPlan: rollback,
				})
				if includeStatus {
					return renderReport(command, report, &status)
				}
				return renderReport(command, report, nil)
			},
		})
	}
	addGate("doctor", "Validate DRA support and runtime health", false, false, false)
	addGate("postflight", "Validate DRA runtime health after a GPU node upgrade", false, false, true)
	addGate("plan", "Validate the legacy-to-DRA migration gate", true, false, false)
	addGate("rollback-plan", "Validate the DRA-to-compatibility rollback gate", false, true, false)
	cmd.AddCommand(&cobra.Command{
		Use: "recovery-plan", Short: "Plan a serialized stale DRA driver Pod recovery", SilenceUsage: true,
		RunE: func(command *cobra.Command, _ []string) error {
			status, err := read(command.Context())
			if err != nil {
				return err
			}
			plan := gpudra.PlanDriverRecovery(status, now(), minDriverUnreadyAge)
			if output == "json" {
				body, err := json.MarshalIndent(plan, "", "  ")
				if err != nil {
					return err
				}
				fmt.Fprintln(command.OutOrStdout(), string(body))
			} else {
				for _, item := range plan.Checks {
					state := "PASS"
					if !item.Pass {
						state = "FAIL"
					}
					fmt.Fprintf(command.OutOrStdout(), "[%s] %s: %s\n", state, item.Name, item.Message)
				}
				if plan.Candidate != nil {
					fmt.Fprintf(command.OutOrStdout(), "Candidate: %s/%s on Ready node %s\n", plan.Candidate.Namespace, plan.Candidate.Name, plan.Candidate.Node)
					fmt.Fprintf(command.OutOrStdout(), "Execute separately after review: kubectl --kubeconfig \"$KUBECONFIG\" -n %s delete pod %s --wait=false\n", plan.Candidate.Namespace, plan.Candidate.Name)
				}
			}
			if !plan.Eligible {
				return fmt.Errorf("no safe stale-driver recovery candidate; no mutation was made")
			}
			return nil
		},
	})
	cmd.PersistentFlags().StringVar(&kubeconfig, "kubeconfig", "", "Explicit admin kubeconfig for the target cluster (required)")
	cmd.PersistentFlags().StringVar(&driver, "driver", "hami-core-gpu.project-hami.io", "Qualified DRA driver name")
	cmd.PersistentFlags().StringVar(&deviceClass, "device-class", "kube-dc-nvidia-v100-shared-8g", "Fixed product DeviceClass")
	cmd.PersistentFlags().StringVar(&digest, "driver-digest", gpudra.DefaultDriverDigest, "Required HAMi DRA image digest")
	cmd.PersistentFlags().DurationVar(&maxCanaryAge, "max-canary-age", 10*time.Minute, "Maximum accepted age of the allocation canary")
	cmd.PersistentFlags().DurationVar(&minDriverUnreadyAge, "min-driver-unready-age", 10*time.Minute, "Minimum age of an unready driver Pod before recovery is eligible")
	cmd.PersistentFlags().StringVar(&output, "output", "text", "Output format: text or json")
	_ = cmd.MarkPersistentFlagRequired("kubeconfig")
	cmd.PersistentPreRunE = func(_ *cobra.Command, _ []string) error {
		if output != "text" && output != "json" {
			return fmt.Errorf("--output must be text or json")
		}
		return nil
	}
	return cmd
}

func evaluateGPUAutoDRA(ctx context.Context, kubeconfig string, now time.Time) (gpudra.Report, error) {
	client, err := bootstrap.NewGPUDRAOnly(kubeconfig)
	if err != nil {
		return gpudra.Report{}, err
	}
	status, err := client.GPUDRAStatus(ctx, "hami-core-gpu.project-hami.io", "kube-dc-nvidia-v100-shared-8g")
	if err != nil {
		return gpudra.Report{}, err
	}
	return gpudra.Evaluate(status, gpudra.Options{
		Now: now, MaxCanaryAge: 10 * time.Minute,
		RequiredDigest: gpudra.DefaultDriverDigest, MigrationPlan: true,
	}), nil
}

type gpuTransitionFleet struct {
	repoRoot string
	rulePath string
	git      ports.GitClient
	token    func() string
	preSHA   string
}

func (f *gpuTransitionFleet) Prepare(ctx context.Context, node string) (gputransition.Mode, error) {
	preSHA, err := f.git.Head(ctx, f.repoRoot)
	if err != nil {
		return "", fmt.Errorf("read fleet HEAD: %w", err)
	}
	diff, err := f.git.Diff(ctx, f.repoRoot)
	if err != nil {
		return "", fmt.Errorf("check fleet working tree: %w", err)
	}
	if len(diff.Files) > 0 {
		return "", fmt.Errorf("fleet working tree is dirty (%d changed file(s)); commit or stash first", len(diff.Files))
	}
	f.preSHA = preSHA
	return gputransition.ReadNodeMode(f.rulePath, node)
}

func (f *gpuTransitionFleet) Apply(ctx context.Context, node string, from, to gputransition.Mode) (string, error) {
	if f.preSHA == "" {
		return "", fmt.Errorf("fleet transaction was not prepared")
	}
	head, err := f.git.Head(ctx, f.repoRoot)
	if err != nil {
		return "", fmt.Errorf("recheck fleet HEAD: %w", err)
	}
	diff, err := f.git.Diff(ctx, f.repoRoot)
	if err != nil {
		return "", fmt.Errorf("recheck fleet working tree: %w", err)
	}
	if head != f.preSHA || len(diff.Files) > 0 {
		return "", fmt.Errorf("fleet changed after preflight; refusing to stage unrelated changes")
	}
	if err := gputransition.WriteNodeMode(f.rulePath, node, from, to); err != nil {
		return "", err
	}
	message := fmt.Sprintf("feat(gpu): transition %s to %s", node, to)
	sha, err := f.git.CommitAndPush(ctx, f.repoRoot, message, f.token())
	if err == nil {
		return sha, nil
	}
	if resetErr := f.git.ResetHard(ctx, f.repoRoot, f.preSHA); resetErr != nil {
		return "", fmt.Errorf("commit/push failed (%v) and rollback to prior fleet HEAD failed (%v)", err, resetErr)
	}
	return "", fmt.Errorf("commit/push failed and fleet checkout rolled back: %w", err)
}

func bootstrapGPUTransitionCmd(fleetRepo *string) *cobra.Command {
	var (
		kubeconfig, from, to, githubToken, provider string
		yes, dryRun, resumeCordoned                 bool
		timeout                                     time.Duration
	)
	cmd := &cobra.Command{
		Use:           "transition <cluster> <node>",
		Args:          cobra.ExactArgs(2),
		Short:         "Cordon and transition an empty GPU node through GitOps",
		SilenceUsage:  true,
		SilenceErrors: true,
		Long: `Transitions one exact GPU node between pod-hami, pod-hami-dra and vm-passthrough.
The workflow refuses unless both tenant GPU creation gates are closed, the
fleet checkout is clean, live/fleet mode labels agree, and no native GPU Pod or
VMI holder exists. It cordons first, rechecks holders, commits the generated
NodeFeatureRule change, waits for exact target labels and the sole target
device-plugin owner, and only then uncordons.

Any failure after cordon deliberately leaves the node cordoned. Resume only
after investigating the failure, with the same from/to values and
--resume-cordoned. Without --yes the command is a read-only preview.`,
		RunE: func(cmd *cobra.Command, args []string) error {
			if provider != "" && provider != string(clusterinit.ProviderGitHub) && provider != string(clusterinit.ProviderGitLab) {
				return fmt.Errorf("bootstrap gpu transition: --provider must be github or gitlab (got %q)", provider)
			}
			env, repoRoot, err := loadClusterEnv(*fleetRepo, args[0])
			if err != nil {
				return fmt.Errorf("bootstrap gpu transition: %w", err)
			}
			if err := requireGPUCreationClosed(env); err != nil {
				return fmt.Errorf("bootstrap gpu transition: %w", err)
			}
			cluster, err := bootstrap.NewGPUTransitionOnly(kubeconfig)
			if err != nil {
				return err
			}
			git, err := bootstrap.NewGitOnly()
			if err != nil {
				return fmt.Errorf("bootstrap gpu transition: build git adapter: %w", err)
			}
			fleet := &gpuTransitionFleet{
				repoRoot: repoRoot,
				rulePath: filepath.Join(filepath.Dir(env.Path), "gpu-node-modes", "nodefeaturerule.yaml"),
				git:      git,
				token: func() string {
					return resolveGitHubToken(&clusterinit.InitOptions{GitHubToken: githubToken, Provider: clusterinit.Provider(provider)}, cmd.OutOrStdout())
				},
			}
			effectiveDryRun := dryRun || !yes
			_, err = gputransition.Run(cmd.Context(), gputransition.Options{
				Node: args[1], From: gputransition.Mode(from), To: gputransition.Mode(to),
				DryRun: effectiveDryRun, ResumeCordoned: resumeCordoned,
				Timeout: timeout, Out: cmd.OutOrStdout(), Cluster: cluster, Fleet: fleet,
			})
			if err != nil {
				return err
			}
			if effectiveDryRun && !dryRun {
				fmt.Fprintln(cmd.OutOrStdout(), "[gpu transition] re-run with --yes to apply.")
			}
			return nil
		},
	}
	cmd.Flags().StringVar(&kubeconfig, "kubeconfig", "", "Explicit admin kubeconfig for the target cluster (required)")
	cmd.Flags().StringVar(&from, "from", "", "Current GPU node mode: pod-hami, pod-hami-dra or vm-passthrough (required)")
	cmd.Flags().StringVar(&to, "to", "", "Target GPU node mode: pod-hami, pod-hami-dra or vm-passthrough (required)")
	cmd.Flags().BoolVar(&yes, "yes", false, "Apply the cordon and GitOps transition; without this, preview only")
	cmd.Flags().BoolVar(&dryRun, "dry-run", false, "Read and render the transition plan; change nothing")
	cmd.Flags().BoolVar(&resumeCordoned, "resume-cordoned", false, "Resume and eventually uncordon a node known to be cordoned by this workflow")
	cmd.Flags().DurationVar(&timeout, "timeout", 15*time.Minute, "Maximum wait for target labels, Node readiness, and device-plugin ownership")
	cmd.Flags().StringVar(&githubToken, "github-token", "", "Token for the fleet push (default: gh/glab credential helper)")
	cmd.Flags().StringVar(&provider, "provider", "", "Fleet Git host: github (default) or gitlab")
	for _, name := range []string{"kubeconfig", "from", "to"} {
		_ = cmd.MarkFlagRequired(name)
	}
	return cmd
}

func requireGPUCreationClosed(env *config.Env) error {
	var open []string
	for _, key := range []string{"GPU_SHARED_CREATION_ENABLED", "GPU_VM_CREATION_ENABLED"} {
		value, ok := env.Get(key)
		if !ok || value != "false" {
			open = append(open, key)
		}
	}
	if len(open) > 0 {
		return fmt.Errorf("GPU creation must be explicitly closed before transition (set %s=false through bootstrap config)", strings.Join(open, ", "))
	}
	return nil
}

func newGPUUpgradeCheckCmd(now func() time.Time) *cobra.Command {
	var (
		qualification string
		current       gpuupgrade.Target
		target        gpuupgrade.Target
		hardware      []string
		maxAge        time.Duration
	)
	cmd := &cobra.Command{
		Use:           "upgrade-check",
		Short:         "Fail closed unless an exact GPU upgrade tuple has fresh canary evidence",
		SilenceUsage:  true,
		SilenceErrors: true,
		Long: `Upgrade-check is a read-only pre-mutation gate for GPU nodes. It
matches the requested kernel, RKE2, NVIDIA driver, GPU Operator, DCGM exporter and exact PCI
identity against a reviewed qualification record. It also requires recent
allocation, monitoring and rollback canary evidence. Unknown tuples, RKE2
downgrades and skipped Kubernetes minors are blocked.

This command never labels, cordons, drains, restarts, patches, or applies a
System Upgrade Controller Plan. Run it immediately before creating or changing
an upgrade Plan, then retain its output with the change record.`,
		Example: `  kube-dc bootstrap gpu upgrade-check \
    --qualification ./gpu-upgrade-qualification.yaml \
    --pci-id 10de:1db6/10de:124a \
    --current-kernel 6.8.0-134-generic --target-kernel 6.8.0-135-generic \
    --current-rke2 v1.35.3+rke2r3 --target-rke2 v1.36.1+rke2r1 \
    --current-driver 580.126.20 --target-driver 580.130.00 \
    --current-gpu-operator v26.3.3 --target-gpu-operator v26.3.4 \
    --current-dcgm-exporter 4.4.1-4.6.0-ubuntu22.04 --target-dcgm-exporter 4.4.1-4.6.0-ubuntu22.04`,
		RunE: func(cmd *cobra.Command, _ []string) error {
			q, err := gpuupgrade.Load(qualification)
			if err != nil {
				return fmt.Errorf("GPU upgrade blocked: %w", err)
			}
			result, err := gpuupgrade.Check(q, gpuupgrade.Request{
				Current: current, Target: target, Hardware: hardware,
				Now: now(), MaxAge: maxAge,
			})
			if err != nil {
				return err
			}
			fmt.Fprintln(cmd.OutOrStdout(), "GPU upgrade gate: PASS")
			fmt.Fprintf(cmd.OutOrStdout(), "Qualification: %s\n", result.QualificationID)
			fmt.Fprintf(cmd.OutOrStdout(), "Approved by: %s\n", result.ApprovedBy)
			fmt.Fprintf(cmd.OutOrStdout(), "Source revision: %s\n", result.SourceRevision)
			fmt.Fprintf(cmd.OutOrStdout(), "Canary completed: %s\n", result.CompletedAt.Format(time.RFC3339))
			fmt.Fprintln(cmd.OutOrStdout(), "Next: apply only the reviewed serialized canary plan; stop on any health or allocation regression.")
			return nil
		},
	}

	cmd.Flags().StringVar(&qualification, "qualification", "", "Reviewed GPUUpgradeQualification YAML (required)")
	cmd.Flags().StringSliceVar(&hardware, "pci-id", nil, "Exact vendor:device/subvendor:subdevice identity (repeatable; required)")
	cmd.Flags().StringVar(&current.Kernel, "current-kernel", "", "Currently running kernel release (required)")
	cmd.Flags().StringVar(&target.Kernel, "target-kernel", "", "Proposed kernel release (required)")
	cmd.Flags().StringVar(&current.RKE2, "current-rke2", "", "Current RKE2 version (required)")
	cmd.Flags().StringVar(&target.RKE2, "target-rke2", "", "Proposed RKE2 version (required)")
	cmd.Flags().StringVar(&current.Driver, "current-driver", "", "Current NVIDIA driver version (required)")
	cmd.Flags().StringVar(&target.Driver, "target-driver", "", "Proposed NVIDIA driver version (required)")
	cmd.Flags().StringVar(&current.GPUOperator, "current-gpu-operator", "", "Current GPU Operator chart version (required)")
	cmd.Flags().StringVar(&target.GPUOperator, "target-gpu-operator", "", "Proposed GPU Operator chart version (required)")
	cmd.Flags().StringVar(&current.DCGMExporter, "current-dcgm-exporter", "", "Current DCGM exporter version or immutable digest (required)")
	cmd.Flags().StringVar(&target.DCGMExporter, "target-dcgm-exporter", "", "Proposed DCGM exporter version or immutable digest (required)")
	cmd.Flags().DurationVar(&maxAge, "max-canary-age", gpuupgrade.DefaultMaxAge, "Maximum accepted age of canary evidence")
	for _, name := range []string{"qualification", "pci-id", "current-kernel", "target-kernel", "current-rke2", "target-rke2", "current-driver", "target-driver", "current-gpu-operator", "target-gpu-operator", "current-dcgm-exporter", "target-dcgm-exporter"} {
		_ = cmd.MarkFlagRequired(name)
	}
	return cmd
}
