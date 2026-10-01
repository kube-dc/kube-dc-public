# GitHub Actions runners

Use a managed runner pool to run jobs from one private GitHub.com repository in
your Kube-DC project. GitHub keeps the job queue, logs, and artifacts. Kube-DC
runs each accepted job in a temporary worker pod and stores the runner
registration credential in your project's OpenBao secret store.

The published `github-runner-preview` plan has one concurrent worker slot.
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
- Check the project quota: the default 1 vCPU/2 GiB worker profile reserves
  3.2 vCPU and 6.25 GiB for the active/replacement workers, builders, and
  listener headroom. A Buildx builder requests 500m CPU/1 GiB per job pod;
  the reservation includes a second builder for replacement. Worker sizing
  changes this total.

## Create a pool in the console

1. Open your project, then select **Managed services → New service → GitHub
   Actions runners**.
2. Choose the worker CPU and memory in **Size**. The summary shows the
   reservation. Buildx is enabled for new pools; use **Advanced → Runner
   execution** only if you need a shell-only pool. You can also select a custom
   worker image by digest. These choices are fixed at creation.
3. In **Name & access**, enter the service name and private repository owner/name.
   Select a same-project ManagedSecret or enter a token to save in OpenBao.
4. Review the configuration and create the service. When it reports **Ready**,
   open **Connect** and copy the exact workflow target.
5. Use the **Run a job** example in a trusted branch. Confirm the job succeeds
   in GitHub and the worker count returns to zero afterward.

Zero worker pods while idle is expected. The Metrics page keeps recent CPU and
memory history. It does not report GitHub queue length or job result.

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

Write the token to OpenBao from a mode-0600 file outside Git. The CLI uses your
authenticated project access; replace `PROJECT_NAMESPACE` and `TOKEN_FILE`:

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
resuming regular builds. A project may have one pool, so this replacement
interrupts that pool. If the replacement fails, create a shell-only pool again
and use its newly reported target, or temporarily route the workflow to another
runner you control.

## Troubleshoot and delete

| Symptom | Check |
| --- | --- |
| Service waits for a credential | Open **Secrets** and check synchronization and token repository access. |
| Job remains queued | Match `runs-on` to **Connect**, check **Ready**, available worker slots, and starting-worker events. GitHub owns the actual queue. |
| Buildx fails to start | Check project quota, builder image pull, and temporary disk; compare the Settings reservation with your project limit. |
| `docker build` or Compose fails | Use `docker buildx build --push` for a Dockerfile, or choose a different runtime for Compose/services. |
| Metrics show no current CPU | An idle pool has no worker pod to measure. Check the last observation and historical graph; a stale or unavailable message points to collection trouble. |

To delete a declarative pool, set `deletionProtection: false`, apply the
change, then confirm and delete the ManagedService:

```sh
kubectl annotate managedservice github-ci -n PROJECT_NAMESPACE \
  services.kube-dc.com/confirm-delete=github-ci --overwrite
kubectl delete managedservice github-ci -n PROJECT_NAMESPACE
```

Deletion stops new workers and drains active work for up to 60 minutes before
ARC cleanup. The source ManagedSecret remains. Revoke the registration token
in GitHub and delete the retained project secret when you no longer use it.
