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

> **Current CLI:** `kube-dc bootstrap install` enables the embedded registry
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

The platform has independent TLS clients. A CA trusted by the install host is
not automatically trusted by pods, and a CA mounted into the manager is not
automatically trusted by Node.js or by the Kubernetes OIDC authenticator.
Use one full root+intermediate PEM bundle and verify every consumer:

| Consumer | Current mechanism | Symptom when missing |
|---|---|---|
| install workstation / bastion | OS trust store used by `curl`, `flux`, and bootstrap scripts | Keycloak discovery/bootstrap times out or fails `unknown authority` |
| **cluster nodes** (containerd, kubelet, RKE2) | `bootstrap install --trusted-ca-bundle` writes the CA into the host trust store **before RKE2 starts** | **Air-gapped: every image pull fails.** No pod-level trust can fix this — it happens before any pod exists |
| **Project workloads / CDI / any in-cluster TLS client** | The manager injects the bundle into every ConfigMap labelled `kube-dc.com/inject-trusted-ca=true`; each Project backing namespace gets `kube-dc-trusted-ca` | VM image import fails `x509: certificate signed by unknown authority` against the cluster's own mirror |
| kube-dc manager (Go) | `manager.trustedCA.configMapName` → read-only mount + `SSL_CERT_DIR` | Organization/Keycloak reconciliation fails |
| UI backend (Node.js) | `backend.trustedCA.configMapName` + `fileName` → `NODE_EXTRA_CA_CERTS` | admin pages, Grafana/OpenBao/S3, or cloud-shell token refresh fails |
| Kubernetes OIDC authenticator | manager copies the validated PEM into every `OpenIDConnect.spec.caBundle` | every browser JWT is rejected by the API server with 401 |
| OpenBao OIDC discovery | manager forwards the same bundle as `oidc_discovery_ca_pem` | Organization authentication setup reports discovery TLS/400 errors |
| CNPG/barman S3 client | database `endpointCA` when the API supports it; otherwise the restricted internal HTTP workaround in §4 | continuous archiving fails certificate verification |

For a greenfield install, pass the same certificate-only bundle to the CLI —
**to both commands**:

```bash
# 1. the nodes: OS trust store, before RKE2 starts. Air-gapped installs REQUIRE
#    this, or the first image pull fails before anything else runs.
kube-dc bootstrap install --ssh-host root@node1 ... \
  --trusted-ca-bundle=corp-ca.pem

# 2. the platform: the fleet scaffold, chart consumers, and pod-level
#    distribution to every Project backing namespace.
kube-dc bootstrap init ... \
  --tls-mode=byo-wildcard \
  --tls-cert=wildcard-fullchain.pem \
  --tls-key=wildcard-key.pem \
  --trusted-ca-bundle=corp-ca.pem
```

Two commands because there are two trust layers: a CA in a ConfigMap is invisible
to containerd, and a CA on the node is invisible to a pod. `bootstrap install`
prints the CA's subject and fingerprint in its plan before touching the host, and
refuses a bundle containing a private key or a non-CA leaf.

`--trusted-ca-bundle` is the trust contract; `--tls-*` is the served-certificate
contract. They are deliberately separate. The CLI refuses private-key or leaf
PEM blocks in the CA bundle, plan-pins its canonical SHA-256, writes
`clusters/<name>/trusted-ca.yaml`, lists it in the cluster Kustomization, and
sets both chart consumers to the generated `kube-dc-private-ca` ConfigMap.
The manager mount then supplies the same validated bundle to every
`OpenIDConnect.spec.caBundle` and to OpenBao `oidc_discovery_ca_pem`; the
backend consumes `ca.pem` through `NODE_EXTRA_CA_CERTS`.

For an existing cluster created before this installer input, the equivalent
day-2 migration is to commit that ConfigMap and the three env keys
`MANAGER_TRUSTED_CA_CONFIGMAP=kube-dc-private-ca`,
`BACKEND_TRUSTED_CA_CONFIGMAP=kube-dc-private-ca`, and
`BACKEND_TRUSTED_CA_FILENAME=ca.pem` to its fleet overlay. Do not patch live
objects only: the manager owns OIDC specs and Flux owns the workloads.

The configured directory is fail-closed: unreadable/missing directories and
malformed certificate blocks stop reconciliation explicitly. Public-CA
clusters omit the flag and continue using system roots.

The chart also consumes the bootstrap-created Keycloak admin client through
`backend.keycloakAdminClient.secretName`. Do not duplicate the client ID,
secret, URL, or realm in `backend.extraEnv`.

