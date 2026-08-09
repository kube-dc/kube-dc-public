# Project Service Exposure Examples

This directory contains examples for exposing services within a Kube-DC Project using cloud networking.

## Overview

Projects with `egressNetworkType: cloud` use private IP addresses from the `ext-cloud` subnet. To expose services externally, you can use **automatic route creation** via service annotations:

- **HTTP** - Plain HTTP traffic on port 80 (`expose-route: http`)
- **HTTPS** - Gateway-terminated TLS on port 443 (`expose-route: https`) **(recommended)**
- **TLS Passthrough** - End-to-end TLS on port 443 (`expose-route: tls-passthrough`)

## Automatic Route Creation

Simply add an annotation to your LoadBalancer Service:

```yaml
apiVersion: v1
kind: Service
metadata:
  name: my-app
  namespace: acme-production
  annotations:
    # Automatically creates: EIP, Backend, Certificate, Gateway Listener, HTTPRoute
    service.nlb.kube-dc.com/expose-route: "https"  # or "http" or "tls-passthrough"
spec:
  type: LoadBalancer
  selector:
    app: my-app
  ports:
    - name: http
      port: 80
      targetPort: 8080
```

The controller will automatically create:
- **EIP** - External IP allocation
- **Backend** - For Gateway routing
- **HTTPRoute/TLSRoute** - With auto-generated hostname

### Auto-Generated Hostname

Format: `{service}-{backing-namespace}.{base-domain}`

Example: `my-app-acme-production.stage.kube-dc.com`

### View Assigned Hostname

After the route is created, check the status annotation:

```bash
kubectl get svc my-app -o jsonpath='{.metadata.annotations.service\.nlb\.kube-dc\.com/route-hostname-status}'
# Output: my-app-acme-production.stage.kube-dc.com
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
│              Shared Envoy Gateway (203.0.113.250)                    │
├─────────────────────────────────────────────────────────────────────┤
│  HTTP :80       │  HTTPS :443 (terminate)  │  TLS :443 (passthrough)│
│  HTTPRoute      │  HTTPRoute + Cert        │  TLSRoute (SNI-based)  │
└─────────────────────────────────────────────────────────────────────┘
                  ↓ (auto-created)
┌─────────────────────────────────────────────────────────────────────┐
│  Project: production / backing namespace: acme-production          │
│  - Deployment/Pod running your app                                  │
│  - LoadBalancer Service + expose-route annotation                   │
│  - EIP, Backend, Route (auto-created by controller)                 │
│  - Certificate + Gateway Listener (for expose-route: https)         │
│  - Issuer (required for automatic HTTPS certificates)              │
└─────────────────────────────────────────────────────────────────────┘
```

## Prerequisites

1. You have the standard Project `admin` role
2. Your Project uses cloud networking (`egressNetworkType: cloud`)

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

# Read the generated hostname, then test it
HOST="$(kubectl get svc my-app -o jsonpath='{.metadata.annotations.service\\.nlb\\.kube-dc\\.com/route-hostname-status}')"
curl "http://$HOST"
```

### HTTPS Service with Let's Encrypt

```bash
# 1. Create issuer (one-time)
kubectl apply -f 00-issuer.yaml

# 2. Deploy HTTPS service
kubectl apply -f 02-https-service.yaml

# 3. Verify certificate
kubectl get certificate

# 4. Read the generated hostname, then test it
HOST="$(kubectl get svc my-secure-app -o jsonpath='{.metadata.annotations.service\\.nlb\\.kube-dc\\.com/route-hostname-status}')"
curl "https://$HOST"
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
4. **Hostname generated** as `{service}-{backing-namespace}.{base-domain}`
5. **Gateway routes** traffic to your app via Backend

### Certificate Issuance (ACME HTTP-01)

1. The route controller creates a `Certificate` for the selected hostname
2. cert-manager creates an ACME challenge
3. The ACME controller auto-creates solver HTTPRoutes
4. Let's Encrypt verifies via the Gateway
5. Certificate is issued and stored as a Secret
6. Your app uses the certificate for TLS termination

## Route Types

| Type | Port | TLS Termination | App Serves | Use Case |
|------|------|-----------------|------------|----------|
| `http` | 80 | None | HTTP | Plain HTTP traffic |
| `https` | 443 | **Gateway** | HTTP | HTTPS with automatic certificate **(recommended)** |
| `tls-passthrough` | 443 | App | HTTPS | End-to-end TLS (app terminates) |

**Recommendation**: Use `expose-route: https` for simplest setup - your app just serves HTTP and the Gateway handles TLS with auto-provisioned Let's Encrypt certificates.

**Note**: For `tls-passthrough`, your application must handle TLS termination using a certificate you configure.

## Permissions

Use the standard Project `admin` role for these examples. The manifests create
Deployments, Services, and an Issuer in the Project's backing namespace; the
platform creates the route resources. Custom roles need equivalent permissions;
see
[Team Management](../../docs/cloud/team-management.md) for the supported role
model.

## Troubleshooting

### Route not created

```bash
# Check service annotations
kubectl get svc my-app -o yaml | grep -A5 annotations

# Inspect the Service and the Project-visible route resources
kubectl describe svc my-app
kubectl get eip,backend,httproute,tlsroute

# If no status explains the failure, contact your platform administrator
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
