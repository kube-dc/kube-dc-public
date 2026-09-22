# Deploy your first Kubernetes application

This guide deploys a small web service to a Kube-DC Project and publishes it
through the shared HTTP gateway. It uses only the namespaced resources that
Projects support.

## Before you begin

You need:

- A [Project](first-project.md) with free CPU, memory, pod, and IP quota
- [CLI access](cli-kubeconfig.md), with `kubectl` connected to that Project
- The `developer` or `admin` role

To confirm the current context and backing namespace, run:

```bash
kubectl config current-context
kubectl config view --minify -o jsonpath='{..namespace}'
```

The backing namespace normally follows `{organization}-{project}`, for example
`acme-demo`.

## 1. Deploy the workload

Create a web Deployment with two replicas. The image listens on port 8080 and
does not need root privileges. Use the following manifest:

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

## 2. Check the application

To verify the pods and the Service, run:

```bash
kubectl get pods -l app=hello
kubectl get service hello
```

A pod in the `Running` state is not proof that the application is healthy. The
rollout command waits for the Deployment readiness condition.

## 3. Open the endpoint

The Service annotation asks the platform to create a Gateway Route and a DNS
name. To read the assigned hostname from the Service, run:

```bash
kubectl get service hello \
  -o jsonpath='{.metadata.annotations.service\.nlb\.kube-dc\.com/route-hostname-status}'
```

After the route is ready, open `http://HOSTNAME`. Replace `HOSTNAME` with the
value the preceding command printed. DNS propagation can finish after the pods
become ready.

If no hostname appears, inspect the Service status and its events:

```bash
kubectl describe service hello
kubectl get events --sort-by=.lastTimestamp
```

To add HTTPS with the required Issuer, or to use custom hostnames, TCP and UDP
services, EIPs, and FIPs, see [Service exposure](service-exposure.md).

## Update the application

Change the image or the configuration in `hello.yaml`, then apply it again:

```bash
kubectl apply -f hello.yaml
kubectl rollout status deployment/hello
kubectl rollout history deployment/hello
```

Kubernetes rolls out the new ReplicaSet according to the Deployment strategy.
Availability depends on readiness checks, capacity, and application behavior.

## Clean up

To remove the Deployment and the Service, run:

```bash
kubectl delete -f hello.yaml
```

Before you reuse any dedicated address the Service held, confirm that the
Service and its route have finished cleanup.

## Next steps

- [Deploy a WordPress stack](deploy-wordpress-stack.md)
- [Choose a service exposure method](service-exposure.md)
- [Use block storage](block-storage.md)
- [Connect CI or an external GitOps controller](gitops.md)
