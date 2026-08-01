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
StorageClasses, NetworkPolicies, CronJobs, or privileged/host access.

## Resource Scope

| Resource | API group/version | Scope and purpose |
|---|---|---|
| `Organization` | `kube-dc.com/v1` | Organization namespace; identity, billing, quota |
| `OrganizationGroup` | `kube-dc.com/v1` | Organization namespace; maps groups to Project roles |
| `Project` | `kube-dc.com/v1` | Organization namespace; creates a governed workload boundary |
| `EIp`, `FIp` | `kube-dc.com/v1` | Project backing namespace; external/floating addresses |
| `KdcCluster` | `k8s.kube-dc.com/v1alpha1` | Project backing namespace; Managed Cluster |
| `KdcDatabase` | `db.kube-dc.com/v1alpha1` | Project backing namespace; PostgreSQL or MariaDB |
| `ManagedSecret` | `security.kube-dc.com/v1alpha1` | Project backing namespace; OpenBao-backed secret intent |
| `ManagedCertificate` | `security.kube-dc.com/v1alpha1` | Project backing namespace; managed X.509 certificate |
| `KMSKey` | `security.kube-dc.com/v1alpha1` | Project backing namespace; OpenBao Transit key |
| `DatabaseCredentialPolicy` | `security.kube-dc.com/v1alpha1` | Project backing namespace; static password rotation |
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
7. For HTTPS routes, create the Project's cert-manager `Issuer` first.
8. Give manually created ObjectBucketClaims the
   `kube-dc.com/organization: {organization}` label so dashboard and
   Organization usage attribution remain correct.
9. Keep each `KdcCluster.spec.dataStore.port` unique within a Project.
10. Treat provider domains, pools, plans, versions, and StorageClasses as
    installation-specific. Read live configuration instead of inventing them.

## Service Exposure

### Hostname-based web traffic

Use a `LoadBalancer` Service with a Gateway route for HTTP, HTTPS, or TLS
passthrough:

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

Create an `EIp`, then bind a `LoadBalancer` Service:

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
- Managed database wizard exposure is `internal` or `loadbalancer`. Gateway is
  manifest-only and compatible only with PostgreSQL 17 direct TLS; it is not a
  MariaDB or PostgreSQL 14-16 path. Standard roles cannot use port-forward.
- `DatabaseCredentialPolicy` supports `static-rotated`. `dynamic` remains
  `Ready=False/DynamicModeDeferred`, and credential issuance returns HTTP 501.
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
- [Managed databases](docs/cloud/managed-databases.md)
- [Managed Cluster operations](docs/cloud/cluster-management.md)
- [Full documentation for agents](https://docs.kube-dc.com/llms-full.txt)
