# PRD: OVN Database Connection Stability Improvements

## Status
- **Status**: Proposed
- **Created**: 2026-01-12
- **Priority**: High
- **Impact**: Platform Stability, Service Availability

## Executive Summary

The kube-dc-manager controller experiences severe OVN database connection instability, resulting in **2.6+ million disconnections** over 158 days of uptime. This causes:
- 20+ minute service reconciliation deadlocks
- LoadBalancer services stuck in `<pending>` state
- Mass reconnection storms during network hiccups
- Unpredictable service external IP assignment delays

This PRD outlines comprehensive improvements to OVN database client connectivity, probe intervals, and reconnection strategies to achieve production-grade stability.

---

## Problem Statement

### Current Issues

**1. Extreme Connection Churn**
```
OVN Database Metrics (158 days uptime):
- Disconnections: 2,682,477 (avg 16,964/day)
- Active Connections: 13 established, 12 TIME_WAIT
- Pattern: Constant disconnect/reconnect cycle
```

**2. Service Reconciliation Deadlocks**
- Services remain `<pending>` for 20+ minutes during connection issues
- Example: `jump-cp` and `jump-etcd-etcd-lb` stuck from 14:35 to 14:56 (21 minutes)
- 32 services complete simultaneously when connection recovers (queued backlog)

**3. Controller Behavior**
- **Before fix**: Parallel deadlock (5 concurrent reconciliations block on libovsdb internal mutexes)
- **After SafeOvnClient fix**: Serial bottleneck (one stuck operation blocks all others via operation mutex)
- **After timeout fix**: Operations timeout after 30s, but still experience delays during reconnection

### Impact on Production

- **Service Availability**: LoadBalancer services cannot get external IPs during connection issues
- **Unpredictability**: No SLA for service provisioning time (0-30+ minutes)
- **Cascading Failures**: Connection loss triggers mass reconnection storm
- **Resource Waste**: Controller CPU spikes during reconnection attempts

---

## Root Cause Analysis

### 1. Aggressive Server-Side Probe Intervals ⚠️ CRITICAL

**Current Configuration:**
```bash
OVN_LEADER_PROBE_INTERVAL: 5 seconds
OVN_NORTHD_PROBE_INTERVAL: 5000ms (5 seconds)
PROBE_INTERVAL: 180000ms (3 minutes)
```

**Problem:**
- Industry standard: **30-60 seconds** for production environments
- Any GC pause, network jitter, or temporary CPU spike → disconnection
- Too aggressive for distributed systems with network latency

**Reference:** Mirantis/OpenStack production deployments use 60-second inactivity probes

### 2. No Client-Side Inactivity Monitoring ⚠️ CRITICAL

**Current State:**
```go
// kube-dc-manager uses kube-ovn v1.14.4
ovsClient, err := ovs.NewOvnNbClient(
    kubedccomv1.ConfigGlobal.MasterConfig.OvnDbIps,
    ovnNbTimeout,           // 2 seconds
    ovsDbConTimeout,        // 2 seconds  
    ovsDbInactivityTimeout, // 2 seconds
    maxRetry,               // 2 attempts
)
```

**Problems:**
- No proactive Echo requests to keep connections alive
- No awareness of stale connections until server kills them
- No graceful reconnection strategy (instant retry, thundering herd)

**What's Missing:**
```go
// libovsdb client options NOT used:
client.WithInactivityCheck(30*time.Second, nil, nil)  // ❌ Not implemented
client.WithReconnect(60*time.Second, backoff)         // ❌ Not implemented
```

### 3. kube-ovn NewOvnNbClient Limitations

