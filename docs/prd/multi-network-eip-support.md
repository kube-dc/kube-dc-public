# PRD: Multi-Network EIP Support

## Overview

This document describes the architecture and capabilities for projects to use External IPs (EIPs) from multiple external networks (e.g., `public` and `cloud`) simultaneously.

## Problem Statement

Organizations often need workloads to use different types of external IPs:
- **Public IPs**: Real IPv4 addresses accessible from the internet (limited, expensive)
- **Cloud IPs**: Internal cloud network IPs for inter-project communication (abundant, cost-effective)

Currently, a project's `egressNetworkType` determines which external network is used for:
1. Default gateway SNAT (outbound traffic)
2. Default EIP allocation

**Question**: Can a project mix IPs from both networks?

## Current Implementation Analysis

### Architecture

```
                     ┌─────────────────────────────────────────────────────┐
                     │                    Project VPC                      │
                     │                                                     │
                     │  ┌─────────────┐     ┌─────────────────────────┐   │
                     │  │   Subnet    │     │      OvnSnatRule        │   │
                     │  │ 10.x.x.0/24 │────▶│ V4IpCidr: 10.x.x.0/24   │   │
                     │  └─────────────┘     │ OvnEip: vpc-ext-{net}   │   │
                     │                      └───────────┬─────────────┘   │
                     │                                  │                 │
                     └──────────────────────────────────┼─────────────────┘
                                                        │
                          ┌─────────────────────────────┼─────────────────────────────┐
                          │                             ▼                             │
              ┌───────────┴───────────┐     ┌───────────────────────┐     ┌──────────┴──────────┐
              │      ext-public       │     │       OvnEip          │     │      ext-cloud      │
              │   91.224.11.0/24      │◀────│   (allocated from     │────▶│   100.65.0.0/16     │
              │      VLAN 883         │     │    external subnet)   │     │      VLAN 884       │
              └───────────────────────┘     └───────────────────────┘     └─────────────────────┘
```

### Kube-OVN Resources

| Resource | Purpose | Key Fields |
|----------|---------|------------|
| `OvnEip` | External IP allocation | `ExternalSubnet`, `V4Ip`, `Type` |
| `OvnSnatRule` | SNAT for outbound traffic | `OvnEip`, `VpcSubnet`, `V4IpCidr` |
| `OvnFipRule` | Floating IP for inbound traffic | `OvnEip`, `V4Ip` (internal) |

### Kube-DC Resources

| Resource | Purpose | Network Selection |
|----------|---------|-------------------|
| `Project` | Project definition | `spec.egressNetworkType` (immutable) |
| `EIp` | EIP abstraction | `spec.externalNetworkType` |
| `FIp` | Floating IP | `spec.externalNetworkType` |

## Current Capabilities

### ✅ Supported: Different Network Types per Resource

Each resource can independently specify its network type:

```yaml
# Project with cloud SNAT (default egress)
apiVersion: kube-dc.com/v1
kind: Project
metadata:
  name: my-project
  namespace: my-org
spec:
  cidrBlock: 10.100.0.0/24
  egressNetworkType: cloud  # Default SNAT uses cloud network
---
# FIP using public network (different from project default)
apiVersion: kube-dc.com/v1
kind: FIp
metadata:
  name: web-server-fip
  namespace: my-org-my-project
spec:
  vmTarget:
    vmName: web-server
  externalNetworkType: public  # This FIP uses public network
---
# Another FIP using cloud network
apiVersion: kube-dc.com/v1
kind: FIp
metadata:
  name: internal-service-fip
  namespace: my-org-my-project
spec:
  vmTarget:
    vmName: internal-service
  externalNetworkType: cloud  # This FIP uses cloud network
```

### ✅ Supported: Multiple EIPs per Project

Projects can have multiple EIPs from different networks:

```yaml
# Default gateway EIP (created automatically based on project.spec.egressNetworkType)
apiVersion: kube-dc.com/v1
kind: EIp
metadata:
  name: default-gw
  namespace: my-org-my-project
spec:
  externalNetworkType: cloud
---
# Additional EIP for load balancer (public network)
apiVersion: kube-dc.com/v1
kind: EIp
metadata:
  name: lb-public
  namespace: my-org-my-project
spec:
  externalNetworkType: public
```

