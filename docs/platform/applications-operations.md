# Operate Applications

This runbook is for platform operators of the Application API
(`apps.kube-dc.com`): the alerts, what tenants see when one fires, how to
check and how to recover. How builds work and how to size them is in
[GitHub App](github-app.md#builds-on-push).

## The parts

| Part | Where | What it does |
|---|---|---|
| App controller | Deployment `kube-dc-manager-apps-controller` in namespace `kube-dc-apps` (release `kube-dc`, `apps.namespace`) | Resolves sources, runs builds, releases and previews. Lease `apps.kube-dc.com` |
| Manager | Deployment `kube-dc-manager` in `kube-dc` | Receives GitHub webhooks, mirrors the GitHub App into Secret `kube-dc-apps-github` of `kube-dc-apps`, fills registry push Secrets |
| Build pods | The tenant's project, label `apps.kube-dc.com/build` | Rootless BuildKit; removed a day after the build ends |

Running apps do not depend on the app controller: when it stops, apps keep
serving, and builds, releases and previews wait until it is back.

Check the state:

```bash
kubectl -n kube-dc-apps get deploy,pods
kubectl -n kube-dc-apps get lease apps.kube-dc.com -o jsonpath='{.spec.holderIdentity}{"\n"}'
kubectl get applications.apps.kube-dc.com -A
kubectl get applicationbuilds.apps.kube-dc.com -A --sort-by=.metadata.creationTimestamp | tail -20
kubectl get pods -A -l apps.kube-dc.com/build -o wide
```

## Pause builds

Platform settings → Integrations → GitHub → **Builds on push** off (or the
chart's `githubApp.builds.enabled: false`). New builds then wait with the
reason `BuildsPaused` and running ones finish. Running apps are not
affected. One tenant's Application is paused with `spec.suspend: true`.

## Alerts

The chart's PrometheusRule `kube-dc-manager-apps-controller-metrics` (group
`kube-dc.applications`) is rendered when `apps.enabled`,
`manager.metrics.prometheusRule.enabled` and the app controller's
ServiceMonitor are on. Every expression reads the app controller's own job.

### KubeDcAppBuildQueueSlow

**Meaning.** Over 30 minutes, the 90th percentile of the time builds waited
for a build slot is above 10 minutes.

**Tenants see** builds that stay `Queued` with "Waiting for a build slot,
position N".

**Check.**

```bash
# Who holds the slots, and who waits:
kubectl get applicationbuilds -A -o custom-columns='NS:.metadata.namespace,NAME:.metadata.name,PHASE:.status.phase,REASON:.status.reason,SLOT:.status.slot.reservedAt'
kubectl -n kube-dc-apps get deploy kube-dc-manager-apps-controller -o jsonpath='{.spec.template.spec.containers[0].args}' | tr ',' '\n' | grep slots
```

`kube_dc_app_builds_waiting` counts builds queued for a slot; paused builds
are not counted. The metric `kube_dc_app_build_slots{kind="used"}` against
`kube_dc_app_build_slots{kind="limit"}` shows which limit is reached:
project, organization or cluster.

**Recover.** If one organization holds its whole limit, the others still get
the remaining slots; nothing is wrong. If the cluster limit is reached
for long, add build capacity and raise `apps.builds.slots.cluster` (see
[How many builds at once](github-app.md#builds-on-push)). Do not raise it past
what the nodes' free disk holds.

### KubeDcAppBuildsWaitingLong

**Meaning.** Builds wait for a slot and no slot was granted for 30 minutes.

**Tenants see** builds that never leave `Queued`.

**Check.** Whether the builds that hold slots are still running:

```bash
kubectl get applicationbuilds -A -o json | jq -r '.items[] | select(.status.slot.reservedAt != null and .status.slot.releasedAt == null) | "\(.metadata.namespace)/\(.metadata.name) \(.status.phase) \(.status.reason)"'
kubectl -n kube-dc-apps logs deploy/kube-dc-manager-apps-controller --since=30m | grep -i -E 'slot|error' | tail -50
```

**Recover.** A build holds its slot until its pod is gone. A pod stuck in
`Terminating` on an unreachable node keeps the slot; the controller
force-deletes it 30 seconds after its grace period. If the controller is not
the leader or is crash-looping, see
[KubeDcAppControllerDown](#kubedcappcontrollerdown). Restarting the app
controller reloads the slot ledger from the builds' status.

### KubeDcAppBuildsPlatformFailures

**Meaning.** At least three builds ended `Evicted` or `NoCapacity` in an
hour. Both are the platform's side:
- `Evicted`: the kubelet evicted the build pod, usually for node disk
  pressure;
- `NoCapacity`: no node took the pod for three hours.

**Tenants see** failed builds with "No build node is available. This is on
the platform's side; it retries by itself", or an evicted build.

**Check.**

```bash
kubectl get applicationbuilds -A -o json | jq -r '.items[] | select(.status.failure.reason == "Evicted" or .status.failure.reason == "NoCapacity") | "\(.metadata.namespace)/\(.metadata.name) \(.status.failure.reason) \(.status.failure.message)"'
kubectl get nodes -o custom-columns='NAME:.metadata.name,DISK:.status.conditions[?(@.type=="DiskPressure")].status'
kubectl get --raw /api/v1/nodes/<node>/proxy/stats/summary | jq '.node.fs | {availableBytes, capacityBytes}'
```

**Recover.** Free disk on the build nodes, or lower `apps.builds.slots` to
what the nodes hold. Where no node holds two builds, set
`apps.builds.placement.onePerNode: true`. `PushFailed` is not part of
this alert: a tenant's full registry or a refused push Secret also ends that
way. Read the build's `status.failure.message` to tell a registry outage
(many projects at once) from one tenant's registry.

### KubeDcAppGitHubAppRejected

**Meaning.** GitHub answered 401 to a request signed with the GitHub App's
own key (a JWT). Every Application's resolve, every build's token and every
check run then fails.

**Tenants see** "GitHub cannot be reached; builds resume by themselves" on
Applications from GitHub. Running apps keep serving.

**Check, in this order:**

1. **The clock.** A JWT is refused when the node's clock is off by more
   than a minute: `kubectl -n kube-dc-apps exec deploy/kube-dc-manager-apps-controller -- date -u`
   against a trusted clock.
2. **The App ID.** It must match the App whose key is configured: Platform
   settings → Integrations → GitHub, and the App's page on GitHub.
3. **The key.** Whether the App's key was revoked or regenerated on GitHub.
4. **The mirror.** Whether the manager mirrored the same App:
   `kubectl -n kube-dc-apps get secret kube-dc-apps-github -o jsonpath='{.data.app-id}' | base64 -d`.

**Recover.** Fix the clock or the App ID. If the key was revoked, generate a
new one on GitHub and save it in Integrations (see
[Rotate or revoke the key](github-app.md#rotate-or-revoke-the-key)). The
manager mirrors it within a minute, and the controller reads it within 10
seconds.

### KubeDcAppWebhooksMissed

**Meaning.** In 6 hours, periodic reads found new commits at least three
times (counted per Application: three apps of one branch count three). A
delivered push is normally read within seconds, so these pushes were not
delivered, or were delivered late (check the manager's delivery log for slow
handling as well as for failures). Pushes reach the cluster only
through the periodic read, so builds on push start late. The interval is
`ResolveEvery`, about 10 minutes.

**Tenants see** builds that start minutes after a push instead of seconds.

**Check.**

1. On GitHub, the App's settings → Advanced → Recent Deliveries: are
   deliveries failing, and with what status?
2. The manager's view: `kube_dc_app_webhook_deliveries_total` by `result`
   (`bad_signature` means the webhook secret differs) and the manager's log,
   which names each delivery's ID:
   `kubectl -n kube-dc logs deploy/kube-dc-manager | grep -E 'push delivered|push not passed'`.
   The app controller logs `source resolved` with the same `delivery` for the
   read that delivery caused.
3. The route: the webhook URL in the App's settings must reach the manager
   (see [The webhook](github-app.md#the-webhook)).

**Recover.**
- **Webhook secret:** set the same secret in GitHub and in Integrations.
- **Route:** fix the URL, the DNS or the route.
- **Lost deliveries:** GitHub does not retry deliveries; the periodic read
  catches up by itself.

### KubeDcAppControllerDown

**Meaning.** The app controller has not been scraped for 10 minutes, or no
replica holds the `apps.kube-dc.com` lease.

**Tenants see** running apps unchanged. New pushes, spec changes, builds,
releases and previews wait.

**Check.**

```bash
kubectl -n kube-dc-apps get deploy,pods
kubectl -n kube-dc-apps describe pod -l app.kubernetes.io/name=kube-dc-manager-apps-controller | tail -30
kubectl -n kube-dc-apps logs deploy/kube-dc-manager-apps-controller --previous | tail -50
```

The alert rules come with the chart and expect the manager image of the
same release: an older image does not export some of the series, and the
rules that read them then stay silent. After an upgrade, check that the
series exist (in Prometheus): `kube_dc_app_build_slots`,
`kube_dc_app_builds_waiting`, `kube_dc_app_resolves_total` and
`kube_dc_app_github_auth_failures_total`, each with
`namespace="kube-dc-apps"`.

**Recover.** An `OOMKilled` controller needs a higher
`apps.controller.resources.limits.memory`. A controller that does not
start says why in its first log lines: a refused `APPS_BUILD_PLACEMENT`
is a chart value the binary does not know (keep chart and manager on the
same release). Everything converges once it runs again: builds and
releases are level-triggered.

## Build logs

- **While the build pod exists** (a day after the build ends): the console's
  Builds tab shows its log, and so does
  `kubectl -n <project> logs -l apps.kube-dc.com/build=<build> -c buildkit`.
- **A failed build** also keeps the last 2 KB of its log in
  `status.failure.logTail`, for as long as the build record exists.
- **After that**, the lines are in the installation's log store (Loki),
  under the project's tenant, for the log retention
  (`LOKI_CUSTOMER_RETENTION`). The console's **Log** reads the last 2,000
  of them through the backend route
  `GET /api/ui/v1/application-builds/<project>/<build>/log`, for a person who
  may read pod logs in that project. It says a log is no longer kept after
  `BUILD_LOG_RETENTION_DAYS` (backend environment, default 7): keep it equal
  to the Loki retention. The query selects `namespace="<project>"`,
  `container="buildkit"` and the exact pod names of the build's attempts
  (`build-<build>`, `build-<build>-2`, `build-<build>-3`; a name over 63
  characters is shortened with a hash, as the controller names the pod),
  within the build's own time range.

## Registry storage

A managed registry keeps every image a build pushes; nothing deletes images
on its own. On the development plan the registry's volume fills, and pushes
then fail with `PushFailed`; the alerts `ManagedRegistryStorageHigh` (85%)
and `ManagedRegistryStorageFull` (95%) warn first. Watch the registry's
storage tile on its Overview page. To make room, raise its size (an
`ExpandStorage` operation).

Nothing deletes an app's images today, and nobody should by hand: a release
an app may roll back to, a preview, an image index or a build the controller
may still choose can need any of them, and those checks cannot be done
safely from outside the controller. Free space by raising the registry's
storage (`ExpandStorage`). Automatic retention is planned in the registry
itself.
