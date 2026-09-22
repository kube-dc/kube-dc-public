# Credentials and rotation

This page covers how PostgreSQL credentials change after a service is created:
rotating a credential on demand with a `RotateCredentials` operation, rotating
on a schedule with a `ServiceCredentialPolicy`, managing the password of an
existing SQL login, reading credential status without a binding, and the
break-glass superuser entitlement.

To deliver a credential to an application, see
[Connect applications](postgresql-connect.md).

## Before you begin

- A PostgreSQL service that is `Ready` with a current configuration. See
  [Create a PostgreSQL service](postgresql-create.md#read-the-status).
- The service UID:

  ```bash
  kubectl get managedservice orders-db -n my-project -o jsonpath='{.metadata.uid}{"\n"}'
  ```

- The `admin` or `developer` role to create operations and credential
  policies. `project-manager` can only update existing policies, which means
  pausing, resuming or changing the interval, and `user` can only read. See
  [Project roles](managed-services.md#project-roles).
- Your plan must allow `RotateCredentials` in `operations.allowed`. If the
  plan does not also list it in `operations.autoApprove`, every rotation, manual
  or scheduled, waits for approval by your provider.

Throughout this page, replace `my-project` with your Project's backing
namespace.

## Credential roles

A rotation acts on a credential role, not on a SQL user name:

| Role | SQL login | Available when |
|------|-----------|----------------|
| `owner` | The login named by `parameters.owner` | Always |
| `readonly` | The `readonly` login | The service was created with `readonlyRole: true`, the default |
| The policy's `status.credentialRole`, for example `existing-0123456789abcdef01234567` | An existing SQL login that a `ServiceCredentialPolicy` manages | Your plan must allow `credentials.allowExistingUsers` |

All bindings of a role share one credential. Rotating a role changes the
password for every binding of that role.

## Read credential status

The service reports each credential role in `status.credentials`, with a
version and a fingerprint but never the value:

```bash
kubectl get managedservice orders-db -n my-project \
  -o jsonpath='{range .status.credentials[*]}{.role}{"\t"}version={.version}{"\t"}secret={.secretName}{"\t"}rotatedAt={.rotatedAt}{"\n"}{end}'
```

| Field | Meaning |
|-------|---------|
| `role` | The credential role |
| `secretName` | The platform-managed `Secret` of the role, in the namespace named by `status.instanceNamespace` |
| `version` | Increases on every rotation |
| `fingerprint` | A truncated hash of the credential, never the value |
| `rotatedAt` | When the credential last changed |

A binding reports the version it delivered in `status.credentialVersion`.

A Project identity that can read Secrets can read the role's `Secret` without a
binding; `user` cannot read Secrets. Before you use the reference, check that
the service UID is the one you expect and that its configuration is current.
Check which keys exist without printing values:

```bash
kubectl describe secret "$(kubectl get managedservice orders-db -n my-project \
  -o jsonpath='{.status.credentials[?(@.role=="owner")].secretName}')" -n my-project
```

For applications, prefer a `ServiceBinding`, which delivers a stable `Secret`
name with the connection details and CA certificate. Do not try to edit or
delete the platform-managed `Secret`: the platform refuses changes to objects
it manages from Project identities.

## Rotate a credential now

Create a `RotateCredentials` operation. This example rotates the `readonly`
credential of `orders-db`:

```yaml
apiVersion: services.kube-dc.com/v1alpha1
kind: ServiceOperation
metadata:
  name: orders-db-rotate-readonly-1
  namespace: my-project
spec:
  serviceRef:
    name: orders-db
  # The service's metadata.uid.
  serviceUID: REPLACE_WITH_SERVICE_UID
  type: RotateCredentials
  idempotencyKey: orders-db-rotate-readonly-1
  parameters:
    role: readonly
  execution:
    window: Immediate
```

```bash
kubectl apply -f orders-db-rotate-readonly-1.yaml
kubectl get serviceoperation orders-db-rotate-readonly-1 -n my-project -w
```

| Parameter | Description |
|-----------|-------------|
| `role` | `owner`, `readonly`, or the `status.credentialRole` of an existing-user policy. If you omit it, `owner` is rotated. Always set it |
| `resyncRootUID` | Only for an existing-user role after an in-place restore. See [After a restore](#after-a-restore) |

How the rotation runs:

- **`owner` and `readonly`.** The platform writes a new password into the role's
  credential `Secret`, waits until the database has applied it, and verifies a
  connection with the new credential.
- **An existing login.** The platform records the pending password and the
  login's identity, changes only that login's password, and verifies a fresh
  TLS login before it publishes the new credential.

A successful operation reports a message such as
`readonly credential rotated to version 3 and verified`, or
`existing-user credential rotated to version 2 and verified` for an existing
login.

After a successful rotation, fresh-login checks verify the new password and
refuse the old password. Applications must reload their credentials and
reconnect:

- Values injected through `env` never change in a running container, so restart
  those workloads.
- Files from a mounted `Secret` are refreshed after a delay, and the application
  must reread them.

See [Rotation and restarts](postgresql-connect.md#rotation-and-restarts).

Other points:

- The login name, its grants and the `Secret` names do not change.
- Bindings of the role deliver the new credential version; check their
  `status.credentialVersion`.
- Applying the same manifest again does not rotate again. For another rotation,
  create an operation with a new name and a new `idempotencyKey`.
- A rotation of an `existing-...` role is `Rejected` with `PlanNotEntitled` when
  the operation omits `serviceUID`, or when no existing-user policy of this
  service owns that role, for example because the policy is absent or being
  deleted.
- A rotation of any other role is `Rejected` with `InvalidParameters` when the
  role is not a rotatable role of the service, for example `readonly` on a
  service created with `readonlyRole: false`.
- A rotation is `Rejected` with `ClusterHibernated` while the service is
  hibernated.

See [Day-2 Operations](postgresql-operations.md) for phases, approval and
refusal reasons.

## Rotate on a schedule

A `ServiceCredentialPolicy` creates `RotateCredentials` operations for one
credential target at a fixed interval. It never holds a password, and it never
creates a SQL login or changes privileges.

### Declared role

```yaml
apiVersion: services.kube-dc.com/v1alpha1
kind: ServiceCredentialPolicy
metadata:
  name: orders-db-owner-rotation
  namespace: my-project
spec:
  serviceRef:
    name: orders-db
  # The service's metadata.uid. Required.
  serviceUID: REPLACE_WITH_SERVICE_UID
  role: owner
  # 30 days.
  rotationIntervalSeconds: 2592000
  paused: false
```

```bash
kubectl apply -f orders-db-owner-rotation.yaml
kubectl get servicecredentialpolicy orders-db-owner-rotation -n my-project
```

| Field | Required | Description |
|-------|----------|-------------|
| `spec.serviceRef.name` | Yes | The `ManagedService` name. Cannot change |
| `spec.serviceUID` | Yes | The `ManagedService` UID. Cannot change |
| `spec.role` | One of `role` or `existingUser` | `owner` or `readonly`. Values starting with `existing-` or `sql-user:` are refused. Cannot change |
| `spec.existingUser.username`, `spec.existingUser.database` | One of `role` or `existingUser` | An existing SQL login and a database it may connect to. See [Existing SQL login](#existing-sql-login). Cannot change |
| `spec.rotationIntervalSeconds` | No | Seconds between rotations, from `60` to `31536000`. Default `2592000` (30 days). Can change |
| `spec.paused` | No | While `true`, no new rotation is submitted. Can change |

Only one policy can own a target of a service. A second policy for the same
target reports `CredentialPolicyConflict` and submits nothing.

### Existing SQL login

A policy can take over the password of a login that a database administrator
created, such as a reporting user.

Your plan must allow `credentials.allowExistingUsers`, in addition to
`RotateCredentials` in `operations.allowed`. Without
`credentials.allowExistingUsers`, the policy reports `ExistingUserNotEntitled`.

The login must already exist. The platform refuses to rotate a login that:

- cannot log in, is a superuser, or has the `CREATEROLE`, `REPLICATION` or
  `BYPASSRLS` attribute;
- has a connection limit of `0` or has expired;
- cannot connect to the database named in `existingUser.database`.

A policy is also refused, with `InvalidCredentialTarget`, when it names the
service's owner or `readonly` login or a login name the platform reserves, such
as `postgres`.

```yaml
apiVersion: services.kube-dc.com/v1alpha1
kind: ServiceCredentialPolicy
metadata:
  name: orders-db-reporting-rotation
  namespace: my-project
spec:
  serviceRef:
    name: orders-db
  # The service's metadata.uid. Required.
  serviceUID: REPLACE_WITH_SERVICE_UID
  existingUser:
    # Created by your database administrator; the policy never creates it.
    username: reporting
    database: orders
  rotationIntervalSeconds: 2592000
  paused: false
```

What happens next:

- **The first rotation is due immediately.** It replaces the password the
  administrator set; afterwards, fresh-login checks refuse that old password.
  If the first rotation fails, for example because the login does not exist or
  is not eligible, the policy retries at most once a minute and reports
  `InitializationRetryPending`. After the first success, the interval applies.
- **The policy publishes a credential role.** Read it from
  `status.credentialRole`. Use that value as the `role` of a `ServiceBinding`
  and as the `role` of a manual `RotateCredentials` operation.
- **The role is derived from this policy object.** A new policy for the same
  login, even with the same name, gets a different `status.credentialRole`, and
  bindings must use the new value.
- **Only the password changes.** The login's grants, attributes and memberships
  stay as the administrator set them.

Only one policy per login per service is allowed. A service publishes roles for
at most 14 existing-user policies. The oldest policies are used first; a policy
beyond that limit reports `CredentialRoleUnavailable`.

### When rotations happen

- For a declared role, the first rotation is due one interval after the policy
  is created. For an existing login, it is due immediately.
- Each later rotation is due one interval after the previous attempt completed,
  whether that attempt succeeded or not.
- Missed intervals are not replayed: an overdue policy submits one rotation.
- A rotation is submitted only while the service is `Ready`.
- `paused: true` stops new submissions. A rotation already submitted still runs
  to completion.
- Manual `RotateCredentials` operations remain possible. They and scheduled
  rotations of the same service run one at a time.

Operations that a policy creates are named `credential-<hash>-<sequence>` and
carry the label `services.kube-dc.com/managed-by`. Project identities cannot
change them, so you cannot cancel them. If your plan does not list
`RotateCredentials` in `operations.autoApprove`, each scheduled rotation waits
in `AwaitingApproval` until your provider approves it.

### Pause, resume and change the interval

These are the only fields you can change on an existing policy. The
`project-manager` role can change them too.

```bash
kubectl patch servicecredentialpolicy orders-db-owner-rotation -n my-project \
  --type merge -p '{"spec":{"paused":true}}'
kubectl patch servicecredentialpolicy orders-db-owner-rotation -n my-project \
  --type merge -p '{"spec":{"paused":false,"rotationIntervalSeconds":7776000}}'
```

### Read policy status

```bash
kubectl get servicecredentialpolicy -n my-project
kubectl get servicecredentialpolicy orders-db-owner-rotation -n my-project \
  -o jsonpath='role={.status.credentialRole} next={.status.nextRotationAt} last={.status.lastRotationAt} lastAttempt={.status.lastAttemptAt} active={.status.activeOperation.name} lastOp={.status.lastOperation.name}{"\n"}{range .status.conditions[*]}{.type}={.status} reason={.reason} {.message}{"\n"}{end}'
```

| Field | Meaning |
|-------|---------|
| `status.credentialRole` | The credential role the policy rotates |
| `status.nextRotationAt` | When the next rotation is due |
| `status.lastRotationAt` | When the last successful rotation completed |
| `status.lastAttemptAt` | When the last attempt completed, successful or not |
| `status.activeOperation` | The operation currently submitted, if any |
| `status.lastOperation` | The last operation that completed |
| `status.sequence` | Increases before each submitted rotation |

`Ready` is `True` only with reason `Scheduled` or `Rotated`. It describes the
schedule, not whether your applications have reloaded the credential.

| `Ready` reason | What the platform reports |
|----------------|---------------------------|
| `Scheduled` | The next rotation is scheduled |
| `Rotated` | A scheduled rotation completed |
| `RotationPending`, `Rotating` | A rotation was recorded or submitted and has not finished. See `status.activeOperation` |
| `RotationFailed` | The last rotation ended without `Succeeded`. Read that operation's reason |
| `Paused` | New rotations are paused; delivered credentials and bindings are unchanged |
| `ServiceNotReady` | Waiting for the service to be `Ready` |
| `RotationNotEntitled` | The plan is unavailable, disabled or not verified, or does not allow `RotateCredentials` |
| `ExistingUserNotEntitled` | The plan does not allow `credentials.allowExistingUsers` |
| `InvalidCredentialTarget` | The target is not valid, for example the owner, `readonly` or a reserved login |
| `CredentialRoleUnavailable` | The service has not published the policy's credential role |
| `InitializationRetryPending` | The first rotation of an existing login has not succeeded yet and will be retried |
| `CredentialPolicyConflict` | Another policy owns this target. Retire one of them |
| `ServiceIdentityChanged`, `ServiceNotFound` | The service name now belongs to another UID, or no longer exists. Create a new policy for the new service |
| `ServiceDeleting` | The service is being deleted; no new rotation is submitted |
| `Retiring` | The existing-user policy is being deleted and is waiting for accepted rotations and the withdrawal of its credential role |
| `OperationReceiptMissing` | The record of a submitted rotation disappeared. Do not delete or recreate anything; ask your provider |

## Retire a policy

Retire a policy in this order:

1. Pause it:

   ```bash
   kubectl patch servicecredentialpolicy orders-db-reporting-rotation -n my-project \
     --type merge -p '{"spec":{"paused":true}}'
   ```

2. Wait until `status.activeOperation` is empty, and check the result of the
   operation named in `status.lastOperation`.
3. Verify that your applications connect with the current credential.
4. If the policy's bindings will lose their Secret, or you want to remove a
   binding, move the workloads that use it to another `Secret`, then delete the
   `ServiceBinding` and wait until its `Secret` is gone.
5. Delete the policy:

   ```bash
   kubectl delete servicecredentialpolicy orders-db-reporting-rotation -n my-project
   ```

What deleting a policy does depends on its target:

- **Declared role (`owner`, `readonly`).** Future rotations stop. Bindings of the
  role, their delivered Secrets, the credential `Secret` and the SQL login stay in
  place and keep the current credential.
- **Existing login.** Future rotations stop and the policy's credential role is
  withdrawn. Bindings that use that role lose their projected `Secret`; the
  binding objects remain and report not ready. The source credential `Secret`
  and the SQL login remain, and the current password keeps working.

In both cases a rotation that was already submitted finishes first. Do not drop
the SQL login or delete credential Secrets as cleanup.

## After a restore

- **In-place restore.** The service keeps its UID and its bindings; an owner
  binding was verified to keep working after the restore. An existing login,
  however, comes back from the backup, and its policy cannot rotate it until you
  resynchronize it against the new engine. For each existing-user policy, create
  a `RotateCredentials` operation with the policy's `status.credentialRole` and
  the service's new `status.engineDetails.engineUID`:

  ```yaml
  apiVersion: services.kube-dc.com/v1alpha1
  kind: ServiceOperation
  metadata:
    name: orders-db-resync-reporting-1
    namespace: my-project
  spec:
    serviceRef:
      name: orders-db
    # The service's metadata.uid. It does not change during an in-place restore.
    serviceUID: REPLACE_WITH_SERVICE_UID
    type: RotateCredentials
    idempotencyKey: orders-db-resync-reporting-1
    parameters:
      # The policy's status.credentialRole, not the SQL user name.
      role: REPLACE_WITH_CREDENTIAL_ROLE
      # The service's status.engineDetails.engineUID after the restore.
      resyncRootUID: REPLACE_WITH_NEW_ENGINE_UID
    execution:
      window: Immediate
  ```

  If no existing-user policy of this service owns the role, the operation is
  `Rejected` with `PlanNotEntitled`. For a role that such a policy owns, a
  `resyncRootUID` that is not the current engine UID is `Rejected` with
  `InvalidParameters`; so is a `resyncRootUID` on the `owner` or `readonly` role.
  The resync changes only the password and verifies a fresh login before
  publishing it. If
  the login did not exist when the backup was taken, the database administrator
  must create it again; a resync does not create logins.
- **New service from a backup.** The new service has its own UID and its own
  credentials. Create bindings and credential policies for it with the new UID.

See [Backups and restore](postgresql-backup-restore.md).

## Break-glass superuser access

Break-glass access is an emergency `postgres` superuser login. Your plan must
allow `credentials.allowBreakGlass`; without it, a request for break-glass access
is refused. It is off by default.

To request it, set this parameter in the complete `ManagedService` manifest and
apply it:

```yaml
spec:
  parameters:
    breakGlass:
      enableSuperuserAccess: true
```

The service reports the result in `status.engineDetails`:

| Key | Meaning |
|-----|---------|
| `breakGlass.requested` | Whether access is requested |
| `breakGlass.state` | `Pending`, `Enabled` or `Disabled`. `Enabled` is reported after a superuser login over verified TLS succeeded |
| `breakGlass.message` | Details for the current state |
| `breakGlass.sourceName` | After `Enabled`, the name of the `Secret` that holds the superuser `username` and `password` |

- Anyone in the Project who can read Secrets can read the superuser password.
- Break-glass is not a credential role. You cannot bind it or attach a
  credential policy to it.
- A superuser has database-level authority: it can change SQL roles, grants and
  platform-managed credentials.
- To turn it off, set `enableSuperuserAccess: false` or remove the key, and
  apply. The `Secret` is removed and the old password no longer opens new
  connections. Sessions that are already open are not closed, and changes made
  with the superuser are not undone. Delete any copy of the password you made.
- Turning it on again issues a different password.
- An in-place restore is refused while break-glass access is on.
