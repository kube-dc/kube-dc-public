---
name: create-vm
description: Deploy a virtual machine in a Kube-DC project with SSH, cloud-init, optional external access, and an optional gated Dedicated GPU VM profile. Use for ordinary VMs and whole-device GPU VMs that require platform preflight and non-migration safeguards.
---

## Prerequisites
- Target project must exist and be Ready
- Project namespace: `{org}-{project}`
- SSH keypair secrets auto-exist in project namespace
- **Quota**: verify sufficient CPU, memory, storage, and (if external IP needed) publicIPv4 capacity — use the `check-quota` skill
- **Dedicated GPU only**: require device quota and the independent VM creation gate; raw KubeVirt access is not GPU product authorization

## Steps

### 1. Look Up Available Images

The platform provides a catalog of OS images in the `images-configmap` ConfigMap (namespace `kube-dc`).
If you have access, retrieve the live catalog:

```bash
kubectl get configmap images-configmap -n kube-dc -o jsonpath='{.data.images\.yaml}'
```

Otherwise, use the reference table below.

### 2. Create DataVolume (Boot Disk)

VMs use DataVolumes to import a cloud image and create a PVC. The **primary
path is a registry import** with `pullMethod: node`: the containerdisk is
pulled through the node's containerd — faster, cached on the node for
subsequent VMs, and works from any tenant VPC. The registry URL MUST be
**digest-pinned** (`@sha256:...`) — never a bare tag, never `latest`.

```yaml
apiVersion: cdi.kubevirt.io/v1beta1
kind: DataVolume
metadata:
  name: {vm-name}-disk
  namespace: {project-namespace}
spec:
  source:
    registry:
      url: "docker://quay.io/containerdisks/ubuntu:24.04@sha256:..."   # ALWAYS digest-pinned
      pullMethod: node
  pvc:
    accessModes: [ReadWriteOnce]
    resources:
      requests:
        storage: {disk-size}    # Must be >= MIN_STORAGE for the OS
    storageClassName: local-path
```

Ready digest-pinned refs come from the platform catalog (UI **OS Images** /
`cdi-os-catalog` ConfigMap). For standard Linux images,
`quay.io/containerdisks/<os>` provides upstream containerdisks — resolve the
tag to a digest before use.

**Fallback — HTTP import** (Windows/ISO, custom images, or OSes with no
containerdisk). Uses the in-cluster S3 mirror URLs from the Available Images
table:

```yaml
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

### 3a. Add a Dedicated GPU (Optional)

When the user explicitly requests a whole-device GPU VM, read
[references/dedicated-gpu.md](references/dedicated-gpu.md) before generating
the VM. Use the Kube-DC wizard or authenticated backend validation/create
transaction. Do not attach native host devices or apply a GPU VM directly with
`kubectl`.

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

Source: `cdi-os-catalog` ConfigMap in `kube-dc` namespace (multi-version,
schema v2). Where the catalog carries a digest-pinned registry ref
(standard Linux families), prefer the registry import from step 2; the
HTTP URLs below are the fallback path (Windows/ISO, custom images).
The cluster mirrors every OS onto its own S3 bucket
(`https://s3.<DOMAIN>/cdi-os-images/`); tenants pull HTTP imports from
that mirror, not from upstream CDNs. The `/latest/` URL alias is what the
chart publishes; specific tag URLs are available for version pinning.

URLs below show the `/latest/` form for a cloud cluster at
`s3.kube-dc.cloud` — substitute your cluster's S3 hostname.

