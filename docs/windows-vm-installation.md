# Windows 11 VM Installation with KubeVirt

This guide covers the complete process of installing Windows 11 VMs using KubeVirt, including ISO serving infrastructure, disk sizing fixes, OpenSSH automation, and creating reusable golden images.

## Overview

The Windows 11 VM installation involves several components:
- **ISO Storage Infrastructure**: Dedicated namespace and HTTP server for serving Windows ISOs
- **DataVolumes**: HTTP-sourced ISOs and blank OS disk
- **VM Configuration**: UEFI firmware with VirtIO devices and Multus networking
- **Disk Sizing Fix**: Pre-creating proper disk.img for filesystem-mode PVCs
- **OpenSSH Integration**: Automated SSH setup via cloud-init and PowerShell scripts
- **Golden Image Creation**: Capturing installed VMs for reuse
- **VM Metrics Limitations**: Windows guest agent differences vs Linux VMs

## Architecture

```
┌─────────────────┐    ┌──────────────────┐    ┌─────────────────┐
│   iso namespace │    │   shalb-dev      │    │  Golden Images  │
│                 │    │   namespace      │    │                 │
│ ┌─────────────┐ │    │ ┌──────────────┐ │    │ ┌─────────────┐ │
│ │ Nginx Pod   │ │    │ │ Windows VM   │ │    │ │ DataSource  │ │
│ │ (ISO files) │ │    │ │              │ │    │ │ (Template)  │ │
│ └─────────────┘ │    │ └──────────────┘ │    │ └─────────────┘ │
│        │        │    │        │         │    │        │        │
│ ┌─────────────┐ │    │ ┌──────────────┐ │    │ ┌─────────────┐ │
│ │ 20GB PVC    │ │    │ │ DataVolumes  │ │    │ │ Clone DVs   │ │
│ │ (Storage)   │ │    │ │ (OS + ISOs)  │ │    │ │ (New VMs)   │ │
│ └─────────────┘ │    │ └──────────────┘ │    │ └─────────────┘ │
└─────────────────┘    └──────────────────┘    └─────────────────┘
         │                       │                       │
         └───────────────────────┼───────────────────────┘
                                 │
                    ┌─────────────────────────┐
                    │ iso.stage.kube-dc.com   │
                    │ (Ingress)               │
                    └─────────────────────────┘
```

## Prerequisites

- KubeVirt and CDI installed
- Multus CNI with OVN network (`shalb-dev/default`)
- StorageClass `local-path` available
- Windows 11 ISO file (~6GB)
- VirtIO drivers ISO (~700MB)

## Step 1: ISO Storage Infrastructure

### 1.1 Create ISO Namespace

```yaml
# hack/windows/iso-namespace.yaml
apiVersion: v1
kind: Namespace
metadata:
  name: iso
  annotations:
    ovn.kubernetes.io/logical_switch: ovn-default
---
apiVersion: "k8s.ovn.org/v1"
kind: Subnet
metadata:
  name: iso-subnet
spec:
  default: false
  protocol: IPv4
  cidr: 10.1.0.0/16
  gateway: 10.1.0.1
  namespaces:
    - iso
```

### 1.2 Create Storage for ISOs

```yaml
# hack/windows/iso-storage-pvc-iso-ns.yaml
apiVersion: v1
kind: PersistentVolumeClaim
metadata:
  name: iso-storage
  namespace: iso
spec:
  accessModes:
    - ReadWriteOnce
  resources:
    requests:
      storage: 20Gi
  storageClassName: local-path
---
# hack/windows/iso-storage-pod-iso-ns.yaml
apiVersion: v1
kind: Pod
metadata:
  name: iso-storage-pod
  namespace: iso
spec:
  containers:
  - name: storage
    image: busybox:1.36
    command: ["sleep", "infinity"]
    volumeMounts:
    - name: storage
      mountPath: /storage
  volumes:
  - name: storage
    persistentVolumeClaim:
      claimName: iso-storage
```

### 1.3 Upload ISO Files

