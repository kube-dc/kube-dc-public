# Kube-DC Cloud Storage Design

## Overview

Storage architecture for kube-dc.cloud production deployment. Phased approach starting with minimal S3 backup storage, expandable to full replicated Ceph cluster.

---

## Infrastructure Summary

### Available Storage

| Host | Role | Disk | Size | Current Use | Available |
|------|------|------|------|-------------|-----------|
| ams1-blade179-8 | Master 1 | nvme0n1 | 1.7T | OS (/) | ~1.6T |
| ams1-blade184-5 | Master 2 | nvme0n1 | 1.7T | OS (/) | ~1.6T |
| ams1-blade58-2 | Master 3 | sda | 931G | OS (/) | ~900G |
| | | nvme0n1 | 1.8T | /home | 1.8T (can repurpose) |
| ams1-blade187-2 | Worker | sda | 20T | OS (/) | ~19T |

**Total Capacity**: ~25T across 4 nodes

---

## Storage Components

### 1. local-path Provisioner (Primary)

Default storage class for general workloads.

```yaml
apiVersion: v1
kind: ConfigMap
metadata:
  name: local-path-config
  namespace: local-path-storage
data:
  config.json: |
    {
      "nodePathMap": [
        {
          "node": "DEFAULT_PATH_FOR_NON_LISTED_NODES",
          "paths": ["/opt/local-path-provisioner"]
        }
      ]
    }
```

**Use cases**:
- Prometheus/Loki data
- Keycloak PostgreSQL
- etcd (Kamaji)
- General PVCs

### 2. Rook Ceph (S3 Object Storage)

For S3-compatible backup storage and future RWX volumes.

---

## Phased Deployment

### Phase 1: local-path Only ✅

**Timeline**: Day 1

```
┌─────────────────────────────────────────────────────────┐
│                    Kubernetes Cluster                    │
├──────────────┬──────────────┬──────────────┬────────────┤
│   Master 1   │   Master 2   │   Master 3   │   Worker   │
│   1.7T NVMe  │   1.7T NVMe  │   931G SSD   │   20T HDD  │
│              │              │   +1.8T NVMe │            │
├──────────────┴──────────────┴──────────────┴────────────┤
│  local-path-provisioner (default StorageClass)          │
│  Path: /opt/local-path-provisioner                      │
└─────────────────────────────────────────────────────────┘
```

**Deliverables**:
- local-path-provisioner deployed
- StorageClass `local-path` as default
- Platform PVCs working (Prometheus, Loki, Keycloak, etcd)

### Phase 2: Rook Ceph S3 on Worker ⬜

**Timeline**: Week 1-2

Single-node Ceph cluster on worker for S3 backup storage.

```
┌─────────────────────────────────────────────────────────┐
│                    Kubernetes Cluster                    │
├──────────────┬──────────────┬──────────────┬────────────┤
│   Master 1   │   Master 2   │   Master 3   │   Worker   │
│              │              │              │            │
│              │              │              │  ┌──────┐  │
│              │              │              │  │ MON  │  │
│              │              │              │  │ MGR  │  │
│              │              │              │  │ OSD  │  │
│              │              │              │  │ RGW  │  │
│              │              │              │  └──────┘  │
│              │              │              │   ~10T     │
└──────────────┴──────────────┴──────────────┴────────────┘
                                                   ↓
                                            S3 API (backup)
```

**Configuration**:

```yaml
# Rook Ceph Cluster - Phase 2
apiVersion: ceph.rook.io/v1
kind: CephCluster
metadata:
  name: rook-ceph
  namespace: rook-ceph
spec:
  cephVersion:
    image: quay.io/ceph/ceph:v19.2.1
  dataDirHostPath: /var/lib/rook
  
  mon:
    count: 1  # Single mon for Phase 2
    allowMultiplePerNode: true
  
  mgr:
    count: 1
    modules:
      - name: rook
        enabled: true
  
  storage:
    storageClassDeviceSets:
      - name: s3-backup
        count: 1
        portable: false
        placement:
          nodeAffinity:
            requiredDuringSchedulingIgnoredDuringExecution:
              nodeSelectorTerms:
                - matchExpressions:
                    - key: kubernetes.io/hostname
                      operator: In
                      values:
                        - ams1-blade187-2
        volumeClaimTemplates:
          - metadata:
              name: data
            spec:
              resources:
                requests:
                  storage: 10Ti
              storageClassName: local-path
              volumeMode: Filesystem
              accessModes:
                - ReadWriteOnce
```

**Object Store (S3)**:

```yaml
apiVersion: ceph.rook.io/v1
kind: CephObjectStore
metadata:
  name: backup-store
  namespace: rook-ceph
spec:
  metadataPool:
    replicated:
      size: 1  # No replication
  dataPool:
    replicated:
      size: 1  # No replication
  gateway:
    port: 80
    instances: 1
```

**Deliverables**:
- Rook Ceph operator deployed
- Single OSD on worker node (~10T)
- S3 endpoint: `s3.kube-dc.cloud`
- Backup bucket for platform backups

### Phase 3: HA Ceph Cluster ⬜

**Timeline**: When needed for production storage

Extend to 3+ OSDs across nodes for replication.

