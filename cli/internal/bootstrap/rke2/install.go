// Package rke2 drives the host-side RKE2 install for
// `kube-dc bootstrap install` — the first step of a one-command
// install. It pushes the canonical RKE2 server config + installer to a
// control-plane node over SSH and runs it, leaving a `cni: none`
// cluster that `bootstrap fetch-kubeconfig` + `bootstrap init` then
// finish (kube-ovn + the platform arrive via Flux).
//
// The config is produced by the embedded install-server.sh (the same
// script the fleet ships, byte-for-byte minus two incident comments
// that named real hosts — see the drift guard test). Embedding it
// keeps the CLI self-contained (no fleet checkout needed for the RKE2
// step) and reuses the battle-tested memory-tier / tls-san / DNS logic
// rather than re-deriving it in Go. The CLI's only job is to resolve
// the right env from the operator's preset + the node's facts and hand
// it to the script — so the RKE2 CIDRs always match the fleet the
// operator will later `init` with (the class of mismatch behind E2E
// findings 12/13).
package rke2

import (
	"context"
	"fmt"
	"io"
	"net"
	"sort"
	"strconv"
	"strings"
	"time"

	_ "embed"

	"github.com/shalb/kube-dc/cli/internal/bootstrap/ports"
)

//go:embed install-server.sh
var installServerScript []byte

// remoteScriptPath is where the installer is staged on the node.
const remoteScriptPath = "/tmp/kube-dc-rke2-install-server.sh"

// defaultRKE2Version pins the RKE2 line the installer requests when the
// operator doesn't override it. Kept in sync with the install guide +
// the fleet script default.
const defaultRKE2Version = "v1.35.0+rke2r1"

// InstallOptions parameterizes a single control-plane node install.
type InstallOptions struct {
	SSH  ports.SSHClient
	Host ports.SSHHost

	// NodeName is the RKE2 node-name (also the kube-ovn master label
	// target). Required — must match what the operator uses elsewhere
	// (e.g. --rook-osd-node, KUBE_OVN_MASTER_NODES).
	NodeName string
	// Domain is the cluster public FQDN; drives tls-san
	// (<domain> + kube-api.<domain>). Required.
	Domain string

	// PodCIDR / ServiceCIDR / ClusterDNS come from the operator's
	// preset (clusterinit.EnvMapFor) so RKE2 and the fleet agree.
	// Required.
	PodCIDR     string
	ServiceCIDR string
	ClusterDNS  string

	// NodeIP is the node's primary internal IP (also the apiserver
	// advertise-address — never a NAT/floating public IP, per E2E
	// finding 13). Empty → detected over SSH.
	NodeIP string
	// ExternalIP is RKE2's node-external-ip. Empty → defaults to
	// NodeIP, which is the safe choice on every topology (advertise-
	// address pins the apiserver to the internal IP regardless).
	ExternalIP string

	// RKE2Version overrides defaultRKE2Version.
	RKE2Version string

	// JoinToken + JoinServer turn a first-server install into an
	// ADDITIONAL control-plane (server) join for HA/etcd-quorum.
	// JoinServer is the EXISTING control-plane's INTERNAL IP (the :9345
	// supervisor endpoint — never a NAT/floating public IP, same rule as
	// advertise-address). JoinToken is the cluster node-token (SECRET —
	// never printed; redacted from all output and errors). Both empty →
	// first-server install. install-server.sh branches on these as its
	// two positional args (<token> <server-ip>); an additional server
	// still needs Domain + the same CIDRs as the first server (it writes
	// its own tls-san / advertise-address), which is why this rides on
	// InstallOptions rather than the (config-inheriting) worker path.
	JoinToken  string
	JoinServer string
	// CPPort overrides the control-plane supervisor port the joining
	// server dials (join mode only; 0 → 9345). Passed to the installer
	// via CP_PORT env.
	CPPort int

	// Force re-runs the installer even when rke2-server is already
	// active on the node.
	Force bool
	// DryRun resolves + prints the plan (incl. read-only SSH probes)
	// but writes nothing and installs nothing.
	DryRun bool

	Out io.Writer
}

