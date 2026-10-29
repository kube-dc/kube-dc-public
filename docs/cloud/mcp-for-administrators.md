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

Each URL also publishes:

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
  `create_project` and `create_managed_service` return a task that the app
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
| Token audience | The exact MCP URL the app connected to |

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

The platform operator sets these lists for the whole installation.

### Apps that need a client ID and secret

Some apps take a client ID and secret that an administrator enters by hand
instead of registering themselves: for example Claude for Enterprise
connectors and Microsoft Copilot Studio's manual setup. An Organization
administrator registers such an app as an `MCPApp` in the Organization
namespace:

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
people allowed, and **Revoke** stops one from signing in again at once.
Access it already holds ends within 15 minutes, when its token expires.

## What the tools can do

Every tool call runs as the person who signed in: the MCP token is exchanged
for a Kubernetes token of that person, and Kubernetes RBAC decides. The server
adds no rights, and the MCP token itself never reaches Kubernetes.

Tools carry MCP annotations, so apps ask the person before a destructive one.

| Tool | Kind | What it does |
|---|---|---|
| `whoami` | read | The signed-in person, Organization and groups. |
| `list` | read | Lists by kind: projects, VMs, disks, snapshots, services, floating IPs, external IPs, buckets, managed services and their plans, Kubernetes clusters, OS images, storage classes, events. |
| `get_vm` | read | One VM's status, size, addresses, disks and conditions. |
| `get_quota` | read | A project's quota and usage; for Organization administrators also the Organization's usage. |
| `get_logs` | read | The last lines of a pod's log. |
| `get_resources` | read | Any Kubernetes object the person may read. Secret values are masked. |
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
| `create_managed_service` | write | Orders a managed database, cache or queue: the first call returns the price, the second (after the person saw it) creates it. |
| `apply_manifest` | write | Creates or updates one Kubernetes object. |
| `delete` | destructive | Deletes a project, VM, disk, floating IP, external IP or any Kubernetes object. Apps that support it ask the person to type the name first. |

Read-only URLs offer only the six read tools.

## Data handling

- **What leaves the platform:** tool results go to the AI app, and from there
  to its model provider, under that app's terms. Only what a tool returns is
  sent; the server does not stream anything else.
- **Secrets:** `get_resources` masks Secret values (keys and sizes only). It
  returns them only when the assistant asks to reveal them, never on a
  read-only URL, and every reveal is recorded in the audit log.
- **Workload output:** logs and events are marked as data, not instructions,
  so apps do not follow text a workload wrote.
- **Audit:** every tool call is an audit event naming the person, the app and
  the project. The project **Overview** shows the apps used this week and the
  changes they made (**Changes via AI apps**).
- **Limits:** by default 600 requests a minute per person and app, and 1200 a
  minute per client address.
