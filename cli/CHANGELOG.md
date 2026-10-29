# kube-dc CLI changelog

## v0.9.11 — 2026-10-29

- Add typed platform API commands, capability discovery, machine identities, and MCP integration for supported installations.
- Verify downloaded update binaries against release checksums.
- Reconcile fresh-install component, image digest, Services catalog, and Keycloak theme pins. Scan the portable starter for installation identities and verify its manifest and file modes.
- Use ManagedServices for new databases; exclude legacy db-manager from the fresh-install profile.
- Keep billing available as an explicit installation option, with provider `none` and PAYG disabled by default.
- Include the accepted KMS ownership, PostgreSQL CA and credential isolation, backup bucket compatibility, admission, and verified console transport corrections in the coordinated platform release.
- The CLI release is one artifact in the 0.9.11 bundle. Use the matching starter only after its final publication and installation acceptance; existing installations retain their own rollout and migration requirements.

## Unreleased

- `kube-dc secrets get --value`, `secrets delete --destroy` and `secrets consumers` no longer need the backend where the installation serves the typed operations: the value comes from `managedsecretvalues` (with its storage metadata), destruction from `managedsecretversiondestructions` with `allVersions` before the ManagedSecret is deleted, and the consumer scan reads workloads with your credentials. Output is unchanged. Managed-service credentials and older installations keep the backend route.
- `kube-dc secrets import` adopts a Secret without the backend where the installation serves `managedsecretvalues`: it reads the source Secret with your credentials, creates the ManagedSecret with sync off, writes the first version (removing the ManagedSecret again if that fails), then turns sync on to the source Secret. `secrets create` writes its first value the same way. Cross-namespace import still needs `--cross-namespace`.
- `kube-dc kms rotate`, `kube-dc db credentials get|issue|rotate` and `kube-dc secrets destroy-version` use the typed `platform.kube-dc.com` operations where served (RBAC on `kmskeys/rotate`, `databasecredentialpolicies/issue|rotate`, `databasecredentials` and `managedsecrets/destroy-version`), else the backend route; output is unchanged.
- `kube-dc secrets put` writes through the typed `managedsecretvalues` operation where the installation serves it (RBAC `update managedsecretvalues`; audited by the installation), else the backend route. `secrets get --value` stays on the backend route for now: its output carries OpenBao version metadata the typed read does not.
- `kube-dc kms encrypt|decrypt` use the typed `platform.kube-dc.com` operations (`kmsencryptions`, `kmsdecryptions`) where the installation serves them: Kubernetes RBAC on `kmskeys/encrypt` and `kmskeys/decrypt` decides, and the installation audits each call. Elsewhere they keep the backend route. Output and flags are unchanged.
- The CLI knows what it talks to and keeps working with older and newer installations:
  - Before a Project command, it reads the installation's contract from `/.well-known/kube-dc-config`: the platform release and the CLI versions it accepts. It refuses to run when the installation requires a newer CLI (`kube-dc update`), and says when a newer one is recommended. An installation without a contract, or one that cannot be reached, never blocks it.
  - It asks Kubernetes discovery which APIs the cluster serves, and uses the newest one, falling back to the backend route older installations have. Both answers are cached for an hour per installation.
- `kube-dc kms keys list|describe|create|delete` can use the Kubernetes KMSKey resource directly, with the same output and checks. They do so only where the cluster serves it and the installation declares that its platform audit log records Kubernetes calls on `security.kube-dc.com` (`audit.kubernetesGroups` in the contract). Everywhere else they keep the backend route, which writes the audit events itself. Encrypt, decrypt and rotation still use the backend.
- `kube-dc certificates list|get|request|delete` and `kube-dc db credentials list|describe|create|delete` follow the same rule for ManagedCertificate and DatabaseCredentialPolicy: native where the cluster serves the resource and the installation audits `security.kube-dc.com`, the backend route everywhere else. Renewal, credential values, issue and rotate still use the backend.
- The CLI keeps itself current. In an interactive terminal it checks for a newer release at most once a day, in the background. It installs one only when the binary matches the SHA-256 in the release's `checksums.txt`. Signed checksums are prepared and can be made mandatory later. It replaces itself atomically and says so after the command finishes. It never updates development builds, in CI, when output is not a terminal, from `kube-dc credential` (the kubectl plugin), for binaries managed by Homebrew, Nix or Snap, or when its directory is not writable. Set `KUBE_DC_NO_UPDATE=1` to turn it off. `kube-dc update` (and `--check`) does the same on demand.

