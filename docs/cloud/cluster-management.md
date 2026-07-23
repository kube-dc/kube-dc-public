# Cluster Management

Once your managed Kubernetes cluster is running, you can deploy workloads, expose services with external IPs or HTTPS routes, use persistent storage, scale worker pools, and manage the cluster lifecycle.

## Getting the Kubeconfig

The admin kubeconfig lives in a Secret in your project namespace on the
management cluster. It contains several variants that differ only in the API
server endpoint:

| Key | Endpoint | Use from |
|-----|----------|----------|
| `admin.svc` / `super-admin.svc` | in-cluster Service | inside the management cluster |
| `admin.conf` / `super-admin.conf` | the cluster's EIP | inside the project VPC / cloud network |

```bash
kubectl get secret dev-cp-admin-kubeconfig -n my-project \
  -o jsonpath='{.data.admin\.conf}' | base64 -d > /tmp/dev-kubeconfig
```

For access **from the internet** (clusters created with the public API route
enabled), point the kubeconfig at the cluster's public endpoint from
`status.endpoint`:

```bash
ENDPOINT=$(kubectl get kdccluster dev -n my-project -o jsonpath='{.status.endpoint}')
sed -i "s|server: .*|server: $ENDPOINT|" /tmp/dev-kubeconfig
kubectl --kubeconfig=/tmp/dev-kubeconfig get nodes
```

The dashboard's kubeconfig download does this for you.

## Exposing Services (LoadBalancer)

When you create a `Service` of type `LoadBalancer` inside your tenant cluster, the Cloud Controller Manager (CCM) provisions a real LoadBalancer service in the management cluster, giving your application an external IP.

### How It Works

```
┌──── Tenant Cluster ─────┐        ┌──── Management Cluster ────────────┐
│                         │        │  Project Namespace                 │
│  Service (LoadBalancer) │───────▶│  Service (LoadBalancer)            │
│  langfuse-web-lb:3000   │  CCM   │  → External IP: 100.65.0.169       │
│                         │        │                                    │
└─────────────────────────┘        └────────────────────────────────────┘
```

1. You create a `Service` of type `LoadBalancer` in the tenant cluster
2. The CCM running in your project namespace detects the service
3. A corresponding LoadBalancer service is created in the management cluster
4. An external IP is allocated and reported back to the tenant cluster service

All `service.nlb.kube-dc.com/*` and `network.kube-dc.com/*` annotations on the
tenant service are copied to the management-cluster service, so every exposure
method from the [Service Exposure Guide](service-exposure.md) — Gateway routes
with automatic TLS, dedicated EIPs, public IPs — also works from inside a
tenant cluster.

!!! warning "Annotations are copied at creation time only"
    The CCM copies annotations when it first creates the management-cluster
    service. Adding or changing an annotation on an existing tenant service has
    no effect — delete and recreate the service with the annotations in place.

### Example: Expose a Web Application

Inside your tenant cluster, create a LoadBalancer service:

```yaml
apiVersion: v1
kind: Service
metadata:
  name: my-app-lb
  namespace: default
  annotations:
    network.kube-dc.com/external-network-type: "public"
spec:
  type: LoadBalancer
  ports:
    - name: http
      port: 80
      targetPort: 8080
      protocol: TCP
  selector:
    app: my-app
```

```bash
kubectl --kubeconfig=/tmp/dev-kubeconfig apply -f service.yaml
```

### Service Annotations

| Annotation | Value | Description |
|------------|-------|-------------|
| `network.kube-dc.com/external-network-type` | `public` | Allocate a public IP for the service |
| `network.kube-dc.com/external-network-type` | `cloud` | Use a cloud-internal IP (default) |

### Verify the Service

```bash
# Check the service in the tenant cluster
kubectl --kubeconfig=/tmp/dev-kubeconfig get svc my-app-lb

# Example output:
# NAME        TYPE           CLUSTER-IP     EXTERNAL-IP    PORT(S)        AGE
# my-app-lb   LoadBalancer   10.96.201.161  100.65.0.169   80:30092/TCP   5m
```

