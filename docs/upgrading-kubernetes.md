# Upgrading Tenant Clusters to a New Kubernetes Version

This guide covers upgrading Kubernetes versions for tenant clusters managed by kube-dc.
It applies to all infrastructure providers (KubeVirt, CloudSigma, etc.).

## How Upgrades Work

```
KdcCluster.spec.version: "v1.35.0"
KdcCluster.spec.workers[].image: "shalb/ubuntu-2404-container-disk:v1.35.2"  (KubeVirt)
        │
        ▼
kube-dc-k8-manager controller
  ├── Updates KamajiControlPlane.spec.version
  ├── Recreates infrastructure MachineTemplate (immutable in CAPI)
  └── Updates MachineDeployment.spec.template.spec.version
        │
        ▼
Kamaji controller
  ├── Validates: no downgrades, sequential minor upgrades only
  ├── Runs kubeadm upgrade on control plane pods
  └── Updates kubelet-config ConfigMap in tenant cluster
        │
        ▼
CAPI MachineDeployment rolling update (maxSurge=1, maxUnavailable=0)
  ├── Creates new worker machine with new image + version
  ├── Waits for new node to become Ready
  └── Drains and deletes old worker machine
```

A single `kubectl patch` on the KdcCluster triggers the entire upgrade automatically.

## Prerequisites

### 1. Worker Node Image

Each Kubernetes version requires a matching worker node image with the correct
kubelet/kubeadm/kubectl binaries pre-installed.

**KubeVirt** — container-disk images:

| K8s Version | Image | Components |
|-------------|-------|------------|
| v1.34.x | `quay.io/capk/ubuntu-2404-container-disk:v1.34.1` | kubelet 1.34.1, containerd 1.7.x |
| v1.35.x | `docker.io/shalb/ubuntu-2404-container-disk:v1.35.2` | kubelet 1.35.2, containerd 2.2.2 |

To build a new container-disk image, see `images/kubevirt-container-disk/`.

**CloudSigma** — VM images built with Packer (see `cluster-api-provider-cloudsigma/images/ubuntu-k8s/`).

### 2. Kamaji Controller

The Kamaji controller must support the target Kubernetes version. Check:
```bash
# The KubeadmVersion constant defines the max supported version
kubectl get pods -n kamaji-system -o jsonpath='{.items[*].spec.containers[*].image}'
```

Current deployed image `shalb/kamaji:edge-26.02.11-v3-kube-dc` supports up to v1.35.0.

### 3. kube-dc-k8-manager Controller

The controller must include upgrade propagation fixes (version/image change detection).
Minimum version: the build that includes fixes to `kdccluster_capi.go`, `kdccluster_workers.go`,
`kdccluster_kubevirt.go`, and `kdccluster_cloudsigma.go`.

```bash
kubectl get deployment -n kube-dc kube-dc-k8-manager \
  -o jsonpath='{.spec.template.spec.containers[0].image}'
```

## Upgrade Procedure

### Step 1: Verify Current State

```bash
# List all tenant clusters
kubectl get kdccluster -A

# Check specific cluster
kubectl get kdccluster <name> -n <namespace> -o yaml
```

### Step 2: Upgrade (Single Patch)

Patch **both** `spec.version` and the worker image in a single operation:

**KubeVirt clusters:**
```bash
kubectl patch kdccluster <name> -n <namespace> --type=json -p '[
  {"op":"replace","path":"/spec/version","value":"v1.35.0"},
  {"op":"replace","path":"/spec/workers/0/image","value":"docker.io/shalb/ubuntu-2404-container-disk:v1.35.2"}
]'
```

**CloudSigma clusters:**
```bash
kubectl patch kdccluster <name> -n <namespace> --type=json -p '[
  {"op":"replace","path":"/spec/version","value":"v1.35.0"},
  {"op":"replace","path":"/spec/workers/0/cloudsigma/imageUUID","value":"<NEW-IMAGE-UUID>"}
]'
```

The controller automatically:
1. Updates **KamajiControlPlane** `spec.version` → control plane upgrade
2. Updates **MachineDeployment** `spec.template.spec.version` → worker rollout
3. Recreates **MachineTemplate** with new image (templates are immutable in CAPI)

### Step 3: Monitor

