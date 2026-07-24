# VM storage tiers & live migration (operator guide)

Kube-DC offers three storage tiers for KubeVirt VM root disks. This page is for
**cluster operators**: how to choose a tier, how to enable the Ceph-RBD tiers on a
cluster, and how live migration works. For end-user VM creation see
[Creating a VM](/cloud/creating-vm); for the golden-image mirror see
[Managing OS Images](/platform/managing-os-images).

## The three tiers

| Tier | StorageClass / mode | Best for | Trade-off |
|---|---|---|---|
| **Default** | `local-path` (Filesystem, RWO) | single-node & write-sensitive VMs — databases, anything fsync-heavy | node-local (no HA, **not** live-migratable); **fastest durable writes** |
| **Instant-clone** | `rbd-vm` **Filesystem**, RWO | fast boot from a shared golden, HA data | HA, but RWO ⇒ **not** live-migratable |
| **Migratable (HA)** | `rbd-vm` **Block**, RWX | HA **+ live migration** (maintenance, rebalancing) | RWX-Block ⇒ live-migratable, at a **much longer durable-write latency tail** |

**`rbd-vm` is an HA / mobility tier, not a universal performance tier.** On a
representative NVMe node, synchronous 4k fio showed rbd-vm durable writes ≈ **64×
slower** than node-local disk (single-digit-ms vs ~100µs); reads and streaming
throughput are competitive. So the default StorageClass stays `local-path`, and the
UI never silently prefers `rbd-vm`. Put latency-sensitive/fsync-heavy VMs (databases)
on `local-path`.

## How tenants pick a tier: storage-first, not "profiles"

End users don't pick a StorageClass directly, and they don't pick a "tier" by name. The
Console asks the real infrastructure question — **Root disk storage: Local disk vs
Shared RBD** — and treats snapshots + live migration as capabilities of shared storage:

| UI choice | Tier above | Rendered spec |
|---|---|---|
| **Local disk** | Default | cluster default (non-rbd) StorageClass, RWO Filesystem |
| **Shared RBD** (no golden) | — | `rbd-vm` RWO import (CDI DataVolume) |
| **Shared RBD** (golden present) | Instant-clone | `rbd-vm` FS clone from the OS's golden snapshot |
| **Shared RBD + Enable live migration** | Migratable (HA) | `rbd-vm` **RWX Block** clone + `evictionStrategy: LiveMigrate` + pinned `cpu.model` |

**Shared RBD** appears only when the `rbd-vm` StorageClass exists on the cluster; under
it, **provisioning** (prepared-golden clone vs import) is chosen automatically, and the
**Enable live migration** checkbox is offered only when the OS has a *Block* golden
**and** a migration pool exists. The UI renders **explicit KubeVirt resources** — no
mutating webhook, no server-side expansion. See the user-facing walkthrough + generated
manifest in [Creating a VM → Root disk storage](/cloud/creating-vm#root-disk-storage).

**Descriptive labels (non-authoritative).** Rendered VMs and their root PVC/DataVolume
carry `kube-dc.com/vm-profile` (`default` | `shared-rbd` | `instant-clone` |
`migratable`) and `kube-dc.com/storage-tier` (`<class>` | `rbd-vm-fs` | `rbd-vm-block`)
so the choice is greppable in `kubectl`. **Nothing depends on them** — the spec fields
(accessModes/volumeMode/storageClassName, evictionStrategy, cpu.model) are the source
of truth, and KubeVirt computes `LiveMigratable` from those, not from a label. Editing
or dropping a label does not change VM behaviour.

## Enabling the Ceph-RBD tiers on a cluster

Both RBD tiers are **opt-in per cluster** and require: Rook `HEALTH_OK`, a Ready
`CephBlockPool`, the RBD CSI driver, and a snapshot controller.

### 1. The `rbd-vm` StorageClass + FS instant-clone

Wire the `rbd-vm` component as its own Flux Kustomization (it is intentionally
excluded from the shared platform path so clusters opt in). The http golden images
resolve their S3 host from the cluster config, so the Kustomization **must** enable
`postBuild.substituteFrom` the cluster-config ConfigMap. Once applied, the platform's
per-project seeder makes each project a same-namespace snapshot of every golden, and
VM creation from a matching OS becomes a ~1s copy-on-write clone.

### 2. The RWX-Block (migratable) golden

The live-migratable root is an **RWX-Block** clone, and CDI cannot populate a Block
volume — so a small **privileged converter Job** builds the Block golden with
`qemu-img convert`, isolated in its own namespace, then snapshots it. This is opt-in
on top of the FS tier and adds:

- a per-cluster overlay providing the golden's source image (URL + sha256) and an
  egress allow-rule to the cluster's in-cluster S3/RGW front door;
- the digest-pinned converter image + a `dependsOn` snapshot step.

**Rebuilding the Block golden** (new OS point release): delete the converter Job and
the Block `VolumeSnapshot`; Flux recreates them and re-snapshots.

The converter is a deliberately minimal privileged surface (no API token, no host
namespaces, sha256-verified pinned source, default-deny egress). Engineers: the
as-built internals are in the private `rbd-block-golden-and-seeding` doc.

## Live migration

When a VM's root is RWX-Block and the cluster has a **migration pool**, the VM is
live-migratable — its `VirtualMachineInstance` reports `LiveMigratable: True` and
`StorageLiveMigratable: True`.

### Migration pools (the CPU constraint)

You **cannot live-migrate a VM across incompatible CPUs** (e.g. AMD ↔ Intel). A
*migration pool* is a set of **≥2 schedulable, Ready, un-cordoned nodes of the same
vendor** that share a CPU model. The platform derives pools from the KubeVirt
node-labeller and the UI enables the **Enable live migration** checkbox (under Shared
RBD) only when a pool exists **and** a Block golden exists for the selected OS.
Migratable VMs are pinned to the pool's CPU model so they land on — and stay within —
that pool.

On a mixed cluster (say 1 AMD node + 2 Intel nodes) there is exactly one pool (the two
Intel nodes); the lone AMD node can host VMs but they aren't migratable.

### Triggering & prerequisites

- The UI sets `evictionStrategy: LiveMigrate` on migratable VMs, so a node drain
  live-migrates them automatically. To trigger manually, create a
  `VirtualMachineInstanceMigration` referencing the VMI.
- :::warning CPU headroom
  Live migration **temporarily runs two copies** of the VM (source + target
  `virt-launcher` pods) while memory is copied. The project needs **free CPU quota ≥
  the VM's CPU** or the migration is rejected (`migrationRejectedByResourceQuota`) and
  sits Pending with no target pod. Keep headroom, or migrate smaller VMs first.
  :::
- Storage: a large golden clone needs matching free **storage** quota — e.g. a ~75Gi
  Windows golden needs ~75–80Gi free before the clone will provision.

### Verifying

```bash
# Is the VM migratable?
kubectl get vmi <name> -n <project> \
  -o jsonpath='{range .status.conditions[*]}{.type}={.status} {end}'
# LiveMigratable=True StorageLiveMigratable=True → good

# Migrate it
kubectl apply -f - <<EOF
apiVersion: kubevirt.io/v1
kind: VirtualMachineInstanceMigration
metadata: { name: <name>-m, namespace: <project> }
spec: { vmiName: <name> }
EOF
kubectl get vmim <name>-m -n <project> -o jsonpath='{.status.phase}{"\n"}'  # Succeeded
```
