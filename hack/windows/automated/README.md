# Automated Windows 11 golden build + containerdisk + monthly refresh

**Status: authored, NOT yet run.** These manifests turn the manual, VNC-driven
Windows golden build (`docs/platform/windows-vm-setup.md`) into a hands-off,
repeatable pipeline. The one thing that still fundamentally needs a human is
**providing a licensed Windows 11 ISO** (see §4) — everything after that is
automated. Validate end-to-end on a non-prod cluster before wiring the CronJob.

## Can we build + regularly update a Windows containerdisk? — assessment

Yes, with the pieces here:

| Blocker in the manual flow | Fixed by |
|---|---|
| Interactive Setup/OOBE at the VNC console | `autounattend.xml` (fully unattended: driver load, partition, edition, local admin, OOBE skip) |
| VirtIO SCSI driver load at disk-selection | `autounattend.xml` windowsPE `DriverPaths` |
| Win11 TPM/SecureBoot/RAM gate | `autounattend.xml` `LabConfig` bypass (build VM also provides real vTPM+secureBoot) |
| OpenSSH / qemu-guest-agent by hand | `FirstLogonCommands` → `install-openssh-windows.ps1` + qemu-ga MSI |
| No monthly patching | `apply-updates.ps1` (PSWindowsUpdate) gated by `WIN_APPLY_UPDATES=1` |
| Golden is a raw qcow2, never a containerdisk | `package-containerdisk.yaml` (`crane append`, `FROM scratch` + `/disk`) → zot |

## Flow

```
 (human, once/quarter)   Windows 11 Enterprise EVAL ISO  ──┐
 virtio-win.iso (auto)  ─────────────────────────────────┐ │
                                                          v v
 stage inputs ──► iso-server (nginx) + build a config-drive ISO
   (autounattend.xml + install-openssh-windows.ps1 + apply-updates.ps1;
    envsubst ${WIN_BUILD_ADMIN_PASSWORD}, ${DOMAIN})
                          │
                          v
 build-vm.yaml  ──►  KubeVirt VM boots ISO, Setup auto-reads autounattend.xml,
                     installs unattended, FirstLogon (ssh+ga+updates), sysprep
                     /generalize /shutdown  ──►  VMI stops on its own
                          │
                          v
 export (existing export-golden-image.yaml pattern): qemu-img convert -c ->
   windows11-x64-golden.qcow2  ──►  S3 windows/11/latest/  (cdi-os-mirror rotates it)
                          │
             ┌────────────┴─────────────┐
             v                          v
   http/S3 golden (tenant VMs,   package-containerdisk.yaml (opt-in):
   the default, per PRD)         crane -> registry.${DOMAIN}/cdi-os/windows-11@<digest>
```

## Regular (monthly) update

Two options, both reuse the same manifests:

1. **CronJob** (recommended once validated): monthly, run the build VM with
   `WIN_APPLY_UPDATES=1`, wait for the VMI to halt, export, then
   `package-containerdisk.yaml`. Because the base install is unattended, no human
   is in the loop for the *refresh* — only the initial ISO is human-provided, and
   the same ISO is reused until a new Windows feature release. A skeleton CronJob
   is intentionally left out until the flow is validated on non-prod (a broken
   unattended build should not silently loop on a schedule).
2. **On-demand**: `kubectl apply -f build-vm.yaml` → watch → export → apply
   `package-containerdisk.yaml`. Good for the first run + feature-release rebuilds.

## Human-in-the-loop (what can't be automated)

- **The Windows ISO + license.** Microsoft's 90-day **Enterprise Evaluation** ISO
  (no product key) is fine for a golden and is what the manual flow uses; it must
  be downloaded + staged by a human (licensing, not a technical blocker). A new ISO
  is only needed on a Windows *feature* release (e.g. 24H2 → next), not monthly.
- **First validation run.** The `autounattend.xml` edition index / virtio driver
  paths (`E:\...\w11\amd64`) should be confirmed against the exact ISO + virtio-win
  version once, over VNC, before trusting the unattended path.

## Files

| File | Role |
|---|---|
| `autounattend.xml` | fully-unattended Windows Setup answer file (the key missing piece) |
| `build-vm.yaml` | KubeVirt build VM + the 3 input DataVolumes (installer/virtio/golden disk) |
| `apply-updates.ps1` | PSWindowsUpdate monthly patching (gated by `WIN_APPLY_UPDATES`) |
| `package-containerdisk.yaml` | wrap the exported qcow2 as a containerdisk → zot (opt-in) |
| `../install-openssh-windows.ps1` | reused verbatim for OpenSSH/RDP enablement |
| `../export-golden-image.yaml` | qcow2 export → iso-server (see caveat below) |
| `validate-clone.sh` | **release gate** — clone/boot/agent/RDP; must exit 0 before promoting |

## Why Windows stays http/S3 for tenants (containerdisk is opt-in)

Per `docs/prd/vm-startup-acceleration.md`: spegel serves a blob from a *single*
peer, so one 23 GiB Windows containerdisk pull saturates one NIC. Tenant Windows
VMs use the http/S3 golden (instant path via the RBD snapshot seeder like the Linux
OSes). The containerdisk exists for operators who explicitly want
registry/`pullMethod: node` distribution for Windows.

## Export caveat (read before the first run)

`../export-golden-image.yaml` is the ORIGINAL manual-flow manifest: it mounts the
`windows11-disk` PVC in the **`shalb-dev`** namespace and uploads to the in-cluster
`iso-server`. It does **not** match this pipeline's namespace
(`golden-images-build`), disk name (`win11-golden-disk`), or the S3 mirror path.
Before the first automated bake, adapt it (namespace + `claimName` + destination)
or replace it with a direct S3 upload to
`s3://cdi-os-images/windows/11/<date>/windows11-x64-golden.qcow2`. It now fails
closed on a failed upload, so a broken export can no longer look successful.

After export, promote with the **blue/green** procedure (stage → `validate-clone.sh`
→ flip `kube-dc.com/golden-active`) in `docs/platform/os-image-operations.md`.

## Assets are baked, not configmapped

The cloudbase-init MSI (~20 MB) and the in-guest scripts live in
**`images/windows-bake-assets`**, built into a KubeVirt **containerDisk** and
attached to the build VM as a CDROM. Kubernetes ConfigMaps/Secrets cap at ~1 MiB
so the MSI physically cannot ship that way, and downloading it inside the guest
would make a bake depend on upstream availability + guest egress and would not be
reproducible. Fetching happens once, at image build time, with the hash recorded
into the image; `bootstrap.ps1` re-verifies it in the guest and refuses to
continue on mismatch.

The only Kubernetes object carrying build content is a ~6 KB Secret holding the
per-build `autounattend.xml` (it embeds a build-time password, so it cannot be
baked into a shared image).

Build + pin:

```sh
docker build -t shalb/kube-dc-windows-bake-assets:v1 images/windows-bake-assets
docker push shalb/kube-dc-windows-bake-assets:v1
# then pin the digest in hack/windows/automated/build-vm.yaml
```