## v0.9.10 — 2026-10-07

- Pin the kube-dc platform set qualified on cloud in the matching `v0.9.10` fleet-starter: chart, manager, backend, console and admin console move together. The starter also includes the qualified managed-service observation and native-resource metrics corrections. Native operator, kamaji and other pins are unchanged.
- Give every Floating IP and LoadBalancer Service exactly one External IP. The manager names it from its owner, so a retry can no longer create a duplicate, and removes duplicates left by earlier versions that kept a Floating IP Not ready.
- The console shows why a Floating IP, External IP or LoadBalancer is not ready, confirms an ambiguous VM delete before reporting it failed, checks managed-cluster worker sizes against project limits and quota, and sends security headers (CSP, HSTS, frame denial) on every page.
- Organization members are managed by the platform's own Keycloak credential after Kubernetes authorizes the caller; tenant accounts hold no Keycloak admin role, and the manager revokes earlier grants except partner service accounts.
- An Organization Group binds a custom Role only if it grants nothing beyond the four standard Project roles combined; otherwise it raises a `RoleNotBindable` event.
- Keep managed-service observations fresh with fair scheduling, bounded polling throughput and fewer duplicate backup checks. Preserve stale warnings and readiness safeguards.
- Resolve database metrics through the controller-reported native resource identity, including services with generated native names.
- New installations run upstream Keycloak 26.8 (`quay.io/keycloak/keycloak`) with a CloudNativePG database instead of the Bitnami chart, whose free images stopped at 26.3. The kube-dc login theme, the `keycloak` Service and admin Secret, metrics and the OIDC finalizer are unchanged. Existing installations keep their release.
- Every Organization's `kube-dc` sign-in client, and the platform `kube-dc-admin` and `sso-broker` clients, accept only their own callbacks: the console, the API reference, and the CLI's local callback. They previously accepted any redirect address, so a crafted link to the real sign-in page could hand a login to another site.
- Keep startup acceleration disabled in new installations until local qualification.

## v0.9.9 — 2026-10-06

- Bound generated ClickHouse Keeper names for fresh local services with long names. Preserve existing signed roots and qualified accelerated profiles.
- Pin the reviewed hub correction in the matching `v0.9.9` fleet-starter. Preserve the qualified runner, manager, native operator, backend, UI and chart pins.
- Keep startup acceleration disabled in new installations until local qualification. Provider catalogs use the annotation keys consumed by each adapter.

## v0.9.8 — 2026-10-06

- Preserve the signed Kafka cluster and node-pool identities when a shared service is retained. Replacement brokers continue to receive the scoped API access needed to read their existing credentials.
- Pin the reviewed runner repair in the matching `v0.9.8` fleet-starter. Preserve manager, hub, native operator, UI, backend and chart pins.
- Keep shared startup acceleration disabled in new installations until local qualification. This refresh does not establish a cold-engine startup guarantee.

## v0.9.7 — 2026-10-06

- Fix a blocked Kafka startup retry: let the services hub request a native PodSet retry after its exact engine UIDs are durably attested. The manager continues to reject changes to workloads, ownership and credentials.
- Pin the reviewed manager repair in the matching `v0.9.7` fleet-starter. Preserve other platform, UI, backend, service-controller and native operator pins.
- Keep shared startup acceleration disabled in new installations until local qualification. This refresh does not establish a cold-engine startup guarantee.

## v0.9.6 — 2026-10-06

- Pin the reviewed ClickHouse startup topology bounds and services observation recovery in the matched `v0.9.6` fleet-starter. Include the read-only native and archive inventory and the retirement-only admission probe required for safe retained-service cleanup.
- Preserve all existing platform, UI, backend and native operator pins. Existing installations retain their own Fleet settings.
- Keep shared startup acceleration disabled in new installations until their local ownership, credential, TLS, capacity and backup checks pass. This artifact refresh does not establish a cold-engine startup guarantee.

## v0.9.5 — 2026-10-06

