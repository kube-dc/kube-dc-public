# Project lifecycle and resources

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

The Project spec has these fields:

| Field | Meaning |
|---|---|
| `spec.cidrBlock` | Optional workload subnet for the Project VPC. When omitted, the controller allocates one from the Project CIDR pool. An explicit value must not overlap networks the workloads need to reach. |
| `spec.egressNetworkType` | Required, immutable external network selection: `cloud` or `public`. |
| `spec.gwLanIp` | Optional Project gateway address inside `cidrBlock`; normally omitted. |
| `spec.quota.hard` | Optional cap for this Project inside the Organization's shared quota. It uses ResourceQuota names, such as `requests.cpu`, `requests.memory`, `requests.storage`, and `pods`, or an accelerator dimension of the Organization quota. Admission refuses a dimension the Organization quota does not have, or a value above it. The controller writes the Project's ResourceQuota from it only when `projectQuota.source` is `spec`; otherwise the field is ignored. |
| `spec.network.loadBalancerBlocks` | Optional number (0 to 64) of 16-address ranges to pre-allocate for Managed Cluster LoadBalancer pools. The value can only grow. |

`public` Projects are accepted only when the operator has configured a public
external network and enabled `allow_public_projects`. Fresh installations keep
that gate disabled by default.

The generated backing namespace is `<organization>-<project>`. For the example
it is `acme-production`.

## Project CIDR allocation

When a Project has no `spec.cidrBlock`, the Project controller picks the first
free block of the Project CIDR pool, in address order. A block is free when it
overlaps no other Project's `spec.cidrBlock` and no reserved range. The
controller writes the block to `spec.cidrBlock` before it builds the network. It
never changes an explicit `spec.cidrBlock`.

Configure the pool with the `backend.projectCidr` chart values. The manager and
the console backend read the same values:

| Value | Environment variable | Default | Meaning |
|---|---|---|---|
| `backend.projectCidr.pool` | `PROJECT_CIDR_POOL` | `10.0.0.0/8` | IPv4 supernet that Project blocks come from. |
| `backend.projectCidr.prefix` | `PROJECT_CIDR_PREFIX` | `22` | Prefix length of each Project block. `22` gives 1024 addresses. |
| `backend.projectCidr.reserved` | `PROJECT_CIDR_RESERVED` | `10.100.0.0/16`, `10.101.0.0/16` | Ranges the allocator never uses. The defaults are the system pod network (`ovn-default`) and the Service CIDR. |

Add your underlay networks and any other routed networks to `reserved`.

If the controller cannot allocate a block, it sets the Project condition
`CIDRAllocated` to `False` with one of these reasons:

| Reason | Meaning | Action |
|---|---|---|
| `InvalidPoolConfig` | `PROJECT_CIDR_PREFIX` is not an integer. | Fix `backend.projectCidr.prefix`. |
| `AllocationFailed` | The pool is not an IPv4 prefix, the prefix length is outside the pool, or a claimed or reserved CIDR cannot be parsed. | Fix the pool values, or the Project with the unparseable `spec.cidrBlock`. |
| `PoolExhausted` | No free block of the configured size remains in the pool. | Set `spec.cidrBlock` on the Project, or widen the pool. |

To read the condition, run this command:

```bash
kubectl -n <organization> get project <project> \
  -o jsonpath='{.status.conditions[?(@.type=="CIDRAllocated")]}'
```

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

- the `managed-k8s-backups` ObjectBucketClaim for Managed Cluster etcd backups.
  New bucket names are bounded to 63 characters; long namespace names use a
  truncated prefix and namespace hash. Existing buckets keep their names;
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

To delete a Project in the console, open **Organization > Projects**, select
its name, and select **Delete project**. Enter the exact Project name to confirm.
You need Organization administrator access. Deletion permanently removes the
Project's workloads, storage, networking, and access.

The console confirms the displayed Project UID. If another Project replaces it
under the same name, deletion returns `409`; refresh and review that Project
before confirming again. The console shows **Deleting** while cleanup runs and
stops requesting namespace quotas and members. A Project that is being deleted
returns `409` from the quota API; this does not grant any additional access.

Deleting a Project is a coordinated teardown. The controller first removes its
permissions from Organization Groups, preserving each group and its permissions
for other Projects. The controller:

1. removes cluster-scoped golden snapshot content owned by the Project;
2. drains external-state workloads such as Managed Clusters and object buckets;
3. removes public egress and default SNAT state;
4. releases external addresses and VPC DNS resources;
5. deletes the backing namespace so namespaced workloads and addresses drain;
6. normally deletes retained Pod IP allocations after the namespace disappears,
   including StatefulSet or VM allocations on the infrastructure network;
7. waits for Kube-OVN to release those allocations, then removes the subnet and VPC;
8. cleans up per-Project identity and security-service state.

Finalizers protect this order. A Project can remain `Terminating` while a
dependent controller releases infrastructure. When a resource disappears,
its completed deletion clears any earlier timeout condition. A timeout for an
external IP can therefore be historical; inspect the latest Project conditions
and the remaining resources to find the active blocker.

## Troubleshoot deletion

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
