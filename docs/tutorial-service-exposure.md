# Service Exposure Guide

This guide explains how to expose services in Kube-DC projects. The method you use depends on your **project's network type** and your requirements.

## Quick Reference

| Network Type | Default EIP Source | Best For | Recommended Method |
|--------------|-------------------|----------|-------------------|
| **Cloud** | `ext-cloud` subnet | Web apps, APIs | Gateway Routes (`expose-route`) |
| **Public** | `ext-public` subnet | VMs, custom protocols | EIP + LoadBalancer |

> **Note**: Both network types support EIPs and LoadBalancers. The difference is where EIPs are allocated from.

## Understanding Project Network Types

When creating a project, you choose an `egressNetworkType`:

```yaml
apiVersion: kube-dc.com/v1
kind: Project
metadata:
  name: my-project
  namespace: my-org
spec:
  egressNetworkType: cloud  # or "public"
```

### Cloud Network (`egressNetworkType: cloud`)

- **Default EIPs** allocated from `ext-cloud` subnet (shared/NAT IPs)
- Outbound traffic goes through a **shared NAT gateway**
- **Can create public EIPs** by specifying `externalNetworkType: public`
- **Gateway Routes** provide easy HTTPS exposure with auto-certificates
- Supports VMs, pods, and all workload types
- **Best for**: Web applications, APIs, microservices, cost-optimized workloads
- **Cost**: Lower (shared infrastructure, cloud IPs often included)

### Public Network (`egressNetworkType: public`)

- **Default EIPs** allocated from `ext-public` subnet (dedicated public IPs)
- Direct internet connectivity without NAT
- Each EIP is a dedicated public IP address
- Supports any TCP/UDP protocol
- Supports VMs, pods, and all workload types
- **Best for**: Game servers, custom protocols, direct IP requirements
- **Cost**: Higher (dedicated public IPs)

### Feature Comparison

| Feature | Cloud Project | Public Project |
|---------|---------------|----------------|
| **Default EIP source** | `ext-cloud` | `ext-public` |
| **Can get public EIPs** | ✅ Yes (specify `externalNetworkType: public`) | ✅ Yes (default) |
| **Can use Gateway Routes** | ✅ Yes | ✅ Yes |
| **Can use EIP + LB** | ✅ Yes | ✅ Yes |
| **Can run VMs** | ✅ Yes | ✅ Yes |
| **Can run Pods** | ✅ Yes | ✅ Yes |

---

# Part 1: Cloud Network Projects

For projects with `egressNetworkType: cloud`, use Gateway Routes to expose services.

## All Service Annotations Reference

### Gateway Route Annotations

| Annotation | Description | Example Values |
|------------|-------------|----------------|
| `expose-route` | Enable Gateway route | `http`, `https`, `tls-passthrough` |
| `route-hostname` | Custom hostname (optional) | `api.example.com` |
| `route-port` | Target port (optional) | `8080`, `50051` |
| `tls-issuer` | cert-manager Issuer name | `letsencrypt` (default) |
| `tls-secret` | User-provided TLS secret | `my-tls-secret` |

### EIP/LoadBalancer Annotations

| Annotation | Description | Example Values |
|------------|-------------|----------------|
| `bind-on-default-gw-eip` | Use project's default EIP | `"true"` |
| `bind-on-eip` | Use a specific EIP by name | `my-eip` |
| `autodelete` | Auto-delete EIP when service deleted | `"true"` |
| `create-gateway-backend` | Create Envoy Gateway backend | `"true"` |

> **Note**: Prefix is `service.nlb.kube-dc.com/`

### Network Type Annotation

| Annotation | Description | Example Values |
|------------|-------------|----------------|
| `network.kube-dc.com/external-network-type` | EIP type for auto-created EIP | `cloud`, `public` |

> **Tip**: Use this on a LoadBalancer service to get a public EIP in a cloud project:
> ```yaml
> annotations:
>   network.kube-dc.com/external-network-type: "public"
> ```

### Status Annotations (Read-Only)

| Annotation | Description |
|------------|-------------|
| `route-hostname-status` | Assigned hostname (set by controller) |

> **Note**: All annotations use prefix `service.nlb.kube-dc.com/`

## Gateway Route Annotations (Details)

Add these annotations to your `LoadBalancer` Service:

### Route Type Comparison

| Route Type | Port | TLS | App Serves | Use Case |
|------------|------|-----|------------|----------|
| `http` | 80 | None | HTTP | Plain HTTP traffic |
| `https` | 443 | Gateway terminates | HTTP | ⭐ **Recommended** - Auto TLS certs |
| `tls-passthrough` | 443 | App terminates | HTTPS | End-to-end encryption |

## Example: HTTPS Web Application (Recommended)

The simplest way to expose a web app with automatic TLS:

### Step 1: Create the Issuer (once per namespace)

```yaml
apiVersion: cert-manager.io/v1
kind: Issuer
metadata:
  name: letsencrypt
  namespace: my-project
spec:
  acme:
    server: https://acme-v02.api.letsencrypt.org/directory
    email: your-email@example.com  # Replace with valid email
    privateKeySecretRef:
      name: letsencrypt-account-key
    solvers:
    - http01:
        gatewayHTTPRoute:
          parentRefs:
          - group: gateway.networking.k8s.io
            kind: Gateway
            name: eg
            namespace: envoy-gateway-system
```

### Step 2: Deploy your application

```yaml
apiVersion: apps/v1
kind: Deployment
metadata:
  name: my-app
  namespace: my-project
spec:
  replicas: 2
  selector:
    matchLabels:
      app: my-app
  template:
    metadata:
      labels:
        app: my-app
    spec:
      containers:
      - name: app
        image: nginx:alpine
        ports:
        - containerPort: 80
```

### Step 3: Create LoadBalancer Service with HTTPS route

```yaml
apiVersion: v1
kind: Service
metadata:
  name: my-app
  namespace: my-project
  annotations:
    # Expose via HTTPS with auto-provisioned certificate
    service.nlb.kube-dc.com/expose-route: "https"
spec:
  type: LoadBalancer
  selector:
    app: my-app
  ports:
  - port: 80
    targetPort: 80
```

### Step 4: Verify and access

```bash
# Check assigned hostname
kubectl get svc my-app -n my-project -o jsonpath='{.metadata.annotations.service\.nlb\.kube-dc\.com/route-hostname-status}'
# Output: my-app-my-project.stage.kube-dc.com

# Check certificate status
kubectl get certificate -n my-project

# Test access
curl https://my-app-my-project.stage.kube-dc.com
```

## Example: Plain HTTP

For non-TLS HTTP traffic:

```yaml
apiVersion: v1
kind: Service
metadata:
  name: my-app
  namespace: my-project
  annotations:
    service.nlb.kube-dc.com/expose-route: "http"
spec:
  type: LoadBalancer
  selector:
    app: my-app
  ports:
  - port: 80
    targetPort: 80
```

Access via: `http://my-app-my-project.stage.kube-dc.com`

## Example: TLS Passthrough (Kubernetes API)

For services that handle their own TLS (like Kubernetes control planes):

```yaml
apiVersion: v1
kind: Service
metadata:
  name: cluster-api
  namespace: my-project
  annotations:
    service.nlb.kube-dc.com/expose-route: "tls-passthrough"
spec:
  type: LoadBalancer
  selector:
    app: kube-apiserver
  ports:
  - port: 6443
    targetPort: 6443
```

Access via: `https://cluster-api-my-project.stage.kube-dc.com:6443`

## Example: Custom Hostname

Override the auto-generated hostname:

```yaml
apiVersion: v1
kind: Service
metadata:
  name: my-app
  namespace: my-project
  annotations:
    service.nlb.kube-dc.com/expose-route: "https"
    service.nlb.kube-dc.com/route-hostname: "api.mycompany.com"
spec:
  type: LoadBalancer
  selector:
    app: my-app
  ports:
  - port: 80
    targetPort: 80
```

**Note**: You must configure DNS to point `api.mycompany.com` to the Gateway IP.

## Example: User-Provided Certificate

Use your own TLS certificate instead of auto-provisioning:

```yaml
# First, create your TLS secret
kubectl create secret tls my-tls-secret \
  --cert=path/to/tls.crt \
  --key=path/to/tls.key \
  -n my-project
---
apiVersion: v1
kind: Service
metadata:
  name: my-app
  namespace: my-project
  annotations:
    service.nlb.kube-dc.com/expose-route: "https"
    service.nlb.kube-dc.com/tls-secret: "my-tls-secret"
    service.nlb.kube-dc.com/route-hostname: "secure.mycompany.com"
spec:
  type: LoadBalancer
  selector:
    app: my-app
  ports:
  - port: 80
    targetPort: 80
```

## Example: gRPC Service

gRPC services work with HTTPS routes (HTTP/2):

```yaml
apiVersion: v1
kind: Service
metadata:
  name: grpc-api
  namespace: my-project
  annotations:
    service.nlb.kube-dc.com/expose-route: "https"
    service.nlb.kube-dc.com/route-port: "50051"
spec:
  type: LoadBalancer
  selector:
    app: grpc-server
  ports:
  - name: grpc
    port: 50051
    targetPort: 50051
```

---

# Part 2: EIP-Based Exposure (Both Project Types)

Both cloud and public projects can use EIPs and LoadBalancer services.

### Default EIP Allocation

