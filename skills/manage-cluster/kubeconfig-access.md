# Accessing Managed Cluster Kubeconfig

## Secret Pattern

When a KdcCluster reaches Ready state, Kube-DC creates a Kamaji kubeconfig secret:

| Secret Name | Data Key | Endpoint | Use |
|-------------|----------|----------|-----|
| `{cluster}-cp-admin-kubeconfig` | `admin.conf` | `https://{cluster}-cp-{ns}.kube-dc.cloud:443` | **External public URL** (recommended) |
| `{cluster}-cp-admin-kubeconfig` | `super-admin.conf` | `https://{internal-ip}:6443` | Internal VPC IP |
| `{cluster}-cp-admin-kubeconfig` | `super-admin.svc` | `https://{cluster}-cp.{ns}.svc:6443` | Internal Service (mgmt cluster only) |
| `{cluster}-cp-admin-kubeconfig` | `admin.svc` | `https://{cluster}-cp.{ns}.svc:6443` | Internal Service (admin, not super-admin) |

There is also a CAPI-generated secret:

| Secret Name | Data Key | Endpoint |
|-------------|----------|----------|
| `{cluster}-kubeconfig` | `value` | `https://{cluster}-cp-{ns}.kube-dc.cloud:443` |

## Extract Kubeconfig (External Public URL)

```bash
# Recommended: admin.conf has the external public endpoint
kubectl get secret {cluster}-cp-admin-kubeconfig -n {namespace} \
  -o jsonpath='{.data.admin\.conf}' | base64 -d > /tmp/{cluster}-kubeconfig
chmod 600 /tmp/{cluster}-kubeconfig
```

Alternative via CAPI secret:
```bash
kubectl get secret {cluster}-kubeconfig -n {namespace} \
  -o jsonpath='{.data.value}' | base64 -d > /tmp/{cluster}-kubeconfig
chmod 600 /tmp/{cluster}-kubeconfig
```

## Use Kubeconfig

```bash
# List nodes
kubectl --kubeconfig=/tmp/{cluster}-kubeconfig get nodes

# List all pods
kubectl --kubeconfig=/tmp/{cluster}-kubeconfig get pods -A

# Deploy to tenant cluster
kubectl --kubeconfig=/tmp/{cluster}-kubeconfig apply -f manifest.yaml
```

## Cluster API Endpoint

All clusters are exposed externally via TLS passthrough:

```
https://\{cluster\}-cp-\{namespace\}.kube-dc.cloud:443
```

## Security Notes

- `admin.conf` and `super-admin.conf` are **cluster-admin** credentials
- Never expose kubeconfig contents in chat output
- Write to temporary file with restricted permissions (`chmod 600`)
- Clean up after use: `rm /tmp/{cluster}-kubeconfig`
- The kubeconfig can also be downloaded via the Kube-DC console UI
- **Do NOT use `super-admin.svc`** unless running from within the management cluster
