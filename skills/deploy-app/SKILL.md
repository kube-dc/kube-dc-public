---
name: deploy-app
description: Deploy a containerized application to a Kube-DC Project with optional persistent storage, a managed database, external exposure, or gated Shared GPU acceleration; set up keyless deploys, builds on push and pull request previews from a GitHub repository, with a managed container registry or the user's own.
---

# Deploy an Application

Deploy ordinary Kubernetes workloads into a Project's backing namespace. Keep
the Project context, quota, storage, database, and exposure decisions explicit.

## With the hosted MCP server

When the Kube-DC MCP connector is available, prefer its app tools over
writing the manifests below:

- `deploy_app` (project, name, image, port; optional `healthPath`, `env`,
  `envFromSecrets` for a database binding Secret, `pullSecret` for a private
  registry, `public: false` for in-Project only) creates or updates the
  Deployment and a ClusterIP Service with `expose-route: https`. Call it again
  with a new image tag to ship a new version.
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
  in the console (**Environment** on the Deployment). After such an edit, a
  `deploy_app` that sets a different value for the same variable reports a
  field-ownership conflict: confirm with the user, then retry with
  `force: true`.
- Release command (migrations): `deploy_app releaseCommand` (for example
  `["npm", "run", "migrate"]`, optional `releaseTimeoutMinutes`, default 15)
  runs once with each new image, with the app's variables and secrets,
  before that image takes traffic. If it fails, the running version keeps
  serving and `rollout_status` reports `failed` (`ReleaseFailed`) with the
  command's log; deploying the same image again retries. It runs for builds
  on push and previews too, never on a rollback: write migrations the
  previous version can live with (expand/contract).
- Workers: leave out `port` for a queue consumer (no Service, no hostname,
  no `warmPrevious`).
- Jobs: `run_job` runs a command once (`app` for the app's image and
  secrets, or `image`); `schedule_job` runs one on a cron `schedule`
  (`timeZone`, `concurrency`, `suspend`). A cron job made from an app follows
  its new image and is deleted with it; previews run no schedules.
  `list kind=jobs` shows runs and pods; `get_logs` reads them.
- `autoscale: { min, max, cpuPercent }` scales on CPU as a share of the CPU
  limit (not with `replicas`); `volume: { size, path }` mounts a persistent
  volume `<app>-data` (one replica; kept when the app is deleted). Tell the
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
  console (**Organization → Domains → Connect a domain**; `apps.<domain>` is
  suggested so the website and mail stay untouched): a TXT record proves it,
  a wildcard `*.<domain>` points every name under it at the platform, and the
  first domain becomes the default for new apps. Suggest this to users who
  want many apps on their own domain, instead of one record per app.
- Organization default domain: when it is set and `*.<domain>` points at the
  platform, a new public app without `domain` gets `<app>.<domain>` (or
  `<app>-<project>.<domain>` when taken); the result says so. Pass
  `domain: ""` for the platform address. Existing apps and previews never
  change.
- The main domain itself (`domain: "example.com"`, a verified domain of the
  organization): one app may take it; `www.example.com` then redirects to it.
  It needs A records for `@` (or an ALIAS/ANAME/flattened CNAME to
  `gateway.<baseDomain>`) and a `www` CNAME to the domain; tell the user a
  website already there moves to this app.
- `rollout_status` reports `ready` with the HTTPS URL, `progressing`, or
  `failed` with the reason (image pull, crash loop, scheduling) and the
  failing pod's last log lines. Check about 30 seconds after a deploy; a new
  hostname gets its certificate within 1 to 3 minutes.
- `restart` and `rollback` (to the previous revision by default) change a
  running app: ask the user first.
- Delete an app: `delete` with `kind: app` removes the Deployment, Service,
  autoscaler, cron jobs, the warm previous version and its previews (with
  their database copies). The data volume `<app>-data` stays unless
  `deleteData: true`; registry images and the repository are kept. Ask the
  user first and say what goes. A preview (`{app}-pr-<n>`) is refused: it
  goes when its pull request closes or it expires (`extend_preview` with
  `hours: 1` ends it within the hour; the console's **Previews → Remove**
  ends it now).
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
- Previews: `deploy_app` with `previewHours` (for example a pull request's
  copy, named `{app}-pr-{number}`) is removed that long after its last
  deploy. The platform warns two hours before; `extend_preview` adds hours.
- GitHub: pass `githubRepository` (`owner/repo`), `githubSha` (the full
  commit) and optionally `githubEnvironment` to `deploy_app`. Where the
  installation's GitHub App is installed on the repository, the commit and
  pull request then show the deployment with its live URL or the failure.
  Otherwise `rollout_status` says how to record the result with `gh api`.
