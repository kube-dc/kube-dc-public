# Project RBAC Roles

Kube-DC creates four standard Kubernetes `Role` objects in every Project's
backing namespace.

| Role | Intended use | Important boundaries |
|---|---|---|
| `admin` | Project administration | Manages supported namespaced resources and namespaced RBAC; not controller-owned quota or cluster scope |
| `developer` | Workload delivery | Manages workloads, VMs, volumes, Services, and managed services; no RBAC management |
| `project-manager` | Operations and oversight | Reads and monitors resources, uses the VM console, and updates selected managed-security resources |
| `user` | Read-only visibility | No raw Secret access or VM console |

The exact Kubernetes rules installed on a cluster are authoritative:

```bash
kubectl -n {project-backing-namespace} get role admin developer project-manager user
kubectl -n {project-backing-namespace} get role developer -o yaml
```

No standard role grants pod exec, attach, port-forward, VM/VMI port-forward, or
NetworkPolicy management. Project admission blocks pod exec and attach even if
a custom Role attempts to grant those subresources.

## How Organization Groups use roles

For each Project/role pair in an `OrganizationGroup`, the controller creates a
RoleBinding in that Project's backing namespace:

- binding name: `{role}-{organization-group}`
- role reference: the requested namespaced `Role`
- group subject: `{organization}:{organization-group}`

The Project controller separately binds the Organization's built-in
`org-admin` group to `admin` and its `user` group to `user` in every Project.

## Custom role example

A Project `admin` can define a narrower Role:

```yaml
apiVersion: rbac.authorization.k8s.io/v1
kind: Role
metadata:
  name: release-reader
  namespace: "{project-backing-namespace}"
rules:
  - apiGroups: ["apps"]
    resources: ["deployments", "replicasets"]
    verbs: ["get", "list", "watch"]
  - apiGroups: [""]
    resources: ["pods", "pods/log", "services", "configmaps"]
    verbs: ["get", "list", "watch"]
```

Reference it by name from the Organization API namespace:

```yaml
apiVersion: kube-dc.com/v1
kind: OrganizationGroup
metadata:
  name: release-auditors
  namespace: "{organization}"
spec:
  permissions:
    - project: production
      roles:
        - release-reader
```

Kubernetes Roles are namespace-scoped. Create the custom Role separately in
every Project where it is used. Prefer Organization Groups over unmanaged
direct RoleBindings so identity-group lifecycle remains declarative.
