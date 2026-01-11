# Kube-DC Infrastructure Requirements

This document outlines the resource requirements for deploying and operating a Kube-DC platform.

## Architecture Overview

### Management Platform (Provider Infrastructure)

The Kube-DC management platform runs on **3 VMs** that host:
- Kubernetes control plane (RKE2/K3s)
- All Kube-DC platform components (controllers, UI, monitoring)
- Kamaji controller for managing tenant control planes
- Tenant control plane pods (scaled on-demand)

**Key Principle:** Tenant control planes run as pods on the management cluster and scale flexibly by adding more VMs to the management cluster as tenant count grows.

### Tenant Infrastructure (Customer Infrastructure)

- **Worker VMs:** Run on customer's CloudSigma account
- **Storage (Disks):** Provisioned on customer's CloudSigma account via CSI driver
- **Networking:** Customer's CloudSigma network resources (IPs, VLANs)

**Key Principle:** Customer workloads and data remain on customer infrastructure. Only the control plane is managed centrally.

---

## Management Cluster Base Requirements

### Production Setup: 3-VM Architecture

| VM Role | Nb VMs | CPU Cores | CPU (GHz) | RAM (GB) | DISK (GB) | Purpose |
|---------|--------|-----------|-----------|----------|-----------|---------|
| **Control Plane + Tenant CP Host** | 3 | 8 | 16-24 | 64 | 200+ | K8s control plane, platform components, tenant control planes |

**Scaling Model:**
- Start with 3 VMs for HA and base capacity
- Add VMs horizontally as tenant cluster count grows
- Each additional VM can host ~10-20 tenant control planes
- No distinction between "master" and "worker" - all nodes can host tenant control planes

---

## Management Cluster Components (Shared Infrastructure)

These components run once in the management cluster and serve all tenant clusters.

### Core Platform Components

| Component | Nb Instances | CPU (cores) | CPU (MHz) | RAM per Instance (GB) | DISK per Instance (GB) | Unmounted Drives (GB) | Type | Notes |
|-----------|--------------|-------------|-----------|----------------------|----------------------|---------------------|------|-------|
| **Kube-DC Manager** | 1 | 0.5 | 1000 | 0.5 | - | - | Pod | Core controller managing CRDs |
| **UI Frontend** | 1 | 0.2 | 400 | 0.5 | - | - | Pod | React application |
| **UI Backend** | 1 | 0.5 | 1000 | 1 | - | - | Pod | Node.js API server |
| **Keycloak** | 1 | 0.25 | 500 | 0.5 | - | - | Pod | Identity and access management |
| **Keycloak PostgreSQL** | 1 | 0.25 | 500 | 0.25 | 5 | - | Pod | Keycloak database |
| **Kamaji Controller** | 1 | 0.5 | 1000 | 0.25 | - | - | Pod | Tenant control plane manager |
| **Kamaji etcd** | 3 | 0.5 | 1000 | 0.5 | - | 10 | StatefulSet | Shared etcd for all tenant control planes (default) |
| **Cert-Manager** | 1 | 0.2 | 400 | 0.25 | - | - | Pod | Certificate automation |
| **Envoy Gateway** | 2 | 0.5 | 1000 | 0.5 | - | - | Deployment | Ingress controller |
| **noVNC** | 1 | 0.1 | 200 | 0.25 | - | - | Pod | VM console access |

**Subtotal (Core Platform):** ~5 CPU cores, ~5 GB RAM, ~15 GB storage

### Monitoring Stack (Shared)

