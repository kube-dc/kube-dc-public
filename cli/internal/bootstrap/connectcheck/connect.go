// Package connectcheck powers `kube-dc bootstrap connect` — a Layer-0
// reachability pre-flight run BEFORE `install`. It answers "can the CLI
// actually drive this node?" from wherever the operator sits (including
// through a bastion via ProxyJump), checking the things `install` needs:
// SSH reach + auth, passwordless sudo, and the internal IP that would
// become the apiserver advertise-address.
package connectcheck

import (
	"context"
	"fmt"
	"io"
	"strings"

	"github.com/shalb/kube-dc/cli/internal/bootstrap/ports"
)

// Options parameterize a connectivity check.
type Options struct {
	SSH  ports.SSHClient
	Host ports.SSHHost
	Out  io.Writer
}

// Errors.
var (
	ErrMissingDependency  = fmt.Errorf("connect: missing dependency")
	ErrUnreachable        = fmt.Errorf("connect: node not reachable over SSH")
	ErrNoPasswordlessSudo = fmt.Errorf("connect: passwordless sudo unavailable (install runs 'sudo -n')")
)

// probeScript gathers everything in ONE session: login user, OS, whether
// passwordless sudo works, and the route-source internal IP. Every field
// is emitted on its own KEY=VALUE line; the script itself always exits 0
// (the sudo test is guarded) so a non-nil Run error means the CONNECT
// (dial/handshake/auth), not the command, failed.
const probeScript = `echo "USER=$(id -un 2>/dev/null)"; ` +
	`echo "OS=$(. /etc/os-release 2>/dev/null && printf '%s' "$PRETTY_NAME" || uname -sr)"; ` +
	`if sudo -n true 2>/dev/null; then echo "SUDO=yes"; else echo "SUDO=no"; fi; ` +
	`echo "IP=$(ip -4 route get 1.1.1.1 2>/dev/null | sed -n 's/.*src \([0-9.]*\).*/\1/p' | head -1)"`

// Check runs the pre-flight and prints a report. Returns ErrUnreachable
// if SSH connect/auth fails, or ErrNoPasswordlessSudo if the node lacks
// passwordless sudo — both are hard `install` blockers, so a non-zero
// exit lets CI gate on them. The report is printed either way.
func Check(ctx context.Context, o Options) error {
	out := o.Out
	if out == nil {
		out = io.Discard
	}
	if o.SSH == nil {
		return fmt.Errorf("%w: SSH client", ErrMissingDependency)
	}

	fmt.Fprintf(out, "== connect pre-flight — %s ==\n", sshHostArg(o.Host))
	if via := o.Host.ProxyJump; via != "" {
		fmt.Fprintf(out, "  via jump:          %s\n", via)
	}

	res, err := o.SSH.Run(ctx, o.Host, probeScript)
	if err != nil {
		fmt.Fprintf(out, "  ✗ SSH reach+auth:  FAILED\n")
		fmt.Fprintln(out, indent(err.Error(), "      "))
		return fmt.Errorf("%w: %s", ErrUnreachable, sshHostArg(o.Host))
	}
	fields := parseFields(res)
	fmt.Fprintf(out, "  ✓ SSH reach+auth:  ok (user %s)\n", orNA(fields["USER"]))
	fmt.Fprintf(out, "  •  OS:             %s\n", orNA(fields["OS"]))

	nodeIP := fields["IP"]
	if nodeIP != "" {
		fmt.Fprintf(out, "  ✓ internal IP:     %s (would be the apiserver advertise-address)\n", nodeIP)
	} else {
		fmt.Fprintf(out, "  ⚠ internal IP:     could not detect (install will retry / pass --node-ip)\n")
	}

	sudoOK := fields["SUDO"] == "yes"
	if sudoOK {
		fmt.Fprintf(out, "  ✓ passwordless sudo: ok\n")
	} else {
		fmt.Fprintf(out, "  ✗ passwordless sudo: MISSING — install runs 'sudo -n' and will fail\n")
	}

	if !sudoOK {
		return ErrNoPasswordlessSudo
	}
	fmt.Fprintf(out, "[connect] ready — 'kube-dc bootstrap install' can drive %s\n", sshHostArg(o.Host))
	return nil
}

// parseFields turns the KEY=VALUE lines into a map.
func parseFields(b []byte) map[string]string {
	m := map[string]string{}
	for _, line := range strings.Split(string(b), "\n") {
		line = strings.TrimSpace(line)
		if i := strings.Index(line, "="); i > 0 {
			key := strings.TrimSpace(line[:i])
			if key != "" {
				m[key] = strings.TrimSpace(line[i+1:])
			}
		}
	}
	return m
}

func orNA(s string) string {
	if s == "" {
		return "(unknown)"
	}
	return s
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
