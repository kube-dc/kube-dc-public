# PRD: ACME HTTP-01 Challenge Integration with Envoy Gateway

## Status

| Component | Status |
|-----------|--------|
| Gateway Backend auto-creation | ✅ **Implemented** (commit `dac0122`) |
| Shared Envoy Gateway | ✅ **Deployed** (replaces nginx-ingress) |
| HTTPRoute Controller (mutation) | 🔲 Pending |
| ACME Solver Service detection | 🔲 Pending |
| Manual test | ✅ **Verified** |

## Business Context

**Goal:** Minimize IPv4 usage per tenant by using shared gateways for traffic multiplexing.

**Architecture Vision:**
```
┌─────────────────────────────────────────────────────────────────────┐
│              Shared Envoy Gateway: eg (envoy-gateway-system)        │
│                         88.99.29.250                                │
├─────────────────────────────────────────────────────────────────────┤
│  HTTP :80 (ACME + redirect)  │  TLS :6443 (passthrough)            │
│           ↓                  │           ↓                          │
│  HTTPRoute → Backend         │  TLSRoute → Backend                  │
│  (solver ext-cloud IP)       │  (tenant K8s API ext-cloud IP)       │
├─────────────────────────────────────────────────────────────────────┤
│  HTTPS :443 (per-host TLS termination for platform services)       │
└─────────────────────────────────────────────────────────────────────┘
                ↓                           ↓
┌───────────────────────────┐  ┌───────────────────────────┐
│  Customer A (ext-cloud)   │  │  Customer B (ext-cloud)   │
│  100.65.0.10              │  │  100.65.0.20              │
│  - Gets cert via ACME     │  │  - Gets cert via ACME     │
│  - Terminates TLS itself  │  │  - Terminates TLS itself  │
│  - No public IP needed    │  │  - No public IP needed    │
└───────────────────────────┘  └───────────────────────────┘
```

**Key Benefits:**
- **1000 customers, 1-3 public IPs** (instead of 1000 public IPs)
- **No certificate sharing** - gateway never sees customer certs (TLS passthrough)
- **Customer isolation** - each customer terminates TLS in their own namespace
- **Standard Let's Encrypt** - customers use HTTP-01 challenge, no DNS API needed

## Problem Statement

Customers want to use Let's Encrypt certificates with HTTP-01 challenges in their project namespaces. However, the current network isolation between Envoy Gateway (`envoy-gateway-system` / `ovn-default`) and project namespaces (isolated subnets) prevents cert-manager's ACME solver from working with the shared Gateway.

### Scope: Cloud Networking Only

This solution applies **only to cloud networking** (`ext-cloud` / `externalNetworkType: cloud`):

| Network Type | Gateway can reach Service? | Solution Needed? |
|--------------|---------------------------|------------------|
| **Public** (`ext-public`) | ✅ Yes - routable public IP | ❌ No - standard routing works |
| **Cloud** (`ext-cloud`) | ❌ No - isolated VPC subnet | ✅ Yes - Backend required |

For **public network**, the Gateway can directly route to services via their public IPs - no Backend workaround needed.

For **cloud network**, services get private IPs (e.g., `100.65.x.x`) that are only reachable via static routes on nodes. The Gateway (in `ovn-default`) cannot reach these IPs directly, hence the Backend + ext-cloud IP pattern.

## Current Architecture

### What Works: TLS Passthrough with Backend

```
Gateway → TLSRoute → Backend (ext-cloud IP) → K8s API Service
```

We use a **Backend resource** pointing to an **external IP** (ext-cloud), which is reachable from the Gateway via static routes on nodes.

```yaml
apiVersion: gateway.envoyproxy.io/v1alpha1
kind: Backend
metadata:
  name: demo-cluster-api
  namespace: shalb-demo
spec:
  endpoints:
  - ip:
      address: 100.65.0.104  # ext-cloud IP - reachable from Gateway
      port: 6443
```

### What Doesn't Work: ACME HTTP-01 Challenge