| Component | Nb Instances | CPU (cores) | CPU (MHz) | RAM per Instance (GB) | DISK per Instance (GB) | Unmounted Drives (GB) | Type | Notes |
|-----------|--------------|-------------|-----------|----------------------|----------------------|---------------------|------|-------|
| **Prometheus** | 1 | 1 | 2000 | 2 | - | 20 | StatefulSet | Metrics storage and alerting |
| **Prometheus Operator** | 1 | 0.5 | 1000 | 0.25 | - | - | Pod | Prometheus lifecycle manager |
| **Alertmanager** | 1 | 0.5 | 1000 | 0.25 | - | - | Pod | Alert routing and notification |
| **Grafana** | 1 | 0.5 | 1000 | 0.5 | - | - | Pod | Visualization dashboards |
| **Loki Backend** | 1 | 0.5 | 1000 | 1 | - | - | Pod | Log aggregation backend |
| **Loki Read** | 1 | 0.5 | 1000 | 0.5 | - | - | Pod | Log query service |
| **Loki Write** | 1 | 0.5 | 1000 | 0.5 | - | - | Pod | Log ingestion service |
| **Loki Minio** | 1 | 0.5 | 1000 | 0.25 | - | 10 | StatefulSet | Object storage for logs (2 drives) |
| **Grafana Alloy** | 1+ | 0.2 | 400 | 0.25 | - | - | DaemonSet | Metrics/logs collector (per node) |
| **Node Exporter** | 1+ | 0.1 | 200 | 0.06 | - | - | DaemonSet | Node metrics (per node) |
| **Kube State Metrics** | 1 | 0.2 | 400 | 0.12 | - | - | Pod | Kubernetes metrics |

**Subtotal (Monitoring):** ~5 CPU cores, ~6 GB RAM, ~30 GB storage

### Networking Stack (Shared)

| Component | Nb Instances | CPU (cores) | CPU (MHz) | RAM per Instance (GB) | DISK per Instance (GB) | Unmounted Drives (GB) | Type | Notes |
|-----------|--------------|-------------|-----------|----------------------|----------------------|---------------------|------|-------|
| **Kube-OVN Controller** | 1 | 1 | 2000 | 1 | - | - | Pod | OVN network controller |
| **Kube-OVN CNI** | 1+ | 0.5 | 1000 | 0.5 | - | - | DaemonSet | CNI plugin (per node) |
| **OVN Central** | 3 | 0.5 | 1000 | 1 | - | - | StatefulSet | OVN database cluster |
| **Multus CNI** | 1+ | 0.1 | 200 | 0.25 | - | - | DaemonSet | Multi-network support (per node) |

**Subtotal (Networking):** ~4 CPU cores, ~5 GB RAM

### Virtualization Stack (Shared)

| Component | Nb Instances | CPU (cores) | CPU (MHz) | RAM per Instance (GB) | DISK per Instance (GB) | Unmounted Drives (GB) | Type | Notes |
|-----------|--------------|-------------|-----------|----------------------|----------------------|---------------------|------|-------|
| **KubeVirt Operator** | 1 | 0.5 | 1000 | 0.5 | - | - | Pod | VM lifecycle manager |
| **KubeVirt Handler** | 1+ | 0.2 | 400 | 0.25 | - | - | DaemonSet | VM runtime (per node) |
| **CDI Operator** | 1 | 0.5 | 1000 | 0.5 | - | - | Pod | DataVolume manager |
| **CDI Controller** | 1 | 0.2 | 400 | 0.25 | - | - | Pod | Import/upload controller |

**Subtotal (Virtualization):** ~2 CPU cores, ~2 GB RAM

### Cluster API Stack (Shared)

| Component | Nb Instances | CPU (cores) | CPU (MHz) | RAM per Instance (GB) | DISK per Instance (GB) | Unmounted Drives (GB) | Type | Notes |
|-----------|--------------|-------------|-----------|----------------------|----------------------|---------------------|------|-------|
| **CAPI Controller** | 1 | 0.5 | 1000 | 0.5 | - | - | Pod | Cluster API core |
| **CAPI KubeVirt Provider** | 1 | 0.2 | 400 | 0.25 | - | - | Pod | KubeVirt infrastructure provider |
| **CAPI K3s Provider** | 1 | 0.2 | 400 | 0.25 | - | - | Pod | K3s bootstrap/control plane |
| **Sveltos** | 1 | 0.2 | 400 | 0.25 | - | - | Pod | Addon deployment controller |
| **Kyverno** | 3 | 0.2 | 400 | 0.25 | - | - | Deployment | Policy engine |

**Subtotal (Cluster API):** ~2 CPU cores, ~2 GB RAM

---

## Management Cluster Total (Base Infrastructure)

| Category | CPU Cores | RAM (GB) | Storage (GB) |
|----------|-----------|----------|--------------|
| Core Platform | ~5 | ~5 | ~15 |
| Monitoring | ~5 | ~6 | ~30 |
| Networking | ~4 | ~5 | - |
| Virtualization | ~2 | ~2 | - |
| Cluster API | ~2 | ~2 | - |
| **TOTAL** | **~18** | **~20** | **~45** |