| Project Type | Default EIP Source | Can Request |
|--------------|-------------------|-------------|
| Cloud | `ext-cloud` subnet | Both `cloud` and `public` EIPs |
| Public | `ext-public` subnet | Both `cloud` and `public` EIPs |

> **When to use EIPs vs Gateway Routes:**
> - Use **Gateway Routes** for HTTP/HTTPS/gRPC (simpler, auto-TLS)
> - Use **EIPs** for TCP/UDP protocols, VMs, or when you need a dedicated IP

## Understanding EIPs

External IPs (EIPs) provide IP addresses for your project from the configured external network.

### Default Gateway EIP

Every project automatically gets a default EIP (`default-gw`) that acts as:
- NAT gateway for outbound traffic
- Default endpoint for LoadBalancer services

### Creating Additional EIPs

For services that need dedicated IPs:

```yaml
apiVersion: kube-dc.com/v1
kind: EIp
metadata:
  name: web-server-eip
  namespace: my-project
spec:
  externalNetworkType: public  # or "cloud"
```

**EIP Types:**

| `externalNetworkType` | Description | Use Case |
|-----------------------|-------------|----------|
| `cloud` | Shared/NAT pool IP | Cost-effective, outbound NAT |
| `public` | Dedicated public IP | Direct access, static IP, VMs |

> **Tip**: Cloud projects can request public EIPs for services that need dedicated IPs (e.g., game servers, VMs with direct access).

## LoadBalancer Service Annotations

| Annotation | Description |
|------------|-------------|
| `service.nlb.kube-dc.com/bind-on-default-gw-eip: "true"` | Use project's default EIP |
| `service.nlb.kube-dc.com/bind-on-eip: "eip-name"` | Use a specific EIP |

## Example: Web Server on Default EIP

```yaml
apiVersion: v1
kind: Service
metadata:
  name: nginx-lb
  namespace: my-project
  annotations:
    service.nlb.kube-dc.com/bind-on-default-gw-eip: "true"
spec:
  type: LoadBalancer
  selector:
    app: nginx
  ports:
  - name: http
    port: 80
    targetPort: 80
  - name: https
    port: 443
    targetPort: 443
```

## Example: Service on Dedicated EIP

```yaml
# Step 1: Create dedicated EIP
apiVersion: kube-dc.com/v1
kind: EIp
metadata:
  name: api-eip
  namespace: my-project
spec:
  externalNetworkType: public
---
# Step 2: Bind service to the EIP
apiVersion: v1
kind: Service
metadata:
  name: api-lb
  namespace: my-project
  annotations:
    service.nlb.kube-dc.com/bind-on-eip: "api-eip"
spec:
  type: LoadBalancer
  selector:
    app: api-server
  ports:
  - port: 443
    targetPort: 443
```

## Example: VM SSH Access

Expose SSH access to a virtual machine:

```yaml
apiVersion: v1
kind: Service
metadata:
  name: vm-ssh
  namespace: my-project
  annotations:
    service.nlb.kube-dc.com/bind-on-default-gw-eip: "true"
spec:
  type: LoadBalancer
  selector:
    vm.kubevirt.io/name: my-vm  # Target VM name
  ports:
  - name: ssh
    port: 2222      # External port
    targetPort: 22  # Internal SSH port
```

## Floating IPs (FIPs)

Floating IPs map an internal IP directly to an EIP, providing 1:1 NAT.

### When to Use FIPs

- Direct IP mapping for VMs
- Services that need to see their public IP
- Protocols that don't work behind NAT

### Creating a FIP

```yaml
apiVersion: kube-dc.com/v1
kind: FIp
metadata:
  name: vm-fip
  namespace: my-project
spec:
  ipAddress: 10.0.10.5    # Internal IP of VM or pod
  eip: my-eip             # Name of existing EIP
```

### ⚠️ Important Limitation: FIP and LoadBalancer Conflicts

**A pod/VM cannot simultaneously serve as:**
1. A target for a **public FIP**
2. A backend for a **cloud-network LoadBalancer** service

This is because public FIPs create source-based policy routes that redirect ALL outbound traffic from that IP to the public gateway, breaking cloud-network services.

**Example conflict:**
```
Pod IP: 10.0.0.30
├── Public FIP → Routes all traffic to public gateway (91.224.11.1)
└── Cloud LoadBalancer → Expects traffic via cloud gateway (100.65.0.1) ❌ BROKEN
```

**Workarounds:**
- Use separate pods for FIP targets and cloud-service backends
- Use the same network type for both (all public or all cloud)
- Choose one exposure method per pod

---

# Part 3: Choosing the Right Approach

## Decision Tree

