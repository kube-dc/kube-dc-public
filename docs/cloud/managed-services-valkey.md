# Valkey

Valkey is an in-memory key-value store spoken to with the Redis serialization
protocol (RESP). Kube-DC offers it in two shapes: the class `valkey`, a single
node with a data volume, and the class `valkey-ha`, three persistent members
watched by three Sentinels with automatic failover. Both are reachable inside
your Project over TLS only.

Use it for caches, session stores, queues and rate limiters. Read
[Limits](#limits) before you use it for anything you cannot rebuild:
replication in the HA shape is asynchronous, and a restore always produces a
new service.

In the console, pick the Valkey tile; the Dev tier is the single node and the
Production tier is the Sentinel cluster. The manifests below create the same
services.

## Create a service

```yaml
apiVersion: services.kube-dc.com/v1alpha1
kind: ManagedService
metadata:
  name: sessions
  namespace: my-project
spec:
  classRef:
    name: valkey
  planRef:
    name: valkey-development
  placement:
    mode: ProviderShared
  connectivity:
    classRef:
      name: tenant-native
  compute:
    cpu: 250m
    memory: 512Mi
  storage:
    size: 2Gi
  parameters:
    appendonly: "yes"
    settings:
      maxmemory-policy: allkeys-lru
  deletionPolicy: Delete
  deletionProtection: false
```

The HA shape names the other class and plan and requires exactly three members:

```yaml
spec:
  classRef:
    name: valkey-ha
  planRef:
    name: valkey-production
  topology:
    instances: 3
```

Wait for `Ready` before you connect:

```sh
kubectl -n my-project get managedservice sessions -w
```

### Parameters

| Parameter | Meaning | Changes after creation |
|-----------|---------|------------------------|
| `appendonly` | `"yes"` or `"no"`: whether writes are also appended to a log on the data volume. Default `"yes"` | `CreateOnly` |
| `databases` | Number of logical databases. Default `16` | `CreateOnly` |
| `backupStorageSize` | Size of the volume that holds RDB snapshots; defaults to twice the data volume. Single node only | `CreateOnly` |
| `settings.maxmemory` | Memory budget for data, such as `256mb` or `1gb`. When unset the platform derives it from the plan's memory, leaving 25 % headroom for the process | `OnlineDesired` |
| `settings.maxmemory-policy` | The eviction policy. `noeviction` unless you choose one of the seven Valkey policies | `OnlineDesired` |
| `settings.maxclients`, `settings.timeout`, `settings.notify-keyspace-events` | Connection limit, idle timeout and keyspace notifications | `OnlineDesired` |

Sizing (`spec.compute`, `spec.storage.size`) follows the plan bounds like every
family. Storage grows through `ExpandStorage`; compute changes through
`Resize`. The member count is fixed by the class: one, or three.

## Connect an application

Ask for a credential with a `ServiceBinding`. Valkey has one credential role,
`default`, on the `cache` endpoint. The HA class adds a `read-only` role whose
binding defaults to the replica endpoint.

```yaml
apiVersion: services.kube-dc.com/v1alpha1
kind: ServiceBinding
metadata:
  name: sessions-client
  namespace: my-project
spec:
  serviceRef:
    name: sessions
  serviceUID: REPLACE_WITH_SERVICE_UID
  role: default
  consumer:
    kind: ServiceAccount
    name: default
    namespace: my-project
  delivery:
    secretName: sessions-client
```

When the binding is `Ready` it delivers a Secret with `host`, `port` (`6379`),
`username`, `password`, `uri` and `ca.crt`. Mount them; never copy them into an
image or a manifest.

**TLS is required.** The endpoint speaks TLS only, and the certificate is issued
by the platform's own authority, so your client must trust the delivered
`ca.crt`. A quick check from a pod in the same Project:

```sh
valkey-cli --tls --cacert /path/to/ca.crt \
  -h "$VALKEY_HOST" -p "$VALKEY_PORT" \
  --user default --pass "$VALKEY_PASSWORD" --no-auth-warning PING
```

Most client libraries need TLS enabled explicitly and the CA passed in; a client
that connects without TLS fails to handshake. The console's **Connect** card
shows the same connection for `valkey-cli`, Node, Python, Go and a `.env` file.

## Day-2 operations

Request an operation with a `ServiceOperation`, or use the action in the
console. Which ones your plan allows, and which run without provider approval,
is in the plan (`operations.allowed`, `operations.autoApprove`).

| Operation | Single node | HA | What it does |
|-----------|-------------|----|--------------|
| `Backup` | ✅ | ✅ | Writes a verified RDB archive to the Project's backup bucket |
| `ExpandStorage` | ✅ | ✅ | Grows the data volume online, up to the plan's ceiling. Volumes never shrink |
| `Resize` | ✅ | ✅ | Changes CPU and memory within the plan's bounds. The HA shape resizes members one at a time |
| `RotateCredentials` | ✅ | ✅ | Issues a new password for `default` and updates every binding that delivers it |
| `MinorUpgrade` | ✅ | ✅ | Moves to another image on the Valkey 8 line named in the plan's `allowedImages` |
| `UpdateParameters` | ✅ | ✅ | Changes the allow-listed `settings` |
| `Hibernate`, `Resume` | ✅ | ❌ | Scales the node to zero and back, keeping both volumes |
| `RestoreToNew` | ✅ | ✅ | Creates a new service from a completed backup |

`UpdateParameters` takes its settings under `parameters`, as strings:

```yaml
spec:
  type: UpdateParameters
  parameters:
    parameters:
      maxmemory-policy: allkeys-lru
```

Settings outside the allow-list are refused, and so is a value that is not a
string: the operation is `Rejected` with the reason in its status, and nothing
changes. There is no `Scale`, no switchover and no major upgrade: the release
line and the member count are fixed when the service is created.

## Credentials

`RotateCredentials` rotates the `default` password and republishes it to every
binding. The rotation is a live cutover of a few minutes: the previous password
keeps working during a short grace period so a running application can pick
the new one up, then it is rejected. Applications that read the password from
an environment variable need new Pods, because environment variables are fixed
at Pod start; mount the Secret as a file and reread it if you want rotation
without a restart.

You can also schedule rotation with a `ServiceCredentialPolicy` for the
`default` role. ACL users you create yourself are yours to manage: Kube-DC
neither rotates nor restores them.

## Backups and restore

A `Backup` operation, and the plan's schedule when the plan enables backups,
write a durable RDB archive to your Project's backup bucket and record it as a
`ServiceBackup`. The standard Production plan backs up daily and keeps 14 days;
Dev keeps 7. There is no point-in-time recovery: a restore returns the data as
it was when that snapshot was taken.

Restore is always into a **new service**: a `RestoreToNew` operation, or a new
`ManagedService` with `spec.restoreFrom` naming the source service, its UID and
the backup. There is no restore in place. The procedure is the same as for
PostgreSQL; see
[Restore into a new service](postgresql-backup-restore.md#restore-into-a-new-service).

## Deleting a service

- **`Delete`** removes the members, the data volume, the snapshot volume of a
  single node, and the delivered credentials. Backups already in the bucket
  stay until their retention ends. Confirm by annotating the service with
  `services.kube-dc.com/confirm-delete: <name>`.
- **`SnapshotAndDelete`** takes a final backup, then removes the service. If the
  data is already gone so that no backup can be taken, the platform deletes
  nothing and reports the deletion as blocked.
- **`Retain`** leaves everything in place, still consuming your Project's
  capacity, and only withdraws management.

Deletion protection must be turned off in its own update before a delete is
accepted; see [Status and Deletion](managed-services-status-deletion.md).

## Limits

| | Single node (`valkey`) | Sentinel (`valkey-ha`) |
|---|---|---|
| High availability | None. A node failure means downtime, and writes since the last flush can be lost | Automatic failover between three members. Replication is asynchronous: acknowledged writes can be lost on failover |
| Read scaling | None | `read-only` bindings default to the replica endpoint; a persistent connection stays on one member and reads can lag writes |
| Backups | Verified RDB archives to the Project bucket, scheduled and on demand | Same |
| Restore | Into a new service only. No point-in-time recovery | Same |
| Hibernation | Yes | No |
| Exposure | Inside the Project only | Same |
| Fixed at creation | Release line, `appendonly`, `databases`, snapshot volume size | Release line, three members, `appendonly`, `databases` |