The `EXTERNAL-IP` is a real IP routable from outside the cluster. For public IPs, the service is accessible from the internet.

### Working Example

A Langfuse deployment exposed via LoadBalancer in a real tenant cluster:

```bash
$ kubectl --kubeconfig=/tmp/dev-kubeconfig get svc langfuse-web-lb -n langfuse
NAME             TYPE           CLUSTER-IP      EXTERNAL-IP    PORT(S)          AGE
langfuse-web-lb  LoadBalancer   10.96.201.161   100.65.0.169   3000:30092/TCP   15d
```

The service is annotated with `network.kube-dc.com/external-network-type: public` and receives a dedicated public IP (`203.0.113.45`) accessible on port 3000. Without the annotation the service gets a cloud IP from the shared `100.65.0.0/16` range instead, reachable from other projects in the cloud but not from the internet.

!!! note "Public IPs count against your organization quota"
    Public EIPs are limited per organization by your plan. If the quota is
    exhausted, the tenant service stays at `EXTERNAL-IP: <pending>` forever —
    the quota error is only visible on the management cluster (`kubectl
    describe eip -n <project>` or the dashboard), not inside the tenant
    cluster. Check your organization's public IPv4 usage before exposing
    services with `external-network-type: public`. Cloud IPs (the default)
    are not quota-limited.

## Exposing Services (HTTPS Gateway Route)

For web applications, the simplest exposure method is a **Gateway route**: one
annotation gives you HTTPS with an automatically provisioned Let's Encrypt
certificate, served by the management cluster's Envoy Gateway. This works from
inside tenant clusters because the CCM propagates the annotations.

### Step 1: Create the ACME Issuer (once per project)

The certificate Issuer lives in your **project namespace on the management
cluster** (use your Kube-DC project kubeconfig, not the tenant cluster one):

```yaml
apiVersion: cert-manager.io/v1
kind: Issuer
metadata:
  name: letsencrypt
  namespace: my-project
spec:
  acme:
    server: https://acme-v02.api.letsencrypt.org/directory
    email: your-email@example.com  # Replace with a valid email
    privateKeySecretRef:
      name: letsencrypt-account-key
    solvers:
    - http01:
        gatewayHTTPRoute:
          parentRefs:
          - group: gateway.networking.k8s.io
            kind: Gateway
            name: eg
            namespace: envoy-gateway-system
```

Without the Issuer, HTTPS routes stay pending: the Certificate is created but
never issued.

### Step 2: Annotate a LoadBalancer service in the tenant cluster

```yaml
apiVersion: v1
kind: Service
metadata:
  name: my-app
  namespace: default
  annotations:
    service.nlb.kube-dc.com/expose-route: "https"
    service.nlb.kube-dc.com/route-hostname: "my-app-my-project.kube-dc.cloud"
spec:
  type: LoadBalancer
  selector:
    app: my-app
  ports:
  - port: 80
    targetPort: 80
```

!!! tip "Always pin `route-hostname`"
    Without `route-hostname`, the hostname is auto-generated from the
    management-side service name (a UID-derived hash like
    `af3d76ff…-my-project.kube-dc.cloud`). It is hard to read **and it
    changes** if the tenant Service is ever deleted and recreated. Set an
    explicit hostname under the cluster's wildcard domain (or your own domain
    with DNS pointed at the Gateway).

### Step 3: Verify

```bash
# Certificate + route are created in the project namespace (management cluster)
kubectl get certificate,httproute -n my-project

# After ~1-2 minutes:
curl https://my-app-my-project.kube-dc.cloud
```

Issuance typically completes in one to two minutes. `http` and
`tls-passthrough` route types work the same way — see the
[Service Exposure Guide](service-exposure.md) for all annotations.

## Persistent Storage (KubeVirt CSI)

When KubeVirt CSI is enabled during cluster creation, you can use PersistentVolumeClaims inside your tenant cluster. The CSI driver creates DataVolumes in your project namespace on the management cluster, providing real persistent storage.

### How It Works

