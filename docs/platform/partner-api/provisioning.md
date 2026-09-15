import Tabs from '@theme/Tabs';
import TabItem from '@theme/TabItem';

# Provisioning customers

Creating a customer builds a Kube-DC organization (namespace, identity realm and owner user) and, by default, a first project. It takes minutes, so the create call returns at once with an operation you follow.

## Create a customer

`POST /customers` (scope `customers:write`):

```json
{
  "external_id": "crm-1042",
  "email": "jane@example.com",
  "first_name": "Jane",
  "last_name": "Doe",
  "company_name": "Example Ltd",
  "plan_id": "dev-pool",
  "project": {"name": "default", "egress_network_type": "cloud"}
}
```

| Field | Required | Notes |
|---|---|---|
| `email` | yes | The owner's email. |
| `external_id` | recommended | Your own id for this customer, up to 128 characters. It deduplicates creates (see below) and lets you address the customer as `ext:<external_id>`. |
| `name` | one of `name`, `company_name`, `external_id` | Preferred slug for the customer id. |
| `company_name` | | Used for the id when `name` is absent. |
| `first_name`, `last_name` | no | The owner's name, shown in the console. |
| `description` | no | Free text, up to 512 characters. |
| `plan_id` | no | Defaults to your partner default plan. It must be a plan you may assign; see [Plans, quota and capacity](./plans-quota-capacity.md). |
| `project` | no | The first project. Omit it for a project with your partner defaults (normally named `default`); send an object to choose `name` and `egress_network_type` (`cloud` for shared NAT egress, `public` for a dedicated public gateway IP); send `null` to create no project. |

Unknown request fields are ignored.

### The customer id

The response carries the customer id: `<your prefix>-<slug>`. The prefix is set on your partner account by the operator (by default, your partner name cut to 16 characters). The slug comes from `name`, else `company_name`, else `external_id`: lowercased, accents folded, other characters turned into `-`, and truncated. The id is 3 to 40 characters.

**Store the id you receive; do not compute it.** Alternatively, address the customer everywhere as `ext:<external_id>`.

If the id, or the namespace of the first project, is already taken on the platform, the create answers `409` with `ALREADY_EXISTS`. Send a different `name`.

### The response

A new customer answers `202 Accepted` with `Retry-After: 2`:

```json
{
  "status": "success",
  "data": {
    "customer": {"id": "acme-example-ltd", "external_id": "crm-1042", "status": "provisioning", "...": "..."},
    "operation": {"id": "op_5f0c2a9e1b7d4c3a8e6f1d20", "type": "customer.create", "state": "running", "phase": "organization", "progress": 0, "...": "..."}
  }
}
```

Other refusals on create: `403` with `LIMIT_REACHED` (your maximum number of customers), `PLAN_NOT_ALLOWED` or `GPU_PROFILE_NOT_ALLOWED`; `409` with `PARTNER_CAPACITY_EXCEEDED`, `CUSTOMER_CAPACITY_EXCEEDED` or `BUSY`; `429` with `TOO_MANY_OPERATIONS`. See [Plans, quota and capacity](./plans-quota-capacity.md).

## Follow the operation

Poll `GET /operations/{id}` until `state` is `succeeded` or `failed`. While the operation is `running`, the response carries `Retry-After: 2`; wait that long between polls. Reading the operation also drives provisioning forward, so keep polling even when you use webhooks.

An operation lists its `phases` in order, each `pending`, `running`, `succeeded` or `failed`, and reports the current `phase` and a `progress` percentage. Use them for a progress display:

| Phase | What happens |
|---|---|
| `organization` | The organization and its namespace are created. |
| `realm` | The customer's identity realm is set up. |
| `owner` | The owner user `admin` is created. |
| `project` | The first project and its network are created. Absent when the customer is created without a project. |
| `ready` | Everything is reconciled; the customer is `active`. |

`progress` is a fixed percentage per phase, so a stalled phase visibly stays put. Operations never report success early: `succeeded` means the organization is ready and the first project is reconciled.

When an operation fails, `error.code` says why: `PROVISIONING_FAILED`, `PROVISIONING_TIMEOUT` (after 15 minutes), `NO_ADDRESS_SPACE`, `CUSTOMER_DELETING`, `CUSTOMER_DELETED`, or `UPSTREAM_<status>` when the platform refused a step with that HTTP status. Tolerate codes you do not know.