Verification:

```bash
# manager/backend mounts and environment
kubectl -n kube-dc get deploy -l app.kubernetes.io/name=kube-dc -o yaml | \
  grep -E 'SSL_CERT_DIR|NODE_EXTRA_CA_CERTS|trusted-ca'
# every organization realm CR should carry a non-empty base64 caBundle
kubectl get openidconnect -o jsonpath='{range .items[*]}{.metadata.name}{"\t"}{.spec.caBundle}{"\n"}{end}'
# a real Keycloak token must authenticate, not merely reach the endpoint
kubectl --token="${OIDC_TOKEN}" auth can-i get projects.kube-dc.com
```

## 3. Tenant networking — management API contract

Use `MANAGEMENT_API_MODE=service` with Tenant Networking v2. Platform-owned
controllers that must call the management Kubernetes API are classified into
the `api-client` role and admitted with an immutable route to the exact
`K8S_SERVICE_IP/32` through `INFRA_ATTACHMENT_GATEWAY`. Kube-OVN service load
balancing on `infra-net` performs the DNAT. The route (destination **and**
gateway), not a mutable SecurityGroup label, is the source of truth.

Do not add per-project routes to a MetalLB kube-api VIP or a node anchor, and
do not make the entire Service CIDR reachable. Those were topology-specific
workarounds that create a node SPOF and can authorize a pod whose route is
absent. Ordinary customer workloads intentionally cannot reach the management
ClusterIP; only controller-proven API clients receive the exact TCP/443 rule.

Acceptance must test both boundaries:

1. CSI, CCM, CNPG/MariaDB bootstrap Jobs and other classified platform clients
   can call `https://$K8S_SERVICE_IP:443` and carry an
   `ovn.kubernetes.io/routes` entry with the current infra gateway.
2. A plain tenant workload cannot call that ClusterIP. Its external platform
   access (console/login/Gateway routes) is a separate ingress contract and
   must not be “fixed” by widening management-API policy.

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
  it every Organization sync logs `400 error checking oidc discovery URL`.
- `OPENBAO_URL=http://openbao.openbao.svc:8200` (internal service) — the
  public `bao.${DOMAIN}` host is generally unreachable from
  `external-secrets-system` and from db-manager's engine registration;
  without this, SecretStores show `unable to create client` and
  DatabaseCredentialPolicies stay `engine-not-ready`.

## 5. Managed Cluster add-ons

> **Usually already done for you.** A fresh scaffold by the current
> `kube-dc bootstrap init` writes `clusters/<name>/tenant-addons.yaml`
> whenever the starter carries `platform/tenant-addons` (independent of the
> image-acceleration setting — see §6). This section is the manual wiring for
> an overlay scaffolded by an older CLI/starter, or an existing overlay that a
> resumed `init` does not backfill.

Wire `platform/tenant-addons` into a Flux Kustomization (`tenant-addons`,
dependsOn platform). Without it managed clusters get **no CNI**: worker nodes
stay NotReady → `kubelet-csr-approver` Pending → MachineDeployments stuck
`ScalingUp 0/1`. The Sveltos ClusterProfiles select
`kube-dc.com/tenant-addons=enabled`:

- `cilium-cni` — the CNI (UI addon toggle: `cni=disabled` opts out)
- `coredns` — Managed Cluster DNS (`coredns=disabled` opts out)
- `kubevirt-csi` — tenant-side CSI node driver + default StorageClass
  (`csi=disabled` opts out). **Scope its selector with
  `tenant-addons In [enabled]`** — the management cluster is itself a
  SveltosCluster and must never receive the tenant default StorageClass.

## 6. Image acceleration (complete and on by default)

`kube-dc bootstrap init` now scaffolds the stack for every new cluster
(`--image-acceleration=false` opts out); `bootstrap install` enables spegel per
node. What you get, and what it needs:

- **spegel** — RKE2 embedded registry (§1 note; nodes P2P-share image content).
- **tenant-addons** — Sveltos ClusterProfiles (Cilium CNI, CoreDNS) for
  managed/nested Managed Clusters. Without this a Managed Cluster gets **no
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

With `BILLING_PROVIDER=none`, project creation is intentionally subscription-
free in both backend and frontend. The manager treats organizations as
unmetered: it creates object-store users without a quota block. Do not encode
"unmetered" as zero — zero is the suspended/disabled quota. Verify a fresh
organization can create a Project and reaches a Ready
`CephObjectStoreUser/<organization>` without a subscription annotation.

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
