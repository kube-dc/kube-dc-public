# Kube-DC CLI

Browser-based authentication CLI for Kube-DC clusters, following patterns from AWS CLI, GCloud, and DigitalOcean CLI.

## Features

- **Browser-based OAuth login** - No passwords in terminal
- **Automatic token refresh** - Seamless kubectl usage for ~30 days
- **kubectx/kubens compatible** - Works with popular tools
- **Preserves existing kubeconfig** - Never overwrites non-Kube-DC entries
- **Cross-platform** - Linux, macOS, Windows

## Installation

### From Release

```bash
# Linux (amd64)
sudo curl -sL https://github.com/kube-dc/kube-dc-public/releases/latest/download/kube-dc_linux_amd64 -o /usr/local/bin/kube-dc && sudo chmod +x /usr/local/bin/kube-dc

# Linux (arm64)
sudo curl -sL https://github.com/kube-dc/kube-dc-public/releases/latest/download/kube-dc_linux_arm64 -o /usr/local/bin/kube-dc && sudo chmod +x /usr/local/bin/kube-dc

# macOS (Apple Silicon)
sudo curl -sL https://github.com/kube-dc/kube-dc-public/releases/latest/download/kube-dc_darwin_arm64 -o /usr/local/bin/kube-dc && sudo chmod +x /usr/local/bin/kube-dc

# macOS (Intel)
sudo curl -sL https://github.com/kube-dc/kube-dc-public/releases/latest/download/kube-dc_darwin_amd64 -o /usr/local/bin/kube-dc && sudo chmod +x /usr/local/bin/kube-dc

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
kube-dc use shalb/demo

# Use kubectl normally
kubectl get pods

# Switch namespace
kube-dc ns shalb-dev
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

Switch between organization/project contexts.

```bash
kube-dc use shalb/demo        # Switch to specific project
kube-dc use                   # Interactive selection
```

### `kube-dc ns`

Switch or list namespaces (from JWT claims).

```bash
kube-dc ns                    # List available namespaces
kube-dc ns shalb-dev          # Switch to namespace
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
3. Prompts for re-login only when refresh_token expires (~30 days)

### kubectx Compatibility

Contexts are named `kube-dc/{org}/{project}` for clear identification:

```bash
$ kubectx
minikube
production-aws
kube-dc/shalb/demo     # Kube-DC contexts
kube-dc/shalb/dev

$ kubectx kube-dc/shalb/demo
Switched to context "kube-dc/shalb/demo".
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

1. Make changes in `shalb/kube-dc` repository
2. Commit and push to `main` branch
3. Sync workflow automatically pushes CLI to `kube-dc/kube-dc-public`
4. Create and push tag on `kube-dc-public`:

```bash
cd kube-dc-public
git pull origin main
git tag v0.2.3  # increment version
git push origin v0.2.3
```

5. GoReleaser workflow builds and publishes binaries automatically

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
