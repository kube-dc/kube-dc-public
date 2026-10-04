import {ExternalNetworksDiagram} from '@site/src/components/Diagram/PlatformTopologyDiagrams';

# Additional external network configuration

This guide explains how to add additional external networks to Kube-DC alongside the default cloud network.

For a new installation, first use
[Choose an installation network layout](installer-network-layouts.md). It covers
the TUI choices, per-server interfaces, address reservations, and the separate
platform ingress decision.

## Overview

The configuration demonstrates how to add a second external network (public) to an existing Kube-DC setup that already has a cloud external network, using multiple VLANs on a single physical interface per node.

## Network types explained by example

### Cloud network (`egressNetworkType: cloud`)
- **Purpose**: Default external network for most workloads
- **Subnet**: `ext-cloud` (100.65.0.0/16) on VLAN 200
- **Use Cases**: 
  - General internet access for applications
  - Standard egress traffic from project workloads
  - Cost-effective external connectivity
- **IP Pool**: Large address space (65,000+ IPs available)

### Public network (`egressNetworkType: public`)
- **Purpose**: Premium external network for specialized workloads
- **Subnet**: `ext-public` (192.0.2.0/28) on VLAN 300
- **Use Cases**:
  
  - Production services requiring dedicated public IPs
  - Load balancers and ingress controllers
  - Services needing specific public IP ranges or routing
- **IP Pool**: Limited address space with public IPv4 addresses (16 IPs total)

## Architecture

<details data-github-only>
<summary>Diagram source for GitHub</summary>

```
Physical Interface (bond0)
├── VLAN 200 (Cloud Network) - 100.65.0.0/16 (ext-cloud)
└── VLAN 300 (Public Network) - 192.0.2.0/28 (ext-public)
```

</details>

<ExternalNetworksDiagram />

> **Routed / L3-only datacenters**: the external networks above are L2
> segments, tagged or untagged. `EXT_NET_VLAN_ID=0` is supported when the
> carrier NIC *is* the segment). Tenant EIP/FIP reachability is ARP-based
> and needs that L2 adjacency. The **platform ingress VIPs** announced by
> MetalLB can alternatively be advertised over **BGP** (`--ingress-address-layer=metallb-bgp`)
> for fabrics with no shared L2. See the installation guide's
> "BGP mode" section.

> **The egress gateway must answer ARP.** `EXT_NET_GATEWAY` (the tenant
> internet next-hop on the ext network) has to be a live L2 neighbour that
> replies to ARP on the ext interface. An address that is only *inside*
> the ext CIDR but silent on ARP produces a clean install with **black-holed
> tenant egress** (pods route out, get no return path). `kube-dc bootstrap
> init` now arpings the gateway from the node before CNI and prints a
> warning if it is unreachable, but the check is advisory (fail-open); verify
> by hand on any node once the ext interface is up:
>
> ```bash
> arping -c2 -I <ext-iface> "$EXT_NET_GATEWAY"   # for example, arping -c2 -I bond0.200 192.0.2.1
> ```
>
> A node whose own anchor IP *is* the gateway (node-egress topology) needs no
> ARP reply and is skipped by the probe.

## Example cluster usage

- **demo-cloud project**: Uses `egressNetworkType: cloud` → EIP: 100.65.0.102 (development/testing)
- **demo-public project**: Uses `egressNetworkType: public` → EIP: 192.0.2.6 (development with public access)
- **demo-envoy project**: Uses `egressNetworkType: public` → EIPs: 192.0.2.7, 192.0.2.8 (production load balancer)

### Choose the right network

**Use Cloud Network when:**
- Need basic internet connectivity
- Don't require specific public IP ranges

**Use Public Network when:**
- Need dedicated public IP addresses
- Have specific routing or compliance requirements
- Running load balancers or ingress controllers

## OVS/OVN resources generated

### 1. OVS bridge configuration
With the physical NIC already configured as a VLAN trunk, Kube-OVN creates the following host OVS resources from the fleet manifests:

**Bridge: `br-ext-cloud`**
- Physical interface `bond0` attached with VLAN trunking
- Trunk VLANs: `[0, 300, 200]`
- Patch ports for both external networks:
  - `patch-localnet.ext-cloud-to-br-int` ↔ `patch-br-int-to-localnet.ext-cloud`
  - `patch-localnet.ext-public-to-br-int` ↔ `patch-br-int-to-localnet.ext-public`

