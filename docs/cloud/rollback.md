# Roll back an application

A rollback returns a Deployment to the revision before the current one. By
default Kubernetes starts new pods from that revision, which takes from
seconds to minutes (image pull, readiness, free capacity). With **instant
rollback** the previous version keeps running next to the current one, and a
rollback moves traffic to it in about a second.

## How instant rollback works

When a Deployment `<app>` has instant rollback turned on and a rollout
completes, the platform starts a copy of the previous revision as the
Deployment `<app>-previous`:

- one replica, with that revision's image, settings and Secret references
  (the Secrets themselves are read as they are now: a removed or rotated
  Secret can break the old version);
- its pods have their own labels (for example
  `app.kubernetes.io/name: <app>-previous`), so the Service `<app>` sends no
  traffic to it until a rollback;
- it has the environment variable `KUBE_DC_WARM=1`;
- it belongs to `<app>`: deleting the app deletes it.

On a rollback the platform:

1. stops the gateway reusing connections to the application while the
   rollback runs (each request opens its own connection);
2. switches the Service `<app>` to the warm copy (traffic moves in a few
   seconds; requests in flight finish);
3. rolls `<app>` back to the previous revision behind it, and, once the
   Service sends traffic to the warm copy only, stops the pods of the version
   you rolled back from, so no open connection keeps reaching it (their logs
   end there);
4. switches the Service back to `<app>` when it is ready, and lets the gateway
   reuse connections again.

If the warm copy is not ready, the rollback starts new pods from the previous
revision as without instant rollback. The result says which happened
(`instant: true` or `false`).

After the window you choose (at most 24 hours) the warm copy is scaled to 0
and kept, so its node usually still has the image. The next completed rollout
replaces it with the new previous version.

## Turn it on

Instant rollback is off by default and set per application.

- **Console**: in **Kubernetes resources > Deployments**, open the
  application's actions and choose **Instant rollback**, then a window.
- **AI app (hosted MCP)**: ask to deploy with `warmPrevious`, for example
  "deploy notes with warmPrevious 1h". `warmPrevious: off` turns it off.
- **kubectl**: label the Deployment with the window:

  ```bash
  kubectl label deployment hello apps.kube-dc.com/warm-previous=1h
  kubectl label deployment hello apps.kube-dc.com/warm-previous-   # off
  ```

The application needs a Service with the same name as the Deployment that
selects its pods by labels (as `deploy_app` and the console create it). The
platform refuses, and says why in the Deployment's
`apps.kube-dc.com/warm-error` annotation, when another Service or Deployment
in the project would select the warm copy's pods, or when the Service selects
on a label the pods do not have.

Pull request previews (`<app>-pr-<n>`) do not get a warm copy unless you turn
it on for them.

## Roll back

- **Console**: **Roll back** in the Deployment's actions. The dialog says
  whether the previous version is running before you confirm.
- **AI app**: ask to roll back the application (`rollback`).
- **kubectl**: `kubectl rollout undo deployment/hello` always starts new pods.
  To request an instant rollback, annotate the Deployment; the result appears
  in `apps.kube-dc.com/rollback-status`:

  ```bash
  kubectl annotate deployment hello apps.kube-dc.com/rollback="$(date +%s)"
  kubectl get deployment hello -o jsonpath='{.metadata.annotations.apps\.kube-dc\.com/rollback-status}'
  ```

The status also names the stopped ReplicaSet (`drained`, `drainedAt`): once
traffic is on the previous version, the platform stops the version you rolled
back from, so the connections it still holds close. That takes as long as
your application's own graceful shutdown after `SIGTERM`; an application that
closes idle keep-alive connections promptly gets the cleanest switch.

`rollout_status` (AI app) shows the warm copy (`warmPrevious`) and where
traffic is while a rollback is switched.

### Builds on push

A build that is deployed during a rollback wins: a rollback that was not yet
answered is cancelled, and a switched rollback returns traffic to the
application as soon as the new build is ready. When the platform's GitHub App
reports deployments, an instant rollback marks the rolled-back commit as the
active deployment at once.

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
switch it grows to the application's replica count until the switch back.
Database connections from both versions count against the database's
connection limit.

The warm copy never blocks a deploy: if the application's rollout cannot
create pods (for example because of the project quota), or the warm copy is
not ready 5 minutes after it started, it is scaled to 0 until the next
rollout.

## Background work in the old version

The warm copy runs the old code, including anything it starts on its own:
cron loops, queue consumers, migrations at start. Skip that work while
`KUBE_DC_WARM` is `1`, or run it in a separate Deployment.

On an instant rollback, connections to the version you rolled back from close
when its pods stop: clients holding a WebSocket or another long connection
reconnect, to the previous version.
