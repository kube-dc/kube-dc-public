# Assigning GPU capacity without billing (enterprise / no-billing installs)

Most Kube-DC clusters drive organization quota from the billing plan: an org with
an active plan gets a `HierarchicalResourceQuota` (HRQ) and, on top of it, any
addons. Enterprise and private-cloud installs frequently run **without a billing
provider** — the organizations have no plan, so historically they had no HRQ and
therefore no way to receive a bounded GPU capacity grant.

This page documents the billing-independent path: a **platform operator grants GPU
capacity to an organization through a GPU addon**, and the organization admin
sub-assigns it to a project. No billing provider, plan, or payment flow is
involved. The same catalog profile, DeviceClass, DRA admission policies, per-project
accelerator cap editor, and DRA quota enforcement are reused unchanged.

## How it works

1. **Catalog** — a GPU profile is published (`gpu.profiles`, `GPU_CATALOG_ENABLED=true`)
   and marked **grantable** (`billingEligible: true` — this flag means "may be
   granted"; it does **not** require a payment provider). Its DeviceClass is generated
   by the chart.
2. **Addon** — a GPU addon is defined in the billing config `addons` map with a `gpu`
   grant against that profile (e.g. `gpu: { <profileId>: { shares: 1 } }`), `disabled:
   false`.
3. **Operator grant** — the platform admin assigns the addon to an organization
   (admin console → GPU add-on management, or the `set-addons` admin API, which writes
   the `billing.kube-dc.com/addons` annotation). The org needs no plan; its
   subscription status only has to be active-family.
4. **Org HRQ** — the manager now renders an **accelerator-only HRQ** for an active,
   plan-less org that carries GPU addons: the HRQ holds only the
   `<deviceClass>.deviceclass.resource.k8s.io/devices` key(s). cpu/memory/pods/storage
   stay unmanaged, exactly as for any plan-less org — the grant never imposes a quota
   the deployment did not opt into.
5. **Org → project** — the organization admin caps a project with the existing
   per-project accelerator cap editor (Organization → project quota), bounded by the
   org grant.
6. **Enforcement** — Kubernetes DRA ResourceQuota bounds actual concurrent claims to
   the project cap; the DRA shape policies force the fixed product.

## Why it is safe

- An accelerator-only HRQ deliberately omits the base dimensions. Emitting them for a
  plan-less org would write `requests.cpu=0` / `pods=0` and deny every workload; the
  manager omits them so those dimensions remain unmanaged.
- `billingEligible: true` must be set on the profile **before** a GPU addon that
  grants it is enabled — the same ordering rule as any GPU addon; reversing it fails
  plan-config load. No payment provider is required either way.
- Removing the addon (or setting its quantity to zero) drops the org HRQ accelerator
  key on the next reconcile; existing project caps then have nothing to draw on.

## Relationship to billing plans

Where a billing provider *is* configured, the same addon mechanism still applies on
top of the plan HRQ; nothing changes. The only new behavior is that an **active org
with no plan** now receives an accelerator-only HRQ from its GPU addons instead of no
HRQ at all.
