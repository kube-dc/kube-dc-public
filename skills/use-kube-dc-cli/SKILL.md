---
name: use-kube-dc-cli
description: Use the kube-dc CLI for authentication, context switching, and namespace management. Covers login, use, ns, config, and kubectl integration.
---

# Use Kube-DC CLI

The `kube-dc` CLI provides browser-based OAuth authentication and context management for Kube-DC clusters. It integrates as a kubectl exec credential plugin.

## Installation

```bash
# Linux (amd64)
sudo curl -sL https://github.com/kube-dc/kube-dc-public/releases/latest/download/kube-dc_linux_amd64 -o /usr/local/bin/kube-dc && sudo chmod +x /usr/local/bin/kube-dc

# Linux (arm64)
sudo curl -sL https://github.com/kube-dc/kube-dc-public/releases/latest/download/kube-dc_linux_arm64 -o /usr/local/bin/kube-dc && sudo chmod +x /usr/local/bin/kube-dc

# macOS (Apple Silicon)
sudo curl -sL https://github.com/kube-dc/kube-dc-public/releases/latest/download/kube-dc_darwin_arm64 -o /usr/local/bin/kube-dc && sudo chmod +x /usr/local/bin/kube-dc

# macOS (Intel)
sudo curl -sL https://github.com/kube-dc/kube-dc-public/releases/latest/download/kube-dc_darwin_amd64 -o /usr/local/bin/kube-dc && sudo chmod +x /usr/local/bin/kube-dc
```

## Commands

### Login

```bash
kube-dc login --domain kube-dc.cloud --org {org-name}
kube-dc login --domain stage.kube-dc.com --org {org-name}
kube-dc login --domain internal.example.com --org {org-name} --ca-cert /path/to/ca.crt
```

Opens browser for Keycloak OAuth login. Tokens cached for ~30 days with auto-refresh.

### Switch Context (org/project)

```bash
kube-dc use {org}/{project}    # Switch to specific project
kube-dc use                    # Interactive selection
```

Context names follow pattern: `kube-dc/{domain}/{org}/{project}`

### List / Switch Namespace

```bash
kube-dc ns                     # List available namespaces (from JWT claims)
kube-dc ns {namespace}         # Switch to specific namespace
```

### Show Configuration

```bash
kube-dc config show            # Current context, server, cached credentials
kube-dc config get-contexts    # List all kube-dc contexts
```

### Logout

```bash
kube-dc logout                 # Remove credentials for current server
kube-dc logout --all           # Remove all cached credentials
```

## Determine Current Context

Before creating resources, always check the current context:

```bash
# Quick check
kubectl config current-context

# Detailed info (server, namespace, token status)
kube-dc config show

# Available namespaces for current user
kube-dc ns
```

The namespace tells you the project: namespace `{org}-{project}` → org=`{org}`, project=`{project}`.

## How kubectl Integration Works

After `kube-dc login`, the CLI configures kubectl with an exec credential plugin:

```yaml
users:
- name: kube-dc@{org}
  user:
    exec:
      apiVersion: client.authentication.k8s.io/v1
      command: kube-dc
      args: ["credential", "--server", "https://kube-api.{domain}:6443"]
```

Every kubectl command automatically:
1. Returns cached token if valid
2. Refreshes token using refresh_token if expired
3. Prompts re-login only when refresh_token expires (~30 days)

## kubectx / kubens Compatibility

```bash
kubectx                            # Lists all contexts including kube-dc ones
kubectx kube-dc/shalb/demo        # Switch to Kube-DC context
```

## File Locations

| Path | Purpose |
|------|---------|
| `~/.kube/config` | Kubeconfig (Kube-DC entries merged, never overwrites others) |
| `~/.kube-dc/config.yaml` | CLI configuration |
| `~/.kube-dc/credentials/` | Cached OAuth tokens (0600 permissions) |

## Safety
- The CLI never stores passwords — only OAuth tokens with auto-refresh
- Tokens are cached with 0600 permissions (owner-read-only)
- Never log or display token contents in chat output
- If token is expired, suggest `kube-dc login` rather than trying to extract tokens manually
