import Tabs from '@theme/Tabs';
import TabItem from '@theme/TabItem';

# Webhooks

Webhooks tell your backend when a customer or project changes, so you do not have to poll for every change. They are an optimisation over polling, never a replacement: every event stays observable through the API.

## Configure a receiver

`PUT /webhook` (scope `customers:write`):

```json
{"url": "https://portal.example.com/hooks/kube-dc", "events": ["customer.*", "project.ready"], "enabled": true}
```

- `url` must be HTTPS on a public host, without credentials in the URL. It is checked again on every delivery; private addresses and redirects are refused. (Your operator can allow a private destination for your partner account if your receiver really is private.)
- `events` takes event names such as `customer.ready`, families such as `customer.*`, or `*`. It defaults to `*`; an empty list also subscribes to everything. Unknown events are refused with `400` and `VALIDATION_ERROR`, and `details.known_events` lists the valid names. `ping` cannot be named explicitly; `*` covers it.
- `enabled` defaults to `true`; only `false` disables delivery.

**The signing secret is shown once.** The first `PUT /webhook` that creates a secret returns it in `signing_secret` (64 hexadecimal characters). Later calls answer `signing_secret_configured: true` instead. Store the secret in your secret store immediately.

**Lost the response?** Repeat the same `PUT /webhook` with the same `Idempotency-Key` within 24 hours: the replayed response carries the secret. Otherwise rotate it. See [Idempotency and retries](./idempotency.md).

`GET /webhook` returns the configuration (never the secret), and `DELETE /webhook` stops delivery.

### Rotating the secret

`POST /webhook/secret` generates a new secret and returns it once. **The previous secret stops working immediately**, and retries of deliveries sent before the rotation keep their original signature. To rotate without rejecting valid deliveries:

1. Call `POST /webhook/secret` and store the new secret.
2. For a few minutes, accept a delivery that verifies with either the new or the previous secret.
3. Remove the previous secret.

## Verify every delivery

Each event is a POST with a JSON body and these headers:

| Header | Content |
|---|---|
| `X-KubeDC-Signature` | `sha256=<hex HMAC-SHA256>` of `<timestamp>.<raw body>`, keyed with the signing secret. |
| `X-KubeDC-Timestamp` | Unix seconds when the delivery was signed. |
| `X-KubeDC-Event` | The event name. Not signed. |
| `X-KubeDC-Delivery` | The delivery id. Not signed. |

To verify:

1. Read the **raw request body bytes**, before any JSON parsing. A body parsed and serialised again will not match.
2. Compute HMAC-SHA256 over the timestamp header, a `.`, and the raw body, with your signing secret, as lowercase hex.
3. Compare it with the value after `sha256=` using a **constant-time** comparison.
4. Reject the delivery if the timestamp is more than **5 minutes** away from your clock. Keep your servers' clocks synchronised.
5. Only then parse the body, and take the event name and the delivery id from the verified body (`event`, `id`), never from the unsigned headers.

The SDKs include a verification helper that does all of this; see the example below.

## Answer quickly

- Answer any `2xx` to acknowledge. Put slow work in a queue and answer first: deliveries time out after 5 seconds.
- A `3xx` counts as a failure; redirects are never followed.
- A `429` or `5xx`, a timeout or a network error is retried after 1, 2, 8 and 60 seconds, for 5 attempts in total, with the same delivery id, timestamp and signature.
- Any other `4xx` stops the retries. Answer `400` only when the signature is invalid, and `2xx` for events you do not handle.

## Delivery guarantees

- **At least once.** The same delivery can arrive more than once. Deduplicate on the verified body's `id` (`dlv_...`): record processed ids for longer than the retry schedule, and skip ids you have seen.
- **Order is not guaranteed.** Compare timestamps or re-read the resource rather than trusting arrival order.
- **Deliveries can be dropped.** A delivery that exhausts its retries is not queued anywhere. Polling is authoritative: keep a periodic job that reconciles your records with the API, for example operations still running and customers whose status you expect to change.
- **New events may appear within v1.** Acknowledge and ignore events you do not know.

**Example — Step 6: Webhooks.** Select the SDK language; the choice is kept across pages.

<Tabs groupId="partner-sdk-language">
<TabItem value="typescript" label="TypeScript" default>

Tutorial file `examples/tutorial/06-webhooks.ts` of the TypeScript SDK:

```ts
/**
 * Step 6 — Receive webhooks instead of polling for every change.
 *
 * Every delivery is signed with your webhook secret. Verify it over the EXACT raw body:
 * use express.raw() on this route, never a JSON-parsed and re-serialized body.
 */
import express from 'express';
import { KubeDC, verifyWebhook, WebhookVerificationError } from '@kube-dc/partner-sdk';
import type { KubeDCWebhookEvent } from '@kube-dc/partner-sdk';

/** Run once (e.g. from a setup script). The signing secret is returned only by the call that creates it. */
export async function registerWebhook(kdc: KubeDC): Promise<void> {
  // Safe to retry: a retry with the same Idempotency-Key (the SDK's, or a fixed one of yours such
  // as 'setup-webhook-v1') replays the first response, secret included.
  const hook = await kdc.webhook.set({ url: 'https://portal.example.com/hooks/kube-dc', events: ['customer.*'] }, { idempotencyKey: 'setup-webhook-v1' });
  if (hook.signing_secret) console.log('Store this as KUBEDC_WEBHOOK_SECRET:', hook.signing_secret);
  await kdc.webhook.test(); // sends a signed `ping`
  for await (const delivery of kdc.webhook.deliveries({ limit: 10 })) {
    console.log(`${delivery.at} ${delivery.event}: ${delivery.delivered ? 'delivered' : delivery.error}`);
  }
}

const secret = process.env.KUBEDC_WEBHOOK_SECRET;
if (!secret) throw new Error('Set KUBEDC_WEBHOOK_SECRET');
const app = express();

app.post('/hooks/kube-dc', express.raw({ type: 'application/json' }), (req, res) => {
  let event: KubeDCWebhookEvent;
  try {
    event = verifyWebhook({ payload: req.body as Buffer, headers: req.headers, secret });
  } catch (err) {
    if (err instanceof WebhookVerificationError) {
      res.status(400).send('invalid signature');
      return;
    }
    throw err;
  }
  // Delivery is at-least-once: skip event.id values you have already processed.
  switch (event.event) {
    case 'customer.ready': console.log(`${event.data.customer_id} is ready`); break;
    case 'customer.failed': console.log(`${event.data.customer_id} failed: ${event.data.error?.code}`); break;
    case 'customer.suspended': console.log(`${event.data.customer_id} suspended`); break;
    default: break; // new event types may appear within v1: acknowledge and ignore them
  }
  res.sendStatus(204); // answer 2xx quickly; do slow work in a queue
});

app.listen(3001);
```

