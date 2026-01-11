# Kube-DC Components Inventory

## Kube-DC Product Components

| Component | Description |
|-----------|-------------|
| **Kube-DC Manager (Controller)** | Core Kubernetes operator managing custom resources (Organizations, Projects, EIp, FIp, etc.), implementing multi-tenancy, networking policies, and resource lifecycle management |
| **UI Frontend** | React-based web console built with PatternFly UI framework, providing dashboard for managing organizations, projects, virtual machines, and monitoring |
| **UI Backend (API Server)** | Node.js/Express API server handling authentication, Kubernetes API proxying, metrics collection (Prometheus), VM management, and cloud shell access |
| **Authentication Service** | JWT-based authentication integration with Keycloak, managing user sessions, organization/project access tokens, and RBAC mapping |
| **Virtual Machine Management** | KubeVirt integration providing VM lifecycle management, live migration, snapshots, cloud-init configuration, and performance monitoring |
| **Networking Service** | Kube-OVN integration managing VPCs, subnets, external IPs (EIp/FIp), SNAT rules, load balancers, and multi-network connectivity |
| **Organization & Project Management** | Multi-tenant resource isolation, namespace provisioning, Keycloak realm/group synchronization, and hierarchical RBAC |
| **Monitoring & Metrics Service** | Prometheus integration for real-time and historical metrics collection, VM performance tracking, cluster observability |
| **Billing Integration** | Resource usage tracking, cost allocation per organization/project, usage reporting and quota management |
| **Kube-DC K8s Manager** | Kamaji-based multi-tenant Kubernetes control plane manager, enabling creation of isolated tenant Kubernetes clusters with dedicated/shared etcd |
| **Cluster API Provider CloudSigma** | Infrastructure provider for Cluster API enabling declarative Kubernetes cluster management on CloudSigma cloud platform |
| **CloudSigma Cloud Controller Manager (CCM)** | Kubernetes cloud-provider implementation for CloudSigma, providing node registration with metadata, LoadBalancer service support, and node lifecycle management |
| **CloudSigma CSI Driver** | Container Storage Interface driver enabling dynamic volume provisioning, attachment, expansion (offline), snapshots, and persistent storage management on CloudSigma infrastructure |
| **Kubernetes Image Builder** | Packer-based automation for building Ubuntu 24.04 images with pre-installed Kubernetes components (kubelet, kubeadm, kubectl, containerd) for CloudSigma worker nodes |

---

## Open Source Components

