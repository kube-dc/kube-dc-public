# PAYG billing (optional)

This page is for operators who decide whether an installation uses Kube-DC
pay-as-you-go (PAYG) billing. After you read it, you can turn PAYG on when you
install, check the result, and turn it off. PAYG is an optional,
per-installation feature, and it is **off by default**. Enable it when you use
Kube-DC to price usage and prepare PAYG statements. Keep it off when you use
only fixed plans, quotas, or raw usage feeds.

## Billing provider rule

PAYG runs only on an installation that bills through Kube-DC. Set the billing
provider to `stripe` or `whmcs`. In a Fleet installation, the provider is
`BILLING_PROVIDER` in `clusters/<name>/cluster-config.env`, where `<name>` is
the cluster name. The Fleet passes it to the chart value `billing.provider`.
`kube-dc bootstrap init --payg` refuses an unset provider, `none`, and an
installation billed by a partner.

## Turn PAYG on when you install

To turn PAYG on, add `--payg` and the billing provider to your
`kube-dc bootstrap init` command:

```bash
kube-dc bootstrap init <init-flags> \
  --set BILLING_PROVIDER=stripe \
  --payg \
  --dry-run
```

`<init-flags>` are the other flags of your installation. See
[Run kube-dc bootstrap init](installation-guide.md#33-run-kube-dc-bootstrap-init).
Use `--set BILLING_PROVIDER=whmcs` for a WHMCS installation. Review the plan,
then run the same command with `--yes` in place of `--dry-run`.

The CLI checks the provider rule when it validates the inputs. It checks the
rule again against the generated `cluster-config.env` before it writes any
PAYG file.

With `--payg`, the scaffold writes these files and values:

- `PAYG_ENABLED=true`, a generated `PAYG_INSTALLATION_UID`,
  `PAYG_INSTALLATION_BINDING=<name>/<domain>`, and
  `PAYG_PRODUCTS_REVISION=<name>-<yyyymmdd>` in
  `clusters/<name>/cluster-config.env`. The Fleet passes `PAYG_ENABLED` to the
  chart value `billing.payg.enabled`.
- The collector image, `PAYG_METERING_IMAGE`, from the release pins. It must
  be pinned by digest. To override it, pass
  `--set PAYG_METERING_IMAGE=<repository>:<tag>@sha256:<digest>`.
- `clusters/<name>/payg.yaml`, the Flux Kustomization for the shared
  `platform/payg` bundle, listed in `clusters/<name>/kustomization.yaml`.
- `clusters/<name>/metering-db.enc.yaml`, the collector's database login,
  encrypted with SOPS.
- A patch in `clusters/<name>/platform.yaml` that adds the collector's role to
  the platform CNPG cluster's managed roles. Roles that the cluster already
  declares stay in place.

Without `--payg`, `bootstrap init` writes no PAYG switch, identity, Flux layer,
database login, or role. The installation runs no usage collector and no
billing service, and the consoles show no PAYG screens. The release pins still
add `PAYG_METERING_IMAGE` to every `cluster-config.env`. Nothing reads that key
until a `payg.yaml` layer exists, so it has no effect.

Region and trusted producers are per-installation settings. Set `PAYG_REGION`
and `PAYG_DECLARED_PRODUCERS` with `--set`, or later in `cluster-config.env`.
The `platform/payg/README.md` file in your Fleet repository lists every
variable.

### Installation UID

The installation UID is part of the identity of every usage record. The CLI
generates it one time, binds it to the cluster name and domain, and never changes
it. Do not copy it from another installation. The CLI refuses
`--set PAYG_INSTALLATION_UID`. A `--config` file taken from another cluster
does not carry PAYG over, so pass `--payg` again.

A re-run refuses an overlay whose UID is bound to another installation. It
also refuses a UID that appears in another cluster directory of the Fleet.
These checks apply with or without `--payg`.

### Re-runs and resumes

`--payg` applies only when `init` creates the cluster overlay. If a run fails during the PAYG step, the CLI
removes the partial overlay, and the next run starts clean. For example, the
step fails when SOPS cannot reach an age key.

Every resume checks each PAYG piece that the overlay contains, with or without
`--payg`. The resume stops before anything is pushed in these cases:

- The billing provider is not `stripe` or `whmcs`.
- The database login is not one fully encrypted Secret.
- `payg.yaml` differs from the file that the CLI generates. Do not customize
  it: an extra `substitute` or `substituteFrom` could override the
  installation UID or the image.
- The role patch in `platform.yaml` differs from the one that the CLI
  generates.
- Another patch or replacement might select the CNPG cluster, in
  `platform.yaml` or in the shared `platform/` tree.
- A patch in the cluster's `kustomization.yaml`, or in a kustomization that it
  includes, might select `payg.yaml`, the `platform` Kustomization, the
  database login, or the `cluster-config` ConfigMap.
- The cluster build uses `components`, `transformers`, `validators`,
  `replacements`, `generators`, `namespace`, or a name prefix or suffix. The
  generated scaffold uses none of them.
- A source other than the scaffold's own entry generates `cluster-config`, or
  another generator uses its name or the database login's name.
- Another Flux Kustomization builds `./platform/payg`, or another object in
  the cluster directory reuses the name of `payg`, `platform`, the login, or
  `cluster-config`.

These checks match exactly and refuse anything else. To merge an extra change
to those objects, edit them by hand and follow `platform/payg/README.md`.

The checks protect the CLI's own scaffold from mistakes: a copied overlay, a
re-run, or a hand edit that would override the installation UID, the image, or
the database login. They are not a security boundary. Anyone who can write to
the Fleet repository can change the deployed objects directly. You keep each
installation UID unique. As a second line of defense, the billing service
refuses data from a data plane whose installation UID is not its own.

`--payg` on a resume only finishes a PAYG scaffold that is partly present. It
refuses an overlay that has no PAYG at all. It also refuses PAYG that was set
up by hand without an installation UID from the CLI, because finishing it would
give a running installation a second identity.

## Turn PAYG on for an installed cluster

`bootstrap init --payg` does not add PAYG to an installed cluster. To turn PAYG
on for an installed cluster, follow the steps in `platform/payg/README.md` in
your Fleet repository. The billing provider rule applies.

## Check the result

After Flux reconciles the `payg` Kustomization, the collector logs
`billing schema migrated` and `billing API serving addr=:8443`. To check, run
this command:

```bash
kubectl -n monitoring logs deploy/kube-dc-metering | grep -E 'schema migrated|billing API serving'
```

On a running installation, `GET /api/system/features` reports `"payg": true`
or `"payg": false`.

## What the switch controls

The chart value `billing.payg.enabled` controls these parts of the platform:

| Part | PAYG off (default) | PAYG on |
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
`whmcs`) and the `billing-plans` ConfigMap do not depend on this switch. PAYG
depends on the provider, as the [billing provider rule](#billing-provider-rule)
states. See
[Billing plans & resource quota configuration](billing-plans-configuration.md).

## Chart values for an installation without the Fleet

In a Fleet installation, set PAYG with the CLI and `cluster-config.env`, as
this page describes. If you install the `kube-dc` chart without the Fleet, the
switch is one Helm value on the chart. Set `billing.provider` to `stripe` or
`whmcs` in the same values file:

```yaml
billing:
  provider: stripe
  payg:
    enabled: true   # or false; unset by default, which means off
```

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

With `billing.service.url` left empty, the chart uses the in-cluster defaults
in the preceding list. If the billing service runs somewhere else, set
`billing.service.url` and name its CA with `billing.service.caIssuer.name`, or
with `billing.service.caSecretName` if a secret in the release namespace
already holds it.

Self-service PAYG at signup is a separate, second switch under
`billing.policy.selfService`, and it needs `billing.payg.enabled: true`. Read
the comments on those values in the chart's `values.yaml` before you turn it
on. A PAYG organization has no plan, so its ceiling comes only from its billing
policy.

To check the backend's billing wiring, render the chart with your values:

```bash
helm template kube-dc charts/kube-dc -f my-values.yaml \
  | grep -A1 'name: BILLING_SERVICE_URL'
```

The command prints nothing when PAYG is off.

### Upgrade an installation that already uses the billing service

Before this switch existed, setting `billing.service.url` (or
`billing.service.caIssuer.name` or `billing.service.caSecretName`) was enough
to wire the backend to the billing service. With the switch unset, an upgrade
would drop that wiring. To prevent a silent loss, the chart refuses to render
any of those values while `billing.payg.enabled` is unset. It prints this
error:

```text
billing.service.url is set: set billing.payg.enabled explicitly — true to keep PAYG, false to turn it off (see docs/platform/payg-billing.md)
```

Set `billing.payg.enabled: true` to keep PAYG as it was, or `false` to turn it
off. With `false`, the chart ignores the `billing.service` values.

## Turn PAYG off

To turn PAYG off, set the switch to false. In a Fleet installation, set
`PAYG_ENABLED=false` in `clusters/<name>/cluster-config.env`. Without the
Fleet, set `billing.payg.enabled: false`, and set
`billing.policy.selfService.enabled: false` in the same change. The chart
refuses self-service PAYG without PAYG.

The backend then loses its link to the billing service, the PAYG routes return
404, and both consoles hide the PAYG pages and price panels. The chart does not
delete existing `OrganizationBillingPolicy` objects, and with PAYG off the
admin console can no longer change them. Move organizations off their billing
policies before you turn PAYG off.
