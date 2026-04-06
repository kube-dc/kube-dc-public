# Accessing Managed Cluster Kubeconfig

## Secret Pattern

When a KdcCluster reaches Ready state, Kube-DC creates:

| Secret Name | Data Key | Content |
|-------------|----------|---------|
| `{cluster}-cp-admin-kubeconfig` | `super-admin.svc` | Full admin kubeconfig |

## Extract Kubeconfig

```bash
kubectl get secret {cluster}-cp-admin-kubeconfig -n {namespace} \
  -o jsonpath='{.data.super-admin\.svc}' | base64 -d > /tmp/{cluster}-kubeconfig
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

External access (if `k8s.kube-dc.com/expose-route: "true"` is set):

```
https://{cluster}-cp-{namespace}.kube-dc.cloud:443
```

## Security Notes

- This is a **super-admin** credential with full cluster-admin access
- Never expose kubeconfig contents in chat output
- Write to temporary file with restricted permissions (`chmod 600`)
- Clean up after use: `rm /tmp/{cluster}-kubeconfig`
- The kubeconfig can also be downloaded via the Kube-DC console UI