// Errors.
var (
	ErrMissingDependency = fmt.Errorf("rke2 install: missing dependency")
	ErrInvalidOption     = fmt.Errorf("rke2 install: invalid option")
	ErrInstallFailed     = fmt.Errorf("rke2 install: installer script failed")
	ErrAlreadyRunning    = fmt.Errorf("rke2 install: rke2-server already active (use --force to re-run)")
)

// buildInstallEnv assembles the env the install-server.sh script reads.
// Pure (no I/O) so it's unit-testable; callers resolve NodeIP first.
func buildInstallEnv(o InstallOptions) map[string]string {
	ext := o.ExternalIP
	if ext == "" {
		ext = o.NodeIP
	}
	ver := o.RKE2Version
	if ver == "" {
		ver = defaultRKE2Version
	}
	env := map[string]string{
		"RKE2_VERSION": ver,
		"NODE_NAME":    o.NodeName,
		"NODE_IP":      o.NodeIP,
		"EXTERNAL_IP":  ext,
		"DOMAIN":       o.Domain,
		"POD_CIDR":     o.PodCIDR,
		"SERVICE_CIDR": o.ServiceCIDR,
		"CLUSTER_DNS":  o.ClusterDNS,
	}
	// A non-default supervisor port only applies to a control-plane join;
	// otherwise let the script's own 9345 default stand (mirrors the
	// worker path, which sets CP_PORT only when non-default).
	if o.isJoin() && o.CPPort != 0 && o.CPPort != defaultSupervisorPort {
		env["CP_PORT"] = strconv.Itoa(o.CPPort)
	}
	return env
}

// validate checks the caller-supplied (pre-detection) options. Beyond
// presence it SEMANTICALLY validates every value that gets interpolated
// into /etc/rancher/rke2/config.yaml — a bad CIDR / IP or a
// whitespace/newline in domain/name would otherwise produce a broken
// RKE2 config (and with --force, restart a live server onto it). This is
// the airtight backstop for any caller; the cobra layer additionally
// runs the richer clusterinit field validators for nicer UX.
func (o InstallOptions) validate() error {
	switch {
	case o.SSH == nil:
		return fmt.Errorf("%w: SSH client", ErrMissingDependency)
	case o.NodeName == "":
		return fmt.Errorf("%w: NodeName", ErrMissingDependency)
	case o.Domain == "":
		return fmt.Errorf("%w: Domain", ErrMissingDependency)
	case o.PodCIDR == "" || o.ServiceCIDR == "" || o.ClusterDNS == "":
		return fmt.Errorf("%w: PodCIDR/ServiceCIDR/ClusterDNS (from the preset)", ErrMissingDependency)
	}
	if _, _, err := net.ParseCIDR(o.PodCIDR); err != nil {
		return fmt.Errorf("%w: PodCIDR %q is not a CIDR", ErrInvalidOption, o.PodCIDR)
	}
	if _, _, err := net.ParseCIDR(o.ServiceCIDR); err != nil {
		return fmt.Errorf("%w: ServiceCIDR %q is not a CIDR", ErrInvalidOption, o.ServiceCIDR)
	}
	if net.ParseIP(o.ClusterDNS) == nil {
		return fmt.Errorf("%w: ClusterDNS %q is not an IP", ErrInvalidOption, o.ClusterDNS)
	}
	if o.NodeIP != "" && net.ParseIP(o.NodeIP) == nil {
		return fmt.Errorf("%w: NodeIP %q is not an IP", ErrInvalidOption, o.NodeIP)
	}
	if o.ExternalIP != "" && net.ParseIP(o.ExternalIP) == nil {
		return fmt.Errorf("%w: ExternalIP %q is not an IP", ErrInvalidOption, o.ExternalIP)
	}
	// Server-join consistency: both or neither, and JoinServer (the
	// existing CP's supervisor IP) must be an IP — it goes verbatim into
	// the join config's `server:` URL and tls-san.
	if (o.JoinToken == "") != (o.JoinServer == "") {
		return fmt.Errorf("%w: a control-plane join needs BOTH JoinToken and JoinServer", ErrInvalidOption)
	}
	if o.JoinServer != "" && net.ParseIP(o.JoinServer) == nil {
		return fmt.Errorf("%w: JoinServer %q is not an IP", ErrInvalidOption, o.JoinServer)
	}
	if o.CPPort != 0 && (o.CPPort < 1 || o.CPPort > 65535) {
		return fmt.Errorf("%w: CPPort %d out of range (1-65535)", ErrInvalidOption, o.CPPort)
	}
	// Domain + NodeName go verbatim into YAML (tls-san / node-name) —
	// reject anything that could break the document or inject a line.
	for _, f := range []struct{ name, val string }{{"Domain", o.Domain}, {"NodeName", o.NodeName}} {
		if strings.ContainsAny(f.val, " \t\r\n\"'") {
			return fmt.Errorf("%w: %s %q contains whitespace or quotes", ErrInvalidOption, f.name, f.val)
		}
	}
	return nil
}