- Pin the reviewed manager admission repairs, MariaDB initialization-claim provenance, MySQL helper image dispatch, and services accounting and archive-retirement fixes in the matched `v0.9.5` fleet-starter.
- Validate the exact compiled catalog, native operator image pins, and generated installation manifests. Existing installations retain their Fleet pins.
- Keep startup acceleration disabled in new installations until local ownership, credential, TLS, capacity, and backup qualification. This artifact refresh does not establish a cold-engine startup guarantee.

## v0.9.4 — 2026-10-06

- Verify MariaDB and Kafka operator attestations against their configured executable versions independently of their Helm chart versions. Reject drift in the live MariaDB and Strimzi operator, topic-operator and user-operator image pins.
- Use the matched `v0.9.4` fleet-starter with the same reviewed manager, services controllers and native operator artifacts as v0.9.3. Existing installations retain their Fleet pins.
- Keep additional startup acceleration profiles disabled until local qualification. This patch fixes component verification and does not claim a cold-engine startup guarantee.

## v0.9.3 — 2026-10-05

- Refresh the matched fleet-starter with the reviewed manager, commercial services controllers and family operators deployed on Stage and Cloud. Preserve installation-specific authorization, catalog ownership and commercial writer identities.
- Validate independent ClickHouse operator and metrics image repositories. Include the exact Strimzi operator, topic-operator and user-operator image pins in managed-services installation and component verification.
- Keep additional shared-startup profiles disabled in new installations until their local ownership, credential, TLS and backup safeguards are qualified. This artifact refresh does not establish a cold-engine startup guarantee.
- Verify published image/chart digests, generated installation manifests, compiled service catalog, monitoring and bootstrap ordering before publication.
- Existing installations retain their Fleet pins; this CLI release does not upgrade them automatically.

## v0.9.2 — 2026-10-04

- Use the matched `v0.9.2` fleet-starter with the qualified services runtime, including faster operation convergence and PostgreSQL recovery-window refresh after a primary timeline change.
- Align the new-installation PostgreSQL catalog with its compiled adapter and pin every packaged component explicitly. Keep installation-specific activation receipts out of the starter.
- Preserve existing UI, backend and sibling image pins. New installations keep startup acceleration disabled until their own ownership safeguards are qualified.
- Validate registry artifacts, generated installation manifests and compiled catalog contracts. The clean-install evidence remains the v0.9.1 qualification; this refresh does not claim another fresh-install run.
- Existing installations retain their Fleet pins; this CLI release does not upgrade them automatically.

## v0.9.1 — 2026-10-03

- Install the six managed-service families with verified Development and Production plans using the matching `v0.9.1` fleet-starter artifact.
- Add guarded managed-services enablement, rebuild preparation and a read-only `bootstrap services verify` gate for installation and component upgrades.
- Correct first-install API endpoint discovery, Flux controller/policy ordering and OpenBao initialization ordering. Preserve verified TLS, backup admission and live GitOps target checks.
- Qualify a clean single-node development installation and a verified-TLS tenant service backup, RestoreToNew and confirmed deletion. A bounded Flux source-controller replacement also recovered artifact retrieval without policy changes.
- Existing installations retain their Fleet pins; this CLI release does not upgrade them automatically.

## v0.8.3 — 2026-09-16

- Complete the aligned UI migration bundle with correct handling of successful empty RGW key-deletion responses.

## v0.8.2 — 2026-09-16

- Align the UI migration bundle after live acceptance: encode S3 copy-source headers for Unicode object names and retry temporarily unavailable project-terminal log verification.

## v0.8.1 — 2026-09-16

- Ship the corrected console quota/log routing and complete matching platform, UI and starter bundle.
- Preserve v0.8.0 artifacts; use v0.8.1 for new installations and UI migrations.

## v0.8.0 — 2026-09-16

- Align the CLI and fleet starter with the v0.8.0 platform, controllers and managed services.
- Fresh installations use the new tenant/admin consoles and matching Keycloak theme.
- Root UI configuration and backend Console API v1 are installation-specific, with verified upstream TLS and one-use terminal tickets.

## v0.7.12