```bash
# Apply storage infrastructure
kubectl apply -f hack/windows/iso-namespace.yaml
kubectl apply -f hack/windows/iso-storage-pvc-iso-ns.yaml
kubectl apply -f hack/windows/nginx-iso-server-iso-ns.yaml
kubectl apply -f hack/windows/iso-ingress-iso-ns.yaml

# Create temporary upload pod (nginx has read-only filesystem)
cat <<EOF | kubectl apply -f -
apiVersion: v1
kind: Pod
metadata:
  name: iso-upload-pod
  namespace: iso
spec:
  restartPolicy: Never
  containers:
  - name: uploader
    image: busybox:1.36
    command: ["sleep", "3600"]
    volumeMounts:
    - name: storage
      mountPath: /storage
  volumes:
  - name: storage
    persistentVolumeClaim:
      claimName: iso-storage
EOF

# Wait for upload pod to be ready
kubectl wait --for=condition=Ready pod/iso-upload-pod -n iso --timeout=60s

# Upload Windows 11 Enterprise ISO (replace with your ISO path)
# Note: iso namespace hosts the correct Enterprise version
kubectl cp /path/to/Win11_24H2_Enterprise_x64.iso iso/iso-upload-pod:/storage/

# Upload VirtIO drivers ISO
kubectl cp /path/to/virtio-win.iso iso/iso-upload-pod:/storage/

# Clean up upload pod
kubectl delete pod iso-upload-pod -n iso --wait=true
```

### 1.4 HTTP Server for ISOs

```yaml
# hack/windows/nginx-iso-server-iso-ns.yaml
apiVersion: apps/v1
kind: Deployment
metadata:
  name: nginx-iso-server
  namespace: iso
spec:
  replicas: 1
  selector:
    matchLabels:
      app: nginx-iso-server
  template:
    metadata:
      labels:
        app: nginx-iso-server
    spec:
      containers:
      - name: nginx
        image: nginx:1.25-alpine
        ports:
        - containerPort: 80
        volumeMounts:
        - name: iso-storage
          mountPath: /usr/share/nginx/html
        - name: nginx-config
          mountPath: /etc/nginx/conf.d
      volumes:
      - name: iso-storage
        persistentVolumeClaim:
          claimName: iso-storage
      - name: nginx-config
        configMap:
          name: nginx-iso-config
---
apiVersion: v1
kind: ConfigMap
metadata:
  name: nginx-iso-config
  namespace: iso
data:
  default.conf: |
    server {
        listen 80;
        server_name _;
        root /usr/share/nginx/html;
        
        location / {
            autoindex on;
            autoindex_exact_size off;
            autoindex_localtime on;
        }
        
        location ~* \.(iso|img)$ {
            add_header Content-Type application/octet-stream;
            add_header Cache-Control "public, max-age=3600";
        }
    }
---
apiVersion: v1
kind: Service
metadata:
  name: nginx-iso-server
  namespace: iso
spec:
  selector:
    app: nginx-iso-server
  ports:
  - port: 80
    targetPort: 80
```

### 1.5 Ingress for ISO Access

```yaml
# hack/windows/iso-ingress-iso-ns.yaml
apiVersion: networking.k8s.io/v1
kind: Ingress
metadata:
  name: iso-ingress
  namespace: iso
  annotations:
    nginx.ingress.kubernetes.io/proxy-body-size: "10g"
    nginx.ingress.kubernetes.io/proxy-read-timeout: "600"
    nginx.ingress.kubernetes.io/proxy-send-timeout: "600"
spec:
  rules:
  - host: iso.stage.kube-dc.com
    http:
      paths:
      - path: /
        pathType: Prefix
        backend:
          service:
            name: nginx-iso-server
            port:
              number: 80
```

```bash
# Apply HTTP server
kubectl apply -f hack/windows/nginx-iso-server-iso-ns.yaml
kubectl apply -f hack/windows/iso-ingress-iso-ns.yaml

# Verify ISOs are accessible
curl -I http://iso.stage.kube-dc.com/Win11_24H2_Enterprise_x64.iso
curl -I http://iso.stage.kube-dc.com/virtio-win.iso
```

## Step 2: Windows VM Configuration

### 2.1 Create DataVolumes

