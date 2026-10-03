# PAYG billing (optional)

Pay-as-you-go (PAYG) billing is an optional, per-installation feature. It is
**off by default**. Turn it on only on an installation that sells pay as you go
from Kube-DC itself. An installation that bills its customers somewhere else,
such as a hosting partner's own billing system, a plan-based provider, or no
billing at all, keeps it off.

The switch is one Helm value on the `kube-dc` chart:

```yaml
billing:
  payg:
    enabled: true   # or false; unset by default, which means off
```

## Upgrading an installation that already uses the billing service

Before this switch existed, setting `billing.service.url` (or
`billing.service.caIssuer.name` or `billing.service.caSecretName`) was enough
to wire the backend to the billing service. With the switch unset, an upgrade
would drop that wiring. To prevent a silent loss, the chart refuses to render
any of those values while `billing.payg.enabled` is unset:

```text
billing.service.url is set: set billing.payg.enabled explicitly — true to keep PAYG, false to turn it off
```

Set `billing.payg.enabled: true` to keep PAYG as it was, or `false` to turn it
off. With `false`, the chart ignores the `billing.service` values.

## What the switch controls

| | PAYG off (default) | PAYG on |
|---|---|---|
| Backend link to the billing service (`BILLING_SERVICE_URL`, projected token, CA) | Not rendered, even if `billing.service.url` is set | Rendered |
| Admin API: billing policies, financial holds, settlement periods, rate cards | Routes do not exist (404) | Available |
| Tenant API: managed-service price quotes, budget and forecast, account restrictions, PAYG usage estimate | Routes do not exist (404) | Available |
| `GET /api/system/features` and the admin console's features | `payg: false` | `payg: true` |
| Admin console: **PAYG prices** and **Settlement periods** pages, the organization billing tab's holds card and billing-policy action | Hidden | Shown |
| Tenant console: price panel when creating or resizing a managed service | Hidden, and no quote is requested | Shown |
| Self-service PAYG at signup (`billing.policy.selfService.enabled`) | Refused: the chart fails to render | Allowed |

Some parts ship regardless of the switch. None of them bills anyone:

- The `OrganizationBillingPolicy` CRD. It is inert: an organization without a
  policy is billed exactly as it was before policies existed.
- The backend's read-only Role for billing policies.
- The optional admission policy that protects them
  (`billing.policy.admissionEnabled`, off by default).

The plan-based billing providers (`billing.provider`: `none`, `stripe` or
`whmcs`) and the `billing-plans` ConfigMap do not depend on this switch. See
[Billing plans & resource quota configuration](billing-plans-configuration.md).

## Turn PAYG on

Before you enable the switch, make sure these are in place:

1. The metering deployment runs its billing API. By default the chart expects
   `https://kube-dc-metering.monitoring.svc.cluster.local:8443/internal/billing/v1`.
2. A CA issuer signed the billing API's serving certificate. By default the
   chart expects a cert-manager `ClusterIssuer` named
   `kube-dc-billing-ca-issuer`. The chart asks it for a certificate in the
   release namespace, and the backend trusts that certificate's `ca.crt`. The
   issuer must be a real CA issuer, not a `selfSigned` one.
3. The billing API accepts the backend as a caller. Add the backend's service
   account, for example `system:serviceaccount:kube-dc:kube-dc-backend`, to the
   billing API's `BILLING_API_ALLOWED_CALLERS`.

Then set the switch:

```yaml
billing:
  payg:
    enabled: true
```

With `billing.service.url` left empty, the chart uses the in-cluster defaults
above. If the billing service runs somewhere else, set `billing.service.url`
and name its CA with `billing.service.caIssuer.name`, or with
`billing.service.caSecretName` if a secret in the release namespace already
holds it.

Self-service PAYG at signup is a separate, second switch under
`billing.policy.selfService`, and it needs `billing.payg.enabled: true`. Read
the comments on those values in the chart's `values.yaml` before you turn it
on. A PAYG organization has no plan, so its ceiling comes only from its billing
policy.

## Check the result

Render the chart with your values and confirm the backend's billing wiring:

```bash
helm template kube-dc charts/kube-dc -f my-values.yaml \
  | grep -A1 'name: BILLING_SERVICE_URL'
```

The command prints nothing when PAYG is off. On a running installation,
`GET /api/system/features` reports `"payg": true` or `"payg": false`.

## Turn PAYG off

Set `billing.payg.enabled: false`, and set
`billing.policy.selfService.enabled: false` in the same change. The chart
refuses self-service PAYG without PAYG.

The backend then loses its link to the billing service, the PAYG routes return
404, and both consoles hide the PAYG pages and price panels. The chart does not
delete existing `OrganizationBillingPolicy` objects, and with PAYG off the
admin console can no longer change them. Move organizations off their billing
policies before you turn PAYG off.
