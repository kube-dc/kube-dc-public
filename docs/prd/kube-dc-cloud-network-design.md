# Kube-DC Cloud Network Design

## Overview

Network architecture for kube-dc.cloud using **Kube-OVN v1.15.0** underlay networking features.

---

## Kube-OVN v1.15.0 Key Features

| Feature | PR | Benefit |
|---------|----|---------| 
| **Node Selectors for ProviderNetwork** | #5518 | Use labels instead of per-node config |
| **Shared VLAN across ProviderNetworks** | #5471 | Multiple networks on same VLAN |
| **Auto create VLAN sub-interfaces** | #5966 | No manual VLAN interface creation |
| **Auto move VLAN to OVS bridges** | #5949 | Automatic OVS integration |

**Key insight**: With v1.15.0, **Kube-OVN auto-creates VLAN interfaces** - no need to configure them in netplan!

---

## Physical Interface Mapping

| Node | Hostname | Primary Interface | Public IP |
|------|----------|-------------------|-----------|
| Master 1 | ams1-blade179-8 | `enp94s0f0np0` | 213.111.154.233 |
| Master 2 | ams1-blade184-5 | `enp94s0f0np0` | 213.111.154.229 |
| Master 3 | ams1-blade58-2 | `enp8s0f0` | 213.111.154.223 |
| Worker | ams1-blade187-2 | `eno1` | 213.111.154.234 |

### Interface Name Differences

Three different interface naming patterns:
- `enp94s0f0np0` - Intel/Mellanox NICs (Master 1, 2)
- `enp8s0f0` - Standard PCIe NIC (Master 3)
- `eno1` - Onboard NIC (Worker)

---

## Network Architecture

### VLAN Layout

| VLAN | Network | Purpose | Managed By |
|------|---------|---------|------------|
| 885 | 192.168.0.0/18 | K8s Internal (etcd, kubelet) | Netplan |
| 884 | 100.65.0.0/16 | Cloud Network (VMs, Pods) | Kube-OVN |

### K8s Internal Network (VLAN 885)

| Node | Public IP | Private IP |
|------|-----------|------------|
| Master 1 | 213.111.154.233 | 192.168.0.2 |
| Master 2 | 213.111.154.229 | 192.168.0.3 |
| Master 3 | 213.111.154.223 | 192.168.0.4 |
| Worker 1 | 213.111.154.234 | 192.168.0.10 |

### Network Diagram

```
┌─────────────────────────────────────────────────────────────────┐
│                        Physical Switch                          │
│         VLAN 885 (K8s Internal)  │  VLAN 884 (Cloud Network)   │
└─────────────┬───────────────┬────┴──────────┬───────────────┬───┘
              │               │               │               │
         ┌────┴────┐     ┌────┴────┐     ┌────┴────┐     ┌────┴────┐
         │Master 1 │     │Master 2 │     │Master 3 │     │ Worker  │
         │.233     │     │.229     │     │.223     │     │.234     │
         │192.168. │     │192.168. │     │192.168. │     │192.168. │
         │  0.2    │     │  0.3    │     │  0.4    │     │  0.10   │
         └────┬────┘     └────┬────┘     └────┬────┘     └────┬────┘
              │               │               │               │
              └───────────────┴───────────────┴───────────────┘
                                    │
                    ┌───────────────┴───────────────┐
                    │                               │
           ┌────────┴────────┐            ┌────────┴────────┐
           │   VLAN 885      │            │   Kube-OVN      │
           │  K8s Internal   │            │ ProviderNetwork │
           │ 192.168.0.0/18  │            │  VLAN 884       │
           │  (via netplan)  │            │ 100.65.0.0/16   │
           └─────────────────┘            └─────────────────┘
```

---

## Netplan Configuration

### Host Netplan Configuration

Each node has:
- Public IP on physical interface
- VLAN 885 for K8s internal traffic (configured in netplan)
- VLAN 884 for cloud network (managed by Kube-OVN, NOT in netplan)

**Master 1** (`/etc/netplan/01-netcfg.yaml`):
```yaml
network:
  version: 2
  renderer: networkd
  ethernets:
    enp94s0f0np0:
      addresses: [213.111.154.233/23]
      routes:
        - to: default
          via: 213.111.154.1
  vlans:
    vlan885:
      id: 885
      link: enp94s0f0np0
      addresses: [192.168.0.2/18]
```

