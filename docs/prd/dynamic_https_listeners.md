# PRD: Dynamic HTTPS Listeners - Gateway-Terminated TLS

## Executive Summary

This PRD defines the automatic HTTPS listener creation mechanism for services that want the Gateway to terminate TLS. When a Service is annotated with `expose-route: https`, the controller automatically creates:
1. A TLS Certificate (via cert-manager)
2. A dedicated Gateway listener for the hostname
3. An HTTPRoute pointing to the service's Backend

This enables users to expose services over HTTPS without handling TLS in their application.

## Problem Statement

Currently, there are two options for HTTPS exposure:

### Option 1: TLS Passthrough (Current)
```yaml
service.nlb.kube-dc.com/expose-route: "tls-passthrough"
```
- ✅ Works today
- ❌ App must terminate TLS (configure nginx/app with certificates)
- ❌ More complex application setup

### Option 2: Platform-Style HTTPS (Manual)
```yaml
# Requires manual Gateway listener configuration by admin
# Only available for platform services (console, keycloak, etc.)
```
- ✅ App serves plain HTTP
- ❌ Requires admin intervention
- ❌ Not self-service for users

### Desired State
```yaml
service.nlb.kube-dc.com/expose-route: "https"
```
- ✅ App serves plain HTTP (no TLS handling needed)
- ✅ Gateway terminates TLS automatically
- ✅ Certificate auto-provisioned via cert-manager
- ✅ Self-service for project users

## Solution: Dynamic Listener Creation

### User Experience

```yaml
apiVersion: v1
kind: Service
metadata:
  name: my-web-app
  annotations:
    # Single annotation for full HTTPS automation
    service.nlb.kube-dc.com/expose-route: "https"
spec:
  type: LoadBalancer
  selector:
    app: my-web-app
  ports:
  - port: 80        # App serves plain HTTP
    targetPort: 8080
```

**Result:**
- ✅ Certificate requested via cert-manager (ACME HTTP-01)
- ✅ Gateway listener created for `my-web-app-{namespace}.{base_domain}`
- ✅ HTTPRoute created pointing to Backend
- ✅ User accesses `https://my-web-app-demo.stage.kube-dc.com`

### Traffic Flow

```
User Browser
    │
    │ HTTPS (encrypted)
    ▼
┌─────────────────────────────────────┐
│         Envoy Gateway               │
│  Listener: https-my-web-app-demo    │
│  Port: 443                          │
│  TLS: Terminate (uses certificate)  │
└─────────────────────────────────────┘
    │
    │ HTTP (plain, internal)
    ▼
┌─────────────────────────────────────┐
│         Backend                     │
│  Target: 100.65.x.x:80              │
└─────────────────────────────────────┘
    │
    │ HTTP
    ▼
┌─────────────────────────────────────┐
│         User Application            │
│  Listens on: 0.0.0.0:8080           │
│  No TLS configuration needed        │
└─────────────────────────────────────┘
```

## Annotation Schema

### Route Types (Extended)

| Annotation Value | Description | App Requirements |
|------------------|-------------|------------------|
| `http` | Plain HTTP on port 80 | Serves HTTP |
| `tls-passthrough` | TLS passthrough on port 443 | Serves HTTPS (app terminates TLS) |
| `https` | **NEW** Gateway-terminated TLS on port 443 | Serves HTTP (gateway terminates TLS) |

### Additional Annotations

| Annotation | Values | Default | Description |
|------------|--------|---------|-------------|
| `service.nlb.kube-dc.com/expose-route` | `http`, `tls-passthrough`, `https` | - | Route type |
| `service.nlb.kube-dc.com/route-hostname` | string | auto-generated | Custom hostname |
| `service.nlb.kube-dc.com/route-port` | number | first port | Backend port |
| `service.nlb.kube-dc.com/tls-issuer` | string | `letsencrypt` | cert-manager Issuer name |

## Architecture

### Resources Created for `expose-route: https`

```
Service (user creates)
    │
    ├── EIP (auto-created)
    │
    ├── Backend (auto-created)
    │
    ├── Certificate (auto-created)
    │       │
    │       └── Secret (cert-manager creates)
    │
    ├── Gateway Listener (auto-created via patch)
    │       │
    │       └── References Certificate Secret
    │
    ├── ReferenceGrant (auto-created)
    │       │
    │       └── Allows Gateway to access Certificate Secret
    │
    └── HTTPRoute (auto-created)
            │
            └── References Gateway Listener and Backend
```

