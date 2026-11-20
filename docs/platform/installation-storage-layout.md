# Plan the storage layout

Use this guide before `kube-dc bootstrap init`, to decide where each kind of
data of an installation is stored. Decide it once, with the capacity numbers
written down: the object storage mode, and an erasure-coded bucket pool, cannot
be changed later without a data migration.

Related pages: the flags and keys are in the
[Installation guide](installation-guide.md#33-run-kube-dc-bootstrap-init),
how Kube-DC reads a StorageClass is in [Storage classes](storage-classes.md),
and the VM disk tiers are in [VM storage tiers](vm-storage-tiers.md).

## What needs storage

| Data | What it needs | Where it goes |
|---|---|---|
| Platform state: the Keycloak and Grafana PostgreSQL databases, OpenBao | Small volumes, low write latency. Each keeps its own replicas | With three or more manager nodes, `init` places the two databases on those nodes on `local-path`, one instance per node, whatever `MONITORING_STORAGE_CLASS` selects (and OpenBao too with `OPENBAO_REPLICAS=3`). With fewer, the databases follow `MONITORING_STORAGE_CLASS` (default `local-path`) |
| Metrics and logs: Mimir, Loki and Prometheus volumes | Block volumes. Long-term blocks go to object storage | `MONITORING_STORAGE_CLASS`, `LOKI_STORAGE_CLASS`, `PROM_STORAGE_CLASS`; `ceph-block` with Ceph |
| Object data: tenant buckets, PostgreSQL backups and WAL, OpenBao snapshots, Mimir and Loki blocks, OS images | Capacity and sequential throughput. Latency matters little | The Ceph object store (data pool) |
| Bucket index and metadata | Small, latency-sensitive | The Ceph object store (metadata pool) |
| Managed-service databases | Low random-read latency, expandable volumes | The managed-services database class: `ceph-block` for `rook-ceph-local` and `rook-ceph-pvc`, `rbd-vm` for `rook-ceph-multi-node` with shared-RBD VM disks, or the class you name |
| VM disks | A block class. Live migration needs a volume that two nodes can attach at the same time (RWX Block) | `local-path`, `rbd-vm`, or a class of your storage array: see [VM disks](#vm-disks) |

Fast storage is for random reads and durable writes: databases, the bucket
index, VM disks. Capacity storage is for large sequential data: objects,
backups, images. When the site has both, do not put everything on one of them.

## Choose the Ceph mode

| The servers have | `OBJECT_STORAGE_MODE` |
|---|---|
| Their own empty raw disks | `rook-ceph-multi-node`: one raw disk per OSD node |
| Volumes from a storage array (Fibre Channel or iSCSI LUNs) or from a cloud volume service | `rook-ceph-pvc`: each OSD is a PersistentVolumeClaim |
| One node, a lab | `rook-ceph-local` |

## Ceph on volumes from an array (`rook-ceph-pvc`)

Each OSD claims one volume in Block mode from `CEPH_OSD_STORAGE_CLASS`:
`CEPH_OSD_COUNT` claims of `CEPH_OSD_VOLUME_SIZE_GB` each. Rook creates the
claims while `init` waits for the platform. The class must be able to bind them
at that time. If nothing binds them, Ceph gets no OSDs, object storage does not
become Ready, and `init` does not finish.

- **A CSI driver serves the class.** The driver must be installed and working
  before `init`.
- **The nodes see the LUNs directly and there is no CSI driver.** Create one
  static PersistentVolume for each LUN, and a class without a provisioner,
  after `fetch-kubeconfig` and before `init`. They are plain API objects: they
  do not need the CNI, so you can create them while the nodes are still
  `NotReady`.

```yaml
apiVersion: storage.k8s.io/v1
kind: StorageClass
metadata:
  name: array-osd
provisioner: kubernetes.io/no-provisioner
volumeBindingMode: WaitForFirstConsumer
reclaimPolicy: Retain
---
apiVersion: v1
kind: PersistentVolume
metadata:
  name: ceph-osd-worker-1          # one PersistentVolume for each LUN
spec:
  capacity: {storage: 2000Gi}      # not smaller than CEPH_OSD_VOLUME_SIZE_GB
  volumeMode: Block
  accessModes: [ReadWriteOnce]
  persistentVolumeReclaimPolicy: Retain
  storageClassName: array-osd
  local: {path: /dev/disk/by-id/dm-uuid-mpath-<wwid>}   # the multipath device, a stable name
  nodeAffinity:
    required:
      nodeSelectorTerms:
        - matchExpressions:
            - {key: kubernetes.io/hostname, operator: In, values: [worker-1]}
```

Rules for static volumes:

- Put each OSD on a different node. The pools keep their copies on different
  hosts, so two OSDs on one node do not count as two.
- Present each LUN to one node only, and use the multipath device, not one of
  its paths.
- The LUN must be empty. A LUN that an earlier Ceph cluster used needs Ceph's
  own zap: see the warning about re-used disks in the
  [Installation guide](installation-guide.md#33-run-kube-dc-bootstrap-init).
- After the installation, keep the manifests in the fleet repository, in a
  Flux Kustomization with `prune: false`, so that the volumes are under review
  and a removed file never deletes a bound volume.
- Kube-DC never offers a class without a provisioner to tenants.

`rook-ceph-pvc` keeps two copies by default (`CEPH_REPLICATION_SIZE`). The
array already protects each LUN against a disk failure; the Ceph copies
protect against the loss of a node or of its paths.

## Two kinds of storage in one Ceph

With `rook-ceph-pvc`, Ceph can use a fast set and a capacity set of volumes and
keep each pool on one of them. Use it when the site has, for example, a flash
array and a disk array, or a flash tier and a disk tier in one array.

| Key | Meaning |
|---|---|
| `CEPH_OSD_DEVICE_CLASS` | Device class of the first OSD set (`CEPH_OSD_*`), for example `ssd`. Required when there is a second set |
| `CEPH_CAPACITY_OSD_STORAGE_CLASS`, `_COUNT`, `_VOLUME_SIZE_GB` | The second OSD set |
| `CEPH_CAPACITY_OSD_DEVICE_CLASS` | Its device class (default `hdd`); must differ from the first |
| `CEPH_CAPACITY_OSD_PORTABLE` | `true` only when the class can attach a volume on any node. Static volumes are not portable |
| `CEPH_BLOCK_POOL_DEVICE_CLASS` | Where `ceph-block` volumes are stored. Use the fast class |
| `CEPH_RGW_METADATA_DEVICE_CLASS` | Where the bucket index and metadata are stored. Use the fast class |
| `CEPH_RGW_DATA_DEVICE_CLASS` | Where the bucket data is stored. Use the capacity class |
| `CEPH_RGW_DATA_EC` | `K+M`, for example `4+2`: the bucket data pool is erasure-coded instead of replicated |

- A replicated pool needs `CEPH_REPLICATION_SIZE` OSDs in its class, on
  different hosts. An erasure-coded pool needs at least K+M. `init` checks the
  counts and stops with the reason when they do not fit.
- Erasure coding uses less raw capacity: usable space is raw × K / (K+M),
  against raw / copies for a replicated pool. With `4+2`, six volumes of 6 TiB
  give 24 TiB, where two copies give 18 TiB. With exactly K+M hosts there is no
  spare host to rebuild onto after a host loss: add one more for that.
- **Choose erasure coding before the first installation.** Ceph cannot convert
  an existing pool.
- Both sets must be able to bind their volumes when `init` runs. The system
  buckets (metrics, logs, backups) are in the data pool, and that pool needs
  the capacity OSDs from the start.

Without these keys, the mode has one OSD set and every pool uses every OSD.

## Managed services

Managed-service databases use the class in the table above, unless you set
`KUBE_DC_INIT_SERVICES_DATABASE_CLASS` to an expandable block class of your
own. `KUBE_DC_INIT_SERVICES_STORAGE_BUDGET` is the total the catalog may
allocate. With `rook-ceph-pvc` and no budget given, `init` derives one: the
raw capacity of the OSDs that hold `ceph-block`, divided by the copies, divided
by four. Set the budget yourself when your capacity plan gives another number.
See [Enable managed services](managed-services-enable.md).

## VM disks

| Choice | Use when | Limit |
|---|---|---|
| `local-path` (`KUBE_DC_INIT_VM_STORAGE_MODE=local`) | Fastest durable writes; VMs that do not move | Bound to one node: no live migration, and the disk is lost with the node |
| `rbd-vm` (`KUBE_DC_INIT_VM_STORAGE_MODE=shared-rbd`) | Snapshots, fast clones from golden images, live migration | Needs a Ceph mode. The disks use the same Ceph pool as `ceph-block`: count them in the fast set. Durable writes are slower than on a local disk |
| A class of your storage array, through its CSI driver | The array has the capacity and the driver supports what VMs need | Kube-DC reads a class of an unknown provisioner as RWO Filesystem only. Declare what it can do: see [Storage classes](storage-classes.md#declaring-a-class) |

For an array class, declare `rwx-block` and `live-migration` only after a live
migration between two nodes worked with a test VM on that class. The console's
**Shared RBD** choice and its golden-image clones use `rbd-vm`: see
[VM storage tiers](vm-storage-tiers.md). Install the array's CSI driver from
the fleet repository, not by hand on the cluster.

KubeVirt and CDI open a raw block volume as a non-root user (uid 107). The
platform's `cdi-block-device-access` policy covers drivers that publish the
device with the `disk` group, as Ceph RBD does. A driver that publishes it
readable by root only needs containerd's own setting on every node, before the
first VM on that class:

```toml
# /var/lib/rancher/rke2/agent/etc/containerd/config-v3.toml.d/10-device-ownership.toml
[plugins."io.containerd.cri.v1.runtime"]
  device_ownership_from_security_context = true
```

Restart the RKE2 service on one node at a time (`rke2-agent` on workers,
`rke2-server` on managers); running pods keep running. Check a published
device with `ls -lL` under `/var/lib/kubelet/pods/*/volumeDevices/` to see
which case a driver is. Without the setting, the image import and the VM fail
with `Permission denied` on the device.

## Failure domains

- Database backups are written to object storage. When the database volumes
  and the object data are on the same array, one array failure takes the data
  and its backups together. Put them on different arrays, or keep a copy of
  the backup buckets outside that array.
- The same applies to VM disks and the backups of those VMs.
- Write down what stops when each array, or each fabric, is lost, and agree it
  with the site before the installation.

## Capacity worksheet

1. For each OSD set: raw capacity = volumes × size.
2. For each pool: usable = raw / copies, or raw × K / (K+M).
3. On the fast set, add the managed-services budget, the monitoring volumes,
   the VM disks on `rbd-vm`, tenant volumes on `ceph-block`, and the bucket
   index.
4. On the capacity set (or on the only set), add tenant buckets, backups with
   their retention, metrics and log blocks, and OS images.
5. Keep free room. By default Ceph warns when an OSD is 85 % full and stops
   writes at 95 %; after a host loss the remaining OSDs take its data.
