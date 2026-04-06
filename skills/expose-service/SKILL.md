---
name: expose-service
description: Expose a Kube-DC service externally via Gateway Route (HTTP/HTTPS/gRPC with auto TLS) or Direct EIP + LoadBalancer (any TCP/UDP). Includes decision guide for choosing the right exposure method.
---

## Prerequisites
- Target service or deployment must exist in a project namespace
- For HTTPS: cert-manager `Issuer` must exist in the namespace

## Decision Guide

| Need | → Method | Annotation |
|------|----------|------------|
| HTTP/HTTPS web app | Gateway Route | `expose-route: "https"` |
| Auto TLS certificate | Gateway Route | `expose-route: "https"` |
| gRPC service | Gateway Route | `expose-route: "https"` + `route-port` |
| TLS passthrough (app handles TLS) | Gateway Route | `expose-route: "tls-passthrough"` |
| SSH to VM | Direct EIP | `bind-on-eip: "{eip-name}"` |
| Game server (UDP) | Direct EIP | `bind-on-eip: "{eip-name}"` |
| Database external access | Direct EIP or Gateway | See create-database skill |
| Custom TCP protocol | Direct EIP | `bind-on-eip: "{eip-name}"` |

## Path A: Gateway Route (HTTP/HTTPS/gRPC)

Traffic flows through shared Envoy Gateway with auto DNS + TLS.

### HTTPS (most common)

```yaml
apiVersion: v1
kind: Service
metadata:
  name: {service-name}
  namespace: {project-namespace}
  annotations:
    service.nlb.kube-dc.com/expose-route: "https"
spec:
  type: LoadBalancer
  ports:
    - port: {port}
      targetPort: {port}
  selector:
    app: {app-name}
```

→ Auto hostname: `{service-name}-{project-namespace}.kube-dc.cloud`
→ Auto TLS: Let's Encrypt certificate provisioned automatically

### Custom Hostname

```yaml
annotations:
  service.nlb.kube-dc.com/expose-route: "https"
  service.nlb.kube-dc.com/route-hostname: "myapp.example.com"
```

You must configure DNS (CNAME or A record) to point to the gateway IP.

### Custom TLS Certificate

```yaml
annotations:
  service.nlb.kube-dc.com/expose-route: "https"
  service.nlb.kube-dc.com/tls-secret: "my-tls-secret"
```

### gRPC Service

```yaml
annotations:
  service.nlb.kube-dc.com/expose-route: "https"
  service.nlb.kube-dc.com/route-port: "50051"
```

### TLS Passthrough

Application terminates TLS itself:

```yaml
annotations:
  service.nlb.kube-dc.com/expose-route: "tls-passthrough"
```

### Issuer Setup (Required for HTTPS)

```yaml
apiVersion: cert-manager.io/v1
kind: Issuer
metadata:
  name: letsencrypt
  namespace: {project-namespace}
spec:
  acme:
    server: https://acme-v02.api.letsencrypt.org/directory
    email: {email}
    privateKeySecretRef:
      name: letsencrypt-key
    solvers:
      - http01:
          ingress:
            ingressClassName: envoy
```

See @envoy-gateway-examples.yaml for complete examples.

## Path B: Direct EIP + LoadBalancer (Any TCP/UDP)

Dedicated public IP, no Envoy. Application handles its own TLS/DNS.

### Step 1: Create EIP

```yaml
apiVersion: kube-dc.com/v1
kind: EIp
metadata:
  name: {eip-name}
  namespace: {project-namespace}
spec:
  externalNetworkType: public    # or: cloud
```

### Step 2: Bind Service

```yaml
apiVersion: v1
kind: Service
metadata:
  name: {service-name}
  namespace: {project-namespace}
  annotations:
    service.nlb.kube-dc.com/bind-on-eip: "{eip-name}"
spec:
  type: LoadBalancer
  ports:
    - port: {port}
      targetPort: {port}
      protocol: TCP    # or: UDP
  selector:
    app: {app-name}
```

See @eip-loadbalancer-examples.yaml for SSH, game server, and multi-port examples.

## Annotations Quick Reference

| Annotation | Values | Effect |
|------------|--------|--------|
| `service.nlb.kube-dc.com/expose-route` | `http`, `https`, `tls-passthrough` | Create Gateway Route |
| `service.nlb.kube-dc.com/route-hostname` | FQDN | Override auto hostname |
| `service.nlb.kube-dc.com/route-port` | port number | Target port for gateway |
| `service.nlb.kube-dc.com/tls-issuer` | issuer name | cert-manager Issuer (default: `letsencrypt`) |
| `service.nlb.kube-dc.com/tls-secret` | secret name | User-provided TLS cert |
| `service.nlb.kube-dc.com/bind-on-eip` | EIP name | Bind LB to specific EIP |
| `service.nlb.kube-dc.com/autodelete` | `"true"` | Auto-delete EIP when service deleted |

## Verification

After exposing the service, run these checks:

### Gateway Route
```bash
# 1. Check hostname was assigned
kubectl get svc {service-name} -n {project-namespace} -o jsonpath='{.metadata.annotations.service\.nlb\.kube-dc\.com/route-hostname-status}'
# Expected: {service-name}-{project-namespace}.kube-dc.cloud

# 2. Check TLS certificate is issued (for HTTPS)
kubectl get certificate -n {project-namespace}
# Expected: READY=True

# 3. Test endpoint
curl -s -o /dev/null -w "%{http_code}" https://\{service-name\}-\{project-namespace\}.kube-dc.cloud
# Expected: HTTP status from your app (200, 301, etc.)
```

### Direct EIP
```bash
# 1. Check EIP has allocated IP
kubectl get eip {eip-name} -n {project-namespace} -o jsonpath='{.status.ipAddress}'
# Expected: public IP address

# 2. Check service has external IP
kubectl get svc {service-name} -n {project-namespace} -o jsonpath='{.status.loadBalancer.ingress[0].ip}'
# Expected: same IP as EIP

# 3. Test connectivity
nc -zv {external-ip} {port}
# Expected: Connection succeeded
```

**Success**: Hostname assigned (Gateway) or external IP allocated (EIP), endpoint reachable.
**Failure**:
- No hostname: check Issuer exists, service type is LoadBalancer
- No EIP IP: `kubectl describe eip {eip-name} -n {project-namespace}`
- Connection refused: verify selector matches pods, targetPort is correct
## Safety
- Never mix Gateway Route and Direct EIP on the same service
- FIP and LoadBalancer on the same target are mutually exclusive
- Verify Issuer exists before using `expose-route: https`
- Prefer Gateway Route for HTTP workloads (cost-effective, auto TLS)
- Use Direct EIP only when you need raw TCP/UDP access
