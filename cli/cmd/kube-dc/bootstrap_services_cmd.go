package main

import (
	"context"
	"fmt"
	"io"
	"net/netip"
	"sort"
	"strings"

	"github.com/spf13/cobra"

	"github.com/shalb/kube-dc/cli/internal/bootstrap"
	"github.com/shalb/kube-dc/cli/internal/bootstrap/adapters/git"
	"github.com/shalb/kube-dc/cli/internal/bootstrap/adapters/sops"
	"github.com/shalb/kube-dc/cli/internal/bootstrap/clusterinit"
	"github.com/shalb/kube-dc/cli/internal/bootstrap/oidccutover"
)

func bootstrapServicesCmd(fleetRepo *string) *cobra.Command {
	cmd := &cobra.Command{Use: "services", Short: "Finalize a managed-services installation and refresh backup admission"}
	cmd.AddCommand(bootstrapServicesFinalizeCmd(fleetRepo))
	cmd.AddCommand(bootstrapServicesAdmissionCmd(fleetRepo))
	cmd.AddCommand(bootstrapServicesRebuildPrepareCmd(fleetRepo))
	cmd.AddCommand(bootstrapServicesRestoreKeyCmd(fleetRepo))
	cmd.AddCommand(bootstrapServicesVerifyCmd(fleetRepo))
	return cmd
}

func bootstrapServicesVerifyCmd(fleetRepo *string) *cobra.Command {
	var kubeconfig string
	var greenfield bool
	cmd := &cobra.Command{
		Use: "verify <cluster>", Short: "Check the managed-services release, catalog and data plane without changing them",
		Args: cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			repo, err := resolveFleetRepo(*fleetRepo)
			if err != nil {
				return err
			}
			pins, err := managedServicesOverlayPins(repo, args[0])
			if err != nil {
				return err
			}
			if kubeconfig == "" {
				kubeconfig = clusterinit.DefaultKubeconfigPath()
			}
			reader, err := pinnedManagedServicesKubectl(cmd.Context(), kubeconfig)
			if err != nil {
				return err
			}
			if err := verifyManagedServicesRelease(cmd.Context(), reader, pins); err != nil {
				return err
			}
			if err := checkManagedServicesCatalog(cmd.Context(), reader, pins, greenfield); err != nil {
				return err
			}
			fmt.Fprintln(cmd.OutOrStdout(), "Managed-services release, six family bundles and twelve current-generation plans verified")
			return nil
		},
	}
	cmd.Flags().StringVar(&kubeconfig, "kubeconfig", "", "Admin kubeconfig for the named cluster")
	cmd.Flags().BoolVar(&greenfield, "greenfield", false, "Also require legacy plans to be disabled, as on a new installation")
	return cmd
}

func bootstrapServicesRebuildPrepareCmd(fleetRepo *string) *cobra.Command {
	var kubeconfig string
	cmd := &cobra.Command{
		Use: "rebuild-prepare <cluster>", Short: "Suspend the hub and catalog before rebuilding a cluster",
		Args: cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			repo, err := resolveFleetRepo(*fleetRepo)
			if err != nil {
				return err
			}
			if kubeconfig == "" {
				kubeconfig = clusterinit.DefaultKubeconfigPath()
			}
			reader, err := pinnedManagedServicesKubectl(cmd.Context(), kubeconfig)
			if err != nil {
				return err
			}
			if err := verifyManagedServicesFleetPushTarget(cmd.Context(), repo, args[0], reader); err != nil {
				return err
			}
			token := resolveGitHubToken(&clusterinit.InitOptions{Name: args[0], Repo: repo}, cmd.ErrOrStderr())
			if err := prepareManagedServicesRebuild(cmd.Context(), repo, args[0], token, git.New(), sops.New()); err != nil {
				return err
			}
			fmt.Fprintln(cmd.OutOrStdout(), "Managed-services hub and catalog are suspended in Fleet for rebuild")
			return nil
		},
	}
	cmd.Flags().StringVar(&kubeconfig, "kubeconfig", "", "Admin kubeconfig for the existing cluster")
	return cmd
}