```yaml
# hack/windows/windows11-vm.yaml (DataVolume sections)
---
apiVersion: cdi.kubevirt.io/v1beta1
kind: DataVolume
metadata:
  name: windows11-installer
  namespace: shalb-dev
spec:
  source:
    http:
      url: "http://iso.stage.kube-dc.com/Win11_24H2_Enterprise_x64.iso"
  storage:
    accessModes:
      - ReadWriteOnce
    resources:
      requests:
        storage: 6Gi
    storageClassName: local-path
---
apiVersion: cdi.kubevirt.io/v1beta1
kind: DataVolume
metadata:
  name: virtio-drivers
  namespace: shalb-dev
spec:
  source:
    http:
      url: "http://iso.stage.kube-dc.com/virtio-win.iso"
  storage:
    accessModes:
      - ReadWriteOnce
    resources:
      requests:
        storage: 1Gi
    storageClassName: local-path
---
apiVersion: cdi.kubevirt.io/v1beta1
kind: DataVolume
metadata:
  name: windows11-disk
  namespace: shalb-dev
spec:
  source:
    blank: {}
  storage:
    accessModes:
      - ReadWriteOnce
    resources:
      requests:
        storage: 60Gi
    storageClassName: local-path
```

### 2.2 VM Specification

```yaml
# hack/windows/windows11-vm.yaml (VM section)
---
apiVersion: kubevirt.io/v1
kind: VirtualMachine
metadata:
  name: windows11-vm
  namespace: shalb-dev
spec:
  runStrategy: Always
  template:
    metadata:
      labels:
        kubevirt.io/vm: windows11-vm
    spec:
      domain:
        machine:
          type: q35
        cpu:
          cores: 4
        memory:
          guest: 12Gi
        firmware:
          bootloader:
            efi:
              secureBoot: true
              persistent: true
        features:
          smm:
            enabled: true
        devices:
          tpm:
            persistent: true
          autoattachGraphicsDevice: true
          disks:
            - name: windows11-installer
              cdrom:
                bus: sata
              bootOrder: 1
            - name: virtio-drivers
              cdrom:
                bus: sata
            - name: windows11-disk
              disk:
                bus: sata
          interfaces:
            - name: vpc_net_0
              bridge: {}
      networks:
        - name: vpc_net_0
          multus:
            default: true
            networkName: shalb-dev/default
      volumes:
        - name: windows11-disk
          dataVolume:
            name: windows11-disk
        - name: windows11-installer
          dataVolume:
            name: windows11-installer
        - name: virtio-drivers
          dataVolume:
            name: virtio-drivers
```

## Step 3: Disk Sizing Fix

⚠️ **CRITICAL**: This step is required for `local-path` StorageClass to avoid 3.4GB disk size issue.

### Problem
With `storageClassName: local-path` (filesystem mode), KubeVirt creates a `disk.img` file that may be undersized due to filesystem overhead calculations. Without this fix, Windows will only see ~3.4GB instead of the full 60GB.

### Solution: Pre-create disk.img

```yaml
# hack/windows/pvc-init-windows11-disk.yaml
apiVersion: v1
kind: Pod
metadata:
  name: pvc-init-windows11-disk
  namespace: shalb-dev
spec:
  restartPolicy: Never
  containers:
    - name: init
      image: busybox:1.36
      command:
        - sh
        - -c
        - |
          set -euo pipefail
          cd /pvc
          echo "Creating 60G sparse disk.img on PVC..."
          rm -f disk.img || true
          truncate -s 60G disk.img
          chown 107:107 disk.img
          ls -lh disk.img
          echo "Done. Sleeping to allow inspection..."
          sleep 600
      volumeMounts:
        - name: pvc
          mountPath: /pvc
  volumes:
    - name: pvc
      persistentVolumeClaim:
        claimName: windows11-disk
```

### Fix Process

