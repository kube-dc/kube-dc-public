# Ways to deploy

There are three ways to deploy into a Project, and you can use them side by
side. One is plain Kubernetes; the other two are Applications, which differ
only in where the app comes from:

- **Kubernetes manifests.** A Project is a standard Kubernetes API target.
  Apply your own Deployments, Services and Jobs with `kubectl`, or install a
  Helm chart. You decide every detail and manage the objects yourself.
- **Applications.** You describe the app in one resource, an `Application`,
  and the platform runs it: it creates the Deployment and Service, publishes
  an HTTPS address and keeps a history of releases you can roll back to. An
  Application starts from one of two sources:
  - **a container image** you already have, or
  - **a GitHub repository**, which the platform builds on every push. Each
    pull request can get its own preview.

Applications are available where your provider offers them, and builds from
GitHub where your provider offers builds. Kubernetes manifests work in every
Project.

## The options at a glance

| | Kubernetes manifests | Application from an image | Application from GitHub |
|---|---|---|---|
| You provide | Deployments, Services and other manifests, or a Helm chart | An image reference | A repository and a branch, plus a registry for the built images (the console sets up the Project's managed registry for you) |
| Who builds the image | You, or your own CI | You, or your own CI | The platform, inside your Project, on every push |
| HTTPS address and certificate | You add one annotation to the Service | Automatic | Automatic |
| New version | You apply the changed manifests | You change the image in the Application | You push to the branch |
| Rollback | `kubectl rollout undo`, Helm or your own tooling | Numbered releases, one click or one command | Numbered releases, one click or one command |
| Pull request previews | Not from the platform. Your own GitHub Actions workflow can make them (see [Which one to choose](#which-one-to-choose)) | No | Yes, with a database of their own if you want one |
| Shape of the workload | Anything a Project allows: several containers, StatefulSets, TCP and UDP ports, several hostnames (one Service each) | One container per pod, one HTTP port, one hostname; or a worker with no port | The same as an image Application |
| Start here | [Projects](kubernetes-projects.md#deploy-into-a-project) | [Deploy your first application](deploy-first-app.md) | [Deploy from GitHub](deploy-from-github.md) |

## Deploy Kubernetes manifests directly

Choose this when you already have manifests or a Helm chart, or when the
workload does not fit the shape of an Application: a database you run
yourself in a StatefulSet, a pod with sidecar containers, a TCP or UDP
service, or an app that answers on several hostnames (one Service each).

```yaml
apiVersion: apps/v1
kind: Deployment
metadata:
  name: hello
spec:
  replicas: 2
  selector:
    matchLabels: {app: hello}
  template:
    metadata:
      labels: {app: hello}
    spec:
      containers:
        - name: web
          image: nginxinc/nginx-unprivileged:1.27-alpine
          ports:
            - containerPort: 8080
---
apiVersion: v1
kind: Service
metadata:
  name: hello
  annotations:
    service.nlb.kube-dc.com/expose-route: "https"   # an HTTPS address with a platform certificate
spec:
  selector: {app: hello}
  ports:
    - port: 80
      targetPort: 8080
```

```bash
kubectl apply -f hello.yaml
kubectl rollout status deployment/hello
kubectl get svc hello -o jsonpath='{.metadata.annotations.service\.nlb\.kube-dc\.com/route-hostname-status}'
```

The last command prints the address,
`hello-<project-namespace>.<platform-domain>`. It stays empty until the
certificate is issued, which takes 1 to 3 minutes.

What you take care of yourself: building and pushing the image, the Service
and its exposure, updates and rollbacks.

- [Projects](kubernetes-projects.md): what a Project accepts, Helm charts and `Forbidden` errors
- [Project compatibility and restrictions](security-restrictions.md)
- [Service exposure](service-exposure.md): HTTPS addresses, your own domain, public IPs for TCP and UDP
- [GitOps and CI](gitops.md): apply the manifests from Argo CD, Flux or a pipeline

## Deploy an Application from a container image

Choose this when you have an image, built anywhere, and want the platform to
run it. The whole app is one short resource:

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
    port: 8080
```

The platform creates the Deployment and the Service, publishes
`https://<app>-<project-namespace>.<platform-domain>` with a certificate, and records a
numbered release every time you change what the pods run. Variables, Secrets,
a data volume, autoscaling, release commands and a custom domain are fields
of the same resource.

- [Deploy your first application](deploy-first-app.md): step by step
- [Manage applications](applications.md): every field, the console, the CLI and AI apps
- [Roll back an application](rollback.md)

## Deploy an Application from GitHub, built by the platform

Choose this when your code is in a GitHub repository and you want a push to
be a deploy. Where your provider offers builds:

- **The build runs inside the platform**, in your Project. It uses the
  repository's `Dockerfile`, or detects the language when there is none. You
  need no CI pipeline, and no file is added to the repository.
- **Every push to the branch** is built and deployed as a new release.
- **Every pull request can get a preview**: a separate copy of the app at its
  own address, named `<app>-pr-<n>`. It is removed when the pull request
  closes, or after 72 hours without a new build unless you keep it longer. A
  preview can have its own database, empty or copied from another one.
- **Built images are stored** in the Project's
  [managed registry](managed-services-registry.md), or in a registry of your
  own.

In the console, open your Project, then **Applications**, and click **New
application**. The same app can be written as an Application with
`source.github`.

- [Deploy from GitHub](deploy-from-github.md): the console flow, the manifest, builds and previews

## Which one to choose

- **Your code is on GitHub and you want builds and previews without a
  pipeline:** an Application from GitHub.
- **You already build images**, in your own CI or by hand, and want an HTTPS
  address and rollback without writing manifests: an Application from an
  image.
- **You have manifests or a Helm chart**, or the workload needs more than one
  container, port or hostname: Kubernetes manifests.
- **You want your own GitHub Actions workflow to build and deploy**, for
  example to run tests first: see
  [Build in your own GitHub Actions workflow](deploy-from-github.md#build-in-your-own-github-actions-workflow-instead).
  That workflow deploys a plain Deployment and Service, not an Application.
- **The software needs operators, custom resource definitions or its own
  Kubernetes API:** a [Managed Cluster](provisioning-cluster.md), not a
  Project.

You can mix them in one Project. For example, run the web app as an
Application from GitHub and a message broker from a Helm chart next to it:
they share the Project's private network and reach each other by Service
name.

One rule when you mix: an Application owns the Deployment, the Service and
the autoscaler named after it, the volume claim `<app>-data`, and other
objects it creates under names that start with `<app>-`, such as previews
and release jobs. Change the Application, not those objects, and do not give
your own manifests the same names: if a name is already taken, the app
reports the name as in use and does not become ready.

## The same tools for every option

The options above are about what you deploy. How you send it is a separate
choice, and all of these work with manifests and with Applications:

- **`kubectl`** and Helm, with the [Project kubeconfig](cli-kubeconfig.md);
- **the console**: the **Applications** view for apps, the **Kubernetes**
  section to browse Deployments, Services and other objects, and the
  [Project console](dashboard-overview.md#project-console), a web terminal
  with a signed-in `kubectl`;
- **an AI app** through the [hosted MCP server](mcp-for-administrators.md)
  or the [agent skills](ai-ide-integration.md);
- **GitOps and CI**: see [GitOps and CI](gitops.md).
