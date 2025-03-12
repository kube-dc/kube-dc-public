# Kube-DC Installation Overview

This document provides a technical overview of the Kube-DC installation process, with detailed explanations of key configuration files and their parameters.

## Installation Methods

Kube-DC can be installed in several ways:

  - **Master-Worker deployment**: Recommended starting point for new deployments
  - **Multi-node HA cluster**: For production environments
  - **On top of existing Kubernetes**: For extending an existing cluster

## Prerequisites

Before installing Kube-DC, ensure your system meets the following requirements:

- **Hardware**: Minimum 4 CPU cores, 8GB RAM per node
- **Operating System**: Ubuntu 20.04 LTS or newer (24.04 LTS recommended)
- **Network**: Dedicated network interface for VM traffic with VLAN support
- **Storage**: Local or network storage with support for dynamic provisioning
- **Kubernetes**: Version 1.31+ if installing on existing cluster

## Network Configuration

Kube-DC requires proper network configuration for optimal performance. The key requirement is that your external network must be routed through a VLAN to enable advanced networking features.

### External Network Requirements

Kube-DC networking is built on top of Kube-OVN and requires the following network configuration:

- **VLAN-capable network interface**: A dedicated network interface with VLAN support
- **External subnet with routing**: An external subnet that's properly routed to your infrastructure
- **Static IP configuration**: Static IP addressing (no DHCP) to ensure network stability

This configuration allows Kube-DC to implement:

- **Floating IP allocation**: Dynamically assign public IPs to workloads
- **Load balancer with external IPs**: Distribute traffic to services with public visibility
- **Default gateway per project**: Isolate network traffic between projects

All of these features work as a wrapper on top of Kube-OVN, providing enterprise-grade networking capabilities for your infrastructure.

### Example Network Configuration

Below is an example Netplan configuration with detailed comments for a VLAN-enabled network:

```yaml
network:
  version: 2  # Netplan version
  renderer: networkd  # Network renderer to use
  ethernets:
    eth0:  # Primary network interface name (check your actual interface name)
      addresses:
        - 192.168.1.2/24  # Primary IP address and subnet mask
      routes:
        - to: 0.0.0.0/0  # Default route for all traffic
          via: 192.168.1.1  # Gateway IP address
          on-link: true  # Indicates the gateway is directly reachable
          metric: 100  # Route priority (lower = higher priority)
      nameservers:
        addresses:
          - 8.8.8.8  # Primary DNS server (Google)
          - 8.8.4.4  # Secondary DNS server (Google)
  vlans:
    eth0.100:  # VLAN interface (format: interface.vlan_id)
      id: 100  # VLAN ID
      link: eth0  # Parent interface for VLAN 
      mtu: 1500  # Recommended MTU for your network
      addresses:
        - 10.100.0.2/24  # Private IP on the VLAN network
```

!!! note "Important"
    Do not use DHCP for the VLAN interface as it would break the initial Kube-OVN setup. Always use static IP configuration.

## Networking Components

The Kube-DC network setup consists of several key components that work together:

1. **Kube-OVN**: Core CNI providing overlay and underlay networking
2. **Multus CNI**: Enables multiple network interfaces for pods
3. **VLAN Integration**: Connects Kubernetes networking to physical infrastructure

### External Network Configuration

Kube-OVN is configured with the following settings to enable external connectivity through VLAN routing:

```yaml
# Subnet configuration for external connectivity
apiVersion: kubeovn.io/v1
kind: Subnet
metadata:
  name: external-network  # Network name
  labels:
    network.kube-dc.com/allow-projects: "all"  # Allow all projects to use this network
spec:
  protocol: IPv4  # IP protocol version
  cidrBlock: 203.0.113.0/24  # Your allocated external subnet
  gateway: 203.0.113.1  # Gateway IP (first IP in your external subnet)
  vlan: vlan100  # VLAN ID reference matching your network configuration
  mtu: 1500  # MTU size optimized for your network
```

```yaml
# VLAN configuration
apiVersion: kubeovn.io/v1
kind: Vlan
metadata:
  name: vlan4012  # VLAN name reference
spec:
  id: 4012  # VLAN ID matching your Hetzner vSwitch ID
  provider: external-network  # Provider name
```

## System Optimization

For optimal performance, several system settings should be configured:

### System Control Parameters

Add the following to `/etc/sysctl.conf`:

```bash
# Disable IPv6 to prevent issues with dual-stack networks
net.ipv6.conf.all.disable_ipv6 = 1
net.ipv6.conf.default.disable_ipv6 = 1
net.ipv6.conf.lo.disable_ipv6 = 1

# Increase inotify limits for Kubernetes workloads
fs.inotify.max_user_watches=1524288  # Number of watches per user
fs.inotify.max_user_instances=4024   # Number of watch instances per user
```

### DNS Configuration

Disable `systemd-resolved` to prevent conflicts with container DNS:

```bash
systemctl stop systemd-resolved
systemctl disable systemd-resolved
rm /etc/resolv.conf
echo "nameserver 8.8.8.8" > /etc/resolv.conf
echo "nameserver 8.8.4.4" >> /etc/resolv.conf
```

## RKE2 Configuration

RKE2 (Rancher Kubernetes Engine 2) is the preferred Kubernetes distribution for Kube-DC. The configuration differs depending on the node role:

### Initial Master Node Configuration

Create `/etc/rancher/rke2/config.yaml` with the following parameters:

```yaml
node-name: kube-dc-master-1  # Unique node name
disable-cloud-controller: true  # Disable default cloud controller as Kube-DC uses its own
disable: rke2-ingress-nginx  # Disable default ingress as Kube-DC provides its own
cni: none  # Disable default CNI as Kube-DC uses Kube-OVN
cluster-cidr: "10.100.0.0/16"  # Pod network CIDR range
service-cidr: "10.101.0.0/16"  # Service network CIDR range
cluster-dns: "10.101.0.11"  # Cluster DNS service IP
node-label:  # Node labels used by components to identify master nodes
  - kube-dc-manager=true  # Identifies this node for management components
  - kube-ovn/role=master  # Identifies this node for Kube-OVN control plane
kube-apiserver-arg:  # Additional API server arguments
  - authentication-config=/etc/rancher/auth-conf.yaml  # Path to auth config for custom authentication
debug: true  # Enable debug logging
node-external-ip: 138.201.253.201  # External IP for this node
tls-san:  # Subject Alternative Names for TLS certificates
  - kube-api.dev.kube-dc.com  # DNS name for API server
  - 138.201.253.201  # IP address for API server
advertise-address: 138.201.253.201  # IP address to advertise for API server
node-ip: 192.168.100.2  # Internal cluster IP address
```

### Authentication Configuration

Create `/etc/rancher/auth-conf.yaml` to configure Kubernetes API authentication:

```yaml
apiVersion: apiserver.config.k8s.io/v1beta1
kind: AuthenticationConfiguration
jwt: []  # Empty JWT configuration allows for extending authentication later
```

## Cluster.dev Installation

Kube-DC uses [Cluster.dev](https://docs.cluster.dev/) as the deployment tool for installing and managing components. Install it with:

```bash
curl -fsSL https://raw.githubusercontent.com/shalb/cluster.dev/master/scripts/get_cdev.sh | sh
```

## Core Components

The Kube-DC installer deploys the following core components:

1. **Kube-OVN**: Advanced networking solution that provides overlay and underlay networking
2. **Multus CNI**: CNI that enables attaching multiple network interfaces to pods
3. **KubeVirt**: Virtualization layer for running VMs on Kubernetes
4. **Keycloak**: Identity and access management solution
5. **Cert-Manager**: Certificate management for TLS
6. **Ingress-NGINX**: Ingress controller for external access
7. **Prometheus & Loki**: Monitoring and logging stack
8. **Kube-DC Core**: The core management components for Kube-DC

## Installation Process Overview

The installation process follows these high-level steps:

1. **System Preparation**: Configure network, optimize system settings, and install prerequisites
2. **Kubernetes Installation**: Install RKE2 on master and worker nodes
3. **Kube-DC Installation**: Use cluster.dev to deploy Kube-DC components
4. **Post-Installation Setup**: Configure authentication, networking, and initial organization

For detailed step-by-step instructions, refer to:
- [Master-Worker Setup (Dedicated Servers)](quickstart-hetzner.md)
- [Minimal HA Setup (Bare Metal)](quickstart-bare-metal.md)
- [Installing on Existing K8s](quickstart-existing-k8s.md)

## Version Compatibility

The main components of Kube-DC have the following version compatibility:

| Component | Minimum Version | Recommended Version |
|-----------|-----------------|---------------------|
| Kubernetes | 1.31.0 | 1.32.1 |
| KubeVirt | 1.2.0 | 1.3.0 |
| Kube-OVN | 1.12.0 | 1.13.2 |
| Keycloak | 24.0.0 | 24.3.0 |
