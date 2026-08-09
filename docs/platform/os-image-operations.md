---
title: OS-image operations
description: How OS images and golden images are mirrored, produced, distributed, gated and promoted — for Linux and for Windows.
---

# OS-image operations — canonical operator guide

This is the operator-facing guide to how a tenant gets a working VM disk. It covers
both halves of the system and both producers. The S3 mirror machinery itself
(discovery adapters, refresh/gc CronJobs, bucket layout) is documented separately in
[Managing OS images](managing-os-images.md); this page is about the pipeline that turns
those images into something a tenant can boot in seconds.

## The two halves, and why both exist

**The mirror** copies upstream OS images into our own S3 bucket (`cdi-os-images`) on a
schedule and rebuilds the catalog the UI dropdown reads. It exists so that creating a VM
never depends on quay.io or a distro mirror being up — and so restricted or air-gapped
installs work at all.

**The goldens** turn those images into **ready-to-clone snapshots** on Ceph. Creating a
VM is then not a download: it is a snapshot clone, near-instant, costing almost no space
until the guest starts writing.

You need both. The mirror alone still leaves every VM creation waiting on a multi-GiB
disk write. The goldens alone leave you depending on the internet to rebuild them.

## The golden contract — the one thing that must not drift

A golden is a **Ready `VolumeSnapshot`** carrying:

| Key | Kind | Meaning |
|---|---|---|
| `kube-dc.com/golden-os` | label | OS family id (also accepted as the `os-family-id` annotation) |
| `kube-dc.com/os-family-id` | annotation | canonical family id, matches the chart catalog entry |
| `kube-dc.com/os-name` | annotation | display name on the Admin → OS Images page |
| `kube-dc.com/golden-mode` | label/annotation | `Block` (RWX-Block, live-migratable) or `Filesystem` (default; assumed if absent) |
| `kube-dc.com/golden-active` | label | blue/green selector — see [Promotion](#promotion-bluegreen) |
| `kube-dc.com/clone-min-size` | annotation | stamped by the per-project seeder; the floor a clone PVC must request |

They live in `golden-images` (Filesystem) and `golden-images-block` (Block).

`ui/backend/controllers/goldenImages.js` is a *pure transform* over exactly this — it has
no idea how any golden was produced. That separation is the design: **producers vary
enormously; the contract does not.** A Windows golden built by a two-hour VM installation
and a Debian golden built by a five-minute `qemu-img convert` appear identically in the
UI and the create-VM flow, and neither required a line of UI change.

If you add a producer, produce this. Do not extend the contract to describe your
producer.

## Producer 1 — Linux (conversion)

Distros publish *cloud images*: already installed, already generalised. The pipeline only
changes format and snapshots them. Three chained Flux Kustomizations over
`platform/rbd-vm-block/incluster/`, with a per-cluster overlay choosing which OSes:

```
import    CDI DataVolume from a digest-pinned containerdisk   wait:true
convert   Block PVC + privileged qemu-img Job                 dependsOn import
snapshot  VolumeSnapshot                                      dependsOn convert
```

Adding an OS is three files in `clusters/<cluster>/rbd-vm-block/{import,convert,snapshot}`.
Distribution is *rebuild per cluster* from immutable inputs — nothing large crosses a
network boundary, and every cluster converges on an equivalent result.

## Producer 2 — Windows (bake)

Microsoft does not publish a cloud image. There is only an installer ISO, so we
manufacture the equivalent: boot a VM, run Setup unattended, install the guest agent,
OpenSSH and cloudbase-init, then `sysprep /generalize /oobe /shutdown`.

```
remaster  rebuild the ISO with efisys_noprompt.bin        hack/windows/automated/remaster-noprompt-iso.yaml
bake      VM installs Windows unattended, then sysprep    hack/windows/automated/build-vm.yaml
verify    assert the disk is genuinely sealed             (offline, see below)
export    compressed qcow2 -> image bucket                hack/windows/automated/export-golden.yaml
```

### Run it

```bash
# 0. credentials (they live in kube-dc, the build namespace needs its own copy)
kubectl -n kube-dc get secret cdi-os-images -o yaml \
  | sed 's/namespace: kube-dc/namespace: golden-images-build/' | kubectl apply -f -

# 1. remaster the ISO once per ISO revision — creates its own namespace, reads S3
kubectl apply -f hack/windows/automated/remaster-noprompt-iso.yaml
kubectl -n golden-images-build logs -f job/win11-iso-remaster

# 2. per-build answer file (carries a build-time password, so never commit it)
export WIN_BUILD_ADMIN_PASSWORD='...'
envsubst < hack/windows/automated/autounattend.xml > /tmp/autounattend.xml
kubectl -n golden-images-build create secret generic win11-sysprep \
  --from-file=autounattend.xml=/tmp/autounattend.xml

# 3. bake (~2h20m on SSD-backed Ceph)
kubectl apply -f hack/windows/automated/build-vm.yaml

# 4. export the sealed disk -> s3://cdi-os-images/windows/11/<date>/windows11-x64-golden.qcow2
kubectl apply -f hack/windows/automated/export-golden.yaml
```

Add `--from-literal=apply-updates.enabled=1` to the sysprep Secret for the monthly
refresh build; it is deliberately absent otherwise.

### Bake on fast storage only

Measured 2026-08-09: on SSD/NVMe-backed Ceph the bake takes **~2h20m** (~90m WIM apply,
~25m specialize, ~20m in-guest bootstrap). On an HDD-backed cluster the same bake ran at
roughly **1% per 40 minutes** — days, not hours, because a Windows install is
small-random-write heavy.

**Bake where storage is fast; distribute the artifact everywhere else.** A cluster on
spinning disks is a golden *consumer*, not a producer.

### Verifying a seal — "Stopped" is not "sealed"

A VM reporting `Running` and `Ready` tells you nothing about whether the build is alive,
and a VM that reached `Stopped` may have died with `C:\BUILD_FAILED` on disk. Verify
offline, with the VM halted:

```bash
# mount the Windows partition (offset from `fdisk -l` on disk.img; sector*512)
mount -t ntfs-3g -o ro,force,loop,offset=$((1312768*512)) /golden/disk.img /mnt/w
```

Four assertions, all of which must hold:

- the partition begins with an **NTFS** signature, not `-FVE-FS-` (BitLocker)
- `C:\BUILD_FAILED` is **absent**
- `C:\kube-dc-bootstrap.ps1` is **absent** — the reboot-resume task must not ship
- `C:\bootstrap.log` ends on the `sysprep /generalize /oobe /shutdown` line

While a bake is running, the console is the only honest progress signal;
`hack/windows/automated/classify-screen.py` classifies a VNC screenshot as
`setup` / `firmware` / `bootprompt` so you can tell "installing" from "wedged in the edk2
boot manager" without reading it yourself.

## Distribution

Measured: a sealed Windows golden holds ~7.7 GiB of real data on a 62.6 GiB virtual disk
and compresses to a **6.35 GiB** qcow2. Import on a receiving cluster takes ~15 minutes.

| Route | Verdict |
|---|---|
| Rebuild per cluster | Fine for Linux. For Windows it needs the ISO on every cluster, ~2.5h, and fast storage |
| Containerdisk in the zot depot | **Rejected** — a ~23 GiB blob defeated the zot S3 driver |
| **qcow2 in the image bucket, imported by CDI** | **Use this** — proven cross-cluster, ~6 GiB, minutes |

Receiving cluster:

```yaml
apiVersion: cdi.kubevirt.io/v1beta1
kind: DataVolume
metadata:
  name: windows-11-golden-<date>
  namespace: golden-images
  labels: { kube-dc.com/golden-os: windows-11-golden }
spec:
  source:
    http:
      url: "http://rook-ceph-rgw-my-store.rook-ceph.svc/cdi-os-images/windows/11/<date>/windows11-x64-golden.qcow2"
  storage:
    accessModes: [ReadWriteOnce]
    volumeMode: Filesystem
    storageClassName: rbd-vm
    resources: { requests: { storage: 75Gi } }
```

Then snapshot that PVC with the contract labels above. Name goldens **versioned**
(`golden-windows-11-golden-2026-08-09`) so a new one can sit beside the incumbent.

> **BYOL.** Our Windows golden carries **no `<ProductKey>`** — it is an unactivated
> Enterprise Evaluation install, and cloudbase-init lets a tenant apply their own licence.
> Never publish an activated, volume-licensed, KMS-configured or key-bearing image to the
> anonymously readable bucket. Licensed media belongs behind a private prefix with a
> per-cluster credential first.

## Gating

Promotion must never happen on inspection alone. `hack/windows/automated/validate-clone.sh`
clones the candidate exactly as a tenant would and checks the boot contract:

```bash
KEEP=1 MEM=8Gi ./hack/windows/automated/validate-clone.sh <project-ns> <snapshot> 45
```

- **Run it in a project namespace.** `golden-images` enforces PodSecurity `baseline` and
  KubeVirt needs `privileged`; do not relax a Flux-managed system namespace to force it.
- **`MEM` must be ≥ 8Gi** (the catalog `minMemory`). A failure below that is
  inconclusive, not a regression.
- **Use `KEEP=1`** when you may need to diagnose — the default deletes the VM on exit,
  taking the evidence with it.

To gate a candidate that the seeder has not selected, publish it into the project
namespace the way the seeder does: a cluster-scoped pre-provisioned
`VolumeSnapshotContent` (`deletionPolicy: Retain`) pointing at the same `snapshotHandle`,
plus a namespaced `VolumeSnapshot` bound to it.

## Promotion (blue/green)

Goldens are selected by `kube-dc.com/golden-active`. Exactly one per family may carry
`"true"` — `activeGoldenConflicts()` rejects more, and `preferActive()` sorts an active
golden ahead of the rest.

With **two goldens in a family and neither active**, selection falls back to list order
rather than intent. That is safe (the incumbent keeps serving) but it is ambiguous, and it
is the reason the label exists. Set it deliberately, only after the gate passes:

```bash
kubectl -n golden-images label volumesnapshot golden-windows-11-golden-<date> \
  kube-dc.com/golden-active=true
kubectl -n golden-images label volumesnapshot golden-windows-11-golden \
  kube-dc.com/golden-active- --ignore-not-found
```

Rollback is the same two commands with the names swapped. Projects pick the change up on
the next Project resync (15 min).

## Operational traps

Every one of these cost real time and none is discoverable from the symptom.

**A completed pod still holds its PVCs.** A finished Job or a stopped inspection pod keeps
its volumes attached, and the DataVolume you recreate comes back with an *empty phase* and
`ErrResourceMarkedForDeletion`. Delete the pod or Job, not just the DataVolume.

**Windows installer ISOs are ISO9660+UDF hybrids.** `xorriso -extract` reads only the
ISO9660 side and silently yields a 135-byte stub. Loop-mount them.

**Removing the boot prompt changes boot order semantics.** The "Press any key" timeout was
what let the machine fall through to the disk after Setup's first reboot. With a no-prompt
ISO the CD boots unconditionally, so the **disk must be `bootOrder: 1`** or Setup
reinstalls forever — visible only as the percentage going *down*.

**Windows 11 24H2 encrypts the OS volume during OOBE** on any machine with a TPM and
Secure Boot, which every build VM has. The result is a golden sealed to the build
machine's TPM. `PreventDeviceEncryption` in the `specialize` pass prevents it; the seal
gate refuses to publish an encrypted volume.

**Do not blocklist an RBD client to clear a stale lock.** krbd shares a client session per
node, so blocklisting cuts RBD mapping for every workload on that node
(`rbd: map failed: (108)`). Remove the entry with `ceph osd blocklist rm` if you already
did it.

**Failed bakes leave 68Gi images behind.** On a small cluster they fill the OSD, Ceph
marks all pools full, and then released PVs *cannot* self-delete — freeing an RBD image
needs metadata writes that the full flag blocks. Break the deadlock by raising
`full-ratio` to 0.97, deleting the released PVs, then **restoring 0.95/0.90 in the same
session**.

## Current state (2026-08-09)

**Verified end-to-end:** the remastered ISO boots into Setup with zero keypresses on two
different clusters; disk-first boot order survives Setup's reboots; offline Win32-OpenSSH
installs in ~1 minute without touching Windows Update; `PreventDeviceEncryption` keeps the
volume NTFS; the bake seals itself and the seal verifies offline; export → cross-cluster
ship → import → snapshot works and produces a byte-identical 6.35 GiB artifact; a clone of
the candidate provisions instantly and boots to a connected guest agent reporting
Windows 11 build 26100.

**Open:** the clone gate fails at PHASE 1 because RDP 3389 never answered within 45
minutes. Note the gate reports this as "genuine boot failure", which is wrong here — the
guest agent connected on the same VM, so Windows did boot. Either RDP is not coming up in
the image or it is not reachable from where the gate probes; until that is resolved
**nothing has been promoted** and the incumbent golden still serves every project. The
gate should also check the agent before RDP, since the agent is the stronger and faster
boot proof.
