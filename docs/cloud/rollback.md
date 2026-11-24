# Roll back an application

Every version of an application that ran is a numbered release. A new image,
a new build, or a change to what its pods run (variables, resources, port,
health path, pull Secrets, adding or removing the volume) makes the next one.
A volume's size and the release command are not part of a release: a changed
command runs before the next release.

A rollback pins the app to an earlier release until you resume. By default
the platform starts new pods of that release, which takes from seconds to
minutes (image pull, readiness, free capacity). With **instant rollback** the
previous release keeps running next to the current one, and a rollback moves
traffic to it in about a second.

## Roll back

- **Console**: on **Builds & releases**, choose **Roll back to this release**
  on a release in the **Releases** table. The page then shows a banner with
  **Resume deploys**.
- **AI app (hosted MCP)**: ask to roll back the application (`rollback`, with
  `release` for a specific one); `rollback` with `resume: true` ends it.
- **Command line** (a [kube-dc CLI with `kube-dc app`](applications.md#from-the-command-line)):
  `kube-dc app rollback <app>` (the release before the running one, or
  `--to <release>`), then `kube-dc app resume <app>`.
- **kubectl**: set the pin, and remove it to resume:

  ```bash
  kubectl patch app hello --type merge -p '{"spec":{"release":{"pin":{"release":5}}}}'
  kubectl patch app hello --type json -p '[{"op":"remove","path":"/spec/release/pin"}]'
  ```

While a release is pinned, a banner with **Resume deploys** shows. Once the
pinned release is live, the app shows as **Rolled back** (its `Ready`
condition says `Pinned`, and the release is marked **Pinned**).

- **While pinned**, pushes are still built and recorded, but new builds and
  changes to what the pods run wait. Replicas, the autoscaler, hostnames and
  `public` still apply.
- **Resume deploys** removes the pin. The newest release then rolls out once
  the build of the app's current settings has succeeded; after a failed build
  the pinned release keeps running.
- **Which releases you can pin**: the newest ten are kept, and a pinned one.
  A pin of a release that is no longer kept is refused with `PinInvalid`. A
  pin of a release that mounts the data volume, while the app now has
  autoscaling or more than one replica, is refused with `PinIncompatible`.
  Choose another release, or resume.

A rollback does not run the release command, and it restores the release's
image and settings, not your data (see
[Database migrations](#database-migrations-expand-then-contract)).

## Instant rollback

Set how long the previous release stays running after each rollout, from 1
minute to 24 hours:

```yaml
spec:
  run:
    keepPreviousWarm: 1h
```

or ask an AI app to deploy with `warmPrevious: 1h` (`off` turns it off). The
**Processes** tab shows it as **Instant rollback**.

After each completed rollout, the platform runs the release before the
current one as the Deployment `<app>-previous`:

- one replica, with that release's image, settings and Secret references (the
  Secrets themselves are read as they are now: a removed or rotated Secret can
  break the old version);
- its pods have their own name label (`app.kubernetes.io/name:
  <app>-previous`), so the app's Service sends it no traffic until a
  rollback;
- it has the environment variable `KUBE_DC_WARM=1` (init containers too);
- it belongs to the app: deleting the app deletes it.

A rollback to the release it runs is a switch, not a rollout. The platform:

1. moves the app's traffic to the warm copy;
2. rolls the app out to that release behind it and, once only the warm
   copy's pods get traffic, stops the pods of the release you rolled back
   from, so no open connection keeps reaching it;
3. moves traffic back to the app when its pods are ready, or at once if the
   warm copy stops serving while the app has a ready pod.

Some limits apply:

- If the app cannot start its pods after a switch (project quota, a full
  cluster), the warm copy carries the traffic until it can. It stops growing
  when the quota is tight, so the app gets those pods back.
- A copy that is not ready 5 minutes after it started, or that keeps a
  rollout from creating its pods, is scaled to 0 until the next release; the
  `WarmPrevious` condition says so.
- After the window the warm copy is scaled to 0.
- After a rollback, the warm copy becomes the release you rolled back from.

The `WarmPrevious` condition says whether the copy runs and, if not, why. It
does not affect the app's **Ready** state. The platform keeps no warm copy,
and says why there, when a second running version would cause harm:

- a NetworkPolicy in the Project selects the app's pods, or lets traffic in
  or out, by the name label the copy renames;
- another Service or Deployment of the Project would select the copy's pods;
- a Deployment named `<app>-previous` exists that the app did not make;
- the previous release mounts a volume claim.

A worker, or an app with a data volume, cannot keep a warm copy: the
Application refuses `keepPreviousWarm` for them.

`rollout_status` (AI app) shows the warm copy and where traffic is while a
rollback is switched.

## Database migrations: expand, then contract

A rollback runs old code against the current database. It does not undo
migrations, and restoring data is a database restore, not a rollback. If a
release dropped or renamed a column the old version reads, the old version
fails, warm or not.

Change the schema in two releases:

1. **Expand**: add the new column or table; the new code writes both shapes
   and reads either. The previous release still works.
2. **Contract**: once no running or warm version needs the old shape (one
   release later, after the warm window), remove it.

## Cost and capacity

The warm copy is ordinary pods, billed and counted against the project quota
like the application's own: one replica during the window, then none. After a
switch it grows toward the application's replica count until the switch back.
Database connections from both versions count against the database's
connection limit.

## Background work in the old version

The warm copy runs the old code, including anything it starts on its own:
cron loops, queue consumers, migrations at start. Skip that work while
`KUBE_DC_WARM` is `1`, or run it in a [worker](app-jobs-and-workers.md#workers).

On an instant rollback, connections to the release you rolled back from close
when its pods stop: clients holding a WebSocket or another long connection
reconnect, to the previous version.
