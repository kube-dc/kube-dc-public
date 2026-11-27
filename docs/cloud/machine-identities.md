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
identity**. Choose a name (lowercase letters, digits and dashes, at most 40
characters), at least one group, and optionally a description and the token
lifetime (60 to 3600 seconds; empty uses your Organization's default).

| Group | What the identity may do |
|---|---|
| `org-admin` | Everything an Organization administrator may do: create Projects, manage every Project |
| `user` | Read-only access to every Project, like a Member. Set it with the CLI or `kubectl` |
| an Organization group | The project roles that group grants, in the projects it lists |

Prefer an Organization group with only the projects and roles the tool needs.

With the CLI, signed in as an Organization administrator
(`kube-dc login --domain <domain> --org <organization>`):

```bash
kube-dc machine-identities create ci --group org-admin --description "Terraform pipeline"
kube-dc machine-identities kubeconfig ci --out ci.kubeconfig
```

The kubeconfig holds the client secret: keep it like a password (the CLI writes
it with mode 0600). Its user runs `kube-dc machine-identities token`, which
fetches a fresh token whenever the previous one expires, so long runs keep
working. The machine that uses the kubeconfig needs the `kube-dc` CLI at the
same path as the machine that wrote it (`/usr/local/bin/kube-dc` with the
standard install), or change `users[0].user.exec.command` in the file.
`create` also takes `--token-lifetime` (for example `15m`), and `kubeconfig`
takes `-n <namespace>` for the default namespace.

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
`KUBE_DC_CLIENT_ID` and `KUBE_DC_CLIENT_SECRET` in its environment. The console
shows the values under **Show credentials**, with the **Kubernetes API**
address and, where your provider uses a private CA, the **API CA
certificate**. This example assumes the machine running Terraform already
trusts the API's certificate; with a private CA, use the kubeconfig above,
which carries it:

```hcl
provider "kubernetes" {
  host = "https://kube-api.example.com:6443"   # "Kubernetes API" under Show credentials
  exec {
    api_version = "client.authentication.k8s.io/v1"
    command     = "kube-dc"
    args        = ["machine-identities", "token"]
    env = {
      KUBE_DC_TOKEN_URL     = var.token_url
      KUBE_DC_CLIENT_ID     = var.client_id
      KUBE_DC_CLIENT_SECRET = var.client_secret
    }
  }
}
```

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

Where your provider enables the platform API, an administrator identity can
read the Organization's usage through the Kubernetes API:

```bash
kubectl --kubeconfig ci.kubeconfig get organizationusages.platform.kube-dc.com my-org -n my-org -o yaml
```

For a time window, add `from`, `to` (RFC 3339) and `granularity` (`hour`, the
default, for windows up to 31 days, or `day`). A window is at most 90 days;
without `from` it is the 30 days before `to`:

```bash
kubectl --kubeconfig ci.kubeconfig get --raw \
  '/apis/platform.kube-dc.com/v1alpha1/namespaces/my-org/organizationusages/my-org?from=2026-11-01T00:00:00Z&to=2026-11-08T00:00:00Z&granularity=hour'
```

## GitHub Actions without a secret

You need this only when your own GitHub Actions workflow deploys. An
Application the platform builds from GitHub ([Deploy from GitHub](deploy-from-github.md))
needs no workflow and no machine identity.

For a workflow, the hosted MCP tool `connect_repository` (Organization
administrators) creates the identities for you: `gh-<base>-prod` and, with
previews, `gh-<base>-preview`, each in an Organization group of the same name.
`<base>` is the repository name in lowercase letters, digits and dashes, at
most 24 characters. Each group gets the `developer` role in its Project, or
`admin` when the image is private (`privateImage`), so the workflow can create
the image pull Secret. The rest of this section is the same setup by hand.

A machine identity can let one GitHub Actions workflow sign in with the
workflow's own OIDC token instead of a client secret. Nothing is stored in
GitHub, and each token is short-lived. In the console, fill **GitHub Actions
subject (keyless, optional)** in the identity's form; with `kubectl`, set
`spec.github.subject`. Either way it is the exact `sub` claim of the
workflow's token (the CLI's `create` has no flag for it):

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
In the console such an identity shows **Keyless** under **Secret issued**, and
**Show credentials** gives the **GitHub audience** instead of a client
secret. The same sign-in can push to a
[managed registry](managed-services-registry.md#push) without a stored token.

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
          AUDIENCE: ${{ vars.KUBE_DC_AUDIENCE }}       # status.audience
          TOKEN_URL: ${{ vars.KUBE_DC_TOKEN_URL }}     # status.tokenUrl
          CLIENT_ID: ${{ vars.KUBE_DC_CLIENT_ID }}     # status.clientId
          API_SERVER: ${{ vars.KUBE_DC_API_SERVER }}   # "Kubernetes API" under Show credentials
          NAMESPACE: ${{ vars.KUBE_DC_NAMESPACE }}     # <organization>-<project>
        run: |
          gh=$(curl -fsS -H "Authorization: bearer $ACTIONS_ID_TOKEN_REQUEST_TOKEN" \
            "$ACTIONS_ID_TOKEN_REQUEST_URL&audience=$AUDIENCE" | jq -r .value)
          token=$(curl -fsS "$TOKEN_URL" -d grant_type=client_credentials --data-urlencode "client_id=$CLIENT_ID" \
            -d client_assertion_type=urn:ietf:params:oauth:client-assertion-type:jwt-bearer \
            --data-urlencode "client_assertion=$gh" | jq -r .access_token)
          echo "::add-mask::$token"
          kubectl config set-cluster kube-dc --server="$API_SERVER"
          kubectl config set-credentials ci --token="$token"
          kubectl config set-context kube-dc --cluster=kube-dc --user=ci --namespace="$NAMESPACE"
          kubectl config use-context kube-dc
```

If **Show credentials** lists an **API CA certificate**, save it as the
variable `KUBE_DC_CA_CERT`, write it to a file in the job and add
`--certificate-authority=<file> --embed-certs` to `set-cluster`.

A GitHub token can be exchanged once; request a new one for each sign-in.

## Deployment status on GitHub

This section is about a plain Deployment: one your own workflow deploys or
one you apply yourself. An [Application](applications.md) the platform builds from GitHub needs none of it: each build shows
on the commit as the check run `kube-dc` (see
[Builds](deploy-from-github.md#builds)). An Application that runs an image you
built gets nothing on GitHub.

Where the installation's operator enabled it, the platform's GitHub App reports
the rollouts of such a Deployment to GitHub: the commit and pull request show
the deployment with its URL, or a failure. Install the App on your repository
once: in the console, **Applications → Connect GitHub**, step **Install the
GitHub App** (see [Deploy from GitHub](deploy-from-github.md)). Each
installation registers its own App. Then name the repository and commit on the
Deployment:

```yaml
metadata:
  annotations:
    github.kube-dc.com/repository: owner/repo
    github.kube-dc.com/sha: <commit the image was built from>
    github.kube-dc.com/environment: production   # optional
```

The hosted MCP tool `deploy_app` sets them from its `githubRepository`,
`githubSha` and `githubEnvironment` arguments when it deploys a plain
Deployment. Where it creates an Application, the arguments are kept only as
the image's provenance and nothing is reported. A workflow that deploys inside a
GitHub `environment` already gets a deployment from GitHub Actions; add the
annotations there only if you also want the App's status after the workflow
ended. To hear about rollouts outside GitHub, use
[event webhooks](event-webhooks.md) (`DeploymentRolledOut`,
`DeploymentFailed`).

## Rotate, disable, delete

| Action | Effect |
|---|---|
| **Rotate secret** (`kube-dc machine-identities rotate ci`) | A new secret is issued. The old one stops working at once: regenerate the kubeconfig everywhere it is used. Not shown for a keyless identity: it has no secret |
| **Disable sign-in** (`kubectl -n my-org patch mid ci --type merge -p '{"spec":{"disabled":true}}'`) | New tokens are refused; issued ones expire within their lifetime. **Enable sign-in** turns it back on |
| **Delete identity** (`kube-dc machine-identities delete ci`) | The identity, its client and its credentials Secret are removed. New sign-ins fail at once; issued tokens expire within their lifetime |

If an identity shows **Failed**, one of its groups is not a group of your
Organization; the details name it. A group that was just created can show the
same for about a minute; the identity is checked again every minute.
**Pending** means the identity is still being set up or the sign-in service
could not be reached; the status gives the reason.
