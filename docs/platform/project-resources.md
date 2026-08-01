# Project Lifecycle and Resources

A Kube-DC **Project** is the governed workload boundary inside an
**Organization**. The Project controller materializes the Kubernetes,
networking, identity, security, and optional storage resources that make the
workspace usable.

This page describes the stable operational contract. Individual generated
objects can change between releases; Project status and controller events
are the authoritative view of reconciliation health.

## Project API

Create the `Project` in its Organization namespace:

```yaml
apiVersion: kube-dc.com/v1
kind: Project
metadata:
  name: production
  namespace: acme
spec:
  cidrBlock: 10.40.0.0/20
  egressNetworkType: cloud
```

| Field | Meaning |
|---|---|
| `spec.cidrBlock` | Required workload subnet for the Project VPC. It must not overlap networks the workloads need to reach. |
| `spec.egressNetworkType` | Required, immutable external network selection: `cloud` or `public`. |
| `spec.gwLanIp` | Optional Project gateway address inside `cidrBlock`; normally omitted. |

`public` Projects are accepted only when the operator has configured a public
external network and enabled `allow_public_projects`. Fresh installations keep
that gate disabled by default.

The generated backing namespace is `{organization}-{project}`. For the example
above it is `acme-production`.

## What reconciliation provides

### Workspace and quota hierarchy

- the backing namespace;
- an HNC hierarchy link to the Organization namespace;
- Project quota and security status aggregation.

The hierarchy lets shared Organization quota and selected policies apply across
its Projects. It does not turn arbitrary namespaces into Projects.

### Networking

- a dedicated Kube-OVN VPC and workload subnet;
- a `default` NetworkAttachmentDefinition in the backing namespace;
- a default `EIp` and SNAT path on the selected external network;
- VPC DNS resources;
- ingress and egress router policies when network isolation is enabled;
- logical-router-port EIPs needed by the current Kube-OVN topology.

`cloud` selects the private cloud external network. `public` selects the public
external network; it does not bypass firewall, quota, or exposure controls.

### Identity and RBAC

- the Keycloak client role used for Project access;
- Kubernetes Roles named `admin`, `project-manager`, `developer`, and `user`;
- RoleBindings for the Organization's built-in `org-admin` and `user` groups;
- additional RoleBindings derived from Organization Groups.

The Roles are standard Kubernetes Roles copied from platform templates. Their
effective rules can be inspected in the backing namespace.

### Access and trust material

- `ssh-keypair-default` and `authorized-keys-default` Secrets for VM access;
- a trusted-CA ConfigMap, populated when the platform has a private trust
  bundle;
- OpenBao references and External Secrets resources in the backing namespace
  when OpenBao is enabled.

### Optional services

When their platform dependencies are installed, reconciliation can also
prepare:

- the `managed-k8s-backups` ObjectBucketClaim for Managed Cluster etcd backups;
- golden image snapshots for fast VM cloning on the supported RBD storage path;
- security-service state for managed secrets, certificates, and KMS keys.

The absence of an optional dependency should not make the base Project
unusable. Inspect conditions on the relevant service resource for its own
readiness.

## Readiness

```bash
kubectl -n <organization> get project <project>
kubectl -n <organization> get project <project> -o yaml
kubectl -n <backing-namespace> get events --sort-by=.lastTimestamp
```

`status.namespace` reports the generated backing namespace.
`status.ready: true` means the core Project resources reconciled. Service-level
status, such as a Managed Cluster backup bucket or a managed certificate, must
still be checked on that resource.

An Organization can limit how many Projects reconcile. The value is configured
through `MasterConfig.OrganizationProjectsLimit`; when unset, the current
controller default is 50. A Project blocked by the limit remains present and
reports a `LimitCheck` condition until capacity becomes available.

## Deletion behavior

Deleting a Project is a coordinated teardown, not only a namespace deletion.
The controller:

1. removes cluster-scoped golden snapshot content owned by the Project;
2. drains external-state workloads such as Managed Clusters and object buckets;
3. removes public egress and default SNAT state;
4. releases external addresses and VPC DNS resources;
5. deletes the backing namespace so namespaced workloads and addresses drain;
6. removes the Kube-OVN subnet and VPC;
7. cleans up per-Project identity and security-service state.

Finalizers protect this order. A Project can remain `Terminating` while a
dependent controller releases infrastructure.

## Troubleshooting deletion

Start with read-only inspection:

```bash
kubectl -n <organization> describe project <project>
kubectl -n <backing-namespace> get kdccluster,obc,eip,fip -o wide
kubectl get subnet,vpc,ovneip,ovnsnatrule -o wide
kubectl -n kube-dc logs deployment/kube-dc-manager --since=30m
```

Resolve the dependent resource or controller error first. Removing finalizers
by hand can orphan addresses, storage, OpenBao material, or Kube-OVN state and
is not a normal recovery procedure. Escalate a persistent deletion to the
platform operator with the Project YAML, related events, and controller logs.

## Related documentation

- [Multi-tenancy and access control](architecture-multi-tenancy.md)
- [Networking architecture](architecture-networking.md)
- [Security model](security-model.md)
