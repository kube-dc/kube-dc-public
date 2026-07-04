# Internal Platform Endpoints

Tenant pods in a Kube-DC cluster need to reach the platform's own public hostnames — `kube-api.<DOMAIN>`, `login.<DOMAIN>` (Keycloak), `console.<DOMAIN>` (UI), `backend.<DOMAIN>` (kube-dc API), `billing.<DOMAIN>`. On some cluster topologies that path works "naturally" through the cluster's public IP. On others, the same packet black-holes at the upstream NAT box. **Internal Platform Endpoints** is the optional Kube-DC feature that provides a cluster-internal path to those hostnames regardless of upstream topology.

This page tells you whether your installation needs the feature, how to decide, and how to enable and operate it.

| Section | What's in it |
|---|---|
| [Do you need this?](#do-you-need-this) | Three topology classes + decision rule |
| [How it works](#how-it-works) | The Fork E pattern, anchors, Envoy front-door |
| [Enabling on a cluster](#enabling-on-a-cluster) | Helm values + fleet wiring + vpc-dns Corefile |
| [Verifying](#verifying) | Smoke tests for the four hostnames |
| [Day-2 operations](#day-2-operations) | Adding hostnames, draining nodes, MetalLB speaker election |
| [Troubleshooting](#troubleshooting) | What goes wrong and how to diagnose |
| [Configuration reference](#configuration-reference) | Full chart surface |

> This page is the operator-facing reference. Kube-DC engineering
> records and rollout runbooks for this feature are maintained
> internally.

---

## Do you need this?

There are three topology classes that matter. Pick the one that matches your cluster.

### Use the CLI classifier first

Before reading the three class definitions, run the built-in topology classifier:

```bash
kube-dc bootstrap doctor topology
```

It probes the current kubeconfig's cluster for four signals (Fork E Services already deployed, cloud-provider `providerID`, EnvoyProxy CR `externalIPs` configuration, EnvoyProxy hostNetwork patch) and prints a classification + verdict + confidence level. Sample output:

```
PROBE              DETAIL                                                                  CLASS HINT  CONFIDENCE
Fork E Services    neither platform-endpoint Service present                               —           high
cloud-provider     no providerID on any node                                               —           high
Envoy externalIPs  externalIPs=[203.0.113.10] (svc type=LoadBalancer lb-class=metallb)    B           high
Envoy hostNetwork  false or unset (standard pod networking)                                —           —

Classification: Class B  (confidence: high)
Internal Platform Endpoints: not-needed
```

If the classifier returns Class A/B/C with `confidence: high`, you can skip the manual decision aid below — the recommended verdict is reliable. For `confidence: medium` or the `ambiguous` verdict, read on and use the manual 5-line smoke test as the authoritative check.

### Class A — Single public IP behind 1:1 NAT (hairpin breaks)

| Symptom | Decision |
|---|---|
| Cluster has **one** public IP, NAT'd at an upstream router to one or more internal node IPs. Tenant pods trying to reach `https://login.<DOMAIN>` get a TCP timeout. External `kubectl` against `kube-api.<DOMAIN>:6443` works (the same NAT, observed from outside). | **You MUST enable internal platform endpoints**. Without it, tenant pods cannot reach Keycloak, the console, or `kube-api.<DOMAIN>:6443` — the OIDC login flow, the in-cluster console pod, and managed-K8s tenants all break. |

Typical hardware: a single colo'd public IP routed to a private bare-metal cluster via 1:1 NAT on an edge router. The hairpin failure happens because the NAT box won't reflect a packet back through itself.

### Class B — Flat-L2 with per-node externalIPs

| Symptom | Decision |
|---|---|
| Each control-plane node has a **public IP** directly bound on its `br-ext-cloud` (or equivalent external bridge), and all CP nodes share the same `/<N>` broadcast domain that includes the platform's public hostname IPs. Tenant pods reach `https://login.<DOMAIN>` today without any extra config. | **You DO NOT need internal platform endpoints**. Tenant traffic is SNAT'd through the per-tenant ext-cloud egress IP, the destination resolves L2-locally on `br-ext-cloud`, no upstream NAT box is involved in the hairpin. |

Typical hardware: a colo or hosted environment that delivers a small public `/<N>` to your CP nodes directly, with Envoy binding `externalIPs:` per node.

### Class C — Cloud-provider LoadBalancer

| Symptom | Decision |
|---|---|
| The platform's public hostnames are served by a cloud-provider LoadBalancer (AWS NLB, GCP LB, Azure LB, Hetzner LB, etc.) that handles hairpin natively. Tenant pods reach `https://login.<DOMAIN>` today. | **You DO NOT need internal platform endpoints**. The provider LB does the right thing. |

### Quick decision aid

If you're not sure which class you have, run this from a tenant pod:

```bash
# Get the public IP of your platform's login hostname
PUBLIC_IP=$(getent hosts login.<DOMAIN> | awk '{print $1}')
echo "Public IP: $PUBLIC_IP"

# From inside the tenant pod, try to reach it
curl -sk -m 6 -o /dev/null -w "HTTP=%{http_code}\n" https://login.<DOMAIN>/realms/master/.well-known/openid-configuration
```

| Result | Topology hint |
|---|---|
| `HTTP=200` (or any 2xx/3xx/4xx response in well under 6s) | **Class B or C** — you do not need this feature. |
| Hangs and times out at 6s | **Class A** — you need this feature. |

If you can't exec into tenant pods (e.g. you have a `restrict-pod-exec-in-projects` admission policy), the equivalent **structural** check is:

```bash
# On any control-plane node:
ip -4 -br addr show br-ext-cloud  # does this show a routable public IP?
ip -4 route get $PUBLIC_IP        # does this say "dev br-ext-cloud" (L2-local)?
```

If both answers are yes → Class B. If the public IP doesn't appear on any node and the route goes via an upstream gateway you don't control → likely Class A.

---

## How it works

The feature uses one architectural pattern, **Fork E** in the engineering record, applied to two distinct platform endpoints: `kubeAPI` (port 6443) and `envoyGateway` (port 443, fans out via Envoy HTTPRoutes).

### The Fork E pattern

```
┌─────────────────────────────────────────────────────────────────────────┐
│                          ext-cloud subnet                               │
│                                                                         │
│   ┌────────────────────────────────────────┐                            │
│   │  MetalLB-announced VIP (e.g. 100.65.0.30) │ ← tenant pods target    │
│   │  ┌────────────────────────────────────┐ │   this address            │
│   │  │ Selectorless Service               │ │                           │
│   │  │ (no pod selector — manager owns    │ │                           │
│   │  │  the EndpointSlice)                │ │                           │
│   │  └────────────────────────────────────┘ │                           │
│   │  ┌────────────────────────────────────┐ │                           │
│   │  │ Manager-owned EndpointSlice        │ │                           │
│   │  │ [192.168.110.11, .12, .13] ready=T │ │ ← updated every 5s from   │
│   │  └────────────────────────────────────┘ │   live health probes      │
│   └────────────────────────────────────────┘                            │
│                          ↓                                              │
│   kube-proxy DNATs to one of the healthy backend IPs                    │
│                          ↓                                              │
│   Backend (kube-apiserver on :6443 OR Envoy on :443)                    │
└─────────────────────────────────────────────────────────────────────────┘
```

`kube-dc-manager` probes each control-plane node's `InternalIP` directly:
- For `kubeAPI`: `GET https://<nodeIP>:6443/readyz` — apiserver is alive if it returns any non-5xx.
- For `envoyGateway`: `GET https://<nodeIP>:443/` with `SNI=console.<DOMAIN>` — Envoy is alive if it returns 200 at the console HTTPRoute.

Probes update the manager-owned EndpointSlice every 5s. Backends that stop responding for 2 consecutive intervals drop out; backends that come back are re-added.

### Per-node MetalLB L3 anchors

MetalLB's L2 (layer-2) mode announces a VIP via GARP from one elected "speaker" node. With Kube-OVN's `ext-cloud` subnet and `lb-class: metallb` Services, the elected speaker needs an L3-routable address in the subnet that **other** nodes can ARP-resolve. That's the **anchor IP** — a per-node, host-bound IP in the `ext-cloud` subnet, managed by a tiny systemd unit (`kube-dc-anchor.service`).

| Concept | Belongs to |
|---|---|
| **VIP** (e.g. `100.64.0.30`) | The selectorless Service; MetalLB announces it from whichever speaker is currently elected |
| **Anchor** (e.g. srv5 → `100.64.0.11/16`) | A per-CP-node, host-bound IP on `br-ext-cloud`. Lets the elected speaker's GARP get ARP-resolved by the other CPs. No allowlist entry needed (GARPs are L2 frames, never reach the LR policy table). |

Anchors are seeded by `kube-dc bootstrap anchors apply` and verified by `kube-dc bootstrap doctor anchors`. See [`docs/platform/cluster-cli-fleet.md`](cluster-cli-fleet.md) for the CLI workflow.

> ⚠️ **Anchor IPs must NOT collide with OVN logical-router-port IPs.** The kernel and OVN both ARP-respond for the same IP on the cloud VLAN; the cloud router's cache flips between host MAC and OVN LRP MAC and silently black-holes SNAT'd egress reply traffic. Before picking anchor IPs on any cluster, enumerate currently-allocated OVN LRP IPs and pick anchors disjoint from that set:
>
> ```bash
> kubectl -n kube-system exec deploy/ovn-central -c ovn-central -- \
>   ovn-nbctl --columns=name,networks list logical_router_port | grep '<your EXT_NET_CIDR>'
> ```
>
> Most critically: do not collide with the `ovn-cluster-ext-cloud` LRP — that's the management-VPC pod-egress SNAT IP, and a collision there manifests as ~80-min recurring outages of every controller's reach to tenant EIPs. The rule is: **anchors must come from a subset of `EXT_NET_EXCLUDE_IPS` that is disjoint from existing OVN LRP IPs**, not literally "always `.11/.12/.13`". An empty-OVN cluster can use any anchors; on a cluster with existing tenant VPCs / LRPs, audit first. Live-fix of a collided cluster requires `promote_secondaries=1` + ADD-new-before-DEL-old + per-node `ovs-ovn` restart — coordinate with the platform team before attempting (see `docs/internal/internal-platform-endpoints-runbook.md`).

### The `kubeAPI` endpoint

| Field | Value |
|---|---|
| Default name | `kube-system/kube-api-platform` |
| Default port | 6443 (apiserver) |
| Backend mode | `node-control-plane` — manager populates the slice with CP `InternalIP`s |
| Probe target | `https://<nodeIP>:6443/readyz` |
| Probe SNI | none (bare IP — apiserver cert SANs are validated via `insecureSkipVerify: true`) |
| Tenant resolution | vpc-dns Corefile: `<VIP> kube-api.<DOMAIN>` |

### The `envoyGateway` endpoint (generic front-door)

The single `envoyGateway` VIP covers **all** Envoy-routed platform hostnames at once — `login.<DOMAIN>`, `backend.<DOMAIN>`, `console.<DOMAIN>`, `billing.<DOMAIN>`, and anything else you add to your Gateway. Traffic arrives at the VIP, MetalLB-elected speaker DNATs to a healthy Envoy backend, Envoy's existing HTTPRoute matching dispatches to the right Service.

| Field | Value |
|---|---|
| Default name | `envoy-gateway-system/envoy-gateway-platform` |
| Default port | 443 (Envoy data plane) |
| Backend mode | `node-control-plane` — Envoy is hostNetwork on CP nodes |
| Probe target | `https://<nodeIP>:443/` |
| Probe SNI | **REQUIRED** — `console.<DOMAIN>` (any HTTPRoute hostname; see SNI gotcha below) |
| Tenant resolution | vpc-dns Corefile: `<VIP> login.<DOMAIN> backend.<DOMAIN> console.<DOMAIN> billing.<DOMAIN>` |

#### Critical SNI gotcha

Envoy rejects HTTPS handshakes whose SNI doesn't match a configured Gateway listener. Probing the bare node IP gets `connection reset by peer`. The probe must send a valid SNI via `platformEndpoints.envoyGateway.backend.health.host`.

**`health.host` MUST be an `HTTPRoute` hostname, not a `TLSRoute` hostname.** `kube-api.<DOMAIN>` looks tempting (every cluster has it) but it's a TLSRoute that passes through to the apiserver — Envoy doesn't terminate TLS for it, so the probe handshake reaches the apiserver and gets the wrong cert. The fleet default is `console.<DOMAIN>` because every Kube-DC cluster ships the frontend HTTPRoute and it returns 200 at `/`. Other safe choices: `backend.<DOMAIN>`, `login.<DOMAIN>`.

### vpc-dns Corefile rewrite

The tenant-side resolver (`vpc-dns-<project>` Deployment) is configured per-cluster with a CoreDNS `hosts` block that maps the platform hostnames to the internal VIPs:

```corefile
hosts {
    ${KUBE_API_INTERNAL_VIP} kube-api.${DOMAIN}
    ${ENVOY_GATEWAY_INTERNAL_VIP} login.${DOMAIN} backend.${DOMAIN} console.${DOMAIN} billing.${DOMAIN} s3.${DOMAIN}
    fallthrough
}
```

`s3.${DOMAIN}` (the cluster's Rook-Ceph RGW front-door) sits in the same list because per-tenant managed-K8s etcd-backup CronJobs upload snapshots there — without an internal-DNS override they hit the public IP and hairpin-fail on Class A topologies.

External resolution (laptop `kubectl`, browser hitting `console.<DOMAIN>`) is unaffected — public DNS still points at the cluster's public IP, the public path keeps working for non-tenant clients.

### Required tenant LR allowlists

Tenant logical routers default-deny traffic to anything in the `ext-cloud` subnet that isn't a known platform IP. The VIPs must be present in both `INGRESS_GLOBAL_ALLOWLIST` (so return traffic flows) and `EGRESS_GLOBAL_ALLOWLIST` (so outbound packets aren't dropped at priority-29000):

```
INGRESS_GLOBAL_ALLOWLIST=[<system_SNAT_IPs>, <KUBE_API_VIP>, <ENVOY_GATEWAY_VIP>]
EGRESS_GLOBAL_ALLOWLIST =[<system_SNAT_IPs>, <KUBE_API_VIP>, <ENVOY_GATEWAY_VIP>]
```

Per-node anchor IPs do **not** belong in the allowlists (they're L2 GARP sources, not L3 packet sources/sinks).

---

## Enabling on a cluster

This section assumes a Class A cluster (you need the feature). For Class B and C, skip — the chart default is off.

### 1. Pick VIPs from the `ext-cloud` subnet

Pick two IPs that are:
- In your `ext-cloud` subnet (typically `100.64.0.0/16` or `100.65.0.0/16`).
- Not already in use by a tenant EIp or system SNAT.
- Adjacent if possible (keeps allowlists tidy).

Typical choice: `100.64.0.30` for `kubeAPI`, `100.64.0.31` for `envoyGateway`.

### 2. Exclude them from kube-ovn IPAM

Add the VIPs to `EXT_NET_EXCLUDE_IPS` so kube-ovn doesn't hand them out to tenants:

```bash
# In your cluster's cluster-config.env (Fleet) or values.yaml (direct Helm):
EXT_NET_EXCLUDE_IPS="100.64.0.10,100.64.0.30,100.64.0.31,100.64.0.21,100.64.0.11..100.64.0.31"
```

### 3. Add them to both tenant allowlists

```bash
INGRESS_GLOBAL_ALLOWLIST=["<existing>", "100.64.0.30", "100.64.0.31"]
EGRESS_GLOBAL_ALLOWLIST =["<existing>", "100.64.0.30", "100.64.0.31"]
```

### 4. Enable the chart switches

```yaml
# values.yaml (or Helm CLI --set)
platformEndpoints:
  kubeAPI:
    enabled: true
    vip: "100.64.0.30"
  envoyGateway:
    enabled: true
    vip: "100.64.0.31"
    backend:
      health:
        host: "console.${DOMAIN}"     # any HTTPRoute hostname; console is the default
```

For Flux/GitOps installations the same fields go in your `HelmRelease.spec.values` (or are populated from `cluster-config.env` via postBuild substitution). See [`docs/platform/cluster-cli-fleet.md`](cluster-cli-fleet.md).

### 5. Update vpc-dns Corefile

Add the platform hostnames to the per-cluster Corefile (typically a per-cluster Flux overlay on the `vpc-dns-corefile` ConfigMap, or a direct edit if you're not on Fleet):

```corefile
hosts {
    100.64.0.30 kube-api.example.com
    100.64.0.31 login.example.com backend.example.com console.example.com billing.example.com s3.example.com
    fallthrough
}
```

### 6. Restart per-tenant vpc-dns Deployments

CoreDNS doesn't watch ConfigMaps for changes — force-restart so each per-tenant resolver picks up the new Corefile:

```bash
kubectl -n kube-system get deploy -o name | grep '^deployment.apps/vpc-dns-' | \
  xargs -I{} kubectl -n kube-system rollout restart {}

kubectl -n kube-system get deploy -o name | grep '^deployment.apps/vpc-dns-' | \
  xargs -I{} kubectl -n kube-system rollout status {} --timeout=120s
```

### 7. Bootstrap anchors (CLI)

```bash
kube-dc bootstrap anchors apply        # seed per-CP-node anchors in ext-cloud
kube-dc bootstrap doctor anchors       # verify (must be all green)
```

This binds the anchor IPs to each CP node's `br-ext-cloud` interface via a small systemd unit. Without anchors, MetalLB GARPs are still emitted but the elected speaker has no L3 presence in the subnet — return traffic black-holes.

---

## Verifying

From a tenant pod (any namespace with a Project):

```bash
# Resolves via vpc-dns to the internal VIP, not the public IP
nslookup kube-api.<DOMAIN>            # expect: <KUBE_API_INTERNAL_VIP>
nslookup login.<DOMAIN>               # expect: <ENVOY_GATEWAY_INTERNAL_VIP>

# Reaches the apiserver via the internal path
curl -sk https://kube-api.<DOMAIN>:6443/readyz
# expect: HTTP 401 (apiserver alive, auth-gated)

# Reaches the Envoy front-door for each platform hostname
curl -sk https://console.<DOMAIN>/
curl -sk https://login.<DOMAIN>/realms/master/.well-known/openid-configuration
curl -sk https://backend.<DOMAIN>/healthz
# expect: HTTP 200 each
```

From the cluster (control-plane perspective):

```bash
# The manager-owned EndpointSlice has all CP nodes ready
kubectl -n kube-system get endpointslice kube-api-platform-mgr \
  -o jsonpath='{range .endpoints[*]}{.addresses[0]} ready={.conditions.ready}{"\n"}{end}'

kubectl -n envoy-gateway-system get endpointslice envoy-gateway-platform-mgr \
  -o jsonpath='{range .endpoints[*]}{.addresses[0]} ready={.conditions.ready}{"\n"}{end}'

# MetalLB-elected speaker for each VIP
kubectl -n kube-system get svc kube-api-platform -o jsonpath='{.status.loadBalancer}'
kubectl -n envoy-gateway-system get svc envoy-gateway-platform -o jsonpath='{.status.loadBalancer}'

# kube-dc-manager events on the Services (BackendHealthy + EndpointsUpdated)
kubectl -n kube-system describe svc kube-api-platform | tail -30
kubectl -n envoy-gateway-system describe svc envoy-gateway-platform | tail -30
```

---

## Day-2 operations

### Adding a new platform hostname behind `envoyGateway`

Any new HTTPRoute hostname your cluster serves through Envoy (e.g. a new admin UI at `admin.<DOMAIN>`) automatically works from tenant pods — `envoyGateway` is generic.

But you still need to tell `vpc-dns` to resolve the new name internally. Edit the per-cluster Corefile hosts block:

```diff
 hosts {
     100.64.0.30 kube-api.example.com
-    100.64.0.31 login.example.com backend.example.com console.example.com billing.example.com
+    100.64.0.31 login.example.com backend.example.com console.example.com billing.example.com admin.example.com
     fallthrough
 }
```

Then restart per-tenant vpc-dns Deployments (same `rollout restart` loop as enablement step 6).

### Draining a control-plane node

The data path is resilient to single-node drains. With Envoy running 3 replicas (one per CP node — the platform default), the `envoy-data-plane` PodDisruptionBudget keeps at least 2 serving during a drain. The manager-owned EndpointSlice drops the drained node within ~10–15 seconds (probe `failureThreshold × intervalSeconds = 2 × 5s`).

```bash
kubectl drain <cp-node> --ignore-daemonsets --delete-emptydir-data
# expect: completes within seconds; PDB blocks if it would take Envoy below 2 replicas

# Tenant traffic during drain:
#   - kubectl from tenant pods: 1–2 reconnect blips during MetalLB speaker re-election (~6s)
#   - Sustained HTTP probe: all 200 except for the same ~6s window
```

After uncordon, the slice repopulates automatically — no manual intervention.

### Anchor IP retirement / re-pick

If you need to reclaim an anchor IP for another use (e.g. retire a CP node and reassign its anchor):

1. Drain the CP node hosting the old anchor.
2. Update `EXT_NET_ANCHOR_IPS` (cluster-config.env or values) with the new IP.
3. Re-run `kube-dc bootstrap anchors apply`.
4. Re-run `kube-dc bootstrap doctor anchors` to verify.

Note: anchor IPs do not appear in `INGRESS_GLOBAL_ALLOWLIST` / `EGRESS_GLOBAL_ALLOWLIST`. They're L2-only. If you find an old anchor IP still in either allowlist, it's a stale Phase-0 entry — safe to remove once vpc-dns no longer references it.

### Disabling the feature (rollback)

If a cluster was misclassified as Class A and you want to turn the feature off, or you're decommissioning a cluster, the rollback is the enablement steps in reverse — and unlike the enablement, it's order-sensitive:

1. **First, restore the public-DNS path for tenant pods.** Remove the platform hostnames from the per-cluster vpc-dns Corefile `hosts` block (and keep `fallthrough` so resolution falls back to public DNS):
   ```diff
    hosts {
   -    100.64.0.30 kube-api.example.com
   -    100.64.0.31 login.example.com backend.example.com console.example.com billing.example.com s3.example.com
        fallthrough
    }
   ```
   Restart per-tenant `vpc-dns-*` Deployments (same `rollout restart` loop as enablement step 6). **Wait at least 5 minutes** after the rollout so any client process caches re-resolve to the public IPs.

2. **Verify tenant pods now reach platform hostnames via the public path** (Class B/C requirement). If they don't, you have a real Class A topology and rollback would break tenant traffic — STOP and revert step 1.

3. **Disable the chart switches**:
   ```yaml
   platformEndpoints:
     kubeAPI:
       enabled: false
     envoyGateway:
       enabled: false
   ```
   Push, let Flux reconcile. The chart-rendered `IPAddressPool` / `L2Advertisement` / `Service` / `EndpointSlice` resources are removed; the manager stops probing CP node InternalIPs.

4. **Remove the VIPs from both allowlists** (`INGRESS_GLOBAL_ALLOWLIST` / `EGRESS_GLOBAL_ALLOWLIST` in `cluster-config.env`). Same pattern as the Phase D.6 `.11` retirement procedure: tenant LR `lr-policy-list` should drop the VIPs from priority-32000 + 29500 allow rules on the next manager reconcile.

5. **Narrow `EXT_NET_EXCLUDE_IPS`** back to the pre-Fork-E range if you want kube-ovn to be able to hand out the previously-reserved VIP addresses to tenants. Safe to leave widened too — it just costs you 2 unused IPs in the ext-cloud pool.

6. **Per-node MetalLB L3 anchors** (`kube-dc-anchor.service` systemd units bound to `br-ext-cloud`) — keep them as long as the cluster runs MetalLB for ANY other Service type=LoadBalancer. They're not Fork-E-specific. Only remove if you're decommissioning MetalLB entirely.

**Rollback safety**: steps 1–4 are reversible at any point — re-add the hosts entry / re-enable the chart switch / re-add the allowlist entries. Step 5 is reversible too (just rewiden again). Step 6 has the largest blast radius if you get it wrong (deleting an anchor on a node that still hosts MetalLB-announced VIPs breaks those VIPs' announcement). Don't touch step 6 unless you're sure no other MetalLB VIP depends on the anchor.

---

## Troubleshooting

| Symptom | Likely cause | Fix |
|---|---|---|
| Tenant pod gets `connection refused` to VIP | EndpointSlice has no ready backends. Manager probe is failing. | Check manager logs: `kubectl -n kube-dc logs deploy/kube-dc-manager \| grep platform-endpoint`. Common causes: NetworkPolicy blocking manager → CP `:443`, or wrong `health.host` SNI. |
| Tenant pod gets `connection reset by peer` to envoyGateway VIP | `health.host` is set to a TLSRoute hostname (most commonly `kube-api.<DOMAIN>`). Envoy resets the probe handshake. | Set `health.host: "console.<DOMAIN>"` or another HTTPRoute hostname. |
| Tenant pod gets timeout to VIP, but apiserver/Envoy is healthy | VIP is missing from `INGRESS_GLOBAL_ALLOWLIST` / `EGRESS_GLOBAL_ALLOWLIST`. Tenant LR drops the packet at priority-29000. | Add VIP to both allowlists, push. Verify with `kubectl ko nbctl lr-policy-list <tenant-lr>` — VIP should appear in priority-32000 + 29500 allow rules. |
| `nslookup login.<DOMAIN>` from tenant pod returns the public IP, not the internal VIP | Per-tenant `vpc-dns-<project>` Deployment hasn't picked up the new Corefile. | `kubectl rollout restart deploy/vpc-dns-<project> -n kube-system` and wait. |
| MetalLB doesn't announce the VIP (no GARP) | Anchor not bound on any CP node. | Run `kube-dc bootstrap doctor anchors`. Re-apply with `kube-dc bootstrap anchors apply` if any fail. |
| EndpointSlice has only one backend even though 3 CP nodes exist | Envoy is running single-replica (typically pinned to one node). Probes for other CP IPs correctly fail because there's no Envoy bound there. | Ship Envoy data-plane HA: `replicas=3` + pod anti-affinity + PDB `minAvailable=2`. See the chart's `platformEndpoints` reference. |
| Single-replica controller restarts cause data-plane config drift | xDS controller is single-replica. | Set `envoy-gateway` chart `deployment.replicas=2` with leader election. PDB `minAvailable=1`. |

For deeper debugging beyond the operator surface above, contact
Kube-DC support with the relevant `kubectl -n kube-dc logs` output
and the result of `kube-dc bootstrap doctor anchors`.

---

## Configuration reference

### Chart values (`charts/kube-dc/values.yaml`)

```yaml
platformEndpoints:

  # kubeAPI — internal VIP for tenant kubectl against kube-api.<DOMAIN>:6443
  kubeAPI:
    enabled: false                    # opt-in per cluster
    name: kube-api-platform
    namespace: kube-system
    vip: ""                           # REQUIRED if enabled
    pool: kube-api-platform
    interface: br-ext-cloud
    loadBalancerClass: metallb
    port: 6443
    backend:
      mode: node-control-plane        # populate slice from CP InternalIPs
      nodeSelector:
        node-role.kubernetes.io/control-plane: ""
      addressType: InternalIP
      health:
        scheme: https
        path: /readyz
        port: 6443
        host: ""                      # empty = probe URL host is the bare IP
        intervalSeconds: 5
        timeoutSeconds: 2
        failureThreshold: 2
        successThreshold: 1
        insecureSkipVerify: true      # apiserver cert SAN won't include the node IP

  # envoyGateway — internal VIP for tenant traffic to every Envoy-routed hostname
  envoyGateway:
    enabled: false                    # opt-in per cluster
    name: envoy-gateway-platform
    namespace: envoy-gateway-system
    vip: ""                           # REQUIRED if enabled
    pool: envoy-gateway-platform
    interface: br-ext-cloud
    loadBalancerClass: metallb
    port: 443
    backend:
      mode: node-control-plane        # Envoy is hostNetwork on CP nodes
      nodeSelector:
        node-role.kubernetes.io/control-plane: ""
      addressType: InternalIP
      health:
        scheme: https
        path: /
        port: 443
        host: ""                      # REQUIRED — see SNI gotcha
        intervalSeconds: 5
        timeoutSeconds: 2
        failureThreshold: 2
        successThreshold: 1
        insecureSkipVerify: true
        statusMax: 499                # Envoy returns 302/404/421 on many paths;
                                      # treat anything <500 as healthy
```

### Required cluster-config fields (Fleet `cluster-config.env`)

```bash
# Exclude VIPs from kube-ovn IPAM
EXT_NET_EXCLUDE_IPS=...,100.64.0.30,100.64.0.31

# Tenant allowlists (must include both VIPs in both lists)
INGRESS_GLOBAL_ALLOWLIST=[...,"100.64.0.30","100.64.0.31"]
EGRESS_GLOBAL_ALLOWLIST =[...,"100.64.0.30","100.64.0.31"]

# Feature switches
PLATFORM_ENDPOINT_KUBE_API_ENABLED=true
KUBE_API_INTERNAL_VIP=100.64.0.30
PLATFORM_ENDPOINT_ENVOY_GATEWAY_ENABLED=true
ENVOY_GATEWAY_INTERNAL_VIP=100.64.0.31

# Per-CP anchors (one IP per CP node, host-bound)
EXT_NET_ANCHOR_IPS=srv1=100.64.0.11,srv2=100.64.0.12,srv3=100.64.0.13
```

### Annotations on the Service (set by chart, read by manager)

| Annotation | Purpose |
|---|---|
| `kube-dc.com/backend-source: node-control-plane` | Tells manager to populate slice from CP InternalIPs. |
| `kube-dc.com/health-scheme: https` | Probe scheme. |
| `kube-dc.com/health-path: /readyz` | Probe path. |
| `kube-dc.com/health-port: "6443"` | Probe port. |
| `kube-dc.com/health-host: console.<DOMAIN>` | SNI + HTTP Host override for the probe. Empty = use bare IP. |
| `kube-dc.com/health-interval-seconds: "5"` | Probe interval. |
| `kube-dc.com/health-failure-threshold: "2"` | Consecutive failures before marking NotReady. |

---

## Cross-references

- [`docs/platform/architecture-networking.md`](architecture-networking.md) — VPCs, subnets, OVN logical layout.
- [`docs/platform/networking-external.md`](networking-external.md) — adding additional external networks (`ext-public` etc).
- [`docs/platform/deploy-metallb-ha.md`](deploy-metallb-ha.md) — MetalLB HA install.
- [`docs/platform/cluster-cli-fleet.md`](cluster-cli-fleet.md) — Fleet CLI workflow including `bootstrap anchors`.