// isJoin reports whether these options describe an additional
// control-plane (server) join rather than a first-server install.
func (o InstallOptions) isJoin() bool { return o.JoinToken != "" && o.JoinServer != "" }

// Install runs the full host-side flow: resolve node IP, idempotency
// check, (dry-run) plan, push the script, run it, verify the node.
// With JoinToken+JoinServer set it joins the node as an ADDITIONAL
// control-plane instead (same embedded install-server.sh, driven with
// its two join positional args).
func Install(ctx context.Context, o InstallOptions) error {
	out := o.Out
	if out == nil {
		out = io.Discard
	}
	if err := o.validate(); err != nil {
		return err
	}

	// Resolve the node's primary internal IP if the operator didn't
	// pin it — this becomes both node-ip and advertise-address.
	if o.NodeIP == "" {
		ip, err := DetectNodeIP(ctx, o.SSH, o.Host)
		if err != nil {
			return fmt.Errorf("rke2 install: resolve node IP (pass --node-ip to skip): %w", err)
		}
		o.NodeIP = ip
		fmt.Fprintf(out, "[install] detected node IP %s (apiserver advertise-address)\n", ip)
	}

	env := buildInstallEnv(o)

	// Plan (always printed).
	renderPlan(out, o, env)

	if o.DryRun {
		fmt.Fprintln(out, "[install] --dry-run: no changes made.")
		return nil
	}

	// Idempotency: skip when rke2-server is already active unless --force.
	wasActive, err := rke2Active(ctx, o.SSH, o.Host)
	if err != nil {
		return fmt.Errorf("rke2 install: probe rke2-server state: %w", err)
	}
	if wasActive && !o.Force {
		fmt.Fprintf(out, "[install] rke2-server already active on %s — nothing to do (use --force to re-run).\n", o.NodeName)
		return nil
	}

	// Push the installer.
	fmt.Fprintf(out, "[install] pushing RKE2 installer to %s\n", remoteScriptPath)
	if err := o.SSH.Put(ctx, o.Host, remoteScriptPath, installServerScript, 0o755); err != nil {
		return fmt.Errorf("rke2 install: push installer: %w", err)
	}

	// Run it. Buffered (the SSH port returns combined output on
	// completion); warn the operator it takes a few minutes. For a
	// control-plane join the token rides as a positional arg — build the
	// command here but NEVER print it, and run both the remote output and
	// any error through redactToken (the SSH adapter embeds the full
	// command, token included, in errors). redactToken is a no-op for a
	// first-server install (empty token).
	if o.isJoin() {
		fmt.Fprintln(out, "[install] running RKE2 installer (writes join config, installs + joins as an additional control-plane — ~2-4 min)...")
	} else {
		fmt.Fprintln(out, "[install] running RKE2 installer (writes config, installs + starts rke2-server — ~2-4 min)...")
	}
	cmd := remoteInstallCmd(env, remoteScriptPath)
	if o.isJoin() {
		cmd += " " + shellQuote(o.JoinToken) + " " + shellQuote(o.JoinServer)
	}
	res, err := o.SSH.Run(ctx, o.Host, cmd)
	if len(res) > 0 {
		fmt.Fprintln(out, indent(redactToken(string(res), o.JoinToken), "    | "))
	}
	if err != nil {
		return fmt.Errorf("%w: %s", ErrInstallFailed, redactToken(err.Error(), o.JoinToken))
	}

	// --force over an ALREADY-RUNNING node: the installer's
	// `systemctl start` is a no-op on a live service, so a rewritten
	// config (e.g. a bumped max-pods) sits on disk unapplied. Restart
	// to apply it. Skipped on a fresh install (the service's first
	// start already loaded the new config) so we never blip a
	// just-installed apiserver needlessly.
	if wasActive {
		fmt.Fprintln(out, "[install] --force on a running node: restarting rke2-server to apply the rewritten config...")
		if _, rerr := o.SSH.Run(ctx, o.Host, "sudo -n systemctl restart rke2-server"); rerr != nil {
			return fmt.Errorf("rke2 install: restart rke2-server to apply config: %w", rerr)
		}
	}

	if o.isJoin() {
		fmt.Fprintf(out, "[install] additional control-plane up on %s — it joins the existing cluster's etcd quorum (NotReady until kube-ovn schedules onto it)\n", o.NodeName)
		fmt.Fprintln(out, "[install] verify: kubectl get nodes            (expect the new node, control-plane role)")
		fmt.Fprintln(out, "[install]         kubectl -n kube-system get pods -l component=etcd -o wide   (a new etcd member)")
		fmt.Fprintln(out, "[install] reminder: run the OIDC-webhook per-node cutover on this node too (see the installer output banner)")
		return nil
	}
	fmt.Fprintf(out, "[install] RKE2 server up on %s (node will be NotReady until a CNI is installed — that's expected)\n", o.NodeName)
	fmt.Fprintf(out, "[install] next: kube-dc bootstrap fetch-kubeconfig %s --ssh-host %s --domain %s\n",
		o.NodeName, sshHostArg(o.Host), o.Domain)
	fmt.Fprintf(out, "[install]       then: kube-dc bootstrap init --name <cluster> --domain %s --ssh-host %s ... --yes\n",
		o.Domain, sshHostArg(o.Host))
	return nil
}

