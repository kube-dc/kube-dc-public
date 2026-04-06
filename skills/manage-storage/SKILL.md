---
name: manage-storage
description: Manage Kube-DC storage resources — create S3 buckets (ObjectBucketClaim), DataVolumes for VMs, and PersistentVolumeClaims for containers.
---

## Prerequisites
- Target project must exist and be Ready
- Project namespace: `{org}-{project}`

## S3 Object Storage (ObjectBucketClaim)

### Create Bucket

```yaml
apiVersion: objectbucket.io/v1alpha1
kind: ObjectBucketClaim
metadata:
  name: {bucket-name}
  namespace: {project-namespace}
  labels:
    kube-dc.com/organization: {org}    # REQUIRED label
spec:
  bucketName: {project-namespace}-{bucket-name}
  storageClassName: ceph-bucket
```

**Required**: The `kube-dc.com/organization` label MUST be set.

### Auto-Created Resources

When OBC is provisioned, Kubernetes creates:

| Resource | Name | Keys |
|----------|------|------|
| Secret | `{bucket-name}` | `AWS_ACCESS_KEY_ID`, `AWS_SECRET_ACCESS_KEY` |
| ConfigMap | `{bucket-name}` | `BUCKET_HOST`, `BUCKET_NAME`, `BUCKET_PORT`, `BUCKET_REGION` |

### Mount in Workload

```yaml
containers:
  - name: app
    envFrom:
      - secretRef:
          name: {bucket-name}
      - configMapRef:
          name: {bucket-name}
    env:
      - name: S3_ENDPOINT
        value: "https://s3.kube-dc.cloud"
```

### AWS CLI Access

```bash
# Get credentials
export AWS_ACCESS_KEY_ID=$(kubectl get secret {bucket-name} -n {namespace} -o jsonpath='{.data.AWS_ACCESS_KEY_ID}' | base64 -d)
export AWS_SECRET_ACCESS_KEY=$(kubectl get secret {bucket-name} -n {namespace} -o jsonpath='{.data.AWS_SECRET_ACCESS_KEY}' | base64 -d)

# Use AWS CLI
aws s3 ls s3://{project-namespace}-{bucket-name}/ --endpoint-url https://s3.kube-dc.cloud
aws s3 cp myfile.txt s3://{project-namespace}-{bucket-name}/ --endpoint-url https://s3.kube-dc.cloud
```

## Block Storage (DataVolume for VMs)

### Import from URL

```yaml
apiVersion: cdi.kubevirt.io/v1beta1
kind: DataVolume
metadata:
  name: {disk-name}
  namespace: {project-namespace}
spec:
  source:
    http:
      url: "{image-url}"
  storage:
    accessModes: [ReadWriteOnce]
    resources:
      requests:
        storage: {size}    # e.g. 20Gi
    storageClassName: local-path
```

### Blank Disk (Additional Data Volume)

```yaml
apiVersion: cdi.kubevirt.io/v1beta1
kind: DataVolume
metadata:
  name: {disk-name}
  namespace: {project-namespace}
spec:
  source:
    blank: {}
  storage:
    accessModes: [ReadWriteOnce]
    resources:
      requests:
        storage: {size}
    storageClassName: local-path
```

### Attach Additional Disk to VM

Add to VM spec:
```yaml
spec:
  template:
    spec:
      domain:
        devices:
          disks:
            - name: datadisk
              disk:
                bus: virtio
      volumes:
        - name: datadisk
          dataVolume:
            name: {disk-name}
```

## Block Storage (PVC for Containers)

```yaml
apiVersion: v1
kind: PersistentVolumeClaim
metadata:
  name: {pvc-name}
  namespace: {project-namespace}
spec:
  accessModes: [ReadWriteOnce]
  storageClassName: local-path
  resources:
    requests:
      storage: {size}
```

## Safety
- OBC MUST have `kube-dc.com/organization: {org}` label
- S3 endpoint: `https://s3.kube-dc.cloud`, region: `us-east-1`
- Always use `storageClassName: local-path` (default)
- Bucket name pattern: `{namespace}-{name}` — must be globally unique