### Controller Flow

```go
func (m *GatewayRouteManager) syncHTTPSRoute(ctx context.Context, hostname string, port int32) error {
    // 1. Ensure Issuer exists (or use default)
    issuer := m.getIssuer()
    
    // 2. Create Certificate
    cert := m.createCertificate(hostname, issuer)
    
    // 3. Wait for Certificate to be ready
    if !m.isCertificateReady(cert) {
        return requeueAfter(30 * time.Second)
    }
    
    // 4. Create ReferenceGrant (allows Gateway to access cert secret)
    m.createReferenceGrant(hostname)
    
    // 5. Patch Gateway to add listener
    m.patchGatewayAddListener(hostname, cert.Spec.SecretName)
    
    // 6. Create HTTPRoute
    m.createHTTPRoute(hostname, port)
    
    return nil
}
```

## Implementation Details

### 1. Certificate Creation

```yaml
apiVersion: cert-manager.io/v1
kind: Certificate
metadata:
  name: my-web-app-tls
  namespace: shalb-demo
  ownerReferences:
  - apiVersion: v1
    kind: Service
    name: my-web-app
    uid: <service-uid>
spec:
  secretName: my-web-app-tls-secret
  issuerRef:
    name: letsencrypt
    kind: Issuer
  dnsNames:
  - my-web-app-demo.stage.kube-dc.com
```

### 2. ReferenceGrant

Allows the Gateway in `envoy-gateway-system` to reference the certificate Secret in the user's namespace:

```yaml
apiVersion: gateway.networking.k8s.io/v1beta1
kind: ReferenceGrant
metadata:
  name: allow-gateway-cert-my-web-app
  namespace: shalb-demo
  ownerReferences:
  - apiVersion: v1
    kind: Service
    name: my-web-app
spec:
  from:
  - group: gateway.networking.k8s.io
    kind: Gateway
    namespace: envoy-gateway-system
  to:
  - group: ""
    kind: Secret
    name: my-web-app-tls-secret
```

### 3. Gateway Listener Patch

```yaml
# Controller patches Gateway to add:
- name: https-my-web-app-shalb-demo
  hostname: my-web-app-demo.stage.kube-dc.com
  port: 443
  protocol: HTTPS
  tls:
    mode: Terminate
    certificateRefs:
    - group: ""
      kind: Secret
      name: my-web-app-tls-secret
      namespace: shalb-demo
  allowedRoutes:
    namespaces:
      from: Same
```

### 4. HTTPRoute

```yaml
apiVersion: gateway.networking.k8s.io/v1
kind: HTTPRoute
metadata:
  name: my-web-app-route
  namespace: shalb-demo
  ownerReferences:
  - apiVersion: v1
    kind: Service
    name: my-web-app
spec:
  parentRefs:
  - group: gateway.networking.k8s.io
    kind: Gateway
    name: eg
    namespace: envoy-gateway-system
    sectionName: https-my-web-app-shalb-demo
  hostnames:
  - my-web-app-demo.stage.kube-dc.com
  rules:
  - backendRefs:
    - group: gateway.envoyproxy.io
      kind: Backend
      name: my-web-app-backend
      port: 80
```

## RBAC Requirements

### Controller Permissions (New)

```yaml
# +kubebuilder:rbac:groups=gateway.networking.k8s.io,resources=gateways,verbs=get;list;watch;patch
# +kubebuilder:rbac:groups=gateway.networking.k8s.io,resources=referencegrants,verbs=get;list;watch;create;update;patch;delete
# +kubebuilder:rbac:groups=cert-manager.io,resources=certificates,verbs=get;list;watch;create;update;patch;delete
```

### Security Considerations

| Concern | Mitigation |
|---------|------------|
| Controller can modify Gateway | Limited to adding/removing listeners, not core config |
| Certificate in user namespace | ReferenceGrant scoped to specific secret |
| Listener naming conflicts | Use `https-{service}-{namespace}` pattern |
| Orphaned listeners | OwnerReference on Service triggers cleanup |

## Cleanup

When Service is deleted:

1. **OwnerReferences** automatically delete:
   - Certificate
   - ReferenceGrant
   - HTTPRoute
   - Backend
   - EIP

2. **Controller explicitly removes**:
   - Gateway listener (via patch)

