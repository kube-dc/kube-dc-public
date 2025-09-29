# OS Images Testing Scripts

This directory contains scripts for testing OS images and guest agent functionality in Kube-DC.

## Scripts

### `generate-os-test-vms.py`
**Main script** - Generates VMs and DataVolumes from the ConfigMap template for testing all OS images.

**Features:**
- Parses OS configurations directly from `charts/kube-dc/templates/os-images-configmap.yaml`
- Single source of truth - no hardcoded values
- Supports all OS types including Linux and Windows
- Automatic resource naming and labeling
- Guest agent and SSH key injection testing

**Usage:**
```bash
# Create all test VMs and DataVolumes
python3 generate-os-test-vms.py [namespace] create

# Create/update specific OS VMs (partial name matching)
python3 generate-os-test-vms.py [namespace] create [os-filter]

# Check status of test resources
python3 generate-os-test-vms.py [namespace] status

# Delete all test resources
python3 generate-os-test-vms.py [namespace] delete

# Delete specific OS VMs (partial name matching)
python3 generate-os-test-vms.py [namespace] delete [os-filter]

# List available OS images from ConfigMap
python3 generate-os-test-vms.py list
```

**Examples:**
```bash
# Create all VMs
python3 generate-os-test-vms.py shalb-dev create

# Create only Ubuntu VMs
python3 generate-os-test-vms.py shalb-dev create ubuntu

# Create only Windows VMs
python3 generate-os-test-vms.py shalb-dev create windows

# Delete only Fedora VMs
python3 generate-os-test-vms.py shalb-dev delete fedora

# List all available OS images
python3 generate-os-test-vms.py list
```

**Default namespace:** `shalb-dev`

**Idempotent Operation:**
The script is idempotent - running it multiple times will only create/update resources that are missing or have changed configurations. This makes it safe to run repeatedly.

### `check-test-vms.sh`
**Status checker** - Provides detailed status report of VM guest agents and SSH key injection.

**Usage:**
```bash
./check-test-vms.sh
```

**Output:**
- VM status overview
- Detailed guest agent status per OS
- DataVolume import progress
- Production readiness summary

## OS Images Tested

The scripts automatically test all OS images defined in the ConfigMap:

**Linux Distributions:**
- Ubuntu 24.04
- CentOS Stream 9  
- Debian 12 LTS
- Fedora 41
- openSUSE Leap 15.3
- Alpine Linux 3.19
- Gentoo Linux

**Windows:**
- Windows 11 Enterprise (Golden Image)
- Windows 11 Enterprise (Fresh Install)

**Testing Images:**
- CirrOS (minimal, guest agent not supported)

## Configuration

All OS configurations are sourced from:
`charts/kube-dc/templates/os-images-configmap.yaml`

This ensures:
- ✅ Single source of truth
- ✅ No hardcoded values in test scripts  
- ✅ Automatic testing of ConfigMap changes
- ✅ Production-ready configurations

## Test Results

Expected results for production-ready OS images:
- ✅ **Agent Connected**: True
- ✅ **SSH Keys Synced**: True
- ✅ **Guest Agent**: Fully functional
- ✅ **Cloud-Init**: Working correctly

## Cleanup

The scripts use labels to track created resources:
- `app.kubernetes.io/created-by=os-test-script`
- `os-test/os-name=<sanitized-os-name>`

This allows safe cleanup without affecting other resources.

## Quick Start Workflow

### 1. Test All OS Images
```bash
cd /home/voa/projects/kube-dc/tests/iso-images

# Create all test VMs from ConfigMap
python3 generate-os-test-vms.py shalb-dev create

# Check status (run multiple times to monitor progress)
python3 generate-os-test-vms.py shalb-dev status

# Detailed guest agent status
./check-test-vms.sh
```

### 2. Test Specific OS Images
Create, update, or delete specific OS VMs:
```bash
# List available OS images
python3 generate-os-test-vms.py list

# Create only Ubuntu VM
python3 generate-os-test-vms.py shalb-dev create ubuntu

# Create all Windows VMs
python3 generate-os-test-vms.py shalb-dev create windows

# Delete and recreate Fedora VM (useful for testing changes)
python3 generate-os-test-vms.py shalb-dev delete fedora
python3 generate-os-test-vms.py shalb-dev create fedora

# Example output for specific OS:
# [18:09:36] Creating/updating OS test resources for 'ubuntu' in namespace 'shalb-dev'...
# [18:09:36] Found 1 matching OS image(s)
# [18:10:07] Creating DataVolume test-ubuntu-24-04-root...
# [18:10:07] Creating VM test-ubuntu-24-04...
# ✅ Resources synchronized successfully!
# DataVolumes: 1 created, 0 updated
# VMs: 1 created, 0 updated
```

### 3. Test ConfigMap Changes
When you update an OS configuration in the ConfigMap:
```bash
# Run the script again - it will only update changed resources
python3 generate-os-test-vms.py shalb-dev create

# Or update only the changed OS
python3 generate-os-test-vms.py shalb-dev create fedora
```

### 4. Monitor VM Boot Progress
```bash
# Watch DataVolume imports
kubectl get dv -n shalb-dev -l app.kubernetes.io/created-by=os-test-script -w

# Watch VM status
kubectl get vm -n shalb-dev -l app.kubernetes.io/created-by=os-test-script -w

# Check specific VM details
kubectl describe vm test-ubuntu-24-04 -n shalb-dev
```

### 5. Test Guest Agent Functionality
```bash
# Check guest agent connection
kubectl get vmi -n shalb-dev -o jsonpath='{range .items[*]}{.metadata.name}{"\t"}{.status.conditions[?(@.type=="AgentConnected")].status}{"\n"}{end}'

# Test SSH key injection
kubectl get vmi -n shalb-dev -o jsonpath='{range .items[*]}{.metadata.name}{"\t"}{.status.conditions[?(@.type=="AccessCredentialsSynchronized")].status}{"\n"}{end}'
```

### 6. Cleanup
```bash
# Delete all test resources
python3 generate-os-test-vms.py shalb-dev delete

# Delete specific OS VMs only
python3 generate-os-test-vms.py shalb-dev delete ubuntu
python3 generate-os-test-vms.py shalb-dev delete windows

# Verify cleanup
kubectl get vm,dv -n shalb-dev -l app.kubernetes.io/created-by=os-test-script
```

## Troubleshooting

### Common Issues

**DataVolume Import Failures:**
```bash
# Check importer pod logs
kubectl logs -n shalb-dev importer-<vm-name>-root --tail=50

# Common causes: insufficient storage, invalid image URL, network issues
```

**VM Won't Start:**
```bash
# Check VM events
kubectl describe vm <vm-name> -n shalb-dev

# Check virt-launcher pod
kubectl get pods -n shalb-dev | grep virt-launcher-<vm-name>
kubectl logs -n shalb-dev virt-launcher-<vm-name>-<hash> -c compute
```

**Guest Agent Not Connected:**
```bash
# Check VM console (for debugging cloud-init)
virtctl console <vm-name> -n shalb-dev

# Check cloud-init logs inside VM
# (Access via console or SSH if available)
```

### Expected Timing
- **Linux VMs**: 2-5 minutes to boot and connect guest agent
- **Windows VMs**: 10-15 minutes for first boot and setup
- **DataVolume Import**: 1-10 minutes depending on image size and network
