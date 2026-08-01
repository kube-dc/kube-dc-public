# Deploy Your First Kubernetes Application

This guide deploys a small web service to a Kube-DC Project and publishes it
through the shared HTTP gateway. It uses only namespaced resources supported by Projects.

## Prerequisites

- A [Project](first-project.md) with available CPU, memory, Pod, and IP quota
- [CLI access](cli-kubeconfig.md) with `kubectl` connected to that Project
- The `developer` or `admin` role

Confirm the current context and backing namespace:

```bash
kubectl config current-context
kubectl config view --minify -o jsonpath='{..namespace}'
```

The backing namespace normally follows `{organization}-{project}`, for example `acme-demo`.

## 1. Deploy the Workload

Create a two-replica web Deployment. The image listens on port 8080 and does not require root privileges.

```yaml
apiVersion: apps/v1
kind: Deployment
metadata:
  name: hello
spec:
  replicas: 2
  selector:
    matchLabels:
      app: hello
  template:
    metadata:
      labels:
        app: hello
    spec:
      containers:
        - name: web
          image: nginxinc/nginx-unprivileged:stable-alpine
          ports:
            - name: http
              containerPort: 8080
          readinessProbe:
            httpGet:
              path: /
              port: http
            periodSeconds: 5
            timeoutSeconds: 2
            failureThreshold: 6
          resources:
            requests:
              cpu: 50m
              memory: 64Mi
            limits:
              cpu: 250m
              memory: 128Mi
---
apiVersion: v1
kind: Service
metadata:
  name: hello
  annotations:
    service.nlb.kube-dc.com/expose-route: "http"
spec:
  type: LoadBalancer
  selector:
    app: hello
  ports:
    - name: http
      port: 80
      targetPort: 8080
```

Save the manifest as `hello.yaml`, then apply it:

```bash
kubectl apply -f hello.yaml
kubectl rollout status deployment/hello
```

## 2. Check the Application

Verify the Pods and Service:

```bash
kubectl get pods -l app=hello
kubectl get service hello
```

A Pod in `Running` state is not automatically proof that the application is healthy. The rollout command waits for the Deployment readiness condition.

## 3. Open the Endpoint

The Service annotation asks the platform to create a Gateway Route and DNS
name. Read the assigned hostname from the Service:

```bash
kubectl get service hello \
  -o jsonpath='{.metadata.annotations.service\.nlb\.kube-dc\.com/route-hostname-status}'
```

Open `http://<assigned-hostname>` after the route is ready. DNS propagation can
finish after the Pods become ready.

If no hostname appears, inspect the Service status and events:

```bash
kubectl describe service hello
kubectl get events --sort-by=.lastTimestamp
```

See [Service Exposure](service-exposure.md) to add HTTPS with the required
Issuer, or to use custom hostnames, TCP/UDP services, EIPs, and FIPs.

## Update the Application

Change the image or configuration in `hello.yaml`, then apply it again:

```bash
kubectl apply -f hello.yaml
kubectl rollout status deployment/hello
kubectl rollout history deployment/hello
```

Kubernetes rolls out the new ReplicaSet according to the Deployment strategy. Availability depends on readiness checks, capacity, and application behavior.

## Clean Up

```bash
kubectl delete -f hello.yaml
```

This deletes the Deployment and Service. Confirm that the Service and its route have finished cleanup before reusing any dedicated address associated with it.

## Next Steps

- [Deploy a WordPress stack](deploy-wordpress-stack.md)
- [Choose a service exposure method](service-exposure.md)
- [Use block storage](block-storage.md)
- [Connect CI or an external GitOps controller](gitops.md)
