import {InternalEndpointDiagram} from '@site/src/components/Diagram/PlatformTopologyDiagrams';

# Internal platform endpoints

Project Pods in a Kube-DC cluster need to reach the platform's own public hostnames: `kube-api.<DOMAIN>`, `login.<DOMAIN>` (Keycloak), `console.<DOMAIN>` (UI), `backend.<DOMAIN>` (kube-dc API), `billing.<DOMAIN>`. On some cluster topologies that path works "naturally" through the cluster's public IP. On others, the same packet black-holes at the upstream NAT box. **Internal Platform Endpoints** is the optional Kube-DC feature that provides a cluster-internal path to those hostnames regardless of upstream topology.

This page tells you whether your installation needs the feature, how to decide, and how to enable and operate it.

| Section | What's in it |
|---|---|
| [How Project Pods reach the platform](#how-project-pods-reach-the-platform) | Dual-homed Pods, which NIC a destination uses, Project DNS |
| [Do you need this?](#do-you-need-this) | Four topology classes + decision rule |
| [How it works](#how-it-works) | The platform-endpoint pattern, anchors, Envoy front-door |
| [Enable the feature on a cluster](#enable-the-feature-on-a-cluster) | Helm values + fleet wiring + vpc-dns Corefile |
| [Verify the result](#verify-the-result) | Smoke tests for the four hostnames |
| [Day-2 operations](#day-2-operations) | Adding hostnames, draining nodes, MetalLB speaker election |
| [Troubleshooting](#troubleshooting) | What goes wrong and how to diagnose |
| [Configuration reference](#configuration-reference) | Full chart surface |

> This page is the operator-facing reference. Kube-DC engineering
> records and rollout runbooks for this feature are maintained
> internally.

---

## How Project Pods reach the platform

Read this first: it decides which of the classes below applies to you.

### Two network interfaces per Project Pod

With infra attachment enabled (`INFRA_ATTACHMENT_ENABLED=true`, the default for
new installations), newly created Pods in a Project namespace get two NICs:

| NIC | Network | Routes | Policed by |
|---|---|---|---|
| `net1` | the Project VPC subnet | the **default route**: internet, the external network, everything not listed below | the Project VPC: SNAT/EIP, egress policy routes, Project and global allowlists |
| `eth0` | the shared infra subnet (`INFRA_ATTACHMENT_SUBNET`, default `infra-net`, CIDR `INFRA_ATTACHMENT_CIDR`, default `100.66.0.0/16`) | only `INFRA_ATTACHMENT_ROUTES` (node subnet, join subnet, pod CIDR) and, for platform API clients, the management API ServiceIP | a per-Project kube-ovn SecurityGroup (`infra-lock-<namespace>`) |

The node subnet has to be routed over `eth0`; otherwise kubelet probe replies
leave on the wrong NIC. Host-network and KubeVirt `virt-launcher` Pods are not
dual-homed, and a VM sees only its Project NIC.

The kernel picks the NIC by longest prefix. **A platform address inside
`INFRA_ATTACHMENT_ROUTES` is dialled over `eth0`; any other address over
`net1`.**

### The `eth0` lock

The SecurityGroup on `eth0` is reply-only except for exact grants, chosen from
the Pod's controller-proven owner:

| Role | Pods | New connections allowed on `eth0` |
|---|---|---|
| workload | ordinary tenant Pods | same-Project peers; the front door (`<VIP>:443`) only with `INFRA_ATTACHMENT_PLATFORM_INGRESS_WORKLOAD_EGRESS=true` |
| management API client | managed service engines (CNPG, MariaDB, ...) and tenant cloud-shell Jobs, in `MANAGEMENT_API_MODE=service`; otherwise they are workloads | the management API ServiceIP:443, same-Project workload peers |
| control plane | Managed Cluster apiservers and etcd-backup Jobs | the Cluster's etcd (TCP/2379); the front door `<VIP>:443` when `INFRA_ATTACHMENT_PLATFORM_INGRESS_VIP` is set |
| datastore | Managed Cluster etcd | etcd peers |

Everything else that leaves `eth0` is dropped. So a Project Pod dials a
platform hostname over `net1`, exactly like a VM, **unless that hostname
resolves to an address inside `INFRA_ATTACHMENT_ROUTES`**. Choosing `net1`
does not by itself guarantee success: the destination must also pass the
Project's egress policy routes and allowlists and be announced.

### Project DNS

Inside each Project VPC the cluster DNS address (`CLUSTER_DNS`, e.g.
`10.101.0.11`) is a kube-ovn SwitchLBRule to that Project's own resolver
(`vpc-dns-<project>` in `kube-system`). Pods and VMs in a Project both resolve
through it, using the shared `vpc-dns-corefile` ConfigMap. The `hosts` block
described below therefore applies to Project Pods and VMs alike; platform Pods
use `rke2-coredns`.

Exception: managed GitHub runner workers use public resolvers and a policy that
denies private and CGNAT destinations unless granted, so a Project DNS rewrite
does not apply to them. Their builds reach the platform through the runner
profile's platform endpoints instead.

---

## Do you need this?

There are four topology classes that matter. Pick the one that matches your cluster.

### Use the CLI classifier first

Before reading the three class definitions, run the built-in topology classifier:

```bash
kube-dc bootstrap doctor topology
```

It probes the current kubeconfig's cluster for four signals (platform-endpoint Services already deployed, cloud-provider `providerID`, EnvoyProxy CR `externalIPs` configuration, EnvoyProxy hostNetwork patch) and prints a classification, a verdict, and a confidence level. On any cluster that carries the host-bind front door, the hostNetwork row reads `true`. That is **not** evidence of anything unusual; see the following warning. Sample output:

```
PROBE              DETAIL                                                                  CLASS HINT  CONFIDENCE
platform endpoints neither platform-endpoint Service present                               none        high
cloud-provider     no providerID on any node                                               none        high
Envoy externalIPs  externalIPs=[203.0.113.10] (svc type=LoadBalancer lb-class=metallb)    B           high
Envoy hostNetwork  false or unset (standard pod networking)                                none        none

Classification: Class B  (confidence: high)
Internal Platform Endpoints: not-needed
```

The classifier does not yet inspect `INFRA_ATTACHMENT_ROUTES` and cannot
report Class D: on any installation with infra attachment enabled, run the
manual check below from an ordinary Project Pod even when it reports Class B
with high confidence.

If the classifier returns Class A/B/C with `confidence: high` and infra
attachment is off, you can skip the following manual decision aid, because the recommended verdict is reliable. For `confidence: medium` or the `ambiguous` verdict, read on and use the manual 5-line smoke test as the authoritative check.

### Class A: single public IP behind 1:1 NAT (hairpin breaks)

| Symptom | Decision |
|---|---|
| Cluster has **one** public IP, NAT'd at an upstream router to one or more internal node IPs. Project Pods trying to reach `https://login.<DOMAIN>` get a TCP timeout. External `kubectl` against `kube-api.<DOMAIN>:6443` works (the same NAT, observed from outside). | **You MUST enable internal platform endpoints**. Without it, Project Pods cannot reach Keycloak, the console, or `kube-api.<DOMAIN>:6443`. The OIDC login flow, the in-cluster console pod, and the Managed Cluster control planes all break. |

Typical hardware: a single colo'd public IP routed to a private bare-metal cluster through 1:1 NAT on an edge router. The hairpin failure happens because the NAT box won't reflect a packet back through itself.

:::warning What the ingress rework did to this classification
`doctor topology` infers your class from how Envoy is exposed (`externalIPs`, Service
type, hostNetwork). The front-door rework changed which of those signals still carry
information, and an earlier version of this warning predicted it wrongly. It said the
rework would make the shape **the same on every cluster**. It did not. There are two
shapes, and which one you get is decided by `INGRESS_ADDRESS_LAYER`:

| Signal | After the rework |
|---|---|
| `hostNetwork` | **true on every cluster.** Envoy always binds the node's `:80` and `:443`. This has stopped being a discriminator. |
| `externalIPs` | present on a layer of `none` (the node address IS the front door); **cleared** on a `metallb-l2`/`metallb-bgp` layer, where the announced VIP is. Still a discriminator. |
| Service type / class | `ClusterIP` with no class on `none`; `LoadBalancer` + `loadBalancerClass: metallb` on a MetalLB layer. Still a discriminator. |

So a `hostNetwork: true` reading no longer means anything unusual, and it is the
`externalIPs`-versus-VIP pair that tells you where the address lives. The question this
page is really asking does not change: **is the platform address reachable from inside the
cluster (on a node's NIC), or translated by an upstream router?** A MetalLB VIP is
L2-local to the announcing node, which is the Class B answer; a 1:1 NAT from an upstream
router is not, whatever the Service says. Answer it with the manual smoke test below,
which stays valid either way.
:::

### Class B: flat L2 with per-node externalIPs

| Symptom | Decision |
|---|---|
| Each control-plane node has a **public IP** directly bound on its `br-ext-cloud` (or equivalent external bridge), and all CP nodes share the same `/<N>` broadcast domain that includes the platform's public hostname IPs. Project Pods reach `https://login.<DOMAIN>` today without any extra config. | **You DO NOT need internal platform endpoints**. Project traffic is SNAT'd through the per-Project ext-cloud egress IP, the destination resolves L2-locally on `br-ext-cloud`, no upstream NAT box is involved in the hairpin. |

Typical hardware: a colo or hosted environment that delivers a small public `/<N>` to your CP nodes directly. With the host-bind front door this is either the node's own address carried on the Service's `externalIPs` (layer `none`), or a MetalLB VIP inside that same broadcast domain (layer `metallb-l2`). Both are L2-local to the announcing node, which is what makes this Class B.

Class B holds only while the front-door address is **outside**
`INFRA_ATTACHMENT_ROUTES`. If it is inside the node subnet those routes carry,
you have Class D.

### Class D: front door on the node network, dual-homed Project Pods

| Symptom | Decision |
|---|---|
| The front door is a MetalLB VIP (or node address) on the **node network**, and that network is in `INFRA_ATTACHMENT_ROUTES`. Platform Pods, hosts and Project VMs reach `https://login.<DOMAIN>`; Project **Pods** time out on every platform hostname: S3 buckets, managed database WAL archiving and backups, Keycloak OIDC from applications, the console API, registry pushes. | **You MUST give Project Pods a front door outside the infra routes**: enable the `envoyGateway` internal endpoint with a VIP on the external network and map the platform hostnames to it in the Project DNS (the same pattern as Class A). |

`kube-dc bootstrap init` (CLI 0.9.17 and later) configures this itself for a
new installation when all of these hold: managed services on, ingress layer
`metallb-l2` with the VIP inside the infra routes, and Envoy on control-plane
nodes (the endpoint's backends). It prints `front door ... is inside the infra
routes`, reserves the VIP and anchors right after `EXT_NET_EXCLUDE_IPS`, and
makes Project DNS answer every name under the domain except `kube-api` with
it. When Envoy runs on dedicated ingress workers it prints a WARNING; for
other front-door layers (`none`, `metallb-bgp`) it does nothing. In those
cases configure the endpoint by hand. Existing installations are not changed.

The failure is the `eth0` lock above, not routing: the hostname resolves to an
address inside the infra routes, the Pod dials it over `eth0`, and no role but
the Managed Cluster control plane may open connections to the front door there.
A VM in the same Project, which has only `net1`, reaches it.

Managed Cluster control planes are the exception that works today: they hold
the front-door grant on `eth0`. When you add the internal endpoint, decide
their path explicitly: `INFRA_ATTACHMENT_PLATFORM_INGRESS_VIP` must stay an
address inside the infra routes (the manager refuses one outside them), so
either keep it at the node-network VIP and let control planes resolve `bao`
and `s3` to it, or move them to the Project path and verify encrypted startup
and etcd backups under the Project's egress policy.

### Class C: cloud-provider LoadBalancer

| Symptom | Decision |
|---|---|
| The platform's public hostnames are served by a cloud-provider LoadBalancer such as an AWS NLB, a GCP LB, an Azure LB, or a Hetzner LB, that handles hairpin natively. Project Pods reach `https://login.<DOMAIN>` today. | **You DO NOT need internal platform endpoints**. The provider LB does the right thing. |

### Quick decision aid

If you're not sure which class you have, run this from a diagnostic Pod in a Project backing namespace:

```bash
# Get the public IP of your platform's login hostname
PUBLIC_IP=$(getent hosts login.<DOMAIN> | awk '{print $1}')
echo "Public IP: $PUBLIC_IP"

# From inside the Project Pod, try to reach it
curl -sk -m 6 -o /dev/null -w "HTTP=%{http_code}\n" https://login.<DOMAIN>/realms/master/.well-known/openid-configuration
```

| Result | Topology hint |
|---|---|
| `HTTP=200` (or any 2xx/3xx/4xx response in well under 6s) | **Class B or C.** You do not need this feature. |
| Hangs and times out at 6s | **Class A or D.** You need this feature. Class D when `ip route get <address>` in the Pod says `dev eth0`. |

Run it from an ordinary Pod (not a VM, not a host-network Pod): only a
dual-homed Pod shows Class D.

If you cannot run the diagnostic inside a Project Pod, use this equivalent **structural** check:

```bash
# On any control-plane node:
ip -4 -br addr show br-ext-cloud  # does this show a routable public IP?
ip -4 route get $PUBLIC_IP        # does this say "dev br-ext-cloud" (L2-local)?
```

If both answers are yes → Class B, unless the address is inside
`INFRA_ATTACHMENT_ROUTES` (then Class D). If the public IP doesn't appear on any node and the route goes through an upstream gateway you don't control → likely Class A.

---

## How it works

The feature uses one architectural pattern applied to two distinct platform endpoints: `kubeAPI` (port 6443) and `envoyGateway` (port 443, fans out through Envoy HTTPRoutes).

### The platform-endpoint pattern

<details data-github-only>
<summary>Diagram source for GitHub</summary>

```
┌─────────────────────────────────────────────────────────────────────────┐
│                          ext-cloud subnet                               │
│                                                                         │
│   ┌─────────────────────────────────────────┐                           │
│   │  MetalLB VIP (e.g. 100.65.0.30)         │ ← Project Pods target     │
│   │  ┌────────────────────────────────────┐ │   this address            │
│   │  │ Selectorless Service               │ │                           │
│   │  │ (no pod selector: manager owns     │ │                           │
│   │  │  the EndpointSlice)                │ │                           │
│   │  └────────────────────────────────────┘ │                           │
│   │  ┌────────────────────────────────────┐ │                           │
│   │  │ Manager-owned EndpointSlice        │ │                           │
│   │  │ [192.168.110.11, .12, .13] ready=T │ │ ← updated every 5s from   │
│   │  └────────────────────────────────────┘ │   live health probes      │
│   └─────────────────────────────────────────┘                           │
│                          ↓                                              │
│   kube-proxy DNATs to one of the healthy backend IPs                    │
│                          ↓                                              │
│   Backend (kube-apiserver on :6443 OR Envoy on :443)                    │
└─────────────────────────────────────────────────────────────────────────┘
```

</details>

<InternalEndpointDiagram />

`kube-dc-manager` probes each control-plane node's `InternalIP` directly:
- For `kubeAPI`: `GET https://<nodeIP>:6443/readyz`. The apiserver is alive if it returns any non-5xx.
- For `envoyGateway`: `GET https://<nodeIP>:443/` with `SNI=console.<DOMAIN>`. Envoy is alive if it returns 200 at the console HTTPRoute.

Probes update the manager-owned EndpointSlice every 5s. Backends that stop responding for 2 consecutive intervals drop out; backends that come back are re-added.

### Per-node MetalLB L3 anchors

MetalLB's L2 (layer-2) mode announces a VIP through GARP from one elected "speaker" node. With Kube-OVN's `ext-cloud` subnet and `lb-class: metallb` Services, the elected speaker needs an L3-routable address in the subnet that **other** nodes can ARP-resolve. That is the **anchor IP**: a per-node, host-bound IP in the `ext-cloud` subnet, managed by a tiny systemd unit (`kube-dc-anchor.service`).

| Concept | Belongs to |
|---|---|
| **VIP** (for example, `100.64.0.30`) | The selectorless Service; MetalLB announces it from whichever speaker is currently elected |
| **Anchor** (for example, srv5 → `100.64.0.11/16`) | A per-CP-node, host-bound IP on `br-ext-cloud`. Lets the elected speaker's GARP get ARP-resolved by the other CPs. No allowlist entry needed (GARPs are L2 frames, never reach the LR policy table). |

Anchors are seeded by `kube-dc bootstrap anchors apply` and verified by `kube-dc bootstrap doctor anchors`. See [`docs/platform/cluster-cli-fleet.md`](cluster-cli-fleet.md) for the CLI workflow.

> ⚠️ **Anchor IPs must NOT collide with OVN logical-router-port IPs.** The kernel and OVN both ARP-respond for the same IP on the cloud VLAN; the cloud router's cache flips between host MAC and OVN LRP MAC and silently black-holes SNAT'd egress reply traffic. Before picking anchor IPs on any cluster, enumerate currently-allocated OVN LRP IPs and pick anchors disjoint from that set:
>
> ```bash
> kubectl -n kube-system exec deploy/ovn-central -c ovn-central -- \
>   ovn-nbctl --columns=name,networks list logical_router_port | grep '<your EXT_NET_CIDR>'
> ```
>
> Most critically: do not collide with the `ovn-cluster-ext-cloud` LRP. That is the management-VPC pod-egress SNAT IP, and a collision there manifests as ~80-min recurring outages of every controller's reach to Project EIPs. The rule is: **anchors must come from a subset of `EXT_NET_EXCLUDE_IPS` that is disjoint from existing OVN LRP IPs**, not literally "always `.11/.12/.13`". An empty-OVN cluster can use any anchors; on a cluster with existing Project VPCs / LRPs, audit first. Live-fix of a collided cluster requires `promote_secondaries=1` + ADD-new-before-DEL-old + per-node `ovs-ovn` restart. Coordinate with the platform team before you attempt it. The live-fix procedure is held in the internal runbook rather than here, because it is destructive and cluster-specific.

### The `kubeAPI` endpoint

| Field | Value |
|---|---|
| Default name | `kube-system/kube-api-platform` |
| Default port | 6443 (apiserver) |
| Backend mode | `node-control-plane`: the manager populates the slice with CP `InternalIP`s |
| Probe target | `https://<nodeIP>:6443/readyz` |
| Probe SNI | none (bare IP; apiserver cert SANs are validated through `insecureSkipVerify: true`) |
| Project resolution | vpc-dns Corefile: `<VIP> kube-api.<DOMAIN>` |

### The `envoyGateway` endpoint (generic front-door)

The single `envoyGateway` VIP covers **all** Envoy-routed platform hostnames at once: `login.<DOMAIN>`, `backend.<DOMAIN>`, `console.<DOMAIN>`, `billing.<DOMAIN>`, and anything else you add to your Gateway. Traffic arrives at the VIP, MetalLB-elected speaker DNATs to a healthy Envoy backend, Envoy's existing HTTPRoute matching dispatches to the right Service.

| Field | Value |
|---|---|
| Default name | `envoy-gateway-system/envoy-gateway-platform` |
| Default port | 443 (Envoy data plane) |
| Backend mode | `node-control-plane`: Envoy is hostNetwork on CP nodes |
| Probe target | `https://<nodeIP>:443/` |
| Probe SNI | **REQUIRED.** `console.<DOMAIN>`, or any HTTPRoute hostname; see the SNI gotcha |
| Project resolution | vpc-dns Corefile: `<VIP> login.<DOMAIN> backend.<DOMAIN> console.<DOMAIN> billing.<DOMAIN>` |

#### Critical SNI gotcha

Envoy rejects HTTPS handshakes whose SNI doesn't match a configured Gateway listener. Probing the bare node IP gets `connection reset by peer`. The probe must send a valid SNI through `platformEndpoints.envoyGateway.backend.health.host`.

**`health.host` MUST be an `HTTPRoute` hostname, not a `TLSRoute` hostname.** `kube-api.<DOMAIN>` looks tempting (every cluster has it) but it is a TLSRoute that passes through to the apiserver, so Envoy does not terminate TLS for it, so the probe handshake reaches the apiserver and gets the wrong cert. The fleet default is `console.<DOMAIN>` because every Kube-DC cluster ships the frontend HTTPRoute and it returns 200 at `/`. Other safe choices: `backend.<DOMAIN>`, `login.<DOMAIN>`.

### vpc-dns Corefile rewrite

The per-Project resolver (`vpc-dns-<project>` Deployment) is configured for each Project with a CoreDNS `hosts` block that maps the platform hostnames to the internal VIPs:

```corefile
hosts {
    ${KUBE_API_INTERNAL_VIP} kube-api.${DOMAIN}
    ${ENVOY_GATEWAY_INTERNAL_VIP} login.${DOMAIN} backend.${DOMAIN} console.${DOMAIN} admin.${DOMAIN} billing.${DOMAIN} bao.${DOMAIN} grafana.${DOMAIN} s3.${DOMAIN} recovery.${DOMAIN} registry.${DOMAIN}
    fallthrough
}
```

Every hostname a Project workload may dial belongs in the list:

| Hostname | Dialled from Projects by |
|---|---|
| `s3` | managed database WAL archiving and backups, application buckets, Managed Cluster etcd backups |
| `recovery` | managed-services restores into shared Projects |
| `registry` | builds that push to the managed registry |
| `bao` | the Managed Cluster KMS plugin (etcd encryption) |
| `login` | OIDC from applications and Managed Clusters |
| `console`, `backend`, `admin`, `billing`, `grafana` | the CLI, cloud shell and API clients inside Projects |

A hostname missing from the list resolves through public DNS and fails the
same way it would without the feature. Fixed lists do not cover generated
names: managed registries (`<instance>-<namespace>.<DOMAIN>`) and Services
exposed through the Gateway get their own hostnames. Where Projects call those
over HTTPS, answer the whole zone with the internal VIP (a CoreDNS `template`
for `<DOMAIN>`, with `kube-api` mapped first) instead of listing names. The
internal endpoint serves port 443 only.

External resolution (laptop `kubectl`, browser hitting `console.<DOMAIN>`) is unaffected. Public DNS still points at the cluster's public IP, the public path keeps working for external clients.

### Required Project logical-router allowlists

Project logical routers default-deny traffic to anything in the `ext-cloud` subnet that isn't a known platform IP. The VIPs must be present in both `INGRESS_GLOBAL_ALLOWLIST` (so return traffic flows) and `EGRESS_GLOBAL_ALLOWLIST` (so outbound packets aren't dropped at priority-29000):

```
INGRESS_GLOBAL_ALLOWLIST=[<system_SNAT_IPs>, <KUBE_API_VIP>, <ENVOY_GATEWAY_VIP>]
EGRESS_GLOBAL_ALLOWLIST =[<system_SNAT_IPs>, <KUBE_API_VIP>, <ENVOY_GATEWAY_VIP>]
```

Per-node anchor IPs do **not** belong in the allowlists (they're L2 GARP sources, not L3 packet sources/sinks).

---

## Enable the feature on a cluster

This section assumes a Class A cluster (you need the feature). For Class B and C, skip this section. The chart default is off.

### 1. Pick VIPs from the `ext-cloud` subnet

Pick two IPs that are:
- In your `ext-cloud` subnet (typically `100.64.0.0/16` or `100.65.0.0/16`).
- Not already in use by a Project EIp or system SNAT.
- Adjacent if possible (keeps allowlists tidy).

Typical choice: `100.64.0.30` for `kubeAPI`, `100.64.0.31` for `envoyGateway`.

### 2. Exclude them from kube-ovn IPAM

Add the VIPs to `EXT_NET_EXCLUDE_IPS` so kube-ovn doesn't hand them out to Projects:

```bash
# In your cluster's cluster-config.env (Fleet) or values.yaml (direct Helm):
EXT_NET_EXCLUDE_IPS="100.64.0.10,100.64.0.30,100.64.0.31,100.64.0.21,100.64.0.11..100.64.0.31"
```

### 3. Add them to both Project logical-router allowlists

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

For Flux/GitOps installations the same fields go in your `HelmRelease.spec.values` (or are populated from `cluster-config.env` through postBuild substitution). See [`docs/platform/cluster-cli-fleet.md`](cluster-cli-fleet.md).

### 5. Update vpc-dns Corefile

Add the platform hostnames to the per-cluster Corefile (typically a per-cluster Flux overlay on the `vpc-dns-corefile` ConfigMap, or a direct edit if you're not on Fleet):

```corefile
hosts {
    100.64.0.30 kube-api.example.com
    100.64.0.31 login.example.com backend.example.com console.example.com admin.example.com billing.example.com bao.example.com grafana.example.com s3.example.com recovery.example.com registry.example.com
    fallthrough
}
```

See [vpc-dns Corefile rewrite](#vpc-dns-corefile-rewrite) for the full list and
for generated hostnames.

### 6. Restart per-Project vpc-dns Deployments

CoreDNS does not watch ConfigMaps for changes. Force a restart, so that each per-Project resolver picks up the new Corefile:

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

This binds the anchor IPs to each CP node's `br-ext-cloud` interface through a small systemd unit. Without anchors, MetalLB GARPs are still emitted but the elected speaker has no L3 presence in the subnet, so return traffic black-holes.

---

## Verify the result

From a Pod in any Project backing namespace:

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

### Add a platform hostname behind `envoyGateway`

Any new HTTPRoute hostname your cluster serves through Envoy (for example, a new admin UI at `admin.<DOMAIN>`) automatically works from Project Pods, because `envoyGateway` is generic.

But you still need to tell `vpc-dns` to resolve the new name internally. Edit the per-cluster Corefile hosts block:

```diff
 hosts {
     100.64.0.30 kube-api.example.com
-    100.64.0.31 login.example.com backend.example.com console.example.com billing.example.com
+    100.64.0.31 login.example.com backend.example.com console.example.com billing.example.com admin.example.com
     fallthrough
 }
```

Then restart per-Project vpc-dns Deployments (same `rollout restart` loop as enablement step 6).

### Drain a control-plane node

The data path is resilient to single-node drains. With Envoy running 3 replicas (one per CP node, the platform default), the `envoy-data-plane` PodDisruptionBudget keeps at least 2 serving during a drain. The manager-owned EndpointSlice drops the drained node within ~10–15 seconds (probe `failureThreshold × intervalSeconds = 2 × 5s`).

```bash
kubectl drain <cp-node> --ignore-daemonsets --delete-emptydir-data
# expect: completes within seconds; PDB blocks if it would take Envoy below 2 replicas

# Project traffic during drain:
#   - kubectl from Project Pods: 1–2 reconnect blips during MetalLB speaker re-election (~6s)
#   - Sustained HTTP probe: all 200 except for the same ~6s window
```

After uncordon, the slice repopulates automatically, with no manual intervention.

### Anchor IP retirement / re-pick

If you need to reclaim an anchor IP for another use (for example, retire a CP node and reassign its anchor):

1. Drain the CP node hosting the old anchor.
2. Update `EXT_NET_ANCHOR_IPS` (cluster-config.env or values) with the new IP.
3. Re-run `kube-dc bootstrap anchors apply`.
4. Re-run `kube-dc bootstrap doctor anchors` to verify.

Note: anchor IPs do not appear in `INGRESS_GLOBAL_ALLOWLIST` / `EGRESS_GLOBAL_ALLOWLIST`. They're L2-only. If you find an old anchor IP still in either allowlist, it is a stale Phase-0 entry, safe to remove after vpc-dns no longer references it.

### Disable the feature (rollback)

If a cluster was misclassified as Class A and you want to turn the feature off, or you're decommissioning a cluster, the rollback is the enablement steps in reverse. Unlike the enablement, it is order-sensitive:

1. **First, restore the public-DNS path for Project Pods.** Remove the platform hostnames from the per-cluster vpc-dns Corefile `hosts` block (and keep `fallthrough` so resolution falls back to public DNS):
   ```diff
    hosts {
   -    100.64.0.30 kube-api.example.com
   -    100.64.0.31 login.example.com backend.example.com console.example.com billing.example.com s3.example.com
        fallthrough
    }
   ```
   Restart per-Project `vpc-dns-*` Deployments (same `rollout restart` loop as enablement step 6). **Wait at least 5 minutes** after the rollout so any client process caches re-resolve to the public IPs.

2. **Verify Project Pods now reach platform hostnames through the public path** (Class B/C requirement). If they do not, you have a real Class A topology, and the rollback would break Project traffic. STOP and revert step 1.

3. **Disable the chart switches**:
   ```yaml
   platformEndpoints:
     kubeAPI:
       enabled: false
     envoyGateway:
       enabled: false
   ```
   Push, let Flux reconcile. The chart-rendered `IPAddressPool` / `L2Advertisement` / `Service` / `EndpointSlice` resources are removed; the manager stops probing CP node InternalIPs.

4. **Remove the VIPs from both allowlists** (`INGRESS_GLOBAL_ALLOWLIST` / `EGRESS_GLOBAL_ALLOWLIST` in `cluster-config.env`). Same pattern as the Phase D.6 `.11` retirement procedure: Project logical router `lr-policy-list` should drop the VIPs from priority-32000 + 29500 allow rules on the next manager reconcile.

5. **Narrow `EXT_NET_EXCLUDE_IPS`** back to the pre-Fork-E range if you want kube-ovn to be able to hand out the previously-reserved VIP addresses to Projects. It is safe to leave widened too. It costs you 2 unused IPs in the ext-cloud pool.

6. **Per-node MetalLB L3 anchors** (`kube-dc-anchor.service` systemd units bound to `br-ext-cloud`): keep them as long as the cluster runs MetalLB for ANY other Service type=LoadBalancer. They're not Fork-E-specific. Only remove if you're decommissioning MetalLB entirely.

**Rollback safety**: steps 1 to 4 are reversible at any point. Re-add the hosts entry, re-enable the chart switch, or re-add the allowlist entries. Step 5 is reversible too (just rewiden again). Step 6 has the largest blast radius if you get it wrong (deleting an anchor on a node that still hosts MetalLB-announced VIPs breaks those VIPs' announcement). Don't touch step 6 unless you're sure no other MetalLB VIP depends on the anchor.

---

## Troubleshooting

| Symptom | Likely cause | Fix |
|---|---|---|
| Project Pod gets `connection refused` to VIP | EndpointSlice has no ready backends. Manager probe is failing. | Check manager logs: `kubectl -n kube-dc logs deploy/kube-dc-manager \| grep platform-endpoint`. Common causes: NetworkPolicy blocking manager → CP `:443`, or wrong `health.host` SNI. |
| Project Pod gets `connection reset by peer` to envoyGateway VIP | `health.host` is set to a TLSRoute hostname (most commonly `kube-api.<DOMAIN>`). Envoy resets the probe handshake. | Set `health.host: "console.<DOMAIN>"` or another HTTPRoute hostname. |
| Project Pod gets timeout to VIP, but apiserver/Envoy is healthy | VIP is missing from `INGRESS_GLOBAL_ALLOWLIST` / `EGRESS_GLOBAL_ALLOWLIST`. Project logical router drops the packet at priority-29000. | Add VIP to both allowlists, push. Verify with `kubectl ko nbctl lr-policy-list <project-lr>`. The VIP appears in the priority-32000 and 29500 allow rules. |
| `nslookup login.<DOMAIN>` from Project Pod returns the public IP, not the internal VIP | Per-Project `vpc-dns-<project>` Deployment hasn't picked up the new Corefile. | `kubectl rollout restart deploy/vpc-dns-<project> -n kube-system` and wait. |
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

  # kubeAPI: internal VIP for Project-user `kubectl` against kube-api.<DOMAIN>:6443
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

  # envoyGateway: internal VIP for Project traffic to every Envoy-routed hostname
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
        host: ""                      # REQUIRED: see the SNI gotcha
        intervalSeconds: 5
        timeoutSeconds: 2
        failureThreshold: 2
        successThreshold: 1
        insecureSkipVerify: true
        statusMax: 499                # Envoy returns 302/404/421 on many paths;
                                      # treat anything <500 as healthy
```

### Required cluster-config fields (fleet `cluster-config.env`)

```bash
# Exclude VIPs from kube-ovn IPAM
EXT_NET_EXCLUDE_IPS=...,100.64.0.30,100.64.0.31

# Project logical-router allowlists (must include both VIPs in both lists)
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

- [`docs/platform/architecture-networking.md`](architecture-networking.md): VPCs, subnets, OVN logical layout.
- [`docs/platform/networking-external.md`](networking-external.md): adding additional external networks (`ext-public` etc).
- [`docs/platform/deploy-metallb-ha.md`](deploy-metallb-ha.md): MetalLB HA install.
- [`docs/platform/cluster-cli-fleet.md`](cluster-cli-fleet.md): Fleet CLI workflow including `bootstrap anchors`.
