# PRD: Public Ingress Exposure for Tenant Clusters

## Overview

This document defines the requirements for exposing tenant Kubernetes API servers and LoadBalancer services publicly via SSL passthrough ingress. The pattern enables external access to services running within the cloud network (`ext-cloud`) through public gateway nodes.

## Problem Statement

Currently, tenant clusters (KdcCluster) have their API servers exposed on the internal cloud network (`ext-cloud`, e.g., `100.65.0.0/16`). To enable external access:

1. **SSL Passthrough** is required for K8s API servers (clients verify server certificates)
2. **Certificate SANs** must include public hostnames for TLS validation
3. **Proxy services** must be created to route traffic from gateway to internal endpoints
4. **DNS records** must be configured for public domain resolution

## Architecture

```
Internet
    │
    │ HTTPS (port 443)
    ▼
┌─────────────────────────────────────────────────────────────────┐
│               Public Gateway (hostNetwork)                       │
│               nginx-ingress / Higress / Envoy Gateway           │
│                                                                 │
│   SNI: demo-cluster-cp.stage.kube-dc.com                       │
│         └──► Route to ClusterIP proxy service                   │
└─────────────────────────────────────────────────────────────────┘
    │
    │ TCP Proxy (via ClusterIP Service + Endpoints)
    ▼
┌─────────────────────────────────────────────────────────────────┐
│               OVN Cloud Network (ext-cloud)                      │
│                                                                 │
│   LoadBalancer Service: 100.65.0.105:6443                       │
│         └──► Kamaji TenantControlPlane Pod                      │
└─────────────────────────────────────────────────────────────────┘
```

## Components Affected

### 1. kube-dc-k8-manager (KdcCluster Controller)

**File**: `internal/controller/kdccluster_controller.go`

#### Current Implementation

```go
func (r *KdcClusterReconciler) reconcileTenantControlPlane(...) error {
    tcp = &kamajiv1alpha1.TenantControlPlane{
        Spec: kamajiv1alpha1.TenantControlPlaneSpec{
            NetworkProfile: kamajiv1alpha1.NetworkProfileSpec{
                Port: 6443,
                // Missing: CertSANs for public hostname
            },
        },
    }
}
```

#### Required Changes

1. **Add `PublicExposure` field to KdcClusterSpec**:

```go
// In api/v1alpha1/kdccluster_types.go

// PublicExposureSpec defines public ingress exposure configuration
type PublicExposureSpec struct {
    // Enable public exposure via SSL passthrough ingress
    // +kubebuilder:default=false
    // +optional
    Enabled bool `json:"enabled,omitempty"`

    // Public domain suffix (e.g., "stage.kube-dc.com")
    // Final hostname: {cluster-name}-cp.{domainSuffix}
    // +optional
    DomainSuffix string `json:"domainSuffix,omitempty"`

    // Additional certificate SANs to add to the API server certificate
    // Auto-generated public hostname is always included when enabled
    // +optional
    AdditionalCertSANs []string `json:"additionalCertSANs,omitempty"`
}

type KdcClusterSpec struct {
    // ... existing fields ...

    // Public exposure configuration for API server
    // +optional
    PublicExposure PublicExposureSpec `json:"publicExposure,omitempty"`
}
```

2. **Update `reconcileTenantControlPlane` to include certSANs**:

```go
func (r *KdcClusterReconciler) reconcileTenantControlPlane(ctx context.Context, cluster *k8sv1alpha1.KdcCluster, dataStoreName string) error {
    // Build certSANs list
    certSANs := []string{}
    
    if cluster.Spec.PublicExposure.Enabled {
        // Generate public hostname
        publicHostname := fmt.Sprintf("%s-cp.%s", 
            cluster.Name, 
            cluster.Spec.PublicExposure.DomainSuffix)
        certSANs = append(certSANs, publicHostname)
        
        // Add any additional SANs
        certSANs = append(certSANs, cluster.Spec.PublicExposure.AdditionalCertSANs...)
    }

    tcp = &kamajiv1alpha1.TenantControlPlane{
        Spec: kamajiv1alpha1.TenantControlPlaneSpec{
            NetworkProfile: kamajiv1alpha1.NetworkProfileSpec{
                Port:     6443,
                CertSANs: certSANs,  // Add this field
            },
        },
    }
    // ...
}
```

