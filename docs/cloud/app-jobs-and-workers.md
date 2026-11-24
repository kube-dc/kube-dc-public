# Release commands, workers and cron jobs

Beyond a web server, most applications also need a database migration on
each release, background workers, scheduled jobs, more pods under load or a
place to keep files. This page covers each of them.

Most are fields of the [Application](applications.md) (`spec.run`), which an
AI app sets with the matching `deploy_app` option. One-off and cron jobs are
hosted MCP tools instead (`run_job`, `schedule_job`); without an AI app,
apply a Kubernetes Job or CronJob manifest.

## Release command (migrations)

A release command runs once with each new release **before** it takes
traffic, for example `npm run migrate` or `rails db:migrate`:

```yaml
spec:
  run:
    releaseCommand: ["npm", "run", "migrate"]
    releaseTimeoutMinutes: 10
```

(`deploy_app` with `releaseCommand` and `releaseTimeoutMinutes`.)

- The command runs in a Job with the release's image, variables and Secrets,
  without its ports, health check or data volume. The Job belongs to the
  application and is named `<app>-release-<hash>`, from the release, the
  command, its timeout, the variables and `spec.release.attempt`.
- When it succeeds, the release rolls out as usual.
- When it fails or passes the timeout (default 15 minutes), the running
  version **keeps serving**: the **Release** step says the release command
  failed, and `kubectl describe app <app>` gives the reason (`ReleaseFailed`
  with the Job's message). **Retry release** (on **Builds & releases**),
  `kube-dc app release retry <app>` (with a [kube-dc CLI that has it](applications.md#from-the-command-line)),
  raising `spec.release.attempt`, or `deploy_app` again (the same image for
  an image app; for an app built from GitHub, `buildFrom` again with `image`
  or `buildFrom.registry` and without `buildNow`) runs it again.
- A newer release stops a command that is still running for an older one.
- It does not run on a rollback: a rollback does not undo a migration, so
  write migrations that the previous version can live with (add a column
  first, remove the old one a release later).
- A command added to an app that is already running runs before its next
  release, not for the running one.
- A new application starts no pods until its first release command
  succeeds.

## Workers

A worker, such as a queue consumer, has no port, address or readiness check:

```yaml
spec:
  run:
    type: Worker
    replicas: 2
```

`deploy_app` without `port` makes one when it creates the app; an existing
app keeps its type. A worker cannot keep a warm previous
version (`keepPreviousWarm`): a second running copy would take the same work
twice.

## One-off jobs

`run_job` runs a command once, to completion, for example a seed or a
backfill. With `app` it runs with that application's image, variables and
secrets; with `image` it runs any image. `list kind=jobs` shows its state and
pod, and `get_logs` reads the pod. The Job is removed a day after it finishes.

## Cron jobs

`schedule_job` runs a command on a schedule:

```json
{ "project": "web", "name": "nightly-report", "app": "shop",
  "command": ["node", "report.js"], "schedule": "0 2 * * *",
  "timeZone": "Europe/Berlin" }
```

- With `app`, the cron job belongs to the application: it is deleted with it,
  and it runs the application's new image after each release. The
  **Processes** tab lists it with **Run now** and **Suspend**.
- `concurrency` decides what happens when a run is still going at the next
  time: `Forbid` (default) skips, `Replace` stops the running one, `Allow`
  runs both. `suspend: true` pauses it.
- A cron job of a preview is deleted with the preview.
- `delete` with kind `CronJob` and apiVersion `batch/v1` removes it.

## Autoscaling

```yaml
spec:
  run:
    resources: { cpu: "1" }
    autoscale: { min: 2, max: 6, cpuPercent: 70 }
```

Kube-DC adds pods when the pods' average CPU use passes `cpuPercent` (default
70) of the **CPU limit** (here 700m) and removes them as it drops, between
`min` and `max`. The CPU limit is 500m unless `resources.cpu` sets it.
`autoscale` cannot be combined with `replicas` or a volume. With
`deploy_app`, `autoscale: { "min": 2, "max": 6, "cpuPercent": 70 }` turns it
on and `autoscale: "off"` removes it. Autoscaling needs the cluster's CPU
metrics. The **Processes** tab shows it, for example "2 to 6 (autoscaled at
70% CPU)".

## A volume

```yaml
spec:
  run:
    volume: { path: /data, size: 5Gi, storageClass: rbd-vm }
  deletionPolicy:
    volume: Retain
```

A persistent volume `<app>-data` (ReadWriteOnce) is mounted at `path`.

- **Storage class**: `storageClass` picks it (`kubectl get storageclass`
  lists them). Without it the cluster's default class is used, which can be
  node-local, so its data is lost with the node. The class applies when the
  volume is created.
- **One pod**: the application runs one pod, and a new release stops the old
  pod before it starts the new one (two pods cannot mount the volume). A
  volume cannot be combined with `autoscale`, `keepPreviousWarm` or more than
  one replica.
- **Size**: it can grow; a smaller size is ignored.
- **Deleting the app**: `kubectl delete app` keeps the volume and its data
  unless `deletionPolicy.volume` is `Delete`. `kube-dc app delete` (with a
  [kube-dc CLI that has it](applications.md#from-the-command-line)) and the
  AI tool `delete` keep it unless you pass `--delete-volume` or `deleteData`,
  whatever the policy says. A kept volume is not used by a new Application
  of the same name: copy the data out and delete the claim before you create
  the app again.
- **Previews** get empty scratch storage at the same path.

## Restarts when a Secret changes

```yaml
spec:
  run:
    envFromSecrets:
      - name: shop-config
    restartOnSecretChange: true
```

When a Secret the app reads gets new values, its pods are replaced and read
the new values. That is a restart, not a new release.

## Cost

Release, one-off and cron job pods, and the extra pods autoscaling adds, use
the project's quota and are billed like the application's own pods.
