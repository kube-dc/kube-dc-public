package rke2

import (
	"context"
	"fmt"
	"io"
	"net"
	"strconv"
	"strings"

	_ "embed"

	"github.com/shalb/kube-dc/cli/internal/bootstrap/ports"
)

//go:embed install-agent.sh
var installAgentScript []byte

const remoteAgentScriptPath = "/tmp/kube-dc-rke2-install-agent.sh"

// defaultSupervisorPort is the RKE2 control-plane supervisor port an
// agent joins on.
const defaultSupervisorPort = 9345

// nodeTokenPath is where a control-plane node keeps the join token.
const nodeTokenPath = "/var/lib/rancher/rke2/server/node-token"

// JoinWorkerOptions parameterizes joining a NEW worker (rke2-agent) to an
// existing cluster. The join token + the control-plane's INTERNAL IP are
// read from an existing control-plane node over SSH (so the agent dials
// the internal supervisor IP, never a NAT/floating public IP), then the
// agent is installed on the worker.
type JoinWorkerOptions struct {
	SSH ports.SSHClient

	// Worker is the NEW node being joined.
	Worker     ports.SSHHost
	WorkerName string
	// WorkerIP is the worker's internal IP (node-ip). Empty → detected.
	WorkerIP string

	// ControlPlane is an existing control-plane node — its node-token and
	// internal IP are read from here unless JoinToken/CPHost are supplied.
	ControlPlane ports.SSHHost
	// JoinToken (node-token) — empty → fetched from ControlPlane. SECRET:
	// never printed.
	JoinToken string
	// CPHost is the control-plane INTERNAL IP the agent dials. Empty →
	// detected from ControlPlane.
	CPHost string
	// CPPort defaults to 9345.
	CPPort int

	RKE2Version string
	// DisableEmbeddedRegistry opts this worker out of participation in
	// the RKE2 embedded spegel mirror. Zero value keeps the default on.
	DisableEmbeddedRegistry bool
	Force                   bool
	DryRun                  bool
	Out                     io.Writer
}

