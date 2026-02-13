# PRD: Rook Ceph Object Storage Service for Kube-DC

## Overview

S3-compatible object storage as a managed service within Kube-DC. Users create buckets, manage access keys, and upload/download objects through the UI — similar to AWS S3 or DigitalOcean Spaces. Backed by Rook Ceph RGW (RADOS Gateway), with support for both local and remote Ceph clusters.

---

## Architecture

```
┌─────────────────────────────────────────────────────────────────────┐
│                          Kube-DC UI                                 │
│  ┌──────────────┐  ┌──────────────┐  ┌──────────────────────────┐  │
│  │ Object       │  │ Bucket       │  │ File Browser             │  │
│  │ Storage Tab  │  │ Management   │  │ (Upload/Download/Delete) │  │
│  └──────┬───────┘  └──────┬───────┘  └──────────┬───────────────┘  │
│         │                 │                      │                  │
├─────────┴─────────────────┴──────────────────────┴──────────────────┤
│                       UI Backend (Express.js)                       │
│  ┌──────────────┐  ┌──────────────┐  ┌──────────────────────────┐  │
│  │ Bucket API   │  │ S3 Keys API  │  │ S3 Proxy (presigned URLs)│  │
│  │ (K8s CRDs)   │  │ (K8s Secret) │  │ (AWS SDK → RGW)         │  │
│  └──────┬───────┘  └──────┬───────┘  └──────────┬───────────────┘  │
│         │                 │                      │                  │
├─────────┴─────────────────┴──────────────────────┴──────────────────┤
│                    Kubernetes API / S3 API                           │
│                                                                     │
│  ┌─────────────────────────┐    ┌────────────────────────────────┐  │
│  │ Go Controller           │    │ Rook-Ceph                      │  │
│  │ (kube-dc manager)       │    │                                │  │
│  │                         │    │  CephObjectStore (my-store)    │  │
│  │ Organization Reconciler │───▶│  CephObjectStoreUser (per-org) │  │
│  │ - CephObjectStoreUser   │    │  ObjectBucketClaim (per-bucket)│  │
│  │ - S3 Quota enforcement  │    │  RGW Gateway (S3 API)         │  │
│  │ - Billing integration   │    │                                │  │
│  └─────────────────────────┘    └────────────────────────────────┘  │
│                                                                     │
│  ┌─────────────────────────────────────────────────────────────┐    │
│  │ Ceph Cluster (local or remote)                              │    │
│  │  MON → MGR → OSD(s) → RGW                                  │    │
│  │  S3 Endpoint: s3.kube-dc.cloud (via Envoy Gateway)         │    │
│  └─────────────────────────────────────────────────────────────┘    │
└─────────────────────────────────────────────────────────────────────┘
```

---

## What Already Exists

### Go Controller (kube-dc manager)
- **`res_s3_quota.go`**: Creates/updates `CephObjectStoreUser` per organization with quotas (`maxSize`, `maxBuckets`, `maxObjects`). Handles suspension (sets `maxSize=0`). Deletes user on org removal.
- **`plan_config.go`**: `ObjectStorage` field per billing plan (dev=20GB, pro=100GB, scale=500GB). `MaxBuckets` auto-calculated. `ObjectStorageConfig` for Ceph namespace/store name.
- **`organization.go`**: Syncs S3 quota during organization reconciliation. Handles suspended/canceled orgs.

### Billing Plans (values.yaml)
```yaml
dev-pool:   objectStorage: 20    # 20 GB, 5 buckets
pro-pool:   objectStorage: 100   # 100 GB, 20 buckets
scale-pool: objectStorage: 500   # 500 GB, 50 buckets
```

### Examples
- `ObjectBucketClaim` examples in `examples/organization/object-storage/`
- S3 public policy JSON example

### Implementation Status (2026-02-13)

