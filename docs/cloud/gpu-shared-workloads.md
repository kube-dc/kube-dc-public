# Shared GPU workloads

Shared GPU lets several container workloads run on one physical GPU at the same
time, each holding a fixed fraction of the device's memory and compute. It is
the right choice when a job needs GPU acceleration but not a whole card — model
inference, notebooks, small training runs, batch scoring. You run it the same
way you run any other workload: a Kubernetes manifest applied with `kubectl`.

Shared GPU is a **preview capability**. Your operator enables it per project and
grants a per-model entitlement (a GPU quota). If your project has no GPU quota,
it is not enabled for you yet — ask your operator.

For a stronger isolation boundary — one whole physical GPU bound to one guest —
use a [Dedicated GPU VM](gpu-vm-guests.md) instead. Shared GPU trades that
boundary for density.

## How sharing works

A Shared GPU product is a **fixed fraction** of a specific GPU model — for
example, an 8 GiB slice of an NVIDIA V100. You request the product; you do not
tune memory and compute freely.

- **The memory slice is enforced.** Inside the container, `nvidia-smi` reports
  the slice size, not the physical card. On the 8 GiB V100 product,
  `nvidia-smi --query-gpu=memory.total` returns `8192 MiB` even though the card
  has 32 GB.
- **The compute share is cooperative.** The platform steers each workload toward
  its share in steady state, but startup and some CUDA library paths can briefly
  exceed it. It is not guaranteed performance or a security boundary. If you need
  a hard boundary, use a [Dedicated GPU VM](gpu-vm-guests.md).
- **Quota is an entitlement, not a reservation.** Holding quota does not reserve
  a physical slice. When every compatible GPU is busy, a valid workload
  legitimately **queues** — its pod stays `Pending` until a slice frees up.
- **Each GPU model is metered independently**, so spending your V100 entitlement
  never consumes another model's headroom.

## Check your entitlement

Your GPU entitlement appears in your project quota as a line per product:

```bash
kubectl get resourcequota -n {project-namespace} -o yaml | grep deviceclass
# kube-dc-nvidia-v100-shared-8g.deviceclass.resource.k8s.io/devices: "1"
```

The number is how many concurrent slices of that product you may hold.

## Run a Shared GPU workload

