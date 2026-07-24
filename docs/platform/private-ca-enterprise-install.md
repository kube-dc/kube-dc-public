# Replicating an enterprise (private-CA) install with the kube-dc CLI

This runbook captures everything needed to reproduce an **enterprise-class**
installation — an on-prem cluster where every public hostname
(`login.`, `console.`, `s3.`, `bao.`, … `${DOMAIN}`) is served by a
**private/corporate CA**, the outbound path is firewalled, and tenants get
dual-homed (infra-attachment) networking. It was distilled from a real
greenfield install; every step below is either a plain `kube-dc` command or an
explicitly listed, accepted per-cluster customization in the fleet repo.

Throughout, placeholders:

| Placeholder | Meaning | Example (RFC 5737 / reserved) |
|---|---|---|
| `${DOMAIN}` | cluster base domain | `kube.example.com` |
| `${EXT_CIDR}` | ext-cloud VLAN CIDR | `192.0.2.0/24` |
| `${NODE_GW}` | node anchor IP on ext-cloud (egress NAT) | `192.0.2.11` |
| `${ENVOY_VIP}` | MetalLB internal VIP for Envoy | `192.0.2.31` |
| `${KUBE_API_VIP}` | MetalLB internal VIP for kube-api | `192.0.2.32` |
| corporate CA | root+intermediate PEM bundle | `corp-ca.pem` |

## 1. Base install — kube-dc commands only

```bash
# RKE2 hosts (server/agent) — the bootstrap scripts default to the embedded
# registry mirror (spegel): embedded-registry: true + a mirrors:"*" entry in
# /etc/rancher/rke2/registries.yaml. --repo is a LOCAL checkout path; the
# GitHub destination is selected separately. Verify after install:
#   ss -tln | grep :5001        # spegel listening on servers
kube-dc bootstrap init \
  --domain "${DOMAIN}" \
  --fleet-mode=new-repo \
  --provider=github \
  --github-owner=<org> \
  --github-repo=kube-dc-fleet \
  --repo="${HOME}/kube-dc-fleet"
# ... follow the interactive flow (nodes, ext-net, object storage mode,
# OpenBao, keycloak). Flux then converges the platform.
```

Post-`init` day-2 sanity: `kube-dc bootstrap status <cluster> --repo <path>` and
`kube-dc bootstrap config list <cluster> --repo <path>`.

> **CLI ≥ v0.5.3:** `kube-dc bootstrap install` enables the embedded registry
> by default on every node (`--embedded-registry=false` opts out; an
> existing operator-managed `registries.yaml` is never overwritten). If that
> file has no non-empty `mirrors:` mapping, install refuses before restarting
> RKE2; either add a mirror or opt out explicitly. A forced re-run also refuses
> while KubeVirt/QEMU workloads are resident on the node.
>
> On clusters installed with an older CLI, enable spegel per node: append
> `embedded-registry: true` + `supervisor-metrics: true` to
> `/etc/rancher/rke2/config.yaml` (servers), write `mirrors:\n  "*":` into
> `registries.yaml` (all nodes), restart `rke2-server`/`rke2-agent` one node at
> a time.
>
> ⚠️ **Drain or stop VMs before restarting rke2 on a node.** Restarting the
> service under running KubeVirt VMs hard-kills qemu and can race the CSI node
> plugin into unmapping an RBD device **under a live ext4 mount**. The node then
> wedges hours later: kubelet's volume-ownership pass blocks in an unkillable
> `chown` (kernel D-state), the half-dead kubelet keeps holding `:10250` with a
> full accept queue, and the node flips NotReady while `rbd showmapped` on it is
> empty. Recovery: stop affected VMs, lazy-unmount the dead mounts
> (`umount -l`), delete the stale `VolumeAttachment`s, reboot the node. Seen
> twice in real installs; treat the drain rule as mandatory.

## 2. Private-CA trust — independent consumers

The platform has **at least six independent TLS trust paths**. There is no
single process-wide switch; each applicable path must receive the corporate CA
bundle or its subsystem breaks independently:

| Consumer | Mechanism | Symptom when missing |
|---|---|---|
| kube-dc-manager (Go) | `MANAGER_TRUSTED_CA_CONFIGMAP=<cm>` in `cluster-config.env` → chart mounts it + `SSL_CERT_DIR` | every Organization reconcile fails `x509: unknown authority`; **new org realms are never created**; admin Users page shows realm 404s |
| UI backend (Node) | `backend.extraEnv: NODE_EXTRA_CA_CERTS=/etc/kube-dc-ca/ca.pem` + configMap volume (HR values) | "Keycloak admin client not configured"; grafana-launch / OpenBao / S3 calls fail |
| oidc-webhook-authenticator | `SSL_CERT_DIR=/etc/ssl/certs:/extra-ca` + CA configMap on the DaemonSet | **cluster-wide 401** for all OIDC users |
| cloud-shell job (Go CLI) | shipped automatically from the backend's `NODE_EXTRA_CA_CERTS` into the per-shell Secret (`ca.pem`) + `SSL_CERT_DIR` | shell loops `session expired. Run: kube-dc login…` because token refresh can't TLS to Keycloak |
| OpenBao OIDC discovery | manager supplies the same PEM as `oidc_discovery_ca_pem` | every org sync reports a discovery URL TLS/400 error |
| CNPG/barman S3 client | database `endpointCA` when supported; otherwise the restricted internal HTTP workaround in §4 | continuous archiving fails certificate verification |

Create one ConfigMap (e.g. `kube-dc-trusted-ca`, key `ca.pem`, full chain) in
`kube-dc` as the source for mechanisms that accept a mounted bundle, and feed
that same chain into the protocol-specific OpenBao/CNPG settings. **Verify the
bundle actually contains the full chain** — a wrong or stale ConfigMap fails
identically to a missing one.

Additionally, the chart (≤ v0.5.16) does **not** consume
`keycloakAdminClient.secretName`; wire the admin client through
`backend.extraEnv` (`KEYCLOAK_ADMIN_CLIENT_URL/_REALM/_ID/_SECRET`, ID/secret
from the bootstrap-created secret).

## 3. Tenant networking — accepted customizations

On this topology the tenant VPC **cannot reach MetalLB VIPs** on the ext-cloud
localnet (VIP ARP never crosses; only real host IPs answer). Accepted fixes:

1. **kube-api**: add a per-VPC static route `${KUBE_API_VIP}/32 → ${NODE_GW}`
   (`spec.staticRoutes` on the tenant VPC). The node's kube-proxy DNATs the
   VIP to the apiservers. *Gap (tracked): the project controller should add
   this route automatically for new tenants.*
2. **Envoy-served hostnames** (`login/backend/console/billing/s3/bao`): point
   the **vpc-dns hosts block** at the **Envoy Service ClusterIP** — not the
   VIP. ClusterIP DNAT via the node egress path works from tenant pods *and*
   VMs. Pin the value as `ENVOY_GATEWAY_CLUSTER_IP` in `cluster-config.env`
   (re-pin if the Envoy Service is ever recreated).
3. **Pod-backed ClusterIPs are NOT reachable from the tenant VPC** (only
   host-backed ones — kube-api, hostNetwork Envoy). Don't point tenant
   workloads at e.g. the internal RGW Service.
4. Egress throughput: if L3-forwarded traffic is shaped by an external
   firewall, use the node-NAT egress (`EXT_NET_GATEWAY=${NODE_GW}` + the
   egress-nat DaemonSet with internet-only masquerade + INPUT anchor guard).

## 4. Object storage / S3

- **Virtual-host addressing**: boto/barman default to `bucket.s3.${DOMAIN}`.
  Set `hosting.dnsNames: [s3.${DOMAIN}]` (+ `advertiseEndpoint`) on the
  `CephObjectStore`, and add `*.s3.${DOMAIN}` to the S3 HTTPRoute hostnames —
  otherwise S3 clients get 301/404 and CNPG WAL archiving fails.
- **CNPG/barman cannot verify a private CA** (boto bundles its own certs and
  `barmanObjectStore` exposes no CA knob from the KdcDatabase layer): add a
  **plain-HTTP S3 route** on the Gateway's `:80` listener and set the
  databases' `spec.backup.s3Endpoint: http://s3.${DOMAIN}`. Traffic stays
  on-cluster via the vpc-dns→ClusterIP mapping. **Pair it with an Envoy
  Gateway `SecurityPolicy`** restricting the route to RFC1918 client CIDRs —
  the `:80` listener is otherwise reachable by anything that can reach the
  Gateway. *Durable fix (tracked): endpointCA support in db-manager.*
