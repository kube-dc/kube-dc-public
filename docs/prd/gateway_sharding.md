# PRD: Gateway Sharding for Scale

## Overview

This document outlines enhancement options for scaling the Gateway infrastructure to support thousands of tenants and services in Kube-DC.

## Problem Statement

The current implementation creates a **per-service HTTPS listener** on a single shared Gateway. This approach has scaling limitations:

| Constraint | Limit | Impact |
|------------|-------|--------|
| Envoy listeners per Gateway | ~100-1000 | Hard limit on services with `expose-route: https` |
| Gateway memory | Proportional to listeners | Memory pressure with many listeners |
| Configuration reload time | Increases with listeners | Slower route updates |
| Single point of failure | 1 Gateway | All tenants affected by Gateway issues |

## Current Architecture

```
┌─────────────────────────────────────────────────────────────────────┐
│                    Single Gateway "eg"                               │
│                 (envoy-gateway-system)                               │
├─────────────────────────────────────────────────────────────────────┤
│  Listeners:                                                          │
│  - http :80 (shared)                                                │
│  - tls-passthrough :443 (wildcard)                                  │
│  - https-svc1-ns1 :443 (per-service)  ←─┐                          │
│  - https-svc2-ns1 :443 (per-service)    │ N listeners              │
│  - https-svc3-ns2 :443 (per-service)    │ (scales linearly)        │
│  - ...                                ←─┘                          │
└─────────────────────────────────────────────────────────────────────┘
                              │
              ┌───────────────┼───────────────┐
              ▼               ▼               ▼
         Org A (N svcs)  Org B (N svcs)  Org C (N svcs)
```

## Proposed Solutions

### Option 1: Wildcard Listeners per Organization

**Concept**: Replace per-service listeners with wildcard listeners per organization.

```
┌─────────────────────────────────────────────────────────────────────┐
│                    Single Gateway "eg"                               │
├─────────────────────────────────────────────────────────────────────┤
│  Listeners:                                                          │
│  - http :80                                                         │
│  - tls-passthrough :443                                             │
│  - https-org-a :443 (*.org-a.kube-dc.com)  ←─┐                     │
│  - https-org-b :443 (*.org-b.kube-dc.com)    │ M listeners         │
│  - https-org-c :443 (*.org-c.kube-dc.com)  ←─┘ (M = # of orgs)     │
└─────────────────────────────────────────────────────────────────────┘
```

**Benefits**:
- Reduces listeners from N (services) to M (organizations)
- Single wildcard certificate per organization
- SNI routing handles service selection within listener

**Requirements**:
- Wildcard certificate issuance per organization
- DNS wildcard records per organization
- Organization controller creates listener when org is created

**Implementation Complexity**: Medium

---

### Option 2: Gateway per Organization

**Concept**: Each organization gets its own dedicated Gateway instance.

```
┌─────────────────────┐  ┌─────────────────────┐  ┌─────────────────────┐
│  Gateway "gw-org-a" │  │  Gateway "gw-org-b" │  │  Gateway "gw-org-c" │
│  IP: 88.99.29.250   │  │  IP: 88.99.29.251   │  │  IP: 88.99.29.252   │
├─────────────────────┤  ├─────────────────────┤  ├─────────────────────┤
│  *.org-a.kube-dc.com│  │  *.org-b.kube-dc.com│  │  *.org-c.kube-dc.com│
│  - All org A routes │  │  - All org B routes │  │  - All org C routes │
└─────────────────────┘  └─────────────────────┘  └─────────────────────┘
```

**Benefits**:
- Complete isolation between organizations
- No listener limits per org (own Gateway)
- Independent scaling and upgrades
- Failure isolation

**Requirements**:
- EIP allocation per organization Gateway
- GatewayClass configuration for multi-Gateway
- Organization controller provisions Gateway

**Implementation Complexity**: High

---

### Option 3: Hash-Based Gateway Sharding

**Concept**: Distribute services across N Gateway instances using consistent hashing.

```
                         ┌──────────────────┐
                         │   Hash Function  │
                         │  hash(hostname)  │
                         └────────┬─────────┘
                                  │
              ┌───────────────────┼───────────────────┐
              ▼                   ▼                   ▼
┌─────────────────────┐ ┌─────────────────────┐ ┌─────────────────────┐
│   Gateway "gw-0"    │ │   Gateway "gw-1"    │ │   Gateway "gw-2"    │
│   hash % 3 == 0     │ │   hash % 3 == 1     │ │   hash % 3 == 2     │
└─────────────────────┘ └─────────────────────┘ └─────────────────────┘
```

**Benefits**:
- Even distribution across Gateways
- Scales horizontally by adding Gateways
- No org-specific infrastructure

**Requirements**:
- DNS-based load balancing or L4 load balancer
- Consistent hashing for stable assignments
- Gateway selector in route controller

**Implementation Complexity**: High

---

### Option 4: Hybrid Approach (Recommended)

**Concept**: Combine wildcard listeners with optional Gateway sharding.

**Phase 1** (Quick Win):
- Convert per-service listeners to per-organization wildcard listeners
- Single Gateway, wildcard cert per org
- Reduces N services → M orgs listeners

**Phase 2** (Scale Out):
- Add Gateway per org for large organizations
- Small orgs share "default" Gateway
- Org annotation controls Gateway assignment

```yaml
# Organization with dedicated Gateway
apiVersion: kube-dc.com/v1
kind: Organization
metadata:
  name: large-enterprise
spec:
  gateway:
    dedicated: true  # Gets its own Gateway
    
# Small org uses shared Gateway
apiVersion: kube-dc.com/v1
kind: Organization
metadata:
  name: small-startup
spec:
  gateway:
    dedicated: false  # Uses shared Gateway (default)
```

