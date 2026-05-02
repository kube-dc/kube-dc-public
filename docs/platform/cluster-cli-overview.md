# Cluster Operator CLI — Overview

The `kube-dc` CLI ships two surfaces:

- **Tenant-facing** — `kube-dc login`, `kube-dc use`, `kube-dc ns`. Browser-based OIDC for tenants of a Kube-DC cluster. Documented in the user guide under [CLI – Console & IDE Access](../cloud/cli-kubeconfig.md).
- **Operator-facing** (this section) — `kube-dc bootstrap …`. Bubble Tea TUIs and subcommands for cluster operators: browse a fleet of clusters, log in as a platform admin, manage kubeconfig contexts safely, recover via break-glass when OIDC is broken.

This chapter set is a hands-on guide to the operator surface. Skim the headings; run the commands you need.

---

## What "fleet" means here

A **fleet repo** is a Git repository that holds the GitOps state for one or more Kube-DC clusters. The reference layout is `kube-dc-fleet`:

```
clusters/
  <cluster-1>/
    cluster-config.env          # image tags, network plumbing, hostnames
    secrets.enc.yaml            # SOPS-encrypted secrets
    break-glass-kubeconfig.enc.yaml   # SOPS-encrypted recovery kubeconfig (optional)
    ...
  <cluster-2>/
    ...
infrastructure/                 # shared kustomizations
bootstrap/                      # one-shot setup scripts (Keycloak OIDC, Flux install, …)
.sops.yaml                      # age recipients
```

Flux on each cluster reconciles `clusters/<name>/` to the cluster's actual state. The CLI never edits live clusters directly — it edits the fleet repo (via your local clone) or talks to the apiserver via OIDC. This keeps every change reviewable and reversible.

If you don't have a fleet repo yet, see [Installation Guide](installation-guide.md) for greenfield setup.

---

## Install the CLI

### From a release (recommended)

The CLI is one Go binary (~16 MB). Pre-built binaries are published on every release of [kube-dc-public](https://github.com/kube-dc/kube-dc-public/releases):

```bash
# Linux (amd64)
curl -sL https://github.com/kube-dc/kube-dc-public/releases/latest/download/kube-dc_linux_amd64 \
  -o /usr/local/bin/kube-dc
chmod +x /usr/local/bin/kube-dc

# macOS (amd64)
curl -sL https://github.com/kube-dc/kube-dc-public/releases/latest/download/kube-dc_darwin_amd64 \
  -o /usr/local/bin/kube-dc
chmod +x /usr/local/bin/kube-dc

kube-dc version
kube-dc --help
```

### From source

```bash
git clone https://github.com/kube-dc/kube-dc-public.git
cd kube-dc-public/cli
go build -o /tmp/kdc-bin/kube-dc ./cmd/kube-dc
export PATH=/tmp/kdc-bin:$PATH

kube-dc version
```

No runtime dependencies beyond what `kubectl` already needs (network, OIDC, optional `gh` for GitHub auth on `bootstrap install`).

---

## Point the CLI at your fleet repo

The `kube-dc bootstrap` commands need to know where the fleet repo lives on disk. Resolution order:

1. `--repo <path>` flag
2. `KUBE_DC_FLEET` environment variable
3. `~/.kube-dc/fleet` (default — owned by `bootstrap fleet init` once that ships)

Most operators set the env var once and forget about it:

```bash
export KUBE_DC_FLEET=~/path/to/your/kube-dc-fleet
```

Add it to your shell rc (`.zshrc`, `.bashrc`, …) so every new terminal session picks it up.

---

## What's in the CLI

The chapters that follow cover each surface in detail.

| Chapter | Command | Purpose |
|---|---|---|
| [Fleet Management](cluster-cli-fleet.md) | `kube-dc bootstrap`, `kube-dc bootstrap kubeconfig` | Browse the fleet TUI; materialise a kubeconfig for a named cluster |
| [Platform Admin Login](cluster-cli-admin-login.md) | `kube-dc login --admin` | OIDC against the master Keycloak realm; `cluster-admin` via `platform:admin` group |
| [Context Manager](cluster-cli-context-manager.md) | `kube-dc bootstrap context` | kubectx-aware TUI for `~/.kube/config` |
| [Break-Glass Recovery](cluster-cli-break-glass.md) | `kube-dc bootstrap break-glass …` | SOPS-encrypted static-token kubeconfig for OIDC-down recovery |
| [Common Checks & Troubleshooting](cluster-cli-troubleshooting.md) | – | Health checks, JWT debugging, common errors |
