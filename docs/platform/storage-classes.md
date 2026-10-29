# Storage classes and the capability contract (operator guide)

Kube-DC decides which StorageClass may serve which volume from what the class can do,
never from its name. This page is for **cluster operators**: how Kube-DC reads a class,
how to declare one explicitly, and how to check what tenants are offered. For the
tenant view see [Block storage](/cloud/block-storage); for VM tiers and live migration
see [VM storage tiers](/platform/vm-storage-tiers).

The same rules are applied by the console pickers, the backend create guards, the
managed-cluster worker disk (k8-manager) and the database storage default (db-manager).

## Classes without labels

A class without the `storage.kube-dc.com/tier` label is classified from its provisioner,
so a cluster works before any class is labelled:

| Provisioner | Kind | Capabilities |
|---|---|---|
| contains `bucket` (Rook object store) | object | none: buckets only, never volumes |
| `kubernetes.io/no-provisioner` | block, node-local | none, and no dynamic provisioning: never offered |
| `rancher.io/local-path`, OpenEBS local, TopoLVM, host-path drivers | block, node-local | expansion if the class allows it |
| contains `rbd` (ceph-csi RBD) | block | Block volume mode, RWX Block, clone, live migration, expansion if allowed |
| contains `cephfs` or `nfs` | file | RWX Filesystem, clone, expansion if allowed |
| `csi.cloudsigma.com` | block | RWO only, expansion if allowed; never snapshots, shared access or live migration, whatever is declared |
| anything else | block | RWO Filesystem, expansion if allowed |

A class also gets **snapshots** when its driver has a VolumeSnapshotClass with
`deletionPolicy: Delete`. Kube-DC uses the driver's default VolumeSnapshotClass, else the
first by name. A `Retain` class is never used for tenant snapshots, even when it is the
default: deleting the snapshot would leave its data behind.

## Declaring a class

Add the `storage.kube-dc.com/tier` label to take over from the provisioner rules. Then
these labels and annotations apply:

| Key | Type | Meaning |
|---|---|---|
| `storage.kube-dc.com/tier` (label) | `local`, `local-snap`, `capacity`, `balanced`, `performance`, `scratch` | Performance tier. `scratch` is internal, not host-redundant, and never offered for tenant volumes |
| `storage.kube-dc.com/kind` (label) | `block`, `file`, `object` | What the class provisions. Defaults to what the provisioner implies; a bucket provisioner is always `object` |
| `storage.kube-dc.com/deprecated` (label) | `"true"` | An alias kept for existing volumes: hidden from new volumes, still accepted (StatefulSet scale-up, replica replacement, clones) |
| `storage.kube-dc.com/user-visible` (label) | `"true"` / `"false"` | Whether tenants are offered the class. Defaults to visible unless deprecated, scratch or without dynamic provisioning |
| `storage.kube-dc.com/capabilities` | comma list: `snapshot`, `clone`, `expand`, `block-mode`, `rwx-block`, `rwx-file`, `live-migration` | What the class can do. `expand` counts only when `allowVolumeExpansion: true` |
| `storage.kube-dc.com/snapshot-class` | name | VolumeSnapshotClass for this class's snapshots |
| `storage.kube-dc.com/node-local` | `"true"` / `"false"` | Whether volumes are pinned to one node. Defaults to true for the `local` and `local-snap` tiers |
| `storage.kube-dc.com/roles` | comma list | Platform roles this class fills (below) |
| `storage.kube-dc.com/pool` | `<cluster>/<pool>` | Storage pool identity, only when the driver's parameters do not say (ceph-csi's `clusterID` with `pool` or `fsName` do) |
| `storage.kube-dc.com/display-name` | text | Name shown in pickers |

Example: a replicated RBD class tenants see as "Balanced (replicated SSD)":

```yaml
apiVersion: storage.k8s.io/v1
kind: StorageClass
metadata:
  name: balanced
  labels:
    storage.kube-dc.com/tier: balanced
    storage.kube-dc.com/kind: block
  annotations:
    storage.kube-dc.com/display-name: Balanced (replicated SSD)
    storage.kube-dc.com/capabilities: snapshot,clone,expand,block-mode,rwx-block,live-migration
    storage.kube-dc.com/snapshot-class: block-snapshot
    storage.kube-dc.com/roles: database
provisioner: rook-ceph.rbd.csi.ceph.com
allowVolumeExpansion: true
parameters:
  clusterID: rook-ceph
  pool: rbd-pool
  # ...the usual ceph-csi parameters
```

StorageClasses are immutable, so a new tier is a new class. Keep the old class as a
deprecated alias while volumes still use it.

## Purposes

Each create path asks whether a class may be **offered** in its picker and whether a
request naming it is **accepted**. Deprecated aliases and classes hidden from tenants are
accepted but not offered.

| Purpose | Used by | Accepts |
|---|---|---|
| `vm-root`, `vm-data` | VM boot and data disks | block classes |
| `vm-migratable` | disks of a live-migratable VM | block, not node-local, with `rwx-block` and `live-migration` |
| `volume` | PersistentVolumeClaims | block and file classes |
| `restore` | snapshot restores and CSI clones | block and file classes on the source's driver and pool |
| `k8s-worker-root` | managed-cluster worker disks | block classes |
| `k8s-etcd` | managed-cluster etcd | block, not the `capacity` tier |
| `k8s-tenant-volumes` | volumes inside managed clusters | block; node-local only where the plan allows it |
| `database` | KdcDatabase, managed services | block; node-local where the caller allows it (KdcDatabase does); not `capacity` for production plans. `expand` is not required, but a database grows only on a class that has it |
| `backup-bucket` | backup buckets | object classes |

Object, scratch and no-provisioner classes are refused for every volume purpose. Any
volume restored or cloned from another one must stay on the source's driver and, when
both pools are known, its pool.

## Roles and defaults

- **`database`**: a KdcDatabase created without a storage class goes to the class with
  this role, if the contract accepts it for a database; otherwise to `local-path`.
  An existing database always keeps the class it already uses.
  Several classes with the role are a configuration error, and the platform falls back
  to `local-path`.
- **`vm-root-default`**: the class a VM root disk gets by default; without a declaration,
  the cluster default class.
- **Cluster default class**: the newest class annotated as default (GA or beta
  annotation), then the first by name. This is the class Kubernetes gives a claim that
  names none, and the class the guards check for it.

## Check what tenants are offered

The backend lists the classes for a purpose, as the tenant's own token sees them:

```bash
curl -s -H "Authorization: Bearer $TOKEN" \
  "https://console.example.com/api/create-vm/<org>-<project>/storageclasses?purpose=vm-data" | jq
```

Each entry carries `tier`, `kind`, `nodeLocal`, `features`, `modes` (access and volume
mode pairs), `snapshotClass` and `pool`. Add `allowNodeLocal=true` or `production=true`
for the plan options, and `sourceClass=<class>` for a restore or clone.
