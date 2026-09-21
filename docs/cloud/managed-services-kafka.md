# Kafka

Kafka is an event streaming platform. Kube-DC offers it as the class `kafka`:
Apache Kafka 4.x in KRaft mode on the Strimzi operator, with brokers you size
and a controller quorum the platform runs for you. Producers and consumers
authenticate with SCRAM-SHA-512 over TLS.

In the console, pick the Kafka tile and a tier; the Size step's *Brokers*
slider sets the broker count. The manifest below creates the same service.

## Shapes

| | Dev tier | Production tier |
|---|---|---|
| Brokers | 1 (up to 3) | 3 (up to 9) |
| Controllers | 1, platform-owned | 3, platform-owned |
| Durability | Replication only | Replication only |

## Create a service

```yaml
apiVersion: services.kube-dc.com/v1alpha1
kind: ManagedService
metadata:
  name: team-events
  namespace: my-project
spec:
  classRef:
    name: kafka
  planRef:
    name: kafka-production
  placement:
    mode: ProviderShared
  connectivity:
    classRef:
      name: tenant-native
  topology:
    instances: 3           # brokers; the controllers are not counted here
  compute:
    cpu: "1"
    memory: 2Gi
  storage:
    size: 20Gi             # per broker
  parameters:
    kafka:
      parameters:
        num.partitions: "3"
        log.retention.hours: "72"
        compression.type: producer
    monitoring:
      level: PER_TOPIC_PER_BROKER
    rebalance:
      enabled: true
      autoOnScale: true
  deletionPolicy: Retain
  deletionProtection: true
```

```bash
kubectl apply --dry-run=server -f team-events.yaml
kubectl apply -f team-events.yaml
kubectl get managedservice team-events -n my-project -w
```

### Parameters

| Parameter | Meaning | Changes after creation |
|-----------|---------|------------------------|
| `kafka.parameters` | Allow-listed broker settings as strings, the cluster-configuration surface you know from managed Kafka elsewhere (partitions, retention, compression, message sizes, and so on). Listeners, advertised addresses, node identity, storage layout, security and the internal-topic replication factors are platform-owned and derived from the broker count | `OnlineDesired`, or `UpdateParameters` |
| `monitoring.level` | `DEFAULT`, `PER_BROKER`, `PER_TOPIC_PER_BROKER` or `PER_TOPIC_PER_PARTITION` | `OnlineDesired` |
| `rebalance.enabled`, `rebalance.autoOnScale` | Cruise Control partition rebalancing, and whether a rebalance follows every `Scale` | `OnlineDesired` |

Broker compute is chosen at creation on the standard plans (no `Resize`
entitlement). Storage grows through `ExpandStorage`; brokers scale through
`Scale`.

## Connect an application

Two credential roles: `admin`, which may manage topics and ACLs, and
`client`, for producers and consumers. Both use the `bootstrap` endpoint on
port `9093`.

```yaml
apiVersion: services.kube-dc.com/v1alpha1
kind: ServiceBinding
metadata:
  name: orders-producer
  namespace: my-project
spec:
  serviceRef:
    name: team-events
  serviceUID: REPLACE_WITH_SERVICE_UID
  role: client
  consumer:
    kind: ServiceAccount
    name: orders-producer
    namespace: my-project
  delivery:
    secretName: orders-producer
```

The platform verifies the credential against the cluster before it delivers
the `Secret`, which has these keys:

| Key | Value |
|-----|-------|
| `bootstrapServers` | `<host>:9093` |
| `securityProtocol` | `SASL_SSL` |
| `saslMechanism` | `SCRAM-SHA-512` |
| `username`, `password` | The login of the credential role |
| `saslJaasConfig` | A ready `org.apache.kafka.common.security.scram.ScramLoginModule` line for JVM clients |
| `ca.crt` | The CA that signs the broker certificates |
| `uri` | A connection URI for clients that take one |

A check from a pod in the Project with `kcat`:

```sh
kcat -b "$KAFKA_BOOTSTRAP_SERVERS" -X security.protocol=SASL_SSL \
  -X sasl.mechanism=SCRAM-SHA-512 -X sasl.username="$KAFKA_USER" \
  -X sasl.password="$KAFKA_PASSWORD" -X ssl.ca.location=/etc/kafka/ca.crt -L
```

The console's **Connect** card shows the same for `kcat`, the Java properties,
Python (`confluent_kafka`), Go (`franz-go`) and a `.env` file.

Keep a replication factor of at least 3 and `min.insync.replicas` of 2 on a
cluster of three or more brokers. New services default to one topic partition
per broker; consumer-group parallelism is bounded by partitions, not by
replicas.

## Day-2 operations

| Operation | What it does |
|-----------|--------------|
| `Scale` | Changes the broker count within the plan's bounds; with `rebalance.autoOnScale`, partitions are rebalanced afterwards |
| `ExpandStorage` | Grows every broker's volume; never shrinks |
| `UpdateParameters` | Changes allow-listed broker settings |
| `RotateCredentials` | New password for `admin` or `client`, republished to every binding of that role |
| `MinorUpgrade` | Moves to another patch of the same release line, by `version` |
| `MajorUpgrade` | Moves to the next release line. KRaft metadata finalisation is one-way: there is no downgrade |
| `Backup` | Exports metadata: topics, configuration, ACLs, client quotas and committed consumer-group offsets |
| `Custom` | Maintenance actions by `parameters.kind`: `RebootNode`, `Rebalance`, `CreateTopic`, `UpdateTopic`, `DeleteTopic`, `SetQuotas` |

Every type must be in your plan's `operations.allowed`. Topics can also be
created by clients with the `admin` credential.

## Backups

**There is no backup of message data.** Durability comes from replication
across brokers; size your replication factor and `min.insync.replicas`
accordingly and mirror to another cluster if you need a copy elsewhere. A
`Backup` operation exports the cluster's metadata (topics, configuration,
ACLs, quotas, committed offsets) so that a cluster can be rebuilt with the
same shape; it does not restore records, and there is no `RestoreToNew` for
this family.

## Limits

| | |
|---|---|
| Versions | 4.2 and 4.3 release lines, KRaft only |
| High availability | Production plan: three controllers and at least three brokers; a broker loss is covered by topic replication |
| Sizing after creation | Brokers and storage; broker compute is fixed |
| Message backup | None |
| Exposure | Inside the Project only |
