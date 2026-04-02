# Installation Guide

This guide walks you through deploying Kube-DC on three bare-metal servers from scratch. By the end, you will have a fully operational cloud platform with HA control plane, virtual machine support, managed Kubernetes clusters, and public IP networking.

**Time estimate:** ~60 minutes (excluding server provisioning).

:::info Prerequisites
Read the [Installation Overview](installation-overview.md) first to understand the reference architecture and network requirements.
:::

## Phase 1 — Server Preparation

### 1.1 Provision Servers

Provision three servers with **Ubuntu 24.04 LTS**. Throughout this guide, we use:

| Hostname | Role | Management IP |
|----------|------|---------------|
| `master-1` | Control plane + workloads | `192.168.0.1` |
| `master-2` | Control plane + workloads | `192.168.0.2` |
| `master-3` | Control plane + workloads | `192.168.0.3` |
| `bastion` (optional) | SSH jump host | `192.168.0.10` |

### 1.2 Configure Network Interfaces

Each server needs a static management IP and trunk access to the cloud and provider VLANs. The management network can be either the native (untagged) VLAN or a tagged VLAN — adapt the Netplan config below to match your switch configuration.

Create `/etc/netplan/60-kube-dc.yaml` on **each server** (adjust IPs and interface names):

```yaml
# /etc/netplan/60-kube-dc.yaml — master-1 example
network:
  version: 2
  renderer: networkd
  ethernets:
    # Management interface — carries node-to-node, API server, etcd traffic
    eth0:
      addresses:
        - 192.168.0.1/18          # Static IP on management network
      routes:
        - to: 0.0.0.0/0
          via: 192.168.0.254      # Management network gateway (internet access)
          on-link: true
          metric: 100
      nameservers:
        addresses: [8.8.8.8, 8.8.4.4]

    # Trunk interface — carries Cloud and Provider VLANs
    # Do NOT assign an IP here; Kube-OVN manages this interface via OVS bridges
    eth1:
      mtu: 9000                   # Jumbo frames recommended for cloud traffic
```

:::warning Important
- Replace `eth0` and `eth1` with your actual interface names (run `ip link` to check).
- Do **not** assign IPs to the trunk interface (`eth1`) — Kube-OVN will create OVS bridges and VLAN subinterfaces automatically.
- On `master-2` use `192.168.0.2`, on `master-3` use `192.168.0.3`.
:::

Back up the default netplan and apply:

```bash
sudo mkdir -p /root/netplan-backup
sudo cp /etc/netplan/*.yaml /root/netplan-backup/
sudo netplan apply
```

### 1.3 Update Hosts File

On **each server**, add all node entries:

```bash
cat <<EOF | sudo tee -a /etc/hosts
192.168.0.1  master-1
192.168.0.2  master-2
192.168.0.3  master-3
EOF
```

### 1.4 System Optimization

Run the following on **all three nodes**:

```bash
# Install required packages
sudo apt update && sudo apt -y upgrade
sudo apt -y install unzip iptables curl linux-headers-$(uname -r)

# Kernel parameters
cat <<EOF | sudo tee -a /etc/sysctl.conf
# Kube-DC requirements
fs.inotify.max_user_watches=1524288
fs.inotify.max_user_instances=4024
net.ipv4.ip_forward = 1
EOF
sudo sysctl -p

# Load conntrack module (required for kube-proxy)
sudo modprobe nf_conntrack
echo "nf_conntrack" | sudo tee -a /etc/modules

# Disable systemd-resolved to prevent DNS conflicts with CoreDNS
sudo systemctl stop systemd-resolved
sudo systemctl disable systemd-resolved
sudo rm -f /etc/resolv.conf
echo -e "nameserver 8.8.8.8\nnameserver 8.8.4.4" | sudo tee /etc/resolv.conf
```

---

## Phase 2 — RKE2 Cluster Bootstrap

### 2.1 Install RKE2 on master-1

