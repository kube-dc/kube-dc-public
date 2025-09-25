# Managing OS Images in Kube-DC

This guide explains how to manage operating system images in the Kube-DC platform, including adding new OS options, modifying existing configurations, and updating the system.

## Overview

OS images in Kube-DC are configured through a Kubernetes ConfigMap that defines:
- Available operating systems in the VM creation UI
- Default resource requirements (memory, CPU, storage)
- Firmware and virtualization settings
- Cloud-init configurations
- Image URLs and user credentials

## Architecture

```
┌─────────────────┐    ┌──────────────────┐    ┌─────────────────┐
│   Helm Chart    │───▶│   ConfigMap      │───▶│  Backend API    │
│   Template      │    │ images-configmap │    │ /os-images      │
└─────────────────┘    └──────────────────┘    └─────────────────┘
                                │
                                ▼
                       ┌─────────────────┐
                       │  Frontend UI    │
                       │ Create VM Modal │
                       └─────────────────┘
```

## ConfigMap Structure

The OS images are defined in `/charts/kube-dc/templates/os-images-configmap.yaml`:

```yaml
apiVersion: v1
kind: ConfigMap
metadata:
  name: images-configmap
  namespace: {{ .Release.Namespace }}
data:
  images.yaml: |
    images:
      - OS_NAME: "Ubuntu 24.04"
        CLOUD_USER: ubuntu
        OS_IMAGE_URL: "https://cloud-images.ubuntu.com/noble/current/noble-server-cloudimg-amd64.img"
        MIN_MEMORY: "1G"
        MIN_VCPU: "1"
        MIN_STORAGE: "20G"
        FIRMWARE_TYPE: "bios"
        MACHINE_TYPE: "q35"
        FEATURES: "acpi"
        CLOUD_INIT: |
          #cloud-config
          package_update: true
          packages:
            - qemu-guest-agent
```

## Configuration Fields

### Required Fields

| Field | Description | Example |
|-------|-------------|---------|
| `OS_NAME` | Display name in UI dropdown | `"Ubuntu 24.04"` |
| `CLOUD_USER` | Default SSH user for the OS | `ubuntu` |
| `OS_IMAGE_URL` | HTTP URL to the disk image | `https://example.com/image.qcow2` |

### Resource Requirements

| Field | Description | Example | Notes |
|-------|-------------|---------|-------|
| `MIN_MEMORY` | Minimum RAM requirement | `"8G"`, `"512M"` | Supports G/M suffixes |
| `MIN_VCPU` | Minimum CPU cores | `"2"` | String format |
| `MIN_STORAGE` | Minimum disk size | `"60G"` | Supports G suffix |

### Virtualization Settings

| Field | Description | Options | Notes |
|-------|-------------|---------|-------|
| `FIRMWARE_TYPE` | Boot firmware | `"bios"`, `"efi"` | EFI required for Windows 11 |
| `MACHINE_TYPE` | QEMU machine type | `"q35"`, `"pc-q35-rhel8.6.0"` | Specific types for compatibility |
| `FEATURES` | Virtualization features | `"acpi"`, `"hyperv,acpi,apic,smm,tpm"` | Comma-separated list |

### Supported Features

- `acpi` - Advanced Configuration and Power Interface
- `apic` - Advanced Programmable Interrupt Controller  
- `hyperv` - Microsoft Hyper-V enlightenments
- `smm` - System Management Mode
- `tpm` - Trusted Platform Module (required for Windows 11)

## OS-Specific Configurations

### Linux Distributions

**Ubuntu/Debian:**
```yaml
- OS_NAME: "Ubuntu 24.04"
  CLOUD_USER: ubuntu
  MIN_MEMORY: "1G"
  MIN_VCPU: "1"
  MIN_STORAGE: "20G"
  FIRMWARE_TYPE: "bios"
  MACHINE_TYPE: "q35"
  FEATURES: "acpi"
```

