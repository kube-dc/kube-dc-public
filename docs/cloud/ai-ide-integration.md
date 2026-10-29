import {AiMcpFlowDiagram} from '@site/src/components/Diagram/CloudFlowDiagrams';

# AI IDE integration

:::tip Agent Skills
Kube-DC ships **16 Agent Skills**: structured procedures and templates that help compatible coding agents generate Kube-DC manifests. Skills complement MCP servers, which provide live cluster access. See [Agent skills setup](#agent-skills-setup) below.
:::

AI coding assistants such as Claude Code, Cursor, Devin Desktop (formerly Windsurf), and VS Code with Copilot can connect to the Kube-DC management cluster in a Project context, through the **Model Context Protocol (MCP)**. After they connect, they can manage workloads, inspect logs, and diagnose resources within the permissions of that Project context.

There are two ways to connect:

- **Hosted MCP endpoint** (recommended): nothing to install; the app signs you in through your Organization. See the next section.
- **Local MCP server**: a Kubernetes MCP server on your machine that uses your kubeconfig. See [How it works](#how-it-works-mcp-and-kubernetes) and the per-IDE sections below.

---

## Hosted MCP endpoint (no install)

When your installation runs the hosted endpoint, every Organization has its
own MCP server URL. An AI app connects to it, you sign in on your
Organization's normal login page, and the assistant works with exactly your
permissions. Nothing is installed and no kubeconfig or secret is stored on your
machine.

Find the URL on your project's **Overview** page (**AI apps → Connect an AI
app**, with one-click installs for Cursor and VS Code) or, for organization
administrators, under **Organization → Access → Machine identities → Connect an
AI app**. It looks like `https://backend.<domain>/mcp/<organization>`.

A **project URL**, `https://backend.<domain>/mcp/<organization>/<project>`,
is the same server with that project as the default: you can ask "list my VMs"
without naming the project. Sign-in and permissions are the same. Add one per
project (for example `kube-dc-dev`, `kube-dc-prod`) to keep them apart.

For **read-only** use, add `/read_only` to either URL (or tick **Read-only** in
the console dialog), for example
`https://backend.<domain>/mcp/<organization>/<project>/read_only`. The app asks
for read access only ("Read your Kube-DC resources…" on the consent page) and
gets only the tools that read: it can list, inspect, read logs and events, but
cannot create, change, stop or delete anything. Where the installation enables
it, Kubernetes itself refuses changes made with such a session's credentials. An app you already allowed full
use keeps it until you revoke it; the connected apps list shows each app's
access (**Full** or **Read only**).

The console's **Connect an AI app** dialog shows these commands and install
links filled in for your project:

| App | How to add the URL |
|---|---|
| Claude Code | `claude mcp add --transport http kube-dc <url>`, then `/mcp` → **Authenticate** |
| Cursor | **Add to Cursor** in the console, or Settings → MCP: `{ "mcpServers": { "kube-dc": { "url": "<url>" } } }` |
| VS Code (Copilot) | **Install in VS Code** in the console, or `code --add-mcp '{"name":"kube-dc","type":"http","url":"<url>"}'`; use Agent mode |
| Claude (web, desktop) | Settings → Connectors → **Add custom connector** |
| ChatGPT | Add a custom MCP server (developer mode) with the URL and OAuth sign-in; on Business/Enterprise plans an admin may need to allow it |
| Google Antigravity | MCP Servers → Manage → View raw config: `{ "mcpServers": { "kube-dc": { "serverUrl": "<url>" } } }` |
| Mistral Le Chat | Connectors → Add connector → Custom MCP connector (Work mode) |

The app opens your Organization's sign-in page the first time and asks you to
allow it.

**Add the Kube-DC skills too (recommended).** The MCP server gives the app
tools; the [agent skills](#agent-skills-setup) give it Kube-DC's step-by-step
procedures and manifests (managed databases, HTTPS routes, VMs…). Apps with a
skills folder (Claude Code, Cursor, Codex, VS Code Copilot, Windsurf) load
them after one command:

```bash
npx skills add kube-dc/kube-dc-public -g --all   # every workspace
npx skills add kube-dc/kube-dc-public -y         # this workspace only
```

Run it again to update them. Apps without skills (ChatGPT, Claude web,
Le Chat) need nothing extra: the MCP server itself serves the guides (see
**Guides** below).

**Tools (20).** Read: `whoami`, `list`, `get_vm`, `get_quota`, `get_logs`,
`get_resources`. `list` takes a kind: `projects`, `vms`, `disks`,
`snapshots`, `services`, `floating_ips`, `external_ips`, `buckets`,
`managed_services` (with the plans the project may order), `clusters`,
`os_images`, `storage_classes` or `events`. `get_quota` shows a project's
quota and, for organization administrators, the organization's usage.
Change: `create_project`, `create_vm`, `vm_power` (start, stop, restart),
`create_floating_ip`, `create_external_ip`, `expose_vm`, `create_disk`,
`attach_disk`, `detach_disk`, `restore_snapshot`, `create_bucket`,
`create_managed_service`, `apply_manifest` and `delete` (a project, VM,
disk, floating IP or external IP, or any Kubernetes object by kind and
apiVersion). `create_managed_service` goes through the console's own
path: its first call returns the price, and the service is created only when
the app calls it again to confirm. Kubernetes clusters are listed; create them
in the console. VMs get the project key pair, as in the console. Tools that delete or stop something are marked so your app
asks you first. Everything runs as you: what you may not do in the console,
the assistant may not do either.

Administrators and connector reviewers: addresses, sign-in details, every
tool's effect and data handling are on
[Hosted MCP for administrators](mcp-for-administrators.md).

**Safety.** Secret values are masked in `get_resources` unless the assistant
asks to reveal them, which is recorded in the audit log (never on a read-only
URL). Logs and events come from workloads; the server tells the app to treat
them as data. A project URL sets a default project; it is not a boundary, as
the tools can still name any project you may access.

Every tool call is recorded in the project's audit log with you and the app
that made it. The project Overview shows the apps used there this week and
the changes they made (**Changes via AI apps**); the **Connect an AI app**
dialog lists the apps you allowed. **Revoke** stops the app from signing in
again at once; access it already holds ends within 15 minutes, when its token
expires.

**Preferred paths.** The server tells apps the platform's defaults, in its
instructions and in the tool descriptions:

- Databases, caches and queues run as managed services
  (`create_managed_service`), not as pods. The development plan (one
  instance) is the default; the production plan (three instances, HA) is used
  when you ask for production or HA.
- Web apps are published through the shared Gateway: a `ClusterIP` Service
  with `service.nlb.kube-dc.com/expose-route: https` gets a hostname and a
  certificate and uses no public IP. `list` with kind `services` shows the hostname once
  it is ready; `whoami` shows the domain hostnames are created under.
- A public IPv4 (external IP, floating IP, `LoadBalancer`) is used only for
  non-HTTP ports or when you ask for a dedicated address; the app checks
  `get_quota` and asks you first.

**Guides.** The server also serves step-by-step guides as MCP resources and
prompts, so apps get them with no install:

- Resources `kube-dc://guide/web-stack` (app + managed database + HTTPS),
  `wordpress`, `publish-http`, `public-ip` and `managed-database`, written in
  terms of the tools above, and every agent skill as
  `kube-dc://skill/<name>` (for example `kube-dc://skill/deploy-web-stack`).
  Claude Code lets the assistant read them itself; in VS Code and Cursor you
  attach one to the chat.
- Prompts (slash commands in apps that support them): `deploy-web-app`,
  `deploy-wordpress`, `publish-http` and `project-health`. A read-only URL
  offers only `project-health`.

For CI, Terraform or a local MCP server, use a
[machine identity](machine-identities.md) instead.

---

## How it works: MCP and Kubernetes

The **Model Context Protocol (MCP)** is an open standard that gives AI assistants structured access to external tools and data sources. A Kubernetes MCP server acts as a bridge between your AI IDE and the Kube-DC management cluster:

<details data-github-only>
<summary>Diagram source for GitHub</summary>

```
AI IDE (Claude/Cursor/Devin Desktop)
         │
         │  MCP protocol
         ▼
Kubernetes MCP Server
         │
         │  Kubernetes API
         ▼
Kube-DC management cluster (Project context via kubeconfig)
```

</details>

<AiMcpFlowDiagram />

The AI can then answer questions like *"Why is my deployment not scaling?"* or execute commands like *"Scale the nginx deployment to 3 replicas in Project `production` in Organization `acme`"*, subject to the permissions in your kubeconfig.

---

## Agent skills setup

A generic Kubernetes MCP server lets an AI assistant run `kubectl`, but it does not know the Kube-DC CRDs, annotations, naming conventions, or multi-tenant constraints. **Agent Skills** bridge this gap by providing structured, Kube-DC-specific knowledge that AI assistants load automatically.

### What skills provide

| Without Skills | With Skills |
|----------------|-------------|
| Agent guesses CRD schemas | Agent knows exact `apiVersion`, fields, and defaults |
| Wrong annotations | Correct `service.nlb.kube-dc.com/*` annotations |
| Missing `qemu-guest-agent` in VMs | Always included (safety rule) |
| Incorrect backing namespace patterns | Correct `{organization}-{project}` backing namespace |
| No awareness of exposure paths | Knows when to use a Gateway Route and when to use a direct EIP |
| Generic kubectl advice | Kube-DC-specific templates and workflows |

### Available skills

| Skill | What It Does |
|-------|-------------|
| `create-project` | Create a project with VPC networking and correct network type |
| `deploy-app` | Deploy a containerized app with optional database and HTTPS |
| `deploy-web-stack` | Web app + managed database + volume + HTTPS hostname (no public IP) in one procedure; WordPress, CMSs, APIs |
| `create-vm` | Provision a VM with SSH access, cloud-init, and guest agent |
| `create-database` | Create a managed database (PostgreSQL, MySQL, MariaDB, ClickHouse) or cache with a binding Secret and connection patterns |
| `manage-database-credentials` | Deliver credential roles to workloads as Secrets and rotate them on demand or on a schedule |
| `expose-service` | Expose through Gateway Route (HTTPS) or Direct EIP (TCP/UDP) |
| `manage-cluster` | Scale workers, upgrade K8s version, access kubeconfig |
| `manage-networking` | Create EIPs, FIPs, understand VPC networking |
| `manage-storage` | S3 buckets (OBC), DataVolumes, PVCs |
| `manage-access` | OrganizationGroup RBAC and role management |
| `manage-secrets` | Project secrets backed by OpenBao, synced to Kubernetes Secrets |
| `manage-certificates` | X.509 certificates from the Organization private CA or public ACME |
| `manage-kms` | Per-Project encryption keys + envelope encryption helpers (Go/Python) |
| `check-quota` | Inspect Organization and Project resource quotas before deploying |
| `ssh-into-vm` | SSH into a VM using the project's auto-generated keypair |
| `use-kube-dc-cli` | Authentication, context switching and Project selection through the kube-dc CLI |

### Install skills

There are three ways to add Kube-DC skills to your IDE, depending on your setup:

#### Option A: `npx skills add` (recommended)

The `skills` CLI can install the catalog globally or into one workspace. A global install makes the selected skills available across workspaces for the targeted coding agents.

```bash
# Install all Kube-DC skills globally (available in every workspace)
npx skills add kube-dc/kube-dc-public -g --all

# Or install specific skills only
npx skills add kube-dc/kube-dc-public --skill create-vm --skill deploy-app -g

# Or install to current workspace only (without -g)
npx skills add kube-dc/kube-dc-public -y
```

The CLI supports many coding agents. Let it detect installed clients, or pass `--agent <name>` to target one explicitly; use the upstream [Supported Agents](https://github.com/vercel-labs/skills#supported-agents) table for current identifiers and paths.

Installation locations vary by agent and scope. Verify the result with `npx skills list` for the workspace or `npx skills list -g` for global installs instead of assuming a shared symlink layout.

:::tip
Use `npx skills add kube-dc/kube-dc-public --list` to see all available skills before installing.
:::

#### Option B: System prompt / IDE settings (lightweight)

If you can't install skills globally, you can paste the Kube-DC context into your IDE's system prompt settings. Copy the content of `_agent-instructions.md` (or `AGENTS.md`):

| IDE | Where to Paste |
|-----|---------------|
| **Claude Code** | Settings → Custom Instructions (or use `CLAUDE.md` in any project) |
| **Cursor** | Settings → Rules for AI → User Rules |
| **Devin Desktop** | Workspace `AGENTS.md` (Devin Local and Cascade) |
| **VS Code + Copilot** | Settings → GitHub Copilot → Instructions |
| **Codex** | Workspace `AGENTS.md` (no global setting) |

The `_agent-instructions.md` file contains CRD tables, naming conventions, safety rules, and service exposure patterns.

:::note Limitations
The system prompt provides **awareness** (correct namespaces, annotations, safety rules) but not the detailed step-by-step procedures and YAML templates that skills include. For full manifest generation capability, use **Option A** (global skills install).

| Capability | Option A (Skills) | Option B (System Prompt) |
|-----------|:-----------------:|:------------------------:|
| CRD reference & naming | ✅ | ✅ |
| Safety rules & constraints | ✅ | ✅ |
| Service exposure decision guide | ✅ | ✅ |
| Step-by-step procedures | ✅ | ❌ |
| Ready-to-use YAML templates | ✅ | ❌ |
| DB connection patterns | ✅ | ❌ |
| Cluster scaling/upgrade guides | ✅ | ❌ |

:::

#### Option C: Workspace install

If you want the full package (skills + docs + examples + workflows) in a specific project:

```bash
git clone https://github.com/kube-dc/kube-dc-public.git
cd kube-dc-public
# Open this folder in your IDE
```

Or add it as a submodule in your own repo:

```bash
cd /path/to/your-repo
git submodule add https://github.com/kube-dc/kube-dc-public.git .kube-dc
```

### Repository structure

```
kube-dc-public/
├── AGENTS.md                          # Universal instructions (all IDEs)
├── CLAUDE.md                          # Claude Code instructions
├── _agent-instructions.md             # Canonical source (edit here)
├── skills/                            # 16 workflow skills
├── knowledge/
│   └── index.md                       # Master catalog of CRDs, skills, docs
├── .agents/skills → ../skills         # Shared skill discovery
├── .claude/skills → ../skills         # Claude Code discovery
├── .cursor/rules/kube-dc-conventions/ # Cursor project rules
├── .devin/                            # Devin rules, skills, and workflows
├── docs/                              # Full documentation
└── examples/                          # Ready-to-use YAML manifests
```

### Verify skill discovery

Do not infer discovery from a directory name. Ask the installer which skills are available at each scope:

```bash
npx skills list
npx skills list -g
```

### Test the skills

Open any project in your IDE with the skills installed globally or in that workspace, then try:

```
Create a Project called "production" in Organization "acme" with cloud networking.
```

```
Deploy a PostgreSQL HA database called "app-db" in Project "production" in Organization "acme".
Show me how to connect my app to it.
```

```
Create an Ubuntu VM with SSH access in Project "production" in Organization "acme".
How do I SSH into it?
```

```
Expose my nginx service over HTTPS with auto TLS in Project "production" in Organization "acme".
```

The agent generates correct Kube-DC manifests, with the right CRD schemas, annotations, and namespace patterns, and you correct nothing by hand.

---

## Step 0: Get your Kube-DC kubeconfig

All integrations below require a valid Project context in a kubeconfig for the Kube-DC management cluster. An Organization login creates one context for each accessible Project; each context selects that Project's backing namespace.

1. Log in to the Kube-DC console
2. On the project Overview, under **Command-line access**, click **Get kubeconfig**
3. Follow the displayed commands to download your kubeconfig

Your kubeconfig will be saved at `~/.kube/config` by default. Verify it works:

```bash
kube-dc config show
```

See [CLI & Kubeconfig](cli-kubeconfig.md) for full setup instructions.

---

## Claude Code

[Claude Code](https://claude.com/product/claude-code) is Anthropic's terminal-based AI agent. It can read files, run commands, manage Git, and, with MCP, work with your Kubernetes cluster from the terminal.

### Install the MCP server

```bash
# Add Kubernetes MCP to Claude Code (one-time setup)
claude mcp add kubernetes -- npx mcp-server-kubernetes
```

This reads your `~/.kube/config` automatically. Verify the connection:

```bash
claude mcp list
```

### Example workflows

After they connect, use natural language in the Claude Code terminal:

```
> Show me all pods that are not running in Project production in Organization acme
> Why is the nginx deployment failing?
> Apply this deployment manifest and wait for it to be ready
> Get the logs from the last crashed container in pod my-app-xxx
> Scale the api-server deployment to 3 replicas
```

Claude Code chains several `kubectl` operations automatically. When a pod is crashing, for example, it fetches the events and the logs, and describes the pod, in one response.

### Use agent skills with Claude Code

Open the `kube-dc-public` repo in Claude Code. It contains:

- `CLAUDE.md`: loaded automatically, references `@_agent-instructions.md` and `@knowledge/index.md`
- `.claude/skills/`: 16 workflow skills (symlink to `skills/`)

With both MCP and skills, Claude Code can generate correct Kube-DC manifests **and** apply them directly. See [Agent skills setup](#agent-skills-setup) above.

### Restrict destructive tools

The npm MCP server can hide destructive tools while retaining create, update, patch, and scale operations:

```bash
claude mcp add kubernetes-safe \
  --env ALLOW_ONLY_NON_DESTRUCTIVE_TOOLS=true \
  -- npx mcp-server-kubernetes
```

This is not read-only access. For inspection-only sessions, use the `containers/kubernetes-mcp-server` binary with `--read-only`, shown in the VS Code section, or a dedicated read-only Kubernetes ServiceAccount.

---

## Cursor

[Cursor](https://www.cursor.com) is a VS Code fork built around AI pair programming. It supports MCP servers through its AI configuration.

### Configure Cursor MCP

Create or edit `~/.cursor/mcp.json`:

```json
{
  "mcpServers": {
    "kubernetes": {
      "command": "npx",
      "args": ["mcp-server-kubernetes"]
    }
  }
}
```

Restart Cursor. The Kubernetes MCP server will start automatically when Cursor's AI features are used.

### Use Cursor MCP

Open the Cursor chat (`Cmd+L` / `Ctrl+L`) and ask questions about your cluster:

```
@kubernetes list all pods in backing namespace acme-production that have been restarting
@kubernetes describe the ingress for my-app and check if the service exists
@kubernetes what is the resource usage against quota in Project production in Organization acme?
```

Cursor can also generate and apply manifests directly from the chat, editing files and running `kubectl apply` in sequence.

### Use agent skills with Cursor

Instead of a generic `.cursorrules` file, open the `kube-dc-public` repo in Cursor. It contains:

- `AGENTS.md`: Cursor reads this automatically for Kube-DC context
- `.cursor/rules/kube-dc-conventions/RULE.md`: always-on rules for namespace patterns, CRD naming, and safety constraints

Cursor then knows the Kube-DC CRDs, annotations, and naming conventions, which a hand-written rules file does not cover. See [Agent skills setup](#agent-skills-setup) above.

---

## Devin Desktop

[Devin Desktop](https://devin.ai/desktop) provides two
local agents: Devin Local, the default for new tabs, and the legacy Cascade
agent. Both support MCP, but they use separate configuration stores.

### Configure Devin local MCP

Add the Kubernetes server at user scope so it is available across workspaces:

```bash
devin mcp add -s user kubernetes -- npx -y mcp-server-kubernetes
devin mcp list
```

Devin Local stores user-scoped servers in
`~/.config/devin/mcp_config.json` on macOS and Linux, or
`%APPDATA%\devin\mcp_config.json` on Windows. Use `-s project` instead to
write a shared `.devin/mcp_config.json` in the current workspace. See the
official [Devin CLI MCP configuration](https://docs.devin.ai/cli/extensibility/mcp/configuration).

### Configure Cascade MCP

In the Cascade panel, select the **MCPs** icon in the top-right menu. You can
also open **Devin Settings → Cascade → MCP Servers**. If the Kubernetes server
is not available in the marketplace, edit the raw Cascade configuration and
add:

```json
{
  "mcpServers": {
    "kubernetes": {
      "command": "npx",
      "args": ["-y", "mcp-server-kubernetes"]
    }
  }
}
```

The raw Cascade configuration remains at the legacy path
`~/.codeium/windsurf/mcp_config.json`. These settings apply only to Cascade;
Devin Local uses the Devin CLI configuration described above. See the official
[Cascade MCP guide](https://docs.devin.ai/desktop/cascade/mcp).

### Use the Kubernetes MCP

Ask Devin Local or Cascade to use the Kubernetes tools when you describe an
infrastructure task:

```
Deploy the app from this Dockerfile to Project production in Organization acme.
Check whether all pods in backing namespace acme-production are healthy.
Summarize any issues.
```

The agent can chain file edits, terminal commands, and MCP tool calls in a
single flow: writing the Deployment YAML, applying it, and monitoring rollout
status.

### Use agent skills with Devin Desktop

Install the catalog through
[Option A](#option-a-npx-skills-add-recommended) and let the `skills` CLI detect
the installed client, or use the current identifier from its
[Supported Agents](https://github.com/vercel-labs/skills#supported-agents)
table. Confirm discovery with `npx skills list -g`. Devin Desktop also
discovers the repository root `AGENTS.md` and preferred `.devin/`
configuration.

---

## VS Code

VS Code supports Kubernetes cluster management with dedicated extensions and with MCP through GitHub Copilot.

### Essential extensions

**[Kubernetes](https://marketplace.visualstudio.com/items?itemName=ms-kubernetes-tools.vscode-kubernetes-tools)**  
The official Kubernetes extension provides a full cluster browser in the VS Code sidebar:
- Browse namespaces, pods, deployments, services, and more
- View and edit live resources
- Stream pod logs directly in the editor
- Supports several kubeconfig contexts, so you switch between Kube-DC Projects at once

**[YAML](https://marketplace.visualstudio.com/items?itemName=redhat.vscode-yaml)**  
Provides schema validation and autocompletion for Kubernetes manifests. YAML files containing `apiVersion:` and `kind:` are automatically validated against Kubernetes schemas.

**[GitLens](https://marketplace.visualstudio.com/items?itemName=eamodio.gitlens)** *(optional)*  
Recommended for GitOps workflows where your manifests live in Git.

### MCP with VS Code + GitHub Copilot

VS Code + GitHub Copilot supports MCP through a workspace configuration. Create `.vscode/mcp.json`:

```json
{
  "servers": {
    "kubernetes": {
      "command": "npx",
      "args": ["mcp-server-kubernetes"]
    }
  }
}
```

With this enabled, you can ask Copilot in the chat panel (`Ctrl+Alt+I`):

```
@workspace show me all crashlooping pods and suggest fixes
@workspace generate a Deployment for nginx with resource limits matching our Dev Pool plan
```

### Alternative: Red Hat Kubernetes MCP server

The [Red Hat kubernetes-mcp-server](https://github.com/containers/kubernetes-mcp-server) is a single-binary MCP server with no external dependencies (no Node.js or kubectl needed):

```bash
# Download the binary for your platform
curl -L https://github.com/containers/kubernetes-mcp-server/releases/latest/download/kubernetes-mcp-server-linux-amd64 \
  -o ~/.local/bin/kubernetes-mcp-server && chmod +x ~/.local/bin/kubernetes-mcp-server

# Save as .vscode/mcp.json
{
  "servers": {
    "kubernetes": {
      "command": "kubernetes-mcp-server",
      "args": ["--read-only"]
    }
  }
}
```

Recommended for production use due to its safety modes and single-binary deployment.

---

## MCP server capabilities

Kubernetes MCP servers expose different tool sets. Kube-DC standard Project roles can inspect events and logs, but they do not grant pod exec, pod attach, pod port-forward, or VM/VMI port-forward. An admission policy also blocks exec and attach in Project backing namespaces, even if a custom Role grants those subresources. Port-forward has a different boundary: a platform operator can grant it through separate diagnostic RBAC. Use an External IP, LoadBalancer, Gateway Route, or the VM browser console for normal interactive access. Common capabilities, subject to the kubeconfig's RBAC, include:

| Operation | Example natural language prompt |
|-----------|--------------------------------|
| List resources | "Show all pods in backing namespace acme-production" |
| Describe resource | "Describe the ingress my-app and check if the backend service exists" |
| Get logs | "Get logs from the last crashed container in pod api-xxx" |
| Apply manifest | "Apply this deployment YAML to the current Project" |
| Scale | "Scale the api deployment to 3 replicas" |
| Delete | "Delete all completed jobs in backing namespace acme-production" |
| Helm | "Install this application chart in the current Project" |
| Diagnose | "Why is my pod in CrashLoopBackOff? Walk through logs, events, and describe" |
| Quota check | "What is the CPU and memory usage against quota for my organization?" |

---

## Practical Kube-DC workflows

### Deploy WordPress with managed database

```
Deploy WordPress with a managed HA MariaDB database in Project production in Organization acme.
Expose it over HTTPS with auto TLS.
```

With Agent Skills loaded or the hosted MCP endpoint, the agent will: create a MariaDB `ManagedService` (WordPress core has no PostgreSQL driver) on the `mariadb-production` plan because the prompt asks for HA (`mariadb-development` otherwise) and a `ServiceBinding` for its owner credential, wait for both to become Ready, deploy WordPress with the delivered credentials on a Ceph-backed volume, expose it with a `ClusterIP` Service carrying `service.nlb.kube-dc.com/expose-route: https` (no public IP), and report the auto-generated hostname. The full manifest set is in [Deploy a full WordPress stack](deploy-wordpress-stack.md).

### Create a VM with SSH access

```
Create an Ubuntu 24.04 VM called "dev-box" with 4 CPU cores and 8GB RAM
in Project production in Organization acme. I need to SSH into it from outside the cluster.
```

The agent will: create a DataVolume + VirtualMachine with `qemu-guest-agent`, expose port 22 with a LoadBalancer Service on the Project gateway address (`bind-on-default-gw-eip`, no new IP; a dedicated EIP or floating IP only if you ask), extract the SSH private key, and provide the connection command.

### Scale a Managed Cluster

```
Scale the "production-cluster" Managed Cluster worker pool to 5 replicas in Project production in Organization acme.
Also show me how to access the cluster's kubeconfig.
```

The agent will: use a JSON patch targeting the requested pool replica field, extract the external workstation kubeconfig from the `{cluster}-cp-admin-kubeconfig-external` Secret (`admin.conf`), and write it to a temp file.

### Expose a gRPC service

```
I have a gRPC service running on port 50051 in Project production in Organization acme.
Expose it externally through a dedicated public IP.
```

The service annotations create `HTTPRoute` or `TLSRoute` resources; they do not create a `GRPCRoute`. Use a Direct EIP + `LoadBalancer` Service for ordinary gRPC/TCP exposure and configure TLS in the application. Platform operators can instead configure an explicit `GRPCRoute` and backend protocol policy.

### Debug a failing deployment

```
The deployment my-api in backing namespace acme-production is not ready.
Check the pod events, describe the deployment, get the last 100 lines of logs,
and tell me what is wrong and how to fix it.
```

### Create an S3 bucket and connect it to an app

```
Create an S3 bucket called "uploads" in Project production in Organization acme.
Show me how to mount the credentials in my deployment.
```

The agent will: create an ObjectBucketClaim with the required `kube-dc.com/organization` label, and show the `envFrom` pattern for mounting the auto-created Secret and ConfigMap.

---

## Security considerations

- **Use a genuinely read-only server mode** (`--read-only`) or a read-only ServiceAccount when only inspection is needed
- **Use a dedicated ServiceAccount** with minimal RBAC instead of a cluster-admin kubeconfig when sharing MCP access with a team
- **Never commit kubeconfig files** to Git repositories
- **Treat the selected backing namespace as context, not a security boundary**: Kubernetes RBAC determines what the credential may access
- Kube-DC uses short-lived access tokens with refresh support; rerun `kube-dc login` when the session can no longer refresh

---

## Further reading

- [kubectl-ai by Google Cloud](https://github.com/GoogleCloudPlatform/kubectl-ai): AI-powered kubectl with natural language to command translation
- [kubectl-mcp-server](https://github.com/rohitg00/kubectl-mcp-server): MCP server with natural language to kubectl, supports Gemini, Claude, Cursor, Devin Desktop, and Copilot
- [mcp-server-kubernetes](https://github.com/Flux159/mcp-server-kubernetes): Full-featured npm MCP server
- [containers/kubernetes-mcp-server](https://github.com/containers/kubernetes-mcp-server): Red Hat single-binary MCP server
- [GitOps with Kube-DC](gitops.md): Managing cluster state declaratively through Git
- [CLI & Kubeconfig](cli-kubeconfig.md): Setting up kubectl for Kube-DC
