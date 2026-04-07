---
name: create-vm
description: Deploy a virtual machine in a Kube-DC project with SSH access, cloud-init configuration, and optional external IP exposure. Supports Ubuntu, Debian, CentOS, Fedora, Alpine, openSUSE, Gentoo, and Windows images.
---

## Prerequisites
- Target project must exist and be Ready
- Project namespace: `{org}-{project}`
- SSH keypair secrets auto-exist in project namespace
- **Quota**: verify sufficient CPU, memory, storage, and (if external IP needed) publicIPv4 capacity — use the `check-quota` skill

## Steps

### 1. Look Up Available Images

The platform provides a catalog of OS images in the `images-configmap` ConfigMap (namespace `kube-dc`).
If you have access, retrieve the live catalog:

```bash
kubectl get configmap images-configmap -n kube-dc -o jsonpath='{.data.images\.yaml}'
```

Otherwise, use the reference table below.

### 2. Create DataVolume (Boot Disk)

VMs use DataVolumes to import cloud images via HTTP. The DataVolume downloads the image and creates a PVC.

```yaml
apiVersion: cdi.kubevirt.io/v1beta1
kind: DataVolume
metadata:
  name: {vm-name}-disk
  namespace: {project-namespace}
spec:
  source:
    http:
      url: "{os-image-url}"    # From Available Images table
  storage:
    accessModes: [ReadWriteOnce]
    resources:
      requests:
        storage: {disk-size}    # Must be >= MIN_STORAGE for the OS
    storageClassName: local-path
```

See @vm-template.yaml for the complete VM + DataVolume manifest.

### 3. Create VirtualMachine

Key requirements:
- **Network**: MUST use `networkName: {namespace}/default` with Multus bridge
- **Guest agent**: MUST install `qemu-guest-agent` in cloud-init (use the OS-specific cloud-init from the images table)
- **SSH keys**: Reference `authorized-keys-default` via `accessCredentials`
- **Firmware**: Use the correct `firmware` and `machine` type for the OS (see table)

### 4. Wait for VM Ready

```bash
kubectl get vm {vm-name} -n {project-namespace} -w
kubectl get vmi {vm-name} -n {project-namespace} -o jsonpath='{.status.interfaces[0].ipAddress}'
```

### 5. SSH Access

```bash
# Extract project SSH private key
kubectl get secret ssh-keypair-default -n {project-namespace} \
  -o jsonpath='{.data.id_rsa}' | base64 -d > /tmp/vm_ssh_key
chmod 600 /tmp/vm_ssh_key

# Get VM IP
VM_IP=$(kubectl get vmi {vm-name} -n {project-namespace} \
  -o jsonpath='{.status.interfaces[0].ipAddress}')

# SSH in
ssh -i /tmp/vm_ssh_key {cloud-user}@$VM_IP
```

### 6. External Access (Optional)

For SSH from outside the cluster, use Direct EIP + LoadBalancer:

```yaml
apiVersion: kube-dc.com/v1
kind: EIp
metadata:
  name: {vm-name}-eip
  namespace: {project-namespace}
spec:
  externalNetworkType: public
---
apiVersion: v1
kind: Service
metadata:
  name: {vm-name}-ssh
  namespace: {project-namespace}
  annotations:
    service.nlb.kube-dc.com/bind-on-eip: "{vm-name}-eip"
spec:
  type: LoadBalancer
  ports:
  - port: 22
    targetPort: 22
  selector:
    kubevirt.io/domain: {vm-name}
```

Or use a Floating IP for direct 1:1 NAT:

```yaml
apiVersion: kube-dc.com/v1
kind: FIp
metadata:
  name: {vm-name}-fip
  namespace: {project-namespace}
spec:
  externalNetworkType: public
  vmTarget:
    vmName: {vm-name}
    interfaceName: default
```

## Available Images

Source: `images-configmap` ConfigMap in `kube-dc` namespace.

