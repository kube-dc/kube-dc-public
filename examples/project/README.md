# Project Service Exposure Examples

This directory contains examples for exposing services within a Kube-DC project using cloud networking.

## Overview

Projects with `egressNetworkType: cloud` use private IP addresses from the `ext-cloud` subnet. To expose services externally, you use the shared Envoy Gateway with:

- **HTTP** - Plain HTTP traffic on port 80
- **HTTPS** - TLS-terminated traffic on port 443 with automatic certificates
- **gRPC** - gRPC services with TLS

## Architecture

```
┌─────────────────────────────────────────────────────────────────────┐
│              Shared Envoy Gateway (88.99.29.250)                    │
├─────────────────────────────────────────────────────────────────────┤
│  HTTP :80       │  HTTPS :443         │  TLS :6443 (passthrough)   │
│  HTTPRoute      │  HTTPRoute + TLS    │  TLSRoute                   │
└─────────────────────────────────────────────────────────────────────┘
                  ↓
┌─────────────────────────────────────────────────────────────────────┐
│  Your Project Namespace                                             │
│  - Deployment/Pod running your app                                  │
│  - LoadBalancer Service (ext-cloud IP)                              │
│  - Backend resource (auto-created)                                  │
│  - Certificate + Issuer (for HTTPS)                                 │
└─────────────────────────────────────────────────────────────────────┘
```

## Prerequisites

1. You have project admin access (inherited from `default-project-admin-role`)
2. Your project uses cloud networking (`egressNetworkType: cloud`)
3. DNS record pointing your domain to the Gateway IP

## Examples

| Example | Description | File |
|---------|-------------|------|
| Issuer | Let's Encrypt HTTP-01 issuer | [00-issuer.yaml](00-issuer.yaml) |
| HTTP | Simple HTTP service exposure | [01-http-service.yaml](01-http-service.yaml) |
| HTTPS | HTTPS with auto-certificate | [02-https-service.yaml](02-https-service.yaml) |
| gRPC | gRPC service with TLS | [03-grpc-service.yaml](03-grpc-service.yaml) |
| TLS Passthrough | End-to-end TLS (app terminates) | [04-tls-passthrough.yaml](04-tls-passthrough.yaml) |

## Quick Start

### Step 1: Create Issuer (one-time setup)

```bash
kubectl apply -f 00-issuer.yaml
```

### Step 2: Deploy your application

```bash
# For HTTP only
kubectl apply -f 01-http-service.yaml

# For HTTPS with certificate
kubectl apply -f 02-https-service.yaml
```

### Step 3: Verify

```bash
# Check certificate status
kubectl get certificate

# Check HTTPRoute status
kubectl get httproute

# Test your service
curl https://your-app.example.com
```

## How It Works

### Cloud Networking Flow

1. **Your app** runs in a pod with a private IP
2. **LoadBalancer Service** gets an ext-cloud IP (e.g., `100.65.0.x`)
3. **Backend resource** is auto-created pointing to the ext-cloud IP
4. **HTTPRoute** routes traffic from Gateway to Backend
5. **Gateway** receives external traffic and routes to your app

### Certificate Issuance (ACME HTTP-01)

1. You create a `Certificate` resource
2. cert-manager creates an ACME challenge
3. The ACME controller auto-creates solver Service/Backend
4. Let's Encrypt verifies via the Gateway
5. Certificate is issued and stored as a Secret

## Permissions Required

Your project admin role (`default-project-admin-role`) includes:

| Resource | API Group | Permissions |
|----------|-----------|-------------|
| services | core | * |
| secrets | core | * |
| deployments | apps | * |
| issuers | cert-manager.io | create, get, list, watch, delete |
| certificates | cert-manager.io | create, get, list, watch, delete |
| httproutes | gateway.networking.k8s.io | create, get, list, watch, delete, update, patch |
| grpcroutes | gateway.networking.k8s.io | create, get, list, watch, delete, update, patch |
| tlsroutes | gateway.networking.k8s.io | create, get, list, watch, delete, update, patch |
| backends | gateway.envoyproxy.io | get, list, watch (read-only, auto-created) |

## Troubleshooting

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
# Check Backend was created
kubectl get backend

# Check HTTPRoute status
kubectl get httproute -o wide

# Check LoadBalancer IP
kubectl get svc my-app -o wide
```

### DNS issues

Ensure your DNS record points to the Gateway IP:
```bash
dig your-app.example.com
# Should return: 88.99.29.250 (Gateway IP)
```