SSH into `master-1` and install kubectl:

```bash
curl -LO "https://dl.k8s.io/release/$(curl -L -s https://dl.k8s.io/release/stable.txt)/bin/linux/amd64/kubectl"
chmod +x kubectl && sudo mv kubectl /usr/local/bin/
```

Create the RKE2 server configuration:

```bash
sudo mkdir -p /etc/rancher/rke2/

cat <<EOF | sudo tee /etc/rancher/rke2/config.yaml
node-name: master-1
disable-cloud-controller: true
disable: rke2-ingress-nginx              # Replaced by Envoy Gateway
cni: none                                # Replaced by Kube-OVN
cluster-cidr: "10.100.0.0/16"
service-cidr: "10.101.0.0/16"
cluster-dns: "10.101.0.11"
node-label:
  - kube-dc-manager=true
  - kube-ovn/role=master
kube-apiserver-arg:
  - authentication-config=/etc/rancher/auth-conf.yaml
node-ip: 192.168.0.1                     # Management network IP
advertise-address: 192.168.0.1
tls-san:
  - kube-api.example.com                 # Your API server domain
  - 192.168.0.1
  - 192.168.0.2
  - 192.168.0.3
EOF
```

Create the authentication configuration placeholder (Kube-DC will configure OIDC later):

```bash
cat <<EOF | sudo tee /etc/rancher/auth-conf.yaml
apiVersion: apiserver.config.k8s.io/v1beta1
kind: AuthenticationConfiguration
jwt: []
EOF
sudo chmod 666 /etc/rancher/auth-conf.yaml
```

Install and start RKE2:

```bash
export INSTALL_RKE2_VERSION="v1.35.0+rke2r1"
export INSTALL_RKE2_TYPE="server"
curl -sfL https://get.rke2.io | sh -
sudo systemctl enable rke2-server.service
sudo systemctl start rke2-server.service
```

Monitor startup (wait until the node is `Ready`):

```bash
sudo journalctl -u rke2-server -f
```

Configure kubectl:

```bash
mkdir -p ~/.kube
sudo cp /etc/rancher/rke2/rke2.yaml ~/.kube/config
sudo chown $(id -u):$(id -g) ~/.kube/config
chmod 600 ~/.kube/config
```

Verify:

```bash
kubectl get nodes
# NAME       STATUS     ROLES                       AGE   VERSION
# master-1   NotReady   control-plane,etcd,master   1m    v1.35.0+rke2r1
```

The node will show `NotReady` until a CNI is installed — this is expected.

### 2.2 Get the Join Token

On `master-1`, retrieve the join token:

```bash
sudo cat /var/lib/rancher/rke2/server/node-token
```

Save this token — you need it for `master-2` and `master-3`.

### 2.3 Join master-2 and master-3

On **each additional node** (`master-2`, `master-3`), create the RKE2 config and join:

```bash
sudo mkdir -p /etc/rancher/rke2/

cat <<EOF | sudo tee /etc/rancher/rke2/config.yaml
token: <TOKEN_FROM_MASTER_1>
server: https://192.168.0.1:9345         # master-1 management IP
node-name: master-2                      # Use master-3 on the third node
disable-cloud-controller: true
disable: rke2-ingress-nginx
cni: none
node-label:
  - kube-dc-manager=true
  - kube-ovn/role=master
kube-apiserver-arg:
  - authentication-config=/etc/rancher/auth-conf.yaml
node-ip: 192.168.0.2                     # This node's management IP
advertise-address: 192.168.0.2
tls-san:
  - kube-api.example.com
  - 192.168.0.1
  - 192.168.0.2
  - 192.168.0.3
EOF

# Create auth config placeholder
cat <<EOF | sudo tee /etc/rancher/auth-conf.yaml
apiVersion: apiserver.config.k8s.io/v1beta1
kind: AuthenticationConfiguration
jwt: []
EOF
sudo chmod 666 /etc/rancher/auth-conf.yaml

# Install and start
export INSTALL_RKE2_VERSION="v1.35.0+rke2r1"
export INSTALL_RKE2_TYPE="server"
curl -sfL https://get.rke2.io | sh -
sudo systemctl enable rke2-server.service
sudo systemctl start rke2-server.service
```

