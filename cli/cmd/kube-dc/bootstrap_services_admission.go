package main

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"net/netip"
	"os/exec"
	"reflect"
	"sort"
	"strings"
	"time"

	"github.com/shalb/kube-dc/cli/internal/bootstrap/oidccutover"
	"github.com/shalb/kube-dc/cli/internal/bootstrap/ports"
)

type managedServicesServer struct {
	Name     string
	IP       string
	Hostname string
}

type managedServicesAdmissionServer struct {
	Address       string `json:"address"`
	TLSServerName string `json:"tlsServerName"`
	LeaseRef      struct {
		Name string `json:"name"`
		UID  string `json:"uid"`
	} `json:"leaseRef"`
	HolderIdentity string `json:"holderIdentity"`
}

type managedServicesAdmissionTopology struct {
	Revision           int                              `json:"revision"`
	State              string                           `json:"state"`
	SystemNamespaceUID string                           `json:"systemNamespaceUID"`
	Servers            []managedServicesAdmissionServer `json:"servers"`
}

// buildManagedServicesAdmissionTopology refuses missing, duplicated and stale
// API-server identity Leases. Discovery of Leases alone is insufficient: the
// caller supplies the complete, independently discovered server inventory.
func buildManagedServicesAdmissionTopology(inventory []managedServicesServer, leases map[string]any, systemUID string, previousRevision int) (managedServicesAdmissionTopology, error) {
	var topology managedServicesAdmissionTopology
	if len(inventory) == 0 || len(inventory) > 9 || systemUID == "" || previousRevision < 0 {
		return topology, fmt.Errorf("backup admission requires 1–9 inventoried servers, kube-system UID and a nonnegative revision")
	}
	byNode := make(map[string]map[string]any)
	for _, raw := range nestedSlice(leases, "items") {
		lease, ok := raw.(map[string]any)
		if !ok || nestedString(lease, "metadata", "labels", "apiserver.kubernetes.io/identity") != "kube-apiserver" {
			continue
		}
		name := nestedString(lease, "metadata", "labels", "kubernetes.io/hostname")
		if name == "" || byNode[name] != nil {
			return topology, fmt.Errorf("API-server Lease has no unique hostname label")
		}
		byNode[name] = lease
	}
	if len(byNode) != len(inventory) {
		return topology, fmt.Errorf("API-server identity Lease count %d differs from server inventory %d", len(byNode), len(inventory))
	}
	topology = managedServicesAdmissionTopology{Revision: previousRevision + 1, State: "Active", SystemNamespaceUID: systemUID}
	seenName, seenIP, seenLease := make(map[string]bool), make(map[string]bool), make(map[string]bool)
	for _, server := range inventory {
		ip, err := netip.ParseAddr(server.IP)
		if err != nil || !ip.IsValid() || seenName[server.Name] || seenIP[ip.String()] {
			return managedServicesAdmissionTopology{}, fmt.Errorf("server inventory has an invalid or duplicate name/IP")
		}
		seenName[server.Name], seenIP[ip.String()] = true, true
		if server.Hostname == "" {
			return managedServicesAdmissionTopology{}, fmt.Errorf("API-server %s has no verified hostname mapping", server.Name)
		}
		lease := byNode[server.Hostname]
		if lease == nil {
			return managedServicesAdmissionTopology{}, fmt.Errorf("API-server %s has no matching identity Lease", server.Name)
		}
		name, uid := nestedString(lease, "metadata", "name"), nestedString(lease, "metadata", "uid")
		holder := nestedString(lease, "spec", "holderIdentity")
		if name == "" || uid == "" || holder == "" || seenLease[name] || nestedString(lease, "metadata", "deletionTimestamp") != "" {
			return managedServicesAdmissionTopology{}, fmt.Errorf("API-server %s has an incomplete or duplicate identity Lease", server.Name)
		}
		seenLease[name] = true
		entry := managedServicesAdmissionServer{Address: netip.AddrPortFrom(ip, 6443).String(), TLSServerName: "kubernetes.default.svc", HolderIdentity: holder}
		entry.LeaseRef.Name, entry.LeaseRef.UID = name, uid
		topology.Servers = append(topology.Servers, entry)
	}
	sort.Slice(topology.Servers, func(i, j int) bool { return topology.Servers[i].Address < topology.Servers[j].Address })
	return topology, nil
}

