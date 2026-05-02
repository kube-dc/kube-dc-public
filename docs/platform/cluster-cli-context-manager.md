# Context Manager

The **Contexts** tab of the integrated bootstrap TUI is a kubectx-aware view that lists every kubeconfig context, tags each with its identity, shows you who you actually are on the selected cluster, and lets you switch / delete contexts safely.

```bash
# Open the integrated TUI directly on the Contexts tab
kube-dc bootstrap context

# Or open on Fleet first and press ] to cycle to Contexts
kube-dc bootstrap
```

Press `]` / `[` to cycle to other tabs (e.g. **Fleet**), or `1` / `2` to jump directly. Top-tab keys are deliberately distinct from `Tab` / `Shift+Tab`, which mean pane focus *inside* the Contexts view.

## Identity badges

| Badge | What it means |
|---|---|
| `ADMIN` (purple) | `kube-dc login --admin` context — master realm, `cluster-admin` |
| `TENANT` (blue) | `kube-dc login --org X` context — per-org realm, namespace-scoped |
| `BREAK-GLASS` (red) | static-token kubeconfig pointing at a kube-api server (decrypted break-glass) |
| `EXTERNAL` (grey) | every other context — `kubectx`-managed, vendor exec plugins, manual entries |

The classifier matches by exec-plugin shape and context name pattern, never by surface name alone — a context called `kube-dc-admin` that points at an unrelated apiserver won't be tagged ADMIN.

## Keys

| Key | Action |
|---|---|
| `↑`/`k`, `↓`/`j` | Move selection |
| `↵` | Activate (set `current-context`) |
| `L` | **Re-login for the selected context's cluster** — admin context → `kube-dc login --admin`; tenant context → `kube-dc login --org <realm>`. Runs as a subprocess (browser opens for OIDC), then the kubeconfig is re-read so updates show inline. |
| `l` | Tenant login (only meaningful on a TENANT row; uses the row's realm). |
| `t` | **Test auth right now** — issues a single GET `/readyz` against the cluster API using the operator's currently-cached token. Result lands in the right pane: `200 OK` (auth works), `401` (token expired — re-login), `403` (RBAC). |
| `d` | Delete just the selected context (cluster + user GC'd only if no other context references them; non-kube-dc contexts can be deleted too). |
| `r` | Re-read kubeconfig |
| `q` | Quit |

## Right pane

The right pane shows:

- Cluster, server, user, namespace, realm.
- Auth method (exec plugin or static token).
- For ADMIN/TENANT: the cached JWT's email + group claims + token expiry. Read this first when something's not working — usually the answer is "oh, the token expired hours ago".

:::tip Safe-delete
Pressing `d` removes only the selected context plus any cluster/user it solely references. Other kube-dc contexts on the same cluster stay put. The screen never modifies `EXTERNAL` contexts beyond setting `current-context` — your `kubectx`-managed entries, AWS-EKS exec plugins, and manual contexts are safe by design.
:::
