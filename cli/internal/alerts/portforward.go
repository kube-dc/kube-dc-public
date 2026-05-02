package alerts

import (
	"bufio"
	"context"
	"fmt"
	"io"
	"net"
	"os"
	"os/exec"
	"strings"
	"time"
)

// PortForward manages a `kubectl port-forward` subprocess.
//
// It's intentionally thin: relies on an installed kubectl + the user's
// active kubeconfig. Auth, TLS, and tenant-id will later be replaced by
// a direct Mimir client; until then, the kubeconfig-based forward is the
// simplest secure path to in-cluster Alertmanager.
type PortForward struct {
	Namespace string
	Service   string
	RemotePort int

	cmd        *exec.Cmd
	LocalPort  int
	cancel     context.CancelFunc
}

// NewAlertmanagerPortForward returns a PortForward targeting the standard
// Alertmanager service shipped with the kube-prometheus-stack.
func NewAlertmanagerPortForward() *PortForward {
	return &PortForward{
		Namespace:  "monitoring",
		Service:    "svc/prom-operator-alertmanager",
		RemotePort: 9093,
	}
}

// Start spawns kubectl port-forward and waits until the local port is ready.
// It picks a free local port automatically. The caller MUST call Stop().
func (p *PortForward) Start(ctx context.Context) error {
	if _, err := exec.LookPath("kubectl"); err != nil {
		return fmt.Errorf("kubectl not found in PATH: %w", err)
	}

	port, err := freePort()
	if err != nil {
		return fmt.Errorf("allocate local port: %w", err)
	}
	p.LocalPort = port

	// The subprocess must outlive the caller's startup-timeout context.
	// Use a long-lived background-derived context so the process runs until
	// Stop() is called explicitly.
	lifecycleCtx, cancel := context.WithCancel(context.Background())
	p.cancel = cancel

	args := []string{
		"port-forward",
		"-n", p.Namespace,
		p.Service,
		fmt.Sprintf("%d:%d", p.LocalPort, p.RemotePort),
	}
	p.cmd = exec.CommandContext(lifecycleCtx, "kubectl", args...)
	p.cmd.Env = os.Environ()

	stdout, err := p.cmd.StdoutPipe()
	if err != nil {
		cancel()
		return err
	}
	stderr, err := p.cmd.StderrPipe()
	if err != nil {
		cancel()
		return err
	}

	if err := p.cmd.Start(); err != nil {
		cancel()
		return fmt.Errorf("start kubectl: %w", err)
	}

	// Wait until kubectl reports "Forwarding from" on stdout, then we know
	// the socket is bound. Give it a 10s ceiling.
	ready := make(chan struct{}, 1)
	failed := make(chan error, 1)
	go func() {
		scanner := bufio.NewScanner(stdout)
		for scanner.Scan() {
			if strings.Contains(scanner.Text(), "Forwarding from") {
				select {
				case ready <- struct{}{}:
				default:
				}
				break
			}
		}
		_, _ = io.Copy(io.Discard, stdout) // drain
	}()
	go func() {
		// Collect stderr so we can surface a meaningful error on failure.
		var sb strings.Builder
		scanner := bufio.NewScanner(stderr)
		for scanner.Scan() {
			sb.WriteString(scanner.Text())
			sb.WriteString("\n")
		}
		if sb.Len() > 0 {
			select {
			case failed <- fmt.Errorf("kubectl port-forward: %s", strings.TrimSpace(sb.String())):
			default:
			}
		}
	}()

	select {
	case <-ready:
		return nil
	case err := <-failed:
		_ = p.Stop()
		return err
	case <-time.After(10 * time.Second):
		_ = p.Stop()
		return fmt.Errorf("port-forward timed out")
	case <-ctx.Done():
		_ = p.Stop()
		return ctx.Err()
	}
}

// Stop tears down the subprocess.
func (p *PortForward) Stop() error {
	if p.cancel != nil {
		p.cancel()
	}
	if p.cmd != nil && p.cmd.Process != nil {
		_ = p.cmd.Process.Kill()
		_, _ = p.cmd.Process.Wait()
	}
	return nil
}

// URL returns the HTTP URL the local forward is reachable on.
func (p *PortForward) URL() string {
	return fmt.Sprintf("http://127.0.0.1:%d", p.LocalPort)
}

func freePort() (int, error) {
	l, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		return 0, err
	}
	defer l.Close()
	return l.Addr().(*net.TCPAddr).Port, nil
}