**Master 2**:
```yaml
network:
  version: 2
  renderer: networkd
  ethernets:
    enp94s0f0np0:
      addresses: [213.111.154.229/23]
      routes:
        - to: default
          via: 213.111.154.1
  vlans:
    vlan885:
      id: 885
      link: enp94s0f0np0
      addresses: [192.168.0.3/18]
```

**Master 3**:
```yaml
network:
  version: 2
  renderer: networkd
  ethernets:
    enp8s0f0:
      addresses: [213.111.154.223/23]
      routes:
        - to: default
          via: 213.111.154.1
  vlans:
    vlan885:
      id: 885
      link: enp8s0f0
      addresses: [192.168.0.4/18]
```

**Worker**:
```yaml
network:
  version: 2
  renderer: networkd
  ethernets:
    eno1:
      addresses: [213.111.154.234/23]
      routes:
        - to: default
          via: 213.111.154.1
  vlans:
    vlan885:
      id: 885
      link: eno1
      addresses: [192.168.0.10/18]
```

> **Note**: VLAN 884 is NOT configured in netplan - Kube-OVN manages it automatically.

---

## Kube-OVN ProviderNetwork Configuration

### Using customInterfaces for Different NIC Names

The cluster has **three different interface naming patterns** - Kube-OVN handles this with `customInterfaces`:

```yaml
apiVersion: kubeovn.io/v1
kind: ProviderNetwork
metadata:
  name: ext-cloud
spec:
  # Default interface for most nodes (Master 1, 2)
  defaultInterface: enp94s0f0np0
  
  # Custom interfaces for nodes with different NIC names
  customInterfaces:
    - interface: enp8s0f0
      nodes:
        - ams1-blade58-2      # Master 3
    - interface: eno1
      nodes:
        - ams1-blade187-2     # Worker
  
  # NEW in v1.15.0: Auto-create VLAN subinterfaces
  # When combined with customInterfaces, creates VLAN interfaces
  # on each node's specific NIC (e.g., enp94s0f0np0.884, enp8s0f0.884, eno1.884)
  autoCreateVlanSubinterfaces: true
  
  # NEW in v1.15.0: Preserve/migrate existing VLAN configs to OVS
  # Transfers IPs and routes from kernel VLAN interfaces to OVS ports
  preserveVlanInterfaces: true
  
  # Alternative: Use node selectors (v1.15.0 feature)
  # nodeSelector:
  #   matchLabels:
  #     node-role.kubernetes.io/control-plane: ""
```

### How customInterfaces + autoCreateVlanSubinterfaces Works

When both are configured, Kube-OVN:

1. **Resolves interface per node**:
   - Master 1, 2 → `enp94s0f0np0` (defaultInterface)
   - Master 3 → `enp8s0f0` (from customInterfaces)
   - Worker → `eno1` (from customInterfaces)

2. **Auto-creates VLAN subinterfaces on each node**:
   - Master 1, 2 → `enp94s0f0np0.884`
   - Master 3 → `enp8s0f0.884`
   - Worker → `eno1.884`

3. **Moves VLAN interfaces to OVS bridges** with `preserveVlanInterfaces: true`:
   - Transfers IPs from kernel VLAN interface to OVS internal port
   - Preserves routing table entries
   - Eliminates duplicate IP/route issues

### VLAN Configuration

```yaml
apiVersion: kubeovn.io/v1
kind: Vlan
metadata:
  name: vlan884
spec:
  id: 884
  provider: ext-cloud
```

### Subnet Configuration

```yaml
apiVersion: kubeovn.io/v1
kind: Subnet
metadata:
  name: ext-cloud
spec:
  protocol: IPv4
  cidrBlock: 100.65.0.0/16
  gateway: 100.65.0.1
  vlan: vlan884
  excludeIps:
    - 100.65.0.1..100.65.0.10  # Reserved for infrastructure
```

---

## Configuration Approaches Comparison

### Option A: Manual VLAN in Netplan (Traditional)

```yaml
# Netplan creates VLAN interface
vlans:
  enp94s0f0np0.884:
    id: 884
    link: enp94s0f0np0
    addresses:
      - 100.65.0.2/16
```

