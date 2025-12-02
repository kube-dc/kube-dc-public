# Service LoadBalancer OVN Sync Issue

## Summary

When kube-ovn-controller restarts (due to OVN database timeouts or other failures), the LoadBalancer VIP entries managed by `kube-dc-manager` are lost from the OVN Northbound database. This causes external services (like tenant cluster API servers) to become unreachable until `kube-dc-manager` is manually restarted.

## Issue Details

### Observed Behavior

**Date:** 2025-12-01

**Symptoms:**
- Tenant cluster control planes unreachable: `dial tcp 168.119.17.55:6443: connect: no route to host`
- All EIP resources show `READY: true`
- OVN EIP resources show `READY: true`
- VMs are running correctly

**Timeline:**
1. `kube-ovn-controller` experienced OVN database connection timeouts:
   ```
   E1201 10:34:42.771547 controller.go:1021] OVN database echo timeout (4/5) after 60s
   E1201 10:35:57.772950 controller.go:1021] OVN database echo timeout (5/5) after 60s
   E1201 10:35:57.773181 klog.go:10] "OVN database connection timeout after 5 attempts"
   ```
2. `kube-ovn-controller` pod restarted at 10:35:58
3. After restart, OVN NB database was missing LoadBalancer entries for:
   - `shalb-envoy-user-a-cp-tcp` (168.119.17.55:6443)
   - `shalb-envoy-user-b-cp-tcp` (168.119.17.53:6443)
   - `shalb-envoy-user-c-cp-tcp` (168.119.17.56:6443)
   - `shalb-envoy-envoy-delta-controller-envoy-tcp` (168.119.17.54:443/10000/9901)

### Root Cause Analysis

```
┌─────────────────────┐     writes to      ┌─────────────────────┐
│  kube-dc-manager    │ ─────────────────→ │   OVN NB Database   │
│  (service_lb.go)    │                    │   (LoadBalancers)   │
└─────────────────────┘                    └─────────────────────┘
                                                    ↑
                                                    │ reconciles
                                                    │
                                           ┌─────────────────────┐
                                           │ kube-ovn-controller │
                                           │                     │
                                           └─────────────────────┘
```

**The Problem:**

1. `kube-dc-manager` writes LoadBalancer VIPs directly to OVN NB database via `ovs.NbClient`
2. `kube-ovn-controller` manages its own OVN resources but does NOT manage `kube-dc-manager`'s LBs
3. When `kube-ovn-controller` restarts, it reconciles resources it knows about
4. `kube-dc-manager`'s LBs are NOT reconciled because:
   - They are not tracked by any Kubernetes resource that `kube-ovn-controller` watches
   - `kube-dc-manager` only reconciles on Endpoints changes, not on OVN state changes

### Affected Code

**`internal/service_lb/service_lb.go`:**

```go
func (r *LBResource) Sync(ctx context.Context) error {
    // Builds VIP maps
    vipListTcp := map[string]string{}
    // ...
    
    // Updates OVN LB directly
    err = r.updateLbs(r.tcpLbName(), ovnnb.LoadBalancerProtocolTCP, vipListTcp)
    
    // Attaches to router and switch
    err = r.ovsCli.LogicalRouterUpdateLoadBalancers(r.projectRouter.Name, ...)
    err = r.ovsCli.LogicalSwitchUpdateLoadBalancers(project.SubnetName(r.project), ...)
}
```

The sync is triggered by:
- Endpoint creation/update
- Service creation/update

**NOT triggered by:**
- OVN database reconnection
- kube-ovn-controller restart
- OVN NB database state changes

## Solutions Considered

| Option | Description | Pros | Cons | Status |
|--------|-------------|------|------|--------|
| Periodic Reconciliation | Full resync every N minutes | Simple | Downtime up to N min, wasteful | ❌ Rejected |
| OVN Connection Monitor | Track connection state | Immediate | Requires OVS client mods | ❌ Rejected |
| K8s Annotation State | Store LB state in annotations | Declarative | Complex implementation | ❌ Rejected |
| OVSDB Event Watch | Subscribe to LB deletions | Real-time | Complex, stability concerns | ❌ Rejected |
| **LB Watcher + Restart Detection** | Verify LBs periodically + detect restarts | Minimal impact, targeted | Slight delay | ✅ **Implemented** |

## Implemented Solution

**LB Watcher with Periodic Verification + kube-ovn-controller Restart Detection**

