# OpenSearch

Use the `opensearch` class for application search indexes over verified HTTPS.
Cloud offers the Development and Production plans below. Other installations
choose which qualified service families to enable in their catalog.

| Plan | Data nodes | Cluster managers | Starting allocation per data node |
| --- | --- | --- | --- |
| `opensearch-development` | Three fixed combined data/ingest/voting nodes | The same three nodes | 500m CPU, 2Gi memory, 10Gi storage |
| `opensearch-production` | Three to six data/ingest nodes | Three separate fixed managers | 1 CPU, 4Gi memory, 20Gi storage |

Production managers each reserve 250m CPU, 1Gi memory and 4Gi storage. They are
excluded from `topology.dataReplicas`. Both plans require at least three distinct
hosts. Each data member needs its own eligible host, so four data members need
four hosts with enough free capacity.
Sub-core members may burst to one CPU when the host has capacity. Placement
and billing use the selected CPU reservation; memory stays bounded.

The service endpoint distributes connections across data nodes; dedicated
managers do not receive application traffic.

Allow at least 9Gi of organization memory quota for default Development startup
or 18Gi for default Production startup, plus your other workloads. These totals
include metrics and reserved bootstrap/archive headroom. Check CPU, storage and
eligible-host capacity too; a price quote does not reserve placement capacity.

## Create a service

Replace `my-project` with your Project namespace. This manifest requests the
Development plan with its default HA membership:

```yaml
apiVersion: services.kube-dc.com/v1alpha1
kind: ManagedService
metadata:
  name: search
  namespace: my-project
spec:
  classRef:
    name: opensearch
  planRef:
    name: opensearch-development
  placement:
    mode: ProviderShared
  connectivity:
    classRef:
      name: tenant-native
  compute:
    cpu: 500m
    memory: 2Gi
  storage:
    size: 10Gi
  topology:
    dataReplicas: 3
  parameters:
    indexPrefix: app
  maintenance:
    window:
      day: Sun
      startHour: 2
      durationMinutes: 120
  deletionPolicy: Retain
```

Use `dataReplicas` alone. The API rejects `instances`, shard topology and
mixed shapes for OpenSearch. Storage class is resolved from the plan's role
and stays fixed after placement. Apply the manifest and inspect status:

```sh
kubectl apply --dry-run=server -f search.yaml
kubectl apply -f search.yaml
kubectl get managedservice search -n my-project -w
```

## Connect your application

Create a ServiceBinding using the service's actual UID. Replace
`REPLACE_WITH_SERVICE_UID` with `metadata.uid` from `search`:

```yaml
apiVersion: services.kube-dc.com/v1alpha1
kind: ServiceBinding
metadata:
  name: search-app
  namespace: my-project
spec:
  serviceRef:
    name: search
  serviceUID: REPLACE_WITH_SERVICE_UID
  role: owner
  consumer:
    kind: ServiceAccount
    name: search-app
    namespace: my-project
  delivery:
    secretName: search-app
```

The delivered Secret contains `username`, `password`, `host`, `port`, `url`,
`indexPrefix`, `ca.crt` and `tls=verify-full`. Mount the CA in your workload and
verify the hostname. The URL does not embed a password. Do not disable TLS
verification.

The `owner` role reads, writes, creates and deletes application indexes and
manages mappings and aliases within the admitted index scope. `readonly` reads
that same scope. Neither role administers security, repositories or global
cluster state. Index names and aliases use the selected prefix, such as
`app-events`. The prefix is fixed at creation. New application indexes receive
one shard replica; an application index with no replica prevents healthy
service readiness.

## Change capacity and maintain the service

Use the existing [service operations API](managed-services-operations.md):

| Operation | Parameters | Behavior |
| --- | --- | --- |
| `Resize` | `cpu`, `memory`, or both | Rolls data nodes and adjusts their heap within plan bounds |
| `ExpandStorage` | `size` | Increases per-data-node allocation; cannot shrink |
| `Scale` | `pool: data`, `dataReplicas` | Production only; leaves the three managers fixed |
| `RotateCredentials` | `role: owner` or `role: readonly` | Verifies the new password and retirement of the old password on every data node |
| `Backup` | None | Captures the admitted application index scope |

On qualified local-path plans, storage is a recorded reservation rather than a
directory limit. Expansion reserves the additional allocation without changing
the native PVC request or moving existing data. Other storage classes must
support physical volume expansion. Maintenance uses the service's recurring
UTC window unless the operation requests immediate execution.

The safe cluster settings are `search.max_buckets` (a string integer from
`1` to `65536`) and `search.allow_expensive_queries` (`"true"` or `"false"`).
Set these under `spec.parameters.settings`. Arbitrary plugins, node roles,
images, TLS settings and raw operator configuration are unavailable.

## Back up and restore

Backups use `opensearch-snapshot-v1`: a verified manifest and its native S3
repository payload. RestoreToNew creates an independent target and preserves
its own credentials. Snapshots include application indexes and their scoped
aliases, including hidden indexes in that scope. They exclude security indexes,
users and global cluster state. Restore does not modify the source.

OpenSearch offers exact snapshot recovery, not point-in-time recovery. Use
[backup and restore](managed-services-backup-restore.md) for the common request,
retention and deletion contracts. `Retain` preserves the engine and data;
`Delete` removes them; `SnapshotAndDelete` requires a verified final backup
before removal. Retained backups remain subject to their original expiry and
Project bucket lifecycle.

## Monitor the service

The Metrics tab reports data-node CPU, memory and recorded storage, application
document count and primary index storage, search and indexing rates and mean shard latency, active,
unassigned and relocating shards, data-node count, JVM heap and garbage
collection time, rejected operations, and the least available data filesystem.
A latency chart can be empty when there are no operations in its interval.
Manager quorum and native cluster health are also reported in service status.

Local-path storage shares its host filesystem. Recorded allocation is not free
disk space; the engine's disk watermarks can block replicas when a host fills.
Keep enough free space on every data host. Pod readiness alone does not make a
service Ready: native membership, replicated shards, TLS, credentials and the
required backup must pass.
