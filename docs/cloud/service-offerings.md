# List available plans

As a Project member, use service offerings to see which managed-service plans your Project may use and what each plan allows.
A `ServiceOffering` is a read-only copy of one plan, published into your Project namespace by the platform.
It lists the plan's defaults, limits and choices, and a manifest you can start from.

:::note Availability
Service offerings are available on installations where the provider has enabled them for managed services.
Check whether your installation serves them:

```bash
kubectl api-resources --api-group=services.kube-dc.com | grep -i serviceofferings
```

If the command prints nothing, or `kubectl get serviceofferings` finds no resources in your Project, no plans are published to it this way. Use the plan names from the console or your provider; see [Classes and plans](managed-services-plans.md#names-to-use).
:::

## How offerings relate to plans

Each offering has the same name as its plan. Use that name in `spec.planRef.name` of a `ManagedService`.

An offering appears in a Project namespace only when all of these are true:

- the provider has published the plan and it is ready to accept services;
- the plan is offered to your Project. Providers can restrict a plan to selected Projects;
- the platform has finished setting up the Project namespace. A new Project can take a short time to show its offerings.

Offerings are read-only. Every standard Project role (`admin`, `developer`, `project-manager`, `user`) can get, list and watch them; no Project role can create, edit or delete them.

An offering is for discovery only. It shows the plan's last projected revision (`plan.lastProjectedRevision`) for new services. Listing it does not reserve capacity and is not permission on its own: the plan, your quota and the plan's limits are checked again when you create the service.
Offerings do not describe existing services: a running service keeps the plan revision it was created with, and its own status decides what it may do.

## List the offerings

List the plans offered to the Project:

```bash
kubectl get serviceofferings -n <project>
```

Replace `<project>` with the Project namespace. The short name is `so`:

```bash
kubectl get so -n <project>
kubectl get so -n <project> -o wide      # adds the plan revision
kubectl get so -n <project> -l services.kube-dc.com/class=postgresql
```

The output shows the class, the engine version, the default instance count and the `Ready` condition. Sharded and data-pool plans leave the instance column empty; read their topology from `status.defaults`.
Use only offerings whose `READY` column is `True`; see [Check that the offering is usable](#check-that-the-offering-is-usable).

Compare plans side by side:

```bash
kubectl get so -n <project> -o custom-columns='PLAN:.metadata.name,CLASS:.status.class.name,CPU:.status.defaults.perInstance.cpu,MEMORY:.status.defaults.perInstance.memory,STORAGE:.status.defaults.perInstance.storage,HA:.status.capabilities.ha,PER_PROJECT:.status.limits.servicesPerProject'
```

## Read an offering

Read the complete offering:

```bash
kubectl get so <plan> -n <project> -o yaml
```

### Check that the offering is usable

Use an offering only when `status.projectionFormat` is `1` and its `Ready` condition is `True`:

```bash
kubectl get so <plan> -n <project> -o jsonpath='{.status.projectionFormat} {.status.conditions[?(@.type=="Ready")].status}{"\n"}'
```

The command prints `1 True` for a usable offering. An offering without a status, with another format or without `Ready=True` is not offered yet: wait and read it again, or ask your provider.
Also check that the fields you need, such as `defaults` or `createExample`, are present; if one is missing, do not use the offering for that step.
On a usable offering, some fields are optional by design and their absence has a defined meaning; see the table below.

### Fields

All content is in `status`; `spec` is empty. The status fields correspond to plan fields described in [What a plan decides](managed-services-plans.md#what-a-plan-decides):

| Field | What it tells you |
|---|---|
| `plan.name` | The value for `spec.planRef.name`. It equals the offering name |
| `class.name` | The value for `spec.classRef.name` |
| `class.family` | The engine family, such as `postgresql` or `mariadb`. It is not a manifest reference |
| `description` | The provider's description of the plan. It can be empty |
| `engineVersions` | The engine version a new service starts on |
| `defaults` | What a new service gets for fields it leaves unset: CPU, memory and storage per instance, and the topology (`instances`, or `shards` with `replicasPerShard`, or `dataReplicas`). No default storage means the plan has no persistent storage |
| `constraints` | The allowed ranges for the same fields. Equal `min` and `max` mean the value is fixed. In the per-instance CPU, memory and storage ranges, an absent `min` or `max` means the plan sets no bound at that end. Topology ranges always have both ends; if one is missing, do not use the offering. The service is still validated when you create it. Storage is per instance |
| `capabilities` | `ha` (automatic failover when the service runs more than one instance, or more than one replica per shard; several shards with one replica each do not fail over), `synchronousStandbys` (PostgreSQL), `pitr` (point-in-time recovery within the retention window) |
| `placementModes`, `connectivityClasses`, `deletionPolicies` | The values you may set in `spec.placement.mode`, `spec.connectivity.classRef.name` and `spec.deletionPolicy` |
| `backup` | Present only when the plan takes backups: the retention range and default in days, the default schedule (UTC) and the minimum interval between scheduled backups |
| `operations` | The `ServiceOperation` types a new service on this plan may request, and whether each one waits for provider approval. For an existing service, its own status and the operation's result decide |
| `limits.servicesPerProject` | How many services on this plan one Project may have. On a usable offering, absent means the plan sets no per-Project count; quota still applies when you create the service |
| `createExample` | A `ManagedService` manifest built from the defaults |

Some plan settings are not part of the offering, for example storage classes, exposure and credential entitlements, upgrade images and maintenance windows.
Ask your provider about them.

Print single values with JSONPath:

```bash
# Defaults and allowed ranges
kubectl get so <plan> -n <project> -o jsonpath='{.status.defaults}{"\n"}'
kubectl get so <plan> -n <project> -o jsonpath='{.status.constraints.perInstance}{"\n"}'
# Instance range: instance-based plans only (sharded and data-pool plans use
# .status.constraints.shards, .replicasPerShard or .dataReplicas instead)
kubectl get so <plan> -n <project> -o jsonpath='{.status.constraints.instances.min}-{.status.constraints.instances.max}{"\n"}'

# Services allowed per Project, and backup retention
kubectl get so <plan> -n <project> -o jsonpath='{.status.limits.servicesPerProject}{"\n"}'
kubectl get so <plan> -n <project> -o jsonpath='{.status.backup.retentionDays}{"\n"}'

# Operations and approval for new services on this plan
kubectl get so <plan> -n <project> -o jsonpath='{range .status.operations[*]}{.type}{"\t"}{.requiresApproval}{"\n"}{end}'
```

For a plan such as `postgresql-production`, the first command prints a value like `{"instances":3,"perInstance":{"cpu":"1","memory":"2Gi","storage":"20Gi"}}`.

## Create a service from an offering

`status.createExample` holds a `ManagedService` manifest that uses the plan's defaults, its first placement mode and its first connectivity class.
It sets `deletionPolicy` only when the plan does not allow the default, `Retain`.
Check that the offering is [usable](#check-that-the-offering-is-usable) and that `createExample` is not empty before you start.

1. Save the example:

   ```bash
   kubectl get so <plan> -n <project> -o jsonpath='{.status.createExample}' > service.yaml
   ```

   For a PostgreSQL production plan, the file looks like this:

   ```yaml
   apiVersion: services.kube-dc.com/v1alpha1
   kind: ManagedService
   metadata:
     name: example
   spec:
     classRef:
       name: postgresql
     connectivity:
       classRef:
         name: tenant-native
     placement:
       mode: ProviderShared
     planRef:
       name: postgresql-production
     storage:
       size: 20Gi
     topology:
       instances: 3
   ```

2. Edit the file. Replace `example` with your service name, and add the service family's parameters and any sizing within `constraints`.
   See the family guide, for example [Create a PostgreSQL service](postgresql-create.md).
   Use `placement.mode: ProviderShared`. If the offering lists no `ProviderShared` mode, ask your provider for a plan you can use.
3. Validate and create the service:

   ```bash
   kubectl apply --dry-run=server -n <project> -f service.yaml
   kubectl apply -n <project> -f service.yaml
   ```

The example is valid for the defaults it was built from. It is not a guarantee of acceptance: authorization, available capacity, quota and plan limits are checked when you create the service.
A `ManagedService` that names a plan resolves it again when it is accepted, so it can get a newer plan revision than the one shown in the offering.

:::tip Keep offerings out of Git
Store your `ManagedService` manifests in Git, not offerings. The platform rewrites offerings when a plan changes, and removes them when a plan is withdrawn.
:::

## Check quota before you create

An offering shows the plan's own limits. It does not show how much of your Organization or Project quota is left.
Before you create a service, compare what the service will request with the remaining quota:

1. Multiply the per-instance values by the member count. For the defaults above, that is 3 × 1 CPU, 3 × 2Gi memory and 3 × 20Gi storage.
2. Leave headroom: backups, connection poolers and operations can start additional Pods.
3. Read the remaining Organization and Project quota; see [Track usage with kubectl](billing-usage.md#track-usage-with-kubectl).
4. Check `limits.servicesPerProject` against the services that already use the plan in the Project:

   ```bash
   kubectl get managedservice -n <project> -o custom-columns='NAME:.metadata.name,PLAN:.spec.planRef.name'
   ```

A service over the plan's per-Project limit is refused with `PlanQuotaExceeded`. A service whose Pods do not fit the Organization quota stops making progress until capacity is freed, and its conditions can report `OrganizationQuotaExceeded`. See [Common refusals](managed-services-status.md#common-refusals).

## Offerings and billing

Offerings work the same way for every billing mode: pay-as-you-go, a fixed subscription plan, or no billing plan.
They show plan defaults and limits, not prices.

- **Pay-as-you-go:** ask your provider how resources and a managed service's management fee are charged to your Organization. Prices depend on your Organization.
- **Fixed subscription plan:** the plan sets your Organization's quota, which all its Projects share; see [Understand resource quotas](billing-usage.md#understand-resource-quotas).
- **Organization without a billing plan:** ask your provider how managed services are charged. The absence of a plan does not mean a service is free.

In every mode, creating a service is still subject to your Organization and Project quota.

## Why a plan is missing

If a plan you expect is not listed, the offerings cannot confirm that it is available to you now. That does not prove the plan is refused: the check that counts runs when you create the service (`Accepted` condition; see [Common refusals](managed-services-status.md#common-refusals)). Common causes:

| Cause | What to do |
|---|---|
| The plan is not offered to this Project | Ask your provider to offer it to the Project |
| The plan is disabled, withdrawn or not published, or its service class is not ready | Ask your provider. An offering is removed shortly after its plan stops being offered |
| The provider is still verifying a new or changed plan | Wait and list again. An existing offering stays as it is until verification completes |
| The Project was created moments ago, or the platform has not updated offerings yet | Wait and list again |
| Offerings are not enabled on the installation | Use the console or ask your provider for plan names |

A removed offering does not affect services that already use the plan. Read their `CatalogPinned` condition to see whether their plan changed; see [When the catalog changes](managed-services-plans.md#when-the-catalog-changes).

## Next steps

- [Classes and plans](managed-services-plans.md)
- [Create a PostgreSQL service](postgresql-create.md)
- [Check status and troubleshoot](managed-services-status.md)
- [Billing and usage](billing-usage.md)
