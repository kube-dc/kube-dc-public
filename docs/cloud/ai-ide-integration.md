# AI IDE Integration

:::tip New: Agent Skills
Kube-DC now ships **Agent Skills** — structured knowledge packages that teach AI assistants how to generate correct Kube-DC manifests. Skills work alongside MCP servers and are supported by Claude Code, Cursor, and Windsurf. See [Agent Skills Setup](#agent-skills-setup) below.
:::

Modern AI coding assistants — Claude Code, Cursor, Windsurf, and VS Code with Copilot — can connect directly to your Kube-DC cluster via the **Model Context Protocol (MCP)**. Once connected, you can manage workloads, debug pods, apply manifests, and inspect logs entirely through natural language, without leaving your editor.

---

## How It Works: MCP and Kubernetes

The **Model Context Protocol (MCP)** is an open standard that gives AI assistants structured access to external tools and data sources. A Kubernetes MCP server acts as a bridge between your AI IDE and your cluster:

```
AI IDE (Claude/Cursor/Windsurf)
         │
         │  MCP protocol
         ▼
Kubernetes MCP Server
         │
         │  Kubernetes API
         ▼
Kube-DC Cluster (via kubeconfig)
```

The AI can then answer questions like *"Why is my deployment not scaling?"* or execute commands like *"Scale the nginx deployment to 3 replicas in project acme-dev"* with full awareness of your cluster state.

---

## Agent Skills Setup

Generic Kubernetes MCP servers let AI assistants run `kubectl` — but they don't know about Kube-DC CRDs, annotations, naming conventions, or multi-tenant constraints. **Agent Skills** bridge this gap by providing structured, Kube-DC-specific knowledge that AI assistants load automatically.

### What Skills Provide

| Without Skills | With Skills |
|----------------|-------------|
| Agent guesses CRD schemas | Agent knows exact `apiVersion`, fields, and defaults |
| Wrong annotations | Correct `service.nlb.kube-dc.com/*` annotations |
| Missing `qemu-guest-agent` in VMs | Always included (safety rule) |
| Wrong namespace patterns | Correct `{org}-{project}` naming |
| No awareness of exposure paths | Knows Gateway Route vs Direct EIP decision |
| Generic kubectl advice | Kube-DC-specific templates and workflows |

### Available Skills

| Skill | What It Does |
|-------|-------------|
| `create-project` | Create a project with VPC networking and correct network type |
| `deploy-app` | Deploy a containerized app with optional database and HTTPS |
| `create-vm` | Provision a VM with SSH access, cloud-init, and guest agent |
| `create-database` | Create managed PostgreSQL/MariaDB with connection patterns |
| `expose-service` | Expose via Gateway Route (HTTPS) or Direct EIP (TCP/UDP) |
| `manage-cluster` | Scale workers, upgrade K8s version, access kubeconfig |
| `manage-networking` | Create EIPs, FIPs, understand VPC networking |
| `manage-storage` | S3 buckets (OBC), DataVolumes, PVCs |
| `manage-access` | OrganizationGroup RBAC and role management |
| `ssh-into-vm` | SSH into a VM using the project's auto-generated keypair |
| `use-kube-dc-cli` | Authentication, context switching, and namespace management via kube-dc CLI |

### Install Skills

There are three ways to add Kube-DC skills to your IDE, depending on your setup:

#### Option A: `npx skills add` (Recommended)

The standard way to install agent skills — one command installs to the right directory for your IDE. Skills are available in **any project** you open.

```bash
# Install all Kube-DC skills globally (available in every workspace)
npx skills add kube-dc/kube-dc-public -g -y

# Or install specific skills only
npx skills add kube-dc/kube-dc-public --skill create-vm --skill deploy-app -g

# Or install to current workspace only (without -g)
npx skills add kube-dc/kube-dc-public -y
```

The CLI auto-detects your installed IDEs (Claude Code, Cursor, Windsurf, Codex, Copilot, and [40+ more](https://github.com/vercel-labs/skills#supported-agents)) and installs skills to the correct directory:

| IDE | Global Path | Workspace Path |
|-----|------------|---------------|
| Claude Code | `~/.claude/skills/` | `.claude/skills/` |
| Cursor | `~/.cursor/skills/` | `.agents/skills/` |
| Windsurf | `~/.codeium/windsurf/skills/` | `.windsurf/skills/` |
| Codex | `~/.codex/skills/` | `.agents/skills/` |
| Copilot | `~/.copilot/skills/` | `.agents/skills/` |

Each skill's `name` and `description` appear in the agent's context at startup (~100 tokens per skill). The full SKILL.md and supporting templates are loaded on demand when the agent detects a matching task.

:::tip
Use `npx skills add kube-dc/kube-dc-public --list` to see all available skills before installing.
:::

#### Option B: System Prompt / IDE Settings (Lightweight)

If you can't install skills globally, you can paste the Kube-DC context into your IDE's system prompt settings. Copy the content of `_agent-instructions.md` (or `AGENTS.md`):

| IDE | Where to Paste |
|-----|---------------|
| **Claude Code** | Settings → Custom Instructions (or use `CLAUDE.md` in any project) |
| **Cursor** | Settings → Rules for AI → User Rules |
| **Windsurf** | Settings → AI Rules → Global Rules |
| **VS Code + Copilot** | Settings → GitHub Copilot → Instructions |
| **Codex** | Workspace `AGENTS.md` (no global setting) |

The `_agent-instructions.md` file (~150 lines) contains CRD tables, naming conventions, safety rules, and service exposure patterns — compact enough to fit in any system prompt field.

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
| Windsurf workflows (`/deploy-wordpress`) | ✅ | ❌ |
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
cd my-project
git submodule add https://github.com/kube-dc/kube-dc-public.git .kube-dc
```

### Repository Structure

```
kube-dc-public/
├── AGENTS.md                          # Universal instructions (all IDEs)
├── CLAUDE.md                          # Claude Code instructions
├── _agent-instructions.md             # Canonical source (edit here)
├── skills/                            # 10 workflow-grouped skills (npx skills discovers this)
│   ├── create-project/
│   ├── deploy-app/
│   ├── create-vm/
│   ├── create-database/
│   ├── expose-service/
│   ├── manage-cluster/
│   ├── manage-networking/
│   ├── manage-storage/
│   ├── manage-access/
│   └── ssh-into-vm/
├── knowledge/
│   └── index.md                       # Master catalog of CRDs, skills, docs
├── .windsurf/
│   ├── rules/kube-dc-conventions.md   # Always-on safety rules
│   ├── skills/ → skills/              # Symlink
│   └── workflows/                     # /deploy-wordpress, /setup-project
├── .claude/
│   └── skills/ → skills/              # Symlink
├── .cursor/
│   └── rules/kube-dc-conventions/     # Always-on safety rules
├── docs/                              # Full documentation
└── examples/                          # Ready-to-use YAML manifests
```

### What Each IDE Discovers

| IDE | Install via `npx skills` | Workspace Discovery | System Prompt |
|-----|:------------------------:|:-------------------:|:-------------:|
| **Claude Code** | ✅ `~/.claude/skills/` | `CLAUDE.md` + `.claude/skills/` | Settings → Custom Instructions |
| **Cursor** | ✅ `~/.cursor/skills/` | `AGENTS.md` + `.cursor/rules/` | Settings → Rules for AI |
| **Windsurf** | ✅ `~/.codeium/windsurf/skills/` | `AGENTS.md` + `.windsurf/skills/` | Settings → Global Rules |
| **Codex** | ✅ `~/.codex/skills/` | `AGENTS.md` | — |
| **Copilot** | ✅ `~/.copilot/skills/` | `AGENTS.md` | Settings → Instructions |

### Test the Skills

Open any project in your IDE (with skills installed globally or via system prompt) and try:

```
Create a new project called "demo" in organization "myorg" with cloud networking.
```

```
Deploy a PostgreSQL HA database called "app-db" in project shalb-demo.
Show me how to connect my app to it.
```

```
Create an Ubuntu VM with SSH access in namespace shalb-demo.
How do I SSH into it?
```

```
Expose my nginx service via HTTPS with auto TLS in namespace shalb-demo.
```

The agent should generate correct Kube-DC manifests with proper CRD schemas, annotations, and namespace patterns — without any manual correction.

---

## Step 0: Get Your Kube-DC Kubeconfig

All integrations below require a valid kubeconfig pointing at your Kube-DC cluster.

1. Log in to the Kube-DC console
2. Click **Get CLI Access** in the dashboard
3. Follow the displayed commands to download your kubeconfig

Your kubeconfig will be saved at `~/.kube/config` by default. Verify it works:

```bash
kubectl get namespaces
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
> Show me all pods that are not running in namespace shalb-demo
> Why is the nginx deployment failing?
> Apply this deployment manifest and wait for it to be ready
> Get the logs from the last crashed container in pod my-app-xxx
> Scale the api-server deployment to 3 replicas
```

Claude Code can chain multiple kubectl operations automatically — for example, if a pod is crashing, it will fetch events, logs, and describe the pod in a single response.

### Use Kube-DC Agent Skills

Open the `kube-dc-public` repo in Claude Code. It contains:

- `CLAUDE.md` — loaded automatically, references `@_agent-instructions.md` and `@knowledge/index.md`
- `.claude/skills/` — 10 workflow-grouped skills (symlink to `.agents/skills/`)

With both MCP and skills, Claude Code can generate correct Kube-DC manifests **and** apply them directly. See [Agent Skills Setup](#agent-skills-setup) above.

### Non-destructive mode

For production clusters, run the MCP server in read-only mode to prevent accidental changes:

```bash
claude mcp add kubernetes -- npx mcp-server-kubernetes --non-destructive
```

In this mode the server can read, describe, get logs, and explain resources but cannot create, delete, patch, or scale.

---

## Cursor

[Cursor](https://www.cursor.com) is a VS Code fork built around AI pair programming. It supports MCP servers through its AI configuration.

### Configure the MCP server

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

### Usage

Open the Cursor chat (`Cmd+L` / `Ctrl+L`) and ask questions about your cluster:

```
@kubernetes list all pods in shalb-prod that have been restarting
@kubernetes describe the ingress for my-app and check if the service exists
@kubernetes what is the current resource usage vs quota in namespace shalb-demo?
```

Cursor can also generate and apply manifests directly from the chat, editing files and running `kubectl apply` in sequence.

### Tip: Use Kube-DC Agent Skills

Instead of a generic `.cursorrules` file, open the `kube-dc-public` repo in Cursor. It contains:

- `AGENTS.md` — Cursor reads this automatically for Kube-DC context
- `.cursor/rules/kube-dc-conventions/RULE.md` — always-on rules for namespace patterns, CRD naming, and safety constraints

This gives Cursor full awareness of Kube-DC CRDs, annotations, and naming conventions — far more comprehensive than a hand-written rules file. See [Agent Skills Setup](#agent-skills-setup) above.

---

## Windsurf

[Windsurf](https://windsurf.com) (by Codeium) is an AI-native IDE with a built-in agentic system called Cascade. It supports MCP servers natively through its settings.

### Configure the MCP server

Open Windsurf Settings → MCP Servers and add:

```json
{
  "mcpServers": {
    "kubernetes": {
      "command": "npx",
      "args": ["mcp-server-kubernetes"],
      "description": "Kube-DC cluster management"
    }
  }
}
```

Alternatively, edit `~/.codeium/windsurf/mcp_config.json` directly.

### Usage

Cascade (Windsurf's AI agent) can use the Kubernetes MCP automatically when you describe infrastructure tasks. In the Cascade panel:

```
Deploy the app from this Dockerfile to my kube-dc cluster in the staging namespace.
Check if all pods in acme-corp-prod are healthy and summarize any issues.
```

Cascade can chain file edits, terminal commands, and MCP tool calls in a single flow — writing the Deployment YAML, applying it, and monitoring rollout status.

### Tip: Use Kube-DC Agent Skills + Workflows

Instead of a generic `WINDSURF.md` file, open the `kube-dc-public` repo in Windsurf. It contains:

- `AGENTS.md` — loaded automatically for Kube-DC context
- `.windsurf/rules/kube-dc-conventions.md` — always-on safety rules
- `.windsurf/skills/` — 10 workflow-grouped skills (symlink to `.agents/skills/`)
- `.windsurf/workflows/` — slash commands:
  - `/deploy-wordpress` — Deploy WordPress with managed PostgreSQL, HTTPS, and auto TLS
  - `/setup-project` — Create a new project with organization verification and optional resources

This gives Cascade full awareness of Kube-DC CRDs, annotations, naming conventions, and step-by-step procedures. See [Agent Skills Setup](#agent-skills-setup) above.

---

## VS Code

VS Code supports Kubernetes cluster management via both dedicated extensions and MCP through GitHub Copilot.

### Essential Extensions

**[Kubernetes](https://marketplace.visualstudio.com/items?itemName=ms-kubernetes-tools.vscode-kubernetes-tools)**  
The official Kubernetes extension provides a full cluster browser in the VS Code sidebar:
- Browse namespaces, pods, deployments, services, and more
- View and edit live resources
- Stream pod logs directly in the editor
- Port-forward to services with a single click
- Supports multiple kubeconfig contexts — switch between Kube-DC projects instantly

**[YAML](https://marketplace.visualstudio.com/items?itemName=redhat.vscode-yaml)**  
Provides schema validation and autocompletion for Kubernetes manifests. YAML files containing `apiVersion:` and `kind:` are automatically validated against Kubernetes schemas.

**[GitLens](https://marketplace.visualstudio.com/items?itemName=eamodio.gitlens)** *(optional)*  
Recommended for GitOps workflows where your manifests live in Git.

### MCP with VS Code + GitHub Copilot

VS Code + GitHub Copilot supports MCP via settings. Add to your VS Code `settings.json`:

```json
{
  "mcp": {
    "servers": {
      "kubernetes": {
        "command": "npx",
        "args": ["mcp-server-kubernetes"],
        "description": "Kubernetes cluster management"
      }
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

# Add to VS Code settings.json
{
  "mcp": {
    "servers": {
      "kubernetes": {
        "command": "kubernetes-mcp-server",
        "args": ["--read-only"]
      }
    }
  }
}
```

Recommended for production use due to its safety modes and single-binary deployment.

---

## MCP Server Capabilities

All Kubernetes MCP servers expose a common set of operations that AI tools can call:

| Operation | Example natural language prompt |
|-----------|--------------------------------|
| List resources | "Show all pods in namespace acme-prod" |
| Describe resource | "Describe the ingress my-app and check if the backend service exists" |
| Get logs | "Get logs from the last crashed container in pod api-xxx" |
| Apply manifest | "Apply this deployment YAML to the cluster" |
| Scale | "Scale the api deployment to 3 replicas" |
| Delete | "Delete all completed jobs in acme-dev" |
| Port forward | "Port forward port 8080 from the backend pod to my local machine" |
| Helm | "Install nginx-ingress using Helm in namespace ingress-nginx" |
| Diagnose | "Why is my pod in CrashLoopBackOff? Walk through logs, events, and describe" |
| Quota check | "What is the current CPU and memory usage vs quota for my organization?" |

---

## Practical Kube-DC Workflows

### Deploy WordPress with managed database

```
Deploy WordPress with a managed HA PostgreSQL database in project shalb-demo.
Expose it via HTTPS with auto TLS.
```

With Agent Skills loaded, the agent will: create a KdcDatabase, wait for it, deploy WordPress with correct `secretKeyRef` for the DB password, create a LoadBalancer service with `expose-route: https`, and report the auto-generated hostname.

### Create a VM with SSH access

```
Create an Ubuntu 24.04 VM called "dev-box" with 4 CPU cores and 8GB RAM
in namespace shalb-demo. I need to SSH into it from outside the cluster.
```

The agent will: create a DataVolume + VirtualMachine with `qemu-guest-agent`, create an EIP + LoadBalancer service for SSH, extract the SSH private key, and provide the connection command.

### Scale a managed Kubernetes cluster

```
Scale the "production" cluster's workers pool to 5 replicas in project shalb-prod.
Also show me how to access the cluster's kubeconfig.
```

The agent will: use `kubectl patch kdccluster` with `--type merge` (including all pools), extract the kubeconfig from the `{cluster}-cp-admin-kubeconfig` secret, and write it to a temp file.

### Expose a gRPC service

```
I have a gRPC service running on port 50051 in namespace shalb-demo.
Expose it externally with auto TLS.
```

The agent will use the correct Gateway Route annotations: `expose-route: https` + `route-port: "50051"`.

### Debug a failing deployment

```
The deployment my-api in namespace shalb-prod is not ready.
Check the pod events, describe the deployment, get the last 100 lines of logs,
and tell me what is wrong and how to fix it.
```

### Create an S3 bucket and connect it to an app

```
Create an S3 bucket called "uploads" in project shalb-demo.
Show me how to mount the credentials in my deployment.
```

The agent will: create an ObjectBucketClaim with the required `kube-dc.com/organization` label, and show the `envFrom` pattern for mounting the auto-created Secret and ConfigMap.

---

## Security Considerations

- **Use read-only mode** (`--non-destructive` or `--read-only`) for production clusters when only inspection is needed
- **Use a dedicated ServiceAccount** with minimal RBAC instead of a cluster-admin kubeconfig when sharing MCP access with a team
- **Never commit kubeconfig files** to Git repositories
- **Scope by namespace** — your Kube-DC kubeconfig is already scoped to your organization's projects
- The Kube-DC kubeconfig uses short-lived tokens; re-download it from the console if the AI reports authentication errors

---

## Further Reading

- [kubectl-ai by Google Cloud](https://github.com/GoogleCloudPlatform/kubectl-ai) — AI-powered kubectl with natural language to command translation
- [kubectl-mcp-server](https://github.com/rohitg00/kubectl-mcp-server) — MCP server with natural language to kubectl, supports Gemini, Claude, Cursor, Windsurf, Copilot
- [mcp-server-kubernetes](https://github.com/Flux159/mcp-server-kubernetes) — Full-featured npm MCP server
- [containers/kubernetes-mcp-server](https://github.com/containers/kubernetes-mcp-server) — Red Hat single-binary MCP server
- [GitOps with Kube-DC](gitops.md) — Managing cluster state declaratively via Git
- [CLI & Kubeconfig](cli-kubeconfig.md) — Setting up kubectl for Kube-DC
