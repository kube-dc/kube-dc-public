# Exposing Cloud Network Services via Public Ingress

## Problem Statement

Tenant Kubernetes clusters (Kamaji TenantControlPlanes) have their API servers exposed on the **ext-cloud** network (`100.65.0.0/16`), which is a private cloud network not directly accessible from the public internet.

Users need to access tenant cluster APIs from the public internet using hostnames like:
- `demo-cluster-cp.stage.kube-dc.com`
- `cluster-a.stage.kube-dc.com`

## Architecture Overview

```
┌─────────────────────────────────────────────────────────────────────────────┐
│                           Public Internet                                    │
│                                  │                                           │
│                                  ▼                                           │
│                    ┌─────────────────────────────┐                          │
│                    │    Nginx Ingress Controller  │                          │
│                    │      88.99.29.250:443       │                          │
│                    │      (ext-public VLAN)       │                          │
│                    └─────────────┬───────────────┘                          │
│                                  │ SSL Passthrough (SNI)                     │
│                                  ▼                                           │
│  ┌───────────────────────────────────────────────────────────────────────┐  │
│  │                        ovn-cluster VPC                                 │  │
│  │                                                                        │  │
│  │  ┌──────────────────┐      SNAT via       ┌──────────────────┐        │  │
│  │  │   ovn-default    │    100.65.0.101     │    ext-cloud     │        │  │
│  │  │  10.100.0.0/16   │ ─────────────────▶  │  100.65.0.0/16   │        │  │
│  │  │                  │                      │                  │        │  │
│  │  │  - ingress-nginx │                      │  - Tenant APIs   │        │  │
│  │  │  - kamaji-system │                      │  - etcd LBs      │        │  │
│  │  │  - default ns    │                      │                  │        │  │
│  │  └──────────────────┘                      └──────────────────┘        │  │
│  └───────────────────────────────────────────────────────────────────────┘  │
└─────────────────────────────────────────────────────────────────────────────┘
```

## Solution

### Prerequisites

1. **SNAT rule configured** on `ovn-cluster` VPC (see `cloud_network_enable_cluster.md`)
2. **SSL passthrough enabled** on nginx ingress controller: `--enable-ssl-passthrough`
3. **TCP services ConfigMap** configured: `--tcp-services-configmap=$(POD_NAMESPACE)/tcp-services`

### Nginx Ingress Controller Configuration

The following args must be added to the nginx ingress controller deployment:

```yaml
spec:
  template:
    spec:
      containers:
      - name: controller
        args:
        - /nginx-ingress-controller
        - --enable-ssl-passthrough           # Required for SNI-based routing
        - --tcp-services-configmap=$(POD_NAMESPACE)/tcp-services  # Optional: for TCP proxy
        # ... other args
```

### Two Exposure Methods

#### Method 1: SSL Passthrough (Recommended for K8s APIs)

**Use case**: Multi-tenant with hostname-based routing on port 443

**How it works**:
1. Nginx intercepts TLS traffic on port 443
2. Reads SNI (Server Name Indication) to determine hostname
3. Forwards entire TLS connection to backend without termination
4. Backend handles TLS (preserves original certificates)

**Configuration per tenant cluster**:

```yaml
# 1. ClusterIP Service (MUST NOT be headless)
apiVersion: v1
kind: Service
metadata:
  name: {cluster-name}-api-proxy
  namespace: default
  labels:
    kube-dc.com/tenant-cluster: "{cluster-name}"
spec:
  type: ClusterIP  # Required - ssl-passthrough sends traffic to ClusterIP
  ports:
  - name: api
    port: 6443
    protocol: TCP
---
# 2. Endpoints pointing to ext-cloud IP
apiVersion: v1
kind: Endpoints
metadata:
  name: {cluster-name}-api-proxy
  namespace: default
subsets:
- addresses:
  - ip: {ext-cloud-ip}  # e.g., 100.65.0.105
  ports:
  - name: api
    port: 6443
    protocol: TCP
---
# 3. Ingress with ssl-passthrough annotation
apiVersion: networking.k8s.io/v1
kind: Ingress
metadata:
  name: {cluster-name}-api
  namespace: default
  annotations:
    nginx.ingress.kubernetes.io/ssl-passthrough: "true"
spec:
  ingressClassName: nginx
  rules:
  - host: {cluster-name}.stage.kube-dc.com
    http:
      paths:
      - path: /
        pathType: Prefix
        backend:
          service:
            name: {cluster-name}-api-proxy
            port:
              number: 6443
```

