import {AiMcpFlowDiagram} from '@site/src/components/Diagram/CloudFlowDiagrams';

# AI IDE Integration

:::tip Agent Skills
Kube-DC ships **16 Agent Skills**: structured procedures and templates that help compatible coding agents generate Kube-DC manifests. Skills complement MCP servers, which provide live cluster access. See [Agent Skills Setup](#agent-skills-setup) below.
:::

Modern AI coding assistants — Claude Code, Cursor, Devin Desktop (formerly Windsurf), and VS Code with Copilot — can connect to the Kube-DC management cluster in a Project context through the **Model Context Protocol (MCP)**. Once connected, they can manage workloads, inspect logs, and diagnose resources within the permissions of that Project context.

---

## How It Works: MCP and Kubernetes

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

## Agent Skills Setup

Generic Kubernetes MCP servers let AI assistants run `kubectl` — but they don't know about Kube-DC CRDs, annotations, naming conventions, or multi-tenant constraints. **Agent Skills** bridge this gap by providing structured, Kube-DC-specific knowledge that AI assistants load automatically.

### What Skills Provide

| Without Skills | With Skills |
|----------------|-------------|
| Agent guesses CRD schemas | Agent knows exact `apiVersion`, fields, and defaults |
| Wrong annotations | Correct `service.nlb.kube-dc.com/*` annotations |
| Missing `qemu-guest-agent` in VMs | Always included (safety rule) |
| Incorrect backing namespace patterns | Correct `{organization}-{project}` backing namespace |
| No awareness of exposure paths | Knows Gateway Route vs Direct EIP decision |
| Generic kubectl advice | Kube-DC-specific templates and workflows |

### Available Skills

| Skill | What It Does |
|-------|-------------|
| `create-project` | Create a project with VPC networking and correct network type |
| `deploy-app` | Deploy a containerized app with optional database and HTTPS |
| `create-vm` | Provision a VM with SSH access, cloud-init, and guest agent |
| `create-database` | Create managed PostgreSQL/MariaDB with connection patterns |
| `manage-database-credentials` | Rotate database-user passwords and project the current value into a Secret |
| `expose-service` | Expose via Gateway Route (HTTPS) or Direct EIP (TCP/UDP) |
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

### Install Skills

There are three ways to add Kube-DC skills to your IDE, depending on your setup:

#### Option A: `npx skills add` (Recommended)

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

#### Option B: System Prompt / IDE Settings (Lightweight)

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

#### Option C: Workspace Install

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

### Repository Structure

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

### Verify Skill Discovery

Do not infer discovery from a directory name. Ask the installer which skills are available at each scope:

```bash
npx skills list
npx skills list -g
```

### Test the Skills

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
Expose my nginx service via HTTPS with auto TLS in Project "production" in Organization "acme".
```

The agent should generate correct Kube-DC manifests with proper CRD schemas, annotations, and namespace patterns — without any manual correction.

---

## Step 0: Get Your Kube-DC Kubeconfig

All integrations below require a valid Project context in a kubeconfig for the Kube-DC management cluster. An Organization login creates one context for each accessible Project; each context selects that Project's backing namespace.

1. Log in to the Kube-DC console
2. Click **Get CLI Access** in the dashboard
3. Follow the displayed commands to download your kubeconfig

Your kubeconfig will be saved at `~/.kube/config` by default. Verify it works:

```bash
kube-dc config show
```

See [CLI & Kubeconfig](cli-kubeconfig.md) for full setup instructions.

---

## Claude Code

[Claude Code](https://claude.com/product/claude-code) is Anthropic's terminal-based AI agent. It can read files, run commands, manage Git, and — with MCP — interact with your Kubernetes cluster directly from the terminal.

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

Once connected, use natural language in the Claude Code terminal:

```
> Show me all pods that are not running in Project production in Organization acme
> Why is the nginx deployment failing?
> Apply this deployment manifest and wait for it to be ready
> Get the logs from the last crashed container in pod my-app-xxx
> Scale the api-server deployment to 3 replicas
```

Claude Code can chain multiple kubectl operations automatically — for example, if a pod is crashing, it will fetch events, logs, and describe the pod in a single response.

### Use Agent Skills with Claude Code

Open the `kube-dc-public` repo in Claude Code. It contains:

- `CLAUDE.md` — loaded automatically, references `@_agent-instructions.md` and `@knowledge/index.md`
- `.claude/skills/` — 16 workflow skills (symlink to `skills/`)

With both MCP and skills, Claude Code can generate correct Kube-DC manifests **and** apply them directly. See [Agent Skills Setup](#agent-skills-setup) above.

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
@kubernetes what is the current resource usage vs quota in Project production in Organization acme?
```

Cursor can also generate and apply manifests directly from the chat, editing files and running `kubectl apply` in sequence.

### Use Agent Skills with Cursor

Instead of a generic `.cursorrules` file, open the `kube-dc-public` repo in Cursor. It contains:

- `AGENTS.md` — Cursor reads this automatically for Kube-DC context
- `.cursor/rules/kube-dc-conventions/RULE.md` — always-on rules for namespace patterns, CRD naming, and safety constraints

This gives Cursor full awareness of Kube-DC CRDs, annotations, and naming conventions — far more comprehensive than a hand-written rules file. See [Agent Skills Setup](#agent-skills-setup) above.

---

## Devin Desktop

[Devin Desktop](https://devin.ai/desktop) provides two
local agents: Devin Local, the default for new tabs, and the legacy Cascade
agent. Both support MCP, but they use separate configuration stores.

### Configure Devin Local MCP

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

### Use Agent Skills with Devin Desktop

Install the catalog through
[Option A](#option-a-npx-skills-add-recommended) and let the `skills` CLI detect
the installed client, or use the current identifier from its
[Supported Agents](https://github.com/vercel-labs/skills#supported-agents)
table. Confirm discovery with `npx skills list -g`. Devin Desktop also
discovers the repository root `AGENTS.md` and preferred `.devin/`
configuration.

---

## VS Code

VS Code supports Kubernetes cluster management via both dedicated extensions and MCP through GitHub Copilot.

### Essential Extensions

**[Kubernetes](https://marketplace.visualstudio.com/items?itemName=ms-kubernetes-tools.vscode-kubernetes-tools)**  
The official Kubernetes extension provides a full cluster browser in the VS Code sidebar:
- Browse namespaces, pods, deployments, services, and more
- View and edit live resources
- Stream pod logs directly in the editor
- Supports multiple kubeconfig contexts — switch between Kube-DC Projects instantly

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

### Alternative: Red Hat Kubernetes MCP Server

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

## MCP Server Capabilities

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
| Quota check | "What is the current CPU and memory usage vs quota for my organization?" |

---

## Practical Kube-DC Workflows

### Deploy WordPress with managed database

```
Deploy WordPress with a managed HA MariaDB database in Project production in Organization acme.
Expose it via HTTPS with auto TLS.
```

With Agent Skills loaded, the agent will: create a `KdcDatabase` with `engine: mariadb` (WordPress core has no PostgreSQL driver — `engine: postgresql` produces a tight CrashLoop), wait for it to become Ready, build a small bridge Secret aliasing the auto-generated password to the chart's expected key (`mariadb-password`), deploy WordPress via `helm install` with `externalDatabase.existingSecret` pointing at the bridge, expose with `service.type=LoadBalancer` plus `service.nlb.kube-dc.com/expose-route: https`, and report the auto-generated hostname.

### Create a VM with SSH access

```
Create an Ubuntu 24.04 VM called "dev-box" with 4 CPU cores and 8GB RAM
in Project production in Organization acme. I need to SSH into it from outside the cluster.
```

The agent will: create a DataVolume + VirtualMachine with `qemu-guest-agent`, create an EIP + LoadBalancer service for SSH, extract the SSH private key, and provide the connection command.

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

## Security Considerations

- **Use a genuinely read-only server mode** (`--read-only`) or a read-only ServiceAccount when only inspection is needed
- **Use a dedicated ServiceAccount** with minimal RBAC instead of a cluster-admin kubeconfig when sharing MCP access with a team
- **Never commit kubeconfig files** to Git repositories
- **Treat the selected backing namespace as context, not a security boundary** — Kubernetes RBAC determines what the credential may access
- Kube-DC uses short-lived access tokens with refresh support; rerun `kube-dc login` when the session can no longer refresh

---

## Further Reading

- [kubectl-ai by Google Cloud](https://github.com/GoogleCloudPlatform/kubectl-ai) — AI-powered kubectl with natural language to command translation
- [kubectl-mcp-server](https://github.com/rohitg00/kubectl-mcp-server) — MCP server with natural language to kubectl, supports Gemini, Claude, Cursor, Devin Desktop, and Copilot
- [mcp-server-kubernetes](https://github.com/Flux159/mcp-server-kubernetes) — Full-featured npm MCP server
- [containers/kubernetes-mcp-server](https://github.com/containers/kubernetes-mcp-server) — Red Hat single-binary MCP server
- [GitOps with Kube-DC](gitops.md) — Managing cluster state declaratively via Git
- [CLI & Kubeconfig](cli-kubeconfig.md) — Setting up kubectl for Kube-DC