| OS | Cloud User | `/latest/` Image URL | Min RAM | Min CPU | Min Disk | Firmware |
|----|-----------|----------------------|---------|---------|----------|----------|
| Ubuntu 26.04 LTS | `ubuntu` | `https://s3.kube-dc.cloud/cdi-os-images/ubuntu/26.04/latest/resolute-server-cloudimg-amd64.img` | 1G | 1 | 20G | bios |
| Ubuntu 24.04 LTS | `ubuntu` | `https://s3.kube-dc.cloud/cdi-os-images/ubuntu/24.04/latest/noble-server-cloudimg-amd64.img` | 1G | 1 | 20G | bios |
| Ubuntu 22.04 LTS | `ubuntu` | `https://s3.kube-dc.cloud/cdi-os-images/ubuntu/22.04/latest/jammy-server-cloudimg-amd64.img` | 1G | 1 | 20G | bios |
| Debian 12 LTS | `debian` | `https://s3.kube-dc.cloud/cdi-os-images/debian/12/latest/debian-12-generic-amd64.qcow2` | 1G | 1 | 20G | bios |
| CentOS Stream 9 | `centos` | `https://s3.kube-dc.cloud/cdi-os-images/centos-stream/9/latest/CentOS-Stream-GenericCloud-9-latest.x86_64.qcow2` | 2G | 1 | 20G | bios |
| Fedora 42 | `fedora` | `https://s3.kube-dc.cloud/cdi-os-images/fedora/42/latest/Fedora-Cloud-Base-Generic-42-1.1.x86_64.qcow2` | 2G | 1 | 25G | bios |
| Alpine Linux 3.21 | `alpine` | `https://s3.kube-dc.cloud/cdi-os-images/alpine/3.21/latest/nocloud_alpine-3.21.0-x86_64-bios-cloudinit-r0.qcow2` | 512M | 1 | 10G | bios |
| openSUSE Leap 15.6 | `opensuse` | `https://s3.kube-dc.cloud/cdi-os-images/opensuse-leap/15.6/latest/openSUSE-Leap-15.6-Minimal-VM.x86_64-Cloud.qcow2` | 2G | 1 | 20G | bios |
| Gentoo Linux | `gentoo` | `https://s3.kube-dc.cloud/cdi-os-images/gentoo/amd64/latest/gentoo-cloudinit-amd64.qcow2` | 2G | 1 | 20G | bios |
| CirrOS (test) | `cirros` | `https://s3.kube-dc.cloud/cdi-os-images/cirros/0.5.2/latest/cirros-0.5.2-x86_64-disk.img` | 512M | 1 | 5G | bios |
| Windows 11 (Golden) | `kube-dc` | `https://s3.kube-dc.cloud/cdi-os-images/windows/11/latest/windows11-x64-golden.qcow2` | 8G | 2 | 70G | efi |
| Windows 11 (Fresh ISO) | `Administrator` | `https://s3.kube-dc.cloud/cdi-os-images/windows/11/latest/win11-x64.iso` | 8G | 4 | 70G | efi |

### Multi-version selection (advanced)

Most Linux families now mirror up to 4 historical versions. Take
**`/latest/`** by default — the cluster repoints it weekly. Pin a
specific version only when you need reproducibility against a known
build (e.g. troubleshooting a regression, locking a test fleet to a
specific kernel).

Versioned URLs follow this shape:
- Ubuntu (streams): `.../ubuntu/24.04/<YYYYMMDD>/<file>` (e.g. `20260321`)
- Debian: `.../debian/12/<YYYYMMDD-buildN>/<file>` (e.g. `20260518-2482`)
- CentOS Stream: `.../centos-stream/9/<YYYYMMDD.N>/<file>` (e.g. `20260513.0`)
- Alpine: `.../alpine/3.21/<X.Y.Z-rN>/<file>` (e.g. `3.21.7-r0`)
- openSUSE Leap: `.../opensuse-leap/15.6/<release-BuildN.M>/<file>` (e.g. `15.6.0-19.146`)
- Fedora: `.../fedora/42/<release-minor>/<file>` (e.g. `42-1.1`)
- Gentoo: `.../gentoo/amd64/<YYYYMMDDTHHMMSSZ>/<file>` (e.g. `20260517T170110Z`)
- CirrOS / Ubuntu 26.04 / Windows: `.../<family>/<release>/<YYYY-MM-DD-sha8>/<file>`

To see the full version list a cluster has on hand, query:

```bash
kubectl -n kube-dc get cm cdi-os-catalog -o jsonpath='{.data.catalog\.json}' \
  | jq '.families[] | {id, versions: [.versions[].tag]}'
```

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
- For a Dedicated GPU VM, follow [references/dedicated-gpu.md](references/dedicated-gpu.md); enforce `evictionStrategy: None` and never bypass backend preflight
- ALWAYS include `qemu-guest-agent` in cloud-init — without it, IP reporting and SSH key injection won't work
- ALWAYS use `networkName: {namespace}/default` — other networks don't exist in the VPC
- Use `storageClassName: local-path` for DataVolumes
- FIP and LoadBalancer on the same VM are mutually exclusive
- Use the correct firmware type: `bios` for most Linux and Gentoo; `efi` only for Windows
- Windows VMs need machine type `pc-q35-rhel8.6.0` and HyperV features
- Registry DataVolume URLs MUST be digest-pinned (`@sha256:...`) — never a bare tag or `latest`; take refs from the platform catalog (UI "OS Images")
- For HTTP fallback imports, prefer `/latest/` URLs from the cluster S3 mirror over upstream CDN URLs — survives upstream rotations and is the only source the refresh CronJob keeps fresh
