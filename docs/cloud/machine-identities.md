# Machine identities: Terraform, MCP and CI

For an AI app used by a person (Claude, ChatGPT, Cursor, VS Code…), use the
[hosted MCP endpoint](ai-ide-integration.md#hosted-mcp-endpoint-no-install)
instead: the app signs you in, no secret is stored.

A **machine identity** lets a tool sign in without a person: Terraform, an AI
assistant's MCP server, a CI pipeline. It has a client ID and secret. With them
it gets short-lived tokens, which the Kubernetes API accepts as a user of your
Organization in the groups you choose. The same permissions that apply to
people apply to it.

Only Organization administrators create and manage machine identities and
read their credentials.

## Create one

In the console: **Organization → Access → Machine identities → Create
identity**. Choose a name, its groups and optionally the token lifetime.

| Group | What the identity may do |
|---|---|
| `org-admin` | Everything an Organization administrator may do: create Projects, manage every Project |
| an Organization group | The project roles that group grants, in the projects it lists |

Prefer an Organization group with only the projects and roles the tool needs.

With the CLI:

```bash
kube-dc machine-identities create ci --group org-admin --description "Terraform pipeline"
kube-dc machine-identities kubeconfig ci --out ci.kubeconfig
```

The kubeconfig holds the client secret: keep it like a password (the CLI writes
it with mode 0600). Its user runs `kube-dc machine-identities token`, which
fetches a fresh token whenever the previous one expires, so long runs keep
working.

The identity's Kubernetes user is `<organization>:<name>@machine.kube-dc.invalid`
and appears under that name in audit logs.

## Terraform

Use the standard `hashicorp/kubernetes` provider with the kubeconfig:

```hcl
provider "kubernetes" {
  config_path = "ci.kubeconfig"
}

resource "kubernetes_manifest" "project" {
  manifest = {
    apiVersion = "kube-dc.com/v1"
    kind       = "Project"
    metadata   = { name = "web", namespace = "my-org" }
    spec       = { egressNetworkType = "cloud" }
  }
  computed_fields = ["spec.cidrBlock", "metadata.annotations", "metadata.labels"]
  wait { fields = { "status.ready" = "true" } }
}
```

Without a kubeconfig file, give the provider the API server and an `exec`
block that runs `kube-dc machine-identities token` with `KUBE_DC_TOKEN_URL`,
`KUBE_DC_CLIENT_ID` and `KUBE_DC_CLIENT_SECRET` in its environment (the values
are shown in the console under **Show credentials**).

Virtual machines, Floating IPs and the other Kube-DC resources are declared the
same way. A Floating IP with `externalNetworkType: cloud` and no `eip` gets an
address from the cloud pool.

## MCP servers

Point the Kubernetes MCP server at the kubeconfig (see
[AI IDE integration](ai-ide-integration.md)):

```bash
claude mcp add kubernetes --env KUBECONFIG=$PWD/ci.kubeconfig -- npx mcp-server-kubernetes
```

The assistant then works with the identity's permissions, and nothing else.

## Usage

An administrator identity can read the Organization's usage through the
Kubernetes API:

```bash
kubectl --kubeconfig ci.kubeconfig get organizationusages.platform.kube-dc.com my-org -n my-org -o yaml
```

For a time window, request
`/apis/platform.kube-dc.com/v1alpha1/namespaces/my-org/organizationusages/my-org?from=…&to=…&granularity=hour`.

## Rotate, disable, delete

| Action | Effect |
|---|---|
| **Rotate secret** (`kube-dc machine-identities rotate ci`) | A new secret is issued. The old one stops working at once: regenerate the kubeconfig everywhere it is used |
| **Disable sign-in** | New tokens are refused; issued ones expire within their lifetime |
| **Delete** | The identity and its client are removed |

If an identity shows **Failed**, one of its groups is not a group of your
Organization; the details name it.