3. **Create proxy service and ingress for public exposure**:

```go
func (r *KdcClusterReconciler) reconcilePublicExposure(ctx context.Context, cluster *k8sv1alpha1.KdcCluster) error {
    if !cluster.Spec.PublicExposure.Enabled {
        return nil
    }

    // Get the LoadBalancer service external IP
    tcpSvc := &corev1.Service{}
    tcpName := fmt.Sprintf("%s-cp", cluster.Name)
    if err := r.Get(ctx, types.NamespacedName{Name: tcpName, Namespace: cluster.Namespace}, tcpSvc); err != nil {
        return err
    }
    
    externalIP := ""
    for _, ingress := range tcpSvc.Status.LoadBalancer.Ingress {
        if ingress.IP != "" {
            externalIP = ingress.IP
            break
        }
    }
    if externalIP == "" {
        return fmt.Errorf("LoadBalancer IP not yet assigned")
    }

    // Create proxy service in ingress namespace
    proxyServiceName := fmt.Sprintf("%s-api-proxy", cluster.Name)
    publicHostname := fmt.Sprintf("%s-cp.%s", cluster.Name, cluster.Spec.PublicExposure.DomainSuffix)
    
    // 1. Create ClusterIP Service
    proxySvc := &corev1.Service{
        ObjectMeta: metav1.ObjectMeta{
            Name:      proxyServiceName,
            Namespace: "default",  // Or dedicated ingress namespace
            Labels: map[string]string{
                "kube-dc.com/managed-by":     "kdccluster-controller",
                "kube-dc.com/cluster":        cluster.Name,
                "kube-dc.com/cluster-ns":     cluster.Namespace,
                "kube-dc.com/public-exposure": "true",
            },
        },
        Spec: corev1.ServiceSpec{
            Type:      corev1.ServiceTypeClusterIP,
            ClusterIP: corev1.ClusterIPNone,  // Headless
            Ports: []corev1.ServicePort{{
                Name:     "https",
                Port:     6443,
                Protocol: corev1.ProtocolTCP,
            }},
        },
    }

    // 2. Create Endpoints pointing to ext-cloud IP
    endpoints := &corev1.Endpoints{
        ObjectMeta: metav1.ObjectMeta{
            Name:      proxyServiceName,
            Namespace: "default",
        },
        Subsets: []corev1.EndpointSubset{{
            Addresses: []corev1.EndpointAddress{{
                IP: externalIP,
            }},
            Ports: []corev1.EndpointPort{{
                Name:     "https",
                Port:     6443,
                Protocol: corev1.ProtocolTCP,
            }},
        }},
    }

    // 3. Create Ingress with SSL passthrough
    ingress := &networkingv1.Ingress{
        ObjectMeta: metav1.ObjectMeta{
            Name:      proxyServiceName,
            Namespace: "default",
            Annotations: map[string]string{
                "nginx.ingress.kubernetes.io/ssl-passthrough": "true",
                "nginx.ingress.kubernetes.io/backend-protocol": "HTTPS",
            },
        },
        Spec: networkingv1.IngressSpec{
            IngressClassName: ptr.To("nginx"),
            Rules: []networkingv1.IngressRule{{
                Host: publicHostname,
                IngressRuleValue: networkingv1.IngressRuleValue{
                    HTTP: &networkingv1.HTTPIngressRuleValue{
                        Paths: []networkingv1.HTTPIngressPath{{
                            Path:     "/",
                            PathType: ptr.To(networkingv1.PathTypePrefix),
                            Backend: networkingv1.IngressBackend{
                                Service: &networkingv1.IngressServiceBackend{
                                    Name: proxyServiceName,
                                    Port: networkingv1.ServiceBackendPort{
                                        Number: 6443,
                                    },
                                },
                            },
                        }},
                    },
                },
            }},
        },
    }

    // Apply resources...
    return nil
}
```

4. **Update KdcCluster status with public endpoint**:

```go
type KdcClusterStatus struct {
    // ... existing fields ...

    // PublicEndpoint is the publicly accessible API server endpoint
    // +optional
    PublicEndpoint string `json:"publicEndpoint,omitempty"`
}
```

### 2. kube-dc Service Controller (Optional Enhancement)

**File**: `internal/service_lb/external_endpoint.go`

