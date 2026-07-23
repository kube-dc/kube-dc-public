# Dedicated GPU VM guest setup

Dedicated GPU VMs attach one whole physical GPU to one virtual machine. They
provide a stronger workload boundary than Shared GPU containers, but they are
not live-migratable. Planned maintenance requires a shutdown and restart, and a
later start can wait for capacity or receive a different physical device.

This guide documents the first pilot candidates. It does not make an image or
driver generally supported before the platform operator completes the fresh
guest qualification checklist at the end of this page.

## Pilot support matrix

| Guest | Driver candidate | Product state |
|---|---|---|
| Ubuntu 24.04 LTS x86_64 | NVIDIA Data Center R580, proprietary kernel modules | Documented pilot candidate; guest proof pending |
| Windows 11 Enterprise x86_64 golden image | NVIDIA Data Center R580 Windows driver | Documented pilot candidate; guest proof pending |
| Windows 11 fresh-install image | Same candidate after VirtIO and OS setup | Installation-only image; not a pilot support target |
| Other catalog images | Not selected | Unsupported until separately qualified |

R580 is the candidate because NVIDIA's R580 release notes include V100 and
Linux/Windows packages. The V100 is Volta, so Linux must use NVIDIA's
**proprietary** kernel modules; NVIDIA supports its open kernel modules only on
Turing and newer GPUs. Operators must publish an exact approved package version
and checksum before enabling the GPU VM creation gate. Do not install a vGPU
guest package for a whole-device VM.

