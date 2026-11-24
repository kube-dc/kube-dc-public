# Deploy from GitHub

Where your provider offers builds, the platform builds a GitHub repository on
every push to a branch and deploys it as an [Application](applications.md).
Every pull request can get its own preview, with its own database if you
want one. Nothing is added to your repository.

You need:

- the developer role in the Project;
- to be an admin of the GitHub repository;
- the platform's GitHub App installed on the repository (the console offers
  to install it);
- a container registry for the built images: the Project's
  [managed registry](managed-services-registry.md), or one of your own such as
  GHCR.

## Create the app in the console

1. Open your Project, then **Applications**, and click **New application**.
2. Click **Continue with GitHub** and sign in. The list shows the
   repositories you administer that have the GitHub App installed. If the App
   is not installed yet, the list offers to install it; to add more
   repositories, click **Install the App on more repositories**.
3. Pick a repository and click **Continue**. Kube-DC sets the repository
   variable `KUBE_DC_ORGANIZATION` to your organization (see
   [Allow your organization](#allow-your-organization-on-the-repository)). If
   the variable names another organization, the console asks before it
   replaces it.
4. Check the app name (it must start with a letter and not be in use) and
   the port. The branch is shown when the repository has more than one (the
   default branch is selected), and the **Address** when your organization
   has a domain to offer. Choose whether pull requests get previews (on by
   default), then click **Create app**.

The page follows the registry setup, the first build and the rollout. The app
is ready when its pods are available and its address is live. It is an
Application where your provider has
[turned that on](applications.md#apps-created-before-applications). After
that, every push to the branch builds and deploys a new release. Manage the
app under **Applications** (see [Manage applications](applications.md)).

What **New application** sets up:

| | |
|---|---|
| Build | The `Dockerfile` at the repository root. Without one, the platform detects the language and builds with [Railpack](https://railpack.com) (Node.js, Python, Go, PHP, static sites and more) |
| Port | The port you choose (it suggests the one the Dockerfile `EXPOSE`s, else `8080`); the app also gets it as `$PORT` |
| Registry | The Project's managed registry. A Project without one gets a development registry (billed like any managed service); the first build waits a few minutes for it |
| Address | Your organization's default domain (`<app>.<domain>`) if it has one, else the platform address |
| Previews | `<app>-pr-<n>` for every pull request, in the same Project, removed when the pull request closes |

If the GitHub App is later uninstalled from the repository, the app keeps
running, and its **Source** step (the `SourceResolved` condition) says why new
pushes are not built. Installing the App again clears it.

## Or write the Application

The same app as a manifest, for `kubectl apply` or a GitOps repository:

```yaml
apiVersion: apps.kube-dc.com/v1alpha1
kind: Application
metadata:
  name: notes
spec:
  source:
    github:
      repository: acme/notes
      branch: main
  image:
    registry:
      managedService:
        name: registry          # the managed registry in this Project
  run:
    port: 8080
  previews:
    enabled: true
```

Set the repository variable `KUBE_DC_ORGANIZATION` first (see
[the next section](#allow-your-organization-on-the-repository)); without it
the platform does not build the repository.

| Field | Value |
|---|---|
| `source.github.repository` | `owner/repo` |
| `source.github.branch` | optional: the branch whose pushes are built (default: the repository's default branch) |
| `source.github.contextDir` | optional: a directory of the repository to build from |
| `image.registry.managedService` | a managed registry: `name`, and `project` when it is in another Project that lists yours |
| `image.repository`, `image.pushSecretRef` | instead, your own registry: a path without a tag such as `ghcr.io/<owner>/<app>`, and the Secret to push with (see [Where images go](#where-images-go)) |
| `build.strategy` | `Auto` (default: the Dockerfile if there is one, else Railpack), `Dockerfile` or `Railpack` |
| `build.dockerfile` | optional: the Dockerfile's path, relative to `contextDir` |
| `build.args` | optional: Dockerfile `ARG` values, for example `VITE_API_URL` (with Railpack, environment variables of every build step). Not for secrets: build arguments stay in the image history |
| `build.secretRefs` | optional: Secrets of the Project; the Dockerfile reads each key with `RUN --mount=type=secret,id=<key>` (for example `NPM_TOKEN`; with Railpack, an environment variable), and nothing is stored in the image |
| `build.automatic` | optional: `false` builds only when asked (**Redeploy**, `kube-dc app rebuild`, or an `ApplicationBuild`) |
| `run.port` | the port the app listens on (default `8080`). Only a Railpack build gets it as `$PORT`; with a Dockerfile, set it to the port your app uses |
| `previews` | optional: [previews of pull requests](#previews) |

## Allow your organization on the repository

One GitHub App serves every organization on the platform. The repository
variable `KUBE_DC_ORGANIZATION` is the repository admins' consent that *your*
organization may build the repository; without it, anyone who knows a
repository name could ask the platform to build its code. Only repository
admins can set repository variables.

**New application** sets it for you. To set it yourself:

```bash
gh variable set KUBE_DC_ORGANIZATION -R owner/repo -b <organization>
```

## Where images go

| | Managed registry (recommended) | Your own registry (for example GHCR) |
|---|---|---|
| Registry | A [Kube-DC container registry](managed-services-registry.md) in your Project, or one in another Project that lists yours | GHCR or another registry you control |
| Pushing | Nothing to set up | A push token saved as a Secret in the Project |
| Pulling | Kube-DC keeps the pull Secret fresh | Public images need nothing; private ones are pulled with the push Secret |

**A GitHub token for GHCR.** GitHub does not let apps push to GHCR, so the
platform needs a classic personal access token with the `write:packages`
scope:

1. Create the token on GitHub.
2. Save it in the Project as a `kubernetes.io/dockerconfigjson` Secret, for
   example `notes-ghcr-push`, and name it in `image.pushSecretRef`. The
   console's **Applications → Connect GitHub** page has a form that saves it
   for you. Developers usually cannot create Secrets: ask a Project admin.

Never paste the token into a chat or an AI app. Without the Secret, an
Application pushing to GHCR is accepted, but every build fails at the push.

```yaml
spec:
  image:
    repository: ghcr.io/acme/notes
    pushSecretRef: { name: notes-ghcr-push }   # also used to pull
```

## With an AI app

Connect an AI app to the Project's [hosted MCP server](mcp-for-administrators.md)
and ask it to deploy the repository. It calls `deploy_app` with `buildFrom`,
which creates the same Application. It can also create a registry
(`create_managed_service`) and give you a `docker login` command for one
(`registry_login`). On **Applications → Connect GitHub**, the console builds
a prompt from your choices for you to paste. Skip its **Set it up by hand**
steps for builds on push, which do not make an Application: use **New
application**, the prompt or the manifest above.

## Builds

A build runs in a BuildKit pod in your Project. Repeated builds reuse a layer
cache, so rebuilding an unchanged app is fast. Each build is an
`ApplicationBuild`: the **Builds & releases** tab and `kubectl get appbuild`
list them, and GitHub shows the check run `kube-dc` on the commit.

- A failed build is named on the app's **Build** step (the `Built`
  condition). The running release keeps serving.
- A build that fails in your code (the build fails, there is nothing to
  build, or it runs past 60 minutes) is not retried: fix it and push, or
  click **Build again**.
- A build the cluster could not run (the source or registry unreachable, its
  node gone) is tried again at once, up to 3 times. After that, or when there
  is no capacity or Project quota, it is tried again after 15 minutes, then
  after twice as long each time, up to every 2 hours.
- Builds count against the Project quota: each attempt requests 200m CPU and
  1Gi of memory, and may use up to 2 CPU, 2Gi of memory, 26Gi of disk and 60
  minutes.
- **Build again** on the **Builds & releases** tab, or
  `kube-dc app rebuild <app>`, builds the branch's latest commit again.

A bad release can be rolled back; while an app is rolled back, new pushes are
built and recorded but not deployed until you resume. See
[Roll back an application](rollback.md).

## Previews

With `previews.enabled: true`, every open pull request from a branch of the
repository gets a preview, an Application named `<app>-pr-<n>`. Pull requests
from forks get none.

```yaml
spec:
  previews:
    enabled: true
    hours: 72              # how long a preview lives after its last build (default)
    project: notes-review  # optional: another Project of the organization
    buildArgs: { VITE_API_URL: "https://api-test.example.com" }   # over build.args
```

- A preview builds the pull request's head and runs one replica with the
  app's settings. A data volume becomes empty scratch storage.
- Closing the pull request removes the preview and its database. A new
  commit on the pull request brings back an expired or removed preview.
- The **Previews** tab lists them. While previews are on, **Create preview**
  makes one again for a pull request whose preview expired or was removed. A
  preview's **Settings** tab offers **Keep at least 24 hours from now**,
  **Keep at least 72 hours from now** and **Remove preview**.
- At most 20 previews run per Project.

### Secrets in previews

A preview never reads the app's own Secrets for its variables and build
secrets (it does use the app's registry push and pull Secrets). Where the app
reads a Secret `<name>`, its preview reads `<name>-preview` when that exists
in the preview's Project, for example `stripe-preview` with Stripe's test
key. In a separate previews Project, a Secret named `<name>` there is used
too.

Variables from other Secrets are left out of the preview. A build secret
with no preview copy is left out of the preview's build, which then usually
fails at its `RUN --mount` step (the preview's **Build** step shows it).

### Databases in previews

For an app that uses a managed database, each preview can get its own, so a
pull request never writes to production data. The database is
`<app>-pr-<n>-db`, removed with the preview, and its credentials are in the
Secret `<app>-pr-<n>-db-owner`.

```yaml
spec:
  previews:
    enabled: true
    database:
      from: notes-db       # a managed database in the preview's Project
      mode: Branch         # or Restore (the latest backup, the default)
```

| Setting | The preview's database |
|---|---|
| `database.plan`, for example `postgresql-development` | A new, empty database from that plan |
| `database.from` with `mode: Restore` | A copy of the service's latest backup (takes minutes; data as of that backup) |
| `database.from` with `mode: Branch` | A branch of the live service: its data at the moment the preview deploys (about two minutes) |

`database.secretName` names the Secret the app reads its database
credentials from (default `<app>-db-owner`); in the preview, those variables
read `<app>-pr-<n>-db-owner` instead. With an AI app, the `deploy_app`
options are `buildFrom.previewDatabase`, `buildFrom.previewDatabaseFrom` and
`buildFrom.previewDatabaseMode`, together with `buildFrom.previewProject`.

A copy (`from`) needs a [copy grant](#copy-grants); without one, the preview
gets an empty database of the source's plan, and its `DatabaseReady`
condition says why. The source must be in the preview's Project.

A branch starts from an online snapshot, so the source keeps serving. If the
source can't be branched (its plan doesn't allow it, the storage has no
snapshots, or the service was created before branching existed), the preview
restores the latest backup instead where the grant allows `Restore`, and
otherwise gets an empty database.

A branch is a separate database: writes to it never reach the source. Only
the branch's own owner can log in to it; the other logins copied from the
source are locked, so a production password does not open a preview. While
branches of a service exist, deleting the service, restoring it in place and
upgrading its major version are refused: close the pull requests, or remove
their previews, first.

Problems with a preview's database are in the preview's `DatabaseReady`
condition (`kubectl describe app <app>-pr-<n>`).

#### Copy grants

A Project admin of the source's Project allows copies with a
ServiceCopyGrant:

```yaml
apiVersion: services.kube-dc.com/v1alpha1
kind: ServiceCopyGrant
metadata:
  name: notes-previews
  namespace: <org>-<project>          # the source's Project
spec:
  source: { name: notes-db, uid: <uid of notes-db> }   # a recreated service is not granted
  app: notes                          # the Application whose previews may copy it
  appNamespace: <org>-<app project>   # omit when it is this Project
  modes: [Branch, Restore]
```

`modes` lists what the previews may use: `Branch` (the live data) and
`Restore` (the latest backup, also the fallback when a branch fails). A grant
cannot be changed after it is created; create a new one instead.

#### Masking copied data

A copy carries production data, personal data included, into previews that
run pull-request code. Add `masking` to the grant to change the data in every
copy before anyone can connect to it:

```yaml
apiVersion: v1
kind: ConfigMap
metadata: { name: notes-masking }
data:
  mask.sql: |
    UPDATE users SET email = 'user' || id || '@example.invalid', name = 'User ' || id;
    UPDATE payments SET card_last4 = '0000';
    TRUNCATE sessions;
---
apiVersion: services.kube-dc.com/v1alpha1
kind: ServiceCopyGrant
metadata: { name: notes-previews }
spec:
  source: { name: notes-db, uid: <uid of notes-db> }
  app: notes
  modes: [Branch, Restore]
  masking:
    configMap: notes-masking
    key: mask.sql
    sha256: <sha256 of the script>   # kubectl get cm notes-masking -o jsonpath='{.data.mask\.sql}' | sha256sum
```

- The script is plain SQL (no `psql` commands), at most 64 KiB. It runs as
  the database's owner while the copy is still closed to everyone else, in
  one transaction (unless it manages its own), with a 30-minute statement
  timeout. Write it so it can run twice.
- The owner must own every table in the database, and no table may use
  `FORCE ROW LEVEL SECURITY`; otherwise the copy is refused rather than
  half-masked. Copied logical replication subscriptions are disabled.
- The digest pins the script: changing the ConfigMap afterwards fails the
  copy, so a new script needs a new grant.
- Materialized views the owner owns are rebuilt from the masked tables.
  Large objects are not in any table: the script must remove them
  (`lo_unlink`), or the copy is refused.
- When any grant for a service and app masks, every copy for them masks.
- If the script fails, the copy is deleted and the preview has no database.
  Its `DatabaseReady` condition gives the SQL error (code and message, never
  row values). Fix the script, create a new grant, then close and reopen the
  pull request.
- A copy keeps backups like its source, in the Project's own object store,
  so they hold its data from before masking. Only members of the Project can
  read them, as with the source's own backups.
- Masking protects data from the preview's public address and from
  pull-request code. It does not hide data from members of the Project, who
  can read the source anyway.

## Build in your own GitHub Actions workflow instead

Use your own workflow when a deploy needs tests or custom steps before it
ships. In the console, open **Applications → Connect GitHub** and choose
**In a GitHub Actions workflow** under **Builds**; the page builds a prompt
for an AI app (`connect_repository`, organization admins only) and shows the
same steps under **Set it up by hand**.

The setup adds `.github/workflows/deploy.yml` and `preview.yml` to the
repository and creates [machine identities](machine-identities.md) the
workflow signs in with, so no secret is stored in GitHub. The workflow builds
on [managed runners](managed-services-github-runners.md) or GitHub-hosted
runners and deploys a plain Deployment and Service, not an Application, so
the releases and rollback of Applications do not apply to it; `preview.yml`
makes its previews. Use one way per app: both would build and deploy every
push twice.

A workflow's previews get a database through the `connect_repository`
options `previewDatabase`, `previewDatabaseFrom`, `previewDatabaseMode` and
`previewDatabaseGrant`, or the repository variables `KUBE_DC_PREVIEW_DATABASE`,
`KUBE_DC_PREVIEW_DATABASE_FROM`, `KUBE_DC_PREVIEW_DATABASE_MODE` and
`KUBE_DC_PREVIEW_DATABASE_GRANT`. A workflow's copies are made by its machine
identity, and are refused unless they name a copy grant. That identity works
in the source's Project, so pull-request code can read what it can, the
source's credentials included: use builds by the platform when pull requests
may come from people who must not see production data.