cert-manager's `gatewayHTTPRoute` solver creates:

```yaml
apiVersion: gateway.networking.k8s.io/v1
kind: HTTPRoute
spec:
  backendRefs:
  - kind: Service  # ← Points to ClusterIP Service directly
    name: cm-acme-http-solver-xyz
    namespace: shalb-demo
    port: 8089
```

**Why it fails:**
1. Gateway tries to reach ClusterIP (`10.x.x.x`) in isolated project subnet
2. Gateway (in `ovn-default`) can't reach project subnet due to network isolation
3. ACME challenge fails with connection timeout

## Root Cause

| Resource | Created By | Points To | Reachable? |
|----------|------------|-----------|------------|
| TLSRoute (K8s CP) | User | Backend with ext-cloud IP | ✓ Yes |
| HTTPRoute (ACME) | cert-manager | Service (ClusterIP) | ✗ No |

The difference: we control Backend creation for K8s CP, but cert-manager hardcodes Service references for ACME challenges.

## Proposed Solution

### Controller-Based Approach (Recommended)

Instead of using a Mutating Admission Webhook, we use a **controller-based reconciliation pattern**. This approach:

1. **Follows Kubernetes best practices** - Controllers are the standard way to manage resources
2. **No webhook latency** - Webhooks add latency to API server requests
3. **Simpler deployment** - No need for webhook certificates, TLS configuration
4. **Idempotent** - Controllers naturally handle retries and eventual consistency
5. **Easier debugging** - Controller logs are easier to trace than webhook calls

**Why not Mutating Webhook?**
- Webhooks intercept requests synchronously, adding latency
- Webhook failures can block resource creation entirely
- Requires additional TLS certificate management
- More complex failure modes

**Controller Pattern:**
- Watch HTTPRoutes with ACME solver labels
- Reconcile by modifying `backendRefs` from Service → Backend
- Idempotent updates - if already correct, no change needed

### Architecture

```
┌─────────────────────────────────────────────────────────────┐
│  HTTPRoute Controller                                       │
│  (existing kube-dc-manager)                                 │
│                                                             │
│  Watches:                                                   │
│  - HTTPRoutes with label acme.cert-manager.io/http-domain   │
│  - Services with label acme.cert-manager.io/http01-solver   │
│                                                             │
│  Actions:                                                   │
│  1. When ACME solver Service created:                       │
│     → Convert to LoadBalancer (bind to default-gw EIP)      │
│     → Auto-creates Backend via existing annotation          │
│                                                             │
│  2. When ACME HTTPRoute created:                            │
│     → Patch backendRef: Service → Backend                   │
│     → Gateway can now route to ext-cloud IP                 │
└─────────────────────────────────────────────────────────────┘
```

### Flow Diagram

```
1. Customer creates Certificate resource
                ↓
2. cert-manager creates Challenge
                ↓
3. cert-manager creates Solver Pod + Service (ClusterIP)
   (label: acme.cert-manager.io/http01-solver=true)
                ↓
4. [CONTROLLER] Detects ACME solver Service
   → Patches Service: type=LoadBalancer, annotations for EIP + Backend
   → Existing service controller creates:
     - EIP (externalNetworkType: cloud)
     - Backend pointing to EIP (via create-gateway-backend annotation)
                ↓
5. cert-manager creates HTTPRoute (pointing to Service)
   (label: acme.cert-manager.io/http-domain=<domain>)
                ↓
6. [CONTROLLER] Detects ACME HTTPRoute
   → Patches backendRef: Service → Backend
   → Uses Backend name: <service-name>-backend
                ↓
7. Gateway routes ACME challenge to Backend (ext-cloud IP)
                ↓
8. ACME challenge succeeds
                ↓
9. Certificate issued
                ↓
10. cert-manager deletes solver resources
    → Service deletion cascades to Backend (ownerReference)
```

## Implementation Details

### Implemented: Gateway Backend Auto-Creation

**Already implemented** in commit `dac0122`. When a LoadBalancer service has the annotation:

```yaml
annotations:
  service.nlb.kube-dc.com/create-gateway-backend: "true"
```

The service controller automatically creates a Backend resource pointing to the service's external IP.

**Usage:**
```yaml
apiVersion: v1
kind: Service
metadata:
  name: my-app
  annotations:
    service.nlb.kube-dc.com/bind-on-eip: default-gw
    service.nlb.kube-dc.com/create-gateway-backend: "true"
spec:
  type: LoadBalancer
  ports:
  - port: 8089
```

This creates:
- LoadBalancer with ext-cloud IP (e.g., `100.65.0.102`)
- Backend `my-app-backend` pointing to `100.65.0.102:8089`

### Part 1: ACME Solver Service Controller

Extend the existing service controller to detect ACME solver services and automatically add the required annotations. **Only applies to cloud networking.**

**Watch Predicate:**
```go
func isACMESolverService(obj client.Object) bool {
    labels := obj.GetLabels()
    return labels["acme.cert-manager.io/http01-solver"] == "true"
}
```

**Reconcile Logic:**
```go
func (r *ServiceReconciler) reconcileACMESolver(ctx context.Context, svc *corev1.Service, project *kubedcv1.Project) error {
    // Check if this is an ACME solver service
    if svc.Labels["acme.cert-manager.io/http01-solver"] != "true" {
        return nil
    }
    
    // Determine network type from:
    // 1. Service annotation (explicit override)
    // 2. Project default
    netType := svc.GetAnnotations()[kubedccomv1.NetworkExternalTypeLabelKey]
    if netType == "" {
        netType = string(project.Spec.ExternalNetworkType)
    }
    
    // Only apply Backend workaround for cloud networking
    // Public network can route directly - no Backend needed
    if netType != "cloud" {
        return nil
    }
    
    // Check if already patched
    if svc.Spec.Type == corev1.ServiceTypeLoadBalancer {
        return nil
    }
    
    // Patch service to LoadBalancer with Backend annotation
    patch := client.MergeFrom(svc.DeepCopy())
    svc.Spec.Type = corev1.ServiceTypeLoadBalancer
    if svc.Annotations == nil {
        svc.Annotations = make(map[string]string)
    }
    svc.Annotations["service.nlb.kube-dc.com/bind-on-eip"] = "default-gw"
    svc.Annotations["service.nlb.kube-dc.com/create-gateway-backend"] = "true"
    
    return r.Patch(ctx, svc, patch)
}
```

**Network Type Annotation:**
Services can override project's default network type using:
```yaml
annotations:
  network.kube-dc.com/external-network-type: "cloud"  # or "public"
```

### Part 2: HTTPRoute Controller (Not Webhook)

**Why Controller instead of Mutating Webhook:**

| Aspect | Mutating Webhook | Controller |
|--------|------------------|------------|
| Timing | Synchronous (blocks API) | Asynchronous (eventual) |
| Failure mode | Blocks resource creation | Retries automatically |
| TLS certs | Required for webhook | Not needed |
| Latency | Adds to every request | No API latency |
| Debugging | Harder to trace | Controller logs |
| Best practice | For validation/defaults | For resource management |

