package oidccutover

import (
	"context"
	"fmt"
	"io"
	"sort"
	"strings"
	"time"

	"github.com/shalb/kube-dc/cli/internal/bootstrap/ports"
)

// WebhookNamespace holds the authenticator brought up by Flux in infra-core.
const WebhookNamespace = "oidc-webhook-authenticator"

// Node pairs a control-plane node with the SSH endpoint that reaches it.
type Node struct {
	Name string
	Host ports.SSHHost
}

// Options drives Run.
type Options struct {
	SSH ports.SSHClient
	K8s ports.K8sClient

	// Nodes must cover EVERY control-plane node. Run refuses a partial set:
	// see PartialCutoverHazard.
	Nodes []Node

	// DryRun reports what each node needs and changes nothing.
	DryRun bool

	// Rollback restores the pre-cutover snapshot instead of applying.
	Rollback bool

	// ReadyTimeout bounds the wait for an apiserver to come back after its
	// restart. 30-60s is normal on a single-node cluster.
	ReadyTimeout time.Duration

	// PollInterval paces the readyz probe. Injectable so tests do not sleep
	// through real restart timings.
	PollInterval time.Duration

	Out io.Writer
}

// Result reports per-node outcomes so the caller can summarise honestly.
type Result struct {
	Wired        []string
	AlreadyWired []string
	Skipped      map[string]string
}

// Run performs the cutover one control-plane node at a time, gating each on its
// apiserver coming back before touching the next.
//
// It is sequential and fail-fast BY DESIGN. Restarting several apiservers at
// once can take quorum below tolerance; and stopping at the first failure leaves
// a partially-wired cluster whose symptom (intermittent 401) is at least
// reported here, loudly, with the exact remaining nodes named.
func Run(ctx context.Context, o Options) (Result, error) {
	res := Result{Skipped: map[string]string{}}
	if o.Out == nil {
		o.Out = io.Discard
	}
	if o.ReadyTimeout == 0 {
		o.ReadyTimeout = 5 * time.Minute
	}
	if o.PollInterval <= 0 {
		o.PollInterval = 5 * time.Second
	}
	if len(o.Nodes) == 0 {
		return res, fmt.Errorf("oidccutover: no control-plane nodes given")
	}

	if o.Rollback {
		return res, rollback(ctx, o)
	}

	if err := preflight(ctx, o); err != nil {
		return res, err
	}

	for _, node := range o.Nodes {
		label := node.Name
		if label == "" {
			label = node.Host.Hostname
		}
		fmt.Fprintf(o.Out, "\n[oidc-cutover] %s\n", label)

		body, err := o.SSH.Fetch(ctx, node.Host, RKE2ConfigPath)
		if err != nil {
			return res, fmt.Errorf("oidccutover: %s: read %s: %w", label, RKE2ConfigPath, err)
		}
		patched, changed, err := PatchConfig(string(body))
		if err != nil {
			return res, fmt.Errorf("oidccutover: %s: %w", label, err)
		}
		if !changed {
			fmt.Fprintf(o.Out, "  already wired to the authenticator — nothing to do\n")
			res.AlreadyWired = append(res.AlreadyWired, label)
			continue
		}

		// The :6443 conflict that bit a production node: a hostNetwork Envoy
		// on this control-plane node owns the port via SO_REUSEPORT. After
		// `systemctl restart rke2-server` Envoy is the SOLE owner and the
		// apiserver cannot re-bind, so the node comes back with a dead
		// apiserver. Refuse rather than break it — draining a data-plane pod
		// is the operator's call, not ours.
		if owner, err := foreignPortOwner(ctx, o, node.Host); err != nil {
			// FAIL CLOSED. This check guards against a restart that leaves the
			// apiserver unable to re-bind :6443 — an outcome that takes the node
			// out. "I could not look" is not a reason to proceed.
			return res, fmt.Errorf("oidccutover: %s: cannot determine what is listening on :6443 (%w). "+
				"Refusing to restart rke2-server blind: if something else holds that port the apiserver "+
				"will not come back. Check by hand with `ss -tlpn | grep :6443` on the node", label, err)
		} else if owner != "" {
			reason := fmt.Sprintf(":6443 is held by %q, not kube-apiserver. Restarting rke2-server now "+
				"would leave the apiserver unable to re-bind (SO_REUSEPORT: the second binder must ask, "+
				"and the apiserver only asks when the port is free). Move that workload off this node "+
				"first (`kubectl cordon %s`, delete the hostNetwork pod there), then re-run", owner, label)
			res.Skipped[label] = reason
			return res, fmt.Errorf("oidccutover: %s: %s", label, reason)
		}

		if o.DryRun {
			fmt.Fprintf(o.Out, "  DRY RUN: would write %s (+%d flag line(s)) and restart rke2-server\n",
				RKE2ConfigPath, countAddedFlagLines(string(body), patched))
			res.Wired = append(res.Wired, label)
			continue
		}

		// Snapshot with `cp -n`: never clobber an existing pre-oidc backup. A
		// second run must not overwrite the known-good original with an
		// already-patched copy, or the rollback path restores the wrong thing.
		if _, err := o.SSH.Run(ctx, node.Host, sudo(fmt.Sprintf(
			"cp -n %s %s%s", RKE2ConfigPath, RKE2ConfigPath, BackupSuffix))); err != nil {
			return res, fmt.Errorf("oidccutover: %s: snapshot %s: %w", label, RKE2ConfigPath, err)
		}
		if err := o.SSH.Put(ctx, node.Host, RKE2ConfigPath, []byte(patched), 0o600); err != nil {
			return res, fmt.Errorf("oidccutover: %s: write %s: %w", label, RKE2ConfigPath, err)
		}
		fmt.Fprintf(o.Out, "  %s updated (snapshot at %s%s)\n", RKE2ConfigPath, RKE2ConfigPath, BackupSuffix)

		if _, err := o.SSH.Run(ctx, node.Host, sudo("systemctl restart rke2-server")); err != nil {
			return res, fmt.Errorf("oidccutover: %s: restart rke2-server: %w", label, err)
		}
		fmt.Fprintf(o.Out, "  rke2-server restarting; waiting for the apiserver\n")

		if err := waitAPIServerReady(ctx, o, node); err != nil {
			return res, fmt.Errorf("oidccutover: %s: %w (rollback: restore %s%s on this node and "+
				"`systemctl restart rke2-server`)", label, err, RKE2ConfigPath, BackupSuffix)
		}
		if err := verifyFlagLive(ctx, o, node.Host); err != nil {
			return res, fmt.Errorf("oidccutover: %s: %w", label, err)
		}
		fmt.Fprintf(o.Out, "  apiserver back and running with the webhook flag\n")
		res.Wired = append(res.Wired, label)
	}
	return res, nil
}