### 2. OVN logical switches
Two logical switches are created automatically:
- `ext-cloud` (for VLAN 200)
- `ext-public` (for VLAN 300)

### 3. ProviderNetwork status
The existing ProviderNetwork `ext-cloud` is updated to include both VLANs:
```yaml
status:
  vlans: ["vlan200", "vlan300"]
  ready: true
  readyNodes:
  - kube-dc-master-1
  - kube-dc-worker-1
```

## Configuration steps

For a greenfield install, supply these values to `kube-dc bootstrap init`; the
CLI writes the ProviderNetwork patch, public-network Flux layer, and (when the
L2 VIP is in the public CIDR) anchor contract. The raw `kubectl` flow below is
a day-2 fallback for an older overlay and must be committed back into the fleet
to avoid GitOps drift.


### 1. Apply VLAN configuration
```bash
kubectl apply -f examples/networking/additional-external-network.yaml
```

### 2. Verify configuration
```bash
# Check ProviderNetwork VLANs
kubectl get provider-network ext-cloud -o jsonpath='{.status.vlans}'
# Expected output: ["vlan200","vlan300"]

# Check external subnets
kubectl get subnets ext-cloud ext-public
# Expected: ext-cloud (100.65.0.0/16) and ext-public (192.0.2.0/28)

# Check EIP assignments
kubectl get eips -A
# Shows which projects are using which external networks

# Check OVS bridge configuration
kubectl exec -n kube-system [ovs-pod] -- ovs-vsctl show | grep -A 10 "br-ext-cloud"

# Check OVN logical switches
kubectl exec -n kube-system [ovn-central-pod] -- ovn-nbctl ls-list | grep ext
```

### 3. Test with Project
Create projects to test both network types:

**Project using Cloud Network:**
```yaml
apiVersion: kube-dc.com/v1
kind: Project
metadata:
  name: test-project-cloud
  namespace: test-org
spec:
  cidrBlock: 10.200.0.0/24
  egressNetworkType: cloud  # Uses ext-cloud subnet (100.65.0.0/16)
```

**Project using Public Network:**
```yaml
apiVersion: kube-dc.com/v1
kind: Project
metadata:
  name: test-project-public
  namespace: test-org
spec:
  cidrBlock: 10.201.0.0/24
  egressNetworkType: public  # Uses ext-public subnet (192.0.2.0/28)
```

## Public-VLAN addressing contract (MetalLB L2 ingress VIP)

When the platform ingress VIP lives on the routed public VLAN
(`--preset cloud+public-vlan`, `--ingress-address-layer=metallb-l2`), the public CIDR is
partitioned by a fixed contract. Example for a `/28`
(`EXT_PUBLIC_CIDR=192.0.2.0/28`):

| Address | Role |
|---|---|
| `192.0.2.1` | Public VLAN gateway (`EXT_PUBLIC_GATEWAY`) |
| `192.0.2.2` | MetalLB floating ingress VIP (`METALLB_FLOATING_IP`) |
| `192.0.2.3-.5` | **Per-node anchor addresses**, one for each gateway node (`EXT_NET_PUBLIC_ANCHOR_IPS`) |
| `192.0.2.6-.14` | Tenant pool: public EIPs and per-project VPC router ports (LRPs) |

The current `kube-dc bootstrap init` derives the anchors (VIP+1, VIP+2, …)
and writes `EXT_PUBLIC_EXCLUDE_IPS_1/2` so gateway + VIP + anchors are
reserved in kube-ovn IPAM. Three rules are load-bearing:

1. **Anchors must hold addresses.** MetalLB's ARP responder needs no
   address, so an address-less announcement *looks* alive. ARP resolves
   and TCP connects, but the reply routes out the node's default
   (management VLAN), asymmetric through the datacenter's stateful
   firewall, which drops it. Clients see accept-then-timeout. The
   fleet's `ext-net-bridge-tag` DaemonSet binds each node's anchor and a
   policy route (`from <VIP> lookup 129`, default through the public
   gateway) continuously, so the setting survives reboots and kube-ovn
   bridge recreation. The shared `L2Advertisement` selects the same
   `ovn.kubernetes.io/external-gw=true` nodes. This selector is load-bearing:
   MetalLB's `interfaces` field filters interfaces but does not constrain leader
   election, so an unanchored worker must not be eligible.
