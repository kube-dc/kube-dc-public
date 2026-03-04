# AI IDE Integration

:::caution Work in Progress
This page is actively being expanded. Current content covers MCP server setup and core workflows. Sections on advanced multi-cluster setups and Windsurf-specific features are still being written.
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

### Tip: Add a `.cursorrules` file

Add a `.cursorrules` file to your project to give Cursor context about your Kube-DC environment:

```
This project deploys to Kube-DC Cloud (kube-dc.com).
Organization: acme-corp
Projects are deployed in namespaces following the pattern: {org}-{project}
Example namespaces: acme-corp-dev, acme-corp-staging, acme-corp-prod
Resource quotas are enforced at the organization level.
Use the kubeconfig at ~/.kube/config for all kubectl operations.
```

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

### Tip: Add a `WINDSURF.md` file

Create a `WINDSURF.md` in your project root with cluster-specific context Cascade should always be aware of:

```markdown
## Kube-DC Cluster Context
- Cluster: kube-dc.cloud (kubeconfig at ~/.kube/config)
- Org namespace pattern: {org}-{project}
- Storage class: local-path
- External IP annotation: service.nlb.kube-dc.com/bind-on-default-gw-eip: "true"
- Default image registry: registry.kube-dc.cloud
```

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

### Deploying an application

```
Deploy a Nginx deployment with 2 replicas to namespace acme-corp-demo.
Use resource requests of 100m CPU and 128Mi memory to stay within quota.
Expose it via a ClusterIP service on port 80.
```

### Debugging a failing deployment

```
The deployment my-api in namespace acme-corp-prod is not ready.
Check the pod events, describe the deployment, get the last 100 lines of logs,
and tell me what is wrong and how to fix it.
```

### Quota-aware manifest generation

```
Generate a Kubernetes Deployment for a Node.js app.
The organization is on the Pro Pool plan (8 vCPU / 24 GB total).
I want to allocate 2 vCPU and 4 GB to this service.
Include appropriate resource requests and limits.
```

### Checking organization resource usage

```
Show me the current resource quota usage for namespace acme-corp.
Which projects are consuming the most CPU and memory?
Is there headroom to deploy another 2-replica deployment?
```

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
