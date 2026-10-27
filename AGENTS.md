# Kube-DC AI Agent Instructions

Kube-DC is a Kubernetes-native cloud platform for governed application,
virtual-machine, data, and Managed Cluster workloads.

## Product Model

- An **Organization** owns identity, membership, billing, and shared quota.
- A **Project** is the governed workload boundary for a team or environment.
- Kubernetes stores a Project's resources in its **backing namespace**,
  `{organization}-{project}`. This name is an implementation detail, not a
  separate product object.
- A **Managed Cluster** is created inside a Project and has its own
  Kubernetes API, control plane, namespaces, CRDs, and cluster-scoped policy.

Use a Project for supported namespaced applications, VMs, databases, storage,
and platform services. Use a Managed Cluster when software needs CRDs,
operators, multiple namespaces, cluster-scoped RBAC, admission webhooks,
StorageClasses, NetworkPolicies, or privileged/host access.

## Resource Scope

| Resource | API group/version | Scope and purpose |
|---|---|---|
| `Organization` | `kube-dc.com/v1` | Organization namespace; identity, billing, quota |
| `OrganizationGroup` | `kube-dc.com/v1` | Organization namespace; maps groups to Project roles |
| `Project` | `kube-dc.com/v1` | Organization namespace; creates a governed workload boundary |
| `EIp`, `FIp` | `kube-dc.com/v1` | Project backing namespace; external/floating addresses |
| `KdcCluster` | `k8s.kube-dc.com/v1alpha1` | Project backing namespace; Managed Cluster |
| `ManagedService` | `services.kube-dc.com/v1alpha1` | Project backing namespace; a service from the provider's catalog, with a class and plan |
| `ServiceBinding` | `services.kube-dc.com/v1alpha1` | Project backing namespace; delivers one credential role of a service as a Secret |
| `ServiceOperation` | `services.kube-dc.com/v1alpha1` | Project backing namespace; one immutable day-2 action (backup, scale, rotate, upgrade) |
| `ServiceCredentialPolicy` | `services.kube-dc.com/v1alpha1` | Project backing namespace; scheduled credential rotation |
| `ServiceOffering` | `services.kube-dc.com/v1alpha1` | Project backing namespace; read-only copy of one plan offered to the Project (defaults, limits, `createExample`); only where the provider enables offerings |
| `ManagedSecret` | `security.kube-dc.com/v1alpha1` | Project backing namespace; OpenBao-backed secret intent |
| `ManagedCertificate` | `security.kube-dc.com/v1alpha1` | Project backing namespace; managed X.509 certificate |
| `KMSKey` | `security.kube-dc.com/v1alpha1` | Project backing namespace; OpenBao Transit key |
| `VirtualMachine` | `kubevirt.io/v1` | Project backing namespace; KubeVirt VM |
| `DataVolume` | `cdi.kubevirt.io/v1beta1` | Project backing namespace; VM disk |
| `ObjectBucketClaim` | `objectbucket.io/v1alpha1` | Project backing namespace; S3 bucket claim |

## Standard Project Roles

The exact role names are `admin`, `developer`, `project-manager`, and `user`.

| Role | Summary |
|---|---|
| `admin` | Manage supported Project resources and namespaced RBAC; quota objects remain read-only |
| `developer` | Manage supported workloads, volumes, Services, and managed services |
| `project-manager` | Read resources and use approved VM and managed-security operations |
| `user` | Read supported resources and logs |

No standard Project role grants pod exec, attach, port-forward, or
NetworkPolicy authoring. Pod exec and attach are also admission-blocked in
Project backing namespaces. Use Jobs for one-off application administration.

Users are managed in the Kube-DC console through Keycloak. There is no User
custom resource. Only Organization Admins manage members, Projects, billing,
shared quota, and Organization Groups.

## Safety and Naming

1. Create workload resources in the selected Project's backing namespace.
2. Before creating VMs, applications, databases, Managed Clusters, storage, or
   public addresses, inspect Organization and Project quota.
3. A VM's default Multus network is `{backing-namespace}/default`.
4. Install `qemu-guest-agent` in supported VM guests and use the Project's
   `authorized-keys-default` Secret for SSH key injection.
5. A public `FIp` creates and owns its required `EIp`; do not create both.
6. Do not make one VM/pod both a public FIP target and a cloud LoadBalancer
   backend.
7. HTTPS routes get a platform certificate; do not create an Issuer. Set
   `tls-issuer` only to use your own dns01 or CA Issuer.
8. Give manually created ObjectBucketClaims the
   `kube-dc.com/organization: {organization}` label so dashboard and
   Organization usage attribution remain correct.
9. Keep each `KdcCluster.spec.dataStore.port` unique within a Project.
10. Treat provider domains, pools, plans, versions, and StorageClasses as
    installation-specific. Read live configuration instead of inventing them.
11. Run databases, caches and queues as managed services (`create-database`),
    not in pods. Use the `{family}-development` plan unless the user asks for
    production or high availability.
12. Publish web traffic through the shared Gateway (a `ClusterIP` Service with
    `expose-route`), never a `LoadBalancer` with its own EIP. Use a public
    IPv4 (EIP, FIP, `LoadBalancer`) only for non-HTTP ports or when the user
    asks for a dedicated address, after checking quota.

## Service Exposure

### Hostname-based web traffic

