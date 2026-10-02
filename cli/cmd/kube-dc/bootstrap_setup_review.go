package main

import (
	"encoding/json"
	"fmt"

	"github.com/shalb/kube-dc/cli/internal/bootstrap/adapters/script"
	sshadapter "github.com/shalb/kube-dc/cli/internal/bootstrap/adapters/ssh"
	"github.com/shalb/kube-dc/cli/internal/bootstrap/clusterinit"
	"github.com/shalb/kube-dc/cli/internal/bootstrap/discover"
	"github.com/shalb/kube-dc/cli/internal/bootstrap/ports"
	"github.com/shalb/kube-dc/cli/internal/bootstrap/setup"
	"github.com/spf13/cobra"
)

func bootstrapSetupReviewCmd() *cobra.Command {
	var configPath, cache, directory, save, format string
	cmd := &cobra.Command{Use: "review", Short: "Prepare exact Fleet files and bind read-only safety evidence", Args: cobra.NoArgs, SilenceUsage: true,
		Long: "Read pinned hosts, check cached release bytes and the Git destination, then run the existing scaffold in a new private local directory. Save the actual generated environment and file hashes. This review does not authorize production setup. It does not install packages or change hosts, Git, or Kubernetes.",
		RunE: func(cmd *cobra.Command, _ []string) error {
			if configPath == "" || cache == "" || directory == "" || save == "" {
				return fmt.Errorf("--config, --artifact-cache, --prepare-dir, and --save-review are required")
			}
			if format != "text" && format != "json" {
				return fmt.Errorf("--format must be text or json")
			}
			spec, err := setup.Load(configPath)
			if err != nil {
				return err
			}
			compiled, err := setup.Compile(spec)
			if err != nil {
				return err
			}
			checks, err := discover.CheckRequiredTools(cmd.Context(), initToolSelection(&compiled.Init))
			if err != nil {
				return err
			}
			if blocked := manualToolBlockers(checks, false); len(blocked) != 0 {
				return fmt.Errorf("install or upgrade required tools before review: %v", blocked)
			}
			plan, err := clusterinit.BuildPlan(&compiled.Init, clusterinit.FleetState{})
			if err != nil {
				return err
			}
			tls, err := loadWildcardTLSFromOptions(&compiled.Init)
			if err != nil {
				return err
			}
			route53, err := loadDNS01FromOptions(&compiled.Init)
			if err != nil {
				return err
			}
			cloudflare, err := loadDNS01CloudflareFromOptions(&compiled.Init)
			if err != nil {
				return err
			}
			ca, err := loadTrustedCAFromOptions(&compiled.Init)
			if err != nil {
				return err
			}
			executable := runningCLIPath()
			review, err := setup.CreateSafetyReview(cmd.Context(), compiled, setup.SafetyReviewOptions{ArtifactCache: cache, CLIPath: executable, Directory: directory, SSH: sshadapter.New(), Runner: func(root string) ports.ScriptRunner { return script.New(root, "", nil) }, Scaffold: clusterinit.ScaffoldOptions{Plan: plan, FleetRepo: compiled.Init.Repo, WildcardTLS: tls, DNS01Route53: route53, DNS01Cloudflare: cloudflare, TrustedCA: ca}})
			if err != nil {
				return err
			}
			if err := setup.SaveSafetyReview(save, review); err != nil {
				return err
			}
			if format == "json" {
				encoder := json.NewEncoder(cmd.OutOrStdout())
				encoder.SetIndent("", "  ")
				return encoder.Encode(review)
			}
			fmt.Fprintf(cmd.OutOrStdout(), "Review saved: %s\nPrepared files: %s\nFleet files: %d\nHosts: %d\nRaw storage identities: %d\nReview SHA-256: %s\n", save, review.Prepared.Directory, len(review.Prepared.Files), len(review.Hosts.Hosts), len(review.Storage), review.Hash)
			fmt.Fprintln(cmd.OutOrStdout(), "The JSON review contains the generated environment and file hashes. Prepared files contain the exact patches and encrypted Secrets. Host observations expire after five minutes. Production setup remains blocked.")
			return nil
		},
	}
	cmd.Flags().StringVar(&configPath, "config", "", "Path to a version 1 setup JSON file")
	cmd.Flags().StringVar(&cache, "artifact-cache", "", "Content-addressed release cache: blobs/sha256/DIGEST")
	cmd.Flags().StringVar(&directory, "prepare-dir", "", "New private directory outside the selected Git checkout")
	cmd.Flags().StringVar(&save, "save-review", "", "New private JSON review file")
	cmd.Flags().StringVar(&format, "format", "text", "Output format: text or json")
	return cmd
}

func bootstrapSetupRecheckCmd() *cobra.Command {
	var configPath, path string
	cmd := &cobra.Command{Use: "recheck", Short: "Read targets again and reject changes since a saved review", Args: cobra.NoArgs, SilenceUsage: true, RunE: func(cmd *cobra.Command, _ []string) error {
		if configPath == "" || path == "" {
			return fmt.Errorf("--config and --review are required")
		}
		spec, err := setup.Load(configPath)
		if err != nil {
			return err
		}
		compiled, err := setup.Compile(spec)
		if err != nil {
			return err
		}
		review, err := setup.LoadSafetyReview(path)
		if err != nil {
			return err
		}
		executable := runningCLIPath()
		if err := setup.RecheckSafetyReview(cmd.Context(), compiled, review, sshadapter.New(), executable); err != nil {
			return err
		}
		fmt.Fprintln(cmd.OutOrStdout(), "Reviewed bytes and fresh targets match. Production setup remains blocked; no installation writes occurred.")
		return nil
	}}
	cmd.Flags().StringVar(&configPath, "config", "", "Path to the reviewed setup JSON file")
	cmd.Flags().StringVar(&path, "review", "", "Private JSON file produced by setup review")
	return cmd
}
