# Kube-DC RBAC Roles

## Standard Roles (Auto-Created Per Project)

| Role | Description | Typical Use |
|------|-------------|-------------|
| `admin` | Full admin access to all project resources | Team leads, project owners |
| `developer` | CRUD on VMs, workloads, databases, networking | Developers, DevOps |
| `project-manager` | Read-only access + console/VNC for VMs | Managers, stakeholders |
| `user` | Basic read access | Observers, auditors |

## How Roles Work

1. When a **Project** is created, standard Kubernetes `Role` resources are auto-created in the project namespace
2. When an **OrganizationGroup** references a role, a `RoleBinding` is created linking the Keycloak group to the Role
3. Keycloak SSO tokens include group memberships, which Kubernetes maps to RBAC permissions

## Custom Roles

You can create custom Kubernetes `Role` resources in a project namespace and reference them from OrganizationGroup:

```yaml
# Step 1: Create custom role in project namespace
apiVersion: rbac.authorization.k8s.io/v1
kind: Role
metadata:
  name: vm-operator
  namespace: {org}-{project}
rules:
  - apiGroups: ["kubevirt.io"]
    resources: ["virtualmachines", "virtualmachineinstances"]
    verbs: ["get", "list", "watch", "create", "update", "patch", "delete"]
  - apiGroups: ["cdi.kubevirt.io"]
    resources: ["datavolumes"]
    verbs: ["get", "list", "watch", "create", "delete"]
---
# Step 2: Reference from OrganizationGroup
apiVersion: kube-dc.com/v1
kind: OrganizationGroup
metadata:
  name: vm-team
  namespace: {org}
spec:
  permissions:
    - project: {project}
      roles:
        - vm-operator    # References the custom Role above
```

## Permission Inheritance

- Organization admin → access to all projects (via Keycloak realm admin)
- OrganizationGroup → scoped to specific projects + roles
- Direct RoleBinding → Kubernetes-native, bypasses OrganizationGroup (not recommended)
