---
draft: true
---

# Container registry

A private OCI registry for your project's images. Kube-DC offers it as the
class `registry` (zot). It stores no password: CI and people sign in with
your organization's short-lived tokens, and Kube-DC keeps the pull Secret of
each listed project fresh. Use it with [Deploy from GitHub](deploy-from-github.md)
or any CI that can sign in to Kube-DC.

| Plan | Shape | Storage | Operations |
|---|---|---|---|
| `registry-development` | One replica, no failover | A volume (10Gi default, up to the plan maximum) | Resize, Expand storage |
| `registry-production` | Two members on different nodes | A bucket in your project's object storage | Resize, Expand storage (a bucket the registry created) |

There are no backups: images are build artifacts, so rebuild them from source.
There is no management fee: you pay for what the registry uses (see
[Pricing](#pricing)).
In Production each repository is served by one member. While that member
restarts (about 20 seconds) its repositories are briefly unavailable; the
data stays in the bucket.

## Create a registry

In the console: **Managed services → Create → Container registry**, or with
an AI app: *"Create a development container registry named images"*. YAML:

```yaml
apiVersion: services.kube-dc.com/v1alpha1
kind: ManagedService
metadata:
  name: images
  namespace: my-project
spec:
  classRef: {name: registry}
  planRef: {name: registry-development}   # or registry-production
  placement: {mode: ProviderShared}
  connectivity: {classRef: {name: tenant-native}}
  engineVersion: "2.1.22"
  topology: {instances: 1}                # 2 for registry-production
  deletionPolicy: Delete
  parameters:
    projects: [my-previews]               # optional, see below
```

When it is Ready, its address is `https://<name>-<project namespace>.<domain>`,
for example `images-acme-dev.example.com`. The console shows it as the
service's `https` endpoint.

### Projects and rights

Each project gets the path `<project>/` in the registry. The registry's own
project is always included; list more projects of the same organization in
`parameters.projects` (for example the project that runs pull request
previews).

| Who | Can |
|---|---|
| Admins and developers of a listed project | Push and pull `<project>/…` |
| Every other member of a listed project | Pull `<project>/…` |
| Organization admins | Push and pull every project's path |
| Anyone else, and every path outside `<project>/` | Nothing |

Listing a project grants nothing by itself: only a token that already carries
that project's right gets in.

### A bucket for Production

`registry-production` creates its own bucket `<name>-images` in your project
(billed like any bucket, capped at the registry's storage size). To use a
bucket you already have, name its claim:

```yaml
  parameters:
    bucket: {claimName: my-bucket}   # an ObjectBucketClaim on ceph-bucket
```

The registry marks the bucket with the annotation
`services.kube-dc.com/registry-bucket-owner`. It never deletes a bucket it
did not create; deleting the registry with `Delete` releases it. Only one
registry can use a bucket. Production is offered only where the object store
is available.

## Push

**From GitHub Actions (no stored token).** The machine identity's token is
exchanged for a registry token, then `docker login` uses it. The
[Deploy from GitHub](deploy-from-github.md) page and `connect_repository`
generate this for you (`deploy/registry-login.sh`):

```bash
deploy/registry-login.sh          # needs KUBE_DC_REGISTRY, KUBE_DC_TOKEN_URL,
                                  # KUBE_DC_CLIENT_ID, KUBE_DC_AUDIENCE
docker push images-my-project.<domain>/my-project/app:$GITHUB_SHA
```

Registry tokens live 15 minutes: sign in right before pushing. A token for
the Kubernetes API is refused by the registry, and a registry token is
refused by Kubernetes.

**From your machine.** Open the registry in the console and use **docker
login** in its *Push and pull* card (or the AI tool `registry_login`). It
shows a command valid for 15 minutes:

```bash
echo '<token>' | docker login images-my-project.<domain> --username kube-dc --password-stdin
```

**Builds on push.** Set `build.kube-dc.com/image` to
`<registry host>/<project>/<app>` and no push Secret is needed; see
[Deploy from GitHub](deploy-from-github.md).

## Pull

Kube-DC writes the Secret `kdc-registry-<name>` in every listed project,
refreshes it every 5 minutes and adds it to the project's `default`
ServiceAccount. Pods that use the default ServiceAccount and set no
`imagePullSecrets` of their own pull without any change. A pod that sets its
own `imagePullSecrets` replaces the ServiceAccount's, so add
`kdc-registry-<name>` to that list.

## Operations

| Operation | Development | Production |
|---|---|---|
| **Resize** (CPU and memory within the plan bounds, 100m–2 CPU and 256Mi–4Gi) | The single replica restarts: a short outage | Per member. The members restart one at a time; while a member restarts (about 20 seconds) the repositories it serves are unavailable, the other member keeps serving its own |
| **Expand storage** (never shrinks) | Grows the volume online, up to 50Gi | Raises the maximum size of the bucket the registry created, up to 500Gi; nothing restarts. A bucket you named keeps the size you give it: change `spec.additionalConfig.maxSize` on your own claim |

Both are in the console (the service's operations) and as `ManagedServiceOperation`
objects. A registry created before these operations existed gets them once the
platform operator adopts the current catalog for it.

## Metrics

The console's health tiles show CPU, memory, the volume (Development), and
from the registry itself: requests per second, server errors (5xx), image
pushes and pulls per second, repositories and image storage. Production
stores images in a bucket, which zot does not measure: its *Repositories* and
*Image storage* tiles show no data; the bucket's size is in your object
storage usage. A registry created before metrics existed shows only CPU,
memory and volume until the platform operator adopts the current catalog for
it (the members restart once).

The metrics stay inside the platform: the registry's public address answers
`/metrics` with 404.

## Pricing

No management fee. You pay for the resources the registry uses, at your
project's rates:

| Plan | Charged as |
|---|---|
| Development | CPU and memory of the replica, plus its volume (block storage, by provisioned size) |
| Production | CPU and memory of both members, plus the bucket as object storage, by the bytes it holds (not by its maximum size) |

Each member also runs a small proxy (50m CPU, 64Mi memory) that serves the
public address; it is charged as project CPU and memory. The price preview
when you create a registry shows CPU, memory and (Development) the volume;
it lists the bucket's object storage and the proxy as costs it does not
include.

## Delete

`Delete` removes the registry, its volume or the bucket it created, and the
pull Secrets (and their entries on the `default` ServiceAccounts). `Retain`
keeps the volume or bucket. Confirm a `Delete` with the annotation
`services.kube-dc.com/confirm-delete=<name>`, or in the console.
