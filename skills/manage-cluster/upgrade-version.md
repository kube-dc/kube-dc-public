# Upgrading Kubernetes Version

## Constraints

1. **Sequential minor versions only** — v1.34 → v1.35 is OK, v1.34 → v1.36 is NOT
2. **No downgrades** — once upgraded, you cannot go back
3. **Worker image must match version** — the container disk image contains matching kubelet/kubeadm
4. **Rolling update** — new worker is created and becomes Ready before old one is removed

## Procedure

### 1. Check Current Version

```bash
kubectl get kdccluster {cluster} -n {namespace} -o jsonpath='{.spec.version}'
```

### 2. Determine Target Version

Only +1 minor version from current. If current is `v1.34.x`, target must be `v1.35.x`.

### 3. Apply Upgrade Patch

Use `--type=json` to update version + worker image in a single patch:

```bash
kubectl patch kdccluster {cluster} -n {namespace} --type=json -p '[
  {"op":"replace","path":"/spec/version","value":"{target-version}"},
  {"op":"replace","path":"/spec/workers/0/image","value":"docker.io/shalb/ubuntu-2404-container-disk:{image-tag}"}
]'
```

For multiple worker pools, patch each pool's image:

```bash
kubectl patch kdccluster {cluster} -n {namespace} --type=json -p '[
  {"op":"replace","path":"/spec/version","value":"{target-version}"},
  {"op":"replace","path":"/spec/workers/0/image","value":"docker.io/shalb/ubuntu-2404-container-disk:{image-tag}"},
  {"op":"replace","path":"/spec/workers/1/image","value":"docker.io/shalb/ubuntu-2404-container-disk:{image-tag}"}
]'
```

### 4. Monitor Upgrade

```bash
kubectl get kdccluster {cluster} -n {namespace} -w
# Wait for phase to return to Ready
```

## Image Naming Convention

Image tag format: `v{kubelet-version}` — e.g., `v1.35.2` for Kubernetes v1.35.0+

```
docker.io/shalb/ubuntu-2404-container-disk:v1.35.2
```