| Component | Open Source License | Open Source Link | Features/Purpose |
|-----------|-------------------|------------------|------------------|
| **Kubernetes** | Apache 2.0 | https://github.com/kubernetes/kubernetes | Container orchestration platform, base for all Kube-DC operations |
| **Kube-OVN** | Apache 2.0 | https://github.com/kubeovn/kube-ovn | Software-defined networking with VPC, subnet, VLAN support, SNAT/DNAT, load balancing |
| **Multus CNI** | Apache 2.0 | https://github.com/k8snetworkplumbingwg/multus-cni | Multi-network plugin enabling multiple network interfaces for pods |
| **KubeVirt** | Apache 2.0 | https://github.com/kubevirt/kubevirt | Virtual machine management on Kubernetes, enabling VM workloads alongside containers |
| **CDI (Containerized Data Importer)** | Apache 2.0 | https://github.com/kubevirt/containerized-data-importer | Persistent storage management for KubeVirt VMs, image import/upload, DataVolumes |
| **Cert-Manager** | Apache 2.0 | https://github.com/cert-manager/cert-manager | Automatic TLS certificate provisioning and management, Let's Encrypt integration |
| **Envoy Gateway** | Apache 2.0 | https://github.com/envoyproxy/gateway | Kubernetes Gateway API implementation for advanced ingress, routing, and TLS passthrough |
| **Keycloak** | Apache 2.0 | https://github.com/keycloak/keycloak | Identity and access management, SSO, OIDC/SAML provider for multi-tenant authentication |
| **Prometheus Operator** | Apache 2.0 | https://github.com/prometheus-operator/prometheus-operator | Kubernetes-native Prometheus deployment, ServiceMonitors, alerting rules management |
| **Grafana** | AGPL-3.0 | https://github.com/grafana/grafana | Observability dashboards, metrics visualization, alerting UI |
| **Grafana Loki** | AGPL-3.0 | https://github.com/grafana/loki | Log aggregation system optimized for Kubernetes, stores and queries logs |
| **Grafana Alloy** | Apache 2.0 | https://github.com/grafana/alloy | OpenTelemetry collector for metrics, logs, and traces, Kubernetes events collection |
| **Kamaji** | Apache 2.0 | https://github.com/clastix/kamaji | Multi-tenant Kubernetes control plane manager, runs tenant API servers as pods |
| **Kamaji-etcd** | Apache 2.0 | https://github.com/clastix/kamaji-etcd | Shared or dedicated etcd datastore management for Kamaji tenant clusters |
| **Cluster API** | Apache 2.0 | https://github.com/kubernetes-sigs/cluster-api | Declarative Kubernetes cluster lifecycle management, infrastructure abstraction |
| **CAPI Provider KubeVirt** | Apache 2.0 | https://github.com/kubernetes-sigs/cluster-api-provider-kubevirt | KubeVirt infrastructure provider for Cluster API, enables VM-based worker nodes |
| **CAPI Provider K3s** | Apache 2.0 | https://github.com/cluster-api-provider-k3s | K3s bootstrap and control plane provider for lightweight Kubernetes clusters |
| **Sveltos** | Apache 2.0 | https://github.com/projectsveltos/sveltos | Kubernetes addon controller for automated application deployment across clusters |
| **Kyverno** | Apache 2.0 | https://github.com/kyverno/kyverno | Kubernetes policy engine for security, compliance, and resource management policies |
| **Local Path Provisioner** | Apache 2.0 | https://github.com/rancher/local-path-provisioner | Dynamic local storage provisioner, default StorageClass for persistent volumes |
| **noVNC** | MPL 2.0 | https://github.com/novnc/noVNC | Browser-based VNC client for VM console access via WebSocket |
| **CloudSigma Cloud Controller Manager** | Apache 2.0 | https://github.com/kube-dc/cluster-api-provider-cloudsigma/tree/main/ccm | Cloud provider implementation for CloudSigma infrastructure, node initialization, LoadBalancer services |
| **CloudSigma CSI Driver** | Apache 2.0 | https://github.com/kube-dc/cluster-api-provider-cloudsigma/tree/main/csi | Container Storage Interface driver for CloudSigma persistent storage, dynamic volume provisioning, snapshots |
| **CloudSigma Go SDK** | Apache 2.0 | https://github.com/cloudsigma/cloudsigma-sdk-go | Official CloudSigma API client library for Go, used by CAPCS, CCM, and CSI |
| **Packer** | MPL 2.0 | https://github.com/hashicorp/packer | HashiCorp image builder tool for creating Kubernetes node images |

---

## Frontend Dependencies (UI Stack)

| Component | License | Purpose |
|-----------|---------|---------|
| **React** | MIT | UI framework for building interactive user interfaces |
| **PatternFly React** | MIT | Enterprise UI component library with accessible, responsive design |
| **React Router** | MIT | Client-side routing for single-page application navigation |
| **Victory Charts** | MIT | Data visualization library for performance and metrics charts |
| **Keycloak-js** | Apache 2.0 | JavaScript adapter for Keycloak authentication |
| **Webpack** | MIT | Module bundler for frontend assets and code optimization |
| **TypeScript** | Apache 2.0 | Type-safe JavaScript for improved developer experience |

---

## Backend Dependencies (API Stack)

| Component | License | Purpose |
|-----------|---------|---------|
| **Node.js + Express** | MIT | JavaScript runtime and web framework for REST API server |
| **@kubernetes/client-node** | Apache 2.0 | Official Kubernetes JavaScript client library |
| **@kubevirt-ui/kubevirt-api** | Apache 2.0 | KubeVirt API TypeScript definitions and utilities |
| **openid-client** | MIT | OpenID Connect relying party implementation for OIDC authentication |
| **jose** | MIT | JavaScript Object Signing and Encryption, JWT handling |
| **axios** | MIT | HTTP client for REST API requests to Kubernetes and external services |
| **express-ws** | BSD-2-Clause | WebSocket support for Express, enables VM console streaming |
| **http-proxy-middleware** | MIT | Proxy middleware for routing requests to Kubernetes API |
| **js-yaml** | MIT | YAML parser for Kubernetes manifests and configuration |
| **mustache** | MIT | Template engine for dynamic resource generation |
| **moment** | MIT | Date/time manipulation for metrics and timestamps |
| **swagger-jsdoc** | MIT | OpenAPI documentation generation from JSDoc comments |
| **swagger-ui-express** | MIT | Interactive API documentation UI |

---

## Version Information (as of latest installer)

