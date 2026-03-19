# Backups & Snapshots

:::caution Work in Progress
Backup functionality is currently in **testing and validation phase**. Current implementation backs up VM and workload **metadata only** (configurations, definitions) but not actual disk data due to storage backend limitations.

**For full VM data protection**, migration to snapshot-capable storage (Ceph RBD, Longhorn) is required.
:::

Kube-DC provides backup and restore capabilities for virtual machines, containers, and persistent data using Velero. Backups are stored in S3-compatible object storage (Rook Ceph RGW) at `https://s3.kube-dc.cloud`.

## Overview

### Current Capabilities

Velero currently backs up:
- **Virtual Machine Configurations** — VM definitions, DataVolumes, PVC metadata
- **VirtualMachineSnapshots** — KubeVirt snapshot resources (metadata)
- **Container Workloads** — Deployments, StatefulSets, and all Kubernetes resources
- **Kubernetes Resources** — ConfigMaps, Secrets, Services, networking resources

### Known Limitations

⚠️ **Volume Data Not Backed Up**: With the current `local-path` storage backend:
- Only **metadata** (configurations) is backed up
- VM disk data is **not** captured
- Suitable for disaster recovery of infrastructure configurations
- **Not suitable** for data loss protection

For full data backup, snapshot-capable storage is required (planned upgrade to Ceph RBD).

### Backup Scope

Backups are created in the `velero` namespace but target project resources using `includedNamespaces`. Each project can only back up resources within its own namespace, providing complete isolation.

## Prerequisites

### Step 1: Create a Backup Bucket

Before creating backups, you need an S3 bucket to store them.

**Via Dashboard**:
1. Navigate to **Object Storage** → **Buckets**
2. Click **+ Create Bucket**
3. Enter bucket name: `my-project-backups`
4. Choose **Private** access
5. Click **Create**

**Via kubectl**:
```bash
kubectl apply -f - <<EOF
apiVersion: objectbucket.io/v1alpha1
kind: ObjectBucketClaim
metadata:
  name: project-backups
  namespace: my-project
  labels:
    kube-dc.com/organization: myorg
spec:
  bucketName: myorg-myproject-backups
  storageClassName: ceph-bucket
EOF
```

Wait for the bucket to be provisioned:
```bash
kubectl get objectbucketclaim project-backups -n my-project
# Wait for status: Bound
```

### Step 2: Request Backup Storage Setup

:::info Manual Setup Required
Currently, backup storage configuration requires administrator assistance. Contact support to enable backup storage for your project by providing:
- **Project namespace**: `my-project`
- **Bucket name**: `myorg-myproject-backups`

The administrator will configure a BackupStorageLocation that allows you to create backups referencing your bucket.

**Automated setup via controller is planned.**
:::

**What the administrator does**:
```bash
# Extract S3 credentials from your project
ACCESS_KEY=$(kubectl get secret project-backups -n my-project -o jsonpath='{.data.AWS_ACCESS_KEY_ID}' | base64 -d)
SECRET_KEY=$(kubectl get secret project-backups -n my-project -o jsonpath='{.data.AWS_SECRET_ACCESS_KEY}' | base64 -d)
BUCKET=$(kubectl get cm project-backups -n my-project -o jsonpath='{.data.BUCKET_NAME}')

# Create credential secret in velero namespace
kubectl create secret generic bsl-my-project -n velero \
  --from-literal=cloud="[default]
aws_access_key_id=${ACCESS_KEY}
aws_secret_access_key=${SECRET_KEY}"

# Create BackupStorageLocation
kubectl apply -f - <<EOF
apiVersion: velero.io/v1
kind: BackupStorageLocation
metadata:
  name: my-project
  namespace: velero
  labels:
    kube-dc.com/project: my-project
spec:
  provider: aws
  objectStorage:
    bucket: ${BUCKET}
  config:
    region: us-east-1
    s3ForcePathStyle: "true"
    s3Url: https://s3.kube-dc.cloud
  credential:
    name: bsl-my-project
    key: cloud
EOF
```

Once configured, you'll be able to reference the BackupStorageLocation by your project namespace name in backup resources.

## Creating Backups

:::info Important: Backup Namespace
All Backup resources must be created in the **`velero` namespace**, not in your project namespace. Use `includedNamespaces` to specify which project namespaces to back up.

This is a Velero requirement — backup controllers only watch the `velero` namespace.
:::

### Backup a Virtual Machine