Use a `ClusterIP` Service with a Gateway route for route-only HTTP, HTTPS, or
TLS passthrough. Existing Gateway LoadBalancer Services retain their direct EIP
binding when edited; restrictive lists on combined exposure require both methods
to be qualified:

```yaml
metadata:
  annotations:
    service.nlb.kube-dc.com/expose-route: "https" # http | https | tls-passthrough
    # service.nlb.kube-dc.com/route-hostname: "app.example.com"
    # service.nlb.kube-dc.com/route-port: "8080"
    # service.nlb.kube-dc.com/tls-issuer: "letsencrypt"
```

`https` creates an HTTPRoute and terminates TLS at the shared Gateway.
`tls-passthrough` creates a TLSRoute and requires the backend to present a
certificate valid for the public hostname. These annotations do not create a
GRPCRoute; validate HTTP/2/gRPC compatibility for the application or use a
dedicated LoadBalancer.

### Selected TCP or UDP ports

First try the Project gateway address: a `LoadBalancer` Service annotated
`service.nlb.kube-dc.com/bind-on-default-gw-eip: "true"` shares it and
allocates nothing new. For a dedicated address the user asked for, create an
`EIp`, then bind a `LoadBalancer` Service:

```yaml
apiVersion: kube-dc.com/v1
kind: EIp
metadata:
  name: app-ip
  namespace: {backing-namespace}
spec:
  externalNetworkType: public
---
apiVersion: v1
kind: Service
metadata:
  name: app
  namespace: {backing-namespace}
  annotations:
    service.nlb.kube-dc.com/bind-on-eip: app-ip
spec:
  type: LoadBalancer
  ports:
  - port: 443
    targetPort: 8443
  selector:
    app: app
```

The `service.nlb.kube-dc.com/autodelete: "true"` annotation is advanced
recovery behavior: it can delete an endpoint-less Service. It does not delete
an EIP when the Service is deleted.

## Important Product Truths

- The supported external Managed Cluster kubeconfig is Secret
  `{cluster}-cp-admin-kubeconfig-external`, key `admin.conf`. It exists only
  when external API exposure is enabled.
- Databases are managed services. A `ManagedService` names a class and a plan;
  its sizing is typed (`spec.compute`, `spec.storage`, `spec.topology`,
  `spec.engineVersion`), never under `spec.parameters`. Workloads get
  credentials from a `ServiceBinding` Secret (`host`, `port`, `username`,
  `password`, plus family-specific keys; see the service guide for the exact
  contract). RabbitMQ adds `vhost`, `ca.crt`, and `tls`, without a `uri` key.
  Every endpoint is
  TLS-only with the platform's own CA. Tenants cannot list the cluster-scoped
  plans. Where `kubectl get serviceofferings -n {backing-namespace}` is served,
  use only usable offerings: `status.projectionFormat` is `1`, the `Ready`
  condition is `True`, and the required fields for the action (`class.family`,
  `class.name`, `defaults`, `placementModes`, `connectivityClasses`,
  `deletionPolicies`, `createExample`) are present. Match the user's engine to
  `class.family` and put the offering's `class.name` in `spec.classRef.name`.
  Pick an offering whose `placementModes` and `connectivityClasses` include
  what the user needs (`ProviderShared` and `tenant-native` by default when
  offered) and use only its listed values. On a usable offering, absent
  `limits` means no per-Project limit beyond quota, and an absent `min` or
  `max` in a per-instance CPU, memory or storage range means the plan sets no
  bound there. Topology and backup-retention ranges always carry both ends; if
  one is missing, do not use the offering for that step. Quota and the service
  validator still decide at creation. Without a usable offering, get the plan name and
  its supported placement and connectivity from the console, the installation's
  documentation or the provider, or ask the user; use `ProviderShared` and
  `tenant-native` only when support for them is confirmed, and never guess.
  Offerings show the plan's last projected revision for new services, not
  existing services, remaining quota or prices.
- The catalog can grow beyond databases, caches, and messaging. Use only the
  classes, plans, and operations that the installation offers. For recovery,
  use `RestoreToNew` with a typed `spec.restore` request; the platform generates
  the target service's restore fields.
- PostgreSQL Gateway exposure needs a plan entitlement and PostgreSQL 17+
  direct-TLS clients. Standard roles cannot use port-forward.
- Gateway HTTPS creates a raw cert-manager `Certificate`, not a
  `ManagedCertificate`.
- KMS key material never leaves OpenBao, but direct encrypt plaintext travels
  through the Kube-DC backend to OpenBao and decrypted plaintext returns the
  same way. Kube-DC does not provide a general workload ServiceAccount login
  for Transit; application integrations need an operator-approved auth path.
- A bound ObjectBucketClaim gets its own Secret and ConfigMap. Use those
  per-bucket credentials for that bucket; Organization account keys do not
  automatically grant access to all OBC buckets.

## References

- [Cloud documentation](docs/cloud/index.md)
- [Projects](docs/cloud/kubernetes-projects.md)
- [Service exposure](docs/cloud/service-exposure.md)
- [Managed services](docs/cloud/managed-services.md)
- [Managed service operations](docs/cloud/managed-services-operations.md)
- [Managed Cluster operations](docs/cloud/cluster-management.md)
- [Full documentation for agents](https://docs.kube-dc.com/llms-full.txt)