// preflight refuses to start unless the authenticator is actually up and every
// node has the kubeconfig the apiserver is about to be pointed at.
//
// Skipping this is how a cluster ends up with an apiserver configured to call a
// webhook that does not exist: it then rejects EVERY token, including the
// service-account tokens its own controllers use, which is far worse than the
// 401s the cutover is meant to fix.
func preflight(ctx context.Context, o Options) error {
	if o.K8s != nil {
		pods, err := o.K8s.ListPodNames(ctx, WebhookNamespace, "")
		switch {
		case err != nil:
			fmt.Fprintf(o.Out, "[oidc-cutover] WARNING: could not list %s pods (%v) — "+
				"verify the authenticator is Ready before continuing\n", WebhookNamespace, err)
		case len(pods) == 0:
			return fmt.Errorf("oidccutover: no pods in namespace %s — the oidc-webhook-authenticator "+
				"is not up yet. Wait for Flux to finish infra-core, then re-run. Pointing the apiserver "+
				"at a webhook that does not answer makes it reject EVERY token, not just Keycloak ones",
				WebhookNamespace)
		default:
			fmt.Fprintf(o.Out, "[oidc-cutover] authenticator: %d pod(s) in %s\n", len(pods), WebhookNamespace)
		}
	}

	var missing []string
	for _, node := range o.Nodes {
		out, err := o.SSH.Run(ctx, node.Host, fmt.Sprintf(
			"test -s %s && echo present || echo absent", WebhookKubeconfigPath))
		if err != nil {
			// Discovery reaches nodes at their InternalIP, which is right for a
			// bastion on the same network and wrong for one that is not —
			// clusters behind a jump host or a VPN need explicit targets.
			return fmt.Errorf("oidccutover: %s: cannot reach %s over SSH (%w). "+
				"Node addresses were discovered from the cluster, which only works when this machine "+
				"can SSH to them directly. Name reachable targets instead: "+
				"--ssh-host user@host (repeatable, ssh_config aliases work)",
				node.Name, sshTarget(node.Host), err)
		}
		if !strings.Contains(string(out), "present") {
			missing = append(missing, node.Name)
		}
	}
	if len(missing) > 0 {
		sort.Strings(missing)
		return fmt.Errorf("oidccutover: %s is missing on %s — it is written by the authenticator's "+
			"kubeconfig-writer init container, so this means infra-core has not finished on those nodes. "+
			"Wait and re-run; do NOT create the file by hand",
			WebhookKubeconfigPath, strings.Join(missing, ", "))
	}
	fmt.Fprintf(o.Out, "[oidc-cutover] %s present on all %d control-plane node(s)\n",
		WebhookKubeconfigPath, len(o.Nodes))
	return nil
}

