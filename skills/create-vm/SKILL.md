---
name: create-vm
description: Deploy a virtual machine in a Kube-DC project with SSH access, cloud-init configuration, and optional external IP exposure. Supports Ubuntu, Debian, and Windows images.
---

## Prerequisites
- Target project must exist and be Ready
- Project namespace: `{org}-{project}`
- SSH keypair secrets auto-exist in project namespace

## Steps

### 1. Create DataVolume (Boot Disk)

```yaml
apiVersion: cdi.kubevirt.io/v1beta1
kind: DataVolume
metadata:
  name: {vm-name}-disk
  namespace: {project-namespace}
spec:
  source:
    http:
      url: "{image-url}"
  storage:
    accessModes: [ReadWriteOnce]
    resources:
      requests:
        storage: {disk-size}    # e.g. 20Gi
    storageClassName: local-path
```

See @vm-template.yaml for the complete VM + DataVolume manifest.

### 2. Create VirtualMachine

Key requirements:
- **Network**: MUST use `networkName: {namespace}/default` with Multus bridge
- **Guest agent**: MUST install `qemu-guest-agent` in cloud-init
- **SSH keys**: Reference `authorized-keys-default` via `accessCredentials`

### 3. Wait for VM Ready

```bash
kubectl get vm {vm-name} -n {project-namespace} -w
kubectl get vmi {vm-name} -n {project-namespace} -o jsonpath='{.status.interfaces[0].ipAddress}'
```

### 4. SSH Access

```bash
# Extract project SSH private key
kubectl get secret ssh-keypair-default -n {project-namespace} \
  -o jsonpath='{.data.id_rsa}' | base64 -d > /tmp/vm_ssh_key
chmod 600 /tmp/vm_ssh_key

# Get VM IP
VM_IP=$(kubectl get vmi {vm-name} -n {project-namespace} \
  -o jsonpath='{.status.interfaces[0].ipAddress}')

# SSH in
ssh -i /tmp/vm_ssh_key {os-user}@$VM_IP
```

**Default OS users**: `ubuntu` (Ubuntu), `debian` (Debian), `kube-dc` (Windows)

### 5. External Access (Optional)

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

| OS | Image URL | Default User |
|----|-----------|--------------|
| Ubuntu 24.04 | `docker.io/shalb/ubuntu-2404-container-disk:v1.4.2` | `ubuntu` |
| Debian 12 | `docker.io/shalb/debian-12-container-disk:v1.4.2` | `debian` |

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
