# Object Storage (S3)

Kube-DC provides S3-compatible object storage for storing files, images, backups, and any unstructured data. You can manage buckets and files through the dashboard or use standard S3 tools like AWS CLI, s3cmd, and boto3.

## Buckets

A **bucket** is a container for your objects (files). Each bucket has a unique name, its own access credentials, and can be set to **Private** or **Public Read** access.

<img src={require('./images/s3-bucket-view.png').default} alt="Object Storage buckets view" style={{maxWidth: '800px', width: '100%'}} />

The Object Storage view shows all your buckets with their S3 bucket name, status, access level, credentials availability, and age. The sidebar tree provides quick navigation between **Overview**, **Buckets**, and **Access Keys**.

Click on a bucket to expand its details:

- **General Information** — Name, S3 bucket name, namespace, storage class, status, and creation date
- **S3 Connection** — Endpoint URL, region, access toggle (Public/Private), and public URL
- **Bucket Credentials** — Per-bucket Access Key ID and Secret Access Key
- **Actions** — Browse Files or Delete the bucket

### Create a Bucket

#### Via Dashboard

1. Navigate to your project → **Object Storage** → **Buckets**
2. Click **+ Create Bucket**
3. Enter a bucket name (lowercase, alphanumeric, hyphens allowed)
4. Choose access policy: **Private** (default) or **Public Read**
5. Click **Create**

The bucket name in S3 will be prefixed with your project namespace for uniqueness (e.g., bucket `one` becomes `shalb-jumbolot-one`).

#### Via kubectl

Create an `ObjectBucketClaim` in your project namespace:

```yaml
apiVersion: objectbucket.io/v1alpha1
kind: ObjectBucketClaim
metadata:
  name: my-bucket
  namespace: shalb-jumbolot
  labels:
    kube-dc.com/organization: shalb
spec:
  bucketName: shalb-jumbolot-my-bucket
  storageClassName: ceph-bucket
```

When the claim is bound, Rook automatically creates a **Secret** and **ConfigMap** in your namespace with the bucket's S3 credentials:

```bash
# Check bucket status
kubectl get objectbucketclaim -n shalb-jumbolot

# Get per-bucket S3 credentials
kubectl get secret my-bucket -n shalb-jumbolot -o yaml

# Get bucket connection info
kubectl get configmap my-bucket -n shalb-jumbolot -o yaml
```

The ConfigMap contains:
- `BUCKET_HOST` — Internal S3 endpoint
- `BUCKET_NAME` — Full bucket name in S3
- `BUCKET_PORT` — Service port
- `BUCKET_REGION` — Region identifier

### Bucket Access: Private vs Public

- **Private** (default) — Only accessible with valid S3 credentials
- **Public Read** — Anyone with the URL can read objects; writing still requires credentials

Toggle access from the bucket detail view in the dashboard. The public URL follows the pattern:

```
https://s3.kube-dc.cloud/<bucket-name>/<object-key>
```

### Delete a Bucket

```bash
kubectl delete objectbucketclaim my-bucket -n shalb-jumbolot
```

:::warning
Deleting a bucket removes all objects inside it permanently.
:::

## File Browser

The built-in file browser lets you manage objects directly from the dashboard without any external tools.

<img src={require('./images/s3-manage-files.png').default} alt="S3 file browser" style={{maxWidth: '800px', width: '100%'}} />

From the bucket detail view, click **Browse Files** to open the file browser. You can:

- **Upload Files** — Click **Upload Files** or drag-and-drop files into the browser
- **Create Folders** — Click **+ Create Folder** to organize objects into prefixes
- **Download** — Right-click or use the action menu to download files
- **Move** — Move objects to a different folder within the bucket
- **Copy Public URL** — Get a direct link for public buckets
- **Delete** — Remove individual files or folders

The file browser shows each object's name, size, last modified date, and type (File or Folder).

## Access Keys

Kube-DC provides two types of S3 credentials:

### Organization-Level Keys

These keys provide access to **all buckets** across all projects in your organization. Manage them from the **Access Keys** section.

<img src={require('./images/s3-access-keys.png').default} alt="S3 access keys" style={{maxWidth: '600px', width: '100%'}} />

The Access Keys view shows:

- **Credentials** — Your primary Access Key ID, Secret Access Key (click to reveal), S3 endpoint, and region
- **Key Management** — Generate additional keys or revoke existing ones

Use these keys with any S3-compatible tool:

```bash
# S3 Endpoint
https://s3.kube-dc.cloud

# Region
us-east-1
```

### Per-Bucket Keys

Each bucket also has its own credentials, available in the bucket detail view or as Kubernetes Secrets in your project namespace:

```bash
# Get per-bucket credentials
kubectl get secret my-bucket -n shalb-jumbolot -o jsonpath='{.data.AWS_ACCESS_KEY_ID}' | base64 -d
kubectl get secret my-bucket -n shalb-jumbolot -o jsonpath='{.data.AWS_SECRET_ACCESS_KEY}' | base64 -d
```

Per-bucket keys are scoped to that specific bucket only.

## Using S3 Tools

### AWS CLI

```bash
# Configure credentials
aws configure set aws_access_key_id YOUR_ACCESS_KEY
aws configure set aws_secret_access_key YOUR_SECRET_KEY

# List buckets
aws --endpoint-url https://s3.kube-dc.cloud s3 ls

# Upload a file
aws --endpoint-url https://s3.kube-dc.cloud s3 cp myfile.txt s3://shalb-jumbolot-my-bucket/

# Download a file
aws --endpoint-url https://s3.kube-dc.cloud s3 cp s3://shalb-jumbolot-my-bucket/myfile.txt ./

# List objects in a bucket
aws --endpoint-url https://s3.kube-dc.cloud s3 ls s3://shalb-jumbolot-my-bucket/

# Sync a directory
aws --endpoint-url https://s3.kube-dc.cloud s3 sync ./backups s3://shalb-jumbolot-my-bucket/backups/
```

### Python (boto3)

```python
import boto3

s3 = boto3.client(
    's3',
    endpoint_url='https://s3.kube-dc.cloud',
    aws_access_key_id='YOUR_ACCESS_KEY',
    aws_secret_access_key='YOUR_SECRET_KEY',
)

# List buckets
response = s3.list_buckets()
for bucket in response['Buckets']:
    print(bucket['Name'])

# Upload a file
s3.upload_file('myfile.txt', 'shalb-jumbolot-my-bucket', 'myfile.txt')

# Download a file
s3.download_file('shalb-jumbolot-my-bucket', 'myfile.txt', 'downloaded.txt')
```

### s3cmd

```bash
# Configure
s3cmd --configure \
  --host=s3.kube-dc.cloud \
  --host-bucket=s3.kube-dc.cloud \
  --access_key=YOUR_ACCESS_KEY \
  --secret_key=YOUR_SECRET_KEY

# List buckets
s3cmd ls

# Upload
s3cmd put myfile.txt s3://shalb-jumbolot-my-bucket/
```

### Using Credentials from Kubernetes Secrets

For workloads running inside your project, you can mount the per-bucket credentials directly:

```yaml
apiVersion: v1
kind: Pod
metadata:
  name: s3-worker
  namespace: shalb-jumbolot
spec:
  containers:
    - name: worker
      image: amazon/aws-cli
      env:
        - name: AWS_ACCESS_KEY_ID
          valueFrom:
            secretKeyRef:
              name: my-bucket
              key: AWS_ACCESS_KEY_ID
        - name: AWS_SECRET_ACCESS_KEY
          valueFrom:
            secretKeyRef:
              name: my-bucket
              key: AWS_SECRET_ACCESS_KEY
        - name: BUCKET_HOST
          valueFrom:
            configMapKeyRef:
              name: my-bucket
              key: BUCKET_HOST
        - name: BUCKET_NAME
          valueFrom:
            configMapKeyRef:
              name: my-bucket
              key: BUCKET_NAME
```

## Quotas

Object storage quotas are set by your organization's billing plan:

| Plan | Storage Limit | Max Buckets |
|------|--------------|-------------|
| Dev | 20 GB | 5 |
| Pro | 100 GB | 20 |
| Scale | 500 GB | 50 |

Quotas are enforced at the storage level. When the quota is exceeded:
- New uploads are rejected with an error
- Existing objects remain accessible
- The dashboard shows a warning

Storage usage is visible in the **Object Storage Overview** and on the **Billing** page.

## Quick Reference

| Action | Command |
|--------|---------|
| List buckets | `kubectl get objectbucketclaims -n my-project` |
| Create bucket | `kubectl apply -f bucket.yaml` |
| Delete bucket | `kubectl delete objectbucketclaim <name> -n my-project` |
| Get bucket credentials | `kubectl get secret <bucket-name> -n my-project` |
| Get bucket config | `kubectl get configmap <bucket-name> -n my-project` |
| S3 list (AWS CLI) | `aws --endpoint-url https://s3.kube-dc.cloud s3 ls` |
| S3 upload | `aws --endpoint-url https://s3.kube-dc.cloud s3 cp file s3://bucket/` |

## Next Steps

- [Block Storage](block-storage.md) — Persistent volumes for VMs and containers
- [Backups & Snapshots](backups-snapshots.md)
