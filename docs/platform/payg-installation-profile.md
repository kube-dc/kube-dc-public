# Configure the optional billable PAYG profile

Use this guide to configure usage billing on a Kube-DC installation. You start
with the default-off PAYG capability, select the meter sources the installation
provides, then enable organizations and approved statement export. This is an
operator setup procedure; ordinary collection and subsequent month processing
run automatically after setup.

## Before you begin

Use a matched CLI, Fleet starter and complete PAYG image release. Every PAYG
image must be pinned by digest and compatible with the installation's database
schema. Use the Fleet starter's `bootstrap/release-pins.env`; do not combine an
old starter's billing binaries with a newer database. The supported database profile uses **PostgreSQL 16**, as the starter's
`platform/monitoring/grafana-pg/cluster.yaml` specifies. PostgreSQL 17 is not
qualified for these migration contracts; selecting a dedicated database does
not change that requirement. The PAYG database has its own role and verified
TLS connection. It can share the installation's CNPG
cluster, or use the separately configured `PAYG_DB_CLUSTER` and
`PAYG_DB_CA_SECRET` for a dedicated cluster.

The installation's billing provider is `stripe` or `whmcs`. This selects who
owns billing; it does not enable PAYG invoice export. Start without PAYG provider
export, and use approved JSON/CSV statements for an existing billing system.

## Prepare database durability and backup capacity

For the billable reference profile, use two PostgreSQL instances with one
required synchronous standby. Losing that standby blocks database write commits
until replication is restored. Check actual streaming/quorum state and replication
slot health; two Ready Pods alone do not prove replication. Confirm a recent
verified backup, including full dump decoding, before enabling charges.

Size WAL retention for measured write bursts and available disk. Cloud's
11 November cleanup produced approximately 2 GB of WAL in two minutes and
invalidated a 1 GB replication-slot allowance. Its reviewed reload-only budgets
are:

| PostgreSQL parameter | Cloud reference value |
| --- | --- |
| `max_slot_wal_keep_size` | `16GB` |
| `wal_keep_size` | `2GB` |
| `max_wal_size` | `4GB` |

Apply installation-specific values after any Fleet component patches. In
particular, the optional `logical-backups` component sets a 1 GB slot allowance;
a high-volume PAYG installation must override it. These values do not remove
synchronous durability or guarantee that an arbitrary outage fits the retained
WAL. Monitor actual slot lag and free disk.

The logical-backup component retains three verified bundles, with a **4 GiB
compressed bundle limit** and 20 GB bucket quota. Physical database size includes
indexes and reusable space, so measure the completed compressed backup. If the
bundle exceeds that limit, select a suitably sized PostgreSQL backup profile;
do not disable the size check or discard required financial tables. A large
installation should size a dedicated PostgreSQL cluster and backup system for
its resource and evidence cardinality.

## Select the components

A starter includes the following optional bundles. Including a directory in
the starter does not run it. Only a selected per-cluster Flux layer or component
installs it; keep unneeded sources unselected.

| Capability | Fleet bundle or component | Required setup |
| --- | --- | --- |
| Core usage, estimate API and statements | `platform/payg` | `--payg`, installation identity, database/TLS, resource products, rate card and organization policy |
| Automatic source preparation and proof | `platform/payg/components/continuous-source-hours`, `continuous-native-hours`, `selected-hour-proofs`, `shared-evidence` | Installation-owned active source manifest and compatible worker pins |
| Automatic monthly settlement | `platform/payg/components/settlement-automation` | Standing authorization, cutoff, start month, and configured source continuation |
| Stock log and metric charges | `platform/payg/components/stock-telemetry-shadow` | Stock Loki/Mimir source identity, exact topology, namespace attribution and qualified v2 pricing terms |
| Object and platform backup capacity | `platform/payg-rgw`, `platform/payg-backup`, `platform/payg/components/object-uid-census` | Local RGW store identity, census scope, customer/platform allocation, source manifest and backup registrations |
| Ceph block and retained storage | `platform/payg-rbd` | Local Ceph identity, storage classes, payer bindings and activated RBD source contract |
| Managed-service plan fees | `platform/payg-managed-fee` | `--payg-managed-fees`, rendered local scope and activated first fee enrollment |
| Managed Kubernetes fees | `platform/payg-kubernetes-fee` | Local controller scope and activated first fee enrollment |
| Retention and dictionary cleanup | `platform/payg/components/hot-archive` | Compatible SQL role, prepared GC profile, selected retention policy and any required archive credentials |

