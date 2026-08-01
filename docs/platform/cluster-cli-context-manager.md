# Context Manager

The **Contexts** tab of the integrated bootstrap TUI lists every kubeconfig
context, tags its identity, shows the active credentials, and lets you switch or
delete entries. Deletion is immediate, including for non-Kube-DC contexts.

```bash
# Open the integrated TUI directly on the Contexts tab
kube-dc bootstrap context

# Or open on Fleet first and press ] to cycle to Contexts
kube-dc bootstrap
```

Press `]` / `[` to cycle to other tabs (e.g. **Fleet**), or `1` / `2` to jump directly. Top-tab keys are deliberately distinct from `Tab` / `Shift+Tab`, which mean pane focus *inside* the Contexts view.

## Navigate the panes

Same vocabulary as the [Fleet view](cluster-cli-fleet.md): two panes, one focused at a time, marked by a highlighted border.

- `Tab` / `Shift+Tab` toggle focus between the **context list** (top) and the **details pane** (bottom).
- `↑` / `↓` (and `pgup` / `pgdown` / `g` / `G`) act on the focused pane only.
  - List pane focused → arrows move the row cursor.
  - Details pane focused → arrows scroll the details viewport.
- `Esc` from the details pane jumps focus back to the list.

## Identity badges

| Badge | What it means |
|---|---|
| `ADMIN` (purple) | `kube-dc login --admin` context — master realm, `cluster-admin` |
| `TENANT` (blue) | Organization-authenticated Project context created by `kube-dc login --org X`; its namespace field selects the Project's backing namespace |
| `BREAK-GLASS` (red) | static-token kubeconfig pointing at a kube-api server (decrypted break-glass) |
| `EXTERNAL` (grey) | every other context — `kubectx`-managed, vendor exec plugins, manual entries |

The classifier matches by exec-plugin shape and context name pattern, never by surface name alone — a context called `kube-dc-admin` that points at an unrelated apiserver won't be tagged ADMIN.

## Keys

The help bar at the bottom only lists keys that are **actionable in the current state** (e.g. `t test auth` is hidden on `EXTERNAL` rows since there's no kube-dc token to test). Press `?` for the expanded list.

| Key | Action |
|---|---|
| `↑`/`k`, `↓`/`j`, `pgup`, `pgdown`, `g`, `G` | Navigate the focused pane |
| `Tab` / `Shift+Tab` | Toggle pane focus (list ↔ details) |
| `]` / `[` | Cycle to other top tabs (Fleet ↔ Contexts) |
| `Esc` | Return focus to the list |
| `↵` | Activate (set `current-context`) |
| `L` | **Re-login for the selected context's Kube-DC installation** — admin context → `kube-dc login --admin`; Organization context → `kube-dc login --org <realm>`. Runs as a subprocess (browser opens for OIDC), then the kubeconfig is re-read so updates show inline. |
| `l` | Organization login (only meaningful on a `TENANT` row; uses the row's Organization realm). |
| `t` | **Test auth right now** — issues a single GET `/readyz` against the cluster API using the operator's currently-cached token. Result lands in the right pane: `200 OK` (auth works), `401` (token expired — re-login), `403` (RBAC). |
| `d` | Delete the selected context immediately. This also works on `EXTERNAL` rows; there is no confirmation dialog. Cluster and user entries are removed only when no other context references them. |
| `r` | Re-read kubeconfig |
| `q` | Quit |

## Right pane

The right pane shows:

- Cluster, server, user, Project backing namespace, and Organization realm.
- Auth method (exec plugin or static token).
- For ADMIN/TENANT: the cached JWT's email + group claims + token expiry. Read this first when something's not working — usually the answer is "oh, the token expired hours ago".

:::warning Context deletion
Pressing `d` removes the selected row without a confirmation dialog, including
an `EXTERNAL` context. Shared cluster and user records remain while another
context references them. Keep a kubeconfig backup and use `r` only to reload
from disk; it does not undo a deletion.
:::
