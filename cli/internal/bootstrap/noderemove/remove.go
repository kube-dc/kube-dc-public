// Package noderemove drives `kube-dc bootstrap remove-node` — the safe
// teardown counterpart to `bootstrap install --join-server`. It removes a
// node from a live kube-dc cluster in the order that protects etcd
// quorum:
//
//	(control-plane/etcd node) etcd member remove  ← FIRST, while healthy
//	cordon → drain → delete node → node-side rke2 teardown over SSH
//
// The ordering is the whole point: deleting a control-plane node/VM
// without first removing its etcd member strands the member and, on a
// 2-member cluster, breaks quorum the moment the node stops (learned the
// hard way during the HA-join live test). remove-node encodes that so
// operators can't trip it.
//
// Cluster-side steps shell out to the operator's `kubectl` (so `drain`
// reuses kubectl's own PDB-aware eviction logic rather than a fragile
// re-implementation); the node-side teardown uses the SSH port.
package noderemove

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"strings"

	"github.com/shalb/kube-dc/cli/internal/bootstrap/ports"
)

// Kubectl runs the operator's kubectl against the target cluster. The
// real impl shells the binary with the resolved KUBECONFIG; tests inject
// a fake. Returns combined stdout+stderr and a non-nil error on failure.
type Kubectl interface {
	Run(ctx context.Context, args ...string) ([]byte, error)
}

// etcd client-cert paths inside the RKE2 etcd static pod (RKE2
// convention — same layout every control-plane node).
const (
	etcdCA   = "/var/lib/rancher/rke2/server/tls/etcd/server-ca.crt"
	etcdCert = "/var/lib/rancher/rke2/server/tls/etcd/server-client.crt"
	etcdKey  = "/var/lib/rancher/rke2/server/tls/etcd/server-client.key"
	etcdEP   = "https://127.0.0.1:2379"
)

// Errors.
var (
	ErrMissingDependency = fmt.Errorf("remove-node: missing dependency")
	ErrNodeNotFound      = fmt.Errorf("remove-node: node not found")
	ErrLastControlPlane  = fmt.Errorf("remove-node: refusing to remove the last control-plane/etcd node")
	ErrNoEtcdPeer        = fmt.Errorf("remove-node: no other etcd pod to run member-remove from")
	ErrEtcdMemberMissing = fmt.Errorf("remove-node: could not find the node's etcd member")
)

// Options parameterize a node removal.
type Options struct {
	Kubectl Kubectl
	// SSH + Node drive the node-side rke2 teardown. SSH may be nil (or
	// Node zero) to skip teardown — e.g. the node is already gone.
	SSH  ports.SSHClient
	Node ports.SSHHost

	NodeName string

	// Uninstall runs rke2-uninstall.sh (full removal) instead of
	// rke2-killall.sh (stop only) during node-side teardown.
	Uninstall bool
	// SkipDrain skips cordon+drain (delete the node object directly).
	SkipDrain bool
	// DrainTimeout is passed to `kubectl drain --timeout` (default 120s).
	DrainTimeout string
	// Force passes --force/--disable-eviction to drain and continues past
	// a best-effort teardown failure.
	Force bool

	DryRun bool
	Out    io.Writer
}

type nodeInfo struct {
	name   string
	labels map[string]string
}

func (n nodeInfo) isEtcd() bool {
	_, etcd := n.labels["node-role.kubernetes.io/etcd"]
	_, cp := n.labels["node-role.kubernetes.io/control-plane"]
	_, master := n.labels["node-role.kubernetes.io/master"]
	return etcd || cp || master
}