The existing `ExternalEndpointManager` already creates headless services with endpoints pointing to LoadBalancer external IPs. This can be leveraged for the proxy service pattern.

#### Current Capability

- Creates `{service-name}-ext` headless service
- Populates endpoints with LoadBalancer external IP
- Labels: `kube-dc.com/managed-by`, `kube-dc.com/source-service`

#### Optional Enhancement: Public Exposure Annotation

Add support for `expose-to-public` annotation on LoadBalancer services:

```go
// In api/kube-dc.com/v1/values.go
const (
    // ... existing constants ...
    
    // ServiceLbExposeToPublicAnnotation triggers public ingress creation
    ServiceLbExposeToPublicAnnotation = "service.nlb.kube-dc.com/expose-to-public"
    
    // ServiceLbPublicHostnameAnnotation specifies custom public hostname
    ServiceLbPublicHostnameAnnotation = "service.nlb.kube-dc.com/public-hostname"
)
```

When a LoadBalancer service has `expose-to-public: "true"`:

1. Controller creates proxy service in gateway namespace
2. Controller creates SSL passthrough ingress
3. Public hostname pattern: `{svc-name}-{namespace}.{domain-suffix}`

## Usage Examples

### Example 1: KdcCluster with Public Exposure

```yaml
apiVersion: k8s.kube-dc.com/v1alpha1
kind: KdcCluster
metadata:
  name: demo-cluster
  namespace: shalb-demo
spec:
  version: "v1.34.1"
  controlPlane:
    replicas: 2
  publicExposure:
    enabled: true
    domainSuffix: "stage.kube-dc.com"
    # Additional SANs (optional)
    additionalCertSANs:
    - "api.demo.example.com"
```

**Result**:
- API server certificate includes: `demo-cluster-cp.stage.kube-dc.com`
- Ingress created: `demo-cluster-api-proxy` with SSL passthrough
- Public endpoint: `https://demo-cluster-cp.stage.kube-dc.com:443`

### Example 2: Generic LoadBalancer Service with Public Exposure

```yaml
apiVersion: v1
kind: Service
metadata:
  name: my-tcp-service
  namespace: shalb-demo
  annotations:
    service.nlb.kube-dc.com/expose-to-public: "true"
    service.nlb.kube-dc.com/public-hostname: "myapp.stage.kube-dc.com"
spec:
  type: LoadBalancer
  ports:
  - port: 8443
    targetPort: 8443
    protocol: TCP
```

**Result**:
- LoadBalancer gets ext-cloud IP (e.g., `100.65.0.110`)
- Proxy service + endpoints created in gateway namespace
- Ingress with SSL passthrough to `myapp.stage.kube-dc.com`

## Implementation Checklist

### Phase 1: KdcCluster certSANs (Required)

- [ ] Add `PublicExposureSpec` to `KdcClusterSpec` in `api/v1alpha1/kdccluster_types.go`
- [ ] Update `reconcileTenantControlPlane()` to pass `certSANs` to Kamaji
- [ ] Update `reconcileKamajiControlPlane()` for CAPI mode
- [ ] Add `PublicEndpoint` to `KdcClusterStatus`
- [ ] Run `make generate manifests`
- [ ] Add unit tests

### Phase 2: Proxy Service & Ingress Creation (Required)

- [ ] Add `reconcilePublicExposure()` function to KdcCluster controller
- [ ] Create ClusterIP headless service in gateway namespace
- [ ] Create Endpoints with ext-cloud LoadBalancer IP
- [ ] Create Ingress with SSL passthrough annotation
- [ ] Handle cleanup on cluster deletion
- [ ] Add integration tests

### Phase 3: Generic Service Exposure (Optional)

- [ ] Add `ServiceLbExposeToPublicAnnotation` constant
- [ ] Update service controller to watch for annotation
- [ ] Create ingress for annotated services
- [ ] Document annotation usage

### Phase 4: DNS Integration (Future)

- [ ] Integrate with external-dns or custom DNS controller
- [ ] Auto-create DNS records for public hostnames
- [ ] Support wildcard DNS patterns

## Testing

### Manual Test: KdcCluster Public Exposure

