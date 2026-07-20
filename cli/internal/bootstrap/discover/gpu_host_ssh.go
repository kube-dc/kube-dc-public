package discover

import (
	"context"
	"fmt"
	"io/fs"
	"strings"
	"time"

	"github.com/shalb/kube-dc/cli/internal/bootstrap/ports"
)

// Discovery currently reuses the generic SSH port and therefore opens several
// bounded sessions while walking sysfs. Two minutes covers high-latency
// bastions without making an unreachable host an unbounded installer stall.
const gpuSSHDiscoveryTimeout = 2 * time.Minute

// DiscoverGPUHostSSH collects the same read-only sysfs/procfs inventory as
// DiscoverGPUHost, but from a remote node through the installer's existing SSH
// port. It does not run lspci, modprobe, bind a driver, or otherwise mutate the
// host. The context bounds the complete probe; a defensive timeout is applied
// when the caller did not provide one.
func DiscoverGPUHostSSH(ctx context.Context, client ports.SSHClient, host ports.SSHHost) (GPUHostInventory, error) {
	if client == nil {
		return GPUHostInventory{}, fmt.Errorf("discover GPU host over SSH: nil client")
	}
	if _, ok := ctx.Deadline(); !ok {
		var cancel context.CancelFunc
		ctx, cancel = context.WithTimeout(ctx, gpuSSHDiscoveryTimeout)
		defer cancel()
	}
	inv, err := DiscoverGPUHost(sshHostFS{ctx: ctx, client: client, host: host})
	if err != nil {
		return GPUHostInventory{}, fmt.Errorf("discover GPU host over SSH: %w", err)
	}
	// DiscoverGPUHost intentionally treats some sysfs files as optional for
	// local probes. A transport deadline is not optional: returning the partial
	// inventory would misclassify timed-out vendor/driver reads as unsupported
	// hardware. Fail the remote qualification explicitly instead.
	if err := ctx.Err(); err != nil {
		return GPUHostInventory{}, fmt.Errorf("discover GPU host over SSH: probe incomplete: %w", err)
	}
	return inv, nil
}

type sshHostFS struct {
	ctx    context.Context
	client ports.SSHClient
	host   ports.SSHHost
}

func (s sshHostFS) ReadFile(path string) ([]byte, error) {
	body, err := s.client.Fetch(s.ctx, s.host, path)
	if err != nil {
		return nil, fmt.Errorf("fetch %s: %w", path, err)
	}
	return body, nil
}

func (s sshHostFS) ReadDir(path string) ([]fs.DirEntry, error) {
	// Paths originate from fixed Linux paths plus kernel-owned PCI/group names.
	// Quote again at the shell boundary so even a compromised response cannot
	// turn a subsequent directory read into command execution.
	command := "find -- " + shellQuote(path) + " -mindepth 1 -maxdepth 1 -printf '%f\\n'"
	body, err := s.client.Run(s.ctx, s.host, command)
	if err != nil {
		return nil, fmt.Errorf("list %s: %w", path, err)
	}
	lines := strings.Split(strings.TrimSpace(string(body)), "\n")
	entries := make([]fs.DirEntry, 0, len(lines))
	for _, line := range lines {
		name := strings.TrimSpace(line)
		if name == "" || name == "." || name == ".." || strings.ContainsAny(name, "/\x00") {
			continue
		}
		entries = append(entries, remoteDirEntry(name))
	}
	return entries, nil
}

func shellQuote(value string) string {
	return "'" + strings.ReplaceAll(value, "'", "'\"'\"'") + "'"
}

type remoteDirEntry string

func (e remoteDirEntry) Name() string               { return string(e) }
func (e remoteDirEntry) IsDir() bool                { return false }
func (e remoteDirEntry) Type() fs.FileMode          { return 0 }
func (e remoteDirEntry) Info() (fs.FileInfo, error) { return nil, fs.ErrInvalid }
