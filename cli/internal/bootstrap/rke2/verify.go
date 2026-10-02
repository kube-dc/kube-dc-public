package rke2

import (
	"context"
	"encoding/json"
	"fmt"
	"net"
	"regexp"
	"strings"
	"time"

	"github.com/shalb/kube-dc/cli/internal/bootstrap/ports"
)

const maxNodeVerifyOutput = 64 << 10
const defaultNodeVerifyAttempts = 30
const defaultNodeVerifyDelay = 4 * time.Second
const maxNodeVerifyDuration = 3 * time.Minute
const maxNodeReadDuration = 20 * time.Second

var verifiedNodeLabel = regexp.MustCompile(`^[a-z0-9]([-a-z0-9]*[a-z0-9])?$`)

// VerifyNodeOptions describes one node after the RKE2 installer returns.
// Both SSH endpoints must carry independently reviewed host-key fingerprints.
type VerifyNodeOptions struct {
	SSH        ports.CappedSSHHostKeyClient
	Node       ports.SSHHost
	APIHost    ports.SSHHost
	NodeName   string
	Role       string // server or agent
	InternalIP string
	Attempts   int
	RetryDelay time.Duration
}

// VerifiedNode records only non-secret registration evidence. Ready is not a
// requirement: nodes can remain NotReady until the platform installs its CNI.
type VerifiedNode struct {
	Name       string
	UID        string
	InternalIP string
	Role       string
}

// VerifyRegisteredNode waits for the target RKE2 service and for the pinned
// API host to report the selected node with the expected role and internal IP.
// It makes only bounded SSH reads and never prints kubeconfig or join tokens.
func VerifyRegisteredNode(ctx context.Context, o VerifyNodeOptions) (VerifiedNode, error) {
	if err := validateVerifyNodeOptions(o); err != nil {
		return VerifiedNode{}, err
	}
	attempts, delay := o.Attempts, o.RetryDelay
	if attempts == 0 {
		attempts = defaultNodeVerifyAttempts
	}
	if delay == 0 {
		delay = defaultNodeVerifyDelay
	}
	ctx, cancel := context.WithTimeout(ctx, maxNodeVerifyDuration)
	defer cancel()
	var last error
	for attempt := 0; attempt < attempts; attempt++ {
		if err := ctx.Err(); err != nil {
			return VerifiedNode{}, err
		}
		service := "rke2-agent"
		if o.Role == "server" {
			service = "rke2-server"
		}
		body, err := pinnedRead(ctx, o.SSH, o.Node, "sudo -n systemctl show -p LoadState -p ActiveState "+service)
		if err != nil {
			last = fmt.Errorf("read %s service: %w", service, err)
		} else if !activeRKE2Service(body) {
			last = fmt.Errorf("%s service is not active", service)
		} else {
			var node VerifiedNode
			node, last = readRegisteredNode(ctx, o.SSH, o.APIHost, o.NodeName, o.Role, o.InternalIP)
			if last == nil {
				return node, nil
			}
		}
		if attempt+1 == attempts {
			break
		}
		timer := time.NewTimer(delay)
		select {
		case <-ctx.Done():
			timer.Stop()
			return VerifiedNode{}, ctx.Err()
		case <-timer.C:
		}
	}
	return VerifiedNode{}, fmt.Errorf("node %s did not register after %d checks: %w", o.NodeName, attempts, last)
}

// CheckNodeAbsent prevents a join from mistaking a pre-existing node with the
// selected name for the node it just installed. Run it before the join write.
func CheckNodeAbsent(ctx context.Context, ssh ports.CappedSSHHostKeyClient, apiHost ports.SSHHost, nodeName string) error {
	if ssh == nil || apiHost.ExpectedHostKeySHA256 == "" || !validVerifiedNodeName(nodeName) {
		return fmt.Errorf("node absence check needs a pinned API host and valid node name")
	}
	body, err := pinnedRead(ctx, ssh, apiHost, nodeQueryCommand(nodeName))
	if err != nil {
		return fmt.Errorf("query existing node %s: %w", nodeName, err)
	}
	if len(strings.TrimSpace(string(body))) != 0 {
		return fmt.Errorf("node %s already exists in the selected cluster", nodeName)
	}
	return nil
}

