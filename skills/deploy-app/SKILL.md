---
name: deploy-app
description: Deploy a containerized application to a Kube-DC Project as an Application (apps.kube-dc.com) or a plain workload, with optional persistent storage, a managed database, external exposure, or gated Shared GPU acceleration; set up keyless deploys, builds on push and pull request previews from a GitHub repository, with a managed container registry or the user's own; roll back, pause and inspect builds.
---

# Deploy an Application

An app on Kube-DC is an **Application** (`apps.kube-dc.com/v1alpha1`) in the
Project's backing namespace: it names the source (a container image or a
GitHub repository) and how the app runs, and the platform builds it, deploys
it, publishes its address and keeps it that way. It generates and owns a
Deployment and a Service named after the app; change the Application, never
those objects. Plain Deployments remain for what an Application does not
cover (below). Keep the Project context, quota, storage, database, and
exposure decisions explicit.

**Choose the path before writing anything.** `curl -s https://backend.<domain>/api/platform/info` (public, no token;
`<domain>` is the installation's base domain, as in `kube-dc login`) reports
`applications.enabled` and `applications.create`. Listing the resource type
(`kubectl api-resources --api-group=apps.kube-dc.com`) is not enough: the CRD
can be installed where the controller does not run. Never apply an
Application to find out, and never delete one because it has no status yet.

- `enabled` and `create` true: the app is an Application (`deploy_app`, the
  manifests below, builds on push). `deploy_app` makes only Applications (its
  output has `application: Application/<name>`).
- `enabled` false, or `create` false for a new app: use the plain manifests
  below (a Deployment and a Service) and, for builds from GitHub, the user's
  own GitHub Actions workflow (`connect_repository`, which adds `deploy.yml`
  and `preview.yml`). With the hosted MCP server, a `deploy_app` refusal that
  says the Application API is off means the same.

## With the hosted MCP server

When the Kube-DC MCP connector is available, prefer its app tools over
writing the manifests below:

- `deploy_app` (project, name, image, port; optional `healthPath`, `env`,
  `envFromSecrets` for a database binding Secret, `pullSecret` for a private
  registry, `public: false` for in-Project only) creates or updates the app.
  It creates an Application or changes an existing one through its spec.
  It merges the arguments into the Application: an omitted argument keeps its value, except variables: `env`
  or `secretEnv` replaces the whole list, so pass every variable the app
  needs (a call with only `env` drops `DATABASE_URL` from a Secret). Call it
  again with a new image tag to ship a new version. Every call needs the
  app's `image` (or, for an app built from GitHub, `buildFrom` again with
  `image` or `buildFrom.registry`), also a call that only sets `suspend`.
  `suspend: true` pauses an Application's builds and rollouts (the running
  version keeps serving), `suspend: false` resumes them.
- Secrets (API keys, session keys): never ask the user for a value or put
  one in `env`. `list_secrets` (project) shows the project's managed
  secrets with key names only, never values, and which apps use each.
  `create_secret` either generates a value nobody sees (`generate`, key
  `password`, optional `rotateEvery` such as `90d`) or creates an empty
  secret with the `keys` the user must set and returns its console page:
  give the user that link and wait until they say the values are saved.
  Then pass `secretEnv` (`{ "STRIPE_KEY": { "secret": "stripe", "key":
  "STRIPE_SECRET_KEY" } }`, one variable from one key) or `envFromSecrets`
  (every key becomes a variable) to `deploy_app`.
- Restart on a new value: a new app restarts by itself when a managed
  secret it uses gets a new value (a rotation, within an hour, or a console
  edit); `restartOnSecretChange: false` turns that off, and an existing app
  keeps its setting. The user can also edit variables, secret keys and the switch
  in the console (an Application's **Environment** tab and **Edit YAML**).
- Release command (migrations): `deploy_app releaseCommand` (for example
  `["npm", "run", "migrate"]`, optional `releaseTimeoutMinutes`, default 15)
  runs once with each new image, with the app's variables and secrets,
  before that image takes traffic. If it fails, the running version keeps
  serving and `rollout_status` reports `failed` (`ReleaseFailed`) with the
  Job's failure message (read the release Job's pod with `get_logs`);
  deploying again retries (the console's
  **Retry release** does the same). It runs for builds on push and previews
  too, never on a rollback: write migrations the previous version can live
  with (expand/contract). A command added to a running app runs before its
  next release, not for the running one.