func bootstrapServicesRestoreKeyCmd(fleetRepo *string) *cobra.Command {
	var kubeconfig string
	cmd := &cobra.Command{
		Use: "restore-key <cluster>", Short: "Restore the escrowed signing key before resuming the hub",
		Args: cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			repo, err := resolveFleetRepo(*fleetRepo)
			if err != nil {
				return err
			}
			if kubeconfig == "" {
				kubeconfig = clusterinit.DefaultKubeconfigPath()
			}
			reader, err := pinnedManagedServicesKubectl(cmd.Context(), kubeconfig)
			if err != nil {
				return err
			}
			if err := restoreManagedServicesCellKey(cmd.Context(), reader, repo, args[0], sops.New()); err != nil {
				return err
			}
			fmt.Fprintln(cmd.OutOrStdout(), "Managed-services signing key restored; resume the services Kustomization in Fleet, then run services finalize")
			return nil
		},
	}
	cmd.Flags().StringVar(&kubeconfig, "kubeconfig", "", "Admin kubeconfig for the rebuilt cluster")
	return cmd
}

func bootstrapServicesFinalizeCmd(fleetRepo *string) *cobra.Command {
	var kubeconfig, sshUser string
	var sshHosts []string
	cmd := &cobra.Command{
		Use: "finalize <cluster>", Short: "Publish a verified managed-services catalog and backup admission",
		Args: cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			repo, err := resolveFleetRepo(*fleetRepo)
			if err != nil {
				return err
			}
			if kubeconfig == "" {
				kubeconfig = clusterinit.DefaultKubeconfigPath()
			}
			session, err := bootstrap.NewSession(bootstrap.Options{FleetRepoPath: repo, Kubeconfig: kubeconfig})
			if err != nil {
				return err
			}
			defer session.Close()
			nodes, nodesErr := resolveCutoverNodes(cmd.Context(), kubeconfig, nil, sshUser, false)
			if nodesErr == nil {
				nodes, nodesErr = mapManagedServicesSSHHosts(nodes, sshHosts, sshUser)
			}
			if nodesErr != nil && len(sshHosts) > 0 {
				return fmt.Errorf("resolve complete control-plane SSH targets: %w", nodesErr)
			}
			cutoverComplete := nodesErr == nil
			if cutoverComplete {
				cutoverComplete = verifyManagedServicesCutover(cmd.Context(), session, nodes) == nil
			}
			if !cutoverComplete {
				fmt.Fprintln(cmd.ErrOrStderr(), "OIDC cutover is not proven on every API server; catalog publication may finish, but backup admission will remain deferred")
			}
			o := &clusterinit.InitOptions{Name: args[0], Repo: repo}
			token := resolveGitHubToken(o, cmd.ErrOrStderr())
			return finalizeManagedServices(cmd.Context(), cmd.OutOrStdout(), o, session, kubeconfig, token, sshUser, true, cutoverComplete, nodes)
		},
	}
	cmd.Flags().StringVar(&kubeconfig, "kubeconfig", "", "Admin kubeconfig for the named cluster")
	cmd.Flags().StringVar(&sshUser, "ssh-user", "", "SSH user for read-only OIDC cutover verification")
	cmd.Flags().StringArrayVar(&sshHosts, "ssh-host", nil, "Reachable control-plane SSH target: NODE=HOST for each server, or HOST for a single-server cluster")
	return cmd
}

