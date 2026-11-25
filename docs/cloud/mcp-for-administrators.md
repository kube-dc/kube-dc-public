# Hosted MCP for administrators

This page gives organization administrators and connector reviewers what they
need to approve or configure the hosted Kube-DC MCP server: its addresses, how
apps sign in, what each tool can do, and what data leaves the platform. For
connecting an app as a user, see [AI IDE integration](ai-ide-integration.md).

## Addresses

Every Organization has its own server. `<domain>` is your installation's
domain and `<organization>` your Organization's name.

| URL | Use |
|---|---|
| `https://backend.<domain>/mcp/<organization>` | The Organization: every project the person may access. |
| `https://backend.<domain>/mcp/<organization>/<project>` | The same server with `<project>` as the default project. It is not a boundary: tools can still name other projects the person may access. |
| `…/read_only` after either URL | Read tools only, with a read-only token that Kubernetes also refuses for changes (where the installation enables it). |
| `https://backend.<domain>/mcp` | Documentation search only: `search_docs` and `get_doc`, no sign-in, nothing of your Organization. Its Server Card is at `/mcp/server-card` and `/.well-known/mcp/server-card.json`. Absent when your provider turned the documentation tools off. |

Each Organization or project URL (not the documentation-only one) also publishes:

- **Protected Resource Metadata** (RFC 9728) at
  `https://backend.<domain>/.well-known/oauth-protected-resource/mcp/<organization>[/<project>][/read_only]`.
  It names the authorization server and the one scope to ask for.
- **A Server Card** at `<URL>/server-card`: public metadata for discovery
  (name, version, the URL to connect to and the MCP protocol versions served).
  It lists no tools and carries nothing private.

The server speaks Streamable HTTP without sessions (MCP 2026-07-28, and the
2025-03-26, 2025-06-18 and 2025-11-25 revisions on the same URL).

Two MCP extensions are offered to apps that support them; other apps work as
before:

- **Tasks** (`io.modelcontextprotocol/tasks`, MCP 2026-07-28): `create_vm`,
  `create_project`, `create_managed_service` and `create_cluster` return a task that the app
  follows until the object is ready, instead of polling the list. The task is
  read again from Kubernetes with the person's own permissions on every check,
  and only the person who started it can follow it. Cancelling a task does not
  delete what was created; use `delete`.
- **MCP Apps** (`io.modelcontextprotocol/ui`): the results of `list`, `get_vm`
  and `get_quota` show as tables, VM details and quota bars in apps that render
  MCP Apps (Claude, VS Code with GitHub Copilot, Microsoft 365 Copilot). The
  view only reads; its Refresh button repeats the same call.

## Sign-in

| | |
|---|---|
| Authorization server | `https://login.<domain>/realms/<organization>`: your Organization's own Keycloak realm and login page |
| Scope | `kube-dc:mcp` (full), `kube-dc:mcp:read` (read-only URLs) |
| Grant | Authorization code with PKCE (S256) |
| Token audience | The Organization's MCP URL (`https://backend.<domain>/mcp/<organization>`), also when the app connected to a project or read-only URL |

Apps register themselves; nobody has to create a client:

1. **Client ID Metadata Documents** first: apps whose client ID is an https URL
   on a trusted domain (by default `claude.ai`, `vscode.dev`,
   `code.visualstudio.com`, `antigravity.google`).
2. **Dynamic Client Registration** otherwise, limited to redirect URIs on
   trusted hosts (by default `localhost`, `127.0.0.1`, `claude.ai`,
   `claude.com`, `chatgpt.com`, `vscode.dev`, `cursor.com`, `www.cursor.com`,
   `anysphere.cursor-mcp`, `global.consent.azure-apim.net`,
   `callback.mistral.ai`).
3. A few apps are **pre-registered** by the installation (ChatGPT and Codex).

The platform operator sets these lists for the whole installation, and can
hide the endpoint from tenants; see [Hosted MCP endpoint](/platform/hosted-mcp).

### Apps that need a client ID and secret

Some apps take a client ID and secret that an administrator enters by hand
instead of registering themselves: for example Claude for Enterprise
connectors and Microsoft Copilot Studio's manual setup. An Organization
administrator registers such an app as an `MCPApp` in the Organization
namespace.

In the console, open **Organization → Access → Machine identities**, and under
**Apps with a client ID and secret** click **Register app**. The same section
shows the client ID and secret, issues a new secret and removes the app. The
manifest below does the same:

