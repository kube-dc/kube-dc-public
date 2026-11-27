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
| `Application` | `apps.kube-dc.com` | `v1alpha1` | `app` | An app from an image or a GitHub repository; generates and owns its Deployment and Service |
| `ApplicationBuild` | `apps.kube-dc.com` | `v1alpha1` | `appbuild` | One build of an Application; create one to rebuild |
| `ManagedService` | `services.kube-dc.com` | `v1alpha1` | `msvc` | Service from the provider's catalog, with a class and plan |
| `ServiceBinding` | `services.kube-dc.com` | `v1alpha1` | | Credential role delivered as a Secret |
| `ServiceOperation` | `services.kube-dc.com` | `v1alpha1` | | One day-2 action on a service |
| `ServiceCredentialPolicy` | `services.kube-dc.com` | `v1alpha1` | | Scheduled credential rotation |
| `ServiceOffering` | `services.kube-dc.com` | `v1alpha1` | `so` | Read-only copy of a plan offered to the Project: defaults, limits, `createExample` (where enabled) |
| `ManagedSecret` | `security.kube-dc.com` | `v1alpha1` | — | Project secret backed by OpenBao, optionally projected into a K8s Secret via ESO |
| `ManagedCertificate` | `security.kube-dc.com` | `v1alpha1` | `mcert` | x509 cert from Org private CA or public ACME, auto-renewed |
| `KMSKey` | `security.kube-dc.com` | `v1alpha1` | — | Per-project encryption key backed by OpenBao Transit |
| `VirtualMachine` | `kubevirt.io` | `v1` | `vm` | KubeVirt VM definition |
| `DataVolume` | `cdi.kubevirt.io` | `v1beta1` | `dv` | VM disk import (http) or blank |
| `ObjectBucketClaim` | `objectbucket.io` | `v1alpha1` | `obc` | S3 bucket claim (Rook-Ceph) |

## Skills (Agent Procedures)

| Skill | Description | Key Files |
|-------|-------------|-----------|
| `check-quota` | Check org/project quota usage before deploying; troubleshoot exceeded-quota errors | SKILL.md |
| `create-project` | Create project with VPC networking | SKILL.md, project-template.yaml, network-types.md |
| `deploy-app` | Deploy an app as an Application (image or GitHub source) or a plain workload, with optional DB + HTTPS; keyless deploys, builds on push and PR previews from GitHub, with a managed container registry or the user's own; rollback, pause, builds | SKILL.md, references/shared-gpu.md |
| `deploy-web-stack` | Web app + managed database + volume + HTTPS hostname through the Gateway (no EIP), one procedure | SKILL.md, web-stack-template.yaml |
| `create-vm` | Provision VM with SSH access and cloud-init | SKILL.md, vm-template.yaml |
| `create-database` | Create a managed PostgreSQL, MySQL, MariaDB, ClickHouse or Valkey service, bind its credential to a workload, back up and restore | SKILL.md, postgresql-template.yaml, mysql-mariadb-template.yaml, clickhouse-template.yaml, valkey-template.yaml, binding-template.yaml, db-connection-patterns.md, backup-restore-patterns.md |
| `manage-database-credentials` | Deliver a managed service credential role to a workload with `ServiceBinding`, rotate it now with a `RotateCredentials` operation or on a schedule with `ServiceCredentialPolicy`. Pair with `create-database`. | SKILL.md, binding-template.yaml, rotation-policy-template.yaml, rotate-operation-template.yaml |
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
| `install-kube-dc` | Operator: plan and install a new Kube-DC platform (host and disk checks, network, front door, Ceph, DNS, certificates, bootstrap install/init/accept) | SKILL.md, decisions.md, host-checks.md, install-input.env.example |

Skills location: `skills/{skill-name}/SKILL.md`

> **Best practice**: Run `check-quota` before creating VMs, clusters, databases, or public EIPs to avoid quota-exceeded failures mid-deployment.

