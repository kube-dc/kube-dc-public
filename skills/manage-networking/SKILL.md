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

## Verification

After creating networking resources:

### EIp
```bash
# 1. Check EIP has allocated IP
kubectl get eip {eip-name} -n {project-namespace} -o jsonpath='{.status.ipAddress}'
# Expected: allocated IP address

# 2. Check EIP phase
kubectl get eip {eip-name} -n {project-namespace} -o jsonpath='{.status.phase}'
# Expected: Active
```

### FIp
```bash
# 1. Check FIP has allocated IP
kubectl get fip {fip-name} -n {project-namespace} -o jsonpath='{.status.ipAddress}'
# Expected: allocated public IP

# 2. Check FIP is bound to target
kubectl get fip {fip-name} -n {project-namespace} -o jsonpath='{.status.phase}'
# Expected: Active

# 3. Test connectivity to VM via FIP
ping -c 3 {fip-external-ip}
ssh -i /tmp/vm_ssh_key {os-user}@{fip-external-ip}
```

**Success**: IP allocated, phase Active.
**Failure**: `kubectl describe eip|fip {name} -n {project-namespace}` — check events.
## Safety
- **FIP + LoadBalancer conflict**: A VM CANNOT simultaneously be a FIP target AND a cloud-network LoadBalancer backend
- FIPs with `externalNetworkType: public` auto-create an EIP — don't manually create both
- Each project gets a default gateway EIP automatically
- Prefer `externalNetworkType: public` when you need internet-routable IPs
- Use `externalNetworkType: cloud` for internal/NAT-only access
