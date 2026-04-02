# Metal3 Bare-Metal Worker Nodes

This guide covers deploying and managing bare-metal worker nodes for Kube-DC using [Metal3](https://metal3.io/) — the Cluster API infrastructure provider for bare metal. Metal3 automates server discovery, OS provisioning, Kubernetes node joining, and lifecycle management through standard CAPI resources.

## Architecture

```
┌─────────────────────────────────────────────────────────────────────────────────┐
│                     KUBE-DC MANAGEMENT CLUSTER                                  │
│                                                                                 │
│   ┌─────────────┐  ┌─────────────┐  ┌─────────────┐                             │
│   │  master-1   │  │  master-2   │  │  master-3   │  ← Control-plane nodes      │
│   │  RKE2 server│  │  RKE2 server│  │  RKE2 server│    (already installed)      │
│   │  OVN-DB     │  │  OVN-DB     │  │  OVN-DB     │                             │
│   └──────┬──────┘  └──────┬──────┘  └──────┬──────┘                             │
│          │                │                │                                    │
│   ┌──────┴────────────────┴────────────────┴──────┐                             │
│   │          Metal3 Control Plane Components      │                             │
│   │                                               │                             │
│   │  ┌──────────────────┐  ┌───────────────────┐  │                             │
│   │  │  Bare Metal      │  │  Ironic           │  │                             │
│   │  │  Operator (BMO)  │  │  (Provisioning)   │  │                             │
│   │  │                  │  │                   │  │                             │
│   │  │  Manages BMH CRs │  │  PXE/Virtual Media│  │                             │
│   │  └────────┬─────────┘  └────────┬──────────┘  │                             │
│   │           │                     │             │                             │
│   │  ┌────────┴─────────────────────┴──────────┐  │                             │
│   │  │  CAPM3 (Cluster API Provider Metal3)    │  │                             │
│   │  │  + Metal3 IPAM Controller               │  │                             │
│   │  └─────────────────────────────────────────┘  │                             │
│   └─────────────────────────┬─────────────────────┘                             │
│                             │                                                   │
│                             │  BMC (IPMI/Redfish/iDRAC)                         │
│                             ▼                                                   │
│   ┌─────────────────────────────────────────────────────────────────────────┐   │
│   │                     BARE-METAL WORKER POOL                              │   │
│   │                                                                         │   │
│   │   ┌───────────┐ ┌───────────┐  ┌───────────┐  ┌───────────┐             │   │
│   │   │ worker-1  │ │ worker-2  │  │ worker-3  │  │ worker-N  │             │   │
│   │   │ RKE2 agent│ │ RKE2 agent│  │ RKE2 agent│  │ RKE2 agent│             │   │
│   │   │ KubeVirt  │ │ KubeVirt  │  │ KubeVirt  │  │ KubeVirt  │             │   │
│   │   └───────────┘ └───────────┘  └───────────┘  └───────────┘             │   │
│   └─────────────────────────────────────────────────────────────────────────┘   │
└─────────────────────────────────────────────────────────────────────────────────┘

Network Connectivity:
─────────────────────
  Management VLAN ──── All nodes (masters + workers): Kubernetes API, etcd, SSH
  Cloud VLAN      ──── All nodes: Kube-OVN underlay, project VPCs, VM traffic
  Provider VLAN   ──── All nodes: Public IPs (EIPs, FIPs, Service LoadBalancers)
  BMC Network     ──── Masters → Worker BMCs: IPMI/Redfish for power management
```

### How It Works

1. **Enroll** — Register each bare-metal server as a `BareMetalHost` (BMH) CR with its BMC address and credentials
2. **Inspect** — Ironic powers on the server via BMC, PXE-boots a ramdisk, and collects hardware inventory (CPUs, RAM, disks, NICs, MAC addresses)
3. **Provision** — When a `MachineDeployment` scales up, CAPM3 selects an `available` BMH, writes an OS image to disk via Ironic, and injects cloud-init user/network data
4. **Join** — The provisioned server boots into Ubuntu with RKE2 agent pre-configured, joins the management cluster, and becomes a schedulable worker node
5. **Heal** — `MachineHealthCheck` monitors node health; if a node becomes unhealthy, the Metal3 remediation controller power-cycles it via BMC or reprovisions it

## Prerequisites

### Hardware Requirements

| Requirement | Details |
|-------------|---------|
| **BMC access** | Each worker server must have a Baseboard Management Controller (IPMI, Redfish, iDRAC, iLO) reachable from the management cluster |
| **PXE or Virtual Media boot** | Servers must support network boot (PXE) or Redfish Virtual Media for OS provisioning |
| **Boot mode** | UEFI recommended (legacy BIOS supported but not recommended) |
| **Network interfaces** | Minimum 2 NICs: one for management, one trunk for cloud/provider VLANs |
| **Storage** | At least one disk for OS installation (SSD recommended, 100 GB+) |

### Network Requirements

The management cluster nodes must be able to reach:

| Target | Protocol | Port | Purpose |
|--------|----------|------|---------|
| Worker BMCs | IPMI/Redfish | 623 (IPMI), 443 (Redfish) | Power management, virtual media |
| Worker PXE NICs | DHCP + TFTP | 67-69, 6180 | PXE boot (if not using virtual media) |
| Worker management NICs | SSH | 22 | Post-provisioning verification |

Workers must have the **same VLAN trunk access** as the master nodes — they need connectivity to the management, cloud, and provider VLANs. See [Network Architecture](installation-overview.md#network-architecture) for VLAN details.

### Software Requirements

| Component | Status | Notes |
|-----------|--------|-------|
| **Kube-DC management cluster** | Required | 3-node HA cluster per [Installation Guide](installation-guide.md) |
| **cert-manager** | Already installed | Deployed by cdev installer |
| **Cluster API core** | Already installed | Deployed by cdev installer |
| **CAPM3 + BMO + Ironic** | To be installed | This guide covers installation |
| **OS disk image** | To be prepared | Ubuntu 24.04 with RKE2 agent pre-baked |

### Information to Collect

Before proceeding, gather the following for **each worker server**:

| Info | Example | How to obtain |
|------|---------|---------------|
| BMC IP address | `192.168.1.101` | Check BMC/iDRAC web UI or server documentation |
| BMC protocol | `redfish-virtualmedia` | Depends on hardware vendor (see [supported hardware](https://book.metal3.io/bmo/supported_hardware)) |
| BMC credentials | `admin` / `password` | Set via BMC web interface |
| Boot NIC MAC address | `aa:bb:cc:dd:ee:01` | Check `ip link` output or BMC hardware inventory |
| Management NIC name | `eth0` or `eno1` | Varies by hardware; inspect after first boot |
| Trunk NIC name | `eth1` or `enp94s0f0np0` | The VLAN-capable NIC connected to cloud/provider switch |

---

## Phase 1 — Install Metal3 Components

### 1.1 Initialize CAPM3

The Kube-DC installer already deploys Cluster API core components. Add the Metal3 infrastructure provider and IPAM:

```bash
clusterctl init --infrastructure metal3 --ipam metal3
```

This installs:
- **CAPM3** — Cluster API Provider Metal3 (manages `Metal3Machine`, `Metal3MachineTemplate`)
- **Metal3 IPAM** — IP address management for static IP assignment during provisioning
- **Bare Metal Operator (BMO)** — Manages `BareMetalHost` lifecycle

### 1.2 Deploy Ironic

Ironic is the provisioning engine that Metal3 uses to interact with hardware via BMC protocols. Deploy it using the Ironic Standalone Operator:

```bash
# Install Ironic Standalone Operator
kubectl apply -k https://github.com/metal3-io/ironic-standalone-operator/config/default

kubectl -n ironic-standalone-operator-system wait \
  --for=condition=Available --timeout=300s \
  deploy/ironic-standalone-operator-controller-manager
```

Create the Ironic deployment. Adjust the network settings to match your BMC network:

```yaml
# ironic.yaml
apiVersion: metal3.io/v1alpha1
kind: Ironic
metadata:
  name: ironic
  namespace: baremetal-operator-system
spec:
  networking:
    interface: eth0                       # Management interface on master nodes
    ipAddress: 192.168.0.1               # Management IP of the node running Ironic
    dhcp:
      networkCIDR: 192.168.0.0/18        # Management network CIDR
      rangeBegin: 192.168.10.1           # DHCP range for PXE boot (avoid conflicts)
      rangeEnd: 192.168.10.254
  databaseRef:
    name: ironic-mariadb
    namespace: baremetal-operator-system
```

```bash
kubectl create ns baremetal-operator-system
kubectl apply -f ironic.yaml
```

:::warning PXE vs Virtual Media
If your hardware supports **Redfish Virtual Media** (most modern servers do), you can skip the DHCP configuration entirely. Virtual Media mounts the boot ISO directly via BMC, avoiding PXE network complexity. Use BMC addresses like `redfish-virtualmedia://192.168.1.101/redfish/v1/Systems/1` in your BareMetalHost specs.
:::

### 1.3 Verify Metal3 Stack

```bash
# Check all Metal3 components are running
kubectl get pods -n capm3-system
kubectl get pods -n baremetal-operator-system
kubectl get pods -n ironic-standalone-operator-system

# Verify CRDs are installed
kubectl api-resources | grep metal3
# Expected: baremetalhosts, metal3machines, metal3machinetemplates, etc.
```

---

## Phase 2 — Prepare the Worker OS Image

Metal3 provisions servers by writing a disk image. This image must contain:
- **Ubuntu 24.04 LTS** base system
- **RKE2 agent** binaries (pre-installed but not started)
- **cloud-init** for first-boot configuration (network, hostname, cluster join)
- **Kernel modules** required by Kube-OVN (`openvswitch`, `nf_conntrack`)

### 2.1 Build the Image

Use a tool like [image-builder](https://github.com/kubernetes-sigs/image-builder) or create a custom image with Packer:

```bash
# Example: Download base Ubuntu 24.04 cloud image and customize
wget https://cloud-images.ubuntu.com/noble/current/noble-server-cloudimg-amd64.img

# Customize with virt-customize (libguestfs)
virt-customize -a noble-server-cloudimg-amd64.img \
  --install curl,iptables,linux-headers-generic,nfs-common,open-iscsi \
  --run-command 'curl -sfL https://get.rke2.io | INSTALL_RKE2_VERSION=v1.35.0+rke2r1 INSTALL_RKE2_TYPE=agent sh -' \
  --run-command 'systemctl enable rke2-agent.service' \
  --run-command 'echo nf_conntrack >> /etc/modules' \
  --run-command 'echo "fs.inotify.max_user_watches=1524288" >> /etc/sysctl.conf' \
  --run-command 'echo "fs.inotify.max_user_instances=4024" >> /etc/sysctl.conf' \
  --run-command 'echo "net.ipv4.ip_forward=1" >> /etc/sysctl.conf' \
  --run-command 'systemctl disable systemd-resolved' \
  --run-command 'rm -f /etc/resolv.conf && echo -e "nameserver 8.8.8.8\nnameserver 8.8.4.4" > /etc/resolv.conf'
```

### 2.2 Host the Image

Make the image available via HTTP from a server reachable by Ironic:

```bash
# Compute checksum
sha256sum noble-server-cloudimg-amd64.img > noble-server-cloudimg-amd64.img.sha256sum

# Serve via nginx, Apache, or any HTTP server
# Example URL: http://192.168.0.1:8080/images/noble-server-cloudimg-amd64.img
```

---

## Phase 3 — Enroll Bare-Metal Hosts

### 3.1 Create BMC Credentials

Create a Kubernetes secret for each worker server's BMC credentials:

```yaml
# bmh-secrets.yaml
apiVersion: v1
kind: Secret
metadata:
  name: worker-1-bmc
  namespace: baremetal-operator-system
type: Opaque
stringData:
  username: admin
  password: your-bmc-password
---
apiVersion: v1
kind: Secret
metadata:
  name: worker-2-bmc
  namespace: baremetal-operator-system
type: Opaque
stringData:
  username: admin
  password: your-bmc-password
```

```bash
kubectl apply -f bmh-secrets.yaml
```

### 3.2 Create BareMetalHost Resources

Register each server with its BMC address, boot MAC, and boot mode:

```yaml
# baremetalhosts.yaml
apiVersion: metal3.io/v1alpha1
kind: BareMetalHost
metadata:
  name: worker-1
  namespace: baremetal-operator-system
spec:
  online: true
  bootMACAddress: "aa:bb:cc:dd:ee:01"    # MAC of the PXE/management NIC
  bootMode: UEFI
  bmc:
    address: redfish-virtualmedia://192.168.1.101/redfish/v1/Systems/1
    credentialsName: worker-1-bmc
    disableCertificateVerification: true
  automatedCleaningMode: metadata          # Clean disk metadata between provisions
  rootDeviceHints:
    minSizeGigabytes: 100                  # Select disk ≥100 GB for OS install
---
apiVersion: metal3.io/v1alpha1
kind: BareMetalHost
metadata:
  name: worker-2
  namespace: baremetal-operator-system
spec:
  online: true
  bootMACAddress: "aa:bb:cc:dd:ee:02"
  bootMode: UEFI
  bmc:
    address: redfish-virtualmedia://192.168.1.102/redfish/v1/Systems/1
    credentialsName: worker-2-bmc
    disableCertificateVerification: true
  automatedCleaningMode: metadata
  rootDeviceHints:
    minSizeGigabytes: 100
```

```bash
kubectl apply -f baremetalhosts.yaml
```

### 3.3 Wait for Inspection

Watch the BMH resources progress through `registering` → `inspecting` → `available`:

```bash
kubectl get bmh -n baremetal-operator-system -w

# NAME       STATE        CONSUMER   ONLINE   ERROR   AGE
# worker-1   registering             true             10s
# worker-1   inspecting              true             30s
# worker-1   available               true             5m
# worker-2   available               true             5m
```

Once a BMH reaches `available`, Ironic has successfully:
- Powered on the server via BMC
- PXE-booted a ramdisk
- Collected hardware inventory (CPUs, RAM, disks, NICs with MAC addresses)
- Powered the server back off

Inspect the discovered hardware:

```bash
kubectl get bmh worker-1 -n baremetal-operator-system -o jsonpath='{.status.hardware}' | jq .
```

This shows all discovered NICs, disks, CPU, and RAM — essential for configuring network data templates.

---

## Phase 4 — Configure CAPI Resources for Worker Provisioning

### 4.1 Create Metal3 IPAM Pool

Define an IP pool for worker management network addresses:

```yaml
# ippool-mgmt.yaml
apiVersion: ipam.metal3.io/v1alpha1
kind: IPPool
metadata:
  name: worker-mgmt-pool
  namespace: baremetal-operator-system
spec:
  clusterName: kube-dc-mgmt
  namePrefix: worker-mgmt
  pools:
    - start: 192.168.0.50
      end: 192.168.0.99
      prefix: 18
      gateway: 192.168.0.254
```

```bash
kubectl apply -f ippool-mgmt.yaml
```

### 4.2 Create Metal3DataTemplate

The `Metal3DataTemplate` defines how network data and metadata are generated for each provisioned worker. This is critical — it tells cloud-init how to configure the server's network interfaces.

```yaml
# metal3datatemplate.yaml
apiVersion: infrastructure.cluster.x-k8s.io/v1beta1
kind: Metal3DataTemplate
metadata:
  name: worker-data-template
  namespace: baremetal-operator-system
spec:
  clusterName: kube-dc-mgmt
  metaData:
    strings:
      - key: local-hostname
        value: "{{ ds.meta_data.name }}"
  networkData:
    links:
      ethernets:
        # Management NIC — carries Kubernetes API, SSH, node-to-node traffic
        - id: mgmt-nic
          macAddress:
            fromHostInterface: eth0       # Matched against BMH hardware inventory
          type: phy
          mtu: 1500
        # Trunk NIC — carries cloud and provider VLANs
        # Do NOT assign an IP; Kube-OVN manages this via OVS bridges
        - id: trunk-nic
          macAddress:
            fromHostInterface: eth1       # Matched against BMH hardware inventory
          type: phy
          mtu: 9000
    networks:
      ipv4:
        - id: mgmt-network
          ipAddressFromIPPool: worker-mgmt-pool
          link: mgmt-nic
          routes:
            - network: 0.0.0.0
              prefix: 0
              gateway:
                fromIPPool: worker-mgmt-pool
    services:
      dns:
        - 8.8.8.8
        - 8.8.4.4
```

```bash
kubectl apply -f metal3datatemplate.yaml
```

:::warning NIC Name Matching
The `fromHostInterface` values (`eth0`, `eth1`) must match the NIC names discovered during BMH inspection. Check `kubectl get bmh worker-1 -o jsonpath='{.status.hardware.nics}'` to see the actual interface names on your hardware. If NICs have different names across servers, use MAC-based matching or ensure consistent naming via udev rules in the OS image.
:::

### 4.3 Create Metal3MachineTemplate

```yaml
# metal3machinetemplate.yaml
apiVersion: infrastructure.cluster.x-k8s.io/v1beta1
kind: Metal3MachineTemplate
metadata:
  name: worker-machine-template
  namespace: baremetal-operator-system
spec:
  template:
    spec:
      image:
        url: http://192.168.0.1:8080/images/noble-server-cloudimg-amd64.img
        checksum: http://192.168.0.1:8080/images/noble-server-cloudimg-amd64.img.sha256sum
        checksumType: sha256
        format: qcow2
      dataTemplate:
        name: worker-data-template
      hostSelector: {}                    # Selects any available BMH (or use matchLabels)
```

```bash
kubectl apply -f metal3machinetemplate.yaml
```

### 4.4 Create Cloud-Init UserData

The cloud-init user data configures RKE2 agent to join the management cluster and applies required system settings:

```yaml
# worker-userdata-secret.yaml
apiVersion: v1
kind: Secret
metadata:
  name: worker-userdata
  namespace: baremetal-operator-system
type: Opaque
stringData:
  userData: |
    #cloud-config
    hostname: '{{ ds.meta_data.local-hostname }}'
    manage_etc_hosts: true

    write_files:
      - path: /etc/rancher/rke2/config.yaml
        owner: root:root
        permissions: '0644'
        content: |
          token: <RKE2_JOIN_TOKEN>
          server: https://192.168.0.1:9345
          cni: none
          node-ip: '{{ ds.meta_data.local-hostname }}'

      - path: /etc/sysctl.d/99-kube-dc.conf
        owner: root:root
        permissions: '0644'
        content: |
          fs.inotify.max_user_watches=1524288
          fs.inotify.max_user_instances=4024
          net.ipv4.ip_forward=1

    runcmd:
      - sysctl --system
      - modprobe nf_conntrack
      - echo "nf_conntrack" >> /etc/modules
      - systemctl stop systemd-resolved || true
      - systemctl disable systemd-resolved || true
      - rm -f /etc/resolv.conf
      - echo -e "nameserver 8.8.8.8\nnameserver 8.8.4.4" > /etc/resolv.conf
      - systemctl enable rke2-agent.service
      - systemctl start rke2-agent.service
```

:::warning Security
Replace `<RKE2_JOIN_TOKEN>` with the actual token from `master-1`:
```bash
sudo cat /var/lib/rancher/rke2/server/node-token
```
For production, consider using a Kubernetes Secret reference or sealed secret instead of embedding the token directly.
:::

```bash
kubectl apply -f worker-userdata-secret.yaml
```

### 4.5 Create MachineDeployment

The `MachineDeployment` controls how many worker nodes to provision and links all the templates together:

```yaml
# machinedeployment.yaml
apiVersion: cluster.x-k8s.io/v1beta1
kind: MachineDeployment
metadata:
  name: kube-dc-workers
  namespace: baremetal-operator-system
  labels:
    cluster.x-k8s.io/cluster-name: kube-dc-mgmt
    nodepool: kube-dc-worker-pool
spec:
  clusterName: kube-dc-mgmt
  replicas: 2                             # Number of worker nodes to provision
  selector:
    matchLabels:
      cluster.x-k8s.io/cluster-name: kube-dc-mgmt
      nodepool: kube-dc-worker-pool
  template:
    metadata:
      labels:
        cluster.x-k8s.io/cluster-name: kube-dc-mgmt
        nodepool: kube-dc-worker-pool
    spec:
      clusterName: kube-dc-mgmt
      bootstrap:
        dataSecretName: worker-userdata
      infrastructureRef:
        apiVersion: infrastructure.cluster.x-k8s.io/v1beta1
        kind: Metal3MachineTemplate
        name: worker-machine-template
      version: v1.35.0
```

```bash
kubectl apply -f machinedeployment.yaml
```

Watch the provisioning:

```bash
# Watch BMH state changes
kubectl get bmh -n baremetal-operator-system -w

# Watch Machine status
kubectl get machines -n baremetal-operator-system

# Watch nodes joining the cluster
kubectl get nodes -w
```

Provisioning typically takes **10–20 minutes** per server (depending on hardware, network speed, and image size).

---

## Phase 5 — Post-Provisioning Network Configuration

After worker nodes join the cluster, configure Kube-OVN networking to include them.

### 5.1 Update ProviderNetwork

The Kube-OVN `ProviderNetwork` must include the worker nodes so they can participate in cloud and provider VLAN traffic. If workers have the **same trunk NIC name** as the masters, they are automatically included via `defaultInterface`. If NICs differ, add `customInterfaces`:

```bash
# Check what NIC names the workers have
kubectl get bmh worker-1 -n baremetal-operator-system \
  -o jsonpath='{.status.hardware.nics[*].name}'
```

Patch the ProviderNetwork:

```yaml
apiVersion: kubeovn.io/v1
kind: ProviderNetwork
metadata:
  name: ext-cloud
spec:
  defaultInterface: eth1                  # Default trunk NIC (most nodes)
  customInterfaces:
    - interface: eno2                     # Override for workers with different NIC names
      nodes:
        - worker-1
        - worker-2
  autoCreateVlanSubinterfaces: true
  preserveVlanInterfaces: true
```

```bash
kubectl apply -f provider-network-patch.yaml
```

Verify all nodes (masters + workers) are ready in the ProviderNetwork:

```bash
kubectl get provider-networks ext-cloud -o jsonpath='{.status.readyNodes}' | jq .
# Expected: ["master-1", "master-2", "master-3", "worker-1", "worker-2"]
```

### 5.2 Node Labels

Worker nodes provisioned by Metal3 do **not** need the `kube-ovn/role=master` label — that is only for control-plane nodes running OVN Northbound/Southbound databases. However, verify these labels are **absent** on workers:

```bash
# Workers should NOT have these labels:
kubectl get node worker-1 --show-labels | grep -E 'kube-ovn/role|kube-dc-manager'
# Expected: no output
```

| Label | Masters | Workers | Purpose |
|-------|---------|---------|---------|
| `kube-ovn/role=master` | ✅ Yes | ❌ No | Runs OVN central databases |
| `kube-dc-manager=true` | ✅ Yes | ❌ No | Schedules Kube-DC control-plane pods |
| `node-role.kubernetes.io/worker` | ❌ No | ✅ Yes (auto) | Standard Kubernetes worker role |

### 5.3 Verify Worker Networking

After the ProviderNetwork is updated, Kube-OVN creates OVS bridges on the worker nodes:

```bash
# Check OVS bridges on a worker
kubectl exec -n kube-system -it $(kubectl get pod -n kube-system -l app=ovs-ovn \
  --field-selector spec.nodeName=worker-1 -o name) -- ovs-vsctl show

# Check that VLAN subinterfaces were created
kubectl get provider-networks ext-cloud -o jsonpath='{.status.vlans}'
# Expected: ["vlan200","vlan300"] (your cloud and provider VLANs)
```

---

## Phase 6 — Health Checks and Auto-Remediation

Metal3 supports automated health checking and remediation of worker nodes through CAPI `MachineHealthCheck` and `Metal3RemediationTemplate` resources.

### 6.1 Create Metal3RemediationTemplate

The remediation template defines the strategy for handling unhealthy nodes. The **reboot** strategy is recommended for bare metal — it power-cycles the server via BMC rather than reprovisioning from scratch:

```yaml
# metal3remediationtemplate.yaml
apiVersion: infrastructure.cluster.x-k8s.io/v1beta1
kind: Metal3RemediationTemplate
metadata:
  name: worker-remediation
  namespace: baremetal-operator-system
spec:
  template:
    spec:
      strategy:
        type: Reboot
        retryLimit: 2                     # Retry power-cycle up to 2 times
        timeout: 600s                     # Wait 10 minutes for node to recover
```

### 6.2 Create MachineHealthCheck

```yaml
# machinehealthcheck.yaml
apiVersion: cluster.x-k8s.io/v1beta1
kind: MachineHealthCheck
metadata:
  name: worker-healthcheck
  namespace: baremetal-operator-system
spec:
  clusterName: kube-dc-mgmt
  # Match all machines in the worker pool
  selector:
    matchLabels:
      nodepool: kube-dc-worker-pool
  # Safety valve: don't remediate if >40% of nodes are unhealthy
  maxUnhealthy: 40%
  # Time to wait for a new node to join before considering it unhealthy
  nodeStartupTimeout: 30m                 # Bare metal is slow — allow 30 minutes
  # Conditions that trigger remediation
  unhealthyConditions:
    - type: Ready
      status: Unknown
      timeout: 300s                       # Node is unreachable for 5 minutes
    - type: Ready
      status: "False"
      timeout: 300s                       # Node reports NotReady for 5 minutes
  # Use Metal3 remediation (power-cycle via BMC)
  remediationTemplate:
    kind: Metal3RemediationTemplate
    apiVersion: infrastructure.cluster.x-k8s.io/v1beta1
    name: worker-remediation
```

```bash
kubectl apply -f metal3remediationtemplate.yaml
kubectl apply -f machinehealthcheck.yaml
```

### 6.3 How Remediation Works

When a worker node becomes unhealthy:

```
1. MachineHealthCheck detects unhealthy condition (Ready=Unknown for 5 min)
         │
         ▼
2. Creates Metal3Remediation request
         │
         ▼
3. CAPM3 Remediation Controller:
   a. Powers OFF the server via BMC
   b. Applies Out-of-Service taint on the Node
      (triggers StatefulSet/PV rescheduling to healthy nodes)
   c. Powers ON the server via BMC
         │
         ▼
4. Server reboots → RKE2 agent reconnects → Node becomes Ready
         │
         ▼
5. If still unhealthy after retryLimit, Machine is deleted and
   a new one is provisioned from the available BMH pool
```

### 6.4 Monitor Health Checks

```bash
# Check MachineHealthCheck status
kubectl get machinehealthcheck -n baremetal-operator-system

# Check for active remediations
kubectl get metal3remediation -n baremetal-operator-system

# Check Machine health conditions
kubectl get machines -n baremetal-operator-system -o wide
```

---

## Scaling Workers

### Scale Up

Increase replicas in the MachineDeployment:

```bash
kubectl scale machinedeployment kube-dc-workers \
  -n baremetal-operator-system --replicas=4
```

CAPM3 will select `available` BareMetalHosts and provision them. Remember to:
1. Update the `ProviderNetwork` if new workers have different NIC names
2. Ensure enough BareMetalHosts are enrolled and in `available` state

### Scale Down

```bash
kubectl scale machinedeployment kube-dc-workers \
  -n baremetal-operator-system --replicas=1
```

CAPM3 will:
1. Cordon and drain the selected worker
2. Power off the server via BMC
3. Clean the disk (per `automatedCleaningMode`)
4. Return the BMH to `available` state for future use

---

## Best Practices

### Disk Management

- **Set `rootDeviceHints`** on BareMetalHosts to ensure the OS is installed on the correct disk, especially on servers with multiple drives
- **Use `automatedCleaningMode: metadata`** to wipe partition tables between provisions without full disk erase (saves time)
- For sensitive environments, use `automatedCleaningMode: disk` for full disk wipe

### BMC Security

- Use **Redfish Virtual Media** over IPMI when possible — it's more secure and reliable
- Enable **TLS** on BMC interfaces and avoid `disableCertificateVerification: true` in production
- Rotate BMC credentials regularly and store them as Kubernetes Secrets

### Image Management

- **Pre-bake** RKE2 agent, kernel modules, and system packages into the OS image to reduce first-boot time
- Maintain versioned images (e.g., `ubuntu-24.04-rke2-v1.35.0.qcow2`) for reproducible deployments
- Host images on a local HTTP server within the management network — downloading from the internet during provisioning is slow and unreliable

### Node Reuse

- Metal3 supports **node reuse** during rolling upgrades — instead of provisioning a fresh BMH, it reprovisions the same server with a new image
- Enable this by using the scale-in upgrade strategy on MachineDeployments
- This significantly reduces upgrade time for bare-metal clusters

### Monitoring and Alerting

- Set up **Prometheus alerts** for BMH state changes (e.g., `error`, `provisioning failed`)
- Monitor the `MachineHealthCheck` targets count and current healthy/unhealthy ratios
- Alert on `Metal3Remediation` objects being created — they indicate node failures

### ProviderNetwork Consistency

- If all worker servers are the **same hardware model**, they will have consistent NIC names — use `defaultInterface` in the ProviderNetwork
- For **mixed hardware**, use `customInterfaces` to map each node to its correct trunk NIC
- Always verify workers appear in `provider-networks ext-cloud` status after joining

---

## Troubleshooting

### BMH Stuck in "registering"

```bash
kubectl get bmh worker-1 -n baremetal-operator-system -o yaml | grep -A5 errorMessage
```

Common causes:
- BMC IP unreachable from management cluster — check network/firewall
- Wrong BMC credentials — verify the Secret
- Unsupported BMC protocol — check [Metal3 supported hardware](https://book.metal3.io/bmo/supported_hardware)

### BMH Stuck in "inspecting"

- Server failed to PXE boot — check BIOS boot order, PXE NIC settings
- Ironic ramdisk didn't start — check Ironic logs: `kubectl logs -n baremetal-operator-system -l app=ironic`
- Virtual Media mount failed — ensure BMC firmware supports the protocol

### Worker Node Not Joining Cluster

```bash
# Check RKE2 agent logs on the worker (via BMC console or SSH)
sudo journalctl -u rke2-agent -f

# Common issues:
# - Wrong join token in cloud-init userData
# - master-1 unreachable on management network (check routing)
# - DNS resolution failing (check /etc/resolv.conf)
```

### Kube-OVN Not Working on Worker

```bash
kubectl get pods -n kube-system -l app=kube-ovn-cni --field-selector spec.nodeName=worker-1
kubectl logs -n kube-system -l app=kube-ovn-cni --field-selector spec.nodeName=worker-1
```

Common cause: Worker's trunk NIC not matched in ProviderNetwork. Fix by adding a `customInterfaces` entry.

---

## Related Documentation

- [Installation Overview](installation-overview.md) — Reference architecture and network prerequisites
- [Installation Guide](installation-guide.md) — Management cluster deployment
- [Networking Architecture](architecture-networking.md) — Kube-OVN, VLANs, VPCs, service exposure
- [Deploy MetalLB HA](deploy-metallb-ha.md) — Floating IP for Envoy Gateway
- [Metal3 User Guide](https://book.metal3.io/) — Upstream Metal3 documentation
- [CAPM3 Remediation](https://book.metal3.io/capm3/remediaton) — Health check and remediation details
