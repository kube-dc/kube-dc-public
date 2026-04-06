# Kube-DC — AI Agent Instructions

Kube-DC is an open-source Kubernetes-native data center platform providing multi-tenancy, virtual machines (KubeVirt), managed Kubernetes clusters (Kamaji + Cluster API), managed databases (PostgreSQL, MariaDB), OVN networking with public/floating IPs, S3 object storage, block storage, backups, SSO (Keycloak), and RBAC.

## Architecture

- **Organizations** → **Projects** → **Resources** (VMs, clusters, databases, services)
- Namespace pattern: org namespace = `{org}`, project namespace = `{org}-{project}`
- All user resources MUST be created in project namespaces
- Each project gets an isolated VPC (Kube-OVN) with its own subnet

## Custom Resources (CRDs)

| Resource | API Group | Version | Short | Namespaced | Purpose |
|----------|-----------|---------|-------|------------|---------|
| `Organization` | `kube-dc.com` | `v1` | — | Yes¹ | Tenant account (billing, users, quotas) |
| `OrganizationGroup` | `kube-dc.com` | `v1` | — | Yes | Maps groups → K8s roles per project |
| `Project` | `kube-dc.com` | `v1` | — | Yes | Isolated workspace with VPC |
| `EIp` | `kube-dc.com` | `v1` | — | Yes | External IP (cloud or public) |
| `FIp` | `kube-dc.com` | `v1` | — | Yes | Floating IP (1:1 NAT to VM/pod) |
| `KdcCluster` | `k8s.kube-dc.com` | `v1alpha1` | `kdc-cl` | Yes | Managed Kubernetes cluster |
| `KdcDatabase` | `db.kube-dc.com` | `v1alpha1` | `kdcdb` | Yes | Managed PostgreSQL / MariaDB |
| `VirtualMachine` | `kubevirt.io` | `v1` | `vm` | Yes | KubeVirt VM definition |
| `DataVolume` | `cdi.kubevirt.io` | `v1beta1` | `dv` | Yes | VM disk (import/blank) |
| `ObjectBucketClaim` | `objectbucket.io` | `v1alpha1` | `obc` | Yes | S3 bucket claim |

¹ Organizations live in a namespace with the same name as the organization.

## Key Constraints & Safety Rules

1. **Namespace scope** — All user resources MUST be in the project namespace (`{org}-{project}`)
2. **Network reference** — VMs MUST use `networkName: {namespace}/default` with Multus bridge
3. **StorageClass** — Default is `local-path`; always specify `storageClassName`
4. **Guest agent** — VMs MUST include `qemu-guest-agent` in cloud-init for IP reporting and SSH key injection
5. **EIP before FIP** — FIPs with `externalNetworkType: public` auto-create EIPs; don't create both manually
6. **FIP + LB conflict** — A pod/VM CANNOT simultaneously be a public FIP target AND a cloud-network LB backend
7. **Issuer before HTTPS** — A cert-manager `Issuer` must exist in the namespace before using `expose-route: https`
8. **OBC label** — ObjectBucketClaims MUST have `kube-dc.com/organization: {org}` label
9. **KdcCluster ports** — Each cluster's `dataStore.port` must be unique within the project
10. **Users — UI only** — No User CRD exists. Users are managed via Kube-DC console (Keycloak). Direct users to the UI for user management.

## Naming Conventions

| Entity | Pattern | Example |
|--------|---------|---------|
| Org namespace | `{org}` | `shalb` |
| Project namespace | `{org}-{project}` | `shalb-docs` |
| Auto hostname | `{svc}-{namespace}.{domain}` | `wordpress-shalb-docs.kube-dc.cloud` |
| S3 bucket name | `{namespace}-{bucket}` | `shalb-docs-my-bucket` |
| VM network | `{namespace}/default` | `shalb-docs/default` |
| DB endpoint (PG) | `{name}-rw.{ns}.svc:5432` | `docs-pg-rw.shalb-docs.svc:5432` |
| DB endpoint (Maria) | `{name}.{ns}.svc:3306` | `my-mariadb.shalb-docs.svc:3306` |
| DB secret (PG) | `{name}-app` | `docs-pg-app` |
| DB secret (Maria) | `{name}-password` | `my-mariadb-password` |
| DB gateway endpoint | `{name}-db-{ns}.kube-dc.cloud:{port}` | `docs-pg-db-shalb-docs.kube-dc.cloud:5432` |
| SSH keypair secret | `ssh-keypair-default` | per project namespace |
| SSH auth keys secret | `authorized-keys-default` | per project namespace |
| Cluster kubeconfig | `{cluster}-cp-admin-kubeconfig` | data key: `admin.conf` (external URL) |
| Cluster API endpoint | `https://{cluster}-cp-{ns}.{domain}:443` | `https://dev-cp-shalb-docs.kube-dc.cloud:443` |