</TabItem>
<TabItem value="php" label="PHP">

Tutorial file `examples/tutorial/06-webhooks.php` of the PHP SDK:

```php
<?php

/**
 * Step 6 — Receive webhooks instead of polling for every change.
 *
 * A plain PHP front controller (php -S 0.0.0.0:8080 06-webhooks.php). Register the URL once and
 * store the signing secret it returns:
 *   $hook = $kdc->webhook()->set($url, ['customer.*'], idempotencyKey: 'setup-webhook-v1');
 * Safe to retry: a repeat with the same key replays the first response, secret included.
 *   Laravel:  Webhook::verify($request->getContent(), $request->headers->all(), $secret)
 *             (and exclude the route from CSRF protection)
 *   Symfony:  the same getContent() / headers->all() pair
 */

declare(strict_types=1);

require __DIR__ . '/../../vendor/autoload.php';

use KubeDC\Partner\Exception\WebhookVerificationException;
use KubeDC\Partner\Model\WebhookEvent;
use KubeDC\Partner\Webhook;

$payload = (string) file_get_contents('php://input');   // the EXACT raw body, never re-encoded JSON

try {
    $event = Webhook::verify($payload, $_SERVER, (string) getenv('KUBEDC_WEBHOOK_SECRET'));
} catch (WebhookVerificationException $e) {
    http_response_code(400);
    exit;
}

// Delivery is at-least-once: skip $event->id values you have already processed.
switch ($event->event) {
    case WebhookEvent::CUSTOMER_READY:
        error_log("customer {$event->customerId()} is ready");
        break;
    case WebhookEvent::CUSTOMER_FAILED:
        error_log("customer {$event->customerId()} failed to provision");
        break;
    case WebhookEvent::CUSTOMER_SUSPENDED:
        error_log("customer {$event->customerId()} suspended");
        break;
    default:
        break;   // new event types may appear within v1: acknowledge and ignore them
}

http_response_code(204);   // answer 2xx quickly; do slow work in a queue
```

</TabItem>
</Tabs>

## Events

Every event body is `{"id", "event", "created_at", "partner_id", "data"}`, with `data` depending on the event:

| Event | Sent when | `data` |
|---|---|---|
| `ping` | `POST /webhook/test` is called. | `message` |
| `customer.ready` | A customer create operation succeeded. | `customer_id`, `operation_id`, `project`, `state`, `error` |
| `customer.failed` | A customer create operation failed. | `customer_id`, `operation_id`, `project`, `state`, `error` (with `code` and `message`) |
| `customer.suspended` | `POST /customers/{id}/suspend` succeeded. | `customer_id`, `state`, `suspended_at`, `workloads_running` |
| `customer.resumed` | `POST /customers/{id}/resume` succeeded. | `customer_id`, `state`, `workloads_running`, `cancelled_scheduled_deletion` |
| `customer.deletion_scheduled` | A scheduled, recoverable deletion was requested. | `customer_id`, `state`, `scheduled_deletion_at`, `grace_days`, `recoverable` |
| `customer.deleted` | Teardown removed the customer's organization: minutes after an immediate delete, or when a grace period ran out. | `customer_id`, `immediate` |
| `customer.quota_changed` | `PUT /customers/{id}/quota` succeeded. | `customer_id`, `quota` |
| `customer.plan_changed` | `PUT /customers/{id}/plan` succeeded. | `customer_id`, `plan_id` |
| `project.ready` | A project create operation succeeded. | `customer_id`, `operation_id`, `project`, `state`, `error` |
| `project.failed` | A project create operation failed. | `customer_id`, `operation_id`, `project`, `state`, `error` |
| `project.deleted` | `DELETE /customers/{id}/projects/{project}` was accepted (not when teardown finishes). | `customer_id`, `project` |

## Test and inspect

- `POST /webhook/test` sends one `ping` synchronously: a single attempt with a 5-second timeout and no retries, whatever your `events` filter and even while delivery is disabled. It reports `delivered`, `delivery_id`, `attempts`, the HTTP `status` your endpoint answered and any `error`. It answers `400` with `NOT_CONFIGURED` when no webhook URL or secret is configured.
- `GET /webhook/deliveries` lists the 50 most recent delivery attempts, newest first, with `id`, `event`, `delivered`, `attempts`, `status`, `error` and `at`. Bodies are never kept. It is a best-effort log for debugging, not a replay queue.


### Deletion completion

`customer.deleted` and `project.deleted` mean the original resource has disappeared after finalization. Accepting a DELETE only starts that process. These completion events survive a backend restart and retry with the same delivery ID; deduplicate the ID before applying an event. A replacement resource reusing the name is a separate resource.