**Note:** This excludes:
- OS and system processes overhead (~2 CPU, ~4 GB RAM)
- etcd for Kubernetes control plane (~1 CPU, ~2 GB RAM, ~20 GB storage)
- RKE2/K3s components (~2 CPU, ~4 GB RAM)

**Recommended Management Cluster Minimum:** 24 CPU cores, 32 GB RAM, 100 GB storage per node

---

## Per-Tenant Cluster Components (Multiply by Number of Clusters)

These components are deployed for EACH tenant Kubernetes cluster created via KdcCluster.

### Control Plane (Kamaji TenantControlPlane)

| Component | Nb Instances | CPU (cores) | CPU (MHz) | RAM per Instance (GB) | DISK per Instance (GB) | Unmounted Drives (GB) | Type | Notes |
|-----------|--------------|-------------|-----------|----------------------|----------------------|---------------------|------|-------|
| **kube-apiserver** | 2 | 0.5 | 1000 | 1 | - | - | Pod | Kubernetes API server (HA) |
| **kube-controller-manager** | 2 | 0.5 | 1000 | 0.5 | - | - | Pod | K8s controller manager |
| **kube-scheduler** | 2 | 0.2 | 400 | 0.25 | - | - | Pod | K8s scheduler |

**Subtotal per Cluster (Control Plane with shared etcd):** ~2.4 CPU cores, ~3.5 GB RAM

### Dedicated etcd (Optional, per Cluster)

| Component | Nb Instances | CPU (cores) | CPU (MHz) | RAM per Instance (GB) | DISK per Instance (GB) | Unmounted Drives (GB) | Type | Notes |
|-----------|--------------|-------------|-----------|----------------------|----------------------|---------------------|------|-------|
| **etcd** | 3 | 0.5 | 1000 | 0.5 | - | 10 | StatefulSet | Dedicated etcd cluster (if not using shared) |

**Subtotal (Dedicated etcd):** ~1.5 CPU cores, ~1.5 GB RAM, ~30 GB storage

### Worker Nodes (Configurable per Cluster)

Worker nodes are provisioned as VMs via Cluster API. Resources depend on tenant requirements.

#### Example Worker Configurations

**Minimal Worker:**
- CPU: 1 core (2000 MHz)
- RAM: 1 GB
- Disk: 20 GB

**Standard Worker:**
- CPU: 2 cores (4000 MHz)
- RAM: 4 GB
- Disk: 40 GB

**High-Memory Worker:**
- CPU: 4 cores (8000 MHz)
- RAM: 16 GB
- Disk: 80 GB

### CloudSigma-Specific Components (Per Cluster using CloudSigma)

| Component | Nb Instances | CPU (cores) | CPU (MHz) | RAM per Instance (GB) | DISK per Instance (GB) | Unmounted Drives (GB) | Type | Notes |
|-----------|--------------|-------------|-----------|----------------------|----------------------|---------------------|------|-------|
| **CloudSigma CCM** | 1 | 0.2 | 400 | 0.1 | - | - | DaemonSet | Cloud controller manager (on control plane) |
| **CloudSigma CSI Controller** | 1 | 0.5 | 1000 | 0.25 | - | - | Deployment | Storage provisioner controller |
| **CloudSigma CSI Node** | 1+ | 0.5 | 1000 | 0.25 | - | - | DaemonSet | CSI node plugin (per worker) |

**Subtotal (CloudSigma integration per cluster):** ~1.2 CPU cores, ~0.6 GB RAM

### CNI/Networking (Per Cluster - if using Cilium)

| Component | Nb Instances | CPU (cores) | CPU (MHz) | RAM per Instance (GB) | DISK per Instance (GB) | Unmounted Drives (GB) | Type | Notes |
|-----------|--------------|-------------|-----------|----------------------|----------------------|---------------------|------|-------|
| **Cilium Agent** | 1+ | 0.1 | 200 | 0.5 | - | - | DaemonSet | CNI agent (per node) |
| **Cilium Operator** | 1 | 0.1 | 200 | 0.12 | - | - | Deployment | Cilium operator |

**Subtotal (Cilium per cluster):** ~0.2 CPU cores (+ per node), ~0.62 GB RAM

