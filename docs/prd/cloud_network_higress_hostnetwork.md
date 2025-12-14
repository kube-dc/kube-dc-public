# Scalable Public Ingress with Higress on hostNetwork

## Problem Statement

The current Nginx Ingress Controller has limitations when scaling to thousands of tenant clusters:

1. **Reload Instability**: Configuration changes cause temporary connection drops
2. **Long Connection Termination**: Active connections terminated during config updates (critical for K8s API watch streams)
3. **Slow Config Propagation**: 2+ minutes for route updates at 10,000+ routes
4. **SSL Passthrough Overhead**: Performance penalty when bypassing nginx for TLS passthrough
5. **Single VIP Bottleneck**: All traffic through one LoadBalancer IP

## Solution Overview

Deploy **Higress** (Alibaba's Envoy-based gateway) using **hostNetwork** on dedicated gateway nodes to achieve:

- **Wire-speed performance**: Direct network access, no NAT/DNAT overhead
- **Multiple public IPs**: Horizontal scaling across gateway nodes
- **Hot reload**: Millisecond config updates via Envoy xDS protocol
- **20,000+ routes**: Tested at scale by Sealos (87,000 users, 2,000+ tenants)

## Architecture

```
                              Internet
                                 │
                    ┌────────────┴────────────┐
                    │    DNS Round-Robin      │
                    │  *.stage.kube-dc.com    │
                    └────────────┬────────────┘
                                 │
        ┌────────────────────────┼────────────────────────┐
        │                        │                        │
        ▼                        ▼                        ▼
┌───────────────┐        ┌───────────────┐        ┌───────────────┐
│ gw-node-1     │        │ gw-node-2     │        │ gw-node-3     │
│ 168.119.17.49 │        │ 168.119.17.50 │        │ 168.119.17.51 │
│               │        │               │        │               │
│ ┌───────────┐ │        │ ┌───────────┐ │        │ ┌───────────┐ │
│ │ Higress   │ │        │ │ Higress   │ │        │ │ Higress   │ │
│ │ Gateway   │ │        │ │ Gateway   │ │        │ │ Gateway   │ │
│ │ (hostNet) │ │        │ │ (hostNet) │ │        │ │ (hostNet) │ │
│ └─────┬─────┘ │        │ └─────┬─────┘ │        │ └─────┬─────┘ │
└───────┼───────┘        └───────┼───────┘        └───────┼───────┘
        │                        │                        │
        └────────────────────────┼────────────────────────┘
                                 │
                    ┌────────────┴────────────┐
                    │    ovn-cluster VPC      │
                    │    (SNAT 100.65.0.101)  │
                    └────────────┬────────────┘
                                 │
                    ┌────────────┴────────────┐
                    │   ext-cloud Network     │
                    │   100.65.0.0/16         │
                    │                         │
                    │  ┌─────────────────┐    │
                    │  │ Tenant Clusters │    │
                    │  │ API Servers     │    │
                    │  │ 100.65.0.x:6443 │    │
                    │  └─────────────────┘    │
                    └─────────────────────────┘
```

## Why Higress?

### Comparison with Nginx Ingress

| Feature | Nginx Ingress | Higress |
|---------|---------------|---------|
| Config Update | Reload (connection drops) | Hot reload (xDS) |
| Update Time (10K routes) | ~2 minutes | ~3 seconds |
| SSL Passthrough | Performance penalty | Native Envoy |
| Long Connections | Terminated on reload | Preserved |
| Memory at Scale | High | Low |
| Nginx Compatibility | Native | Annotation support |

### Why Not Full Istio?

- Istio is a full service mesh (overkill for ingress-only)
- Higher resource consumption
- More complex operations
- Higress uses Envoy directly without Istio dependency

## Implementation

### Prerequisites

1. **Dedicated Gateway Nodes**: 2-3 nodes with public IPs on ext-public network
2. **Node Labels**: `node-role.kubernetes.io/gateway=true`
3. **DNS Configuration**: Wildcard DNS pointing to gateway node IPs

### Step 1: Label Gateway Nodes

```bash
# Label nodes that have public IPs for gateway duty
kubectl label node kube-dc-gw-1 node-role.kubernetes.io/gateway=true
kubectl label node kube-dc-gw-2 node-role.kubernetes.io/gateway=true
kubectl label node kube-dc-gw-3 node-role.kubernetes.io/gateway=true

# Optionally taint to prevent other workloads
kubectl taint node kube-dc-gw-1 node-role.kubernetes.io/gateway=true:NoSchedule
kubectl taint node kube-dc-gw-2 node-role.kubernetes.io/gateway=true:NoSchedule
kubectl taint node kube-dc-gw-3 node-role.kubernetes.io/gateway=true:NoSchedule
```

### Step 2: Install Higress

```bash
# Add Higress Helm repository
helm repo add higress https://higress.io/helm-charts
helm repo update

# Install Higress with hostNetwork configuration
helm install higress higress/higress \
  -n higress-system \
  --create-namespace \
  --set global.enableIstioAPI=false \
  --set higress-core.gateway.hostNetwork=true \
  --set higress-core.gateway.dnsPolicy=ClusterFirstWithHostNet \
  --set higress-core.gateway.nodeSelector."node-role\.kubernetes\.io/gateway"=true \
  --set higress-core.gateway.tolerations[0].key=node-role.kubernetes.io/gateway \
  --set higress-core.gateway.tolerations[0].operator=Exists \
  --set higress-core.gateway.tolerations[0].effect=NoSchedule \
  --set higress-core.gateway.replicas=3
```

### Step 3: Configure TLS Passthrough for Tenant APIs

#### Option A: Using Ingress with SSL Passthrough Annotation

```yaml
apiVersion: networking.k8s.io/v1
kind: Ingress
metadata:
  name: demo-cluster-api
  namespace: default
  annotations:
    higress.io/ssl-passthrough: "true"
spec:
  ingressClassName: higress
  rules:
  - host: demo-cluster-cp.stage.kube-dc.com
    http:
      paths:
      - path: /
        pathType: Prefix
        backend:
          service:
            name: demo-cluster-api-proxy
            port:
              number: 6443
```

#### Option B: Using Gateway API (Recommended)

```yaml
apiVersion: gateway.networking.k8s.io/v1
kind: Gateway
metadata:
  name: tenant-gateway
  namespace: higress-system
spec:
  gatewayClassName: higress
  listeners:
  - name: tls-passthrough
    port: 443
    protocol: TLS
    hostname: "*.stage.kube-dc.com"
    tls:
      mode: Passthrough
    allowedRoutes:
      namespaces:
        from: All
---
apiVersion: gateway.networking.k8s.io/v1alpha2
kind: TLSRoute
metadata:
  name: demo-cluster-api
  namespace: default
spec:
  parentRefs:
  - name: tenant-gateway
    namespace: higress-system
    sectionName: tls-passthrough
  hostnames:
  - "demo-cluster-cp.stage.kube-dc.com"
  rules:
  - backendRefs:
    - name: demo-cluster-api-proxy
      port: 6443
```

### Step 4: Backend Service Configuration

Create ClusterIP service with endpoints pointing to ext-cloud tenant API:

```yaml
apiVersion: v1
kind: Service
metadata:
  name: demo-cluster-api-proxy
  namespace: default
spec:
  type: ClusterIP
  ports:
  - name: api
    port: 6443
    protocol: TCP
---
apiVersion: v1
kind: Endpoints
metadata:
  name: demo-cluster-api-proxy
  namespace: default
subsets:
- addresses:
  - ip: 100.65.0.105  # Tenant API on ext-cloud
  ports:
  - name: api
    port: 6443
    protocol: TCP
```

### Step 5: DNS Configuration

Configure wildcard DNS to point to all gateway nodes:

```
; Round-robin DNS for high availability
*.stage.kube-dc.com.    300    IN    A    168.119.17.49
*.stage.kube-dc.com.    300    IN    A    168.119.17.50
*.stage.kube-dc.com.    300    IN    A    168.119.17.51
```

## Automation for Tenant Clusters

### Controller Integration

The `kdc-cluster-controller` should automatically create:

1. **ClusterIP Service** with ext-cloud endpoint
2. **TLSRoute** (or Ingress) for the tenant API
3. **DNS record** (if using external-dns)

Example controller logic:

```go
func (r *KdcClusterReconciler) reconcileIngress(ctx context.Context, cluster *kdcv1.KdcCluster) error {
    // Get tenant API external IP from cloud network
    externalIP := cluster.Status.ControlPlaneEndpoint.Host
    
    // Create proxy service
    proxySvc := &corev1.Service{
        ObjectMeta: metav1.ObjectMeta{
            Name:      cluster.Name + "-api-proxy",
            Namespace: "default",
        },
        Spec: corev1.ServiceSpec{
            Type: corev1.ServiceTypeClusterIP,
            Ports: []corev1.ServicePort{{
                Name: "api",
                Port: 6443,
            }},
        },
    }
    
    // Create endpoints pointing to ext-cloud IP
    endpoints := &corev1.Endpoints{
        ObjectMeta: metav1.ObjectMeta{
            Name:      cluster.Name + "-api-proxy",
            Namespace: "default",
        },
        Subsets: []corev1.EndpointSubset{{
            Addresses: []corev1.EndpointAddress{{
                IP: externalIP,
            }},
            Ports: []corev1.EndpointPort{{
                Name: "api",
                Port: 6443,
            }},
        }},
    }
    
    // Create TLSRoute for SSL passthrough
    tlsRoute := &gatewayv1alpha2.TLSRoute{
        ObjectMeta: metav1.ObjectMeta{
            Name:      cluster.Name + "-api",
            Namespace: "default",
        },
        Spec: gatewayv1alpha2.TLSRouteSpec{
            Hostnames: []gatewayv1alpha2.Hostname{
                gatewayv1alpha2.Hostname(cluster.Name + ".stage.kube-dc.com"),
            },
            Rules: []gatewayv1alpha2.TLSRouteRule{{
                BackendRefs: []gatewayv1alpha2.BackendRef{{
                    BackendObjectReference: gatewayv1alpha2.BackendObjectReference{
                        Name: gatewayv1alpha2.ObjectName(cluster.Name + "-api-proxy"),
                        Port: ptr.To(gatewayv1alpha2.PortNumber(6443)),
                    },
                }},
            }},
        },
    }
    
    return r.createOrUpdate(ctx, proxySvc, endpoints, tlsRoute)
}
```

## Verification

### Test TLS Passthrough

```bash
# Test from external network
curl -k -v https://demo-cluster-cp.stage.kube-dc.com:443/healthz

# Verify SNI routing
openssl s_client -connect 168.119.17.49:443 \
  -servername demo-cluster-cp.stage.kube-dc.com </dev/null 2>/dev/null | \
  openssl x509 -noout -subject

# Test kubectl access
kubectl --kubeconfig=/path/to/tenant-kubeconfig get nodes
```

### Monitor Gateway Performance

```bash
# Check Higress controller logs
kubectl logs -n higress-system -l app=higress-controller -f

# Check gateway pods
kubectl get pods -n higress-system -l app=higress-gateway -o wide

# Verify hostNetwork binding
kubectl exec -n higress-system <gateway-pod> -- netstat -tlnp | grep 443
```

## Scaling Considerations

### Horizontal Scaling

| Tenants | Gateway Nodes | Recommended Setup |
|---------|---------------|-------------------|
| < 100 | 2 | Basic HA |
| 100-500 | 3 | Standard |
| 500-2000 | 5 | High capacity |
| 2000+ | 7+ | Enterprise scale |

### Resource Requirements per Gateway Node

| Scale | CPU | Memory | Network |
|-------|-----|--------|---------|
| < 500 routes | 2 cores | 2 GB | 1 Gbps |
| 500-2000 routes | 4 cores | 4 GB | 10 Gbps |
| 2000-10000 routes | 8 cores | 8 GB | 10 Gbps |
| 10000+ routes | 16 cores | 16 GB | 25 Gbps |

## Security Considerations

1. **Network Isolation**: Gateway nodes should only expose ports 80/443
2. **Rate Limiting**: Configure Higress rate limiting plugins per tenant
3. **WAF**: Enable Higress WAF plugin for DDoS protection
4. **mTLS**: Consider mTLS between gateway and backend services
5. **Certificate Management**: Use cert-manager with Let's Encrypt

## Migration from Nginx Ingress

### Phase 1: Parallel Deployment
1. Deploy Higress alongside existing nginx
2. Test with non-critical tenants
3. Validate TLS passthrough works

### Phase 2: Gradual Migration
1. Create Higress routes for new tenants
2. Migrate existing tenants incrementally
3. Monitor for issues

### Phase 3: Cutover
1. Update DNS to point to Higress nodes
2. Keep nginx running for rollback
3. After validation, decommission nginx

## Troubleshooting

### Route Not Working

```bash
# Check Higress controller config sync
kubectl logs -n higress-system -l app=higress-controller | grep -i error

# Verify route is loaded
kubectl exec -n higress-system <gateway-pod> -- \
  curl -s localhost:15000/config_dump | jq '.configs[] | select(.["@type"] | contains("RouteConfiguration"))'
```

### Connection Timeouts

```bash
# Verify gateway can reach ext-cloud network
kubectl exec -n higress-system <gateway-pod> -- \
  nc -zv 100.65.0.105 6443

# Check SNAT is working
kubectl exec -n higress-system <gateway-pod> -- \
  curl -v --connect-timeout 5 https://100.65.0.105:6443/healthz -k
```

### SSL Passthrough Not Working

```bash
# Verify TLS passthrough is enabled
kubectl get ingress -A -o yaml | grep -A5 "ssl-passthrough"

# Check listener configuration
kubectl exec -n higress-system <gateway-pod> -- \
  curl -s localhost:15000/listeners | jq '.[] | select(.name | contains("443"))'
```

## References

- [Higress GitHub](https://github.com/alibaba/higress)
- [Higress Documentation](https://higress.io/docs)
- [Sealos Migration Story](https://sealos.io/blog/sealos-envoy-vs-nginx-2000-tenants)
- [Gateway API TLSRoute](https://gateway-api.sigs.k8s.io/references/spec/#gateway.networking.k8s.io/v1alpha2.TLSRoute)
- [Envoy TLS Passthrough](https://www.envoyproxy.io/docs/envoy/latest/intro/arch_overview/security/ssl)