func applyManagedServicesAdmission(ctx context.Context, transport managedServicesKubectl, planeName string, topology managedServicesAdmissionTopology) error {
	if transport.kubeconfig == "" || transport.contextName == "" || planeName == "" || len(topology.Servers) == 0 {
		return fmt.Errorf("incomplete managed-services admission apply")
	}
	object := map[string]any{
		"apiVersion": "services.kube-dc.com/v1alpha1", "kind": "ServiceDataPlane",
		"metadata": map[string]any{"name": planeName},
		"spec":     map[string]any{"backupAdmission": topology},
	}
	body, err := json.Marshal(object)
	if err != nil {
		return err
	}
	callCtx, cancel := context.WithTimeout(ctx, 30*time.Second)
	defer cancel()
	args := append(transport.kubectlArgs(), "--request-timeout=20s", "apply", "--server-side", "--field-manager=kube-dc-cli", "-f", "-")
	cmd := exec.CommandContext(callCtx, "kubectl", args...)
	cmd.Stdin = bytes.NewReader(body)
	output, err := cmd.CombinedOutput()
	if err != nil {
		return fmt.Errorf("apply managed-services backup admission: %w: %s", err, strings.TrimSpace(string(output)))
	}
	return nil
}

func refreshManagedServicesAdmission(ctx context.Context, reader managedServicesReader, kubeconfig string, pins map[string]string, inventory []managedServicesServer, cutoverComplete bool) error {
	config, err := reader.Get(ctx, "flux-system", "configmap", "cluster-config")
	if err != nil {
		return err
	}
	if nestedString(config, "data", "CLUSTER_NAME") != pins["CLUSTER_NAME"] ||
		nestedString(config, "data", "SERVICES_CELL_ID") != pins["SERVICES_CELL_ID"] {
		return fmt.Errorf("backup admission target differs from the reviewed cluster and cell")
	}
	planeName := pins["SERVICES_DATAPLANE_NAME"]
	if planeName == "" {
		return fmt.Errorf("reviewed overlay has no data plane name")
	}
	plane, err := reader.Get(ctx, "", "servicedataplane", planeName)
	if err != nil {
		return err
	}
	service, err := reader.Get(ctx, "kube-dc-services", "helmrelease", "kube-dc-services")
	if err != nil {
		return err
	}
	accountName := "dp-" + planeName
	account, err := reader.Get(ctx, "kube-dc-services", "serviceaccount", accountName)
	if err != nil {
		return err
	}
	secret, err := reader.Get(ctx, "kube-dc-services", "secret", accountName+"-token")
	if err != nil {
		return err
	}
	if err := verifyManagedServicesSelfPlane(plane, service, account, secret, pins); err != nil {
		return err
	}
	previous := nestedInt(plane, "spec", "backupAdmission", "revision")
	if previous == 0 && !cutoverComplete {
		return fmt.Errorf("initial backup admission requires verified OIDC cutover on every API server")
	}
	if previous > 0 {
		if nestedString(plane, "spec", "backupAdmission", "state") != "Active" {
			return fmt.Errorf("backup admission is paused; refresh cannot resume it")
		}
		if err := sameManagedServicesServerAddresses(plane, inventory); err != nil {
			return err
		}
	}
	system, err := reader.Get(ctx, "", "namespace", "kube-system")
	if err != nil {
		return err
	}
	systemUID := nestedString(system, "metadata", "uid")
	if existingUID := nestedString(plane, "spec", "backupAdmission", "systemNamespaceUID"); existingUID != "" && existingUID != systemUID {
		return fmt.Errorf("backup admission points to a different kube-system namespace UID")
	}
	leases, err := reader.Get(ctx, "kube-system", "leases", "")
	if err != nil {
		return err
	}
	nodes, err := reader.Get(ctx, "", "nodes", "")
	if err != nil {
		return err
	}
	inventory, err = completeManagedServicesInventory(inventory, nodes)
	if err != nil {
		return err
	}
	proposed, err := buildManagedServicesAdmissionTopology(inventory, leases, systemUID, previous)
	if err != nil {
		return err
	}
	if previous > 0 {
		var current managedServicesAdmissionTopology
		body, err := json.Marshal(nestedValue(plane, "spec", "backupAdmission"))
		if err != nil {
			return err
		}
		if err := json.Unmarshal(body, &current); err != nil {
			return err
		}
		proposed.Revision = previous
		if reflect.DeepEqual(current, proposed) {
			return nil
		}
		proposed.Revision = previous + 1
	}
	transport, ok := reader.(managedServicesKubectl)
	if !ok || transport.kubeconfig != kubeconfig || transport.contextName == "" {
		return fmt.Errorf("backup admission apply requires a pinned managed-services kubeconfig context")
	}
	if err := applyManagedServicesAdmission(ctx, transport, planeName, proposed); err != nil {
		return err
	}
	updated, err := reader.Get(ctx, "", "servicedataplane", planeName)
	if err != nil {
		return err
	}
	encoded, err := json.Marshal(nestedValue(updated, "spec", "backupAdmission"))
	if err != nil {
		return err
	}
	var observed managedServicesAdmissionTopology
	if err := json.Unmarshal(encoded, &observed); err != nil {
		return err
	}
	if !reflect.DeepEqual(observed, proposed) {
		return fmt.Errorf("backup admission apply was not observed on the data plane")
	}
	return nil
}

