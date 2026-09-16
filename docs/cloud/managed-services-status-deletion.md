# Status and Deletion

This page covers what a `ManagedService` reports while it is being deleted, how
deletion protection and the three deletion policies work, what remains after a
deletion, and what happens to services when their Project is deleted.

Everything on this page applies to the in-Project placement this chapter
covers, where a service's `status.instanceNamespace` is the Project namespace.

Throughout this page, replace `my-project` with your Project's backing
namespace.

## Read the status

The phases and conditions of a service, and how to check that a result is
current, are described in
[Read the status](postgresql-create.md#read-the-status). This section adds what
the status shows during deletion.

### Status during deletion

When you delete a `ManagedService`, Kubernetes keeps the object until the
platform has carried out its deletion policy. Until then, `status.phase` is
`Deleting` and the `Ready` condition is `False` with one of these reasons:

| `Ready` reason | Meaning |
|----------------|---------|
| `DeletionConfirmationNeeded` | `deletionPolicy` is `Delete` and the service does not carry the confirmation annotation. Nothing is removed until you add it. See [Delete](#delete) |
| `DeletionBlocked` | The deletion is waiting. Read the message. If it says that `deletionProtection` is enabled, set `deletionProtection: false`. Otherwise ask your provider if the reason does not clear |
| `Retained` | The deletion is in progress. Read the message |

The deletion is complete when the `ManagedService` no longer exists:

```bash
kubectl get managedservice orders-db -n my-project -w
kubectl get managedservice orders-db -n my-project \
  -o jsonpath='phase={.status.phase}{"\n"}{range .status.conditions[?(@.type=="Ready")]}reason={.reason} {.message}{"\n"}{end}'
kubectl get events -n my-project --field-selector involvedObject.name=orders-db
```

These events are specific to deletion:

| Event | Type | Recorded when |
|-------|------|---------------|
| `DeletionPolicyDowngraded` | `Warning` | The platform could not apply the deletion policy you set and uses a less destructive one. See [When the policy cannot be applied](#when-the-policy-cannot-be-applied) |
| `ProjectDeleted` | `Normal` | The service is being deleted because its Project is being deleted. See [Deleting a Project](#deleting-a-project) |

## Deletion protection

While `spec.deletionProtection` is `true`, a request to delete the
`ManagedService` is refused:

```text
deletionProtection is enabled on this ManagedService; set spec.deletionProtection=false first
```

- The check reads the stored object, so turning protection off must be its own
  update before the delete request. Set `deletionProtection: false` in the
  manifest, apply it, and then delete the service.
- The protection covers deletion of the `ManagedService` only. It does not
  protect the service from deletion of its Project; see
  [Deleting a Project](#deleting-a-project).
- An in-place restore also requires `deletionProtection: false`; see
  [Restore in place](postgresql-backup-restore.md#restore-in-place).

We recommend keeping `deletionProtection: true` in the manifests your GitOps
tool applies, so that an accidental prune of the object is refused.

## Deletion policies

`spec.deletionPolicy` decides what deleting the `ManagedService` does to the
engine and its data. The default is `Retain`. The value must be one of your
plan's `allowedDeletionPolicies`; you cannot read the plan, so ask your provider
which policies it allows.

| | `Retain` | `SnapshotAndDelete` | `Delete` |
|--|----------|---------------------|----------|
| Final backup | No | Yes, before removal | No |
| Engine and data volumes | Kept in the Project | Removed after the final backup | Removed |
| Your data | Stays on the retained volumes | Kept in the final backup | Lost, except in earlier backups |
| Extra requirement | None | The plan's `backup.enabled: true` and working backups | The annotation `services.kube-dc.com/confirm-delete` set to the service name |

### Retain

- The platform stops managing the service. The database engine, its data
  volumes, its credential Secrets and the Services it created, including a
  Gateway route, stay in the Project. They can keep using capacity and quota.
- The retained objects are protected: Project identities cannot change or
  delete them. Ask your provider to remove them when you no longer need them.
- Retained objects no longer belong to a `ManagedService`, so you cannot run
  operations on them or create bindings for them.
- The service's backup history records remain, and you can restore them into a
  new service.
- Because retained resources can include a Gateway route, the database can stay
  reachable. Gateway behaviour under `Retain` is not yet qualified. Turn off
  Gateway access and delete the bindings you no longer need before you delete
  the service. See
  [Disable Gateway access](postgresql-external-access.md#disable-gateway-access).

### SnapshotAndDelete

- The plan must set `backup.enabled: true`, and backups must work for the
  service.
- The platform takes a final backup, recorded as a `ServiceBackup` with
  `spec.origin: Final`, and then removes the engine and its data volumes.
- If the engine no longer exists when the deletion runs, no final backup can be
  taken. The data volumes and credential Secrets are then retained instead of
  deleted. Ask your provider.
- Handling of a final backup that fails is not yet qualified. Ask your provider
  for help.
- You can create a new service from the final backup; see
  [Restore into a new service](postgresql-backup-restore.md#restore-into-a-new-service).
  Test that restore before you rely on it.

### Delete

- The engine and its data volumes are removed, and the data is lost. No final
  backup is taken.
- The service must carry the annotation `services.kube-dc.com/confirm-delete`
  with the service name as its value. Without it, or with another value, the
  deletion waits in phase `Deleting` with reason `DeletionConfirmationNeeded`
  and the engine is not touched. The message is
  `deletionPolicy Delete destroys data; annotate services.kube-dc.com/confirm-delete=<name> to confirm, or the instance stays retained`.
- Set the annotation in the manifest before you delete the service.
- Backup records made before the deletion remain. Restoring from them after a
  `Delete` is not yet qualified.

### When the policy cannot be applied

When you change `deletionPolicy`, the platform applies the new value, at the
latest when you delete the service. If it cannot apply the new value at that
point, for example because the service's configuration is no longer accepted,
the deletion runs under the less destructive of the policy already in force and
the policy you set. The service then records a `Warning` event
`DeletionPolicyDowngraded` that names the cause and the policy used. When that
policy is `Retain`, the message says that the engine and its volumes are
retained and keep consuming capacity.

Change the policy and check that the change is current before you delete the
service.

## Delete a service

1. Choose the deletion policy, and check with your provider that your plan
   allows it.
2. Keep what you need. For example, take a `Backup` and wait for it to succeed;
   see [Take a backup now](postgresql-backup-restore.md#take-a-backup-now).
3. Move applications off the service. If the service has Gateway access, turn
   it off.
4. Update the manifest: set `deletionPolicy`, add the confirmation annotation
   for `Delete`, and set `deletionProtection: false`. Apply it, and check that
   the change is current as described in
   [Is the result current?](postgresql-create.md#is-the-result-current)
5. Delete the service and watch until it is gone.

This manifest prepares the service from
[Create a PostgreSQL Service](postgresql-create.md#create-the-service) for
deletion with `Delete`. All other fields stay unchanged:

```yaml
apiVersion: services.kube-dc.com/v1alpha1
kind: ManagedService
metadata:
  name: orders-db
  namespace: my-project
  annotations:
    # Required for deletionPolicy: Delete. The value is the service name.
    services.kube-dc.com/confirm-delete: orders-db
spec:
  classRef:
    name: postgresql
  planRef:
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
  deletionPolicy: Delete
  deletionProtection: false
```

```bash
kubectl apply -f orders-db.yaml
kubectl delete managedservice orders-db -n my-project
kubectl get managedservice orders-db -n my-project -w
```

Automation that calls the Kubernetes API directly can send the delete request
with `preconditions.uid` set to the service's `metadata.uid`. The API server
then refuses the request when the name belongs to a different service. A plain
`kubectl delete` does not check the UID.

## What remains after deletion

| Object | After the `ManagedService` is deleted |
|--------|----------------------------------------|
| `ServiceBackup` records | Kept. They have no owner, and Project roles can read them but cannot delete them. They are removed when the Project is deleted. Use them to [restore into a new service](postgresql-backup-restore.md#restore-into-a-new-service); a record does not prove that its backup data is still retained, see [Backup history](postgresql-backup-restore.md#backup-history) |
| `ServiceBinding` objects | Kept, with `Ready=False`, reason `CredentialPending` and a message such as `service not found: orders-db`. After a `Delete`, their delivered Secrets are removed. Delete the binding objects |
| `ServiceCredentialPolicy` objects | Kept. While the service is being deleted they report `ServiceDeleting` and submit no rotations. Delete them; see [Retire a policy](postgresql-credentials.md#retire-a-policy) |
| Engine, data volumes, credential Secrets and published Services | Kept under `Retain`. Under `SnapshotAndDelete`, the data volumes and credential Secrets are kept when the engine no longer existed, so no final backup could be taken. Ask your provider to remove them |

## Deleting a Project

:::warning Deleting a Project deletes its services
For the in-Project placement, deleting a Project deletes every service in it
together with its data volumes. This happens whatever `deletionPolicy` says,
and regardless of `deletionProtection` or the confirmation annotation. No final
backup is taken, and the Project's `ServiceBackup` records are deleted too.
:::

- Each service records a `ProjectDeleted` event while its Project is being
  deleted. The events are deleted with the Project.
- Bindings, their Secrets, credential policies and operation records are
  objects in the Project and are deleted with it.
- A restore works only within the same Project, so the backups of these
  services cannot be restored through Managed Services after the Project is
  gone.

Before you delete a Project:

1. List its services and check that each one runs in the Project namespace:

   ```bash
   kubectl get managedservices -n my-project \
     -o custom-columns='NAME:.metadata.name,INSTANCE_NAMESPACE:.status.instanceNamespace,POLICY:.spec.deletionPolicy,PROTECTED:.spec.deletionProtection'
   ```

2. Copy every piece of data you need to keep to a place outside the Project.

A service whose `status.instanceNamespace` is not the Project namespace is
outside the placement this chapter covers; ask your provider what deleting the
Project does to it. See also
[Deleting services and Projects](managed-services.md#deleting-services-and-projects).

## Limits

These cases are not yet qualified. Ask your provider if you need them:

- Recovery from a deletion that fails or is interrupted part way.
- Restoring from the backups of a service that was deleted with `Delete`.
- `SnapshotAndDelete` when the final backup fails, or when the engine no longer
  exists.
- Deleting a service with `Retain` or `SnapshotAndDelete` while Gateway access
  is on.
