# Kubernetes Projects

A **Kubernetes Project** is Kube-DC's default deployment environment. It gives your team a Project-scoped kubeconfig, namespace, RBAC, private network (VPC) and optional quota — so you deploy applications and virtual machines directly, without provisioning a cluster first.

The distinction to keep in mind: **a Project is where you deploy; an app is what you deploy.** One Project typically holds several apps, their databases and storage — and VMs, side by side.

Most teams run everything they need in a Project and never operate a cluster of their own. Capacity is already running: your first deployment is a `kubectl apply`, not a provisioning wizard.

A Project is a governed environment on the shared platform cluster. It is not a tenant Kubernetes cluster and does not give you control over cluster-scoped APIs — that is what [Managed Clusters](provisioning-cluster.md) are for, and you can create one from the same Project when you need it.

## What a Project gives you

| | |
|---|---|
| **Kubernetes API access** | A kubeconfig for `kubectl`, compatible Helm charts, Terraform, or an external GitOps controller |
| **Application compute** | Deployments, StatefulSets, DaemonSets, Pods, Jobs, Services, Ingress and Gateway API routes, autoscalers, disruption budgets |
| **Virtual machines** | Full KubeVirt Linux and Windows guests, on the same network as your containers |
| **Private networking** | A VPC with load balancers, external IPs and floating IPs where you need them |
| **Persistent data** | Block volumes (PVCs) and S3-compatible object buckets |
| **Managed services** | Databases, certificates, secrets, database credentials and KMS keys — created as Kubernetes resources in your namespace |
| **Governance** | Per-role RBAC for your team and an optional resource quota |

Identity stays scoped to your Organization; billing is aggregated there too. The Project supplies the namespace, authorization, network and governance boundaries for everything in it.

Use `kubectl logs` and `kubectl top` for containers. `kubectl exec`/`attach` are blocked in project namespaces by design — run administrative tasks as **Jobs** mounting the same volumes (the [WordPress guide](deploy-wordpress-stack.md) shows the pattern), and use the console/VNC for VMs.

## Deploy in about 60 seconds

There is no cluster provisioning step — once the Project exists, deploy:

```bash
# 1. Download the Project kubeconfig (Console → Get CLI Access, or the kube-dc CLI)
export KUBECONFIG="$PWD/kube-dc-project.yaml"

# 2. Verify context and permission
kubectl config current-context
kubectl auth can-i create deployments.apps

# 3. Deploy manifests…
kubectl apply -f ./k8s/
kubectl rollout status deployment/my-app

# …or a compatible Helm chart
helm upgrade --install my-app ./chart --wait
```

See [Deploy Your First Application](deploy-first-app.md) for a complete walkthrough including HTTPS exposure with automatic certificates.

## What "compatible" means

Projects support Kubernetes resources that are **namespaced**, granted by Project RBAC, and accepted by Project pod-security policy. That covers the standard application resources listed above, plus instances of the platform's own APIs (databases, IPs, certificates, VMs).

Charts or manifests that create any of the following are **not supported in a Project**:

- CustomResourceDefinitions (operators, and the charts that ship them)
- ClusterRoles or ClusterRoleBindings
- Admission webhooks
- StorageClasses
- NetworkPolicies
- CronJobs *(readable, but not writable in Projects)*
- Privileged containers, or workloads using `hostPath`, `hostNetwork`, `hostPID`, `hostIPC`
- Additional namespaces

Some charts can disable their optional operator, webhook or scheduled components through values — if so, pin those values and re-review the rendered output. Compatibility applies to a *chart version + values* combination, not to every release of a chart.

### Check an unfamiliar chart before installing

```bash
helm template my-app ./chart --include-crds --values values.yaml > rendered.yaml
kubectl apply --dry-run=server -f rendered.yaml
```

Server-side dry-run reports `Forbidden` for anything outside the Project boundary — before anything is installed. This is a screening step, not a guarantee: charts using `lookup`, hooks or controllers can have runtime behaviour a local render does not reveal.

## Understanding `Forbidden` errors

A `Forbidden` response usually means the Kubernetes API is telling you which boundary the workload crossed:

| Error refers to | What it means |
|---|---|
| CRDs, cluster RBAC, webhooks, StorageClasses | The software needs cluster scope → use a [Managed Cluster](provisioning-cluster.md) |
| NetworkPolicies or CronJobs | Not writable in Projects → disable the optional component, or use a Managed Cluster |
| Privileged / host-access settings | Reconfigure the workload as unprivileged, or use a Managed Cluster |
| A supported namespaced resource | Check your Project role and quota first — this is likely a permission or limit issue, not the tier boundary |

Retrying in another Project will not change the result for the first three rows — the chart genuinely requires the cluster tier. Use `kubectl auth can-i` to distinguish a role problem from an API the Project does not expose:

```bash
kubectl auth can-i create deployments.apps       # yes — supported
kubectl auth can-i create cronjobs.batch          # no — Project boundary
kubectl auth can-i create customresourcedefinitions.apiextensions.k8s.io   # no — cluster scope
```

## When to choose a Managed Cluster instead

Choose a [Managed Kubernetes Cluster](provisioning-cluster.md) when your software:

- Installs an operator or CRDs
- Creates cluster-scoped RBAC or admission webhooks
- Manages StorageClasses
- Spans multiple namespaces
- Needs tenant-authored NetworkPolicies or CronJobs
- Requires privileged containers or host-level access

A Managed Cluster is provisioned from your Project in minutes and has its own API endpoint. The choice is made per workload, upfront — switching later means redeploying against the new endpoint and planning migration for Helm state, data, IPs and DNS.

## GitOps and automation

A Project is a standard Kubernetes API target: point an externally hosted Argo CD or Flux, Terraform's Kubernetes provider, or CI at your Project kubeconfig. (The Argo CD and Flux *controller stacks themselves* install CRDs, so they run outside the Project — see [GitOps & Automation](gitops.md).) AI coding assistants can drive Projects directly through the [agent skills](ai-ide-integration.md).

## Next steps

- [Connect with a Project kubeconfig](cli-kubeconfig.md)
- [Deploy your first application](deploy-first-app.md)
- [Project compatibility and restrictions](security-restrictions.md)
- [Provision a Managed Kubernetes Cluster](provisioning-cluster.md)
