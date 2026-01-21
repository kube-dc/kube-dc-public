# PRD: EIP Support on Secondary External Networks

## Status: ✅ IMPLEMENTED

**Completed**: 2026-01-21

## Overview

This document describes the implementation for enabling External IPs (EIPs) to work correctly when allocated from a secondary external network (different from the project's default egress network). This affects both **FIPs** and **Service LoadBalancers**.

## Problem Statement

When a project has `egressNetworkType: cloud`, FIPs or LoadBalancer services allocated from `ext-public` (secondary network) fail to respond. The DNAT works (traffic reaches the pod), but return traffic is dropped because OVN's SNAT only triggers when `outport` matches the NAT's gateway port.

```
VPC: test-jump (egressNetworkType: cloud)
├── Default Route: 0.0.0.0/0 → 100.65.0.1 (ext-cloud)
└── FIP NAT: 91.224.11.10 ↔ 10.0.0.7 (on ext-public)

Traffic Flow (Broken):
1. Inbound: 91.224.11.10 → DNAT → 10.0.0.7 ✅
2. Return: 10.0.0.7 → routing → outport = ext-cloud (default route)
3. SNAT check: outport == ext-public? NO → SNAT skipped
4. Traffic exits via ext-cloud with source 10.0.0.7 → DROPPED
```

## Solution: Policy-Based Routing

Add source-based policy routes to override default routing for EIPs on secondary networks.

```bash
ovn-nbctl lr-policy-add test-jump 30000 "ip4.src == 10.0.0.7" reroute 91.224.11.1
```

## Implementation Details

### Files Modified

| File | Changes |
|------|---------|
| `internal/utils/policy_route.go` | New file - PolicyRouteManager for VPC policy route operations |
| `internal/controller/kube-dc.com/fip_controller.go` | Added `syncPolicyRoute()` and `deletePolicyRoute()` |
| `internal/service_lb/service_lb.go` | Added `syncPolicyRoutes()` and `deletePolicyRoutes()` |

### Policy Route Manager (`internal/utils/policy_route.go`)

```go
const (
    PolicyRoutePriorityFIP   int = 30000  // FIP policy routes
    PolicyRoutePrioritySvcLB int = 30010  // SvcLB policy routes (different priority to avoid conflicts)
)

type PolicyRouteManager struct {
    cli      client.Client
    vpcName  string
    priority int
}

// Key methods:
func (m *PolicyRouteManager) SyncPolicyRoute(ctx context.Context, internalIP, gateway string) error
func (m *PolicyRouteManager) DeletePolicyRoute(ctx context.Context, internalIP string) error
func (m *PolicyRouteManager) SyncPolicyRoutes(ctx context.Context, internalIPs []string, gateway string) error
func (m *PolicyRouteManager) DeleteAllPolicyRoutes(ctx context.Context) error

// Helper functions:
func NeedsPolicyRoute(eipSubnetName, projectDefaultSubnetName string) bool
func GetSubnetGateway(ctx context.Context, cli client.Client, subnetName string) (string, error)
func GetOvnEipSubnet(ctx context.Context, cli client.Client, ovnEipName string) (string, error)
```

### FIP Controller Integration

```go
// In reconcileSync(), after FIP is synced:
if err := r.syncPolicyRoute(ctx, reconciledFIp, resourceEIp.Found().Status.OvnEipRef, targetIP, &l); err != nil {
    l.Error(err, "Failed to sync policy route for FIP")
}

// In reconcileDelete():
if err := r.deletePolicyRoute(ctx, reconciledFIp, &l); err != nil {
    l.Error(err, "Failed to delete policy route for FIP")
}

// syncPolicyRoute checks if policy route is needed:
func (r *FIpReconciler) syncPolicyRoute(...) error {
    // Get project's default external subnet
    defaultSubnet, _ := proc.GetDefaultExternalSubnet()
    
    // Get EIP's actual subnet
    eipSubnetName, _ := utils.GetOvnEipSubnet(ctx, r.Client, ovnEipName)
    
    // Only add policy route if EIP is on secondary network
    if !utils.NeedsPolicyRoute(eipSubnetName, defaultSubnet.Name) {
        return nil
    }
    
    // Get gateway and create policy route
    gateway, _ := utils.GetSubnetGateway(ctx, r.Client, eipSubnetName)
    prm := utils.NewPolicyRouteManager(r.Client, vpcName, utils.PolicyRoutePriorityFIP)
    return prm.SyncPolicyRoute(ctx, internalIP, gateway)
}
```

### SvcLB Controller Integration

```go
// In LBResource.Sync(), after LB VIPs are synced:
if err := r.syncPolicyRoutes(ctx, backends); err != nil {
    r.LogDebug("Failed to sync policy routes: %v", err)
}

// In LBResource.Delete():
if err := r.deletePolicyRoutes(ctx); err != nil {
    r.LogDebug("Failed to delete policy routes, continuing: %v", err)
}

// syncPolicyRoutes handles multiple backend IPs:
func (r *LBResource) syncPolicyRoutes(ctx context.Context, backends []string) error {
    needsRoute, gateway, _ := r.checkPolicyRouteNeeded(ctx)
    if !needsRoute {
        return nil
    }
    
    prm := utils.NewPolicyRouteManager(r.Cli, vpcName, utils.PolicyRoutePrioritySvcLB)
    return prm.SyncPolicyRoutes(ctx, backends, gateway)
}
```

### VPC Patching with RetryHandler

Policy routes are stored in `Vpc.Spec.PolicyRoutes` (Kubernetes CRD). Critical: Create RetryHandler BEFORE modifying the VPC to capture original state for proper patch diff:

```go
vpc := &kubeovn.Vpc{}
cli.Get(ctx, client.ObjectKey{Name: m.vpcName}, vpc)

// Create handler BEFORE modifications
vpcUpd := objmgr.NewRetryHandler(m.cli, vpc).WithContext(ctx)

// Now modify
vpc.Spec.PolicyRoutes = append(vpc.Spec.PolicyRoutes, newRoute)

// Patch with proper diff
vpcUpd.WaitPatch()
```

## State Persistence

Policy routes are stored in Kubernetes VPC CRD (etcd), not directly in OVN DB:

```
Kubernetes VPC CRD → Kube-OVN Controller → OVN NB Database
     (etcd)              (watches)           (ovsdb)
```

After any restart, kube-ovn rebuilds OVN state from Kubernetes CRDs automatically.

## Multiple External Networks

The implementation is generic and supports any number of external networks. The gateway is dynamically retrieved from the EIP's actual subnet:

```go
eipSubnetName, _ := GetOvnEipSubnet(ctx, cli, ovnEipName)  // e.g., "ext-public-2"
gateway, _ := GetSubnetGateway(ctx, cli, eipSubnetName)     // e.g., "92.0.0.1"
```

## Test Results

```bash
# VPC policy routes after creating FIP and SvcLB with public EIPs:
$ kubectl get vpc test-jump -o jsonpath='{.spec.policyRoutes}' | jq
[
  {"action": "reroute", "match": "ip4.src == 10.0.0.7",  "nextHopIP": "91.224.11.1", "priority": 30000},
  {"action": "reroute", "match": "ip4.src == 10.0.0.13", "nextHopIP": "91.224.11.1", "priority": 30010},
  {"action": "reroute", "match": "ip4.src == 10.0.0.14", "nextHopIP": "91.224.11.1", "priority": 30010}
]

# Test public SvcLB on cloud project:
$ curl 91.224.11.13:80
<!DOCTYPE html>
<html>
<head><title>Welcome to nginx!</title>
...
```

## Known Limitations

### FIP and Cloud-Network Service Sharing Same Pod IP

**A pod cannot simultaneously serve as:**
1. A target for a **public FIP**
2. A backend for a **cloud-network LoadBalancer** service

**Root Cause**: Policy routes are source-based (`ip4.src == <internal-ip>`), affecting ALL outbound traffic from that IP. A public FIP's policy route breaks cloud-network services using the same pod.

**Workarounds:**
1. Use separate pods for FIP targets and cloud-service backends
2. Use consistent network types (all public or all cloud)
3. Remove conflicting FIP if cloud service access is required

See [tutorial-service-exposure.md](../tutorial-service-exposure.md#️-important-limitation-fip-and-loadbalancer-conflicts) for details.

## Priority Reference

| Priority | Purpose |
|----------|---------|
| 31000 | Allow internal subnet traffic (kube-ovn default) |
| 30010 | SvcLB source-based reroute |
| 30000 | FIP source-based reroute |
| Default | Routing table lookup |

## OVN Commands Reference

```bash
# List policy routes
ovn-nbctl lr-policy-list <router>

# Add policy route manually (for testing)
ovn-nbctl lr-policy-add <router> <priority> <match> reroute <nexthop>

# Delete policy route
ovn-nbctl lr-policy-del <router> <priority> [match]
```