Operation records are kept for 7 days; after that, `GET /operations/{id}` answers `404`. `GET /customers/{id}/operations` lists a customer's recorded operations, newest first.

### Webhooks instead of tight polling

Kube-DC sends `customer.ready` or `customer.failed` once, when the operation first reaches a terminal state. The event data carries `customer_id`, `operation_id`, `project`, `state` and `error`. A robust integration does both:

- react to the webhook to update your portal quickly;
- keep a background job that polls operations still running in your records, so a lost webhook never leaves a customer stuck in "provisioning" on your side.

In a web request, do not wait for provisioning. Return after the create call and let a background job or your own progress endpoint follow the operation.

**Example — Step 2: Create a customer.** Select the SDK language; the choice is kept across pages.

<Tabs groupId="partner-sdk-language">
<TabItem value="typescript" label="TypeScript" default>

Tutorial file `examples/tutorial/02-create-customer.ts` of the TypeScript SDK:

```ts
/**
 * Step 2 — Create a customer and follow its provisioning.
 *
 * `create` answers in about a second with an operation; provisioning takes minutes.
 * In a web request, return `created.operation.id` to your UI and poll from there, or
 * run `waitForOperation` in a background job as shown here.
 *
 * Retrying is safe. Every mutating call carries an Idempotency-Key, and the SDK resends the same
 * key on its own retries. Derive the key from your order, so that YOUR retries (a re-run job, a
 * restarted worker) are deduplicated too: the API answers them with the first response.
 */
import { KubeDC, isIdempotencyKeyReused, isLimitReached, isOperationFailed } from '@kube-dc/partner-sdk';

const kdc = new KubeDC({ apiKey: env('KUBEDC_API_KEY'), baseUrl: env('KUBEDC_API_URL') });

// An order from your shop or billing system.
const order = { id: 'ord-1042', crmId: 'crm-1042', email: 'jane@example.com', company: 'Example Ltd' };

try {
  const created = await kdc.customers.create(
    {
      external_id: order.crmId, // YOUR id for this customer: address it later as ext('crm-1042')
      email: order.email, // the owner; receives the welcome email
      company_name: order.company,
      // plan_id: 'dev-pool',   // optional: otherwise your partner default plan
    },
    {
      idempotencyKey: `${order.id}-create-customer`, // same order, same key: never a second tenant
      onMeta: (meta) => {
        if (meta.idempotentReplayed) console.log('This order was already submitted: showing the first answer.');
      },
    },
  );
  console.log(`Customer ${created.customer.id} is ${created.customer.status}`);

  // A create matched on an existing external_id (with a new key) may carry no running operation.
  if (created.operation?.state === 'running') {
    const op = await kdc.waitForOperation(created, {
      // organization -> realm -> owner -> project -> ready
      onPhase: (o) => console.log(`  ${o.phase.padEnd(12)} ${o.progress}%`),
    });
    console.log(`Ready: ${op.customer_id}`);
  }
} catch (err) {
  if (isLimitReached(err)) console.error('You have reached your maximum number of customers.');
  else if (isIdempotencyKeyReused(err)) console.error('This order key was already used for a different request.');
  else if (isOperationFailed(err)) console.error(`Provisioning failed: ${err.code} ${err.message}`);
  throw err;
}

function env(name: string): string {
  const value = process.env[name];
  if (!value) throw new Error(`Set ${name}`);
  return value;
}
```

</TabItem>
<TabItem value="php" label="PHP">

Tutorial file `examples/tutorial/02-create-customer.php` of the PHP SDK:

```php
<?php

/**
 * Step 2 — Create a customer and follow its provisioning.
 *
 * create() answers in about a second with an operation; provisioning takes minutes.
 * In a web request (or WHMCS CreateAccount), return after create() and poll the
 * operation from a cron job, or react to the customer.ready webhook (step 6).
 *
 * Retrying is safe. Every mutating call carries an Idempotency-Key, and the SDK resends the same
 * key on its own retries. Derive the key from your order, so that YOUR retries (a re-run cron,
 * WHMCS running CreateAccount again) are deduplicated too: the API answers with the first response.
 */

declare(strict_types=1);

require __DIR__ . '/../../vendor/autoload.php';

use KubeDC\Partner\Client;
use KubeDC\Partner\Exception\IdempotencyKeyReusedException;
use KubeDC\Partner\Exception\LimitReachedException;
use KubeDC\Partner\Exception\OperationFailedException;
use KubeDC\Partner\Model\Operation;

$kdc = new Client(['api_key' => (string) getenv('KUBEDC_API_KEY'), 'base_url' => (string) getenv('KUBEDC_API_URL')]);

$orderId = 'ord-1042';   // your order (in WHMCS: 'whmcs-' . $params['serviceid'])

try {
    $result = $kdc->customers()->create([
        'external_id' => 'crm-1042',        // YOUR id for this customer: address it later as 'ext:crm-1042'
        'email' => 'jane@example.com',      // the owner; receives the welcome email
        'company_name' => 'Example Ltd',
        // 'plan_id' => 'dev-pool',         // optional: otherwise your partner default plan
    ], idempotencyKey: $orderId . '-create-customer');   // same order, same key: never a second tenant

    if ($kdc->lastResponse()?->idempotentReplayed === true) {
        echo "This order was already submitted: showing the first answer.\n";
    }
    echo "Customer {$result->customer->id} is {$result->customer->status}\n";

    if ($result->operation !== null && !$result->operation->isTerminal()) {
        $op = $kdc->waitForOperation($result->operation, static function (Operation $op): void {
            printf("  %-12s %3d%%\n", $op->phase, $op->progress);   // organization -> realm -> owner -> project -> ready
        });
        echo "Ready: {$op->customerId}\n";
    }
} catch (LimitReachedException $e) {
    fwrite(STDERR, "You have reached your maximum number of customers.\n");
    exit(2);
} catch (IdempotencyKeyReusedException $e) {
    fwrite(STDERR, "This order key was already used for a different request.\n");
    exit(2);
} catch (OperationFailedException $e) {
    fwrite(STDERR, "Provisioning failed ({$e->errorCode}): {$e->getMessage()}\n");
    exit(3);
}
```

</TabItem>
</Tabs>

## Deduplicate with external_id

`POST /customers` is idempotent on `external_id`. If you repeat a create with an `external_id` that already belongs to one of your customers, nothing is created or changed, and you get `200` instead of `202`:

- `data.idempotent` is `true`;
- `data.customer` is the existing customer, including its projects and its current status (which may be `suspended` or `deleting`);
- `data.operation` is the most recent operation recorded for that customer, of any type and possibly finished, or `null` when none is retained.

Only the string checks run before the lookup: no other field is compared. A create is therefore never a way to update a customer, and a replay with different details still returns the original customer. Read the replayed customer's `status` rather than assuming provisioning just started.

This dedupe works even when you send no `Idempotency-Key` header. Send both; see [Idempotency and retries](./idempotency.md).

## Read customers

- `GET /customers/{id}` returns one customer with its projects.
- `GET /customers` lists your customers, ordered by id, with the filters `external_id` and `status`. Items in a list have an empty `projects` array. See [Lists and pagination](./pagination.md).

A customer's `status` is `provisioning`, `active`, `suspended` or `deleting`. `scheduled_deletion_at` is set while a scheduled deletion is pending, `plan_id` is `null` while the customer is on an explicit quota, and `console_url` is the customer's console address.

## Projects

The default project covers most customers. To add more:

- `POST /customers/{id}/projects` with a `name` (and optionally `egress_network_type`) answers `202` with an operation, exactly like a customer create. Its first three phases report `succeeded` at once, then `project` and `ready` run. On completion Kube-DC sends `project.ready` or `project.failed`.
- The project namespace is `<customer id>-<name>` and must fit in 63 characters.
- Refusals: `403` with `LIMIT_REACHED` (maximum projects per customer), `409` with `NOT_READY` (the customer is still provisioning), `CONFLICT` (the customer is being deleted) or `ALREADY_EXISTS`, and `429` with `TOO_MANY_OPERATIONS`.
- `GET /customers/{id}/projects` and `GET /customers/{id}/projects/{project}` read them. A project's status is `provisioning`, `active` or `deleting`.
- `DELETE /customers/{id}/projects/{project}` is irreversible. It is refused with `409` and `CONFLICT` while the project runs VMs or databases (`details` lists them), unless you pass `force=true`. It answers `202`; the project reads `deleting` until its teardown completes, and `project.deleted` is sent when the deletion is accepted.
