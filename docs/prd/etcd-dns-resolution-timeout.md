# Etcd DNS Resolution Timeout Issue in Kamaji TenantControlPlanes

## Problem

New clusters created via cs-marketplace-partner-kubedc experience control plane CrashLoopBackOff with error:

```
grpc: addrConn.createTransport failed to connect ... dial tcp: lookup {etcd-endpoint}.svc.cluster.local: operation was canceled
```

## Root Cause

- kube-apiserver's gRPC client creates 100+ parallel connections to etcd during startup
- Each connection triggers a DNS lookup for the etcd-lb-ext service
- DNS lookups timeout before the gRPC dial timeout (typically 20s default)
- Results in "operation was canceled" errors

## Diagnosis Steps

1. Check apiserver logs:
   ```bash
   kubectl logs -n {namespace} -l kamaji.clastix.io/name={tcp-name} -c kube-apiserver | grep -i "lookup\|canceled"
   ```

2. Verify DNS works from test pod:
   ```bash
   kubectl run test --image=busybox -n {namespace} --rm -it -- nslookup {etcd-endpoint}
   ```

3. Verify TCP connectivity:
   ```bash
   kubectl run test --image=busybox -n {namespace} --rm -it -- nc -zv {etcd-ip} {port}
   ```

## Resolution

Usually self-resolves after several pod restarts as DNS cache warms up. To expedite:

```bash
kubectl delete pod -n {namespace} -l kamaji.clastix.io/name={tcp-name}
```

## Important Notes

- **Do NOT use IP address directly in DataStore endpoint** - TLS certificates are issued for DNS names
- The etcd-lb-ext service is a headless service pointing to LoadBalancer IP
- Port 32380+ are used for dedicated datastores (auto-allocated)

## Potential Improvements

1. Add startup probe with longer failureThreshold in TenantControlPlane
2. Consider pre-warming DNS cache before control plane creation
3. Investigate Kamaji options for etcd client dial timeout configuration

## Related Components

- `kube-dc-k8-manager`: Creates KdcClusterDatastore and Kamaji DataStore resources
- `cs-marketplace-partner-kubedc`: Backend that triggers cluster creation with dedicated datastores
- Kamaji: Manages TenantControlPlane pods that connect to etcd
