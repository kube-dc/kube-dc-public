# Velero Backup & Restore Service - Product Requirements Document

## Executive Summary

**Status**: 🚧 **Work in Progress** - Initial testing and validation phase

This PRD defines the implementation of Velero-based backup and restore capabilities for Kube-DC, enabling users to protect their virtual machines, containers, and persistent data. The solution provides per-project backup isolation using S3-compatible storage (Rook Ceph RGW) with an automated controller managing backup storage locations.

**Current State**: Velero is deployed and functional. Initial testing reveals important limitations with the current storage backend that affect VM volume data backup.

## Problem Statement

Users need a reliable way to:
- **Backup VMs and containers**: Protect against data loss from accidental deletion, corruption, or infrastructure failures
- **Disaster recovery**: Restore entire projects or individual workloads to previous states
- **Migration**: Move workloads between clusters or projects
- **Compliance**: Meet data retention and recovery time objectives (RTO/RPO)

Current gaps:
- No native backup mechanism for KubeVirt VMs and persistent volumes
- Manual snapshot processes are error-prone and don't capture entire application state
- No multi-tenant backup isolation

## Goals

### Primary Goals
1. **Per-Project Backup Isolation**: Each project has its own S3 bucket for backups
2. **Automated Backup Storage Provisioning**: Controller automatically configures Velero BSLs when projects create backup buckets
3. **VM and Container Support**: Backup KubeVirt VMs, DataVolumes, PVCs, and Kubernetes workloads
4. **File-Level PV Backup**: Use Velero's restic/node-agent for file-level backup of persistent volumes
5. **RBAC Integration**: Project users can create/restore backups within their namespace permissions

### Non-Goals
- Block-level snapshots (CSI snapshots) - local-path storage doesn't support this
- Cross-region replication
- Backup encryption (S3 bucket-level encryption can be added separately)
- Automated backup scheduling via UI (manual Schedule resources initially)

### Known Limitations (Testing Phase)
- **VM Volume Data Backup**: With `local-path` storage, only VM metadata (configurations) is backed up, not actual disk data
- **VirtualMachineSnapshot**: Works but creates metadata-only snapshots without snapshot-capable storage
- **Restic/Node-Agent**: Cannot backup KubeVirt volumes (block devices vs. mounted filesystems)
- **Production Readiness**: Requires migration to snapshot-capable storage (Ceph RBD, Longhorn, etc.) for full VM data protection

## Architecture

### Components

```
┌─────────────────────────────────────────────────────────────────┐
│                        Management Cluster                        │
│                                                                   │
│  ┌─────────────────┐      ┌──────────────────────────────────┐  │
│  │ Velero Namespace│      │   Project Namespace (shalb-demo) │  │
│  │                 │      │                                   │  │
│  │ ┌─────────────┐ │      │ ┌────────────────────────────┐  │  │
│  │ │   Velero    │ │      │ │  ObjectBucketClaim         │  │  │
│  │ │ Controller  │ │      │ │  name: project-backups     │  │  │
│  │ └─────────────┘ │      │ │  bucket: shalb-demo-backups│  │  │
│  │                 │      │ └────────────────────────────┘  │  │
│  │ ┌─────────────┐ │      │           │                      │  │
│  │ │ Node Agent  │ │      │           │ creates Secret +     │  │
│  │ │ (DaemonSet) │ │      │           │ ConfigMap            │  │
│  │ └─────────────┘ │      │           ▼                      │  │
│  │                 │      │ ┌────────────────────────────┐  │  │
│  │ ┌─────────────┐ │      │ │  Backup Resource           │  │  │
│  │ │BackupStorage│ │◀─────┼─│  name: vm-backup-001       │  │  │
│  │ │Location     │ │ ref  │ │  includedNamespaces:       │  │  │
│  │ │name:        │ │      │ │    - shalb-demo            │  │  │
│  │ │shalb-demo   │ │      │ │  storageLocation:          │  │  │
│  │ │             │ │      │ │    shalb-demo              │  │  │
│  │ │bucket:      │ │      │ └────────────────────────────┘  │  │
│  │ │shalb-demo-  │ │      │                                  │  │
│  │ │backups      │ │      │ ┌────────────────────────────┐  │  │
│  │ └─────────────┘ │      │ │  Restore Resource          │  │  │
│  │        │        │      │ │  backupName: vm-backup-001 │  │  │
│  └────────┼────────┘      │ └────────────────────────────┘  │  │
│           │               │                                  │  │
│           │               └──────────────────────────────────┘  │
│           │                                                      │
│           ▼                                                      │
│  ┌─────────────────────────────────────────────────────────┐   │
│  │              Rook Ceph Object Storage (S3)               │   │
│  │                                                           │   │
│  │  ┌──────────────┐  ┌──────────────┐  ┌──────────────┐  │   │
│  │  │ shalb-demo-  │  │ shalb-prod-  │  │ shalb-dev-   │  │   │
│  │  │ backups      │  │ backups      │  │ backups      │  │   │
│  │  └──────────────┘  └──────────────┘  └──────────────┘  │   │
│  └─────────────────────────────────────────────────────────┘   │
│                                                                  │
└──────────────────────────────────────────────────────────────┘
```