// detectNodeIPAttempts / detectNodeIPDelay bound the just-booted-node
// retry below. On a VM seconds after SSH comes up BOTH the default route
// AND `hostname -I` can transiently return nothing (netplan/DHCP still
// settling), so a single probe pass can spuriously fail. Vars (not
// consts) so tests can zero the delay.
var (
	detectNodeIPAttempts = 3
	detectNodeIPDelay    = 2 * time.Second
)

// DetectNodeIP returns the node's primary internal IPv4. It prefers the
// route-source IP (what the kernel uses to reach the internet) and falls
// back to `hostname -I`'s first address — the same default the installer
// script uses. Each pass tries both; the whole pass is retried a few
// times (see detectNodeIP*) to ride out a just-booted node whose
// networking hasn't settled. Exported so the cobra layer can surface it
// in error text. Honors ctx cancellation between retries.
func DetectNodeIP(ctx context.Context, ssh ports.SSHClient, host ports.SSHHost) (string, error) {
	var lastErr error
	for attempt := 0; attempt < detectNodeIPAttempts; attempt++ {
		if attempt > 0 {
			select {
			case <-ctx.Done():
				return "", ctx.Err()
			case <-time.After(detectNodeIPDelay):
			}
		}
		ip, err := detectNodeIPOnce(ctx, ssh, host)
		if err == nil {
			return ip, nil
		}
		lastErr = err
	}
	return "", lastErr
}

// detectNodeIPOnce runs a single route-then-hostname-I probe pass.
func detectNodeIPOnce(ctx context.Context, ssh ports.SSHClient, host ports.SSHHost) (string, error) {
	if out, err := ssh.Run(ctx, host, "ip -4 route get 1.1.1.1"); err == nil {
		if ip, perr := parseRouteSrc(out); perr == nil {
			return ip, nil
		}
	}
	// Fallback: first global IPv4 from `hostname -I`.
	out, err := ssh.Run(ctx, host, "hostname -I")
	if err != nil {
		return "", fmt.Errorf("route lookup empty and `hostname -I` failed: %w", err)
	}
	if ip := firstIPv4(out); ip != "" {
		return ip, nil
	}
	return "", fmt.Errorf("could not determine node IP (route lookup empty; `hostname -I` = %q)", strings.TrimSpace(string(out)))
}

