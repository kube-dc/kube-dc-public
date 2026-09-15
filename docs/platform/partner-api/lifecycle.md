import Tabs from '@theme/Tabs';
import TabItem from '@theme/TabItem';

# Customer lifecycle

Drive a customer's lifecycle from your billing events:

| Your event | Call | Result |
|---|---|---|
| Invoice overdue | `POST /customers/{id}/suspend` | `suspended`; workloads keep running unless you ask to stop them. |
| Invoice paid | `POST /customers/{id}/resume` | `active` again; also cancels a pending scheduled deletion. |
| Subscription cancelled | `DELETE /customers/{id}` | Deletion scheduled after a grace period; recoverable until then. |
| Account closed for good | `DELETE /customers/{id}?immediate=true` | Irreversible teardown starts now. |

**Example — Step 4: Suspend, resume and delete.** Select the SDK language; the choice is kept across pages.

<Tabs groupId="partner-sdk-language">
<TabItem value="typescript" label="TypeScript" default>

Tutorial file `examples/tutorial/04-lifecycle.ts` of the TypeScript SDK:

```ts
/**
 * Step 4 — Suspend, resume and delete a customer from your billing events.
 *
 * Billing systems deliver events more than once. Key each call by the event that caused it:
 * a repeated event then gets the first answer back instead of acting twice.
 */
import { KubeDC, ext, isConflict } from '@kube-dc/partner-sdk';

const kdc = new KubeDC({ apiKey: env('KUBEDC_API_KEY'), baseUrl: env('KUBEDC_API_URL') });
const customerId = ext('crm-1042');

// Invoice overdue: suspend. Workloads keep running unless you pass stop_workloads: true.
const suspended = await kdc.customers.suspend(
  customerId,
  { reason: 'Invoice 2026-0912 overdue' },
  { idempotencyKey: 'inv-2026-0912-overdue' },
);
console.log(`Suspended at ${suspended.suspended_at}; workloads running: ${suspended.workloads_running}`);

// Paid: resume. Resume also cancels a scheduled deletion.
const resumed = await kdc.customers.resume(customerId, { idempotencyKey: 'inv-2026-0912-paid' });
console.log(`Customer is ${resumed.state}`);

// Cancelled: schedule the deletion. Service stops now; the teardown runs after graceDays
// (0-90, default 7). Until then, resume() brings the customer back.
const deletion = await kdc.customers.delete(customerId, { graceDays: 14 }, { idempotencyKey: 'sub-77-cancelled' });
console.log(`${deletion.message} Teardown at ${deletion.scheduled_deletion_at}; recoverable: ${deletion.recoverable}`);

// Nightly reconciliation with your billing system: every suspended customer, across all pages.
// The iterator requests page after page (100 at a time) until the list is exhausted.
for await (const customer of kdc.customers.list({ status: 'suspended', limit: 100 })) {
  console.log(`${customer.id} (${customer.external_id ?? 'no external id'}): deletion ${customer.scheduled_deletion_at ?? 'not scheduled'}`);
}

/** Irreversible teardown now. Refused with 409 while VMs or databases run, unless force is true. */
export async function deleteImmediately(id: string, force = false): Promise<void> {
  try {
    await kdc.customers.delete(id, { immediate: true, force });
  } catch (err) {
    if (isConflict(err)) console.error('Still running, stop these first:', err.details);
    throw err;
  }
}

function env(name: string): string {
  const value = process.env[name];
  if (!value) throw new Error(`Set ${name}`);
  return value;
}
```

</TabItem>
<TabItem value="php" label="PHP">

Tutorial file `examples/tutorial/04-lifecycle.php` of the PHP SDK:

