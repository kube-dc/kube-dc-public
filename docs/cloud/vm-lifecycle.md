# Manage the VM lifecycle

This guide covers the VM lifecycle operations: start, stop, restart, pause, and delete.

## Before you begin

- A [Virtual machine](creating-vm.md) created in your project
- [CLI access](cli-kubeconfig.md) configured for kubectl/virtctl methods

---

## Start, stop, and restart

### Start, stop, or restart in the console

1. Navigate to **Virtual Machines** in your project
2. Click on your VM name to open the details page
3. Use the action buttons:
   - **Start**: boot a stopped VM
   - **Stop**: gracefully shut down a running VM
   - **Restart**: reboot a running VM

The console shows the current VM status, such as `Running`, `Stopped`, or `Starting`, and it updates the value automatically.

### Start, stop, or restart with kubectl

#### Start a VM

```bash
# Using virtctl (recommended)
virtctl start ubuntu

# Using kubectl patch
kubectl patch vm ubuntu --type merge -p '{"spec":{"running":true}}'
```

#### Stop a VM

```bash
# Using virtctl (recommended)
virtctl stop ubuntu

# Using kubectl patch
kubectl patch vm ubuntu --type merge -p '{"spec":{"running":false}}'
```

#### Restart a VM

```bash
virtctl restart ubuntu
```

This performs a graceful reboot. It sends an ACPI shutdown signal to the guest OS, waits for the guest to stop, then starts the VM again.

:::tip Restart compared with stop and start
`virtctl restart` reboots the guest. Persistent disks survive, but memory and process state do not. Use **Pause** when you need to preserve memory state temporarily.
:::

:::warning Dedicated GPU VMs
An attached GPU cannot live migrate. Stop releases the device, and a later
start may queue or attach a different physical device. A restart also requires
compatible capacity, so do not rely on stable device identity. See
[Dedicated GPU VM guest setup](gpu-vm-guests.md) for the complete lifecycle.
:::

### Check VM status

```bash
# List VMs with status
kubectl get vm

# Get detailed status
kubectl get vm ubuntu -o jsonpath='{.status.printableStatus}'

# Watch status changes
kubectl get vm ubuntu -w
```

Common statuses:
- `Running`: the virtual machine instance is running; use guest-agent or application health checks to determine guest readiness
- `Stopped`: VM is powered off
- `Starting`: VM is booting up
- `Stopping`: VM is shutting down
- `Paused`: VM is frozen in memory

---

## Pause and unpause

A pause freezes the VM's state in memory. Use it to suspend a VM without a full shutdown. The resume is instant.

### Pause or unpause in the console

From the VM details page:
- **Pause**: freeze the VM (CPU stops, memory preserved)
- **Unpause**: resume from paused state

### Via kubectl/virtctl

```bash
# Pause a running VM
virtctl pause vm ubuntu

# Resume a paused VM
virtctl unpause vm ubuntu
```

Check if a VM is paused:

```bash
kubectl get vmi ubuntu -o jsonpath='{.status.conditions[?(@.type=="Paused")].status}'
```

:::note Pause compared with stop
- **Pause** keeps the exact memory state. The resume is instant, but the VM keeps its memory.
- **Stop** shuts the OS down. The boot is slow, but the VM frees all its resources.
:::

---

## Delete a VM

Deleting a VM removes the VirtualMachine resource and terminates the running instance. DataVolumes are preserved by default.

### Delete in the console

1. Navigate to **Virtual Machines**
2. Click on the VM name
3. Click **Delete**
4. Confirm the deletion

### Delete with kubectl

```bash
# Delete the VM
kubectl delete vm ubuntu

# Delete VM and wait for termination
kubectl delete vm ubuntu --wait=true
```

### Clean up DataVolumes

VM deletion does **not** automatically delete DataVolumes (disk images). To fully remove all VM data:

```bash
# List DataVolumes
kubectl get dv

# Delete the VM's root disk
kubectl delete dv ubuntu-root

# Delete each additional disk only after confirming its name and ownership
kubectl delete dv ubuntu-data
```

:::warning Data Loss
Deleting DataVolumes is permanent. Ensure you have backups before removing disk images.
:::

### A VM is stuck in deletion

Collect `kubectl describe vm <name>`, the related VMI and DataVolume status, and recent events before escalating to your platform operator. Do not remove finalizers from a Project account: bypassing controller cleanup can orphan disks, launcher pods, or network resources.

---

## Lifecycle with virtctl

Use `virtctl` for KubeVirt lifecycle operations in the current Project:

```bash
virtctl start ubuntu -n acme-production
virtctl stop ubuntu -n acme-production
virtctl restart ubuntu -n acme-production
virtctl pause vm ubuntu -n acme-production
virtctl unpause vm ubuntu -n acme-production
```

:::note
These commands use KubeVirt subresources and require the corresponding Project role permission.
:::

---

## Graceful shutdown

VMs use ACPI shutdown signals for graceful termination. The guest OS receives a shutdown request and can cleanly unmount filesystems before powering off.

### Configure termination grace period

```yaml
spec:
  template:
    spec:
      terminationGracePeriodSeconds: 60
```

This gives the VM 60 seconds to shut down gracefully before force termination.

For Windows VMs or VMs with long-running processes, increase this value:

```yaml
terminationGracePeriodSeconds: 300  # 5 minutes
```

---

## Troubleshooting

### VM won't start

```bash
# Check VM status and events
kubectl describe vm ubuntu

# Check DataVolume readiness
kubectl get dv

# Find the launcher pod, then inspect its compute-container log
kubectl get pods -l kubevirt.io/domain=ubuntu
kubectl logs <virt-launcher-pod> -c compute
```

Common causes:
- DataVolume not ready (still downloading image)
- Insufficient node resources (CPU/memory)
- Invalid cloud-init configuration

### The VM does not stop

Request a normal stop and watch both resources:

```bash
kubectl patch vm ubuntu --type merge -p '{"spec":{"running":false}}'
kubectl get vm,vmi ubuntu -w
```

If the VMI remains after the guest shutdown timeout, inspect `kubectl describe
vm ubuntu` and `kubectl describe vmi ubuntu`, then contact support. Force-deleting
a VMI can abruptly terminate the guest, lose unwritten data, and conflict with
KubeVirt reconciliation; it is an operator recovery action, not a routine stop.

### The VM is stuck in "Starting"

```bash
# Check pod status
kubectl get pods -l vm.kubevirt.io/name=ubuntu

# View pod events
kubectl describe pod virt-launcher-ubuntu-xxxxx

# Review Project-visible scheduling events
kubectl get events --sort-by=.lastTimestamp
```

Project roles cannot inspect cluster Nodes. If events report insufficient host
capacity or a platform scheduling failure, send the VM and event details to support.

---

## Next steps

- [Connecting to VMs](connecting-vm.md): Access methods (SSH, VNC, console)
- [Creating VMs](creating-vm.md): Deploy new virtual machines
- [Service exposure](service-exposure.md): Expose VM services externally
