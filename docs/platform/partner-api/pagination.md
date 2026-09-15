# Lists and pagination

Every list endpoint answers with the same envelope:

```json
{"status": "success", "data": [{"id": "acme-example-ltd", "...": "..."}], "pagination": {"next_cursor": "eyJsIjoiY3VzdG9tZXJzIiwiayI6ImFjbWUtZXhhbXBsZS1sdGQifQ", "limit": 50}}
```

`data` is the array of items; `pagination.next_cursor` is the cursor for the next page, or `null` on the last page; `pagination.limit` is the page size that was applied.

## Parameters

| Parameter | Notes |
|---|---|
| `limit` | Page size, an integer from 1 to 200. Default 50. Anything else answers `400` with `VALIDATION_ERROR`. |
| `cursor` | The `next_cursor` from the previous page, passed back unchanged. |

## Walking a list

1. Request the first page without `cursor`.
2. Process `data`.
3. If `pagination.next_cursor` is `null`, stop. Otherwise request the next page with `cursor` set to it, keeping every other query parameter the same.

Rules that make this safe:

- **Cursors are opaque.** Do not decode, build or modify them. A cursor belongs to the list that issued it; passing it to another list answers `400` with `VALIDATION_ERROR`.
- **Filters apply before paging.** Every page except the last is full, and `next_cursor` is `null` exactly when no further matches exist.
- **Order is stable.** Each list follows a fixed order and a cursor names the last item of the previous page rather than an offset, so items created or deleted while you walk the list do not make you skip or repeat others.

## List endpoints

| Endpoint | Order | Notes |
|---|---|---|
| `GET /customers` | Customer id | Filters `external_id` and `status`. Items do not include `projects`; `GET /customers/{id}` does. |
| `GET /customers/{id}/operations` | Newest first | Reading a page also advances the provisioning of the operations on it. Records are kept for 7 days. |
| `GET /customers/{id}/projects` | Project name | |
| `GET /customers/{id}/users` | Username | Includes the owner `admin`. At most the first 1,000 users of a customer are listable. |
| `GET /catalog/plans` | Plan id | Only plans you may assign. |
| `GET /webhook/deliveries` | Newest first | Only the 50 most recent deliveries are retained. |
| `GET /usage` | Customer id | Usage windows are computed for the customers on each page; see [Usage and billing export](./usage-billing.md). |

The SDKs walk pages for you: iterate over a list call and pages are fetched lazily, or request a single page and pass its cursor yourself.
