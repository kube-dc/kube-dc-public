# GitHub Actions runners

Use a managed runner pool to run jobs from one private GitHub.com repository in
your Kube-DC project. GitHub keeps the job queue, logs, and artifacts. Kube-DC
runs each accepted job in a temporary worker pod and stores the runner
registration credential in your project's OpenBao secret store.

The published `github-runner-preview` plan has one concurrent worker slot.
Every Cloud project can find the offer in the catalog. If its CPU or memory
quota cannot host the minimum worker and builder, the card shows the
requirements and a support path; creation becomes available after the project
qualifies.
Kube-DC support owns this no-charge preview. Usage-based billing and paid
service terms will be announced only after measurement and pricing are
qualified. If creation or a job remains stuck, use your account's support
channel or email [support@kube-dc.com](mailto:support@kube-dc.com). Include
the Project and runner name, approximate time, and sanitized status or events;
never send the GitHub token.
New pools include a temporary Buildx builder for Dockerfile builds. Shell jobs
also run on that pool. Docker Compose, Docker daemon commands, job/service
containers, persistent workspaces, and private worker images are unavailable.
The pool does not supply a Kubernetes or application credential to jobs.

## Before you begin

- Use a private GitHub.com repository whose workflow contributors you trust.
- Create a short-lived [fine-grained GitHub personal access token](https://github.com/settings/personal-access-tokens/new)
  restricted to that repository. Grant **Repository permissions → Administration:
  Read and write**. An organization might require approval before the token
  works. See [GitHub's ARC authentication guide](https://docs.github.com/en/actions/how-tos/manage-runners/use-actions-runner-controller/authenticate-to-the-api).
- Have permission to create managed services and to write/read the selected
  project secret. Authorized project Secret readers and the shared ARC controller
  can use the registration token. It never belongs in a workflow or Git commit.
- Check project and organization quota. A Buildx pool reserves an active worker
  and a possible replacement before it accepts jobs. The pool reservation also
  includes listener headroom, which runs outside your Organization quota:

  | Worker size | Pool reservation | Organization worker requests | Organization worker limits | Worker Pod slots |
  | --- | --- | --- | --- | --- |
  | 1 vCPU / 2 GiB | 3.2 vCPU / 6.25 GiB | 3 vCPU / 6 GiB | 6 vCPU / 8 GiB | 2 |
  | 500m CPU / 1 GiB | 2.2 vCPU / 4.25 GiB | 2 vCPU / 4 GiB | 5 vCPU / 6 GiB | 2 |

  Other workloads and idle runner pools also use Organization headroom. If
  there is not enough room, the service is rejected before placement with
  `OrganizationQuotaExceeded`. Reduce worker size or ask Kube-DC support to
  review your quota. A later workload can still consume free quota before a
  worker starts; in that case, the pool reports **Capacity unavailable**.

## Create a pool in the console

1. Open your project, then select **Managed services → New service → GitHub
   Actions runners**.
2. Choose the worker CPU and memory in **Size**. The summary shows the
   reservation. Buildx is enabled for new pools; use **Advanced → Runner
   execution** only if you need a shell-only pool. You can also select a custom
   worker image by digest. These choices are fixed at creation.
3. In **Name & access**, enter the service name and private repository owner/name.
   Select a same-project ManagedSecret or enter a token to save in OpenBao.
   If you just created the Project, wait for its Secrets status to become Ready
   before saving the token. If the first value write returns an OpenBao 403
   while the Project role is enrolling, retry the write to the same
   ManagedSecret; you do not need to create another secret.
4. Review the configuration and create the service. When it reports **Ready**,
   open **Connect** and copy the exact workflow target.
5. Use the **Run a job** example in a trusted branch. Confirm the job succeeds
   in GitHub and the worker count returns to zero afterward.

If the runner appears as **Unavailable** in the engine catalog, this Project
has not passed runner admission. Review its quota and network/secret
prerequisites, then ask [Kube-DC support](mailto:support@kube-dc.com) to help
qualify it. The card does not permit creating a pool until admission succeeds.

Zero worker pods while idle is expected. During a job, Metrics shows the
running worker count and current CPU/memory samples; it keeps recent history
after the pod exits. It does not report GitHub queue length or job result.

## Build and push a Dockerfile

Use **Connect → Build & push** after the pool reports Buildx enabled. The
following workflow uses the job's GitHub token for GHCR; replace the target,
image name, and tag with your own values:

```yaml
name: Build and push image
on: workflow_dispatch
permissions:
  contents: read
  packages: write
jobs:
  image:
    runs-on: RUNNER_TARGET
    steps:
      - uses: actions/checkout@v7
      - name: Sign in to GHCR
        env:
          GH_TOKEN: ${{ secrets.GITHUB_TOKEN }}
        run: echo "$GH_TOKEN" | docker login ghcr.io -u "$GITHUB_ACTOR" --password-stdin
      - run: docker buildx build --push -t ghcr.io/OWNER/IMAGE:TAG .
```

`RUNNER_TARGET` is the exact value in **Connect**. `OWNER/IMAGE:TAG` names a
package that this repository's `GITHUB_TOKEN` may publish. If package access
is denied, check repository/package permissions in GitHub. The pool's OpenBao
registration token does not authenticate image pushes. For a validation build
without registry publication, export an OCI archive with Buildx instead.

The builder is selected when the worker starts. Do not add a Docker daemon or
`docker-container` setup step. Use one `docker buildx build --push` command in
place of separate `docker build` and `docker push` commands. Docker Compose
and `services:` in workflow jobs require a different runtime and are not
supported by this plan.

## Create the same pool with custom resources

Declarative creation uses the same plan, credential permissions, and project
boundary. Your GitOps identity needs permission to create the service and read
both the named ManagedSecret and its synchronized Kubernetes Secret. Keep the
token value outside Git. Apply this ManagedSecret after replacing the namespace:

```yaml
apiVersion: security.kube-dc.com/v1alpha1
kind: ManagedSecret
metadata:
  name: github-runner-auth
  namespace: PROJECT_NAMESPACE
spec:
  type: opaque
  sync:
    enabled: true
    targetSecretName: github-runner-auth
    refreshInterval: 1m
    keys: [github_token]
  rotation:
    enabled: false
```

Write the token to OpenBao from a mode-0600 file outside Git. The file must
contain only the token: a trailing newline or spaces make it invalid. The CLI
uses your authenticated project access; replace `PROJECT_NAMESPACE` and
`TOKEN_FILE`:

```sh
kube-dc secrets put github-runner-auth --namespace PROJECT_NAMESPACE \
  --from-file=github_token=TOKEN_FILE
```

Apply the service after replacing `PROJECT_NAMESPACE`, `OWNER`, and
`REPOSITORY`. Omitting `parameters.buildMode` selects Buildx for a newly created
pool; set it to `none` explicitly for a shell-only pool:

```yaml
apiVersion: services.kube-dc.com/v1alpha1
kind: ManagedService
metadata:
  name: github-ci
  namespace: PROJECT_NAMESPACE
spec:
  classRef: {name: github-runner}
  planRef: {name: github-runner-preview}
  engineVersion: 2.337.0
  compute: {cpu: "1", memory: 2Gi}
  storage: {mode: None, size: "0"}
  topology: {instances: 1}
  placement: {mode: ProviderShared}
  connectivity: {classRef: {name: tenant-native}}
  deletionPolicy: Delete
  deletionProtection: true
  credentialInputs:
    - purpose: github-auth
      managedSecretRef: {name: github-runner-auth}
      secretRef: {name: github-runner-auth}
      authorizationRevision: "1"
      github: {owner: OWNER, repository: REPOSITORY}
```

After applying, wait for `Ready` and read the exact target:

```sh
kubectl wait managedservice/github-ci -n PROJECT_NAMESPACE \
  --for=condition=Ready --timeout=10m
kubectl get managedservice github-ci -n PROJECT_NAMESPACE \
  -o jsonpath='{.status.engineDetails.workflowTarget}'
```

You can reapply the same manifests without creating another pool. If the
credential sync is still pending, the service waits. Rotate the token by
writing a new OpenBao version under the same ManagedSecret. Replacing the
ManagedSecret object itself requires a new `authorizationRevision` and fresh
permission checks. The console shows the same service when it is available.

## Customize workers and migrate an existing pool

Select 500m–4 CPU and 1–8 GiB of memory for each worker at creation. A custom
worker image must be a public `ghcr.io` or `docker.io` Linux amd64 image named
by `sha256` digest. It must run as UID/GID 1001 and provide the ARC runner
entrypoint/workspace. A Buildx pool's image must also contain Docker CLI and
the Buildx plugin. Build tools into the image; workflow steps run without sudo.
Worker workspace is temporary, capped at 2 GiB. The worker container has a
4 GiB ephemeral-storage limit. The Buildx builder has a separate 8 GiB
temporary state volume and 10 GiB ephemeral-storage limit. Worker CPU/memory
sizing does not raise those disk limits. If a Dockerfile build exceeds them,
reduce its temporary layers or contact support. Jobs have a 60-minute lifetime.

`buildMode`, image, and size are fixed at creation. To move an existing
shell-only pool to Buildx, let active jobs finish, delete the old pool with
normal draining, retain its OpenBao ManagedSecret, and create a Buildx pool.
Copy the new `runs-on` target into affected workflows and run a test job before
resuming regular builds. The preview plan permits one pool per Project; a
replacement can be created only after the old service finishes deleting.
If the replacement fails, create a shell-only pool again and use its newly
reported target, or temporarily route the workflow to another runner you control.

## Troubleshoot and delete

| Symptom | Check |
| --- | --- |
| Service waits for a credential | Open **Secrets** and check synchronization and token repository access. |
| A second pool reports `PlanQuotaExceeded` | Wait for the old pool to finish draining and deleting, then retry the new pool. The preview permits one pool per Project. |
| Service reports a GitHub repository verification refusal | Check that the repository is private and the fine-grained token targets that exact repository, has **Administration: Read and write**, has not expired, and has any required organization approval. Save a corrected token in the same ManagedSecret; the controller retries. |
| Service reports that `github_token` contains whitespace or control characters | Replace the value with a file containing only the token, without a trailing newline or spaces. Save it to the same ManagedSecret; the pool retries. |
| Job remains queued | Match `runs-on` to **Connect**, check **Ready**, available worker slots, and starting-worker events. GitHub owns the actual queue. |
| Service reports **Capacity unavailable** | A worker pod could not start. Check both project and organization CPU/memory quota against the pool's Settings reservation. Reduce worker size or ask Kube-DC support to review available capacity, then rerun the GitHub job. |
| Service is **Rejected** with `OrganizationQuotaExceeded` | The Organization cannot reserve the active and replacement workers for this pool. Reduce worker size, free capacity held by another pool, or ask Kube-DC support to review quota; then retry creation. |
| Buildx fails to start | Check project and organization quota, builder image pull, and temporary disk; compare the Settings reservation with the available limits. |
| `docker build` or Compose fails | Use `docker buildx build --push` for a Dockerfile, or choose a different runtime for Compose/services. |
| Metrics show no current CPU | An idle pool has no worker pod to measure. Check the last observation and historical graph; a stale or unavailable message points to collection trouble. |

To delete a declarative pool, confirm the specific service, clear deletion
protection, then delete the ManagedService:

```sh
kubectl annotate managedservice github-ci -n PROJECT_NAMESPACE \
  services.kube-dc.com/confirm-delete=github-ci --overwrite
kubectl patch managedservice github-ci -n PROJECT_NAMESPACE --type=merge \
  -p '{"spec":{"deletionProtection":false}}'
kubectl delete managedservice github-ci -n PROJECT_NAMESPACE
```

Deletion stops new workers and drains active work for up to 60 minutes before
ARC cleanup. The source ManagedSecret remains. Revoke the registration token
in GitHub and delete the retained project secret when you no longer use it.