---

## Per-Tenant Cluster Total

### Configuration 1: Minimal (Shared etcd, 2 minimal workers)

| Component | CPU Cores | RAM (GB) | Storage (GB) |
|-----------|-----------|----------|--------------|
| Control Plane (2 replicas) | 2.4 | 3.5 | - |
| Workers (2x minimal) | 2 | 2 | 40 |
| CloudSigma Integration | 1.2 | 0.6 | - |
| Cilium CNI | 0.4 | 1.1 | - |
| **TOTAL** | **6** | **7.2** | **40** |

### Configuration 2: Standard (Shared etcd, 3 standard workers)

| Component | CPU Cores | RAM (GB) | Storage (GB) |
|-----------|-----------|----------|--------------|
| Control Plane (2 replicas) | 2.4 | 3.5 | - |
| Workers (3x standard) | 6 | 12 | 120 |
| CloudSigma Integration | 1.7 | 1.35 | - |
| Cilium CNI | 0.5 | 1.62 | - |
| **TOTAL** | **10.6** | **18.47** | **120** |

### Configuration 3: Production (Dedicated etcd, 3 standard workers)

| Component | CPU Cores | RAM (GB) | Storage (GB) |
|-----------|-----------|----------|--------------|
| Control Plane (2 replicas) | 2.4 | 3.5 | - |
| Dedicated etcd (3 replicas) | 1.5 | 1.5 | 30 |
| Workers (3x standard) | 6 | 12 | 120 |
| CloudSigma Integration | 1.7 | 1.35 | - |
| Cilium CNI | 0.5 | 1.62 | - |
| **TOTAL** | **12.1** | **19.97** | **150** |

---

## Example Deployment Scenarios

### Scenario 1: Small Platform (5 Tenant Clusters)

| Infrastructure | CPU Cores | RAM (GB) | Storage (GB) |
|----------------|-----------|----------|--------------|
| Management Cluster Base | 18 | 20 | 45 |
| 5x Tenant Clusters (Standard) | 53 | 92.35 | 600 |
| OS/System Overhead (3 nodes) | 6 | 12 | 60 |
| **TOTAL** | **77** | **124.35** | **705** |

**Recommended Infrastructure:**
- 3x Master/Worker nodes: 32 cores, 48 GB RAM, 300 GB SSD each

### Scenario 2: Medium Platform (20 Tenant Clusters)

| Infrastructure | CPU Cores | RAM (GB) | Storage (GB) |
|----------------|-----------|----------|--------------|
| Management Cluster Base | 18 | 20 | 45 |
| 20x Tenant Clusters (Standard) | 212 | 369.4 | 2400 |
| OS/System Overhead (6 nodes) | 12 | 24 | 120 |
| **TOTAL** | **242** | **413.4** | **2565** |

**Recommended Infrastructure:**
- 3x Master nodes: 32 cores, 64 GB RAM, 500 GB SSD each
- 6x Worker nodes: 48 cores, 96 GB RAM, 1 TB SSD each

### Scenario 3: Large Platform (50 Tenant Clusters)

| Infrastructure | CPU Cores | RAM (GB) | Storage (GB) |
|----------------|-----------|----------|--------------|
| Management Cluster Base | 18 | 20 | 45 |
| 50x Tenant Clusters (Standard) | 530 | 923.5 | 6000 |
| OS/System Overhead (12 nodes) | 24 | 48 | 240 |
| **TOTAL** | **572** | **991.5** | **6285** |

**Recommended Infrastructure:**
- 3x Master nodes: 64 cores, 128 GB RAM, 1 TB SSD each
- 12x Worker nodes: 64 cores, 128 GB RAM, 2 TB SSD each

---

## Storage Requirements Breakdown

### Management Cluster Persistent Storage

| Volume Type | Size per Instance | Instances | Total | Purpose |
|-------------|------------------|-----------|-------|---------|
| Kamaji etcd | 10 GB | 3 | 30 GB | Shared etcd datastore |
| Prometheus | 20 GB | 1 | 20 GB | Metrics storage (configurable: 365d retention) |
| Loki Minio | 10 GB | 1 | 10 GB | Log storage (7d retention) |
| Keycloak PostgreSQL | 5 GB | 1 | 5 GB | User database |
| **TOTAL** | - | - | **65 GB** | - |

