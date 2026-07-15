# Shared GPU application workflow

Use this workflow only when the user explicitly requests a Shared GPU workload.

## Preconditions

1. Keep the generic CPU Deployment path for ordinary apps.
2. Open the project's **Accelerators** card or call
   `GET /api/capabilities/{project-namespace}/gpus` with the caller's Kube-DC
   bearer token.
3. Continue only when:
   - `creationEnabled` is `true`;
   - a Shared GPU profile has `canCreate: true`;
   - the project has headroom in shares, GPU memory, and GPU compute.
4. Treat quota as entitlement, not reserved physical capacity. A valid request
   can queue when every compatible GPU is busy.

Do not add native GPU resource names to the generic Deployment and do not apply
a handcrafted GPU manifest with `kubectl`. That would bypass the independent
product creation flag and skip the platform preview. Use the gated backend
workflow below; it builds the policy-valid Deployment and creates it with the
caller's Kubernetes identity.

## Preview, then create

Submit this JSON shape to the preview endpoint:

```http
POST /api/capabilities/{project-namespace}/gpus/shared-workloads/preview
Authorization: Bearer {kube-dc-jwt}
Content-Type: application/json
```

```json
{
  "profileId": "nvidia-v100-hami",
  "name": "{app-name}",
  "image": "{glibc-cuda-image}@sha256:{64-lowercase-hex}",
  "replicas": 1,
  "memoryMiB": 8192,
  "corePercent": 25,
  "allowQueue": true
}
```

The preview returns canonical YAML, aggregate quota consumption, warnings, and
`willQueue`. Show those values to the user. Do not infer memory or compute from
shares. Require explicit confirmation before queueing.

After confirmation, send the same payload to:

```http
POST /api/capabilities/{project-namespace}/gpus/shared-workloads
```

Set `allowQueue: true` only when the user accepted queueing. The backend
rechecks authorization, catalog health, all quota dimensions, capacity, and the
creation flag immediately before it creates the Deployment.

## Guardrails

- Use a complete digest-pinned, glibc-based CUDA application image.
- Do not use Alpine/musl, Docker-in-Docker, BuildKit, Kaniko, a custom runtime
  class, host namespaces, hostPath, direct devices, node selection, `envFrom`,
  privileged containers, or `CUDA_DISABLE_CONTROL`.
- Do not set `schedulerName`; the platform's mutating admission path injects
  the approved scheduler.
- Treat compute percentage as a cooperative steady-state target, not a hard
  security or performance boundary.
- Use a Dedicated GPU VM when the workload needs a whole-device isolation
  boundary.

## Verify and release

A workload can legitimately remain Pending when `willQueue` is true. Otherwise:

```bash
kubectl get deployment,pod -n {project-namespace} -l app.kubernetes.io/name={app-name}
kubectl describe pod -n {project-namespace} -l app.kubernetes.io/name={app-name}
```

Verify the application through its normal CUDA framework. Do not expose or rely
on GPU UUID, PCI address, or node name.

Release the holder through the gated API so the Deployment is removed rather
than deleting only its current Pod:

```http
DELETE /api/capabilities/{project-namespace}/gpus/shared-workloads/{app-name}
Authorization: Bearer {kube-dc-jwt}
```

Wait for all three quota dimensions to return to their expected values.
