package main

import (
	"context"
	"encoding/json"
	"fmt"
	"net/netip"
	"reflect"

	"github.com/spf13/cobra"

	"github.com/shalb/kube-dc/cli/internal/bootstrap"
	"github.com/shalb/kube-dc/cli/internal/bootstrap/clusterinit"
	"github.com/shalb/kube-dc/cli/internal/bootstrap/oidccutover"
	"github.com/shalb/kube-dc/cli/internal/bootstrap/ports"
)

// bootstrapDoctorServicesCmd checks that the saved deletion topology still
// names every live API server, at its current IP and Lease identity.
func bootstrapDoctorServicesCmd(fleetRepo *string) *cobra.Command {
	var kubeconfig, sshUser string
	cmd := &cobra.Command{
		Use: "services <cluster>", Short: "Check managed-services backup admission against live API servers",
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
			reader, err := pinnedManagedServicesKubectl(cmd.Context(), kubeconfig)
			if err != nil {
				return err
			}
			ssh, err := bootstrap.NewSSHOnly()
			if err != nil {
				return err
			}
			inventory, err := discoverManagedServicesDoctorInventory(cmd.Context(), reader, ssh, sshUser)
			if err != nil {
				return err
			}
			if err := checkManagedServicesAdmissionFresh(cmd.Context(), reader, pins, inventory); err != nil {
				return fmt.Errorf("managed-services backup admission is stale: %w; run bootstrap services admission refresh", err)
			}
			fmt.Fprintln(cmd.OutOrStdout(), "Managed-services backup admission matches the live API servers and Leases")
			return nil
		},
	}
	cmd.Flags().StringVar(&kubeconfig, "kubeconfig", "", "Admin kubeconfig for the named cluster")
	cmd.Flags().StringVar(&sshUser, "ssh-user", "", "SSH user for read-only API-server hostname checks")
	return cmd
}

type managedServicesObjectReader interface {
	Get(context.Context, string, string, string) (map[string]any, error)
}

func discoverManagedServicesDoctorInventory(ctx context.Context, reader managedServicesObjectReader, ssh ports.SSHClient, sshUser string) ([]managedServicesServer, error) {
	nodes, err := reader.Get(ctx, "", "nodes", "")
	if err != nil {
		return nil, err
	}
	var servers []oidccutover.Node
	for _, raw := range nestedSlice(nodes, "items") {
		node, ok := raw.(map[string]any)
		if !ok {
			return nil, fmt.Errorf("live Node inventory is malformed")
		}
		labels, _ := nestedValue(node, "metadata", "labels").(map[string]any)
		_, controlPlane := labels["node-role.kubernetes.io/control-plane"]
		_, legacyMaster := labels["node-role.kubernetes.io/master"]
		if !controlPlane && !legacyMaster {
			continue
		}
		name := nestedString(node, "metadata", "name")
		ip := ""
		for _, rawAddress := range nestedSlice(node, "status", "addresses") {
			address, _ := rawAddress.(map[string]any)
			if nestedString(address, "type") == "InternalIP" {
				if ip != "" {
					return nil, fmt.Errorf("API-server %s has multiple InternalIP addresses", name)
				}
				ip = nestedString(address, "address")
			}
		}
		if name == "" || ip == "" {
			return nil, fmt.Errorf("API-server Node has no name or InternalIP")
		}
		if _, err := netip.ParseAddr(ip); err != nil {
			return nil, fmt.Errorf("API-server %s has an invalid InternalIP", name)
		}
		servers = append(servers, oidccutover.Node{Name: name, Host: ports.SSHHost{Hostname: ip, User: sshUser}})
	}
	if len(servers) == 0 {
		return nil, fmt.Errorf("no live control-plane Nodes were found")
	}
	return resolveManagedServicesOSHostnames(ctx, ssh, servers)
}

func checkManagedServicesAdmissionFresh(ctx context.Context, reader managedServicesObjectReader, pins map[string]string, inventory []managedServicesServer) error {
	config, err := reader.Get(ctx, "flux-system", "configmap", "cluster-config")
	if err != nil {
		return err
	}
	if nestedString(config, "data", "CLUSTER_NAME") != pins["CLUSTER_NAME"] ||
		nestedString(config, "data", "SERVICES_CELL_ID") != pins["SERVICES_CELL_ID"] {
		return fmt.Errorf("live cluster identity differs from the Fleet overlay")
	}
	plane, err := reader.Get(ctx, "", "servicedataplane", pins["SERVICES_DATAPLANE_NAME"])
	if err != nil {
		return err
	}
	rawAdmission := nestedValue(plane, "spec", "backupAdmission")
	admission, ok := rawAdmission.(map[string]any)
	if !ok || nestedString(admission, "state") != "Active" || nestedInt(admission, "revision") <= 0 {
		return fmt.Errorf("backup admission is absent or inactive")
	}
	system, err := reader.Get(ctx, "", "namespace", "kube-system")
	if err != nil {
		return err
	}
	systemUID := nestedString(system, "metadata", "uid")
	if systemUID == "" || nestedString(admission, "systemNamespaceUID") != systemUID {
		return fmt.Errorf("kube-system identity changed")
	}
	nodes, err := reader.Get(ctx, "", "nodes", "")
	if err != nil {
		return err
	}
	inventory, err = completeManagedServicesInventory(inventory, nodes)
	if err != nil {
		return err
	}
	leases, err := reader.Get(ctx, "kube-system", "lease", "")
	if err != nil {
		return err
	}
	expected, err := buildManagedServicesAdmissionTopology(inventory, leases, systemUID, nestedInt(admission, "revision")-1)
	if err != nil {
		return err
	}
	var current managedServicesAdmissionTopology
	encoded, err := json.Marshal(rawAdmission)
	if err != nil {
		return err
	}
	if err := json.Unmarshal(encoded, &current); err != nil {
		return err
	}
	if !reflect.DeepEqual(current, expected) {
		return fmt.Errorf("backup-admission endpoints or Lease identities differ from the live API servers")
	}
	return nil
}
