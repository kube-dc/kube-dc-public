# Kube-DC CLI - Kubernetes Access

This guide explains how to install and use the `kube-dc` CLI tool to access Kubernetes clusters managed by the kube-dc platform.

## Overview

The `kube-dc` CLI provides secure, browser-based authentication for Kubernetes access. It handles:

- **Browser-based login** - No passwords in terminal
- **Automatic token refresh** - 30-day session with seamless refresh
- **Multi-cluster support** - Manage multiple organizations and projects
- **Namespace switching** - Easy namespace management based on your permissions

## Installation

### macOS

```bash
# Using Homebrew
brew install kube-dc/tap/kube-dc

# Or download directly
curl -sL https://github.com/kube-dc/kube-dc-public/releases/latest/download/kube-dc_darwin_amd64 -o /usr/local/bin/kube-dc
chmod +x /usr/local/bin/kube-dc
```

### Linux

```bash
curl -sL https://github.com/kube-dc/kube-dc-public/releases/latest/download/kube-dc_linux_amd64 -o /usr/local/bin/kube-dc
chmod +x /usr/local/bin/kube-dc
```

### Windows

```powershell
# Download from releases page
Invoke-WebRequest -Uri "https://github.com/kube-dc/kube-dc-public/releases/latest/download/kube-dc_windows_amd64.exe" -OutFile "$env:USERPROFILE\bin\kube-dc.exe"
```

## Quick Start

### 1. Login to your organization

```bash
kube-dc login --domain kube-dc.cloud --org shalb
```

This opens your browser for secure authentication. After login:
- Your kubeconfig is automatically configured
- Contexts are created for each project you have access to
- Tokens are securely cached (~/.kube-dc/credentials/)

### 2. Switch namespace/project

```bash
# List available namespaces
kube-dc ns

# Switch to a project
kube-dc ns shalb-demo
```

### 3. Use kubectl normally

```bash
kubectl get pods
kubectl top pods
kubectl logs -f my-pod
```

## Commands Reference

### `kube-dc login`

Authenticate with a kube-dc platform.

```bash
kube-dc login --domain <domain> --org <organization>

# Examples
kube-dc login --domain kube-dc.cloud --org shalb
kube-dc login --domain stage.kube-dc.com --org mycompany
```

**Options:**
- `--domain` - Platform domain (e.g., kube-dc.cloud)
- `--org` - Organization/realm name
- `--insecure` - Skip TLS verification (not recommended for production)

### `kube-dc ns`

Switch between namespaces you have access to.

```bash
# List available namespaces (shows * for current)
kube-dc ns

# Switch to a namespace
kube-dc ns shalb-demo
```

### `kube-dc use`

Switch between kube-dc contexts.

```bash
# List all kube-dc contexts
kube-dc use

# Switch to a specific context
kube-dc use shalb/demo
```

### `kube-dc logout`

Remove cached credentials.

```bash
# Logout from current server
kube-dc logout

# Logout from all servers
kube-dc logout --all
```

### `kube-dc config`

View configuration and token status.

```bash
# Show current configuration
kube-dc config show

# List all kube-dc contexts
kube-dc config get-contexts
```

## How It Works

### Authentication Flow

1. **Login**: Browser opens to Keycloak login page
2. **OAuth2 PKCE**: Secure token exchange without exposing credentials
3. **Token Storage**: Encrypted tokens stored in `~/.kube-dc/credentials/`
4. **kubectl Integration**: Acts as credential plugin for kubectl

### Kubeconfig Integration

After login, your kubeconfig contains entries like:

```yaml
contexts:
- name: kube-dc/shalb/demo
  context:
    cluster: kube-dc-kube-dc.cloud-shalb
    user: kube-dc@kube-dc.cloud/shalb
    namespace: shalb-demo

users:
- name: kube-dc@kube-dc.cloud/shalb
  user:
    exec:
      apiVersion: client.authentication.k8s.io/v1
      command: kube-dc
      args:
        - credential
        - --server
        - https://kube-api.kube-dc.cloud:6443
```

### Token Lifecycle

- **Access Token**: Short-lived (5 minutes), automatically refreshed
- **Refresh Token**: 30-day validity with offline_access scope
- **Auto-refresh**: Tokens refresh transparently when kubectl runs

## Shell Completions

Enable tab completion for your shell:

```bash
# Bash
kube-dc completion bash > /etc/bash_completion.d/kube-dc

# Zsh
kube-dc completion zsh > "${fpath[1]}/_kube-dc"

# Fish
kube-dc completion fish > ~/.config/fish/completions/kube-dc.fish
```

## Troubleshooting

### Session Expired

If you see "session expired", your refresh token has expired (after 30 days of inactivity):

```bash
kube-dc login --domain <domain> --org <org>
```

### Context Not Found

If `kube-dc ns` shows "not a kube-dc context":

```bash
# Check current context
kubectl config current-context

# Switch to a kube-dc context
kube-dc use shalb/demo
```

### Clear All Credentials

To start fresh:

```bash
kube-dc logout --all
rm -rf ~/.kube-dc/credentials/
```

### Debug Mode

For troubleshooting connection issues:

```bash
kube-dc login --domain kube-dc.cloud --org shalb --debug
```

## Security Best Practices

- **Never share** your `~/.kube-dc/credentials/` directory
- Use `--insecure` only for development/testing
- Logout when finished: `kube-dc logout`
- Credentials are stored with 600 permissions

## Project Console (Web Terminal)

For quick access without CLI installation, use the **Project Console** from the UI:

1. Click your username in the top-right
2. Select "Project console"
3. A web terminal opens with kubectl pre-configured

The web console includes:
- `kubectl`, `helm`, `k9s`, `stern`, `virtctl`
- Shell completions for all tools
- Common aliases: `k`, `kgp`, `kgs`, `kl`, etc.

## Next Steps

- [User and Group Management](user-groups.md): Learn about role-based access control
- [Tutorial: Virtual Machines](tutorial-virtual-machines.md): Deploy your first VM
- [Examples](../examples/): Explore example manifests for various resources
