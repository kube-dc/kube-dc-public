# Delete a managed service

As a Project administrator or developer, choose a deletion policy and remove a managed service after preserving required data.
This guide covers the shared contract for in-Project placement, where `status.instanceNamespace` matches the Project namespace.
For service readiness and troubleshooting, see [Check service status](managed-services-status.md).

## Before you begin

Check these requirements:

- You have the Project `admin` or `developer` role.
- You know which deletion policies the plan allows.
- You have moved application traffic and preserved required data.
- You have checked the family guide for retained resources and final-backup behavior.

For another placement, ask your provider what service or Project deletion removes.

## Read the status

Read the service before you change its deletion settings:

```bash
kubectl get managedservice <service> -n <project> -o yaml
```

Replace `<service>` with the service name and `<project>` with its Project namespace.
Check `status.instanceNamespace`, `spec.deletionPolicy`, and `spec.deletionProtection`.
See [Is the result current?](managed-services-status.md#is-the-result-current) before relying on a reported configuration.

### Status during deletion

The object remains in phase `Deleting` while the platform applies its deletion policy.
Read the `Ready` condition's reason and message:

| Reason | What to do |
|---|---|
| `DeletionConfirmationNeeded` | Set the confirmation annotation to the exact service name for the `Delete` policy |
| `DeletionBlocked` | Read the message for the blocking condition; contact your provider if it does not clear |
| `Retained` | Read the message for retained resources and deletion progress |

Inspect events when deletion stalls:

```bash
kubectl get events -n <project> --field-selector involvedObject.name=<service>
```

A `DeletionPolicyDowngraded` warning means a less destructive policy applies.
A `ProjectDeleted` event identifies deletion caused by Project removal.
Do not remove platform finalizers or delete protected child resources to force completion.

## Deletion protection

`spec.deletionProtection: true` blocks deletion of the service resource.
Set it to `false` in a separate update before you submit a delete request.
Keep protection enabled in GitOps manifests until you intend to remove the service.

Protection does not prevent deletion of the Project.
Some destructive family operations also require protection to be disabled; check their procedures before requesting them.

## Deletion policies

The default policy is `Retain`. The family and the plan's `allowedDeletionPolicies` determine which policies you can use:

| Policy | Requested outcome | Requirement |
|---|---|---|
| `Retain` | Stop management and keep service resources | Arrange provider cleanup when retained resources are no longer needed |
| `SnapshotAndDelete` | Take a final backup before removing service resources | Family support, enabled backups, and a successful final backup |
| `Delete` | Remove service resources without a final backup | Confirm the service name with an annotation |

### Retain

Retained workloads and storage can continue to use capacity and quota.
Retained endpoints can remain reachable.
Move applications and disable external access before deletion when you no longer want the service reachable.

After the `ManagedService` is gone, you cannot submit operations or create bindings for the retained instance.
Project identities cannot change or delete protected retained resources; arrange cleanup with your provider.

### SnapshotAndDelete

The platform requests a final backup before removal.
A final backup is recorded with `spec.origin: Final`.
If the engine or backup is unavailable, do not assume that deletion completed or that recovery is possible.
Read the status and the family guide's failure behavior.

### Delete

This policy removes data without a final backup.
Set `services.kube-dc.com/confirm-delete` to the service name before deleting it.
Without a matching value, deletion waits with reason `DeletionConfirmationNeeded`.

Earlier backup records do not prove that their archives remain recoverable.
Verify required recovery points before deletion.

### When the policy cannot be applied

If the platform cannot apply a changed deletion policy, it uses the less destructive of the accepted and requested policies.
The `DeletionPolicyDowngraded` event explains the cause and policy used.
Retained resources can continue to consume capacity.

## Delete a service

1. Select a policy supported by your family and plan.
2. [Back up and verify recovery](managed-services-backup-restore.md) for data you need to keep.
3. Move applications off the service.
4. Disable external access using the family procedure, if configured.
5. Edit the complete service manifest to set the chosen policy and `deletionProtection: false`.

   For `Delete`, include this annotation and these fields:

   ```yaml
   metadata:
     annotations:
       services.kube-dc.com/confirm-delete: <service>
   spec:
     deletionPolicy: Delete
     deletionProtection: false
   ```

   Replace `<service>` with the exact service name.
   This is a fragment to merge into your existing manifest, not a complete resource.

6. Apply the updated manifest:

   ```bash
   kubectl apply -f <service-manifest>
   ```

   Replace `<service-manifest>` with its file path.

7. [Check that the configuration is current](managed-services-status.md#is-the-result-current).
8. Delete the service:

   ```bash
   kubectl delete managedservice <service> -n <project>
   ```

9. Confirm that the object no longer exists:

   ```bash
   kubectl get managedservice <service> -n <project>
   ```

   The expected result is `NotFound`. If the object remains, inspect [Status during deletion](#status-during-deletion).

Automation can supply `preconditions.uid` in an API delete request to reject a replacement service with the same name.
A plain `kubectl delete` does not check that UID.

## What remains after deletion

Check the family procedure for exact resource behavior:

| Service | Deletion details |
|---|---|
| PostgreSQL | [Retained resources, final backups, Gateway access, and cleanup](postgresql-deletion.md) |
| MySQL and MariaDB | [Deletion policy and Project deletion](managed-services-mysql-mariadb.md#deletion) |
| Valkey | [Members, volumes, final backups, and credentials](managed-services-valkey.md#delete-a-service) |
| GitHub Actions runners | [Pool cleanup](managed-services-github-runners.md#troubleshoot-and-delete) |
| Other catalog services | Ask your provider for the integration's retention and cleanup behavior |

Review leftover bindings and credential policies after removing the service.
Remove resources that applications no longer use.
See [Retire a rotation policy](managed-services-credentials.md#pause-or-retire-a-policy).

`ServiceBackup` records can remain after service deletion.
Check archive retention and recovery information before [restoring into another service](managed-services-backup-restore.md#restore-into-a-new-service).

## Delete a Project

:::warning Project deletion removes service data
For in-Project placement, deleting a Project removes its services and data volumes without a final backup.
This applies regardless of the service's deletion policy, deletion protection, or confirmation annotation.
Copy required data outside the Project before deleting it.
:::

The Project's bindings, Secrets, credential policies, operations, and backup history records are removed too.
Managed service restore works within the same Project; it cannot recover a deleted Project through these records.

Before deleting a Project, list its services and verify their placement:

```bash
kubectl get managedservices -n <project> \
  -o custom-columns='NAME:.metadata.name,INSTANCE_NAMESPACE:.status.instanceNamespace,POLICY:.spec.deletionPolicy,PROTECTED:.spec.deletionProtection'
```

For any service outside the Project namespace, ask your provider about its deletion behavior.

## Limits

Final-backup failures, interrupted deletion, external access, and recovery after deletion require family-specific checks.
For PostgreSQL qualification limits, see [PostgreSQL deletion limits](postgresql-deletion.md#limits).