- **OpenBao OIDC discovery**: OpenBao verifies the Keycloak discovery URL with
  its *own* trust store; the manager forwards its private-CA bundle as
  `oidc_discovery_ca_pem` automatically (from `SSL_CERT_DIR` extras). Without
  it every org sync logs `400 error checking oidc discovery URL`.
- `OPENBAO_URL=http://openbao.openbao.svc:8200` (internal service) — the
  public `bao.${DOMAIN}` host is generally unreachable from
  `external-secrets-system` and from db-manager's engine registration;
  without this, SecretStores show `unable to create client` and
  DatabaseCredentialPolicies stay `engine-not-ready`.

## 5. Tenant-cluster addons (managed K8s)

Wire `platform/tenant-addons` into a Flux Kustomization (`tenant-addons`,
dependsOn platform). Without it managed clusters get **no CNI**: worker nodes
stay NotReady → `kubelet-csr-approver` Pending → MachineDeployments stuck
`ScalingUp 0/1`. The Sveltos ClusterProfiles select
`kube-dc.com/tenant-addons=enabled`:

- `cilium-cni` — the CNI (UI addon toggle: `cni=disabled` opts out)
- `coredns` — tenant-cluster DNS (`coredns=disabled` opts out)
- `kubevirt-csi` — tenant-side CSI node driver + default StorageClass
  (`csi=disabled` opts out). **Scope its selector with
  `tenant-addons In [enabled]`** — the management cluster is itself a
  SveltosCluster and must never receive the tenant default StorageClass.

## 6. Image acceleration (CLI ≥ v0.5.3: complete and on by default)

`kube-dc bootstrap init` now scaffolds the stack for every new cluster
(`--image-acceleration=false` opts out); `bootstrap install` enables spegel per
node. What you get, and what it needs:

- **spegel** — RKE2 embedded registry (§1 note; nodes P2P-share image content).
- **tenant-addons** — Sveltos ClusterProfiles (Cilium CNI, CoreDNS) for
  managed/nested tenant clusters. Without this a tenant cluster gets **no
  CNI**: nodes stay NotReady, `kubelet-csr-approver` never schedules, and the
  worker MachineDeployment wedges at `ScalingUp 0/1`.
- **registry-depot (zot)** — S3-backed local container registry; `init` mints
  the SOPS-encrypted push credential with your fleet's age key.
- **cdi-os-mirror** — S3 mirror of tenant OS images + weekly refresh CronJob;
  set `osImages.mirrorBaseURL: https://s3.${DOMAIN}/cdi-os-images` on the HR
  and trigger the first run manually
  (`kubectl -n kube-dc create job --from=cronjob/cdi-os-mirror-refresh first-run`).
- **rbd-vm goldens** — opt-in via `--vm-storage-mode=shared-rbd`
  (DataImportCrons with `pullMethod: node` pre-import VM base images into
  golden sources for instant clones). Start with the registry-based subset
  (ubuntu/debian/fedora); http-based goldens need the cdi-os-mirror populated
  first.

The S3-backed pieces (registry-depot, cdi-os-mirror) require an object-storage
mode — `init` skips them (with a warning) on installs without one. On clusters
scaffolded by an older CLI, wire the same three Flux Kustomizations by hand and
mind the drain rule in §1 when enabling spegel.

## 7. Billing (quota-only mode)

With `BILLING_PROVIDER=none` the backend skips the subscription gate but the
frontend still disables **Create New Project** until the org carries
`billing.kube-dc.com/subscription=active` — annotate each org (tracked UI gap).

## 8. Windows VMs

Windows guests don't run the QEMU guest agent out of the box; VM templates
must not gate readiness on `guestAgentPing` for Windows images (fixed in the
console) — otherwise a healthy, booting VM reports NotReady forever. First
boot takes several minutes at the TianoCore/Windows Boot Manager screen;
watch via VNC.

## 9. Verification checklist

```bash
# tenant pod → platform endpoints (all must answer, not timeout):
curl -sk https://login.${DOMAIN}/            # 30x
curl -sk https://s3.${DOMAIN}/               # 200
curl -sk https://kube-api.${DOMAIN}:6443/livez  # 401
# managed cluster: node Ready, csr-approver Running, coredns 2/2, CSI DS ready
# DB: KdcDatabase Ready, DBCP Ready=True, CNPG ContinuousArchiving=True
# goldens: kubectl -n golden-images get volumesnapshot (READYTOUSE=true)
# spegel: ss -tln | grep :5001 on servers
```