### Architecture

```
┌─────────────────────────────────────────────────────────────────────────┐
│                            LBWatcher                                     │
├─────────────────────────────────────────────────────────────────────────┤
│                                                                         │
│  ┌─────────────────────────────┐    ┌─────────────────────────────┐    │
│  │  Periodic Verification      │    │  Restart Detection          │    │
│  │  (every 2 min)              │    │  (poll every 30s)           │    │
│  ├─────────────────────────────┤    ├─────────────────────────────┤    │
│  │ 1. List LoadBalancer svcs   │    │ 1. Check pod restart count  │    │
│  │ 2. Check OVN LB exists      │    │ 2. If changed: wait 30s     │    │
│  │ 3. If missing: trigger      │    │ 3. Verify all LBs           │    │
│  │    reconciliation           │    │                             │    │
│  └─────────────────────────────┘    └─────────────────────────────┘    │
│                 │                                │                      │
│                 └────────────────┬───────────────┘                      │
│                                  ▼                                      │
│                    Update annotation on Service                         │
│                    kube-dc.com/lb-resync-timestamp                      │
│                                  │                                      │
└──────────────────────────────────┼──────────────────────────────────────┘
                                   ▼
                    ┌─────────────────────────────┐
                    │  Service Controller         │
                    │  (existing reconcile loop)  │
                    │  - Detects annotation change│
                    │  - Recreates OVN LB         │
                    └─────────────────────────────┘
```

### Files Modified/Created

1. **`internal/service_lb/lb_watcher.go`** (New)
   - `LBWatcher` struct implementing `manager.Runnable`
   - Periodic verification every 2 minutes
   - kube-ovn-controller pod restart detection (every 30s)
   - Triggers reconciliation via annotation update

2. **`internal/controller/core/service_controller.go`** (Modified)
   - Added `lbWatcher` field to `ServiceReconciler`
   - Added RBAC for pods: `+kubebuilder:rbac:groups=core,resources=pods,verbs=get;list;watch`
   - Added predicate for `kube-dc.com/lb-resync-timestamp` annotation
   - Integrated `LBWatcher` startup via `mgr.Add()`

### Performance Impact

| Metric | Value | Notes |
|--------|-------|-------|
| Periodic check interval | 2 min | Configurable via `LBVerificationInterval` |
| Pod restart poll | 30s | Single API call |
| OVN LB check | ~10ms per svc | Only for provisioned LB services |
| Reconciliation trigger | On-demand | Only when LB actually missing |

### Test Results (2025-12-02)

**Test: Manual LB Deletion Recovery**

```
# Deleted OVN LB manually
$ ovn-nbctl lb-del shalb-dev-etcd-lb-tcp

# LB Watcher detected and recovered (within 2 min):
12:46:14 [WARN] LB watcher: OVN LB missing for service shalb-dev/etcd-lb, triggering reconciliation
12:46:14 [INFO] Update service-lb resync annotation changed, run reconciliation
12:46:15 [INFO] Kind: ServiceLb, Message: Create Lb shalb-dev-etcd-lb-tcp
12:46:15 [INFO] Kind: ServiceLb, Message: Add Vip Lb shalb-dev-etcd-lb-tcp, Vip 168.119.17.51:2379
12:46:15 [INFO] LB watcher: triggered reconciliation for 1 services with missing LBs
```

**Result:** ✅ LB automatically recreated within 2 minutes

## Verification Steps

After implementing fix, verify with:

```bash
# 1. Check OVN LBs exist
kubectl exec -n kube-system deployment/ovn-central -- \
  ovn-nbctl --no-leader-only lb-list | grep "168.119"

# 2. Simulate failure - restart kube-ovn-controller
kubectl rollout restart deployment/kube-ovn-controller -n kube-system

# 3. Wait 2 minutes and verify LBs still exist
sleep 120
kubectl exec -n kube-system deployment/ovn-central -- \
  ovn-nbctl --no-leader-only lb-list | grep "168.119"

# 4. Test connectivity
curl -k https://168.119.17.55:6443/version
```

## Related Files

- `internal/service_lb/lb_watcher.go` - **NEW** LB Watcher implementation
- `internal/service_lb/service_lb.go` - ServiceLB OVN sync logic
- `internal/controller/core/service_controller.go` - Service controller (modified)

## Version

- **Implemented in:** v0.1.34-dev4
- **Date:** 2025-12-02
