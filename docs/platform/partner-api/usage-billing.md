# Usage and billing export

Two endpoints return usage, both with scope `usage:read`:

- `GET /customers/{id}/usage` for one customer;
- `GET /usage` for every one of your customers, a page at a time, ordered by customer id, for invoicing runs.

## Current usage

`current` is what each customer uses against its limits, as last observed by the platform. For `cpu`, `memory`, `storage` and `pods` it gives `used` and `limit` as Kubernetes quantities, plus a `gpu` entry per GPU profile (`devices`, `shares`, `memory_mib`, `core_percent`, each with `used` and `limit`), and `observed_at` says when the figures were refreshed. It suits dashboards and "you are using 7 of 32 GiB" displays. It is not a billing measure.

## Billable window

Pass `from`, `to` or `granularity` to also get a `window`: the reserved capacity (the resources the customer's workloads request) integrated over time.

| Parameter | Default | Notes |
|---|---|---|
| `from` | 30 days before `to` | RFC 3339 timestamp. Must be before `to`. |
| `to` | now | RFC 3339 timestamp. |
| `granularity` | `hour` per customer, `day` for all customers | `hour` or `day`. `hour` is limited to windows of up to 31 days. |

The window carries `from`, `to`, `granularity` and three totals: `cpu_hours`, `memory_gib_hours` and `storage_gib_hours`. The longest window the platform answers is `limits.max_usage_window_days` in `GET /auth/whoami` (the per-customer response also repeats it as `max_window_days`). A longer window, bad timestamps or an unknown granularity answer `400` with `VALIDATION_ERROR`.

Samples are attributed to the immutable customer identity, including projects removed during the window. Reusing a customer name starts a new history. Periods before ownership-attributed metrics were enabled, and gaps in those metrics, are unavailable; time before this customer was created contributes zero. Hourly and daily sampling approximate reservations between observations.

**Unmeasurable is never zero.** When a value cannot be measured, it is `null` and that window carries `partial: true` with a `partial_reason`. With `GET /usage`, check `window.partial` on every entry.

## Turning usage into invoices

1. **Pick the billing period** as a half-open window aligned to your billing cycle, in UTC, for example `from=2026-08-01T00:00:00Z` and `to=2026-09-01T00:00:00Z`.
2. **Fetch it once per run** with `GET /usage` and `granularity=day`, walking every page as described in [Lists and pagination](./pagination.md). Keep `from`, `to` and `granularity` identical on every page. Each entry carries `customer_id` and your `external_id`, so you can match it to your own account without a lookup table.
3. **Price it with your own rates**: `cpu_hours` times your price per CPU-hour, `memory_gib_hours` times your price per GiB-hour of memory, `storage_gib_hours` times your price per GiB-hour of storage. The `price` in the plan catalog is Kube-DC's list price, not your price.
4. **Never invoice a partial window as zero.** Hold that customer's invoice and fetch the window again later, or fall back to your quota-based price for that customer.
5. **Keep the evidence.** Store the request window and the returned figures with the invoice so you can answer disputes.

If you sell fixed capacity rather than pay-as-you-go, bill the quota you set (see [Plans, quota and capacity](./plans-quota-capacity.md)) and use usage only to show customers how much of it they use.