// Remove removes NodeName from the cluster, protecting etcd quorum.
func Remove(ctx context.Context, o Options) error {
	out := o.Out
	if out == nil {
		out = io.Discard
	}
	if o.Kubectl == nil {
		return fmt.Errorf("%w: Kubectl", ErrMissingDependency)
	}
	if strings.TrimSpace(o.NodeName) == "" {
		return fmt.Errorf("%w: NodeName", ErrMissingDependency)
	}
	if o.DrainTimeout == "" {
		o.DrainTimeout = "120s"
	}

	nodes, err := o.listNodes(ctx)
	if err != nil {
		return err
	}
	target, ok := nodes[o.NodeName]
	if !ok {
		return fmt.Errorf("%w: %q (kubectl sees: %s)", ErrNodeNotFound, o.NodeName, strings.Join(nodeNames(nodes), ", "))
	}

	// Guard: never remove the last control-plane/etcd node — that
	// destroys the cluster.
	etcdCount := 0
	for _, n := range nodes {
		if n.isEtcd() {
			etcdCount++
		}
	}
	targetIsEtcd := target.isEtcd()
	if targetIsEtcd && etcdCount <= 1 {
		return fmt.Errorf("%w (%q is the only one)", ErrLastControlPlane, o.NodeName)
	}

	// Resolve the etcd member (if any) BEFORE mutating anything, so the
	// plan can show it and the dry-run is fully informative.
	var memberID, peerPod string
	if targetIsEtcd {
		peerPod, err = o.findEtcdPeerPod(ctx, o.NodeName)
		if err != nil {
			return err
		}
		memberID, err = o.findEtcdMemberID(ctx, peerPod, o.NodeName)
		if err != nil {
			return err
		}
	}

	renderPlan(out, o, target, etcdCount, memberID)
	if o.DryRun {
		fmt.Fprintln(out, "[remove-node] --dry-run: no changes made.")
		return nil
	}

	// 1. etcd member remove FIRST (while the member is still healthy) so
	// quorum shrinks cleanly from N to N-1 rather than being stranded.
	if targetIsEtcd {
		fmt.Fprintf(out, "[remove-node] removing etcd member %s (%s) via pod %s\n", memberID, o.NodeName, peerPod)
		if _, err := o.etcdctl(ctx, peerPod, "member", "remove", memberID); err != nil {
			return fmt.Errorf("remove-node: etcd member remove %s: %w", memberID, err)
		}
	}

	// 2. cordon + drain (unless skipped).
	if !o.SkipDrain {
		fmt.Fprintf(out, "[remove-node] cordoning %s\n", o.NodeName)
		if _, err := o.Kubectl.Run(ctx, "cordon", o.NodeName); err != nil {
			return fmt.Errorf("remove-node: cordon: %w", err)
		}
		fmt.Fprintf(out, "[remove-node] draining %s (timeout %s)\n", o.NodeName, o.DrainTimeout)
		drainArgs := []string{"drain", o.NodeName, "--ignore-daemonsets", "--delete-emptydir-data", "--timeout", o.DrainTimeout}
		if o.Force {
			drainArgs = append(drainArgs, "--force", "--disable-eviction")
		}
		if dout, err := o.Kubectl.Run(ctx, drainArgs...); err != nil {
			if !o.Force {
				return fmt.Errorf("remove-node: drain (pass --force to override): %w\n%s", err, strings.TrimSpace(string(dout)))
			}
			fmt.Fprintf(out, "[remove-node] drain reported issues (continuing, --force): %s\n", strings.TrimSpace(string(dout)))
		}
	}

	// 3. delete the node object.
	fmt.Fprintf(out, "[remove-node] deleting node object %s\n", o.NodeName)
	if _, err := o.Kubectl.Run(ctx, "delete", "node", o.NodeName, "--ignore-not-found"); err != nil {
		return fmt.Errorf("remove-node: delete node: %w", err)
	}

	// 4. node-side rke2 teardown over SSH (best-effort — the node may be
	// unreachable; that's fine, the cluster-side removal already
	// succeeded).
	if o.SSH != nil && (o.Node.Alias != "" || o.Node.Hostname != "") {
		script := "/usr/local/bin/rke2-killall.sh"
		if o.Uninstall {
			script = "/usr/local/bin/rke2-uninstall.sh"
		}
		fmt.Fprintf(out, "[remove-node] node-side teardown: sudo -n %s\n", script)
		if teardown, err := o.SSH.Run(ctx, o.Node, "sudo -n "+script); err != nil {
			fmt.Fprintf(out, "[remove-node] WARNING: node-side teardown failed (node already removed from the cluster; run %s manually if the host still runs rke2): %v\n", script, err)
		} else if len(teardown) > 0 {
			fmt.Fprintln(out, indent(string(teardown), "    | "))
		}
	} else {
		fmt.Fprintln(out, "[remove-node] no --ssh-host: skipping node-side rke2 teardown (run rke2-killall.sh / rke2-uninstall.sh on the host yourself)")
	}

	fmt.Fprintf(out, "[remove-node] done — %s removed from the cluster.\n", o.NodeName)
	fmt.Fprintln(out, "[remove-node] reminder: delete the underlying VM/host separately — kube-dc does not manage node infrastructure.")
	return nil
}

// listNodes returns every node keyed by name, with its labels.
func (o Options) listNodes(ctx context.Context) (map[string]nodeInfo, error) {
	raw, err := o.Kubectl.Run(ctx, "get", "nodes", "-o", "json")
	if err != nil {
		return nil, fmt.Errorf("remove-node: list nodes: %w\n%s", err, strings.TrimSpace(string(raw)))
	}
	var list struct {
		Items []struct {
			Metadata struct {
				Name   string            `json:"name"`
				Labels map[string]string `json:"labels"`
			} `json:"metadata"`
		} `json:"items"`
	}
	if err := json.Unmarshal(raw, &list); err != nil {
		return nil, fmt.Errorf("remove-node: parse nodes json: %w", err)
	}
	nodes := make(map[string]nodeInfo, len(list.Items))
	for _, it := range list.Items {
		nodes[it.Metadata.Name] = nodeInfo{name: it.Metadata.Name, labels: it.Metadata.Labels}
	}
	return nodes, nil
}