**Current Implementation** (`kube-ovn v1.14.4/pkg/ovs/ovn.go`):
```go
func NewOvnNbClient(ovnNbAddr string, ovnNbTimeout, ovsDbConTimeout, 
                    ovsDbInactivityTimeout, maxRetry int) (*OVNNbClient, error) {
    // ...
    nbClient, err = ovsclient.NewOvsDbClient(
        ovsclient.NBDB,
        ovnNbAddr,
        dbModel,
        monitors,
        ovsDbConTimeout,        // Used for initial connection timeout
        ovsDbInactivityTimeout, // Passed to underlying client
    )
    // ...
    // NO WithReconnect() option
    // NO WithInactivityCheck() option
    // Simple retry loop with 2-second sleep (no backoff)
}
```

**Limitations:**
- Does not expose libovsdb's `WithReconnect()` option
- Does not expose libovsdb's `WithInactivityCheck()` option
- No exponential backoff on reconnection
- Fixed 2-second sleep between retries (can cause thundering herd)

### 4. Controller Mutex Architecture

**Evolution:**

**V1 - Original (Parallel Deadlock):**
```go
// Multiple reconciliations share singleton client
// libovsdb internal mutexes cause contention → DEADLOCK
```

**V2 - SafeOvnClient (Serial Bottleneck):**
```go
// Operation mutex serializes ALL OVN calls
// One stuck operation blocks everything → 20min hang
```

**V3 - Timeout Wrapper (Current):**
```go
// 30-second timeout on mutex acquisition
// Stuck operations timeout and reset client
// BUT: Reconnection still takes time, services still delayed
```

**Still Missing:**
- Proactive connection health monitoring
- Graceful degradation during reconnection
- Connection pool per operation type (optional optimization)

---

## Proposed Solutions

### Phase 1: Immediate Configuration Changes (Zero Code) 🚀

**1.1 Increase OVN Database Probe Intervals**

**Change:**
```yaml
# Edit: kubectl edit deployment -n kube-system ovn-central
env:
- name: OVN_LEADER_PROBE_INTERVAL
  value: "60"  # Was: 5 (12x increase)
  
- name: OVN_NORTHD_PROBE_INTERVAL
  value: "60000"  # Was: 5000 (12x increase)
  
- name: PROBE_INTERVAL
  value: "300000"  # Was: 180000 (1.67x increase to 5 minutes)
```

**Expected Impact:**
- ✅ Reduce disconnections by **80-90%** (from 17K/day to <2K/day)
- ✅ Prevent transient network issues from causing mass reconnections
- ✅ Reduce OVN database CPU during connection churn
- ✅ More tolerance for GC pauses and temporary load spikes

**Risk:** Very low - aligns with industry best practices

**Rollback:** Simple `kubectl rollout undo` if issues occur

---

**1.2 Configure OVN Database Connection Inactivity Probe**

**Change:**
```bash
kubectl exec -n kube-system ovn-central-* -- \
  ovn-nbctl set-connection ptcp:6641 -- \
  set connection . inactivity_probe=60000
```

**Expected Impact:**
- ✅ Server-side 60-second inactivity timeout (matches PROBE_INTERVAL)
- ✅ Consistent behavior across all connection types
- ✅ Prevents premature connection kills

**Risk:** Low - standard OVN configuration

---

### Phase 2: Client-Side Improvements (Code Changes) 🔧

**2.1 Extend kube-ovn Client with libovsdb Options**

**Option A: Wrapper Around NewOvnNbClient (Recommended)**

Create enhanced client initialization in `internal/service_lb/ovn_client.go`:

