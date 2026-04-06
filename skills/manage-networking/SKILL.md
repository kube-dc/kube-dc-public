---
name: manage-networking
description: Manage Kube-DC networking resources — create External IPs (EIp), Floating IPs (FIp), and understand VPC networking. Includes decision guide for choosing between EIP and FIP.
---

## Prerequisites
- Target project must exist and be Ready
- Project namespace: `{org}-{project}`

## Concepts

### EIp (External IP)
An IP address allocated from an external network pool. Used with LoadBalancer services to expose apps.

### FIp (Floating IP)
A 1:1 NAT mapping from a public IP directly to a VM or pod. Traffic goes straight to the target — no LoadBalancer needed.

### VPC Network
Every project gets `{namespace}/default` — an isolated Kube-OVN subnet. All VMs and pods connect here.

## Decision Guide

| Need | → Use |
|------|-------|
| Expose a Service (HTTP/TCP/UDP) | EIp + LoadBalancer (or Gateway Route) |
| Direct IP access to a VM | FIp with `vmTarget` |
| Multiple services on one IP | EIp + multiple LoadBalancer services |
| Dedicated IP per VM | FIp (one FIP per VM interface) |
| SSH to a VM | FIp OR EIp + LoadBalancer port 22 |

## Create EIp

```yaml
apiVersion: kube-dc.com/v1
kind: EIp
metadata:
  name: {eip-name}
  namespace: {project-namespace}
spec:
  externalNetworkType: public    # public = routable internet IP
                                  # cloud = shared NAT pool IP
```

See @eip-template.yaml

### Use EIp with a Service

```yaml
annotations:
  service.nlb.kube-dc.com/bind-on-eip: "{eip-name}"
```

## Create FIp (Floating IP for VM)

```yaml
apiVersion: kube-dc.com/v1
kind: FIp
metadata:
  name: {fip-name}
  namespace: {project-namespace}
spec:
  externalNetworkType: public
  vmTarget:
    vmName: {vm-name}
    interfaceName: default
```

See @fip-template.yaml

### Check FIp Status

```bash
kubectl get fip {fip-name} -n {project-namespace}
# Shows allocated external IP in status
```

## List Networking Resources

```bash
kubectl get eip -n {project-namespace}
kubectl get fip -n {project-namespace}
kubectl get svc -n {project-namespace}
```

## Safety
- **FIP + LoadBalancer conflict**: A VM CANNOT simultaneously be a FIP target AND a cloud-network LoadBalancer backend
- FIPs with `externalNetworkType: public` auto-create an EIP — don't manually create both
- Each project gets a default gateway EIP automatically
- Prefer `externalNetworkType: public` when you need internet-routable IPs
- Use `externalNetworkType: cloud` for internal/NAT-only access