### Per-Tenant Cluster Storage

| Volume Type | Size per Instance | Instances | Total | Purpose |
|-------------|------------------|-----------|-------|---------|
| Dedicated etcd (optional) | 10 GB | 3 | 30 GB | Per-cluster etcd |
| Worker OS Disk | 20-80 GB | N | Variable | OS and container images |
| Tenant PVCs | Variable | Variable | Variable | Application storage via CSI |

---

## Network Requirements

### Bandwidth

- **Management Cluster Internal:** 10 Gbps recommended (node-to-node)
- **External Access:** 1 Gbps minimum for API/UI
- **Storage Network (if separate):** 10 Gbps recommended

### IP Address Requirements

#### Management Cluster (Kube-OVN)
- Pod CIDR: /16 recommended (65,536 IPs)
- Service CIDR: /16 recommended (65,536 IPs)
- Per Project VPC: /16 (configurable)

#### Per-Tenant Cluster
- Pod CIDR: /16 (65,536 IPs per cluster)
- Service CIDR: /16 (65,536 IPs per cluster)

#### External IPs
- Management cluster: 1-5 public IPs (UI, API, monitoring)
- Per tenant cluster: Variable (depends on LoadBalancer services)

---

## Notes and Recommendations

### Scaling Considerations

1. **Horizontal Scaling:**
   - Add worker nodes to management cluster as tenant cluster count grows
   - Management cluster workers can be scaled independently
   - Each worker node can host ~10-20 tenant control plane pods

2. **Vertical Scaling:**
   - Increase worker node resources for larger tenant worker VMs
   - Monitoring stack may need scaling with high pod/metric count

3. **Storage Scaling:**
   - Prometheus retention and storage grow with cluster count
   - Consider external object storage for Loki at scale
   - Plan for ~50 GB additional storage per 10 tenant clusters

### Resource Overhead

- **Kubernetes System:** ~10-15% CPU/RAM overhead
- **VM Runtime (KubeVirt):** ~10-20% overhead per VM
- **Network Overhead:** ~5-10% CPU for OVN/CNI

### High Availability

- **Management Cluster:** 3+ master nodes, 3+ worker nodes
- **etcd:** Always use 3 or 5 replicas (odd number)
- **Control Planes:** 2+ replicas per tenant cluster
- **Monitoring:** Consider external storage for production

### Cost Optimization

1. Use shared etcd for non-critical tenant clusters
2. Right-size worker VMs based on actual workload
3. Enable cluster autoscaling for variable workloads
4. Use lower retention for Prometheus/Loki in dev environments

---

## Actual Production Cluster Analysis

### Current Deployment (stage.kube-dc.com)

**Management Cluster Hardware:**
- **Node Count:** 2 nodes (kube-dc-master-1, kube-dc-worker-1)
- **Per Node:** 8 CPU cores, 64 GB RAM, Debian 12
- **Total Capacity:** 16 cores, 128 GB RAM

**Resource Utilization (Live):**
- **kube-dc-master-1:** 40% CPU (3.2 cores), 49% RAM (31 GB)
- **kube-dc-worker-1:** 33% CPU (2.7 cores), 64% RAM (41 GB)
- **Total Used:** ~6 cores (37%), ~72 GB RAM (56%)

**Tenant Clusters Hosted:** 7 active KdcClusters

| Namespace | Cluster Name | Control Plane Replicas | Worker Replicas | Datastore Type |
|-----------|--------------|------------------------|-----------------|----------------|
| cloudsigma-example-3415fbd3 | banja | 1 | 2 | banja-etcd (dedicated) |
| cloudsigma-example-3415fbd3 | xxx | 1 | 1 | banja-etcd (shared) |
| cloudsigma-tan-dovan-90e02d2d | c1 | 1 | 3 | c1-etcd (dedicated) |
| shalb-demo | demo-cluster | 1 | 1 | demo-cluster-etcd (dedicated) |
| shalb-demo | k3s-cluster | 2 | 1 | k3s-cluster-etcd (dedicated) |
| shalb-demo | my-cluster | 2 | 2 | my-cluster-etcd (dedicated) |
| shalb-envoy | three-cluster | 2 | 1 | three-cluster-etcd (dedicated) |

### Tenant Cluster Resource Consumption Analysis

