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

### Pane focus & arrow scoping (BIOS-style)

The screen has two panes: **cluster list** (top) and **cluster details** (bottom). Exactly one pane has keyboard focus at a time, marked by a highlighted border.

- `Tab` cycles focus forward (list → details → list); `Shift+Tab` cycles backward.
- `↑` / `↓` (and `pgup` / `pgdown` / `g` / `G`) act on the **focused** pane only.
  - List pane focused → arrows move the cluster cursor.
  - Details pane focused → arrows move the cursor within the Kustomizations sub-list (or scroll the viewport when no Kustomization rows are present).
- `Esc` from any non-list pane jumps focus back to the cluster list.

This mirrors UEFI/BIOS setup, `htop`'s field selector, and `dialog`-based installers — fewer collisions per screen because a key like `↵` can mean different things depending on which pane is focused.

### Actionable status (`↵` on a row runs the suggested fix)

When a cluster row's status detail says `not logged in. Run: kube-dc login --domain X --admin` or `auth failed (403)`, pressing **`↵` (Enter)** on that row **runs the suggested command without leaving the TUI**. The TUI knows the cluster's domain and the canonical fleet identity is platform-admin, so there's no copy-paste step. While the dispatched login is in flight the row's status pill switches to `Running` so you see your click landed.

For Ready rows, `↵` shifts focus into the details pane so you can drill into Kustomizations directly without a `Tab`.

### Drill-down for Kustomizations (right-side panel)

When the **details pane** has focus and a Kustomization row is selected, pressing **`↵`** opens a right-side drill-down panel showing the full condition Reason and Message — useful when a Kustomization is `✗ NoReadyCondition` or `Reconciliation failed` and you need to see *why* without leaving the TUI.

- The bottom pane splits 60/40: details on the left, drill-down on the right.
- Focus moves to the drill-down panel automatically. Arrow keys scroll its content.
- The drill-down also surfaces the exact `kubectl describe` and `flux logs` commands so you can copy-paste for deeper investigation.
- `Esc` closes the drill-down and returns focus to the details pane.

### Keys

The help bar at the bottom only lists keys that are **actionable in the current state** (e.g. `↵ open` only when the cursor sits on a row that has something to open). Press `?` for the expanded list.

| Key | Action |
|---|---|
| `↑`/`k`, `↓`/`j`, `pgup`, `pgdown`, `g`, `G` | Navigate the focused pane |
| `Tab` / `Shift+Tab` | Cycle pane focus (list ↔ details ↔ drill-down) |
| `↵` | List: run row's FixAction (admin login) when present, else step into details. Details: open Kustomization drill-down. |
| `Esc` | Close drill-down / return focus to list |
| `L` | **Admin login for the selected cluster** — suspends the TUI, runs `kube-dc login --domain X --admin` (browser opens), then resumes and re-probes the row. Works on any row regardless of FixAction. |
| `l` | Tenant login (org-prompt is v1.1 — for now run `kube-dc login --org` outside the TUI). |
| `r` | Refresh (re-runs probes against every cluster) |
| `?` | Toggle full help |
| `q` | Quit |

The status pills auto-refresh every 60 s in the background — you don't need to keep hitting `r`.

:::tip First-time state
Every row will show `Unreachable` until you log in to the cluster (the probe needs a working OIDC token to call the API server). Just press `↵` on a row — the TUI auto-routes to admin login. No `Tab`, no copy-paste.
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