```
┌─────────────────────────────────────────────────────────────┐
│                  What are you exposing?                      │
└─────────────────────────────────────────────────────────────┘
                            │
            ┌───────────────┼───────────────┐
            ▼               ▼               ▼
      Web App/API      VM Direct       Custom Protocol
            │           Access              │
            │               │               │
            ▼               ▼               ▼
   ┌─────────────┐   ┌───────────┐   ┌───────────────┐
   │Cloud Project│   │  Public   │   │    Public     │
   │expose-route │   │  Project  │   │    Project    │
   │   : https   │   │  EIP+FIP  │   │   EIP + LB    │
   └─────────────┘   └───────────┘   └───────────────┘
```

## Comparison Table

| Feature | Gateway Routes (Cloud) | EIP + LoadBalancer (Public) |
|---------|------------------------|----------------------------|
| **IP Address** | Shared Gateway IP | Dedicated per EIP |
| **Protocols** | HTTP, HTTPS, gRPC | Any TCP/UDP |
| **TLS Termination** | Gateway (auto-cert) | Application |
| **Cost** | Lower | Higher |
| **Setup** | Simple annotation | EIP + Service config |
| **DNS** | Auto hostname | Manual |
| **Best For** | Web apps, APIs | VMs, game servers |

---

# Part 4: Advanced Topics

## Envoy Gateway Backend

Use the `create-gateway-backend` annotation to register a service as an Envoy Gateway Backend for advanced routing scenarios:

```yaml
apiVersion: v1
kind: Service
metadata:
  name: my-backend
  namespace: my-project
  annotations:
    service.nlb.kube-dc.com/create-gateway-backend: "true"
spec:
  type: ClusterIP
  selector:
    app: my-app
  ports:
  - port: 8080
    targetPort: 8080
```

This creates an Envoy Gateway `Backend` resource, enabling:
- Cross-namespace routing from Gateway
- Custom backend policies
- Advanced load balancing configurations

## Automatic External Endpoints

For every LoadBalancer service, Kube-DC creates external endpoints for cross-VPC DNS access:

- **External Service**: `<service-name>-ext`
- **DNS**: `<service>-ext.<namespace>.svc.cluster.local`

```bash
# Verify external endpoints
kubectl get svc,endpoints -n my-project my-app-ext
```

## Namespace-Scoped Ingress Controller

For advanced HTTP routing beyond Gateway capabilities, deploy a dedicated ingress-nginx:

```yaml
# ingress-values.yaml
controller:
  ingressClassResource:
    enabled: false
  scope:
    enabled: true
    namespace: my-project
  admissionWebhooks:
    enabled: false
  service:
    annotations:
      service.nlb.kube-dc.com/bind-on-default-gw-eip: "true"
rbac:
  create: true
  scope: true
defaultBackend:
  enabled: false
```

```bash
helm install ingress ingress-nginx/ingress-nginx \
  --namespace my-project \
  --values ingress-values.yaml
```

---

# Troubleshooting

## Gateway Routes (Cloud Projects)

```bash
# Check route hostname was assigned
kubectl get svc my-app -o yaml | grep route-hostname-status

# Check certificate status
kubectl get certificate -n my-project
kubectl describe certificate my-app-tls -n my-project

# Check HTTPRoute created
kubectl get httproute -n my-project

# Check Gateway listener
kubectl get gateway eg -n envoy-gateway-system -o yaml | grep -A5 "https-my-app"

# Controller logs
kubectl logs -n kube-dc deployment/kube-dc-manager | grep my-app
```

## EIP/LoadBalancer (Public Projects)

```bash
# Check EIP status
kubectl get eip -n my-project
kubectl describe eip my-eip -n my-project

# Check LoadBalancer external IP
kubectl get svc -n my-project

# Check service events
kubectl describe svc my-lb -n my-project
```

## Common Issues

| Issue | Cause | Solution |
|-------|-------|----------|
| No hostname assigned | Missing `expose-route` annotation | Add annotation |
| Certificate not ready | Issuer not created | Create Issuer first |
| 503 error | Backend not ready | Check pod status |
| EIP pending | No available IPs | Check subnet capacity |
| Connection timeout | DNS not configured | Point DNS to Gateway/EIP |
| Cloud LB stopped working after FIP created | FIP policy route conflict | Use separate pods or delete FIP (see [limitation](#️-important-limitation-fip-and-loadbalancer-conflicts)) |

---

# Summary

| Project Type | Service Type | Annotation | Result |
|--------------|--------------|------------|--------|
| Cloud | LoadBalancer | `expose-route: https` | Auto HTTPS with cert |
| Cloud | LoadBalancer | `expose-route: http` | HTTP only |
| Cloud | LoadBalancer | `expose-route: tls-passthrough` | App handles TLS |
| Public | LoadBalancer | `bind-on-default-gw-eip: "true"` | Use default EIP |
| Public | LoadBalancer | `bind-on-eip: "name"` | Use specific EIP |
| Public | FIp | N/A | 1:1 NAT mapping |
