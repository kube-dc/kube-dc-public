# Kube-DC CLI

Browser-based authentication CLI for Kube-DC installations, following patterns
from AWS CLI, Google Cloud CLI, and DigitalOcean CLI.

## Features

- **Browser-based OAuth login** - No passwords in terminal
- **Automatic token refresh** - Short-lived access tokens refresh while the
  cached session remains valid
- **Project context switching** - Keep the selected Project and backing
  namespace aligned
- **kubectx compatible** - Works with existing context workflows
- **Preserves existing kubeconfig** - Never overwrites non-Kube-DC entries
- **Cross-platform** - Linux, macOS, Windows

## Installation

### From Release

```bash
# Linux amd64 (use kube-dc_linux_arm64, kube-dc_darwin_arm64, or
# kube-dc_darwin_amd64 for another platform)
asset=kube-dc_linux_amd64
task_cli_tmp="$(mktemp -d)"
curl -fSL "https://github.com/kube-dc/kube-dc-public/releases/latest/download/${asset}" -o "${task_cli_tmp}/${asset}"
curl -fSL https://github.com/kube-dc/kube-dc-public/releases/latest/download/checksums.txt -o "${task_cli_tmp}/checksums.txt"
if command -v sha256sum >/dev/null; then
  ( cd "${task_cli_tmp}" && grep " ${asset}$" checksums.txt | sha256sum -c - )
else
  ( cd "${task_cli_tmp}" && grep " ${asset}$" checksums.txt | shasum -a 256 -c - )
fi
sudo install -m 0755 "${task_cli_tmp}/${asset}" /usr/local/bin/kube-dc
rm -rf "${task_cli_tmp}"
hash -r
type -a kube-dc
kube-dc version

# Windows (PowerShell)
Invoke-WebRequest -Uri https://github.com/kube-dc/kube-dc-public/releases/latest/download/kube-dc_windows_amd64.exe -OutFile kube-dc.exe
Move-Item kube-dc.exe C:\Windows\System32\
```

### From Source

```bash
cd cli
make install
```

## Quick Start

```bash
# Login to staging
kube-dc login --domain stage.kube-dc.com --org shalb

# Login to production
kube-dc login --domain kube-dc.cloud --org myorg

# Switch project context
kube-dc use stage.kube-dc.com/shalb/demo

# Use kubectl normally
kubectl get pods
```

## Commands

### `kube-dc login`

Authenticate with a Kube-DC server using browser-based OAuth.

```bash
kube-dc login --domain stage.kube-dc.com --org shalb
kube-dc login --domain kube-dc.cloud --org myorg
kube-dc login --domain internal.example.com --org myorg --ca-cert /path/to/ca.crt
```

### `kube-dc logout`

Remove cached credentials.

```bash
kube-dc logout
kube-dc logout --server https://api.kube-dc.cloud
kube-dc logout --all
```

### `kube-dc use`

Switch between Organization and Project contexts.

```bash
kube-dc use stage.kube-dc.com/shalb/demo  # Switch to a specific Project
kube-dc use                               # Interactive selection
```

### `kube-dc ns`

Compatibility selector for Project backing namespaces. It changes the namespace
on the current context without changing that context's name, so prefer
`kube-dc use` for normal Project switching.

```bash
kube-dc ns                    # List accessible backing namespaces
kube-dc ns shalb-dev          # Legacy: change only the current namespace
```

### `kube-dc config`

Manage CLI configuration.

```bash
kube-dc config show           # Show current config
kube-dc config get-contexts   # List kube-dc contexts
```

### `kube-dc credential`

Exec credential plugin for kubectl (called automatically by kubectl).

```bash
kube-dc credential --server https://api.kube-dc.cloud
```

## How It Works

### Login Flow

1. CLI starts local HTTP server for OAuth callback
2. Opens browser to Keycloak login page
3. User authenticates in browser
4. Keycloak redirects to local callback with auth code
5. CLI exchanges code for tokens (using PKCE)
6. Tokens cached to `~/.kube-dc/credentials/`
7. Kubeconfig updated with exec credential plugin

### kubectl Integration

The CLI configures kubectl to use the `kube-dc credential` command as an exec credential plugin:

```yaml
users:
- name: kube-dc@shalb
  user:
    exec:
      apiVersion: client.authentication.k8s.io/v1
      command: kube-dc
      args: ["credential", "--server", "https://api.kube-dc.cloud"]
```

Every kubectl command triggers the credential plugin, which:
1. Returns cached token if still valid
2. Automatically refreshes token using refresh_token if expired
3. Prompts for login again when the identity-provider session can no longer
   refresh

### kubectx Compatibility

Contexts are named `kube-dc/{domain}/{organization}/{project}` so installations
with the same Organization and Project names remain unambiguous:

```bash
$ kubectx
minikube
production-aws
kube-dc/stage.kube-dc.com/shalb/demo
kube-dc/stage.kube-dc.com/shalb/dev

$ kubectx kube-dc/stage.kube-dc.com/shalb/demo
Switched to context "kube-dc/stage.kube-dc.com/shalb/demo".
```

## File Locations

| Path | Purpose |
|------|---------|
| `~/.kube/config` | Kubeconfig (Kube-DC entries merged) |
| `~/.kube-dc/config.yaml` | CLI configuration |
| `~/.kube-dc/credentials/` | Cached tokens (0600 permissions) |

## Development

```bash
# Build
cd cli
go build -o kube-dc ./cmd/kube-dc

# Test
go test ./...

# Run
./kube-dc version
```

## Release Process

### Creating a New Release

The CLI source is maintained in the product repository and deliberately mirrored
to `kube-dc/kube-dc-public`. There is no automatic source-sync workflow. The CLI,
its version-matched starter, and the cloud-shell image are one release sequence:

1. Update and test `cli/` in the product repository.
2. Bump the coordinated CLI and cloud-shell inputs in
   `cicd/release/release-set.yaml`.
3. Mirror the release script's allowlisted public surface, commit both
   repositories, and make sure both clean `main` branches match their origins.
4. Run `cicd/release/release --cli` from the product repository and review the
   dry-run preflight. After release approval, run the corresponding command with
   `--publish`; it publishes the version-matched starter and pushes the public
   CLI tag.
5. Wait for GoReleaser to publish the binaries, then rebuild and pin the
   cloud-shell image against that immutable CLI release.

The release preflight refuses dirty or divergent mirror trees and existing tags.
Do not create the normal release tag by hand ahead of it.

### Release Artifacts

Binaries are published to: https://github.com/kube-dc/kube-dc-public/releases

| Platform | Binary |
|----------|--------|
| Linux (amd64) | `kube-dc_linux_amd64` |
| Linux (arm64) | `kube-dc_linux_arm64` |
| macOS (Intel) | `kube-dc_darwin_amd64` |
| macOS (Apple Silicon) | `kube-dc_darwin_arm64` |
| Windows | `kube-dc_windows_amd64.exe` |

### Version History

| Version | Changes |
|---------|---------|
| v0.2.2 | Offline access tokens (30-day sessions), fix error messages |
| v0.2.1 | Raw binary releases |
| v0.2.0 | Initial release with browser OAuth |

## See Also

- [PRD: Kube-DC CLI](../docs/prd/kube-dc-cli.md) - Detailed product requirements
- [kubelogin](https://github.com/int128/kubelogin) - Similar OIDC login tool
- [AWS CLI SSO](https://docs.aws.amazon.com/cli/latest/userguide/cli-configure-sso.html)
