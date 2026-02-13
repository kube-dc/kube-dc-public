# Deploying Rook Ceph Object Storage (S3) for Kube-DC

S3-compatible object storage backed by Rook Ceph RGW, integrated with Kube-DC billing, quotas, and the UI.

## Prerequisites

- Kubernetes cluster with Kube-DC installed
- A worker node with available block storage (dedicated disk or loop device)
- [Envoy Gateway](https://gateway.envoyproxy.io/) for external S3 endpoint (optional)
- [cert-manager](https://cert-manager.io/) with `--enable-gateway-api` for TLS (optional)
- DNS record for your S3 endpoint (e.g., `s3.example.com`)

## Architecture

```
s3.example.com (HTTPS)
  → Gateway (TLS termination)
    → rook-ceph-rgw-<store-name>:80 (RGW)
      → Ceph OSD (block device on worker)
```

| Component | Purpose | Resources |
|-----------|---------|-----------|
| Rook Operator | Manages Ceph lifecycle | 200m CPU / 256Mi |
| MON | Cluster monitor | 100m CPU / 384Mi |
| MGR | Cluster manager | 200m CPU / 512Mi |
| OSD (per disk) | Object storage daemon | 300m CPU / 1Gi |
| RGW | S3 gateway | 100m CPU / 256Mi |

---

## Step 1: Install Rook Operator

```bash
kubectl create namespace rook-ceph

helm repo add rook-release https://charts.rook.io/release
helm repo update rook-release

helm install rook-ceph rook-release/rook-ceph \
  --namespace rook-ceph \
  --version v1.19.1 \
  --set crds.enabled=true \
  --set allowLoopDevices=true \
  --set resources.requests.cpu=200m \
  --set resources.requests.memory=256Mi \
  --set resources.limits.memory=512Mi

# Verify operator is running
kubectl wait --for=condition=Ready pod -l app=rook-ceph-operator -n rook-ceph --timeout=120s
```

If using loop devices (dev/test), enable them:

```bash
kubectl set env deployment/rook-ceph-operator -n rook-ceph ROOK_ALLOW_LOOP_DEVICES=true
```

---

## Step 2: Prepare Storage

### Option A: Dedicated Disk (Production)

If your worker node has a dedicated disk (e.g., `/dev/sdb`), skip to Step 3 and reference it directly in the `CephCluster` spec.

### Option B: Loop Device (Dev/Test)

For testing on nodes without a spare disk, create a sparse file-backed loop device:

```yaml
# 01-loop-device-setup.yaml
apiVersion: v1
kind: Pod
metadata:
  name: setup-loop-device
  namespace: rook-ceph
spec:
  nodeName: <YOUR_WORKER_NODE>     # Replace with your worker node name
  restartPolicy: Never
  hostPID: true
  containers:
  - name: setup
    image: ubuntu:24.04
    securityContext:
      privileged: true
    command:
    - /bin/bash
    - -c
    - |
      set -ex
      LOOP_FILE=/host/var/lib/ceph-osd-block.img
      LOOP_SIZE_GB=500                # Adjust size as needed

      if losetup -a | grep -q ceph-osd-block; then
        echo "Loop device already set up:"
        losetup -a | grep ceph-osd-block
        exit 0
      fi

      if [ ! -f "$LOOP_FILE" ]; then
        echo "Creating ${LOOP_SIZE_GB}GB sparse file..."
        truncate -s ${LOOP_SIZE_GB}G "$LOOP_FILE"
      fi

      LOOP_DEV=$(losetup --find --show "$LOOP_FILE")
      echo "Loop device created: $LOOP_DEV"

      # Persist across reboots via systemd
      cat > /host/etc/systemd/system/ceph-loop-device.service << 'SVC'
      [Unit]
      Description=Setup Ceph OSD loop device
      Before=kubelet.service
      After=local-fs.target

      [Service]
      Type=oneshot
      RemainAfterExit=yes
      ExecStart=/bin/bash -c 'losetup /dev/loop0 /var/lib/ceph-osd-block.img || true'
      ExecStop=/bin/bash -c 'losetup -d /dev/loop0 || true'

      [Install]
      WantedBy=multi-user.target
      SVC

      nsenter -t 1 -m -u -i -n -p -- systemctl daemon-reload
      nsenter -t 1 -m -u -i -n -p -- systemctl enable ceph-loop-device.service
      echo "Done."
    volumeMounts:
    - name: host-root
      mountPath: /host
      mountPropagation: Bidirectional
    - name: dev
      mountPath: /dev
  volumes:
  - name: host-root
    hostPath:
      path: /
  - name: dev
    hostPath:
      path: /dev
```

```bash
kubectl apply -f 01-loop-device-setup.yaml
kubectl wait --for=jsonpath='{.status.phase}'=Succeeded pod/setup-loop-device -n rook-ceph --timeout=60s
kubectl delete pod setup-loop-device -n rook-ceph
```

---

## Step 3: Deploy CephCluster

Adjust `nodeName`, device name, and placement to match your environment.

```yaml
# 02-ceph-cluster.yaml
apiVersion: ceph.rook.io/v1
kind: CephCluster
metadata:
  name: rook-ceph
  namespace: rook-ceph
spec:
  cephVersion:
    image: quay.io/ceph/ceph:v19.2.1
    allowUnsupported: false
  dataDirHostPath: /var/lib/rook
  mon:
    count: 1                        # Use 3 for production HA
    allowMultiplePerNode: true
  mgr:
    count: 1
    modules:
      - name: rook
        enabled: true
  dashboard:
    enabled: false
  storage:
    useAllNodes: false
    useAllDevices: false
    nodes:
      - name: <YOUR_WORKER_NODE>    # Replace with your worker node name
        devices:
          - name: loop0             # Or sdb, nvme0n1, etc.
  placement:
    mon:
      nodeAffinity:
        requiredDuringSchedulingIgnoredDuringExecution:
          nodeSelectorTerms:
            - matchExpressions:
                - key: kubernetes.io/hostname
                  operator: In
                  values:
                    - <YOUR_WORKER_NODE>
    mgr:
      nodeAffinity:
        requiredDuringSchedulingIgnoredDuringExecution:
          nodeSelectorTerms:
            - matchExpressions:
                - key: kubernetes.io/hostname
                  operator: In
                  values:
                    - <YOUR_WORKER_NODE>
  resources:
    mon:
      requests:
        cpu: 100m
        memory: 384Mi
      limits:
        memory: 1Gi
    mgr:
      requests:
        cpu: 200m
        memory: 512Mi
      limits:
        memory: 2Gi
    osd:
      requests:
        cpu: 300m
        memory: 1Gi
      limits:
        memory: 4Gi
```

```bash
kubectl apply -f 02-ceph-cluster.yaml

# Wait for MON, MGR, OSD (~2-3 min)
watch kubectl get pods -n rook-ceph | grep -E 'mon|mgr|osd'

# Verify Ceph health
kubectl -n rook-ceph exec deploy/rook-ceph-operator -- \
  ceph -c /var/lib/rook/rook-ceph/rook-ceph.config status
```

> **Note**: `HEALTH_WARN` with "OSD count < osd_pool_default_size" is expected for single-node deployments with replication size 1.

---

## Step 4: Deploy CephObjectStore (RGW)

```yaml
# 03-object-store.yaml
apiVersion: ceph.rook.io/v1
kind: CephObjectStore
metadata:
  name: my-store
  namespace: rook-ceph
spec:
  metadataPool:
    replicated:
      size: 1                       # Use 3 for production HA
  dataPool:
    replicated:
      size: 1                       # Use 3 for production HA
  preservePoolsOnDelete: true
  gateway:
    port: 80
    instances: 1                    # Use 2+ for production HA
    resources:
      requests:
        cpu: 100m
        memory: 256Mi
      limits:
        memory: 1Gi
    placement:
      nodeAffinity:
        requiredDuringSchedulingIgnoredDuringExecution:
          nodeSelectorTerms:
            - matchExpressions:
                - key: kubernetes.io/hostname
                  operator: In
                  values:
                    - <YOUR_WORKER_NODE>
```

```bash
kubectl apply -f 03-object-store.yaml

# Wait for RGW pod
kubectl wait --for=condition=Ready pod -l app=rook-ceph-rgw -n rook-ceph --timeout=120s

# Verify service
kubectl get svc -n rook-ceph | grep rgw
```

---

## Step 5: Create StorageClass for Bucket Provisioning

```yaml
# 04-storage-class.yaml
apiVersion: storage.k8s.io/v1
kind: StorageClass
metadata:
  name: ceph-bucket
provisioner: rook-ceph.ceph.rook.io/bucket
reclaimPolicy: Delete
parameters:
  objectStoreName: my-store
  objectStoreNamespace: rook-ceph
```

```bash
kubectl apply -f 04-storage-class.yaml
kubectl get sc ceph-bucket
```

---

## Step 6: Expose S3 Endpoint (Optional — External Access)

Skip this step if you only need internal S3 access via the in-cluster service `rook-ceph-rgw-my-store.rook-ceph.svc:80`.

### 6.1 DNS

Create an A record pointing your S3 domain to your gateway's load balancer IP:

```
s3.example.com → <GATEWAY_LB_IP>
```

### 6.2 Gateway Listener

Add an HTTPS listener for the S3 hostname to your gateway:

```bash
kubectl patch gateway eg -n envoy-gateway-system --type=json \
  -p='[{"op":"add","path":"/spec/listeners/-","value":{
    "name":"https-s3",
    "hostname":"s3.example.com",
    "port":443,
    "protocol":"HTTPS",
    "allowedRoutes":{"namespaces":{"from":"All"}},
    "tls":{"mode":"Terminate","certificateRefs":[{
      "group":"","kind":"Secret","name":"s3-server-tls","namespace":"rook-ceph"
    }]}
  }}]'
```

### 6.3 TLS Certificate, ReferenceGrant, HTTPRoute, and Timeouts

```yaml
# 05-s3-endpoint.yaml
---
# TLS Certificate via cert-manager
apiVersion: cert-manager.io/v1
kind: Certificate
metadata:
  name: s3-tls
  namespace: rook-ceph
spec:
  secretName: s3-server-tls
  dnsNames:
    - s3.example.com
  issuerRef:
    kind: ClusterIssuer
    name: letsencrypt-prod-http    # Your ClusterIssuer name
---
# Allow gateway to reference secrets/services in rook-ceph namespace
apiVersion: gateway.networking.k8s.io/v1beta1
kind: ReferenceGrant
metadata:
  name: s3-tls-ref
  namespace: rook-ceph
spec:
  from:
    - group: gateway.networking.k8s.io
      kind: Gateway
      namespace: envoy-gateway-system
  to:
    - group: ""
      kind: Secret
    - group: ""
      kind: Service
---
# Route: s3.example.com → rook-ceph-rgw-my-store:80
apiVersion: gateway.networking.k8s.io/v1
kind: HTTPRoute
metadata:
  name: s3-endpoint
  namespace: rook-ceph
spec:
  hostnames:
    - s3.example.com
  parentRefs:
    - name: eg
      namespace: envoy-gateway-system
      sectionName: https-s3
  rules:
    - backendRefs:
        - name: rook-ceph-rgw-my-store
          port: 80
      matches:
        - path:
            type: PathPrefix
            value: /
---
# Disable request timeout for large uploads
apiVersion: gateway.envoyproxy.io/v1alpha1
kind: BackendTrafficPolicy
metadata:
  name: s3-timeouts
  namespace: rook-ceph
spec:
  targetRefs:
    - group: gateway.networking.k8s.io
      kind: HTTPRoute
      name: s3-endpoint
  timeout:
    http:
      requestTimeout: "0s"
      connectionIdleTimeout: 3600s
```

```bash
kubectl apply -f 05-s3-endpoint.yaml

# Wait for TLS cert
kubectl get certificate s3-tls -n rook-ceph -w

# Test
curl -s -o /dev/null -w "%{http_code}" https://s3.example.com/
# Expected: 200
```

---

## Step 7: Configure Kube-DC

### 7.1 Helm Values

Configure the object storage store name and namespace in your Kube-DC Helm values. The defaults match the manifests above:

```yaml
# values.yaml (kube-dc chart)
objectStorageConfig:
  namespace: rook-ceph       # default
  storeName: my-store        # default
```

### 7.2 Backend Environment Variables

The UI backend reads these from environment (all have sensible defaults):

| Variable | Default | Description |
|----------|---------|-------------|
| `ROOK_NAMESPACE` | `rook-ceph` | Namespace where Rook-Ceph is deployed |
| `CEPH_STORE_NAME` | `my-store` | Name of the `CephObjectStore` resource |
| `CEPH_STORAGE_CLASS` | `ceph-bucket` | StorageClass for `ObjectBucketClaim` provisioning |
| `S3_ENDPOINT` | `https://s3.example.com` | External S3 endpoint URL used by the UI and presigned URLs |

Set `S3_ENDPOINT` to your actual S3 domain in the backend deployment.

### 7.3 Billing Plans

Object storage quotas are defined per billing plan in `values.yaml`:

```yaml
plans:
  dev-pool:
    objectStorage: 20      # 20 GB, auto: 5 buckets
  pro-pool:
    objectStorage: 100     # 100 GB, auto: 20 buckets
  scale-pool:
    objectStorage: 500     # 500 GB, auto: 50 buckets
```

The Go controller automatically creates a `CephObjectStoreUser` per organization with these quotas when the org is reconciled.

### 7.4 Backend Service Account RBAC

The backend service account needs read access to rook-ceph resources. The Kube-DC Helm chart already includes these rules in `backend-sa.yaml`:

```yaml
# Object Storage: CephObjectStoreUser quota + S3 keys from rook-ceph
- apiGroups: ["ceph.rook.io"]
  resources: ["cephobjectstoreusers"]
  verbs: ["get", "list"]
- apiGroups: ["objectbucket.io"]
  resources: ["objectbucketclaims"]
  verbs: ["get", "list"]
```

### 7.5 Project Role Permissions

Users need `objectbucketclaims` permissions in their project namespace. The default project roles already include:

```yaml
# In default-project-admin-role (and developer role)
- apiGroups: [objectbucket.io]
  resources: [objectbucketclaims]
  verbs: [create, get, list, watch, delete]
```

---

## Kube-DC Integration Summary

Once deployed, the following features are automatically available:

### Go Controller (automatic)
- Creates `CephObjectStoreUser` per organization with quotas from billing plan
- Updates quotas on plan change, blocks uploads on suspension (`maxSize=0`)
- Deletes user on organization removal
- User capabilities: `user=*`, `bucket=*` (enables key management via RGW Admin API)

### UI Backend API
- **Bucket Management**: Create/delete buckets via `ObjectBucketClaim`, list with usage stats
- **File Browser**: Upload, download (presigned URLs), delete, create folders
- **S3 Access Keys**: View org-level credentials, generate additional keys, revoke keys
- **Quota & Usage**: Real-time storage usage via RGW Admin API, quota limits from `CephObjectStoreUser`
- **Bucket Policies**: Toggle public-read / private access per bucket

### UI Frontend
- **Object Storage sidebar tab** with tree view (Overview, Buckets, Access Keys)
- **Overview**: Quota usage bars, endpoint info, bucket count
- **Buckets**: Table with expandable details, access toggle, file browser
- **File Browser**: Breadcrumb navigation, drag-and-drop upload, download, folder creation
- **Access Keys**: Primary credentials, key management (generate/revoke), CLI examples
- **Billing Integration**: Object storage usage bars on Billing Overview and Project pages

---

## Verification

### Test S3 Access

```python
import boto3
from botocore.config import Config

s3 = boto3.client('s3',
    endpoint_url='https://s3.example.com',
    aws_access_key_id='<ACCESS_KEY>',
    aws_secret_access_key='<SECRET_KEY>',
    region_name='us-east-1',
    config=Config(signature_version='s3v4')
)

s3.create_bucket(Bucket='test-bucket')
s3.put_object(Bucket='test-bucket', Key='hello.txt', Body=b'Hello!')
resp = s3.get_object(Bucket='test-bucket', Key='hello.txt')
print(resp['Body'].read().decode())
```

### Check Ceph Health

```bash
kubectl -n rook-ceph exec deploy/rook-ceph-operator -- \
  ceph -c /var/lib/rook/rook-ceph/rook-ceph.config status
```

### Verify Org User Created

```bash
kubectl get cephobjectstoreuser -n rook-ceph
# Should show one user per organization with objectStorage in their plan
```

---

## Cleanup

```bash
# Delete object store
kubectl delete cephobjectstore my-store -n rook-ceph

# Delete cluster
kubectl delete cephcluster rook-ceph -n rook-ceph

# Wait for cleanup, then uninstall operator
helm uninstall rook-ceph -n rook-ceph

# Clean host data (on worker node)
rm -rf /var/lib/rook/*
```

---

## References

- [Rook Ceph Documentation](https://rook.io/docs/rook/latest/)
- [Rook CephObjectStore](https://rook.io/docs/rook/latest/Storage-Configuration/Object-Storage-RGW/object-storage/)
- [Rook ObjectBucketClaim](https://rook.io/docs/rook/latest/Storage-Configuration/Object-Storage-RGW/ceph-object-bucket-claim/)
- [Ceph RGW Admin API](https://docs.ceph.com/en/latest/radosgw/adminops/)
- [Rook External Cluster](https://rook.io/docs/rook/latest/CRDs/Cluster/external-cluster/) (for remote Ceph)
