import Tabs from '@theme/Tabs';
import TabItem from '@theme/TabItem';

# Authentication

The Partner API uses two credentials: a long-lived **API key** that your operator issues, and a short-lived **access token** that you obtain with the key and send on every call.

## API keys

A key looks like `kdc_live_<partner>_<kid>_<secret>` (or `kdc_test_...`). The `kid` part identifies the key without revealing it, so it is safe to mention in support requests; the whole key is not.

The key is shown to your operator exactly once, when it is created, and handed to you over a secure channel. Kube-DC stores only a hash of it and cannot show it again.

**The key stays on your server.** It can create, resize and delete every one of your customers. Keep it in your server-side secret store and use it only from backend code: your portal's server, a worker, a CLI you run. Never put it in a browser bundle, a mobile or desktop app, a customer-visible configuration file, a repository or a log line.

## Getting an access token

Exchange the key with `POST /auth/token`:

```http
POST /api/public/v1/auth/token
Content-Type: application/json

{"api_key": "kdc_live_..."}
```

The response is the standard OAuth2 client-credentials shape and, unlike every other response, is **not** wrapped in the success envelope:

```json
{"access_token": "eyJ...", "token_type": "Bearer", "expires_in": 900, "scope": "customers:read customers:write customers:sso usage:read"}
```

The endpoint also accepts the OAuth2 form encoding, `grant_type=client_credentials&client_secret=<api key>` as `application/x-www-form-urlencoded`, for generic OAuth2 clients.

Send the token on every other call:

```http
Authorization: Bearer eyJ...
```

`GET /auth/whoami` is the self-test. It needs no particular scope and returns your partner id and display name, the scopes on the token, your limits and defaults, your customer count and the token's expiry.

**Example — Step 1: Create a client.** Select the SDK language; the choice is kept across pages.

<Tabs groupId="partner-sdk-language">
<TabItem value="typescript" label="TypeScript" default>

Tutorial file `examples/tutorial/01-client.ts` of the TypeScript SDK:

```ts
/**
 * Step 1 — Create a client and check your API key.
 *
 *   KUBEDC_API_KEY=kdc_live_... KUBEDC_API_URL=https://backend.kube-dc.example npx tsx 01-client.ts
 *
 * The API key can create, resize and delete every one of your customers. Use it only in
 * server-side code (your portal's backend, a worker, a CLI) — never in a browser bundle.
 */
import { KubeDC, isUnauthorized } from '@kube-dc/partner-sdk';

const apiKey = process.env.KUBEDC_API_KEY; // kdc_live_<partner>_<kid>_<secret>, from your secret store
const baseUrl = process.env.KUBEDC_API_URL; // backend origin; the SDK appends /api/public/v1
if (!apiKey || !baseUrl) {
  throw new Error('Set KUBEDC_API_KEY and KUBEDC_API_URL');
}

// One client per process is enough: it caches and refreshes the access token for you, and
// retries transient failures (mutating calls safely, with an Idempotency-Key).
const kdc = new KubeDC({ apiKey, baseUrl });

try {
  // whoami is the self-test: it exchanges the key for a token and reports who you are.
  const me = await kdc.auth.whoami();
  console.log(`Authenticated as ${me.partner.display_name} (${me.partner.id})`);
  console.log(`Scopes: ${me.scopes.join(', ')}`);
  console.log(`Customers: ${me.customer_count} of ${me.limits.max_customers || 'unlimited'}`);
  console.log(`Usage exports: windows of up to ${me.limits.max_usage_window_days} days`);
} catch (err) {
  if (isUnauthorized(err)) {
    console.error('The API key was refused: check it, or ask the operator for a new one.');
  }
  throw err;
}
```

</TabItem>
<TabItem value="php" label="PHP">

Tutorial file `examples/tutorial/01-client.php` of the PHP SDK:

```php
<?php

/**
 * Step 1 — Create a client and check your API key.
 *
 *   KUBEDC_API_KEY=kdc_live_... KUBEDC_API_URL=https://backend.kube-dc.example php 01-client.php
 *
 * The API key can create, resize and delete every one of your customers. Keep it in
 * server-side code and configuration only (in WHMCS: the server record's Access Hash).
 */

declare(strict_types=1);

require __DIR__ . '/../../vendor/autoload.php';

use KubeDC\Partner\Client;
use KubeDC\Partner\Exception\UnauthorizedException;

$apiKey = getenv('KUBEDC_API_KEY');   // kdc_live_<partner>_<kid>_<secret>
$baseUrl = getenv('KUBEDC_API_URL');  // backend origin; the SDK appends /api/public/v1
if (!is_string($apiKey) || $apiKey === '' || !is_string($baseUrl) || $baseUrl === '') {
    fwrite(STDERR, "Set KUBEDC_API_KEY and KUBEDC_API_URL\n");
    exit(1);
}

// The client caches the access token (pass a PSR-16 'cache' to share it between PHP-FPM workers)
// and retries transient failures; mutating calls safely, with an Idempotency-Key.
$kdc = new Client(['api_key' => $apiKey, 'base_url' => $baseUrl]);

try {
    // whoami is the self-test: it exchanges the key for a token and reports who you are.
    $me = $kdc->auth()->whoami();
    echo "Authenticated as {$me->partnerDisplayName} ({$me->partnerId})\n";
    echo 'Scopes: ' . implode(', ', $me->scopes) . "\n";
    echo "Customers: {$me->customerCount} of " . ($me->maxCustomers > 0 ? $me->maxCustomers : 'unlimited') . "\n";
    echo "Usage exports: windows of up to {$me->maxUsageWindowDays} days\n";
} catch (UnauthorizedException $e) {
    fwrite(STDERR, "The API key was refused (request {$e->requestId}): check it, or ask the operator for a new one.\n");
    exit(1);
}
```

</TabItem>
</Tabs>

## Caching and refreshing tokens

Access tokens live at most 15 minutes (`expires_in` is in seconds). Handle them like this:

1. **Cache the token** and reuse it for every call until shortly before it expires. Refresh about 60 seconds before `expires_in` elapses. Never exchange the key once per API call: the token endpoint has its own rate limit (30 requests per minute per key, burst 10).
2. **Refresh single-flight.** When many requests need a new token at the same moment, let one of them exchange the key and make the others wait for that result. With several server processes, share the cached token (for example in Redis or APCu) or accept one exchange per process.
3. **On a `401` from a normal call**, drop the cached token, exchange the key once and replay the call once. If the replay also answers `401` with `UNAUTHORIZED`, stop and alert: the key was revoked, expired or restricted.

The SDKs do all three for you.

Every credential failure on the token endpoint (an unknown, malformed, revoked, expired or source-address-restricted key) answers the same `401` with `UNAUTHORIZED`, on purpose. Check the key and, if it is correct, ask your operator.

## Scopes

Your operator grants your partner account a set of scopes. Every call names the scope it requires; a token without it gets `403` with `FORBIDDEN`.

| Scope | Allows |
|---|---|
| `customers:read` | Reading customers, operations, quota, plans and the plan catalog, capacity, projects, users, the webhook configuration and the delivery log. |
| `customers:write` | Creating and deleting customers, projects and users; suspend and resume; setting quota and plans; configuring, testing and removing the webhook and rotating its secret. |
| `customers:sso` | Minting one-click console login URLs. This is impersonation of your customers' users, so request it only if you use console login. |
| `usage:read` | Reading usage, per customer and for all customers. |

The scopes a token carries are fixed when it is issued. After your operator changes your scopes, obtain a new token.

## Rotating a key

Your partner account can hold several active keys at once, so you can rotate without downtime:

1. Ask your operator for a new key.
2. Deploy the new key to every server that calls the API, and confirm each one with `GET /auth/whoami`.
3. Ask your operator to revoke the old key.

Revocation stops the key from exchanging immediately. Access tokens already issued with it stay valid until they expire, **at most 15 minutes**. Keys can also carry an expiry date and a source-address allowlist set by your operator.

**If a key leaks**, contact your operator at once. The operator can suspend your partner account, which refuses every token within seconds, then issue a replacement key.

## Partner account states

| Code | Status | Meaning | What to do |
|---|---|---|---|
| `PARTNER_SUSPENDED` | 403 | Your partner account is suspended by the operator. Token exchange is refused, and calls with tokens issued earlier answer `403` with `FORBIDDEN`. Your customers keep running. | Do not retry. Contact your operator. |
| `PARTNER_NOT_READY` | 503 | Your partner credentials are still being provisioned, typically right after the account was created. | Retry after `Retry-After` seconds. |
| `UNAVAILABLE` | 503 | The token service cannot issue a token right now. | Retry after `Retry-After` seconds, with backoff. |

## Rate limits

Authenticated calls are limited per partner with a token bucket: by default 120 requests per minute with a burst of 40, set per partner by your operator. Every response carries `X-RateLimit-Limit` and `X-RateLimit-Remaining`. Over the limit you get `429` with `RATE_LIMITED` and a `Retry-After` header in seconds; wait that long before retrying. See [Idempotency and retries](./idempotency.md).
