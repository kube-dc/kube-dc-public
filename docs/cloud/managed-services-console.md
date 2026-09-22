# Use the console

Everything in this chapter can be done from the Kube-DC console as well as
with manifests. The console shows the same objects the manifests create, so
you can start in one and continue in the other: every creation flow offers
the exact YAML it is about to submit.

Open **Managed services** in the Project navigation. Whether the entry is
present depends on your provider having enabled managed services for your
organization.

## The catalog

The catalog lists every engine your provider publishes for your Project,
grouped by what it is for: databases, caches and key-value stores, streaming
and messaging, search and analytics, storage, applications. A tile shows the
product mark, the engine version, one sentence about the engine and a tag
such as GA, Beta or Popular. Search by engine name or narrow the list by
category.

An engine appears only when at least one of its plans is published for your
installation. If an engine you expect is missing, ask your provider.

## Create a service

**New service** opens a three-step sheet.

1. **Engine.** Pick a tile. The summary rail on the right fills in with the
   defaults of the recommended plan.
2. **Size.** Choose a tier: *Dev* for development and previews, *Production*
   (recommended) for backed-up services with predictable capacity, *HA* where
   the family offers automatic failover. Each tier is one of the provider's
   published plans, and the sheet lists its facts: compute, backups, failover
   or storage expansion. Below the tiers, sliders move storage, CPU, memory
   and the number of instances within what the plan allows. Families that
   are sized by their shard shape, such as ClickHouse, show *Replicas per
   shard* instead of instances. A slider that says *Fixed by this tier*
   cannot move on that plan. **Advanced · pick a published plan** lists every
   plan by name for the cases the tiers do not cover.
3. **Name & access.** The name is prefilled (`pg-1`, `ch-1`, and so on) and
   must be unique in the Project. *Deliver credentials to* creates the owner
   credential as a Kubernetes Secret named `<service>-owner` in the Project
   as soon as the service is ready; choose *Later* to grant access per
   workload afterwards from **Users & access**. *Reachability* is the
   connectivity class; *Project only* publishes the service inside your
   Project's network and allocates no public address. **Advanced · engine
   settings and deletion** holds the engine parameters the class exposes,
   deletion protection and the deletion policy.

The rail always shows what you will get: engine and version, tier, compute,
storage, topology, backups, network and the Secret name, followed by the
outcomes in plain words. **or get the YAML for GitOps** in the footer shows the
`ManagedService` manifest (and the `ServiceBinding` when you chose Secret
delivery) exactly as the console will submit them, with a copy button. Commit
that YAML to your repository and apply it with your pipeline instead of
pressing **Deploy**, if that is how your Project is managed. The two paths
produce the same objects; see [Create a PostgreSQL service](postgresql-create.md)
for the manifest fields.

**Deploy** submits the request. The bell in the header follows it: the
notification names the engine and the service and links to it, and it turns
into a success or failure notice when the platform reports the outcome. A
request the platform refuses stays in the sheet with the reason, so you can
correct it and retry with the same idempotency key.

## The service page

Every service has one page with five tabs. The header shows the product mark,
the name, the status pill (Ready, Provisioning, an operation in progress such
as *Backing up*, Hibernated, Failed) and a **Running ⇄ Hibernated** switch on
plans that allow hibernation. While something is happening to the service, a
line above the tabs says what and since when, and links to where to follow it.

| Tab | What it holds |
|-----|---------------|
| **Overview** | The **Connect** card with the endpoint, TLS requirement and copy-paste snippets for the engine's own client, common drivers and a `.env` block, all reading credentials from the delivered Secret; who is connected (bindings); the facts of the service; the actions the plan offers, each labelled with its effect; recent activity |
| **Settings** | Compute & storage (capacity rows change through actions such as *Resize compute* or *Expand storage*), Backups, Connectivity, the engine's parameters with the plan default shown in grey, Deletion, Advanced. Changes are staged and applied together after **Review & apply**, which shows the resulting request |
| **Users & access** | Bindings (which workload receives which credential role over which endpoint) and rotation policies. Create a binding here when you chose *Later* at creation |
| **Backups** | Backup history with its recovery window, **Back up now**, and restore into a new service or in place where the family supports it |
| **Events** | Change history, platform events and conditions, newest first |

**View YAML** shows the live object. The same window has an **Edit YAML**
switch; **Validate with Kubernetes** runs a server-side dry run before you
save, so a refused change never leaves the window.

## Lists and row actions

The list of services shows the engine mark, name, status and plan for every
service in the Project, newest first. A row's menu offers the lifecycle
actions of that service, including **Resume** for a hibernated service, and
the list header offers **resume all** when several are hibernated.

## Deprecated: the databases area

Databases created with the earlier db-manager product (`KdcDatabase`) are
deprecated. The **Databases** entry appears only for the organizations and
Projects your provider has listed as still running them, with a notice that
new databases are managed services. Everyone else does not see it, and a
bookmarked link explains where to go. See
[Migrating from db-manager databases](managed-services-migration.md).