## Service Exposure — Two Distinct Paths

### Path A: Gateway Routes via Envoy (HTTP/HTTPS/gRPC)

Traffic flows through shared Envoy Gateway. Auto TLS certificates and DNS hostnames.

```yaml
annotations:
  service.nlb.kube-dc.com/expose-route: "https"   # http | https | tls-passthrough
  # Optional:
  # service.nlb.kube-dc.com/route-hostname: "myapp.example.com"
  # service.nlb.kube-dc.com/route-port: "8080"
  # service.nlb.kube-dc.com/tls-issuer: "letsencrypt"
```

Best for: web apps, APIs, microservices.

### Path B: Direct EIP + LoadBalancer (Any TCP/UDP)

Dedicated External IP bound to LoadBalancer service. No Envoy, no auto-TLS.

```yaml
# Step 1: Create a public EIP
apiVersion: kube-dc.com/v1
kind: EIp
metadata:
  name: my-eip
  namespace: {project-namespace}
spec:
  externalNetworkType: public
---
# Step 2: Bind service to the EIP
apiVersion: v1
kind: Service
metadata:
  name: my-service
  namespace: {project-namespace}
  annotations:
    service.nlb.kube-dc.com/bind-on-eip: "my-eip"
spec:
  type: LoadBalancer
  ports:
  - port: 22
    targetPort: 22
  selector:
    app: my-app
```

Best for: SSH, game servers, databases, custom TCP/UDP protocols.

### Decision Guide

| Need | → Use |
|------|-------|
| HTTP/HTTPS web app | Gateway Route (`expose-route: https`) |
| Auto TLS cert | Gateway Route (`expose-route: https`) |
| SSH to VM | Direct EIP (`bind-on-eip`) |
| Game server (UDP) | Direct EIP (`bind-on-eip`) |
| Custom TCP protocol | Direct EIP (`bind-on-eip`) |
| gRPC service | Gateway Route (`expose-route: https` + `route-port`) |

## Service Annotations Reference

| Annotation | Values | Effect |
|------------|--------|--------|
| `service.nlb.kube-dc.com/expose-route` | `http`, `https`, `tls-passthrough` | Create Gateway HTTPRoute/TLSRoute |
| `service.nlb.kube-dc.com/route-hostname` | FQDN | Override auto hostname |
| `service.nlb.kube-dc.com/route-port` | port number | Target port for gateway |
| `service.nlb.kube-dc.com/tls-issuer` | issuer name | cert-manager Issuer (default: `letsencrypt`) |
| `service.nlb.kube-dc.com/tls-secret` | secret name | User-provided TLS cert |
| `service.nlb.kube-dc.com/bind-on-eip` | EIP name | Bind LB to specific EIP |
| `service.nlb.kube-dc.com/bind-on-default-gw-eip` | `"true"` | Bind LB to project default EIP |
| `service.nlb.kube-dc.com/autodelete` | `"true"` | Auto-delete EIP when service deleted |
| `network.kube-dc.com/external-network-type` | `cloud`, `public` | EIP type for auto-created EIP |
| `k8s.kube-dc.com/expose-route` | `"true"` | Expose K8s API via TLSRoute |

## Project Network Types

**Cloud (`egressNetworkType: cloud`) is the recommended default.**

| Aspect | Cloud (Recommended) | Public |
|--------|---------------------|--------|
| Gateway | Shared NAT (more secure) | Dedicated public IP |
| Default EIPs | From `ext-cloud` | From `ext-public` |
| Can get public EIPs | Yes (specify `externalNetworkType: public`) | Yes (default) |
| Best for | Web apps, APIs | Game servers, direct IP |

## Resources Reference

- **Docs**: See `docs/cloud/` — full documentation per resource domain
- **Examples**: See `examples/` — ready-to-use YAML manifests
- **Skills**: See `skills/` — step-by-step procedures with templates
- **Knowledge**: See `knowledge/` — compiled CRD references and patterns
- **Full docs dump**: See https://docs.kube-dc.com/llms-full.txt