```php
<?php

/**
 * Step 4 — Suspend, resume and delete a customer from your billing events
 * (WHMCS: SuspendAccount, UnsuspendAccount, TerminateAccount).
 *
 * Billing systems deliver events more than once. Key each call by the event that caused it:
 * a repeated event then gets the first answer back instead of acting twice.
 */

declare(strict_types=1);

require __DIR__ . '/../../vendor/autoload.php';

use KubeDC\Partner\Client;
use KubeDC\Partner\Exception\ConflictException;

$kdc = new Client(['api_key' => (string) getenv('KUBEDC_API_KEY'), 'base_url' => (string) getenv('KUBEDC_API_URL')]);
$customerId = 'ext:crm-1042';

// Invoice overdue: suspend. Workloads keep running unless $stopWorkloads is true.
$suspended = $kdc->customers()->suspend($customerId, reason: 'Invoice 2026-0912 overdue', idempotencyKey: 'inv-2026-0912-overdue');
echo "Suspended at {$suspended->suspendedAt?->format(DATE_ATOM)}\n";

// Paid: resume. Resume also cancels a scheduled deletion.
$resumed = $kdc->customers()->resume($customerId, idempotencyKey: 'inv-2026-0912-paid');
echo "Customer is {$resumed->state}\n";

// Cancelled: schedule the deletion. Service stops now; teardown after graceDays (0-90, default 7).
// Until then, resume() brings the customer back.
$deletion = $kdc->customers()->delete($customerId, graceDays: 14, idempotencyKey: 'sub-77-cancelled');
echo "{$deletion->message} Teardown at {$deletion->scheduledDeletionAt?->format(DATE_ATOM)}\n";

// Nightly reconciliation with your billing system: every suspended customer, across all pages.
// The generator requests page after page (100 at a time) until the list is exhausted.
foreach ($kdc->customers()->list(status: 'suspended', limit: 100) as $customer) {
    $teardown = $customer->scheduledDeletionAt?->format(DATE_ATOM) ?? 'not scheduled';
    echo "{$customer->id} (" . ($customer->externalId ?? 'no external id') . "): deletion {$teardown}\n";
}

/** Irreversible teardown now. Refused while VMs or databases run, unless $force is true. */
function deleteImmediately(Client $kdc, string $id, bool $force = false): void
{
    try {
        $kdc->customers()->delete($id, immediate: true, force: $force);
    } catch (ConflictException $e) {
        fwrite(STDERR, 'Still running, stop these first: ' . json_encode($e->details) . "\n");
        throw $e;
    }
}
```

</TabItem>
</Tabs>

## Suspend

`POST /customers/{id}/suspend` takes an optional body:

- `reason`: recorded on the customer, truncated to 256 characters;
- `stop_workloads`: `false` by default, so workloads keep running (the shape for "payment is late"); only `true` scales them down.

The response carries `state` (`suspended`), `suspended_at` and `workloads_running`. The customer reports `suspended` either way, and its enforced quota drops to the suspended floor. Kube-DC sends `customer.suspended`.

## Resume

`POST /customers/{id}/resume` brings the customer back to `active` and restores stopped workloads. If a scheduled deletion was pending, it is cancelled and `cancelled_scheduled_deletion` carries its due date. Kube-DC sends `customer.resumed`.

Resume is refused with `409` and `CONFLICT` when:

- an irreversible teardown is in progress; or
- the customer was suspended by the platform operator rather than through the Partner API. You can only lift holds that a partner placed. Contact your operator about an operator hold.

## Scheduled deletion

`DELETE /customers/{id}` without `immediate` **schedules** the deletion and always answers `202` at once:

- service is cancelled now: workloads stop and data is retained;
- teardown becomes due after `grace_days` (an integer from 0 to 90, default 7), passed as a query parameter or in a JSON body; if you send both, they must be equal;
- the customer reports `deleting`, with `scheduled_deletion_at` set, and the response says `recoverable: true`;
- Kube-DC sends `customer.deletion_scheduled`;
- `POST /customers/{id}/resume` before the due date cancels the deletion;
- repeating the DELETE re-schedules the teardown from now.

When the grace period runs out, teardown starts on its own shortly after the due time.

## Immediate teardown

`DELETE /customers/{id}?immediate=true` starts an irreversible teardown now and answers `202`:

- it is refused with `409` and `CONFLICT` while the customer runs VMs or databases; `details` lists them. Stop them, or pass `force=true` as well;
- the customer reports `deleting` until it is gone, then `404`. Treat that `404` as the successful end of a deletion you requested;
- Kube-DC sends `customer.deleted` once, when the organization is removed. Its `immediate` field is `true` here and `false` when a scheduled deletion's grace period ran out;
- while a teardown is in progress, any further DELETE is a no-op `202`.

Deleting a customer removes its projects and users with it. To remove a single project, see [Provisioning customers](./provisioning.md).
