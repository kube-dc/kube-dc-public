# Additional External Network Configuration

This directory contains examples for configuring additional external networks in Kube-OVN alongside the default cloud network.

## Overview

The configuration demonstrates how to add a second external network (public) to an existing Kube-OVN setup that already has a cloud external network, using multiple VLANs on a single physical interface.

## Architecture

```
Physical Interface (enp0s31f6)
├── VLAN 4013 (Cloud Network) - 100.65.0.0/16
└── VLAN 4011 (Public Network) - 168.119.17.48/28
```

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
```

## Configuration Steps

### 1. Apply VLAN Configuration
```bash
kubectl apply -f additional-external-network.yaml
```

### 2. Verify Configuration
```bash
# Check ProviderNetwork VLANs
kubectl get provider-network ext-cloud -o jsonpath='{.status.vlans}'

# Check OVS bridge configuration
kubectl exec -n kube-system [ovs-pod] -- ovs-vsctl show | grep -A 10 "br-ext-cloud"

# Check OVN logical switches
kubectl exec -n kube-system [ovn-central-pod] -- ovn-nbctl ls-list | grep ext
```

### 3. Test with Project
Create a project with `egressNetworkType: public` to use the new external network:
```yaml
apiVersion: kube-dc.com/v1
kind: Project
metadata:
  name: test-project
  namespace: test-org
spec:
  cidrBlock: 10.200.0.0/24
  egressNetworkType: public  # Uses ext-public subnet
```

## Key Points

1. **Single ProviderNetwork**: Use one ProviderNetwork per physical interface with multiple VLANs attached
2. **Automatic Configuration**: OVS bridges, patch ports, and OVN logical switches are created automatically
3. **VLAN Trunking**: The physical interface supports multiple VLANs simultaneously
4. **No Manual OVS Changes**: All OVS/OVN modifications are handled by Kube-OVN controllers

## Prerequisites

- Physical network infrastructure supporting VLAN trunking
- Hetzner vSwitch configured with appropriate VLAN IDs
- Public subnet properly configured in Hetzner Robot
- Existing Kube-OVN installation with ProviderNetwork

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
