# Going live checklist

Work through this list before your integration handles real customers.

## Credentials

- The API key lives only in your server-side secret store. It is not in source control, container images, client-side code, customer-visible configuration or logs.
- Each server that calls the API can be switched to a new key without a code change, so you can rotate keys (see [Authentication](./authentication.md)).
- The webhook signing secret is stored the same way, and you know how to rotate it without rejecting valid deliveries.
- You requested only the scopes you use. `customers:sso` only if you use console login.

## Tokens

- Access tokens are cached and refreshed about 60 seconds before they expire, never fetched per request.
- Concurrent refreshes are single-flight.
- A `401` triggers one token refresh and one replay, not a loop.

## Requests

- Every POST, PUT, PATCH and DELETE (except the token exchange) carries an `Idempotency-Key`, stored with your record and reused on retries.
- Every customer create carries your `external_id`.
- Retries honour `Retry-After`, use exponential backoff with jitter, and are bounded. Only the failures listed in [Idempotency and retries](./idempotency.md) are retried.
- Your code branches on `error.code`, not on messages, and tolerates unknown codes, fields, enum values and events.
- Lists are walked until `next_cursor` is `null`.

## Provisioning and lifecycle

- Web requests return right after a create; a background job follows the operation.
- Your portal shows provisioning progress from `phase` and `progress`, and a clear state for a failed operation.
- A periodic job reconciles your records with the API: operations still running, and customers whose status you expect to change. Polling is authoritative; webhooks only make you faster.
- Suspend, resume and delete are wired to your billing events, and you handle `409` with `CONFLICT` on resume (an operator hold).

## Webhooks

- The receiver verifies the signature over the raw body with a constant-time comparison, and rejects timestamps more than 5 minutes off.
- Event names and ids are taken from the verified body, not the headers.
- Deliveries are deduplicated on `id`.
- The receiver answers `2xx` within 5 seconds and queues slow work.
- `POST /webhook/test` succeeds from your production receiver.

## Portal UI

- Sliders are capped with `GET /capacity`, and `PARTNER_CAPACITY_EXCEEDED`, `CUSTOMER_CAPACITY_EXCEEDED`, `LIMIT_REACHED` and `BUSY` show a helpful message instead of a generic error.
- Only plans from `GET /catalog/plans` and GPU profiles you are allowed are offered.
- The "Open console" button mints the login URL at click time on your server, after checking the user may open that customer, and redirects immediately.

## Operations

- **Clocks are synchronised** (NTP) on every server that verifies webhooks or computes billing windows.
- **Logs contain no secrets**: no API keys, access tokens, console login URLs or webhook secrets. They do contain `X-Request-Id` values, error codes and operation ids.
- You alert on repeated `401`, `403` and `5xx` responses and on operations that stay `running` for more than 15 minutes.
- Usage exports never invoice a partial window as zero.

## Support

- **Incident contact:** your platform operator's support contact for urgent incidents. Record it (name, email and phone) where your on-call team can find it before you go live.
- When you report a problem, include the `X-Request-Id` or operation id, the time in UTC and the error code.
- Tell your operator at once if a key or signing secret may have leaked.
