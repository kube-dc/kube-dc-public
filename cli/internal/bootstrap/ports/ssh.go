package ports

import "context"

// SSHClient is the contract for remote shell + file-fetch operations
// against control-plane nodes. Used by M4-T06 (auto-pull kubeconfig
// from `/etc/rancher/rke2/rke2.yaml` on a fresh RKE2 master) and v2
// add-node (V4 will drive `install-agent.sh` over SSH).
//
// **Auth flow** (per B-004 resolution):
//   1. ssh-agent first (via `$SSH_AUTH_SOCK`).
//   2. Fall back to `IdentityFile` from `~/.ssh/config` parsed via
//      kevinburke/ssh_config or equivalent.
//   3. NEVER accept a `--ssh-key` flag. Operators put keys in their
//      ssh config; the CLI doesn't manage them.
//
// Host config (User, Port, ProxyJump, …) is taken from `~/.ssh/config`
// when present. The adapter respects standard ssh_config semantics so
// `kube-dc bootstrap init --ssh-host ams1-blade179-8` works against
// the operator's existing host aliases.
type SSHClient interface {
	// Run executes `cmd` on the host and returns combined stdout+stderr.
	// Caller-set context timeout governs both connect and command runtime.
	Run(ctx context.Context, host SSHHost, cmd string) ([]byte, error)

	// Fetch reads `remotePath` from the host and returns its bytes.
	// Used to grab `/etc/rancher/rke2/rke2.yaml` for the M4-T06
	// kubeconfig auto-pull. The bytes are in-memory only — never
	// written to a temp file by the adapter; caller is responsible
	// for whatever they do with them next.
	//
	// Files larger than 4 MiB return ErrFileTooLarge — guards against
	// accidentally fetching a binary blob.
	Fetch(ctx context.Context, host SSHHost, remotePath string) ([]byte, error)

	// Put writes `body` to `remotePath` on the host with mode `mode`
	// (octal, e.g. 0755 for executables, 0644 for unit files). Used by
	// the anchors package (`kube-dc bootstrap anchors apply`) to install
	// the per-node systemd unit + binding script. Implementation pipes
	// `body` via session Stdin to `install -m <mode> /dev/stdin
	// <remotePath>` so the write is atomic + mode-tagged in one syscall.
	//
	// `remotePath` MUST be absolute. Parent directories must exist —
	// Put does NOT mkdir -p (operator paths are predictable:
	// /usr/local/sbin/, /etc/systemd/system/). Returns an error
	// without partial-write if the remote install fails.
	//
	// Files larger than 4 MiB return ErrFileTooLarge — same sanity
	// ceiling as Fetch.
	Put(ctx context.Context, host SSHHost, remotePath string, body []byte, mode uint32) error
}

// SSHHost identifies an SSH endpoint. When `Alias` matches a Host block
// in `~/.ssh/config`, the adapter resolves the rest from the config and
// the other fields here are overrides. When `Alias` is empty, the
// adapter requires at least Hostname (+ defaults User="root", Port=22).
type SSHHost struct {
	// Alias is a name from the operator's `~/.ssh/config` Host block
	// (e.g. "ams1-blade179-8" or "bastion"). Optional.
	Alias string

	// Hostname is the explicit hostname / IP. Required when Alias is
	// empty; overrides the alias's HostName when both are set.
	Hostname string

	// User defaults to "root" when empty.
	User string

	// Port defaults to 22 when zero.
	Port int
}
