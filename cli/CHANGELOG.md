# kube-dc CLI changelog

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