#### Example: "banja" and "xxx" Clusters (cloudsigma-example-3415fbd3)

**Control Plane Pods (Management Cluster):**

| Component | Pod Count | CPU Request | Memory Request | Actual Location |
|-----------|-----------|-------------|----------------|------------------|
| banja-cp | 1 | None | None | kube-dc-worker-1 |
| banja-etcd | 1 | None | None | kube-dc-worker-1 |
| xxx-cp | 1 | None | None | kube-dc-worker-1 |
| csccm-banja (CCM) | 1 | 10m | 64Mi | kube-dc-worker-1 |
| csccm-xxx (CCM) | 1 | 10m | 64Mi | kube-dc-worker-1 |

**Worker VMs (Customer CloudSigma Account):**
- Banja: 2 workers (2 cores, 4GB each) = 4 cores, 8GB total
- xxx: 1 worker (2 cores, 4GB) = 2 cores, 4GB total

**Key Observations:**

1. **No Resource Limits:** Most tenant control plane pods have no CPU/memory requests, relying on cluster overcommit
2. **Shared etcd:** xxx cluster uses banja's etcd datastore (cost optimization)
3. **Lightweight Control Planes:** Each tenant CP consumes ~0.5-1 GB RAM in practice
4. **Management Overhead:** CloudSigma CCM pods: 10m CPU, 64Mi RAM per cluster
5. **Worker VMs on Customer Infrastructure:** Worker nodes NOT visible in management cluster metrics

### Platform Components Resource Usage

**Core Platform (kube-dc namespace):**

| Component | CPU Request | Memory Request | Notes |
|-----------|-------------|----------------|-------|
| kube-dc-manager | None | None | Core controller |
| kube-dc-backend | None | None | API server |
| kube-dc-frontend | None | None | React UI |
| kube-dc-k8-manager | 10m | 64Mi | Tenant cluster controller |

**Monitoring Stack (monitoring namespace):**

| Component | CPU Request | Memory Request | Storage |
|-----------|-------------|----------------|----------|
| Prometheus | None | 1Gi | 20Gi |
| Grafana | None | None | - |
| Alertmanager | None | 128Mi | - |
| Loki Backend | None | None | - |
| Loki Write | None | 256Mi | - |
| Loki Read | None | 256Mi | - |
| Loki Minio | 100m | 128Mi | 10Gi |
| Node Exporter (per node) | None | 32Mi | - |

**Shared Infrastructure (kamaji-system namespace):**

| Component | CPU Request | Memory Request | Storage |
|-----------|-------------|----------------|----------|
| Kamaji Controller | 100m | 20Mi | - |
| Kamaji etcd (3 replicas) | None | None | 30Gi (3x10Gi) |
| CAPI Kamaji Controller | 10m | 64Mi | - |

### Actual vs Theoretical Requirements

**Current Cluster Efficiency:**
- **7 tenant clusters** running on 16 cores, 128 GB RAM
- **Average per cluster:** ~0.9 cores, ~10 GB RAM (management overhead only)
- **Platform base:** ~4 cores, ~20 GB RAM
- **Remaining capacity:** ~6 cores, ~36 GB RAM for additional tenants

**Scaling Projection:**
- Current 2-node cluster can support ~10-15 tenant clusters before CPU/RAM constraints
- With 3-node architecture (24 cores, 192 GB): ~20-30 tenant clusters
- Adding 1 node per 10 tenant clusters is recommended for stability