- Control-plane servers installed with this version get a kube-apiserver audit
  policy. `bootstrap install` and the fleet's `bootstrap/rke2/install-server.sh`
  write `/etc/rancher/rke2/kube-dc-audit-policy.yaml` and set `audit-policy-file`
  in the RKE2 config. It records at Metadata level, with no request or response
  bodies:
  - every Secret request;
  - pod log, exec, attach, port-forward and proxy access;
  - Pod and workload-controller writes by anyone other than kube-system
    controllers and nodes;
  - writes to managed-service intents.

  The log is `/var/lib/rancher/rke2/server/logs/audit.log` on each server. RKE2
  rotates it at 100 MB and keeps up to 10 rotated files, none older than 30 days
  (about 1.1 GB with the active log). Existing servers are not changed: do not
  re-run the installer on a live server to enable it, because a re-run
  regenerates `config.yaml`. Never pass a credential as a `kubectl exec`
  argument: request URIs are recorded.
- `kube-dc login --device-code` signs in from an SSH session or a machine without
  a browser: open the displayed URL on another device, enter the code and approve.
  Credentials and kubeconfig are saved on the machine running the CLI. If Keycloak
  rejects it, a platform administrator enables the OAuth 2.0 Device Authorization
  Grant on the Organization's `kube-dc` client.
- Login discovers and embeds the Kubernetes API's CA certificate when needed,
  over the backend's verified HTTPS endpoint, and verifies the API before saving
  the context. No existing kubeconfig is required. On an older installation
  without discovery, pass a trusted CA bundle with `--ca-cert`. Login keeps
  existing kubeconfig fields: proxy and TLS server names, CA file references.
- `bootstrap openbao` no longer lets the controller-manager token write the KV
  prefix reserved for managed services.
- This CLI and the fleet-starter carry `v0.7.12`, the platform release version.

## v0.7.9

One version across the whole platform again. Every image we ship, the chart,
this CLI, the cloud-shell image and the fleet-starter carry `v0.7.9`, rebuilt
from main — the 0.7.5-0.7.8 line had drifted far enough apart that the release
tooling refused to run (kube-pod was still pinned to a v0.7.5 CLI while v0.7.8
was released). A v0.7.9 CLI installs a v0.7.9 cluster.

No CLI behaviour change of its own beyond what 0.7.6-0.7.8 already added; this
tag exists so a greenfield install gets the whole current platform, since the
fleet-starter is bound to the CLI version and starter tags are immutable.

## v0.7.8

Starter-only portability cleanup; no CLI behavior change from v0.7.7:

- Keep the shared incident rules and notification policy provider-neutral.
  Provider overlays retain any legacy resource names required for upgrades.
- Keep customer rollout evidence, site names and account identifiers out of the
  product documentation, tests and public starter.

## v0.7.7

Starter-only corrections; no CLI behavior change from v0.7.6:

- Keep failed backup jobs visible when a provider-specific replacement backup
  freshness alert is not installed.
- Preserve independent managed-cluster health signals when the inventory
  exporter is unavailable; missing inventory alone is not proof of deletion.

## v0.7.6

- Add `bootstrap keycloak init --admin-console-only` for scoped Admin Console
  enrollment without changing existing users, passwords, roles or realm settings.
- The matching Fleet starter includes named Grafana-only operators, password
  onboarding without SMTP, grouped alerts with public Grafana links, and secure
  Slack/Opsgenie settings. Notification destinations start disabled.
- Standardize new Prometheus installations on a 1 GiB memory request and 4 GiB
  limit, separate expensive historical recording rules, and stop deleted managed
  clusters and worker pools from sustaining stale health alerts.

## v0.7.5

### Added

- **The scaffold seeds the platform ingress VIP.** A dual-homed managed control
  plane reaches `bao.` and `s3.<domain>` — the Envoy front door — over its infra
  NIC, and the control-plane security group previously allowed only etcd on
  2379. The kms-plugin sidecar's OpenBao login timed out, so the apiserver never
  became Ready and CAPI refused every worker. `init` now seeds
  `INFRA_ATTACHMENT_PLATFORM_INGRESS_VIP` where this cluster's front door is
  established and sits inside the injected routes, and says exactly which key to
  set where it cannot prove that itself. Set it to `none` to opt out.

### Fixed