### ⚠️ Limitation: Single Default SNAT Network

The default SNAT rule uses one network type (from `project.spec.egressNetworkType`). This is immutable after project creation.

**Workaround**: Create additional SNAT rules for specific source CIDRs if needed (advanced use case).

## Resource Flow

### EIP Allocation Flow

```
User creates EIp with externalNetworkType=public
           │
           ▼
┌─────────────────────────────────┐
│     EIP Controller              │
│  1. Find ext-public subnet      │
│  2. Find free OvnEip or create  │
│  3. Label OvnEip with EIp ref   │
│  4. Update EIp status           │
└─────────────────────────────────┘
           │
           ▼
OvnEip allocated from ext-public: 91.224.11.x
```

### FIP Allocation Flow

```
User creates FIp with externalNetworkType=public
           │
           ▼
┌─────────────────────────────────┐
│     FIP Controller              │
│  1. Resolve VM target IP        │
│  2. Create/find EIp (public)    │
│  3. Create OvnFipRule           │
│  4. Update FIp status           │
└─────────────────────────────────┘
           │
           ▼
VM internal IP ←→ Public external IP (bidirectional NAT)
```

## Configuration

### External Networks Setup

Two external networks configured on the same ProviderNetwork:

```yaml
# VLAN for public network (real IPv4)
apiVersion: kubeovn.io/v1
kind: Vlan
metadata:
  name: vlan883
spec:
  id: 883
  provider: ext-cloud

---
# Public subnet
apiVersion: kubeovn.io/v1
kind: Subnet
metadata:
  name: ext-public
  labels:
    network.kube-dc.com/external-network-type: public
spec:
  cidrBlock: 91.224.11.0/24
  gateway: 91.224.11.254
  vlan: vlan883
  excludeIps:
    - 91.224.11.1..91.224.11.3
    - 91.224.11.254

---
# VLAN for cloud network (internal)
apiVersion: kubeovn.io/v1
kind: Vlan
metadata:
  name: vlan884
spec:
  id: 884
  provider: ext-cloud

---
# Cloud subnet
apiVersion: kubeovn.io/v1
kind: Subnet
metadata:
  name: ext-cloud
  labels:
    network.kube-dc.com/external-network-type: cloud
spec:
  cidrBlock: 100.65.0.0/16
  gateway: 100.65.0.1
  vlan: vlan884
```

### Master Config Defaults

```yaml
apiVersion: v1
kind: ConfigMap
metadata:
  name: kube-dc-master-config
data:
  config: |
    default_gw_network_type: cloud
    default_eip_network_type: cloud
    default_fip_network_type: public
    default_svc_lb_network_type: public
```

## Use Cases

### Use Case 1: Web Application with Public Frontend

**Scenario**: Project needs public IP for web server, cloud IP for backend services.

```yaml
# Project uses cloud for default SNAT (backend services)
apiVersion: kube-dc.com/v1
kind: Project
spec:
  egressNetworkType: cloud

---
# Public FIP for web server VM
apiVersion: kube-dc.com/v1
kind: FIp
metadata:
  name: web-fip
spec:
  vmTarget:
    vmName: web-server
  externalNetworkType: public
```

**Result**:
- Web server: Accessible via public IP (91.224.11.x)
- Backend services: Egress via cloud IP (100.65.x.x)

### Use Case 2: Development vs Production IPs

**Scenario**: Use cloud IPs for development, public IPs for production workloads.

```yaml
# Dev FIP (cloud network)
apiVersion: kube-dc.com/v1
kind: FIp
metadata:
  name: dev-app-fip
spec:
  vmTarget:
    vmName: dev-app
  externalNetworkType: cloud

---
# Prod FIP (public network)
apiVersion: kube-dc.com/v1
kind: FIp
metadata:
  name: prod-app-fip
spec:
  vmTarget:
    vmName: prod-app
  externalNetworkType: public
```

### Use Case 3: LoadBalancer Service with Public IP

**Scenario**: Kubernetes service in tenant cluster needs public LoadBalancer IP.