// sshTarget renders a host for error messages.
func sshTarget(h ports.SSHHost) string {
	target := h.Hostname
	if target == "" {
		target = h.Alias
	}
	if h.User != "" {
		return h.User + "@" + target
	}
	return target
}

// foreignPortOwner returns the name of a non-apiserver process listening on
// :6443, or "" when only the apiserver (or nothing) holds it.
func foreignPortOwner(ctx context.Context, o Options, host ports.SSHHost) (string, error) {
	out, err := o.SSH.Run(ctx, host, sudo(`ss -tlpnH 'sport = :6443' 2>/dev/null || true`))
	if err != nil {
		return "", err
	}
	// One ss line can list SEVERAL processes on the same socket, e.g.
	//   users:(("kube-apiserver",pid=1,fd=3),("envoy",pid=2,fd=9))
	// Reading only the first meant a line that happened to name the apiserver
	// first hid the very conflict this check exists to find — the answer would
	// depend on ss's ordering.
	for _, line := range strings.Split(string(out), "\n") {
		if strings.TrimSpace(line) == "" {
			continue
		}
		rest := line
		for {
			at := strings.Index(rest, `("`)
			if at < 0 {
				break
			}
			rest = rest[at+2:]
			end := strings.Index(rest, `"`)
			if end < 0 {
				break
			}
			name := rest[:end]
			rest = rest[end:]
			if name != "" && name != "kube-apiserver" {
				return name, nil
			}
		}
	}
	return "", nil
}

// waitAPIServerReady polls /readyz FROM THE NODE ITSELF on 127.0.0.1.
//
// Not the node IP: the Envoy Gateway Service publishes a :6443 SNI-passthrough
// listener and pins externalIPs, so kube-proxy captures <node-ip>:6443 on every
// node. A request without matching SNI fails there, which is indistinguishable
// from a dead apiserver — probing the node IP would report failure on a
// perfectly healthy cluster and send the operator into a rollback they do not
// need.
func waitAPIServerReady(ctx context.Context, o Options, node Node) error {
	deadline := time.Now().Add(o.ReadyTimeout)
	var last string
	for time.Now().Before(deadline) {
		select {
		case <-ctx.Done():
			return ctx.Err()
		case <-time.After(o.PollInterval):
		}
		// AUTHENTICATED probe via the node-local admin kubeconfig — not an
		// anonymous curl. Kubernetes 1.35 answers unauthenticated /readyz with
		// 401 (anonymous access to the health endpoints is no longer a given),
		// so the curl form can NEVER see 200 there: on the first 1.35 install
		// (mod, 2026-08-09) the cutover wired the node, the webhook was live in
		// the running kube-apiserver, authenticated /readyz said ok — and this
		// gate still reported failure and told the operator to roll back a
		// healthy change. Same failure class as the busybox-TLS CNI probe: a
		// probe whose own limitations are indistinguishable from the outage it
		// checks for. sudo because rke2.yaml is root-owned; the cutover already
		// requires passwordless sudo for the config write and the restart.
		out, err := o.SSH.Run(ctx, node.Host,
			`sudo -n sh -c 'KUBECONFIG=/etc/rancher/rke2/rke2.yaml /var/lib/rancher/rke2/bin/kubectl get --raw=/readyz' 2>&1 || true`)
		if err != nil {
			last = err.Error()
			continue // SSH itself may blip while the node restarts
		}
		last = strings.TrimSpace(string(out))
		if last == "ok" {
			return nil
		}
	}
	return fmt.Errorf("apiserver did not report /readyz=ok on 127.0.0.1 within %s (last: %q) — "+
		"NOTE: the config change and restart already happened; verify with an authenticated "+
		"readyz before rolling back (a probe failure is not proof the apiserver is unhealthy)",
		o.ReadyTimeout, last)
}