CPU, memory, IPv4 and local-path resource collection use the core collector's
installation-specific products and class provenance. A class name alone is not
proof of the payer or storage origin.

If the installation does not offer a product, omit that product and its source
from its price card and manifest. If it offers a product, configure and verify
its source before enabling billable collection. A missing optional source must
not become a permanently missing required meter. A source that the card offers
must not silently disappear from the statement.

The starter's bundles include their variable and enrollment references in
`platform/payg/README.md` and the source bundle's `README.md`. Read those for the
selected installation; the first source enrollment uses that installation's
actual identity, running writer pins and own public trust keys. Never copy a
Cloud or Stage installation UID, enrollment, source scope or private receipt.

## Prepare a new or existing installation

For a new installation, add `--payg` and its billing provider to your usual
`kube-dc bootstrap init` command. With managed services enabled, add
`--payg-managed-fees`. Review a `--dry-run` first. The managed-fee layer starts
suspended until you render its scope from the running installation; follow
[managed-service fees](payg-managed-fees.md).

For an existing installation, follow the day-2 procedure in
`platform/payg/README.md`. `bootstrap init --payg` does not add PAYG to an
existing overlay. Keep the existing installation's billing identity when you
upgrade or add components.

Add the selected core components to the existing `payg` Flux layer. Add each
selected source bundle as a separate per-cluster Flux layer, using its README.
Configure the required identities and digest-pinned images before unsuspending
it. The setup has a deliberate one-time enrollment boundary: publish and
activate the installation's first qualified source terms before their future
whole-hour start. Installing CronJobs alone does not authorize charges.

## Run the preflight

Complete these checks in order before moving the first organization to billable
collection:

1. Render the selected layer with the installation's values using the Fleet
   `scripts/render_as_flux.sh` procedure. Confirm no unresolved substitutions,
   installation identities from another cluster, floating images, or unselected
   source dependencies. Keep the generated overlay and encrypted credentials in
   the installation's own Fleet repository.
2. Confirm the metering receiver is Ready, logs its compatible billing schema,
   and reaches PostgreSQL with verified TLS. Check the backend's authenticated
   billing connection; `GET /api/system/features` reports `payg: true`.
3. Publish the installation's card and configure product/class mappings. Start
   one organization in PAYG shadow mode. Check quantities and grouping against
   its actual resources; keep other organizations on their existing quota mode.
4. Check the selected sources' enrolled identities, activated effective start,
   and retained current-hour results. Each selected family needs a qualified
   result or a valid explicit zero; Job completion alone is not that result.
   Do not infer source activation from the presence of a newer binary.
5. Enable settlement automation with its explicit standing authorization and
   source continuation. Check the per-account processing status and latest
   source watermark. It must resume after a retry and leave no unexplained
   current source errors.
6. Move the organization to billable collection at the approved future boundary.
   Use the normal statement tools to prepare and reconcile a representative
   period, finalize it, review and approve its immutable statement key, then
   export JSON/CSV. A repeated export preserves the key and exact cent total.

Finance approval is the intended business action. Creating ordinary resources,
normal compatible writer releases, month continuation, and cleanup must not
need per-hour operator approval. Missing measurements follow the configured
free-usage policy; ownership conflicts or corrupt evidence remain errors.

## Offer paid self-service signup

This option requires the schema 169 starter and matching console, backend,
manager and chart. A fresh installation leaves paid signup and invoice export
off. Selecting `--payg` prepares the billing infrastructure. Complete the
database, source and settlement preflight above before you offer paid signup.

1. Publish the installation's price card and record its exact revision. Select
   a whole-hour settlement cutoff that matches your finance terms. The qualified
   paid profile uses `2h` after the end of the UTC month.
