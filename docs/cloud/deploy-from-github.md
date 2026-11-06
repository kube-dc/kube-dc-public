# Deploy from GitHub

Build and deploy a GitHub repository on every push, with a preview for every
pull request. In the console open your project, then **Overview → Deploy from
GitHub** (or the *Deploy from GitHub* card on the project summary). The page
fills in every value it knows; you type only the repository and the app name.

## 1. Install the GitHub App and allow your organization

1. **Install the GitHub App** on the repository (button on the page). It
   reports deployments and check runs on your commits, builds on push and
   removes previews when a pull request closes.
2. **Allow this organization.** Sign in to GitHub when asked; Kube-DC checks
   that you are an admin of the repository and sets the repository variable
   `KUBE_DC_ORGANIZATION` to your organization.

The page checks both and shows what is missing. Without the button you can
set the variable yourself:

```bash
gh variable set KUBE_DC_ORGANIZATION -R owner/repo -b <organization>
```

**Why the variable:** one GitHub App serves every organization on the
platform. The variable is the repository admins' consent that *this*
organization may build the repository; without it, anyone who knows a
repository name could ask the platform to build its code. Only repository
admins can set repository variables.

## 2. Choose where images go

Under **Where images go**, select **Your own registry**. Images then go to a
registry you control, for example GHCR. This path behaves as follows:

| Topic | Your own registry |
|---|---|
| Registry | GHCR or another registry you control |
| Secrets in GitHub | None for workflows (GitHub's `GITHUB_TOKEN`); builds on push need a personal token saved in the project |
| Pulling | Public images need nothing; private ones need a pull Secret |

**Builds on push: the GitHub token.** GitHub does not let
apps push to GHCR, so builds on push need a classic personal access token:

1. **Create a token on GitHub** (button on the page; scope `write:packages`).
2. **Save it in this project** with the form on the page. It becomes a
   `kubernetes.io/dockerconfigjson` Secret (default `<app>-ghcr-push`) and is
   selected as the push Secret. Developers usually cannot create Secrets: ask
   a project admin.

Never paste the token into a chat or an AI app.

## 3. Choose how builds run

| | On push, by the platform | In a GitHub Actions workflow |
|---|---|---|
| Where it builds | A BuildKit pod in your project, started by the push | A workflow job on [managed runners](managed-services-github-runners.md) or GitHub-hosted runners |
| In your repository | Nothing; the app's Deployment carries `build.kube-dc.com/*` annotations | `.github/workflows/deploy.yml` and `preview.yml` |
| Steps | `docker build` of the Dockerfile, push, roll out | Anything: tests, migrations, several images |
| Feedback | Check run `kube-dc` and a GitHub deployment on the commit | The workflow's log in the Actions tab |
| Previews | `<app>-pr-<n>` in the previews project, removed when the PR closes | Done by `preview.yml` |

Use one per app; both would build and deploy every push twice. Platform
builds suit "deploy my Dockerfile"; repeated builds reuse a layer cache, so
a rebuild of an unchanged app is faster. Use a workflow when you need tests or
custom steps.

## 4. Let your AI app do it (or do it by hand)

Connect an AI app to the project (MCP), then copy the prompt the page builds
from your choices. It uses:

- `deploy_app` with `buildFrom` for builds on push;
- `connect_repository` for a workflow (it opens a pull request with the
  workflows and sets the repository variables; organization admins only);
- `create_managed_service` for a runner pool.

*Set it up by hand* on the page shows the same steps as commands and YAML.
For a workflow it fills the repository variables for every project member,
including the Kubernetes API address (`KUBE_DC_API_SERVER`) and its CA
(`KUBE_DC_CA_CERT`). The keyless
machine identity's client id and audience are filled for organization admins;
others see placeholders to ask an admin for.

## Builds on push: the annotations

| On the Deployment | Value |
|---|---|
| label `build.kube-dc.com/on-push` | `"true"` |
| `build.kube-dc.com/repository` | `owner/repo` |
| `build.kube-dc.com/image` | registry path without a tag, for example `ghcr.io/<owner>/<app>` |
| `build.kube-dc.com/push-secret` | the Secret from step 2 |
| `build.kube-dc.com/previews` | optional: a project for pull request previews |
| `build.kube-dc.com/build-args` | optional: Dockerfile `ARG` values as JSON, for example `{"VITE_API_URL": "https://api.example.com"}`. Not for secrets: build arguments stay in the image history |
| `build.kube-dc.com/preview-build-args` | optional: JSON that overrides `build-args` key by key for preview builds |
| `build.kube-dc.com/build-secrets` | optional: comma-separated Secrets of the project; the Dockerfile reads each key with `RUN --mount=type=secret,id=<key>` (for example `NPM_TOKEN`), and nothing is stored in the image |
| `build.kube-dc.com/preview-database` | optional: a plan such as `postgresql-development`; every preview gets a new, empty database (see [Databases in previews](#databases-in-previews)) |
| `build.kube-dc.com/preview-database-from` | optional, instead: a managed service in the previews project; every preview gets a copy of it when a [ServiceCopyGrant](#copy-grants) allows it, else an empty database of its plan |
| `build.kube-dc.com/preview-database-mode` | optional, with `preview-database-from`: `restore` (default) or `branch` |
| `build.kube-dc.com/preview-database-secret` | optional: the Secret the app reads its database credentials from, default `<app>-db-owner`; the preview reads its own `<app>-pr-<n>-db-owner` there instead |

### Secrets in previews

A preview never reads the app's own Secrets. Where the app reads a Secret
`<name>` (environment variables or a build secret), its preview reads
`<name>-preview` when that exists in the preview's project, for example
`stripe-preview` with Stripe's test key. In a separate previews project, a
Secret named `<name>` there is used too. Variables from other Secrets are
left out of the preview, and a preview build that needs a missing build
secret is not started (the reason is in `build.kube-dc.com/last-error`).

### Databases in previews

Each pull request preview can get its own database (`<app>-pr-<n>-db`,
removed with the preview; its credentials are in the Secret
`<app>-pr-<n>-db-owner`). With builds on push, set the `preview-database`
annotations above (`deploy_app` `buildFrom.previewDatabase`,
`previewDatabaseFrom`, `previewDatabaseMode`). With a workflow, set the
options on `connect_repository`, or the matching repository variables:

| Option | Variable | The preview's database |
|---|---|---|
| `previewDatabase: postgresql-development` | `KUBE_DC_PREVIEW_DATABASE` | A new, empty database from that plan |
| `previewDatabaseFrom: <service>` | `KUBE_DC_PREVIEW_DATABASE_FROM` | A copy of the service's latest backup (takes minutes; data as of that backup) |
| also `previewDatabaseMode: branch` | `KUBE_DC_PREVIEW_DATABASE_MODE=branch` | A branch of the live service: a copy of its data as of the moment the preview deploys |
| also `previewDatabaseGrant: <grant>` | `KUBE_DC_PREVIEW_DATABASE_GRANT` | The [ServiceCopyGrant](#copy-grants) the copy names (required for copies by the workflow) |

The source service must be in the preview project. A branch starts from
an online snapshot, so the source keeps serving, and the platform's part
takes about two minutes. If the source can't be branched, the preview
restores the latest backup instead. This happens when the plan doesn't
allow branching, the storage has no snapshots, or the service was created
before branching existed.

A branch is a separate database. Writes to it never reach the source. Only
the branch's own owner can log in to it; the other logins it copied from the
source are locked, so a password from production does not open a preview.
While branches of a service exist, deleting that service, restoring it in
place, and upgrading its major version wait until the branches are removed.
They are removed with their previews.

Problems with a preview's database are in the preview Deployment's
annotation `build.kube-dc.com/preview-database-error`.

#### Copy grants

A Project admin of the source's project allows copies with a
ServiceCopyGrant. With builds on push the platform makes the copy: without a
grant, a preview gets an empty database of the source's plan, and the error
annotation says why. With a workflow, the copy is made by the workflow's
machine identity, and the platform refuses it unless it names a grant
(`KUBE_DC_PREVIEW_DATABASE_GRANT`; `app` is the production app's name, the
repository name in lower case). Pull request code can change the workflow,
so a grant is not optional there. Machine identities cannot create grants.

```yaml
apiVersion: services.kube-dc.com/v1alpha1
kind: ServiceCopyGrant
metadata:
  name: notes-previews
  namespace: <org>-<project>          # the source's project
spec:
  source: { name: notes-db, uid: <uid of notes-db> }   # a recreated service is not granted
  app: notes                          # the app whose previews may copy it
  appNamespace: <org>-<app project>   # omit when it is this project
  modes: [Branch, Restore]
```

`modes` lists what the previews may use: `Branch` (the live data) and
`Restore` (the latest backup, also the fallback when a branch fails).

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

- The script is plain SQL (no `psql` commands), at most 64 KiB, and runs as
  the database's owner while the copy is still closed to everyone else, in
  one transaction (unless it manages its own) with a 30-minute statement
  timeout. Write it so it can run twice.
- The owner must own every table in the database, and no table may use
  `FORCE ROW LEVEL SECURITY`; otherwise the copy is refused rather than
  half-masked. Copied logical replication subscriptions are disabled.
- The digest pins the script: changing the ConfigMap afterwards fails the
  copy, so a new script needs a new grant (grants cannot be edited).
- When any grant for a service and app masks, every copy for them masks.
- If the script fails, the copy stays closed. With builds on push it is
  deleted and the preview has no database;
  `build.kube-dc.com/preview-database-error` gives the SQL error (code and
  message, never row values). Fix the script, then close and reopen the pull
  request. With a workflow, `deploy.sh` fails with the same error; fix the
  script, create a new grant, delete the copy and deploy again.
- A copy keeps backups like its source, in the project's own object store,
  so they hold its data from before masking; they are readable like the
  source's own backups, by members of the project only.
- Masking protects data from pull-request code and the preview's public
  address. It does not hide data from members of the project, who can read
  the source anyway.

A failed build is named in the Deployment's `build.kube-dc.com/last-error`
and in the check run. Builds count against the project quota (they request
200m CPU and may use up to 2 CPU, 26Gi of disk and 60 minutes).

A bad build can be rolled back in about a second when the application keeps
its previous version running; a newer build always wins over a rollback. See
[Roll back an application](rollback.md).
