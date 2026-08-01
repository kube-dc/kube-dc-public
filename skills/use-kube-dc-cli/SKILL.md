---
name: use-kube-dc-cli
description: Install and use the kube-dc CLI for Organization login, Project context switching, kubeconfig integration, and logout.
---

# Use the Kube-DC CLI

`kube-dc` uses browser-based OAuth with PKCE, caches tokens locally, and writes
one kubeconfig context for every accessible Project. The context keeps the
user-facing Project and its backing namespace aligned.

## Install

Prefer the command shown by **Get CLI Access** in the Kube-DC console. For a
GitHub release, select the correct asset and verify the published checksum:

```bash
asset=kube-dc_linux_amd64
tmpdir="$(mktemp -d)"

curl --fail --location "https://github.com/kube-dc/kube-dc-public/releases/latest/download/$asset" --output "$tmpdir/$asset"
curl --fail --location "https://github.com/kube-dc/kube-dc-public/releases/latest/download/checksums.txt" --output "$tmpdir/checksums.txt"

if command -v sha256sum >/dev/null; then
  (cd "$tmpdir" && grep " $asset$" checksums.txt | sha256sum -c -)
else
  (cd "$tmpdir" && grep " $asset$" checksums.txt | shasum -a 256 -c -)
fi

sudo install -m 0755 "$tmpdir/$asset" /usr/local/bin/kube-dc
rm -rf "$tmpdir"
kube-dc version
```

Replace the example asset with `kube-dc_linux_arm64`,
`kube-dc_darwin_amd64`, or `kube-dc_darwin_arm64` as appropriate. A Windows
amd64 `.exe` asset is also published.

## Log in to an Organization

```bash
kube-dc login --domain {platform-domain} --org {organization}
```

`--org` takes the Organization name, which is also the identity realm. Do not
pass a Project name or `{organization}-{project}` backing namespace.

The CLI derives:

- Kubernetes API: `https://kube-api.{platform-domain}:6443`
- identity endpoint: `https://login.{platform-domain}`

It opens a browser, caches the OAuth session under `~/.kube-dc/credentials/`,
and creates contexts named:

```text
kube-dc/{platform-domain}/{organization}/{project}
```

Use `--ca-cert /path/to/ca.crt` for a private CA. `--insecure` disables TLS
verification and should be limited to controlled diagnostics. The exposed
`--device-code` flag is not implemented in the current CLI.

## Select a Project

```bash
# List Kube-DC contexts
kube-dc use

# Select one Project
kube-dc use {platform-domain}/{organization}/{project}

# Confirm context and backing namespace
kubectl config current-context
kubectl config view --minify -o jsonpath='{.contexts[0].context.namespace}{"\n"}'
```

`kube-dc use` is the normal Project switcher. `kube-dc ns` is a compatibility
command that rewrites only the namespace field of the current context; using it
can leave the context name and selected Project out of sync.

## Use kubectl

The kubeconfig user invokes `kube-dc credential` as an exec credential plugin.
It returns a valid cached access token or refreshes it when possible:

```bash
kubectl auth whoami
kubectl get pods
kube-dc config show
```

Do not assume a fixed session lifetime. The identity provider controls token
expiry. When it reports no finite refresh expiry, the CLI records a 30-day
local refresh window.

The CLI honors the active kubeconfig path. If `KUBECONFIG` points somewhere
other than `~/.kube/config`, login asks before writing contexts there.

## Log out

```bash
# Remove credentials for the active Kube-DC server
kube-dc logout

# Also remove its generated kubeconfig contexts
kube-dc logout --remove-contexts

# Remove all cached server credentials
kube-dc logout --all --remove-contexts
```

Without `--remove-contexts`, logout leaves the contexts in kubeconfig, but
kubectl cannot authenticate through them.

## Files and local security

| Path | Purpose |
|---|---|
| Active kubeconfig path | Clusters, users, and Project contexts |
| `~/.kube-dc/credentials/` | Cached OAuth tokens, protected with owner-only permissions |
| `~/.kube-dc/config.yaml` | CLI configuration |

The credential cache is not encrypted at rest. Protect the local account and
disk, never print tokens into logs, and use logout when a workstation or
session is no longer trusted.

## Troubleshooting

- **Unknown realm / 404**: pass the Organization name to `--org`.
- **No Project contexts**: confirm Organization membership and sign in again
  after group changes.
- **Context missing from kubectx**: compare `KUBECONFIG` with the file kubectx
  reads.
- **Forbidden**: authentication succeeded; check the selected Project role.
- **Refresh token expired**: run `kube-dc login` again.