```bash
# 1. Create cluster with public exposure
kubectl apply -f - <<EOF
apiVersion: k8s.kube-dc.com/v1alpha1
kind: KdcCluster
metadata:
  name: test-public
  namespace: shalb-demo
spec:
  version: "v1.34.1"
  publicExposure:
    enabled: true
    domainSuffix: "stage.kube-dc.com"
EOF

# 2. Wait for cluster ready
kubectl wait kdccluster test-public -n shalb-demo --for=condition=Ready --timeout=300s

# 3. Verify certSANs
kubectl get tenantcontrolplane test-public-cp -n shalb-demo -o jsonpath='{.spec.networkProfile.certSANs}'
# Expected: ["test-public-cp.stage.kube-dc.com"]

# 4. Verify ingress created
kubectl get ingress test-public-api-proxy -n default

# 5. Test public access
kubectl --kubeconfig=<(kubectl get secret test-public-cp-admin-kubeconfig -n shalb-demo -o jsonpath='{.data.admin\.conf}' | base64 -d | sed 's|server:.*|server: https://test-public-cp.stage.kube-dc.com:443|') get nodes
```

## Security Considerations

1. **Certificate Validation**: Ensure public hostname is in certSANs before creating ingress
2. **Network Policies**: Gateway pods need access to ext-cloud network
3. **RBAC**: Controller needs permissions to create ingress in gateway namespace
4. **Rate Limiting**: Consider rate limiting on public endpoints
5. **Audit Logging**: Log public exposure changes

## Dependencies

- nginx-ingress-controller with `--enable-ssl-passthrough` flag
- DNS configured for `*.stage.kube-dc.com` pointing to gateway nodes
- Network connectivity: gateway nodes → ext-cloud network (via SNAT/routes)

---

# Part 2: Customer Self-Service Public Exposure

## Overview

This section describes how customers can expose their own services (websites, APIs) running in the cloud network to the public internet using Gateway API resources created in their own namespace.

## Two Exposure Patterns

