package main

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"regexp"
	"strings"

	"github.com/shalb/kube-dc/cli/internal/bootstrap/ports"
)

const databasePluginFile = "kube-dc-postgresql-database-plugin-v0.4.0"

var databasePluginSHA256 = regexp.MustCompile(`^[a-f0-9]{64}$`)

type qualifiedDatabasePlugin struct {
	active string
	pods   []string
}

// Verify the binary on every voter before minting a root token. A catalog
// entry would strand revocations after failover if any voter lacked it.
func qualifyDatabasePlugin(ctx context.Context, bao ports.OpenBaoClient, k8s ports.K8sClient, sha string) (qualifiedDatabasePlugin, error) {
	if !databasePluginSHA256.MatchString(sha) {
		return qualifiedDatabasePlugin{}, fmt.Errorf("database plugin SHA-256 must be 64 lowercase hex digits")
	}
	pods, err := bao.PodList(ctx)
	if err != nil {
		return qualifiedDatabasePlugin{}, fmt.Errorf("database plugin: voter inventory unavailable: %w", err)
	}
	if len(pods) == 0 {
		return qualifiedDatabasePlugin{}, fmt.Errorf("database plugin: no OpenBao voters")
	}
	active := ""
	for _, pod := range pods {
		status, err := bao.Status(ctx, pod)
		if err != nil || !status.Initialized || status.Sealed || status.StorageType != "raft" {
			return qualifiedDatabasePlugin{}, fmt.Errorf("database plugin: voter %s is not healthy and unsealed", pod)
		}
		args := []string{"sha256sum", "/plugins/" + databasePluginFile}
		out, err := k8s.PodExec(ctx, "openbao", pod, args, nil)
		matched := false
		for _, line := range strings.Split(string(out), "\n") {
			fields := strings.Fields(line)
			matched = matched || len(fields) == 2 && fields[0] == sha && fields[1] == "/plugins/"+databasePluginFile
		}
		if err != nil || !matched {
			out, err = k8s.PodExecViaKubectl(ctx, "openbao", pod, args, nil)
			for _, line := range strings.Split(string(out), "\n") {
				fields := strings.Fields(line)
				matched = matched || len(fields) == 2 && fields[0] == sha && fields[1] == "/plugins/"+databasePluginFile
			}
		}
		if err != nil || !matched {
			return qualifiedDatabasePlugin{}, fmt.Errorf("database plugin: voter %s lacks the pinned executable", pod)
		}
		if status.HAMode == "active" {
			active = pod
		}
	}
	if active == "" {
		return qualifiedDatabasePlugin{}, fmt.Errorf("database plugin: no active OpenBao voter")
	}
	return qualifiedDatabasePlugin{active: active, pods: pods}, nil
}

// Register during setup-controller-auth's already scoped root ceremony. The
// token rides stdin only; neither the command nor its output contains it.
func registerDatabasePlugin(ctx context.Context, k8s ports.K8sClient, pod string, token []byte, sha string) error {
	stdin := append(append([]byte(nil), token...), '\n')
	defer func() {
		for i := range stdin {
			stdin[i] = 0
		}
	}()
	const script = `read -r tok
BAO_ADDR=http://127.0.0.1:8200 BAO_TOKEN="$tok" bao plugin register -sha256="$1" -command=kube-dc-postgresql-database-plugin-v0.4.0 -version=v0.4.0 database kube-dc-postgresql-database-plugin >/dev/null || exit 1
echo KDC_DATABASE_PLUGIN_REGISTERED`
	out, err := k8s.PodExec(ctx, "openbao", pod, []string{"sh", "-c", script, "_", sha}, stdin)
	if err != nil {
		return fmt.Errorf("register database plugin on %s: %w", pod, err)
	}
	if !strings.Contains(string(out), "KDC_DATABASE_PLUGIN_REGISTERED") {
		return fmt.Errorf("register database plugin on %s: success acknowledgement missing", pod)
	}
	return nil
}

// The pod list alone is insufficient: a missing StatefulSet pod can still be
// a Raft peer. Query the authenticated membership and require every peer to
// be one of the verified pods before publishing the plugin catalog entry.
func verifyDatabasePluginPeers(ctx context.Context, k8s ports.K8sClient, qualification qualifiedDatabasePlugin, token []byte) error {
	stdin := append(append([]byte(nil), token...), '\n')
	defer func() {
		for i := range stdin {
			stdin[i] = 0
		}
	}()
	const script = `read -r tok
BAO_ADDR=http://127.0.0.1:8200 BAO_TOKEN="$tok" bao operator raft list-peers -format=json`
	out, err := k8s.PodExec(ctx, "openbao", qualification.active, []string{"sh", "-c", script}, stdin)
	if err != nil {
		return fmt.Errorf("database plugin: Raft membership unavailable: %w", err)
	}
	start := bytes.IndexByte(out, '{')
	if start < 0 {
		return fmt.Errorf("database plugin: Raft membership response missing")
	}
	var response struct {
		Data struct {
			Config struct {
				Servers []struct {
					NodeID string `json:"node_id"`
				} `json:"servers"`
			} `json:"config"`
		} `json:"data"`
	}
	if err := json.NewDecoder(bytes.NewReader(out[start:])).Decode(&response); err != nil {
		return fmt.Errorf("database plugin: Raft membership response invalid: %w", err)
	}
	verified := make(map[string]bool, len(qualification.pods))
	for _, name := range qualification.pods {
		verified[name] = true
	}
	if len(response.Data.Config.Servers) == 0 || len(response.Data.Config.Servers) != len(verified) {
		return fmt.Errorf("database plugin: Raft peers differ from verified OpenBao pods")
	}
	for _, server := range response.Data.Config.Servers {
		if !verified[server.NodeID] {
			return fmt.Errorf("database plugin: Raft peer %s lacks a verified Pod", server.NodeID)
		}
		delete(verified, server.NodeID)
	}
	return nil
}
