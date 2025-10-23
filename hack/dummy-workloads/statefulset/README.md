# StatefulSet Examples

This directory contains example StatefulSet manifests for testing the StatefulSet view.

## Examples

### 1. Simple StatefulSet (`simple-statefulset.yaml`)
A minimal StatefulSet with a single replica running busybox.
- **Replicas**: 1
- **Image**: busybox:latest
- **No persistent storage**

```bash
kubectl apply -f simple-statefulset.yaml
```

### 2. Nginx StatefulSet (`nginx-statefulset.yaml`)
A StatefulSet running nginx with persistent storage.
- **Replicas**: 3
- **Image**: nginx:1.21-alpine
- **Storage**: 1Gi per pod

```bash
kubectl apply -f nginx-statefulset.yaml
```

### 3. Redis StatefulSet (`redis-statefulset.yaml`)
A StatefulSet running Redis with persistent data storage.
- **Replicas**: 2
- **Image**: redis:7-alpine
- **Storage**: 500Mi per pod

```bash
kubectl apply -f redis-statefulset.yaml
```

## Apply All Examples

```bash
kubectl apply -f .
```

## Clean Up

```bash
kubectl delete -f .
```

## Notes

- All StatefulSets include a headless service (clusterIP: None) which is required for StatefulSets
- StatefulSets with `volumeClaimTemplates` will create PersistentVolumeClaims automatically
- Pods are created in order (0, 1, 2, ...) and deleted in reverse order
- Each pod gets a stable network identity: `<statefulset-name>-<ordinal>`