func bootstrapServicesAdmissionCmd(fleetRepo *string) *cobra.Command {
	var kubeconfig, sshUser string
	var serverArgs []string
	var sshHosts []string
	cmd := &cobra.Command{Use: "admission", Short: "Manage backup-admission topology"}
	refresh := &cobra.Command{
		Use: "refresh <cluster>", Short: "Refresh API-server Lease identities without changing the server IP set",
		Args: cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			repo, err := resolveFleetRepo(*fleetRepo)
			if err != nil {
				return err
			}
			if kubeconfig == "" {
				kubeconfig = clusterinit.DefaultKubeconfigPath()
			}
			pins, err := managedServicesOverlayPins(repo, args[0])
			if err != nil {
				return err
			}
			inventory, err := parseManagedServicesServerArgs(serverArgs)
			if err != nil {
				return err
			}
			liveNodes, err := resolveCutoverNodes(cmd.Context(), kubeconfig, nil, "", false)
			if err != nil {
				return err
			}
			liveInventory := make([]managedServicesServer, 0, len(liveNodes))
			for _, node := range liveNodes {
				liveInventory = append(liveInventory, managedServicesServer{Name: node.Name, IP: node.Host.Hostname})
			}
			if err := sameManagedServicesInventory(liveInventory, inventory); err != nil {
				return err
			}
			session, err := bootstrap.NewSession(bootstrap.Options{FleetRepoPath: repo, Kubeconfig: kubeconfig})
			if err != nil {
				return err
			}
			defer session.Close()
			liveNodes, err = mapManagedServicesSSHHosts(liveNodes, sshHosts, sshUser)
			if err != nil {
				return err
			}
			inventory, err = resolveManagedServicesOSHostnames(cmd.Context(), session.SSH, liveNodes)
			if err != nil {
				return err
			}
			reader, err := pinnedManagedServicesKubectl(cmd.Context(), kubeconfig)
			if err != nil {
				return err
			}
			if err := checkManagedServicesPublished(cmd.Context(), reader, pins); err != nil {
				return err
			}
			plane, err := reader.Get(cmd.Context(), "", "servicedataplane", pins["SERVICES_DATAPLANE_NAME"])
			if err != nil {
				return err
			}
			if err := sameManagedServicesServerAddresses(plane, inventory); err != nil {
				return err
			}
			initial := nestedInt(plane, "spec", "backupAdmission", "revision") == 0
			if initial {
				if err := verifyManagedServicesCutover(cmd.Context(), session, liveNodes); err != nil {
					return err
				}
			}
			if err := refreshManagedServicesAdmission(cmd.Context(), reader, kubeconfig, pins, inventory, initial); err != nil {
				return err
			}
			fmt.Fprintln(cmd.OutOrStdout(), "Backup admission refreshed against the current API-server Leases")
			return nil
		},
	}
	refresh.Flags().StringVar(&kubeconfig, "kubeconfig", "", "Admin kubeconfig for the named cluster")
	refresh.Flags().StringVar(&sshUser, "ssh-user", "", "SSH user for API-server hostname and initial cutover verification")
	refresh.Flags().StringArrayVar(&sshHosts, "ssh-host", nil, "Reachable control-plane SSH target: NODE=HOST for each server, or HOST for a single-server cluster")
	refresh.Flags().StringArrayVar(&serverArgs, "server", nil, "Authoritative control-plane server as node-name=IP (repeat for every server)")
	cmd.AddCommand(refresh)
	return cmd
}