- `create_webhook` (organization admins) sends the organization's platform
  events, such as `DeploymentRolledOut`, `DeploymentFailed` and
  `RouteReady`, to an HTTPS URL, signed. Use it when the user wants to hear
  about deploys elsewhere (chat, a script) instead of polling.

An app first deployed with `kubectl apply` keeps its selector; `deploy_app`
reports a field-ownership conflict and needs `force: true` to take over.
Use the manifests below for volumes, several containers, Helm charts or
anything the tool does not cover.

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
  no sign-in; `baseDomain` from `whoami`) returns `githubApp.enabled` and
  `githubApp.installUrl`. No `githubApp`, or `enabled: false`, means no
  builds on push and no deployment status on commits: use a GitHub Actions
  workflow with GHCR. `deploy_app` with `buildFrom` also refuses with "This
  installation does not build on push".

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

- **Builds on push by the platform** (no CI in the repository): `deploy_app`
  with `buildFrom` and an `image` path without a tag. Managed registry:
  `<registry host>/<project>/<app>`, no push Secret. Own registry: also
  `pushSecret`, a `kubernetes.io/dockerconfigjson` Secret the user created.
  Pushes build and deploy; pull requests build a preview when a previews
  project is given; the commit shows a `kube-dc` check run.
  - New project registry: `buildFrom.registry: <name>` names a managed
    registry of the project (family `registry`) instead of `image`, also one
    still being created; the platform sets the image path when it is Ready
    and the first build waits for it (up to 30 minutes).
  - A registry in another project: one registry serves several projects of
    the organization. Add the app's project to that registry's
    `parameters.projects` (`apply_manifest` on the ManagedService, by someone
    with write access to the registry's project), wait until it is Ready again,
    then pass `image: <registry host>/<app's project>/<app>`. The host is
    the registry's `https` endpoint, `<registry>-<org>-<registry's
    project>.<baseDomain>`. The list is replaced, not merged: read it first
    and send every project, the existing ones too. Removing a project
    later is a separate edit; deleting the project does not do it.
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
  - First build: a new app is built from the default branch at once
    (`buildFrom.buildNow`, default true for a new app); pass `buildNow: true`
    again to rebuild an existing app without a push.
  - No Dockerfile: `buildFrom.builder: railpack` detects the language (Node.js,
    Python, Go, Rust, Ruby, PHP, Java, .NET, Elixir, Deno, static sites). The
    app must listen on `$PORT`: set `env: { PORT: "8080" }` and `port: 8080`.
    `buildArgs` and `buildSecrets` reach every build step as environment
    variables. A repository without app files (only a README) cannot be built.
  - Another branch: `buildFrom.branch: <branch>` builds pushes to that branch
    instead of the default branch; pull request previews are unchanged.
  - The console's **Applications → New application** does all of this for
    the user in a few clicks (repository picker, registry, port, branch):
    suggest it to users who prefer the console. Each app then has a page
    under **Applications** with its versions, processes and autoscaling, own
    domain, previews, logs and **Settings → Delete application**.
  - Build arguments: `buildFrom.buildArgs` sets Dockerfile `ARG` values for
    every build (for example `{ "VITE_API_URL": "https://api.example.com" }`)
    and `previewBuildArgs` overrides them key by key for previews. They stay
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
    `build.kube-dc.com/preview-database-error` says why.
    Copies carry production data: suggest masking. The grant's
    `spec.masking: {configMap, key, sha256}` names a plain-SQL script (no
    psql commands, idempotent, run as the database owner before anyone can
    connect); the sha256 is of the ConfigMap value's exact bytes. A failed
    script deletes the copy and the error annotation gives the SQL error. The app must read its database from one Secret
    (`envFromSecrets`/`secretEnv`), default `<name>-db-owner`, else name it
    in `previewDatabaseSecret`; a preview reads `<name>-pr-<n>-db-owner` there.
  - Previews never read the app's own secrets: a preview reads
    `<secret>-preview` in its project instead (in a separate previews
    project, a secret of the same name there also counts), for runtime
    variables and build secrets. Variables from other secrets are left out,
    and a preview build missing a build secret is not started (reason in
    `build.kube-dc.com/last-error`). Create the copies, with test keys,
    using `create_secret` in the preview's project.
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
  3. Write every returned file into the repository unchanged, run the
     returned `gh variable set` commands, commit and push.

  A push to `main` then builds the image, deploys it, and succeeds only when
  the live URL answers.

To push to a managed registry by hand, `registry_login` (project, registry
name) returns a `docker login` command valid for 15 minutes with the user's
own rights: project admins and developers push to `<project>/`, members pull.

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
- Ship schema changes expand/contract so the previous version can still run
  after a rollback.
- Default to internal access, then add only the exposure the caller needs.
- Confirm PVC retention before uninstalling a Helm release; Helm commonly
  leaves claims behind.
- Use the gated product workflow for Shared GPU.