func verifyManagedServicesSelfPlane(plane, service, account, secret map[string]any, pins map[string]string) error {
	name := pins["SERVICES_DATAPLANE_NAME"]
	accountName := "dp-" + name
	if nestedString(plane, "metadata", "name") != name ||
		nestedString(plane, "spec", "cell") != pins["SERVICES_CELL_ID"] ||
		nestedString(plane, "spec", "provider") != "kube-dc" ||
		nestedValue(plane, "spec", "source") != nil ||
		nestedString(plane, "spec", "connector", "type") != "Direct" ||
		nestedString(plane, "spec", "connector", "direct", "kubeconfigSecretRef", "name") != accountName+"-token" ||
		nestedString(plane, "spec", "connector", "direct", "kubeconfigSecretRef", "namespace") != "kube-dc-services" ||
		nestedString(plane, "spec", "connector", "direct", "kubeconfigSecretRef", "key") != "value" ||
		nestedString(service, "spec", "values", "dataPlane", "selfName") != name ||
		nestedString(account, "metadata", "name") != accountName ||
		nestedString(account, "metadata", "uid") == "" ||
		nestedString(secret, "metadata", "name") != accountName+"-token" ||
		nestedString(secret, "type") != "kubernetes.io/service-account-token" ||
		nestedString(secret, "metadata", "annotations", "kubernetes.io/service-account.name") != accountName ||
		nestedString(secret, "metadata", "annotations", "kubernetes.io/service-account.uid") != nestedString(account, "metadata", "uid") ||
		nestedString(secret, "data", "token") == "" || nestedString(secret, "data", "ca.crt") == "" {
		return fmt.Errorf("backup admission target is not the local managed-services self plane")
	}
	return nil
}

func completeManagedServicesInventory(inventory []managedServicesServer, nodes map[string]any) ([]managedServicesServer, error) {
	byName := make(map[string]map[string]any)
	for _, raw := range nestedSlice(nodes, "items") {
		if node, ok := raw.(map[string]any); ok {
			byName[nestedString(node, "metadata", "name")] = node
		}
	}
	completed := make([]managedServicesServer, len(inventory))
	seenHostname := make(map[string]bool)
	for i, server := range inventory {
		node := byName[server.Name]
		if node == nil {
			return nil, fmt.Errorf("API-server %s is not a live Node", server.Name)
		}
		hostname := server.Hostname
		if hostname == "" || seenHostname[hostname] {
			return nil, fmt.Errorf("API-server OS hostname is absent or duplicated")
		}
		seenHostname[hostname] = true
		addressMatches := false
		for _, raw := range nestedSlice(node, "status", "addresses") {
			address, ok := raw.(map[string]any)
			if ok && nestedString(address, "type") == "InternalIP" && nestedString(address, "address") == server.IP {
				addressMatches = true
			}
		}
		if !addressMatches {
			return nil, fmt.Errorf("API-server %s IP differs from the live Node InternalIP", server.Name)
		}
		completed[i] = server
	}
	return completed, nil
}

// resolveManagedServicesOSHostnames reads each server's own hostname over the
// same SSH target the OIDC cutover verified. RKE2 node-name overrides can make
// Node.metadata.name and its hostname label differ from this OS hostname.
func resolveManagedServicesOSHostnames(ctx context.Context, ssh ports.SSHClient, nodes []oidccutover.Node) ([]managedServicesServer, error) {
	if ssh == nil || len(nodes) == 0 {
		return nil, fmt.Errorf("API-server OS hostnames require an SSH client and full node inventory")
	}
	inventory := make([]managedServicesServer, 0, len(nodes))
	for _, node := range nodes {
		callCtx, cancel := context.WithTimeout(ctx, 10*time.Second)
		output, err := ssh.Run(callCtx, node.Host, "hostname")
		cancel()
		if err != nil {
			return nil, fmt.Errorf("read API-server OS hostname for %s: %w", node.Name, err)
		}
		hostname := strings.TrimSpace(string(output))
		if hostname == "" || len(hostname) > 253 || strings.ContainsAny(hostname, " \t\r\n/") {
			return nil, fmt.Errorf("API-server %s returned an invalid OS hostname", node.Name)
		}
		ip := node.InternalIP
		if ip == "" { // callers with a direct SSH target predate separate addresses
			ip = node.Host.Hostname
		}
		inventory = append(inventory, managedServicesServer{Name: node.Name, IP: ip, Hostname: hostname})
	}
	return inventory, nil
}
