import {OutboundTrafficDiagram} from '@site/src/components/Diagram/CloudFlowDiagrams';
import {ProjectPrivateNetworkDiagram} from '@site/src/components/Diagram/CloudTopologyDiagrams';

# VPC & private networking

Every Kube-DC Project gets its own Virtual Private Cloud (VPC) powered by [Kube-OVN](https://kubeovn.github.io/docs/). Private addresses are not routed between Projects by default; cross-Project connectivity requires explicit exposure or an operator-approved routing change.

---

## How Project networking works

When a project is created, Kube-DC automatically provisions:

1. **A dedicated VPC**: isolated virtual routing domain
2. **A default subnet**: private IP range (for example, `10.0.0.0/24`)
3. **A VPC router**: handles routing between the subnet and external networks
4. **A default gateway EIP**: provides outbound NAT when platform egress policy and upstream networking allow it

<details data-github-only>
<summary>Diagram source for GitHub</summary>

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

</details>

<ProjectPrivateNetworkDiagram />

---

## Project isolation

Each Project receives a dedicated VPC and platform-managed traffic controls:

- Cross-Project traffic is blocked by default
- Each Project has its own **subnet, router, and default gateway EIP**
- Kubernetes DNS may resolve Service names outside the Project's backing namespace, but name resolution does not grant network reachability
- Cross-Project connectivity requires an operator-approved routing or allowlist change, or an explicitly exposed service

The primary network boundary is the Project VPC and its platform-managed OVN routing policy. Project users do not manage this boundary with tenant-authored `NetworkPolicy` resources.

---

## Subnet and IP allocation

### Automatic assignment

When you create a VM or pod, it automatically receives an IP from the project's subnet:

```bash
# Check your VM's IP
kubectl get vmi

# Check pod IPs
kubectl get pods -o wide
```

### Subnet details

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

### Network name

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

## Outbound internet access (NAT)

VMs and pods send internet-bound traffic through the Project's default gateway EIP when egress is allowed:

<details data-github-only>
<summary>Diagram source for GitHub</summary>

```
Pod (10.0.0.20)  →  VPC Router  →  SNAT to EIP  →  Internet
```

</details>

<OutboundTrafficDiagram />

The VPC router performs **source NAT (SNAT)**. It rewrites the source IP of an outgoing packet from the private subnet address to the Project's gateway EIP, and it routes the return traffic back.

### Check your Project's Gateway

```bash
kubectl get eip default-gw
```

```
NAME         EXTERNAL IP      NETWORK TYPE   READY
default-gw   100.65.0.115     Cloud          true
```

### Automatic platform configuration

Kube-DC configures the Project route, SNAT, and cluster DNS. Workloads need no extra NAT configuration, but internet reachability still depends on installation-wide egress policy, upstream availability, and any workload firewall.

---

## Inbound access

By default, your VMs and pods are **not accessible from the internet**. To enable inbound access, use one of these methods:

| Method | Use Case | Guide |
|--------|----------|-------|
| **Floating IP** | Direct access to a VM on all ports | [External and floating IPs](public-floating-ips.md) |
| **LoadBalancer + EIP** | Expose specific ports | [Service exposure](service-exposure.md) |
| **Gateway Route** | HTTPS with a configured Project Issuer | [Service exposure](service-exposure.md) |

---

## Internal communication

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

Access through DNS: `my-service.acme-production.svc.cluster.local`. Here,
`acme-production` is the backing namespace for Project `production`.

### Cross-Project communication

Projects are isolated by default. To communicate between projects:

- Expose the destination explicitly with a **Gateway Route** or **LoadBalancer Service**, then use its published hostname or address.
- Ask the platform operator for a private routing and allowlist change when traffic must stay between Project VPCs.

Do not depend on a `<service>-ext` Service name. Some older deployments create that internal alias for specific legacy backends, but it is not the cross-Project service-discovery contract and can be retired automatically.

---

## Reach physical hardware

The VPC above is an overlay Kube-DC builds for you. If a workload has to reach
equipment that already exists in the datacenter, such as a storage array or an
appliance that speaks only to its own subnet, your Project can instead get a
second interface directly on that physical network segment.

That is a **datacenter VLAN**, and it is handed to your organization by the
platform administrator. Your default route stays on the VPC; the VLAN interface
carries only traffic to that segment.

See [Datacenter VLANs](datacenter-vlans.md).

---

## Next steps

- [Datacenter VLANs](datacenter-vlans.md): Attach a project to a physical network segment
- [External and floating IPs](public-floating-ips.md): Manage EIPs and FIPs
- [Service exposure](service-exposure.md): Expose services with Gateway Routes and LoadBalancers
- [How networking works](networking-overview.md): High-level networking concepts
