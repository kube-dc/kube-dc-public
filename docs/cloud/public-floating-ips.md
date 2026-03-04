# External & Floating IPs

Kube-DC provides two types of external IP addresses for connecting your resources to the internet:

- **External IP (EIP)** — an IP address that can be bound to a LoadBalancer service or used as a project gateway
- **Floating IP (FIP)** — a 1:1 NAT mapping between a public IP and a specific VM or pod

---

## Managing IPs via UI

The **Networking** section in the Console UI shows all IP resources in your project:

<img src={require('./network-mgmt.png').default} alt="Network Management" style={{maxWidth: '900px', width: '100%'}} />

From here you can:

- View all **External IPs** with their network type (Cloud/Public), ownership, and status
- View and manage **Floating IPs** and their VM mappings
- View **Load Balancers** and their endpoints
- Click **+ Create External IP** to allocate a new EIP

---

## External IPs (EIPs)

Every project gets a **default gateway EIP** (`default-gw`) automatically. This EIP handles:
- Outbound NAT for all VMs and pods
- Default endpoint for LoadBalancer services (in public projects)

### EIP Network Types

| `externalNetworkType` | Description | Use Case |
|-----------------------|-------------|----------|
| `cloud` | Shared NAT pool IP (not internet-routable) | Cost-effective, outbound NAT |
| `public` | Dedicated public IP (internet-routable) | Direct access, VMs, static IP |

### Create an EIP

```yaml
apiVersion: kube-dc.com/v1
kind: EIp
metadata:
  name: my-eip
spec:
  externalNetworkType: public
```

```bash
kubectl apply -f eip.yaml
kubectl get eip
```

```
NAME         EXTERNAL IP      READY   AGE
default-gw   100.65.0.115     true    37d
my-eip       91.224.11.20     true    5s
```

### Bind an EIP to a LoadBalancer Service

```yaml
apiVersion: v1
kind: Service
metadata:
  name: my-service
  annotations:
    service.nlb.kube-dc.com/bind-on-eip: "my-eip"
spec:
  type: LoadBalancer
  selector:
    app: my-app
  ports:
  - port: 443
    targetPort: 443
```

### Use the Default Gateway EIP

For projects with `egressNetworkType: public`, the default gateway EIP is public and can be shared across services:

```yaml
annotations:
  service.nlb.kube-dc.com/bind-on-default-gw-eip: "true"
```

:::note Cloud Projects
In cloud projects (`egressNetworkType: cloud`), the default gateway is a shared NAT IP — not publicly routable. Create a dedicated public EIP and use `bind-on-eip` instead.
:::

### Delete an EIP

```bash
kubectl delete eip my-eip
```

:::warning
Deleting an EIP that is bound to a service or FIP will disrupt connectivity. Remove the binding first.
:::

---

## Floating IPs (FIPs)

Floating IPs provide **direct 1:1 NAT** between an external IP and a VM's internal IP. All ports are mapped — the VM is fully accessible as if it had a public IP.

### When to Use FIPs

- **VM direct access** — SSH, RDP, or any protocol on any port
- **Protocols that don't work behind NAT** — some applications need to see their own public IP
- **Simple setup** — no LoadBalancer service needed, just point the FIP at a VM

### Create a FIP for a VM

The FIP uses `vmTarget` to automatically resolve the VM's internal IP via the QEMU guest agent:

```yaml
apiVersion: kube-dc.com/v1
kind: FIp
metadata:
  name: ubuntu-fip
spec:
  externalNetworkType: public
  vmTarget:
    vmName: ubuntu
    interfaceName: vpc_net_0
```

```bash
kubectl apply -f fip.yaml
kubectl get fip
```

```
NAME          TARGET IP    EXTERNAL IP    VM       INTERFACE   READY
ubuntu-fip    10.0.0.153   91.224.11.5    ubuntu   vpc_net_0   true
```

Now SSH directly to the VM:

```bash
ssh ubuntu@91.224.11.5
```

:::tip Automatic EIP
When using `externalNetworkType: public`, a dedicated public EIP is automatically allocated and bound to the FIP. You don't need to create an EIP separately.
:::

### Delete a FIP

```bash
kubectl delete fip ubuntu-fip
```

The auto-allocated EIP is released automatically.

---

## FIP and LoadBalancer Conflict

:::warning Important Limitation
A VM/pod **cannot simultaneously** be:
1. A target for a **public FIP**
2. A backend for a **cloud-network LoadBalancer** service

Public FIPs create source-based policy routes that redirect ALL outbound traffic from that IP to the public gateway, breaking cloud-network LoadBalancer services.
:::

**Workarounds:**
- Use separate pods/VMs for FIP targets and cloud-service backends
- Use the same network type for both (all public or all cloud)
- Choose one exposure method per VM/pod

---

## Quick Reference

| Task | Resource | Key Field |
|------|----------|-----------|
| Get a public IP | `EIp` | `externalNetworkType: public` |
| Bind IP to service | Service annotation | `bind-on-eip: "eip-name"` |
| Use shared project IP | Service annotation | `bind-on-default-gw-eip: "true"` |
| Map IP directly to VM | `FIp` | `vmTarget.vmName: "my-vm"` |
| Auto HTTPS with cert | Service annotation | `expose-route: "https"` |

---

## Next Steps

- [VPC & Private Networking](private-networking.md) — Project isolation and subnets
- [Service Exposure Guide](service-exposure.md) — Complete reference for Gateway Routes, LoadBalancers, and advanced options