### Backup Storage Controller

**Purpose**: Automatically provision Velero BackupStorageLocations when users create backup buckets.

**Reconciliation Logic**:

```go
// Watch ObjectBucketClaims with label: kube-dc.com/backup-enabled=true
// in project namespaces

func (r *BackupStorageReconciler) Reconcile(ctx context.Context, req ctrl.Request) {
    // 1. Get ObjectBucketClaim
    obc := &objectbucketv1alpha1.ObjectBucketClaim{}
    if err := r.Get(ctx, req.NamespacedName, obc); err != nil {
        return ctrl.Result{}, client.IgnoreNotFound(err)
    }
    
    // 2. Check if OBC has backup label and is Bound
    if obc.Labels["kube-dc.com/backup-enabled"] != "true" {
        return ctrl.Result{}, nil
    }
    if obc.Status.Phase != "Bound" {
        return ctrl.Result{RequeueAfter: 10 * time.Second}, nil
    }
    
    // 3. Get S3 credentials from Secret
    secret := &corev1.Secret{}
    secretKey := types.NamespacedName{
        Name:      obc.Name,
        Namespace: obc.Namespace,
    }
    if err := r.Get(ctx, secretKey, secret); err != nil {
        return ctrl.Result{}, err
    }
    
    accessKey := string(secret.Data["AWS_ACCESS_KEY_ID"])
    secretKey := string(secret.Data["AWS_SECRET_ACCESS_KEY"])
    
    // 4. Create Secret in velero namespace
    veleroSecret := &corev1.Secret{
        ObjectMeta: metav1.ObjectMeta{
            Name:      fmt.Sprintf("bsl-%s", obc.Namespace),
            Namespace: "velero",
            Labels: map[string]string{
                "kube-dc.com/project": obc.Namespace,
                "kube-dc.com/managed": "true",
            },
        },
        Type: corev1.SecretTypeOpaque,
        StringData: map[string]string{
            "cloud": fmt.Sprintf("[default]\naws_access_key_id=%s\naws_secret_access_key=%s",
                accessKey, secretKey),
        },
    }
    if err := r.Create(ctx, veleroSecret); err != nil && !errors.IsAlreadyExists(err) {
        return ctrl.Result{}, err
    }
    
    // 5. Create BackupStorageLocation in velero namespace
    bsl := &velerov1.BackupStorageLocation{
        ObjectMeta: metav1.ObjectMeta{
            Name:      obc.Namespace, // Use namespace as BSL name
            Namespace: "velero",
            Labels: map[string]string{
                "kube-dc.com/project": obc.Namespace,
                "kube-dc.com/managed": "true",
            },
        },
        Spec: velerov1.BackupStorageLocationSpec{
            Provider: "aws",
            ObjectStorage: &velerov1.ObjectStorageLocation{
                Bucket: obc.Spec.BucketName,
            },
            Config: map[string]string{
                "region":           "us-east-1",
                "s3ForcePathStyle": "true",
                "s3Url":            "https://s3.kube-dc.cloud",  // Use public endpoint
            },
            Credential: &corev1.SecretKeySelector{
                LocalObjectReference: corev1.LocalObjectReference{
                    Name: veleroSecret.Name,
                },
                Key: "cloud",
            },
        },
    }
    
    if err := r.Create(ctx, bsl); err != nil && !errors.IsAlreadyExists(err) {
        return ctrl.Result{}, err
    }
    
    // 6. Update OBC status/annotation with BSL name
    if obc.Annotations == nil {
        obc.Annotations = make(map[string]string)
    }
    obc.Annotations["kube-dc.com/backup-storage-location"] = obc.Namespace
    if err := r.Update(ctx, obc); err != nil {
        return ctrl.Result{}, err
    }
    
    return ctrl.Result{}, nil
}
```