| Component | Version |
|-----------|---------|
| Kube-OVN | v1.14.10 |
| Multus CNI | v4.1.0 |
| KubeVirt | v1.6.0 |
| CDI | v1.59.0 |
| Cert-Manager | v1.14.4 |
| Envoy Gateway | v1.2.6 |
| Keycloak | 24.3.0 |
| Prometheus Operator (kube-prometheus-stack) | 67.4.0 |
| Grafana Loki | 6.11.0 |
| Grafana Alloy | 0.10.1 |
| Kamaji | 1.0.0 |
| Kamaji-etcd | 0.14.0 |
| Cluster API | v1.8.1 |
| CAPI K3s Provider | v1.2.2 |
| Kyverno | v1.15.2 |
| Sveltos | v0.57.3 |
| Local Path Provisioner | v0.0.31 |

---

## Architecture Overview

### Component Interaction Flow

```
┌─────────────────────────────────────────────────────────────────────┐
│                         User Interface Layer                         │
│  ┌──────────────┐    ┌──────────────┐    ┌──────────────┐          │
│  │ Web Browser  │───▶│ UI Frontend  │───▶│  UI Backend  │          │
│  │  (React)     │    │ (PatternFly) │    │  (Node.js)   │          │
│  └──────────────┘    └──────────────┘    └───────┬──────┘          │
└────────────────────────────────────────────────────┼────────────────┘
                                                     │
┌────────────────────────────────────────────────────┼────────────────┐
│                    Authentication Layer            │                 │
│  ┌──────────────┐    ┌──────────────┐             │                 │
│  │  Keycloak    │◀───│ Auth Service │◀────────────┘                 │
│  │   (OIDC)     │    │   (JWT)      │                               │
│  └──────────────┘    └──────────────┘                               │
└──────────────────────────────────────────────────────────────────────┘
                                │
┌───────────────────────────────┼──────────────────────────────────────┐
│              Kubernetes API Layer (Management Cluster)               │
│  ┌──────────────┐    ┌──────────────┐    ┌──────────────┐          │
│  │   Kube-DC    │    │    Kamaji    │    │ Cluster API  │          │
│  │  Controller  │    │  Controller  │    │ Controllers  │          │
│  └───────┬──────┘    └───────┬──────┘    └───────┬──────┘          │
└──────────┼───────────────────┼───────────────────┼──────────────────┘
           │                   │                   │
┌──────────┼───────────────────┼───────────────────┼──────────────────┐
│          │   Infrastructure & Platform Layer     │                  │
│  ┌───────▼──────┐    ┌──────▼─────┐    ┌────────▼───────┐          │
│  │  Kube-OVN    │    │  KubeVirt  │    │   Prometheus   │          │
│  │ (Networking) │    │   (VMs)    │    │  (Monitoring)  │          │
│  └──────────────┘    └────────────┘    └────────────────┘          │
└──────────────────────────────────────────────────────────────────────┘
```

### CloudSigma Integration Components

The cluster-api-provider-cloudsigma repository includes three key components for CloudSigma cloud integration:

#### 1. CloudSigma Cloud Controller Manager (CCM)
**Location**: `/ccm`

**Purpose**: Implements the Kubernetes cloud-provider interface for CloudSigma infrastructure

**Features**:
- **Node Controller**: Automatic node registration with CloudSigma metadata, providerID assignment, topology labels
- **Service Controller**: LoadBalancer service type support with CloudSigma load balancer API integration
- **Route Controller**: Optional pod network route management (typically disabled with CNI)

**Deployment**: DaemonSet on control plane nodes with external cloud provider configuration

**Key Capabilities**:
- Initializes nodes with `providerID: cloudsigma://server-uuid`
- Adds topology labels: `topology.kubernetes.io/region`, `node.kubernetes.io/instance-type`
- Creates CloudSigma load balancers for `type: LoadBalancer` services
- Updates service status with external IPs

#### 2. CloudSigma CSI Driver
**Location**: `/csi`

**Purpose**: Container Storage Interface driver for persistent volume management on CloudSigma

**Features**:
- **Dynamic Volume Provisioning**: Automatically create CloudSigma drives for PersistentVolumeClaims
- **Volume Attachment**: Hot-plug/unplug volumes to running nodes with detachment verification
- **Volume Expansion**: Offline resize support (CloudSigma platform limitation)
- **Volume Snapshots**: Create and restore volume snapshots via CloudSigma API
- **Battle-proof Device Discovery**: Stable `/dev/disk/by-path/` detection with mutex serialization
- **Storage Classes**: Support for DSSD (Distributed SSD) storage type

**Architecture**:
- **Controller Plugin**: Deployment handling volume lifecycle operations (create, delete, attach, detach, expand, snapshot)
- **Node Plugin**: DaemonSet on each node for volume staging, publishing, and filesystem operations

