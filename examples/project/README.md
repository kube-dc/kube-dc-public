# Project Service Exposure Examples

This directory contains examples for exposing services within a Kube-DC project using cloud networking.

## Overview

Projects with `egressNetworkType: cloud` use private IP addresses from the `ext-cloud` subnet. To expose services externally, you can use **automatic route creation** via service annotations:

- **HTTP** - Plain HTTP traffic on port 80 (`expose-route: http`)
- **HTTPS** - Gateway-terminated TLS on port 443 (`expose-route: https`) ⭐ **Recommended**
- **TLS Passthrough** - End-to-end TLS on port 443 (`expose-route: tls-passthrough`)

## Automatic Route Creation

Simply add an annotation to your LoadBalancer Service:

```yaml
apiVersion: v1
kind: Service
metadata:
  name: my-app
  annotations:
    # Automatically creates: EIP, Backend, Certificate, Gateway Listener, HTTPRoute
    service.nlb.kube-dc.com/expose-route: "https"  # or "http" or "tls-passthrough"
spec:
  type: LoadBalancer
  ...
```

The controller will automatically create:
- **EIP** - External IP allocation
- **Backend** - For Gateway routing
- **HTTPRoute/TLSRoute** - With auto-generated hostname

### Auto-Generated Hostname

Format: `{service}-{namespace}.{base_domain}`

Example: `my-app-demo.stage.kube-dc.com`

### View Assigned Hostname

After the route is created, check the status annotation:

```bash
kubectl get svc my-app -o jsonpath='{.metadata.annotations.service\.nlb\.kube-dc\.com/route-hostname-status}'
# Output: my-app-demo.stage.kube-dc.com
```

### Optional Annotations

```yaml
annotations:
  # Override auto-generated hostname
  service.nlb.kube-dc.com/route-hostname: "my-app.example.com"
  
  # Specific port for multi-port services
  service.nlb.kube-dc.com/route-port: "8080"
  
  # Custom issuer name (for expose-route: https)
  service.nlb.kube-dc.com/tls-issuer: "letsencrypt"
  
  # Use your own TLS secret (skips auto-certificate creation)
  service.nlb.kube-dc.com/tls-secret: "my-custom-tls"
```

## Architecture

```
┌─────────────────────────────────────────────────────────────────────┐
│              Shared Envoy Gateway (88.99.29.250)                    │
├─────────────────────────────────────────────────────────────────────┤
│  HTTP :80       │  HTTPS :443 (terminate)  │  TLS :443 (passthrough)│
│  HTTPRoute      │  HTTPRoute + Cert        │  TLSRoute (SNI-based)  │
└─────────────────────────────────────────────────────────────────────┘
                  ↓ (auto-created)
┌─────────────────────────────────────────────────────────────────────┐
│  Your Project Namespace                                             │
│  - Deployment/Pod running your app                                  │
│  - LoadBalancer Service + expose-route annotation                   │
│  - EIP, Backend, Route (auto-created by controller)                 │
│  - Certificate + Gateway Listener (for expose-route: https)         │
│  - Issuer (required for https and tls-passthrough)                  │
└─────────────────────────────────────────────────────────────────────┘
```

## Prerequisites

1. You have project admin access (inherited from `default-project-admin-role`)
2. Your project uses cloud networking (`egressNetworkType: cloud`)

## Examples

| Example | Description | File |
|---------|-------------|------|
| Issuer | Let's Encrypt HTTP-01 issuer | [00-issuer.yaml](00-issuer.yaml) |
| HTTP | Simple HTTP service (auto-route) | [01-http-service.yaml](01-http-service.yaml) |
| HTTPS | **Gateway-terminated TLS (recommended)** | [02-https-service.yaml](02-https-service.yaml) |
| gRPC | gRPC service with TLS | [03-grpc-service.yaml](03-grpc-service.yaml) |
| TLS Passthrough | End-to-end TLS (app terminates) | [04-tls-passthrough.yaml](04-tls-passthrough.yaml) |
| HTTPS Own Cert | HTTPS with user-provided certificate | [05-https-own-cert.yaml](05-https-own-cert.yaml) |

## Quick Start

### HTTP Service (Simplest)

```bash
kubectl apply -f 01-http-service.yaml

# Test (hostname auto-generated)
curl http://my-app-{namespace}.{base_domain}
```

### HTTPS Service with Let's Encrypt

```bash
# 1. Create issuer (one-time)
kubectl apply -f 00-issuer.yaml

# 2. Deploy HTTPS service
kubectl apply -f 02-https-service.yaml

# 3. Verify certificate
kubectl get certificate

# 4. Test
curl https://my-secure-app-{namespace}.{base_domain}
```

### Verify Resources

```bash
# Check auto-created resources
kubectl get eip,backend,httproute,tlsroute

# Check certificate status
kubectl get certificate
```

## How It Works

### Automatic Route Creation Flow

1. **Add annotation** `expose-route: https` (or `http` or `tls-passthrough`)
2. **Controller creates** EIP, Backend, and Route automatically
3. **For HTTPS**: Also creates Certificate, ReferenceGrant, and Gateway Listener
4. **Hostname generated** as `{service}-{namespace}.{base_domain}`
5. **Gateway routes** traffic to your app via Backend

### Certificate Issuance (ACME HTTP-01)

1. You create a `Certificate` resource with the auto-generated hostname
2. cert-manager creates an ACME challenge
3. The ACME controller auto-creates solver HTTPRoutes
4. Let's Encrypt verifies via the Gateway
5. Certificate is issued and stored as a Secret
6. Your app uses the certificate for TLS termination

## Route Types

| Type | Port | TLS Termination | App Serves | Use Case |
|------|------|-----------------|------------|----------|
| `http` | 80 | None | HTTP | Plain HTTP traffic |
| `https` | 443 | **Gateway** | HTTP | HTTPS with auto-cert ⭐ **Recommended** |
| `tls-passthrough` | 443 | App | HTTPS | End-to-end TLS (app terminates) |

**Recommendation**: Use `expose-route: https` for simplest setup - your app just serves HTTP and the Gateway handles TLS with auto-provisioned Let's Encrypt certificates.

**Note**: For `tls-passthrough`, your application must handle TLS termination using a certificate you configure.

## Permissions Required

Your project admin role (`default-project-admin-role`) includes:

| Resource | API Group | Permissions |
|----------|-----------|-------------|
| services | core | * |
| secrets | core | * |
| deployments | apps | * |
| issuers | cert-manager.io | create, get, list, watch, delete |
| certificates | cert-manager.io | create, get, list, watch, delete |
| httproutes | gateway.networking.k8s.io | get, list, watch (auto-created) |
| tlsroutes | gateway.networking.k8s.io | get, list, watch (auto-created) |
| backends | gateway.envoyproxy.io | get, list, watch (auto-created) |

## Troubleshooting

### Route not created

```bash
# Check service annotations
kubectl get svc my-app -o yaml | grep -A5 annotations

# Check controller logs
kubectl logs -n kube-dc deployment/kube-dc-manager | grep my-app
```

### Certificate not issuing

```bash
# Check certificate status
kubectl describe certificate my-app-tls

# Check challenges
kubectl get challenges

# Check issuer
kubectl describe issuer letsencrypt
```

### Service not accessible

```bash
# Check auto-created resources
kubectl get eip,backend,httproute,tlsroute

# Check route status
kubectl get httproute -o wide
kubectl get tlsroute -o wide

# Check LoadBalancer IP
kubectl get svc my-app -o wide
```
