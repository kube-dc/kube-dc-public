package main

import (
	"bytes"
	"context"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"

	gogit "github.com/go-git/go-git/v5"
	"github.com/go-git/go-git/v5/plumbing"

	"github.com/shalb/kube-dc/cli/internal/bootstrap"
	"github.com/shalb/kube-dc/cli/internal/bootstrap/anchors"
	"github.com/shalb/kube-dc/cli/internal/bootstrap/clusterinit"
	"github.com/shalb/kube-dc/cli/internal/bootstrap/oidccutover"
	"github.com/shalb/kube-dc/cli/internal/bootstrap/ports"
)

// finalizeManagedServices publishes only after live readiness and escrow are
// confirmed. On a push failure it rolls the local publication commit back,
// so a later generic bootstrap retry cannot push it before re-verification.
func finalizeManagedServices(ctx context.Context, out io.Writer, o *clusterinit.InitOptions, session *bootstrap.Session, kubeconfig, token, sshUser string, fetchVerified, cutoverComplete bool, cutoverNodes []oidccutover.Node) error {
	if o.NoPush {
		return fmt.Errorf("managed-services publication requires a pushed Fleet overlay")
	}
	pins, err := managedServicesOverlayPins(o.Repo, o.Name)
	if err != nil {
		return err
	}
	reader, err := pinnedManagedServicesKubectl(ctx, kubeconfig)
	if err != nil {
		return err
	}
	prepublish, err := waitManagedServicesPrepublish(ctx, reader, pins)
	if err != nil {
		return err
	}
	if err := verifyManagedServicesFleetPushTarget(ctx, o.Repo, o.Name, reader); err != nil {
		return err
	}
	if err := checkManagedServicesFleetDiff(ctx, session.Git, o.Repo); err != nil {
		return err
	}
	if err := applyManagedServicesInternalAnchors(ctx, out, o, session, kubeconfig, sshUser, cutoverNodes); err != nil {
		return err
	}
	base := filepath.Join(o.Repo, "clusters", o.Name)
	beforeCatalog, err := os.ReadFile(filepath.Join(base, "services-catalog.yaml"))
	if err != nil {
		return err
	}
	beforeConfig, err := os.ReadFile(filepath.Join(base, "cluster-config.env"))
	if err != nil {
		return err
	}
	if _, err := clusterinit.EscrowManagedServicesCellKey(ctx, o.Repo, o.Name, prepublish.CellID, prepublish.CellKey, session.SOPS); err != nil {
		return err
	}
	if _, err := clusterinit.PublishManagedServicesFiles(o.Repo, o.Name); err != nil {
		return err
	}
	if err := verifyManagedServicesPublicationDelta(base, beforeCatalog, beforeConfig); err != nil {
		return err
	}
	diff, err := session.Git.Diff(ctx, o.Repo)
	if err != nil {
		return err
	}
	if err := checkManagedServicesDiffPaths(diff, o.Name); err != nil {
		return err
	}
	if len(diff.Files) > 0 {
		if err := commitManagedServicesPublication(ctx, session.Git, o.Repo, o.Name, token); err != nil {
			return err
		}
	} else if err := session.Git.Push(ctx, o.Repo, token); err != nil {
		return fmt.Errorf("push existing managed-services publication: %w", err)
	}
	fmt.Fprintln(out, "[finalize] managed-services catalog pushed; waiting for six bundles and twelve plans")
	if err := waitManagedServicesPublished(ctx, reader, pins); err != nil {
		return err
	}
	if !fetchVerified || !cutoverComplete || o.NoSSH {
		return fmt.Errorf("catalog published, but backup admission needs a fresh kubeconfig, completed OIDC cutover and authoritative server inventory; run bootstrap services admission refresh")
	}
	if len(cutoverNodes) == 0 {
		var err error
		cutoverNodes, err = resolveCutoverNodes(ctx, kubeconfig, nil, sshUser, false)
		if err != nil {
			return err
		}
		cutoverNodes = overrideFinalizeCutoverHosts(cutoverNodes, o)
	}
	inventory, err := resolveManagedServicesOSHostnames(ctx, session.SSH, cutoverNodes)
	if err != nil {
		return err
	}
	if err := refreshManagedServicesAdmission(ctx, reader, kubeconfig, pins, inventory, true); err != nil {
		return err
	}
	fmt.Fprintln(out, "[finalize] managed-services catalog and backup admission are Ready")
	return nil
}