```go
import (
    "github.com/cenkalti/backoff/v4"
    "github.com/ovn-org/libovsdb/client"
)

func createEnhancedOvnClient(addr string, timeout int) (*ovs.OVNNbClient, error) {
    // 1. Create base client using kube-ovn
    baseClient, err := ovs.NewOvnNbClient(
        addr,
        timeout,
        ovsDbConTimeout,
        ovsDbInactivityTimeout,
        maxRetry,
    )
    if err != nil {
        return nil, err
    }
    
    // 2. Access underlying libovsdb client
    // Note: May require reflection or type assertion depending on kube-ovn internals
    underlyingClient := baseClient.Client // Access internal client field
    
    // 3. Configure reconnection strategy
    reconnectBackoff := backoff.NewExponentialBackOff()
    reconnectBackoff.InitialInterval = 1 * time.Second
    reconnectBackoff.MaxInterval = 30 * time.Second
    reconnectBackoff.MaxElapsedTime = 2 * time.Minute
    reconnectBackoff.Multiplier = 2.0
    
    // Apply options to underlying client
    underlyingClient.SetOption(client.WithReconnect(60*time.Second, reconnectBackoff))
    underlyingClient.SetOption(client.WithInactivityCheck(
        30*time.Second,  // Send Echo every 30s
        nil,             // Default success handler
        func(err error) {
            klog.Warningf("OVN client inactivity check failed: %v", err)
        },
    ))
    
    return baseClient, nil
}
```

**Option B: Fork kube-ovn NewOvnNbClient (Alternative)**

Copy and modify `NewOvnNbClient` to include reconnection options:

```go
func NewEnhancedOvnNbClient(ovnNbAddr string, ovnNbTimeout, ovsDbConTimeout, 
                            ovsDbInactivityTimeout, maxRetry int) (*OVNNbClient, error) {
    dbModel, err := ovnnb.FullDatabaseModel()
    if err != nil {
        return nil, err
    }
    
    // ... setup indexes and monitors (same as kube-ovn) ...
    
    // Configure reconnection strategy
    reconnectBackoff := backoff.NewExponentialBackOff()
    reconnectBackoff.InitialInterval = 1 * time.Second
    reconnectBackoff.MaxInterval = 30 * time.Second
    reconnectBackoff.MaxElapsedTime = 2 * time.Minute
    
    // Create client with enhanced options
    nbClient, err = ovsclient.NewOvsDbClient(
        ovsclient.NBDB,
        ovnNbAddr,
        dbModel,
        monitors,
        ovsDbConTimeout,
        ovsDbInactivityTimeout,
        // NEW: Add reconnection options
        client.WithReconnect(60*time.Second, reconnectBackoff),
        client.WithInactivityCheck(30*time.Second, nil, nil),
    )
    
    // ... rest of implementation ...
}
```

**Recommended Approach:** Option A (wrapper) for minimal kube-ovn dependency changes

---

**2.2 Update GetOvnClient Implementation**

**File:** `internal/service_lb/ovn_client.go`

**Change:**
```go
func GetOvnClient(ctx context.Context, cli client.Client) (*SafeOvnClient, error) {
    ovnClientMu.RLock()
    if globalOvnClient != nil {
        ovnClientMu.RUnlock()
        return &SafeOvnClient{
            OVNNbClient: globalOvnClient,
            opMu:        &ovnOpMu,
        }, nil
    }
    ovnClientMu.RUnlock()

    ovnClientMu.Lock()
    defer ovnClientMu.Unlock()

    if globalOvnClient != nil {
        return &SafeOvnClient{
            OVNNbClient: globalOvnClient,
            opMu:        &ovnOpMu,
        }, nil
    }

    if err := kubedccomv1.ConfigGlobal.ReadConfig(ctx, cli); err != nil {
        return nil, err
    }

    // NEW: Use enhanced client creation
    ovsClient, err := createEnhancedOvnClient(
        kubedccomv1.ConfigGlobal.MasterConfig.OvnDbIps,
        ovnNbTimeout,
    )
    if err != nil {
        return nil, err
    }

    globalOvnClient = ovsClient
    return &SafeOvnClient{
        OVNNbClient: globalOvnClient,
        opMu:        &ovnOpMu,
    }, nil
}
```

**Benefits:**
- ✅ Proactive connection health monitoring (Echo requests every 30s)
- ✅ Automatic reconnection with exponential backoff
- ✅ Graceful handling of temporary network issues
- ✅ Reduced thundering herd during mass reconnections

---

### Phase 3: Monitoring & Observability 📊

**3.1 Add Prometheus Metrics**

