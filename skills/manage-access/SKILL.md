---
name: manage-access
description: Manage Kube-DC organization groups and RBAC — create OrganizationGroup resources to map user groups to Kubernetes roles per project. Note that user management is UI-only (Keycloak).
---

## Prerequisites
- Organization must exist and be Ready
- Organization namespace: `{org}`

## Key Concepts

- **OrganizationGroup** — Maps a group of users to specific Kubernetes roles in specific projects
- **Users** — Managed exclusively via Kube-DC console UI (Keycloak). **No User CRD exists.** If asked to manage users, direct them to the UI.
- **Roles** — Standard roles auto-created per project: `admin`, `developer`, `project-manager`, `user`

## Create OrganizationGroup

```yaml
apiVersion: kube-dc.com/v1
kind: OrganizationGroup
metadata:
  name: {group-name}
  namespace: {org}              # MUST be in the organization namespace
spec:
  permissions:
    - project: {project-1}
      roles:
        - developer             # Full CRUD on VMs and workloads
    - project: {project-2}
      roles:
        - admin                 # Full admin access
    - project: {project-3}
      roles:
        - project-manager       # Read-only + console/VNC
```

See @org-group-template.yaml

## Standard Roles

| Role | Access Level |
|------|-------------|
| `admin` | Full admin access to the project |
| `developer` | CRUD on VMs, workloads, databases, networking |
| `project-manager` | Read-only + console/VNC access |
| `user` | Basic read access |

Custom roles can also be referenced if a Kubernetes `Role` exists in the target project namespace.

## Controller Lifecycle

1. **On create** → Keycloak group created + RoleBindings in each project namespace
2. **On update** → RoleBindings reconciled (added/removed as permissions change)
3. **On delete** → Keycloak group + all RoleBindings removed

## List Groups

```bash
kubectl get organizationgroup -n {org}
kubectl describe organizationgroup {group-name} -n {org}
```

## Update Permissions

Edit the OrganizationGroup to add/remove project-role mappings:

```bash
kubectl edit organizationgroup {group-name} -n {org}
```

## User Management (UI Only)

Users are managed via the Kube-DC console:

- **Create user** → Manage Organization → Users → Create User
- **Assign to groups** → Users → Assign Groups
- **Approve join requests** → Users → Pending Requests
- **Delete user** → Users tab in the UI

Agents CANNOT create, delete, or modify users via kubectl.

## Safety
- OrganizationGroups MUST be in the organization namespace, not a project namespace
- Never attempt to create or manage users via kubectl — direct to UI
- Validate that referenced projects exist before creating the group
- Validate that referenced roles exist in the target project namespace
