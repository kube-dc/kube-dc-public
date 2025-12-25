# Networking Architecture

Kube-DC provides enterprise-grade networking through Kube-OVN and Envoy Gateway, enabling multi-tenant isolation, flexible service exposure, and automatic TLS management.

## Quick Navigation

| Section | Description |
|---------|-------------|
| [Network Types](#network-types) | Cloud vs Public networks |
| [Physical Layer](#physical-network-layer) | VLANs and provider bridges |
| [OVN Architecture](#ovn-logical-network) | VPCs, subnets, routers |
| [Service Exposure](#service-exposure) | LoadBalancers, Gateway Routes |
| [Envoy Gateway](#envoy-gateway) | HTTP/HTTPS/gRPC routing |

---

## Network Types

Kube-DC supports two external network types:

| Type | Subnet | IP Range | Internet Routable | Use Case |
|------|--------|----------|-------------------|----------|
| **Cloud** | `ext-cloud` | 100.65.0.0/16 | ❌ No (NAT pool) | Web apps, APIs, cost-effective |
| **Public** | `ext-public` | 168.119.17.48/28 | ✅ Yes | VMs, game servers, direct access |

---

## Physical Network Layer

```
┌─────────────────────────────────────────────────────────────────────────────┐
│                         PHYSICAL NETWORK                                    │
│                                                                             │
│  ┌─────────────────────────┐              ┌─────────────────────────┐       │
│  │      VLAN 4011          │              │      VLAN 4013          │       │
│  │      ext-public         │              │      ext-cloud          │       │
│  │   168.119.17.48/28      │              │   100.65.0.0/16         │       │
│  │                         │              │                         │       │
│  │   Gateway: 168.119.17.49│              │   Gateway: 100.65.0.1   │       │
│  │   Internet-routable     │              │   Internal-only         │       │
│  └───────────┬─────────────┘              └───────────┬─────────────┘       │
│              │                                        │                     │
│              └────────────────┬───────────────────────┘                     │
│                               │                                             │
│                     ┌─────────┴─────────┐                                   │
│                     │   Provider Bridge │                                   │
│                     │   br-ext-cloud    │                                   │
│                     │   (on each node)  │                                   │
│                     └─────────┬─────────┘                                   │
└───────────────────────────────┼─────────────────────────────────────────────┘
                                │
                                ▼
┌───────────────────────────────────────────────────────────────────────────────┐
│                              OVN NETWORK                                      │
└───────────────────────────────────────────────────────────────────────────────┘
```

---

## OVN Logical Network

### Management VPC (ovn-cluster)

```
┌────────────────────────────────────────────────────────────────────────────────┐
│                          ovn-cluster VPC (Management)                          │
│                                                                                │
│   ┌─────────────────┐  ┌─────────────────┐  ┌─────────────────┐  ┌───────────┐ │
│   │   ovn-default   │  │    ext-cloud    │  │   ext-public    │  │   join    │ │
│   │  10.100.0.0/16  │  │  100.65.0.0/16  │  │168.119.17.48/28 │  │172.30.0.0 │ │
│   │                 │  │                 │  │                 │  │   /22     │ │
│   │ • kube-system   │  │ • Cloud LB VIPs │  │ • Public LB VIPs│  │ • Node IPs│ │
│   │ • envoy-gateway │  │ • Cloud EIPs    │  │ • Public EIPs   │  │           │ │
│   └────────┬────────┘  └────────┬────────┘  └────────┬────────┘  └─────┬─────┘ │
│            │                    │                    │                 │       │
│            └────────────────────┼────────────────────┼─────────────────┘       │
│                                 │                    │                         │
│                       ┌─────────┴────────────────────┴─────────┐               │
│                       │         ovn-cluster Router             │               │
│                       │                                        │               │
│                       │  Ports:                                │               │
│                       │  • ovn-default: 10.100.0.1             │               │
│                       │  • ext-cloud: 100.65.0.101             │               │
│                       │  • join: 172.30.0.1                    │               │
│                       │                                        │               │
│                       │  SNAT: 10.100.0.0/16 → 100.65.0.101    │               │
│                       └────────────────────────────────────────┘               │
└────────────────────────────────────────────────────────────────────────────────┘
```

### Project VPCs

```
┌─────────────────────────────────┐  ┌─────────────────────────────────┐
│   Cloud Project VPC             │  │   Public Project VPC            │
│   (egressNetworkType: cloud)    │  │   (egressNetworkType: public)   │
│                                 │  │                                 │
│   ┌─────────────────────────┐   │  │   ┌─────────────────────────┐   │
│   │  project-a-default      │   │  │   │  project-b-default      │   │
│   │     10.0.10.0/24        │   │  │   │     10.0.20.0/24        │   │
│   │                         │   │  │   │                         │   │
│   │  • Customer pods        │   │  │   │  • Customer pods        │   │
│   │  • Customer VMs         │   │  │   │  • Customer VMs         │   │
│   └───────────┬─────────────┘   │  │   └───────────┬─────────────┘   │
│               │                 │  │               │                 │
│   ┌───────────┴─────────────┐   │  │   ┌───────────┴─────────────┐   │
│   │  project-a Router       │   │  │   │  project-b Router       │   │
│   │                         │   │  │   │                         │   │
│   │  EIP: 100.65.0.102      │   │  │   │  EIP: 168.119.17.51     │   │
│   │  (ext-cloud)            │   │  │   │  (ext-public)           │   │
│   │                         │   │  │   │                         │   │
│   │  SNAT: 10.0.10.0/24     │   │  │   │  SNAT: 10.0.20.0/24     │   │
│   │       → 100.65.0.102    │   │  │   │       → 168.119.17.51   │   │
│   └─────────────────────────┘   │  │   └─────────────────────────┘   │
└─────────────────────────────────┘  └─────────────────────────────────┘
```

### Subnet Summary

| Subnet | VPC | CIDR | Purpose |
|--------|-----|------|---------|
| `ovn-default` | ovn-cluster | 10.100.0.0/16 | Management pods |
| `ext-cloud` | ovn-cluster | 100.65.0.0/16 | Cloud LB VIPs, EIPs |
| `ext-public` | ovn-cluster | 168.119.17.48/28 | Public LB VIPs, EIPs |
| `join` | ovn-cluster | 172.30.0.0/22 | Node-to-OVN connectivity |
| `{project}-default` | {project} | 10.x.x.x/24 | Customer pods/VMs |

---

## Service Exposure

Kube-DC provides multiple ways to expose services:

```
┌─────────────────────────────────────────────────────────────────────────────────┐
│                         SERVICE EXPOSURE OPTIONS                                │
│                                                                                 │
│   ┌─────────────────────────────────────────────────────────────────────────┐   │
│   │                      1. Gateway Routes (Recommended)                    │   │
│   │                                                                         │   │
│   │   Internet → Envoy Gateway (88.99.29.250:443) → HTTPRoute → Service     │   │
│   │                                                                         │   │
│   │   -  Automatic TLS certificates                                         │   │
│   │   -  Auto-generated hostnames                                           │   │
│   │   -  Shared infrastructure (cost-effective)                             │   │
│   │   -  HTTP/HTTPS/gRPC support                                            │   │
│   │                                                                         │   │
│   │   Annotations:                                                          │   │
│   │   • expose-route: https                                                 │   │
│   │   • route-hostname: custom.domain.com (optional)                        │   │
│   └─────────────────────────────────────────────────────────────────────────┘   │
│                                                                                 │
│   ┌─────────────────────────────────────────────────────────────────────────┐   │
│   │                      2. EIP + LoadBalancer                              │   │
│   │                                                                         │   │
│   │   Internet → EIP (dedicated IP) → OVN LB → Service → Pods/VMs           │   │
│   │                                                                         │   │
│   │   -  Dedicated IP address                                               │   │
│   │   -  Any TCP/UDP protocol                                               │   │
│   │   -  Direct VM access                                                   │   │
│   │                                                                         │   │
│   │   Annotations:                                                          │   │
│   │   • bind-on-default-gw-eip: "true"                                      │   │
│   │   • bind-on-eip: "my-eip"                                               │   │
│   └─────────────────────────────────────────────────────────────────────────┘   │
│                                                                                 │
│   ┌─────────────────────────────────────────────────────────────────────────┐   │
│   │                      3. Floating IP (FIP)                               │   │
│   │                                                                         │   │
│   │   Internet → EIP → 1:1 NAT → Internal IP (VM/Pod)                       │   │
│   │                                                                         │   │
│   │   -  Direct IP mapping                                                  │   │
│   │   -  VM sees public IP                                                  │   │
│   └─────────────────────────────────────────────────────────────────────────┘   │
└─────────────────────────────────────────────────────────────────────────────────┘
```

## Network Elements

### User-Visible Network Resources

#### External IP (EIP)

External IPs provide connectivity from the public internet to resources within Kube-DC. Each EIP is allocated from the provider network.

**Example EIP YAML:**

```yaml
apiVersion: kube-dc.com/v1
kind: EIp
metadata:
  name: ssh-arti
  namespace: shalb-demo
spec: {}  
```

#### Floating IP (FIP)

Floating IPs map an internal IP address (of a VM or pod) to an External IP, enabling direct access to specific resources.

**Example FIP YAML:**

```yaml
apiVersion: kube-dc.com/v1
kind: FIp
metadata:
  name: fedora-arti
  namespace: shalb-demo
spec:
  ipAddress: 10.0.10.171
  eip: ssh-arti
```

#### Kubernetes Service

Standard Kubernetes Services for in-cluster service discovery and load balancing.

#### Service Type LoadBalancer

Creates and maps an EIP to a service that routes traffic to pods or VMs. Can use either a dedicated EIP or the project's default EIP.

**Example Service LoadBalancer YAML with default gateway EIP:**

```yaml
apiVersion: v1
kind: Service
metadata:
  name: nginx-service-lb
  namespace: shalb-demo
  annotations:
    service.nlb.kube-dc.com/bind-on-default-gw-eip: "true"
spec:
  type: LoadBalancer
  selector:
    app: nginx
  ports:
    - name: http
      protocol: TCP
      port: 80
      targetPort: 80
    - name: https
      protocol: TCP
      port: 443
      targetPort: 443
```

**Example Service LoadBalancer for VM SSH access:**

```yaml
apiVersion: v1
kind: Service
metadata:
  name: vm-ssh
  namespace: shalb-demo
  annotations:
    service.nlb.kube-dc.com/bind-on-default-gw-eip: "true"
spec:
  type: LoadBalancer
  selector:
    vm.kubevirt.io/name: debian
  ports:
    - name: ssh
      protocol: TCP
      port: 2222
      targetPort: 22
```

### Internal Network Resources

#### DNAT Rule

Destination Network Address Translation rules proxy requests from the internet through an EIP to resources within the VPC network. These are created automatically when an EIP is associated with a resource.

#### SNAT

Source Network Address Translation is used for outbound connections from VPC subnets through EIPs, allowing resources within the VPC to communicate with the internet.

## Project Network Provisioning

When a new project is created in Kube-DC:

1. The project is allocated a dedicated subnet from the VPC CIDR range
2. Each project connected to the internet receives an EIP
3. All project outbound traffic is routed through its assigned EIP
4. Project-specific network policies are applied for isolation

**Example project creation with CIDR allocation:**

```yaml
apiVersion: kube-dc.com/v1
kind: Project
metadata:
  name: demo
  namespace: shalb
spec:
  cidrBlock: "10.0.10.0/24"
```

## Load Balancer Implementation

Kube-DC uses a specialized implementation for Service LoadBalancers:

- When a Service with type `LoadBalancer` is created, an OVS-based LoadBalancer routes traffic to service endpoints
- Endpoints can be Kubernetes pods or KubeVirt VMs
- The LoadBalancer can use either:
  - The project's default gateway EIP (with annotation `service.nlb.kube-dc.com/bind-on-default-gw-eip: "true"`)
  - A dedicated EIP (with annotation `service.nlb.kube-dc.com/bind-on-eip: "eip-name"`)

### Automatic External Endpoints (v0.1.34+)

Kube-DC automatically creates external endpoints for LoadBalancer services to enable cross-VPC communication.

When a LoadBalancer receives an external IP, the controller creates:
- **External Service** (`<service-name>-ext`): Headless service
- **Endpoints** (`<service-name>-ext`): Points to the LoadBalancer's external IP

This solves cross-VPC access by providing stable DNS names (e.g., `etcd-lb-ext.shalb-envoy.svc.cluster.local`) instead of hardcoded IPs. Endpoints are automatically updated when IPs change and deleted with the LoadBalancer service.

External endpoints are labeled with `kube-dc.com/managed-by: service-lb-controller`.

## Kube-OVN for VPC Management

Kube-OVN is a key component of Kube-DC's networking architecture, providing the foundation for multi-tenant network isolation through VPC networks.

### VPC Isolation

Different VPC networks are independent of each other and can be separately configured with:
- Subnet CIDRs
- Routing policies
- Security policies
- Outbound gateways
- EIP allocations

## Overlay vs. Underlay Networks

Kube-DC supports both networking approaches:

### Overlay Networks

- Software-defined networks that encapsulate packets
- Provide maximum flexibility for network segmentation
- Independent of physical network topology
- Managed entirely by Kube-OVN
- Ideal for multi-tenant environments

### Underlay Networks

- Direct mapping to physical network infrastructure
- Better performance with reduced encapsulation overhead
- Requires coordination with physical network infrastructure
- Physical switches handle data-plane forwarding
- Cannot be isolated by VPCs as they are managed by physical switches

## Network Security

Kube-DC implements multiple layers of network security:

**Project Isolation**

   - Each project receives its own subnet
   - Traffic between projects is controlled by network policies

**VPC Segmentation**

   - Projects can be placed in different VPCs for stricter isolation
   - Each VPC has its own network stack and routing tables

**Kubernetes Network Policies**

   - Fine-grained control over ingress and egress traffic
   - Can be applied at the namespace, pod, or service level

**Subnet ACLs**

   - Control traffic at the subnet level
   - Provide an additional layer of security beyond network policies

---

## Envoy Gateway

Envoy Gateway provides HTTP/HTTPS/gRPC routing with automatic TLS management.

### Architecture

```
┌──────────────────────────────────────────────────────────────────────────────┐
│                         ENVOY GATEWAY ARCHITECTURE                           │
│                                                                              │
│   ┌───────────────────────────────────────────────────────────────────────┐  │
│   │                    envoy-gateway-system namespace                     │  │
│   │                    (in ovn-default subnet: 10.100.0.0/16)             │  │
│   │                                                                       │  │
│   │   ┌────────────────────────┐     ┌────────────────────────────────┐   │  │
│   │   │  Envoy Gateway         │     │  Envoy Proxy Pod               │   │  │
│   │   │  Controller            │     │                                │   │  │
│   │   │                        │     │  Listens on:                   │   │  │
│   │   │  • Watches Gateway,    │     │  • :443 (HTTPS)                │   │  │
│   │   │    HTTPRoute, TLSRoute │     │  • :80 (HTTP)                  │   │  │
│   │   │  • Manages certs       │     │                                │   │  │
│   │   └────────────────────────┘     └────────────────────────────────┘   │  │
│   │                                                   │                   │  │
│   │   ┌───────────────────────────────────────────────┴───────────────┐   │  │
│   │   │  Gateway: eg                                                  │   │  │
│   │   │                                                               │   │  │
│   │   │  Listeners:                                                   │   │  │
│   │   │  • http (80)    - Redirect to HTTPS / ACME challenges         │   │  │
│   │   │  • https (443)  - Dynamic per-service listeners               │   │  │
│   │   │  • tls (443)    - TLS passthrough for Kubernetes API          │   │  │
│   │   └───────────────────────────────────────────────────────────────┘   │  │
│   └───────────────────────────────────────────────────────────────────────┘  │
│                                                                              │
│                                        │                                     │
│                                        │ externalIPs: 88.99.29.250           │
│                                        ▼                                     │
│   ┌───────────────────────────────────────────────────────────────────────┐  │
│   │                         TRAFFIC FLOW                                  │  │
│   │                                                                       │  │
│   │   Client (https://my-app.stage.kube-dc.com)                           │  │
│   │         │                                                             │  │
│   │         ▼                                                             │  │
│   │   DNS → 88.99.29.250 (Gateway external IP)                            │  │
│   │         │                                                             │  │
│   │         ▼                                                             │  │
│   │   Envoy Gateway (TLS termination with auto-cert)                      │  │
│   │         │                                                             │  │
│   │         ▼ HTTPRoute matches hostname                                  │  │
│   │   Backend Service (in customer namespace)                             │  │
│   └───────────────────────────────────────────────────────────────────────┘  │
└──────────────────────────────────────────────────────────────────────────────┘
```

### Gateway Route Flow

When a service with `expose-route: https` annotation is created:

```
┌─────────────────────────────────────────────────────────────────────────────┐
│                     GATEWAY ROUTE CREATION FLOW                             │
│                                                                             │
│   1. Service Created                                                        │
│      └─► annotations:                                                       │
│            service.nlb.kube-dc.com/expose-route: "https"                    │
│                                                                             │
│   2. Controller Creates:                                                    │
│      ├─► Certificate (cert-manager)                                         │
│      │     name: my-app-tls                                                 │
│      │     issuer: letsencrypt                                              │
│      │                                                                      │
│      ├─► Gateway Listener (patched into Gateway)                            │
│      │     name: https-my-app-namespace                                     │
│      │     port: 443                                                        │
│      │     hostname: my-app-namespace.stage.kube-dc.com                     │
│      │     certificateRef: my-app-tls                                       │
│      │                                                                      │
│      ├─► HTTPRoute                                                          │
│      │     parentRef: Gateway/eg (listener: https-my-app-namespace)         │
│      │     backendRef: Service/my-app                                       │
│      │                                                                      │
│      └─► ReferenceGrant (if cross-namespace)                                │
│                                                                             │
│   3. Status Updated:                                                        │
│      └─► route-hostname-status: my-app-namespace.stage.kube-dc.com          │
└─────────────────────────────────────────────────────────────────────────────┘
```

### Route Types

| Type | Port | TLS Handling | Use Case |
|------|------|--------------|----------|
| `http` | 80 | None | Plain HTTP traffic |
| `https` | 443 | Gateway terminates (auto-cert) | Web apps, APIs |
| `tls-passthrough` | 443 | App terminates | Kubernetes API, end-to-end encryption |

---

## Related Documentation

- [Service Exposure Guide](tutorial-service-exposure.md) - How to expose services
- [Virtual Machines](tutorial-virtual-machines.md) - VM networking
- [User & Group Management](tutorial-user-groups.md) - RBAC for network resources