| Pattern | Use Case | Certificate Location | Gateway Role |
|---------|----------|---------------------|--------------|
| **TLS Passthrough** | K8s API, databases, custom TLS apps | Backend (customer) | L4 proxy (SNI routing) |
| **TLS Termination** | Websites, REST APIs | Gateway (Let's Encrypt) | L7 proxy (HTTP routing) |

### Pattern 1: TLS Passthrough (Backend Owns Certificate)

```
Customer App (has own cert) ←── Gateway (SNI routing) ←── Internet
                                    │
                                    └── TLSRoute (customer namespace)
```

- Customer's service terminates TLS
- Gateway only routes based on SNI header
- No certificate management at gateway level

### Pattern 2: TLS Termination (Gateway Owns Certificate)

```
Customer App (plain HTTP) ←── Gateway (TLS termination) ←── Internet
                                    │                          │
                                    └── HTTPRoute              └── Let's Encrypt cert
                                        (customer namespace)
```

- Gateway terminates TLS using Let's Encrypt certificate
- Traffic to backend can be HTTP or re-encrypted
- cert-manager automates certificate issuance

## Architecture: Customer Self-Service

```
┌─────────────────────────────────────────────────────────────────────────────┐
│                     Customer Project Namespace (shalb-demo)                  │
│                                                                             │
│  ┌─────────────────────────────────────────────────────────────────────┐   │
│  │  Service (LoadBalancer on ext-cloud)                                │   │
│  │  name: my-website                                                   │   │
│  │  EIP: 100.65.0.120:443                                             │   │
│  └─────────────────────────────────────────────────────────────────────┘   │
│                                                                             │
│  ┌─────────────────────────────────────────────────────────────────────┐   │
│  │  HTTPRoute (TLS Termination) OR TLSRoute (Passthrough)              │   │
│  │  hostname: mywebsite.example.com                                    │   │
│  │  backendRef: points to proxy service in gateway namespace           │   │
│  └─────────────────────────────────────────────────────────────────────┘   │
│                                                                             │
│  ┌─────────────────────────────────────────────────────────────────────┐   │
│  │  Certificate (cert-manager)                                         │   │
│  │  issuerRef: letsencrypt-prod                                        │   │
│  │  dnsNames: [mywebsite.example.com]                                  │   │
│  └─────────────────────────────────────────────────────────────────────┘   │
└─────────────────────────────────────────────────────────────────────────────┘
                                    │
                                    │ Cross-namespace reference via ReferenceGrant
                                    ▼
┌─────────────────────────────────────────────────────────────────────────────┐
│                        Gateway Namespace (higress-system)                    │
│                                                                             │
│  ┌─────────────────────────────────────────────────────────────────────┐   │
│  │  Gateway (shared)                                                   │   │
│  │  listeners:                                                         │   │
│  │    - name: https-terminate (port 443, TLS Terminate)               │   │
│  │    - name: tls-passthrough (port 8443, TLS Passthrough)            │   │
│  │  allowedRoutes:                                                     │   │
│  │    namespaces: {from: All}  # Customer routes attach here          │   │
│  └─────────────────────────────────────────────────────────────────────┘   │
│                                                                             │
│  ┌─────────────────────────────────────────────────────────────────────┐   │
│  │  ReferenceGrant (auto-created by controller)                        │   │
│  │  from: [customer namespace] kind: HTTPRoute/TLSRoute               │   │
│  │  to: Service (proxy services)                                       │   │
│  └─────────────────────────────────────────────────────────────────────┘   │
└─────────────────────────────────────────────────────────────────────────────┘
```

## Gateway API Resources

### Shared Gateway Configuration

```yaml
apiVersion: gateway.networking.k8s.io/v1
kind: Gateway
metadata:
  name: public-gateway
  namespace: higress-system
  annotations:
    # cert-manager will create certs for TLS Terminate listeners
    cert-manager.io/cluster-issuer: letsencrypt-prod
spec:
  gatewayClassName: higress  # or envoy-gateway
  listeners:
  # TLS Termination listener (Let's Encrypt certs)
  - name: https-terminate
    protocol: HTTPS
    port: 443
    hostname: "*.stage.kube-dc.com"
    tls:
      mode: Terminate
      certificateRefs:
      - name: wildcard-stage-kube-dc-com
        kind: Secret
    allowedRoutes:
      namespaces:
        from: All  # Allow routes from any namespace
      kinds:
      - kind: HTTPRoute
  
  # TLS Passthrough listener (customer certs)
  - name: tls-passthrough
    protocol: TLS
    port: 8443
    hostname: "*.stage.kube-dc.com"
    tls:
      mode: Passthrough
    allowedRoutes:
      namespaces:
        from: All
      kinds:
      - kind: TLSRoute
```

### Customer HTTPRoute (TLS Termination)

Customer creates in their namespace:

```yaml
apiVersion: gateway.networking.k8s.io/v1
kind: HTTPRoute
metadata:
  name: my-website
  namespace: shalb-demo  # Customer's namespace
spec:
  parentRefs:
  - name: public-gateway
    namespace: higress-system
    sectionName: https-terminate
  hostnames:
  - "mywebsite.stage.kube-dc.com"
  rules:
  - matches:
    - path:
        type: PathPrefix
        value: /
    backendRefs:
    - name: my-website-proxy  # Proxy service created by controller
      namespace: higress-system
      port: 443
```

### Customer TLSRoute (SSL Passthrough)

For services where customer manages their own certificate:

```yaml
apiVersion: gateway.networking.k8s.io/v1alpha2
kind: TLSRoute
metadata:
  name: my-secure-api
  namespace: shalb-demo
spec:
  parentRefs:
  - name: public-gateway
    namespace: higress-system
    sectionName: tls-passthrough
  hostnames:
  - "api.stage.kube-dc.com"
  rules:
  - backendRefs:
    - name: my-secure-api-proxy
      namespace: higress-system
      port: 8443
```

### ReferenceGrant (Auto-created by Controller)

The kube-dc controller creates this to allow cross-namespace references:

```yaml
apiVersion: gateway.networking.k8s.io/v1beta1
kind: ReferenceGrant
metadata:
  name: allow-shalb-demo-routes
  namespace: higress-system  # Target namespace (gateway)
spec:
  from:
  - group: gateway.networking.k8s.io
    kind: HTTPRoute
    namespace: shalb-demo  # Source namespace (customer)
  - group: gateway.networking.k8s.io
    kind: TLSRoute
    namespace: shalb-demo
  to:
  - group: ""
    kind: Service
```

## cert-manager Integration

### ClusterIssuer for Let's Encrypt

```yaml
apiVersion: cert-manager.io/v1
kind: ClusterIssuer
metadata:
  name: letsencrypt-prod
spec:
  acme:
    server: https://acme-v02.api.letsencrypt.org/directory
    email: admin@kube-dc.com
    privateKeySecretRef:
      name: letsencrypt-prod-account-key
    solvers:
    # HTTP-01 challenge via Gateway
    - http01:
        gatewayHTTPRoute:
          parentRefs:
          - kind: Gateway
            name: public-gateway
            namespace: higress-system
```

### Customer Certificate Request

Customer can request specific certificate:

```yaml
apiVersion: cert-manager.io/v1
kind: Certificate
metadata:
  name: mywebsite-tls
  namespace: shalb-demo
spec:
  secretName: mywebsite-tls-secret
  issuerRef:
    name: letsencrypt-prod
    kind: ClusterIssuer
  dnsNames:
  - mywebsite.example.com  # Customer's own domain
  - www.mywebsite.example.com
```

## Controller Implementation

### New CRD: PublicExposure

```yaml
apiVersion: kube-dc.com/v1
kind: PublicExposure
metadata:
  name: my-website
  namespace: shalb-demo
spec:
  # Reference to the LoadBalancer service in cloud network
  serviceRef:
    name: my-website
    port: 443
  
  # Public hostname(s)
  hostnames:
  - "mywebsite.stage.kube-dc.com"
  
  # Exposure mode
  mode: HTTPRoute  # or TLSRoute for passthrough
  
  # TLS configuration
  tls:
    # For HTTPRoute: use Let's Encrypt
    issuerRef:
      name: letsencrypt-prod
      kind: ClusterIssuer
    # OR for TLSRoute: passthrough (no cert needed at gateway)
    mode: Passthrough
  
  # Optional: customer's own domain (requires DNS validation)
  customDomain:
    hostname: "www.mywebsite.example.com"
    # DNS-01 challenge for custom domains
    dnsChallenge: true
```

### Controller Logic

```go
func (r *PublicExposureReconciler) Reconcile(ctx context.Context, req ctrl.Request) (ctrl.Result, error) {
    pe := &kubedcv1.PublicExposure{}
    if err := r.Get(ctx, req.NamespacedName, pe); err != nil {
        return ctrl.Result{}, client.IgnoreNotFound(err)
    }

    // 1. Get the source LoadBalancer service
    svc := &corev1.Service{}
    if err := r.Get(ctx, types.NamespacedName{
        Name:      pe.Spec.ServiceRef.Name,
        Namespace: pe.Namespace,
    }, svc); err != nil {
        return ctrl.Result{}, err
    }

    // 2. Get external IP from cloud network
    externalIP := getLoadBalancerIP(svc)
    if externalIP == "" {
        return ctrl.Result{RequeueAfter: 10 * time.Second}, nil
    }

    // 3. Create proxy service in gateway namespace
    proxyServiceName := fmt.Sprintf("%s-%s-proxy", pe.Namespace, pe.Name)
    if err := r.reconcileProxyService(ctx, pe, proxyServiceName, externalIP); err != nil {
        return ctrl.Result{}, err
    }

    // 4. Create ReferenceGrant allowing cross-namespace reference
    if err := r.reconcileReferenceGrant(ctx, pe); err != nil {
        return ctrl.Result{}, err
    }

    // 5. Create route based on mode
    switch pe.Spec.Mode {
    case "HTTPRoute":
        // 5a. Create Certificate if using Let's Encrypt
        if pe.Spec.TLS.IssuerRef != nil {
            if err := r.reconcileCertificate(ctx, pe); err != nil {
                return ctrl.Result{}, err
            }
        }
        // 5b. Create HTTPRoute
        if err := r.reconcileHTTPRoute(ctx, pe, proxyServiceName); err != nil {
            return ctrl.Result{}, err
        }
    case "TLSRoute":
        // 5c. Create TLSRoute for passthrough
        if err := r.reconcileTLSRoute(ctx, pe, proxyServiceName); err != nil {
            return ctrl.Result{}, err
        }
    }

    return ctrl.Result{}, nil
}
```

## Customer Usage Examples

### Example 1: Simple Website (TLS Termination with Let's Encrypt)

```yaml
# Customer creates LoadBalancer service for their app
apiVersion: v1
kind: Service
metadata:
  name: my-blog
  namespace: shalb-demo
spec:
  type: LoadBalancer
  ports:
  - port: 80
    targetPort: 8080
  selector:
    app: my-blog
---
# Customer requests public exposure
apiVersion: kube-dc.com/v1
kind: PublicExposure
metadata:
  name: my-blog
  namespace: shalb-demo
spec:
  serviceRef:
    name: my-blog
    port: 80
  hostnames:
  - "blog.stage.kube-dc.com"
  mode: HTTPRoute
  tls:
    issuerRef:
      name: letsencrypt-prod
      kind: ClusterIssuer
```

**Result**:
- Let's Encrypt certificate auto-issued for `blog.stage.kube-dc.com`
- HTTPRoute created, attached to shared Gateway
- Website accessible at `https://blog.stage.kube-dc.com`

### Example 2: Secure API (TLS Passthrough with Own Certificate)

```yaml
apiVersion: v1
kind: Service
metadata:
  name: secure-api
  namespace: shalb-demo
spec:
  type: LoadBalancer
  ports:
  - port: 443
    targetPort: 8443
  selector:
    app: secure-api
---
apiVersion: kube-dc.com/v1
kind: PublicExposure
metadata:
  name: secure-api
  namespace: shalb-demo
spec:
  serviceRef:
    name: secure-api
    port: 443
  hostnames:
  - "api.stage.kube-dc.com"
  mode: TLSRoute
  tls:
    mode: Passthrough  # Customer manages their own cert
```

**Result**:
- TLSRoute created for SNI-based routing
- Customer's app terminates TLS with its own certificate
- Gateway passes through encrypted traffic

### Example 3: Custom Domain (Customer's Own Domain)

```yaml
apiVersion: kube-dc.com/v1
kind: PublicExposure
metadata:
  name: my-saas
  namespace: shalb-demo
spec:
  serviceRef:
    name: my-saas-app
    port: 443
  hostnames:
  - "app.stage.kube-dc.com"  # Platform domain
  mode: HTTPRoute
  tls:
    issuerRef:
      name: letsencrypt-prod
      kind: ClusterIssuer
  # Customer's custom domain
  customDomain:
    hostname: "app.customer-corp.com"
    # Customer must add CNAME: app.customer-corp.com → app.stage.kube-dc.com
    dnsChallenge: false  # Use HTTP-01 after CNAME is set
```

## Implementation Phases

### Phase 1: Basic HTTPRoute/TLSRoute Support

- [ ] Install Gateway API CRDs
- [ ] Configure shared Gateway with multiple listeners
- [ ] Create PublicExposure CRD
- [ ] Implement controller to create proxy services, routes, ReferenceGrants

### Phase 2: cert-manager Integration

- [ ] Install cert-manager
- [ ] Configure ClusterIssuer for Let's Encrypt
- [ ] Auto-create Certificates for HTTPRoute exposures
- [ ] Support wildcard certificates for platform domains

### Phase 3: Custom Domain Support

- [ ] DNS-01 challenge support for custom domains
- [ ] CNAME validation before certificate issuance
- [ ] Documentation for customers on DNS setup

### Phase 4: Advanced Features

- [ ] Rate limiting per PublicExposure
- [ ] WAF integration
- [ ] Metrics and monitoring per exposure
- [ ] Cost allocation/billing integration

## Security Considerations

1. **ReferenceGrant Scope**: Only create ReferenceGrants for projects with valid subscriptions
2. **Rate Limiting**: Prevent abuse of Let's Encrypt rate limits
3. **Domain Validation**: Verify customer owns custom domains before issuance
4. **Network Policies**: Ensure gateway can only reach authorized backends
5. **Audit Logging**: Log all PublicExposure create/update/delete events

## References

- [Kamaji TenantControlPlane NetworkProfile](https://kamaji.clastix.io/reference/api/)
- [nginx-ingress SSL Passthrough](https://kubernetes.github.io/ingress-nginx/user-guide/tls/#ssl-passthrough)
- [Kubernetes Gateway API TLSRoute](https://gateway-api.sigs.k8s.io/guides/tls/)
- [Gateway API ReferenceGrant (GEP-709)](https://gateway-api.sigs.k8s.io/geps/gep-709/)
- [cert-manager Gateway Integration](https://cert-manager.io/docs/usage/gateway/)
- [Let's Encrypt HTTP-01 Challenge](https://letsencrypt.org/docs/challenge-types/)