---

## Comparison Matrix

| Aspect | Current | Option 1 | Option 2 | Option 3 | Option 4 |
|--------|---------|----------|----------|----------|----------|
| **Max Services** | ~1000 | ~100K | Unlimited | Unlimited | Unlimited |
| **Isolation** | None | Partial | Full | None | Configurable |
| **IP Usage** | 1 | 1 | N (per org) | K (shards) | 1 + N (large orgs) |
| **Complexity** | Low | Medium | High | High | Medium |
| **Cert Management** | Per-service | Per-org wildcard | Per-org wildcard | Per-service | Per-org wildcard |
| **Failure Domain** | All tenants | All tenants | Per org | Per shard | Configurable |

---

## Recommended Approach

### Short Term (Option 1)
1. Implement wildcard listeners per organization
2. Use wildcard certificates (`*.{org}.{base_domain}`)
3. SNI routing within listener for service selection
4. Keep single Gateway

### Medium Term (Option 4 - Phase 2)
1. Add `spec.gateway.dedicated` to Organization CRD
2. Organization controller provisions Gateway for dedicated orgs
3. Small orgs continue using shared Gateway
4. Automatic EIP allocation for dedicated Gateways

---

## Implementation Details

### Wildcard Listener Creation (Option 1)

```go
// In organization controller
func (r *OrganizationReconciler) syncGatewayListener(ctx context.Context, org *Organization) error {
    listenerName := fmt.Sprintf("https-%s", org.Name)
    hostname := fmt.Sprintf("*.%s.%s", org.Name, baseDomain)
    
    // Create wildcard certificate
    cert := &certmanagerv1.Certificate{
        Spec: certmanagerv1.CertificateSpec{
            SecretName: fmt.Sprintf("%s-wildcard-tls", org.Name),
            DNSNames:   []string{hostname},
            IssuerRef:  cmmeta.ObjectReference{Name: "letsencrypt", Kind: "ClusterIssuer"},
        },
    }
    
    // Patch Gateway with new listener
    return patchGatewayAddListener(ctx, listenerName, hostname, cert.Spec.SecretName)
}
```

### Service Route Changes

```go
// In service_lb/gateway_route.go
func (m *GatewayRouteManager) syncHTTPSRoute(ctx context.Context, hostname string, port int32) error {
    // With wildcard listeners, we DON'T create per-service listeners
    // Just create HTTPRoute pointing to org's wildcard listener
    
    org := m.getOrganization()
    listenerName := fmt.Sprintf("https-%s", org)
    
    // Create HTTPRoute (no listener creation needed)
    return m.syncHTTPSHTTPRoute(ctx, hostname, port, listenerName)
}
```

---

## DNS Requirements

### Current (Per-Service)
```
svc1-ns1.stage.kube-dc.com  → Gateway IP
svc2-ns1.stage.kube-dc.com  → Gateway IP
svc3-ns2.stage.kube-dc.com  → Gateway IP
```

### With Wildcard (Per-Organization)
```
*.org-a.stage.kube-dc.com   → Gateway IP (wildcard record)
*.org-b.stage.kube-dc.com   → Gateway IP (wildcard record)
```

---

## Certificate Requirements

### Current
- Per-service certificate via HTTP-01 challenge
- Certificate per hostname

### With Wildcard
- Per-organization wildcard certificate
- Requires DNS-01 challenge (not HTTP-01)
- Issuer must support DNS-01 (e.g., with Cloudflare, Route53, etc.)

```yaml
apiVersion: cert-manager.io/v1
kind: ClusterIssuer
metadata:
  name: letsencrypt-dns
spec:
  acme:
    server: https://acme-v02.api.letsencrypt.org/directory
    email: admin@kube-dc.com
    privateKeySecretRef:
      name: letsencrypt-dns-key
    solvers:
    - dns01:
        cloudflare:
          email: admin@kube-dc.com
          apiKeySecretRef:
            name: cloudflare-api-key
            key: api-key
```

---

## Migration Path

### Phase 1: Parallel Operation
1. Keep existing per-service listeners working
2. Add wildcard listener for new organizations
3. New services use wildcard listener

### Phase 2: Migration
1. Migrate existing services to wildcard listeners
2. Update HTTPRoutes to point to org listener
3. Remove old per-service listeners

### Phase 3: Cleanup
1. Remove per-service listener code path
2. Update documentation
3. Deprecate old annotation behavior

---

## Metrics & Monitoring

### Key Metrics to Track
- Listeners per Gateway
- Gateway memory usage
- Route update latency
- Certificate expiration by org

### Alerts
- Gateway listener count > 80% limit
- Certificate renewal failures
- Gateway pod restarts

---

## Timeline Estimate

| Phase | Effort | Duration |
|-------|--------|----------|
| Option 1 (Wildcard Listeners) | Medium | 2-3 weeks |
| Option 4 Phase 2 (Dedicated Gateways) | High | 4-6 weeks |
| Migration of existing services | Medium | 2-3 weeks |

---

## Decision Required

- [ ] Approve Option 1 (Wildcard Listeners) for immediate scale improvement
- [ ] Approve Option 4 (Hybrid) for long-term architecture
- [ ] DNS-01 solver configuration (requires DNS provider API access)
- [ ] Organization subdomain structure (`{service}.{org}.{domain}` vs `{service}-{org}.{domain}`)

---

## References

- [Envoy Gateway Scaling Guide](https://gateway.envoyproxy.io/docs/operations/scaling/)
- [Gateway API Multi-Gateway](https://gateway-api.sigs.k8s.io/guides/multiple-gateways/)
- [cert-manager DNS-01 Solvers](https://cert-manager.io/docs/configuration/acme/dns01/)