```
┌─────────────────────────────────────────────────────────┐
│                    Kubernetes Cluster                    │
├──────────────┬──────────────┬──────────────┬────────────┤
│   Master 1   │   Master 2   │   Master 3   │   Worker   │
│   ┌──────┐   │   ┌──────┐   │   ┌──────┐   │  ┌──────┐  │
│   │ MON  │   │   │ MON  │   │   │ MON  │   │  │ OSD  │  │
│   │ OSD  │   │   │ OSD  │   │   │ OSD  │   │  │ (lg) │  │
│   └──────┘   │   └──────┘   │   └──────┘   │  └──────┘  │
│    ~500G     │    ~500G     │    ~500G     │   ~10T     │
└──────────────┴──────────────┴──────────────┴────────────┘
         ↓              ↓              ↓            ↓
                    Replicated Storage (size=2)
```

**Changes**:
- Add OSDs on master nodes (using local-path PVCs)
- Increase MON count to 3
- Update pools to `size: 2`
- Enable CephFS for RWX volumes

**Configuration Update**:

```yaml
storage:
  storageClassDeviceSets:
    - name: ceph-osd-masters
      count: 3  # One per master
      portable: false
      placement:
        nodeAffinity:
          requiredDuringSchedulingIgnoredDuringExecution:
            nodeSelectorTerms:
              - matchExpressions:
                  - key: node-role.kubernetes.io/control-plane
                    operator: Exists
      volumeClaimTemplates:
        - metadata:
            name: data
          spec:
            resources:
              requests:
                storage: 500Gi
            storageClassName: local-path
            volumeMode: Filesystem
            accessModes:
              - ReadWriteOnce
    - name: ceph-osd-worker
      count: 1
      portable: false
      placement:
        nodeAffinity:
          requiredDuringSchedulingIgnoredDuringExecution:
            nodeSelectorTerms:
              - matchExpressions:
                  - key: kubernetes.io/hostname
                    operator: In
                    values:
                      - ams1-blade187-2
      volumeClaimTemplates:
        - metadata:
            name: data
          spec:
            resources:
              requests:
                storage: 10Ti
            storageClassName: local-path
            volumeMode: Filesystem
            accessModes:
              - ReadWriteOnce
```

---

## Storage Classes

| Name | Provisioner | Use Case | Phase |
|------|-------------|----------|-------|
| `local-path` | rancher.io/local-path | Default, platform PVCs | 1 |
| `ceph-block` | rook-ceph.rbd.csi.ceph.com | Block storage (RWO) | 3 |
| `ceph-filesystem` | rook-ceph.cephfs.csi.ceph.com | Shared storage (RWX) | 3 |
| `ceph-bucket` | rook-ceph.ceph.rook.io/bucket | S3 object storage | 2 |

---

## Resource Requirements

### Rook Ceph Components

| Component | Count | CPU | RAM | Phase |
|-----------|-------|-----|-----|-------|
| rook-operator | 1 | 500m | 256Mi | 2 |
| MON | 1→3 | 100m | 384Mi | 2→3 |
| MGR | 1 | 500m | 2Gi | 2 |
| OSD | 1→4 | 300m | 1Gi | 2→3 |
| RGW | 1 | 100m | 256Mi | 2 |
| MDS | 1 | 500m | 2Gi | 3 |

**Phase 2 Total**: ~1.5 CPU, ~4Gi RAM  
**Phase 3 Total**: ~4 CPU, ~10Gi RAM

---

## Backup Strategy

### Platform Backups (Phase 2)

| Data | Backup Target | Frequency |
|------|---------------|-----------|
| etcd (RKE2) | S3: `backup-store` | Daily |
| Prometheus | S3: `backup-store` | Weekly |
| Keycloak DB | S3: `backup-store` | Daily |
| Loki | S3: `backup-store` | Weekly |

### S3 Backup Bucket

```yaml
apiVersion: objectbucket.io/v1alpha1
kind: ObjectBucketClaim
metadata:
  name: platform-backups
  namespace: kube-system
spec:
  bucketName: platform-backups
  storageClassName: ceph-bucket
```

---

## Limitations & Trade-offs

### Phase 2 (Single Node Ceph)

| Aspect | Status | Mitigation |
|--------|--------|------------|
| **Durability** | ⚠️ Worker node failure = S3 data loss | Acceptable for backups only |
| **Performance** | ⚠️ Single OSD | Sufficient for backup workload |
| **HA** | ❌ No MON quorum | Upgrade to Phase 3 when needed |

### Using local-path for Ceph OSDs

| Aspect | Impact |
|--------|--------|
| **Performance** | ~10-20% overhead vs raw device |
| **Simplicity** | ✅ No disk repartitioning needed |
| **Flexibility** | ✅ Easy to resize/move |

---

## Success Criteria

### Phase 1
- [ ] local-path-provisioner deployed
- [ ] All platform PVCs bound and healthy

### Phase 2
- [ ] Rook Ceph operator running
- [ ] Single OSD healthy on worker
- [ ] S3 endpoint accessible
- [ ] Backup job running successfully

### Phase 3
- [ ] 3+ OSDs across nodes
- [ ] 3 MONs with quorum
- [ ] Pool replication size=2
- [ ] CephFS available for RWX

---

## References

- [Rook Ceph Documentation](https://rook.io/docs/rook/latest/)
- [local-path-provisioner](https://github.com/rancher/local-path-provisioner)
- [Ceph Object Storage](https://docs.ceph.com/en/latest/radosgw/)