- **A new database provisions in seconds and never reports itself Failed while
  it waits.** Creating a database showed a red "Failed" badge with
  `ReconciliationFailed` for its first 90 seconds — the engine's name-reservation
  window, reported through the error path. That window is now 10 seconds and
  shows as Provisioning. The only identity that can plant a claim under the
  engine's names is the tenant, in their own namespace, and leftovers from an
  unclean delete are already caught by the marker scan without any wait.

## v0.7.3

No CLI source change. This tag exists so a greenfield install gets the 0.7.1
and 0.7.2 platform fixes by default: the CLI pulls
`oci://ghcr.io/kube-dc/fleet-starter:<cli-version>`, and starter tags are
immutable, so shipping updated component pins requires a new pair. Installing
with this CLI lands a cluster on chart v0.7.3 rather than v0.7.0.

What the platform gained in between:

- **"Save a final snapshot before deleting" works.** The console asked the
  platform for the snapshot instead of creating the Job itself (0.7.1), and the
  pod classifier learned the chain that lets that snapshot actually reach etcd
  (0.7.2) — without which it was admitted but hung.
- **Cloud shell keeps working after upgrades.** 0.7.1 briefly pinned a
  cloud-shell image that was never built; 0.7.2 corrected it, and from 0.7.3 the
  kube-pod image is rebuilt with the CLI it claims to carry.

## v0.7.0

The CLI joins the platform version: from this release the CLI, the
fleet-starter it pulls, the chart and every image we ship carry the same
number. A v0.7.0 CLI installs a v0.7.0 cluster.

### Added

- **Break-glass recovery.** `bootstrap init` adopts a recovery kubeconfig on
  its own: it detects a cluster whose SSO is unusable, takes the break-glass
  path, and hands back a working admin context instead of leaving the operator
  to reconstruct one by hand.
- **Self-service sign-up.** `kube-dc bootstrap keycloak sso` configures the
  Keycloak realm so users can register themselves, with the flows, mappers and
  redirect rules the product expects.
- **Alert silences.** `kube-dc alerts silence`, `ack`, `silences` and
  `unsilence` speak to Alertmanager directly, so an operator can quiet a firing
  alert (or list and lift silences) without a Grafana session.
- **Cloudflare DNS-01.** A TLS mode that solves the ACME challenge through
  Cloudflare, next to the existing Route53 path, for clusters whose zone lives
  there.
- **Windows in the golden set.** Windows images are part of the default image
  set an install materialises, not an opt-in flag.
- **Install health checks.** `bootstrap` verifies seven properties that Flux
  reports as healthy but that can still be broken underneath, so a green Flux
  no longer hides a cluster that cannot serve tenants.

### Changed

- **The OIDC cutover runs during `init`.** It was a documented manual step
  after every install; every cluster created before this release predates it.
  The docs say so in one place now.
- **`init --mode=auto` is harder to misuse:** it records the provenance of what
  it read, checks the cluster identity it is about to touch, and asks for an
  acknowledgement before it acts — and it says why the automatic default was
  rejected when it is.
- **`MutablePVNodeAffinity` is on by default,** so local-path volumes can be
  repointed and nodes stay drainable.
- **RKE2 control planes get a real CPU floor.** etcd and kube-apiserver receive
  CPU shares that survive a busy node, the full control-plane request set is
  emitted, an existing request line is preserved rather than duplicated, a node
  is recognised by its own config, both YAML spellings are captured, `--force`
  wins, and an inherited override no longer leaks into a new install.

### Fixed

- `--domain` accepts a private zone whose last label starts with a letter and
  contains digits or interior hyphens (for example `kubedc.diia-dcir`), while
  still rejecting URLs, paths and dotted IPs.
- Break-glass automation no longer risks leaking the credentials it handles.
- `EXT_NET_MGMT_SNAT_IP` is derived before the placeholder scan, so a greenfield
  install stops failing on its own placeholder.
- Seven defects in the OIDC cutover automation, found by review before it ran
  anywhere.
- ext4 write barriers are re-enabled on CloudSigma root disks — the `nobarrier`
  default is the cause of the filesystem-corruption class seen on that platform.
- The tenant-egress acceptance probe survives a floating VIP and a slow exec,
  and it verifies that something actually owns the tenant egress gateway.

## v0.6.3 and earlier

See the release notes of each tag on GitHub.