2. **Reserve before the first tenant.** kube-ovn honors `excludeIps`
   for **new** allocations only. An EIP or a VPC router port that grabbed
   an address before the exclusion keeps it, and the host and OVN then
   both answer ARP for one IP on one segment. If an anchor IP ever
   resolves to two MACs, audit `kubectl get ovn-eip -o wide` for a
   pre-exclusion allocation and re-home it (detach/re-attach the VPC's
   external subnet; fresh allocations honor the exclusion).
3. **Test the VIP with SNI, from off the node.** `curl https://<VIP>/`
   gets a TCP handshake and then an Envoy reset (no SNI filter-chain
   match), which you cannot tell apart from a broken VIP. Use
   `curl --resolve console.<domain>:443:<VIP> https://console.<domain>/`.
   And never test from a cluster node: kube-proxy intercepts
   LoadBalancer IPs in the OUTPUT path, so node-originated probes never
   reach the wire.

## Key points

1. **Single ProviderNetwork**: Use one ProviderNetwork per physical interface with multiple VLANs attached
2. **Automatic Configuration**: OVS bridges, patch ports, and OVN logical switches are created automatically
3. **VLAN Trunking**: The physical interface supports multiple VLANs simultaneously
4. **GitOps-owned host state**: Kube-OVN and the `ext-net-bridge-tag` DaemonSet own the host OVS ports; the operator still owns the physical switch trunk and upstream routing

## Before you begin

- Physical network infrastructure supporting VLAN trunking
- vSwitch configured with appropriate VLAN IDs

## Troubleshooting

### Check the VLAN is trunked into OVS on the nodes
The OVS provider bridge carries the public VLAN. Do **not** expect a
Linux `bond0.300` sub-interface to exist (kube-ovn attaches the trunk NIC to
`br-ext-cloud` and tags in OVS). Check the bridge and, on public-L2 clusters,
the anchor interface the fleet creates:
```bash
# On cluster nodes
sudo ovs-vsctl show | grep -A6 br-ext-cloud      # trunk NIC + patch ports present
ip -br link show ext-pub-anchor                  # public L2 anchor (EXT_NET_PUBLIC_ANCHOR_INTERFACE)
```

### Check OVN resources
```bash
# Check OVN-EIP resources
kubectl get ovn-eip | grep ext-public

# Check subnet status
kubectl get subnet ext-public -o yaml
```

### Test connectivity
```bash
# Test from pod
kubectl exec -n [namespace] [pod] -- wget -qO- http://httpbin.org/ip
```

## Qualify client source restrictions

Qualify direct LoadBalancer, Gateway and FIP support separately. Schema presence
and controller Ready pods do not establish enforcement. Preserve client source
addresses on the actual ingress path; Envoy must authorize the socket peer or
an explicitly trusted proxy chain, rather than arbitrary forwarding headers.
The exact source-preservation configuration depends on the address layer and
its local endpoint availability.

The platform chart defaults `manager.fipSourcePolicy.enabled`,
`manager.webhook.protectExposureRoutes`, `backend.exposureSourcePolicy` methods,
and `backend.managedK8sPublicAPISourcePolicy` to disabled. Before publishing
availability, install matching controller/CRD/backend versions, configure exact
producer ServiceAccount identities, protect shared Gateway attachments and
controller acknowledgments, and qualify allowed/denied traffic, tightening,
return traffic, update/removal, restart, deletion and address reuse. FIP needs
router-port ACL support and verified NAT/chassis convergence. HTTP-01 needs the
bounded cert-manager identity, Service certificate ownership and reviewed
platform namespace UID pins, including renewal acceptance under the guard.

### Preserve shared Gateway hostname ownership

Service-generated HTTP, HTTPS, and TLS passthrough share an atomic hostname
reservation ledger on the platform Gateway. Records bind namespace, Service name,
and Service UID. Resource-version updates serialize exact and wildcard overlap
checks across controller replicas. Reservations survive certificate and policy
waits, route withdrawal, source-list transitions, and controller restarts.

The controller imports existing UID-proven publications before withdrawing them.
A protected Service marker also discovers cancellation before the first route
exists. Cleanup releases a reservation after fresh owned-route and listener
absence; orphan cleanup uses exact UID ownership and UID/resource-version delete
preconditions. It never transfers a claim to a same-name replacement Service.