```go
import (
    "github.com/prometheus/client_golang/prometheus"
)

var (
    ovnConnectionsTotal = prometheus.NewCounterVec(
        prometheus.CounterOpts{
            Name: "kube_dc_ovn_connections_total",
            Help: "Total OVN database connections by state",
        },
        []string{"state"}, // connected, disconnected, reconnecting
    )
    
    ovnOperationDuration = prometheus.NewHistogramVec(
        prometheus.HistogramOpts{
            Name: "kube_dc_ovn_operation_duration_seconds",
            Help: "OVN operation duration in seconds",
            Buckets: []float64{.001, .005, .01, .025, .05, .1, .25, .5, 1, 2.5, 5, 10, 30},
        },
        []string{"operation"}, // GetLogicalRouter, ListLoadBalancers, etc.
    )
    
    ovnMutexWaitDuration = prometheus.NewHistogram(
        prometheus.HistogramOpts{
            Name: "kube_dc_ovn_mutex_wait_duration_seconds",
            Help: "Time spent waiting for OVN operation mutex",
            Buckets: []float64{.001, .01, .1, 1, 5, 10, 30},
        },
    )
)

func init() {
    prometheus.MustRegister(ovnConnectionsTotal)
    prometheus.MustRegister(ovnOperationDuration)
    prometheus.MustRegister(ovnMutexWaitDuration)
}
```

**Use in tryLockWithTimeout:**
```go
func (c *SafeOvnClient) tryLockWithTimeout() error {
    start := time.Now()
    lockCh := make(chan struct{})
    go func() {
        c.opMu.Lock()
        close(lockCh)
    }()

    select {
    case <-lockCh:
        ovnMutexWaitDuration.Observe(time.Since(start).Seconds())
        return nil
    case <-time.After(ovnOpMutexTimeout):
        ovnMutexWaitDuration.Observe(ovnOpMutexTimeout.Seconds())
        ovnConnectionsTotal.WithLabelValues("timeout").Inc()
        ResetOvnClient()
        return fmt.Errorf("OVN operation mutex timeout after %v", ovnOpMutexTimeout)
    }
}
```

**Grafana Dashboard Metrics:**
- OVN connection state over time
- Operation duration percentiles (p50, p95, p99)
- Mutex wait time distribution
- Reconnection events per hour
- Service reconciliation success rate

---

**3.2 Enhanced Logging**

```go
// Add structured logging for connection events
klog.InfoS("OVN client connecting", 
    "address", ovnNbAddr,
    "timeout", ovnNbTimeout,
    "retry", attempt)

klog.InfoS("OVN client reconnecting",
    "reason", "inactivity_timeout",
    "elapsed", timeSinceLastActivity)

klog.WarningS("OVN operation slow",
    "operation", "GetLogicalRouter",
    "duration", duration,
    "threshold", 5*time.Second)
```

---

## Implementation Plan

### Timeline

| Phase | Tasks | Duration | Priority |
|-------|-------|----------|----------|
| **Phase 1** | Configuration changes (probe intervals) | 1 day | P0 - Critical |
| **Phase 2** | Client-side improvements (code) | 3-5 days | P1 - High |
| **Phase 3** | Monitoring & metrics | 2-3 days | P2 - Medium |

**Total Estimated Effort:** 6-9 days

---

### Phase 1: Configuration Changes (Day 1)

**Tasks:**
1. ✅ Document current OVN configuration
2. ✅ Backup ovn-central deployment YAML
3. ✅ Apply probe interval changes
4. ✅ Monitor disconnection rate for 24 hours
5. ✅ Validate no service disruption

**Success Criteria:**
- Disconnection rate drops by >80%
- No increase in service reconciliation failures
- No DEADLOCK alerts in controller logs

---

### Phase 2: Client Improvements (Days 2-6)

