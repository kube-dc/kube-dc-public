# Projects

A **Project** is Kube-DC's default deployment environment. It gives your team a
Project kubeconfig, a dedicated backing namespace, RBAC, a private network
(VPC), and an optional quota. You deploy applications and virtual machines into
it directly, with no cluster to provision first.

Keep the difference in mind: a Project is where you deploy, and an app is what
you deploy. One Project usually holds several apps, their databases, their
storage, and VMs, side by side.

Most teams run everything they need in a Project and never operate a cluster of
their own. The capacity is already running, so your first deployment is a
`kubectl apply`, not a provisioning wizard.

A Project is a governed environment on the shared platform cluster. It is not a
tenant Kubernetes cluster, and it does not give you control over cluster-scoped
APIs. For that, use a [Managed Cluster](provisioning-cluster.md), which you can
create from the same Project when you need one.

## What a Project gives you

The following table lists what each Project includes:

| Capability | What you get |
|---|---|
| **Kubernetes API access** | A kubeconfig for `kubectl`, compatible Helm charts, Terraform, or an external GitOps controller |
| **Application compute** | Deployments, StatefulSets, DaemonSets, Pods, Jobs, Services, Ingress and Gateway API routes, autoscalers, disruption budgets |
| **Virtual machines** | Full KubeVirt Linux and Windows guests, on the same network as your containers |
| **Private networking** | A VPC with load balancers, external IPs, and floating IPs where you need them |
| **Persistent data** | Block volumes (PVCs) and S3-compatible object buckets |
| **Managed services** | Databases, certificates, secrets, database credentials, and KMS keys created in your Project |
| **Governance** | Per-role RBAC for your team and an optional resource quota |

Identity and billing stay scoped to your Organization. The Project supplies the
workload, authorization, network, and governance boundaries. Kubernetes stores
the Project's resources in its backing namespace.

For containers, use `kubectl logs` and `kubectl top`. Project backing
namespaces block `kubectl exec` and `kubectl attach` by design. Run
administrative tasks as Jobs that mount the same volumes. The
[WordPress guide](deploy-wordpress-stack.md) shows the pattern. For VMs, use
the console or VNC.

## Deploy into a Project

There is no cluster provisioning step. As soon as the Project exists, you can
deploy:

```bash
# 1. Download the Project kubeconfig (Console > Get CLI Access, or the kube-dc CLI)
export KUBECONFIG="$PWD/kube-dc-project.yaml"

# 2. Verify the context and your permissions
kubectl config current-context
kubectl auth can-i create deployments.apps

# 3. Deploy manifests...
kubectl apply -f ./k8s/
kubectl rollout status deployment/my-app

# ...or a compatible Helm chart
helm upgrade --install my-app ./chart --wait
```

For the first HTTP deployment, see
[Deploy your first application](deploy-first-app.md). To add HTTPS, see
[Service exposure](service-exposure.md).

## What "compatible" means

Projects support Kubernetes resources that are namespaced, granted by Project
RBAC, and accepted by the Project pod-security policy. That covers the standard
application resources listed in the preceding table, plus instances of the
platform's own APIs: databases, IPs, certificates, and VMs.

A Project does not support charts or manifests that create any of the
following:

- CustomResourceDefinitions, including operators and the charts that ship them
- ClusterRoles or ClusterRoleBindings
- Admission webhooks
- StorageClasses
- NetworkPolicies
- CronJobs, which are readable but not writable in Projects
- Privileged containers, or workloads that use `hostPath`, `hostNetwork`,
  `hostPID`, or `hostIPC`
- Additional namespaces

Some charts let you turn off their optional operator, webhook, or scheduled
components through values. If yours does, pin those values and review the
rendered output again. Compatibility applies to one combination of chart
version and values, not to every release of a chart.

### Check an unfamiliar chart before you install it

To render a chart and test it against the API server without installing
anything, run:

```bash
helm template my-app ./chart --include-crds --values values.yaml > rendered.yaml
kubectl apply --dry-run=server -f rendered.yaml
```

A server-side dry run reports `Forbidden` for anything outside the Project
boundary. Treat this as a screening step, not a guarantee. Charts that use
`lookup`, hooks, or controllers can behave at runtime in ways a local render
does not reveal.

## Understand `Forbidden` errors

A `Forbidden` response usually names the boundary the workload crossed. The
following table maps the error to its cause:

| Error refers to | What it means |
|---|---|
| CRDs, cluster RBAC, webhooks, StorageClasses | The software needs cluster scope. Use a [Managed Cluster](provisioning-cluster.md). |
| NetworkPolicies or CronJobs | Not writable in Projects. Turn off the optional component, or use a Managed Cluster. |
| Privileged or host-access settings | Reconfigure the workload as unprivileged, or use a Managed Cluster. |
| A supported namespaced resource | Check your Project role and quota first. This is a permission or limit problem, not the tier boundary. |

For the first three rows, retrying in another Project gives the same result,
because the chart needs the cluster tier. To tell a role problem apart from an
API the Project does not expose, run `kubectl auth can-i`:

```bash
kubectl auth can-i create deployments.apps       # yes: supported
kubectl auth can-i create cronjobs.batch          # no: Project boundary
kubectl auth can-i create customresourcedefinitions.apiextensions.k8s.io   # no: cluster scope
```

## When to choose a Managed Cluster instead

Choose a [Managed Cluster](provisioning-cluster.md) when your software:

- Installs an operator or CRDs
- Creates cluster-scoped RBAC or admission webhooks
- Manages StorageClasses
- Spans multiple namespaces
- Needs tenant-authored NetworkPolicies or CronJobs
- Needs privileged containers or host-level access

Kube-DC provisions a Managed Cluster from your Project in minutes, and the
cluster has its own API endpoint. Make the choice for each workload before you
deploy. To switch later, you redeploy against the new endpoint and plan the
migration of Helm state, data, IPs, and DNS.

## GitOps and automation

A Project is a standard Kubernetes API target. Point an externally hosted Argo
CD or Flux, Terraform's Kubernetes provider, or your CI system at the Project
kubeconfig. The Argo CD and Flux *controller stacks themselves* install CRDs,
so they run outside the Project. See [GitOps and automation](gitops.md). AI
coding assistants can drive Projects directly through the
[agent skills](ai-ide-integration.md).

## Next steps

- [Connect with a Project kubeconfig](cli-kubeconfig.md)
- [Deploy your first application](deploy-first-app.md)
- [Project compatibility and restrictions](security-restrictions.md)
- [Provision a Managed Cluster](provisioning-cluster.md)
