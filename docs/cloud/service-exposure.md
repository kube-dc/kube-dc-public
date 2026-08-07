# Service Exposure Guide

This guide explains how to expose workloads in a Kube-DC Project. Choose the
method by protocol and reachability: use a Gateway route for hostname-based web
traffic, a LoadBalancer Service for selected TCP or UDP ports, and a Floating IP
for direct access to a VM. Both Project network types support these methods.

> **Managed Clusters**: every annotation below also works on
> `LoadBalancer` services **inside** a Managed Cluster — the cloud
> controller manager copies them to the platform cluster at Service creation
> time. See [Cluster Management](cluster-management.md#exposing-services-loadbalancer)
> for the Managed Cluster specifics (Issuer prerequisite, hostname pinning,
> public-IP quota).

## Quick Reference

| Need | Recommended method | Result |
|------|--------------------|--------|
| HTTP or HTTPS hostname | Gateway route (`expose-route`) | Shared gateway, DNS name, optional automatic TLS |
| Selected TCP or UDP ports | LoadBalancer Service + EIP | Address and ports dedicated to the Service |
| Direct VM access | Floating IP | One-to-one NAT to the VM |

> **Note**: Both network types support EIPs and LoadBalancers. The difference is where EIPs are allocated from.

## Understanding Project Network Types

Every installation supports the `cloud` type. The `public` Project type is
available only when the provider enables it; otherwise the dashboard hides it
and the API rejects it. When both are offered, choose an `egressNetworkType`:

```yaml
apiVersion: kube-dc.com/v1
kind: Project
metadata:
  name: production
  namespace: acme
spec:
  egressNetworkType: cloud  # or "public"
```

This creates Project `production` in Organization `acme`. Kubernetes stores that Project's workload resources in the backing namespace `acme-production`.

### Cloud Network (`egressNetworkType: cloud`)

- **Default EIPs** allocated from the cloud address pool
- Outbound traffic is SNATed through the Project gateway EIP
- **Can create public EIPs** by specifying `externalNetworkType: public` when the provider exposes that pool and quota is available
- **Gateway Routes** provide easy HTTPS exposure with auto-certificates
- Supports VMs, pods, and all workload types
- **Best for**: Web applications, APIs, microservices, and internal platform connectivity
- **Cost**: Provider- and plan-dependent

### Public Network (`egressNetworkType: public`)

- **Default EIPs** allocated from the configured public address pool
- Outbound traffic is SNATed through the Project gateway EIP
- The default gateway EIP is allocated from the public address pool
- Supports any TCP/UDP protocol
- Supports VMs, pods, and all workload types
- **Best for**: Game servers, custom protocols, direct IP requirements
- **Cost**: Provider- and plan-dependent; public address quota still applies

### Feature Comparison

| Feature | Cloud Project | Public Project |
|---------|---------------|----------------|
| **Default EIP source** | Configured cloud address pool | Configured public address pool |
| **Can get public EIPs** | When the provider exposes the pool and quota is available | Yes by default, subject to quota |
| **Can use Gateway Routes** | ✅ Yes | ✅ Yes |
| **Can use EIP + LB** | ✅ Yes | ✅ Yes |
| **Can run VMs** | ✅ Yes | ✅ Yes |
| **Can run Pods** | ✅ Yes | ✅ Yes |

---

## Part 1: Gateway Routes

Use Gateway routes for hostname-based HTTP, HTTPS, or TLS passthrough in either
Project network type.

### All Service Annotations Reference

#### Gateway Route Annotations

| Annotation | Description | Example Values |
|------------|-------------|----------------|
| `expose-route` | Enable Gateway route | `http`, `https`, `tls-passthrough` |
| `route-hostname` | Custom hostname (optional) | `api.example.com` |
| `route-port` | Target port (optional) | `8080`, `50051` |
| `tls-issuer` | cert-manager Issuer name | `letsencrypt` (default) |
| `tls-secret` | User-provided TLS secret | `my-tls-secret` |

#### EIP/LoadBalancer Annotations

| Annotation | Description | Example Values |
|------------|-------------|----------------|
| `bind-on-default-gw-eip` | Use project's default EIP | `"true"` |
| `bind-on-eip` | Use a specific EIP by name | `my-eip` |
| `autodelete` | Delete the Service if it remains without endpoints; advanced recovery behavior, not EIP lifecycle | `"true"` |
| `create-gateway-backend` | Create Envoy Gateway backend | `"true"` |

> **Note**: Prefix is `service.nlb.kube-dc.com/`

:::warning
`autodelete` does not manage EIP cleanup. It can delete the Service when the
Service remains without endpoints. Leave it unset for normal workload
lifecycle.
:::

#### Network Type Annotation

| Annotation | Description | Example Values |
|------------|-------------|----------------|
| `network.kube-dc.com/external-network-type` | EIP type for auto-created EIP (set at creation, immutable) | `cloud`, `public` |

> **Tip**: Use this on a LoadBalancer service to get a public EIP in a cloud project:
> ```yaml
> annotations:
>   network.kube-dc.com/external-network-type: "public"
> ```

> **Set this when you create the Service — it cannot be changed afterwards.**
> The annotation chooses which external network the Service's address is
> allocated from, and an external IP keeps the type it was allocated with for
> life. Editing the annotation on a Service that already has an address is
> rejected, so you get a clear error instead of a change that appears to work
> and does nothing.
>
> To move a workload to a different external network, create a second Service
> with the annotation you want, cut traffic over to its address, then delete the
> old one. In that order the workload is never without a reachable address —
> deleting first would release the old IP before the new one is serving.

#### Status Annotations (Read-Only)

| Annotation | Description |
|------------|-------------|
| `route-hostname-status` | Assigned hostname (set by controller) |

> **Note**: All annotations use prefix `service.nlb.kube-dc.com/`

### Gateway Route Annotations (Details)

Add these annotations to your `LoadBalancer` Service.

#### Multi-Port Services

When using `expose-route`, the gateway routes traffic to a **single port** on
your Service. By default this is the first port in `spec.ports`. For every
multi-port Service, set `route-port` explicitly so a manifest reorder cannot
silently change the routed backend.

If your application listens on a non-standard port, use the `route-port` annotation to specify which port the gateway should target:

```yaml
annotations:
  service.nlb.kube-dc.com/expose-route: "https"
  service.nlb.kube-dc.com/route-port: "8080"
```

> **Note**: This applies to all route types (`http`, `https`, `tls-passthrough`). The gateway terminates TLS (for `https`) or passes it through (for `tls-passthrough`), then forwards traffic to the selected port on your service.

#### Route Type Comparison

| Route Type | Port | TLS | App Serves | Use Case |
|------------|------|-----|------------|----------|
| `http` | 80 | None | HTTP | Plain HTTP traffic |
| `https` | 443 | Gateway terminates | HTTP | Recommended for web traffic; automatic TLS |
| `tls-passthrough` | 443 | App terminates | HTTPS | End-to-end encryption |

### Example: HTTPS Web Application (Recommended)

The simplest way to expose a web app with automatic TLS:

#### Step 1: Create the Issuer (once per Project)

```yaml
apiVersion: cert-manager.io/v1
kind: Issuer
metadata:
  name: letsencrypt
  namespace: acme-production
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

#### Step 2: Deploy your application

```yaml
apiVersion: apps/v1
kind: Deployment
metadata:
  name: my-app
  namespace: acme-production
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

#### Step 3: Create LoadBalancer Service with HTTPS route

```yaml
apiVersion: v1
kind: Service
metadata:
  name: my-app
  namespace: acme-production
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

#### Step 4: Verify and access

```bash
# Check assigned hostname
kubectl get svc my-app -n acme-production -o jsonpath='{.metadata.annotations.service\.nlb\.kube-dc\.com/route-hostname-status}'
# Output: my-app-acme-production.kube-dc.cloud

# Check certificate status
kubectl get certificate -n acme-production
kubectl get challenge -n acme-production

# Test access
curl https://my-app-acme-production.kube-dc.cloud
```

For HTTPS routes, hostname status is set after the certificate and route are ready. This can take a few minutes. If the command returns an empty value, check the certificate and ACME challenge status first.

### Example: Plain HTTP

For non-TLS HTTP traffic:

```yaml
apiVersion: v1
kind: Service
metadata:
  name: my-app
  namespace: acme-production
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

Access via: `http://my-app-acme-production.kube-dc.cloud`

### Example: TLS Passthrough (Kubernetes API)

For services that handle their own TLS (like Kubernetes control planes):

```yaml
apiVersion: v1
kind: Service
metadata:
  name: cluster-api
  namespace: acme-production
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

The Gateway listens publicly on port 443 and forwards to Service port 6443:
`https://cluster-api-acme-production.kube-dc.cloud`.

:::caution Configure backend TLS first
TLS passthrough does not issue a certificate or terminate TLS. The `TLSRoute`
selects a backend from the SNI hostname in the client's initial TLS handshake.
The client must therefore connect by the published hostname, and the backend
must present a certificate whose SANs include that hostname. For this example,
add `cluster-api-acme-production.kube-dc.cloud` to the API server certificate
before exposing it. Use a dedicated LoadBalancer address when the protocol does
not start with a TLS handshake or the backend certificate cannot cover the
Gateway hostname.
:::

### Example: Custom Hostname

Override the auto-generated hostname:

```yaml
apiVersion: v1
kind: Service
metadata:
  name: my-app
  namespace: acme-production
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

### Example: User-Provided Certificate

Use your own TLS certificate instead of auto-provisioning:

```bash
# First, create your TLS secret
kubectl create secret tls my-tls-secret \
  --cert=path/to/tls.crt \
  --key=path/to/tls.key \
  -n acme-production
```

```yaml
apiVersion: v1
kind: Service
metadata:
  name: my-app
  namespace: acme-production
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

The certificate in `my-tls-secret` must include `secure.mycompany.com` in its
SANs and chain to a CA trusted by your clients.

### gRPC

The `expose-route` annotation currently creates an `HTTPRoute`, even when a Service port sets `appProtocol: kubernetes.io/h2c`. It does not create a `GRPCRoute` or configure an HTTP/2 backend. For gRPC today, use a dedicated LoadBalancer Service or work with your platform operator to provide explicit Gateway API `GRPCRoute` and backend protocol resources.

---

## Part 2: EIP-Based Exposure (Both Project Types)

Both cloud and public projects can use EIPs and LoadBalancer services.

### Default EIP Allocation

| Project Type | Default EIP Source | Can Request |
|--------------|-------------------|-------------|
| Cloud | Configured cloud address pool | `cloud`; `public` when the provider exposes that pool and quota is available |
| Public | Configured public address pool | `public`; other types depend on provider configuration and quota |

> **When to use EIPs vs Gateway Routes:**
> - Use **Gateway Routes** for HTTP/HTTPS/TLS passthrough (automatic TLS for HTTPS)
> - Use **EIPs** for gRPC and other TCP/UDP protocols, VMs, or when you need a dedicated IP

## Understanding EIPs

External IPs (EIPs) provide addresses for your Project from a provider-configured external network.

### Default Gateway EIP

Every Project automatically gets a default EIP (`default-gw`) for outbound
SNAT. A LoadBalancer Service uses it only when you set
`bind-on-default-gw-eip: "true"`; otherwise the platform can allocate a
Service-specific EIP.

### Creating Additional EIPs

For services that need dedicated IPs:

```yaml
apiVersion: kube-dc.com/v1
kind: EIp
metadata:
  name: web-server-eip
  namespace: acme-production
spec:
  externalNetworkType: public  # or "cloud"
```

**EIP Types:**

| `externalNetworkType` | Description | Use Case |
|-----------------------|-------------|----------|
| `cloud` | Address from the cloud network | Internal platform connectivity and outbound SNAT |
| `public` | Dedicated public IP | Direct access, static IP, VMs |

> **Tip**: When your provider offers public addresses to cloud Projects, request a public EIP for workloads that need a dedicated internet-routable IP.

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
  namespace: acme-production
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
  namespace: acme-production
spec:
  externalNetworkType: public
---
# Step 2: Bind service to the EIP
apiVersion: v1
kind: Service
metadata:
  name: api-lb
  namespace: acme-production
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
  namespace: acme-production
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

Floating IPs map an internal IP directly to an EIP, providing 1:1 NAT. For detailed FIP management, see [External & Floating IPs](public-floating-ips.md).

### When to Use FIPs

- Direct IP mapping for VMs
- Whole-VM exposure across many ports
- Protocols that are awkward to model as individual Service ports

### Creating a FIP for a VM

Use `vmTarget` to point a FIP at a VM. The controller reads the named interface address from the running VirtualMachineInstance status, so the VM must be running and that interface must report an IP:

```yaml
apiVersion: kube-dc.com/v1
kind: FIp
metadata:
  name: vm-fip
  namespace: acme-production
spec:
  externalNetworkType: public
  vmTarget:
    vmName: ubuntu
    interfaceName: vpc_net_0
```

### Important Limitation: FIP and LoadBalancer Conflicts

**A pod/VM cannot simultaneously serve as:**
1. A target for a **public FIP**
2. A backend for a **cloud-network LoadBalancer** service

This is because public FIPs create source-based policy routes that redirect ALL outbound traffic from that IP to the public gateway, breaking cloud-network services.

**Example conflict:**
```
Pod IP: 10.0.0.30
├── Public FIP → Routes all traffic to public gateway (198.51.100.1)
└── Cloud LoadBalancer → Expects traffic via cloud gateway (100.65.0.1) ❌ BROKEN
```

**Workarounds:**
- Use separate pods for FIP targets and cloud-service backends
- Use the same network type for both (all public or all cloud)
- Choose one exposure method per pod

---

## Part 3: Choosing the Right Approach

### Decision Tree

```
┌─────────────────────────────────────────────────────────────┐
│                  What are you exposing?                     │
└─────────────────────────────────────────────────────────────┘
                            │
            ┌───────────────┼───────────────┐
            ▼               ▼               ▼
      Web App/API      VM Direct       Custom Protocol
            │           Access              │
            │               │               │
            ▼               ▼               ▼
   ┌─────────────┐   ┌───────────┐   ┌───────────────┐
   │ Any Project │   │Any Project│   │  Any Project  │
   │expose-route │   │  EIP+FIP  │   │   EIP + LB    │
   │   : https   │   │(public IP)│   │  (any proto)  │
   └─────────────┘   └───────────┘   └───────────────┘
```

### Comparison Table

| Feature | Gateway route (any Project) | EIP + LoadBalancer (any Project) |
|---------|------------------------|----------------------------|
| **IP Address** | Shared Gateway IP | Dedicated per EIP |
| **Protocols** | HTTP, HTTPS, TLS passthrough | Any TCP/UDP |
| **TLS Termination** | Gateway (auto-cert) | Application |
| **Cost** | Provider- and plan-dependent | Provider- and plan-dependent |
| **Setup** | Simple annotation | EIP + Service config |
| **DNS** | Auto hostname | Manual |
| **Best For** | Web apps, APIs | VMs, game servers |

---

## Part 4: Advanced Topics

### Envoy Gateway Backend

Use the `create-gateway-backend` annotation on a **LoadBalancer** Service to
register an Envoy Gateway Backend for advanced routing scenarios. Prefer
`expose-route` unless you are also managing the route yourself.

```yaml
apiVersion: v1
kind: Service
metadata:
  name: my-backend
  namespace: acme-production
  annotations:
    service.nlb.kube-dc.com/create-gateway-backend: "true"
spec:
  type: LoadBalancer
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


### Namespace-Scoped Ingress Controller

For advanced HTTP routing beyond Gateway capabilities, deploy a dedicated ingress-nginx:

This chart creates namespace-scoped Roles and RoleBindings, so installation
requires the Project `admin` role. A `developer` can operate supported workload
resources but cannot install this RBAC. Render and validate the chart against
the server before installing it.
```yaml
# ingress-values.yaml
controller:
  ingressClassResource:
    enabled: false
  scope:
    enabled: true
    namespace: acme-production
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
  --namespace acme-production \
  --values ingress-values.yaml
```

---

## Troubleshooting

### Gateway Routes

```bash
# Check route hostname was assigned
kubectl get svc my-app -o yaml | grep route-hostname-status

# Check certificate status
kubectl get certificate -n acme-production
kubectl describe certificate my-app-tls -n acme-production

# Check HTTPRoute created
kubectl get httproute -n acme-production

```

The platform Gateway and controller run outside your Project permissions. If
the Project resources above are healthy but the route still fails, provide the
Service name and Project name to support.

### EIP and LoadBalancer

```bash
# Check EIP status
kubectl get eip -n acme-production
kubectl describe eip my-eip -n acme-production

# Check LoadBalancer external IP
kubectl get svc -n acme-production

# Check service events
kubectl describe svc my-lb -n acme-production
```

### Common Issues

| Issue | Cause | Solution |
|-------|-------|----------|
| No hostname assigned | Missing `expose-route` annotation | Add annotation |
| Hostname status empty for HTTPS | Certificate is still pending | Check `kubectl get certificate,challenge -n acme-production` |
| Certificate not ready | Issuer not created, ACME challenge pending, or quota prevents solver pod creation | Create Issuer first and make sure the project has free CPU/memory for cert-manager HTTP-01 solver pods |
| 503 error | Backend not ready | Check pod status |
| EIP pending | No available IPs | Check subnet capacity |
| Connection timeout | DNS not configured | Point DNS to Gateway/EIP |
| Cloud LB stopped working after FIP created | FIP policy route conflict | Use separate pods or delete FIP (see [limitation](#important-limitation-fip-and-loadbalancer-conflicts)) |

---

## Summary

| Need | Resource or annotation | Result |
|------|------------------------|--------|
| Automatic HTTPS | LoadBalancer + `expose-route: https` | Gateway terminates TLS and assigns a hostname |
| Plain HTTP | LoadBalancer + `expose-route: http` | Gateway serves HTTP |
| End-to-end TLS | LoadBalancer + `expose-route: tls-passthrough` | Application terminates TLS; public listener is 443 |
| Reuse Project gateway EIP | LoadBalancer + `bind-on-default-gw-eip: "true"` | Selected ports share the gateway address |
| Use a selected EIP | LoadBalancer + `bind-on-eip: "name"` | Selected ports use that address |
| Direct VM mapping | `FIp` | One-to-one NAT to the VM |