// verifyFlagLive confirms the flag reached the RUNNING process, not just the
// file. A typo in config.yaml is accepted by RKE2 and dropped silently, leaving
// a node that looks done and still 401s.
func verifyFlagLive(ctx context.Context, o Options, host ports.SSHHost) error {
	out, err := o.SSH.Run(ctx, host,
		`ps -eo args= | grep '[k]ube-apiserver' | tr ' ' '\n' | grep authentication-token-webhook || true`)
	if err != nil {
		return fmt.Errorf("cannot inspect the running apiserver: %w", err)
	}
	// The exact flag with its exact path: a substring match on the flag NAME
	// would pass for a webhook pointed at a file that does not exist, which
	// makes the apiserver reject every token — the failure mode this command is
	// supposed to prevent.
	if !strings.Contains(string(out), webhookFlag) {
		return fmt.Errorf("the apiserver came back WITHOUT the webhook flag — %s was written but the "+
			"process does not carry it. Check RKE2 accepted the file (`journalctl -u rke2-server`); "+
			"this node still returns 401 for every Keycloak token", RKE2ConfigPath)
	}
	return nil
}

// rollback restores each node's snapshot and restarts, newest node first.
func rollback(ctx context.Context, o Options) error {
	for _, node := range o.Nodes {
		label := node.Name
		if label == "" {
			label = node.Host.Hostname
		}
		fmt.Fprintf(o.Out, "\n[oidc-cutover] ROLLBACK %s\n", label)
		if o.DryRun {
			fmt.Fprintf(o.Out, "  DRY RUN: would restore %s%s and restart rke2-server\n",
				RKE2ConfigPath, BackupSuffix)
			continue
		}
		if _, err := o.SSH.Run(ctx, node.Host, sudo(fmt.Sprintf(
			"test -s %s%s", RKE2ConfigPath, BackupSuffix))); err != nil {
			return fmt.Errorf("oidccutover: %s: no snapshot at %s%s to roll back to",
				label, RKE2ConfigPath, BackupSuffix)
		}
		if _, err := o.SSH.Run(ctx, node.Host, sudo(fmt.Sprintf(
			"cp %s%s %s", RKE2ConfigPath, BackupSuffix, RKE2ConfigPath))); err != nil {
			return fmt.Errorf("oidccutover: %s: restore: %w", label, err)
		}
		if _, err := o.SSH.Run(ctx, node.Host, sudo("systemctl restart rke2-server")); err != nil {
			return fmt.Errorf("oidccutover: %s: restart: %w", label, err)
		}
		if err := waitAPIServerReady(ctx, o, node); err != nil {
			return fmt.Errorf("oidccutover: %s: %w", label, err)
		}
		fmt.Fprintf(o.Out, "  restored and apiserver healthy\n")
	}
	return nil
}

// countAddedFlagLines is dry-run reporting only.
func countAddedFlagLines(before, after string) int {
	return len(strings.Split(after, "\n")) - len(strings.Split(before, "\n"))
}

// sudo elevates only when the SSH user is not already root, matching the ssh
// adapter's Put behaviour so a non-root operator account works unchanged.
func sudo(cmd string) string {
	return fmt.Sprintf(`if [ "$(id -u)" -ne 0 ] && command -v sudo >/dev/null 2>&1; `+
		`then sudo -n sh -c %s; else sh -c %s; fi`, shellQuote(cmd), shellQuote(cmd))
}

func shellQuote(s string) string {
	return "'" + strings.ReplaceAll(s, "'", `'"'"'`) + "'"
}