**Pros**: Full control, works with any Kube-OVN version  
**Cons**: Must configure on each node, different interface names = different configs

### Option B: Kube-OVN Auto-VLAN (v1.15.0) ✅ Recommended

```yaml
# ProviderNetwork with customInterfaces + auto VLAN features
spec:
  defaultInterface: enp94s0f0np0
  customInterfaces:
    - interface: enp8s0f0
      nodes: [ams1-blade58-2]
    - interface: eno1
      nodes: [ams1-blade187-2]
  autoCreateVlanSubinterfaces: true   # Creates VLAN interfaces automatically
  preserveVlanInterfaces: true        # Migrates IPs/routes to OVS
```

**Pros**: 
- Single configuration point (ProviderNetwork CRD)
- Handles different NIC names elegantly via `customInterfaces`
- Auto creates VLAN sub-interfaces on the **correct NIC per node**
- Auto integrates with OVS bridges
- Properly migrates IPs and routes (no duplicates)
- Works across nodes with different hardware

**Cons**: Requires Kube-OVN v1.15.0+

### Interface Resolution Flow

```
ProviderNetwork.spec.customInterfaces lookup:
  ├── Node: ams1-blade179-8 → Not in customInterfaces → use defaultInterface: enp94s0f0np0
  ├── Node: ams1-blade184-5 → Not in customInterfaces → use defaultInterface: enp94s0f0np0  
  ├── Node: ams1-blade58-2  → Found in customInterfaces → use: enp8s0f0
  └── Node: ams1-blade187-2 → Found in customInterfaces → use: eno1

With autoCreateVlanSubinterfaces=true + Vlan.spec.id=884:
  ├── ams1-blade179-8: Creates enp94s0f0np0.884
  ├── ams1-blade184-5: Creates enp94s0f0np0.884
  ├── ams1-blade58-2:  Creates enp8s0f0.884
  └── ams1-blade187-2: Creates eno1.884
```

---

## Implementation Steps

### Phase 1: Prepare Hosts

1. **Verify netplan has base interface configured** (public IP only)
2. **Remove any manual VLAN configurations** from netplan
3. **Ensure switch ports are configured for VLAN 884 trunking**

### Phase 2: Deploy Kube-OVN v1.15.0

1. Install Kube-OVN with underlay support enabled
2. Apply ProviderNetwork with customInterfaces
3. Apply Vlan and Subnet resources

### Phase 3: Verify

```bash
# Check ProviderNetwork status
kubectl get provider-network ext-cloud -o yaml

# Verify VLAN interfaces created by Kube-OVN
ssh root@<node> 'ip link show | grep 884'

# Check OVS bridge integration
ssh root@<node> 'ovs-vsctl show'
```

---

## Switch Requirements

| Requirement | Status |
|-------------|--------|
| VLAN 884 configured | ⬜ Verify |
| Trunk mode on all node ports | ⬜ Verify |
| Native VLAN for management traffic | ⬜ Verify |
| VLAN 884 allowed on trunk | ⬜ Verify |

---

## IP Allocation

### Cloud Network (100.65.0.0/16)

| Range | Purpose |
|-------|---------|
| 100.65.0.1 | Gateway |
| 100.65.0.2-10 | Reserved (infrastructure) |
| 100.65.0.11-100.65.255.254 | Available for EIPs |

---

## Success Criteria

- [x] All 4 nodes show in ProviderNetwork `readyNodes`
- [x] VLAN 884 sub-interfaces created on all nodes
- [x] OVS bridges configured correctly
- [x] External API access working (issue fixed)
- [ ] Upgrade to Kube-OVN v1.15.0 with new VLAN auto-features
- [ ] Test `autoCreateVlanSubinterfaces` + `customInterfaces` combination
- [ ] Verify no duplicate IPs/routes after upgrade

---

## References

- [Kube-OVN v1.15.0 Release](https://github.com/kubeovn/kube-ovn/releases/tag/v1.15.0)
- [Kube-OVN Underlay Docs](https://kubeovn.github.io/docs/start/underlay/)
- [ProviderNetwork CRD](https://kubeovn.github.io/docs/reference/crd/)
