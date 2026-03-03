# PRD: VPC DNS External Routing Fix

## Overview

**Status:** Implemented  
**Priority:** High  
**Component:** Project Controller, VPC DNS  
**Related:** Network Infrastructure, DNS Resolution

## Problem Statement

VPC DNS pods created by KubeOVN for project VPCs were unable to resolve external domain names, causing cascading failures in project workloads including:

- Browser-based project shells (pshell) unable to authenticate with Keycloak
- Pods unable to resolve `kube-api.kube-dc.cloud` for Kubernetes API access
- Container image pulls from external registries failing
- General external DNS resolution timeouts

### Root Cause

VPC DNS pods utilize a dual-network configuration:
- **eth0** (`10.0.0.x`) - Project VPC network (isolated)
- **net1** (`10.100.0.x`) - OVN cluster network (connected to external)

KubeOVN's VPC DNS controller creates deployments with the following routing annotation:

```yaml
ovn-nad.default.ovn.kubernetes.io/routes: '[{"dst":"10.101.0.1/32","gw":"10.100.0.1"}]'
```

This configuration only routes Kubernetes API traffic (`10.101.0.1`) through the cluster network interface (`net1`). External DNS queries to upstream servers (`1.1.1.1`, `8.8.8.8`) have no explicit route, causing them to default to the isolated VPC network interface (`eth0`), where they timeout due to network isolation.

### Impact

- **Severity:** Critical - Complete DNS failure in project VPCs
- **Scope:** All projects using VPC DNS
- **User Impact:** Project workloads unable to access external services
- **Downtime:** Until route configuration is corrected

## Solution

### Architecture

Implement automatic route patching in the kube-dc project controller to add routes for external DNS servers through the OVN cluster network gateway.

### Technical Implementation

#### 1. Route Patch Function

Created `PatchVpcDnsDeploymentRoutes()` in `internal/project/res_vpc_dns_deployment_patch.go`:

**Functionality:**
- Waits for KubeOVN to create the vpc-dns deployment
- Checks existing route configuration
- Patches deployment annotation with complete routing table
- Implements retry logic and idempotency

**Target Routes:**
```json
[
  {"dst":"10.101.0.1/32","gw":"10.100.0.1"},    // Kubernetes API
  {"dst":"1.1.1.1/32","gw":"10.100.0.1"},       // Cloudflare DNS
  {"dst":"8.8.8.8/32","gw":"10.100.0.1"}        // Google DNS
]
```

#### 2. Integration with Project Sync

Modified `internal/project/project.go` to call the patch function asynchronously after VpcDns resource creation:

- Runs in background goroutine to avoid blocking project creation
- 3-second delay to allow KubeOVN controller to create deployment
- Non-fatal error handling with logging

### Workflow

```
Project Creation
    ↓
Create VpcDns Resource
    ↓
KubeOVN Creates Deployment (with limited routes)
    ↓
[3s delay]
    ↓
Kube-DC Patches Deployment (adds external DNS routes)
    ↓
Deployment Rollout
    ↓
VPC DNS Pod Restarts with Full Connectivity
```

## Acceptance Criteria

- [x] VPC DNS pods can resolve external domain names
- [x] Routes include both Kubernetes API and external DNS servers
- [x] Patch function is idempotent (safe to run multiple times)
- [x] Solution is applied automatically for new projects
- [x] Existing deployments receive the fix
- [x] No blocking delays in project creation flow

## Verification

### Test Cases

1. **New Project Creation**
   - Create a new project
   - Wait for VPC DNS deployment
   - Verify deployment has all three routes configured
   - Verify DNS resolution works from project pods

2. **DNS Resolution**
   - From vpc-dns pod: `nslookup google.com 8.8.8.8` → Success
   - From project pod: `nslookup kube-api.kube-dc.cloud` → Success
   - From pshell: `curl https://kube-api.kube-dc.cloud:6443/api` → Success