```bash
# Controller logs
kubectl logs -n kube-dc deployment/kube-dc-k8-manager --since=60s -f | \
  grep -E "Updating.*version|image changed|Recreated"

# Control plane status
kubectl get tenantcontrolplane -n <namespace> -w

# Worker machine rollout (maxSurge=1: new node created before old is drained)
kubectl get machines -n <namespace> -l cluster.x-k8s.io/cluster-name=<name> -w
```

Upgrade sequence:
1. **Control plane** (~2-5 min): Kamaji updates kube-apiserver/scheduler/controller-manager
2. **kubelet-config**: Updated ConfigMap pushed to tenant cluster
3. **Worker rollout** (~3-10 min per node): New machine created → Ready → old machine drained → deleted

### Step 4: Verify

```bash
# Get tenant kubeconfig
kubectl get secret <name>-cp-admin-kubeconfig -n <namespace> \
  -o jsonpath='{.data.admin\.conf}' | base64 -d > /tmp/<name>-kubeconfig.yaml

# Check nodes
KUBECONFIG=/tmp/<name>-kubeconfig.yaml kubectl get nodes -o wide
# Should show new K8s version and containerd version

# Check control plane
kubectl get kamajicontrolplane <name>-cp -n <namespace> -o jsonpath='{.spec.version}'
```

## Building Worker Node Images

### KubeVirt Container-Disk Images

Located at `images/kubevirt-container-disk/`.

```bash
cd images/kubevirt-container-disk

# Build (requires QEMU + KVM)
K8S_VERSION=1.35.2 ./build.sh

# Or use make
make build K8S_VERSION=1.35.2

# Push to registry
make push
```

The build process:
1. Downloads Ubuntu 24.04 minimal cloud image
2. Boots VM with QEMU + KVM
3. Installs via cloud-init: containerd 2.x (Docker repo), kubelet/kubeadm/kubectl, qemu-guest-agent
4. Shuts down VM, compacts qcow2 disk
5. Packages as container image (`FROM scratch` + qcow2 at `/disk/`)

**Prerequisites**: `qemu-system-x86_64`, `qemu-img`, `cloud-localds` (`apt install cloud-image-utils`), Docker

### Updating the Default Image in kube-dc-k8-manager

After building a new image, update the default in `kdccluster_kubevirt.go`:
```go
image := "docker.io/shalb/ubuntu-2404-container-disk:v1.35.2"
```

And the CRD field description in `api/v1alpha1/kdccluster_types.go`:
```go
// Default for KubeVirt: docker.io/shalb/ubuntu-2404-container-disk:v1.35.2
Image string `json:"image,omitempty"`
```

## Rollback

Kamaji **does not support version downgrades**. The webhook rejects any version decrease.

If an upgrade fails:
1. Check Kamaji logs: `kubectl logs -n kamaji-system deployment/kamaji`
2. Check TenantControlPlane status: `kubectl get tcp -n <namespace> -o yaml`
3. Pause worker rollout if needed: `kubectl annotate machinedeployment <name> cluster.x-k8s.io/paused="true"`
4. If control plane is stuck in "Upgrading", Kamaji will keep retrying automatically

## Kubernetes v1.35 Specific Notes

| Change | Impact |
|--------|--------|
| **cgroup v1 removed** | Worker images MUST have cgroup v2 (Ubuntu 22.04+ has it by default) |
| **containerd v1.x last support** | v1.35 is the last version supporting containerd 1.x; upgrade to 2.x before v1.36 |
| **IPVS mode deprecated** in kube-proxy | Emits warnings; consider migrating to nftables |

## Controller Fixes for Upgrade Support

The following fixes were added to kube-dc-k8-manager to support version upgrades:

| File | Fix |
|------|-----|
| `kdccluster_capi.go` | **KamajiControlPlane version update**: detects version drift and updates KCP |
| `kdccluster_workers.go` | **MachineDeployment version update**: detects version change and updates MD |
| `kdccluster_workers.go` | **Rolling update strategy**: `maxSurge=1, maxUnavailable=0` for safe rollouts |
| `kdccluster_kubevirt.go` | **KubevirtMachineTemplate recreation**: detects image change, deletes + recreates |
| `kdccluster_cloudsigma.go` | **CloudSigmaMachineTemplate recreation**: detects imageUUID change, deletes + recreates |
