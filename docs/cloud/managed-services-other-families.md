# Valkey

Valkey is an in-memory key-value store, spoken to with the Redis serialization
protocol (RESP). Kube-DC offers it as a Managed Services family with the class
`valkey`: a single node with a data volume, reachable inside your Project over
TLS.

Use it for caches, session stores, queues and rate limiters — data your
application can rebuild. Read [Limits](#limits) before you use it for anything
else: Valkey here has no replica, and its backups cannot be restored through
Kube-DC.

Whether it appears in your console depends on your provider publishing the
Valkey plan. If it is not offered, `Create` refuses the plan.

## Create a service

The console offers Valkey wherever the plan is published. The same service in a
manifest:

```yaml
apiVersion: services.kube-dc.com/v1alpha1
kind: ManagedService
metadata:
  name: sessions
  namespace: <your-project-namespace>
spec:
  classRef: {name: valkey}
  planRef: {name: valkey-basic}
  placement: {mode: ProviderShared}
  connectivity: {classRef: {name: tenant-native}}
  deletionPolicy: Delete
  deletionProtection: false
```

The plan sets the size — CPU, memory, the data volume and the ceiling that
`ExpandStorage` may grow it to — and how many Valkey services one Project may
hold. Read yours in the console or with
`kubectl get managedserviceplan valkey-basic -o yaml`; see
[Classes and Plans](managed-services-plans.md).

Wait for `Ready` before you connect:

```sh
kubectl -n <your-project-namespace> get managedservice sessions -w
```

## Connect an application

Ask for a credential with a `ServiceBinding`. Valkey has one credential role,
`default`, and one endpoint role, `cache`:

```yaml
apiVersion: services.kube-dc.com/v1alpha1
kind: ServiceBinding
metadata:
  name: sessions-client
  namespace: <your-project-namespace>
spec:
  serviceRef: {name: sessions}
  serviceUID: <the ManagedService uid>
  role: default
  consumer: {kind: ServiceAccount, name: default, namespace: <your-project-namespace>}
  delivery: {mode: DataPlaneLocalSecret, secretName: sessions-client, endpointName: cache}
```

When the binding is `Ready` it delivers a Secret with `host`, `port`,
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
that connects without TLS will simply fail to handshake.

## Day-2 operations

Request an operation with a `ServiceOperation`. Which ones your plan allows, and
which run without provider approval, is in the plan
(`operations.allowed`, `operations.autoApprove`).

| Operation | What it does |
|---|---|
| `Backup` | Writes an RDB snapshot to the backup volume beside the instance. Read [Backups](#backups-are-not-a-recovery-facility) first. |
| `ExpandStorage` | Grows the data volume, online, up to the plan's ceiling. Volumes never shrink. |
| `RotateCredentials` | Issues a new password for `default` and updates every binding that delivers it. |
| `MinorUpgrade` | Moves to another image on the same Valkey release line. |
| `UpdateParameters` | Changes allow-listed engine settings. The instance restarts once. |
| `Hibernate` | Scales the instance to zero and keeps both volumes. |
| `Resume` | Brings a hibernated instance back. |

`UpdateParameters` takes its settings under `parameters`, as strings:

```yaml
spec:
  type: UpdateParameters
  parameters:
    parameters:
      maxmemory-policy: allkeys-lru
```

Settings outside the adapter's allow-list are refused, and so is a value that is
not a string — the operation is `Rejected` with the reason in its status, and
nothing changes.

There is no `Scale`, no `Resize`, no switchover or failover, and no major
upgrade: the release line, the instance count, `appendonly`, `databases` and the
size of the backup volume are fixed when the service is created.

## Credentials

`RotateCredentials` rotates the `default` password and republishes it to every
binding. The previous password keeps working for a short grace period so a
running application can pick the new one up; after that it is rejected.
Applications that read the password from an environment variable need new Pods,
because environment variables are fixed at Pod start — mount the Secret as a
file and reread it if you want rotation without a restart.

You can also schedule rotation with a `ServiceCredentialPolicy` for the
`default` role. Valkey has no equivalent of a database's extra logins: ACL users
you create yourself are yours to manage, and Kube-DC neither rotates nor
restores them.

## Backups are not a recovery facility

This is the most important limit on the page.

- A `Backup` writes an RDB snapshot to a **second volume in the same
  placement**. It does not go to object storage, so it shares a failure domain
  with the data it protects.
- There are **no scheduled backups** and **no backup history**: the console's
  backup list stays empty for Valkey, because the family records none.
- There is **no restore**. `restoreFrom` is refused, and neither restore-in-place
  nor restore-into-a-new-service exists for this family. A snapshot can only be
  used by copying it off the volume and loading it yourself.

Treat Valkey as data you can rebuild. If you need point-in-time recovery, verified
restores or off-site copies, use [PostgreSQL](postgresql-backup-restore.md).

## Deleting a service

- **`Delete`** removes the instance, the data volume, **the backup volume with
  every snapshot on it**, and the delivered credentials. Nothing is left to
  recover from, so export anything you want to keep before you delete.
- **`SnapshotAndDelete`** quiesces the instance, takes a final snapshot onto the
  backup volume, then removes the instance and the data volume and **keeps the
  backup volume** as the location of that snapshot. The volume stays in your
  Project and keeps consuming its storage capacity until you delete the claim
  yourself. The snapshot is a file on that volume: Kube-DC cannot restore it for
  you, so recovering means mounting the volume and loading the file by hand.
  If the data volume is already gone, the promised snapshot cannot be taken —
  the platform then deletes nothing and reports the deletion as blocked.
- **`Retain`** leaves the instance and both volumes in place, still consuming
  your Project's capacity, and only withdraws management.

Deletion protection must be turned off in its own update before a delete is
accepted; see [Status and Deletion](managed-services-status-deletion.md).

## Limits

| | |
|---|---|
| High availability | None. One node, no replica, no automatic failover. A node failure means downtime, and writes since the last flush to disk can be lost. |
| Backups | On-demand only, onto a volume beside the instance. No history records. |
| Restore | Not available through Kube-DC. |
| Resize | CPU and memory are fixed by the plan; only storage grows. |
| Exposure | Inside the Project only. No public endpoint and no gateway. |
| Credential roles | `default` only. |
| Fixed at creation | Release line, instance count, `appendonly`, `databases`, backup volume size. |

## Other families

Kafka is not offered on the shared, in-Project placement this chapter covers.
Other service families are not covered here.
