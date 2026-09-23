# Connect applications

An application connects to a PostgreSQL `ManagedService` through a
`ServiceBinding`. The binding asks the platform to deliver one credential role
of the service as a Kubernetes `Secret` in your Project. Your workload reads
the connection details from that `Secret` and connects over TLS with full
certificate verification.

## Before you begin

- A PostgreSQL service that is `Ready` with a current configuration. See
  [Create a PostgreSQL service](postgresql-create.md#read-the-status).
- The service UID:

  ```bash
  kubectl get managedservice orders-db -n my-project -o jsonpath='{.metadata.uid}{"\n"}'
  ```

- The `admin` or `developer` role to create bindings. See
  [Project roles](managed-services.md#project-roles).

## Credential roles

A binding names a credential role, not a SQL user:

| Role | SQL login | Available when |
|------|-----------|----------------|
| `owner` | The login named by `parameters.owner`, which owns the application database | Always |
| `readonly` | A login with the `pg_read_all_data` privilege | The service was created with `readonlyRole: true`, the default |
| `status.credentialRole` of a `ServiceCredentialPolicy` | An existing SQL login that a database administrator created and the policy manages | The plan allows `credentials.allowExistingUsers` |

All bindings of the same role share one credential. Creating another binding
does not create another SQL user, and rotating a role changes the password
delivered to every binding of that role.

## Endpoints

A binding also selects one endpoint of the service:

| Endpoint name | Connects to | Available when |
|---------------|-------------|----------------|
| `read-write` | The current primary. After a switchover or failover it follows the new primary | Always. This is the default |
| `read-only` | The replicas | Declared for every service; only a service with two or more instances has a replica behind it |
| `pooled` | PgBouncer in front of the primary | `parameters.pooler.enabled: true` |

Other endpoint names can appear in `status.endpoints`, depending on your plan
and the service's exposure settings. They are not covered on this page. For the
`gateway` endpoint, see [External access](postgresql-external-access.md).

List the endpoints the service publishes and whether each one passed its last
probe:

```bash
kubectl get managedservice orders-db -n my-project \
  -o jsonpath='{range .status.endpoints[*]}{.name}{"\t"}{.addresses[*].host}{"\t"}{.verification}{"\n"}{end}'
```

- A binding over `read-only` has not yet been qualified end to end. Use
  `read-write` for applications, as the examples on this page do, and ask your
  provider before you depend on `read-only`. If you try it, check that its
  `verification` is `Verified`; with a single instance there is no replica.
- The endpoint and the role are independent. An `owner` credential on the
  `read-only` endpoint is still the owner; use the `readonly` role for
  read-only access.
- In `pooled` endpoint `transaction` mode, session state does not survive
  between transactions. See the `pooler` parameters in
  [Create a PostgreSQL service](postgresql-create.md#parameter-reference).
- Do not build host names yourself. Use the `host` key the binding delivers.

## Create a binding

This example gives an `orders-api` workload the `owner` credential over the
`read-write` endpoint. Save it as `orders-api-db.yaml`:

```yaml
apiVersion: v1
kind: ServiceAccount
metadata:
  name: orders-api
  namespace: my-project
automountServiceAccountToken: false
---
apiVersion: services.kube-dc.com/v1alpha1
kind: ServiceBinding
metadata:
  name: orders-api-db
  namespace: my-project
spec:
  serviceRef:
    name: orders-db
  # Optional. The service's metadata.uid, when you know it.
  serviceUID: REPLACE_WITH_SERVICE_UID
  role: owner
  consumer:
    kind: ServiceAccount
    name: orders-api
    namespace: my-project
  delivery:
    secretName: orders-api-db
    endpointName: read-write
```

Leave `serviceUID` out when you apply the binding alongside the service it
names, from a manifest set or a GitOps repository: you cannot know a UID for a
service that does not exist yet. The platform then records the instance the name
resolved to in `status.serviceRef`, and that recording pins the binding from
then on — if the service is later deleted and a new one takes its name, the
binding is refused rather than re-pointed, exactly as a `serviceUID` you set
yourself would be. Set it when you already hold the UID, which is what the
console does.

```bash
kubectl apply -f orders-api-db.yaml
kubectl get servicebinding orders-api-db -n my-project -w
```

| Field | Description |
|-------|-------------|
| `spec.serviceRef.name` | The `ManagedService` name. Cannot change |
| `spec.serviceUID` | The `ManagedService` UID. Optional; omit it when the service does not exist yet. Cannot change |
| `spec.role` | The credential role. Cannot change |
| `spec.consumer.kind`, `spec.consumer.name` | The workload identity the credential is for, normally a `ServiceAccount` |
| `spec.consumer.namespace` | Must be the Project namespace. Defaults to it |
| `spec.delivery.secretName` | The `Secret` to create. Defaults to the binding name. It must not already exist unless this binding created it |
| `spec.delivery.endpointName` | The endpoint embedded in the `Secret`. Defaults to `read-write` |
| `spec.delivery.mode` | Leave unset. The default is the only supported mode |

The binding is ready when `READY` is `True` with reason `CredentialDelivered`,
the condition's `observedGeneration` equals the binding's
`metadata.generation`, and `status.secretRef` names the delivered `Secret`.

### Binding problems

| Reason or message on the `Ready` condition | Meaning and fix |
|--------------------------------------------|-----------------|
| `CredentialPending` | Not delivered yet: the service is missing or not placed, or delivery is still in progress. The message says which. If the service does not exist, check `serviceRef.name` |
| `ServiceIdentityChanged` | The service was deleted and recreated under the same name. Create a new binding with the new UID |
| `ParameterSchemaRejected`: `role "..." is not declared by class postgresql (...)` | The class does not define that role name, for example a misspelled role |
| `role "..." is not declared by the accepted revision` | The role is not available on this service, for example `readonly` on a service created with `readonlyRole: false` |
| `ConsumerNamespaceRejected` | The consumer namespace is not the Project namespace |
| `endpoint "..." is not declared by the accepted revision` | The endpoint does not exist on this service, for example `pooled` while the pooler is off |
| `target secret ... exists and is not owned by this instance and binding` | A `Secret` with that name already exists. Choose another `secretName` |

## Secret keys

The delivered `Secret` has type `Opaque` and these keys:

| Key | Value |
|-----|-------|
| `host` | The host name of the selected endpoint. The server certificate is issued for it |
| `port` | The endpoint port, `5432` |
| `dbname` | The application database |
| `username` | The login of the credential role |
| `password` | The current password |
| `sslmode` | `verify-full` |
| `ca.crt` | The CA certificate that signs the server certificate |
| `uri` | A connection URI: `postgresql://<username>:<password>@<host>:<port>/<dbname>?sslmode=verify-full` |

The `Secret` also carries the annotation
`services.kube-dc.com/credential-version`, the credential version it holds.

Check which keys were delivered without printing their values:

```bash
kubectl describe secret orders-api-db -n my-project
```

Do not print, log or commit the `password` or `uri` values.

## Use TLS with certificate verification

Always connect with `sslmode=verify-full` and trust the delivered `ca.crt`.
For libpq-based clients, including `psql`, set:

- `PGSSLMODE=verify-full`
- `PGSSLROOTCERT` to the path of the mounted `ca.crt` file

Keep the delivered `host` value. Connecting through an IP address or a
different name fails verification. Do not work around a verification error by
lowering `sslmode` to `require`; that turns off the check that you are talking
to your own database.

The `uri` key includes `sslmode=verify-full` but not the CA path. When you use
it with libpq, also set `PGSSLROOTCERT`. For other drivers, configure their
equivalent CA or trust-store setting.

## Workload example

This Deployment reads the connection details from the `Secret` and mounts
`ca.crt` as a file:

```yaml
apiVersion: apps/v1
kind: Deployment
metadata:
  name: orders-api
  namespace: my-project
spec:
  replicas: 2
  selector:
    matchLabels:
      app: orders-api
  template:
    metadata:
      labels:
        app: orders-api
    spec:
      serviceAccountName: orders-api
      automountServiceAccountToken: false
      containers:
        - name: api
          image: ghcr.io/example/orders-api:1.0
          env:
            - name: PGHOST
              valueFrom:
                secretKeyRef:
                  name: orders-api-db
                  key: host
            - name: PGPORT
              valueFrom:
                secretKeyRef:
                  name: orders-api-db
                  key: port
            - name: PGDATABASE
              valueFrom:
                secretKeyRef:
                  name: orders-api-db
                  key: dbname
            - name: PGUSER
              valueFrom:
                secretKeyRef:
                  name: orders-api-db
                  key: username
            - name: PGPASSWORD
              valueFrom:
                secretKeyRef:
                  name: orders-api-db
                  key: password
            - name: PGSSLMODE
              value: verify-full
            - name: PGSSLROOTCERT
              value: /var/run/postgresql-ca/ca.crt
          volumeMounts:
            - name: postgresql-ca
              mountPath: /var/run/postgresql-ca
              readOnly: true
          resources:
            requests:
              cpu: 100m
              memory: 128Mi
            limits:
              cpu: 500m
              memory: 256Mi
      volumes:
        - name: postgresql-ca
          secret:
            secretName: orders-api-db
            items:
              - key: ca.crt
                path: ca.crt
```

### Check a connection

Run a one-off Job that uses the same `Secret` and prints only the database and
user names. Use any image that contains `psql` and can run as a non-root user:

```yaml
apiVersion: batch/v1
kind: Job
metadata:
  name: orders-db-connection-check
  namespace: my-project
spec:
  backoffLimit: 0
  activeDeadlineSeconds: 120
  template:
    spec:
      serviceAccountName: orders-api
      automountServiceAccountToken: false
      restartPolicy: Never
      securityContext:
        runAsNonRoot: true
        runAsUser: 10001
        runAsGroup: 10001
      containers:
        - name: psql
          # Replace with an image that contains psql.
          image: registry.example.com/tools/psql:17
          command: ["psql", "--no-password", "-v", "ON_ERROR_STOP=1", "-c", "SELECT current_database(), current_user;"]
          env:
            - name: PGHOST
              valueFrom:
                secretKeyRef:
                  name: orders-api-db
                  key: host
            - name: PGPORT
              valueFrom:
                secretKeyRef:
                  name: orders-api-db
                  key: port
            - name: PGDATABASE
              valueFrom:
                secretKeyRef:
                  name: orders-api-db
                  key: dbname
            - name: PGUSER
              valueFrom:
                secretKeyRef:
                  name: orders-api-db
                  key: username
            - name: PGPASSWORD
              valueFrom:
                secretKeyRef:
                  name: orders-api-db
                  key: password
            - name: PGSSLMODE
              value: verify-full
            - name: PGSSLROOTCERT
              value: /var/run/postgresql-ca/ca.crt
          volumeMounts:
            - name: postgresql-ca
              mountPath: /var/run/postgresql-ca
              readOnly: true
          resources:
            requests:
              cpu: 100m
              memory: 128Mi
            limits:
              cpu: 250m
              memory: 256Mi
      volumes:
        - name: postgresql-ca
          secret:
            secretName: orders-api-db
            items:
              - key: ca.crt
                path: ca.crt
```

```bash
kubectl apply -f orders-db-connection-check.yaml
kubectl wait --for=condition=Complete --timeout=150s job/orders-db-connection-check -n my-project
kubectl logs job/orders-db-connection-check -n my-project
```

A failed or timed-out Job is a failed check. Fix the cause instead of turning
off TLS verification. Delete the Job before you run the check again.

## Rotation and restarts

A credential changes when a `RotateCredentials` operation completes, either
one you create or one generated by a `ServiceCredentialPolicy`. When a
rotation completes:

- The binding's `status.credentialVersion` increases, and the platform
  rewrites the `Secret` in place under the same name.
- Every binding of the rotated role receives the new password.
- After a successful rotation, fresh-login checks verify the new password and
  refuse the old password. Applications must reload their credentials and
  reconnect.

Running containers do not pick up a new password on their own:

- Values injected through `env` or `envFrom` never change in a running
  container. Restart the workload after the rotation, for example with
  `kubectl rollout restart deployment/orders-api -n my-project`.
- Files from a mounted `Secret` are refreshed after a delay. The application
  must reread them and open new connections.

To rotate the `owner` credential now, create an operation. Your plan must
include `RotateCredentials` in `operations.allowed`; if it is not also in
`operations.autoApprove`, the operation waits in `AwaitingApproval` until your
provider approves it.

```yaml
apiVersion: services.kube-dc.com/v1alpha1
kind: ServiceOperation
metadata:
  name: orders-db-rotate-owner-1
  namespace: my-project
spec:
  serviceRef:
    name: orders-db
  # The service's metadata.uid.
  serviceUID: REPLACE_WITH_SERVICE_UID
  type: RotateCredentials
  idempotencyKey: orders-db-rotate-owner-1
  parameters:
    role: owner
  execution:
    window: Immediate
```

Then wait for the operation to reach `Succeeded`, confirm that the binding's
credential version increased, and restart the workloads that use the role:

```bash
kubectl get serviceoperation orders-db-rotate-owner-1 -n my-project -w
kubectl get servicebinding orders-api-db -n my-project -o jsonpath='{.status.credentialVersion}{"\n"}'
kubectl rollout restart deployment/orders-api -n my-project
```

To rotate on a schedule, create a `ServiceCredentialPolicy`. Each scheduled
rotation is a `RotateCredentials` operation, so the same entitlement applies:
without it the policy reports `RotationNotEntitled` and rotates nothing, and a
plan that requires approval still requires it for every scheduled rotation. The
interval is in seconds, from 60 to 31536000, and defaults to 30 days:

```yaml
apiVersion: services.kube-dc.com/v1alpha1
kind: ServiceCredentialPolicy
metadata:
  name: orders-db-owner-rotation
  namespace: my-project
spec:
  serviceRef:
    name: orders-db
  # The service's metadata.uid.
  serviceUID: REPLACE_WITH_SERVICE_UID
  role: owner
  rotationIntervalSeconds: 2592000
  paused: false
```

A policy only schedules rotations; your applications still need the restart or
reload described above. Rotations the policy creates are subject to the plan's
approval rules, and tenants cannot cancel them.

Deleting a policy stops future rotations. A rotation the policy has already
submitted still runs to its outcome. What else happens depends on the target:

- For a declared role (`owner` or `readonly`), deleting the policy does not
  delete bindings, the Secrets they delivered, or the SQL login.
- For an existing SQL login (`existingUser`), retiring the policy withdraws the
  Secrets projected for its credential role; bindings of that role remain with
  `Ready=False`. The source credential and the SQL login remain.

Plan for other connection interruptions too. A switchover, failover, restart
for a changed setting, or instance replacement closes existing connections.
Applications should reconnect and retry.

## Remove a binding

After you delete a `ServiceBinding`, the platform removes the `Secret` it
delivered. The SQL login,
the credential and other bindings of the same role are not affected. Move your
workloads to another `Secret` before you delete a binding they still use.

```bash
kubectl delete servicebinding orders-api-db -n my-project
```