Keep tenant Gateway metadata writes denied. Deploy the reservation-aware binary
on every leader-capable controller and serving webhook replica before qualifying
concurrent ownership. Trusted native route producers remain outside this
Service-reservation guarantee and require separate ownership review. Malformed
records and annotation growth above 240 KiB stop new claims; source tightening
still withdraws unsafe forwarding. Do not manually remove ownership records to
resolve a conflict. Remove obsolete exposure through the Service lifecycle and
verify withdrawal. A downgrade to a binary without this ledger requires separate
hostname-ownership qualification.

### Plan shared Gateway listener capacity

The Gateway API schema permits at most 64 listeners on one Gateway. Count
platform listeners, dynamic HTTPS listeners and dedicated restricted TLS
listeners together. Tenant admission preflights allocation and the controller
checks capacity again on every optimistic update retry. A full Gateway rejects
a new listener with `platform Gateway listener capacity exhausted (64/64)`; it
does not fall back to unrestricted forwarding. Admission does not reserve a
slot, so a competing allocation can still fail during reconciliation.

Existing owned listeners can be edited at capacity; a mode change that allocates
another listener can be rejected. Metadata updates,
certificate renewal and exposure removal do not need another slot. Remove
unused exposure through its owning Service and verify listener cleanup. Do not
manually delete active listeners or their ownership annotations to free capacity.
Automatic sharding is not implemented. Provide separately qualified Gateway
capacity before onboarding more listener-based exposures; tenant-owned Gateways
require their own admission and source-policy qualification. Do not fill the
shared live Gateway to test exhaustion.

### Qualify Floating IP restrictions

For a bounded FIP qualification, set `manager.fipSourcePolicy.namespaceUIDs`
to an object mapping each qualification namespace to its live Kubernetes UID.
Admission and NAT publication verify that exact namespace incarnation through
the API. Restrictive requests outside the scope fail; losing the scope withdraws
the owned NAT. Open FIPs remain unrestricted and can still be created or edited.
The scope has no enabling effect while `manager.fipSourcePolicy.enabled` is
false. An empty object with the gate true retains installation-wide enablement.
Do not use an empty scope for a bounded qualification.

Deploy the scope-aware manager and chart with the gate false first. Verify the
immutable image and exact UID map on every manager/webhook replica before
separately reviewing the gate change. An older replica ignores the scope and
can admit restrictions outside it if enablement accompanies the image rollout.
Keep backend capabilities off until their traffic and authenticated console
acceptance pass. Scope removal and gate rollback require the restricted-resource
inventory and withdrawal procedure described here.

After bounded qualification, delete owned restrictive FIPs first and verify
their actual NAT and ACL/group rows are absent. Remove the remaining owned
workloads and addresses through normal finalizers. Set the gate false while
retaining the nonempty UID map, then verify every serving webhook replica and
the elected controller disabled. Clear the map only in a separate reviewed
change after that verification.

For VM migration acceptance, record the same VMI UID moving between distinct
nodes, denied-client isolation during the move, fresh recovery controls and
allowed-client interruptions. A successful migration does not establish
uninterrupted endpoint availability. Compare large or fragmented traffic with
a working open baseline; a denied fragment without an allowed control does
not qualify filtering.

Backend method availability can be limited to qualified namespaces through
`backend.exposureSourcePolicy.namespaces`; managed API availability has its own
namespace list. Gateway LoadBalancer Services require both direct and Gateway
qualification. Do not publish a generic capability for untested protocols,
database families, VM migrations or shared External worker transport.

Retain image/chart digests, producer commits, dependency versions, ingress
topology and packet evidence for each installation. Platform chart release
preflight requires an explicit clean producer commit and compares its source-
policy schema to the chart. This prevents schema skew; it does not verify the
selected image's provenance or qualify the installation. Repeat packet
qualification when OVN, Envoy, network topology or policy producers change.

Before downgrade or disabling a gate, inventory restricted resources. Keep a
controller that enforces them, or withdraw their external exposure and verify
unreachability before installing an older version. Never delete protection
while leaving a restricted endpoint reachable. A public address quota addon
cannot resolve exhaustion of the physical routed address pool.
