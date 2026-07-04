package mock

import (
	"context"
	"fmt"
	"sync"

	"github.com/shalb/kube-dc/cli/internal/bootstrap/ports"
)

// SSHClient returns canned fixture data. `Run` returns empty bytes
// (most tests don't care); `Fetch` looks up the requested path in the
// scenario's SSHFetch map. `Put` records writes into a per-instance
// map so anchors-package tests can assert what would have been
// installed without touching the network. The 4 MiB cap is enforced.
type SSHClient struct {
	scenario *Scenario

	mu  sync.Mutex
	put map[putKey][]byte // host+path → body, captured Put calls
	run []runRecord       // recorded Run invocations for assertions
}

type putKey struct {
	Host string
	Path string
}

type runRecord struct {
	Host string
	Cmd  string
}

func NewSSHClient(s *Scenario) *SSHClient {
	return &SSHClient{scenario: s, put: make(map[putKey][]byte)}
}

func (c *SSHClient) Run(ctx context.Context, host ports.SSHHost, cmd string) ([]byte, error) {
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	c.mu.Lock()
	c.run = append(c.run, runRecord{Host: sshIdent(host), Cmd: cmd})
	c.mu.Unlock()
	return nil, nil
}

func (c *SSHClient) Fetch(ctx context.Context, host ports.SSHHost, remotePath string) ([]byte, error) {
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	if c.scenario == nil || c.scenario.SSHFetch == nil {
		return nil, fmt.Errorf("mock: ssh Fetch: scenario has no sshFetch fixture for %s", remotePath)
	}
	body, ok := c.scenario.SSHFetch[remotePath]
	if !ok {
		return nil, fmt.Errorf("mock: ssh Fetch: scenario %s has no fixture for %s", c.scenario.Name, remotePath)
	}
	const maxBytes = 4 << 20
	if len(body) > maxBytes {
		return nil, ports.ErrFileTooLarge
	}
	return []byte(body), nil
}

func (c *SSHClient) Put(ctx context.Context, host ports.SSHHost, remotePath string, body []byte, mode uint32) error {
	if err := ctx.Err(); err != nil {
		return err
	}
	const maxBytes = 4 << 20
	if len(body) > maxBytes {
		return ports.ErrFileTooLarge
	}
	c.mu.Lock()
	defer c.mu.Unlock()
	c.put[putKey{Host: sshIdent(host), Path: remotePath}] = append([]byte(nil), body...)
	return nil
}

// PutCapture returns the body Put recorded for (host, path), or nil if
// nothing was written there. Test-only — exposed so anchors-package
// tests can assert "the right unit file got written to srv5".
func (c *SSHClient) PutCapture(host, path string) []byte {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.put[putKey{Host: host, Path: path}]
}

// RunCaptures returns every (host, cmd) Run was called with, in order.
// Test-only.
func (c *SSHClient) RunCaptures() []runRecord {
	c.mu.Lock()
	defer c.mu.Unlock()
	out := make([]runRecord, len(c.run))
	copy(out, c.run)
	return out
}

func sshIdent(h ports.SSHHost) string {
	if h.Alias != "" {
		return h.Alias
	}
	return h.Hostname
}
