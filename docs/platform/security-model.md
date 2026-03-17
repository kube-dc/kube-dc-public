# Security Model

Kube-DC enforces a multi-layered security model across all project namespaces. This page describes the security enforcement mechanisms, how to manage exemptions, and how to configure per-project exceptions.

## Overview

The platform uses Kubernetes-native admission policies to enforce security boundaries in project namespaces. These policies run in-process in the API server — no external dependencies are required.

| Layer | What it protects | Scope |
|-------|-----------------|-------|
| **Pod Security** | Blocks dangerous pod specs (privileged, hostPath, hostNetwork, etc.) | All project namespaces |
| **Annotation Protection** | Prevents users from modifying system annotations on Organizations, Projects, and OrganizationGroups | Organization and project namespaces |
| **Network Isolation** | Prevents cross-project traffic at the SDN level | Per-project VPC |
| **Egress Isolation** | Restricts outbound traffic to project's own external IPs | Per-project VPC |
| **RBAC** | Role-based access control with org-level and project-level roles | Per-namespace |

## Pod Security Enforcement

All project namespaces are automatically labeled for security policy enforcement. The following pod specifications are **blocked for all user-created pods**:

| Blocked Feature | Risk | Alternative |
|----------------|------|-------------|
| `hostPath` volumes | Direct access to node filesystem | Use `PersistentVolumeClaim`, `ConfigMap`, `Secret`, or `emptyDir` |
| `privileged` containers | Full node-level access | Run containers with minimal capabilities |
| `hostNetwork` | Access to node's network stack | Use standard pod networking or `Service` resources |
| `hostPID` | Visibility into all node processes | Use standard process isolation |
| `hostIPC` | Access to node's IPC namespace | Use standard IPC isolation |

### System Workloads

System controllers that manage infrastructure within project namespaces (e.g., virtual machine launchers, cloud controller managers, control plane components) are **automatically exempted**. These workloads are created by trusted system service accounts and are not subject to the pod security restrictions.

No manual exemption configuration is required for system workloads.

### What Users Can Still Do

- Create standard pods, deployments, statefulsets, daemonsets, jobs
- Use PersistentVolumeClaims for storage
- Mount ConfigMaps and Secrets
- Use emptyDir volumes
- Access pod logs via `kubectl logs`
- Create and manage services, ingresses, and network policies

## Annotation Protection

System annotations on **Project**, **Organization**, and **OrganizationGroup** resources are protected from user modification. This prevents users from:

- Modifying billing metadata on Organizations
- Changing network isolation settings on Projects
- Tampering with controller-managed annotations

### What Users Can Still Do

- Create and delete Projects (org-admins)
- Update Project and OrganizationGroup spec fields
- Manage OrganizationGroup permissions

### Modifying Annotations as Administrator

Cluster administrators (`system:masters` group) retain full access to modify annotations. To set a per-project annotation:

```bash
kubectl annotate project <project-name> -n <org-namespace> \
  <annotation-key>="<value>"
```

For example, to configure a per-project egress allowlist:

```bash
kubectl annotate project my-project -n my-org \
  network.kube-dc.com/egress-allowlist="100.65.0.200,10.8.0.0/24"
```

## Egress Network Isolation

When enabled, egress network isolation restricts outbound traffic from each project's VPC. Only traffic to the project's own external IPs (EIPs) is allowed; all other external traffic is blocked at the SDN level.

### Allow Rule Sources

| Source | Scope | How to configure |
|--------|-------|-----------------|
| **Auto-collected EIPs** | Per-project (automatic) | No action needed — the controller discovers all EIPs in the project |
| **Global allowlist** | All projects | Set `egress_global_allowlist` in the `master-config` Secret |
| **Per-project allowlist** | Single project | Set `network.kube-dc.com/egress-allowlist` annotation on the Project |

### Configuring the Global Allowlist

The global allowlist applies to all projects. Add IP addresses or CIDRs to the `master-config` Secret:

```json
{
  "egress_network_isolation": true,
  "egress_global_allowlist": ["100.65.0.200", "10.8.0.0/24"]
}
```

### Configuring a Per-Project Allowlist

To allow a specific project to reach additional external IPs:

```bash
kubectl annotate project my-project -n my-org \
  network.kube-dc.com/egress-allowlist="100.65.0.200,10.8.0.0/24" \
  --overwrite
```

Changes take effect on the next controller reconcile (typically within seconds).

:::warning
Per-project allowlist annotations are protected — only cluster administrators can set or modify them. This prevents users from bypassing network isolation by whitelisting arbitrary external IPs.
:::

## RBAC Model

Kube-DC uses a hierarchical RBAC model with organization-level and project-level roles. See [User and Group Management](../cloud/team-management.md) for the full RBAC reference.

### Key Security Properties

- **No `pods/exec` or `pods/attach`** — None of the standard roles grant shell access to running pods. This prevents users from accessing secrets or credentials inside containers at runtime.
- **RBAC write is admin-only** — Only the `admin` project role can create or modify Roles and RoleBindings. Developer, project-manager, and user roles cannot escalate their own permissions.
- **Namespace isolation** — Users cannot modify namespace resources (labels, annotations). Namespace lifecycle is managed exclusively by the platform controller.
- **No ClusterRole access** — Users can only create namespace-scoped Roles, not ClusterRoles. Self-escalation is limited to the user's own project namespace.

## Security Policy Lifecycle

### Adding a New System Namespace

If a new system controller is deployed that creates pods in project namespaces, its service account namespace must be added to the pod security policy exemptions. Update the exemption list in the `vap-pod-security.yaml` Helm chart template and redeploy.

### Disabling Pod Security Enforcement

To temporarily disable pod security enforcement for debugging:

```bash
kubectl delete validatingadmissionpolicybinding restrict-pod-security-in-projects
```

To re-enable:

```bash
helm upgrade kube-dc charts/kube-dc -n kube-dc
```

:::danger
Disabling pod security enforcement allows users to create privileged pods with full node access. Only do this temporarily for debugging and re-enable immediately after.
:::

### Monitoring Policy Denials

Policy denials are logged in the API server audit log. Look for `ValidatingAdmissionPolicy` entries with `deny` action to identify blocked requests and the reason for denial.