```bash
# 1. Stop VM to free PVC
kubectl delete vm windows11-vm -n shalb-dev --wait=true

# 2. Pre-create proper disk.img
kubectl apply -f hack/windows/pvc-init-windows11-disk.yaml
kubectl wait --for=condition=Ready pod/pvc-init-windows11-disk -n shalb-dev --timeout=60s
kubectl logs -n shalb-dev pod/pvc-init-windows11-disk

# 3. Clean up and recreate VM
kubectl delete pod pvc-init-windows11-disk -n shalb-dev --wait=true
kubectl apply -f hack/windows/windows11-vm.yaml

# 4. Verify disk size in VM
kubectl wait --for=condition=Ready vmi/windows11-vm -n shalb-dev --timeout=120s
kubectl get pods -n shalb-dev -l kubevirt.io/vm=windows11-vm -o name
kubectl exec -n shalb-dev <virt-launcher-pod> -c compute -- qemu-img info /var/run/kubevirt-private/vmi-disks/windows11-disk/disk.img
```

## Step 4: Installation Process

### 4.1 Access VM Console

```bash
# VNC access (requires virtctl)
virtctl vnc windows11-vm -n shalb-dev

# Or use VNC proxy
virtctl vnc windows11-vm -n shalb-dev --proxy-only --port 5900
# Then connect VNC client to localhost:5900
```

### 4.2 Windows Installation

1. **Boot from Windows ISO**: VM should boot from the Windows installer
2. **Load VirtIO Drivers**: When prompted for disk drivers, browse the VirtIO drivers ISO
3. **Install to 60GB Disk**: Select the full 60GB disk for Windows installation
4. **Complete Setup**: Follow Windows 11 setup process

### 4.3 Post-Installation

1. **Install VirtIO Drivers**: Install remaining VirtIO drivers from the ISO
2. **Windows Updates**: Apply latest updates
3. **Install QEMU Guest Agent**: Required for metrics and SSH key injection
4. **OpenSSH Setup**: Manual or automated via PowerShell script
5. **Sysprep (for templates)**: Run sysprep if creating a golden image

## Step 5: OpenSSH Integration

### 5.1 Manual OpenSSH Installation

For existing Windows VMs without cloud-init:

```powershell
# hack/windows/install-openssh-windows.ps1
# Download and install OpenSSH Server
$url = "https://github.com/PowerShell/Win32-OpenSSH/releases/download/v9.5.0.0p1-Beta/OpenSSH-Win64.zip"
$output = "$env:TEMP\OpenSSH-Win64.zip"
Invoke-WebRequest -Uri $url -OutFile $output

# Extract and install
Expand-Archive -Path $output -DestinationPath "$env:ProgramFiles"
& "$env:ProgramFiles\OpenSSH-Win64\install-sshd.ps1"

# Configure and start service
Set-Service -Name sshd -StartupType Automatic
Start-Service sshd

# Configure firewall
New-NetFirewallRule -DisplayName 'SSH' -Direction Inbound -Protocol TCP -LocalPort 22 -Action Allow
```

### 5.2 Automated SSH with Cloud-Init

For new VMs with cloud-init support:

```yaml
# hack/windows/win11-enterprise-custom.yaml (excerpt)
volumes:
  - name: cloudinitdisk
    cloudInitNoCloud:
      userData: |
        #cloud-config
        users:
          - name: kube-dc
            groups: administrators
            shell: cmd
            lock_passwd: false
        packages:
          - qemu-guest-agent
          - openssh
        runcmd:
          - powershell -Command "Set-Service -Name QEMU-GA -StartupType Automatic"
          - powershell -Command "Start-Service QEMU-GA"
          - powershell -Command "Set-Service -Name sshd -StartupType Automatic"
          - powershell -Command "Start-Service sshd"
          - powershell -Command "New-NetFirewallRule -DisplayName 'SSH' -Direction Inbound -Protocol TCP -LocalPort 22 -Action Allow"
```

### 5.3 SSH Key Injection

```yaml
# SSH key injection via QEMU Guest Agent
accessCredentials:
- sshPublicKey:
    source:
      secret:
        secretName: authorized-keys-default
    propagationMethod:
      qemuGuestAgent:
        users:
        - kube-dc
```

```bash
# Create SSH key secret
kubectl create secret generic authorized-keys-default \
  --from-file=key1=/path/to/your/public/key.pub \
  -n shalb-dev
```

## Step 6: Golden Image Creation

### 6.1 Prepare for Capture

⚠️ **IMPORTANT**: Stop the source VM before creating DataSource to avoid clone conflicts.

