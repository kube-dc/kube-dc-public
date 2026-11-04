# Migrations, workers, cron jobs, autoscaling and volumes

An application on Kube-DC is a Deployment. Beyond a web server, most
applications also need a database migration on each release, background
workers, scheduled jobs, more pods under load or a place to keep files. Each
of these is an option of the hosted MCP's `deploy_app` or a job tool, and
each is an ordinary Kubernetes object you can also write yourself.

## Release command (migrations)

A release command runs once with each new image **before** that image takes
traffic, for example `npm run migrate` or `rails db:migrate`:

```json
{ "name": "shop", "image": "registry.example.com/shop/web:1.4.0", "port": 8080,
  "releaseCommand": ["npm", "run", "migrate"], "releaseTimeoutMinutes": 10 }
```

- The command runs in a Job with the new image and the application's
  variables, secrets, pull secret and service account. The Job is called
  `<app>-release-<hash of the image>`.
- When it succeeds, the new image rolls out as usual.
- When it fails or passes the timeout (default 15 minutes), the running
  version **keeps serving**. `rollout_status` reports `failed` with reason
  `ReleaseFailed` and the command's last log lines. Deploying the same image
  again retries it.
- A newer image stops a release that is still running; one release runs at a
  time per application.
- It also runs for builds on push and for pull request previews (each preview
  has its own database copy). It does not run on a rollback: a rollback does
  not undo a migration, so write migrations that the previous version can
  live with (add a column first, remove the old one a release later).
- A new application waits with no pods until its first release succeeds.

The state is in the Deployment's annotations:
`apps.kube-dc.com/release-command` (the command, JSON),
`apps.kube-dc.com/release-image` (the image waiting for it) and
`apps.kube-dc.com/release-status` (`running`, `succeeded`, `failed` or
`superseded`, with the Job and the reason). Events `ReleaseStarted`,
`ReleaseSucceeded` and `ReleaseFailed` are recorded on the Deployment.

## Workers

Leave out `port` and `deploy_app` runs a worker, such as a queue consumer: no
Service, no hostname and no readiness check. A worker cannot keep a warm
previous version (`warmPrevious`): a second running copy would take the same
work twice.

## One-off jobs

`run_job` runs a command once, to completion, for example a seed or a
backfill. With `app` it runs with that application's image, variables and
secrets; with `image` it runs any image. `list kind=jobs` shows its state and
pod, and `get_logs` reads the pod. The Job is removed a day after it finishes.

## Cron jobs

`schedule_job` runs a command on a schedule:

```json
{ "name": "nightly-report", "app": "shop", "command": ["node", "report.js"],
  "schedule": "0 2 * * *", "timeZone": "Europe/Berlin" }
```

- With `app`, the cron job belongs to the application: it is deleted with it,
  and it runs the application's new image after each deploy or build.
- `concurrency` decides what happens when a run is still going at the next
  time: `Forbid` (default) skips, `Replace` stops the running one, `Allow`
  runs both. `suspend: true` pauses it.
- Pull request previews run no schedules.
- `delete` with kind `CronJob` and apiVersion `batch/v1` removes it.

## Autoscaling

```json
{ "name": "shop", "image": "…", "port": 8080, "cpu": "1", "autoscale": { "min": 2, "max": 6, "cpuPercent": 70 } }
```

Kube-DC adds pods when the pods' average CPU use passes `cpuPercent` of the
**CPU limit** (here 700m) and removes them as it drops, between `min` and
`max`. The application then has no fixed replica count; `autoscale: "off"`
removes it. Autoscaling needs the cluster's CPU metrics; where they are
missing the call says so.

## A volume

```json
{ "name": "files", "image": "…", "port": 8080, "volume": { "size": "5Gi", "path": "/data" } }
```

A persistent volume `<app>-data` (ReadWriteOnce) is mounted at `path`. The
application runs one pod, and a new version stops the old pod before it
starts (two pods cannot mount the volume). Deleting the application keeps the
volume and its data; delete the PersistentVolumeClaim to remove them.
Previews get an empty directory at the same path. A volume cannot be combined
with `autoscale`, `warmPrevious` or more than one replica.

## Cost

Release, one-off and cron job pods, and the extra pods autoscaling adds, use
the project's quota and are billed like the application's own pods.