See NVIDIA's current [driver installation guide](https://docs.nvidia.com/datacenter/tesla/driver-installation-guide/),
[kernel-module matrix](https://docs.nvidia.com/datacenter/tesla/driver-installation-guide/kernel-modules.html),
and [R580 data-center release notes](https://docs.nvidia.com/datacenter/tesla/tesla-release-notes-580-105-08/)
when qualifying a newer package.

## Before creating the VM

You need all of the following:

- organization entitlement and a project device cap with at least one device
  of headroom;
- the platform's independent Dedicated GPU VM creation gate enabled;
- compatible physical capacity, which is quota rather than a reservation;
- an approved guest image and exact driver package from the operator;
- enough root storage for the OS, driver, and application.

In the VM wizard, select the approved **Dedicated GPU VM** profile. The console
turns live migration off and the API rejects edited manifests that re-enable it,
select a node, or name a device outside the catalog. If capacity is occupied,
the VM can remain pending until another holder stops.

## Ubuntu 24.04 walkthrough

1. Create an Ubuntu 24.04 VM with one Dedicated GPU VM device and wait for the
   VM status to become **Running**.
2. Connect over SSH or the web console. Confirm that the guest sees an NVIDIA
   controller before installing software:

   ```bash
   sudo apt-get update
   sudo apt-get install -y pciutils build-essential "linux-headers-$(uname -r)"
   lspci -nn | grep -Ei 'NVIDIA|3D controller|VGA'
   ```

3. Download the exact R580 Ubuntu 24.04 local-repository package approved by
   the operator and verify its published SHA-256 checksum. Then enable that
   repository, replacing both placeholders with the approved values:

   ```bash
   sha256sum nvidia-driver-local-repo-ubuntu2404-<version>_amd64.deb
   sudo dpkg -i nvidia-driver-local-repo-ubuntu2404-<version>_amd64.deb
   sudo cp /var/nvidia-driver-local-repo-ubuntu2404-<version>/nvidia-driver-*-keyring.gpg /usr/share/keyrings/
   sudo apt-get update
   ```

4. Pin R580 and install the proprietary driver. Do not substitute
   `nvidia-open`; it does not support Volta:

   ```bash
   sudo apt-get install -y nvidia-driver-pinning-580
   sudo apt-get install -y cuda-drivers
   sudo reboot
   ```

5. After reconnecting, verify the loaded driver and device:

   ```bash
   cat /proc/driver/nvidia/version
   nvidia-smi --query-gpu=name,driver_version,memory.total --format=csv,noheader
   nvidia-smi -q -d ECC
   ```

`nvidia-smi` proves enumeration and driver initialization; it does not prove
that an application is compatible. Run the operator's pinned CUDA smoke test
and the intended application test before treating the image as qualified. Do
not install a new CUDA major merely because the driver supports it: V100
application/toolkit compatibility is a separate decision.

## Windows 11 walkthrough

1. Create the approved Windows 11 Enterprise golden-image VM with one
   Dedicated GPU VM device. Wait for **Running**, then connect through the web
   console or RDP.
2. Open **Device Manager** and confirm an NVIDIA display/3D controller is
   present. A warning icon before driver installation is expected.
3. Download the exact operator-approved R580 Data Center Windows package from
   NVIDIA. Verify its digital signature and the operator-published checksum.
   Do not use a GRID/vGPU guest package.
4. Run the installer as Administrator. For the documented silent display-driver
   installation, open an elevated PowerShell in the extracted package directory:

   ```powershell
   $process = Start-Process -FilePath .\setup.exe -ArgumentList '-s', '-n', 'Display.Driver' -Wait -PassThru
   $process.ExitCode
   Restart-Computer
   ```

   Exit code `0` means success and `1` means success with a reboot required.
   Any other code fails qualification; retain the installer logs rather than
   repeatedly reinstalling.

5. After the reboot, open an elevated PowerShell and verify the device:

   ```powershell
   nvidia-smi.exe --query-gpu=name,driver_version,memory.total --format=csv,noheader
   Get-PnpDevice -Class Display | Format-Table Status,Class,FriendlyName
   ```

The first pilot is compute-only. Do not promise remote graphics acceleration,
DirectX/OpenGL workstation behavior, or vGPU features from the passthrough
product. A pinned CUDA application smoke test is still required after
`nvidia-smi` succeeds.

## Lifecycle and maintenance

| Action | Device behavior |
|---|---|
| Create/start | The request consumes quota and can wait; a device attaches only after scheduling succeeds |
| Running | The device and quota remain allocated to the VM instance |
| Pause | The device remains allocated; pause does not release capacity |
| Guest reboot | The current VM instance normally keeps its attachment |
| Console restart | Compatible capacity is required; never depend on the same physical identity |
| Stop | The device and native quota release after the VM instance terminates |
| Delete | The device releases asynchronously; disk deletion follows the normal VM choices |
| Platform maintenance | Stop the VM, wait for release, and start it after compatible capacity returns |

Live migration is intentionally unavailable for this product. KubeVirt rejects
a migration request for an attached host device, and Kube-DC also requires the
effective eviction strategy to remain non-migrating. Shared storage protects
the disk but does not make the attached GPU migratable. KubeVirt's
[host-device guide](https://kubevirt.io/user-guide/compute/host-devices/) and
[live-migration limitations](https://kubevirt.io/user-guide/compute/live_migration/)
describe the underlying behavior.

## Troubleshooting

- **VM remains Pending:** quota may be valid while every compatible device is
  busy. Leave it queued or stop it to release the request.
- **Controller is absent in the guest:** stop retrying driver installation and
  contact the operator; this is an attachment/platform problem.
- **Kernel module will not load on Linux:** confirm matching running-kernel
  headers and that the proprietary, not open, module package was installed.
- **`nvidia-smi` reports no devices:** capture the guest OS, driver package,
  kernel/version, and command error. Do not include host node names or device
  identifiers in a tenant support ticket.
- **VM stopped during maintenance:** start it only after the operator restores
  compatible capacity. It can receive a different device.

## Fresh qualification checklist

Operators must record this evidence separately for Ubuntu and Windows before
changing either row to supported:

1. exact immutable image version/digest and OS build;
2. exact signed driver package version and SHA-256 checksum;
3. successful first boot, install, required reboot, and second boot;
4. `nvidia-smi` model, driver, memory, ECC, and zero-error result;
5. pinned CUDA allocation, kernel execution, synchronization, and result check;
6. stop releases the device, start reattaches it, and verification still passes;
7. a second VM queues when capacity is exhausted and starts after release;
8. live migration is rejected and planned stop/start recovery is accepted;
9. deletion releases quota/device state with no stale holder;
10. support owner, rollback procedure, and driver/image update policy.

Until both walkthroughs have retained evidence, Dedicated GPU VM creation must
remain limited to operator-controlled validation rather than tenant beta.
