import {GitOpsProjectMappingDiagram} from '@site/src/components/Diagram/CloudTopologyDiagrams';

# GitOps

GitOps works with Kube-DC, but the controller must run in the right place.

A Project is a governed workload environment on the shared platform, backed by
one Kubernetes namespace. It cannot install the CRDs, webhooks, ClusterRoles, or additional namespaces required by Argo CD or
Flux. Do not bootstrap either controller inside a Project.

Choose one of these supported patterns:

| Where the controller runs | What it manages | Use when |
|---------------------------|-----------------|----------|
| External CI or GitOps platform | One or more Kube-DC Projects through Project-scoped credentials | You already operate a central delivery platform |
| A Kube-DC Managed Cluster | That cluster, and optionally registered external targets | You need Argo CD or Flux inside Kube-DC |
| Manual CI reconciliation | One Project with `kubectl diff/apply` or Helm | You need a small, auditable delivery path without another controller |

## Pattern 1: External Controller to a Project

Configure the external controller with a credential scoped to the target
Project. Do not let the tool bootstrap a cluster-admin account or create
cluster-scoped resources on the Kube-DC platform cluster.

Keep each target explicit:

<details data-github-only>
<summary>Diagram source for GitHub</summary>

```text
delivery-platform
├── Project development  -> backing namespace acme-development
├── Project staging      -> backing namespace acme-staging
└── Project production   -> backing namespace acme-production
```

</details>

<GitOpsProjectMappingDiagram />

The Project kubeconfig sets the namespace, but manifests should still declare
the intended Project backing namespace. A delivery job must fail if a manifest targets
another namespace or includes a cluster-scoped object.

:::warning Credentials for automation
The interactive `kube-dc login` credential is a user session, not a permanent
CI secret. Use the automation identity and credential flow approved by your
Kube-DC administrator. Scope it to one Project, store it in the CI secret store,
and rotate it regularly.
:::

## Pattern 2: GitOps in a Managed Cluster

A [Managed Cluster](provisioning-cluster.md) has its own Kubernetes API, so its
administrator can install Argo CD, Flux, CRDs, and cluster-wide RBAC using the
tool's official installation instructions.

Install the controller with the Managed Cluster kubeconfig, not the parent
Project kubeconfig. The controller can then manage namespaces and applications
inside that cluster. Registering an external Project as another target remains
a separate security decision and requires Project-scoped credentials.

## Pattern 3: Reconcile from CI

For a small deployment, a CI job can render, review, and apply Project-scoped
manifests without a resident controller:

```bash
helm template my-app ./chart -f environments/production.yaml > rendered.yaml

# kubectl diff uses exit 1 for expected differences and >1 for errors.
set +e
kubectl diff -f rendered.yaml
diff_status=$?
set -e
if [ "$diff_status" -gt 1 ]; then
  exit "$diff_status"
fi

kubectl apply -f rendered.yaml
kubectl rollout status deployment/my-app --timeout=5m
```

Use a fixed Project context. Treat `kubectl diff` exit code `1` as a normal
change preview; stop on values greater than `1`, and on rendering, apply, or
rollout errors.
For higher assurance, promote the same image digest and rendered configuration
between environments rather than rebuilding them.

## Repository Structure

Keep reusable configuration separate from environment ownership:

```text
apps/
└── payments/
    ├── base/
    └── overlays/
        ├── development/
        ├── staging/
        └── production/
```

A Project overlay should contain only resources supported in a Project. Put
operators, CRDs, namespaces, and cluster add-ons in a Managed Cluster repository.

## Project Compatibility Checklist

Before enabling reconciliation, confirm that the rendered output:

- targets exactly one Project backing namespace
- contains no `Namespace`, CRD, ClusterRole, or StorageClass objects
- does not require `kubectl exec` or `attach`
- uses Jobs instead of CronJobs in a Project
- disables chart-managed NetworkPolicies
- declares realistic CPU, memory, and storage requests
- keeps Secret values in a secret manager, not Git
- waits for rollout health before promotion

Use `helm template` or the tool's dry-run mode in CI to enforce this check.

## Rollback and Recovery

Git gives you the desired-state history, not a data backup. Reverting a commit
can restore Kubernetes configuration, but it does not roll back a database,
object bucket, or persistent volume. Pair GitOps with the service-specific
recovery plan in [Data Protection and Recovery](backups-snapshots.md).

## Troubleshooting

**The controller cannot create a resource**

Check whether the chart contains a cluster-scoped resource or a resource that
is read-only in Projects. Move that workload to a Managed Cluster or disable
the unsupported chart component.

**The controller tries to manage every namespace**

The target credential or controller configuration is too broad. Restrict its
namespace scope to the Project and rotate any credential that had wider access.

**The deployment is healthy but the application is unreachable**

GitOps reconciles the workload, not its public entry point. Check the
LoadBalancer Service, EIP, or Gateway route in [Service Exposure](service-exposure.md).

## Next Steps

- [Projects](kubernetes-projects.md)
- [Managed Clusters](provisioning-cluster.md)
- [Security Restrictions](security-restrictions.md)
- [Service Exposure](service-exposure.md)
