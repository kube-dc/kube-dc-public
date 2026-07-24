# How Networking Works

This page explains Kube-DC networking concepts — how your project connects to the internet, how traffic flows, and which tools are available to expose your services.

## Key Concepts

| Resource | What It Does |
|----------|-------------|
| **VPC** | Isolated virtual network for your project — all VMs and pods get private IPs here |
| **Subnet** | IP range within your VPC (e.g., `10.0.0.0/24`) — auto-assigned when the project is created |
| **EIP** (External IP) | Public or cloud IP address that can be bound to a LoadBalancer service |
| **FIP** (Floating IP) | 1:1 NAT mapping between an external IP and a specific VM or pod |
| **LoadBalancer** | Kubernetes Service that routes external traffic to pods or VMs via an EIP |
| **Gateway Route** | HTTPS/HTTP route through the shared Envoy Gateway — auto TLS certificates |

---

## Project Network Types

When a project is created, it gets one of two network types:

### Cloud Network (`egressNetworkType: cloud`)

- Default EIP from a **shared NAT pool** (not internet-routable directly)
- Outbound traffic goes through a shared gateway
- **Gateway Routes** provide easy HTTPS with auto-certificates
- Can still create **public EIPs** when direct access is needed
- **Best for**: Web apps, APIs, microservices — cost-effective

### Public Network (`egressNetworkType: public`)

- Default EIP is a **dedicated public IP** (internet-routable)
- Direct internet connectivity without NAT
- Any TCP/UDP protocol supported
- **Best for**: VMs with direct SSH, game servers, custom protocols

### Comparison

| Feature | Cloud | Public |
|---------|-------|--------|
| **Default EIP** | Shared NAT pool | Dedicated public IP |
| **Gateway Routes (HTTPS)** | ✅ Yes | ✅ Yes |
| **EIP + LoadBalancer** | ✅ Yes | ✅ Yes |
| **Floating IPs** | ✅ Yes | ✅ Yes |
| **VMs and Pods** | ✅ Yes | ✅ Yes |
| **Cost** | Lower | Higher |

---

## How Traffic Flows

### Outbound (VM/Pod → Internet)

```
VM/Pod (10.0.0.x)  →  Project Router  →  SNAT via EIP  →  Internet
```

Every project has a default EIP that handles outbound NAT. All VMs and pods can reach the internet automatically.

### Inbound via Gateway Route (HTTPS)

```
Client  →  DNS (*.kube-dc.cloud)  →  Envoy Gateway (shared IP, port 443)
        →  TLS termination (auto Let's Encrypt cert)
        →  HTTPRoute matches hostname
        →  Backend Service  →  Pod
```

One shared Envoy Gateway handles all HTTPS traffic. Each service gets a unique hostname like `my-app-my-project.kube-dc.cloud`.

### Inbound via EIP + LoadBalancer

```
Client  →  EIP (dedicated IP, any port)  →  OVN LoadBalancer  →  Pod/VM
```

The EIP is bound to a LoadBalancer service. Supports any TCP/UDP protocol on any port.

### Inbound via Floating IP

```
Client  →  External IP  →  1:1 NAT  →  VM internal IP (all ports)
```

A FIP maps all ports from an external IP directly to a VM. The VM is fully accessible as if it had a public IP.

---

## Network MTU (1400)

Kube-DC project networks use an encapsulated overlay, so the usable MTU inside a project is **1400 bytes**, not the 1500 you may be used to. Your VMs and pods are configured with this automatically — a VM's interface picks up 1400 over DHCP, and pods get it from the network plugin. You normally never need to think about it.

**The exception is software that assumes 1500 and doesn't inherit the MTU from its host — most commonly Docker.** If you install Docker inside a VM, its default bridge is created with an MTU of 1500. Containers on that bridge then send packets too large for the 1400 network, and those packets are silently dropped.

This produces a distinctive failure: **small requests succeed, large transfers hang and then fail.**

```
# Succeeds — a small response fits
RUN curl -v https://github.com/example/repo

# Fails — bulk data does not
RUN git clone --depth 1 https://github.com/example/repo /src
  error: RPC failed; curl 56 Recv failure: Connection reset by peer
  fatal: expected flush after ref listing
```

It looks like a broken network, but connectivity is fine — only the oversized packets are lost. `docker pull`, `apt-get`, `npm install` and similar can fail the same way.

### Which side is at fault?

Run the failing command **directly on the VM**, outside Docker:

| Result | Cause | Fix |
|--------|-------|-----|
| Works on the VM, fails in Docker | Docker's container MTU | Set Docker's MTU (below) |
| Fails on the VM too | The VM's own interface | Check `ip link show` — it should report `mtu 1400` |

### Fix: set Docker's MTU

Create or edit `/etc/docker/daemon.json` inside the VM:

```json
{
  "mtu": 1400
}
```

Then restart Docker:

```bash
sudo systemctl restart docker
```

Verify a container now gets the right MTU:

```bash
# Should print 1400. Before the fix it prints 1500.
docker run --rm alpine ip link show eth0

# The VM itself should already show 1400
ip link show
```

If you build with BuildKit or `docker buildx`, recreate the builder after changing the daemon config so it picks up the new MTU — or build with `--network=host` so build steps use the VM's interface directly.

Other container runtimes take the same setting: Podman uses `mtu` in its network config, and standalone containerd/CNI sets it in the bridge plugin config.

## Managing Networking via UI

The Console UI provides a **Networking** section with three tabs for managing network resources:

<img src={require('./images/network-mgmt.png').default} alt="Network Management UI" style={{maxWidth: '900px', width: '100%'}} />

- **External IPs** — view and create EIPs, see network type (Cloud/Public), ownership, and status
- **Floating IPs** — manage FIP-to-VM mappings
- **Load Balancers** — view LoadBalancer services and their endpoints

Use the **+ Create External IP** button to allocate a new EIP for your project.

---

## Which Method Should I Use?

```
What are you exposing?
│
├── Web app or API?
│   └── Use Gateway Route (expose-route: https)
│       → Auto TLS, auto hostname, shared infrastructure
│
├── VM with direct SSH/RDP access?
│   └── Use Floating IP (FIP)
│       → Dedicated IP, all ports, 1:1 NAT
│
├── Custom TCP/UDP service?
│   └── Use EIP + LoadBalancer
│       → Dedicated IP, any protocol, specific ports
│
└── Multiple services on one IP?
    └── Use default gateway EIP + LoadBalancer
        → Shared IP, different ports per service
```

| Method | Protocols | TLS | IP Type | Best For |
|--------|-----------|-----|---------|----------|
| **Gateway Route** | HTTP, HTTPS, gRPC | Auto (Let's Encrypt) | Shared | Web apps, APIs |
| **Floating IP** | All TCP/UDP (all ports) | None | Dedicated | VM direct access |
| **EIP + LoadBalancer** | Any TCP/UDP | Application handles | Dedicated or shared | Custom services |

---

## Next Steps

- [External & Floating IPs](public-floating-ips.md) — Create and manage EIPs and FIPs
- [VPC & Private Networking](private-networking.md) — Understand project isolation and subnets
- [Service Exposure Guide](service-exposure.md) — Complete reference for all exposure methods with examples