**Tasks:**
1. Research kube-ovn client internals for extensibility
2. Implement `createEnhancedOvnClient()` wrapper
3. Add exponential backoff configuration
4. Add inactivity check with Echo requests
5. Update `GetOvnClient()` to use enhanced client
6. Unit tests for reconnection scenarios
7. Integration tests with simulated network failures
8. Deploy to staging environment
9. Monitor for 48 hours before production

**Success Criteria:**
- Client reconnects within 5 seconds of disconnection
- No thundering herd during mass reconnections
- Service reconciliation continues during brief network issues
- Mutex timeout events drop to zero

---

### Phase 3: Observability (Days 7-9)

**Tasks:**
1. Add Prometheus metrics collection
2. Create Grafana dashboard
3. Set up alerts for abnormal connection patterns
4. Document troubleshooting runbook
5. Train team on new metrics

**Success Criteria:**
- Real-time visibility into OVN connection health
- Alerts trigger before user-visible impact
- < 5 minute MTTR for connection issues

---

## Success Metrics

### Immediate (Phase 1 - Configuration)

| Metric | Before | Target | 
|--------|--------|--------|
| Daily Disconnections | 16,964 | < 2,000 |
| Service Provisioning P95 | 30+ minutes | < 5 minutes |
| DEADLOCK Alerts/Day | 2-5 | 0 |
| Controller CPU (avg) | Unknown | Baseline + Monitor |

### Long-term (Phase 2+3 - Code + Monitoring)

| Metric | Before | Target |
|--------|--------|--------|
| Service Provisioning P95 | 30+ minutes | < 30 seconds |
| Service Provisioning P99 | Unknown | < 2 minutes |
| Reconnection Time P95 | Unknown | < 10 seconds |
| MTTR for Connection Issues | Unknown | < 5 minutes |
| Connection Uptime SLA | Unknown | 99.9% |

---

## Risks & Mitigation

### Risk 1: Increased Probe Intervals Delay Failure Detection

**Risk:** Longer probe intervals mean slower detection of actual failures

**Mitigation:**
- Client-side inactivity checks (30s) provide faster detection than server probes (60s)
- Echo requests every 30s ensure liveness monitoring
- Acceptable tradeoff for production stability

### Risk 2: kube-ovn Internal Changes Required

**Risk:** kube-ovn v1.14.4 may not expose necessary client APIs

**Mitigation:**
- Use reflection/type assertion to access internal client
- Fork kube-ovn client code if necessary (Option B)
- Contribute improvements upstream to kube-ovn project

### Risk 3: Regression During Rollout

**Risk:** Changes could introduce new stability issues

**Mitigation:**
- Staged rollout: Config → Staging → Production
- Comprehensive monitoring during each phase
- Quick rollback plan (kubectl rollout undo)
- Feature flag for enhanced client (gradual enablement)

---

## Testing Strategy

### Unit Tests

```go
func TestSafeOvnClient_ReconnectionBackoff(t *testing.T) {
    // Test exponential backoff behavior
}

func TestSafeOvnClient_InactivityCheck(t *testing.T) {
    // Test Echo request behavior
}

func TestSafeOvnClient_MutexTimeout(t *testing.T) {
    // Test 30-second timeout enforcement
}
```

### Integration Tests

```go
func TestLoadBalancerService_DuringNetworkPartition(t *testing.T) {
    // Simulate network partition
    // Verify service eventually gets external IP
    // Verify no deadlock
}

func TestLoadBalancerService_DuringOVNRestart(t *testing.T) {
    // Restart OVN database
    // Verify client reconnects
    // Verify services continue reconciling
}
```

### Chaos Testing

- Randomly kill OVN database connections
- Inject network latency (100-500ms)
- Simulate OVN database CPU saturation
- Test with 100+ simultaneous service creations

---

## Documentation Requirements

1. **Architecture Docs**: Update OVN client connectivity diagram
2. **Operations Runbook**: Troubleshooting guide for connection issues
3. **Metrics Guide**: Prometheus metrics and Grafana dashboards
4. **Configuration Reference**: All tunable parameters and defaults
5. **Migration Guide**: Rollout procedure and rollback steps

