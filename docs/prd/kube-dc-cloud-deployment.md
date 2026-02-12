# Kube-DC Cloud Production Deployment

## Overview

Production deployment of Kube-DC platform on 4 bare metal servers in Amsterdam datacenter.

**Domain**: `kube-dc.cloud`  
**Location**: Amsterdam (ams1)  
**Provider**: Bare metal servers

---

## Infrastructure

### Architecture

**3-Node HA Cluster** - All masters run workloads (no dedicated workers)

- Control plane distributed across 3 nodes for high availability
- Masters untainted to allow scheduling workloads
- etcd quorum maintained with 3 nodes

### Hosts

| Role | Hostname | IP | Interface |
|------|----------|-----|-----------|
| Master 1 (primary) | ams1-blade179-8 | 213.111.154.233 | enp94s0f0np0 |
| Master 2 | ams1-blade184-5 | 213.111.154.229 | enp94s0f0np0 |
| Master 3 | ams1-blade58-2 | 213.111.154.223 | enp8s0f0 |
| Worker | ams1-blade187-2 | 213.111.154.234 | eno1 |

### Network Requirements

- **Pod CIDR**: 10.100.0.0/16
- **Service CIDR**: 10.101.0.0/16
- **Join CIDR**: 172.30.0.0/22
- **Cloud Network**: VLAN 884, 100.65.0.0/16 (managed by Kube-OVN)
- **K8s Internal**: VLAN 885, 192.168.0.0/18

### K8s Internal Network (VLAN 885)

| Node | Public IP | Private IP (VLAN 885) |
|------|-----------|----------------------|
| Master 1 | 213.111.154.233 | 192.168.0.2 |
| Master 2 | 213.111.154.229 | 192.168.0.3 |
| Master 3 | 213.111.154.223 | 192.168.0.4 |
| Worker 1 | 213.111.154.234 | 192.168.0.10 |

> **Note**: VLAN 885 is used for Kubernetes internal communication (etcd, kubelet, etc.).
> VLAN 884 is reserved for Kube-OVN cloud network (do not use for K8s internal).

---

## Deployment Phases

### Phase 1: Host Preparation ✅
- [x] SSH key deployment
- [x] OS verification (Ubuntu 24.04.3 LTS on all 4 nodes)
- [x] Kernel upgraded to 6.8.0-90-generic on all nodes
- [x] Sysctl tuning (/etc/sysctl.d/99-kube-dc.conf on all nodes)
- [x] nf_conntrack module configured (max=1000000)
- [x] DNS configured on all nodes (systemd-resolved disabled, static /etc/resolv.conf)
- [x] System packages installed (unzip, iptables, curl, wget)

### Phase 2: Network Setup ✅
- [x] VLAN 885 configured for K8s internal traffic (192.168.0.0/18)
- [x] Netplan configured with VLAN 885 on all nodes
- [x] Inter-node connectivity verified on private network
- [ ] Configure DNS wildcard record (`*.kube-dc.cloud` → 213.111.154.233)
- [x] VLAN 884 reserved for Kube-OVN cloud network (100.65.0.0/16)

> **Note**: VLAN 885 managed by netplan, VLAN 884 auto-managed by Kube-OVN v1.15.0
> **Important**: DNS wildcard must be configured before cdev installation (required for ingress/certs)

### Phase 3: Kubernetes Installation ✅
- [x] RKE2 v1.35.0+rke2r1 installed on all nodes
- [x] Master 1 initialized (192.168.0.2)
- [x] Master 2 joined (192.168.0.3)
- [x] Master 3 joined (192.168.0.4)
- [x] Worker 1 joined (192.168.0.10)
- [x] HA cluster verified (3 control-plane + 1 worker)
- [x] Kubeconfig copied to bastion (~/.kube/cloud_kubeconfig)
- [ ] Untaint masters to allow workloads (after CNI install)

**Cluster uses private IPs (VLAN 885) for internal traffic, public IPs for external access.**

### Phase 4: Kube-DC Installation ⬜
- [ ] Create project.yaml configuration
- [ ] Create stack.yaml configuration
- [ ] Run `cdev apply`
- [ ] Verify all components healthy

### Phase 5: Post-Installation ⬜
- [ ] Verify Keycloak access
- [ ] Create initial organization
- [ ] Test VM deployment
- [ ] Test external network connectivity

---

## Configuration Files

### stack.yaml Template

```yaml
name: cluster
template: https://github.com/kube-dc/kube-dc-public//installer/kube-dc/templates/kube-dc?ref=main
kind: Stack
backend: default
variables:
  debug: "true"
  kubeconfig: /root/.kube/config

  cluster_config:
    pod_cidr: "10.100.0.0/16"
    svc_cidr: "10.101.0.0/16"
    join_cidr: "172.30.0.0/22"
    cluster_dns: "10.101.0.11"
    default_external_network:
      nodes_list:
        - kube-dc-master-1
        - kube-dc-master-2
        - kube-dc-master-3
        - kube-dc-worker-1
      name: ext-public
      vlan_id: "TBD"
      interface: "TBD"
      cidr: "TBD"
      gateway: "TBD"
      mtu: "1400"

  node_external_ip: 213.111.154.233
  email: "admin@kube-dc.cloud"
  domain: "kube-dc.cloud"
  install_terraform: true

  create_default:
    organization:
      name: default
      description: "Default Organization"
      email: "admin@kube-dc.cloud"
    project:
      name: demo
      cidr_block: "10.1.0.0/16"

  versions:
    kube_dc: "v0.1.35"
```

---

## Success Criteria

1. ✅ All 4 nodes joined to cluster (3 masters HA + 1 worker)
2. ✅ Kube-OVN networking operational
3. ✅ KubeVirt VMs can be created
4. ✅ External IPs assignable to workloads
5. ✅ UI accessible at `console.kube-dc.cloud`
6. ✅ Keycloak SSO working

---

## Dependencies

- External subnet allocation from provider
- VLAN configuration on physical switch
- DNS wildcard record: `*.kube-dc.cloud → 213.111.154.233`

---

## Reference

- [Quickstart Hetzner Guide](../quickstart-hetzner.md)
- [Architecture Overview](../architecture-overview.md)
- [Installer Design](../../installer/design.md)