**Via kubectl**:
```bash
kubectl apply -f - <<EOF
apiVersion: velero.io/v1
kind: Backup
metadata:
  name: ubuntu-vm-backup
  namespace: my-project
spec:
  # Include only this namespace
  includedNamespaces:
    - my-project
  
  # Select the VM by label
  labelSelector:
    matchLabels:
      kubevirt.io/vm: ubuntu
  
  # Include VM-related resources
  includedResources:
    - virtualmachines
    - virtualmachineinstances
    - datavolumes
    - persistentvolumeclaims
    - persistentvolumes
  
  # Reference your project's backup storage
  storageLocation: my-project
  
  # Retention period (7 days)
  ttl: 168h
EOF

# Wait for snapshot to complete
kubectl wait --for=condition=Ready virtualmachinesnapshot/ubuntu-snapshot-$(date +%Y%m%d) -n my-project --timeout=5m
```

**Step 2: Backup VM and Snapshot Resources**:
```bash
kubectl apply -f - <<EOF
apiVersion: velero.io/v1
kind: Backup
metadata:
  name: ubuntu-vm-backup-$(date +%Y%m%d)
  namespace: velero  # Must be velero namespace
spec:
  includedNamespaces:
    - my-project
  includedResources:
    - virtualmachines.kubevirt.io
    - virtualmachinesnapshots.snapshot.kubevirt.io
    - virtualmachinesnapshotcontents.snapshot.kubevirt.io
    - datavolumes.cdi.kubevirt.io
    - persistentvolumeclaims
  storageLocation: my-project
  ttl: 168h  # 7 days
  defaultVolumesToFsBackup: true
EOF
```

:::warning Metadata Only
This backup captures VM configuration and resource definitions but **not disk data**. The VM can be recreated but will start with empty/original disks.
:::

### Check Backup Status

```bash
# List backups (in velero namespace)
kubectl get backups -n velero

# Get detailed status
kubectl describe backup ubuntu-vm-backup-$(date +%Y%m%d) -n velero

# View backup logs
kubectl logs -n velero deployment/velero | grep ubuntu-vm-backup-$(date +%Y%m%d)
```

**Backup phases**:
- `New` — Backup request received
- `InProgress` — Backing up resources and volumes
- `Completed` — Backup finished successfully
- `PartiallyFailed` — Some resources failed to back up
- `Failed` — Backup failed

### Backup Entire Project

Backup all resources in your project namespace:

```bash
kubectl apply -f - <<EOF
apiVersion: velero.io/v1
kind: Backup
metadata:
  name: full-project-backup-$(date +%Y%m%d)
  namespace: velero  # Must be velero namespace
spec:
  includedNamespaces:
    - my-project
  storageLocation: my-project
  ttl: 720h  # 30 days
EOF
```

This backs up all VMs, containers, services, configurations, and persistent volumes in the project.

### Backup Specific Resources

**Example: Backup only database StatefulSets**:
```bash
kubectl apply -f - <<EOF
apiVersion: velero.io/v1
kind: Backup
metadata:
  name: database-backup-$(date +%Y%m%d)
  namespace: velero  # Must be velero namespace
spec:
  includedNamespaces:
    - my-project
  includedResources:
    - statefulsets
    - persistentvolumeclaims
    - services
    - configmaps
    - secrets
  labelSelector:
    matchLabels:
      app: postgresql
  storageLocation: my-project
  ttl: 168h
  defaultVolumesToFsBackup: true
EOF
```

## Restoring from Backups

### Restore a Virtual Machine

**List available backups**:
```bash
kubectl get backup -n velero
```

**Create a restore**:
```bash
kubectl apply -f - <<EOF
apiVersion: velero.io/v1
kind: Restore
metadata:
  name: restore-ubuntu-vm-$(date +%Y%m%d)
  namespace: velero  # Must be velero namespace
spec:
  backupName: ubuntu-vm-backup-$(date +%Y%m%d)
  includedNamespaces:
    - my-project
EOF
```

:::warning Data Limitations
Restore recreates VM configuration but **not disk data**. The restored VM will reference the original PVCs if they still exist, or create new empty volumes if they don't.
:::

**Monitor restore progress**:
```bash
# Check restore status
kubectl get restore restore-ubuntu-vm-$(date +%Y%m%d) -n velero

# Get detailed information
kubectl describe restore restore-ubuntu-vm-$(date +%Y%m%d) -n velero

# Watch for completion
kubectl get restore restore-ubuntu-vm-$(date +%Y%m%d) -n velero -w
```

**Restore phases**:
- `New` — Restore request received
- `InProgress` — Restoring resources
- `Completed` — Restore finished successfully
- `PartiallyFailed` — Some resources failed to restore
- `Failed` — Restore failed

### Restore to Different Namespace

To restore to a different project (requires permissions in both namespaces):

```bash
kubectl apply -f - <<EOF
apiVersion: velero.io/v1
kind: Restore
metadata:
  name: restore-to-staging-$(date +%Y%m%d)
  namespace: velero  # Must be velero namespace
spec:
  backupName: full-project-backup-$(date +%Y%m%d)
  namespaceMapping:
    my-project: my-project-staging
  restorePVs: true
EOF
```

