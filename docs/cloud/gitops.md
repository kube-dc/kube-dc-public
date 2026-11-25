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

## Pattern 1: External controller to a Project

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
CI secret. Use a machine identity or a ServiceAccount token instead, as below. Scope it
to one Project, store it in the CI secret store, and rotate it regularly.
:::

### Mint an automation credential

Create a ServiceAccount in your Project namespace, give it only the rights the
pipeline needs, and mint a short-lived token for it. A Project admin can do all
three; no platform administrator is involved.

```bash
# 1. the identity your pipeline will act as
kubectl -n <project-namespace> create serviceaccount ci-deploy

# 2. exactly the rights it needs. This example deploys, and reads nothing secret.
kubectl -n <project-namespace> create role ci-deploy \
  --verb=get,list,watch,create,update,patch \
  --resource=deployments.apps,services,persistentvolumeclaims
kubectl -n <project-namespace> create rolebinding ci-deploy \
  --role=ci-deploy --serviceaccount=<project-namespace>:ci-deploy

# 3. the credential itself
kubectl -n <project-namespace> create token ci-deploy --duration=24h
```

Add `delete` if your tool prunes, and every kind your manifests contain
(ConfigMaps, HorizontalPodAutoscalers, `applications.apps.kube-dc.com`…). A
Role cannot grant more than you hold yourself, so `patch` on Ingresses or Jobs
is refused (see the checklist below). `helm upgrade` also needs Secrets, where
Helm keeps its releases. To give the pipeline a whole Project role instead,
bind `developer`:
`kubectl -n <project-namespace> create rolebinding ci-deploy --role=developer --serviceaccount=<project-namespace>:ci-deploy`.

Put the token in your CI secret store and build a kubeconfig around it, or pass it
as a bearer token. Copy the API server address and its CA from your own
Project kubeconfig, and keep the CA in the pipeline's connection settings:
`kubectl config view --minify --raw --flatten -o jsonpath='{.clusters[0].cluster}'`
prints both, with the CA as `certificate-authority-data`.

**Tokens expire.** `create token` issues a bound, time-limited credential. A
ServiceAccount has no permanent one, by design. Choose a duration your rotation can keep up with, and
re-mint before it lapses. A pipeline cannot re-mint its own token, so rotation is a
Project-admin action (or a job running under a longer-lived credential).

