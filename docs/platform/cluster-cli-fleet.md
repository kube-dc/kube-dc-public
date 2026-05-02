# Fleet Management

This chapter covers the two subcommands that every operator runs daily: the fleet view (`kube-dc bootstrap`) and the kubeconfig writer (`kube-dc bootstrap kubeconfig`).

## Browse the fleet (`kube-dc bootstrap`)

The no-arg form is the **fleet view** — your single dashboard for every cluster the team operates.

```bash
kube-dc bootstrap
```

What you should see:

- One row per `clusters/<name>/` directory in the fleet repo.
- A status pill per row: `Ready` (green), `Reconciling` (blue), `Drifted` (orange), `Failed` (red), `Unreachable` (grey), `Unknown` (purple).
- The selected row's details (cluster, server, IP, ext-net, per-Kustomization breakdown, image-tag drift if any) in the bottom pane.

Keys:

| Key | Action |
|---|---|
| `↑`/`k`, `↓`/`j` | Move selection |
| `L` | **Admin login for the selected cluster** — suspends the TUI, runs `kube-dc login --domain X --admin` (browser opens), then resumes and re-probes the row. The status pill should turn `Ready` when you come back. |
| `l` | Tenant login (org-prompt is v1.1 — for now run `kube-dc login --org` outside the TUI; the help line surfaces the exact command). |
| `r` | Refresh (re-runs probes against every cluster) |
| `?` | Toggle full help |
| `q` | Quit |

The status pills auto-refresh every 60 s in the background — you don't need to keep hitting `r`.

:::tip First-time state
Every row will show `Unreachable` until you log in to the cluster (the probe needs a working OIDC token to call the API server). Press `L` on a row to fix that without leaving the TUI.
:::

---

## Get a kubeconfig (`kube-dc bootstrap kubeconfig`)

Materialise a kubeconfig context for one cluster — without committing credentials anywhere.

```bash
kube-dc bootstrap kubeconfig <cluster>
```

By default the kubeconfig is wired for the **admin identity** (master realm). The context name is `kube-dc/<cluster>/admin` and the exec plugin will use `--realm master`. After the kubeconfig lands you still need one `kube-dc login --domain <cluster-domain> --admin` to mint OIDC tokens.

Examples:

```bash
# Default: admin-flavored kubeconfig
kube-dc bootstrap kubeconfig <cluster>
kube-dc login --domain <cluster-domain> --admin
kubectl get nodes        # cluster-admin

# Preview without modifying anything
kube-dc bootstrap kubeconfig <cluster> --dry-run

# Tenant-flavored (rarely needed; prefer `kube-dc login --org` directly)
kube-dc bootstrap kubeconfig <cluster> --realm <org-name>

# Also commit the synthesised kubeconfig.template.yaml back to the fleet
kube-dc bootstrap kubeconfig <cluster> --commit
```

What it does:

- Reads `clusters/<cluster>/cluster-config.env` from the fleet.
- Probes the API server's TLS handshake to fetch the cluster CA (or skips when system trust covers it).
- Writes ONE new context — `kube-dc/<cluster>/admin` (default) or `kube-dc/<cluster>/<realm>` (tenant override) — into your `~/.kube/config`, leaving every other context alone.
- The user entry's exec plugin **always pins `--realm`** in args, so kubectl never silently picks up the wrong cached identity.

Flags:

| Flag | Purpose |
|---|---|
| `--realm <name>` | Realm to wire the kubeconfig for. Empty / unset → `master` (admin). Pass an org name for tenant. |
| `--dry-run` | Print what would change; touch nothing |
| `--commit` | Also write a `kubeconfig.template.yaml` back to `clusters/<cluster>/` (does not push) |
| `--ca-cert <path>` | Bring your own CA file (skips the TLS-handshake fetch) |
| `--insecure-skip-tls-verify` | Last resort for self-signed test clusters |
| `--set-current` (default true) | Switch `current-context` to the new context |

:::tip
Tenants rarely need this command — `kube-dc login --domain X --org Y` writes its own per-namespace contexts in one step. `bootstrap kubeconfig` is mostly an operator convenience for getting kubectl wired up before / outside of an admin login.
:::
