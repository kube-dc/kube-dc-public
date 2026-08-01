# VPC & Private Networking

Every Kube-DC Project gets its own Virtual Private Cloud (VPC) powered by [Kube-OVN](https://kubeovn.github.io/docs/). Private addresses are not routed between Projects by default; cross-Project connectivity requires explicit exposure or an operator-approved routing change.

---

## How Project Networking Works

When a project is created, Kube-DC automatically provisions:

1. **A dedicated VPC** — isolated virtual routing domain
2. **A default subnet** — private IP range (e.g., `10.0.0.0/24`)
3. **A VPC router** — handles routing between the subnet and external networks
4. **A default gateway EIP** — provides outbound NAT when platform egress policy and upstream networking allow it

```
┌─────────────────────────────────────────────────────┐
│  Project: production                                │
│  Backing namespace: acme-production                 │
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

Each Project receives a dedicated VPC and platform-managed traffic controls:

- Cross-Project traffic is blocked by default
- Each Project has its own **subnet, router, and default gateway EIP**
- Kubernetes DNS may resolve Service names outside the Project's backing namespace, but name resolution does not grant network reachability
- Cross-Project connectivity requires an operator-approved routing or allowlist change, or an explicitly exposed service

The primary network boundary is the Project VPC and its platform-managed OVN routing policy. Project users do not manage this boundary with tenant-authored `NetworkPolicy` resources.

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

The subnet CIDR is configured when the Project is created. Choose a range sized for the expected VMs, Pods, and Managed Cluster infrastructure. Separate Project VPCs can reuse a CIDR, but avoid overlap when you expect an operator to route those networks together later. The underlying Kube-OVN `Subnet` is a platform resource and is not exposed through a Project kubeconfig. You can inspect the selected CIDR in the Project details or definition:

```yaml
apiVersion: kube-dc.com/v1
kind: Project
metadata:
  name: production
  namespace: acme
spec:
  cidrBlock: "10.0.0.0/24"
  egressNetworkType: cloud  # or "public"
```

### Network Name

VMs use the fully qualified `{backing-namespace}/default` NetworkAttachmentDefinition name. For Project `production` in Organization `acme`:

```yaml
networks:
- name: vpc_net_0
  multus:
    default: true
    networkName: acme-production/default
```

This selects the default VPC network owned by that Project.

---

## Outbound Internet Access (NAT)

VMs and pods send internet-bound traffic through the Project's default gateway EIP when egress is allowed:

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

### Automatic Platform Configuration

Kube-DC configures the Project route, SNAT, and cluster DNS. Workloads need no extra NAT configuration, but internet reachability still depends on installation-wide egress policy, upstream availability, and any workload firewall.

---

## Inbound Access

By default, your VMs and pods are **not accessible from the internet**. To enable inbound access, use one of these methods:

| Method | Use Case | Guide |
|--------|----------|-------|
| **Floating IP** | Direct access to a VM on all ports | [External & Floating IPs](public-floating-ips.md) |
| **LoadBalancer + EIP** | Expose specific ports | [Service Exposure](service-exposure.md) |
| **Gateway Route** | HTTPS with a configured Project Issuer | [Service Exposure](service-exposure.md) |

---

## Internal Communication

### Within a Project

VMs and pods within the same Project can communicate over private IPs unless a guest firewall or workload policy blocks the traffic:

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

Access via DNS: `my-service.acme-production.svc.cluster.local`. Here,
`acme-production` is the backing namespace for Project `production`.

### Cross-Project Communication

Projects are isolated by default. To communicate between projects:

- Expose the destination explicitly with a **Gateway Route** or **LoadBalancer Service**, then use its published hostname or address.
- Ask the platform operator for a private routing and allowlist change when traffic must stay between Project VPCs.

Do not depend on a `<service>-ext` Service name. Some older deployments create that internal alias for specific legacy backends, but it is not the cross-Project service-discovery contract and can be retired automatically.

---

## Reaching Physical Hardware

The VPC above is an overlay Kube-DC builds for you. If a workload has to reach
equipment that already exists in the datacenter — a storage array, an appliance,
anything that only speaks to its own subnet — your project can instead be given a
second interface directly on that physical network segment.

That is a **datacenter VLAN**, and it is handed to your organization by the
platform administrator. Your default route stays on the VPC; the VLAN interface
carries only traffic to that segment.

See [Datacenter VLANs](datacenter-vlans.md).

---

## Next Steps

- [Datacenter VLANs](datacenter-vlans.md) — Attach a project to a physical network segment
- [External & Floating IPs](public-floating-ips.md) — Manage EIPs and FIPs
- [Service Exposure Guide](service-exposure.md) — Expose services with Gateway Routes and LoadBalancers
- [How Networking Works](networking-overview.md) — High-level networking concepts