2. Configure Stripe TEST in the installation's own account. Set the backend
   HelmRelease value `billing.stripe.secretName` to a Secret in the backend
   namespace containing `STRIPE_SECRET_KEY` and `STRIPE_WEBHOOK_SECRET`.
   Set `billing.stripe.paygPortalConfiguration` to a restricted configuration
   from that account and mode. With WHMCS as the legacy provider, the chart
   uses this configuration to mount the Stripe API key. In the same profile,
   set `STRIPE_WEBHOOK_SECRET` through a Secret reference in `backend.extraEnv`.
   The legacy Stripe profile projects the webhook secret directly. Configure
   the HTTPS console origin, signed invoice webhook and existing billing-provider
   credentials.
3. Create the metering Secret in the namespace you choose. Give the metering
   service account permission to get that named Secret. Its key must reach
   the same Stripe account and mode as the backend and support verification
   of the saved card and Checkout Session as well as statement export.
4. Set the following substitutions in your installation's Fleet overlay. Supply
   the actual rate revision and Secret reference in place of the examples.
   Keep any earlier pilot scope and its approval cutoff intact. The creation
   pause applies to all new invoices in this installation, including pilot
   invoices. Coordinate that pause with finance on an existing installation.

   ```dotenv
   PAYG_SELF_SERVICE_ENABLED=true
   PAYG_SELF_SERVICE_REQUIRE_CARD=true
   PAYG_SELF_SERVICE_COLLECTION_MODE=billable
   PAYG_SELF_SERVICE_RATE_CARD=your-card@your-revision
   PAYG_SETTLEMENT_CUTOFF=2h
   PAYG_EXPORT_PROVIDER=stripe
   PAYG_STRIPE_EXPORT_MODE=test
   PAYG_STRIPE_SECRET_REF=monitoring/your-export-secret/your-key
   PAYG_EXPORT_AUTO_ENROLL=T
   PAYG_EXPORT_CREATION_PAUSED=T
   ```

5. With an authorized test organization, complete hosted card setup and
   verify the accepted agreement, exact enforced policy revision, quota and
   effective usage boundary. Billable signup requires a verified card and
   configures zero trial days.
6. When finance authorizes invoice creation, set
   `PAYG_EXPORT_CREATION_PAUSED=F`. An invoice still requires an eligible
   usage period and approval of its immutable statement. To qualify a live
   rollout, use the live account, matching backend and metering keys, portal
   configuration and webhook secret. Verify that account independently.

To pause new signup, set `PAYG_SELF_SERVICE_ENABLED=false`. To pause creation
of new provider invoices independently, set `PAYG_EXPORT_CREATION_PAUSED=T`.
Keep metering, earned periods, existing-invoice reconciliation and signed
webhooks running.

## Preserve customer telemetry when billing fails

Run stock upstream Loki and Mimir images. The stock aggregate billing worker
reads their measured counters outside the write path. Customer log and metric
writes must not call or wait for PAYG, its database, statement processing, or an
invoice provider.

Bound billing worker CPU, memory, database connections and query time. When
billing and Grafana share PostgreSQL, billing still shares disk and I/O capacity;
a separate database name does not isolate those resources. Select a separate
CNPG cluster when the installation's measured workload requires it.

## Suspend or disable billing

To pause processing, suspend the selected CronJobs through their configuration
switches, reconcile their Flux layers, and let active Jobs finish. Suspending a
Flux Kustomization does not suspend the CronJobs it already installed.
For managed fees, use the exact switches in
[the managed-fee disable procedure](payg-managed-fees.md#turning-it-off).

To disable the capability, stop the installed PAYG collector and workers and set
`PAYG_ENABLED=false` so the backend and console stop exposing PAYG operations.
Preserve the metering database, credentials needed for approved-statement
recovery, and financial records. Review the layer's prune behavior before
removing it. Keep the telemetry components and all customer workloads enabled.

Changing an organization's future policy and closing an existing statement are
separate operations. Do not delete an existing statement or its evidence to
turn off prospective billing.
