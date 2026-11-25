# Event webhooks

An Organization can send its platform events to an HTTPS endpoint of its own:
a deploy that finished or failed, a hostname that went live, a managed service
that is ready or unhealthy, a quota limit, a failing workload. Each event is
one signed POST, delivered at least once.

Events exist where your provider has turned platform events on:
`kubectl api-resources --api-group=platform.kube-dc.com` then lists
`platformevents`. Only Organization administrators create webhooks and read
their signing secret.

In the console: **Organization → Administration → Event webhooks → Create
webhook**. The row menu has **Show signing secret**, **Edit webhook**,
**Pause delivery**, **Rotate secret** and **Delete webhook**. An AI app on the
[hosted MCP server](mcp-for-administrators.md) uses `create_webhook`.

## Create one

```yaml
apiVersion: kube-dc.com/v1
kind: EventWebhook
metadata:
  name: deploys
  namespace: my-org
spec:
  url: https://hooks.example.com/kube-dc
  eventTypes: [DeploymentRolledOut, DeploymentFailed, RouteReady, PreviewExpiring]   # empty: every type
  # Project names. Empty: every project and organization-wide events (quota,
  # suspension, billing). With a list, organization-wide events are not sent.
  projects: [web]
```

```bash
kubectl apply -f webhook.yaml
kubectl -n my-org get eventwebhook deploys          # READY true
kubectl -n my-org get secret event-webhook-deploys -o jsonpath='{.data.signing-secret}' | base64 -d   # whsec_...
```

READY means the subscription exists, not that your endpoint answered. The
platform does not report failed deliveries, so check your receiver's log.
Use an `https://` URL without a user name or password, on a public host name
or public IP address with a certificate that verifies. The API refuses a URL
that is not `https://`; the other checks happen at delivery, so a URL that
fails them is accepted and never receives anything. Answer within 10 seconds:
a redirect gets the first POST but is not followed, and a slow answer can
bring the same POST again. An unknown name in `eventTypes` is accepted and
matches nothing.

A new webhook starts after the newest event that exists when the platform
first processes it enabled. The platform keeps a webhook's position by name:
a webhook created again under the name of a deleted one continues from where
the old one stopped. Widening `eventTypes` or `projects` sends matching events
that are still kept and come after that position; it does not replay events
before it.

## Event types

| Type | Sent when |
|---|---|
| `DeploymentRolledOut` | A Deployment revision is fully available, an Application's included |
| `DeploymentFailed` | A Deployment rollout passed its progress deadline |
| `RouteReady` | A Service published with `service.nlb.kube-dc.com/expose-route` got its hostname |
| `ManagedServiceReady` | A managed service became ready, and no earlier ready event for it is still kept |
| `PreviewExpiring` | A preview that runs as a plain Deployment (made by your own GitHub Actions workflow, or by an app from before Applications) expires within two hours |
| `WorkloadCrashLooping`, `WorkloadNotReady`, `WorkloadDegraded`, `JobFailed`, `VolumeFillingUp` | A pod restarts in a loop or stays not ready, a Deployment or StatefulSet has fewer ready replicas than wanted, a Job failed, a volume is filling up |
| `WorkloadCpuThrottled`, `WorkloadMemoryNearLimit` | A container is throttled on CPU or close to its memory limit (not for managed-service pods) |
| `ManagedServiceUnhealthy`, `ManagedClusterUnhealthy` | A managed service or Managed Cluster is degraded or failed |
| `QuotaNearLimit`, `QuotaReached`, `QuotaNormal` | Organization quota (CPU, memory, storage, pods, public IPv4, object storage) at 80 %, at 100 %, back under 75 % |
| `OrganizationSuspended`, `OrganizationResumed`, `WorkloadsStopped` | The Organization is suspended or resumed, or its workloads are stopped |
| `PaymentPastDue`, `TrialEnding` | Only where your provider bills through Kube-DC's own billing |

