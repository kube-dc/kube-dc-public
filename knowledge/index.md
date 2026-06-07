# Kube-DC Knowledge Index

Master catalog for AI agents. Read this first, then dive into specific files as needed.

## CRDs (Custom Resource Definitions)

| Resource | API Group | Version | Short | Purpose |
|----------|-----------|---------|-------|---------|
| `Organization` | `kube-dc.com` | `v1` | — | Tenant account, lives in ns=`{org}` |
| `OrganizationGroup` | `kube-dc.com` | `v1` | — | Maps groups → K8s RBAC roles per project |
| `Project` | `kube-dc.com` | `v1` | — | Isolated workspace with VPC, ns=`{org}-{project}` |
| `EIp` | `kube-dc.com` | `v1` | — | External IP allocation (cloud or public) |
| `FIp` | `kube-dc.com` | `v1` | — | Floating IP — 1:1 NAT to VM/pod |
| `KdcCluster` | `k8s.kube-dc.com` | `v1alpha1` | `kdc-cl` | Managed Kubernetes cluster (Kamaji + CAPI) |
| `KdcDatabase` | `db.kube-dc.com` | `v1alpha1` | `kdcdb` | Managed PostgreSQL or MariaDB |
| `ManagedSecret` | `security.kube-dc.com` | `v1alpha1` | — | Project secret backed by OpenBao, optionally projected into a K8s Secret via ESO |
| `ManagedCertificate` | `security.kube-dc.com` | `v1alpha1` | `mcert` | x509 cert from Org private CA or public ACME, auto-renewed |
| `KMSKey` | `security.kube-dc.com` | `v1alpha1` | — | Per-project encryption key backed by OpenBao Transit |
| `DatabaseCredentialPolicy` | `security.kube-dc.com` | `v1alpha1` | `dbcp` | Static-rotated or dynamic DB credentials backed by OpenBao Database engine |
| `VirtualMachine` | `kubevirt.io` | `v1` | `vm` | KubeVirt VM definition |
| `DataVolume` | `cdi.kubevirt.io` | `v1beta1` | `dv` | VM disk import (http) or blank |
| `ObjectBucketClaim` | `objectbucket.io` | `v1alpha1` | `obc` | S3 bucket claim (Rook-Ceph) |

## Skills (Agent Procedures)

| Skill | Description | Key Files |
|-------|-------------|-----------|
| `check-quota` | Check org/project quota usage before deploying; troubleshoot exceeded-quota errors | SKILL.md |
| `create-project` | Create project with VPC networking | SKILL.md, project-template.yaml, network-types.md |
| `deploy-app` | Deploy containerized app with optional DB + HTTPS | SKILL.md |
| `create-vm` | Provision VM with SSH access and cloud-init | SKILL.md, vm-template.yaml |
| `create-database` | Create managed PostgreSQL/MariaDB with access patterns + backup/restore | SKILL.md, pg-template.yaml, mariadb-template.yaml, db-connection-patterns.md, backup-restore-patterns.md |
| `expose-service` | Expose service via Gateway Route or Direct EIP | SKILL.md, envoy-gateway-examples.yaml, eip-loadbalancer-examples.yaml |
| `manage-cluster` | Scale workers, upgrade K8s version, access kubeconfig | SKILL.md, scale-workers.md, upgrade-version.md, kubeconfig-access.md |
| `manage-networking` | Create EIPs, FIPs, understand VPC networking | SKILL.md, eip-template.yaml, fip-template.yaml, decision-guide.md |
| `manage-storage` | S3 buckets (OBC), DataVolumes, PVCs | SKILL.md, obc-template.yaml, datavolume-template.yaml |
| `manage-access` | OrganizationGroup RBAC, user management (UI-only) | SKILL.md, org-group-template.yaml, rbac-roles.md |
| `manage-secrets` | ManagedSecret (OpenBao-backed) + ESO projection for API tokens, signing keys, third-party creds | SKILL.md, managed-secret-template.yaml |
| `manage-certificates` | x509 from Org private CA or public ACME (server / client / mtls / code-signing) | SKILL.md, managed-certificate-template.yaml |
| `manage-kms` | Per-project encryption keys; direct encrypt (≤64 KiB) + envelope encryption (Go & Python helpers) | SKILL.md, kmskey-template.yaml, envelope-encryption-go.md, envelope-encryption-py.md |
| `ssh-into-vm` | SSH into VM using project keypair | SKILL.md |
| `use-kube-dc-cli` | Authentication, context switching, namespace management | SKILL.md |

Skills location: `skills/{skill-name}/SKILL.md`

> **Best practice**: Run `check-quota` before creating VMs, clusters, databases, or public EIPs to avoid quota-exceeded failures mid-deployment.

## Docs (Human-Readable, Also Useful for Agents)

### Cloud User Guide (`docs/cloud/`)

