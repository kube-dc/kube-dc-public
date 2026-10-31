# Event webhooks

An Organization can send its platform events to an HTTPS endpoint of its own:
a deploy that finished or failed, a hostname that went live, a preview about
to expire, a quota limit, a failing workload. Each event is one signed POST,
delivered at least once.

Only Organization administrators create webhooks and read their signing secret.

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
  projects: [web]                                                                  # empty: every project
```

```bash
kubectl apply -f webhook.yaml
kubectl -n my-org get eventwebhook deploys          # READY true
kubectl -n my-org get secret event-webhook-deploys -o jsonpath='{.data.signing-secret}' | base64 -d
```

The URL must be HTTPS and reach a public address. Events that happen before
the webhook is ready are not sent.

## What arrives

`POST <url>` with a JSON body `{id, type, resourceVersion, event}`, where
`event` is the PlatformEvent (`kubectl get platformevents -n my-org`), and:

| Header | Value |
|---|---|
| `X-KubeDC-Delivery` | The event id: use it to ignore a repeat |
| `X-KubeDC-Timestamp` | Unix seconds when the delivery was signed |
| `X-KubeDC-Signature` | `sha256=` + hex HMAC-SHA256 of `<timestamp>.<body>` with the signing secret |

Answer with a 2xx. A 429, a 5xx or no answer is retried with backoff for up to
seven days with the same body and signature; any other 4xx stops.

Verify before trusting the body:

```python
import hmac, hashlib
expected = "sha256=" + hmac.new(secret.encode(), f"{timestamp}.{body}".encode(), hashlib.sha256).hexdigest()
ok = hmac.compare_digest(expected, signature)
```

## Change, pause, rotate, delete

| Change | How |
|---|---|
| Other URL or types | Edit `spec` |
| Pause | `spec.disabled: true` (events meanwhile are not sent later) |
| New signing secret | Increase `spec.secretGeneration`; the old one stops at once |
| Remove | Delete the EventWebhook |