## Docs (Human-Readable, Also Useful for Agents)

### Cloud User Guide (`docs/cloud/`)

| File | Topic | Size |
|------|-------|------|
| `service-exposure.md` | Gateway routes, EIP, FIP, all exposure patterns | ~700 lines |
| `managed-services.md` and the Managed Services chapter | Catalog, plans, plan offerings (`service-offerings.md`), console, operations, bindings, backups, recovery, family guides, status, and deletion | Varies by guide |
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
| `ai-ide-integration.md` | MCP server setup for Claude/Cursor/Windsurf | ~690 lines |
| `deploy-first-app.md` | First app: an image Application with kubectl or an AI app, new release, rollback, clean up; when to use plain manifests | ~125 lines |
| `applications.md` | The Application resource: console, YAML fields, conditions, `kube-dc app` CLI, AI tools, domains, deletion | ~350 lines |
| `rollback.md` | Releases, rollback by pin, instant rollback with a warm previous version | ~145 lines |
| `app-jobs-and-workers.md` | Release commands (migrations), workers, one-off and cron jobs, autoscaling, data volumes, restarts on Secret change | ~155 lines |
| `deploy-from-github.md` | New application from GitHub, the Application manifest, organization consent, managed vs own registry, builds, pull request previews, preview databases, copy grants and masking, own GitHub Actions workflow | ~365 lines |
| `mcp-for-administrators.md` | The hosted MCP server: app registration, consent and revocation, every tool, data handling | ~180 lines |
| `managed-services-registry.md` | Container registry (family `registry`): plans, keyless push, pull Secrets, operations, deletion | ~170 lines |
| `deploy-wordpress-stack.md` | WordPress as an Application: managed MariaDB with a ServiceBinding, data volume, HTTPS address (no EIP), WP-CLI install Job, optional S3 archive | ~395 lines |
| `secrets-manager.md` | ManagedSecret CRD, sync to K8s Secret, KMS comparison | ~270 lines |
| `kms.md` | KMSKey CRD, direct encrypt/decrypt, envelope encryption with Go + Python helpers, rotation, min_decryption_version | ~350 lines |
| `certificate-manager.md` | ManagedCertificate CRD, private CA vs ACME public, mTLS / code-signing | ~250 lines |
| `postgresql-credentials.md` | ServiceBinding delivery, RotateCredentials, ServiceCredentialPolicy for declared roles and existing logins, break-glass | ~450 lines |
| `managed-services-operations.md` | Shared operation contract, supported types, approval, cancellation, and results | ~160 lines |
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
| `github-app.md` | The installation's GitHub App: Create GitHub App in the admin console (Platform settings → Integrations) or by hand, features, webhook, builds on push | ~330 lines |
| `hosted-mcp.md` | Hosted MCP endpoint for AI apps: enabling it, client lists in the admin console, prerequisites, read-only enforcement, operations | ~330 lines |

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
| Org namespace | `{org}` | `acme` |
| Project namespace | `{org}-{project}` | `acme-docs` |
| Auto hostname | `{svc}-{ns}.{installation-domain}`; read it from the Service's `route-hostname-status` annotation | `nginx-acme-docs.<installation-domain>` |
| Managed service endpoint | Read `host` and `port` from the binding Secret | Do not infer an engine Service name |
| Managed service Secret | `ServiceBinding.spec.delivery.secretName` | Keys depend on the family; see `create-database` |
| SSH keypair | `ssh-keypair-default` | per project |
| Cluster kubeconfig | `{cluster}-cp-admin-kubeconfig-external` | data key: `admin.conf` (external URL) |
| VM network | `{ns}/default` | `acme-docs/default` |
| S3 bucket | `{ns}-{name}` | `acme-docs-my-bucket` |

## Full Documentation Dump

For the complete docs in a single file: https://docs.kube-dc.com/llms-full.txt
