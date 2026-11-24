# Deploy your first application

An application in Kube-DC is one resource, an `Application`, that says what
to run: a container image, or a GitHub repository the platform builds. The
platform starts the pods, gives the app an HTTPS address and keeps every
version that ran, so you can roll back.

This guide deploys a container image with `kubectl` or an AI app. The
console's **New application** starts from a GitHub repository instead: to
build and deploy a repository on every push, see
[Deploy from GitHub](deploy-from-github.md).

## Before you begin

You need:

- a [Project](first-project.md) with free CPU, memory and pod quota;
- [CLI access](cli-kubeconfig.md), with `kubectl` connected to that Project;
- the `developer` or `admin` role in the Project.

Your provider must offer Applications. If `kubectl get app` says the server
has no resource type `app`, or a new app still shows no status after a
minute, ask your provider.

## 1. Create the application

Save this as `hello.yaml`:

```yaml
apiVersion: apps.kube-dc.com/v1alpha1
kind: Application
metadata:
  name: hello
spec:
  source:
    image:
      reference: nginxinc/nginx-unprivileged:1.27-alpine
  run:
    port: 8080    # the port the container listens on (8080 is the default)
```

Apply it and wait until it is ready:

```bash
kubectl apply -f hello.yaml
kubectl wait --for=condition=Ready app/hello --timeout=5m
kubectl get app hello
```

```console
NAME    REPOSITORY   COMMIT   STATUS   URL                                         AGE
hello                         Ready    https://hello-acme-demo.<platform-domain>   2m
```

The certificate for the address takes 1 to 3 minutes. Until it is issued,
the `URL` column is empty and `kubectl get app` shows **Degraded**;
`kubectl wait` returns once the address is live.

Behind the scenes the platform has created a Deployment and a Service named
`hello` and published the address on the platform's shared HTTPS entry
point, with its own certificate. No public IP is used.

## 2. Open it

Open the address from the `URL` column, or:

```bash
curl -sI "$(kubectl get app hello -o jsonpath='{.status.url}')" | head -1
```

In the console, open your Project, then **Applications**, and click **hello**.
The **Overview** tab's **Progress** shows each step (an image app needs no
build, so those steps say **Not needed**), and **Open the app** opens the
address.

If the app does not become ready, `kubectl describe app hello` shows which
step waits and why, for example an image that cannot be pulled.

## 3. Ship a new version

Change the image tag in `hello.yaml` (for example to `1.28-alpine`) and apply
it again. Every change to what the pods run, such as the image, a variable or
the resources, makes a new numbered **release**. The **Builds & releases** tab
lists them.

To go back, choose **Roll back to this release** on an earlier release. The
app stays on that release until you click **Resume deploys**. See
[Roll back an application](rollback.md).

## Deploy it with an AI app

Connect an AI app to the Project's [hosted MCP server](mcp-for-administrators.md)
and ask, for example:

> Deploy the image nginxinc/nginx-unprivileged:1.27-alpine as an app named
> hello, listening on port 8080, and tell me its address.

The AI app calls `deploy_app`, which creates the same Application (where
your provider has [turned that on](applications.md#apps-created-before-applications)),
and `rollout_status` to report the address. You can switch between the console, `kubectl` and an AI app at any
time: they all change the same resource.

## Clean up

```bash
kubectl delete app hello
```

This removes the app, its Deployment, Service and address.

## When an Application does not fit

An Application runs one container per pod and publishes one HTTP port on one
hostname. For anything else, such as a TCP or UDP service, sidecar
containers, several hostnames or a StatefulSet, apply your own Kubernetes
manifests: Projects accept Deployments, StatefulSets, Services and Jobs (see
[Project compatibility](security-restrictions.md)), and
[Service exposure](service-exposure.md) publishes them.

## Next steps

- [Manage applications](applications.md): variables, Secrets, your own domain, the CLI and deleting an app
- [Release commands, workers and cron jobs](app-jobs-and-workers.md), autoscaling and a data volume
- [Deploy a full WordPress stack](deploy-wordpress-stack.md): an Application with a managed database and a volume
- [Deploy from GitHub](deploy-from-github.md): builds on push and pull request previews