func validateVerifyNodeOptions(o VerifyNodeOptions) error {
	if o.SSH == nil || o.Node.ExpectedHostKeySHA256 == "" || o.APIHost.ExpectedHostKeySHA256 == "" ||
		!validVerifiedNodeName(o.NodeName) || (o.Role != "server" && o.Role != "agent") ||
		net.ParseIP(o.InternalIP) == nil || o.Attempts < 0 || o.Attempts > 60 || o.RetryDelay < 0 || o.RetryDelay > 30*time.Second ||
		(o.Node.Alias == "" && o.Node.Hostname == "") || (o.APIHost.Alias == "" && o.APIHost.Hostname == "") {
		return fmt.Errorf("node verification needs pinned hosts, a valid name, role, internal IP, and retry bounds")
	}
	return nil
}

func validVerifiedNodeName(name string) bool {
	if len(name) == 0 || len(name) > 253 {
		return false
	}
	for _, label := range strings.Split(name, ".") {
		if len(label) == 0 || len(label) > 63 || !verifiedNodeLabel.MatchString(label) {
			return false
		}
	}
	return true
}

func pinnedRead(ctx context.Context, ssh ports.CappedSSHHostKeyClient, host ports.SSHHost, command string) ([]byte, error) {
	readCtx, cancel := context.WithTimeout(ctx, maxNodeReadDuration)
	defer cancel()
	body, key, err := ssh.RunCappedWithHostKey(readCtx, host, command, maxNodeVerifyOutput)
	if key.FingerprintSHA256 != "" && key.FingerprintSHA256 != host.ExpectedHostKeySHA256 {
		return nil, fmt.Errorf("SSH host key changed")
	}
	if err != nil {
		return nil, err
	}
	if key.FingerprintSHA256 == "" || key.Address == "" || key.Algorithm == "" {
		return nil, fmt.Errorf("SSH target key evidence is missing")
	}
	if len(body) > maxNodeVerifyOutput {
		return nil, fmt.Errorf("SSH verification output exceeds the size limit")
	}
	return body, nil
}

func activeRKE2Service(body []byte) bool {
	load, active := "", ""
	for _, line := range strings.Split(string(body), "\n") {
		key, value, ok := strings.Cut(strings.TrimSpace(line), "=")
		if !ok {
			continue
		}
		switch key {
		case "LoadState":
			load = value
		case "ActiveState":
			active = value
		}
	}
	return load == "loaded" && active == "active"
}

func nodeQueryCommand(name string) string {
	// validVerifiedNodeName rejects shell metacharacters before this is called.
	return "sudo -n /var/lib/rancher/rke2/bin/kubectl --kubeconfig /etc/rancher/rke2/rke2.yaml get node " + name + " --ignore-not-found -o json"
}

func readRegisteredNode(ctx context.Context, ssh ports.CappedSSHHostKeyClient, apiHost ports.SSHHost, name, role, expectedIP string) (VerifiedNode, error) {
	body, err := pinnedRead(ctx, ssh, apiHost, nodeQueryCommand(name))
	if err != nil {
		return VerifiedNode{}, fmt.Errorf("query cluster API: %w", err)
	}
	if len(strings.TrimSpace(string(body))) == 0 {
		return VerifiedNode{}, fmt.Errorf("node is not registered yet")
	}
	var object struct {
		Kind     string `json:"kind"`
		Metadata struct {
			Name   string            `json:"name"`
			UID    string            `json:"uid"`
			Labels map[string]string `json:"labels"`
		} `json:"metadata"`
		Status struct {
			Addresses []struct {
				Type    string `json:"type"`
				Address string `json:"address"`
			} `json:"addresses"`
		} `json:"status"`
	}
	if err := json.Unmarshal(body, &object); err != nil || object.Kind != "Node" || object.Metadata.Name != name || object.Metadata.UID == "" {
		return VerifiedNode{}, fmt.Errorf("cluster API returned invalid node evidence")
	}
	isServer := false
	for key := range object.Metadata.Labels {
		if key == "node-role.kubernetes.io/control-plane" || key == "node-role.kubernetes.io/master" {
			isServer = true
		}
	}
	if (role == "server") != isServer {
		return VerifiedNode{}, fmt.Errorf("node %s has the wrong cluster role", name)
	}
	for _, address := range object.Status.Addresses {
		if address.Type == "InternalIP" && net.ParseIP(address.Address).Equal(net.ParseIP(expectedIP)) {
			return VerifiedNode{Name: name, UID: object.Metadata.UID, InternalIP: expectedIP, Role: role}, nil
		}
	}
	return VerifiedNode{}, fmt.Errorf("node %s has no matching internal IP", name)
}
