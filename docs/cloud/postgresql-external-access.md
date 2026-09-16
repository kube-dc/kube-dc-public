# External Access

A PostgreSQL service is reachable from inside your Project through its internal
endpoints; see [Connect Applications](postgresql-connect.md). This page covers
the one external access option this chapter documents: the **PostgreSQL
direct-TLS Gateway**.

With Gateway access, a provider-operated shared TLS listener routes connections
to your service by TLS server name. Your client connects to a stable host name,
starts TLS immediately, and verifies the PostgreSQL server certificate with the
service's own CA. The platform allocates no dedicated IP address for it, and
your internal endpoints stay unchanged.

:::info Other external access options
Other external access options depend on your provider. They are not covered in
this chapter.
:::

## Before you begin

- A PostgreSQL service in the in-Project placement this chapter covers
  (`placement.mode: ProviderShared`). See
  [Create a PostgreSQL Service](postgresql-create.md).
- PostgreSQL major version 17 or 18. The version comes from your plan's
  `engineVersion`.
- A plan whose `exposure.gateway` field is set. You cannot read the plan; ask
  your provider whether your plan has Gateway access.
- The `admin` or `developer` role to change the service and create bindings.
  See [Project roles](managed-services.md#project-roles).
- A client that supports PostgreSQL direct TLS negotiation. See
  [Client requirements](#client-requirements).

Throughout this page, replace `my-project` with your Project's backing
namespace.

## Plan entitlement

| Plan field | Effect |
|------------|--------|
| `exposure.gateway` | When set, services on the plan may request `parameters.expose.type: gateway`. The provider sets the Gateway listener, its port and the DNS suffix of the host names in this field. When it is absent, the request is refused |

You cannot choose the host name, the listener or the port. The service
parameters have no fields for them.

## Enable Gateway access

Set `parameters.expose.type` to `gateway` in the complete `ManagedService`
manifest of an existing service, and apply it again. The parameter is
`OnlineDesired`, so the change needs no operation. Keep every other field
unchanged.

This example shows the manifest of `orders-db` with Gateway access. Save it as
`orders-db.yaml`:

This is the `orders-db` manifest from
[Create a PostgreSQL Service](postgresql-create.md) with only `expose.type`
added. Keep every other field exactly as you applied it: applying a manifest
that drops a create-only or operation-only field requests its removal, and
that change is refused.

```yaml
apiVersion: services.kube-dc.com/v1alpha1
kind: ManagedService
metadata:
  name: orders-db
  namespace: my-project
spec:
  classRef:
    name: postgresql
  planRef:
    # The plan must allow exposure.gateway and use engineVersion 17 or 18.
    name: postgresql-platform-ha
  placement:
    mode: ProviderShared
  connectivity:
    classRef:
      name: tenant-native
  parameters:
    database: orders
    owner: orders
    instances: 2
    storage:
      size: 10Gi
    readonlyRole: true
    backup:
      enabled: true
    postgresql:
      parameters:
        work_mem: 8MB
    # The only change from the manifest you created:
    expose:
      type: gateway
  deletionPolicy: Retain
  deletionProtection: true
```

```bash
kubectl apply --dry-run=server -f orders-db.yaml
kubectl apply -f orders-db.yaml
kubectl get managedservice orders-db -n my-project -w
```

Wait until the service is `Ready` and its configuration is current, as
described in [Is the result current?](postgresql-create.md#is-the-result-current)

Turning Gateway access on for an existing service has been qualified. Setting
`expose.type: gateway` in the first manifest of a new service is not yet
qualified.

When the plan or the service does not allow Gateway access, the service reports
`Accepted=False` with reason `ParameterSchemaRejected`, and the message names
`spec.parameters.expose.type`:

| Message contains | Cause |
|------------------|-------|
| `the plan does not allow a qualified Gateway` | Your plan does not set `exposure.gateway` |
| `gateway requires PostgreSQL 17 or later and direct-TLS clients` | The service's PostgreSQL major version is not 17 or 18 |
| `gateway requires in-Project placement` | The service does not run in your Project's namespace |

## Host name and port

The service is published under a host name of this form:

```text
kdc-managed-<hash>.<dns-suffix>
```

- `<hash>` is derived from the service's `metadata.uid`, and `<dns-suffix>`
  comes from your provider's plan. Do not build the name yourself; read it
  from the binding's `host` key or from the service status.
- The host name stays the same for the life of the service UID, including when
  you turn Gateway access off and on again. A new service with the same name
  has a different UID and therefore a different host name.
- The host name is also a name in the server certificate, so clients can verify
  it.
- The port is the port of the provider's Gateway listener, not necessarily
  `5432`. Read it from the binding's `port` key.
- Host names that start with `kdc-managed-` are reserved for the platform.
  Gateway API routes, Ingresses and Service route annotations in your Project
  cannot claim them.

Read the published endpoint from the service status:

```bash
kubectl get managedservice orders-db -n my-project \
  -o jsonpath='{range .status.endpoints[?(@.name=="gateway")]}{.addresses[*].host}{"\t"}{.addresses[*].port}{"\t"}negotiation={.tls.negotiation}{"\t"}{.verification}{"\n"}{end}'
```

The `gateway` endpoint connects to the current primary. Its `tls.negotiation`
is `direct`. `verification: Verified` means the platform completed a verified
TLS connection and a SQL query through the Gateway listener from inside the
platform. It does not prove that your network can reach the listener. Until
the route is ready, the endpoint has no addresses and `lastProbe.message` says
what it is waiting for.

## Create a binding over the Gateway

Create a `ServiceBinding` with `delivery.endpointName: gateway`. Both the
`owner` and `readonly` roles work over this endpoint. This example delivers the
`readonly` credential. Save it as `orders-db-gateway.yaml`:

```yaml
apiVersion: v1
kind: ServiceAccount
metadata:
  name: orders-reporting
  namespace: my-project
automountServiceAccountToken: false
---
apiVersion: services.kube-dc.com/v1alpha1
kind: ServiceBinding
metadata:
  name: orders-db-gateway
  namespace: my-project
spec:
  serviceRef:
    name: orders-db
  # The service's metadata.uid.
  serviceUID: REPLACE_WITH_SERVICE_UID
  role: readonly
  consumer:
    kind: ServiceAccount
    name: orders-reporting
    namespace: my-project
  delivery:
    secretName: orders-db-gateway
    endpointName: gateway
```

```bash
kubectl apply -f orders-db-gateway.yaml
kubectl get servicebinding orders-db-gateway -n my-project -w
```

The binding is ready when `READY` is `True` with reason `CredentialDelivered`.
A binding can be delivered before the service itself is `Ready`; wait for both
before you connect. The other binding fields work as for internal bindings; see
[Create a binding](postgresql-connect.md#create-a-binding).

### Secret keys

The delivered `Secret` has the keys of an internal binding, with Gateway values,
plus two more keys:

| Key | Value |
|-----|-------|
| `host` | The Gateway host name of the service |
| `port` | The Gateway listener port |
| `dbname`, `username`, `password` | As for internal bindings |
| `sslmode` | `verify-full` |
| `sslnegotiation` | `direct` |
| `tls-server-name` | The same value as `host`: the TLS server name to send and to verify |
| `ca.crt` | The CA certificate that signs the PostgreSQL server certificate. It is the service's own CA, not a public web CA |
| `uri` | `postgresql://<username>:<password>@<host>:<port>/<dbname>?sslmode=verify-full&sslnegotiation=direct` |

The `Secret` also carries the annotation
`services.kube-dc.com/credential-version`. Check which keys were delivered
without printing their values:

```bash
kubectl describe secret orders-db-gateway -n my-project
```

## Client requirements

The Gateway selects the service by the TLS server name in the first packet of
the connection. Your client must therefore start with a TLS handshake instead
of PostgreSQL's usual in-protocol SSL request:

| Setting | libpq parameter | libpq environment variable |
|---------|-----------------|----------------------------|
| Direct TLS negotiation | `sslnegotiation=direct` | `PGSSLNEGOTIATION=direct` |
| Full certificate verification | `sslmode=verify-full` | `PGSSLMODE=verify-full` |
| The delivered CA | `sslrootcert=<path to ca.crt>` | `PGSSLROOTCERT` |
| The delivered host name | `host=<host>` | `PGHOST` |

- Use libpq 17 or newer, for example `psql` 17, or another driver that supports
  PostgreSQL direct TLS negotiation. libpq 17 with the settings above, and the
  Go driver pgx v5.10.0 configured for direct TLS, have connected through the
  Gateway.
- libpq sends the host name as the TLS server name by default. Do not set
  `sslsni=0`.
- For a driver that is not based on libpq, set its TLS server name to
  `tls-server-name`, trust `ca.crt`, and enable direct TLS. PostgreSQL requires
  such clients to offer the ALPN protocol `postgresql`; see the
  [PostgreSQL connection documentation](https://www.postgresql.org/docs/17/libpq-connect.html)
  and the
  [protocol documentation](https://www.postgresql.org/docs/17/protocol-flow.html).

What does not work:

- **A client that uses standard negotiation.** libpq 16 or older, or libpq 17
  without `sslnegotiation=direct`, sends PostgreSQL's SSL request first. The
  Gateway cannot route that connection, and it fails. Depending on the client,
  the error can look like a closed connection or a timeout.
- **A client that does not know `sslnegotiation`.** Such a client either
  rejects the option, for example in the `uri` value, or ignores the
  environment variable and uses standard negotiation. Either way it cannot
  connect. Upgrade the client.
- **An IP address or another host name.** The Gateway routes by the delivered
  host name, and `verify-full` checks it.
- **A weaker `sslmode`.** Do not work around a verification error by lowering
  `sslmode`. Fix the CA path or the host name instead.

## Connect from a workstation

This example runs `psql` 17 or newer on a workstation that can reach the
Gateway listener. It reads the binding `Secret` with your Project kubeconfig
without printing the password:

```bash
SECRET=orders-db-gateway
NS=my-project
secret_key() {
  kubectl get secret "$SECRET" -n "$NS" -o jsonpath="{.data.$1}" | base64 -d
}
secret_key 'ca\.crt' > orders-db-ca.crt
export PGHOST="$(secret_key host)"
export PGPORT="$(secret_key port)"
export PGDATABASE="$(secret_key dbname)"
export PGUSER="$(secret_key username)"
export PGPASSWORD="$(secret_key password)"
export PGSSLMODE=verify-full
export PGSSLNEGOTIATION=direct
export PGSSLROOTCERT="$PWD/orders-db-ca.crt"
psql --no-password -c 'SELECT current_database(), current_user;'
unset PGPASSWORD
```

Reading Secrets needs a Project role that can read them; `user` cannot. Do not
print, log or commit the `password` or `uri` values.

For a workload in your Project, use the
[workload example](postgresql-connect.md#workload-example) with the Gateway
binding's `Secret`, and add the negotiation setting:

```yaml
env:
  - name: PGSSLNEGOTIATION
    valueFrom:
      secretKeyRef:
        name: orders-db-gateway
        key: sslnegotiation
```

## Switchover and failover

The Gateway route always targets the current primary. After a planned
`Switchover`, new connections through the same host name, port and binding
`Secret` reach the new primary; the host name and the credential do not change.
Existing connections close during the switchover, so applications must
reconnect and retry. See
[Switchover and failover](postgresql-operations.md#switchover-and-failover).

A forced `Failover` and an unplanned primary outage while clients connect
through the Gateway are not yet qualified.

## Disable Gateway access

1. Move clients off the Gateway endpoint.
2. Delete every binding whose `delivery.endpointName` is `gateway`, and wait
   until its `Secret` is gone:

   ```bash
   kubectl delete servicebinding orders-db-gateway -n my-project
   ```

3. Set `parameters.expose.type: internal` in the complete `ManagedService`
   manifest and apply it.

The platform removes the Gateway route. The internal endpoints, the data and
the internal bindings do not change. Connections through the Gateway may end.
A client that copied the credential still has the password; rotate the
credential if it must stop working. See
[Credentials and Rotation](postgresql-credentials.md).

While a binding still uses the `gateway` endpoint, the change is refused: the
service reports `Accepted=False` with reason `ParameterSchemaRejected` and the
message `a ServiceBinding selects the gateway endpoint; delete it or repoint it
to read-write before disabling gateway access`, and Gateway access stays on.
Delete those bindings first.

To turn Gateway access on again, set `expose.type: gateway` and apply. The
service gets the same host name, and you create new bindings over the
`gateway` endpoint.

## Remove it together with the service

What happens to Gateway access when you delete the `ManagedService` depends on
its `deletionPolicy`:

| `deletionPolicy` | Gateway access |
|------------------|----------------|
| `Delete` | The Gateway route and the delivered Gateway binding `Secret` objects are removed together with the engine |
| `Retain` | The platform stops managing the service, and the resources it created, including the Gateway route, are kept. Gateway behaviour under `Retain` is not yet qualified. Turn off Gateway access before you delete the service |
| `SnapshotAndDelete` | Not yet qualified with Gateway access. Turn off Gateway access before you delete the service |

See [Status and Deletion](managed-services-status-deletion.md).

## Limits

These cases are not yet qualified. Do not rely on them without testing, and ask
your provider if you need them:

- A forced `Failover`, or an unplanned outage of the primary, while clients
  connect through the Gateway.
- Deleting a service with `SnapshotAndDelete` while Gateway access is on.
- The remaining failure and recovery cases of the Gateway listener and its
  route.

The endpoint's `verification` shows only that the platform reached the Gateway
listener. Test the connection from the networks your clients use.
