---
name: deploy-app
description: Deploy a containerized application to a Kube-DC Project with optional persistent storage, a managed database, external exposure, or gated Shared GPU acceleration.
---

# Deploy an Application

Deploy ordinary Kubernetes workloads into a Project's backing namespace. Keep
the Project context, quota, storage, database, and exposure decisions explicit.

## Prerequisites

- The Project reports `status.ready: true`.
- `kube-dc use {domain}/{organization}/{project}` selects the intended Project.
- CPU, memory, pod, and storage quota have enough headroom.
- The caller can create the required workload kinds.
- For HTTPS, the Project already has the cert-manager `Issuer` described by the
  `expose-service` skill.

Confirm the active backing namespace:

```bash
kubectl config current-context
kubectl config view --minify -o jsonpath='{.contexts[0].context.namespace}{"\n"}'
kubectl auth can-i create deployments.apps
kubectl auth can-i create services
```

## 1. Create the workload

Pin production images by digest and set requests and limits:

```yaml
apiVersion: apps/v1
kind: Deployment
metadata:
  name: "{app}"
  namespace: "{project-backing-namespace}"
spec:
  replicas: 2
  selector:
    matchLabels:
      app.kubernetes.io/name: "{app}"
  template:
    metadata:
      labels:
        app.kubernetes.io/name: "{app}"
    spec:
      containers:
        - name: "{app}"
          image: "{registry}/{image}@sha256:{digest}"
          ports:
            - name: http
              containerPort: 8080
          resources:
            requests:
              cpu: 100m
              memory: 128Mi
            limits:
              cpu: 500m
              memory: 512Mi
          readinessProbe:
            httpGet:
              path: /health
              port: http
            initialDelaySeconds: 5
            periodSeconds: 10
```

Adapt the health path to the application. Do not add a probe that the image
does not implement.

When persistent data is required, create a PVC with a live Project
StorageClass and mount it. Follow `manage-storage`; do not assume a provider
class name or ReadWriteMany support.

## 2. Add a managed database when needed

Use `create-database` to provision the engine the application actually
supports. Read credentials through the documented Secret instead of putting a
password in the Deployment:

```yaml
env:
  - name: DB_HOST
    valueFrom: { secretKeyRef: { name: "{binding-secret}", key: host } }
  - name: DB_PORT
    valueFrom: { secretKeyRef: { name: "{binding-secret}", key: port } }
  - name: DB_NAME
    valueFrom: { secretKeyRef: { name: "{binding-secret}", key: dbname } }
  - name: DB_USER
    valueFrom: { secretKeyRef: { name: "{binding-secret}", key: username } }
  - name: DB_PASSWORD
    valueFrom:
      secretKeyRef:
        name: "{binding-secret}"
        key: password
```

That example is PostgreSQL. For MySQL and MariaDB, use the `database` key for
`DB_NAME`. Mount the binding's `ca.crt` and configure the client to verify TLS,
as shown in [connection patterns](../create-database/db-connection-patterns.md).
The binding Secret follows credential rotation. Restart Pods that read
credentials from environment variables after rotation.

Prefer a Helm chart option such as `existingSecretPasswordKey` when its expected
key differs. A manually copied bridge Secret does not follow password rotation
unless separate automation updates it.

## 3. Create an internal Service

```yaml
apiVersion: v1
kind: Service
metadata:
  name: "{app}"
  namespace: "{project-backing-namespace}"
spec:
  selector:
    app.kubernetes.io/name: "{app}"
  ports:
    - name: http
      port: 80
      targetPort: http
```

Use `ClusterIP` when only in-Project callers need the application.

## 4. Expose the Service only when requested

Follow `expose-service` for the full contract:

- HTTP/HTTPS: annotate a `LoadBalancer` Service with
  `service.nlb.kube-dc.com/expose-route`.
- Arbitrary TCP/UDP: allocate or select an EIP and bind a `LoadBalancer`
  Service.
- TLS passthrough: the application owns its certificate.
- Do not create a `GRPCRoute`; Kube-DC currently reconciles HTTPRoute and
  TLSRoute.

A minimal HTTPS Service is:

```yaml
apiVersion: v1
kind: Service
metadata:
  name: "{app}"
  namespace: "{project-backing-namespace}"
  annotations:
    service.nlb.kube-dc.com/expose-route: "https"
spec:
  type: LoadBalancer
  selector:
    app.kubernetes.io/name: "{app}"
  ports:
    - name: http
      port: 80
      targetPort: http
```

Read the assigned hostname from status annotation. The provider domain is live
configuration, not a value to construct in client code:

```bash
kubectl get service {app} \
  -o jsonpath='{.metadata.annotations.service\.nlb\.kube-dc\.com/route-hostname-status}{"\n"}'
```

## Helm applications

Review rendered manifests before installing:

```bash
helm template {release} {chart} --namespace {project-backing-namespace} \
  --values values.yaml
helm upgrade --install {release} {chart} \
  --namespace {project-backing-namespace} \
  --values values.yaml
```

Standard Project roles cannot manage NetworkPolicy. Disable a chart's optional
NetworkPolicy objects when present, or ask the platform operator for an
approved network policy change. Also inspect PVC classes, Service annotations,
hooks, and cluster-scoped resources before installation.

## Shared GPU workloads

When Shared GPU is explicitly requested, read
[references/shared-gpu.md](references/shared-gpu.md). Use the authenticated
preview/create API and require confirmation before accepting queued placement.
Do not add native GPU resources to the generic Deployment or bypass the product
creation gate with `kubectl apply`.

## Verify

```bash
kubectl rollout status deployment/{app} --timeout=10m
kubectl get deployment,pod,service
kubectl get endpointslice \
  -l kubernetes.io/service-name={app}
kubectl logs deployment/{app} --tail=100
```

For an exposed Service:

```bash
HOST="$(kubectl get service {app} \
  -o jsonpath='{.metadata.annotations.service\.nlb\.kube-dc\.com/route-hostname-status}')"
test -n "$HOST"
curl --fail --show-error --silent -o /dev/null "https://$HOST/"
```

If rollout fails, use `kubectl describe deployment`, `kubectl describe pod`,
events, and logs. Pod exec and attach are blocked in Project backing
namespaces, so use a temporary Job or an application diagnostic endpoint when
an active check is required.

## Safety

- Keep credentials in Secrets and never print them.
- Keep resource requests below quota with rollout headroom.
- Default to internal access, then add only the exposure the caller needs.
- Confirm PVC retention before uninstalling a Helm release; Helm commonly
  leaves claims behind.
- Use the gated product workflow for Shared GPU.
