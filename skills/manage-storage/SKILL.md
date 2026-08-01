---
name: manage-storage
description: Create ObjectBucketClaims for S3-compatible buckets, DataVolumes for VM disks, and PersistentVolumeClaims for Project workloads.
---

## Prerequisites

- The target Project exists and is Ready.
- Know its backing namespace: `{organization}-{project}`.
- Check storage and object-storage quota.
- Read StorageClass names and the external S3 endpoint from the live
  installation. Do not assume every provider uses the same classes or domain.

## Object Storage

### Create a Bucket Claim

```yaml
apiVersion: objectbucket.io/v1alpha1
kind: ObjectBucketClaim
metadata:
  name: "{bucket-name}"
  namespace: "{backing-namespace}"
  labels:
    kube-dc.com/organization: "{organization}"
spec:
  bucketName: "{backing-namespace}-{bucket-name}"
  storageClassName: "{bucket-storage-class}"
```

The Organization label is required for correct dashboard attribution,
Organization bucket totals, and usage reporting on manually created claims.
The underlying OBC provisioner may still bind an unlabeled claim, but Kube-DC
cannot attribute it correctly.

A bound claim creates a same-name Secret and ConfigMap in the Project's backing
namespace:

| Resource | Important keys |
|---|---|
| Secret | `AWS_ACCESS_KEY_ID`, `AWS_SECRET_ACCESS_KEY` |
| ConfigMap | `BUCKET_HOST`, `BUCKET_NAME`, `BUCKET_PORT`, `BUCKET_REGION` |

These are per-bucket credentials. Use them only for that bucket. Organization
account keys do not automatically grant data access to all ObjectBucketClaims.

### Mount Bucket Configuration

```yaml
envFrom:
- secretRef:
    name: "{bucket-name}"
- configMapRef:
    name: "{bucket-name}"
```

For in-Project traffic, build the endpoint from `BUCKET_HOST` and
`BUCKET_PORT`. For a workstation, set `S3_ENDPOINT` to the external endpoint
shown by the Kube-DC console or provider:

```bash
export AWS_ACCESS_KEY_ID="$(
  kubectl get secret {bucket-name} -n {backing-namespace} \
    -o jsonpath='{.data.AWS_ACCESS_KEY_ID}' | base64 -d
)"
export AWS_SECRET_ACCESS_KEY="$(
  kubectl get secret {bucket-name} -n {backing-namespace} \
    -o jsonpath='{.data.AWS_SECRET_ACCESS_KEY}' | base64 -d
)"
export BUCKET_NAME="$(
  kubectl get configmap {bucket-name} -n {backing-namespace} \
    -o jsonpath='{.data.BUCKET_NAME}'
)"

aws --endpoint-url "$S3_ENDPOINT" s3 ls "s3://$BUCKET_NAME/"
```

Do not print the Secret values.

## VM Disks with DataVolume

Choose a VM storage class offered to the Project. Prefer a digest-pinned
registry image from the live OS image catalog:

```yaml
apiVersion: cdi.kubevirt.io/v1beta1
kind: DataVolume
metadata:
  name: "{disk-name}"
  namespace: "{backing-namespace}"
spec:
  source:
    registry:
      url: "docker://{registry-image}@sha256:{digest}"
      pullMethod: node
  pvc:
    accessModes:
    - ReadWriteOnce
    resources:
      requests:
        storage: "{size}"
    storageClassName: "{vm-storage-class}"
```

Use an HTTP source for provider-supported custom images or installation media:

```yaml
spec:
  source:
    http:
      url: "{image-url}"
  storage:
    accessModes:
    - ReadWriteOnce
    resources:
      requests:
        storage: "{size}"
    storageClassName: "{vm-storage-class}"
```

Create a blank data disk with `source.blank: {}`. See
[datavolume-template.yaml](datavolume-template.yaml) for all three shapes.

Attach a DataVolume to a VM by referencing it under both
`domain.devices.disks` and `volumes[].dataVolume.name`.

## Container Persistent Volumes

```yaml
apiVersion: v1
kind: PersistentVolumeClaim
metadata:
  name: "{claim-name}"
  namespace: "{backing-namespace}"
spec:
  accessModes:
  - ReadWriteOnce
  storageClassName: "{project-storage-class}"
  resources:
    requests:
      storage: "{size}"
```

Choose access modes supported by the selected StorageClass. Do not assume
ReadWriteMany or live migration support from a class name alone.

## Verification

```bash
# Bucket and generated connection resources
kubectl get obc {bucket-name} -n {backing-namespace}
kubectl get secret,configmap {bucket-name} -n {backing-namespace}

# VM disk
kubectl get dv {disk-name} -n {backing-namespace} \
  -o jsonpath='{.status.phase}{"\n"}'
kubectl get pvc {disk-name} -n {backing-namespace}

# Container claim
kubectl get pvc {claim-name} -n {backing-namespace}
```

Expected states are `Bound` for ObjectBucketClaim/PVC and `Succeeded` for a
completed DataVolume import. If a resource remains Pending, inspect its events
and confirm quota, class availability, image access, and placement.

## Safety

- Label manually created OBCs for the owning Organization.
- Use the claim's generated per-bucket credentials.
- Read endpoints, regions, and StorageClasses from the installation.
- Pin registry images by digest for reproducible boot disks.
- Deleting an ObjectBucketClaim can permanently delete every object in its
  bucket. Confirm retention requirements first.