// JoinWorker installs an rke2-agent on the worker and joins it to the
// cluster. Idempotent (skips when rke2-agent is already active unless
// --force).
func JoinWorker(ctx context.Context, o JoinWorkerOptions) error {
	out := o.Out
	if out == nil {
		out = io.Discard
	}
	if o.SSH == nil {
		return fmt.Errorf("%w: SSH client", ErrMissingDependency)
	}
	if o.WorkerName == "" {
		return fmt.Errorf("%w: WorkerName", ErrMissingDependency)
	}
	if err := validateNodeName(o.WorkerName); err != nil {
		return err
	}
	// Need either the control-plane to read from, or both derived values.
	haveCP := o.ControlPlane.Alias != "" || o.ControlPlane.Hostname != ""
	if !haveCP && (o.JoinToken == "" || o.CPHost == "") {
		return fmt.Errorf("%w: control-plane (to read the join token + internal IP) or both --join-token and --cp-host", ErrMissingDependency)
	}
	if o.CPPort == 0 {
		o.CPPort = defaultSupervisorPort
	}
	if o.CPPort < 1 || o.CPPort > 65535 {
		return fmt.Errorf("%w: --cp-port %d out of range (1-65535)", ErrInvalidOption, o.CPPort)
	}

	// Resolve the control-plane endpoint IP + join token (over SSH from
	// the control-plane unless the operator supplied them). Shared with
	// the control-plane-join path.
	tok, cpIP, err := ResolveJoinCredentials(ctx, o.SSH, o.ControlPlane, o.JoinToken, o.CPHost, out)
	if err != nil {
		return err
	}
	o.JoinToken, o.CPHost = tok, cpIP

	// Resolve the worker's internal IP.
	if o.WorkerIP == "" {
		ip, err := DetectNodeIP(ctx, o.SSH, o.Worker)
		if err != nil {
			return fmt.Errorf("rke2 join: resolve worker internal IP (pass --node-ip): %w", err)
		}
		o.WorkerIP = ip
		fmt.Fprintf(out, "[join] detected worker IP %s\n", o.WorkerIP)
	}
	if net.ParseIP(o.WorkerIP) == nil {
		return fmt.Errorf("%w: worker IP %q is not an IP", ErrInvalidOption, o.WorkerIP)
	}

	renderJoinPlan(out, o)
	if o.DryRun {
		fmt.Fprintln(out, "[join] --dry-run: no changes made.")
		return nil
	}

	// Idempotency: skip when rke2-agent already active unless --force.
	wasActive := serviceActive(ctx, o.SSH, o.Worker, "rke2-agent")
	if wasActive && !o.Force {
		fmt.Fprintf(out, "[join] rke2-agent already active on %s — nothing to do (use --force to re-run).\n", o.WorkerName)
		return nil
	}
	if wasActive {
		if err := ensureNoRunningVMs(ctx, o.SSH, o.Worker); err != nil {
			return fmt.Errorf("rke2 join: pre-restart VM safety gate: %w", err)
		}
	}

	fmt.Fprintf(out, "[join] pushing RKE2 agent installer to %s\n", remoteAgentScriptPath)
	if err := o.SSH.Put(ctx, o.Worker, remoteAgentScriptPath, installAgentScript, 0o755); err != nil {
		return fmt.Errorf("rke2 join: push agent installer: %w", err)
	}

	fmt.Fprintln(out, "[join] installing rke2-agent + joining the cluster (~1-3 min)...")
	// install-agent.sh <token> <cp-host> <node-ip>; NODE_NAME / CP_PORT /
	// RKE2_VERSION via env. Token is a positional arg (matches the fleet
	// script) — the command string is built here but NEVER printed, and
	// both the remote output and any error are run through redactToken
	// (the SSH adapter embeds the full command, token included, in errors).
	env := map[string]string{"NODE_NAME": o.WorkerName}
	if o.CPPort != defaultSupervisorPort {
		env["CP_PORT"] = strconv.Itoa(o.CPPort)
	}
	if o.RKE2Version != "" {
		env["RKE2_VERSION"] = o.RKE2Version
	}
	if o.DisableEmbeddedRegistry {
		env["EMBEDDED_REGISTRY"] = "false"
	}
	cmd := remoteAgentCmd(env, remoteAgentScriptPath, o.JoinToken, o.CPHost, o.WorkerIP)
	res, err := o.SSH.Run(ctx, o.Worker, cmd)
	if len(res) > 0 {
		fmt.Fprintln(out, indent(redactToken(string(res), o.JoinToken), "    | "))
	}
	if err != nil {
		return fmt.Errorf("%w: %s", ErrInstallFailed, redactToken(err.Error(), o.JoinToken))
	}
	if wasActive {
		fmt.Fprintln(out, "[join] --force on a running agent: restarting rke2-agent to apply the rewritten config...")
		if _, rerr := o.SSH.Run(ctx, o.Worker, "sudo -n systemctl restart rke2-agent"); rerr != nil {
			return fmt.Errorf("rke2 join: restart rke2-agent to apply config: %w", rerr)
		}
	}

	fmt.Fprintf(out, "[join] rke2-agent up on %s — it will register with the control plane (NotReady until kube-ovn schedules onto it)\n", o.WorkerName)
	fmt.Fprintf(out, "[join] verify: kubectl get nodes -w   (expect %s to appear, then go Ready)\n", o.WorkerName)
	return nil
}

// remoteAgentCmd builds the `sudo -n env K=V ... bash <script> <token>
// <cp> <node-ip>` line. Positional args are shell-quoted; the token is
// among them — callers must NOT print this string (see redactJoinPlan).
func remoteAgentCmd(env map[string]string, scriptPath, token, cpHost, nodeIP string) string {
	base := remoteInstallCmd(env, scriptPath) // "sudo -n env ... bash <script>"
	return base + " " + shellQuote(token) + " " + shellQuote(cpHost) + " " + shellQuote(nodeIP)
}