| OS | Cloud User | Image URL | Min RAM | Min CPU | Min Disk | Firmware |
|----|-----------|-----------|---------|---------|----------|----------|
| Ubuntu 24.04 | `ubuntu` | `https://cloud-images.ubuntu.com/noble/current/noble-server-cloudimg-amd64.img` | 1G | 1 | 20G | bios |
| Debian 12 LTS | `debian` | `https://cloud.debian.org/images/cloud/bookworm/latest/debian-12-generic-amd64.qcow2` | 1G | 1 | 20G | bios |
| CentOS Stream 9 | `centos` | `https://cloud.centos.org/centos/9-stream/x86_64/images/CentOS-Stream-GenericCloud-9-latest.x86_64.qcow2` | 2G | 1 | 20G | bios |
| Fedora 41 | `fedora` | `https://dl.fedoraproject.org/pub/fedora/linux/releases/41/Cloud/x86_64/images/Fedora-Cloud-Base-Generic-41-1.4.x86_64.qcow2` | 2G | 1 | 25G | bios |
| Alpine Linux 3.19 | `alpine` | `https://dl-cdn.alpinelinux.org/alpine/v3.19/releases/cloud/nocloud_alpine-3.19.1-x86_64-bios-cloudinit-r0.qcow2` | 512M | 1 | 10G | bios |
| openSUSE Leap 15.3 | `opensuse` | `https://download.opensuse.org/distribution/leap/15.3/appliances/openSUSE-Leap-15.3-JeOS.x86_64-OpenStack-Cloud.qcow2` | 2G | 1 | 20G | bios |
| Gentoo Linux | `gentoo` | `https://distfiles.gentoo.org/releases/amd64/autobuilds/20250928T160345Z/di-amd64-cloudinit-20250928T160345Z.qcow2` | 2G | 1 | 25G | efi |
| CirrOS (test) | `cirros` | `http://download.cirros-cloud.net/0.5.2/cirros-0.5.2-x86_64-disk.img` | 512M | 1 | 5G | bios |
| Windows 11 (Golden) | `kube-dc` | `https://iso.stage.kube-dc.com/windows11-x64-golden.qcow2` | 8G | 2 | 70G | efi |
| Windows 11 (Fresh) | `Administrator` | `https://iso.stage.kube-dc.com/win11-x64.iso` | 8G | 4 | 70G | efi |

### OS-Specific Cloud-Init

Each OS requires specific cloud-init to install and enable the QEMU guest agent. See @os-cloud-init.yaml for the full cloud-init per OS.

**Linux (Ubuntu, Debian, openSUSE)** — simple:
```yaml
#cloud-config
package_update: true
package_upgrade: true
packages:
  - qemu-guest-agent
runcmd:
  - systemctl enable --now qemu-guest-agent
```

**CentOS/Fedora** — needs SELinux config:
```yaml
#cloud-config
package_update: true
package_upgrade: true
packages:
  - qemu-guest-agent
  - policycoreutils-python-utils
runcmd:
  - setenforce 0
  - sed -i 's/SELINUX=enforcing/SELINUX=permissive/' /etc/selinux/config
  - setsebool -P virt_qemu_ga_manage_ssh 1
  - setsebool -P virt_qemu_ga_exec 1
  - setsebool -P virt_qemu_ga_file_transfer 1
  - restorecon -R /usr/bin/qemu-ga
  - systemctl enable --now qemu-guest-agent
```

**Alpine** — uses rc-service:
```yaml
#cloud-config
package_update: true
packages:
  - qemu-guest-agent
runcmd:
  - rc-service qemu-guest-agent restart
  - rc-update add qemu-guest-agent default
```

**Windows (Golden Image)** — no cloud-init needed (pre-configured with QEMU Guest Agent).

## Verification

After creating the VM, run these checks:

```bash
# 1. Check VM is running
kubectl get vm {vm-name} -n {project-namespace} -o jsonpath='{.status.printableStatus}'
# Expected: Running

# 2. Check VMI exists and has IP
kubectl get vmi {vm-name} -n {project-namespace} -o jsonpath='{.status.interfaces[0].ipAddress}'
# Expected: 10.x.x.x (VPC internal IP)

# 3. Check guest agent is reporting (readiness probe)
kubectl get vmi {vm-name} -n {project-namespace} -o jsonpath='{.status.conditions[?(@.type=="Ready")].status}'
# Expected: True

# 4. Check DataVolume import completed
kubectl get dv {vm-name}-disk -n {project-namespace} -o jsonpath='{.status.phase}'
# Expected: Succeeded
```

**Success**: VM status is `Running`, VMI has an IP, Ready condition is `True`.
**Failure**: If status is `Provisioning` or `Stopped`:
- Check DataVolume: `kubectl describe dv {vm-name}-disk -n {project-namespace}`
- Check VM events: `kubectl describe vm {vm-name} -n {project-namespace}`
- If no IP: guest agent may not be installed — verify cloud-init includes `qemu-guest-agent`

## Safety
- ALWAYS include `qemu-guest-agent` in cloud-init — without it, IP reporting and SSH key injection won't work
- ALWAYS use `networkName: {namespace}/default` — other networks don't exist in the VPC
- Use `storageClassName: local-path` for DataVolumes
- FIP and LoadBalancer on the same VM are mutually exclusive
- Use the correct firmware type: `bios` for most Linux, `efi` for Gentoo and Windows
- Windows VMs need machine type `pc-q35-rhel8.6.0` and HyperV features
