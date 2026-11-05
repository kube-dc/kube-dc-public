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
  in repositories whose admins allowed it (see
  [Connecting a repository](#connecting-a-repository)). Needs the backend
  worker (`backend.worker.enabled`); without it the tool returns the files
  instead;
- **removes previews at once** when their pull request closes, private
  repositories included, if the App's webhook is on. Without it, previews of
  private repositories go only when they expire.

Project users find the setup steps in the console under **Overview → Deploy
from GitHub**: managed runners plus a managed registry (recommended, no token
stored), or their own registry with a token Secret they create themselves.

The feature is off unless configured. Each installation registers **its own**
App under its own name, as described in [Register the App](#register-the-app).
The App's name is what customers see on their commits and pull requests, and
its private key belongs to the operator of that installation. A GitHub App has
one webhook URL, so one App serves one installation. A provider that runs a
white-label installation registers the App under its own brand.

## Required setup

Each feature needs everything in the rows above it.

| Feature | On GitHub (the App) | In the cluster |
|---|---|---|
| Deployment statuses on commits | App with **Deployments** (and **Commit statuses**) permission, installed on the repository | Secret `github-app` with `private-key.pem`; chart `githubApp.enabled`, `githubApp.appId` |
| Install link and installation check in the console | Public App name (slug) | `githubApp.slug` |
| Previews removed on PR close | Webhook active, URL `https://<backend host>/github/webhook`, secret; event **Pull request** | `webhook-secret` in `github-app` or in the Secret named by `githubApp.webhook.secretName`; `githubApp.webhook.enabled` |
| `connect_repository` | Permissions **Contents**, **Pull requests**, **Variables**, **Workflows** (write) | Backend worker on (`backend.worker.enabled`) |
| Builds on push, PR previews, check runs | Events **Push**, **Pull request**, **Check run**, **Repository**; **Checks** (write) | `githubApp.builds.enabled` |
| Allow this organization (console sets `KUBE_DC_ORGANIZATION`) | Callback URL `https://<backend host>/github/callback`; **Request user authorization (OAuth) during installation**; **Redirect on update**; a client secret | `githubApp.clientId`; the client secret under `client-secret` (in `github-app` or a Secret named by `githubApp.clientSecretName`); restart the backend worker |
| Managed registry pushes from builds, docker login button | None | `manager.registry.enabled` (default on); the docker login button also needs the hosted MCP endpoint (`backend.mcp.enabled`) |

Per repository, before builds or `connect_repository`: the App is installed on
it, and `KUBE_DC_ORGANIZATION` names the organization (set by a repository
admin with **Allow this organization** or `gh variable set`). The console's
**Deploy from GitHub** step 1 checks both.

## Register the App

As an owner of your GitHub organization: **Settings → Developer settings →
GitHub Apps → New GitHub App**.

| Field | Value |
|---|---|
| GitHub App name | Your product's name, for example `Example Cloud Deploys` |
| Homepage URL | Your product's site |
| Webhook | **Active**. URL `https://<backend host>/github/webhook` (the chart's `backend.gateway.hostname`). Secret: a random value, for example `openssl rand -hex 32` |
| Repository permissions (Read and write) | **Contents**, **Deployments**, **Commit statuses**, **Checks**, **Packages**, **Pull requests**, **Variables**, **Workflows**. Metadata (read) is added by GitHub. Checks: build check runs on commits. Packages: optional; GHCR refuses pushes with an App token, so builds into GHCR need a push Secret (see [Builds on push](#builds-on-push)) |
| Callback URL | `https://<backend host>/github/callback`, with **Request user authorization (OAuth) during installation** checked. GitHub returns the user there after installing the App or signing in through it; the backend finishes **Allow this organization** (below) and returns them to the console |
| Setup URL | Leave empty (the Callback URL is used) |
| Post installation | **Redirect on update** checked: GitHub then returns to the console also after a user changes the repositories of an existing installation (otherwise only after a new installation) |
| Subscribe to events | **Pull request**, **Push**, **Check run**, **Repository** (re-run of a build; renamed or transferred repositories) |
| Where can this GitHub App be installed? | **Any account**, so your customers can install it |

Only deployment statuses wanted: Deployments alone is enough and the webhook
can stay inactive.

After **Create GitHub App**:

1. Note the **App ID** and the **Client ID** at the top of the App's page
   (General → About).
2. Under **Private keys**, click **Generate a private key**. GitHub downloads a
   `.pem` file once and keeps only its fingerprint. Keep the file like a
   password: whoever holds it can act as the App on every repository that
   installed it.
3. For **Allow this organization** in the console: under **Client secrets**,
   click **Generate a new client secret** and copy it; GitHub shows it once.
   Without it the console shows only the manual `gh variable set` command.

Adding permissions to an existing App asks every installation's owner to
accept them; until they do, `connect_repository` returns the files instead.

## Give the cluster the key

Create a Secret in the Kube-DC namespace (with Fleet, encrypt it with SOPS like
the other platform credentials):

```bash
kubectl -n kube-dc create secret generic github-app \
  --from-file=private-key.pem=./your-app.private-key.pem \
  --from-literal=webhook-secret=<the App's webhook secret> \
  --from-literal=client-secret=<the App's client secret>
```

`client-secret` is optional. The webhook secret and the client secret can
also live in their own Secret, so they can be added later without touching the
Secret that holds the private key: name it in `githubApp.webhook.secretName`
and `githubApp.clientSecretName` (one Secret may hold both keys). With Fleet that is a new SOPS file, which needs
only the public age recipients from `.sops.yaml` (editing the existing file
needs a private age key):

```bash
# from a file only you can read; the value never reaches the terminal
python3 -c 'import json; print(json.dumps({"apiVersion":"v1","kind":"Secret",
  "metadata":{"name":"github-app-client","namespace":"kube-dc"},"type":"Opaque",
  "stringData":{"client-secret":open("client-secret.txt").read().strip(),
                "webhook-secret":open("webhook-secret.txt").read().strip()}}))' \
| sops --encrypt --input-type json --output-type yaml \
    --filename-override clusters/<cluster>/github-app-client.enc.yaml /dev/stdin \
    > clusters/<cluster>/github-app-client.enc.yaml
shred -u client-secret.txt webhook-secret.txt
```

List the file in the cluster's `kustomization.yaml`, set `githubApp.clientId`,
`githubApp.clientSecretName` and `githubApp.webhook.secretName` (both
`github-app-client` here), and restart the backend worker (or the API
pod without a worker): the secret is read at start.

Enable it in the kube-dc chart values:

```yaml
githubApp:
  enabled: true
  appId: "1234567"
  slug: example-cloud-deploys     # github.com/apps/<slug>: the console links its install page
  secretName: github-app          # default
  privateKeyKey: private-key.pem  # default
  clientId: Iv23liExample         # the App's Client ID: "Allow this organization"
  clientSecretKey: client-secret  # default
  clientSecretName: ""            # optional: a separate Secret for the client secret
  builds:
    enabled: true                 # builds on push, previews, check runs
  webhook:
    enabled: true
    secretKey: webhook-secret     # default
    secretName: ""                # optional: a separate Secret for the webhook secret
```

`slug` is the App's public name, the last part of its page
`https://github.com/apps/<slug>`. With it, **Deploy from GitHub** starts with an
**Install the GitHub App** button that opens
`https://github.com/apps/<slug>/installations/new`; without it the console
shows the steps in words. The same step checks a repository for the user: is
the App installed on it, and does its `KUBE_DC_ORGANIZATION` variable name the
user's organization (`GET /api/manage-organization/github-app/check`, any
organization member, green answers cached for a minute, no token returned).

Only the backend process that records platform events (the worker, or the
single API pod without a worker) mounts the key, and with `clientId` the
client secret. The manager mounts only the webhook secret. Then delete your
local copy of the `.pem`.

## Rotate or revoke the key

Generate a new private key on the App's page, replace the Secret's
`private-key.pem`, restart the backend worker, and delete the old key on GitHub.
Deleting the key alone stops every status at once. To rotate the webhook
secret, set the new value on the App and in the Secret's `webhook-secret`, then
restart the manager; deliveries in between are refused. To rotate the client
secret, generate a new one on the App's page, replace `client-secret`, restart
the backend worker and delete the old one on GitHub; sign-ins in progress must
start again.

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

   or with **Allow this organization** in the console (below).

The worker then takes a token limited to that repository, commits the deploy
workflows on branch `kube-dc/connect`, opens a pull request to the default
branch (or moves the branch of an open one), and sets the `KUBE_DC_*`
repository variables. Nothing else in the repository is touched.

## Allowing an organization from the console

`KUBE_DC_ORGANIZATION` is the repository admins' consent that one Kube-DC
organization may connect and build the repository. GitHub lets only
repository admins set repository variables, and Kube-DC honours only that
variable, so the consent cannot come from anyone else. With `clientId` and the
client secret, **Deploy from GitHub → step 1** offers **Allow this
organization** instead of the `gh` command (which stays as a fallback):

1. A member of the organization clicks it for `owner/repo`
   (`POST /api/manage-organization/github-app/authorize`). The backend returns
   GitHub's sign-in URL for the App with a state signed by a key derived from
   the client secret: the organization and the user from their verified
   token, the repository, ten minutes, used once. A repository without the App
   gets the App's install URL with the same state; GitHub signs the user in
   after the installation.
2. A GitHub user who is an **admin** of the repository authorizes. GitHub
   returns to `https://<backend host>/github/callback`. The backend checks the
   state, that the App is installed on the repository, exchanges the code for
   a user token and asks GitHub, as that user, for the repository: it must say
   `permissions.admin: true`. The user token is then revoked and never stored
   or logged. A non-admin grants nothing (audited `repository.authorize`,
   denied).
3. The browser returns to the console with a ten-minute grant, which the
   console redeems with the user's own token
   (`POST /api/manage-organization/github-app/allow`). Only the console user
   who started the flow, in the same organization, can redeem it: a
   repository admin lured into authorizing somebody else's link allows
   nothing. The backend then sets `KUBE_DC_ORGANIZATION` with an installation
   token limited to the repository, and the check turns green. The grant is
   audited (`repository.allow`: organization, repository, GitHub login).

A variable that already names another organization is not overwritten: the
console shows its value and replaces it only after the user confirms. Any
member may start the flow because the repository admin decides; connecting
the repository (`connect_repository`) still needs an organization admin.
Without `clientId`, or without the client secret in the Secret, the console
shows only the command.

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
| `build.kube-dc.com/push-secret` | not needed for a managed registry of the organization (below). Otherwise a `kubernetes.io/dockerconfigjson` Secret in the project that may push there. Required in practice: GHCR refuses pushes with a GitHub App token (`installation not allowed to Write organization package`), even with Packages: write accepted, so for GHCR use a classic personal access token with `write:packages`. Without it the App pushes `ghcr.io/<repository owner>/<name>` and keeps a pull Secret `<app>-ghcr` fresh, which works only if GitHub lifts that limit |
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
start or failed is named in the Deployment's `build.kube-dc.com/last-error`,
with the pod's reason (for example `DeadlineExceeded` or `Evicted`) and the
last lines of the build log, which the kubelet keeps after the log is gone.

Build pods request 200m CPU, 1Gi memory and 4Gi of node disk, and may use up
to 2 CPU, 2Gi memory, 24Gi of BuildKit state (26Gi of node disk in all) and
60 minutes. They count against the project's quota and are removed after a
day. A cold build of a Rust and Node app needs more than 8Gi of
state; plan node disk for one build per pushing app. Build
pods prefer nodes without the `node-role.kubernetes.io/control-plane` label,
so unpacking base images doesn't put a control-plane node under disk
pressure; a cluster of control-plane nodes only still builds.

**Into a managed registry.** When the image's host is the `https` host of a
Ready managed registry (`registry` ManagedService) of the same organization,
no push Secret is needed. The image must be `<registry host>/<project>/<name>`,
where `<project>` is the app's own project, and the registry must list that
project (its own project always counts). The manager pushes with a token of
the project's platform push identity, `kube-dc-registry-push-<project>` in
the organization realm (audience `kube-dc-registry`, claim
`kdc_registry: push:<project>`, 65 minutes so it outlives the build
deadline). The app then pulls with the registry's pull Secret
`kdc-registry-<registry>`, which the manager keeps fresh in every listed
project and adds to the Deployment. A preview into a managed registry must be
in the app's own project. With `build.kube-dc.com/push-secret` set, or for
any other host, builds work as before.

**People pushing to a managed registry.** Registry rights come from the
Keycloak group attribute `kdc_registry`: `push:<project>` for project
`admin`/`developer` groups, `pull:<project>` for other roles, and, kept by the
Organization controller as projects come and go, `push:<project>` for every
project on the reserved `org-admin` group (the `user` group gets none). The
console's **docker login** button (Deploy from GitHub and a registry's
Overview) and the hosted MCP tool `registry_login` give a person a 15-minute
token with those rights: the backend exchanges their sign-in through the
realm's confidential `kube-dc-mcp` client (`audience=kube-dc-registry`,
`scope=kube-dc-registry`) and refuses any answer that Kubernetes would
accept. This needs the hosted MCP endpoint (`backend.mcp.enabled`): the
console API pods then mount the `kube-dc-mcp-requesters` Secret, and the
manager adds `kube-dc-mcp` to the console client's access-token audience,
which standard token exchange requires. All of it is off with
`manager.registry.enabled: false`.

**Pull requests.** With `build.kube-dc.com/previews`, an opened, reopened or
updated pull request is built into a preview: a copy of the app in the
previews project (no replicas until its first build, a published Service, the
container's plain settings only: Secrets the app reads, such as its database,
are not shared). Closing the pull request removes it; so does the janitor
after `preview-hours` without a build. Pull requests from forks are not
built.

**Check runs.** Each build reports a check run `kube-dc` on its commit: in
progress, then success with the app's URL or failure with the reason and
the log tail. It needs the Checks permission; without it builds still run.

The build Pod runs with Unconfined seccomp and AppArmor, like the runners'
BuildKit sidecar; the pod baseline policies admit it because the manager
creates it. The manager mounts the App's private key only with builds on.