// ResolveJoinCredentials reads the cluster node-token + the existing
// control-plane's INTERNAL IP (the :9345 supervisor endpoint) from a
// control-plane node over SSH, unless the caller already supplied them
// (token / cpHost). Shared by the worker join (rke2-agent) and the
// control-plane join (additional server) so the token-fetch + CP-IP
// detection — and their redaction discipline — live in one place. The
// returned token is SECRET: callers must never surface it unredacted
// (see redactToken). Progress lines go to out (never the token).
func ResolveJoinCredentials(ctx context.Context, ssh ports.SSHClient, cp ports.SSHHost, token, cpHost string, out io.Writer) (string, string, error) {
	if out == nil {
		out = io.Discard
	}
	// Resolve the control-plane internal IP (the supervisor endpoint).
	if cpHost == "" {
		ip, err := DetectNodeIP(ctx, ssh, cp)
		if err != nil {
			return "", "", fmt.Errorf("rke2 join: resolve control-plane internal IP (pass --cp-host): %w", err)
		}
		cpHost = ip
		fmt.Fprintf(out, "[join] control-plane internal IP: %s\n", cpHost)
	}
	if net.ParseIP(cpHost) == nil {
		return "", "", fmt.Errorf("%w: control-plane host %q is not an IP", ErrInvalidOption, cpHost)
	}
	// Fetch the join token if not supplied. SECRET — held locally, used
	// as a positional arg, never printed.
	if token == "" {
		raw, err := ssh.Fetch(ctx, cp, nodeTokenPath)
		if err != nil {
			return "", "", fmt.Errorf("rke2 join: read node-token from control-plane %s (pass --join-token): %w", nodeTokenPath, err)
		}
		token = strings.TrimSpace(string(raw))
		if token == "" {
			return "", "", fmt.Errorf("rke2 join: control-plane node-token at %s is empty", nodeTokenPath)
		}
		fmt.Fprintln(out, "[join] fetched join token from control-plane (redacted)")
	}
	return token, cpHost, nil
}

// redactToken strips the join token out of any string bound for the user
// (error text or remote output). The real SSH adapter embeds the full
// command — which carries the token as a positional arg — in returned
// errors, so every failure/output path MUST pass through here before it
// surfaces. No-op when token is empty (dry-run never reaches Run).
func redactToken(s, token string) string {
	if token == "" {
		return s
	}
	return strings.ReplaceAll(s, token, "<redacted>")
}

// serviceActive reports whether <svc>.service is active on the node.
func serviceActive(ctx context.Context, ssh ports.SSHClient, host ports.SSHHost, svc string) bool {
	out, _ := ssh.Run(ctx, host, "systemctl is-active "+svc+".service 2>/dev/null || true")
	return strings.TrimSpace(string(out)) == "active"
}

// validateNodeName rejects whitespace/quotes that would break the YAML
// node-name line (the airtight guard for any caller; the cobra layer
// additionally runs clusterinit.ValidateK8sNodeNameField).
func validateNodeName(name string) error {
	if strings.ContainsAny(name, " \t\r\n\"'") {
		return fmt.Errorf("%w: NodeName %q contains whitespace or quotes", ErrInvalidOption, name)
	}
	return nil
}

func renderJoinPlan(out io.Writer, o JoinWorkerOptions) {
	ver := o.RKE2Version
	if ver == "" {
		ver = "(cluster default / " + defaultRKE2Version + ")"
	}
	fmt.Fprintf(out, "== RKE2 worker-join plan — node %q (%s) ==\n", o.WorkerName, sshHostArg(o.Worker))
	fmt.Fprintf(out, "  role:              worker (rke2-agent)\n")
	fmt.Fprintf(out, "  node-ip:           %s\n", o.WorkerIP)
	fmt.Fprintf(out, "  join server:       %s:%d\n", o.CPHost, o.CPPort)
	fmt.Fprintf(out, "  join token:        (from control-plane, redacted)\n")
	fmt.Fprintf(out, "  RKE2 version:      %s\n", ver)
	if o.DisableEmbeddedRegistry {
		fmt.Fprintln(out, "  embedded registry: disabled (explicit opt-out)")
	} else {
		fmt.Fprintln(out, "  embedded registry: enabled (spegel, wildcard mirror)")
	}
	fmt.Fprintln(out, "  kubelet reserved + max-pods: auto-tiered from the worker's memory")
}
