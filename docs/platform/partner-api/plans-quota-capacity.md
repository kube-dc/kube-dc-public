import Tabs from '@theme/Tabs';
import TabItem from '@theme/TabItem';

# Plans, quota and capacity

A customer's capacity comes from exactly one source: a **plan** or an explicit **quota**. Either way, what you grant is bounded by the **capacity pool** your operator set on your partner account.

## Plans

- `GET /catalog/plans` lists the plans you may assign: your partner allowlist, or every self-service plan when none is configured. Each plan has `id`, `display_name`, `description`, `price` (the monthly list price), `currency`, `resources` and `self_service`.
- `PUT /customers/{id}/plan` with `{"plan_id": "pro-pool"}` assigns a plan. It keeps the customer's existing add-ons, clears any explicit quota, and is synchronous (no operation): the cluster applies it on its next reconcile. A suspended customer stays suspended. Kube-DC sends `customer.plan_changed`.
- `GET /customers/{id}/plan` reports the `source` of the customer's capacity (`partner-quota`, `plan` or `none`), the `plan_id` and the add-ons, each with `addon_id` and `quantity`.

## Customer quota: PUT replaces the whole document

When you sell capacity with your own sliders, send the **absolute** totals the customer now has with `PUT /customers/{id}/quota`. There is no add or subtract arithmetic to get wrong.

```json
{
  "quota": {
    "cpu": "8",
    "memory": "32Gi",
    "storage": "500Gi",
    "pods": 60,
    "load_balancers": 2,
    "public_ips": 3,
    "object_storage": {"size": "1Ti", "buckets": 10},
    "gpu": {"nvidia-a100-10gb": {"devices": 2}},
    "template_plan": "dev-pool"
  },
  "expected_revision": 6
}
```

- **The document replaces the previous one.** A dimension you omit is enforced at zero (nothing granted), not kept from the previous document. Setting a quota also clears any plan: quota and plan are mutually exclusive.
- `cpu`, `memory`, `storage` and `object_storage.size` are Kubernetes quantities such as `"8"`, `"500m"` or `"32Gi"`. `pods`, `load_balancers`, `public_ips` and `object_storage.buckets` are integers up to 1,000,000.
- `gpu` maps a GPU profile id to a grant with `devices`, `shares`, `memory_mib` and `core_percent`. You may only grant profiles your partner account allows.
- `template_plan` never supplies amounts. It supplies scheduling policy (burst ratio and default container limits) and defaults to your partner default plan.
- Numbers are usable capacity: platform overhead is added on top, so `"8"` CPUs means 8 usable CPUs.
- You may also send the bare quota document, with `expected_revision` next to its dimensions.

**Optimistic concurrency.** `GET /customers/{id}/quota` returns the `configured` document with its `revision`. Send that revision back as `expected_revision`: if someone changed the quota in between, you get `409` with `CONFLICT`. Re-read the quota, re-apply your user's intent and send again. The read-only fields (`revision`, `updated_at`, `updated_by`) are ignored on input, so you can send the `configured` document back unchanged.

The quota view also reports `enforcement`, what the platform enforces right now (`suspended-floor`, `partner-quota`, `plan` or `unmanaged`; a suspended customer is held at the suspended floor whatever its source), and the `usage` last observed.

Validation refuses unknown fields, malformed quantities, non-integer counts and an empty document with `400` and `VALIDATION_ERROR`; `details` lists every problem. The cluster applies an accepted quota within seconds, and Kube-DC sends `customer.quota_changed`.

**Example — Step 5: Quota and capacity.** Select the SDK language; the choice is kept across pages.

<Tabs groupId="partner-sdk-language">
<TabItem value="typescript" label="TypeScript" default>

Tutorial file `examples/tutorial/05-quota-and-capacity.ts` of the TypeScript SDK:

```ts
/**
 * Step 5 — Size a customer within your capacity pool.
 *
 * The operator grants your partner account a pool. Read it to cap your portal's sliders,
 * then set the customer's quota and handle the refusals.
 */
import { KubeDC, ext, hasErrorCode, isCapacityExceeded } from '@kube-dc/partner-sdk';

const kdc = new KubeDC({ apiKey: env('KUBEDC_API_KEY'), baseUrl: env('KUBEDC_API_URL') });
const customerId = ext('crm-1042');

// What is left in your pool, per dimension (cpu, memory, object_storage, gpu.<profile>.memory_mib...).
// null means unlimited.
const capacity = await kdc.capacity.get();
for (const d of capacity.dimensions) {
  console.log(`${d.name}: ${d.allocated} of ${d.pool ?? 'unlimited'} ${d.unit}, available ${d.available ?? 'unlimited'}`);
}

// The current quota carries a revision that guards against concurrent edits.
const view = await kdc.customers.quota.get(customerId);

try {
  // The quota is absolute: dimensions you leave out are cleared, and any plan is removed.
  const result = await kdc.customers.quota.set(
    customerId,
    {
      cpu: '8',
      memory: '32Gi',
      storage: '500Gi',
      pods: 100,
      // gpu: { 'nvidia-a100-10gb': { devices: 1, memory_mib: 10240 } },   // profiles your partner may grant
    },
    { expectedRevision: view.configured?.revision ?? 0 },
  );
  console.log(result.message, result.warnings ?? []);
} catch (err) {
  // PARTNER_CAPACITY_EXCEEDED (your pool) or CUSTOMER_CAPACITY_EXCEEDED (the per-customer maximum).
  if (isCapacityExceeded(err)) console.error(`${err.code}: lower the request`, err.details);
  else if (hasErrorCode(err, 'CONFLICT')) console.error('The quota changed since you read it: reload and retry.');
  else if (hasErrorCode(err, 'BUSY')) console.error('Another capacity change is still in progress (the SDK already retried): retry shortly.');
  else throw err;
}

function env(name: string): string {
  const value = process.env[name];
  if (!value) throw new Error(`Set ${name}`);
  return value;
}
```

</TabItem>
<TabItem value="php" label="PHP">

Tutorial file `examples/tutorial/05-quota-and-capacity.php` of the PHP SDK:

```php
<?php

/**
 * Step 5 — Size a customer within your capacity pool.
 *
 * The operator grants your partner account a pool. Read it to cap your portal's sliders,
 * then set the customer's quota and handle the refusals.
 */

declare(strict_types=1);

require __DIR__ . '/../../vendor/autoload.php';

use KubeDC\Partner\Client;
use KubeDC\Partner\Exception\CapacityExceededException;
use KubeDC\Partner\Exception\ConflictException;

$kdc = new Client(['api_key' => (string) getenv('KUBEDC_API_KEY'), 'base_url' => (string) getenv('KUBEDC_API_URL')]);
$customerId = 'ext:crm-1042';

// What is left in your pool, per dimension (cpu, memory, object_storage, gpu.<profile>.memory_mib...).
// null means unlimited.
foreach ($kdc->capacity()->get()->dimensions as $d) {
    printf("%s: %s of %s %s, available %s\n", $d->name, $d->allocated, $d->pool ?? 'unlimited', $d->unit, $d->available ?? 'unlimited');
}

// The current quota carries a revision that guards against concurrent edits.
$view = $kdc->customers()->getQuota($customerId);

try {
    // The quota is absolute: dimensions you leave out are cleared, and any plan is removed.
    $result = $kdc->customers()->setQuota(
        $customerId,
        [
            'cpu' => '8',
            'memory' => '32Gi',
            'storage' => '500Gi',
            'pods' => 100,
            // 'gpu' => ['nvidia-a100-10gb' => ['devices' => 1, 'memory_mib' => 10240]],   // profiles your partner may grant
        ],
        expectedRevision: $view->configured->revision ?? 0,
    );
    echo $result->message . "\n";
} catch (CapacityExceededException $e) {
    // PARTNER_CAPACITY_EXCEEDED (your pool) or CUSTOMER_CAPACITY_EXCEEDED (the per-customer maximum).
    fwrite(STDERR, "{$e->errorCode}: lower the request " . json_encode($e->details) . "\n");
} catch (ConflictException $e) {
    // CONFLICT: the quota changed since you read it, reload and retry. (BUSY is retried by the SDK first.)
    fwrite(STDERR, "{$e->errorCode}: {$e->getMessage()}\n");
}
```