```yaml
# Service with public LoadBalancer
apiVersion: v1
kind: Service
metadata:
  name: my-lb
  annotations:
    service.nlb.kube-dc.com/external-network-type: public
spec:
  type: LoadBalancer
  ports:
    - port: 443
```

## Verification

### Test: Public Network EIP Allocation

```bash
# Create project with public egress
kubectl apply -f - <<EOF
apiVersion: kube-dc.com/v1
kind: Project
metadata:
  name: public-test
  namespace: test
spec:
  cidrBlock: 10.50.0.0/24
  egressNetworkType: public
EOF

# Verify EIP from public network
kubectl get eip -n test-public-test
# NAME         EXTERNAL IP   READY
# default-gw   91.224.11.5   true

# Verify SNAT rule
kubectl get ovn-snat-rules | grep public-test
# test-public-test   test-public-test   91.224.11.4   10.50.0.0/24   true
```

### Test: Mixed Network FIPs in Same Project

```bash
# Create FIP with public network
kubectl apply -f - <<EOF
apiVersion: kube-dc.com/v1
kind: FIp
metadata:
  name: public-vm-fip
  namespace: test-public-test
spec:
  ipAddress: 10.50.0.100
  externalNetworkType: public
EOF

# Create FIP with cloud network (different from project default)
kubectl apply -f - <<EOF
apiVersion: kube-dc.com/v1
kind: FIp
metadata:
  name: cloud-vm-fip
  namespace: test-public-test
spec:
  ipAddress: 10.50.0.101
  externalNetworkType: cloud
EOF

# Verify both FIPs allocated from correct networks
kubectl get fip -n test-public-test
# NAME             TARGET IP    EXTERNAL IP     READY
# public-vm-fip    10.50.0.100  91.224.11.x     true
# cloud-vm-fip     10.50.0.101  100.65.x.x      true
```

## Test Results (2026-01-20)

### Test Environment
- **Cluster**: kube-dc.cloud
- **Networks**: ext-cloud (100.65.0.0/16, VLAN 884), ext-public (91.224.11.0/24, VLAN 883)

### Test: Public Network Project
```bash
# Created project with public egress
kubectl apply -f - <<EOF
apiVersion: kube-dc.com/v1
kind: Project
metadata:
  name: public-test
  namespace: test
spec:
  cidrBlock: 10.50.0.0/24
  egressNetworkType: public
EOF

# Results:
# - EIP: 91.224.11.5 (from ext-public) ✅
# - SNAT: 91.224.11.4 → 10.50.0.0/24 ✅
```

### Test: Mixed Network FIPs
```bash
# Created FIPs with different network types in same project
# Public FIP:
kubectl get ovn-fip test-public-test-test-public-fip
# V4EIP: 91.224.11.6, V4IP: 10.50.0.100, READY: true ✅

# Cloud FIP:
kubectl get ovn-fip test-public-test-test-cloud-fip  
# V4EIP: 100.65.0.112, V4IP: 10.50.0.101, READY: true ✅
```

### Known Issue: FIP Status Sync
The FIP controller creates the OvnFip correctly but doesn't always sync the external IP back to the FIp resource status. The OvnFip is functional but `fip.status.externalIP` may be empty.

**Workaround**: Check `ovn-fip` resources directly for actual external IP assignment.

## Summary

| Feature | Supported | Notes |
|---------|-----------|-------|
| Project default SNAT from specific network | ✅ | Via `project.spec.egressNetworkType` |
| EIP from different network than project default | ✅ | Via `eip.spec.externalNetworkType` |
| FIP from different network than project default | ✅ | Via `fip.spec.externalNetworkType` |
| LoadBalancer from different network | ✅ | Via service annotation |
| Multiple FIPs from different networks in same project | ✅ | Each FIP specifies its network |
| Multiple SNAT rules for different source CIDRs | ⚠️ | Requires manual OvnSnatRule creation |
| Changing project default network after creation | ❌ | `egressNetworkType` is immutable |
| FIP status sync | ⚠️ | OvnFip works, but FIp status may not reflect external IP |

## References

- Kube-OVN v1.15.0 Release Notes
- [Kube-OVN EIP/SNAT Documentation](https://kubeovn.github.io/docs/)
- Kube-DC EIP Controller: `internal/eip/`
- Kube-DC FIP Controller: `internal/fip/`