// findEtcdPeerPod returns the name of an etcd static pod NOT on the node
// being removed (member-remove must run from a surviving member).
func (o Options) findEtcdPeerPod(ctx context.Context, exclude string) (string, error) {
	raw, err := o.Kubectl.Run(ctx, "get", "pods", "-n", "kube-system", "-l", "component=etcd", "-o", "json")
	if err != nil {
		return "", fmt.Errorf("remove-node: list etcd pods: %w\n%s", err, strings.TrimSpace(string(raw)))
	}
	var list struct {
		Items []struct {
			Metadata struct {
				Name string `json:"name"`
			} `json:"metadata"`
			Spec struct {
				NodeName string `json:"nodeName"`
			} `json:"spec"`
		} `json:"items"`
	}
	if err := json.Unmarshal(raw, &list); err != nil {
		return "", fmt.Errorf("remove-node: parse etcd pods json: %w", err)
	}
	for _, it := range list.Items {
		if it.Spec.NodeName != exclude {
			return it.Metadata.Name, nil
		}
	}
	return "", ErrNoEtcdPeer
}

// findEtcdMemberID runs `etcdctl member list` from peerPod and returns
// the hex member ID whose name belongs to nodeName (RKE2 names members
// `<node>-<hash>`), or whose peer URL is otherwise attributable to it.
func (o Options) findEtcdMemberID(ctx context.Context, peerPod, nodeName string) (string, error) {
	out, err := o.etcdctl(ctx, peerPod, "-w", "simple", "member", "list")
	if err != nil {
		return "", fmt.Errorf("remove-node: etcd member list: %w\n%s", err, strings.TrimSpace(string(out)))
	}
	// simple format: `<hexid>, started, <name>, <peerURL>, <clientURL>, <isLearner>`
	for _, line := range strings.Split(string(out), "\n") {
		fields := strings.Split(line, ", ")
		if len(fields) < 3 {
			continue
		}
		id, name := strings.TrimSpace(fields[0]), strings.TrimSpace(fields[2])
		if name == nodeName || strings.HasPrefix(name, nodeName+"-") {
			return id, nil
		}
	}
	return "", fmt.Errorf("%w: %q not in member list", ErrEtcdMemberMissing, nodeName)
}

// etcdctl runs etcdctl inside peerPod with the RKE2 client certs. The
// etcd container is distroless (no shell) so etcdctl is invoked directly
// with its flags — never `sh -c`.
func (o Options) etcdctl(ctx context.Context, peerPod string, etcdctlArgs ...string) ([]byte, error) {
	args := []string{
		"exec", "-n", "kube-system", peerPod, "--",
		"etcdctl",
		"--cacert", etcdCA, "--cert", etcdCert, "--key", etcdKey, "--endpoints", etcdEP,
	}
	args = append(args, etcdctlArgs...)
	return o.Kubectl.Run(ctx, args...)
}

func renderPlan(out io.Writer, o Options, target nodeInfo, etcdCount int, memberID string) {
	fmt.Fprintf(out, "== remove-node plan — %q ==\n", o.NodeName)
	role := "worker"
	if target.isEtcd() {
		role = "control-plane / etcd"
	}
	fmt.Fprintf(out, "  role:              %s\n", role)
	if target.isEtcd() {
		fmt.Fprintf(out, "  etcd members:      %d → %d after removal\n", etcdCount, etcdCount-1)
		fmt.Fprintf(out, "  etcd member:       %s (removed FIRST, before drain/delete)\n", memberID)
		if etcdCount-1 == 1 {
			fmt.Fprintln(out, "  ⚠ warning:         this leaves a SINGLE control-plane — no HA/etcd redundancy until you add another")
		} else if (etcdCount-1)%2 == 0 && etcdCount-1 > 0 {
			fmt.Fprintf(out, "  ⚠ warning:         %d control-planes is an EVEN count — prefer an odd number for etcd quorum\n", etcdCount-1)
		}
	}
	if o.SkipDrain {
		fmt.Fprintln(out, "  drain:             skipped (--skip-drain)")
	} else {
		fmt.Fprintf(out, "  drain:             cordon + drain (timeout %s%s)\n", o.DrainTimeout, forceSuffix(o.Force))
	}
	teardown := "rke2-killall.sh"
	if o.Uninstall {
		teardown = "rke2-uninstall.sh (full removal)"
	}
	if o.SSH != nil && (o.Node.Alias != "" || o.Node.Hostname != "") {
		fmt.Fprintf(out, "  node teardown:     %s over SSH (%s)\n", teardown, sshHostArg(o.Node))
	} else {
		fmt.Fprintln(out, "  node teardown:     skipped (no --ssh-host) — run it on the host yourself")
	}
}

func forceSuffix(force bool) string {
	if force {
		return ", --force"
	}
	return ""
}

func nodeNames(nodes map[string]nodeInfo) []string {
	names := make([]string, 0, len(nodes))
	for n := range nodes {
		names = append(names, n)
	}
	return names
}

func sshHostArg(h ports.SSHHost) string {
	if h.Alias != "" {
		return h.Alias
	}
	if h.User != "" {
		return h.User + "@" + h.Hostname
	}
	return h.Hostname
}

func indent(s, prefix string) string {
	lines := strings.Split(strings.TrimRight(s, "\n"), "\n")
	for i, l := range lines {
		lines[i] = prefix + l
	}
	return strings.Join(lines, "\n")
}
