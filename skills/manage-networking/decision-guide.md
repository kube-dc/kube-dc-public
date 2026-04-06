# Networking Decision Guide

## EIP vs FIP

| Aspect | EIp (External IP) | FIp (Floating IP) |
|--------|-------------------|-------------------|
| **Mechanism** | Allocated IP bound to LoadBalancer Service | 1:1 NAT directly to VM/pod |
| **Target** | Service (any pods matching selector) | Single VM interface |
| **Protocols** | Any (via Service ports) | All traffic (all ports) |
| **Load balancing** | Yes (across pod replicas) | No (single target) |
| **Multiple services** | Yes (one EIP, many services) | No (one FIP per VM) |
| **Use case** | Services, apps, SSH via port mapping | Direct VM access, all ports |

## When to Use What

### Web App (HTTP/HTTPS)
→ **Gateway Route** (`expose-route: https`) — simplest, auto TLS, auto DNS

### SSH to VM
→ **FIp** (simplest — direct IP to VM, all ports) OR
→ **EIp + LoadBalancer** (if you need port mapping or multiple VMs)

### Game Server
→ **EIp + LoadBalancer** with UDP ports

### Database External Access
→ **Gateway** (`spec.expose.type: gateway` on KdcDatabase) — TLS passthrough
→ Or **port-forward** for dev/ad-hoc access

### Multiple VMs with Public Access
→ **One FIp per VM** (each gets a dedicated IP)
→ Or **One EIp + multiple LoadBalancer services** with different ports

## Network Type: Cloud vs Public

| `externalNetworkType` | Pool | Use |
|----------------------|------|-----|
| `public` | Internet-routable IPs | External access from internet |
| `cloud` | NAT pool IPs | Internal/cloud-only access |

Cloud projects can still request `public` EIPs — the project network type only affects the **default** pool.