```
┌──── Tenant Cluster ─────┐        ┌──── Management Cluster ────────────┐
│                         │        │  Project Namespace                 │
│  PVC: my-data (5Gi)     │───────▶│  DataVolume → PVC (5Gi)            │
│  StorageClass: kubevirt │  CSI   │  StorageClass: local-path          │
│                         │        │  (hotplugged to worker VM)         │
└─────────────────────────┘        └────────────────────────────────────┘
```

1. You create a PVC in the tenant cluster using the `kubevirt` StorageClass
2. The CSI controller (running in your project namespace) creates a DataVolume in the management cluster
3. The DataVolume is hotplugged to the worker VM where the pod is scheduled
4. The volume is mounted into the pod as a regular block device

### Example: Create a PersistentVolumeClaim

```yaml
apiVersion: v1
kind: PersistentVolumeClaim
metadata:
  name: postgres-data
  namespace: default
spec:
  accessModes:
    - ReadWriteOnce
  resources:
    requests:
      storage: 10Gi
  storageClassName: kubevirt
```

The `kubevirt` StorageClass is automatically created when KubeVirt CSI is enabled. It is set as the default StorageClass, so you can omit `storageClassName` if you prefer.

### Verify Storage

```bash
# Check PVCs in the tenant cluster
kubectl --kubeconfig=/tmp/dev-kubeconfig get pvc -n langfuse

# Example output:
# NAME                    STATUS   VOLUME       CAPACITY   ACCESS MODES   STORAGECLASS   AGE
# clickhouse-pvc          Bound    pvc-c09...   10Gi       RWO            kubevirt       15d
# langfuse-postgres-pvc   Bound    pvc-377...   5Gi        RWO            kubevirt       15d
```

Each PVC in the tenant cluster corresponds to a DataVolume and PVC in your project namespace on the management cluster:

```bash
# Corresponding PVCs on the management cluster (project namespace)
kubectl get pvc -n my-project | grep pvc-c09
pvc-c09c6404-63ac-4ebc-9aab-671b4583b599   Bound   pvc-2d6bb...   11362347344   RWO   local-path   15d
```

### StorageClass Parameters

The default `kubevirt` StorageClass uses the following configuration:

| Parameter | Value | Description |
|-----------|-------|-------------|
| `provisioner` | `csi.kubevirt.io` | KubeVirt CSI driver |
| `bus` | `scsi` | Disk bus type for hotplug |
| `infraStorageClassName` | `local-path` | Storage class used on the management cluster |

### Access Modes

