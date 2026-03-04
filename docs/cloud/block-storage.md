# Block Storage

Kube-DC provides persistent block storage through two Kubernetes-native mechanisms: **DataVolumes** for virtual machines and **PersistentVolumeClaims (PVCs)** for containers. Both are managed through the Volumes view in the dashboard or via kubectl.

## Volumes Dashboard

<div style={{width: '100%', maxWidth: 'none'}}>
<img src={require('./images/volume-view.png').default} alt="Volumes dashboard" style={{width: '100%', display: 'block'}} />
</div>

The Volumes view shows all persistent storage in your project. Volumes are organized by **Storage Class** in the left sidebar. Each volume shows its name, attachment status, capacity, storage class, type (DataVolume or PVC), age, and which VM or pod it is attached to.

Click on any volume to expand its details:

<img src={require('./images/volumes-view.png').default} alt="Volume detail view" style={{maxWidth: '800px', width: '100%'}} />

The detail panel shows:
- **Volume Information** — Name, type, capacity, and storage class
- **Status** — Attachment status and which VM/pod the volume is attached to
- **Actions** — Detach, Clone, or View YAML

## Understanding Volume Types

### DataVolumes (Virtual Machines)

A **DataVolume** is a KubeVirt resource provided by the [Containerized Data Importer (CDI)](https://kubevirt.io/user-guide/storage/containerized_data_importer/). It automates creating a PVC and populating it with data from a source — typically an OS image for a VM root disk.

When you create a VM, the platform automatically creates a DataVolume as the root disk. DataVolumes can import data from:

- **HTTP/HTTPS URL** — Download a cloud image (e.g., Debian, Ubuntu)
- **Container Registry** — Pull a disk image from a container registry
- **Blank** — Create an empty disk for additional storage

**Example: VM root disk DataVolume**

```yaml
apiVersion: cdi.kubevirt.io/v1beta1
kind: DataVolume
metadata:
  name: debian-root
  namespace: shalb-jumbolot
spec:
  source:
    http:
      url: https://cloud.debian.org/images/cloud/bookworm/latest/debian-12-generic-amd64.qcow2
  pvc:
    accessModes:
      - ReadWriteOnce
    resources:
      requests:
        storage: 20G
    storageClassName: local-path
```

Once the import completes (Phase: `Succeeded`), the DataVolume is ready and can be attached to a VM.

**Example: Blank data disk**

```yaml
apiVersion: cdi.kubevirt.io/v1beta1
kind: DataVolume
metadata:
  name: data-disk
  namespace: shalb-jumbolot
spec:
  source:
    blank: {}
  pvc:
    accessModes:
      - ReadWriteOnce
    resources:
      requests:
        storage: 50G
    storageClassName: local-path
```

### PersistentVolumeClaims (Containers)

A **PVC** is a standard Kubernetes resource for requesting persistent storage. Use PVCs when your containerized workloads (Deployments, StatefulSets, Pods) need data that persists across restarts.

**Example: PVC for a database**

```yaml
apiVersion: v1
kind: PersistentVolumeClaim
metadata:
  name: postgres-data
  namespace: shalb-jumbolot
spec:
  accessModes:
    - ReadWriteOnce
  resources:
    requests:
      storage: 10Gi
  storageClassName: local-path
```

Mount it in a Pod:

```yaml
apiVersion: v1
kind: Pod
metadata:
  name: postgres
  namespace: shalb-jumbolot
spec:
  containers:
    - name: postgres
      image: postgres:16
      volumeMounts:
        - name: data
          mountPath: /var/lib/postgresql/data
  volumes:
    - name: data
      persistentVolumeClaim:
        claimName: postgres-data
```

### DataVolume vs PVC — When to Use Which

| Feature | DataVolume | PVC |
|---------|-----------|-----|
| **Primary use** | VM root and data disks | Container storage |
| **Data import** | Automatic (HTTP, registry, blank) | Manual (empty on creation) |
| **Created by** | VM provisioning or manually | Directly by user |
| **Backed by** | Creates a PVC internally | Directly binds to a PersistentVolume |
| **Visible in UI** | Yes (Volumes tab, type: DataVolume) | Yes (Volumes tab, type: PVC) |

## Storage Classes

A **StorageClass** defines what type of storage backs your volumes. The available storage classes depend on the infrastructure provider.

### Current Default: `local-path`

```
$ kubectl get storageclass
NAME                   PROVISIONER             RECLAIMPOLICY   VOLUMEBINDINGMODE
local-path (default)   rancher.io/local-path   Delete          WaitForFirstConsumer
```

The `local-path` storage class provisions storage on the local disk of the node where the workload runs. Key characteristics:

- **Fast** — Direct local disk I/O, no network overhead
- **Node-bound** — Data is stored on a specific node; the workload must run on the same node
- **Delete reclaim policy** — When a PVC is deleted, the underlying data is removed

:::info Provider-Dependent Storage Classes
Storage classes vary by infrastructure provider. Some platforms may offer:

- **Network-attached storage** (e.g., Ceph RBD, Longhorn) — Data accessible from any node, supports live migration
- **SSD-backed classes** — Higher IOPS for database workloads
- **HDD-backed classes** — Cost-effective for large datasets

Check your available storage classes with `kubectl get storageclass`. Always specify the `storageClassName` in your PVC or DataVolume to ensure you get the expected storage type.
:::

## Creating Volumes

### Via Dashboard

1. Navigate to your project → **Volumes** tab
2. Click **+ Create Volume**
3. Choose the volume type, size, and storage class
4. Click **Create**

### Via kubectl

**Create a DataVolume (for VMs):**

```bash
kubectl apply -f - <<EOF
apiVersion: cdi.kubevirt.io/v1beta1
kind: DataVolume
metadata:
  name: my-data-disk
  namespace: shalb-jumbolot
spec:
  source:
    blank: {}
  pvc:
    accessModes:
      - ReadWriteOnce
    resources:
      requests:
        storage: 30G
    storageClassName: local-path
EOF
```

**Create a PVC (for containers):**

```bash
kubectl apply -f - <<EOF
apiVersion: v1
kind: PersistentVolumeClaim
metadata:
  name: my-pvc
  namespace: shalb-jumbolot
spec:
  accessModes:
    - ReadWriteOnce
  resources:
    requests:
      storage: 10Gi
  storageClassName: local-path
EOF
```

## Managing Volumes

### Check Volume Status

```bash
# List all PVCs
kubectl get pvc -n shalb-jumbolot

# List all DataVolumes
kubectl get datavolumes -n shalb-jumbolot

# Get volume details
kubectl describe pvc debian-root -n shalb-jumbolot
```

### Clone a Volume

From the Volumes dashboard, expand a volume and click **Clone** to create an identical copy. This is useful for creating backups or testing with production data.

### Detach a Volume

Click **Detach** to disconnect a volume from its VM or pod without deleting the data. The volume can be reattached later.

### Delete a Volume

```bash
# Delete a DataVolume (also deletes the underlying PVC)
kubectl delete datavolume my-data-disk -n shalb-jumbolot

# Delete a PVC directly
kubectl delete pvc my-pvc -n shalb-jumbolot
```

:::warning
Deleting a volume permanently removes the data. With the `Delete` reclaim policy (default), there is no recovery after deletion.
:::

## Quick Reference

| Action | Command |
|--------|---------|
| List PVCs | `kubectl get pvc -n my-project` |
| List DataVolumes | `kubectl get datavolumes -n my-project` |
| List StorageClasses | `kubectl get storageclass` |
| Describe volume | `kubectl describe pvc <name> -n my-project` |
| Delete DataVolume | `kubectl delete datavolume <name> -n my-project` |
| Delete PVC | `kubectl delete pvc <name> -n my-project` |

## Next Steps

- [Object Storage](object-storage.md) — S3-compatible storage for files and backups
- [Backups & Snapshots](backups-snapshots.md)