</TabItem>
</Tabs>

## Project caps: PATCH merges

A customer can split its quota between projects. `PATCH /customers/{id}/projects/{project}/quota` sets caps for `cpu`, `memory`, `storage` and `pods`:

- only the fields you send change; `null` or `""` removes a cap;
- a cap above what the customer holds is refused with `400` and `QUOTA_EXCEEDED`, and `details` lists each violation with `quota_key`, `requested` and `organization_hard`;
- `409` with `NOT_READY` means the project has no namespace yet;
- the response carries the project's resulting caps.

## Your capacity pool

Your operator bounds what you can hand out:

| Limit | Meaning |
|---|---|
| Capacity pool | A total per dimension across **all** your customers. Unset dimensions are unlimited. |
| Per-customer maximum | The same dimensions, capping any single customer. |
| Allowed plans | The plans you may assign. |
| Allowed GPU profiles | GPU profiles you may grant. None unless your operator lists them. |
| Maximum customers | Customers your partner account may hold. |
| Maximum projects per customer | Projects per customer. |
| Maximum open operations | Provisioning operations in flight at once. |

How the pool is counted:

- **Allocation, not usage.** A customer counts with what you granted it (its quota, or its plan plus add-ons), not with what it runs. Suspended customers count, so a resume never fails for capacity; customers in an irreversible teardown do not.
- **Only increases are checked.** If the operator lowers your pool below what you already granted, you can still shrink customers; you just cannot grow them.
- **Checks are atomic per partner.** Two concurrent changes cannot both take the last free capacity. The loser gets `409` with `BUSY`; retry after `Retry-After`.

### Showing what is left

`GET /capacity` (scope `customers:read`) returns, for each dimension, its `name`, `unit`, `allocated`, `pool`, `available` and `per_customer_max`; `null` means unlimited. Dimension names are `cpu`, `memory`, `storage`, `object_storage`, `pods`, `public_ips`, `load_balancers`, and `gpu.<profile>.devices`, `gpu.<profile>.shares`, `gpu.<profile>.memory_mib` and `gpu.<profile>.core_percent`. Units are `cores`, `bytes`, `count`, `MiB` and `percent`: convert to the quantities your sliders use (for example `memory` is in bytes, while the quota says `"32Gi"`).

The response also carries `customers` (`count` and `max`), `max_projects_per_customer`, `open_operations` (`count` and `max`), `allowed_plans` and `allowed_gpu_profiles`; a `max` of 0 means unlimited.

To cap a slider for one customer, the most it can hold in a dimension is the smaller of `per_customer_max` and `available` plus what that customer already holds in that dimension.

### Refusals and what they carry

| Code | Status | Details | Show in your portal |
|---|---|---|---|
| `PARTNER_CAPACITY_EXCEEDED` | 409 | One entry per dimension: `dimension`, `unit`, `pool`, `allocated`, `requested`, `available` (what is left for this customer). | "Only *available* left", and lower the slider. |
| `CUSTOMER_CAPACITY_EXCEEDED` | 409 | One entry per dimension: `dimension`, `unit`, `max`, `requested`. | The per-customer maximum. |
| `PLAN_NOT_ALLOWED` | 403 | | Hide plans that `GET /catalog/plans` does not list. |
| `GPU_PROFILE_NOT_ALLOWED` | 403 | The profile ids you may not grant. | Offer only `allowed_gpu_profiles`. |
| `GPU_ENTITLEMENT_REDUCTION_BLOCKED` | 409 | The GPU grants that running workloads still use (`profile_id`, `field`, `used`, `requested`); for a plan change, also the `workloads` to stop. | Ask the customer to stop those workloads first. |
| `GPU_USAGE_UNAVAILABLE` | 503 | | GPU usage could not be verified; retry after `Retry-After`. |
| `LIMIT_REACHED` | 403 | For projects: `max` and `current`. | The customer or project limit is reached. |
| `TOO_MANY_OPERATIONS` | 429 | `open` and `max`. | Queue the request and retry after `Retry-After`. |
| `BUSY` | 409 | | Retry after `Retry-After`. |

Capacity is checked when you create a customer, set a quota and assign a plan.