| Tenant PVC request | Supported | Notes |
|--------------------|-----------|-------|
| `ReadWriteOnce` (Filesystem) | ✅ Yes | The standard case. Volumes survive pod restarts and can be detached from one worker VM and hotplugged to another. With the default node-local infra storage, reattaching to a **different** worker only works when both worker VMs run on the same hypervisor host (see the [troubleshooting note](#pod-stuck-after-rescheduling-to-another-worker) below); a replicated infra class like Ceph RBD removes this restriction. |
| `ReadWriteOnce` (Block) | ✅ Yes | Raw block device inside the pod. |
| `ReadWriteMany` (Filesystem) | ❌ No | Rejected by the CSI driver (`non-block volume with RWX access mode is not supported`). Hotplugged disks cannot be safely filesystem-mounted on two VMs at once. For shared filesystems, run an in-cluster NFS/SeaweedFS on top of RWO volumes, or use S3 object storage. |
| `ReadWriteMany` (Block) | ⚠️ Advanced | Supported by the driver when the infra storage class supports RWX Block (Ceph RBD). Only for cluster-aware software that coordinates raw block access. Availability depends on the platform — ask your operator. |

### Additional Storage Classes

You can create extra tenant StorageClasses that map to any storage class
available in your project on the management cluster (for example replicated
Ceph RBD instead of node-local storage):

```yaml
apiVersion: storage.k8s.io/v1
kind: StorageClass
metadata:
  name: kubevirt-rbd
provisioner: csi.kubevirt.io
parameters:
  bus: scsi
  infraStorageClassName: rbd-vm   # storage class on the management cluster
reclaimPolicy: Delete
allowVolumeExpansion: true
```

Check which infra storage classes your project offers with your Kube-DC
project kubeconfig: `kubectl get storageclass`.

## Scaling Workers

### Scale via kubectl

Use a **JSON patch targeting only the replica count**:

```bash
# Scale the first worker pool to 5 replicas
kubectl patch kdccluster dev -n my-project --type=json \
  -p '[{"op":"replace","path":"/spec/workers/0/replicas","value":5}]'
```

!!! danger "Never scale with a merge patch on `spec.workers`"
    `spec.workers` is a **list**. A merge patch like
    `--type merge -p '{"spec":{"workers":[{"name":"workers","replicas":5}]}}'`
    **replaces the whole list**, silently wiping `cpuCores`, `memory`,
    `diskSize` and `image` from your pool spec — new workers would then be
    created with default sizing instead of yours. Always use a JSON patch
    (`--type=json`) for single-field changes, or apply a full manifest that
    includes every field of every pool.

### Add a Worker Pool

To add a pool (or change pool sizing), apply the **complete** workers list
with all fields for all pools:

```bash
kubectl patch kdccluster dev -n my-project --type merge -p '{
  "spec": {
    "workers": [
      {
        "name": "workers",
        "replicas": 3,
        "cpuCores": 2,
        "memory": "8Gi",
        "image": "docker.io/shalb/ubuntu-2404-container-disk:v1.36.1",
        "infrastructureProvider": "kubevirt",
        "storageType": "datavolume"
      },
      {
        "name": "highmem-pool",
        "replicas": 2,
        "cpuCores": 4,
        "memory": "16Gi",
        "image": "docker.io/shalb/ubuntu-2404-container-disk:v1.36.1",
        "infrastructureProvider": "kubevirt",
        "storageType": "datavolume"
      }
    ]
  }
}'
```

New workers boot, join, and become `Ready` in roughly 3-10 minutes
(root-disk import plus kubeadm join).

### Scale to Zero

Scale a worker pool to zero to temporarily stop worker VMs while keeping the control plane running:

```bash
kubectl patch kdccluster dev -n my-project --type=json \
  -p '[{"op":"replace","path":"/spec/workers/0/replicas","value":0}]'
```

The control plane continues running and the cluster remains accessible via `kubectl`. Scale back up when needed.

## Upgrading Kubernetes Version

You can upgrade your cluster's Kubernetes version with a single command. The upgrade is fully automated — control plane is updated first, then worker nodes are replaced one by one with zero downtime.

### Available Versions

| Version | Worker Image | Status |
|---------|-------------|--------|
| v1.36.1 | `docker.io/shalb/ubuntu-2404-container-disk:v1.36.1` | Latest |
| v1.35.0 | `docker.io/shalb/ubuntu-2404-container-disk:v1.35.2` | Supported |
| v1.34.0 | `quay.io/capk/ubuntu-2404-container-disk:v1.34.1` | Supported |

### Via Dashboard

When an upgrade is available, the cluster detail page shows an **Upgrade to vX.Y.Z** button in the header and a version badge in the Summary tab.

![Kubernetes Upgrade via Dashboard](images/k8s-upgrade.png)

1. Open the cluster detail page in the dashboard
2. Click the **Upgrade to vX.Y.Z** button next to the version badge
3. Review the confirmation dialog — it shows the target version and worker image
4. Click **Upgrade** to start the rolling upgrade

The upgrade progress is visible in the cluster status. The phase will change during the upgrade and return to **Ready** once complete.

### Via kubectl

**Step 1: Check current version**

```bash
kubectl get kdccluster dev -n my-project
# NAME   VERSION   PHASE   ENDPOINT   DATASTORE   AGE
# dev    v1.34.0   Ready   ...        dev-etcd    29d
```

**Step 2: Upgrade version and worker image**

Patch both `spec.version` and the worker image in a single command:

```bash
kubectl patch kdccluster dev -n my-project --type=json -p '[
  {"op":"replace","path":"/spec/version","value":"v1.35.0"},
  {"op":"replace","path":"/spec/workers/0/image","value":"docker.io/shalb/ubuntu-2404-container-disk:v1.35.2"}
]'
```

For clusters with multiple worker pools, update each pool's image:

```bash
kubectl patch kdccluster dev -n my-project --type=json -p '[
  {"op":"replace","path":"/spec/version","value":"v1.35.0"},
  {"op":"replace","path":"/spec/workers/0/image","value":"docker.io/shalb/ubuntu-2404-container-disk:v1.35.2"},
  {"op":"replace","path":"/spec/workers/1/image","value":"docker.io/shalb/ubuntu-2404-container-disk:v1.35.2"}
]'
```

**Step 3: Monitor the upgrade**

The upgrade happens in two phases:

1. **Control plane** (~2-5 min) — API server, scheduler, and controller-manager are updated
2. **Worker rollout** (~3-10 min per node) — New workers are created before old ones are removed

```bash
# Watch cluster status
kubectl get kdccluster dev -n my-project -w

# Watch worker machine rollout
kubectl get machines -n my-project -l cluster.x-k8s.io/cluster-name=dev -w
```

During the worker rollout, you will see both old and new machines running simultaneously. The new worker joins the cluster and becomes Ready before the old worker is drained and removed. Your workloads continue running without interruption.

**Step 4: Verify the upgrade**

```bash
# Check cluster version
kubectl get kdccluster dev -n my-project
# NAME   VERSION   PHASE   ...
# dev    v1.35.0   Ready   ...

# Check node versions inside the tenant cluster
kubectl --kubeconfig=/tmp/dev-kubeconfig get nodes -o wide
# NAME                    STATUS   VERSION   CONTAINER-RUNTIME
# dev-workers-xxx-yyy     Ready    v1.35.2   containerd://2.2.2
```

### Important Notes

- **Sequential minor versions only** — You must upgrade one minor version at a time (e.g., v1.34 → v1.35). Skipping versions is not supported.
- **No downgrades** — Kubernetes version downgrades are not supported. The system will reject any attempt to lower the version.
- **Zero downtime** — Workers are replaced using a rolling update strategy (`maxSurge=1`). A new worker is created and becomes Ready before the old one is removed, so your workloads are never interrupted.
- **Image must match version** — Always update the worker image alongside the version. The image contains the matching kubelet and kubeadm binaries.

## Deleting a Cluster

### Via Dashboard

Navigate to the cluster detail page and use the delete action.

### Via kubectl

```bash
kubectl delete kdccluster dev -n my-project
```

Deletion is fully automated. The controller removes resources in the correct order:

1. Worker nodes (MachineDeployments, VMs)
2. Control plane (TenantControlPlane)
3. Cluster API resources
4. CCM deployment
5. Services and EIPs
6. Dedicated datastore (if applicable)

!!! warning
    Deleting a cluster is irreversible. All workloads, services, and data inside the cluster will be permanently removed. Back up any important data before deleting.

## Troubleshooting

### `kubectl logs`, `exec`, and `port-forward` return "connection refused"

Commands that stream through a worker's kubelet — `kubectl logs`,
`kubectl exec`, `kubectl port-forward`, `kubectl attach`, and `kubectl top` —
currently fail against managed clusters with:

```
Error from server: Get "https://<worker-ip>:10250/containerLogs/...":
  dial tcp <worker-ip>:10250: connect: connection refused
```

This is a known limitation being addressed, and it is confined to those
streaming commands. Everything that goes through the API server directly —
`kubectl get`, `describe`, `apply`, `edit`, `delete`, `scale`, `rollout` —
works normally, and **your workloads and Services are unaffected**: pods run
and LoadBalancers serve traffic as usual. It does not indicate a problem with
your cluster.

To read application logs in the meantime, ship them off the node instead of
relying on `kubectl logs`:

- send container stdout/stderr to an in-cluster logging stack (for example
  Loki + Grafana, or Elasticsearch) or an external log service;
- or have the application write to a mounted volume or object-storage bucket
  you can read out-of-band.

`kubectl logs` and the other streaming commands will start working again once
the fix ships — no changes to your manifests are needed.

### Cluster Stuck in Provisioning

```bash
# Check events in the project namespace
kubectl get events -n my-project --sort-by='.lastTimestamp' | tail -20

# Check KdcCluster status
kubectl describe kdccluster dev -n my-project

# Check control plane pods
kubectl get pods -n my-project -l kamaji.clastix.io/name=dev-cp
```

### Workers Not Joining

```bash
# Check MachineDeployment status
kubectl get machinedeployments -n my-project

# Check individual machines
kubectl get machines -n my-project

# Check worker VM status
kubectl get vmi -n my-project
```

### Service Not Getting External IP

```bash
# Verify CCM is running
kubectl get deploy -n my-project -l k8s-app=kccm-dev

# Check CCM logs
kubectl logs -n my-project -l k8s-app=kccm-dev

# Verify the service annotation in tenant cluster
kubectl --kubeconfig=/tmp/dev-kubeconfig get svc my-app-lb -o yaml | grep -A2 annotations
```

### PVC Stuck in Pending

```bash
# Check the CSI controller logs on the management cluster
kubectl logs -n my-project -l app=kubevirt-csi-driver --all-containers

# Check the DataVolume import on the management cluster
kubectl get dv -n my-project

# Check the CSI node daemonset in the tenant cluster
kubectl --kubeconfig=/tmp/dev-kubeconfig get pods -n kubevirt-csi-driver

# Verify StorageClass exists in tenant cluster
kubectl --kubeconfig=/tmp/dev-kubeconfig get storageclass
```

A PVC requesting `ReadWriteMany` with Filesystem mode stays `Pending` with the
event `non-block volume with RWX access mode is not supported` — this is by
design, see [Access Modes](#access-modes).

### Pod Stuck in ContainerCreating (`couldn't find device by serial id`)

Volume hotplug into a worker VM is **batched per VM**: while any pending
volume on the same worker cannot finish provisioning (for example a stuck
DataVolume import), KubeVirt waits before attaching the other volumes to that
VM too. Already-bound volumes then fail to mount with `couldn't find device by
serial id` until the broken sibling is resolved.

Fix the failing PVC first (check `kubectl get dv -n my-project` on the
management cluster for `ImportInProgress` with restarts) or delete it — the
healthy volumes attach within a minute afterwards.

### Pod Stuck After Rescheduling to Another Worker

With the default `kubevirt` StorageClass, the backing disk lives on
**node-local storage of the hypervisor host** where it was first provisioned.
If your pod is later rescheduled to a worker VM running on a *different*
hypervisor host, the volume cannot follow: on the management cluster the
hotplug helper pod reports
`didn't match PersistentVolume's node affinity`, and inside the tenant
cluster the pod hangs in `ContainerCreating` with
`couldn't find device by serial id`.

Recover by steering the pod back to the worker it ran on before:

```bash
kubectl cordon <other-workers…>   # cordon all workers except the original
kubectl delete pod <stuck-pod>
kubectl uncordon <other-workers…>
```

For workloads that must survive rescheduling to any worker, use a tenant
StorageClass backed by replicated storage (see
[Additional Storage Classes](#additional-storage-classes)) if your platform
offers one.

## End-to-End Example: WordPress

A complete stateful application — MariaDB and WordPress on persistent volumes,
exposed over HTTPS through the management cluster's Gateway. Apply inside the
tenant cluster (the ACME Issuer from
[Exposing Services (HTTPS Gateway Route)](#exposing-services-https-gateway-route)
must exist in your project):

```yaml
apiVersion: v1
kind: Namespace
metadata:
  name: wordpress
---
apiVersion: v1
kind: Secret
metadata:
  name: mariadb-auth
  namespace: wordpress
type: Opaque
stringData:
  root-password: "change-me-root"
  password: "change-me"
---
apiVersion: v1
kind: PersistentVolumeClaim
metadata:
  name: mariadb-data
  namespace: wordpress
spec:
  accessModes: ["ReadWriteOnce"]
  resources:
    requests:
      storage: 5Gi
---
apiVersion: v1
kind: PersistentVolumeClaim
metadata:
  name: wordpress-data
  namespace: wordpress
spec:
  accessModes: ["ReadWriteOnce"]
  resources:
    requests:
      storage: 5Gi
---
apiVersion: apps/v1
kind: Deployment
metadata:
  name: mariadb
  namespace: wordpress
spec:
  replicas: 1
  strategy:
    type: Recreate
  selector:
    matchLabels:
      app: mariadb
  template:
    metadata:
      labels:
        app: mariadb
    spec:
      containers:
      - name: mariadb
        image: mariadb:11.4
        env:
        - name: MARIADB_ROOT_PASSWORD
          valueFrom: {secretKeyRef: {name: mariadb-auth, key: root-password}}
        - name: MARIADB_DATABASE
          value: wordpress
        - name: MARIADB_USER
          value: wordpress
        - name: MARIADB_PASSWORD
          valueFrom: {secretKeyRef: {name: mariadb-auth, key: password}}
        ports:
        - containerPort: 3306
        volumeMounts:
        - name: data
          mountPath: /var/lib/mysql
      volumes:
      - name: data
        persistentVolumeClaim:
          claimName: mariadb-data
---
apiVersion: v1
kind: Service
metadata:
  name: mariadb
  namespace: wordpress
spec:
  selector:
    app: mariadb
  ports:
  - port: 3306
---
apiVersion: apps/v1
kind: Deployment
metadata:
  name: wordpress
  namespace: wordpress
spec:
  replicas: 1
  strategy:
    type: Recreate
  selector:
    matchLabels:
      app: wordpress
  template:
    metadata:
      labels:
        app: wordpress
    spec:
      containers:
      - name: wordpress
        image: wordpress:6-apache
        env:
        - name: WORDPRESS_DB_HOST
          value: mariadb.wordpress.svc.cluster.local
        - name: WORDPRESS_DB_NAME
          value: wordpress
        - name: WORDPRESS_DB_USER
          value: wordpress
        - name: WORDPRESS_DB_PASSWORD
          valueFrom: {secretKeyRef: {name: mariadb-auth, key: password}}
        ports:
        - containerPort: 80
        volumeMounts:
        - name: data
          mountPath: /var/www/html
      volumes:
      - name: data
        persistentVolumeClaim:
          claimName: wordpress-data
---
apiVersion: v1
kind: Service
metadata:
  name: wordpress
  namespace: wordpress
  annotations:
    service.nlb.kube-dc.com/expose-route: "https"
    service.nlb.kube-dc.com/route-hostname: "wordpress-my-project.kube-dc.cloud"
spec:
  type: LoadBalancer
  selector:
    app: wordpress
  ports:
  - port: 80
    targetPort: 80
```

Within ~3 minutes the PVCs bind, the disks hotplug into the worker VMs, the
certificate issues, and `https://wordpress-my-project.kube-dc.cloud` serves
the WordPress installer. Data survives pod restarts; for rescheduling across
workers mind the
[placement caveat](#pod-stuck-after-rescheduling-to-another-worker) of the
default storage class.

## Quick Reference

| Operation | Command |
|-----------|---------|
| List clusters | `kubectl get kdccluster -n my-project` |
| Get cluster details | `kubectl describe kdccluster dev -n my-project` |
| Get kubeconfig | `kubectl get secret dev-cp-admin-kubeconfig -n my-project -o jsonpath='{.data.admin\.conf}' \| base64 -d` (see [Getting the Kubeconfig](#getting-the-kubeconfig)) |
| Check endpoint | `kubectl get kdccluster dev -n my-project -o jsonpath='{.status.endpoint}'` |
| Scale workers | `kubectl patch kdccluster dev -n my-project --type=json -p '[{"op":"replace","path":"/spec/workers/0/replicas","value":5}]'` |
| Delete cluster | `kubectl delete kdccluster dev -n my-project` |
| Check datastore | `kubectl get kdcclusterdatastores -n my-project` |

## Next Steps

- [Provisioning a Cluster](provisioning-cluster.md)
- [Service Exposure Guide](service-exposure.md) — More on networking and service exposure
- [Block Storage](block-storage.md) — Additional storage options
