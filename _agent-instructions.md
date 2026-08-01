# Kube-DC AI Agent Instructions

Use [AGENTS.md](AGENTS.md) as the canonical Kube-DC product model, safety
rules, resource scope, and workflow reference for AI agents.

The essential hierarchy is **Organization -> Project -> resources**. A
Project's `{organization}-{project}` Kubernetes namespace is its backing
namespace, not a separate user-facing service. A Managed Cluster is
a distinct Kubernetes API and control plane created inside a Project.

Before changing infrastructure, select the intended Project, verify the active
context and backing namespace, check quota, and use the relevant file under
`skills/`. Never infer provider-specific domains, plans, versions, address
pools, or StorageClasses when live configuration is available.