The quota, Organization, payment and trial events belong to the whole
Organization and carry no project. A condition (the `Workload*`, `JobFailed`,
`VolumeFillingUp`, `*Unhealthy` and quota types) is sent when it starts, with
`spec.state: firing`, and again when it ends, with `spec.state: resolved`
(`QuotaNormal` for quota). Previews of an Application (`<app>-pr-<n>`) send no
`PreviewExpiring`, and a failed build of an Application sends no event; a
rollout that fails sends `DeploymentFailed`.

## What arrives

`POST <url>` with a JSON body `{id, type, resourceVersion, event}`, where
`event` is the PlatformEvent:

```json
{
  "id": "pe-3f6c0e1b9a7d4c2e8f105b6a7c8d9e0f1a2b3c4d",
  "type": "DeploymentRolledOut",
  "resourceVersion": "1842",
  "event": {
    "apiVersion": "platform.kube-dc.com/v1alpha1",
    "kind": "PlatformEvent",
    "metadata": {"name": "pe-3f6c0e1b9a7d4c2e8f105b6a7c8d9e0f1a2b3c4d", "namespace": "my-org", "resourceVersion": "1842"},
    "spec": {
      "type": "DeploymentRolledOut",
      "severity": "info",
      "organization": "my-org",
      "project": "web",
      "involvedObject": {"apiVersion": "apps/v1", "kind": "Deployment", "name": "hello", "namespace": "my-org-web"},
      "message": "Deployment hello in project web rolled out revision 4: 2 of 2 replicas available.",
      "details": {"revision": "4", "replicas": 2, "url": "https://hello-my-org-web.example.com"},
      "occurredAt": "2026-11-25T10:00:12.000Z"
    }
  }
}
```

`severity` is `info`, `warning` or `critical`. Every member of the
Organization can read the same events with
`kubectl get platformevents -n my-org -o yaml` (add `-w` to watch, or
`--field-selector spec.type=DeploymentFailed`). The request has these
headers, besides `Content-Type: application/json` and
`User-Agent: Kube-DC-Webhooks/1`:

| Header | Value |
|---|---|
| `X-KubeDC-Delivery` | The event id: use it to ignore a repeat |
| `X-KubeDC-Timestamp` | Unix seconds when the delivery was signed |
| `X-KubeDC-Signature` | `sha256=` + hex HMAC-SHA256 of `<timestamp>.<body>` with the signing secret |

Answer with a 2xx within 10 seconds. A 429, a 5xx or no answer is retried
after 1 second, 10 seconds, 1 minute, 10 minutes and then every hour, for as
long as the event is kept, with the same body, timestamp and signature. Most
events are kept seven days; the latest record of a condition (a workload,
service or quota state) can be kept longer. Any other answer, a redirect included, stops that delivery. Do not
reject a delivery because its timestamp is old: a retry carries the time of
the first attempt. Deliveries can arrive out of order; `resourceVersion` is
the event's sequence number.

Verify before trusting the body. `body` is the raw request body as received,
before any JSON parsing:

```python
import hmac, hashlib
expected = "sha256=" + hmac.new(secret.encode(), f"{timestamp}.{body}".encode(), hashlib.sha256).hexdigest()
ok = hmac.compare_digest(expected, signature)
```

## Change, pause, rotate, delete

| Change | How |
|---|---|
| Other URL or types | Edit `spec` |
| Pause | `spec.disabled: true`. Nothing is sent while paused. When you resume, matching events of the pause that are still kept are sent. A webhook created paused starts at the first time it is enabled and sends nothing from before |
| New signing secret | Increase `spec.secretGeneration` (console: **Rotate secret**). New deliveries are signed with the new secret once the rotation is applied. A delivery that is being retried keeps its first signature, so keep accepting the previous secret while your receiver still has failed deliveries; there is no fixed cut-off |
| Remove | Delete the EventWebhook |