### 2.4 Verify the HA Cluster

Back on `master-1`:

```bash
kubectl get nodes
# NAME       STATUS     ROLES                       AGE   VERSION
# master-1   NotReady   control-plane,etcd,master   10m   v1.35.0+rke2r1
# master-2   NotReady   control-plane,etcd,master   3m    v1.35.0+rke2r1
# master-3   NotReady   control-plane,etcd,master   1m    v1.35.0+rke2r1
```

All three nodes should appear with the `control-plane,etcd,master` roles. `NotReady` status is expected until the CNI is deployed in Phase 3.

---

## Phase 3 — Deploy Kube-DC

### 3.1 Install Cluster.dev CLI

On `master-1` (or your bastion host):

```bash
curl -fsSL https://raw.githubusercontent.com/shalb/cluster.dev/master/scripts/get_cdev.sh | sh
```

Verify:

```bash
cdev --version
```

### 3.2 Clone the Installer Template

Create a project directory and set up the installer:

```bash
mkdir -p ~/kube-dc-install
cd ~/kube-dc-install
```

Create the Cluster.dev project file:

```yaml
# ~/kube-dc-install/project.yaml
name: kube-dc
kind: Project
backend: default
variables:
  organization: myorg
exports:
  CDEV_COLLECT_USAGE_STATS: false
```

### 3.3 Configure stack.yaml

Create the stack configuration. This is the main configuration file — customize every value marked with a comment:

```yaml
# ~/kube-dc-install/stack.yaml
name: cluster
template: https://github.com/kube-dc/kube-dc-public//installer/kube-dc/templates/kube-dc?ref=main
kind: Stack
backend: default
variables:
  debug: "true"
  kubeconfig: /root/.kube/config          # Path to your RKE2 kubeconfig

  cluster_config:
    keycloak_hostname: "login.example.com" # Keycloak SSO domain
    default_gw_network_type: "cloud"
    default_eip_network_type: "cloud"
    default_fip_network_type: "cloud"
    default_svc_lb_network_type: "cloud"
    POD_CIDR: "10.100.0.0/16"             # Must match RKE2 cluster-cidr
    POD_GATEWAY: "10.100.0.1"
    SVC_CIDR: "10.101.0.0/16"             # Must match RKE2 service-cidr
    JOIN_CIDR: "172.30.0.0/22"
    cluster_dns: "10.101.0.11"            # Must match RKE2 cluster-dns
    default_external_network:
      type: cloud
      nodes_list:                          # All nodes with the trunk interface
        - master-1
        - master-2
        - master-3
      name: ext-cloud                     # Kube-OVN ProviderNetwork name
      vlan_id: "200"                      # Cloud VLAN ID on your switch
      interface: "eth1"                   # Trunk interface name
      cidr: "10.64.0.0/16"               # Cloud network CIDR
      gateway: "10.64.0.1"
      mtu: "1400"
      exclude_ips:
        - "10.64.0.1..10.64.0.100"       # Reserve first 100 IPs for infrastructure

  node_external_ip: 203.0.113.10          # A public IP of master-1 (for initial DNS)

  email: "admin@example.com"
  domain: "example.com"                   # Your wildcard domain (*.example.com)
  install_terraform: true
  kubeApiExternalUrl: "https://kube-api.example.com:6443"

  create_default:
    enabled: true                          # Create a demo organization on first run
    organization:
      name: demo-org
      description: "Demo Organization"
      email: "admin@example.com"
    project:
      name: demo
      cidr_block: "10.0.10.0/24"
      egress_network_type: cloud

  monitoring:
    prom_storage: 50Gi
    retention_size: 47GiB
    retention: 365d

  versions:
    kube_dc: "v0.1.35"                    # Kube-DC release version
```

