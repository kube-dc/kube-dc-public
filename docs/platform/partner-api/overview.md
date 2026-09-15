# Partner API overview

The Kube-DC Partner API lets your platform sell Kube-DC cloud capacity from your own customer portal. From your backend you can:

- create a customer and follow its provisioning until it is ready;
- size the customer with a named plan or with absolute quota numbers, within the capacity your Kube-DC operator granted you;
- send the customer's browser into the Kube-DC console with one click, without the customer ever handling a Kube-DC password;
- suspend, resume and delete the customer from your billing events;
- export usage for invoicing;
- receive signed webhooks when something changes.

Every call is server to server. Your portal's backend talks to the Partner API; your customers' browsers only ever receive a single-use login redirect.

## Start here

Begin with the [API tutorial (HTTP)](./http-tutorial.md). It walks through a complete integration with plain HTTP requests, `curl` and `jq`: getting a token, creating and sizing a customer, console login, the lifecycle, lists, usage, webhook signature checks and error handling. It needs no SDK. The other chapters hold the full rules for each part.

## Resource model

| Resource | What it is |
|---|---|
| Customer | A Kube-DC organization: its own namespace, identity realm and owner user, plus (unless you opt out) a default project. |
| Project | An isolated network and namespace inside a customer, where workloads run. |
| User | A person inside a customer who can open the console. Every customer has an owner user named `admin`. |
| Operation | The progress record of an asynchronous provisioning step, such as creating a customer or a project. |
| Webhook event | A signed HTTP POST to your receiver when a customer or project changes. |
| Plan or quota | What a customer may use: a named Kube-DC plan, or an explicit quota document you set. |
| Capacity pool | What your partner account may grant across all of its customers. |

Some rules hold everywhere:

- **You only see your own customers.** A customer created by another partner, or by anyone else, answers exactly like one that does not exist: `404` with `NOT_FOUND`.
- **A customer id is `<your prefix>-<slug>`.** Wherever a path takes a customer `{id}`, you can pass `ext:<external_id>` instead to address the customer by your own id. See [Provisioning customers](./provisioning.md).
- **Slow work is asynchronous.** Creating a customer or a project answers `202 Accepted` with an operation to poll. Deletions are asynchronous too.

## Base URL

All paths in this guide are relative to:

```text
https://<your-kube-dc-backend>/api/public/v1
```

Your Kube-DC operator tells you the backend origin, together with your API key. For example, `GET /auth/whoami` means a GET request to `https://<your-kube-dc-backend>/api/public/v1/auth/whoami`.

The contract is published as OpenAPI 3.1: `GET /openapi.yaml` (also `GET /openapi.json`) returns it without authentication, and a browsable rendering is served at `/docs/` under the same base URL. `GET /health` is an unauthenticated liveness check.

## Conventions

- **JSON everywhere.** Field names are snake_case in every request and response, for example `external_id`, `public_ips`, `load_balancers`, `object_storage` and `memory_mib`.
- **One response envelope.** Success is `{"status": "success", "data": ...}`. Errors are `{"status": "error", "error": {"code", "message", "request_id", "details"}}`. The only exception is the token endpoint, which answers in the bare OAuth2 shape. See [Errors reference](./errors.md).
- **Tracing.** Every response carries `X-Request-Id`; send your own `X-Request-Id` header and it is echoed back. Quote it when you contact support. Responses also carry `X-KubeDC-Api-Version: v1`.
- **Compatibility.** Within v1, changes are additive only: new endpoints, new optional request fields, new response fields, new error codes, new events and new values in extensible enums. Ignore response fields you do not know, and tolerate codes and events you do not handle. Breaking changes ship under a new version path with at least six months of overlap.

## SDK or raw HTTP

Your operator can hand you two SDKs, generated from the same contract:

| SDK | Runtime |
|---|---|
| TypeScript / Node.js | Node.js 18 or later |
| PHP | PHP 8.1 or later |

The SDKs exchange and cache access tokens, send an `Idempotency-Key` on every mutating call and retry transient failures with backoff, poll operations, walk paginated lists, verify webhook signatures and raise typed errors.

You do not need an SDK. Everything in this guide is the wire contract, so any HTTP client works, and the [API tutorial (HTTP)](./http-tutorial.md) shows every step without one. The SDK example tabs in the other chapters are optional conveniences for partners who received the SDKs from their operator; they show the SDK tutorial files in the language you select. If you integrate over raw HTTP, pay particular attention to [Authentication](./authentication.md), [Idempotency and retries](./idempotency.md) and [Webhooks](./webhooks.md): those are the parts the SDKs otherwise handle for you.

## What you receive from your operator

- the backend origin (the base URL above);
- one or more API keys, each shown once;
- the scopes and limits of your partner account (you can read them back with `GET /auth/whoami` and `GET /capacity`);
- the SDK packages, if your operator distributes them;
- your platform operator's support contact for incidents (see [Going live checklist](./going-live.md)).