```go
func (m *GatewayRouteManager) deleteHTTPSRoute(ctx context.Context) error {
    hostname := m.getHostname()
    listenerName := m.getListenerName(hostname)
    
    // Remove listener from Gateway
    m.patchGatewayRemoveListener(listenerName)
    
    // Other resources cleaned up by OwnerReferences
    return nil
}
```

## Comparison: Route Types

| Feature | `http` | `tls-passthrough` | `https` |
|---------|--------|-------------------|---------|
| Gateway Port | 80 | 443 | 443 |
| TLS Termination | None | App | Gateway |
| App Complexity | Low | High | Low |
| Certificate | Not needed | App manages | Auto-provisioned |
| Gateway Listener | Shared | Shared wildcard | Per-service |
| Gateway Modification | No | No | **Yes** |

## Example Usage

### Simple Web Application

```yaml
apiVersion: apps/v1
kind: Deployment
metadata:
  name: my-web-app
spec:
  replicas: 2
  selector:
    matchLabels:
      app: my-web-app
  template:
    metadata:
      labels:
        app: my-web-app
    spec:
      containers:
      - name: app
        image: nginx:alpine
        ports:
        - containerPort: 80  # Plain HTTP
---
apiVersion: v1
kind: Service
metadata:
  name: my-web-app
  annotations:
    service.nlb.kube-dc.com/expose-route: "https"
spec:
  type: LoadBalancer
  selector:
    app: my-web-app
  ports:
  - port: 80
    targetPort: 80
```

**Access:** `https://my-web-app-{namespace}.{base_domain}`

### Custom Hostname

```yaml
apiVersion: v1
kind: Service
metadata:
  name: my-web-app
  annotations:
    service.nlb.kube-dc.com/expose-route: "https"
    service.nlb.kube-dc.com/route-hostname: "app.example.com"
spec:
  type: LoadBalancer
  ...
```

**Note:** Custom hostname requires:
1. DNS pointing to Gateway IP
2. Issuer that can validate that domain (may need DNS-01)

## Prerequisites

### Existing Infrastructure

| Component | Status | Notes |
|-----------|--------|-------|
| cert-manager | ✅ Installed | Handles certificate lifecycle |
| Issuer (letsencrypt) | ✅ Available | HTTP-01 challenge via Gateway |
| Gateway API CRDs | ✅ Installed | ReferenceGrant, HTTPRoute |
| Envoy Gateway | ✅ Running | Supports dynamic listeners |

### New Requirements

| Component | Action | Priority |
|-----------|--------|----------|
| Controller RBAC | Add Gateway patch permission | High |
| Controller Logic | Implement HTTPS route sync | High |
| Default Issuer | Configure in master-config | Medium |
| Listener cleanup | Implement deletion logic | High |

## Testing Plan

### Unit Tests

- [ ] Certificate name generation
- [ ] Listener name generation (avoid conflicts)
- [ ] ReferenceGrant creation
- [ ] Gateway patch logic

### Integration Tests

- [ ] Certificate issuance flow
- [ ] Gateway listener addition/removal
- [ ] HTTPRoute attachment to listener
- [ ] End-to-end HTTPS access

### E2E Tests

- [ ] Create service with `expose-route: https`
- [ ] Verify certificate issued
- [ ] Verify HTTPS access works
- [ ] Verify cleanup on service deletion

## Rollout Plan

### Phase 1: Implementation
- Implement controller logic
- Add RBAC permissions
- Unit tests

### Phase 2: Testing
- Integration tests in dev cluster
- E2E tests

### Phase 3: Documentation
- Update examples
- Update README

### Phase 4: Release
- Deploy to staging
- User acceptance testing
- Production rollout

## Open Questions

1. **Issuer Configuration**: Should we require an Issuer per namespace or use a ClusterIssuer?
   - Recommendation: Support both, default to namespace Issuer named `letsencrypt`

2. **Certificate Renewal**: How to handle certificate renewal with listener?
   - cert-manager handles renewal automatically, secret is updated in place

3. **Listener Limits**: Is there a max number of listeners on Gateway?
   - Envoy Gateway: No hard limit, but monitor performance

4. **Custom Domains**: How to handle DNS validation for non-base-domain hostnames?
   - Recommendation: Require DNS-01 capable Issuer for custom domains

## Future Enhancements

1. **Wildcard Certificate Option**: Single `*.{base_domain}` cert for simpler setup
2. **mTLS Support**: Client certificate verification
3. **HTTP→HTTPS Redirect**: Automatic redirect from HTTP to HTTPS
4. **HSTS Headers**: Automatic HSTS header injection