```bash
# 1. Sysprep Windows (inside Windows VM)
# Run: C:\Windows\System32\Sysprep\sysprep.exe
# Options: Generalize, OOBE, Shutdown

# 2. Ensure VM is stopped (CRITICAL for cloning)
kubectl delete vm windows11-vm -n shalb-dev --wait=true
```

### 6.2 Create DataSource

```yaml
# examples/virtual-machine/windows11-golden-datasource.yaml
apiVersion: cdi.kubevirt.io/v1beta1
kind: DataSource
metadata:
  name: windows11-golden
  namespace: shalb-dev
spec:
  source:
    pvc:
      name: windows11-disk
      namespace: shalb-dev
```

### 6.3 Clone from Golden Image

```yaml
# examples/virtual-machine/windows11-from-golden.yaml
apiVersion: cdi.kubevirt.io/v1beta1
kind: DataVolume
metadata:
  name: win11-clone-01
  namespace: demo
spec:
  sourceRef:
    kind: DataSource
    name: windows11-golden
    namespace: shalb-dev
  storage:
    accessModes:
      - ReadWriteOnce
    resources:
      requests:
        storage: 60Gi
    storageClassName: local-path
---
apiVersion: kubevirt.io/v1
kind: VirtualMachine
metadata:
  name: win11-clone-01
  namespace: demo
spec:
  runStrategy: Always
  template:
    metadata:
      labels:
        kubevirt.io/vm: win11-clone-01
    spec:
      domain:
        machine:
          type: q35
        cpu:
          cores: 4
        memory:
          guest: 8Gi
        firmware:
          bootloader:
            efi:
              secureBoot: true
              persistent: true
        features:
          smm:
            enabled: true
        devices:
          tpm:
            persistent: true
          autoattachGraphicsDevice: true
          disks:
            - name: root-disk
              disk:
                bus: virtio  # Can use VirtIO after drivers installed
          interfaces:
            - name: vpc_net_0
              bridge: {}
      networks:
        - name: vpc_net_0
          multus:
            default: true
            networkName: demo/default  # Adjust for target namespace
      volumes:
        - name: root-disk
          dataVolume:
            name: win11-clone-01
```

## Step 7: Alternative Export Method

### Export to QCOW2

```yaml
# hack/pvc-export-windows11.yaml
apiVersion: v1
kind: Pod
metadata:
  name: pvc-export-windows11
  namespace: shalb-dev
spec:
  restartPolicy: Never
  containers:
    - name: hold
      image: busybox:1.36
      command: ["sh","-c","sleep 3600"]
      volumeMounts:
        - name: pvc
          mountPath: /pvc
  volumes:
    - name: pvc
      persistentVolumeClaim:
        claimName: windows11-disk
```

```bash
# Export process
kubectl apply -f hack/pvc-export-windows11.yaml
kubectl wait --for=condition=Ready pod/pvc-export-windows11 -n shalb-dev --timeout=120s

# Copy raw disk image
kubectl cp shalb-dev/pvc-export-windows11:/pvc/disk.img ./windows11-60g.raw

# Convert to QCOW2
qemu-img convert -p -O qcow2 windows11-60g.raw windows11-60g.qcow2

# Upload to ISO server and create HTTP DataVolume
kubectl cp ./windows11-60g.qcow2 iso/iso-storage-pod:/storage/windows/

# Cleanup
kubectl delete pod pvc-export-windows11 -n shalb-dev --wait=true
```

## Step 8: VM Metrics Limitations

### Windows vs Linux Guest Agent Differences

⚠️ **IMPORTANT**: Windows VMs have limited metrics compared to Linux VMs due to QEMU guest agent platform differences.

#### Missing Windows Metrics

The following KubeVirt metrics are **NOT available** for Windows VMs:

- `kubevirt_vmi_guest_load_*` - Load average (Linux concept, not available on Windows)
- `kubevirt_vmi_filesystem_*` - Limited filesystem reporting compared to Linux
- CPU statistics via `guest-get-cpustats` - Linux-specific command
- Disk statistics via `guest-get-diskstats` - Linux-specific command

#### Available Windows Metrics