// The recovery and S3 hostnames resolve to the private gateway inside a
// single-IP NAT installation. Install its L2 return-path anchors before the
// catalog can offer backups or restores to tenants.
func applyManagedServicesInternalAnchors(ctx context.Context, out io.Writer, o *clusterinit.InitOptions, session *bootstrap.Session, kubeconfig, sshUser string, cutoverNodes []oidccutover.Node) error {
	cluster, err := loadAnchorCluster(o.Repo, o.Name)
	if err != nil {
		return err
	}
	if cluster.Env.GetOr("SERVICES_INTERNAL_GATEWAY_REQUIRED", "false") != "true" {
		return nil
	}
	if o.NoSSH || session == nil || session.SSH == nil {
		return fmt.Errorf("managed-services internal gateway requires SSH to apply return-path anchors before catalog publication")
	}
	if len(cutoverNodes) == 0 {
		cutoverNodes, err = resolveCutoverNodes(ctx, kubeconfig, nil, sshUser, false)
		if err != nil {
			return fmt.Errorf("managed-services internal gateway requires live SSH targets: %w", err)
		}
		cutoverNodes = overrideFinalizeCutoverHosts(cutoverNodes, o)
	}
	if err := clusterinit.ValidateAnchorConfig(cluster.Env.AsMap()); err != nil {
		return fmt.Errorf("managed-services internal gateway anchors: %w", err)
	}
	entries, err := anchors.ParseAnchorMap(cluster.Env.GetOr("EXT_NET_ANCHOR_IPS", ""))
	if err != nil || len(entries) == 0 {
		return fmt.Errorf("managed-services internal gateway has no valid return-path anchors: %v", err)
	}
	fleetResolver, err := buildAnchorResolver(cluster.Env.GetOr("EXT_NET_ANCHOR_SSH_HOSTS", ""), nil)
	if err != nil {
		return err
	}
	resolver := managedServicesAnchorResolver(o, cutoverNodes, fleetResolver, len(entries))
	result, err := anchors.Apply(ctx, session.SSH, anchors.ApplyOptions{
		Anchors: entries, Iface: cluster.Env.GetOr("EXT_NET_ANCHOR_INTERFACE", "br-ext-cloud"), Resolver: resolver,
	})
	if err != nil {
		return err
	}
	printAnchorApplyResult(out, result, false)
	if result.Failed > 0 {
		return fmt.Errorf("managed-services internal gateway: %d of %d return-path anchors failed; catalog remains suspended", result.Failed, len(entries))
	}
	return nil
}

func managedServicesAnchorResolver(o *clusterinit.InitOptions, cutoverNodes []oidccutover.Node, fleetResolver anchors.HostResolver, anchorCount int) anchors.HostResolver {
	return func(node string) ports.SSHHost {
		if raw := o.NodeSSHHosts[node]; raw != "" {
			return parseSSHHostArg(raw)
		}
		for _, candidate := range cutoverNodes {
			if candidate.Name == node {
				return candidate.Host
			}
		}
		if o.SSHHost != "" && (node == o.PrimaryNode || anchorCount == 1) {
			return parseSSHHostArg(o.SSHHost)
		}
		return fleetResolver(node)
	}
}

func commitManagedServicesPublication(ctx context.Context, git ports.GitClient, repo, cluster, token string) error {
	beforeCommit, err := git.Head(ctx, repo)
	if err != nil {
		return err
	}
	commit, err := git.CommitAndPush(ctx, repo, "Publish verified managed-services catalog for "+cluster, token)
	if err != nil {
		return rollbackOwnManagedServicesCommit(ctx, git, repo, beforeCommit, commit, err)
	}
	return nil
}