**Controller Implementation:**
```go
// HTTPRouteReconciler watches HTTPRoutes and patches ACME solver routes
type HTTPRouteReconciler struct {
    client.Client
    Scheme *runtime.Scheme
}

// Watch predicate - only ACME HTTPRoutes
func isACMEHTTPRoute(obj client.Object) bool {
    labels := obj.GetLabels()
    _, hasACMELabel := labels["acme.cert-manager.io/http-domain"]
    return hasACMELabel
}

func (r *HTTPRouteReconciler) Reconcile(ctx context.Context, req ctrl.Request) (ctrl.Result, error) {
    route := &gatewayv1.HTTPRoute{}
    if err := r.Get(ctx, req.NamespacedName, route); err != nil {
        return ctrl.Result{}, client.IgnoreNotFound(err)
    }

    // Get project to check network type
    project := r.getProjectForNamespace(ctx, route.Namespace)
    if project == nil {
        return ctrl.Result{}, nil
    }
    
    // Only apply Backend workaround for cloud networking
    // Public network routes directly - no patching needed
    if project.Spec.ExternalNetworkType != "cloud" {
        return ctrl.Result{}, nil
    }

    // Check if already patched (backendRef is Backend, not Service)
    if r.isAlreadyPatched(route) {
        return ctrl.Result{}, nil
    }

    // Get the solver service name from backendRef
    serviceName := r.getServiceNameFromRoute(route)
    if serviceName == "" {
        return ctrl.Result{}, nil
    }

    // Check if Backend exists
    backendName := serviceName + "-backend"
    backend := &egv1alpha1.Backend{}
    if err := r.Get(ctx, types.NamespacedName{
        Name:      backendName,
        Namespace: route.Namespace,
    }, backend); err != nil {
        // Backend not ready yet, requeue
        return ctrl.Result{RequeueAfter: 2 * time.Second}, nil
    }

    // Patch HTTPRoute to use Backend instead of Service
    patch := client.MergeFrom(route.DeepCopy())
    r.patchBackendRefs(route, backendName)
    
    if err := r.Patch(ctx, route, patch); err != nil {
        return ctrl.Result{}, err
    }

    return ctrl.Result{}, nil
}

func (r *HTTPRouteReconciler) patchBackendRefs(route *gatewayv1.HTTPRoute, backendName string) {
    group := gatewayv1.Group("gateway.envoyproxy.io")
    kind := gatewayv1.Kind("Backend")
    
    for i, rule := range route.Spec.Rules {
        for j, ref := range rule.BackendRefs {
            if ref.Kind == nil || *ref.Kind == "Service" {
                route.Spec.Rules[i].BackendRefs[j].Group = &group
                route.Spec.Rules[i].BackendRefs[j].Kind = &kind
                route.Spec.Rules[i].BackendRefs[j].Name = gatewayv1.ObjectName(backendName)
            }
        }
    }
}
```

**Controller Setup:**
```go
func (r *HTTPRouteReconciler) SetupWithManager(mgr ctrl.Manager) error {
    return ctrl.NewControllerManagedBy(mgr).
        For(&gatewayv1.HTTPRoute{}).
        WithEventFilter(predicate.Funcs{
            CreateFunc: func(e event.CreateEvent) bool {
                return isACMEHTTPRoute(e.Object)
            },
            UpdateFunc: func(e event.UpdateEvent) bool {
                return isACMEHTTPRoute(e.ObjectNew)
            },
        }).
        Complete(r)
}
```

## Timing Considerations

**Race Condition:** HTTPRoute may be created before Backend exists.

**Controller Solution:**
The controller uses `RequeueAfter` to handle timing:

1. HTTPRoute created by cert-manager
2. Controller detects HTTPRoute, checks for Backend
3. If Backend doesn't exist yet → `RequeueAfter: 2 * time.Second`
4. Controller retries until Backend is ready
5. Once Backend exists, controller patches HTTPRoute

This is more robust than webhook approach because:
- No blocking of API requests
- Automatic retries with exponential backoff
- cert-manager doesn't see any errors (HTTPRoute created successfully)
- Controller handles the eventual consistency

## Customer Experience

### Before (Manual Process)
1. Create namespace-scoped Issuer with DNS-01 (requires DNS API access)
2. OR use external certificate provider
3. Manually manage certificate renewal