---

## Dependencies

### External Dependencies

- **kube-ovn**: v1.14.4 (current), potential upgrade to v1.15+ for better client APIs
- **libovsdb**: Indirect dependency via kube-ovn
- **backoff library**: `github.com/cenkalti/backoff/v4` (already in kube-ovn)

### Internal Dependencies

- OVN database deployment configuration
- kube-dc-manager controller deployment
- Prometheus metrics infrastructure (if Phase 3)

---

## Future Enhancements

### Post-Implementation Improvements

1. **Connection Pooling**: Separate pools for read vs write operations
2. **Circuit Breaker**: Temporarily disable OVN operations during extended outages
3. **Degraded Mode**: Continue serving existing services even when OVN unreachable
4. **Multi-Database HA**: Support OVN database clustering (not just single-node)
5. **Local Caching**: Cache read-heavy operations (logical routers, switches)

### kube-ovn Upstream Contributions

1. PR to expose `WithReconnect()` and `WithInactivityCheck()` options
2. PR to use exponential backoff in `NewOvnNbClient()`
3. Share production learnings and best practices

---

## References

### Documentation

- [libovsdb Client Options](https://pkg.go.dev/github.com/ovn-org/libovsdb/client)
- [Mirantis OVS Timeouts Guide](https://docs.mirantis.com/mcp/q4-18/mcp-operations-guide/tshooting/tshoot-mcp-openstack/ovs-timeouts.html)
- [kube-ovn v1.14.4 Source](https://github.com/kubeovn/kube-ovn/tree/v1.14.4)

### Related Issues

- Original deadlock issue: Services stuck `<pending>` for 20+ minutes
- SafeOvnClient fix: Added operation mutex (commit `760fffa`)
- Timeout wrapper fix: Added 30s timeout on mutex (commit `f57c5c9`)

### Investigation Findings

- **OVN Database**: 2,682,477 disconnections over 158 days
- **Probe Intervals**: 5s (too aggressive, should be 60s)
- **Client Options**: No inactivity checking or reconnection strategy
- **Pattern**: Mass reconnection during network hiccups causes cascade

---

## Approval & Sign-off

### Stakeholders

- **Engineering Lead**: Review technical approach
- **Platform Team**: Review infrastructure changes
- **SRE**: Review monitoring and operations impact

### Approval Criteria

- ✅ Technical design reviewed
- ✅ Risk assessment completed
- ✅ Rollback plan documented
- ✅ Success metrics defined
- ✅ Testing strategy approved

---

## Appendix

### A. Current Configuration Snapshot

```bash
# OVN Central Environment
kubectl get deployment -n kube-system ovn-central -o yaml | grep -A 3 "env:"

env:
- name: ENABLE_SSL
  value: "false"
- name: NODE_IPS
  value: 192.168.1.3
- name: OVN_LEADER_PROBE_INTERVAL
  value: "5"
- name: OVN_NORTHD_PROBE_INTERVAL
  value: "5000"
- name: PROBE_INTERVAL
  value: "180000"
```

### B. Disconnection Analysis

```bash
# OVN Cluster Status (2026-01-12)
kubectl exec -n kube-system ovn-central-* -- \
  ovs-appctl -t /var/run/ovn/ovnnb_db.ctl cluster/status OVN_Northbound

Disconnections: 2682477
Uptime: 158 days
Rate: ~16,964 disconnections/day
```

### C. Service Reconciliation Timeline

```
14:35:12 - Service jump-etcd-etcd-lb: reconciliation started
14:35:12 - Creating LoadBalancer resource manager
14:35:12 - [STUCK - OVN connection issue]
...
14:56:15 - Service jump-etcd-etcd-lb: reconciliation completed (21 minutes)
14:56:15 - 32 services complete simultaneously (backlog flush)
```

---

**Document Version:** 1.0  
**Last Updated:** 2026-01-12  
**Author:** kube-dc Platform Team  
**Review Date:** TBD