```yaml
apiVersion: kube-dc.com/v1
kind: MCPApp
metadata:
  name: claude
  namespace: <organization>
spec:
  displayName: Claude for Acme        # shown on the consent page and in the console
  redirectUris:                       # from the app's setup page
    - https://claude.ai/api/mcp/auth_callback
  access: full                        # or read: only the read-only URLs and tools
```

Redirect URIs must be `https`, or `http` on `localhost` or `127.0.0.1`; a
trailing `*` matches any path. When the app is ready (`kubectl get mcpapp`),
its Secret `mcp-app-<name>` holds what the app's setup asks for:

| Key | Value |
|---|---|
| `client-id`, `client-secret` | The app's credentials |
| `mcp-url` | The Organization's MCP URL (a project URL or `/read_only` also works) |
| `issuer` | The authorization server |
| `authorize-url`, `token-url` | Its OAuth endpoints, for apps that ask for them |
| `scopes` | What to request: `openid kube-dc:mcp`, or `openid kube-dc:mcp:read` for `access: read` |

People still sign in themselves, see the consent page and act with their own
permissions; the secret only identifies the app. Only Organization
administrators can read the Secret. To issue a new secret, increase
`spec.secretGeneration` (the old one stops working at once). `spec.disabled:
true` refuses new sign-ins; deleting the `MCPApp` removes the app and every
consent given to it.

**Revoking an app:** the project's **Connect an AI app** dialog lists the apps
you allowed (organization admins see every member's apps under **Organization
→ Access → Machine identities → Connected AI apps**, with **Everyone in the
organization** ticked), and **Revoke** stops one from signing in again at once.
Access it already holds ends within 15 minutes, when its token expires.

## What the tools can do

Every tool call runs as the person who signed in: the MCP token is exchanged
for a Kubernetes token of that person, and Kubernetes RBAC decides. The server
adds no rights, and the MCP token itself never reaches Kubernetes.

Tools carry MCP annotations, so apps ask the person before a destructive one.

| Tool | Kind | What it does |
|---|---|---|
| `whoami` | read | The signed-in person, Organization and groups, and the domain HTTPS hostnames are created under. |
| `list` | read | Lists by kind: projects, VMs, disks, snapshots, services, floating IPs, external IPs, buckets, managed services and their plans, Kubernetes clusters, OS images, storage classes, events, jobs, backups, event webhooks, apps and builds. |
| `get_vm` | read | One VM's status, size, addresses, disks and conditions. |
| `get_quota` | read | A project's quota and usage; for Organization administrators also the Organization's usage. |
| `get_logs` | read | The last lines of a pod's log, or of an app's build (`build`). |
| `get_resources` | read | Any Kubernetes object the person may read. Secret values are masked. |
| `rollout_status` | read | Whether an app is live: ready, progressing or failed with the reason, its pods, recent log lines of a failing pod and its HTTPS URL. |
| `list_secrets` | read | The project's managed secrets: type, key names (never values), which apps use each one, and its console page. |
| `create_project` | write | Creates a project. |
| `create_vm` | write | Creates a VM, with the console's own checks (image, quota, storage). |
| `vm_power` | destructive | Starts, stops or restarts a VM. |
| `create_floating_ip` | write | Gives a VM a floating IP. |
| `create_external_ip` | write | Reserves an external IP. |
| `expose_vm` | write | Opens TCP/UDP ports of a VM through a load balancer. |
| `create_disk` | write | Creates a data disk. |
| `attach_disk` | write | Attaches a disk to a VM. |
| `detach_disk` | destructive | Detaches a data disk from a VM. |
| `restore_snapshot` | write | Restores a snapshot to a new disk. |
| `create_bucket` | write | Creates an object storage bucket. |
| `create_managed_service` | write | Orders a managed database, cache or queue: the first call returns the price, the second (after the person saw it and chose what deleting the service later does with its data) creates it and delivers its credentials to a Secret. |
| `create_secret` | write | Creates a managed secret for an app: either a generated value nobody sees, or an empty secret whose values the person types on its console page. Values never pass through the agent. |
| `deploy_app` | write | Creates or changes an app (an [Application](applications.md), which generates the Deployment and Service, where the installation creates them for new apps; else the Deployment and Service directly), from an image or built from GitHub on every push, with an HTTPS hostname when public, optionally on the person's own domain. Called again with a new image it ships a new version; an app built from GitHub builds again when its build settings change or with `buildNow`, and can build a preview for every pull request, with its own database. A release command can run before each new version takes traffic. Without a port it runs a worker (no Service); it can also autoscale on CPU, mount a persistent volume, deploy a temporary app under its own name (`previewHours` makes the platform remove the named app after that many hours; it does not copy an existing app), and pause or resume builds and rollouts of an Application (`suspend`). |
| `run_job` | write | Runs a command once (a seed, a backfill) with an app's image, variables and secrets, or with an image. |
| `schedule_job` | write | Runs a command on a schedule (a cron job), with an app's image and secrets; it follows the app's new image and is deleted with the app. |
| `restart` | destructive | Restarts every pod of an app, one by one. Apps that support it ask the person to type the name first. |
| `rollback` | destructive | Returns an app to an earlier version: an Application to a release (`release`, pinned until `resume: true`), a plain Deployment to a `revision`; with a warm previous version (`deploy_app warmPrevious`) traffic switches to it in seconds (`instant: true`). Apps that support it ask the person to type the name first. |
| `apply_manifest` | write | Creates or updates one Kubernetes object. |
| `delete` | destructive | Deletes a project, app, VM, disk, floating IP, external IP, event webhook or any Kubernetes object, and cancels an app's build (kind `build`). Apps that support it ask the person to type the name first. |
| `extend_preview` | write | Keeps a preview app longer before the platform removes it. |
| `create_webhook` | write | Sends the organization's platform events (deploys, live hostnames, previews, alerts) to an HTTPS URL, signed. Organization admins only. |
| `connect_repository` | write | Sets up keyless deploys from GitHub Actions for a repository: machine identities for its production (and preview) environment, plus the workflow files and repository variables to add. Organization admins only. |
| `registry_login` | write | Where your provider offers a container registry service: a `docker login` command for it, valid 15 minutes, with the user's own push or pull rights. Not offered to read-only sessions. |
| `search_docs` | read | Searches the Kube-DC documentation and returns the best-matching sections with links. |
| `get_doc` | read | Reads one documentation page as Markdown, or lists the pages. |
| `create_cluster` | write | Creates a managed Kubernetes cluster as the console does (catalog version and worker image, dedicated etcd, HTTPS API address). Workers count against the Project's quota. |
| `get_kubeconfig` | write | Returns a managed cluster's admin kubeconfig, a credential. The user confirms first, and the call is audited as a reveal. Not offered to read-only sessions. |
| `create_snapshot` | write | Takes a snapshot of a disk, with the snapshot class of its storage class. |
| `backup_service` | write | Takes a backup of a managed service now, besides its scheduled backups. |
| `restore_backup` | write | Restores a completed managed-service backup into a new service, for PostgreSQL optionally to a point in time. The original is not changed. |