3. **Idempotency**
   - Run patch multiple times
   - Verify no unnecessary deployments rollouts
   - Verify routes remain correct

## Implementation Details

### Files Modified

- **New:** `internal/project/res_vpc_dns_deployment_patch.go`
  - Route patching logic
  - Retry and validation
  
- **Modified:** `internal/project/project.go`
  - Added async patch call after VpcDns sync

### Configuration

No configuration changes required. The solution works with existing:
- `vpc-dns-config` ConfigMap in `kube-system`
- `ovn-nad` NetworkAttachmentDefinition
- KubeOVN VpcDns CRD

### Dependencies

- KubeOVN v1.x with VpcDns CRD support
- Multus CNI for secondary network attachment
- OVN cluster network (`ovn-default` subnet)

## Rollout Plan

### Phase 1: Code Deployment
- Build updated kube-dc-manager image
- Deploy to cluster
- Monitor project controller logs

### Phase 2: Verification
- Create test project
- Verify automatic route configuration
- Test DNS resolution from project workloads

### Phase 3: Documentation
- Update project creation documentation
- Document VPC DNS architecture
- Add troubleshooting guide

## Monitoring

### Success Metrics

- **DNS Resolution Success Rate:** 100% for external domains
- **Project Creation Success Rate:** No degradation
- **VPC DNS Pod Restart Count:** One restart per deployment (expected)

### Logging

Controller logs include:
```
"create or update VpcDns"
"patching vpc-dns deployment with external DNS routes"
"successfully patched vpc-dns deployment with external DNS routes"
```

### Error Conditions

- `vpc-dns deployment not found after X retries` → KubeOVN controller issue
- `failed to patch vpc-dns deployment` → RBAC or API server issue
- `failed to marshal patch` → Code bug (should not occur)

## Security Considerations

- Route patching requires deployment write permissions in `kube-system`
- No sensitive data in route configuration
- DNS traffic routes through secure cluster network
- No changes to network isolation policies

## Future Enhancements

### Potential Improvements

1. **Dynamic DNS Server Configuration**
   - Read DNS servers from ConfigMap
   - Support custom upstream DNS servers per project

2. **Default Route Option**
   - Add `0.0.0.0/0` route through cluster gateway
   - Simplify configuration and support additional services

3. **Health Monitoring**
   - Monitor DNS resolution success rates
   - Alert on route configuration drift

4. **KubeOVN Upstream Contribution**
   - Propose patch to KubeOVN project
   - Add route configuration to VpcDns CRD spec
   - Eliminate need for post-creation patching

## References

- **KubeOVN VPC DNS Documentation:** https://kubeovn.github.io/docs/stable/en/vpc/vpc-internal-dns/
- **Related Issue:** Session expired errors in pshell due to DNS failure
- **Implementation PR:** [Link to PR when created]

## Appendix

### Network Architecture

```
┌─────────────────────────────────────┐
│         VPC DNS Pod                 │
│  ┌──────────────┐  ┌─────────────┐ │
│  │ eth0         │  │ net1        │ │
│  │ 10.0.0.14    │  │ 10.100.0.x  │ │
│  │ (VPC network)│  │ (Cluster)   │ │
│  └──────┬───────┘  └──────┬──────┘ │
└─────────┼──────────────────┼────────┘
          │                  │
          │                  │ Routes:
          │                  │ • 10.101.0.1/32 → 10.100.0.1
          │                  │ • 1.1.1.1/32 → 10.100.0.1
          │                  │ • 8.8.8.8/32 → 10.100.0.1
          │                  │
          ▼                  ▼
    VPC Network        Cluster Network
    (Isolated)         (External Access)
```

### CoreDNS Configuration

VPC DNS uses CoreDNS with the following forwarding config:

```
forward . /etc/resolv.conf {
  prefer_udp
}
```

The `/etc/resolv.conf` in vpc-dns pods points to upstream DNS servers (`1.1.1.1`, `8.8.8.8`), which require the routing fix to be reachable.
