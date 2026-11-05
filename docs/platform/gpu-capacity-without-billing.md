# Assign GPU capacity without billing

This page is for platform operators who run Kube-DC without a billing provider,
or who grant capacity outside the billing plans. After you read it, you can give
an Organization a bounded GPU grant, and its administrator can cap that grant
for each Project.

:::note Pilot
GPU support is a pilot capability. Hardware and profile support is
deployment-specific. See [Roadmap](roadmap.md).
:::

You can grant GPU capacity in two ways:

- **Explicit Organization quota**: set `spec.quota.gpu` on the Organization.
  This is the direct route, and it needs no billing provider, plan, or add-on.
- **GPU add-on**: assign a GPU add-on to an Organization that has no billing
  plan.

## Before you begin

Both routes use the GPU catalog. Prepare it first:

1. Set `gpu.enabled: true` in the chart values.
2. Publish the GPU profile in `gpu.profiles` with `enabled: true`.
3. Set `billingEligible: true` on the profile. The flag means that the profile
   may be granted. It does not require a payment provider.

For a profile with the DRA allocation backend, the chart generates its
DeviceClass. A grant that names an unknown or
disabled profile is refused by the controller.

## Grant GPU capacity with an explicit quota

`spec.quota` is the explicit capacity of an Organization. When it is set, it
takes precedence over any billing plan and over GPU add-ons. To set or change it,
you need the custom `allocate` verb on `organizations.kube-dc.com`.

An omitted dimension in `spec.quota` is not granted. The controller enforces it
as zero where the platform enforces that dimension. Set every dimension the
Organization needs, together with the GPU grant.

To grant GPU capacity, add `quota` to the existing Organization. This example
sets every dimension, because an omitted one is enforced as zero:

```yaml
apiVersion: kube-dc.com/v1
kind: Organization
metadata:
  name: <organization>
  namespace: <organization>
spec:
  email: <contact-email>
  quota:
    cpu: "32"
    memory: 128Gi
    storage: 1Ti
    pods: 200
    loadBalancers: 2
    publicIPs: 2
    objectStorage:
      size: 100Gi
    gpu:
      <profile-id>:
        shares: 2
```

Keep the Organization's existing `email`; the field is required.

- `<organization>` is the Organization name.
- `<profile-id>` is the ID of a profile in `gpu.profiles`.

Each entry in `spec.quota.gpu` maps a profile ID to its grant. A grant has these
fields, all non-negative integers:

| Field | Meaning |
|---|---|
| `devices` | Whole devices, for a profile that is counted in devices. |
| `shares` | Shares, for a profile that is counted in shares. |
| `memoryMiB` | GPU memory budget, only for a HAMi profile with `shares` greater than 0. |
| `corePercent` | GPU core budget, only for a HAMi profile with `shares` greater than 0. |

The map holds at most 32 profiles. The manager writes the grant into the
Organization's HierarchicalResourceQuota, with the other quota dimensions.

## Grant GPU capacity with an add-on

Use this route for an active Organization that has no billing plan and no
`spec.quota`:

1. Define a GPU add-on in the billing configuration `addons` map. Give it a
   `gpu` grant against the profile, for example
   `gpu: { <profile-id>: { shares: 1 } }`, and set `disabled: false`.
2. Assign the add-on to the Organization. In the admin console, open the
   Organization's billing tab and click **Manage add-ons**, or use the
   `set-addons` admin API. Both write the `billing.kube-dc.com/addons`
   annotation. The Organization needs no plan, but its subscription status
   must be `active`, `trialing`, or `canceling`.
3. The manager renders an accelerator-only HierarchicalResourceQuota for the
   Organization. For a DRA profile it holds only the
   `<device-class>.deviceclass.resource.k8s.io/devices` keys, where
   `<device-class>` is the profile's DeviceClass name. CPU, memory, Pods, and
   storage stay unmanaged.

The quota omits the base dimensions on purpose. Writing them for an
Organization with no plan would set `requests.cpu=0` and `pods=0`, and deny
every workload.

To remove the grant, remove the add-on or set its quantity to zero. The manager
drops the accelerator key on the next reconcile, and existing Project caps then
have nothing to draw on.

When a billing provider is configured, GPU add-ons apply on top of the plan's
quota in the same way.

## Cap GPU capacity for a Project

With either route, the Organization administrator caps each Project with the
per-Project accelerator cap editor (**Organization** > Project quota). The cap
is bounded by the Organization grant. A Kubernetes ResourceQuota for DRA bounds
the concurrent claims to the Project cap, and the DRA admission policies force
the fixed product.
