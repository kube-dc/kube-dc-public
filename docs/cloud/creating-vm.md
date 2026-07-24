# Deploying VMs

This guide walks you through deploying virtual machines in Kube-DC using both the Console UI and kubectl manifests.

## Prerequisites

- A Kube-DC Cloud [project](first-project.md)
- [CLI access](cli-kubeconfig.md) configured — `kubectl` working against your project
- (Optional) The [`virtctl`](https://kubevirt.io/user-guide/user_workloads/virtctl_client_tool/) plugin for VM console access

## VM Components

Kube-DC virtualization is powered by [KubeVirt](https://kubevirt.io/) and uses three main resources:

- **DataVolume** — manages the VM's disk image (downloads cloud images, provisions PVCs)
- **VirtualMachine** — defines the VM configuration, resources, and lifecycle
- **VirtualMachineInstance (VMI)** — represents a running VM instance

---

## Creating a VM via Console UI

### Step 1: Open VM Creation

1. Select your project from the sidebar
2. Navigate to **Virtual Machines**
3. Click **+** to create a new VM

<img src={require('./images/vm-list-view.png').default} alt="VM List View" style={{maxWidth: '900px', width: '100%'}} />

### Step 2: Configure the VM

1. **VM Name** — choose a name (e.g., `ubuntu`)
2. **Operating System** — select from available images (Ubuntu 22.04 / 24.04 / 26.04, Debian 12, CentOS Stream 9, Fedora 42, openSUSE Leap 15.6, Alpine 3.21, Gentoo, Windows 11)
3. **Version** *(optional, advanced)* — most Linux families now expose multiple maintained versions (e.g., Ubuntu 24.04 currently keeps `20260321`, `20260225`, `20260209`, `20260131`). Leave the dropdown on **Latest** to take the newest mirrored bytes — Kube-DC keeps `/latest/` pointing at the freshest version per family, refreshed weekly. Pin a specific version only if you need reproducibility against a known build.
4. **vCPUs** and **RAM** — set resources based on your workload
5. **Root Storage Size** — set disk size (e.g., 12 GB for Linux, 70 GB for Windows)
6. **Root disk storage** — the real storage choice for the VM (see [below](#root-disk-storage)):
   - **Local disk (default)** — node-local storage; best durable-write latency. No snapshots, no live migration.
   - **Shared RBD** — shared Ceph-backed storage; supports snapshots and (optionally) live migration. Slower durable writes.
   - When **Shared RBD** is selected, an **Enable live migration** checkbox appears — tick it to let the VM move between nodes during maintenance (available when the OS has a Block golden and the cluster has ≥2 CPU-compatible nodes).
7. **Accelerator** *(when entitled and enabled)* — keep **No GPU** for an
   ordinary VM, or select an available Dedicated GPU VM profile. GPU VMs cannot
   live migrate; the wizard clears live migration and maintenance requires a
   shutdown/restart. Follow the [guest driver and lifecycle guide](gpu-vm-guests.md)
   after first boot.

<img src={require('./images/vm-creation-step1.png').default} alt="VM Creation" style={{maxWidth: '700px', width: '100%'}} />

A compact summary under the selector shows exactly what will be provisioned
(root disk, provisioning, snapshots, live migration) before you submit.

### Step 3: Review and Create

Click **Next** to review the generated YAML, then **Finish** to create the VM.

<img src={require('./images/vm-creation-review.png').default} alt="VM Review" style={{maxWidth: '700px', width: '100%'}} />

The VM will appear in the list. Wait for the status to reach **Running**.

### Managing VMs

Click on a VM to view its details — OS info, status, performance metrics, and conditions.

<img src={require('./images/vm-details-view.png').default} alt="VM Details" style={{maxWidth: '900px', width: '100%'}} />

From the details page you can:

- **Launch Remote Console** — graphical console in the browser
- **Launch SSH Terminal** — web-based SSH terminal
- **Start / Stop / Restart / Delete** — manage the VM lifecycle

---

## Creating a VM via kubectl

### Ubuntu 24.04

<details>
<summary>Ubuntu 24.04 — DataVolume + VirtualMachine manifest</summary>

```yaml
apiVersion: cdi.kubevirt.io/v1beta1
kind: DataVolume
metadata:
  name: ubuntu-root
spec:
  pvc:
    accessModes:
    - ReadWriteOnce
    resources:
      requests:
        storage: 12G
    storageClassName: local-path
  source:
    http:
      url: https://cloud-images.ubuntu.com/noble/current/noble-server-cloudimg-amd64.img
---
apiVersion: kubevirt.io/v1
kind: VirtualMachine
metadata:
  name: ubuntu
spec:
  running: true
  template:
    spec:
      networks:
      - name: vpc_net_0
        multus:
          default: true
          networkName: your-namespace/default  # Replace 'your-namespace' with your actual project namespace
      domain:
        cpu:
          cores: 1
        memory:
          guest: 1G
        devices:
          interfaces:
          - name: vpc_net_0
            bridge: {}
          disks:
          - name: root-volume
            disk:
              bus: virtio
          - name: cloudinitdisk
            disk:
              bus: virtio
      accessCredentials:
      - sshPublicKey:
          source:
            secret:
              secretName: authorized-keys-default
          propagationMethod:
            qemuGuestAgent:
              users:
              - ubuntu
      readinessProbe:
        guestAgentPing: {}
        failureThreshold: 10
        initialDelaySeconds: 30
        periodSeconds: 10
        timeoutSeconds: 5
      volumes:
      - name: root-volume
        dataVolume:
          name: ubuntu-root
      - name: cloudinitdisk
        cloudInitNoCloud:
          userData: |
            #cloud-config
            package_update: true
            package_upgrade: true
            packages:
              - qemu-guest-agent
            runcmd:
              - systemctl enable --now qemu-guest-agent
```

</details>

```bash
kubectl apply -f ubuntu-vm.yaml
```

:::tip SSH Key Injection
The `accessCredentials` section injects your SSH public key from the `authorized-keys-default` secret into the VM via the QEMU guest agent. The `users` field must match the default user for the OS image (`ubuntu` for Ubuntu, `debian` for Debian).
:::

### Accessing VMs via SSH

Once the VM is running, you can SSH into it using the private key stored in your project's `ssh-keypair-default` secret.

#### Step 1: Extract the SSH Private Key

```bash
# Extract the private key from the secret
kubectl get secret ssh-keypair-default -n <your-namespace> -o jsonpath='{.data.id_rsa}' | base64 -d > /tmp/vm_ssh_key
chmod 600 /tmp/vm_ssh_key
```

#### Step 2: Get the VM's IP Address

For VMs with a Floating IP (FIP):
```bash
# Get the external IP from the FIP resource
kubectl get fip -n <your-namespace>
```

For VMs without FIP (internal access only):
```bash
# Get the internal IP from the VMI
kubectl get vmi <vm-name> -n <your-namespace> -o jsonpath='{.status.interfaces[0].ipAddress}'
```

#### Step 3: Connect via SSH

```bash
# SSH using the extracted private key
ssh -i /tmp/vm_ssh_key <username>@<ip-address>

# Examples:
# Ubuntu VM: ssh -i /tmp/vm_ssh_key ubuntu@91.224.11.9
# Debian VM: ssh -i /tmp/vm_ssh_key debian@10.0.0.131
```

:::info Default Users
- **Ubuntu**: `ubuntu`
- **Debian**: `debian`
- **Windows**: `kube-dc`
:::

### Debian 12

<details>
<summary>Debian 12 — DataVolume + VirtualMachine manifest</summary>

```yaml
apiVersion: cdi.kubevirt.io/v1beta1
kind: DataVolume
metadata:
  name: debian-root
spec:
  pvc:
    accessModes:
    - ReadWriteOnce
    resources:
      requests:
        storage: 20G
    storageClassName: local-path
  source:
    http:
      url: https://cloud.debian.org/images/cloud/bookworm/latest/debian-12-generic-amd64.qcow2
---
apiVersion: kubevirt.io/v1
kind: VirtualMachine
metadata:
  name: debian
spec:
  running: true
  template:
    spec:
      networks:
      - name: vpc_net_0
        multus:
          default: true
          networkName: your-namespace/default  # Replace 'your-namespace' with your actual project namespace
      domain:
        cpu:
          cores: 1
        memory:
          guest: 1G
        devices:
          interfaces:
          - name: vpc_net_0
            bridge: {}
          disks:
          - name: root-volume
            disk:
              bus: virtio
          - name: cloudinitdisk
            disk:
              bus: virtio
      accessCredentials:
      - sshPublicKey:
          source:
            secret:
              secretName: authorized-keys-default
          propagationMethod:
            qemuGuestAgent:
              users:
              - debian
      readinessProbe:
        guestAgentPing: {}
        failureThreshold: 10
        initialDelaySeconds: 30
        periodSeconds: 10
        timeoutSeconds: 5
      volumes:
      - name: root-volume
        dataVolume:
          name: debian-root
      - name: cloudinitdisk
        cloudInitNoCloud:
          userData: |
            #cloud-config
            package_update: true
            package_upgrade: true
            packages:
              - qemu-guest-agent
            runcmd:
              - systemctl enable --now qemu-guest-agent
```

</details>

### Windows 11

**From the Console UI (simplest):** pick **Windows 11 Enterprise (Golden Image)** in
the Operating System dropdown, set Root Storage to **70 GB**, and create. The console
clones a pre-built golden (VirtIO drivers, QEMU guest agent and SSH/RDP already
installed) and applies the correct UEFI + TPM + Hyper-V configuration automatically.
The VM boots to the Windows lock screen in a few minutes — open **Launch Remote
Console** to use it.

:::tip Storage quota
A Windows golden is ~75 GB, so its clone needs **~75–80 GB of free storage quota** in
your project. If the project is near its storage limit the clone fails with a quota
error — free space or request more before creating a Windows VM.
:::

The kubectl equivalent, showing the UEFI boot, TPM and Hyper-V features Windows
requires:

<details>
<summary>Windows 11 — DataVolume + VirtualMachine manifest</summary>

```yaml
apiVersion: cdi.kubevirt.io/v1beta1
kind: DataVolume
metadata:
  name: win-root
spec:
  pvc:
    accessModes:
    - ReadWriteOnce
    resources:
      requests:
        storage: 70G
    storageClassName: local-path
  source:
    http:
      # The golden published to your cluster's S3 OS-image mirror (the same entry
      # the Console UI clones). Replace <your-cluster-domain> with your domain.
      url: https://s3.<your-cluster-domain>/cdi-os-images/windows/11/latest/windows11-x64-golden.qcow2
---
apiVersion: kubevirt.io/v1
kind: VirtualMachine
metadata:
  name: win
spec:
  running: true
  template:
    spec:
      networks:
      - name: vpc_net_0
        multus:
          default: true
          networkName: your-namespace/default  # Replace 'your-namespace' with your actual project namespace
      domain:
        cpu:
          cores: 2
          model: host-model
        memory:
          guest: 8G
        features:
          acpi: {}
          apic: {}
          hyperv:
            frequencies: {}
            relaxed: {}
            reset: {}
            runtime: {}
            spinlocks:
              spinlocks: 8191
            synic: {}
            vapic: {}
            vpindex: {}
          smm:
            enabled: true
        firmware:
          bootloader:
            efi:
              persistent: true
              secureBoot: false
        devices:
          interfaces:
          - name: vpc_net_0
            bridge: {}
          disks:
          - name: root-volume
            disk:
              bus: virtio
          - name: cloudinitdisk
            disk:
              bus: virtio
          tpm:
            persistent: true
      accessCredentials:
      - sshPublicKey:
          source:
            secret:
              secretName: authorized-keys-default
          propagationMethod:
            qemuGuestAgent:
              users:
              - kube-dc
      readinessProbe:
        guestAgentPing: {}
        failureThreshold: 10
        initialDelaySeconds: 30
        periodSeconds: 10
        timeoutSeconds: 5
      volumes:
      - name: root-volume
        dataVolume:
          name: win-root
      - name: cloudinitdisk
        cloudInitNoCloud:
          userData: ""
```

</details>

:::note Windows Images
Windows cloud images must be pre-built with VirtIO drivers and the QEMU guest agent installed. See [Managing OS Images](/platform/managing-os-images) and [Windows VM Setup](/platform/windows-vm-setup) for details on preparing golden images.
:::

---

## Root disk storage

When you create a VM the main storage decision is real infrastructure, not a product
tier: **where does the root disk live?**

| Root disk | What you get | Trade-off |
|---|---|---|
| **Local disk (default)** | Node-local storage. **Best durable-write latency.** | Lives on one node — no volume snapshots, and a node drain stops the VM. |
| **Shared RBD** | Shared Ceph-backed storage. Supports **snapshots** and (optionally) **live migration**. | Higher durable-write latency for fsync-heavy workloads. |

The decision most users make is simply: **do I want the fastest disk writes, or shared
storage features (snapshots, live migration)?** Local disk cannot live-migrate because
it is local to one node — that limitation is inherent, not a policy.

When you pick **Shared RBD**, two things follow:

- **Provisioning** — if a prepared *golden* image exists for the OS, Kube-DC clones it
  (the VM boots in seconds); otherwise it imports the image on first boot. This is
  automatic — you don't choose it.
- **Enable live migration** *(checkbox)* — opt in to let the VM move between nodes during
  maintenance. Available when the OS has a *Block* golden and the cluster has ≥2
  CPU-compatible nodes; it uses RWX **Block** mode and a pinned CPU model.

Operators: the cluster-side mechanics (storage tiers, enabling RBD, migration pools, the
CPU-headroom rule) are in [VM storage tiers & live migration](/platform/vm-storage-tiers).

### Create an HA (live-migratable) VM — the simple way

1. In **+ Create VM**, choose your OS (e.g. Ubuntu 24.04) and set CPU / RAM / storage.
2. Under **Root disk storage**, select **Shared RBD**.
3. Tick **Enable live migration**. If your cluster has more than one CPU pool, pick the
   **Migration pool** to pin to.
4. Review the summary, click **Next → Finish**.

That's it — the VM comes up live-migratable. During node maintenance Kube-DC moves it
to another node in the pool with no downtime.

:::warning CPU headroom for migration
A live migration briefly runs **two copies** of the VM (source + target) while memory
is copied. Keep **free CPU quota ≥ the VM's CPU count**, or a migration will wait until
you free capacity. Prefer smaller HA VMs, or migrate them one at a time.
:::

### The generated manifest

Choosing **Shared RBD + live migration** renders explicit KubeVirt resources — there is
no hidden magic and no mutating webhook. Kube-DC also stamps two descriptive labels
(`kube-dc.com/vm-profile`, `kube-dc.com/storage-tier`) so the choice is visible in
`kubectl`, but **nothing depends on them** — the spec fields below are the source of
truth. You can write the same manifest by hand:

<details>
<summary>Ubuntu 24.04 — HA / live-migratable VM (generated manifest)</summary>

```yaml
# Root disk: an RWX Block clone of the OS's Block golden snapshot.
# accessModes: ReadWriteMany + volumeMode: Block + storageClassName: rbd-vm
# are what make the disk (and therefore the VM) live-migratable.
apiVersion: v1
kind: PersistentVolumeClaim
metadata:
  name: ubuntu-root
  labels:
    kube-dc.com/vm-profile: migratable       # descriptive only
    kube-dc.com/storage-tier: rbd-vm-block    # descriptive only
spec:
  accessModes:
  - ReadWriteMany
  volumeMode: Block
  storageClassName: rbd-vm
  resources:
    requests:
      storage: 12Gi
  dataSource:
    apiGroup: snapshot.storage.k8s.io
    kind: VolumeSnapshot
    name: ubuntu-24.04-golden-block   # the per-project Block golden snapshot
---
apiVersion: kubevirt.io/v1
kind: VirtualMachine
metadata:
  name: ubuntu
  labels:
    kube-dc.com/vm-profile: migratable
    kube-dc.com/storage-tier: rbd-vm-block
spec:
  running: true
  template:
    spec:
      evictionStrategy: LiveMigrate    # drain a node ⇒ live-migrate, don't kill
      networks:
      - name: vpc_net_0
        multus:
          default: true
          networkName: your-namespace/default
      domain:
        cpu:
          cores: 1
          model: Skylake-Server-IBRS   # pinned to the migration pool's CPU model
        memory:
          guest: 2G
        devices:
          interfaces:
          - name: vpc_net_0
            bridge: {}
          disks:
          - name: root-volume
            disk:
              bus: virtio
          - name: cloudinitdisk
            disk:
              bus: virtio
      accessCredentials:
      - sshPublicKey:
          source:
            secret:
              secretName: authorized-keys-default
          propagationMethod:
            qemuGuestAgent:
              users:
              - ubuntu
      volumes:
      - name: root-volume
        persistentVolumeClaim:
          claimName: ubuntu-root
      - name: cloudinitdisk
        cloudInitNoCloud:
          userData: |
            #cloud-config
            packages:
              - qemu-guest-agent
            runcmd:
              - systemctl enable --now qemu-guest-agent
```

</details>

The three facts that make it migratable are the PVC's `ReadWriteMany` + `Block` +
`rbd-vm`, the VM's `evictionStrategy: LiveMigrate`, and the pinned `cpu.model`. Verify
with:

```bash
kubectl get vmi ubuntu -n <your-namespace> \
  -o jsonpath='{range .status.conditions[*]}{.type}={.status} {end}'
# LiveMigratable=True StorageLiveMigratable=True → good
```

### Monitor VM Status

```bash
# List VMs
kubectl get vm

# Watch a VM come up
kubectl get vm -w

# Check running instances and IP addresses
kubectl get vmi
```

---

## Exposing VMs with Floating IPs

Floating IPs (FIPs) provide direct public IP access to a VM. The FIP automatically resolves the VM's internal IP via the QEMU guest agent — no need to look up IP addresses manually.

```yaml
apiVersion: kube-dc.com/v1
kind: FIp
metadata:
  name: debian-fip
spec:
  externalNetworkType: public
  vmTarget:
    vmName: debian
    interfaceName: vpc_net_0
```

```bash
kubectl apply -f debian-fip.yaml
```

Check the assigned external IP:

```bash
kubectl get fip
```

```
NAME         TARGET IP    EXTERNAL IP    VM       INTERFACE   READY
debian-fip   10.0.0.131   91.224.11.16   debian   vpc_net_0   true
```

You can now SSH directly to the VM:

```bash
ssh debian@91.224.11.16
```

:::tip No EIP Required
When using `externalNetworkType: public` on a FIP, a dedicated public EIP is automatically allocated and bound. You don't need to create an EIP separately.
:::

### Exposing VM Ports via LoadBalancer

For exposing specific ports (e.g., SSH on a non-standard port) without a dedicated public IP, use a LoadBalancer service:

```yaml
apiVersion: v1
kind: Service
metadata:
  name: ubuntu-ssh
  annotations:
    service.nlb.kube-dc.com/bind-on-default-gw-eip: "true"
spec:
  type: LoadBalancer
  selector:
    vm.kubevirt.io/name: ubuntu
  ports:
  - name: ssh
    port: 2222
    targetPort: 22
```

This binds to the project's shared EIP. Access via:

```bash
ssh -p 2222 ubuntu@<project-eip>
```

See the [Service Exposure Guide](service-exposure.md) for more options including HTTPS routes and dedicated EIPs.

---

## Best Practices

- **Use SSH keys** — the `authorized-keys-default` secret is auto-created per project; add your public keys there
- **Enable guest agent** — always include `qemu-guest-agent` in cloud-init for proper IP reporting, readiness probes, and SSH key injection
- **Right-size resources** — Linux VMs typically need 1 vCPU / 1 GB RAM minimum; Windows needs 2 vCPUs / 8 GB RAM
- **Use readiness probes** — `guestAgentPing` ensures the VM is fully booted before being marked Ready

## Troubleshooting

| Issue | Check |
|-------|-------|
| VM stuck in provisioning | `kubectl get dv` — check DataVolume download progress |
| VM running but not Ready | `kubectl get vmi` — verify guest agent is connected |
| No IP assigned | Check `networkName` matches your project's default network |
| SSH key not injected | Verify `authorized-keys-default` secret exists and guest agent is running |
| Docker in the VM: `git clone` / `docker pull` / `apt-get` hangs then fails, but `curl` works | Docker's default MTU (1500) is larger than the project network's 1400. Set `{"mtu": 1400}` in `/etc/docker/daemon.json` and restart Docker — see [Network MTU](networking-overview.md#network-mtu-1400) |

```bash
# Check events for errors
kubectl get events --sort-by=.lastTimestamp

# Access VM console directly
virtctl console ubuntu

# View VM serial console logs
virtctl logs ubuntu
```

## Next Steps

- [Connecting to VMs](connecting-vm.md) — SSH access and remote console options
- [VM Lifecycle](vm-lifecycle.md) — Start, stop, restart, and snapshot VMs
- [Service Exposure](service-exposure.md) — Expose VM services to the internet
- [Public & Floating IPs](public-floating-ips.md) — Manage IP addresses
