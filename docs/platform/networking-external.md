import {ExternalNetworksDiagram} from '@site/src/components/Diagram/PlatformTopologyDiagrams';

# Additional External Network Configuration

This guide explains how to add additional external networks to Kube-DC alongside the default cloud network.

## Overview

The configuration demonstrates how to add a second external network (public) to an existing Kube-DC setup that already has a cloud external network, using multiple VLANs on a single physical interface per node.

## Network Types Explained by Example

### Cloud Network (`egressNetworkType: cloud`)
- **Purpose**: Default external network for most workloads
- **Subnet**: `ext-cloud` (100.65.0.0/16) on VLAN 200
- **Use Cases**: 
  - General internet access for applications
  - Standard egress traffic from project workloads
  - Cost-effective external connectivity
- **IP Pool**: Large address space (65,000+ IPs available)

### Public Network (`egressNetworkType: public`)
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
> segments (tagged or untagged — `EXT_NET_VLAN_ID=0` is supported when the
> carrier NIC *is* the segment). Tenant EIP/FIP reachability is ARP-based
> and needs that L2 adjacency. The **platform ingress VIPs** announced by
> MetalLB can alternatively be advertised over **BGP** (`METALLB_MODE=bgp`)
> for fabrics with no shared L2 — see the installation guide's
> "BGP announcement" section.

## Example Cluster Usage

- **demo-cloud project**: Uses `egressNetworkType: cloud` → EIP: 100.65.0.102 (development/testing)
- **demo-public project**: Uses `egressNetworkType: public` → EIP: 192.0.2.6 (development with public access)
- **demo-envoy project**: Uses `egressNetworkType: public` → EIPs: 192.0.2.7, 192.0.2.8 (production load balancer)

### Choosing the Right Network

**Use Cloud Network when:**
- Need basic internet connectivity
- Don't require specific public IP ranges

**Use Public Network when:**
- Need dedicated public IP addresses
- Have specific routing or compliance requirements
- Running load balancers or ingress controllers

## OVS/OVN resources generated

### 1. OVS Bridge Configuration
With the physical NIC already configured as a VLAN trunk, Kube-OVN creates the following host OVS resources from the fleet manifests:

**Bridge: `br-ext-cloud`**
- Physical interface `bond0` attached with VLAN trunking
- Trunk VLANs: `[0, 300, 200]`
- Patch ports for both external networks:
  - `patch-localnet.ext-cloud-to-br-int` ↔ `patch-br-int-to-localnet.ext-cloud`
  - `patch-localnet.ext-public-to-br-int` ↔ `patch-br-int-to-localnet.ext-public`

### 2. OVN Logical Switches
Two logical switches are created automatically:
- `ext-cloud` (for VLAN 200)
- `ext-public` (for VLAN 300)

### 3. ProviderNetwork Status
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


### 1. Apply VLAN Configuration
```bash
kubectl apply -f examples/networking/additional-external-network.yaml
```

### 2. Verify Configuration
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
(`--preset cloud+public-vlan`, `METALLB_MODE=l2`), the public CIDR is
partitioned by a fixed contract. Example for a `/28`
(`EXT_PUBLIC_CIDR=192.0.2.0/28`):

| Address | Role |
|---|---|
| `192.0.2.1` | Public VLAN gateway (`EXT_PUBLIC_GATEWAY`) |
| `192.0.2.2` | MetalLB floating ingress VIP (`METALLB_FLOATING_IP`) |
| `192.0.2.3-.5` | **Per-node anchor addresses** — one per gateway node (`EXT_NET_PUBLIC_ANCHOR_IPS`) |
| `192.0.2.6-.14` | Tenant pool: public EIPs and per-project VPC router ports (LRPs) |

The current `kube-dc bootstrap init` derives the anchors (VIP+1, VIP+2, …)
and writes `EXT_PUBLIC_EXCLUDE_IPS_1/2` so gateway + VIP + anchors are
reserved in kube-ovn IPAM. Three rules are load-bearing:

1. **Anchors must hold addresses.** MetalLB's ARP responder needs no
   address, so an address-less announcement *looks* alive — ARP resolves
   and TCP connects — but the reply routes out the node's default
   (management VLAN), asymmetric through the datacenter's stateful
   firewall, which drops it. Clients see accept-then-timeout. The
   fleet's `ext-net-bridge-tag` DaemonSet binds each node's anchor and a
   policy route (`from <VIP> lookup 129`, default via the public
   gateway) continuously, so the setting survives reboots and kube-ovn
   bridge recreation. The shared `L2Advertisement` selects the same
   `ovn.kubernetes.io/external-gw=true` nodes. This selector is load-bearing:
   MetalLB's `interfaces` field filters interfaces but does not constrain leader
   election, so an unanchored worker must not be eligible.
2. **Reserve before the first tenant.** kube-ovn honors `excludeIps`
   for **new** allocations only — an EIP or VPC router port that grabbed
   an address before the exclusion keeps it, and the host and OVN then
   both answer ARP for one IP on one segment. If an anchor IP ever
   resolves to two MACs, audit `kubectl get ovn-eip -o wide` for a
   pre-exclusion allocation and re-home it (detach/re-attach the VPC's
   external subnet — fresh allocations honor the exclusion).
3. **Test the VIP with SNI, from off the node.** `curl https://<VIP>/`
   gets a TCP handshake and then an Envoy reset (no SNI filter-chain
   match) — indistinguishable from a broken VIP. Use
   `curl --resolve console.<domain>:443:<VIP> https://console.<domain>/`.
   And never test from a cluster node: kube-proxy intercepts
   LoadBalancer IPs in the OUTPUT path, so node-originated probes never
   reach the wire.

## Key Points

1. **Single ProviderNetwork**: Use one ProviderNetwork per physical interface with multiple VLANs attached
2. **Automatic Configuration**: OVS bridges, patch ports, and OVN logical switches are created automatically
3. **VLAN Trunking**: The physical interface supports multiple VLANs simultaneously
4. **GitOps-owned host state**: Kube-OVN and the `ext-net-bridge-tag` DaemonSet own the host OVS ports; the operator still owns the physical switch trunk and upstream routing

## Prerequisites

- Physical network infrastructure supporting VLAN trunking
- vSwitch configured with appropriate VLAN IDs

## Troubleshooting

### Check VLAN Interface on Nodes
```bash
# On cluster nodes
ip link show bond0.300
ip addr show bond0.300
```

### Check OVN Resources
```bash
# Check OVN-EIP resources
kubectl get ovn-eip | grep ext-public

# Check subnet status
kubectl get subnet ext-public -o yaml
```

### Test Connectivity
```bash
# Test from pod
kubectl exec -n [namespace] [pod] -- wget -qO- http://httpbin.org/ip
```
