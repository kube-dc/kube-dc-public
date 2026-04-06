# Provisioning a Managed Kubernetes Cluster

Kube-DC provides fully managed Kubernetes clusters that run inside your project namespace. Each cluster is an isolated, production-grade Kubernetes environment with its own control plane, worker nodes, and networking — provisioned in minutes.

## How It Works

When you create a cluster, Kube-DC orchestrates several components:

- **Control Plane** — Managed by [Kamaji](https://kamaji.clastix.io/). The API server, scheduler, and controller manager run as pods in your project namespace — no VMs needed for the control plane.
- **Worker Nodes** — Provisioned as KubeVirt virtual machines via [Cluster API](https://cluster-api.sigs.k8s.io/). Each worker is a real VM running kubelet.
- **etcd DataStore** — A dedicated or shared etcd cluster stores your Kubernetes state, managed automatically with TLS certificates and persistent storage.
- **Cloud Controller Manager (CCM)** — Bridges your tenant cluster with the infrastructure, enabling LoadBalancer services and node lifecycle management.
- **Cluster Addons** — Cilium CNI, CoreDNS, and optionally KubeVirt CSI are deployed automatically via [Sveltos](https://projectsveltos.github.io/sveltos/).

```
┌───────────────────── Management Cluster ───────────────────────────┐
│                                                                    │
│  ┌─────────── Project Namespace (e.g. shalb-jumbolot) ───────────┐ │
│  │                                                               │ │
│  │  KdcCluster "dev"                                             │ │
│  │    ├── Service: dev-cp (LoadBalancer → API server)            │ │
│  │    ├── TenantControlPlane: dev-cp (Kamaji)                    │ │
│  │    │     └── Pods: api-server, scheduler, controller-manager  │ │
│  │    ├── KdcClusterDatastore: dev-etcd                          │ │
│  │    │     └── StatefulSet: dev-etcd-etcd (etcd cluster)        │ │
│  │    ├── MachineDeployment: dev-workers                         │ │
│  │    │     └── VMs: dev-workers-xxxxx (KubeVirt)                │ │
│  │    └── CCM Deployment: kccm-dev                               │ │
│  │                                                               │ │
│  └───────────────────────────────────────────────────────────────┘ │
└────────────────────────────────────────────────────────────────────┘
                              │
                              ▼
┌────────────────── Tenant Cluster "dev" ───────────────────────────┐
│  Nodes: worker-1, worker-2, worker-3                              │
│  Your workloads: Deployments, Services, PVCs                      │
└───────────────────────────────────────────────────────────────────┘
```

## Prerequisites

- A Kube-DC Cloud [project](first-project.md) with sufficient resource quota
- For kubectl access: [kubeconfig configured](cli-kubeconfig.md) for the management cluster

## Managed Kubernetes Clusters View

Navigate to **Kubernetes** in your project to view all managed clusters:

<img src={require('./images/k8s-screen.png').default} alt="Managed Kubernetes clusters view" style={{maxWidth: '900px', width: '100%'}} />

The clusters view shows all your managed Kubernetes clusters with their version, phase (status), API endpoint, datastore backend, and age. From here you can create new clusters, view cluster details, download kubeconfig files, or delete clusters.

## Create a Cluster via Dashboard

The dashboard provides a guided 5-step wizard.

### Step 1: Basic Configuration

<img src={require('./images/k8s-create-cluster.png').default} alt="Create Cluster - Basic Config" style={{maxWidth: '600px', width: '100%'}} />

- **Cluster Name** — Unique within your project (3–12 chars, lowercase, hyphens allowed).
- **Kubernetes Version** — Select the desired version (e.g., `v1.34.0`).
- **Control Plane Replicas**:
  - `1` — Development
  - `2` (High Availability) — Recommended for production
  - `3` — Maximum redundancy
- **External IP Network Type**:
  - **Cloud** (recommended) — API accessible via shared gateway using TLS passthrough. No dedicated public IP consumed.
  - **Public** — Dedicated public IP exposed directly to the internet. Use only when direct IP access is required.
- **Expose API endpoint externally (via TLSRoute)** — Check this to make the API reachable from outside the cluster network (e.g., `https://dev-cp-shalb-jumbolot.kube-dc.cloud:443`).

### Step 2: Datastore Configuration

<img src={require('./images/k8s-datastore.png').default} alt="Create Cluster - Datastore" style={{maxWidth: '600px', width: '100%'}} />

Choose how your cluster's etcd state is stored:

| Mode | Description | Best For |
|------|-------------|----------|
| **Shared Datastore** (cost-effective) | Multiple clusters share one etcd, each on a different port | Development, staging |
| **Dedicated Datastore** (isolated) | Your cluster gets its own etcd | Production workloads |

For dedicated datastores:

- **etcd Replicas** — `1` for dev, `3` for production HA, `5` for maximum redundancy.
- **Service Port** — A unique port on the shared EIP. The UI shows ports already in use to avoid conflicts.

### Step 3: Network & Addons

<img src={require('./images/k8s-network-addons.png').default} alt="Create Cluster - Network" style={{maxWidth: '600px', width: '100%'}} />

- **Service CIDR** — IP range for Kubernetes Services (default: `10.96.0.0/16`). Each cluster has isolated networking.
- **Pod CIDR** — IP range for Pods (default: `10.244.0.0/16`).

**Cluster Addons** deployed automatically:

| Addon | Description | Recommended |
|-------|-------------|-------------|
| **Cilium CNI** | Pod networking and network policy | Yes (required) |
| **CoreDNS** | Cluster DNS (`cluster.local`) | Yes (required) |
| **KubeVirt CSI** | Persistent storage via hotplug DataVolumes | Enable for stateful workloads |

### Step 4: Worker Pools

<img src={require('./images/k8s-workers.png').default} alt="Create Cluster - Workers" style={{maxWidth: '600px', width: '100%'}} />

Define one or more worker pools — each a group of identically configured VMs:

- **Pool Name** — Identifier (e.g., `workers`).
- **Replicas** — Number of worker VMs.
- **CPU Cores** / **Memory (GB)** — Resources per worker.
- **Worker Type** — `DataVolume (Persistent)` provides root disks that survive VM restarts.
- **Container Image** — OS image for worker VMs (e.g., `Ubuntu 24.04 + K8s v1.34.1`).

Click **+ Add Pool** to add pools with different configurations (e.g., a general pool and a high-memory pool).

### Step 5: Review and Create

<img src={require('./images/k8s-review.png').default} alt="Create Cluster - Review" style={{maxWidth: '600px', width: '100%'}} />

The Review step shows the exact YAML manifests that will be applied. Inspect both the **KdcCluster** and **Datastore** tabs to verify the configuration.

Click **Create Cluster** to start provisioning.

## Create a Cluster via kubectl

Apply a `KdcCluster` resource to your project namespace:

```yaml
apiVersion: k8s.kube-dc.com/v1alpha1
kind: KdcCluster
metadata:
  name: dev
  namespace: my-project
  annotations:
    k8s.kube-dc.com/expose-route: "true"
spec:
  version: v1.34.0
  controlPlane:
    replicas: 1
  dataStore:
    dedicated: true
    eipName: default-gw
    port: 32381
  network:
    serviceCIDR: 10.96.0.0/16
    podCIDR: 10.244.0.0/16
  eip:
    create: true
    externalNetworkType: cloud
  enableClusterAPI: true
  workers:
    - name: workers
      replicas: 3
      cpuCores: 2
      memory: 8Gi
      diskSize: 30Gi
      image: docker.io/shalb/ubuntu-2404-container-disk:v1.35.2
      architecture: amd64
      infrastructureProvider: kubevirt
      storageType: datavolume
```

```bash
kubectl apply -f cluster.yaml
```

### Spec Reference

| Field | Description | Default |
|-------|-------------|---------|
| `spec.version` | Kubernetes version | Required |
| `spec.controlPlane.replicas` | API server replicas (1–5) | `2` |
| `spec.dataStore.dedicated` | Create dedicated etcd | `false` |
| `spec.dataStore.eipName` | EIP for datastore LoadBalancer | `default-gw` |
| `spec.dataStore.port` | Port on shared EIP (must be unique per cluster) | `2379` |
| `spec.network.serviceCIDR` | Kubernetes service CIDR | `10.96.0.0/12` |
| `spec.network.podCIDR` | Pod network CIDR | `10.244.0.0/16` |
| `spec.eip.create` | Auto-create EIP for API server | `true` |
| `spec.eip.externalNetworkType` | `cloud` or `public` | `cloud` |
| `spec.enableClusterAPI` | Enable worker node management | `true` |
| `spec.workers[].name` | Worker pool name (unique) | Required |
| `spec.workers[].replicas` | Number of workers in pool | Required |
| `spec.workers[].cpuCores` | CPU cores per worker | Required |
| `spec.workers[].memory` | Memory per worker (e.g., `4Gi`) | Required |
| `spec.workers[].image` | Container disk image for workers | Required |
| `spec.workers[].storageType` | `datavolume` (persistent) or empty (ephemeral) | — |
| `spec.workers[].infrastructureProvider` | `kubevirt` or `cloudsigma` | `kubevirt` |

### Annotations

| Annotation | Description |
|------------|-------------|
| `k8s.kube-dc.com/expose-route: "true"` | Expose API endpoint externally via TLSRoute (recommended for cloud network type) |

## Monitor Cluster Creation

```bash
# Watch cluster status
kubectl get kdccluster -n my-project -w

# Example output:
# NAME   VERSION   PHASE   ENDPOINT                                        DATASTORE   AGE
# dev    v1.34.0   Ready   https://dev-cp-my-project.kube-dc.cloud:443     dev-etcd    5m

# Check datastore status
kubectl get kdcclusterdatastores -n my-project

# Example output:
# NAME       READY   DEDICATED   DATASTORE   AGE
# dev-etcd   Ready   true        dev-etcd    5m
```

Provisioning typically takes 3–5 minutes. The cluster goes through phases: `Pending` → `WaitingForService` → `Provisioning` → `Ready`.

## Access Your Cluster

### Via Dashboard

Once the cluster is `Ready`, click the **Kubeconfig** button on the cluster detail page to download the kubeconfig file.

<img src={require('./images/k8s-cluster-view.png').default} alt="Cluster View" style={{maxWidth: '600px', width: '100%'}} />

The cluster detail page shows:

- **Phase** — Current state (Ready)
- **Control Plane** — Replicas and health status
- **API Endpoint** — The external URL (e.g., `https://dev-cp-shalb-jumbolot.kube-dc.cloud:443`)
- **DataStore** — etcd name and type (Shared/Dedicated)
- **Worker Pools** — Number of pools and total workers
- Tabs for **Summary**, **Workers**, **Network**, and **YAML** views

### Via kubectl

```bash
# Extract kubeconfig from the admin secret
kubectl get secret dev-cp-admin-kubeconfig -n my-project \
  -o jsonpath='{.data.super-admin\.svc}' | base64 -d > /tmp/dev-kubeconfig

# Verify access to the tenant cluster
kubectl --kubeconfig=/tmp/dev-kubeconfig get nodes
```

## What Gets Created

When you create a `KdcCluster`, the following resources are provisioned automatically in your project namespace:

| Resource | Name Pattern | Purpose |
|----------|-------------|---------|
| `Service` (LoadBalancer) | `{cluster}-cp` | API server endpoint |
| `Service` (ClusterIP) | `{cluster}-cp-ext` | External DNS endpoint |
| `TenantControlPlane` | `{cluster}-cp` | Kamaji control plane pods |
| `KdcClusterDatastore` | `{cluster}-etcd` | etcd (if dedicated) |
| `StatefulSet` | `{cluster}-etcd-etcd` | etcd pods |
| `Service` (LoadBalancer) | `{cluster}-etcd-etcd-lb` | etcd external access |
| `Cluster` (CAPI) | `{cluster}` | Cluster API cluster object |
| `MachineDeployment` | `{cluster}-{pool}` | Worker pool VMs |
| `Deployment` | `kccm-{cluster}` | Cloud Controller Manager |

**Example** — Services created for a cluster named `dev` in namespace `shalb-jumbolot`:

```
$ kubectl get svc -n shalb-jumbolot | grep dev
dev-cp                  LoadBalancer   10.101.79.219    100.65.0.148   6443/TCP    18d
dev-cp-ext              ClusterIP      None             <none>         6443/TCP    18d
dev-etcd-etcd           ClusterIP      None             <none>         2379/TCP    18d
dev-etcd-etcd-lb        LoadBalancer   10.101.20.207    100.65.0.115   32382/TCP   18d
dev-etcd-etcd-lb-ext    ClusterIP      None             <none>         32382/TCP   18d
```

## Next Steps

- [Cluster Management](cluster-management.md) — Exposing services, persistent storage, scaling, and operations
- [CLI & Kubeconfig Access](cli-kubeconfig.md)