**Key values to customize:**

| Variable | Description |
|----------|-------------|
| `kubeconfig` | Absolute path to your RKE2 kubeconfig |
| `domain` | Your domain — wildcard `*.example.com` must resolve to your public IP |
| `cluster_config.keycloak_hostname` | FQDN for Keycloak (e.g., `login.example.com`) |
| `default_external_network.interface` | Trunk NIC name (same on all nodes, or patch later) |
| `default_external_network.vlan_id` | Cloud VLAN ID configured on your switch |
| `default_external_network.cidr` | Cloud network CIDR (large private range) |
| `node_external_ip` | Public IP of `master-1` for initial wildcard DNS |
| `versions.kube_dc` | Target Kube-DC release |

### 3.4 Deploy

```bash
cd ~/kube-dc-install
cdev apply
```

The deployment takes **15–20 minutes**. On completion, you will see output like:

```
console_url = https://console.example.com
keycloak_url = https://login.example.com
keycloak_user = admin
keycloak_password = <generated>
organization_name = demo-org
project_name = demo
organization_admin_username = admin
retrieve_organization_password = kubectl get secret realm-access -n demo-org -o jsonpath='{.data.password}' | base64 -d
```

Save these credentials.

---

## Phase 4 — Post-Deployment Configuration

### 4.1 Deploy MetalLB for HA Ingress

MetalLB provides a **floating public IP** for Envoy Gateway that automatically fails over between the three control-plane nodes.

```bash
# Install MetalLB
helm repo add metallb https://metallb.github.io/metallb
helm repo update
helm install metallb metallb/metallb \
  --namespace metallb-system \
  --create-namespace \
  --set loadBalancerClass=metallb \
  --wait
```

Create the IP pool and L2 advertisement:

```bash
cat <<'EOF' | kubectl apply -f -
apiVersion: metallb.io/v1beta1
kind: IPAddressPool
metadata:
  name: envoy-gateway-pool
  namespace: metallb-system
spec:
  addresses:
    - 203.0.113.20/32                     # Your dedicated floating public IP
  autoAssign: false
---
apiVersion: metallb.io/v1beta1
kind: L2Advertisement
metadata:
  name: envoy-gateway-l2
  namespace: metallb-system
spec:
  ipAddressPools:
    - envoy-gateway-pool
  interfaces:
    - br-ext-cloud                        # Kube-OVN provider bridge
EOF
```

Update the Envoy Gateway service to use MetalLB:

```bash
cat <<'EOF' | kubectl replace -f -
apiVersion: gateway.envoyproxy.io/v1alpha1
kind: EnvoyProxy
metadata:
  name: custom-proxy-config
  namespace: envoy-gateway-system
spec:
  logging:
    level:
      default: warn
  provider:
    type: Kubernetes
    kubernetes:
      envoyService:
        externalTrafficPolicy: Cluster
        loadBalancerClass: metallb
        patch:
          type: StrategicMerge
          value:
            metadata:
              annotations:
                metallb.universe.tf/loadBalancerIPs: "203.0.113.20"
EOF
```

Delete the existing Envoy service so it is recreated with the new config (`loadBalancerClass` is immutable):

```bash
kubectl delete svc -n envoy-gateway-system -l gateway.envoyproxy.io/owning-gateway-name
```

Verify:

```bash
# Check MetalLB speakers are running
kubectl get pods -n metallb-system

# Check the floating IP is assigned
kubectl get svc -n envoy-gateway-system -o wide

# Check speaker announcement
kubectl logs -n metallb-system -l app.kubernetes.io/component=speaker --tail=20 | grep serviceAnnounced
```

:::warning loadBalancerClass Isolation
The `loadBalancerClass: metallb` setting is **critical**. Without it, MetalLB will attempt to handle all `LoadBalancer` services in the cluster, conflicting with the Kube-DC LoadBalancer controller that manages project service IPs. This isolation ensures MetalLB only manages the Envoy Gateway floating IP.
:::