Read-only sessions offer only the read tools above, plus `query`, which runs
a short read-only script over the read tools to combine several reads in one
call. `search_docs` and `get_doc` are there unless your provider turned the
documentation tools off.

The server also offers resources and prompts. Resources are read-only text:
six step-by-step guides (`kube-dc://guide/<name>`), the agent skills with
their templates (`kube-dc://skill/<name>`) and the view that MCP Apps hosts
render. Prompts `deploy-web-app`, `deploy-wordpress` and `publish-http` start
a task with a guide attached; `project-health` gives read-only health-check
instructions and is the only prompt read-only sessions get. None of them holds data of your Organization.

## Data handling

- **What leaves the platform:** tool results go to the AI app, and from there
  to its model provider, under that app's terms. Only what a tool returns is
  sent; the server does not stream anything else.
- **Secrets:** `get_resources` masks Secret values (keys and sizes only). It
  returns them only when the assistant asks to reveal them, never on a
  read-only URL, and every reveal is recorded in the audit log.
- **Credentials other tools return:** `get_kubeconfig` returns a managed
  cluster's admin kubeconfig after the person confirms, and `registry_login`
  returns a registry token valid 15 minutes. Both go to the AI app like any
  tool result, and neither is offered to read-only sessions. `create_secret`
  and `create_webhook` return only the name of a Secret, never its value.
- **Workload output:** logs and events are marked as data, not instructions,
  so apps do not follow text a workload wrote.
- **Audit:** every tool call that ran on an Organization or project URL is an
  audit event naming the person and the app, and the project when the call
  names one. A `query` is one event for the script, not one per read inside
  it. Calls to the documentation-only URL are not audited. Refused sign-ins
  are recorded, at most one a minute per Organization and client address on
  each server replica. The project **Overview** shows the apps used this week and the
  changes they made (**Changes via AI apps**).
- **Limits:** by default 600 requests a minute per person and app, and 1200 a
  minute per client address. A request is at most 1 MiB. Long lists are cut
  to fit. A `query` script runs at most 10 seconds, makes at most 50 tool
  calls, reads at most 2 MiB and returns at most 256 KiB; each server replica
  runs at most two at once per Organization.
