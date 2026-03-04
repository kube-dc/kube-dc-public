# Managing VM Lifecycle

This guide covers VM lifecycle operations — starting, stopping, restarting, pausing, and deleting virtual machines.

## Prerequisites

- A [Virtual Machine](creating-vm.md) created in your project
- [CLI access](cli-kubeconfig.md) configured for kubectl/virtctl methods

---

## Start, Stop, and Restart

### Via Console UI

1. Navigate to **Virtual Machines** in your project
2. Click on your VM name to open the details page
3. Use the action buttons:
   - **Start** — boot a stopped VM
   - **Stop** — gracefully shut down a running VM
   - **Restart** — reboot a running VM

The UI shows the current VM status (`Running`, `Stopped`, `Starting`, etc.) and updates automatically.

### Via kubectl

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

This performs a graceful reboot — sends ACPI shutdown signal to the guest OS, waits for it to terminate, then starts it again.

:::tip Restart vs Stop+Start
`virtctl restart` maintains the VM's state and is faster than manually stopping and starting. Use it for applying kernel updates or recovering from hung processes.
:::

### Check VM Status

```bash
# List VMs with status
kubectl get vm

# Get detailed status
kubectl get vm ubuntu -o jsonpath='{.status.printableStatus}'

# Watch status changes
kubectl get vm ubuntu -w
```

Common statuses:
- `Running` — VM is fully booted and ready
- `Stopped` — VM is powered off
- `Starting` — VM is booting up
- `Stopping` — VM is shutting down
- `Paused` — VM is frozen in memory

---

## Pause and Unpause

Pausing a VM freezes its state in memory — useful for temporarily suspending a VM without fully shutting it down. Resume is instant.

### Via Console UI

From the VM details page:
- **Pause** — freeze the VM (CPU stops, memory preserved)
- **Unpause** — resume from paused state

### Via kubectl/virtctl

```bash
# Pause a running VM
virtctl pause ubuntu

# Resume a paused VM
virtctl unpause ubuntu
```

Check if a VM is paused:

```bash
kubectl get vmi ubuntu -o jsonpath='{.status.conditions[?(@.type=="Paused")].status}'
```

:::note Pause vs Stop
- **Pause** preserves exact memory state — instant resume, but consumes memory
- **Stop** shuts down the OS — slow boot, but frees all resources
:::

---

## Delete a VM

Deleting a VM removes the VirtualMachine resource and terminates the running instance. DataVolumes are preserved by default.

### Via Console UI

1. Navigate to **Virtual Machines**
2. Click on the VM name
3. Click **Delete**
4. Confirm the deletion

### Via kubectl

```bash
# Delete the VM
kubectl delete vm ubuntu

# Delete VM and wait for termination
kubectl delete vm ubuntu --wait=true
```

### Clean Up DataVolumes

VM deletion does **not** automatically delete DataVolumes (disk images). To fully remove all VM data:

```bash
# List DataVolumes
kubectl get dv

# Delete the VM's root disk
kubectl delete dv ubuntu-root

# Delete all DataVolumes for a VM
kubectl delete dv -l vm.name=ubuntu
```

:::warning Data Loss
Deleting DataVolumes is permanent. Ensure you have backups before removing disk images.
:::

### Force Delete a Stuck VM

If a VM won't delete due to finalizers:

```bash
# Remove finalizers
kubectl patch vm ubuntu -p '{"metadata":{"finalizers":null}}' --type=merge

# Force delete
kubectl delete vm ubuntu --force --grace-period=0
```

---

## Lifecycle via KubeVirt Subresources

Kube-DC uses KubeVirt's subresources API for VM operations. You can interact with these directly:

```bash
# Start (API endpoint)
kubectl get --raw "/apis/subresources.kubevirt.io/v1/namespaces/my-project/virtualmachines/ubuntu/start" -X PUT

# Stop (API endpoint)
kubectl get --raw "/apis/subresources.kubevirt.io/v1/namespaces/my-project/virtualmachines/ubuntu/stop" -X PUT

# Restart (API endpoint)
kubectl get --raw "/apis/subresources.kubevirt.io/v1/namespaces/my-project/virtualmachines/ubuntu/restart" -X PUT

# Pause (VMI subresource)
kubectl get --raw "/apis/subresources.kubevirt.io/v1/namespaces/my-project/virtualmachineinstances/ubuntu/pause" -X PUT

# Unpause (VMI subresource)
kubectl get --raw "/apis/subresources.kubevirt.io/v1/namespaces/my-project/virtualmachineinstances/ubuntu/unpause" -X PUT
```

:::note
Most users should use `virtctl` instead of raw API calls — it handles errors better and provides clearer output.
:::

---

## Graceful Shutdown

VMs use ACPI shutdown signals for graceful termination. The guest OS receives a shutdown request and can cleanly unmount filesystems before powering off.

### Configure Termination Grace Period

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

### VM Won't Start

```bash
# Check VM status and events
kubectl describe vm ubuntu

# Check DataVolume readiness
kubectl get dv

# View VMI logs
virtctl logs ubuntu
```

Common causes:
- DataVolume not ready (still downloading image)
- Insufficient node resources (CPU/memory)
- Invalid cloud-init configuration

### VM Won't Stop

```bash
# Check if VMI still exists
kubectl get vmi ubuntu

# Force stop by deleting VMI
kubectl delete vmi ubuntu --force --grace-period=0

# Then patch VM to stopped state
kubectl patch vm ubuntu --type merge -p '{"spec":{"running":false}}'
```

### VM Stuck in "Starting"

```bash
# Check pod status
kubectl get pods -l vm.kubevirt.io/name=ubuntu

# View pod events
kubectl describe pod virt-launcher-ubuntu-xxxxx

# Check node capacity
kubectl describe node <node-name>
```

---

## Next Steps

- [Connecting to VMs](connecting-vm.md) — Access methods (SSH, VNC, console)
- [Creating VMs](creating-vm.md) — Deploy new virtual machines
- [Service Exposure](service-exposure.md) — Expose VM services externally
