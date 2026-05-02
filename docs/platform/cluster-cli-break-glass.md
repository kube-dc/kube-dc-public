# Break-Glass Recovery

**When to reach for this:** OIDC is broken — Keycloak is down, `auth-config-sync` is wedged on the apiserver, the master realm is unreachable, or `kube-dc login --admin` fails with "user is authenticated but NOT in the 'admin' group" and you need to fix the cluster *right now*.

For everyday admin work use [`kube-dc login --admin`](cluster-cli-admin-login.md) instead — it gives a per-engineer audit trail. Break-glass logs as `system:serviceaccount:kube-system:break-glass` (a shared identity).

The break-glass kubeconfig is a static `cluster-admin` token bound to `ServiceAccount/break-glass` in `kube-system`, SOPS-encrypted to `clusters/<name>/break-glass-kubeconfig.enc.yaml` in the fleet repo. It is the **deliberate exception** to "no credentials in Git" — encrypted to the same age recipients as the rest of the fleet's secrets.

---

## One-time per cluster: `adopt`

Run from a workstation that already has admin access to the cluster (your normal admin context, or a freshly-installed RKE2's `/etc/rancher/rke2/rke2.yaml`):

```bash
unset KUBECONFIG  # don't leak the result into a non-default kubeconfig
kube-dc bootstrap break-glass adopt <cluster>
```

What happens:

1. Apply `ServiceAccount/break-glass` + `ClusterRoleBinding/break-glass` (→ `cluster-admin`) + `Secret/break-glass-token` (annotation `kubernetes.io/service-account.name`, the K8s ≥ 1.24 long-lived token pattern) in `kube-system`.
2. Poll the SA-token controller until `data.token` and `data.ca.crt` are populated (≤30 s).
3. Build a kubeconfig in memory. The server URL resolves from: `--server` flag → `KUBE_API_EXTERNAL_URL` in `cluster-config.env` → kubectl current-context.
4. SOPS-encrypt to `clusters/<name>/break-glass-kubeconfig.enc.yaml` (atomic: tempfile + `os.Rename`, never a truncating-redirect like `cmd > $target`).
5. **You commit + push** — the file is not auto-pushed:

```bash
cd <fleet-repo-path>
git add clusters/<cluster>/break-glass-kubeconfig.enc.yaml
git commit -m "<cluster>: adopt break-glass kubeconfig"
git push
```

`adopt` is idempotent — re-running refreshes the encrypted file with the **current** Secret's token (not a new one). For a fresh token, use `rotate` instead.

Flags:

| Flag | Purpose |
|---|---|
| `--kube-context <ctx>` | Override current-context |
| `--server https://...` | Override the apiserver URL embedded in the kubeconfig |
| `--dry-run` | Print actions, write nothing |

---

## Recovery: `use`

```bash
unset KUBECONFIG
kube-dc bootstrap break-glass <cluster>   # bare form == break-glass use <cluster>
```

This decrypts the kubeconfig to `~/.kube-dc/break-glass/kubeconfig-*.yaml` (mode `0600`, **not** `/tmp` on shared hosts), exports `KUBECONFIG=<temp>` + `KUBE_DC_BREAK_GLASS=1` into a sub-shell of `$SHELL`, and prints a red banner with the cluster name, server URL, and the rotate command. **Do your recovery work, then `exit`.** The tempfile is removed on shell exit (signal-safe via `defer os.Remove`).

```
  BREAK-GLASS ACTIVE
  cluster: <cluster>
  server:  https://kube-api.<domain>:6443
  identity: ServiceAccount/break-glass (cluster-admin)
  audit:   apiserver logs will record system:serviceaccount:kube-system:break-glass, NOT your email
```

---

## After every session: `rotate`

```bash
unset KUBECONFIG
kube-dc bootstrap break-glass rotate <cluster>
```

Deletes the existing token Secret (forces the SA-token controller to mint a fresh token), re-applies the manifest, waits, re-encrypts. **Then commit + push** so the rest of the team picks up the new token:

```bash
cd <fleet-repo-path>
git add clusters/<cluster>/break-glass-kubeconfig.enc.yaml
git commit -m "<cluster>: rotate break-glass token after recovery"
git push
```

---

## Inspect without using: `status`

```bash
kube-dc bootstrap break-glass status <cluster>
```

Decrypts to memory (never to disk) and prints the cluster name, file path, encrypted size, last-modified mtime, and embedded server URL. Useful for "is the file still valid?" without taking a hit on the audit log.

---

## Where the file lives

The fleet's `.sops.yaml` (at the repo root) gates encryption. SOPS walks up from the current working directory looking for `.sops.yaml`; the CLI sets `cmd.Dir = <fleet root>` for every sops invocation so the config is found regardless of where you ran the command from.

As long as your age key is in the recipient list, decrypt-on-read just works — the same age key already used to read `clusters/<name>/secrets.enc.yaml`.

:::warning Audit trail
Every break-glass use is visible in the apiserver audit log as `system:serviceaccount:kube-system:break-glass` — that is **shared identity**, not per-engineer. Use it for genuine recovery only, run `rotate` after every session, and prefer `kube-dc login --admin` for routine admin work.
:::
