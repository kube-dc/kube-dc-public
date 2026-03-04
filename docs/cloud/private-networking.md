# VPC & Private Networking

Every Kube-DC project gets its own isolated Virtual Private Cloud (VPC) powered by [Kube-OVN](https://kubeovn.github.io/docs/). This provides network isolation between projects — VMs and pods in one project cannot communicate with those in another project.

---

## How Project Networking Works

When a project is created, Kube-DC automatically provisions:

1. **A dedicated VPC** — isolated network namespace
2. **A default subnet** — private IP range (e.g., `10.0.0.0/24`)
3. **A VPC router** — handles routing between the subnet and external networks
4. **A default gateway EIP** — enables outbound internet access via NAT

```
┌─────────────────────────────────────────────────────┐
│  Project: my-org-my-project                         │
│                                                     │
│  ┌─────────────────────────────────┐                │
│  │  Subnet: 10.0.0.0/24            │                │
│  │                                 │                │
│  │  VM: ubuntu    → 10.0.0.10      │                │
│  │  VM: debian    → 10.0.0.11      │                │
│  │  Pod: nginx    → 10.0.0.20      │                │
│  └──────────────┬──────────────────┘                │
│                 │                                   │
│  ┌──────────────┴──────────────────┐                │
│  │  VPC Router                     │                │
│  │  SNAT: 10.0.0.0/24 → EIP        │                │
│  └──────────────┬──────────────────┘                │
│                 │                                   │
│  ┌──────────────┴──────────────────┐                │
│  │  Default Gateway EIP            │                │
│  │  (cloud or public)              │                │
│  └─────────────────────────────────┘                │
└─────────────────────────────────────────────────────┘
```

---

## Project Isolation

Each project is a fully isolated network environment:

- **VMs and pods** in one project **cannot reach** resources in another project
- Each project has its own **subnet, router, and EIP**
- **DNS** resolves only within the project namespace
- Traffic between projects must go through external IPs (EIP/FIP/LoadBalancer)

This isolation is enforced at the OVN level — it's not just Kubernetes network policies, but actual network separation in the virtual switch layer.

---

## Subnet and IP Allocation

### Automatic Assignment

When you create a VM or pod, it automatically receives an IP from the project's subnet:

```bash
# Check your VM's IP
kubectl get vmi

# Check pod IPs
kubectl get pods -o wide
```

### Subnet Details

```bash
# View your project's subnet
kubectl get subnet -l project=my-project
```

Each project subnet is typically a `/24` block (254 usable addresses). The subnet CIDR is configured when the project is created:

```yaml
apiVersion: kube-dc.com/v1
kind: Project
metadata:
  name: my-project
  namespace: my-org
spec:
  cidrBlock: "10.0.0.0/24"
  egressNetworkType: cloud  # or "public"
```

### Network Name

VMs connect to the project network using `networkName: default`:

```yaml
networks:
- name: vpc_net_0
  multus:
    default: true
    networkName: default
```

This refers to the project's default subnet — Kube-DC resolves it to the correct VPC network automatically.

---

## Outbound Internet Access (NAT)

All VMs and pods can access the internet through the project's default gateway EIP:

```
Pod (10.0.0.20)  →  VPC Router  →  SNAT to EIP  →  Internet
```

The VPC router performs **Source NAT (SNAT)** — it rewrites the source IP of outgoing packets from the private subnet IP to the project's gateway EIP. Return traffic is automatically routed back.

### Check Your Project's Gateway

```bash
kubectl get eip default-gw
```

```
NAME         EXTERNAL IP      NETWORK TYPE   READY
default-gw   100.65.0.115     Cloud          true
```

### No Configuration Needed

Outbound internet access works automatically for all VMs and pods — no additional configuration required. DNS resolution also works out of the box.

---

## Inbound Access

By default, your VMs and pods are **not accessible from the internet**. To enable inbound access, use one of these methods:

| Method | Use Case | Guide |
|--------|----------|-------|
| **Floating IP** | Direct access to a VM on all ports | [External & Floating IPs](public-floating-ips.md) |
| **LoadBalancer + EIP** | Expose specific ports | [Service Exposure](service-exposure.md) |
| **Gateway Route** | HTTPS with auto TLS certificate | [Service Exposure](service-exposure.md) |

---

## Internal Communication

### Within a Project

All VMs and pods within the same project can communicate freely over private IPs:

```bash
# From one VM, ping another
ping 10.0.0.11

# Access a pod's service
curl http://10.0.0.20:80
```

### Kubernetes Services

Standard Kubernetes Services work for in-cluster service discovery:

```yaml
apiVersion: v1
kind: Service
metadata:
  name: my-service
spec:
  type: ClusterIP
  selector:
    app: my-app
  ports:
  - port: 80
    targetPort: 80
```

Access via DNS: `my-service.my-org-my-project.svc.cluster.local`

### Cross-Project Communication

Projects are isolated by default. To communicate between projects:

- Use **LoadBalancer services** with EIPs — each project accesses the other via external IP
- Use **external endpoints** — Kube-DC auto-creates `<service>-ext` DNS entries for LoadBalancer services

---

## Next Steps

- [External & Floating IPs](public-floating-ips.md) — Manage EIPs and FIPs
- [Service Exposure Guide](service-exposure.md) — Expose services with Gateway Routes and LoadBalancers
- [How Networking Works](networking-overview.md) — High-level networking concepts