**Cleanup**: When OBC is deleted, the controller should also delete the BSL and Secret in the `velero` namespace.

### Data Flow

#### Backup Creation
1. User creates ObjectBucketClaim with label `kube-dc.com/backup-enabled: "true"`
2. Rook provisions S3 bucket and creates Secret/ConfigMap
3. Backup Storage Controller watches OBC, creates BSL in `velero` namespace
4. **User creates Backup resource in `velero` namespace** (Velero requirement) referencing the BSL name
5. Backup specifies `includedNamespaces` to target project resources
6. Velero controller processes Backup, stores metadata in S3 bucket (via https://s3.kube-dc.cloud)
7. For VMs: User creates VirtualMachineSnapshot first, then backs up snapshot resources

#### Restore Process
1. User creates Restore resource in their project namespace
2. Restore references a Backup by name
3. Velero controller downloads backup metadata from S3
4. Velero recreates Kubernetes resources in the namespace
5. Node agent restores PVC data from restic repository

## Implementation Phases

### Phase 1: Core Infrastructure (Current)
- ✅ Deploy Velero globally in `velero` namespace
- ✅ Deploy node-agent DaemonSet for PV backup
- ✅ Add Velero RBAC permissions to project roles
- ✅ Test manual backup/restore workflow

### Phase 2: Backup Storage Controller
**Timeline**: 2 weeks

**Deliverables**:
- Go controller in `kube-dc-k8-manager` or new `velero-backup-controller` repo
- Watch ObjectBucketClaims with label `kube-dc.com/backup-enabled: "true"`
- Auto-create BackupStorageLocation in `velero` namespace
- Auto-create credential Secret in `velero` namespace
- Handle cleanup when OBC is deleted
- Unit tests and integration tests

**Controller Deployment**:
- Runs in `kube-dc` namespace
- ServiceAccount with permissions to:
  - Read ObjectBucketClaims in all project namespaces
  - Read Secrets in all project namespaces
  - Create/Update/Delete BackupStorageLocations in `velero` namespace
  - Create/Update/Delete Secrets in `velero` namespace

### Phase 3: UI Integration
**Timeline**: 3 weeks

**Features**:
1. **Backup Bucket Creation**
   - New section in Project → Object Storage
   - Toggle "Enable as backup storage"
   - Automatically adds `kube-dc.com/backup-enabled: "true"` label

2. **Backup Management View**
   - List all Backups in current project
   - Show backup status, size, creation time, expiration
   - Create new backup with wizard:
     - Select resources: VMs, containers, specific labels
     - Choose what to include: cluster resources, PVs
     - Set retention period (TTL)
     - Schedule (optional)
   
3. **Restore Management**
   - Browse available backups
   - Preview backup contents
   - Create restore with options:
     - Restore to same namespace or different namespace
     - Include/exclude specific resources
     - Preserve node ports

### Phase 4: Scheduled Backups & Monitoring
**Timeline**: 2 weeks

**Features**:
- UI for creating Velero Schedule resources
- Backup success/failure notifications
- Metrics integration (backup size, duration, success rate)
- Prometheus alerts for failed backups

## Testing Results & Validation

### Test Environment
- **Velero Version**: v1.13.0
- **Storage Backend**: local-path (Rancher)
- **S3 Endpoint**: https://s3.kube-dc.cloud (Rook Ceph RGW)
- **Test VM**: Ubuntu 24.04 with 12GB root disk
- **Test Date**: March 4, 2026

### Test Results Summary

#### ✅ What Works
1. **Velero Installation**: Successfully deployed in `velero` namespace
2. **S3 Integration**: Backup metadata successfully stored in S3 buckets
3. **BackupStorageLocation**: Manual creation and configuration working
4. **Backup Resources**: 39-41 Kubernetes resources backed up per test
5. **VirtualMachineSnapshot**: KubeVirt snapshots create successfully
6. **Metadata Backup**: VM definitions, DataVolumes, PVCs, and configurations preserved
7. **Public S3 Access**: https://s3.kube-dc.cloud endpoint functional with TLS

#### ⚠️ Limitations Discovered
1. **Volume Data Not Backed Up**: 
   - Backup files in S3 are small (27-118 bytes) - metadata only
   - No actual VM disk data captured
   - `defaultVolumesToFsBackup: true` doesn't work with KubeVirt volumes

2. **Root Cause**: local-path Storage Incompatibility
   - KubeVirt attaches PVCs as **block devices** to virt-launcher pods
   - Velero's restic/node-agent requires **mounted filesystems**
   - local-path storage doesn't support CSI volume snapshots
   - VirtualMachineSnapshot creates metadata copies, not data snapshots

3. **Backup Namespace Requirement**:
   - Velero only watches Backup CRDs in `velero` namespace
   - Backups created in project namespaces are ignored
   - Users must create backups in `velero` namespace with `includedNamespaces` filter

### Test Cases Executed

#### Test 1: Basic Backup Creation
```yaml
apiVersion: velero.io/v1
kind: Backup
metadata:
  name: ubuntu-simple-backup
  namespace: velero  # Must be velero namespace
spec:
  includedNamespaces:
    - shalb-jumbolot  # Project namespace
  includedResources:
    - virtualmachines.kubevirt.io
    - datavolumes.cdi.kubevirt.io
    - persistentvolumeclaims
  storageLocation: shalb-jumbolot
  ttl: 24h
```
**Result**: ✅ Completed in 4 seconds, 39 items backed up (metadata only)

#### Test 2: VirtualMachineSnapshot Integration
```yaml
apiVersion: snapshot.kubevirt.io/v1beta1
kind: VirtualMachineSnapshot
metadata:
  name: ubuntu-snapshot-v2
  namespace: shalb-jumbolot
spec:
  source:
    apiGroup: kubevirt.io
    kind: VirtualMachine
    name: ubuntu
```
**Result**: ✅ Snapshot succeeded, VirtualMachineSnapshotContent created
**Limitation**: No VolumeSnapshot resources created (local-path doesn't support snapshots)

#### Test 3: Snapshot Restore
```yaml
apiVersion: snapshot.kubevirt.io/v1beta1
kind: VirtualMachineRestore
metadata:
  name: ubuntu-restore-test
  namespace: shalb-jumbolot
spec:
  target:
    kind: VirtualMachine
    name: ubuntu-restored
  virtualMachineSnapshotName: ubuntu-snapshot-v2
```
**Result**: ✅ Restore completed, new VM created
**Limitation**: Restored VM references same underlying PVC (no data copy)

#### Test 4: S3 Bucket Verification
- **Bucket**: shalb-jumbolot-backups
- **Files Created**: 
  - `ubuntu-backup-test-csi-volumesnapshotclasses.json.gz` (29 B)
  - `ubuntu-backup-test-logs.gz` (6.3 KB)
  - `ubuntu-backup-test-resource-list.json.gz` (27 B)
  - `ubuntu-backup-test-volumeinfo.json.gz` (27 B)
  - `velero-backup.json` (3.8 KB)
- **Total Backup Size**: ~10 KB (metadata only, no volume data)

### Implications for Production

**Current Setup (local-path)**:
- ✅ Suitable for: Disaster recovery of VM configurations
- ✅ Suitable for: Migrating VM definitions between clusters
- ✅ Suitable for: Backing up stateless workloads
- ❌ Not suitable for: VM data protection (data loss scenarios)
- ❌ Not suitable for: Point-in-time recovery with data

**Recommended for Production**:
Migrate to snapshot-capable storage:
1. **Ceph RBD** (already have Rook Ceph for object storage)
2. **Longhorn** (CNCF project, supports snapshots)
3. **Cloud provider storage** (if hybrid/cloud deployment)

### Next Steps Based on Testing
1. Document current limitations clearly for users
2. Update user docs to show metadata-only backup workflow
3. Plan migration to Ceph RBD for block storage
4. Implement warning in UI when creating backups without snapshot support
5. Provide data protection alternatives (VM-level backup agents)

## Technical Specifications

### RBAC Permissions

**Important**: Users need permissions in **both** project namespaces AND `velero` namespace.

**Project Admin Role** (in `velero` namespace):
```yaml
# Required for creating backups in velero namespace
- apiGroups: [velero.io]
  resources: [backups, restores, schedules]
  verbs: [create, get, list, watch, delete, update, patch]
- apiGroups: [velero.io]
  resources: [backups/status, restores/status]
  verbs: [get, list, watch]
```

**Project Admin Role** (in project namespace):
```yaml
# Required for VirtualMachineSnapshot
- apiGroups: [snapshot.kubevirt.io]
  resources: [virtualmachinesnapshots, virtualmachinesnapshotcontents, virtualmachinerestores]
  verbs: [create, get, list, watch, delete, update, patch]
```

**Project Developer Role**: Same as Admin

**Project Manager Role**: Read-only

**Project User Role**: Read-only

### Backup Resources Supported

**KubeVirt VMs** (metadata backup):
- `VirtualMachine` - VM configuration
- `VirtualMachineInstance` - Running VM state
- `DataVolume` - Volume provisioning specs
- `PersistentVolumeClaim` - Volume claims (metadata, not data)
- `VirtualMachineSnapshot` - KubeVirt snapshot resources
- `VirtualMachineSnapshotContent` - Snapshot metadata

**⚠️ Important**: With local-path storage, only **metadata** is backed up, not actual disk data

**Kubernetes Workloads**:
- `Deployment`, `StatefulSet`, `DaemonSet`, `Job`
- `Pod`, `ReplicaSet`
- `ConfigMap`, `Secret`
- `Service`, `Ingress`
- `PersistentVolumeClaim`

**Kube-DC Resources**:
- `EIp`, `FIp`
- `KdcCluster`, `KdcClusterDatastore`
- `ObjectBucketClaim`

### Volume Backup Strategy

**Current Limitation**: local-path storage doesn't support volume snapshots or file-level backup of KubeVirt volumes.

**Workarounds for Current Setup**:

1. **VirtualMachineSnapshot** (metadata only):
   ```yaml
   apiVersion: snapshot.kubevirt.io/v1beta1
   kind: VirtualMachineSnapshot
   metadata:
     name: vm-snapshot
     namespace: my-project
   spec:
     source:
       apiGroup: kubevirt.io
       kind: VirtualMachine
       name: my-vm
   ```
   Then backup the snapshot resources with Velero.

2. **Manual VM Export/Import** (for data migration):
   - Use `virtctl image-upload` to export VM disks
   - Store exported images in S3
   - Import when needed

**Future with Snapshot-Capable Storage** (Ceph RBD):

**Automatic PVC Backup** (requires snapshot-capable storage):
```yaml
apiVersion: velero.io/v1
kind: Backup
metadata:
  name: full-project-backup
  namespace: velero  # Must be in velero namespace
spec:
  snapshotVolumes: true  # Use CSI snapshots when available
  includedNamespaces:
    - my-project
  storageLocation: my-project
```

**Performance Considerations** (with snapshot-capable storage):
- CSI snapshot speed: Near-instant (copy-on-write)
- Large VMs (100GB+): Snapshot in seconds, restore depends on data transfer
- Schedule backups during low-usage periods for consistency

### Backup Retention & Quotas

**Default TTL**: 30 days (configurable per backup)

**Storage Quotas** (per organization billing plan):
- **Dev**: 50GB backup storage, 10 backups per project
- **Pro**: 200GB backup storage, 50 backups per project
- **Scale**: 1TB backup storage, 200 backups per project

Quotas enforced via:
1. S3 bucket quotas (same as object storage quotas)
2. Velero controller can be extended to reject backups exceeding limits

### Security Considerations

1. **Credential Isolation**:
   - Each project's backup bucket has unique S3 credentials
   - Credentials stored in project namespace (not accessible to other projects)
   - BSL credentials stored in `velero` namespace (protected by RBAC)

2. **Backup Access Control**:
   - Users can only create Backups in their own namespaces
   - Users cannot access BackupStorageLocations directly
   - Velero controller has cluster-admin to perform restores

3. **Data Encryption**:
   - S3 bucket encryption can be enabled at Rook level
   - Restic supports encryption (enabled by default with generated key)

4. **Audit Trail**:
   - All Backup/Restore operations logged via Kubernetes audit logs
   - Velero maintains operation history in backup metadata

## User Workflows

### Workflow 1: Enable Backups for Project

**As a Project Admin**:

1. Navigate to **Object Storage** → **Buckets**
2. Click **Create Bucket**
3. Enter bucket name: `my-project-backups`
4. Check **"Enable as backup storage"**
5. Click **Create**

**Behind the scenes**:
- UI creates ObjectBucketClaim with label `kube-dc.com/backup-enabled: "true"`
- Rook provisions bucket
- Backup Storage Controller creates BSL in `velero` namespace
- Status indicator shows "Backup storage ready"

### Workflow 2: Backup a Virtual Machine

**Current Workflow (Metadata Only)**:

**Step 1: Create VirtualMachineSnapshot**
```bash
kubectl apply -f - <<EOF
apiVersion: snapshot.kubevirt.io/v1beta1
kind: VirtualMachineSnapshot
metadata:
  name: ubuntu-snapshot-$(date +%Y%m%d)
  namespace: my-project
spec:
  source:
    apiGroup: kubevirt.io
    kind: VirtualMachine
    name: ubuntu
EOF
```

**Step 2: Backup VM and Snapshot Resources**
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
EOF
```

**⚠️ Limitation**: This backs up VM configuration only, not disk data

### Workflow 3: Restore from Backup

**Via UI**:
1. Navigate to **Backups** → select backup → **Actions** → **Restore**
2. Choose restore options:
   - Restore to original namespace: Yes
   - Include persistent volumes: Yes
3. Click **Restore**
4. Monitor restore progress

**Via kubectl**:
```bash
kubectl apply -f - <<EOF
apiVersion: velero.io/v1
kind: Restore
metadata:
  name: restore-ubuntu-vm
  namespace: velero  # Must be velero namespace
spec:
  backupName: ubuntu-vm-backup-20260304
  includedNamespaces:
    - my-project
EOF

# Check restore status
kubectl get restore restore-ubuntu-vm -n velero -o yaml
```

**⚠️ Limitation**: Restores VM configuration, but references original PVCs (no data copy)

### Workflow 4: Schedule Recurring Backups

**Via kubectl**:
```bash
kubectl apply -f - <<EOF
apiVersion: velero.io/v1
kind: Schedule
metadata:
  name: daily-vm-metadata-backup
  namespace: velero  # Must be velero namespace
spec:
  schedule: "0 2 * * *"  # Every day at 2 AM
  template:
    includedNamespaces:
      - my-project
    includedResources:
      - virtualmachines.kubevirt.io
      - virtualmachinesnapshots.snapshot.kubevirt.io
      - datavolumes.cdi.kubevirt.io
      - persistentvolumeclaims
    storageLocation: my-project
    ttl: 720h  # 30 days retention
EOF
```

**Note**: Consider creating VirtualMachineSnapshots before scheduled backups run

## Testing Strategy

### Unit Tests
- Backup Storage Controller reconciliation logic
- RBAC permission validation
- Credential handling and secret creation

### Integration Tests
1. **Backup Creation Test**:
   - Create ObjectBucketClaim with backup label
   - Verify BSL is created in `velero` namespace
   - Verify credentials are copied correctly
   - Create Backup resource
   - Verify backup completes successfully

2. **Restore Test**:
   - Create VM with data disk
   - Create backup
   - Delete VM
   - Restore from backup
   - Verify VM is running and data is intact

3. **Multi-Project Isolation Test**:
   - Create backups in two different projects
   - Verify Project A cannot restore Project B's backups
   - Verify backups are stored in separate S3 buckets

4. **Cleanup Test**:
   - Delete ObjectBucketClaim
   - Verify BSL is removed from `velero` namespace
   - Verify credential secret is removed

### Performance Tests
- Backup/restore time for 10GB VM
- Backup/restore time for 100GB VM
- Concurrent backups from multiple projects
- Storage overhead (backup size vs. original data size)

## Success Metrics

**Adoption**:
- % of projects with backup storage enabled
- Number of backups created per day/week

**Reliability**:
- Backup success rate (target: 99%)
- Restore success rate (target: 98%)
- Mean time to restore (MTTR)

**Performance**:
- Backup throughput (GB/hour)
- Restore throughput (GB/hour)
- Backup storage efficiency (compression ratio)

## Documentation Requirements

### User Documentation
1. **Backups & Snapshots Guide** (`docs/cloud/backups-snapshots.md`):
   - Overview of backup capabilities
   - How to enable backup storage
   - Creating manual backups
   - Scheduling automated backups
   - Restoring from backups
   - Best practices and troubleshooting

2. **API Reference**:
   - Backup resource specification
   - Restore resource specification
   - Schedule resource specification

### Developer Documentation
1. **Backup Storage Controller Design** (`docs/architecture/backup-storage-controller.md`)
2. **Velero Integration Guide** (`docs/architecture/velero-integration.md`)
3. **Testing Procedures** (`docs/testing/backup-restore-testing.md`)

## Risks & Mitigations

| Risk | Impact | Mitigation |
|------|--------|------------|
| Large VM backups fail | High | Implement backup retry logic, increase node-agent resources |
| S3 bucket quota exceeded | Medium | Implement backup rotation, quota monitoring, user notifications |
| Restore conflicts with existing resources | Medium | Validate restore target namespace, provide conflict resolution options |
| Controller failure leaves orphaned BSLs | Low | Implement garbage collection for orphaned BSLs |
| Backup storage costs exceed budget | Medium | Set default TTLs, implement automated cleanup policies |

## Open Questions

1. **Cross-cluster restore**: Should we support restoring backups to a different management cluster?
   - **Decision**: Phase 2 feature, requires external S3 endpoint

2. **Backup verification**: Should we automatically verify backup integrity?
   - **Decision**: Manual verification initially, automated in Phase 4

3. **Backup compression**: Should we enable compression for all backups?
   - **Decision**: Yes, restic compression enabled by default

4. **VM quiescing**: Should we quiesce VMs before backup (freeze filesystem)?
   - **Decision**: Not initially, users can manually shut down VMs if needed

## References

- [Velero Documentation](https://velero.io/docs/)
- [Velero RBAC Documentation](https://velero.io/docs/main/rbac/)
- [Velero S3 Configuration](https://github.com/vmware-tanzu/velero-plugin-for-aws)
- [Rook Ceph Object Storage](https://rook.io/docs/rook/latest/Storage-Configuration/Object-Storage-RGW/object-storage/)
- [KubeVirt Backup Best Practices](https://kubevirt.io/user-guide/operations/backup_restore/)

## Appendix: Manual Setup (Without Controller)

For immediate testing or temporary deployments without the Backup Storage Controller:

### Step 1: Create Backup Bucket
```yaml
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
```

### Step 2: Extract Credentials
```bash
ACCESS_KEY=$(kubectl get secret project-backups -n my-project -o jsonpath='{.data.AWS_ACCESS_KEY_ID}' | base64 -d)
SECRET_KEY=$(kubectl get secret project-backups -n my-project -o jsonpath='{.data.AWS_SECRET_ACCESS_KEY}' | base64 -d)
BUCKET=$(kubectl get cm project-backups -n my-project -o jsonpath='{.data.BUCKET_NAME}')
```

### Step 3: Create BSL Manually
```bash
# Create credential secret in velero namespace
kubectl create secret generic bsl-my-project -n velero --from-literal=cloud="[default]
aws_access_key_id=${ACCESS_KEY}
aws_secret_access_key=${SECRET_KEY}"

# Create BackupStorageLocation
kubectl apply -f - <<EOF
apiVersion: velero.io/v1
kind: BackupStorageLocation
metadata:
  name: my-project
  namespace: velero
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

### Step 4: Create Backup
Now users can reference the BSL name in their Backup resources (see Workflow 2 above).
