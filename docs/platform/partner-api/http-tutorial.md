# API tutorial (HTTP)

This tutorial walks through a complete integration with nothing but HTTP: `curl` to send requests and `jq` to read the JSON. It creates a customer, follows its provisioning, sizes it, logs its owner into the console, drives its lifecycle, reads lists and usage, sets up webhooks and finally deletes the customer again.

Every step is a request your own backend sends, so the commands translate one to one into any HTTP client. Each step links to the chapter with the full rules.

## 1. Prerequisites and security rules

You need:

- the backend origin and an API key from your Kube-DC operator (see [Partner API overview](./overview.md));
- `bash`, `curl` and `jq` 1.6 or later;
- a customer email address you control, for the owner of the test customer.

Security rules that hold for every step:

- **The API key stays on your server.** It can create, resize and delete every one of your customers. Keep it in your server-side secret store and use it only from backend code. Never put it in a browser, a mobile app, a repository or a log line.
- **Never type the key inline.** Load it from your secret store into an environment variable, so it does not end up in your shell history.
- **Treat access tokens, console login URLs and the webhook signing secret as credentials too.** None of the commands below print them.

Set up the shell:

```bash
export KUBEDC_API="https://<your-kube-dc-backend>/api/public/v1"
# Load the key from your secret store; kdc_live_… keys are never typed or echoed.
export KUBEDC_API_KEY="$(cat /run/secrets/kubedc_api_key)"

# Your own record for the customer this tutorial creates.
EXTERNAL_ID="crm-1042"
COMPANY_NAME="Example Ltd"
OWNER_EMAIL="jane@example.com"
```

Paths in this tutorial are relative to `$KUBEDC_API`. `GET /health` needs no credentials and is a quick connectivity check:

```bash
curl -sS "$KUBEDC_API/health" | jq .
```

```json
{"status": "success", "data": {"status": "ok", "api_version": "v1"}}
```

## 2. Get an access token

Exchange the key for a short-lived access token with `POST /auth/token`. The key goes into the request body through a pipe, so it never appears in a process list:

```bash
get_token() {
  TOKEN_RESPONSE=$(jq -n '{api_key: env.KUBEDC_API_KEY}' |
    curl -sS -X POST "$KUBEDC_API/auth/token" \
      -H 'Content-Type: application/json' \
      --data-binary @-)
  TOKEN=$(jq -r '.access_token // empty' <<<"$TOKEN_RESPONSE")
  TOKEN_EXPIRES_AT=$(( $(date +%s) + $(jq -r '.expires_in // 0' <<<"$TOKEN_RESPONSE") ))
}

# Reuse the cached token; exchange the key again only about 60 seconds before it expires.
fresh_token() {
  if [ -z "${TOKEN:-}" ] || [ "$(date +%s)" -ge $(( TOKEN_EXPIRES_AT - 60 )) ]; then
    get_token
  fi
}

get_token
jq 'del(.access_token)' <<<"$TOKEN_RESPONSE"
```

This response is the bare OAuth2 shape, the only response that is not wrapped in the success envelope:

```json
{"token_type": "Bearer", "expires_in": 900, "scope": "customers:read customers:write customers:sso usage:read"}
```

A failed exchange answers with the error envelope instead, and `$TOKEN` stays empty: `401` with `UNAUTHORIZED` for any problem with the key, `403` with `PARTNER_SUSPENDED`, or `503` with `PARTNER_NOT_READY` or `UNAVAILABLE`.

Token rules for a real integration:

- `expires_in` is in seconds, at most 15 minutes. Cache the token and call `fresh_token` before each request instead of exchanging the key per call: the token endpoint allows 30 exchanges per minute per key.
- If a call answers `401` with `UNAUTHORIZED`, call `get_token` and send the same request once more, with the same `Idempotency-Key` if it has one. If the replay answers `401` again, stop and alert: the key was revoked or restricted.

```bash
whoami_status() {
  curl -sS -o /dev/null -w '%{http_code}' "$KUBEDC_API/auth/whoami" -H "Authorization: Bearer $TOKEN"
}

fresh_token
STATUS=$(whoami_status)
if [ "$STATUS" = 401 ]; then
  get_token
  STATUS=$(whoami_status)   # the single replay; a second 401 means the key itself was refused
fi
echo "GET /auth/whoami answered $STATUS"
```