### 4.2 Configure Provider Network (Custom NIC Names)

If your nodes have **different NIC names** for the trunk interface, you need to patch the ProviderNetwork with custom interface mappings.

Create `provider-network-patch.yaml`:

```yaml
apiVersion: kubeovn.io/v1
kind: ProviderNetwork
metadata:
  name: ext-cloud
spec:
  defaultInterface: eth1                  # Default for most nodes
  customInterfaces:
    - interface: eno1                     # Override for nodes with different NIC names
      nodes:
        - master-3
  autoCreateVlanSubinterfaces: true       # Auto-create VLAN subinterfaces
  preserveVlanInterfaces: true            # Migrate existing VLAN configs to OVS
```

Apply:

```bash
kubectl apply -f provider-network-patch.yaml
```

Verify all nodes are ready:

```bash
kubectl get provider-networks ext-cloud -o jsonpath='{.status.readyNodes}' | jq .
```

### 4.3 Configure External Networks

The base installer creates the cloud network. If you need a **provider (public) network** for dedicated public IPs, create additional Kube-OVN resources.

Create `external-networks.yaml`:

```yaml
---
# Cloud network VLAN and Subnet (created by installer, shown for reference)
apiVersion: kubeovn.io/v1
kind: Vlan
metadata:
  name: vlan200
spec:
  id: 200                                # Your cloud VLAN ID
  provider: ext-cloud
---
apiVersion: kubeovn.io/v1
kind: Subnet
metadata:
  name: ext-cloud
  labels:
    network.kube-dc.com/external-network-type: cloud
    network.kube-dc.com/default-external: "true"
spec:
  protocol: IPv4
  cidrBlock: 10.64.0.0/16
  gateway: 10.64.0.1
  vlan: vlan200
  mtu: 1400
  gatewayType: distributed
  natOutgoing: false
  private: false
  enableLb: true
  excludeIps:
    - 10.64.0.1..10.64.0.100

---
# Provider (public) network — additional VLAN on same ProviderNetwork
apiVersion: kubeovn.io/v1
kind: Vlan
metadata:
  name: vlan300
spec:
  id: 300                                # Your provider VLAN ID
  provider: ext-cloud
---
apiVersion: kubeovn.io/v1
kind: Subnet
metadata:
  name: ext-public
  labels:
    network.kube-dc.com/external-network-type: public
spec:
  protocol: IPv4
  cidrBlock: 203.0.113.0/24              # Your public IPv4 subnet
  gateway: 203.0.113.1
  vlan: vlan300
  mtu: 1400
  gatewayType: distributed
  natOutgoing: false
  private: false
  enableLb: true
  excludeIps:
    - 203.0.113.1..203.0.113.3           # Reserved for infrastructure
    - 203.0.113.254
```

Apply the provider network (the cloud network is already created by the installer):

```bash
kubectl apply -f external-networks.yaml
```

Verify:

```bash
kubectl get vlan
kubectl get subnet ext-cloud ext-public
kubectl get provider-networks ext-cloud -o jsonpath='{.status.vlans}'
# Expected: ["vlan200","vlan300"]
```

### 4.4 Configure DNS

Point a wildcard DNS record to the MetalLB floating IP:

```
*.example.com  →  203.0.113.20  (A record)
```

This enables automatic HTTPS routing for:
- `console.example.com` — Kube-DC web console
- `login.example.com` — Keycloak SSO
- `grafana.example.com` — Grafana dashboards
- `*.example.com` — Tenant cluster API endpoints

---

## Phase 5 — Verify Installation

### Check All Components

