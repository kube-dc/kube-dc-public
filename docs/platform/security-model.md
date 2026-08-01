# Security Model

Kube-DC combines identity, Kubernetes authorization, admission policy, and
Kube-OVN routing controls. No single layer is a complete tenant boundary, and
platform administrators remain privileged across Organizations.

This page describes the controls applied to **Projects** on the management
cluster. A **Managed Cluster** has its own Kubernetes API, RBAC, and workload
security configuration.

GPU workloads add privileged node components and deployment-specific isolation
assumptions. See the [GPU security threat model](gpu-threat-model.md) before
enabling a GPU profile.

## Control layers

| Layer | Protects | Scope |
|---|---|---|
| OIDC and Keycloak | User identity and group claims | Platform and Organization realms |
| Kubernetes RBAC | Which resources a user can read or change | Organization namespaces and Project backing namespaces |
| ValidatingAdmissionPolicy | Dangerous Pod fields, exec/attach, protected annotations, quota, and selected accelerator rules | Resources selected by each policy or binding |
| Kube-OVN VPCs | Primary Project network separation | One VPC and workload subnet per Project |
| Router-policy isolation | Traffic on shared cloud/public external networks | Project VPCs when ingress or egress isolation is enabled |
| Workload policy | Application-specific traffic rules | Optional operator-provided Kubernetes NetworkPolicy; default Project Roles cannot author it |

## Pod admission

Project backing namespaces carry the `kube-dc.com/project` label. The
`restrict-pod-security-in-projects` policy denies user-created or user-updated
Pods that request:

- `hostPath` volumes;
- privileged containers or init containers;
- `hostNetwork`;
- `hostPID`;
- `hostIPC`.

Trusted platform service-account namespaces, node identities, and
`system:masters` are excluded because controllers such as KubeVirt and Kamaji
must create infrastructure Pods in Project backing namespaces. Those exclusions are
part of the platform trust boundary; they are not a user-configurable bypass.

Users can still run ordinary Pods, Deployments, StatefulSets, Jobs, and
DaemonSets, mount supported volume sources, and expose workloads through
Services or Gateway routes.

## Exec and attach

Pod exec and attach are unsupported in Projects. Current standard Role
templates omit `pods/exec` and `pods/attach`, and
`restrict-pod-exec-in-projects` denies CONNECT requests even if a custom or
stale Role grants the subresource.

Run administrative container tasks as a purpose-built Job that mounts the
required volume. Use the VM console or VNC for virtual machines. Platform
service accounts, nodes, and `system:masters` are exempt and must be protected
accordingly.

## Protected annotations

The `protect-kube-dc-resource-annotations` policy prevents non-platform users
from changing annotations on `Organization`, `Project`, and
`OrganizationGroup` resources during UPDATE. Users can still perform the
specification changes allowed by their RBAC.

Cluster administrators can set controller-consumed annotations. For example:

```bash
kubectl -n <organization> annotate project <project> \
  network.kube-dc.com/egress-allowlist="10.8.0.0/24" \
  --overwrite
```

Treat annotations as privileged configuration: admission protects changes, but
does not validate the business reason for an allowlist entry.

## External-network isolation

Project VPC separation is the primary east-west boundary. Optional ingress and
egress router policies add protection when multiple tenants share an external
cloud or public subnet.

### Egress isolation

When `egress_network_isolation` is enabled, Kube-DC drops Project traffic whose
**destination** is another address on a configured external subnet, except for:

- the external subnet gateway, which is needed for SNAT and internet access;
- external addresses owned by the Project;
- the global egress allowlist;
- the Project's `network.kube-dc.com/egress-allowlist` entries.

This control does **not** block all outbound traffic. Internet destinations do
not match the external-subnet drop rule and continue through the Project's SNAT
path.

### Ingress isolation

When `ingress_network_isolation` is enabled, Kube-DC drops traffic whose
**source** is a configured external subnet, except for:

- the external subnet gateway, needed for return traffic;
- the global ingress allowlist;
- the Project's `network.kube-dc.com/ingress-allowlist` entries.

Ingress allowlists describe trusted source addresses. Unlike egress rules, they
are not populated from Project EIPs automatically.

### Platform configuration

The master configuration holds cluster-wide switches and allowlists:

```json
{
  "egress_network_isolation": true,
  "egress_global_allowlist": ["10.8.0.0/24"],
  "ingress_network_isolation": true,
  "ingress_global_allowlist": ["192.0.2.10"]
}
```

Project-specific values are comma-separated IP addresses or CIDRs:

```bash
kubectl -n <organization> annotate project <project> \
  network.kube-dc.com/ingress-allowlist="192.0.2.10,198.51.100.0/28" \
  network.kube-dc.com/egress-allowlist="10.8.0.0/24" \
  --overwrite
```

Changes apply when the Project reconciles. Validate the resulting behavior from
both directions; an allowlist does not replace application authentication,
TLS, or a workload NetworkPolicy.

## Project RBAC

Kube-DC creates four standard Roles in every Project backing namespace:

| Role | Intended access |
|---|---|
| `admin` | Broad lifecycle access to supported Project resources plus namespaced Roles and RoleBindings; quota is read-only |
| `project-manager` | Read and monitor Project resources and use the VM console; update or patch existing managed secrets, certificates, and database credential policies; create, update, or patch KMS keys; no Project, membership, or quota administration |
| `developer` | Manage supported workloads, Services, VMs, and managed services; raw Kubernetes Secrets are get/list only |
| `user` | Read-only Project visibility without raw Secret or VM-console access |

The exact rules come from the platform's default Role templates. Review live
rules rather than inferring privileges from the role name:

```bash
kubectl -n <backing-namespace> get role \
  admin developer project-manager user -o yaml
```

Namespaced RBAC does not grant access to cluster-scoped resources. Organization
Groups create RoleBindings only for the selected Projects. See
[Multi-tenancy and access control](architecture-multi-tenancy.md).

## Operating admission policy

Admission policy and bindings are installed through the Fleet GitOps path.
Inspect them with:

```bash
kubectl get validatingadmissionpolicy
kubectl get validatingadmissionpolicybinding
kubectl describe validatingadmissionpolicy restrict-pod-security-in-projects
```

Change exemptions or enforcement in the Fleet source, review the impact, and
let Flux reconcile it. Deleting a live binding creates an immediate enforcement
gap and Flux may recreate it; it is not a normal debugging procedure.

Policy denials appear in API responses and, when audit logging is enabled, in
the API-server audit log. Capture the denied request, user, policy name, and
message before changing policy.

## Boundaries and residual risk

- Platform controllers and `system:masters` can cross Project boundaries.
- Compromise of a trusted service account can bypass the admission exclusions
  granted to that account.
- Underlay VLAN attachments inherit the physical network's isolation model.
- Network isolation does not replace encryption, application authorization, or
  backup.
- A Project is not a separate Kubernetes cluster. Use a Managed Cluster when a
  tenant needs its own cluster-scoped administration boundary.
