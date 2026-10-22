# Shared GPU application workflow

Use this workflow only when the user explicitly requests a Shared GPU workload.
The full user guide is [Shared GPU workloads](../../../docs/cloud/gpu-shared-workloads.md).

## Preconditions

1. Keep the generic CPU Deployment path for ordinary apps.
2. Check the project's GPU entitlement. Each Shared GPU product is a quota line:

   ```bash
   kubectl get resourcequota -n {project-backing-namespace} -o yaml | grep deviceclass
   # kube-dc-nvidia-v100-shared-8g.deviceclass.resource.k8s.io/devices: "1"
   ```

   The project's **Accelerators** card, or
   `GET /api/capabilities/{project-backing-namespace}/gpus` with the caller's
   Kube-DC bearer token, shows the same products with their fixed memory and
   compute values.
3. Continue only when the project has a quota line for the requested product
   with headroom. No quota line means the operator has not enabled Shared GPU
   for this project: tell the user to ask the operator.
4. Treat quota as entitlement, not reserved physical capacity. A valid workload
   can stay `Pending` while every compatible GPU is busy. Tell the user before
   applying.

## Apply

A Shared GPU workload is two objects: a `ResourceClaimTemplate` for the fixed
product and a `Deployment` that references it. Use the manifest from the user
guide unchanged except for `{app-name}`, the namespace and the image. Admission
policies reject any other change.

```yaml
apiVersion: resource.k8s.io/v1
kind: ResourceClaimTemplate
metadata:
  name: {app-name}-gpu
  labels: &labels
    app.kubernetes.io/name: {app-name}
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
  name: {app-name}
  labels: &labels
    app.kubernetes.io/name: {app-name}
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
      app.kubernetes.io/name: {app-name}
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
          image: "{glibc-cuda-image}@sha256:{64-lowercase-hex}"
          securityContext:
            allowPrivilegeEscalation: false
            capabilities:
              drop: ["ALL"]
          resources:
            claims:
              - name: gpu
      resourceClaims:
        - name: gpu
          resourceClaimTemplateName: {app-name}-gpu
```

For another product, take its device class, profile, memory and compute values
from the capability list; they are fixed per product.

```bash
kubectl apply -n {project-backing-namespace} -f shared-gpu.yaml
```

A `ValidatingAdmissionPolicy ... denied` error means the manifest drifted from
the product: compare it with the contract in the user guide.

## Guardrails

- Use a complete digest-pinned, glibc-based CUDA application image.
- Do not use Alpine/musl, Docker-in-Docker, BuildKit, Kaniko, a custom runtime
  class, host namespaces, hostPath, direct devices, node selection, `envFrom`,
  privileged containers, or `CUDA_DISABLE_CONTROL`.
- Do not set `schedulerName`, `nodeSelector`, `nodeName` or node affinity.
- Do not add native GPU resource names (`nvidia.com/...`) to a Deployment.
- Treat compute percentage as a cooperative steady-state target, not a hard
  security or performance boundary.
- Use a Dedicated GPU VM when the workload needs a whole-device isolation
  boundary.

## Verify and release

A workload can legitimately remain `Pending` while it waits for a free slice.
Otherwise:

```bash
kubectl get deployment,pod -n {project-backing-namespace} -l app.kubernetes.io/name={app-name}
kubectl get resourceclaim -n {project-backing-namespace} -l app.kubernetes.io/name={app-name}
kubectl logs -n {project-backing-namespace} deploy/{app-name}
```

Verify the application through its normal CUDA framework. Do not expose or rely
on GPU UUID, PCI address, or node name.

Release the slice by deleting both objects, not only the Pod (the Deployment
would recreate it and keep the slice):

```bash
kubectl delete -n {project-backing-namespace} -f shared-gpu.yaml
```

Then check that the product's quota line returned to its previous value.