```bash
# All nodes should be Ready
kubectl get nodes

# Core namespaces should have all pods Running
kubectl get pods -n kube-system          # Kube-OVN, CoreDNS
kubectl get pods -n kube-dc              # Kube-DC controllers
kubectl get pods -n keycloak             # Keycloak
kubectl get pods -n envoy-gateway-system # Envoy Gateway
kubectl get pods -n kubevirt             # KubeVirt
kubectl get pods -n monitoring           # Prometheus, Grafana, Loki
kubectl get pods -n kamaji-system        # Kamaji
```

### Access the Web Console

Open `https://console.example.com` in your browser.

Retrieve the demo organization admin password:

```bash
kubectl get secret realm-access -n demo-org -o jsonpath='{.data.password}' | base64 -d
```

Log in with:
- **Username:** `admin`
- **Password:** _(output from above)_

### Test External Connectivity

```bash
# Envoy Gateway should respond on the floating IP
curl -v https://console.example.com

# Check MetalLB floating IP
curl -v http://203.0.113.20
```

---

## Optional Add-ons

### Rook Ceph Object Storage (S3)

For S3-compatible object storage, see [Deploying Rook Ceph Object Storage](deploy-rook-ceph-object-storage.md).

### SSO with Google OAuth

To enable Google OAuth login, see [SSO with Google Auth](sso-google-auth.md).

### Worker Node Scaling with Metal3

Additional worker nodes can be added to the management cluster in two ways:

**Manual addition** — Install RKE2 agent on a new server and join it to the cluster:

```bash
# On the new worker node
sudo mkdir -p /etc/rancher/rke2/
cat <<EOF | sudo tee /etc/rancher/rke2/config.yaml
token: <TOKEN_FROM_MASTER_1>
server: https://192.168.0.1:9345
node-name: worker-1
node-ip: 192.168.0.11
EOF

export INSTALL_RKE2_VERSION="v1.35.0+rke2r1"
export INSTALL_RKE2_TYPE="agent"
curl -sfL https://get.rke2.io | sh -
sudo systemctl enable rke2-agent.service
sudo systemctl start rke2-agent.service
```

**Automated provisioning with Metal3** — Metal3 uses the Cluster API bare-metal provider to PXE-boot and provision new servers automatically. This is ideal for large-scale deployments where servers are managed via IPMI/BMC. Metal3 handles:

- Hardware discovery and inventory via Ironic
- PXE boot and OS provisioning
- Automatic Kubernetes node joining
- Lifecycle management (scale up/down, OS upgrades)

:::note
Metal3 integration documentation is coming soon. For early access, contact [community support](/cloud/community-support).
:::

---

## Troubleshooting

### RKE2 Nodes Not Joining

```bash
# Check RKE2 logs on the joining node
sudo journalctl -u rke2-server -f   # For server nodes
sudo journalctl -u rke2-agent -f    # For worker nodes

# Verify connectivity to master-1
ping 192.168.0.1
curl -k https://192.168.0.1:9345/v1-rke2/readyz
```

### Kube-OVN Pods Not Starting

```bash
kubectl get pods -n kube-system -l app=kube-ovn-controller
kubectl logs -n kube-system -l app=kube-ovn-controller --tail=50
```

Common issue: Nodes have different NIC names. Apply a ProviderNetwork patch (see [Phase 4.2](#42-configure-provider-network-custom-nic-names)).

### MetalLB Not Announcing IP

```bash
kubectl get pods -n metallb-system
kubectl logs -n metallb-system -l app.kubernetes.io/component=speaker --tail=50

# Ensure loadBalancerClass is set correctly
kubectl get svc -n envoy-gateway-system -o yaml | grep loadBalancerClass
```

### Envoy Gateway Not Responding

```bash
kubectl get svc -n envoy-gateway-system
kubectl get gateway -A
kubectl logs -n envoy-gateway-system -l control-plane=envoy-gateway --tail=50
```

### cdev apply Fails

```bash
# Re-run with debug output
cdev apply --log-level debug

# Check specific unit status
cdev output
```

For additional help, consult the [Community & Support](/cloud/community-support) page.
