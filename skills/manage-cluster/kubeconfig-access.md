# Access a Managed Cluster Kubeconfig

A Project kubeconfig manages platform resources in one Project. A Managed
Cluster kubeconfig targets the separate Kubernetes API created by a
`KdcCluster`. Do not confuse the two.

## External Workstation Access

When external API exposure is enabled, Kube-DC creates:

| Secret | Data key |
|---|---|
| `{cluster}-cp-admin-kubeconfig-external` | `admin.conf` |

```bash
umask 077
kubectl get secret {cluster}-cp-admin-kubeconfig-external \
  -n {backing-namespace} \
  -o jsonpath='{.data.admin\.conf}' | base64 -d > /tmp/{cluster}-kubeconfig
chmod 600 /tmp/{cluster}-kubeconfig

kubectl --kubeconfig=/tmp/{cluster}-kubeconfig get nodes
kubectl --kubeconfig=/tmp/{cluster}-kubeconfig get pods -A
```

The cluster detail view's **Kubeconfig** action downloads the same external
configuration.

## If the External Secret Is Missing

The Managed Cluster API is private. Enable external API exposure in the
supported Kube-DC workflow or connect through an operator-approved private
network path. Do not:

- extract `admin.conf` from `{cluster}-cp-admin-kubeconfig` and assume it is
  externally reachable;
- replace the server URL by hand;
- use the Cluster API `{cluster}-kubeconfig` Secret as an external contract.

Those internal Secrets and endpoints are for platform controllers and
management-network access.

## Security

- Treat the kubeconfig as a privileged credential.
- Never paste it into chat, logs, tickets, or source control.
- Store temporary files with mode `0600`.
- Remove the file when the task is complete.
- If access should be revoked, follow the platform's Managed Cluster access
  procedure; deleting a local copy is not credential revocation.