- Basic VM info (`kubevirt_vmi_info`)
- Memory metrics (limited)
- Network metrics
- VM lifecycle metrics

#### Root Cause

Windows QEMU guest agents don't support the same statistical commands as Linux:

**Linux Guest Agent**: ✅ `guest-get-cpustats`, `guest-get-diskstats`, `guest-get-load`
**Windows Guest Agent**: ❌ These commands are not implemented

This is a **platform limitation**, not a configuration issue. The UI backend should handle these differences gracefully.

#### Workarounds

1. **Accept Limited Metrics**: Windows VMs will show fewer metrics in monitoring dashboards
2. **Use Hypervisor Metrics**: Rely on host-level metrics instead of guest agent metrics
3. **Alternative Monitoring**: Consider Windows Performance Counters or WMI for detailed Windows-specific metrics

## Troubleshooting

### Common Issues

1. **3.4GB Disk Size**: Use the disk sizing fix (Step 3) - REQUIRED for local-path StorageClass
2. **Clone Stuck at CloneInProgress**: Stop source VM first (`kubectl delete vm <source-vm> --wait=true`)
3. **CloneSourceInUse Error**: Source PVC is being used by running VM - stop it before cloning
4. **ISO Upload Failed**: Nginx filesystem is read-only - use temporary upload pod method
5. **Boot Issues**: Ensure UEFI firmware and proper boot order
6. **Network Issues**: Verify Multus network exists in target namespace (use `shalb-dev/default`)
7. **VirtIO Drivers**: Load storage drivers during Windows installation
8. **Limited Metrics**: Windows VMs show fewer metrics than Linux VMs due to guest agent limitations

### Verification Commands

```bash
# Check DataVolume status
kubectl get dv -n shalb-dev

# Check VM status
kubectl get vm,vmi -n shalb-dev

# Check disk size in VM
kubectl exec -n shalb-dev <virt-launcher-pod> -c compute -- \
  qemu-img info /var/run/kubevirt-private/vmi-disks/windows11-disk/disk.img

# Check network configuration
kubectl describe vmi windows11-vm -n shalb-dev | grep -A 10 "Networks:"
```

## Files Summary

### Core Infrastructure
- `hack/windows/iso-namespace.yaml` - ISO namespace with OVN subnet
- `hack/windows/iso-storage-pvc-iso-ns.yaml` - PVC for ISO storage (20Gi)
- `hack/windows/nginx-iso-server-iso-ns.yaml` - HTTP server for serving ISOs
- `hack/windows/iso-ingress-iso-ns.yaml` - Ingress for ISO access
- `hack/windows/iso-upload-pod.yaml` - Temporary pod for uploading ISOs

### VM Manifests
- `hack/windows/windows11-vm.yaml` - Complete Windows VM manifest
- `hack/windows/windows11-enterprise-vm.yaml` - Windows 11 Enterprise VM example
- `hack/windows/win11-enterprise-custom.yaml` - Enterprise VM with SSH and cloud-init
- `hack/windows/windows11-golden-vm.yaml` - Golden image VM for templating

### Disk Sizing Fix (Required for local-path)
- `hack/windows/pvc-init-windows11-disk.yaml` - Disk sizing fix helper
- `hack/windows/pvc-init-windows11-enterprise-disk.yaml` - Enterprise disk fix helper

### OpenSSH Integration
- `hack/windows/install-openssh-windows.ps1` - Manual OpenSSH installation script
- SSH key injection via QEMU Guest Agent and cloud-init
- Automated service configuration and firewall rules

### Golden Image Templates
- `examples/virtual-machine/windows11-golden-datasource.yaml` - Golden image DataSource
- `examples/virtual-machine/windows11-from-golden.yaml` - Clone from golden image

## Performance Optimization

After installation and driver setup:
- Switch OS disk bus from `sata` to `virtio`
- Use Block-mode StorageClass if available (avoids disk.img overhead)
- Consider memory ballooning and CPU pinning for production workloads

## Security Considerations

- Enable SecureBoot and TPM for Windows 11 compliance
- Use sysprep for golden images to avoid SID conflicts
- Consider network policies for VM isolation
- Regular Windows updates and antivirus in VMs