### Partial Restore

**Restore only specific resources**:
```bash
kubectl apply -f - <<EOF
apiVersion: velero.io/v1
kind: Restore
metadata:
  name: restore-vm-only-$(date +%Y%m%d)
  namespace: velero  # Must be velero namespace
spec:
  backupName: full-project-backup-$(date +%Y%m%d)
  includedResources:
    - virtualmachines
    - datavolumes
    - persistentvolumeclaims
  labelSelector:
    matchLabels:
      kubevirt.io/vm: ubuntu
  restorePVs: true
EOF
```

## Scheduled Backups

Create automated backup schedules using cron syntax:

```bash
kubectl apply -f - <<EOF
apiVersion: velero.io/v1
kind: Schedule
metadata:
  name: daily-backup
  namespace: velero  # Must be velero namespace
spec:
  schedule: "0 2 * * *"  # Every day at 2 AM
  template:
    includedNamespaces:
      - my-project
    storageLocation: my-project
    ttl: 720h  # Keep for 30 days
EOF
```

**Check scheduled backups**:
```bash
# List schedules
kubectl get schedule -n velero

# View schedule details
kubectl describe schedule daily-backup -n velero

# List backups created by schedule
kubectl get backup -n velero -l velero.io/schedule-name=daily-backup
```

## Backup Best Practices

### Before Backing Up VMs

1. **Shut down VMs for consistent backups** (optional but recommended):
   ```bash
   virtctl stop ubuntu -n my-project
   # Create backup
   # Restart VM
   virtctl start ubuntu -n my-project
   ```

2. **Tag VMs for selective backups**:
   ```yaml
   metadata:
     labels:
       backup-policy: daily
       environment: production
   ```

### Retention Policies

Set appropriate TTLs based on backup type:

| Backup Type | TTL | Use Case |
|-------------|-----|----------|
| Pre-upgrade snapshots | 24h-168h | Short-term rollback |
| Daily automated backups | 168h-720h | Recent recovery |
| Weekly backups | 2160h-4320h | Long-term retention |
| Monthly backups | 8760h | Compliance |

### Performance Considerations

**Backup duration** depends on data size:
- Small VMs (&lt;10GB): 5-15 minutes
- Medium VMs (10-50GB): 30-90 minutes
- Large VMs (&gt;100GB): 2-4 hours

**Tips for faster backups**:
- Schedule backups during low-usage periods
- Exclude temporary data directories
- Use compression (enabled by default in restic)

## Troubleshooting

### Backup Stuck in InProgress

**Check node-agent logs**:
```bash
kubectl logs -n velero -l name=node-agent --tail=100
```

**Check backup details**:
```bash
kubectl describe backup <backup-name> -n my-project
```

**Common causes**:
- Large volumes taking time to back up
- Node-agent pod not running
- Network issues connecting to S3

### Restore Fails with Resource Conflicts

If resources already exist, the restore will fail. Options:

**Option 1: Delete existing resources**:
```bash
kubectl delete vm ubuntu -n my-project
# Then retry restore
```

**Option 2: Restore to different namespace**:
```bash
# Use namespaceMapping in restore spec
```

### Backup Shows PartiallyFailed

**View backup logs**:
```bash
velero backup logs <backup-name> -n my-project
```

**Common issues**:
- Some PVCs are not mounted (restic requires mounted volumes)
- Transient resource errors
- Insufficient S3 storage quota

## Storage Quotas

Backup storage counts toward your object storage quota:

| Plan | Storage Limit | Recommended Max Backups |
|------|--------------|-------------------------|
| Dev | 20 GB | 5-10 backups |
| Pro | 100 GB | 20-30 backups |
| Scale | 500 GB | 50-100 backups |

**Monitor backup storage usage**:
```bash
# List backups with sizes
kubectl get backup -n my-project -o custom-columns=NAME:.metadata.name,SIZE:.status.progress.totalItems,AGE:.metadata.creationTimestamp
```

**Clean up old backups**:
```bash
kubectl delete backup <backup-name> -n my-project
```

Backups are automatically deleted when their TTL expires.

## Quick Reference

| Task | Command |
|------|---------|
| List backups | `kubectl get backup -n my-project` |
| Create VM backup | `kubectl apply -f backup.yaml` |
| Check backup status | `kubectl describe backup <name> -n my-project` |
| List restores | `kubectl get restore -n my-project` |
| Create restore | `kubectl apply -f restore.yaml` |
| List schedules | `kubectl get schedule -n my-project` |
| Delete backup | `kubectl delete backup <name> -n my-project` |
| View backup logs | `velero backup logs <name> -n my-project` |

## Next Steps

- [Block Storage](block-storage.md) — Understand persistent volumes
- [Object Storage](object-storage.md) — Manage S3 buckets
