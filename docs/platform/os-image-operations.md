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
> anonymously readable bucket.
>
> **The `private/` prefix is NOT that protection.** It is a naming convention and
> nothing more. Verified 2026-08-27 on `s3.stage.kube-dc.com`: an anonymous `GET` of
> `private/windows/11/<date>/windows11-x64-golden.qcow2` returned HTTP 200 with the
> full 7.3 GB body. The bucket is public-read throughout, so exporting a licensed
> image "to the private prefix" publishes it to the world. Licensed media needs a
> bucket whose policy actually denies anonymous reads, imported with a CDI
> `secretRef` — and beware that a Rook OBC drops a hand-applied bucket policy on
> reconcile.

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

## Promotion — prefer a GoldenImageSelection

`GoldenImageSelection` names the one golden a family serves. It exists because the
`kube-dc.com/golden-active` label **cannot express a promotion atomically** — see the
section below for what each intermediate state actually does. One object per
(family, mode) makes promotion a single reviewed write, and makes "which image is this
cluster serving?" answerable from Git rather than from labels somebody has to remember
to move in the right order.

```yaml
apiVersion: kube-dc.com/v1
kind: GoldenImageSelection
metadata:
  name: windows-11-golden-filesystem
  namespace: golden-images
spec:
  family: windows-11-golden          # matches kube-dc.com/golden-os on the snapshots
  mode: Filesystem                   # Filesystem | Block — Kubernetes' volumeMode spelling
  snapshotName: golden-windows-11-20260828-f0dea74da9c5
  releaseDigest: "sha256:f0dea74da9c54ba4728b2f4841316490cadd66b7ab1ed10de46549d457eb5018"
```

Promoting a new golden is then: commit the new versioned DataVolume + VolumeSnapshot
(staged, serving nothing), let every cluster import and gate it, then change
`snapshotName` in a second commit. Rollback is the same field, back.

**It fails closed.** A family whose selection names a missing or unready snapshot is
**skipped**, not served from a sibling. Skipping is safe — projects already seeded keep
the copy they have — whereas falling back is the silent wrong-image seeding the type
exists to prevent. Two selections naming different snapshots for one family are
reported and the family held, never resolved by guessing.

**Adoption is optional and order-independent.** A cluster with no CRD and no selections
behaves exactly as before, on the label path, so the manager and the CRD can roll out in
either order without breaking golden seeding.

> `mode` is spelled `Filesystem`/`Block`. It is compared case-insensitively, but a
> selection whose family or mode matches nothing governs nothing — which looks
> identical to having no selection at all. Check `status.ready` rather than assuming.

## Promotion via the golden-active label (legacy)

Goldens are selected by `kube-dc.com/golden-active`. Exactly one per family may carry
`"true"` — `activeGoldenConflicts()` rejects more, and `preferActive()` sorts an active
golden ahead of the rest.

With **two goldens in a family and neither active**, selection falls back to list order
rather than intent. That is safe (the incumbent keeps serving) but it is ambiguous, and it
is the reason the label exists. Set it deliberately, only after the gate passes.

**Neither intermediate state is safe, and no ordering makes one safe.** Read what the
code actually does (`internal/project/res_golden_snapshots.go`) before trusting any
sequence:

- **Zero active.** `preferActive()` is a `sort.SliceStable`, so with nothing labelled it
  preserves *API list order* — which is not a selection contract. Projects seed from
  whichever sibling the API returned first, and **no error is raised**. Silent and
  arbitrary.
- **Two active.** `activeGoldenConflicts()` produces an error, but the caller only
  *appends it to a list* and the loop keeps going: seeding still proceeds, against an
  arbitrary one of the two. Earlier revisions of this guide claimed seeding "fails for
  the whole family" here. It does not.

So the two states differ in whether anyone is told, not in whether the right image is
chosen. Run the two commands back to back, then verify each project resolved to the
handle you intended rather than assuming the sequence protected you.

> The durable fix is a single Git-owned pointer per (family, mode) naming the chosen
> snapshot, so promotion is one write with no intermediate state.

```bash
# 1. stand down the incumbent (family briefly unlabelled — arbitrary AND silent; keep this window short)
kubectl -n golden-images label volumesnapshot golden-windows-11-golden-<old-date> \
  kube-dc.com/golden-active- --ignore-not-found

# 2. promote the candidate
kubectl -n golden-images label volumesnapshot golden-windows-11-golden-<date> \
  kube-dc.com/golden-active=true
```

Rollback is the same two commands with the names swapped — same order, remove then add.
Projects pick the change up on the next Project resync (15 min).

## Disk sizing — three links, and all three are required

A tenant who asks for a 200 GB Windows VM used to receive the bake size. Not
approximately: **exactly** the golden's size, on every cluster. The request was real at
the storage layer and invisible inside the guest.

Tenant VMs are native rbd **CoW restores** from a golden VolumeSnapshot, in
`Filesystem` volume mode. The restore honours the requested PVC size, but the
`disk.img` *inside* that PVC is whatever the golden had. Three separate things must
line up before a customer sees their disk, and if any one is missing the guest
silently keeps the bake size — which is exactly why this went unnoticed for so long:
each piece looks healthy on its own.

| # | Link | Where it lives | Failure signature |
|---|------|----------------|-------------------|
| 1 | The image is grown to fill its PVC | KubeVirt `ExpandDisks` feature gate | Guest disk equals the golden's size, whatever the PVC says |
| 2 | Nothing sits after `C:` on the disk | The bake removes the WinRE partition (v13+) | `Get-PartitionSupportedSize -DriveLetter C` reports `SizeMax == current` |
| 3 | The filesystem is extended into the space | cloudbase-init `ExtendVolumesPlugin` | Disk is large, `C:` is not, free space is unallocated |

**Link 1 — `ExpandDisks`.** Must be in the KubeVirt CR's feature gates
(`platform/kubevirt/kubevirt-cr.yaml`). virt-launcher then expands the image before the
VM starts, and says so:

```
pre-start expansion of image /var/run/kubevirt-private/vmi-disks/rootdisk/disk.img
  to size 98767470592
```

That log line is the quickest way to confirm the gate is doing its job.

**Link 2 — the WinRE partition.** Windows Setup appends a ~600 MB Recovery partition at
the END of the disk, *after* `C:`, even though our answer file only asks for
EFI + MSR + C:(Extend). Windows cannot extend a partition past a following one, so that
600 MB in the wrong place strands every byte beyond it — permanently, on every clone.
The bake now runs `reagentc /disable` (moving WinRE into `C:\Windows\System32\Recovery`,
so this costs the recovery *image*, not the recovery *feature*) and deletes the
partition before sysprep.

**Link 3 — `ExtendVolumesPlugin`.** Already in the golden's plugin list, in both the
unattend and service passes, with `volumes_to_extend` unset so it extends every volume.

Verified end-to-end on stage 2026-08-28, a v13 golden cloned into a 100Gi PVC:

```
golden C:      66.6 GB
guest  C:      98.1 GB      <- after all three links
```

> **local-path caveat.** That provisioner does not enforce PVC size, so with
> `ExpandDisks` on, a guest can grow its image toward a request the node cannot
> satisfy. The image is sparse (nothing is allocated up front) and the ceiling is what
> the customer asked for, but this is the same class of exposure as the 2026-06-12
> node-disk outage. Watch it on local-path-backed VMs specifically.

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