**CentOS/RHEL:**
```yaml
- OS_NAME: "CentOS Stream 9"
  CLOUD_USER: centos
  MIN_MEMORY: "2G"
  MIN_VCPU: "1"
  MIN_STORAGE: "20G"
  FIRMWARE_TYPE: "bios"
  MACHINE_TYPE: "q35"
  FEATURES: "acpi"
```

### Windows Systems

**Windows 11:**
```yaml
- OS_NAME: "Windows 11 Enterprise"
  CLOUD_USER: Administrator
  MIN_MEMORY: "8G"
  MIN_VCPU: "4"
  MIN_STORAGE: "60G"
  FIRMWARE_TYPE: "efi"
  MACHINE_TYPE: "pc-q35-rhel8.6.0"
  FEATURES: "hyperv,acpi,apic,smm,tpm"
```

## Adding a New OS Image

### Step 1: Prepare the Image

1. **Obtain the disk image** (qcow2, raw, or vmdk format)
2. **Host the image** on an HTTP server accessible to your cluster
3. **Test the image** to ensure it boots correctly

### Step 2: Update the ConfigMap

Edit `/charts/kube-dc/templates/os-images-configmap.yaml`:

```yaml
# Add your new OS entry
- OS_NAME: "Fedora 40"
  CLOUD_USER: fedora
  OS_IMAGE_URL: "https://download.fedoraproject.org/pub/fedora/linux/releases/40/Cloud/x86_64/images/Fedora-Cloud-Base-40-1.14.x86_64.qcow2"
  MIN_MEMORY: "2G"
  MIN_VCPU: "1"
  MIN_STORAGE: "25G"
  FIRMWARE_TYPE: "bios"
  MACHINE_TYPE: "q35"
  FEATURES: "acpi"
  CLOUD_INIT: |
    #cloud-config
    package_update: true
    packages:
      - qemu-guest-agent
    runcmd:
      - systemctl enable --now qemu-guest-agent
```

### Step 3: Deploy the Changes

**Option A: Helm Upgrade (Recommended)**
```bash
# From the project root
helm upgrade kube-dc ./charts/kube-dc -n kube-dc
```

**Option B: Direct ConfigMap Update**
```bash
# Apply the ConfigMap directly
kubectl apply -f charts/kube-dc/templates/os-images-configmap.yaml
```

### Step 4: Reload the Backend

The backend caches OS images for performance. After updating the ConfigMap:

```bash
# Restart the backend to reload the cache
kubectl rollout restart deployment/kube-dc-backend -n kube-dc

# Or wait for the cache TTL (30 seconds) to expire
```

## Modifying Existing OS Images

### Inline Editing

You can modify the ConfigMap directly in Kubernetes:

```bash
# Edit the ConfigMap in your cluster
kubectl edit configmap images-configmap -n kube-dc
```

**Example: Increase Windows 11 memory requirement:**
```yaml
# Change from:
MIN_MEMORY: "8G"
# To:
MIN_MEMORY: "16G"
```

After saving, restart the backend:
```bash
kubectl rollout restart deployment/kube-dc-backend -n kube-dc
```

### Updating Image URLs

If an image URL changes or becomes unavailable:

1. **Update the ConfigMap:**
```yaml
# Old URL
OS_IMAGE_URL: "https://old-server.com/ubuntu-24.04.qcow2"
# New URL  
OS_IMAGE_URL: "https://new-server.com/ubuntu-24.04.qcow2"
```

2. **Apply changes:**
```bash
kubectl apply -f charts/kube-dc/templates/os-images-configmap.yaml
kubectl rollout restart deployment/kube-dc-backend -n kube-dc
```

## Testing Changes

### Verify ConfigMap Update

```bash
# Check the ConfigMap was updated
kubectl get configmap images-configmap -n kube-dc -o yaml

# Test the API endpoint
curl -s "https://backend.stage.kube-dc.com/api/create-vm/your-namespace/os-images" | jq '.[].OS_NAME'
```