**Performance**: Sequential R/W 200-500 MB/s, Random IOPS 5k-15k (varies by VM size)

**Current Version**: v1.2.7 with offline expansion and detachment verification

#### 3. Kubernetes Image Builder
**Location**: `/images/ubuntu-k8s`

**Purpose**: Packer-based automation for building Kubernetes-ready Ubuntu images for CloudSigma

**Build Methods**:
- **CloudSigma Build** (Recommended): Builds directly on CloudSigma infrastructure, no upload required
- **Local QEMU Build**: Builds locally, requires manual upload via CloudSigma Web UI

**What's Included**:
- Ubuntu 24.04 LTS base image
- Containerd container runtime
- Kubernetes components: kubelet, kubeadm, kubectl (version configurable)
- CNI plugins pre-installed
- Kernel modules and sysctl configuration
- Cloud-init configured for CAPI bootstrap
- Common Kubernetes images pre-pulled for faster node startup

**Build Configuration**:
```bash
# Build on CloudSigma (recommended)
make build-on-cloudsigma K8S_VERSION=1.34.1

# Build locally with QEMU
make build K8S_VERSION=1.34.1 UBUNTU_VERSION=24.04
```

**Provisioning Scripts**:
1. Base packages installation
2. Containerd installation and configuration
3. Kubernetes component installation
4. System configuration (kernel modules, sysctl)
5. Cleanup and image preparation

---

### Multi-Repository Structure

1. **kube-dc** (Main Repository)
   - Core controller managing Organizations, Projects, EIp, FIp
   - UI Frontend and Backend
   - Multi-tenancy and networking logic
   - Installation templates (cdev/helm)

2. **kube-dc-k8-manager** (Tenant Cluster Manager)
   - Manages tenant Kubernetes control planes via Kamaji
   - Cluster API integration for worker node provisioning
   - Datastore management (shared/dedicated etcd)
   - CloudSigma and KubeVirt infrastructure support

3. **cluster-api-provider-cloudsigma** (Cloud Provider)
   - CAPI infrastructure provider for CloudSigma
   - CloudSigma SDK integration
   - Worker node provisioning on CloudSigma platform
   - **CCM** (`/ccm`): Cloud Controller Manager for node initialization and LoadBalancer services
   - **CSI** (`/csi`): Container Storage Interface driver for persistent volume management
   - **Image Builder** (`/images/ubuntu-k8s`): Packer-based Kubernetes node image automation

---

## Integration Points

### External Systems Integration

| System | Integration Method | Purpose |
|--------|-------------------|----------|
| **Keycloak** | OIDC/OAuth2 | User authentication, organization realms, group management |
| **Kubernetes API** | client-go, @kubernetes/client-node | Resource management, RBAC, CRD operations |
| **Prometheus** | HTTP API | Metrics collection, VM performance data, alerting |
| **CloudSigma API** | Go SDK | Infrastructure provisioning for CAPI clusters |
| **OVN Database** | ovn-nbctl/ovs-vsctl | Network configuration, VPC management, routing |

### Component Communication

- **UI Frontend ↔ UI Backend**: REST API (HTTP/HTTPS), WebSocket (VM console)
- **UI Backend ↔ Kubernetes API**: Kubernetes API client, token-based auth
- **Kube-DC Controller ↔ Kube-OVN**: Custom Resource updates, OVN database queries
- **Kamaji ↔ Tenant Clusters**: TCP/6443 (Kubernetes API), etcd connection
- **CAPI ↔ Infrastructure Providers**: Infrastructure machine creation, health checks

---

## Deployment Architecture

### Installation Method
- **Primary**: cdev (Cloud Development Environment) with Helm charts
- **Templates**: YAML-based templating with variable substitution
- **Namespace Organization**: 
  - `kube-system`: Kube-OVN, Multus
  - `kube-dc`: Main controller, UI
  - `kubevirt`: VM management
  - `monitoring`: Prometheus, Grafana, Loki
  - `keycloak`: Identity management
  - `cert-manager`: Certificate automation
  - `kamaji-system`: Tenant control plane manager
  - `projectsveltos`: Addon controller
  - Organization namespaces: `<org-name>`
  - Project namespaces: `<org-name>-<project-name>`

### High Availability Considerations
- Multi-replica controller deployments
- Kamaji etcd cluster (3+ replicas)
- Kube-OVN OVN database HA
- LoadBalancer services for critical components
- StatefulSet for stateful components (etcd, Loki)

---

*Last Updated: January 2025*
*Document Version: 1.0*