**Resource Optimization Observed:**
- Resource requests not set on most components → cluster overcommit strategy
- Shared etcd reduces storage requirements (xxx uses banja's etcd)
- Control plane memory footprint lower than theoretical (0.5-1GB vs 1-2GB estimated)

### Storage Usage (Persistent Volumes)

**Management Cluster Storage:**
- Kamaji shared etcd: 30 GB (3x 10GB PVCs)
- Prometheus: 20 GB
- Loki/Minio: 10 GB
- Keycloak PostgreSQL: ~5 GB
- **Per tenant etcd (dedicated):** 10-30 GB (1-3 replicas)

**Total Storage in Use:** ~500-600 GB across all tenant etcd instances + monitoring

---

## CloudSigma Platform Capacity Planning

### Proposed Hardware Configuration

**Management Cluster Infrastructure:**

| VM Role | Nb VMs | CPU Cores | CPU (GHz) | RAM (GB) | Disk (GB) | Backup (GB) | Purpose |
|---------|--------|-----------|-----------|----------|-----------|-------------|----------|
| Master-1 | 1 | 8 | 16 | 32 | 200 | 300 | K8s control plane + tenant CPs |
| Master-2 | 1 | 8 | 16 | 32 | 200 | 300 | K8s control plane + tenant CPs |
| Master-3 | 1 | 8 | 16 | 32 | 200 | 300 | K8s control plane + tenant CPs |
| Worker-1 | 1 | 16 | 32 | 64 | 250 | 300 | Tenant CPs + monitoring |
| Worker-2 | 1 | 16 | 32 | 64 | 250 | 300 | Tenant CPs + monitoring |
| Management VM | 1 | 4 | 16 | 32 | 200 | 300 | Optional dedicated management |
| **TOTAL** | **6** | **60** | **128** | **256** | **1300** | **1800** | - |

**Configuration Notes:**
- All 5 nodes (3 masters + 2 workers) can host tenant control planes
- Management VM can be used for backups, monitoring, or additional capacity
- Total usable for tenant CPs: 52 cores, 224 GB RAM (after K8s overhead)

### Per-Tenant Cluster Resource Consumption (CloudSigma)

#### Components per Tenant Cluster:

**1. Tenant Control Plane Pod (on Management Cluster)**
- **kube-apiserver:** ~200-300Mi RAM, 50-100m CPU
- **kube-controller-manager:** ~150-200Mi RAM, 25-50m CPU
- **kube-scheduler:** ~50-100Mi RAM, 10-25m CPU
- **Total per TCP:** ~500Mi RAM, ~100m CPU

**2. CloudSigma Cloud Controller Manager (per cluster)**
- **csccm pod:** 64Mi RAM, 10m CPU (observed)
- **Total:** 64Mi RAM, 10m CPU

**3. etcd Datastore (50% 1-node / 50% 3-node)**

**1-node etcd (50% of clusters):**
- **etcd pod:** 512Mi RAM, 100m CPU, 10Gi storage
- **Total:** 512Mi RAM, 100m CPU, 10Gi

**3-node etcd (50% of clusters):**
- **etcd pods (3x):** 1536Mi RAM (512Mi×3), 300m CPU (100m×3), 30Gi storage (10Gi×3)
- **Total:** 1536Mi RAM, 300m CPU, 30Gi

#### Average per Cluster (Mixed etcd Topology)

| Component | CPU (cores) | RAM (GB) | Storage (GB) | Notes |
|-----------|-------------|----------|--------------|-------|
| Control Plane Pod | 0.1 | 0.5 | - | 3-in-1 pod |
| CloudSigma CCM | 0.01 | 0.06 | - | Per cluster |
| etcd (1-node) | 0.1 | 0.5 | 10 | 50% of clusters |
| etcd (3-node) | 0.3 | 1.5 | 30 | 50% of clusters |
| **Average** | **0.21** | **1.06** | **20** | Weighted avg |
| **Conservative** | **0.3** | **1.5** | **20** | With overhead |

### Capacity Calculation

#### Total Available Resources (after K8s overhead ~15%)

| Resource | Total | K8s Overhead (15%) | Available | Reserved for Platform (20%) | **Usable for Tenants** |
|----------|-------|-------------------|-----------|---------------------------|------------------------|
| CPU Cores | 60 | 9 | 51 | 10 | **41 cores** |
| RAM (GB) | 256 | 38 | 218 | 44 | **174 GB** |
| Storage (GB) | 1300 | - | 1300 | 100 | **1200 GB** |

#### Capacity by Constraint

**CPU Constraint:**
- Usable CPU: 41 cores
- Per cluster: 0.3 cores
- **Capacity: ~136 clusters**

**Memory Constraint:**
- Usable RAM: 174 GB
- Per cluster: 1.5 GB
- **Capacity: ~116 clusters**

**Storage Constraint:**
- Usable Storage: 1200 GB
- Per cluster: 20 GB (average)
- **Capacity: ~60 clusters**

**Bottleneck: Storage** (most restrictive)

### **Recommended Capacity: 50-60 Tenant Clusters**

#### Breakdown by etcd Topology (50/50 split)

| Cluster Type | Count | etcd Nodes | etcd Storage | TCP + CCM Resources |
|--------------|-------|------------|--------------|---------------------|
| **1-node etcd** | 25-30 | 25-30 | 250-300 GB | 25-30 × (0.11 cores, 0.56 GB) |
| **3-node etcd** | 25-30 | 75-90 | 750-900 GB | 25-30 × (0.11 cores, 0.56 GB) |
| **TOTAL** | **50-60** | **100-120** | **1000-1200 GB** | **5.5-6.6 cores, 28-34 GB** |

### Resource Distribution Across Nodes

**Assuming 60 tenant clusters evenly distributed:**

| Node | Role | TCP Pods | etcd Pods | CPU Used | RAM Used | Storage Used |
|------|------|----------|-----------|----------|----------|---------------|
| Master-1 | K8s CP + TCP host | 12 | 20 | ~6 cores | ~20 GB | ~200 GB |
| Master-2 | K8s CP + TCP host | 12 | 20 | ~6 cores | ~20 GB | ~200 GB |
| Master-3 | K8s CP + TCP host | 12 | 20 | ~6 cores | ~20 GB | ~200 GB |
| Worker-1 | TCP + Monitoring | 12 | 20 | ~7 cores | ~25 GB | ~250 GB |
| Worker-2 | TCP + Monitoring | 12 | 20 | ~7 cores | ~25 GB | ~250 GB |
| **TOTAL** | - | **60** | **100** | **32 cores** | **110 GB** | **1100 GB** |

### Scaling Strategies

#### To Increase Capacity Beyond 60 Clusters:

**Option 1: Add Storage**
- Expand disk on Worker-1 and Worker-2 to 500 GB each
- **New Capacity:** ~100 clusters (limited by CPU/RAM)

**Option 2: Add Worker Nodes**
- Add 2 more workers: 32 cores, 128 GB RAM, 500 GB disk each
- **New Capacity:** ~150 clusters

**Option 3: Use Shared etcd (Kamaji default)**
- 3-node shared etcd for all clusters instead of dedicated
- Saves: ~950 GB storage, ~20 cores, ~80 GB RAM
- **New Capacity:** 200+ clusters (limited by control plane density)

**Option 4: External etcd Storage**
- Move etcd storage to separate storage cluster or cloud object storage
- Management cluster only hosts control plane pods
- **New Capacity:** 300+ clusters (Kamaji scaling limit per management cluster)

### Kamaji Scaling Best Practices

**From Kamaji Official Documentation:**

1. **Control Plane Density:** 50-100 tenant control planes per management node recommended
2. **etcd Sizing:** 
   - Shared etcd: 3-node cluster can handle 100+ tenant clusters
   - Dedicated etcd: 1 or 3 nodes per cluster based on SLA requirements
3. **Resource Overcommit:** Kamaji leverages Kubernetes QoS, safe to run without strict resource limits
4. **High Availability:** Distribute TCP pods across nodes using pod anti-affinity

### Production Recommendations

**Conservative (50 clusters):**
- Current hardware configuration as-is
- 25 clusters with 1-node etcd (dev/test)
- 25 clusters with 3-node etcd (production)
- Leaves 20% headroom for growth

**Standard (60 clusters):**
- Current hardware configuration
- 30 clusters with 1-node etcd
- 30 clusters with 3-node etcd
- Uses ~85% of available storage

**Aggressive (100+ clusters):**
- Add storage expansion or additional worker nodes
- Use shared etcd for majority of clusters
- Reserve dedicated etcd for critical production clusters only

### Customer Infrastructure (Not Counted in Management Cluster)

**Per Tenant Cluster Workers (CloudSigma):**
- Worker VMs: Run on customer's CloudSigma account
- CSI Driver storage: Customer's CloudSigma disks
- Networking: Customer's CloudSigma IPs/VLANs

**Example Customer Workload:**
- 3 workers × (4 cores, 8 GB) = 12 cores, 24 GB on customer side
- 100 GB storage per worker via CSI = 300 GB on customer CloudSigma

**Management cluster only hosts control plane - customer workloads NOT included in above calculations.**

---

*Last Updated: January 2025*
*Document Version: 2.1 - Added CloudSigma capacity planning with Kamaji scaling guidelines*