// firstIPv4 returns the first IPv4 token in `hostname -I` output.
func firstIPv4(out []byte) string {
	for _, f := range strings.Fields(string(out)) {
		if ip := net.ParseIP(f); ip != nil && ip.To4() != nil {
			return f
		}
	}
	return ""
}

// parseRouteSrc extracts the `src <ip>` token from `ip route get`.
func parseRouteSrc(out []byte) (string, error) {
	f := strings.Fields(string(out))
	for i, tok := range f {
		if tok == "src" && i+1 < len(f) {
			if net.ParseIP(f[i+1]) == nil {
				return "", fmt.Errorf("route lookup returned invalid src %q", f[i+1])
			}
			return f[i+1], nil
		}
	}
	return "", fmt.Errorf("no `src` in route output %q", strings.TrimSpace(string(out)))
}

// rke2Active reports whether rke2-server.service is active on the node.
// (`is-active` exits non-zero when inactive; the `|| true` keeps a clean
// "not active" rather than an error — see serviceActive in join.go.)
func rke2Active(ctx context.Context, ssh ports.SSHClient, host ports.SSHHost) (bool, error) {
	return serviceActive(ctx, ssh, host, "rke2-server"), nil
}

// remoteInstallCmd builds the `sudo -n env K=V ... bash <script>` line.
// Deterministic key order so the command (and tests) are stable.
// `sudo -n` fails fast instead of hanging on a password prompt (the SSH
// session has no TTY) — passwordless sudo or a root login is required.
func remoteInstallCmd(env map[string]string, scriptPath string) string {
	keys := make([]string, 0, len(env))
	for k := range env {
		keys = append(keys, k)
	}
	sort.Strings(keys)
	var b strings.Builder
	b.WriteString("sudo -n env")
	for _, k := range keys {
		fmt.Fprintf(&b, " %s=%s", k, shellQuote(env[k]))
	}
	b.WriteString(" bash ")
	b.WriteString(scriptPath)
	return b.String()
}

// shellQuote single-quotes a value for safe shell interpolation.
func shellQuote(s string) string {
	return "'" + strings.ReplaceAll(s, "'", `'\''`) + "'"
}

// sshHostArg reconstructs a user@host / alias string for the printed
// "next step" hints.
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

// renderPlan prints the resolved install plan (dry-run and real runs).
func renderPlan(out io.Writer, o InstallOptions, env map[string]string) {
	if o.isJoin() {
		port := o.CPPort
		if port == 0 {
			port = defaultSupervisorPort
		}
		fmt.Fprintf(out, "== RKE2 control-plane JOIN plan — node %q (%s) ==\n", o.NodeName, sshHostArg(o.Host))
		fmt.Fprintf(out, "  mode:              additional control-plane (server), joins %s:%d\n", o.JoinServer, port)
		fmt.Fprintln(out, "  join token:        (from control-plane, redacted)")
	} else {
		fmt.Fprintf(out, "== RKE2 install plan — node %q (%s) ==\n", o.NodeName, sshHostArg(o.Host))
	}
	fmt.Fprintf(out, "  RKE2 version:      %s\n", env["RKE2_VERSION"])
	fmt.Fprintf(out, "  node-ip / advertise-address: %s\n", env["NODE_IP"])
	fmt.Fprintf(out, "  node-external-ip:  %s\n", env["EXTERNAL_IP"])
	fmt.Fprintf(out, "  cluster-cidr:      %s\n", env["POD_CIDR"])
	fmt.Fprintf(out, "  service-cidr:      %s\n", env["SERVICE_CIDR"])
	fmt.Fprintf(out, "  cluster-dns:       %s\n", env["CLUSTER_DNS"])
	fmt.Fprintf(out, "  tls-san:           %s, kube-api.%s, %s, %s\n", o.Domain, o.Domain, env["EXTERNAL_IP"], env["NODE_IP"])
	fmt.Fprintln(out, "  cni:               none (kube-ovn installed later by Flux)")
	fmt.Fprintln(out, "  kubelet reserved + max-pods: auto-tiered from the node's memory")
	fmt.Fprintln(out, "  labels:            kube-dc-manager=true, kube-ovn/role=master")
}
