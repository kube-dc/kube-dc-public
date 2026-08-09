---
title: MetalLB front door (HA ingress address)
---

import {MetalLbHaDiagram} from '@site/src/components/Diagram/PlatformTopologyDiagrams';

# MetalLB front door (HA ingress address)

A MetalLB VIP gives the management cluster's Envoy Gateway a **single address that follows
a healthy node**. It is the recommended address layer, and on a Kube-DC cluster you get it
by declaring one thing rather than by installing MetalLB by hand.

:::note This page is about operating it, not installing it by hand
Everything below the "What you declare" section is verification, failover testing and
troubleshooting. The install itself is GitOps: `kube-dc bootstrap init` writes the addon
layers and the component selection, and Flux applies them. Editing the `EnvoyProxy` or the
MetalLB CRs by hand on a Kube-DC cluster is reverted on the next reconcile.

If you are wiring MetalLB into something that is *not* a Kube-DC cluster, the underlying
objects are in [Appendix: the objects behind the layer](#appendix-the-objects-behind-the-layer).
:::

## Why a VIP rather than a node address

With `INGRESS_ADDRESS_LAYER=none` the Envoy Service keeps a static `externalIPs` entry
pointing at one node's own address. That works and needs nothing from your network, but:

- **no failover** — the address belongs to a single node, so losing that node removes
  external access until DNS or the address is moved by hand;
- **no health gating** — nothing withdraws the address when the Envoy on that node stops
  serving, so a rolling update of the front door has a visible gap;
- the address is **pinned in the Service spec** rather than requested.

With a VIP and `externalTrafficPolicy: Local`, MetalLB announces the address **only from a
node that holds a ready local Envoy**. That single property buys both the node-loss
failover and a near-gapless rolling update, because the address moves to a node that is
already serving instead of waiting for a new pod to start.

<MetalLbHaDiagram />

## What you declare

```bash
kube-dc bootstrap init \
  --ingress-address-layer=metallb-l2 \
  --set=METALLB_FLOATING_IP=203.0.113.40 \
  --set=METALLB_INTERFACE=<host NIC carrying that L2 segment> \
  ...
```

`metallb-bgp` is the same VIP announced as a `/32` to a routed fabric; it additionally
needs `METALLB_BGP_LOCAL_ASN`, `METALLB_BGP_PEER_ASN` and `METALLB_BGP_PEER_ADDRESS`.

That produces, all through Flux:

| Piece | Where it comes from |
|---|---|
| MetalLB operator, class-scoped | `addons/metallb` (`loadBalancerClass: metallb`) |
| `IPAddressPool` + `L2Advertisement` (or `BGPPeer` + `BGPAdvertisement`) | `addons/metallb-config` / `addons/metallb-config-bgp` |
| Service type, class, `externalTrafficPolicy: Local`, the VIP request, and clearing `externalIPs` | `platform/gateway-config/components/address-metallb` |
| Envoy on the host network, on the `kube-dc.com/ingress` nodes | `platform/gateway-config/components/host-bind` |

`address-metallb` must be listed **after** `host-bind` in `spec.components`: its patch on
the Service overwrites `host-bind`'s, and that is what clears `externalIPs`. Listed first,
the node address would keep capturing `:443` alongside the VIP.

:::caution The VIP request must be explicit
The pool is created with `autoAssign: false`, so a Service only gets an address if it
**asks** for one — `address-metallb` emits
`metallb.universe.tf/loadBalancerIPs: ${METALLB_FLOATING_IP}` for exactly that reason.

This matters more than it looks. A Service that holds an address without requesting it has
a *sticky* allocation: it keeps working indefinitely, and cannot re-acquire the same address
if it is ever recreated — the front door would come back with no address at all. If you
inherit a cluster in that state (`metallb.io/ip-allocated-from-pool` present but no
`loadBalancerIPs` request and no `spec.loadBalancerIP`), add the request **before** anything
that might recreate the Service. It is a no-op while the Service exists.
:::

## Prerequisites

- The VIP is in the same L2 segment as the ingress nodes (for `metallb-l2`), or reachable
  via the configured BGP peer (for `metallb-bgp`).
- Every node that may announce it runs a MetalLB **speaker** and carries
  `ovn.kubernetes.io/external-gw` — the shared advertisement selects on that label.
- The ingress set (`kube-dc.com/ingress`) is a **subset** of the announcer set. See
  [the co-location invariant](#when-the-vip-is-announced-by-nobody).
- On CloudSigma: the masters' public NICs must be in **manual** mode via the CloudSigma
  API, which allows traffic for all subscribed IPs on that NIC including the VIP. Without
  it, traffic for an IP not explicitly assigned is dropped. The VIP must be a subscribed
  (owned) address.

## Verify

Start with the scripted check — it covers the cases that every other signal reports as
healthy:

```bash
scripts/frontdoor-check.sh preflight <cluster> <kubeconfig>   # before Flux reconciles
scripts/frontdoor-check.sh smoke     <cluster> <kubeconfig>   # after
```

Then, by hand:

```bash
# operator + one speaker per node
kubectl -n metallb-system get pods

# the pool and the advertisement Flux created
kubectl -n metallb-system get ipaddresspool,l2advertisement,bgppeer,bgpadvertisement

# the Service: EXTERNAL-IP is the VIP, class is metallb, ETP is Local, externalIPs empty
kubectl -n envoy-gateway-system get svc \
  -l gateway.envoyproxy.io/owning-gateway-name=eg \
  -o custom-columns='TYPE:.spec.type,CLASS:.spec.loadBalancerClass,ETP:.spec.externalTrafficPolicy,EXTIPS:.spec.externalIPs,VIP:.status.loadBalancer.ingress[0].ip'

# which node is announcing right now
kubectl -n envoy-gateway-system get events --sort-by=.lastTimestamp | grep -i announc

# and the front door actually answers
curl -sS -o /dev/null -w '%{http_code}\n' https://console.<DOMAIN>
```

## Failover testing

Worth doing once per cluster, because it is the property you chose this layer for.

```bash
# 1. note the announcing node (see above), then drain it
kubectl drain <node> --ignore-daemonsets --delete-emptydir-data

# 2. the announcement should move to another node with a ready Envoy
kubectl -n envoy-gateway-system get events --sort-by=.lastTimestamp | grep -i announc

# 3. traffic should keep working throughout — probe continuously, do not spot-check
while true; do curl -sS -o /dev/null -w '%{http_code} ' --max-time 3 https://console.<DOMAIN>; sleep 1; done

# 4. put it back
kubectl uncordon <node>
```

Under `Local`, the drain is what makes this meaningful: evicting Envoy removes the node's
ready local endpoint, so MetalLB withdraws the announcement from that node rather than
continuing to attract traffic it can no longer serve.

## Coexistence with the kube-dc LoadBalancer controller

MetalLB **must** stay scoped to `loadBalancerClass: metallb`, or it competes with the
kube-dc Service controller for every tenant `LoadBalancer` Service: it would try to
allocate from its pool, fail with "no available IPs", and leave project Services
`<pending>` while blocking the EIP path that should have handled them.

Two halves, both already wired:

1. `addons/metallb` sets the Helm value `loadBalancerClass: metallb`, which adds
   `--lb-class=metallb` to the controller — MetalLB then only considers Services carrying
   that class.
2. `address-metallb` sets `envoyService.loadBalancerClass` as a **first-class field** on the
   `EnvoyProxy`, so Envoy Gateway puts it on the Service at creation time.

That second point is not stylistic: `spec.loadBalancerClass` is **immutable** on a Service.
It cannot be added later by a strategic-merge patch, which is why it is a field on the CR
rather than part of the Service patch.

## Troubleshooting

### When the VIP is announced by nobody

The one front-door failure that is completely silent. MetalLB announces only from
`ovn.kubernetes.io/external-gw` nodes; Envoy answers only on `kube-dc.com/ingress` nodes;
and under `Local` a node announces only while it holds a ready local Envoy. So the address
lives on the **intersection** of those two sets. If they are disjoint, nothing announces
it: the Service shows its external IP, the pods are `Ready`, Flux is green, and the address
is simply dark. You find out by curling it — or by running `frontdoor-check.sh preflight`,
which compares the two sets for you.

### The Service has an address but nothing answers

Check the target ports. A data-plane Service created **before** the host-bind front door
maps `443 → 10443`, and Envoy Gateway does not rewrite those target ports on an existing
Service:

```bash
kubectl -n envoy-gateway-system get svc -l gateway.envoyproxy.io/owning-gateway-name=eg \
  -o jsonpath='{range .items[*].spec.ports[*]}{.port}->{.targetPort}{"\n"}{end}'
```

If `443` does not map to `443`, patch `targetPort` **in place**. That preserves the Service
UID and therefore the MetalLB allocation; deleting the Service withdraws the announcement.

### Envoy is Ready and no port is bound

`frontdoor-check.sh smoke` asserts this directly. The envoy container must run as UID 0
with `NET_BIND_SERVICE`: a non-root process under `NoNewPrivs` never gets an added
capability into its effective set, so a non-root Envoy starts, reports `2/2 Ready`, passes
its probes, and logs `cannot bind '0.0.0.0:443': Permission denied` for every listener.

## Rollback

Roll back **through git** — the Service is generated from the `EnvoyProxy` CR, so a manual
edit is reverted on the next reconcile.

1. Remove `gateway-config/components/address-metallb` from the cluster's `platform`
   `spec.components` (leave `host-bind`).
2. Set `INGRESS_ADDRESS_LAYER=none` and the derived `ENVOY_SERVICE_TYPE=ClusterIP`,
   `ENVOY_LB_CLASS=null`, `ENVOY_TRAFFIC_POLICY=null` in `cluster-config.env`.
3. Commit, push, reconcile. `host-bind` re-asserts `externalIPs` from `NODE_EXTERNAL_IP`,
   which is what gives the Gateway an address again — you do not re-apply it by hand.
4. Point wildcard/API DNS back at the node address.
5. Uninstall MetalLB only after the node-address path is confirmed serving.

Confirm with `scripts/frontdoor-check.sh smoke <cluster> <kubeconfig>`.

## Appendix: the objects behind the layer

For reference, and for non-Kube-DC clusters. On a Kube-DC cluster these are produced by the
addon layers and the component above — do not apply them by hand.

```yaml
# IPAddressPool: autoAssign false, so only a Service that requests it gets the address.
apiVersion: metallb.io/v1beta1
kind: IPAddressPool
metadata:
  name: envoy-gateway-pool
  namespace: metallb-system
spec:
  addresses:
    - 203.0.113.40/32
  autoAssign: false
---
# L2Advertisement: restricted to the external-gateway nodes.
apiVersion: metallb.io/v1beta1
kind: L2Advertisement
metadata:
  name: envoy-gateway-l2
  namespace: metallb-system
spec:
  ipAddressPools:
    - envoy-gateway-pool
  nodeSelectors:
    - matchLabels:
        ovn.kubernetes.io/external-gw: "true"
```

The resulting Service shape — `LoadBalancer`, class `metallb`, `externalTrafficPolicy:
Local`, the `loadBalancerIPs` request, and no `externalIPs` — is what
`gateway-config/components/address-metallb` renders onto the `EnvoyProxy`.