### Test in UI

1. Open the Kube-DC web interface
2. Navigate to **Create VM**
3. Check the **Operation System** dropdown
4. Verify your new OS appears with correct parameters
5. Select the OS and confirm memory/CPU/storage auto-populate

## Troubleshooting

### OS Not Appearing in UI

**Check the ConfigMap:**
```bash
kubectl describe configmap images-configmap -n kube-dc
```

**Verify backend logs:**
```bash
kubectl logs -n kube-dc deployment/kube-dc-backend --tail=50
```

**Common issues:**
- YAML syntax errors in ConfigMap
- Backend cache not refreshed
- Network connectivity to image URL

### VM Creation Fails

**Check image accessibility:**
```bash
# Test if the image URL is reachable
curl -I "https://your-image-url.com/image.qcow2"
```

**Verify resource requirements:**
- Ensure cluster has sufficient resources
- Check storage class availability
- Verify network policies allow image downloads

### Backend Cache Issues

The backend caches OS images for 30 seconds. To force refresh:

```bash
# Restart backend pods
kubectl rollout restart deployment/kube-dc-backend -n kube-dc

# Or wait for cache expiration (30 seconds)
```

## Best Practices

### Image Management

1. **Use stable URLs** - Avoid URLs that change frequently
2. **Host images reliably** - Use CDNs or reliable hosting
3. **Test images** - Verify images boot before adding to production
4. **Document changes** - Keep track of image versions and changes

### Resource Requirements

1. **Set realistic minimums** - Don't under-provision resources
2. **Consider workload** - Different use cases need different resources
3. **Test performance** - Verify VMs perform well with set resources

### Security

1. **Verify image sources** - Only use trusted image providers
2. **Scan images** - Check for vulnerabilities before deployment
3. **Use HTTPS** - Always use secure URLs for image downloads
4. **Regular updates** - Keep OS images updated with security patches

## Advanced Configuration

### Custom Cloud-Init

For complex initialization requirements:

```yaml
CLOUD_INIT: |
  #cloud-config
  users:
    - name: admin
      groups: sudo
      shell: /bin/bash
      sudo: ALL=(ALL) NOPASSWD:ALL
  packages:
    - docker.io
    - nginx
  runcmd:
    - systemctl enable docker
    - systemctl start docker
    - docker run -d -p 80:80 nginx
```

### Windows-Specific Settings

For Windows VMs, additional configuration may be needed:

```yaml
# Windows Server 2022
- OS_NAME: "Windows Server 2022"
  CLOUD_USER: Administrator
  MIN_MEMORY: "4G"
  MIN_VCPU: "2"
  MIN_STORAGE: "80G"
  FIRMWARE_TYPE: "efi"
  MACHINE_TYPE: "pc-q35-rhel8.6.0"
  FEATURES: "hyperv,acpi,apic,smm,tpm"
  BOOT_ORDER: "cdrom,disk"
  ADDITIONAL_DISKS: "virtio-drivers"
```

## API Reference

The OS images are served via the backend API:

**Endpoint:** `GET /api/create-vm/{namespace}/os-images`

**Response format:**
```json
[
  {
    "OS_NAME": "Ubuntu 24.04",
    "CLOUD_USER": "ubuntu",
    "OS_IMAGE_URL": "https://cloud-images.ubuntu.com/...",
    "MIN_MEMORY": "1G",
    "MIN_VCPU": "1",
    "MIN_STORAGE": "20G",
    "FIRMWARE_TYPE": "bios",
    "MACHINE_TYPE": "q35",
    "FEATURES": "acpi",
    "CLOUD_INIT": "#cloud-config\n..."
  }
]
```

## Support

For additional help:
- Check the [Kube-DC documentation](../README.md)
- Review [troubleshooting guides](./troubleshooting.md)
- Open an issue in the project repository