- Workers: leave out `port` for a queue consumer (no Service, no hostname,
  no `warmPrevious`).
- Jobs: `run_job` runs a command once (`app` for the app's image and
  secrets, or `image`); `schedule_job` runs one on a cron `schedule`
  (`timeZone`, `concurrency`, `suspend`). A cron job made from an app follows
  its new image and is deleted with it. Give previews no schedules.
  `list kind=jobs` shows runs and pods; `get_logs` reads them.
- `autoscale: { min, max, cpuPercent }` scales on CPU as a share of the CPU
  limit (not with `replicas`); `volume: { size, path }` mounts a persistent
  volume `<app>-data` (one replica; kept when the app is deleted, and not
  used by a new Application of the same name: copy the data and delete the
  claim before recreating). Tell the
  user that job pods and extra replicas are billed like the app's pods.
- Own domain: `deploy_app domain: "app.example.com"` (public apps). Its DNS
  must point at the platform first: a CNAME to the app's current hostname,
  or for a bare domain A records to the addresses that hostname resolves to
  (or the DNS provider's ALIAS/ANAME). On Cloudflare the record must be
  **DNS only**, not proxied. The call is refused with the record to add until
  DNS resolves. Where the installation requires verified domains, an
  organization admin must first verify the domain (console **Organization →
  Domains**, a TXT record). The app then answers only on that domain; tell
  the user its platform hostname stops working. `domain: ""` brings the
  platform hostname back. The certificate takes 1 to 3 minutes.
- Organization domains: an organization admin connects a domain once in the
  console (**Organization → Domains → Connect a domain**; for many apps an
  `apps.<domain>` option keeps them under that subdomain, so the website and
  mail stay untouched): a TXT record proves it,
  a wildcard `*.<domain>` points every name under it at the platform, and the
  domain becomes the default for new apps when **New apps get…** is ticked
  (new apps get the address once `*.<domain>` points at the platform).
  Suggest this to users who
  want many apps on their own domain, instead of one record per app.
- Organization default domain: when it is set and `*.<domain>` points at the
  platform, a new public app without `domain` gets `<app>.<domain>` (or
  `<app>-<project>.<domain>` when taken); the result's `domain` field shows
  it. Pass
  `domain: ""` for the platform address. Existing apps and previews never
  change.
- The main domain itself (`domain: "example.com"`, a verified domain of the
  organization): one app may take it; `www.example.com` is not redirected
  to it.
  It needs A records for `@` (or an ALIAS/ANAME/flattened CNAME to
  `gateway.<baseDomain>`); tell the user a
  website already there moves to this app.
- `rollout_status` reports `ready` with the HTTPS URL, `progressing`, or
  `failed` with the reason (image pull, crash loop, scheduling, a failed
  build) and the failing pod's last log lines; for an Application also
  `application` (phase, deployed release, commit, `pinned`, `suspended`,
  latest build). Check about 30 seconds after a deploy; a new hostname gets
  its certificate within 1 to 3 minutes.
- `list` kind `apps` lists the Project's Applications;
  kind `builds` lists the Project's builds, or one app's with `app`.
  `get_logs` with `build` (and `attempt`) reads a build's log; `delete` with
  kind `build` cancels a running build.
- `restart` and `rollback` change a running app: ask the user first.
  `rollback` returns to the previous revision by default. For an
  Application it always pins: to the previous release, or to `release: <n>`. While pinned, pushes are still
  built but nothing new deploys until `rollback` with `resume: true`: tell the
  user that.
- Delete an app: `delete` with `kind: app` removes the Deployment, Service,
  autoscaler, cron jobs, the warm previous version and its previews (with
  their database copies). The data volume `<app>-data` stays unless
  `deleteData: true`; registry images and the repository are kept. Ask the
  user first and say what goes. A preview (`{app}-pr-<n>`) is refused by
  kind `app`: it
  goes when its pull request closes or it expires. `extend_preview` only
  extends it;
  end it now with the preview's **Settings → Remove preview** or by deleting
  the preview Application (`delete` with kind `Application` and apiVersion
  `apps.kube-dc.com/v1alpha1`), and it comes back with the pull request's next
  commit.
- Instant rollback: `deploy_app` with `warmPrevious` (for example `1h`, at
  most `24h`; `off` removes it) keeps the previous version running as
  `{app}-previous` after each rollout. `rollback` then switches traffic to it
  in about a second and reports `instant: true`; when the copy is not ready it
  starts new pods as before (`instant: false`). `rollout_status` shows the
  copy (`warmPrevious`) and where traffic is. Tell the user the copy is billed
  like the app's pods (one replica during the window), and that a rollback
  does not undo database migrations: use expand/contract (add the new column
  first, remove the old one a release later, after the window). The old
  version gets `KUBE_DC_WARM=1` and should skip background work (cron,
  queue consumers) while it is set. Not for previews.
- Previews: `deploy_app` with `previewHours` and no `buildFrom` (for example
  a pull request's copy, named `{app}-pr-{number}`) is removed that long
  after it was deployed. `extend_preview` sets the removal to now plus
  `hours` (it never shortens an Application preview, which gets no warning
  before it goes). `buildFrom.previewHours`, `buildFrom.previewBuildArgs` and
  `buildFrom.previewDatabaseSecret` take effect only together with
  `buildFrom.previewProject` in the same call.
- GitHub: an Application built from GitHub gets the `kube-dc` check run of
  its builds on each commit. `githubRepository` and `githubSha` on
  `deploy_app` are kept only as an image Application's provenance; it gets
  nothing on GitHub.
- `create_webhook` (organization admins) sends the organization's platform
  events, such as `DeploymentRolledOut`, `DeploymentFailed` and
  `RouteReady`, to an HTTPS URL, signed. Use it when the user wants to hear
  about deploys elsewhere (chat, a script) instead of polling.

`deploy_app` does not take over a plain Deployment of the same name (one
applied with `kubectl`): the Application reports the name as in use. Before
proposing to replace such an app with an Application, find what it uses:
`kubectl get deployment,service,httproute,persistentvolumeclaim` (its
Service may have another name, its address may come from that Service's
`service.nlb.kube-dc.com/route-hostname` or a route of its own, its data from
any claim). Tell the user what changes: the Application makes its own
Service named after the app and gets its address from `run.hostnames` or the
platform address, so traffic to the old Service or hostname stops until DNS
and clients follow; it does not take over a data volume (it refuses an
existing `{app}-data` claim), so the data must be copied out and back. List
exactly which objects belong to this app alone (its Deployment, and only a
Service, route or claim nothing else selects or mounts) and keep any that
another workload uses. Only after the user agrees to that list, delete those
objects and create the Application.

## Deploy from GitHub on every push

**Ask the user which path they want first**; do not pick for them:

- **(a) Recommended: managed runners and a managed registry.** Images go to a
  Kube-DC container registry (managed service family `registry`) and CI runs
  on a managed runner pool (family `github-runner`) or the build runs on the
  platform. Nothing secret is stored in GitHub: CI exchanges its deploy token
  for a 15-minute registry token, and the platform keeps each project's pull
  Secret `kdc-registry-<registry>` fresh.
- **(b) Their own registry**, such as GHCR. A push token is needed; the user
  creates it and saves it in the console themselves. Never ask for a token's
  value or paste one into a tool.

Check what the installation offers before the user picks; not every
installation has a registry or builds on push:

- Managed registry: a plan of family `registry` in `list kind=managed_services`
  offerings, or an existing registry in another project of the organization
  (see "A registry in another project" below). With neither, offer (b).
- GitHub App: `GET https://backend.<baseDomain>/api/platform/info` (public,
  no sign-in; `baseDomain` from `whoami`) returns `githubApp.enabled`,
  `githubApp.installUrl` and `githubApp.builds`. No `githubApp`, or
  `enabled: false`, means no deployment status on commits; `builds: false`
  means no builds on push: use a GitHub Actions workflow with GHCR.
  `deploy_app` with `buildFrom` also refuses with "This installation does not
  build on push". The installation's operator sets the App up (admin console,
  Platform settings → Integrations); never ask the user for App keys.
- An installation may hide Deploy from GitHub or AI assistants from its
  tenants. A 404 with "Deploy from GitHub is not offered on this installation"
  (or "AI assistants are not offered") means exactly that: say so and use a
  GitHub Actions workflow with the user's own registry instead; do not retry.
  Through the MCP server, where the installation's GitHub-connect worker is
  set up, `connect_repository` reports it inside a successful result, after it has already created the machine identities and groups:
  read the result, tell the user, and do not call it again.

Before either, the repository needs the installation's GitHub App and the
organization's consent. Installing the App is a GitHub organization admin's
step in the browser: give the user `githubApp.installUrl`, ask them to pick
**Only select repositories** with this repository, and wait until they say it
is installed. Consent is the repository variable `KUBE_DC_ORGANIZATION` set to
the organization (the console's **Applications → Connect GitHub → Allow
this organization**, or `gh variable set KUBE_DC_ORGANIZATION -R owner/repo -b <org>`
by a repository admin). The hosted MCP guide
`kube-dc://guide/deploy-from-repository` has the full flow.

Then choose how builds run (one per app; both would deploy every push twice):

- **Builds on push by the platform** (no CI in the repository; only where
  `applications.enabled` and `applications.create` are true, see above):
  `deploy_app` with `buildFrom`. Managed registry of the app's own project:
  `buildFrom.registry: <name>`, no `image` and no push Secret (for an
  Application not already on a managed registry, any `image` outside
  `ghcr.io` without `pushSecret` is refused; GHCR needs `pushSecret` too, as
  GHCR refuses the GitHub App's token).
  Own registry: an `image` path without a tag and `pushSecret`, a
  `kubernetes.io/dockerconfigjson` Secret the user created.
  Pushes build and deploy; pull requests build a preview when a previews
  project is given; the commit shows a `kube-dc` check run.
  - New project registry: `buildFrom.registry: <name>` names a managed
    registry of the project (family `registry`) instead of `image`, also one
    still being created; the platform sets the image path when it is Ready
    and the first build waits for it (the Application reports
    `RegistryNotReady` until it is ready).
  - A registry in another project: one registry serves several projects of
    the organization. Add the app's project to that registry's
    `parameters.projects` (`apply_manifest` on the ManagedService, by someone
    with write access to the registry's project; the list is replaced, not
    merged: read it first and send every project, the existing ones too;
    removing a project later is a separate edit, and deleting the project
    does not do it), wait until it is Ready again.
    `deploy_app` cannot name a registry of another project: write the
    Application with `apply_manifest` (below), with
    `image.registry.managedService: { name: <registry>, project: <its
    project> }`.
  - What is built: the pushed commit, not the working tree. Files that git
    ignores (large media, licensed assets, local `.env` files) are not in
    the build; the app must not depend on them, or a derived file that may
    be published must be committed instead.
  - Static sites and single-page apps: serve the build output with an
    unprivileged web server on a port above 1024 (for example
    `nginxinc/nginx-unprivileged` on 8080), add a `/healthz` location for
    `healthPath`, and fall back to `index.html` for client-side routes.
    An app that lives under a path (`/app/`) must be built with that base
    path.
  - First build: a new app is built from the default branch at once (for a
    new Application even with `buildFrom.buildNow: false`); pass `buildFrom.buildNow: true`
    again to rebuild an existing app without a push.
  - No Dockerfile: `buildFrom.builder: railpack` detects the language (Node.js,
    Python, Go, Rust, Ruby, PHP, Java, .NET, Elixir, Deno, static sites). The
    app must listen on `$PORT`: set `env: { PORT: "8080" }` and `port: 8080`.
    `buildArgs` and `buildSecrets` reach every build step as environment
    variables. A repository without app files (only a README) cannot be built.
  - Another branch: `buildFrom.branch: <branch>` builds pushes to that branch
    instead of the default branch; pull request previews are unchanged.
  - The console's **Applications → New application** does all of this for
    the user in a few clicks (repository picker, port, branch; it uses the
    project's registry, with no choice):
    suggest it to users who prefer the console. Each app then has a page
    under **Applications** with its versions, processes and autoscaling, own
    domain, previews, logs and **Settings → Delete application**.
  - Build arguments: `buildFrom.buildArgs` sets Dockerfile `ARG` values for
    every build (for example `{ "VITE_API_URL": "https://api.example.com" }`)
    and `buildFrom.previewBuildArgs` overrides them key by key for previews. They stay
    in the image history: never put a secret in them.
  - Build secrets: `buildFrom.buildSecrets` lists project secrets the
    Dockerfile reads with `RUN --mount=type=secret,id=<key>` (a private
    package registry token, for example); they never land in the image.
  - Preview databases: `buildFrom.previewDatabase` (a plan, a new empty
    database) or `buildFrom.previewDatabaseFrom` (a managed service in
    `previewProject`, copied) with optional `previewDatabaseMode: branch`
    (the live data, falls back to the latest backup). A copy needs a
    ServiceCopyGrant in the source's project naming the source (name + UID),
    the app (and its namespace) and `modes` (`Branch`, `Restore`); only a
    Project admin there can create it (`apply_manifest`), so ask the user.
    Without it previews get an empty database and the preview's
    `DatabaseReady` condition says why.
    Copies carry production data: suggest masking. The grant's
    `spec.masking: {configMap, key, sha256}` names a plain-SQL script (no
    psql commands, idempotent, run as the database owner before anyone can
    connect); the sha256 is of the ConfigMap value's exact bytes. A failed
    script deletes the copy and `DatabaseReady` gives the SQL error. The app must read its database from one Secret
    (`envFromSecrets`/`secretEnv`), default `<name>-db-owner`, else name it
    in `previewDatabaseSecret`; a preview reads `<name>-pr-<n>-db-owner` there.
  - Previews never read the app's own secrets: a preview reads
    `<secret>-preview` in its project instead (in a separate previews
    project, a secret of the same name there also counts), for runtime
    variables and build secrets. Variables from other secrets are left out,
    and a build secret with no preview copy is left out of the preview's
    build, which then usually fails at its `RUN --mount` step (the preview's
    `Built` condition shows it). Create the copies,
    with test keys, using `create_secret` in the preview's project.
- **A GitHub Actions workflow:** `connect_repository` (organization admins)
  sets up keyless deploys: GitHub Actions signs in with its own short-lived
  OIDC token through a machine identity that accepts only that repository's
  environment.

  1. Get the repository's subject prefix:
     `gh api repos/{owner}/{repo}/actions/oidc/customization/sub --jq .sub_claim_prefix`.
  2. Call `connect_repository` with `repository`, `subjectPrefix`, `project`,
     `port`, and optionally `previewProject` (a copy per pull request, removed
     when it closes or after 72 hours), `healthPath`, `registry` (and
     `registryProject`) for a managed registry, `runnerLabel` for a managed
     runner pool, and `privateImage` for a private GHCR image (the user then
     adds a `GHCR_PULL_TOKEN` repository secret with `read:packages`; never
     ask for the token's value).
     With `previewProject`, a preview database per pull request (the app
     reads the Secret `<app>-pr-<n>-db-owner`; removed with the preview):
     - `previewDatabase`: a plan such as `postgresql-development`, for a new,
       empty database.
     - `previewDatabaseFrom`: a managed service in the preview project. The
       preview gets a copy of its latest backup, which takes minutes.
     - Add `previewDatabaseMode: branch` to copy the live data as of the
       deploy instead. The platform's part takes about two minutes. When the
       source can't branch (the plan doesn't allow it, or an older service),
       the preview falls back to the latest backup.
     - With `previewDatabaseFrom`, also pass `previewDatabaseGrant`: the
       ServiceCopyGrant a Project admin created in that project (`app` = the
       production app's name, the repository name in lower case; optional
       masking, as for builds on push). The platform refuses copies by the
       workflow's identity without one, so ask the user to create it first.
       Masking there does not stop pull-request code (it runs as an
       identity of the source's project); when PR authors must not see
       production data, recommend builds on push instead.
     A branch locks every login it copied except its own owner. While
     branches exist, the source can't be deleted, restored in place or
     upgraded to a new major version; tell the user that before they try.
  3. Where the installation's GitHub-connect worker is set up,
     `connect_repository` opens a pull request with the files and sets the
     variables itself (the returned file and command lists are then empty): tell the user to
     merge it. Otherwise write every returned file into the repository
     unchanged, run the returned `gh variable set` commands, commit and push.

  A push to `main` then builds the image, deploys it, and succeeds only when
  the live URL answers.

To push to a managed registry by hand, `registry_login` (project, registry
name) returns a `docker login` command valid for 15 minutes with the user's
own rights: project admins and developers push to `<project>/`, members pull.

## With a manifest: an Application

Use an Application for a web app or a worker from one image or one
repository. An image app:

```yaml
apiVersion: apps.kube-dc.com/v1alpha1
kind: Application
metadata:
  name: "{app}"
  namespace: "{project-backing-namespace}"
spec:
  source:
    image:
      reference: "{registry}/{image}@sha256:{digest}"
  run:
    port: 8080
    healthPath: /health        # only a path the image serves
    resources: { cpu: 500m, memory: 512Mi }
    env:
      - name: DB_PASSWORD
        secretKeyRef: { name: "{binding-secret}", key: password }
    restartOnSecretChange: true
```

Built from GitHub on every push, into a managed registry, with previews (the
repository needs the GitHub App and the organization's consent, see "Deploy
from GitHub on every push" above):

```yaml
apiVersion: apps.kube-dc.com/v1alpha1
kind: Application
metadata:
  name: "{app}"
  namespace: "{project-backing-namespace}"
spec:
  source:
    github: { repository: "{owner}/{repo}", branch: main }
  image:
    registry:
      managedService: { name: "{registry}" }   # add project: when it is another Project
  run:
    port: 8080
  previews:
    enabled: true
```

Field rules the API enforces (`kubectl apply --dry-run=server` reports them
before anything is stored):

- `source` is exactly one of `image.reference` and `github`; the kind of
  source and the repository cannot change later (create another
  Application), while `image.reference`, `branch` and `contextDir` can. `image` and
  `build` go with a `github` source only: `image.registry.managedService` or
  `image.repository` with `image.pushSecretRef` for the user's own registry.
- `build`: `strategy` `Auto` (default; Dockerfile, else Railpack, then the app
  listens on `$PORT`), `Dockerfile` or `Railpack`; `args` (never secrets),
  `secretRefs` (Dockerfile `RUN --mount=type=secret`), `automatic: false` to
  build only when asked.
- `run.type: Worker` has no `port`, `public`, `hostnames`, `healthPath` or
  `keepPreviousWarm`. `public` defaults to `true` (HTTPS on the shared
  gateway, no EIP); `public: false` keeps it in the Project.
- `run.hostnames`: one custom hostname (DNS must point at the platform
  first, as for `deploy_app domain`); omitted, the platform address. A list
  of two passes the dry run but stops the platform managing the app
  (`UnsupportedField`; what runs keeps serving) until one is removed. Only
  `deploy_app` and the console fill in the organization's default domain for a
  new app; a manifest gets it only by naming it here.
- `run.autoscale` (`min`, `max`, `cpuPercent` of the CPU limit, default 70)
  excludes `replicas` and `volume`. `run.resources.cpu` is the CPU limit
  (default 500m, request 50m); `memory` is request and limit (default 256Mi).
- `run.volume` (`path`, `size`, `storageClass`) is one claim `{app}-data`,
  one replica, no `keepPreviousWarm`; it is kept on delete unless
  `deletionPolicy.volume: Delete`, and a kept claim is not used by a new
  Application of the same name. Name a replicated `storageClass` the
  Project offers (`manage-storage`).
- `run.releaseCommand`, `run.keepPreviousWarm` (1m to 24h), `previews`
  (`hours`, `project`, `buildArgs`, `database` with `plan` or `from` and
  `mode`) work as their `deploy_app` counterparts above.

Operate it:

```bash
kubectl get app                          # repository (GitHub apps), commit, state, URL
kubectl describe app {app}               # conditions and Events: what waits and why
kubectl get appbuild                     # every build; spec.applicationRef names the app (the label misses builds made by hand)
kubectl logs build-{build}               # a build's first attempt (retries: build-{build}-2; long names are shortened)
kubectl patch app {app} --type merge -p '{"spec":{"release":{"pin":{"release":5}}}}'   # roll back
kubectl patch app {app} --type json -p '[{"op":"remove","path":"/spec/release/pin"}]'  # resume
kubectl patch app {app} --type merge -p '{"spec":{"suspend":true}}'                    # pause
```

A rebuild is `kubectl create -f` of an `ApplicationBuild` with
`generateName: {app}-` and `spec.applicationRef.name: {app}`; cancel one with
`kubectl patch appbuild {build} --type merge -p '{"spec":{"cancelled":true}}'`.
Raise `spec.release.attempt` to run a failed release command again.

Conditions: `SourceResolved`, `ImageRepositoryReady`, `Built`, `Released`,
`RoutePublished` and `Available`; `Ready` looks at them in this order and sums
them up
(`Pinned` while a rollback holds). The first one that is not True says what
the app waits for: report its message to the user rather than guessing. All
six can be True while `Ready` is False during a rollout; its own message
says so.

## Plain workloads

Use a plain Deployment (sections 1 to 4) only for what an Application does
not cover: several containers or sidecars, init containers, custom probes or
security settings, a TCP/UDP port behind a `LoadBalancer`, Helm charts, and
Shared GPU workloads.

## Prerequisites

- The Project reports `status.ready: true`.
- `kube-dc use {domain}/{organization}/{project}` selects the intended Project.
- CPU, memory, pod, and storage quota have enough headroom.
- The caller can create the required workload kinds.
- For HTTPS, the platform issues the certificate; no Issuer is needed.

Confirm the active backing namespace:

```bash
kubectl config current-context
kubectl config view --minify -o jsonpath='{.contexts[0].context.namespace}{"\n"}'
kubectl auth can-i create applications.apps.kube-dc.com
kubectl auth can-i create deployments.apps
kubectl auth can-i create services
```

## 1. Create the workload

Pin production images by digest and set requests and limits:

```yaml
apiVersion: apps/v1
kind: Deployment
metadata:
  name: "{app}"
  namespace: "{project-backing-namespace}"
spec:
  replicas: 2
  selector:
    matchLabels:
      app.kubernetes.io/name: "{app}"
  template:
    metadata:
      labels:
        app.kubernetes.io/name: "{app}"
    spec:
      containers:
        - name: "{app}"
          image: "{registry}/{image}@sha256:{digest}"
          ports:
            - name: http
              containerPort: 8080
          resources:
            requests:
              cpu: 100m
              memory: 128Mi
            limits:
              cpu: 500m
              memory: 512Mi
          readinessProbe:
            httpGet:
              path: /health
              port: http
            initialDelaySeconds: 5
            periodSeconds: 10
```

Adapt the health path to the application. Do not add a probe that the image
does not implement.

When persistent data is required, create a PVC with a live Project
StorageClass and mount it. The cluster default class can be node-local
(`local-path`: no expansion, data lost with the node); prefer a replicated
class such as the one the Project offers for volumes. Follow `manage-storage`; do not assume a provider
class name or ReadWriteMany support.

## 2. Add a managed database when needed

Use `create-database` to provision the engine the application actually
supports, as a managed service on the development plan unless the user asks
for production or HA. Do not run a database, cache or queue in a pod, and
disable a chart's bundled database. Read credentials through the documented Secret instead of putting a
password in the Deployment:

```yaml
env:
  - name: DB_HOST
    valueFrom: { secretKeyRef: { name: "{binding-secret}", key: host } }
  - name: DB_PORT
    valueFrom: { secretKeyRef: { name: "{binding-secret}", key: port } }
  - name: DB_NAME
    valueFrom: { secretKeyRef: { name: "{binding-secret}", key: dbname } }
  - name: DB_USER
    valueFrom: { secretKeyRef: { name: "{binding-secret}", key: username } }
  - name: DB_PASSWORD
    valueFrom:
      secretKeyRef:
        name: "{binding-secret}"
        key: password
```

That example is PostgreSQL. For MySQL and MariaDB, use the `database` key for
`DB_NAME`. Mount the binding's `ca.crt` and configure the client to verify TLS,
as shown in [connection patterns](../create-database/db-connection-patterns.md).
The binding Secret follows credential rotation. Restart Pods that read
credentials from environment variables after rotation.

Prefer a Helm chart option such as `existingSecretPasswordKey` when its expected
key differs. A manually copied bridge Secret does not follow password rotation
unless separate automation updates it.

## 3. Create an internal Service

```yaml
apiVersion: v1
kind: Service
metadata:
  name: "{app}"
  namespace: "{project-backing-namespace}"
spec:
  selector:
    app.kubernetes.io/name: "{app}"
  ports:
    - name: http
      port: 80
      targetPort: http
```

Use `ClusterIP` when only in-Project callers need the application.

## 4. Expose the Service only when requested

Follow `expose-service` for the full contract:

- HTTP/HTTPS: annotate a `ClusterIP` Service with
  `service.nlb.kube-dc.com/expose-route`. The shared Gateway publishes it
  with a hostname (and a certificate for `https`) and allocates no EIP. Do
  not use `type: LoadBalancer` for web traffic: it allocates its own EIP,
  usually a public IPv4 that counts against quota.
- Arbitrary TCP/UDP: bind a `LoadBalancer` Service to the Project gateway
  address (`bind-on-default-gw-eip: "true"`), or allocate or select an EIP
  only when the user asks for a dedicated address.
- TLS passthrough: the application owns its certificate.
- Do not create a `GRPCRoute`; Kube-DC currently reconciles HTTPRoute and
  TLSRoute.

A minimal HTTPS Service is:

```yaml
apiVersion: v1
kind: Service
metadata:
  name: "{app}"
  namespace: "{project-backing-namespace}"
  annotations:
    service.nlb.kube-dc.com/expose-route: "https"
    service.nlb.kube-dc.com/https-redirect: "true"   # optional: HTTP -> HTTPS
spec:
  type: ClusterIP
  selector:
    app.kubernetes.io/name: "{app}"
  ports:
    - name: http
      port: 80
      targetPort: http
```

Read the assigned hostname from status annotation. The provider domain is live
configuration, not a value to construct in client code:

```bash
kubectl get service {app} \
  -o jsonpath='{.metadata.annotations.service\.nlb\.kube-dc\.com/route-hostname-status}{"\n"}'
```

## Helm applications

Review rendered manifests before installing:

```bash
helm template {release} {chart} --namespace {project-backing-namespace} \
  --values values.yaml
helm upgrade --install {release} {chart} \
  --namespace {project-backing-namespace} \
  --values values.yaml
```

Helm stores each release in a Secret, and the Project `developer` role cannot
create Secrets: install charts as a Project `admin`, or render with
`helm template` and apply the manifests. For a database, cache or queue a
chart would bundle, disable it in the chart and use a managed service instead
(`create-database`).

Standard Project roles cannot manage NetworkPolicy. Disable a chart's optional
NetworkPolicy objects when present, or ask the platform operator for an
approved network policy change. Also inspect PVC classes, Service annotations,
hooks, and cluster-scoped resources before installation.

## Shared GPU workloads

When Shared GPU is explicitly requested, read
[references/shared-gpu.md](references/shared-gpu.md). Apply the fixed-product
ResourceClaimTemplate and Deployment from that reference, and tell the user
before applying that the workload can wait `Pending` for a free slice. Do not
add native GPU resources to the generic Deployment.

## Verify

For an Application:

```bash
kubectl wait --for=condition=Ready app/{app} --timeout=15m
kubectl get app {app} -o jsonpath='{.status.url}{"\n"}'   # empty for public: false apps and workers
curl --fail --show-error --silent -o /dev/null "$(kubectl get app {app} -o jsonpath='{.status.url}')"
```

For a plain Deployment:

```bash
kubectl rollout status deployment/{app} --timeout=10m
kubectl get deployment,pod,service
kubectl get endpointslice \
  -l kubernetes.io/service-name={app}
kubectl logs deployment/{app} --tail=100
```

For an exposed Service:

```bash
HOST="$(kubectl get service {app} \
  -o jsonpath='{.metadata.annotations.service\.nlb\.kube-dc\.com/route-hostname-status}')"
test -n "$HOST"
curl --fail --show-error --silent -o /dev/null "https://$HOST/"
```

If rollout fails, use `kubectl describe deployment`, `kubectl describe pod`,
events, and logs. Pod exec and attach are blocked in Project backing
namespaces, so use a temporary Job or an application diagnostic endpoint when
an active check is required.

## Safety

- Keep credentials in Secrets and never print them.
- Keep resource requests below quota with rollout headroom (a warm previous
  version takes one more replica of quota during its window).
- Never edit the Deployment or Service an Application generated; change the
  Application.
- Ship schema changes expand/contract so the previous version can still run
  after a rollback.
- Default to internal access, then add only the exposure the caller needs.
- Confirm PVC retention before uninstalling a Helm release; Helm commonly
  leaves claims behind.
- Use the gated product workflow for Shared GPU.