A Shared GPU workload is two objects: a `ResourceClaimTemplate` describing the
fixed product, and a `Deployment` that references it. Copy the manifest below,
replace **`{workload-name}`** and the **container image**, and apply it. Keep
everything else exactly as shown — the platform validates the shape on admission
(see [The contract](#the-contract) below).

```yaml
apiVersion: resource.k8s.io/v1
kind: ResourceClaimTemplate
metadata:
  name: {workload-name}-gpu
  labels: &labels
    app.kubernetes.io/name: {workload-name}
    app.kubernetes.io/managed-by: kube-dc
    kube-dc.com/gpu-backend: dra
    kube-dc.com/gpu-profile: nvidia-v100-hami
  annotations: &annotations
    kube-dc.com/gpu-workload-hash: "0000000000000000000000000000000000000000000000000000000000000000"
    kube-dc.com/gpu-memory-mib: "8192"
    kube-dc.com/gpu-core-percent: "25"
spec:
  metadata:
    labels: *labels
    annotations: *annotations
  spec:
    devices:
      requests:
        - name: gpu
          exactly:
            deviceClassName: kube-dc-nvidia-v100-shared-8g
            allocationMode: ExactCount
            count: 1
            capacity:
              requests:
                memory: 8192Mi     # fixed by the product
                cores: "25"        # fixed by the product
---
apiVersion: apps/v1
kind: Deployment
metadata:
  name: {workload-name}
  labels: &labels
    app.kubernetes.io/name: {workload-name}
    app.kubernetes.io/managed-by: kube-dc
    kube-dc.com/gpu-backend: dra
    kube-dc.com/gpu-profile: nvidia-v100-hami
  annotations: &annotations
    kube-dc.com/gpu-workload-hash: "0000000000000000000000000000000000000000000000000000000000000000"
    kube-dc.com/gpu-memory-mib: "8192"
    kube-dc.com/gpu-core-percent: "25"
spec:
  replicas: 1
  strategy:
    type: Recreate               # release the slice before reacquiring one
  selector:
    matchLabels:
      app.kubernetes.io/name: {workload-name}
  template:
    metadata:
      labels: *labels
      annotations: *annotations
    spec:
      securityContext:
        runAsNonRoot: true
        runAsUser: 65532
        runAsGroup: 65532
        seccompProfile:
          type: RuntimeDefault
      containers:
        - name: workload
          image: {your-cuda-image}@sha256:{digest}   # digest-pinned, glibc-based
          securityContext:
            allowPrivilegeEscalation: false
            capabilities:
              drop: ["ALL"]
          resources:
            claims:
              - name: gpu
          command: ["/bin/sh", "-ec"]
          args: ["nvidia-smi -L; sleep 3600"]
      resourceClaims:
        - name: gpu
          resourceClaimTemplateName: {workload-name}-gpu
```

Apply it into your project:

```bash
kubectl apply -n {project-namespace} -f shared-gpu.yaml
```

> Each document defines its own `&labels` / `&annotations` and reuses them
> within that document — YAML anchors do not carry across the `---` separator, so
> the ResourceClaimTemplate and the Deployment each declare the labels once. If
> your editor strips YAML anchors, just write the same four labels and three
> annotations wherever `*labels` / `*annotations` appears.

## Verify

```bash
kubectl get deployment,pod -n {project-namespace} \
  -l app.kubernetes.io/name={workload-name}

# GPU allocated and quota consumed:
kubectl get resourceclaim -n {project-namespace} \
  -l app.kubernetes.io/name={workload-name}

# The GPU is visible inside the container (note the sliced memory):
kubectl logs -n {project-namespace} deploy/{workload-name}
# GPU 0: Tesla V100-PCIE-32GB (UUID: ...)
```

A workload may legitimately stay `Pending` while it waits for a free slice —
that is expected when your GPUs are busy. Confirm the GPU through your normal
CUDA framework (for example `torch.cuda.is_available()`). Do **not** build logic
around a GPU's UUID, PCI address, or the node name — a rescheduled workload can
land on different hardware.

## Release

Delete the workload to return the slice. Delete the whole set, not just the pod
— deleting only the pod lets the Deployment respawn it and keep the slice:

```bash
kubectl delete -n {project-namespace} -f shared-gpu.yaml
```

Watch the product's quota line return to its previous value.

## The contract

The platform validates every GPU workload on admission, so a manifest that
drifts from the fixed product is rejected. If `kubectl apply` returns a
`ValidatingAdmissionPolicy ... denied` error, check these:

- **Fixed capacity.** `memory` and `cores` must be exactly the product's
  published values (`8192Mi` / `25` for the 8 GiB V100). Requesting more is
  rejected — the fixed product *is* your entitlement.
- **One request named `gpu`**, `allocationMode: ExactCount`, `count: 1`, no
  selectors, no `adminAccess`.
- **Labels** `app.kubernetes.io/managed-by: kube-dc`, `kube-dc.com/gpu-backend:
  dra`, `kube-dc.com/gpu-profile`, and `app.kubernetes.io/name`, identical on the
  object, its `spec.metadata`, and the Deployment's pod template.
- **Name** of the `ResourceClaimTemplate` must be `{workload-name}-gpu`.
- **`strategy: Recreate`**, one container, and the container's
  `resources.claims[0].name` = `gpu` = the pod's `resourceClaims[0].name`.
- **No** `schedulerName`, `nodeSelector`, `nodeName`, node affinity, host
  namespaces, `hostPath`, or privileged containers — the platform schedules and
  isolates the workload for you.

You cannot exceed your entitlement: the fixed product is enforced for every
claim, and your project quota bounds how many slices you can hold at once.

## Limits and guarantees

- Shared GPU is a **preview** capability, enabled per project by your operator.
- The memory slice is a hard cap; the compute share is a cooperative target.
- Quota is an entitlement; workloads queue when GPUs are busy.
- For whole-device isolation or a workload that must not share silicon with
  another tenant, use a [Dedicated GPU VM](gpu-vm-guests.md).