✅ **Phase 1 — Rook Ceph Deployment & S3 Endpoint**: Complete
- Rook Operator v1.19.1 deployed via Helm
- CephCluster: 1 MON + 1 MGR + 1 OSD (500GB loop device, single-node)
- CephObjectStore `my-store` with RGW gateway
- StorageClass `ceph-bucket` for OBC provisioning
- S3 endpoint exposed via Envoy Gateway with TLS (Let's Encrypt)
- BackendTrafficPolicy for large uploads (no timeout)
- Deployment doc: [`docs/deploy-rook-ceph-object-storage.md`](../deploy-rook-ceph-object-storage.md)

✅ **Phase 2 — UI Backend API**: Complete
- File: `ui/backend/controllers/objectStorageModule.js`
- Bucket CRUD (create/delete/list via ObjectBucketClaim)
- Bucket details with per-bucket credentials from OBC secrets
- File browser API (list, upload via presigned URL, download, delete, create folder)
- S3 key retrieval (org-level credentials from rook-ceph secret)
- Key management: list all keys, generate new keys, revoke keys (via RGW Admin API)
- Quota/usage API (RGW Admin API `GET /admin/user?stats=true` + `GET /admin/bucket?stats=true`)
- Bucket policies: public-read / private toggle via S3 bucket policy
- CORS auto-configuration for browser uploads

✅ **Phase 3 — UI Frontend**: Complete
- File: `ui/frontend/src/app/ManageWorkloads/Views/ObjectStorage/ObjectStorageView.tsx`
- Object Storage sidebar tab with tree view (Overview, Buckets, Access Keys)
- Overview: quota usage bars, endpoint info, bucket count, storage used
- Buckets: table with expandable details, access toggle (Public/Private), Edit/Browse/Delete actions
- File Browser (`BucketFileBrowser.tsx`): breadcrumb navigation, drag-and-drop upload, download, delete, create folder
- Access Keys: primary credentials with reveal/copy, Key Management card (generate/revoke), CLI examples (AWS CLI, Python boto3, kubectl)
- Sidebar tree: `ObjectStorageTreeViewImpl.tsx` with per-bucket entries
- Billing integration: Object Storage usage bars on Billing Overview, Subscription, and Project pages

✅ **Go Controller Enhancements**:
- `res_s3_quota.go`: CephObjectStoreUser capabilities upgraded to `user=*`, `bucket=*` (enables key management via RGW Admin API)
- Patch logic also updates capabilities on existing users (not just quotas)
- Billing quota controller (`quotaController.js`): shared helpers `getOrgS3Context`, `sumBucketUsage`, `buildStorageResult` (DRY)
- Project-level object storage usage uses org-level quota limits

🔲 **Phase 4 — Remote Ceph Cluster Support**: Not started
- External CephCluster mode
- Multi-store configuration
- Per-org store assignment

🔲 **Not Yet Implemented**:
- Remote Ceph cluster support
- Virtual-hosted bucket access (`<bucket>.s3.example.com`)
- Custom bucket policies (JSON editor — currently only public-read/private toggle)
- Sub-user creation with restricted permissions (read-only sub-users)

---

## Phased Implementation

### Phase 1: Rook Ceph Deployment & S3 Endpoint

**Goal**: Working S3 endpoint accessible internally and externally.

#### 1.1 Deploy Rook Ceph Operator

Add to installer template or Helm chart:

```yaml
# Rook Operator (namespace: rook-ceph)
apiVersion: helm.toolkit.fluxcd.io/v2beta1
kind: HelmRelease
metadata:
  name: rook-ceph
  namespace: rook-ceph
spec:
  chart:
    spec:
      chart: rook-ceph
      version: v1.16.x
      sourceRef:
        kind: HelmRepository
        name: rook-release
  values:
    crds:
      enabled: true
    resources:
      requests:
        cpu: 200m
        memory: 256Mi
      limits:
        memory: 512Mi
```

#### 1.2 Deploy CephCluster (Single-Node, Worker)

As defined in existing `kube-dc-cloud-storage-design.md` Phase 2:
- Single MON, MGR, OSD on worker node (20T HDD)
- `storageClassDeviceSets` using `local-path`
- No replication (`size: 1`) — acceptable for initial deployment

#### 1.3 Deploy CephObjectStore

```yaml
apiVersion: ceph.rook.io/v1
kind: CephObjectStore
metadata:
  name: my-store
  namespace: rook-ceph
spec:
  metadataPool:
    replicated:
      size: 1
  dataPool:
    replicated:
      size: 1
  preservePoolsOnDelete: true
  gateway:
    port: 80
    securePort: 443
    instances: 1
    resources:
      requests:
        cpu: 100m
        memory: 256Mi
      limits:
        memory: 1Gi
```

#### 1.4 Deploy StorageClass for OBC

```yaml
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

#### 1.5 Expose S3 Endpoint via Envoy Gateway

```yaml
# Internal Service already created by Rook: rook-ceph-rgw-my-store.rook-ceph
# Expose externally via HTTPRoute

apiVersion: gateway.networking.k8s.io/v1
kind: HTTPRoute
metadata:
  name: s3-endpoint
  namespace: rook-ceph
spec:
  hostnames:
    - s3.kube-dc.cloud
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
# BackendTrafficPolicy for large uploads
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
      requestTimeout: "0s"          # No timeout for large uploads
      connectionIdleTimeout: 3600s
      maxConnectionDuration: 7200s
```

#### 1.6 DNS

- `s3.kube-dc.cloud` → Envoy Gateway LB IP
- Wildcard `*.s3.kube-dc.cloud` for virtual-hosted bucket access (optional, path-style first)

**Phase 1 Deliverables**:
- [x] Rook Ceph operator running
- [x] CephCluster healthy (1 MON, 1 MGR, 1 OSD)
- [x] CephObjectStore `my-store` with RGW gateway
- [x] StorageClass `ceph-bucket` for OBC
- [x] S3 endpoint exposed via Envoy Gateway with TLS
- [x] BackendTrafficPolicy for large file uploads
- [x] Existing org reconciler creates `CephObjectStoreUser` automatically

---

### Phase 2: UI Backend API

**Goal**: REST API for bucket and S3 key management.

#### 2.1 Backend Routes

New file: `ui/backend/controllers/objectStorageController.js`

```
GET    /api/object-storage/:namespace/buckets          # List buckets for project
POST   /api/object-storage/:namespace/buckets          # Create bucket (ObjectBucketClaim)
DELETE /api/object-storage/:namespace/buckets/:name     # Delete bucket
GET    /api/object-storage/:namespace/buckets/:name     # Get bucket details + usage

GET    /api/object-storage/:namespace/keys              # Get S3 access keys
POST   /api/object-storage/:namespace/keys/regenerate   # Regenerate S3 keys

GET    /api/object-storage/:namespace/quota             # Get org S3 quota + usage
GET    /api/object-storage/:namespace/endpoint           # Get S3 endpoint URL

# File browser (proxied via presigned URLs)
GET    /api/object-storage/:namespace/buckets/:name/objects?prefix=&delimiter=  # List objects
POST   /api/object-storage/:namespace/buckets/:name/upload-url                  # Get presigned upload URL
GET    /api/object-storage/:namespace/buckets/:name/download-url/:key           # Get presigned download URL
DELETE /api/object-storage/:namespace/buckets/:name/objects/:key                # Delete object
```

#### 2.2 How Bucket Creation Works

When a user creates a bucket through the UI:

1. **Frontend** → `POST /api/object-storage/:namespace/buckets` with `{ name: "my-bucket" }`
2. **Backend** creates an `ObjectBucketClaim` in the user's project namespace:
   ```yaml
   apiVersion: objectbucket.io/v1alpha1
   kind: ObjectBucketClaim
   metadata:
     name: my-bucket
     namespace: shalb-demo        # User's project namespace
     labels:
       kube-dc.com/organization: shalb
   spec:
     bucketName: shalb-demo-my-bucket  # Prefixed with namespace for uniqueness
     storageClassName: ceph-bucket
   ```
3. **Rook OBC controller** provisions the bucket in `my-store` using the org's `CephObjectStoreUser`
4. **Rook** creates a Secret + ConfigMap in the namespace with S3 credentials and endpoint

#### 2.3 S3 Credential Model

Users have **no access** to `rook-ceph` namespace. Two credential paths exist:

##### Per-Bucket Credentials (user-accessible, in project namespace)

When an `ObjectBucketClaim` is created in a project namespace, Rook auto-creates:

```
Secret/<bucket-name>           → AWS_ACCESS_KEY_ID, AWS_SECRET_ACCESS_KEY
ConfigMap/<bucket-name>        → BUCKET_HOST, BUCKET_PORT, BUCKET_NAME, BUCKET_REGION
```

These live in the user's project namespace (e.g., `shalb-demo`) and are directly accessible.
Users can `kubectl get secret my-bucket -o yaml` in their own namespace.

**This is the primary credential path for end users.**

##### Org-Level Credentials (admin-only, proxied via backend API)

The `CephObjectStoreUser` (one per org, created by Go controller) owns the master S3 credentials:

```
Secret: rook-ceph-object-user-my-store-<orgname>   (in rook-ceph namespace)
  AccessKey: XXXX
  SecretKey: XXXX
  Endpoint: http://rook-ceph-rgw-my-store.rook-ceph:80
```

Users **cannot** access this directly. The UI backend reads it via service account and exposes it through the API:

```
GET /api/object-storage/:namespace/keys
```

The backend resolves `namespace → organization` (via namespace labels), then reads the org-level Secret from `rook-ceph`. This gives the user full account access (all buckets under their org's quota).

##### Credential Flow Summary

```
User creates bucket via UI/CLI
  → ObjectBucketClaim in shalb-demo
  → Rook creates Secret + ConfigMap in shalb-demo  ← user can read this
  → User gets per-bucket S3 keys from their own namespace

User requests "Account Keys" via UI
  → Backend reads Secret from rook-ceph (service account)
  → Returns org-level keys to user                  ← proxied, not direct access
```

#### 2.4 File Browser (Presigned URL Proxy)

For the file browser, the backend uses the org's S3 keys to generate presigned URLs:

```javascript
const { S3Client, ListObjectsV2Command, PutObjectCommand, GetObjectCommand, DeleteObjectCommand } = require('@aws-sdk/client-s3');
const { getSignedUrl } = require('@aws-sdk/s3-request-presigner');

// Create S3 client using org's credentials from K8s Secret
const s3Client = new S3Client({
  endpoint: 'https://s3.kube-dc.cloud',
  region: 'us-east-1',  // Ceph default
  credentials: { accessKeyId, secretAccessKey },
  forcePathStyle: true,  // Required for Ceph RGW
});

// List objects
const listObjects = async (bucket, prefix) => {
  const cmd = new ListObjectsV2Command({
    Bucket: bucket,
    Prefix: prefix,
    Delimiter: '/',
  });
  return s3Client.send(cmd);
};

// Generate presigned upload URL (frontend uploads directly to S3)
const getUploadUrl = async (bucket, key) => {
  const cmd = new PutObjectCommand({ Bucket: bucket, Key: key });
  return getSignedUrl(s3Client, cmd, { expiresIn: 3600 });
};
```

**Phase 2 Deliverables**:
- [x] Bucket CRUD API (`ObjectBucketClaim` management)
- [x] S3 key retrieval API
- [x] Key management API (list all keys, generate, revoke via RGW Admin API)
- [x] Quota/usage API (RGW Admin API stats)
- [x] File browser API (list, presigned upload/download, delete, create folder)
- [x] Bucket policy API (public-read / private toggle)
- [x] RBAC: users can only manage buckets in their project namespaces

---

### Phase 3: UI Frontend

**Goal**: Object Storage tab in the sidebar with bucket management and file browser.

#### 3.1 Sidebar Integration

Add "Object Storage" tab to `Sidebar.tsx`, alongside existing tabs (VM, System, Volumes, Networking):

```
Sidebar Tabs:
  [VM] [Nodes] [System] [Volumes] [Networking] [Object Storage]
```

Tree view under Object Storage tab:
```
📦 Object Storage
├── 📊 Overview (quota usage, endpoint info)
├── 🪣 Buckets
│   ├── my-bucket (24 objects, 1.2 GB)
│   ├── backups (8 objects, 5.4 GB)
│   └── + Create Bucket
└── 🔑 Access Keys
```

#### 3.2 Views

**Overview View** (`ObjectStorageOverview.tsx`):
- S3 endpoint URL (copyable): `s3.kube-dc.cloud`
- Storage quota bar: `12.5 GB / 100 GB used`
- Bucket count: `3 / 20 buckets`
- Quick start guide with `aws s3` CLI examples

**Bucket List View** (`BucketListView.tsx`):
- Table: Name, Objects, Size, Created, Actions (Delete)
- "Create Bucket" button → modal

**Bucket Detail / File Browser** (`BucketFileBrowser.tsx`):
- Breadcrumb navigation: `my-bucket / images / 2024 /`
- File/folder list: Name, Size, Last Modified, Actions
- Upload button (drag-and-drop zone)
- Download button (presigned URL)
- Delete button (with confirmation)
- Create folder
- Pagination for large directories

**Access Keys View** (`AccessKeysView.tsx`):
- Show Access Key ID (visible)
- Show Secret Key (hidden, reveal on click)
- Copy buttons
- Regenerate keys button (with confirmation)
- Connection examples:
  ```bash
  # AWS CLI
  aws configure set aws_access_key_id XXXXX
  aws configure set aws_secret_access_key XXXXX
  aws --endpoint-url https://s3.kube-dc.cloud s3 ls

  # s3cmd
  s3cmd --configure --host=s3.kube-dc.cloud --host-bucket=s3.kube-dc.cloud

  # Python boto3
  import boto3
  s3 = boto3.client('s3', endpoint_url='https://s3.kube-dc.cloud',
                     aws_access_key_id='XXXXX', aws_secret_access_key='XXXXX')
  ```

**Create Bucket Modal** (`CreateBucketModal.tsx`):
- Bucket name input (validated: lowercase, alphanumeric, hyphens)
- Access policy: Private (default) / Public Read
- Create button

#### 3.3 Route Structure

```
/project/:projectName/object-storage                → Overview
/project/:projectName/object-storage/buckets         → Bucket list
/project/:projectName/object-storage/bucket/:name    → File browser
/project/:projectName/object-storage/keys            → Access keys
```

**Phase 3 Deliverables**:
- [x] Object Storage sidebar tab (`ObjectStorageTreeViewImpl.tsx`)
- [x] Overview view with quota usage (RGW Admin API stats, quota bars)
- [x] Bucket list view with create/delete, expandable details, access toggle
- [x] File browser with upload/download/delete, folder creation, drag-and-drop
- [x] Access keys view with CLI examples + Key Management (generate/revoke)
- [x] Route integration in `MainContainer.tsx`
- [x] Billing integration: Object Storage usage bars on Billing & Project pages

---

### Phase 4: Remote Ceph Cluster Support

**Goal**: Connect to external Ceph clusters (customer-owned or multi-site).

#### 4.1 CephCluster External Mode

Rook supports connecting to an external Ceph cluster without deploying MON/OSD/MGR locally:

```yaml
apiVersion: ceph.rook.io/v1
kind: CephCluster
metadata:
  name: external-ceph
  namespace: rook-ceph-external
spec:
  external:
    enable: true
  # No storage, mon, mgr specs needed
```

Requirements from the remote cluster:
- Ceph monitor endpoints
- Admin keyring (for user/bucket management)
- RGW endpoint (for S3 access)

#### 4.2 Import Script

Rook provides `create-external-cluster-resources.py` which exports the needed credentials from a source Ceph cluster:

```bash
# On the remote Ceph cluster
python3 create-external-cluster-resources.py \
  --rbd-data-pool-name <pool> \
  --rgw-endpoint <rgw-host>:<port> \
  --namespace rook-ceph-external \
  --format bash
```

This outputs Secrets and ConfigMaps that need to be applied to the management cluster.

#### 4.3 Multi-Store Configuration

Support multiple `CephObjectStore` instances pointing to different clusters:

```yaml
# values.yaml
objectStorageConfig:
  stores:
    - name: local-store
      namespace: rook-ceph
      endpoint: s3.kube-dc.cloud
      default: true
    - name: remote-store
      namespace: rook-ceph-external
      endpoint: s3-remote.kube-dc.cloud
      region: eu-west-1
```

#### 4.4 Per-Organization Store Assignment

Extend `PlanDefinitionYAML` to optionally specify which object store to use:

```yaml
plans:
  scale-pool:
    objectStorage: 500
    objectStoreName: remote-store   # Optional: specific store for this plan
```

#### 4.5 UI: Store Selection

If multiple stores are configured, show store selector in:
- Bucket creation modal (which store to create bucket in)
- Overview (per-store usage breakdown)
- Access keys (per-store credentials)

**Phase 4 Deliverables**:
- [ ] External CephCluster support
- [ ] Import script/workflow for remote cluster credentials
- [ ] Multi-store configuration in billing plans
- [ ] Per-org store assignment
- [ ] UI store selector (when multiple stores exist)

---

## Data Model

### Kubernetes Resources (per organization)

| Resource | Namespace | Created By | Purpose |
|----------|-----------|------------|---------|
| `CephObjectStoreUser/<org>` | `rook-ceph` | Go controller (auto) | Org-level S3 user with quotas |
| `Secret/rook-ceph-object-user-my-store-<org>` | `rook-ceph` | Rook (auto) | S3 access key + secret key |

### Kubernetes Resources (per bucket)

| Resource | Namespace | Created By | Purpose |
|----------|-----------|------------|---------|
| `ObjectBucketClaim/<bucket>` | `<org>-<project>` | UI Backend | Bucket provisioning request |
| `ObjectBucket/<bucket>` | cluster-scoped | Rook (auto) | Actual bucket reference |
| `Secret/bucket-<bucket>` | `<org>-<project>` | Rook (auto) | Bucket-specific S3 credentials |
| `ConfigMap/bucket-<bucket>` | `<org>-<project>` | Rook (auto) | Bucket endpoint + name |

### Quota Enforcement

```
Organization (billing plan)
  └─ CephObjectStoreUser (maxSize: 100G, maxBuckets: 20, maxObjects: 100000)
      └─ Bucket 1 (ObjectBucketClaim in project-ns)
      └─ Bucket 2 (ObjectBucketClaim in project-ns)
      └─ ...
```

Quotas enforced at Ceph RGW level — no bypass possible even via direct S3 API.

---

## RBAC

### User Permissions

Users manage buckets via `ObjectBucketClaim` in their project namespace. Existing Kube-DC RBAC already scopes users to their project namespaces.

Required ClusterRole addition for project members:
```yaml
apiVersion: rbac.authorization.k8s.io/v1
kind: ClusterRole
metadata:
  name: kube-dc-object-storage
rules:
  - apiGroups: ["objectbucket.io"]
    resources: ["objectbucketclaims"]
    verbs: ["get", "list", "create", "delete"]
```

### Backend Service Account

The UI backend needs read access to the `CephObjectStoreUser` Secret in `rook-ceph` namespace for S3 key retrieval:

```yaml
apiVersion: rbac.authorization.k8s.io/v1
kind: ClusterRole
metadata:
  name: kube-dc-s3-keys-reader
rules:
  - apiGroups: [""]
    resources: ["secrets"]
    verbs: ["get"]
    resourceNames: []  # Filtered by org name in code
  - apiGroups: ["ceph.rook.io"]
    resources: ["cephobjectstoreusers"]
    verbs: ["get", "list"]
```

---

## S3 Endpoint Configuration

### Path-Style (Phase 1)
```
https://s3.kube-dc.cloud/<bucket-name>/<key>
```

### Virtual-Hosted (Phase 4, optional)
```
https://<bucket-name>.s3.kube-dc.cloud/<key>
```

Requires wildcard DNS + TLS certificate.

---

## Billing Integration

### Current Plan Limits

| Plan | Object Storage | Max Buckets | Price |
|------|---------------|-------------|-------|
| Dev Pool | 20 GB | 5 | €19/mo |
| Pro Pool | 100 GB | 20 | €49/mo |
| Scale Pool | 500 GB | 50 | €99/mo |

### Usage Tracking

The UI backend queries Ceph RGW admin API (or `radosgw-admin` equivalent) for per-user usage:

```
GET /admin/user?uid=<org-name>&stats=true
```

Returns:
```json
{
  "stats": {
    "size": 12884901888,
    "size_actual": 12884901888,
    "num_objects": 1542
  }
}
```

This is exposed in the UI Overview as quota usage bars.

### Overage / Enforcement

Ceph RGW enforces quotas natively. When quota is exceeded:
- New uploads return `HTTP 403 QuotaExceeded`
- Existing objects remain accessible
- UI shows warning banner

---

## Security Considerations

- **S3 keys**: Stored in Kubernetes Secrets, accessed only via authenticated backend API
- **Bucket isolation**: Each bucket is prefixed with `<namespace>-` to prevent name collisions
- **Quota enforcement**: At Ceph RGW level (server-side, cannot be bypassed)
- **TLS**: S3 endpoint exposed via HTTPS through Envoy Gateway
- **RBAC**: Users can only create OBCs in namespaces they have access to
- **Public buckets**: Optional, requires explicit bucket policy (S3 policy JSON)

---

## Resource Requirements

### Phase 1 (Single-Node Ceph)

| Component | CPU | Memory |
|-----------|-----|--------|
| Rook Operator | 200m | 256Mi |
| MON | 100m | 384Mi |
| MGR | 500m | 2Gi |
| OSD (1x) | 300m | 1Gi |
| RGW | 100m | 256Mi |
| **Total** | **1.2 CPU** | **~4 Gi** |

### Phase 4 (HA + Remote)

| Component | CPU | Memory |
|-----------|-----|--------|
| Rook Operator | 200m | 256Mi |
| MON (3x) | 300m | 1.2Gi |
| MGR (2x) | 1000m | 4Gi |
| OSD (4x) | 1200m | 4Gi |
| RGW (2x) | 200m | 512Mi |
| **Total** | **~3 CPU** | **~10 Gi** |

---

## Testing Strategy

### Unit Tests
- Go controller: `CephObjectStoreUser` creation/update/deletion
- Backend API: bucket CRUD, key management mocking

### Integration Tests
- Create org → verify `CephObjectStoreUser` created with correct quotas
- Create bucket via API → verify `ObjectBucketClaim` + Secret + ConfigMap
- Upload/download file via presigned URL
- Quota enforcement: upload beyond limit → verify rejection
- Delete bucket → verify cleanup

### E2E Tests
- Full flow: create org → subscribe plan → create bucket → upload file → browse → delete
- Suspended org: verify uploads blocked (`maxSize=0`)
- Plan upgrade: verify quota increase reflected

---

## Implementation Priority

```
Phase 1 (Week 1-2):  Rook Ceph deployment + S3 endpoint          ✅ DONE
Phase 2 (Week 2-3):  Backend API for bucket/key management        ✅ DONE
Phase 3 (Week 3-5):  UI frontend (Object Storage tab)             ✅ DONE
Phase 4 (Week 6+):   Remote Ceph cluster support                  🔲 NOT STARTED
```

### Remaining Work
1. Remote Ceph cluster support (Phase 4)
2. Custom bucket policies (JSON editor for fine-grained access control)
3. Sub-user creation with restricted permissions (read-only keys)
4. Virtual-hosted bucket access (`<bucket>.s3.example.com`)

---

## Files to Create / Modify

### Files Created
| File | Purpose |
|------|---------|
| `ui/backend/controllers/objectStorageModule.js` | Backend API: buckets, keys, files, quota, policies |
| `ui/frontend/src/app/ManageWorkloads/Views/ObjectStorage/ObjectStorageView.tsx` | All views: Overview, Buckets, Access Keys (single file) |
| `ui/frontend/src/app/ManageWorkloads/Views/ObjectStorage/BucketFileBrowser.tsx` | File browser with upload/download/delete/folders |
| `ui/frontend/src/app/ManageWorkloads/Sidebar/ObjectStorageTreeViewImpl.tsx` | Sidebar tree with per-bucket entries |
| `docs/deploy-rook-ceph-object-storage.md` | Deployment guide (generic domains) |

### Files Modified
| File | Change |
|------|--------|
| `ui/frontend/src/app/ManageWorkloads/MainContainer.tsx` | Added OBJECT_STORAGE view index + routes |
| `ui/frontend/src/app/ManageWorkloads/Sidebar/Sidebar.tsx` | Added Object Storage tab |
| `ui/backend/server.js` | Registered objectStorage routes |
| `ui/backend/controllers/billing/quotaController.js` | Added object storage usage to billing (DRY helpers) |
| `ui/frontend/src/app/ManageOrganization/Billing/Billing.tsx` | Added Object Storage usage bars |
| `ui/frontend/src/app/ManageOrganization/Billing/types.ts` | Added `ObjectStorageUsage` interface |
| `ui/frontend/src/app/ManageWorkloads/Views/Main/MainView.tsx` | Added Object Storage bar to project quotas, removed Pods bar |
| `internal/organization/res_s3_quota.go` | Upgraded capabilities `user=*`, patch includes capabilities |
| `charts/kube-dc/templates/backend-sa.yaml` | Added RBAC for CephObjectStoreUser + OBC read |
| `charts/kube-dc/templates/default-project-admin-role.yaml` | Added OBC permissions to all project roles |

---

## References

- [Rook Ceph Documentation](https://rook.io/docs/rook/latest/)
- [Rook CephObjectStore](https://rook.io/docs/rook/latest/Storage-Configuration/Object-Storage-RGW/object-storage/)
- [Rook ObjectBucketClaim](https://rook.io/docs/rook/latest/Storage-Configuration/Object-Storage-RGW/ceph-object-bucket-claim/)
- [Rook External Cluster](https://rook.io/docs/rook/latest/CRDs/Cluster/external-cluster/)
- [Ceph RGW Admin API](https://docs.ceph.com/en/latest/radosgw/adminops/)
- [AWS SDK v3 for JS](https://docs.aws.amazon.com/AWSJavaScriptSDK/v3/latest/)
- [Existing design doc](../prd/kube-dc-cloud-storage-design.md)