An Organization administrator can instead create a
[machine identity](machine-identities.md): a client ID and secret that fetch
short-lived tokens for as long as the secret is not rotated, in the
Organization groups you choose. For GitHub Actions it needs no secret at all:
the workflow signs in with its own OIDC token
([GitHub Actions without a secret](machine-identities.md#github-actions-without-a-secret)).

```bash
kube-dc machine-identities create ci --group <organization-group>
kube-dc machine-identities kubeconfig ci -n <project-namespace> --out ci.kubeconfig
```

That kubeconfig runs `kube-dc machine-identities token`, so the `kube-dc` CLI
must be installed where it is used. Use the ServiceAccount token above when
you are a Project admin without Organization rights, or when the tool only
takes a bearer token.

:::note Why a Secret does not work
Creating a `kubernetes.io/service-account-token` Secret is refused. That older
mechanism produces a credential that never expires, and it is filled for whichever
account holds the named ServiceAccount at the moment the controller reaches it,
so a Secret created against a name that is later taken over by a platform-managed
account would be filled with *that* account's credential. `create token` has no
such ambiguity: it names the account, and the token is bound and short-lived.

For the same reason, the platform refuses tokens for its own managed
ServiceAccounts, the accounts behind managed databases and other managed
services. Those carry the
`services.kube-dc.com/managed-by` label and are minted only by the platform.
:::

## Pattern 2: GitOps in a Managed Cluster

A [Managed Cluster](provisioning-cluster.md) has its own Kubernetes API, so its
administrator can install Argo CD, Flux, CRDs, and cluster-wide RBAC using the
tool's official installation instructions.

Install the controller with the Managed Cluster kubeconfig (see
[Get the kubeconfig](cluster-management.md#get-the-kubeconfig)), not the
parent Project kubeconfig. The controller can then manage namespaces and applications
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

For an Application, apply its manifest and wait for the app, not for a
Deployment. Wait until its `Ready` condition describes the spec you applied:

```bash
kubectl apply -f app.yaml
gen=$(kubectl get app my-app -o jsonpath='{.metadata.generation}')
kubectl wait app/my-app --timeout=15m \
  --for=jsonpath="{.status.conditions[?(@.type=='Ready')].observedGeneration}=$gen"
kubectl wait app/my-app --timeout=15m --for=condition=Ready
```

`kube-dc app wait` does the same for a person signed in with `kube-dc login`;
it does not use a machine identity's or a ServiceAccount's kubeconfig.

## Applications and managed services in Git

An [Application](applications.md) and a [managed service](managed-services.md)
are namespaced resources of the Project, so Argo CD, Flux or a pipeline
applies them like any other manifest. A Kube-DC `Application`
(`apps.kube-dc.com/v1alpha1`) is not an Argo CD `Application`.

- **Rights.** The credential needs `applications.apps.kube-dc.com` with
  `get,list,watch,create,update,patch`, and `delete` if the tool prunes. It
  needs no rights on Deployments or Services: the platform creates them. The
  example Role above does not include this; add it, or bind the
  ServiceAccount to the Project's `developer` Role.
- **Do not manage what the Application owns.** Keep these out of Git and give
  your own manifests other names: the Deployment, Service and autoscaler
  named after the app, `<app>-previous`, the volume claim `<app>-data`, the
  app's release and scheduled jobs, and previews `<app>-pr-<n>` with their
  databases. A name that is already taken blocks that object: for the
  Deployment, Service, autoscaler or volume claim the app reports
  `NameInUse`; for `<app>-previous`, its `WarmPrevious` condition reports
  `WarmRefused`.
- **Status is the platform's.** The platform writes `status`, its own
  finalizers (`apps.kube-dc.com/...`) and one annotation
  (`apps.kube-dc.com/resolve-requested-at`). It does not rewrite `spec`, so
  the manifest in Git stays equal to the live one. Do not remove those
  finalizers.
- **One place for changes.** The console, `kube-dc app` and AI apps change
  the same `spec` (a pause is `spec.suspend`, a rollback is
  `spec.release.pin`). When Git owns the Application, make those changes in
  Git, or your tool may undo them.
- **Builds.** An Application from a container image is fully described in
  Git: a new version is a commit that changes `source.image.reference`. An
  Application from GitHub is built by the platform on every push to its
  branch, with no pipeline and no cluster credential in CI; the commit that
  runs is in `status.deployed.commit`, not in Git. To gate deploys on your
  own tests, set `build.automatic: false` and let CI ask for the build: an
  `ApplicationBuild` with `spec.applicationRef.name` and the tested
  40-character `spec.commit` (the credential also needs `create` on
  `applicationbuilds.apps.kube-dc.com`). `kube-dc app rebuild <app> --commit
  <sha>` does the same for a signed-in person. Do not also deploy the same
  app from a workflow: both would deploy every push.
- **Managed services.** Keep `deletionProtection: true` in Git until you mean
  to delete the service. Leave `serviceUID` out of a `ServiceBinding`
  applied together with its service (see
  [Connect applications](postgresql-connect.md)). The Secret a binding
  delivers is made by the platform: keep it out of Git.

## Repository structure

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

## Project compatibility checklist

Before enabling reconciliation, confirm that the rendered output:

- targets exactly one Project backing namespace
- contains no `Namespace`, CRD, ClusterRole, ClusterRoleBinding, admission
  webhook configuration, or StorageClass objects
- contains no ResourceQuota, LimitRange, ServiceMonitor or
  `kubernetes.io/service-account-token` Secret
- contains no HTTPRoute, GRPCRoute or TLSRoute: publish a Service with the
  `service.nlb.kube-dc.com/expose-route` annotation instead (see
  [Service exposure](service-exposure.md))
- does not need to change an Ingress, Job, PodDisruptionBudget, cert-manager
  Issuer or Certificate, or ObjectBucketClaim in place: a Project can create
  and delete these but not patch them. Server-side apply (Flux, Terraform,
  `kubectl apply --server-side`) is refused for them even on first creation;
  use client-side apply, and delete and create again to change one
- uses no name that an Application owns (see above)
- does not require `kubectl exec`, `attach` or `port-forward`
- disables chart-managed NetworkPolicies
- declares realistic CPU, memory, and storage requests
- keeps Secret values in a secret manager, not Git
- waits for rollout health before promotion

Use `helm template` or the tool's dry-run mode in CI to enforce this check.

## Rollback and recovery

Git gives you the desired-state history, not a data backup. Reverting a commit
can restore Kubernetes configuration, but it does not roll back a database,
object bucket, or persistent volume. Pair GitOps with the service-specific
recovery plan in [Data protection and recovery](backups-snapshots.md).

An Application keeps its own numbered releases. Roll one back with
`spec.release.pin` in Git (see [Roll back an application](rollback.md));
reverting the Git commit of an Application built from GitHub does not change
the commit that runs.

## Troubleshooting

**The controller cannot create a resource**

Check whether the chart contains a cluster-scoped resource or a resource that
is read-only in Projects. Move that workload to a Managed Cluster or disable
the unsupported chart component.

**"cannot patch resource" on an Ingress, Job, PodDisruptionBudget, Certificate or ObjectBucketClaim**

A Project can create and delete these kinds but not change them. Delete the
object and let the tool create it again, and do not use server-side apply for
them.

**The controller tries to manage every namespace**

The target credential or controller configuration is too broad. Restrict its
namespace scope to the Project and rotate any credential that had wider access.

**The deployment is healthy but the application is unreachable**

The address comes from the Service your tool applies. For a Service with the
`service.nlb.kube-dc.com/expose-route` annotation, the platform makes the
route and writes the hostname to
`service.nlb.kube-dc.com/route-hostname-status`; for `https` it also issues
a certificate, unless you supply a TLS Secret. For a `LoadBalancer`
Service, the address is in `status.loadBalancer.ingress`. Leave what the
platform writes out of Git. `network.kube-dc.com/external-network-type`
cannot change while the Service stays a `LoadBalancer` that the platform
gives an address, even before the address exists. It can change when the
Service binds to a named EIP or the Project gateway address, whose network
type it then takes. See
[Service exposure](service-exposure.md).

## Next steps

- [Projects](kubernetes-projects.md)
- [Managed Clusters](provisioning-cluster.md)
- [Security restrictions](security-restrictions.md)
- [Service exposure](service-exposure.md)
- [Ways to deploy](deploy-options.md)
- [Manage applications](applications.md)
- [Managed services](managed-services.md)
- [Machine identities](machine-identities.md)
