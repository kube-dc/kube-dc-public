# RabbitMQ

Use the `rabbitmq` class for private AMQPS messaging. Your installation's catalog
lists the plans it offers. These are the initial plan shapes:

| Plan | Brokers | Starting allocation per broker |
| --- | --- | --- |
| `rabbitmq-development` | One, without high availability | 500m CPU, 1Gi memory, 10Gi storage |
| `rabbitmq-production` | Three, on three distinct hosts | 1 CPU, 2Gi memory, 20Gi storage |

You can select CPU, memory and storage within the published plan bounds. Broker
count, storage class and application vhost stay fixed after creation. Physical
volume-backed services require at least 2Gi; local-path storage is a recorded
allocation, independent of the directory's physical free space. The
supported engine is RabbitMQ 4.3.6; an operator default does not select its version.

## Create a service

Replace `my-project` with your Project namespace:

```yaml
apiVersion: services.kube-dc.com/v1alpha1
kind: ManagedService
metadata:
  name: messages
  namespace: my-project
spec:
  classRef:
    name: rabbitmq
  planRef:
    name: rabbitmq-development
  placement:
    mode: ProviderShared
  connectivity:
    classRef:
      name: tenant-native
  compute:
    cpu: 500m
    memory: 1Gi
  storage:
    size: 10Gi
  topology:
    instances: 1
  parameters:
    vhost: app
  maintenance:
    window:
      day: Sun
      startHour: 2
      durationMinutes: 120
  deletionPolicy: Delete
```

For Production, select `rabbitmq-production`, `instances: 3`, and allocations
within that plan's bounds. Each broker needs an eligible host with sufficient
resources. The platform also reserves a bounded archive worker. A price quote
does not reserve placement capacity.

```sh
kubectl apply --dry-run=server -f messages.yaml
kubectl apply -f messages.yaml
kubectl get managedservice messages -n my-project -w
```

## Connect an application

Create a ServiceBinding with the actual service UID:

```yaml
apiVersion: services.kube-dc.com/v1alpha1
kind: ServiceBinding
metadata:
  name: messages-app
  namespace: my-project
spec:
  serviceRef:
    name: messages
  serviceUID: REPLACE_WITH_SERVICE_UID
  role: owner
  consumer:
    kind: ServiceAccount
    name: messages-app
    namespace: my-project
  delivery:
    secretName: messages-app
```

The delivered Secret contains `username`, `password`, `host`, `port`, `vhost`,
`ca.crt` and `tls=verify-full`. Use AMQPS on port 5671 and verify both the
certificate authority and reported hostname. The console provides Python and
Node.js examples that read credentials from the environment and CA from a file.

| Role | Permissions in the application vhost |
| --- | --- |
| `owner` | Create and delete application topology; publish and consume |
| `publisher` | Publish to existing exchanges; cannot configure topology or consume |
| `consumer` | Consume and acknowledge from existing queues; cannot configure topology or publish |

Use `owner` to declare queues, exchanges and bindings before using the narrower
roles. No application role receives management administrator tags. Platform
credentials and the Erlang cookie are not delivered. Native RabbitMQ and
Topology Operator resources are platform-owned; use the managed-service APIs.

The endpoint distributes new connections across eligible brokers. An open
connection stays on one broker. Applications need reconnect handling and should
use publisher confirms, persistent messages, manual acknowledgments and
idempotent processing for reliable delivery.

## Understand availability

The vhost defaults to quorum queues. Production's message availability guarantee
covers durable quorum queues with replicas on all three distinct hosts.
Explicit classic queues, AMQP stream queues, exclusive/transient queues and
auto-delete queues are outside that HA guarantee. A disabled stream-protocol
plugin does not prevent AMQP stream declarations.

Losing a majority prevents quorum writes. With only three eligible hosts,
permanent host loss leaves the service Degraded until that host is repaired or
an eligible replacement host is available. The platform does not invent a third
replica on either surviving host or force recovery without a majority.

## Resize and maintain

Use the [service operations API](managed-services-operations.md):

| Operation | Parameters | Behavior |
| --- | --- | --- |
| `Resize` | `cpu`, `memory`, or both | Rolls brokers within the plan bounds |
| `ExpandStorage` | `size` | Grows allocation per broker; cannot shrink |
| `RotateCredentials` | `role: owner`, `publisher`, or `consumer` | Verifies adoption and old-password rejection on every broker; closes that user's existing connections |
| `Backup` | None | Exports the supported definitions below |

On qualified local-path plans, storage is a recorded allocation rather than a
hard directory limit. Expansion reserves additional logical allocation without
resizing the native PVC or moving data. Volume-backed plans require a storage
class that supports physical expansion. Changes use the service's maintenance
window unless an operation requests immediate execution.

Disk alarms monitor physical free space independently of recorded allocation.
On a shared filesystem, one broker's disk alarm can block publishers across its
cluster. Increasing logical allocation does not create free physical space.
Inspect service status and disk metrics when publishers block.

## Back up and restore definitions

`rabbitmq-definitions-v1` archives durable quorum/classic queue definitions,
standard exchanges and their bindings in the single application vhost.
**Queued messages are not backed up or restored.** Users, password hashes,
permissions, policies, runtime parameters and plugins are also excluded.
Unsupported queue types or transient queue definitions make this backup fail
explicitly rather than silently omitting them.

RestoreToNew creates an independent service on the **same plan** with fresh
credentials. It validates the archive and checks the imported topology and
quorum membership before completing. There is no point-in-time or message-data
recovery. See [backup and restore](managed-services-backup-restore.md) for the
common request and retained-backup expiry contracts.

Only `Delete` is offered initially: it removes the service and its message data.
Definitions backups remain recoverable until their recorded expiry. Neither
`Retain` nor `SnapshotAndDelete` is advertised for RabbitMQ.

## Monitor

The Metrics tab reports queue ready/unacknowledged/total messages, publish,
delivery and acknowledgment rates, connections, channels, memory/disk alarms,
physical disk availability, and broker CPU, memory and recorded storage. Status
checks exact broker membership and quorum queue replication independently of
Kubernetes Pod readiness.
