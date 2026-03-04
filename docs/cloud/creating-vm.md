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
2. **Operating System** — select from available images (e.g., Ubuntu 24.04, Debian 12, Windows 11)
3. **vCPUs** and **RAM** — set resources based on your workload
4. **Root Storage Size** — set disk size (e.g., 12 GB for Linux, 70 GB for Windows)

<img src={require('./images/vm-creation-step1.png').default} alt="VM Creation" style={{maxWidth: '700px', width: '100%'}} />

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
          networkName: default
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
          networkName: default
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

Windows VMs require additional configuration for UEFI boot, TPM, and Hyper-V features:

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
      url: https://iso.stage.kube-dc.com/windows11-x64-golden.qcow2
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
          networkName: default
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
