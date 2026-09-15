# Idempotency and retries

Networks fail. When a request times out or the connection drops, you cannot tell whether Kube-DC executed it. Idempotency keys make it safe to send the same request again.

## The Idempotency-Key header

Send an `Idempotency-Key` header on **every mutating request**: every POST, PUT, PATCH and DELETE, except `POST /auth/token`.

- The value is 1 to 255 characters from `A-Z`, `a-z`, `0-9`, `_`, `.`, `:` and `-`. Anything else answers `400` with `VALIDATION_ERROR`.
- Generate one key per **logical operation**, store it with your own record before you send the request, and reuse it for every retry of that operation. A random UUID works, as does a stable key derived from your data, for example `invoice-2026-0912:suspend`.
- Use a new key for a new operation. Suspending a customer again next month is a new operation and needs a new key. Never mint a new key per attempt: that defeats the protection.
- A key is scoped to your partner account, the HTTP method and the request path with its parameters. The same key on another path is a different key.

## What Kube-DC does with the key

The first request with a key runs normally. Its completed response is stored for **24 hours**, together with a fingerprint of the request (the JSON body and the query parameters).

| Situation | Result |
|---|---|
| Same key and same request, within 24 hours | The stored response comes back exactly: status, body, `Location` and `Retry-After`, plus the header `Idempotent-Replayed: true`. Nothing runs a second time. |
| Same key, different body or query | `409` with `IDEMPOTENCY_KEY_REUSED`. A key was reused for a different operation; this is a bug in your client. Do not retry. |
| Same key while the first request is still running | `409` with `IDEMPOTENCY_KEY_IN_PROGRESS` and `Retry-After`. Wait, then retry with the same key. |
| More than 24 hours later | The key is forgotten; the request runs as new. |

Which responses are stored:

- **Stored:** `2xx` responses and `4xx` errors. A retry of a request that failed validation replays the same error: to send a corrected request, use a new key.
- **Not stored**, because they mean "try again later": `429`, `409` with `BUSY`, `409` with `NOT_READY`, `404` with `UPSTREAM_NOT_FOUND`, and `409` with `IDEMPOTENCY_KEY_IN_PROGRESS`. A retry with the same key runs the request again.
- **Never stored:** `5xx` responses. A retry after one runs the request for real.

A replay returns the response **as it was** the first time. For example, a replayed `202` from a customer create returns the original operation, which may have finished since, and no second operation is created. Read the current state with a GET.

Your partner account can hold a bounded number of live keys (10,000 per 24 hours by default). Past that, a request with a **new** key is refused with `429` and `RATE_LIMITED`; retries of keys you already hold still replay. Reaching the limit almost always means a client mints a key per attempt instead of per operation.

### Recovering a lost webhook signing secret

`PUT /webhook` returns the signing secret only in the response that creates it. If that response is lost, repeat the same `PUT /webhook` with the same `Idempotency-Key` within 24 hours: the replay carries the secret. See [Webhooks](./webhooks.md).

### external_id still deduplicates creates

`POST /customers` is also idempotent on `external_id`: repeating a create for an existing `external_id` returns that customer with `200` and `idempotent: true`. A replayed `Idempotency-Key` response takes precedence; without a key, or with a new one, the `external_id` match applies. Send both. The key protects the exact request; `external_id` protects you from ever creating two tenants for one of your accounts. See [Provisioning customers](./provisioning.md).

## Which failures to retry

| Failure | Retry? |
|---|---|
| `429` with `RATE_LIMITED` or `TOO_MANY_OPERATIONS` | Yes, after `Retry-After` seconds. |
| `409` with `BUSY` | Yes, after `Retry-After` seconds. Another capacity change for your partner held the lock. |
| `409` with `IDEMPOTENCY_KEY_IN_PROGRESS` | Yes, after `Retry-After` seconds, with the same key. |
| `409` with `NOT_READY` | Yes, once the customer or project has finished provisioning. |
| `404` with `UPSTREAM_NOT_FOUND` | Yes, after re-reading the resource, with backoff. |
| `503` with `UNAVAILABLE`, `PARTNER_NOT_READY` or `GPU_USAGE_UNAVAILABLE` | Yes, after `Retry-After` seconds. |
| Network error, timeout, or a `502`/`504` from a proxy | For a GET, yes. For a mutating request, only when it carries an `Idempotency-Key`: if the first attempt is still running you get `IDEMPOTENCY_KEY_IN_PROGRESS`, if it finished you get its stored response. Use backoff. |
| `500` with `INTERNAL_ERROR` | For a GET, yes, with backoff. For a mutating request, not blindly: a `5xx` is never stored, so a retry runs the request again even with the same key. Read the current state first (for example the customer by `external_id`, or the operation), then retry if nothing happened. |
| `401` with `UNAUTHORIZED` | Once, after exchanging the key for a fresh token. See [Authentication](./authentication.md). |
| Any other `400`, `403`, `404` or `409` | No. The request is wrong or the state does not allow it; the same request gets the same answer. |

## Backoff

- When the response has a `Retry-After` header, wait exactly that many seconds, plus a little random jitter.
- Otherwise use exponential backoff with jitter: before attempt *n*, wait a random time between half and all of `base × 2^n`, capped (for example a base of 500 ms and a cap of 30 to 60 seconds).
- Limit the number of attempts. When the wait would exceed what the caller can afford (for example inside a web request), give up and schedule the retry in a background job instead of blocking.
- Keep the same `Idempotency-Key` across all attempts.

The SDKs do all of this for you:

- Every mutating call sends an `Idempotency-Key`: a fresh random key per call, reused on every automatic retry of that call. Pass your own key when a retry may come from another process or after a restart, for example one derived from your order id; the SDK checks its format before sending.
- They retry network errors, timeouts, `429`, `503`, `409` with `BUSY` or `IDEMPOTENCY_KEY_IN_PROGRESS`, and gateway `502`/`504`, with exponential backoff and jitter, honouring `Retry-After`.
- They never retry `409` with `IDEMPOTENCY_KEY_REUSED`, any other `4xx`, or a `500` on a mutating call.
- They report whether a response was a replay (`Idempotent-Replayed`).