| File | Topic | Size |
|------|-------|------|
| `service-exposure.md` | Gateway routes, EIP, FIP, all exposure patterns | ~700 lines |
| `managed-databases.md` | DB creation, connection, external access, scheduled + on-demand backups, restore (in-place + new-name), PostgreSQL PITR | ~620 lines |
| `creating-vm.md` | VM deployment, SSH access, cloud-init | ~210 lines |
| `cluster-management.md` | K8s cluster scaling, upgrading, storage, troubleshooting | ~390 lines |
| `provisioning-cluster.md` | Creating managed K8s clusters, including etcd-at-rest encryption + KEK rotation toggles | ~300 lines |
| `networking-overview.md` | VPC, subnets, network types explained | ~150 lines |
| `public-floating-ips.md` | EIP and FIP usage, allocation, lifecycle | ~200 lines |
| `object-storage.md` | S3 buckets, credentials, AWS CLI usage | ~200 lines |
| `block-storage.md` | DataVolumes, PVCs, storage classes | ~150 lines |
| `team-management.md` | Users, groups, RBAC, OrganizationGroup | ~320 lines |
| `core-concepts.md` | Org → Project → Resources hierarchy | ~100 lines |
| `cli-kubeconfig.md` | CLI install, kubeconfig setup, IDE integration | ~200 lines |
| `ai-ide-integration.md` | MCP server setup for Claude/Cursor/Windsurf | ~330 lines |
| `deploy-first-app.md` | WordPress tutorial with Helm + HTTPS | ~200 lines |
| `secrets-manager.md` | ManagedSecret CRD, sync to K8s Secret, KMS comparison | ~200 lines |
| `kms.md` | KMSKey CRD, direct encrypt/decrypt, envelope encryption with Go + Python helpers, rotation, min_decryption_version | ~350 lines |
| `certificate-manager.md` | ManagedCertificate CRD, private CA vs ACME public, mTLS / code-signing | ~250 lines |
| `database-credentials.md` | DatabaseCredentialPolicy CRD, static-rotated GA + dynamic preview | ~260 lines |
| `backups-snapshots.md` | Velero workload backups + managed-K8s etcd backup envelope mode | ~530 lines |

### Platform Operator Guide (`docs/platform/`)

| File | Topic | Size |
|------|-------|------|
| `architecture-overview.md` | System architecture, components | ~300 lines |
| `architecture-networking.md` | OVN VPCs, Envoy Gateway, MetalLB, network types | ~530 lines |
| `architecture-multi-tenancy.md` | RBAC, Keycloak, namespace isolation | ~300 lines |
| `installation-overview.md` | Installation prerequisites and steps | ~110 lines |
| `project-resources.md` | What gets created per project | ~200 lines |
| `managed-k8s-etcd-encryption.md` | M5 etcd-at-rest encryption for KdcClusters: kms-plugin sidecar, KEK rotation, min_decryption_version safety | ~300 lines |
| `managed-k8s-etcd-backup-restore.md` | Daily etcd snapshot CronJob, envelope-encrypted backup layout, controller-driven restore | ~320 lines |

## Examples (`examples/`)

| Directory | Contents |
|-----------|----------|
| `organization/` | org.yaml, project_*.yaml, eip.yaml, fip.yaml, service_lb.yaml, org_group.yaml, wordpress/ |
| `project/` | issuer.yaml, http/https/grpc/tls-passthrough service examples |
| `virtual-machine/` | Ubuntu, Debian, CentOS, Windows, Alpine VMs |
| `capi-cluster/` | Managed K8s cluster with SSH, DNAT, addons |
| `networking/` | Additional external network config |

## Naming Conventions

| Entity | Pattern | Example |
|--------|---------|---------|
| Org namespace | `{org}` | `shalb` |
| Project namespace | `{org}-{project}` | `shalb-docs` |
| Auto hostname | `{svc}-{ns}.kube-dc.cloud` | `nginx-shalb-docs.kube-dc.cloud` |
| DB endpoint (PG) | `{name}-rw.{ns}.svc:5432` | `docs-pg-rw.shalb-docs.svc:5432` |
| DB secret (PG) | `{name}-app` | `docs-pg-app` |
| DB endpoint (Maria) | `{name}.{ns}.svc:3306` | `my-mariadb.shalb-docs.svc:3306` |
| DB secret (Maria) | `{name}-password` | `my-mariadb-password` |
| SSH keypair | `ssh-keypair-default` | per project |
| Cluster kubeconfig | `{cluster}-cp-admin-kubeconfig` | data key: `admin.conf` (external URL) |
| VM network | `{ns}/default` | `shalb-docs/default` |
| S3 bucket | `{ns}-{name}` | `shalb-docs-my-bucket` |

## Full Documentation Dump

For the complete docs in a single file: https://docs.kube-dc.com/llms-full.txt
