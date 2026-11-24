# Manage applications

An application is an `Application` resource (`apps.kube-dc.com/v1alpha1`)
in your Project. It says where the app comes from (a GitHub repository or a
container image) and how it runs. The platform builds it, deploys it,
publishes its address and keeps it in that state. It creates a Deployment and
a Service named after the app and owns them: change the Application, not
those objects. An Application is not the only way to run containers: you
can also apply your own Kubernetes manifests. [Ways to deploy](deploy-options.md)
compares the options.

You create one with **New application** in the console (from a GitHub
repository, where your provider offers builds; see
[Deploy from GitHub](deploy-from-github.md)), with
`kubectl apply` (see [Deploy your first application](deploy-first-app.md)),
or with an AI app (`deploy_app`). They make the same resource, so you can
switch between them at any time.

**New application** and AI apps make an Application where your provider has
turned that on for new apps (see
[Apps created before Applications](#apps-created-before-applications)).

You need the developer role in the Project to change an application. If an
Application you applied still shows no status after a minute, your provider
has not turned Applications on: delete it and ask your provider.

## In the console

Open your Project, then **Applications**. The list shows each app with its
source, address and state. Click an app to open its page. **Open the app**
opens its address; **Redeploy** builds the branch's latest commit and deploys
it (apps built from GitHub). The tabs:

| Tab | What it shows and does |
|---|---|
| Overview | **Progress** (Source, Registry, Build, Release, Rollout, Address), the latest build, and, while the app is not ready, what is serving now |
| Builds & releases | **Builds**: every build with its commit and result, **Log** or **Log tail**, cancel a running one, **Build again**. **Releases**: every version that ran, **Roll back to this release**, and **Retry release** after a failed release command |
| Processes | Type (web or worker), replicas or autoscaling, release command, data volume, instant rollback, and the app's scheduled jobs |
| Environment | Variables and the Secrets they come from |
| Domains | The app's address and its custom hostname |
| Previews | One preview per pull request; **Create preview** for a pull request whose preview expired or was removed |
| Logs | The app's pods and their logs |
| Settings | **Pause builds** / **Resume builds**, **Edit YAML** and **Delete application** |

Settings are changed with **Edit YAML** on the tab that shows them (the
**Previews** tab has none: edit the app's YAML on another tab).

- A change to what the pods run makes a new numbered release: the image,
  variables, resources, port, health path, pull Secrets, and adding or
  removing the volume.
- Other changes apply without one: replicas, autoscaling, hostnames,
  `public`, instant rollback, previews, a volume's size and the release
  command (a new or changed release command runs before the next release).

While the newest build of a GitHub app is running or has failed, the running
release keeps serving and a change to it waits: a fixed variable rolls out
with the next successful build, or after **Build again**. Replicas, the
autoscaler and restarts on a Secret change apply at once.

**Pause builds** stops new builds and rollouts; the running version keeps
serving.

After a rollback, a banner with **Resume deploys** appears, and once the
release it pins is live the app shows as **Rolled back** (see
[Roll back an application](rollback.md)).

### What the progress steps mean

Each step is a condition on the Application (`kubectl describe app <name>`
shows them, with a message and Events):

| Step | Condition | Done when |
|---|---|---|
| Source | `SourceResolved` | The branch's commit is known (or the image is set) |
| Registry | `ImageRepositoryReady` | The registry the image goes to is ready |
| Build | `Built` | The image of that commit is built |
| Release | `Released` | The release command, if any, succeeded |
| Rollout | `Available` | Pods of the wanted release are ready (while it rolls out, only the previous release's may be) |
| Address | `RoutePublished` | The address and its certificate are live (public apps) |

`Ready` sums them up. While something waits or fails, its message says what
and why, for example a build that failed, a Secret that is missing, or a
GitHub App that is not installed on the repository. An image app needs no
registry or build: those steps show **Not needed**.

## Write an Application yourself

An image app needs only the image:

```yaml
apiVersion: apps.kube-dc.com/v1alpha1
kind: Application
metadata:
  name: hello
spec:
  source:
    image:
      reference: nginxinc/nginx-unprivileged:1.27
  run:
    port: 8080
```

An app built from GitHub also says where the image goes. This one reads a
database password from a Secret, restarts when that Secret changes, and gets a
preview for every pull request:

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
        name: registry        # a managed registry in this project
  run:
    port: 8080
    env:
      - name: LOG_LEVEL
        value: info
      - name: DATABASE_URL
        secretKeyRef: { name: notes-db-owner, key: uri }
    restartOnSecretChange: true
  previews:
    enabled: true
    hours: 72
```

Builds from GitHub are available where your provider offers them; they need
the platform's GitHub App on the repository and a container registry (see
[Deploy from GitHub](deploy-from-github.md)).

`kubectl apply --dry-run=server -f app.yaml` reports a wrong field before
anything is stored. The platform does not rewrite your manifest, so it can
live in a GitOps repository.

Day-to-day commands:

```bash
kubectl get app                       # repository (GitHub apps), commit, state and address
kubectl describe app notes            # conditions and Events
kubectl get appbuild                  # every build, with its app
kubectl logs -f build-notes-7         # the pod of build notes-7 (a retry runs as build-notes-7-2; long names are shortened)
kubectl create -f rebuild.yaml        # build again (below)
kubectl patch appbuild notes-7 --type merge -p '{"spec":{"cancelled":true}}'          # cancel a build
kubectl patch app notes --type merge -p '{"spec":{"release":{"pin":{"release":5}}}}'  # roll back to release 5
```

A rebuild is an `ApplicationBuild` you create; without `commit` it builds the
head of the branch:

```yaml
apiVersion: apps.kube-dc.com/v1alpha1
kind: ApplicationBuild
metadata:
  generateName: notes-
spec:
  applicationRef:
    name: notes
```

### The fields

| Field | What it does |
|---|---|
| `source.image.reference` or `source.github` (`repository`, `branch`, `contextDir`) | Where the app comes from. The kind of source and the repository cannot change later: create another Application |
| `image.registry.managedService` (`name`, `project`) | A managed registry for built images; `project` defaults to the app's own Project. Or `image.repository` with `image.pushSecretRef` for a registry you run (see [Deploy from GitHub](deploy-from-github.md#where-images-go)) |
| `build` | `strategy` (`Auto`, `Dockerfile` or `Railpack`), `dockerfile`, `args`, `secretRefs`, `cache`, and `automatic: false` to build only when asked |
| `run.type` | `Web` (default) or `Worker`, which has no port, address or health check |
| `run.port`, `run.healthPath`, `run.public` | The port the app listens on (default `8080`: set it for an app that listens elsewhere; the platform sets `PORT` only for apps built with Railpack); the readiness path; `public: false` keeps the app inside the Project (default `true`) |
| `run.hostnames` | One custom hostname (see [Use your own domain](#use-your-own-domain)); omitted, the platform address. **New application** and `deploy_app` fill in the organization's default domain for a new public app |
| `run.imagePullSecrets` | Secrets to pull a private image with (an app on a managed registry needs none) |
| `run.env`, `run.envFromSecrets`, `run.restartOnSecretChange` | Variables, from a value or a Secret (or a [ManagedSecret](secrets-manager.md)), and restarts when a Secret they read changes; a restart is not a new release |
| `run.replicas`, `run.resources` (`cpu` limit, `memory`), `run.autoscale` | Size of the app (see [Autoscaling](app-jobs-and-workers.md#autoscaling)) |
| `run.volume` (`path`, `size`, `storageClass`) | A data volume `<app>-data`; see [A volume](app-jobs-and-workers.md#a-volume) |
| `run.releaseCommand`, `run.keepPreviousWarm` | See [Release commands, workers and cron jobs](app-jobs-and-workers.md) and [Roll back an application](rollback.md) |
| `previews` | Previews of pull requests (see [Deploy from GitHub](deploy-from-github.md#previews)) |
| `release.pin`, `release.attempt` | Roll back to a release; run a failed release command again |
| `suspend` | `true` pauses builds and rollouts |
| `deletionPolicy.volume` | `Retain` (default) keeps the data volume when the app is deleted; `Delete` deletes it |
| `detach` | `true` makes the platform stop managing the app for good and leave its Deployment and Service in place; it cannot be undone. A detached app's data volume is kept when the app is deleted, whatever `deletionPolicy` says |

Only one hostname is supported. A list of two passes `--dry-run`, but the
platform then stops managing the app (`UnsupportedField`) until you remove
one; what runs keeps serving.

`kubectl explain application.spec --recursive` lists every field with its
description; where it differs from this table on `run.port` or
`run.hostnames`, this table is current.

## From the command line

The `kube-dc app` commands of the [kube-dc CLI](cli-kubeconfig.md) work on the
current Project (or `-n <namespace>`); they come with the CLI release that
follows v0.9.26 (`kube-dc app --help` shows whether yours has them):

| Command | Does |
|---|---|
| `kube-dc app list` | The Project's applications |
| `kube-dc app status <app>`, `kube-dc app wait <app>` | The state in plain words; wait until the app is ready |
| `kube-dc app builds <app>`, `kube-dc app logs <app> [--build <name>]` | Builds, newest first, and a build's log |
| `kube-dc app rebuild <app> [--commit <sha>]`, `kube-dc app cancel <build>` | Build again; cancel a build |
| `kube-dc app rollback <app> [--to <release>]`, `kube-dc app resume <app>` | Roll back (default: the release before the running one); end the rollback (the newest release deploys once its build has succeeded) |
| `kube-dc app release retry <app>` | Run a failed release command again |
| `kube-dc app pause <app>`, `kube-dc app unpause <app>` | Pause and resume builds and rollouts |
| `kube-dc app open <app>` | Print the address |
| `kube-dc app delete <app> --yes [--delete-volume]` | Delete the app; with `--delete-volume`, its data volume too |

## AI apps

Through the [hosted MCP server](mcp-for-administrators.md), an AI app works
with the same resource:

| Tool | For an Application |
|---|---|
| `deploy_app` | Creates the app, or changes it. Every call names the app's image: `image`, or for an app built from GitHub `buildFrom` again with `image` or `buildFrom.registry`; other omitted arguments keep their value, except variables: `env` or `secretEnv` replaces the whole list, so pass every variable the app needs. `buildFrom` builds from GitHub, `suspend` pauses or resumes builds and rollouts |
| `rollout_status` | State, address, release, latest build and the pods |
| `rollback` | Rolls back to the previous release, or to `release`, and pins the app there (new builds stop deploying); `resume: true` ends the rollback |
| `list` | Kind `apps` lists the Project's apps, kind `builds` the Project's builds (one app's with `app`) |
| `get_logs` | A pod's log, or with `build` a build's log |
| `delete` | Kind `app` deletes an app (`deleteData` for its volume), kind `build` cancels a build |

## Use your own domain

The platform address uses the project namespace,
`<app>-<project-namespace>.<platform-domain>`. To serve the app on your own
domain, set its hostname: on the **Domains** tab click **Edit YAML** and set

```yaml
spec:
  run:
    hostnames: [app.example.com]
```

or ask an AI app to `deploy_app` with `domain: "app.example.com"`. Before
that, add the records at your DNS provider:

- **CNAME** from your domain to the app's platform address. A bare domain
  (`example.com`) cannot have a CNAME: see
  [The main domain itself](#the-main-domain-itself). On Cloudflare, set the
  record to **DNS only** (grey cloud): the platform issues the certificate
  itself and cannot while Cloudflare proxies.
- **TXT**, where your provider requires verified domains: it proves your
  organization controls the domain. An organization admin adds the domain
  under **Organization → Domains**; verifying `example.com` also covers every
  name under it. Keep the TXT record; it is checked again daily. See
  [Verified domains](service-exposure.md#verified-domains).

The certificate is issued within 1 to 3 minutes after the hostname is set;
until then browsers warn and the **Address** step waits. An app answers on
one hostname: setting one replaces the platform address. Remove `hostnames`
to go back.

### Many apps under one domain

To run many apps as `git.example.com`, `jira.example.com`, `app1.example.com`
and so on, connect the domain once for the whole organization. An
organization admin opens **Organization → Domains** (shown where your provider
uses verified domains) and clicks **Connect a domain**:

1. **Domain**: enter the domain.
2. **Use**: choose one of:
   - **Many apps**: each app gets a name under the domain. For a domain like
     `example.com`, tick the `apps.<domain>` option to keep them under that
     subdomain, so your website, mail and other names stay untouched.
   - **One app on it**: one app answers on the domain itself.
   - **Both** (the default).

   **New apps get `<app>.<domain>`** is ticked when your organization has no
   default domain yet; the default is where new apps get their address.
3. **DNS**: add the records shown at your DNS provider; each has copy
   buttons.
   - A **TXT** record `_kube-dc.<domain>` proves your organization controls
     the domain. Keep it: it is checked again daily.
   - For many apps, a **wildcard** `*.<domain>` record (a CNAME to the
     platform name shown, or A records to the addresses shown), so no app
     under the domain needs a record of its own. It only covers names
     without a record of their own, so `@`, `www` and `mail` keep working.
   - For one app on the domain itself (**One app on it** or **Both**), an A
     record for `@` and a `www` CNAME, with a warning that a website you run
     there moves to the app. An Application answers only on the domain
     itself and `www` is not redirected to it: for an Application, leave the
     `www` record out and the wizard's app choice empty, and set the domain
     in its `run.hostnames`.

   If your DNS panel adds the domain itself, enter only the part shown (for
   example `_kube-dc.apps` and `*.apps`). On Cloudflare, set the records to
   **DNS only**. The page also says when the wildcard points somewhere else,
   when an AAAA (IPv6) record must go, and when a CAA record keeps Let's
   Encrypt from issuing certificates.

   **Finish** is enabled once the domain is verified and its records are
   found. Then a new public app made with **New application** or
   `deploy_app` gets `<app>.<domain>` (or `<app>-<project>.<domain>` when
   another app has that name). Existing apps keep their addresses, and an
   Application you write yourself gets the domain only when you set it in
   `hostnames`.

If you replace an existing default, finish the wizard after the new domain is
verified; the previous default serves new apps until then. If you close the
wizard, select **Use for new apps** on the verified domain. An explicit DNS
record takes precedence over the wildcard: if a proposed app hostname points
to another site, keep that record and set a different hostname. **Check now**
uses the installation's DNS resolver, and a missing record can stay cached
until its DNS TTL expires.

If the TXT record disappears later, the domain stays verified and existing
apps keep serving. To stop using a domain, open **Organization → Domains**,
choose **Stop using for new apps** if it is the default, then **Remove
domain**. Removal is refused while an Application has a hostname on the
domain: change those apps' `hostnames` first. Your public DNS records stay
as they are at your DNS provider.

The **Applications** page, and the Deploy from GitHub part of the project
overview's **Create a resource** card, show where new apps get their address.
Previews keep platform addresses. Each name gets its own certificate.

### The main domain itself

An app can answer on the domain itself (`example.com`): set it as the app's
hostname. The main domain needs its own record: `@` as A records to the
platform's addresses or, if your provider offers ALIAS, ANAME or CNAME
flattening (Cloudflare does), pointed at the platform name, so a change of the
platform's address cannot break it. A website you run on the domain moves to
the app once you change that record. An Application answers only on the
hostname you set, so `www.example.com` is not redirected to it.

## Delete an application

**Settings → Delete application**, then type the app's name and click
**Delete resource**. This removes the app, its
address, its autoscaler, its scheduled jobs, its warm previous version and all
its previews with their databases. Its data volume (`<app>-data`) is kept
unless the app has `deletionPolicy.volume: Delete` (the **Settings** tab says
which). A kept volume belongs to the deleted app: a new Application of the
same name does not use it (it reports the name as in use). Copy the data out
and delete the claim before you create the app again. The repository on
GitHub and the images in the registry are not touched, and pushes are no
longer built.

`kubectl delete app <name>` follows `deletionPolicy.volume` as it is.
`kube-dc app delete <name> --yes` and AI apps (`delete` kind `app`, after you
confirm) keep the volume unless you ask for it: without `--delete-volume`
(or `deleteData`) they set the policy to `Retain` first, even when it was
`Delete`.

## Apps created before Applications

Apps deployed before your installation offered Applications keep running, and
the console and AI apps still operate them (the list marks them **Not
migrated**). Until your provider turns Applications on for new apps, **New
application** and AI apps make apps of this older kind too; `kubectl apply`
of an Application makes an Application wherever Applications run. There is no migration tool: to make one an Application, delete it
and create it again with the same name, which keeps its platform address. Set
its custom domain again in `run.hostnames`, and copy its data volume out
first: the new Application does not take over the old `<app>-data` claim.
