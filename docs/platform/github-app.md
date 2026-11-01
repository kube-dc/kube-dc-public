# GitHub App: deployment statuses, connected repositories, previews

With a GitHub App configured, Kube-DC:

- **reports rollouts to GitHub.** For a Deployment that names the repository
  and commit it runs, GitHub shows a deployment for that commit: **success**
  with the app's URL once it rolled out, or **failure** when it did not. Pull
  requests and commits then show the real state on the cluster, also for apps
  deployed outside CI (for example by an AI agent through the hosted MCP tool
  `deploy_app`);
- **connects repositories.** The hosted MCP tool `connect_repository` opens a
  pull request with the deploy workflows and sets the repository variables,
  in repositories whose admins allowed it (see below). Needs the backend
  worker (`backend.worker.enabled`); without it the tool returns the files
  instead;
- **removes previews at once** when their pull request closes, private
  repositories included, if the App's webhook is on. Without it, previews of
  private repositories go only when they expire.

The feature is off unless configured. Each installation registers **its own**
App: the App's name is what customers see on their commits and pull requests,
and its private key belongs to the operator of that installation. A GitHub
App has one webhook URL, so one App serves one installation.

| Installation | App |
|---|---|
| Kube-DC cloud (operated by SHALB) | [kube-dc-cloud](https://github.com/apps/kube-dc-cloud), owned by the `kube-dc` GitHub organization |
| Kube-DC stage (operated by SHALB) | `kube-dc-stage`, owned by the `kube-dc` GitHub organization |
| A provider (white label) or a self-hosted installation | One you register as below, under your own name; or leave the feature off |

With Fleet, `scripts/set-github-app.py <cluster> <app-id> <key.pem>` checks a
key against GitHub, stores it as the SOPS-encrypted Secret and sets the App ID.

## Register the App

As an owner of your GitHub organization: **Settings → Developer settings →
GitHub Apps → New GitHub App**.

| Field | Value |
|---|---|
| GitHub App name | Your product's name, for example `Example Cloud Deploys` |
| Homepage URL | Your product's site |
| Webhook | **Active**. URL `https://<backend host>/github/webhook` (the chart's `backend.gateway.hostname`). Secret: a random value, for example `openssl rand -hex 32` |
| Repository permissions (Read and write) | **Contents**, **Deployments**, **Commit statuses**, **Checks**, **Packages**, **Pull requests**, **Variables**, **Workflows**. Metadata (read) is added by GitHub. Checks: build check runs on commits; Packages: lets builds try GHCR without a registry token (not yet proven to work, see below) |
| Callback URL | `https://<backend host>/github/callback`, with **Request user authorization (OAuth) during installation** checked. After installing, GitHub sends the user there and Kube-DC returns them to the console. Reserved for a later GitHub sign-in (proving repository admin rights); the code is not used yet |
| Setup URL | Leave empty (the Callback URL is used) |
| Subscribe to events | **Pull request**, **Push**, **Check run**, **Repository** (re-run of a build; renamed or transferred repositories) |
| Where can this GitHub App be installed? | **Any account**, so your customers can install it |

Only deployment statuses wanted: Deployments alone is enough and the webhook
can stay inactive.

After **Create GitHub App**:

1. Note the **App ID** at the top of the App's page.
2. Under **Private keys**, click **Generate a private key**. GitHub downloads a
   `.pem` file once and keeps only its fingerprint. Keep the file like a
   password: whoever holds it can act as the App on every repository that
   installed it.

Adding permissions to an existing App asks every installation's owner to
accept them; until they do, `connect_repository` returns the files instead.

## Give the cluster the key

Create a Secret in the Kube-DC namespace (with Fleet, encrypt it with SOPS like
the other platform credentials):

```bash
kubectl -n kube-dc create secret generic github-app \
  --from-file=private-key.pem=./your-app.private-key.pem \
  --from-literal=webhook-secret=<the App's webhook secret>
```

Enable it in the kube-dc chart values:

```yaml
githubApp:
  enabled: true
  appId: "1234567"
  secretName: github-app          # default
  privateKeyKey: private-key.pem  # default
  webhook:
    enabled: true
    secretKey: webhook-secret     # default
```

Only the backend process that records platform events (the worker, or the
single API pod without a worker) mounts the key. The manager mounts only the
webhook secret. Then delete your local copy of the `.pem`.

## Rotate or revoke the key

Generate a new private key on the App's page, replace the Secret's
`private-key.pem`, restart the backend worker, and delete the old key on GitHub.
Deleting the key alone stops every status at once. To rotate the webhook
secret, set the new value on the App and in the Secret's `webhook-secret`, then
restart the manager; deliveries in between are refused.

## What the cluster sends

For each `DeploymentRolledOut` or `DeploymentFailed` platform event of a
Deployment annotated with `github.kube-dc.com/repository` (`owner/repo`) and
`github.kube-dc.com/sha` (the commit), the backend:

1. signs a 9-minute App token with the private key;
2. finds the App's installation on that repository; a repository that did not
   install the App is skipped and logged at info level;
3. takes an installation token limited to that repository and to deployments
   (one hour, cached);
4. creates a GitHub deployment for the commit in the environment named by
   `github.kube-dc.com/environment` (default `production`) and sets its status,
   with the app's HTTPS URL on success.

Nothing is retried after a GitHub error; the platform event itself is kept.

## Connecting a repository

`connect_repository` writes to a repository only when:

1. the caller is an admin of the organization (the worker checks the caller's
   own Kubernetes token with a SelfSubjectAccessReview), and
2. the repository's admins named that organization in the repository variable
   `KUBE_DC_ORGANIZATION` (only repository admins can set variables):

   ```bash
   gh variable set KUBE_DC_ORGANIZATION -R owner/repo -b <organization>
   ```

The worker then takes a token limited to that repository, commits the deploy
workflows on branch `kube-dc/connect`, opens a pull request to the default
branch (or moves the branch of an open one), and sets the `KUBE_DC_*`
repository variables. Nothing else in the repository is touched.

## The webhook

The manager listens on `githubApp.webhook.port` (default 9445), reached through
the backend's HTTPRoute at `/github/webhook`. A delivery without a valid
`X-Hub-Signature-256` for the webhook secret is refused. A closed pull request
removes the previews whose Deployment is annotated
`preview.kube-dc.com/repository` and `preview.kube-dc.com/pull-request` for
it, with their Services, Secrets, ConfigMaps and managed databases. Other
events are acknowledged and ignored.

## Builds on push

With `githubApp.builds.enabled` (and the webhook), a push to a repository's
default branch builds and deploys the apps set up for it, with no CI in the
repository. An app opts in through the hosted MCP tool `deploy_app` with
`buildFrom`, or with these on its Deployment:

| On the Deployment | Value |
|---|---|
| label `build.kube-dc.com/on-push` | `"true"` |
| `build.kube-dc.com/repository` | `owner/repo` |
| `build.kube-dc.com/image` | registry path without a tag; images are tagged with the commit |
| `build.kube-dc.com/push-secret` | a `kubernetes.io/dockerconfigjson` Secret in the project that may push there. Optional for `ghcr.io/<repository owner>/<name>`: then the App pushes the image and the platform keeps a pull Secret `<app>-ghcr` fresh with read tokens. **Experimental:** GHCR may refuse App tokens for pushes; if the build log shows a denied push, name a push Secret |
| `build.kube-dc.com/dockerfile` | optional, default `Dockerfile` |
| `build.kube-dc.com/previews` | optional, a project namespace of the same organization: every pull request is built into `<app>-pr-<n>` there |
| `build.kube-dc.com/preview-hours` | optional, default 72: a preview without a new build is removed after this |

On a signed push the manager checks the repository's `KUBE_DC_ORGANIZATION`
claim against the project's organization, puts a `contents: read` token for
that repository (one hour) in a Secret, and runs the build Pod
`build-<app>-<commit>` in the project: rootless BuildKit
(`githubApp.builds.image`, the managed runners' image by digest) builds the
commit from GitHub and pushes it with the push Secret. A newer push replaces a
running build. When the build succeeds the manager sets the Deployment's image
(a new app is scaled from zero to one) and the `github.kube-dc.com/*`
annotations, so the rollout is reported to GitHub. A build that could not
start or failed is named in the Deployment's `build.kube-dc.com/last-error`.
Build pods count against the project's quota (500m CPU, 1Gi requested) and
are removed after a day.

**Pull requests.** With `build.kube-dc.com/previews`, an opened, reopened or
updated pull request is built into a preview: a copy of the app in the
previews project (no replicas until its first build, a published Service, the
container's plain settings only: Secrets the app reads, such as its database,
are not shared). Closing the pull request removes it; so does the janitor
after `preview-hours` without a build. Pull requests from forks are not
built.

**Check runs.** Each build reports a check run `kube-dc` on its commit: in
progress, then success with the app's URL or failure with how to read the
log. It needs the Checks permission; without it builds still run.

The build Pod runs with Unconfined seccomp and AppArmor, like the runners'
BuildKit sidecar; the pod baseline policies admit it because the manager
creates it. The manager mounts the App's private key only with builds on.
