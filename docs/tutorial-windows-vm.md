# Windows 11 VM Tutorial - Complete Setup Guide

This comprehensive guide covers the complete process of setting up Windows 11 VMs in KubeVirt, from infrastructure setup to golden image creation and deployment.

## Overview

This tutorial provides two deployment methods:

1. **Golden Image Deployment** (Recommended) - Deploy pre-configured VMs in 5-10 minutes
2. **Fresh Installation** - Create custom Windows installations with full control

## Prerequisites

- KubeVirt and CDI installed and running
- Multus CNI with OVN network configured
- StorageClass `local-path` available
- Ingress controller for HTTP access to ISOs

## Step 1: Create ISO Hosting Environment

### 1.1 Create Dedicated Namespace

```yaml
# hack/windows/iso-namespace.yaml
apiVersion: v1
kind: Namespace
metadata:
  name: iso
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
      storage: 30Gi  # Increased for Windows ISO + golden images
  storageClassName: local-path
```

### 1.3 HTTP Server for ISOs

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
        
        location ~* \.(iso|img|qcow2)$ {
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

### 1.4 Ingress Configuration with TLS

```yaml
# hack/windows/iso-ingress-iso-ns.yaml
apiVersion: networking.k8s.io/v1
kind: Ingress
metadata:
  name: iso-server-ingress
  namespace: iso
  annotations:
    cert-manager.io/cluster-issuer: letsencrypt-prod-http
    nginx.ingress.kubernetes.io/proxy-body-size: "0"
    nginx.ingress.kubernetes.io/proxy-read-timeout: "3600"
    nginx.ingress.kubernetes.io/proxy-send-timeout: "3600"
    nginx.ingress.kubernetes.io/proxy-buffering: "off"
spec:
  ingressClassName: nginx
  tls:
  - hosts:
    - iso.stage.kube-dc.com
    secretName: iso-server-tls
  rules:
  - host: iso.stage.kube-dc.com
    http:
      paths:
      - path: /
        pathType: Prefix
        backend:
          service:
            name: iso-server
            port:
              number: 80
```

### 1.5 Deploy Infrastructure

```bash
# Deploy all infrastructure components
kubectl apply -f hack/windows/iso-namespace.yaml
kubectl apply -f hack/windows/iso-storage-pvc-iso-ns.yaml
kubectl apply -f hack/windows/nginx-iso-server-iso-ns.yaml
kubectl apply -f hack/windows/iso-ingress-iso-ns.yaml

# Verify deployment
kubectl get pods -n iso
kubectl get ingress -n iso
```

## Step 2: Download and Upload Windows ISO

### 2.1 Download Windows 11 Enterprise ISO

1. Visit: https://www.microsoft.com/en-us/evalcenter/download-windows-11-enterprise
2. Select: **ISO – Enterprise download 64-bit edition** (90-day evaluation)  
3. Download the ISO file (approximately 5.4GB)

### 2.2 Download VirtIO Drivers

```bash
# Download latest VirtIO drivers
wget https://fedorapeople.org/groups/virt/virtio-win/direct-downloads/stable-virtio/virtio-win.iso
```

### 2.3 Upload Files to Cluster

```bash
# Create temporary upload pod
kubectl apply -f hack/windows/iso-upload-pod.yaml

# Wait for pod to be ready
kubectl wait --for=condition=Ready pod/iso-upload-pod -n iso --timeout=60s

# Upload Windows 11 ISO (replace with your actual filename)
kubectl cp ~/Win11_24H2_EnglishInternational_x64.iso iso/iso-upload-pod:/storage/win11-x64.iso

# Upload VirtIO drivers
kubectl cp ~/virtio-win.iso iso/iso-upload-pod:/storage/virtio-win.iso

# Upload OpenSSH installation script
kubectl cp hack/windows/install-openssh-windows.ps1 iso/iso-upload-pod:/storage/install-openssh-windows.ps1

# Clean up upload pod
kubectl delete pod iso-upload-pod -n iso --wait=true

# Verify files are accessible
curl -I https://iso.stage.kube-dc.com/win11-x64.iso
curl -I https://iso.stage.kube-dc.com/virtio-win.iso
```

## Step 3: Fresh Windows Installation

### 3.1 Deploy Installation VM

Use the complete VM manifest that includes all required DataVolumes:

```bash
# Deploy Windows VM with installation ISOs
kubectl apply -f hack/windows/windows11-vm.yaml

# Monitor DataVolume download progress
kubectl get dv -n shalb-dev

# Check VM status
kubectl get vm,vmi -n shalb-dev | grep windows11
```

### 3.2 Windows Installation Process

```bash
# Access VM console via VNC
virtctl vnc windows11-vm -n shalb-dev

# Or use VNC proxy
virtctl vnc windows11-vm -n shalb-dev --proxy-only --port 5900
# Then connect VNC client to localhost:5900
```

**Installation Steps:**

1. **Boot from Windows ISO**: VM boots from Windows installer (bootOrder: 1)

2. **Load VirtIO Drivers**: 
   - When prompted for disk drivers, click "Load driver"
   - Browse to VirtIO drivers CDROM
   - Navigate to `/amd64/w11/` folder
   - Install **VirtIO SCSI controller** drivers (for disk access)
   - **DO NOT install network drivers yet** (to create local account)

3. **Install Windows**: 
   - Select the 60GB VirtIO disk for installation
   - Complete Windows 11 setup with local account

4. **Post-Installation**:
   
   - Install remaining VirtIO drivers from CDROM (network, balloon, RNG)
   - Install QEMU Guest Agent from VirtIO drivers CDROM
   - Run Windows Updates

### 3.3 Configure SSH and RDP

After Windows installation, configure SSH and RDP access:

```powershell
# Method 1: Download and run script
Invoke-WebRequest -Uri "https://iso.stage.kube-dc.com/install-openssh-windows.ps1" -OutFile "install-openssh-windows.ps1"
Set-ExecutionPolicy -ExecutionPolicy RemoteSigned -Scope CurrentUser
.\install-openssh-windows.ps1

# Method 2: Direct execution (bypass execution policy)
PowerShell -ExecutionPolicy Bypass -Command "Invoke-Expression (Invoke-WebRequest -Uri 'https://iso.stage.kube-dc.com/install-openssh-windows.ps1').Content"
```

**Script Features:**

- ✅ Installs OpenSSH Server using Windows capabilities
- ✅ Configures SSH service for automatic startup  
- ✅ Opens SSH port 22 in Windows Firewall (all network profiles)
- ✅ Enables Remote Desktop (port 3389)
- ✅ Enables ICMP ping (IPv4 and IPv6)
- ✅ Provides detailed verification and status reporting

## Step 4: Create Golden Image

### 4.1 Prepare VM for Golden Image

```bash
# 1. Inside Windows VM, run Sysprep (optional but recommended)
# Navigate to: C:\Windows\System32\Sysprep\sysprep.exe
# Options: Generalize, Enter System Out-of-Box Experience (OOBE), Shutdown

# 2. Stop the source VM (CRITICAL for export)
kubectl patch vm windows11-vm -n shalb-dev --type merge -p '{"spec":{"runStrategy":"Halted"}}'
kubectl wait --for=delete vmi/windows11-vm -n shalb-dev --timeout=300s
```

### 4.2 Export to QCOW2 Golden Image

```yaml
# hack/windows/export-golden-image.yaml
apiVersion: v1
kind: Pod
metadata:
  name: export-golden-image
  namespace: shalb-dev
spec:
  restartPolicy: Never
  containers:
    - name: exporter
      image: ubuntu:22.04
      command: ["sh", "-c"]
      args:
        - |
          set -e
          apt-get update && apt-get install -y qemu-utils curl
          cd /pvc
          
          echo "=== Disk Information ==="
          DISK_SIZE_BYTES=$(qemu-img info --output=json disk.img | grep '"virtual-size"' | cut -d: -f2 | tr -d ' ,')
          DISK_SIZE_GB=$((DISK_SIZE_BYTES / 1024 / 1024 / 1024))
          echo "Source disk: ${DISK_SIZE_GB}GB (${DISK_SIZE_BYTES} bytes)"
          
          echo "=== Converting to compressed QCOW2 ==="
          qemu-img convert -p -O qcow2 -c disk.img windows11-x64-golden.qcow2
          
          echo "=== Final Golden Image ==="
          ls -lh windows11-x64-golden.qcow2
          qemu-img info windows11-x64-golden.qcow2
          
          echo "=== Upload to ISO server ==="
          # Upload to ISO server storage
          curl -T windows11-x64-golden.qcow2 http://nginx-iso-server.iso.svc.cluster.local/windows11-x64-golden.qcow2 || echo "Upload failed, manual copy required"
          
          echo "=== Export completed, sleeping for inspection ==="
          sleep 3600
      volumeMounts:
        - name: source-disk
          mountPath: /pvc
  volumes:
    - name: source-disk
      persistentVolumeClaim:
        claimName: windows11-disk
```

### 4.3 Export Process

```bash
# Export golden image
kubectl apply -f hack/windows/export-golden-image.yaml
kubectl wait --for=condition=Ready pod/export-golden-image -n shalb-dev --timeout=120s

# Monitor export progress
kubectl logs -n shalb-dev export-golden-image -f

# Manual copy to ISO server (if curl upload fails)
kubectl cp shalb-dev/export-golden-image:/pvc/windows11-x64-golden.qcow2 /tmp/
kubectl cp /tmp/windows11-x64-golden.qcow2 iso/iso-upload-pod:/storage/

# Verify golden image is available
curl -I https://iso.stage.kube-dc.com/windows11-x64-golden.qcow2

# Clean up export pod
kubectl delete pod export-golden-image -n shalb-dev --wait=true
```

## Step 5: Deploy from Golden Image

### 5.1 Golden Image Deployment (Recommended)

```bash
# Deploy VM from golden image
kubectl apply -f hack/windows/win11-x64.yaml

# Create SSH key secret for key injection
kubectl create secret generic authorized-keys-default \
  --from-file=key1=~/.ssh/id_rsa.pub \
  -n shalb-dev

# Monitor deployment
kubectl get vm,vmi,dv -n shalb-dev | grep win11-x64

# Get VM IP when ready
kubectl get vmi win11-x64 -n shalb-dev -o jsonpath='{.status.interfaces[0].ipAddress}'

# SSH to VM (once guest agent is ready)
ssh kube-dc@<vm-ip>
```

### 5.2 Golden Image Benefits

| Aspect | Golden Image | Fresh Install |
|--------|--------------|---------------|
| **Deployment Time** | 5-10 minutes | 30+ minutes |
| **Download Size** | 21.3GB compressed | 5.4GB + drivers |
| **Configuration** | Pre-configured | Manual setup required |
| **SSH/RDP** | Ready immediately | Requires script execution |
| **VirtIO Drivers** | Pre-installed | Manual installation |
| **Use Case** | Production deployment | Custom configurations |

## Step 6: Troubleshooting

### 6.1 Common Issues

**DataVolume stuck in ImportScheduled:**
```bash
# Check CDI importer pods
kubectl get pods -n cdi
kubectl logs -n cdi <importer-pod>

# Check storage provisioner
kubectl get pods -n local-path-storage
```

**VM won't start - CPU resources:**
```bash
# Check node resources
kubectl describe nodes | grep -A 10 "Allocated resources"

# Reduce VM CPU if needed
kubectl patch vm <vm-name> -n <namespace> --type merge -p '{"spec":{"template":{"spec":{"domain":{"cpu":{"cores":2}}}}}}'
```

**SSH not working:**
```bash
# Check guest agent connection
kubectl describe vmi <vm-name> -n <namespace> | grep -i agent

# Test network connectivity
kubectl run test-pod --image=nicolaka/netshoot --rm -it -- ping <vm-ip>
kubectl run test-pod --image=nicolaka/netshoot --rm -it -- nc -zv <vm-ip> 22
```

**Storage issues with local-path:**
```bash
# Check local-path provisioner
kubectl get pods -n local-path-storage
kubectl logs -n local-path-storage <provisioner-pod>

# Note: local-path doesn't support Block mode, use Filesystem mode
```

### 6.2 Verification Commands

```bash
# Check all Windows VMs
kubectl get vm -A | grep -i win

# Check DataVolume progress
kubectl get dv -n <namespace>

# Access VM console
virtctl vnc <vm-name> -n <namespace>

# Check VM resource usage
kubectl top pods -n <namespace> | grep virt-launcher
```

## Required Manifests Summary

**Infrastructure (Step 1):**
- `hack/windows/iso-namespace.yaml` - ISO namespace
- `hack/windows/iso-storage-pvc-iso-ns.yaml` - Storage for ISOs
- `hack/windows/nginx-iso-server-iso-ns.yaml` - HTTP server
- `hack/windows/iso-ingress-iso-ns.yaml` - Ingress configuration

**Utilities:**
- `hack/windows/iso-upload-pod.yaml` - Upload files to cluster
- `hack/windows/install-openssh-windows.ps1` - SSH/RDP configuration script

**VM Deployment:**
- `hack/windows/windows11-vm.yaml` - Fresh installation VM
- `hack/windows/win11-x64.yaml` - Golden image deployment

**Golden Image Creation:**
- `hack/windows/export-golden-image.yaml` - Export VM to QCOW2
- `hack/windows/windows11-enterprise-golden-datasource.yaml` - DataSource for cloning

**Optional:**
- `hack/windows/windows11-from-datasource.yaml` - Deploy from DataSource (same namespace)
- `hack/windows/pvc-init-windows11-disk.yaml` - Fix disk size issues if needed

## Available Resources

Once deployed, the following resources are available:

- **Windows 11 ISO**: `https://iso.stage.kube-dc.com/win11-x64.iso` (5.4GB)
- **VirtIO Drivers**: `https://iso.stage.kube-dc.com/virtio-win.iso` (700MB)
- **SSH Script**: `https://iso.stage.kube-dc.com/install-openssh-windows.ps1` (5KB)
- **Golden Image**: `https://iso.stage.kube-dc.com/windows11-x64-golden.qcow2` (21.3GB)

## Security Considerations

- **SSH Keys**: Use KubeVirt accessCredentials for secure key injection
{{ ... }}
- **Network Policies**: Implement Kubernetes network policies for VM isolation
- **Sysprep**: Run before creating golden images to avoid SID conflicts
- **Firewall**: Script configures Windows Firewall appropriately for SSH, RDP, and ICMP

---

**✅ Complete Windows 11 VM infrastructure with golden image support ready for production use!**
