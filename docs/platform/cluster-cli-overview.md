# Cluster Operator CLI — Overview

The `kube-dc` CLI ships two surfaces:

- **Organization-facing** — `kube-dc login` authenticates with browser-based
  OIDC and `kube-dc use` selects a named Project context. `kube-dc ns` remains
  a compatibility selector for backing namespaces. See [CLI, Console, and IDE Access](/cloud/cli-kubeconfig).
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

Flux on each cluster reconciles `clusters/<name>/` to the cluster's actual
state. Most configuration changes go through the Fleet repository, but the CLI
also performs controlled live operations for bootstrap, RKE2 installation,
node removal, endpoint anchors, authentication, and break-glass recovery.
Preview supported operations where a dry-run is available and keep durable
configuration in Git.

If you don't have a fleet repo yet, see [Installation Guide](installation-guide.md) for greenfield setup.

---

## Install the CLI

### From a release (recommended)

Pre-built binaries and `checksums.txt` are published with each
[kube-dc-public release](https://github.com/kube-dc/kube-dc-public/releases).
Select an approved immutable version, download the matching platform asset, and
verify it against that release's checksum before installing it. Do not build an
operator workflow around the mutable `latest` URL.

The [Installation Guide](installation-guide.md#phase-1--server-preparation)
contains the complete Linux and macOS checksum procedure.

### From source

```bash
git clone https://github.com/kube-dc/kube-dc-public.git
cd kube-dc-public/cli
go build -o /tmp/kdc-bin/kube-dc ./cmd/kube-dc
export PATH=/tmp/kdc-bin:$PATH
kube-dc version
```

Organization login uses the CLI binary, a browser, and network access.
Operator bootstrap workflows additionally use tools such as `kubectl`, `flux`,
`helm`, `sops`, `age`, `git`, `gh`, and `ssh`. Run
`kube-dc bootstrap doctor --no-tty` before an installation workflow.

---

## Point the CLI at your fleet repo

The `kube-dc bootstrap` commands need to know where the fleet repo lives on disk. Resolution order:

1. `--repo <path>` flag
2. `KUBE_DC_FLEET` environment variable
3. `~/.kube-dc/fleet` (default)

Most operators set the env var once and forget about it:

```bash
export KUBE_DC_FLEET=~/path/to/your/kube-dc-fleet
```

Add it to your shell rc (`.zshrc`, `.bashrc`, …) so every new terminal session picks it up.

---

## What's in the CLI

`kube-dc bootstrap` is a single integrated TUI with a top tab bar — every interactive screen is reachable as a named tab. Press `]` / `[` to cycle tabs, or `1` / `2` / … to jump directly. The cobra subcommand you run only decides which tab is active on launch:

| Subcommand | Opens on tab |
|---|---|
| `kube-dc bootstrap` | Fleet |
| `kube-dc bootstrap context` | Contexts |

Inside any tab, `Tab` and `Shift+Tab` cycle focus between the panes of that screen (cluster list ↔ details ↔ drill-down). Top-tab and pane-focus navigation are intentionally distinct keys so they never collide.

The chapters that follow cover each tab in detail, plus the non-TUI subcommands (`break-glass`, `kubeconfig`, `login`).

| Chapter | Surface | Purpose |
|---|---|---|
| [Fleet Management](cluster-cli-fleet.md) | Fleet tab + `kube-dc bootstrap kubeconfig` | Browse the fleet; materialise a kubeconfig for a named cluster |
| [Platform Admin Login](cluster-cli-admin-login.md) | `kube-dc login --admin` | OIDC against the master Keycloak realm; `cluster-admin` via `platform:admin` group |
| [Context Manager](cluster-cli-context-manager.md) | Contexts tab | kubectx-aware view of `~/.kube/config` with identity tagging |
| [Break-Glass Recovery](cluster-cli-break-glass.md) | `kube-dc bootstrap break-glass …` | SOPS-encrypted static-token kubeconfig for OIDC-down recovery |
| [Common Checks & Troubleshooting](cluster-cli-troubleshooting.md) | – | Health checks, JWT debugging, common errors |