// mapManagedServicesSSHHosts preserves the live Node names used by API-server
// Leases while replacing unreachable private addresses with operator-provided
// SSH routes. Every server must be named when there is more than one.
func mapManagedServicesSSHHosts(nodes []oidccutover.Node, targets []string, sshUser string) ([]oidccutover.Node, error) {
	if len(targets) == 0 {
		if sshUser != "" {
			for i := range nodes {
				nodes[i].Host.User = sshUser
			}
		}
		return nodes, nil
	}
	if len(targets) != len(nodes) {
		return nil, fmt.Errorf("named %d SSH target(s) for %d control-plane node(s); name every server", len(targets), len(nodes))
	}
	indexes := make(map[string]int, len(nodes))
	for i, node := range nodes {
		indexes[node.Name] = i
	}
	seen := make(map[string]bool, len(targets))
	for _, raw := range targets {
		name, target, named := strings.Cut(raw, "=")
		if !named {
			if len(nodes) != 1 {
				return nil, fmt.Errorf("SSH target %q must be NODE=HOST on a multi-server cluster", raw)
			}
			name, target = nodes[0].Name, raw
		}
		i, ok := indexes[name]
		if !ok || seen[name] || target == "" {
			return nil, fmt.Errorf("SSH target %q does not identify one distinct live control-plane node", raw)
		}
		seen[name] = true
		host := parseSSHHostArg(target)
		if host.User == "" && sshUser != "" {
			host.User = sshUser
		}
		nodes[i].Host = host
	}
	return nodes, nil
}

func verifyManagedServicesCutover(ctx context.Context, session *bootstrap.Session, nodes []oidccutover.Node) error {
	if session == nil || session.SSH == nil || session.K8s == nil || len(nodes) == 0 {
		return fmt.Errorf("OIDC cutover verification requires every API server and an SSH-capable session")
	}
	result, err := oidccutover.Run(ctx, oidccutover.Options{SSH: session.SSH, K8s: session.K8s, Nodes: nodes, DryRun: true, Out: io.Discard})
	if err != nil {
		return err
	}
	if len(result.AlreadyWired) != len(nodes) {
		return fmt.Errorf("OIDC cutover is incomplete on one or more API servers")
	}
	return nil
}

func parseManagedServicesServerArgs(args []string) ([]managedServicesServer, error) {
	if len(args) == 0 || len(args) > 9 {
		return nil, fmt.Errorf("provide every API server with --server node-name=IP (1–9 entries)")
	}
	servers := make([]managedServicesServer, 0, len(args))
	for _, entry := range args {
		name, rawIP, ok := strings.Cut(entry, "=")
		ip, err := netip.ParseAddr(rawIP)
		if !ok || name == "" || err != nil {
			return nil, fmt.Errorf("invalid --server %q; use node-name=IP", entry)
		}
		servers = append(servers, managedServicesServer{Name: name, IP: ip.String()})
	}
	return servers, nil
}

func sameManagedServicesServerAddresses(plane map[string]any, inventory []managedServicesServer) error {
	previous := nestedSlice(plane, "spec", "backupAdmission", "servers")
	if len(previous) == 0 {
		return nil
	}
	oldAddresses := make([]string, 0, len(previous))
	for _, raw := range previous {
		object, ok := raw.(map[string]any)
		if !ok {
			return fmt.Errorf("existing backup-admission server is invalid")
		}
		oldAddresses = append(oldAddresses, nestedString(object, "address"))
	}
	newAddresses := make([]string, 0, len(inventory))
	for _, server := range inventory {
		ip, err := netip.ParseAddr(server.IP)
		if err != nil {
			return err
		}
		newAddresses = append(newAddresses, netip.AddrPortFrom(ip, 6443).String())
	}
	sort.Strings(oldAddresses)
	sort.Strings(newAddresses)
	if strings.Join(oldAddresses, ",") != strings.Join(newAddresses, ",") {
		return fmt.Errorf("API-server IP set changed; use the membership procedure before refreshing backup admission")
	}
	return nil
}

func sameManagedServicesInventory(live, supplied []managedServicesServer) error {
	if len(live) != len(supplied) {
		return fmt.Errorf("--server list does not cover every live control-plane node")
	}
	byName := make(map[string]string, len(live))
	for _, server := range live {
		byName[server.Name] = server.IP
	}
	for _, server := range supplied {
		if byName[server.Name] != server.IP {
			return fmt.Errorf("--server %s does not match the live control-plane inventory", server.Name)
		}
		delete(byName, server.Name)
	}
	if len(byName) != 0 {
		return fmt.Errorf("--server list does not cover every live control-plane node")
	}
	return nil
}
