# Additional External Network Configuration

This guide explains how to add additional external networks to Kube-DC alongside the default cloud network.

## Overview

The configuration demonstrates how to add a second external network (public) to an existing Kube-DC setup that already has a cloud external network, using multiple VLANs on a single physical interface per node.

## Network Types Explained by Example

### Cloud Network (`egressNetworkType: cloud`)
- **Purpose**: Default external network for most workloads
- **Subnet**: `ext-cloud` (100.65.0.0/16) on VLAN 4013
- **Use Cases**: 
  - General internet access for applications
  - Standard egress traffic from project workloads
  - Cost-effective external connectivity
- **IP Pool**: Large address space (65,000+ IPs available)

### Public Network (`egressNetworkType: public`)
- **Purpose**: Premium external network for specialized workloads
- **Subnet**: `ext-public` (168.119.17.48/28) on VLAN 4011
- **Use Cases**:
  
  - Production services requiring dedicated public IPs
  - Load balancers and ingress controllers
  - Services needing specific public IP ranges or routing
- **IP Pool**: Limited address space with real ipv4 addresses (16 IPs total)

## Architecture

```
Physical Interface (enp0s31f6)
├── VLAN 4013 (Cloud Network) - 100.65.0.0/16 (ext-cloud)
└── VLAN 4011 (Public Network) - 168.119.17.48/28 (ext-public)
```

## Example Cluster Usage

- **shalb-demo project**: Uses `egressNetworkType: cloud` → EIP: 100.65.0.102 (development/testing)
- **shalb-dev project**: Uses `egressNetworkType: public` → EIP: 168.119.17.51 (development with public access)
- **shalb-envoy project**: Uses `egressNetworkType: public` → EIPs: 168.119.17.52, 168.119.17.54 (production load balancer)

### Choosing the Right Network

**Use Cloud Network when:**
- Need basic internet connectivity
- Don't require specific public IP ranges

**Use Public Network when:**
- Need dedicated public IP addresses
- Have specific routing or compliance requirements
- Running load balancers or ingress controllers

**Note on Cross-Type Usage:**
You can create a project with `egressNetworkType: cloud` and still use public EIPs for Service LoadBalancers. Kube-DC automatically handles routing via VPC policy routes and sticky subnet selection to ensure proper connectivity.

## OVS/OVN Modifications Applied

### 1. OVS Bridge Configuration
The system automatically creates the necessary OVS infrastructure:

**Bridge: `br-ext-cloud`**
- Physical interface `enp0s31f6` attached with VLAN trunking
- Trunk VLANs: `[0, 4011, 4013]`
- Patch ports for both external networks:
  - `patch-localnet.ext-cloud-to-br-int` ↔ `patch-br-int-to-localnet.ext-cloud`
  - `patch-localnet.ext-public-to-br-int` ↔ `patch-br-int-to-localnet.ext-public`

### 2. OVN Logical Switches
Two logical switches are created automatically:
- `ext-cloud` (for VLAN 4013)
- `ext-public` (for VLAN 4011)

### 3. ProviderNetwork Status
The existing ProviderNetwork `ext-cloud` is updated to include both VLANs:
```yaml
status:
  vlans: ["vlan4013", "vlan4011"]
  ready: true
  readyNodes:
  - kube-dc-master-1
  - kube-dc-worker-1
```

## Configuration Steps

### 1. Apply VLAN Configuration
```bash
kubectl apply -f examples/networking/additional-external-network.yaml
```

### 2. Verify Configuration
```bash
# Check ProviderNetwork VLANs
kubectl get provider-network ext-cloud -o jsonpath='{.status.vlans}'
# Expected output: ["vlan4013","vlan4011"]

# Check external subnets
kubectl get subnets ext-cloud ext-public
# Expected: ext-cloud (100.65.0.0/16) and ext-public (168.119.17.48/28)

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
  egressNetworkType: public  # Uses ext-public subnet (168.119.17.48/28)
```

## Key Points

1. **Single ProviderNetwork**: Use one ProviderNetwork per physical interface with multiple VLANs attached
2. **Automatic Configuration**: OVS bridges, patch ports, and OVN logical switches are created automatically
3. **VLAN Trunking**: The physical interface supports multiple VLANs simultaneously
4. **No Manual OVS Changes**: All OVS/OVN modifications are handled by Kube-DC controllers

## Prerequisites

- Physical network infrastructure supporting VLAN trunking
- vSwitch configured with appropriate VLAN IDs

## Troubleshooting

### Check VLAN Interface on Nodes
```bash
# On cluster nodes
ip link show enp0s31f6.4011
ip addr show enp0s31f6.4011
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