// Reset only the commit this operation created. A concurrent edit or commit
// must remain intact for the operator to inspect and recover deliberately.
func rollbackOwnManagedServicesCommit(ctx context.Context, git ports.GitClient, repo, before, ours string, pushErr error) error {
	if ours == "" {
		return fmt.Errorf("managed-services Git commit or push failed; local changes preserved for inspection: %w", pushErr)
	}
	head, err := git.Head(ctx, repo)
	if err != nil || head != ours {
		return fmt.Errorf("managed-services push failed; HEAD changed concurrently, so local work was preserved: %w", pushErr)
	}
	diff, err := git.Diff(ctx, repo)
	if err != nil || len(diff.Files) != 0 {
		return fmt.Errorf("managed-services push failed; worktree changed concurrently, so local work was preserved: %w", pushErr)
	}
	repository, err := gogit.PlainOpen(repo)
	if err != nil {
		return fmt.Errorf("managed-services push failed; commit ancestry could not be checked, so local work was preserved: %w", pushErr)
	}
	commit, err := repository.CommitObject(plumbing.NewHash(ours))
	if err != nil || commit.NumParents() != 1 || commit.ParentHashes[0].String() != before {
		return fmt.Errorf("managed-services push failed; another commit preceded this operation, so local work was preserved: %w", pushErr)
	}
	if err := git.ResetHard(ctx, repo, before); err != nil {
		return fmt.Errorf("managed-services push failed and local rollback failed: %w", err)
	}
	return fmt.Errorf("managed-services push failed; only this operation's local commit was rolled back: %w", pushErr)
}

func checkManagedServicesFleetDiff(ctx context.Context, git ports.GitClient, repo string) error {
	diff, err := git.Diff(ctx, repo)
	if err != nil {
		return err
	}
	if len(diff.Files) != 0 {
		return fmt.Errorf("Fleet has uncommitted changes; use a clean checkout before managed-services publication")
	}
	return nil
}

func verifyManagedServicesPublicationDelta(base string, beforeCatalog, beforeConfig []byte) error {
	newCatalog, err := os.ReadFile(filepath.Join(base, "services-catalog.yaml"))
	if err != nil {
		return err
	}
	newConfig, err := os.ReadFile(filepath.Join(base, "cluster-config.env"))
	if err != nil {
		return err
	}
	expectedCatalog := bytes.Replace(beforeCatalog, []byte("  suspend: true"), []byte("  suspend: false"), 1)
	if !bytes.Equal(newCatalog, expectedCatalog) {
		return fmt.Errorf("managed-services catalog has changes beyond the suspension toggle")
	}
	lines := strings.Split(string(beforeConfig), "\n")
	for i, line := range lines {
		if line == "KUBE_DC_UI_MANAGED_SERVICES_ALL_ORGANIZATIONS=false" {
			lines[i] = "KUBE_DC_UI_MANAGED_SERVICES_ALL_ORGANIZATIONS=true"
		}
	}
	if string(newConfig) != strings.Join(lines, "\n") {
		return fmt.Errorf("cluster config has changes beyond managed-services visibility")
	}
	return nil
}

func checkManagedServicesDiffPaths(diff ports.Diff, cluster string) error {
	base := filepath.ToSlash(filepath.Join("clusters", cluster)) + "/"
	allowed := map[string]bool{
		base + "services-catalog.yaml":                     true,
		base + "cluster-config.env":                        true,
		base + "escrow/services-cell-signing-key.enc.yaml": true,
	}
	for _, file := range diff.Files {
		if !allowed[filepath.ToSlash(file.Path)] {
			return fmt.Errorf("Fleet has unrelated change %q; use a clean checkout before managed-services publication", file.Path)
		}
		if strings.Contains(file.Status, "D") {
			return fmt.Errorf("Fleet publication file %q is deleted", file.Path)
		}
	}
	return nil
}