Details: [Authentication](./authentication.md#caching-and-refreshing-tokens).

## 3. Check who you are

`GET /auth/whoami` is the self-test. It needs no particular scope:

```bash
WHOAMI=$(curl -sS "$KUBEDC_API/auth/whoami" -H "Authorization: Bearer $TOKEN")
jq .data <<<"$WHOAMI"
PLAN_ID=$(jq -r .data.defaults.plan_id <<<"$WHOAMI")
```

```json
{
  "partner": {"id": "acme", "display_name": "ACME Datacenter", "suspended": false},
  "scopes": ["customers:read", "customers:write", "customers:sso", "usage:read"],
  "limits": {"max_customers": 500, "requests_per_minute": 120, "max_usage_window_days": 90},
  "defaults": {"plan_id": "dev-pool", "project_name": "default", "egress_network_type": "cloud"},
  "customer_count": 42,
  "token": {"expires_at": "2026-09-14T10:15:00.000Z"}
}
```

Check that `scopes` holds what the rest of the tutorial needs: `customers:read`, `customers:write`, `customers:sso` for step 8 and `usage:read` for step 12. `$PLAN_ID` keeps your default plan for step 7.

## 4. Create a customer

`POST /customers` (scope `customers:write`) starts provisioning a customer: its organization, identity realm, owner user and a first project.

Send an `Idempotency-Key` derived from your own record, here the external id. If the request times out, you send exactly the same request again and cannot create a second tenant:

```bash
create_customer() {
  curl -sS -X POST "$KUBEDC_API/customers" \
    -H "Authorization: Bearer $TOKEN" \
    -H 'Content-Type: application/json' \
    -H "Idempotency-Key: $EXTERNAL_ID-create-customer" \
    "$@" \
    --data-binary @- <<EOF
{
  "external_id": "$EXTERNAL_ID",
  "email": "$OWNER_EMAIL",
  "first_name": "Jane",
  "last_name": "Doe",
  "company_name": "$COMPANY_NAME"
}
EOF
}

fresh_token
CREATED=$(create_customer)
jq .data <<<"$CREATED"
CUSTOMER_ID=$(jq -r .data.customer.id <<<"$CREATED")
OP_ID=$(jq -r .data.operation.id <<<"$CREATED")
```

A new customer answers `202 Accepted` with `Retry-After: 2`, the customer and its operation (abbreviated):

```json
{
  "customer": {
    "id": "acme-example-ltd",
    "external_id": "crm-1042",
    "status": "provisioning",
    "scheduled_deletion_at": null,
    "email": "jane@example.com",
    "plan_id": "dev-pool",
    "owner": {"first_name": "Jane", "last_name": "Doe", "username": "admin"},
    "projects": [],
    "console_url": "https://console.kube-dc.example",
    "created_at": "2026-09-14T09:12:03.000Z"
  },
  "operation": {
    "id": "op_5f0c2a9e1b7d4c3a8e6f1d20",
    "type": "customer.create",
    "customer_id": "acme-example-ltd",
    "state": "running",
    "phase": "realm",
    "progress": 35,
    "phases": [{"name": "organization", "state": "succeeded"}, {"name": "realm", "state": "running"}, {"name": "owner", "state": "pending"}, "..."],
    "error": null
  }
}
```

**Store the customer id with your record; do not compute it.** It is `<your prefix>-<slug>`, and the slug depends on what is already taken.

**Replay.** Send the same request again with the same key within 24 hours, and the first response comes back unchanged, marked `Idempotent-Replayed: true`. Nothing runs twice and no second operation is created:

```bash
create_customer -o /dev/null -D - | grep -iE '^(HTTP/|idempotent-replayed:)'
```

```text
HTTP/2 202
idempotent-replayed: true
```

Two more rules protect you:

- The same key with a different body answers `409` with `IDEMPOTENCY_KEY_REUSED`. That is a bug in your client; do not retry.
- A create with a new key but an `external_id` you already used returns the existing customer with `200` and `idempotent: true` instead of a second one.

Details: [Provisioning customers](./provisioning.md) and [Idempotency and retries](./idempotency.md).

## 5. Follow the operation

Provisioning takes minutes. Poll `GET /operations/{id}` until `state` is `succeeded` or `failed`, and wait `Retry-After` seconds between polls. Reading the operation also drives provisioning forward:

```bash
HEADERS=$(mktemp)
while :; do
  fresh_token
  OP=$(curl -sS -D "$HEADERS" "$KUBEDC_API/operations/$OP_ID" -H "Authorization: Bearer $TOKEN")
  STATE=$(jq -r '.data.state // "error"' <<<"$OP")
  jq -r '"\(.data.state) \(.data.phase) \(.data.progress)%"' <<<"$OP"
  [ "$STATE" = running ] || break
  WAIT=$(awk 'tolower($1) == "retry-after:" { print $2 + 0 }' "$HEADERS")
  sleep "${WAIT:-2}"
done
rm -f "$HEADERS"
jq '.data | {state, phase, progress, error}' <<<"$OP"
```

```text
running realm 35%
running realm 35%
running project 75%
running project 75%
succeeded ready 100%
```

`progress` is a fixed percentage per phase, so a phase that takes a while repeats the same line.

A failed operation carries `error.code`, such as `PROVISIONING_FAILED` or `PROVISIONING_TIMEOUT`, and `error.message`. Tolerate codes you do not know.

In production, do not block a web request on this loop. Return right after the create, follow the operation from a background job, and also react to the `customer.ready` and `customer.failed` webhooks from step 13. Keep polling operations your records still show as running, because a webhook can be lost. See [Provisioning customers](./provisioning.md#follow-the-operation).

## 6. Read the customer

`GET /customers/{id}` returns the customer with its projects:

```bash
fresh_token
curl -sS "$KUBEDC_API/customers/$CUSTOMER_ID" -H "Authorization: Bearer $TOKEN" | jq .data
```

Wherever a path takes a customer id, you can pass `ext:<external_id>` instead and address the customer by your own id. This is the same customer:

```bash
curl -sS "$KUBEDC_API/customers/ext:$EXTERNAL_ID" -H "Authorization: Bearer $TOKEN" |
  jq '.data | {id, external_id, status, plan_id, projects}'
```

```json
{
  "id": "acme-example-ltd",
  "external_id": "crm-1042",
  "status": "active",
  "plan_id": "dev-pool",
  "projects": [{"name": "default", "status": "active", "egress_network_type": "cloud"}]
}
```

URL-encode the external id if it contains characters other than letters, digits, `-`, `_` and `.`. A customer that is not yours answers exactly like one that does not exist: `404` with `NOT_FOUND`.

## 7. Size the customer

First read what your capacity pool has left, with `GET /capacity`:

```bash
fresh_token
curl -sS "$KUBEDC_API/capacity" -H "Authorization: Bearer $TOKEN" |
  jq -c '.data.dimensions[] | {name, unit, allocated, pool, available, per_customer_max}'
```

```text
{"name":"cpu","unit":"cores","allocated":96,"pool":256,"available":160,"per_customer_max":32}
{"name":"memory","unit":"bytes","allocated":412316860416,"pool":null,"available":null,"per_customer_max":null}
```

`null` means unlimited. Units are `cores`, `bytes`, `count`, `MiB` and `percent`, so convert before you cap a slider: `memory` is in bytes while the quota document says `"8Gi"`.

Then read the current quota for its `revision`, and send the new document with `PUT /customers/{id}/quota`:

```bash
REVISION=$(curl -sS "$KUBEDC_API/customers/$CUSTOMER_ID/quota" -H "Authorization: Bearer $TOKEN" |
  jq '.data.configured.revision // 0')

QUOTA_RESULT=$(curl -sS -X PUT "$KUBEDC_API/customers/$CUSTOMER_ID/quota" \
  -H "Authorization: Bearer $TOKEN" \
  -H 'Content-Type: application/json' \
  -H "Idempotency-Key: $EXTERNAL_ID-quota-after-revision-$REVISION" \
  --data-binary @- <<EOF
{
  "quota": {
    "cpu": "2",
    "memory": "8Gi",
    "storage": "50Gi",
    "pods": 30,
    "load_balancers": 1,
    "public_ips": 1,
    "object_storage": {"size": "20Gi", "buckets": 2},
    "template_plan": "$PLAN_ID"
  },
  "expected_revision": $REVISION
}
EOF
)

case "$(jq -r '.error.code // empty' <<<"$QUOTA_RESULT")" in
  "")       jq '.data | {source, enforcement, configured, message}' <<<"$QUOTA_RESULT" ;;
  CONFLICT) echo "The quota changed since revision $REVISION: read it again, re-apply your change and send it again" ;;
  *)        jq .error <<<"$QUOTA_RESULT" ;;
esac
```

```json
{
  "source": "partner-quota",
  "enforcement": "partner-quota",
  "configured": {
    "cpu": "2", "memory": "8Gi", "storage": "50Gi", "pods": 30, "load_balancers": 1, "public_ips": 1,
    "object_storage": {"size": "20Gi", "buckets": 2}, "template_plan": "dev-pool",
    "revision": 1, "updated_at": "2026-09-14T09:20:11.000Z", "updated_by": "partner/acme"
  },
  "message": "Quota accepted. The cluster applies it on the next reconcile, normally within seconds."
}
```

Rules for the quota document:

- **Send the complete document every time.** It replaces the previous one, and a dimension you leave out is not granted: the platform enforces it at zero. To raise only the CPU, send every other dimension again unchanged.
- `template_plan` never supplies amounts. It supplies scheduling policy (burst ratio and default container limits) and defaults to your partner default plan.
- `expected_revision` is the `revision` you read. If the quota changed in between, the write answers `409` with `CONFLICT` and changes nothing. Read the quota again, re-apply your user's change and send it with the new revision.
- The `Idempotency-Key` above includes the revision it was computed from, so retrying the same change replays its answer, and a change based on a newer revision gets a new key.
- `409` with `PARTNER_CAPACITY_EXCEEDED` or `CUSTOMER_CAPACITY_EXCEEDED` means the request does not fit; `details` names each dimension. `409` with `BUSY` is retryable after `Retry-After`.

Setting a quota removes the customer's plan: the customer's `plan_id` reads `null` from now on. Details: [Plans, quota and capacity](./plans-quota-capacity.md#customer-quota-put-replaces-the-whole-document).

## 8. One-click console login

When a customer clicks "Open console" in your portal, your server mints a login URL with `POST /customers/{id}/console-login` (scope `customers:sso`) and redirects the browser to it immediately. The URL is valid for 30 seconds, works once, and is a credential: never store, log or email it.

From the shell, check that minting works without printing the URL:

```bash
fresh_token
LOGIN=$(curl -sS -X POST "$KUBEDC_API/customers/ext:$EXTERNAL_ID/console-login" \
  -H "Authorization: Bearer $TOKEN" \
  -H 'Content-Type: application/json' \
  -H "Idempotency-Key: console-login-$(uuidgen)" \
  -d '{}')
jq '.data | {has_url: (.url != null), expires_at, single_use, project}' <<<"$LOGIN"
unset LOGIN
```

```json
{"has_url": true, "expires_at": "2026-09-14T09:21:30.000Z", "single_use": true, "project": "default"}
```

The empty body logs in as the owner, `admin`, and opens the customer's first project. Send `user` to log in as another user (step 9) and `project` to open another project. Use a **new** `Idempotency-Key` for every click: a retry with the same key replays the same, already used URL.

In your portal, the route is a server-side redirect. First authenticate your own user and check that they may open this customer: the route logs the browser in. A minimal Node.js 18 (Express) route, with plain `fetch` and no SDK:

```js
const crypto = require('node:crypto');
const express = require('express');

const app = express();
const KUBEDC_API = process.env.KUBEDC_API; // https://<your-kube-dc-backend>/api/public/v1

// Your own pieces: getAccessToken() returns a cached token from POST /auth/token and refreshes it
// before it expires (step 2); requirePortalUserOwning() is your login check for this customer.
app.get('/customers/:crmId/console', requirePortalUserOwning('crmId'), async (req, res, next) => {
  try {
    const response = await fetch(`${KUBEDC_API}/customers/ext:${encodeURIComponent(req.params.crmId)}/console-login`, {
      method: 'POST',
      headers: {
        Authorization: `Bearer ${await getAccessToken()}`,
        'Content-Type': 'application/json',
        'Idempotency-Key': `console-login-${crypto.randomUUID()}`, // new key per click
      },
      body: '{}',
    });
    const body = await response.json();
    if (response.status === 409 && body.error?.code === 'NOT_READY') {
      return res.status(409).send('Your environment is still being prepared.');
    }
    if (!response.ok) return res.status(502).send(`Console login failed (${body.error?.request_id})`);
    res.redirect(302, body.data.url); // never store, log or render this URL
  } catch (err) {
    next(err);
  }
});
```

The same route in plain PHP with the curl extension:

```php
<?php
// console.php?customer=crm-1042. Authenticate your user and check they may open this customer first.
$crmId = $_GET['customer'] ?? '';
if (!is_string($crmId) || preg_match('/^[A-Za-z0-9._-]{1,128}$/', $crmId) !== 1) {
    http_response_code(400);
    exit;
}

$ch = curl_init(getenv('KUBEDC_API') . '/customers/ext:' . rawurlencode($crmId) . '/console-login');
curl_setopt_array($ch, [
    CURLOPT_POST => true,
    CURLOPT_POSTFIELDS => '{}',
    CURLOPT_RETURNTRANSFER => true,
    CURLOPT_HTTPHEADER => [
        'Authorization: Bearer ' . get_access_token(),   // cached token from POST /auth/token (step 2)
        'Content-Type: application/json',
        'Idempotency-Key: console-login-' . bin2hex(random_bytes(16)),   // new key per click
    ],
]);
$body = json_decode((string) curl_exec($ch), true);
$status = curl_getinfo($ch, CURLINFO_RESPONSE_CODE);
curl_close($ch);

if ($status !== 200 || !isset($body['data']['url'])) {
    http_response_code($status === 409 ? 409 : 502);
    exit;
}
header('Location: ' . $body['data']['url'], true, 302);   // never store, log or render this URL
exit;
```

Details and error codes: [One-click console login](./console-login.md).

## 9. Add a user

Every customer has an owner user, `admin`, which console login uses by default. To give another person their own access, create a user with `POST /customers/{id}/users`:

```bash
fresh_token
USER=$(curl -sS -X POST "$KUBEDC_API/customers/$CUSTOMER_ID/users" \
  -H "Authorization: Bearer $TOKEN" \
  -H 'Content-Type: application/json' \
  -H "Idempotency-Key: $EXTERNAL_ID-add-user-sam" \
  --data-binary @- <<'EOF'
{"email": "sam@example.com", "first_name": "Sam", "last_name": "Lee", "group": "user"}
EOF
)
jq .data <<<"$USER"
USER_ID=$(jq -r .data.id <<<"$USER")
```

The answer is `201 Created`:

```json
{
  "id": "6c1f0e0e-9a51-4d0a-8f7e-3b7c2f9d1a44",
  "username": "sam@example.com",
  "email": "sam@example.com",
  "first_name": "Sam",
  "last_name": "Lee",
  "enabled": true,
  "email_verified": true,
  "group": "user",
  "owner": false,
  "created_at": "2026-09-14T09:22:40.000Z"
}
```

Users have no password and receive no invitation. They reach the console only through step 8, with `{"user": "sam@example.com"}` as the body. `group` is `user` or `org-admin`; `409` with `ALREADY_EXISTS` means the email is taken in this customer. See [One-click console login](./console-login.md#users-and-owner-access).

## 10. Suspend, resume and delete

Drive the lifecycle from your billing events. Key each call by the event that caused it, so a billing event delivered twice gets the first answer back instead of acting twice.

Invoice overdue, suspend. Workloads keep running unless you send `"stop_workloads": true`:

```bash
fresh_token
curl -sS -X POST "$KUBEDC_API/customers/$CUSTOMER_ID/suspend" \
  -H "Authorization: Bearer $TOKEN" \
  -H 'Content-Type: application/json' \
  -H 'Idempotency-Key: invoice-2026-0912-overdue' \
  -d '{"reason": "Invoice 2026-0912 overdue"}' | jq .data
```

```json
{"customer_id": "acme-example-ltd", "state": "suspended", "suspended_at": "2026-09-14T09:25:00.000Z", "workloads_running": true}
```

Invoice paid, resume:

```bash
curl -sS -X POST "$KUBEDC_API/customers/$CUSTOMER_ID/resume" \
  -H "Authorization: Bearer $TOKEN" \
  -H 'Idempotency-Key: invoice-2026-0912-paid' | jq .data
```

```json
{"customer_id": "acme-example-ltd", "state": "active", "workloads_running": true, "cancelled_scheduled_deletion": null}
```

Subscription cancelled, schedule the deletion. Service stops now, data is kept, and teardown becomes due after `grace_days` (0 to 90, default 7):

```bash
curl -sS -X DELETE "$KUBEDC_API/customers/$CUSTOMER_ID?grace_days=14" \
  -H "Authorization: Bearer $TOKEN" \
  -H 'Idempotency-Key: subscription-77-cancelled' | jq .data
```

```json
{
  "customer_id": "acme-example-ltd",
  "state": "deleting",
  "scheduled_deletion_at": "2026-09-28T09:26:00.000Z",
  "grace_days": 14,
  "recoverable": true,
  "message": "Service cancelled. Data is retained until 2026-09-28T09:26:00.000Z; POST /customers/acme-example-ltd/resume before then to restore it."
}
```

Until the due date, a resume cancels the deletion, and `cancelled_scheduled_deletion` carries the date it cancelled:

```bash
curl -sS -X POST "$KUBEDC_API/customers/$CUSTOMER_ID/resume" \
  -H "Authorization: Bearer $TOKEN" \
  -H 'Idempotency-Key: subscription-77-reactivated' | jq .data
```

Account closed for good, delete immediately. This is irreversible, so this tutorial runs it only in step 15:

```bash
curl -sS -X DELETE "$KUBEDC_API/customers/$CUSTOMER_ID?immediate=true" \
  -H "Authorization: Bearer $TOKEN" \
  -H "Idempotency-Key: $EXTERNAL_ID-close-account" | jq .data
```

An immediate delete answers `202` at once and tears the customer down in the background. It is refused with `409` and `CONFLICT` while the customer runs VMs or databases, which `details` lists, unless you also pass `force=true`. A resume is refused with `409` and `CONFLICT` once teardown has started, or when the platform operator rather than you suspended the customer. Details: [Customer lifecycle](./lifecycle.md).

## 11. Walk a list

Every list pages the same way: `limit` (1 to 200, default 50) and `cursor`. Pass `pagination.next_cursor` back unchanged, with the same other parameters, until it is `null`. This loop prints every customer; the small `limit` only makes the paging visible:

```bash
fresh_token
CURSOR=""
while :; do
  PAGE=$(curl -sS -G "$KUBEDC_API/customers" -H "Authorization: Bearer $TOKEN" \
    --data-urlencode 'limit=1' \
    ${CURSOR:+--data-urlencode "cursor=$CURSOR"})
  jq -r '.data[] | "\(.id)\t\(.status)\t\(.external_id // "-")"' <<<"$PAGE"
  CURSOR=$(jq -r '.pagination.next_cursor // empty' <<<"$PAGE")
  [ -n "$CURSOR" ] || break
done
```

A page looks like this:

```json
{"status": "success", "data": [{"id": "acme-example-ltd", "status": "active", "...": "..."}], "pagination": {"next_cursor": "eyJsIjoiY3VzdG9tZXJzIiwiayI6ImFjbWUtZXhhbXBsZS1sdGQifQ", "limit": 1}}
```

`GET /customers` also filters by `external_id` and `status`, for example `--data-urlencode 'status=suspended'` for a nightly reconciliation with your billing system. Cursors are opaque: never decode or build one. See [Lists and pagination](./pagination.md).

## 12. Read usage

`GET /customers/{id}/usage` (scope `usage:read`) returns what one customer uses against its limits:

```bash
fresh_token
curl -sS "$KUBEDC_API/customers/$CUSTOMER_ID/usage" -H "Authorization: Bearer $TOKEN" | jq .data
```

```json
{
  "customer_id": "acme-example-ltd",
  "current": {
    "cpu": {"used": "0", "limit": "2"},
    "memory": {"used": "0Gi", "limit": "8Gi"},
    "storage": {"used": "0Gi", "limit": "50Gi"},
    "pods": {"used": "0", "limit": "30"},
    "gpu": null,
    "observed_at": "2026-09-14T09:30:00Z"
  },
  "max_window_days": 90
}
```

`current` is what the platform last observed, as of `observed_at`. Right after a quota change it can still show the previous limits until the platform refreshes the figures.

For invoicing, `GET /usage` returns every customer a page at a time. Add `from`, `to` and `granularity` to get the billable `window`, and walk the pages as in step 11:

```bash
curl -sS -G "$KUBEDC_API/usage" -H "Authorization: Bearer $TOKEN" \
  --data-urlencode 'from=2026-08-01T00:00:00Z' \
  --data-urlencode 'to=2026-09-01T00:00:00Z' \
  --data-urlencode 'granularity=day' \
  --data-urlencode 'limit=100' |
  jq -c '.data[] | {customer_id, external_id, window}'
```

```text
{"customer_id":"acme-example-ltd","external_id":"crm-1042","window":{"from":"2026-08-01T00:00:00.000Z","to":"2026-09-01T00:00:00.000Z","granularity":"day","cpu_hours":4464.5,"memory_gib_hours":17856,"storage_gib_hours":89280}}
```

A value that could not be measured is `null`, never zero, and its window carries `partial: true`: never invoice a partial window as zero. See [Usage and billing export](./usage-billing.md).

## 13. Webhooks

Webhooks tell your backend when something changes, so you do not have to poll for every change. They are an optimisation over polling, never a replacement.

### Configure the receiver

`PUT /webhook` sets your receiver. The first call that creates the signing secret returns it once, in `signing_secret`. Store it straight in your secret store:

```bash
fresh_token
HOOK=$(curl -sS -X PUT "$KUBEDC_API/webhook" \
  -H "Authorization: Bearer $TOKEN" \
  -H 'Content-Type: application/json' \
  -H 'Idempotency-Key: setup-webhook-v1' \
  --data-binary @- <<'EOF'
{"url": "https://portal.example.com/hooks/kube-dc", "events": ["customer.*", "project.ready"], "enabled": true}
EOF
)
jq '.data | del(.signing_secret)' <<<"$HOOK"          # everything except the secret
jq -r '.data.signing_secret // empty' <<<"$HOOK" | store_secret KUBEDC_WEBHOOK_SECRET
unset HOOK
```

Here `store_secret` stands for your secret store's command line. Later calls answer `signing_secret_configured: true` instead of the secret.

**Lost the response?** Send the same `PUT /webhook` with the same `Idempotency-Key` within 24 hours: the replayed response carries the secret again. After that, rotate the secret with `POST /webhook/secret`.

### Test and inspect

`POST /webhook/test` sends one signed `ping` and reports what your receiver answered:

```bash
fresh_token
curl -sS -X POST "$KUBEDC_API/webhook/test" \
  -H "Authorization: Bearer $TOKEN" \
  -H "Idempotency-Key: webhook-test-$(uuidgen)" | jq .data
```

```json
{"delivered": true, "delivery_id": "dlv_0a1b2c3d4e5f60718293", "attempts": 1, "status": 204, "error": null}
```

`GET /webhook/deliveries` lists the recent delivery attempts, newest first:

```bash
curl -sS -G "$KUBEDC_API/webhook/deliveries" -H "Authorization: Bearer $TOKEN" \
  --data-urlencode 'limit=10' | jq -c '.data[] | {id, event, delivered, attempts, status, error, at}'
```

```text
{"id":"dlv_0a1b2c3d4e5f60718293","event":"ping","delivered":true,"attempts":1,"status":204,"error":null,"at":"2026-09-14T09:31:02.000Z"}
```

### Verify every delivery

Each delivery is a POST with a JSON body and these headers:

- `X-KubeDC-Timestamp`: Unix seconds when the delivery was signed;
- `X-KubeDC-Signature`: `sha256=` followed by the lowercase hex HMAC-SHA256 of the timestamp, a `.` and the raw body, keyed with your signing secret;
- `X-KubeDC-Event` and `X-KubeDC-Delivery`: the event name and delivery id. They are not signed, so take both from the verified body instead.

Verify before you trust anything in the request:

1. Read the **raw body bytes**. A body parsed and serialised again does not match.
2. Compute the HMAC over the timestamp header, `.` and the raw body, and compare it with the header in **constant time**.
3. Reject a timestamp more than **5 minutes** (300 seconds) away from your clock.
4. Parse the body only then, and deduplicate on its `id`: delivery is at least once.
5. Answer `2xx` within 5 seconds and queue slow work. Answer `400` only for a failed verification.

In Node.js, with the built-in `crypto` module:

```js
const crypto = require('node:crypto');

const TOLERANCE_SECONDS = 300;

/** True when a delivery is signed with `secret` and fresh. `rawBody` is the exact request body as a Buffer. */
function verifyKubeDCSignature(rawBody, headers, secret, now = Math.floor(Date.now() / 1000)) {
  const timestamp = headers['x-kubedc-timestamp'];
  const signature = headers['x-kubedc-signature'];
  if (typeof timestamp !== 'string' || !/^[0-9]+$/.test(timestamp)) return false;
  if (typeof signature !== 'string' || !/^sha256=[0-9a-f]{64}$/.test(signature)) return false;
  if (Math.abs(now - Number(timestamp)) > TOLERANCE_SECONDS) return false;

  const expected = crypto.createHmac('sha256', secret).update(`${timestamp}.`).update(rawBody).digest();
  const received = Buffer.from(signature.slice('sha256='.length), 'hex');
  return received.length === expected.length && crypto.timingSafeEqual(received, expected);
}
```

A receiver in Express. `express.raw` keeps the body as the exact bytes that were signed:

```js
const express = require('express');

const app = express();
const secret = process.env.KUBEDC_WEBHOOK_SECRET;

app.post('/hooks/kube-dc', express.raw({ type: 'application/json' }), async (req, res) => {
  if (!verifyKubeDCSignature(req.body, req.headers, secret)) return res.status(400).send('invalid signature');

  const event = JSON.parse(req.body.toString('utf8'));
  // At least once: skip ids you already processed. Keep them in your database, for longer than the retry schedule.
  if (await alreadyProcessed(event.id)) return res.sendStatus(204);
  await enqueue(event); // e.g. customer.ready -> mark event.data.customer_id as ready in your records
  await markProcessed(event.id);
  res.sendStatus(204);
});
```

In PHP, with `hash_hmac` and `hash_equals`:

```php
<?php
/** True when a delivery is signed with $secret and fresh. $server is $_SERVER (or the request's headers in that shape). */
function verify_kubedc_signature(string $rawBody, array $server, string $secret, ?int $now = null): bool
{
    $timestamp = $server['HTTP_X_KUBEDC_TIMESTAMP'] ?? '';
    $signature = $server['HTTP_X_KUBEDC_SIGNATURE'] ?? '';
    if (!is_string($timestamp) || preg_match('/^[0-9]+$/', $timestamp) !== 1) {
        return false;
    }
    if (!is_string($signature) || preg_match('/^sha256=[0-9a-f]{64}$/', $signature) !== 1) {
        return false;
    }
    if (abs(($now ?? time()) - (int) $timestamp) > 300) {
        return false;
    }
    $expected = 'sha256=' . hash_hmac('sha256', $timestamp . '.' . $rawBody, $secret);
    return hash_equals($expected, $signature);
}
```

A receiver as a plain PHP script:

```php
<?php
$rawBody = (string) file_get_contents('php://input');   // the exact raw body, never re-encoded JSON
if (!verify_kubedc_signature($rawBody, $_SERVER, (string) getenv('KUBEDC_WEBHOOK_SECRET'))) {
    http_response_code(400);
    exit;
}

$event = json_decode($rawBody, true, 512, JSON_THROW_ON_ERROR);
// At least once: skip ids you already processed (a unique key on the id in your database works well).
if (!already_processed($event['id'])) {
    enqueue($event);   // e.g. customer.ready -> mark $event['data']['customer_id'] as ready
    mark_processed($event['id']);
}
http_response_code(204);
```

The deduplication and queue helpers in both receivers stand for your own storage and queue. While you rotate the signing secret, accept a delivery that verifies with either the new or the previous secret for a few minutes. Events, retries and rotation: [Webhooks](./webhooks.md).

## 14. Handle errors

Every error uses one envelope. Branch on `code`, never on `message`:

```json
{"status": "error", "error": {"code": "CONFLICT", "message": "Quota revision mismatch: you sent 0, the cluster holds 1. Re-read the quota and retry.", "request_id": "req_k2j4h6g8f0"}}
```

`request_id` equals the `X-Request-Id` response header. Log it with the error code and quote it when you contact support. Some codes also carry `details`.

What to retry:

- **Retry after `Retry-After` seconds:** `429` with `RATE_LIMITED` or `TOO_MANY_OPERATIONS`; `409` with `BUSY` or `IDEMPOTENCY_KEY_IN_PROGRESS`; `503` with `UNAVAILABLE`, `PARTNER_NOT_READY` or `GPU_USAGE_UNAVAILABLE`.
- **Retry when the state allows it:** `409` with `NOT_READY` once provisioning finished; `404` with `UPSTREAM_NOT_FOUND` after reading the resource again.
- **Retry once, after a new token:** `401` with `UNAUTHORIZED`.
- **Network errors, timeouts and gateway `502` or `504`:** retry a GET freely, and a mutating request only with its original `Idempotency-Key`.
- **Do not retry blindly:** `500` with `INTERNAL_ERROR` on a mutating request, because a `5xx` is never stored against the key. Read the current state first.
- **Never retry unchanged:** every other `400`, `403`, `404` or `409`, including `VALIDATION_ERROR`, `FORBIDDEN`, `CONFLICT` and `IDEMPOTENCY_KEY_REUSED`.

Keep the same `Idempotency-Key` across every retry of one operation, wait `Retry-After` when it is present, otherwise back off exponentially with jitter, and bound the number of attempts. The full tables: [Errors reference](./errors.md#error-codes) and [Idempotency and retries](./idempotency.md#which-failures-to-retry).

## 15. Clean up

Delete the tutorial customer immediately, then wait until it answers `404`, which is the successful end of a deletion you requested:

```bash
fresh_token
curl -sS -X DELETE "$KUBEDC_API/customers/$CUSTOMER_ID?immediate=true" \
  -H "Authorization: Bearer $TOKEN" \
  -H "Idempotency-Key: $EXTERNAL_ID-close-account" | jq .data

while :; do
  fresh_token
  STATUS=$(curl -sS -o /dev/null -w '%{http_code}' "$KUBEDC_API/customers/$CUSTOMER_ID" -H "Authorization: Bearer $TOKEN")
  [ "$STATUS" = 404 ] && break
  sleep 15
done
echo "$CUSTOMER_ID is gone"

unset TOKEN TOKEN_RESPONSE KUBEDC_API_KEY
```

```json
{
  "customer_id": "acme-example-ltd",
  "state": "deleting",
  "immediate": true,
  "recoverable": false,
  "message": "Teardown started. The customer reports \"deleting\" until it is gone, then 404; customer.deleted is sent when that happens. This cannot be undone."
}
```

Teardown takes a few minutes, and `customer.deleted` is sent when it completes. If you configured a test webhook in step 13 and no longer need it, remove it with `DELETE /webhook`. Before real customers use your integration, work through the [Going live checklist](./going-live.md).
