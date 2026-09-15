# Errors reference

Every error answers with the same envelope:

```json
{"status": "error", "error": {"code": "NOT_FOUND", "message": "No customer acme-example-ltd", "request_id": "req_k2j4h6g8f0", "details": null}}
```

- **Branch on `code`**, which is stable and machine-readable. `message` is written for the integrator and may change: show or log it, never parse it.
- `details` is present only for some codes, and its shape depends on the code (see the table).
- `request_id` equals the `X-Request-Id` response header. Log it and quote it when you contact support.
- **Tolerate codes you do not know.** New codes may be added within v1; treat an unknown code by its HTTP status.

The SDKs raise typed errors that carry `code`, `message`, `request_id`, `details` and the `Retry-After` value.

## Error codes

| Code | Status | Meaning | What to do |
|---|---|---|---|
| `VALIDATION_ERROR` | 400 | The request is malformed: a missing or invalid field, an unknown field where none are allowed, a bad query parameter. `details` lists the problems (for webhooks, `known_events`). | Fix the request. Do not retry unchanged. |
| `QUOTA_EXCEEDED` | 400 | A project cap is above what the customer holds. `details` lists each violation. | Lower the cap, or raise the customer's quota first. |
| `NOT_CONFIGURED` | 400 | A webhook test was requested with no webhook URL or signing secret. | Configure the webhook with `PUT /webhook`. |
| `UNAUTHORIZED` | 401 | The API key or the access token is missing, invalid, expired or revoked. | Exchange the key for a new token once; if that fails, check the key with your operator. |
| `FORBIDDEN` | 403 | The token lacks the required scope, your partner account is suspended, or the action is not allowed (such as deleting the owner user). | Check your scopes with `GET /auth/whoami`. Do not retry. |
| `PARTNER_SUSPENDED` | 403 | Token exchange refused: your partner account is suspended. | Contact your operator. |
| `LIMIT_REACHED` | 403 | Your maximum number of customers, or the customer's maximum number of projects, is reached. `details` has `max` and `current` for projects. | Tell the user; ask your operator to raise the limit if needed. |
| `PLAN_NOT_ALLOWED` | 403 | The plan is not one you may assign. | Offer only plans from `GET /catalog/plans`. |
| `GPU_PROFILE_NOT_ALLOWED` | 403 | The request grants a GPU profile you may not grant. `details` lists the profiles. | Offer only the profiles in `GET /capacity`. |
| `NOT_FOUND` | 404 | The resource does not exist, or belongs to someone else. | Check the id. For a customer you just deleted, this is the end state. |
| `UPSTREAM_NOT_FOUND` | 404 | A platform object the request depended on disappeared while it ran. | Re-read the resource and retry later. If it persists, contact support with the `request_id`. |
| `ALREADY_EXISTS` | 409 | The customer id, project, namespace or user email is already taken. | For a customer, send a different `name`; otherwise pick another name or email. |
| `CONFLICT` | 409 | The current state does not allow the request: a quota revision mismatch, live VMs or databases blocking a deletion (listed in `details`), a customer being deleted, or an operator hold on resume. | Re-read the resource, resolve the cause, then send a new request. |
| `NOT_READY` | 409 | The customer or project is still provisioning. | Wait until it is `active`, then retry. |
| `BUSY` | 409 | Another capacity change for your partner held the lock. | Retry after `Retry-After` seconds. |
| `CUSTOMER_CAPACITY_EXCEEDED` | 409 | The change exceeds the per-customer maximum. `details` lists each dimension. | Lower the request. |
| `PARTNER_CAPACITY_EXCEEDED` | 409 | The change exceeds your partner capacity pool. `details` lists each dimension with what is available. | Lower the request, or ask your operator for more capacity. |
| `GPU_ENTITLEMENT_REDUCTION_BLOCKED` | 409 | The change would take a GPU grant below what running workloads use. `details` lists the blockers (and, for a plan change, the workloads). | Stop those workloads first. |
| `USER_ACTION_REQUIRED` | 409 | Console login needs the user to complete an identity-provider action first; the message names it. | Show the message; do not retry in a loop. |
| `IDEMPOTENCY_KEY_REUSED` | 409 | The `Idempotency-Key` was already used with a different request body. | Fix your client: use a new key for a new operation. Do not retry. |
| `IDEMPOTENCY_KEY_IN_PROGRESS` | 409 | The first request with this `Idempotency-Key` is still running. | Retry after `Retry-After` seconds with the same key. |
| `RATE_LIMITED` | 429 | Your partner's request rate limit (or the token endpoint's) is exhausted. | Retry after `Retry-After` seconds. |
| `TOO_MANY_OPERATIONS` | 429 | Too many provisioning operations are in flight for your partner. `details` has `open` and `max`. | Retry after `Retry-After` seconds. |
| `INTERNAL_ERROR` | 500 | An unexpected server error. | Retry with backoff only if the request is a GET or carries an `Idempotency-Key`. Report persistent errors with the `request_id`. |
| `UNAVAILABLE` | 503 | A platform dependency is temporarily unavailable. | Retry after `Retry-After` seconds. |
| `PARTNER_NOT_READY` | 503 | Your partner credentials are still being provisioned. | Retry after `Retry-After` seconds. |
| `GPU_USAGE_UNAVAILABLE` | 503 | GPU usage could not be verified for a quota or plan change. | Retry after `Retry-After` seconds. |

## Operation failure codes

A failed operation carries `error.code` and `error.message` in the operation record and in the `customer.failed` and `project.failed` events. These codes appear there, not as HTTP errors:

| Code | Meaning | What to do |
|---|---|---|
| `PROVISIONING_FAILED` | A provisioning step failed. | Show the failure; contact support with the operation id if it repeats. |
| `PROVISIONING_TIMEOUT` | Provisioning did not finish within 15 minutes. | Contact support with the operation id. |
| `NO_ADDRESS_SPACE` | No network address range was left for the project. | Contact your operator. |
| `CUSTOMER_DELETING` | The customer was deleted while it was provisioning. | Expected after a delete; nothing to do. |
| `CUSTOMER_DELETED` | The customer no longer exists. | Expected after a delete; nothing to do. |

A failed step can also report `UPSTREAM_<status>`, for example `UPSTREAM_422`, when the platform refused a step with that HTTP status.