### After (Automated)
1. Create namespace-scoped Issuer with HTTP-01 (standard Let's Encrypt)
2. Create Certificate resource
3. Automatic issuance and renewal via shared Gateway

## End-to-End Customer Flow

### Step 1: Initial Setup (One-time)

Customer creates their application with TLS termination:

```yaml
# Application with nginx for TLS termination
apiVersion: apps/v1
kind: Deployment
metadata:
  name: my-app
  namespace: customer-project
spec:
  template:
    spec:
      containers:
      - name: nginx
        image: nginx:alpine
        volumeMounts:
        - name: tls
          mountPath: /etc/nginx/ssl
      volumes:
      - name: tls
        secret:
          secretName: my-app-tls  # Will be created by cert-manager
---
# Service with ext-cloud IP (no public IP)
apiVersion: v1
kind: Service
metadata:
  name: my-app
  namespace: customer-project
  annotations:
    ovn.kubernetes.io/service_external_ip_from_subnet: ext-cloud
spec:
  type: LoadBalancer
  ports:
  - port: 443
    targetPort: 443
```

### Step 2: Certificate Request

```yaml
# Issuer (points to shared gateway for HTTP-01)
apiVersion: cert-manager.io/v1
kind: Issuer
metadata:
  name: letsencrypt
  namespace: customer-project
spec:
  acme:
    server: https://acme-v02.api.letsencrypt.org/directory
    email: admin@customer.com
    privateKeySecretRef:
      name: letsencrypt-account-key
    solvers:
    - http01:
        gatewayHTTPRoute:
          parentRefs:
          - name: shared-gateway
            namespace: gateway-system
---
# Certificate
apiVersion: cert-manager.io/v1
kind: Certificate
metadata:
  name: my-app-tls
  namespace: customer-project
spec:
  secretName: my-app-tls
  issuerRef:
    name: letsencrypt
  dnsNames:
  - my-app.customer.example.com
```

### Step 3: Automatic ACME Challenge (Our Controller)

```
1. cert-manager creates solver pod + service
2. [Controller] Creates EIP + Backend for solver
3. [Webhook] Mutates HTTPRoute to use Backend
4. ACME challenge succeeds
5. Certificate issued to customer namespace
6. [Controller] Cleans up solver EIP + Backend
```

### Step 4: TLSRoute for Production Traffic

```yaml
# Backend pointing to customer service ext-cloud IP
apiVersion: gateway.envoyproxy.io/v1alpha1
kind: Backend
metadata:
  name: my-app-backend
  namespace: customer-project
spec:
  endpoints:
  - ip:
      address: 100.65.0.50  # Customer's ext-cloud IP
      port: 443
---
# TLSRoute through shared gateway (passthrough)
apiVersion: gateway.networking.k8s.io/v1alpha2
kind: TLSRoute
metadata:
  name: my-app
  namespace: customer-project
spec:
  parentRefs:
  - name: shared-gateway
    namespace: gateway-system
  hostnames:
  - "my-app.customer.example.com"
  rules:
  - backendRefs:
    - group: gateway.envoyproxy.io
      kind: Backend
      name: my-app-backend
      port: 443
```

### Result

```
User browser
    ↓
https://my-app.customer.example.com
    ↓
Shared Gateway (88.99.29.250:443)
    ↓ TLS passthrough (SNI routing)
Customer Service (100.65.0.50:443)
    ↓ TLS termination with Let's Encrypt cert
Application
```

**Customer uses 0 public IPs, gets valid Let's Encrypt certificate.**

### Example Customer Resources

```yaml
# 1. Issuer (customer creates)
apiVersion: cert-manager.io/v1
kind: Issuer
metadata:
  name: letsencrypt
  namespace: my-project
spec:
  acme:
    server: https://acme-v02.api.letsencrypt.org/directory
    email: admin@example.com
    privateKeySecretRef:
      name: letsencrypt-account-key
    solvers:
    - http01:
        gatewayHTTPRoute:
          parentRefs:
          - name: shared-gateway
            namespace: gateway-system
---
# 2. Certificate (customer creates)
apiVersion: cert-manager.io/v1
kind: Certificate
metadata:
  name: my-app-tls
  namespace: my-project
spec:
  secretName: my-app-tls-secret
  issuerRef:
    name: letsencrypt
  dnsNames:
  - my-app.example.com
```

Everything else (EIP, Backend, HTTPRoute mutation) happens automatically.

## Complexity Assessment

| Component | Complexity | Effort | Status |
|-----------|------------|--------|--------|
| Gateway Backend auto-creation | Low | 1 day | ✅ **Done** (commit `dac0122`) |
| ACME Solver Service detection | Low | 1 day | 🔲 Pending |
| HTTPRoute Controller | Low | 1-2 days | 🔲 Pending |
| Testing | Medium | 2-3 days | E2E with Let's Encrypt staging |
| **Total Remaining** | | **4-6 days** | |

## Current Gateway Setup

**Shared Envoy Gateway:** Single gateway shared across all projects.

```
Gateway: eg (envoy-gateway-system)
Address: 88.99.29.250
```

**Listeners:**
| Port | Protocol | Purpose | Allowed Routes |
|------|----------|---------|----------------|
| 80 | HTTP | ACME challenges, HTTP→HTTPS redirect | All namespaces |
| 443 | HTTPS | Per-host TLS termination (platform services) | All namespaces |
| 6443 | TLS | Passthrough for tenant K8s APIs | All namespaces |

**Key Design:**
- **Single Gateway** - All projects share one gateway IP
- **All namespaces allowed** - Projects can create HTTPRoutes/TLSRoutes in their namespace
- **HTTP :80 available** - ACME HTTP-01 challenges can use this listener
- **TLS Passthrough :6443** - Tenant clusters use Backend → ext-cloud IP pattern

## Prerequisites

1. ✅ cert-manager installed with Gateway API support
2. ✅ Shared Gateway with HTTP listener (port 80)
3. ✅ Gateway allows routes from all namespaces
4. ✅ Envoy Gateway deployed (replaces nginx-ingress)
5. ✅ DNS pointing to Gateway IP (88.99.29.250)

## Risks and Mitigations

| Risk | Mitigation |
|------|------------|
| EIP exhaustion | Solver uses project's default-gw EIP (shared, no new EIP) |
| Controller timing | RequeueAfter handles eventual consistency |
| cert-manager version changes | Watch cert-manager release notes for HTTPRoute changes |
| Stale Backends | OwnerReferences ensure cleanup when solver Service deleted |

## Alternatives Considered

| Alternative | Pros | Cons | Decision |
|-------------|------|------|----------|
| Mutating Webhook | Immediate mutation | Latency, TLS certs, complex failure modes | ❌ Rejected |
| Controller-based | Eventual consistency, simpler | Slight delay | ✅ Chosen |
| DNS-01 challenge | No HTTP routing | Requires customer DNS API access | For advanced users |
| Per-project Gateway | Full isolation | More resources, more EIPs | Not scalable |

## Success Criteria

1. Customer can issue Let's Encrypt certificates using HTTP-01 challenge
2. Certificates automatically renew without customer intervention
3. No manual EIP/Backend management required
4. Works with shared Gateway across multiple projects
5. Proper cleanup when challenges complete

## Implementation Order

1. **Replace nginx-ingress with Envoy Gateway** (external task)
   - Move HTTP :80 and HTTPS :443 to Envoy Gateway
   - Update DNS records
   - Verify existing routes work

2. **Implement ACME Solver Service detection**
   - Add predicate for `acme.cert-manager.io/http01-solver` label
   - Auto-patch ClusterIP → LoadBalancer with annotations
   - Test with manual Certificate creation

3. **Implement HTTPRoute Controller**
   - Add HTTPRoute reconciler
   - Watch for ACME HTTPRoutes
   - Patch backendRef Service → Backend
   - Test end-to-end with Let's Encrypt staging

4. **E2E Testing**
   - Test with Let's Encrypt staging environment
   - Verify certificate issuance and renewal
   - Test cleanup after challenge completion

## Future Enhancements

1. **Rate limiting:** Limit concurrent ACME challenges per project
2. **Metrics:** Track challenge success/failure rates
3. **Alerts:** Notify on certificate expiration
4. **ClusterIssuer support:** Platform-wide issuer for all projects
