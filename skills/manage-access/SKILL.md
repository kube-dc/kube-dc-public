---
name: manage-access
description: Manage Kube-DC Organization Groups and Project RBAC. Use OrganizationGroup resources to give teams standard or custom roles in selected Projects; manage individual users in the console.
---

# Manage Project Access

Identity and workload authorization are separate:

- An **Organization** owns its identity realm, members, billing, and shared quota.
- An **Organization Group** maps a team to roles in selected Projects.
- A **Project role** authorizes operations in that Project's backing namespace.
- Individual users and group membership are managed in the Kube-DC console.
  There is no Kube-DC `User` CRD.

## Standard Project roles

Use these exact names:

| Role | Intended access |
|---|---|
| `admin` | Broad lifecycle access to supported Project resources and namespaced RBAC; controller-owned quota remains read-only |
| `developer` | Manage supported workloads, volumes, Services, VMs, and managed services; no RBAC management |
| `project-manager` | Read and monitor Project resources, use the VM console, and perform selected managed-security updates |
| `user` | Read-only Project visibility without raw Secret or VM-console access |

No standard role grants pod exec, attach, port-forward, VM/VMI port-forward, or
NetworkPolicy management. Platform admission also blocks pod exec and attach
in Project backing namespaces, so a custom Role is not a bypass.

## Create an Organization Group

The resource belongs in the Organization API namespace. Each `project` value is
the Project name, not its backing namespace.

```yaml
apiVersion: kube-dc.com/v1
kind: OrganizationGroup
metadata:
  name: application-team
  namespace: "{organization}"
spec:
  permissions:
    - project: production
      roles:
        - developer
    - project: staging
      roles:
        - admin
```

Apply [org-group-template.yaml](org-group-template.yaml):

```bash
kubectl -n {organization} get project production staging
kubectl apply -f organization-group.yaml
```

The controller creates the matching Keycloak group and a RoleBinding for every
Project/role pair. Membership changes affect newly issued identity tokens;
sign out and back in when a change must take effect immediately.

## Verify access reconciliation

`OrganizationGroup.status` currently has no readiness conditions. Verify the
declared resource and generated RoleBindings instead:

```bash
kubectl -n {organization} get organizationgroup application-team -o yaml

kubectl -n {organization}-production get rolebinding -l group=application-team -o custom-columns='BINDING:.metadata.name,ROLE:.roleRef.name,SUBJECT:.subjects[0].name'

kubectl -n {organization}-staging get rolebinding -l group=application-team -o custom-columns='BINDING:.metadata.name,ROLE:.roleRef.name,SUBJECT:.subjects[0].name'
```

A standard binding is named `{role}-{group}` and its subject is the identity
group `{organization}:{group}`.

## Update or remove access

Edit `spec.permissions` declaratively:

```bash
kubectl -n {organization} edit organizationgroup application-team
```

The controller adds, updates, and removes generated RoleBindings. Deleting the
OrganizationGroup removes its Keycloak group and its generated RoleBindings:

```bash
kubectl -n {organization} delete organizationgroup application-team
```

## Custom roles

Use a custom namespaced Kubernetes `Role` when the four standard roles are too
broad. Create the Role in every target Project's backing namespace before
referencing its name in `OrganizationGroup.spec.permissions[].roles`. See
[rbac-roles.md](rbac-roles.md) for an example.

Custom roles remain subject to Kube-DC admission policy and cannot grant
cluster-scoped access.

## User management

Organization administrators use **Manage Organization > Users** in the console
to create or remove users, approve join requests, and assign groups. Do not try
to model these operations with Kubernetes User resources.

## Troubleshooting

If a group does not grant access:

```bash
kubectl -n {organization} get organizationgroup application-team -o yaml
kubectl -n {organization}-production get rolebinding -l group=application-team -o yaml
kubectl -n {organization}-production get role developer
```

Check that the Project names and role names match exactly. The controller skips
a referenced Project that does not exist. After fixing the resource, sign out
and sign in again so the user's token contains current group claims.
