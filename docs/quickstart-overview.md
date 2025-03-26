# Kube-DC Installation Overview

This document provides a technical overview of the Kube-DC installation process, with detailed explanations of key configuration files and their parameters.

## Installation Methods

Kube-DC can be installed in several ways:

  - **Master-Worker deployment**: Recommended starting point for new deployments
  - **Multi-node HA cluster**: For production environments

> Start with actual tested deployment on [Hetzner Bare Metal Servers](quickstart-hetzner.md).

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