**Critical requirement**: The service **must have a ClusterIP** (not headless). From nginx-ingress documentation:
> "traffic to Passthrough backends is sent to the clusterIP of the backing Service instead of individual Endpoints"

#### Method 2: TCP ConfigMap (Port-based)

**Use case**: Single service per port, no hostname routing

**Configuration**:

```yaml
# ConfigMap for TCP services
apiVersion: v1
kind: ConfigMap
metadata:
  name: tcp-services
  namespace: ingress-nginx
data:
  "6443": "default/demo-cluster-api-proxy:6443"
  # Add more ports as needed
  # "6444": "default/cluster-b-api-proxy:6443"
```

**Nginx Service** must expose the TCP port:

```yaml
spec:
  ports:
  - name: tcp-6443
    port: 6443
    targetPort: 6443
    protocol: TCP
```

**Limitation**: Each service requires a unique port. Not suitable for multi-tenant.

## Comparison

| Feature | SSL Passthrough | TCP ConfigMap |
|---------|-----------------|---------------|
| Routing | SNI/hostname-based | Port-based |
| Port | Shared 443 | Unique per service |
| TLS | Preserved (passthrough) | Preserved (passthrough) |
| Multi-tenant | ✅ Yes | ❌ No (port conflicts) |
| DNS required | Yes | No |
| Best for | K8s APIs, multi-tenant | Single service, testing |

## Verification

### Test SSL Passthrough

```bash
# Test via hostname (SSL passthrough)
curl -k https://demo-cluster-cp.stage.kube-dc.com/version

# Expected: Kubernetes API version response
{
  "major": "1",
  "minor": "34",
  "gitVersion": "v1.34.0",
  ...
}
```

### Test TCP Proxy

```bash
# Test via IP:port (TCP proxy)
curl -k https://88.99.29.250:6443/version

# Expected: Same Kubernetes API version response
```

### Verify Connectivity Path

```bash
# Test from nginx namespace to ext-cloud
kubectl run -n ingress-nginx test-conn --rm -it --restart=Never \
  --image=busybox -- nc -zv 100.65.0.105 6443

# Expected: 100.65.0.105 (100.65.0.105:6443) open
```

## Automation

The `kdc-cluster-controller` should automatically create these resources when a TenantControlPlane is provisioned:

1. **Service** in `default` namespace with ClusterIP
2. **Endpoints** pointing to the tenant API's ext-cloud IP
3. **Ingress** with ssl-passthrough annotation

### Implementation Notes

1. **Namespace**: Resources are created in `default` namespace (part of `ovn-default` subnet) to ensure SNAT connectivity to ext-cloud
2. **Naming convention**: `{tenant-namespace}-{cluster-name}-api-proxy`
3. **Cleanup**: Resources should be deleted when TenantControlPlane is deleted

## Troubleshooting

### SSL Passthrough not working

1. **Check ssl-passthrough is enabled**:
   ```bash
   kubectl get deployment -n ingress-nginx ingress-nginx-controller \
     -o jsonpath='{.spec.template.spec.containers[0].args}' | grep passthrough
   ```

2. **Check service has ClusterIP (not headless)**:
   ```bash
   kubectl get svc {service-name} -n default
   # ClusterIP should NOT be "None"
   ```

3. **Check endpoints exist**:
   ```bash
   kubectl get endpoints {service-name} -n default
   ```

4. **Check nginx config includes passthrough server**:
   ```bash
   kubectl exec -n ingress-nginx {nginx-pod} -- \
     grep -i "passthrough" /etc/nginx/nginx.conf
   ```

### Connection timeouts

1. **Verify SNAT rule exists**:
   ```bash
   kubectl ko nbctl lr-nat-list ovn-cluster
   # Should show: snat 10.100.0.0/16 -> 100.65.0.101
   ```

2. **Test connectivity from nginx pod**:
   ```bash
   kubectl run -n ingress-nginx test --rm -it --restart=Never \
     --image=busybox -- nc -zv {ext-cloud-ip} 6443
   ```

## Security Considerations

1. **TLS certificates**: Tenant API certificates are preserved (not terminated at nginx)
2. **Authentication**: Kubernetes RBAC and client certificates work as expected
3. **Network isolation**: Only `ovn-default` pods can reach ext-cloud via SNAT
4. **No credential exposure**: SSL passthrough means nginx never sees unencrypted traffic

## References

- [Nginx Ingress SSL Passthrough](https://kubernetes.github.io/ingress-nginx/user-guide/tls/#ssl-passthrough)
- [Cloud Network Enable Cluster](./cloud_network_enable_cluster.md)
- [Kamaji TenantControlPlane](https://kamaji.clastix.io/)
