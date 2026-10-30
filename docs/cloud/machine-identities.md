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

## GitHub Actions without a secret

A machine identity can let one GitHub Actions workflow sign in with the
workflow's own OIDC token instead of a client secret. Nothing is stored in
GitHub, and each token is short-lived. Set `spec.github.subject` to the
exact `sub` claim of the workflow's token:

```yaml
apiVersion: kube-dc.com/v1
kind: MachineIdentity
metadata:
  name: app-deploy
  namespace: my-org
spec:
  groups: [app-deployers]          # an Organization group with the project roles CI needs
  tokenLifetimeSeconds: 900
  github:
    subject: "repo:octo@1234567/app@89012345:environment:production"
```

The subject says which repository and which job may sign in:

| The workflow job has | Subject ends with |
|---|---|
| `environment: production` | `:environment:production` |
| no environment, on a push to `main` | `:ref:refs/heads/main` |
| no environment, on a pull request | `:pull_request` |

Repositories created, renamed or transferred since 2026-07-15 (or opted in
by their owner) carry immutable ids in the subject:
`repo:<owner>@<owner-id>/<repo>@<repo-id>:...`. Get them with
`gh api repos/<owner>/<repo> --jq '"\(.owner.id) \(.id)"'`; older
repositories use `repo:<owner>/<repo>:...`. One identity serves one subject:
use one identity for production and another, with fewer rights, for pull
requests.

With `spec.github` the client secret stops working and the credentials Secret
keeps only `client-id` and `token-url`. `status.audience` is the audience the
workflow asks GitHub for. Removing `spec.github` issues a new client secret.

In the workflow:

```yaml
permissions:
  id-token: write      # lets the job request an OIDC token
  contents: read
jobs:
  deploy:
    runs-on: ubuntu-latest
    environment: production
    steps:
      - name: Sign in to Kube-DC
        env:
          AUDIENCE: ${{ vars.KUBE_DC_AUDIENCE }}     # status.audience
          TOKEN_URL: ${{ vars.KUBE_DC_TOKEN_URL }}   # status.tokenUrl
          CLIENT_ID: ${{ vars.KUBE_DC_CLIENT_ID }}   # status.clientId
        run: |
          gh=$(curl -fsS -H "Authorization: bearer $ACTIONS_ID_TOKEN_REQUEST_TOKEN" \
            "$ACTIONS_ID_TOKEN_REQUEST_URL&audience=$AUDIENCE" | jq -r .value)
          token=$(curl -fsS "$TOKEN_URL" -d grant_type=client_credentials --data-urlencode "client_id=$CLIENT_ID" \
            -d client_assertion_type=urn:ietf:params:oauth:client-assertion-type:jwt-bearer \
            --data-urlencode "client_assertion=$gh" | jq -r .access_token)
          echo "::add-mask::$token"
          kubectl config set-credentials ci --token="$token"
```

A GitHub token can be exchanged once; request a new one for each sign-in.

## Rotate, disable, delete

| Action | Effect |
|---|---|
| **Rotate secret** (`kube-dc machine-identities rotate ci`) | A new secret is issued. The old one stops working at once: regenerate the kubeconfig everywhere it is used |
| **Disable sign-in** | New tokens are refused; issued ones expire within their lifetime |
| **Delete** | The identity and its client are removed |

If an identity shows **Failed**, one of its groups is not a group of your
Organization; the details name it.
