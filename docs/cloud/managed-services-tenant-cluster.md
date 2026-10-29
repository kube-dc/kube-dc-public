# Run a service on your own cluster

As a Project administrator or developer, place a managed service inside one of your Project's Managed Clusters instead of the provider's shared infrastructure.
The platform still creates and operates the service, takes its backups and runs its operations.
The engine, its volumes and its credentials live on your cluster's workers.
This placement is `TenantCluster`; the in-Project placement described in the rest of this guide is `ProviderShared`.

:::note Availability
Your provider decides whether services can run on Managed Clusters, and for which service families.
The option exists only when the catalog publishes a plan whose `allowedPlacementModes` includes `TenantCluster`.
This page uses `kubectl`. The Kube-DC API's **Data planes** operations offer the same grant, status and withdrawal steps; see [Automate with the API](api-reference.md).
:::

## How it works

A Project admin grants a cluster once. The platform then registers the cluster as a *data plane* and installs a service agent and the family's operator on it.
After that, Project members create services in the Project namespace on the management cluster, exactly as for in-Project services, with one extra field that names the cluster.

| What | Where it lives |
|---|---|
| `ManagedService`, `ServiceBinding`, `ServiceOperation`, `ServiceBackup` and the grant | Your Project namespace on the management cluster |
| The engine Pods, volumes and Services | An instance namespace in your cluster (`status.instanceNamespace` on the service) |
| The service's owner credential | A Secret in the instance namespace in your cluster |
| Binding credentials | A Secret in the consumer namespace you name, in your cluster |
| Backups and write-ahead logs | Your Project's bucket on the provider's object store |

## When to use it

Use this placement when:

- the applications that use the service already run in the Managed Cluster and should reach it over the cluster's own network;
- you want the service to use your cluster's worker capacity;
- your provider offers managed services only on Managed Clusters.

Use an in-Project service instead when you need public exposure, credentials that the console can show, or credentials that your cluster's administrators must not read.

## What you disclose

:::warning Your cluster's administrators can read the service's secrets
Granting a cluster gives everyone with administrator access to that cluster the following:

- **The service's credentials.** The owner credential and every binding Secret are ordinary Secrets in your cluster.
- **The backup bucket key.** The platform copies the access key of the service's backup bucket, normally your Project's shared bucket, into the instance namespace. The key can read, overwrite and delete every object in that bucket, including backups of other Project services, in-Project ones among them, that use it.
- **Control of the engine.** Cluster administrators can stop backups and change or delete the engine, its volumes and the platform's records in the cluster. The platform reports what it observes, but it cannot prevent these changes.

Backups and write-ahead logs leave your cluster over its public egress to the provider's object store.
The platform also stores a record of the provider-hosted control-plane addresses of your cluster in its agent namespace there; it uses this record to authorize backup deletion and restore.

The grant records your acknowledgement as `disclosure: ProjectCredentialsToClusterAdministrators`.
:::

## Before you begin

Check these requirements:

- The Project `admin` role to grant a cluster. Creating services needs the `admin` or `developer` role; see [Project roles](managed-services.md#project-roles).
- A Managed Cluster in the Project with Cluster API worker management enabled (`spec.enableClusterAPI: true`) and Ready workers. See [Provision a Managed Cluster](provisioning-cluster.md).
- Workers that meet the provider's minimum size. A plan with several instances places each one on a different Ready worker, so a two-instance plan needs at least two Ready workers.
- A plan that allows `TenantCluster` placement and the `tenant-native` connectivity class. Plan names differ between installations; this page uses `postgresql-tenant-ha`. See [Classes and plans](managed-services-plans.md).
- The cluster's kubeconfig, to create consumer namespaces and read delivered Secrets. See [Get the kubeconfig](cluster-management.md#get-the-kubeconfig).

Throughout this page, replace `my-project` with your Project's backing namespace and `db-cluster` with the Managed Cluster name.

## Grant a cluster

A grant names one cluster *incarnation* by its UID. A cluster that is deleted and recreated under the same name has a new UID and needs a new grant.

Create the grant first, then opt the cluster in. The API's grant operation performs both steps in this order.

1. Read the cluster's UID:

   ```bash
   kubectl get kdccluster db-cluster -n my-project -o jsonpath='{.metadata.uid}{"\n"}'
   ```

2. Save this grant as `grant.yaml`, with the UID from step 1:

   ```yaml
   apiVersion: services.kube-dc.com/v1alpha1
   kind: ServiceDataPlaneGrant
   metadata:
     name: db-cluster
     namespace: my-project
   spec:
     kdcCluster:
       name: db-cluster
       uid: <cluster-uid>
     # The families the cluster may run. The provider publishes which
     # families are available on Managed Clusters.
     families: [postgresql]
     # Credentials of services on this cluster, and the Project's backup
     # bucket key, become readable by the cluster's administrators.
     disclosure: ProjectCredentialsToClusterAdministrators
   ```

3. Create it:

   ```bash
   kubectl apply -f grant.yaml
   ```

4. Opt the cluster in. `services.kube-dc.com/families` is a comma-separated list:

   ```bash
   kubectl annotate kdccluster db-cluster -n my-project \
     services.kube-dc.com/data-plane=enabled \
     services.kube-dc.com/families=postgresql
   ```

The grant's `spec` is immutable. To change its families, delete the grant and create a new one.
Only the `admin` role can create or delete grants; other Project roles can read them.

### Check the registration

```bash
kubectl get servicedataplanegrant -n my-project
```

| Grant phase | Meaning |
|---|---|
| `Pending` | The grant could register the cluster but is not bound yet. With reason `AmbiguousGrants`, more than one grant could register it: keep one |
| `Bound` | The grant serves the cluster's registration. `DATAPLANE` names the data plane, `dp-<cluster-uid>` |
| `Refused` | The grant cannot register the cluster; read `REASON` |
| `Consumed` | The grant served a registration earlier but no longer serves the cluster's current binding, for example after the registration ended or another grant reinstated it. It cannot be reused |

| Refusal reason | Meaning and fix |
|---|---|
| `DuplicateGrant` | The cluster's current registration is already bound to another grant. Delete the extra grant |
| `NotAfterWatermark` | The grant was created at or before the cluster's grant watermark, the newest grant time the platform recorded at its last registration change. Delete it and create a new one |
| `FamiliesNarrower` | The grant names none of the families in the cluster's `services.kube-dc.com/families` annotation. Replace it, or fix the annotation |
| `IncarnationMismatch` | The cluster was recreated and has another UID. Create a grant with the new UID |
| `Terminating` | The grant is being deleted |

A bound grant does not yet mean the cluster can take a service.
The platform first installs its agent and the family operator on the cluster and checks the cluster's health.
This usually takes a few minutes. Project roles cannot read data planes with `kubectl`; the API's data-plane listing reports each cluster's registration phase, health checks and whether it accepts services. A service created earlier waits in `Pending` with `ClusterNotRegistered` and is placed once the cluster is ready.

The platform installs these objects in your cluster:

| Object | Purpose |
|---|---|
| Namespace `kube-dc-service-runner` | The service agent, its trust configuration and its records |
| The family operator, for PostgreSQL in namespace `cnpg-system` | Runs the engine |
| An admission policy protecting the agent's records | Keeps the agent's state consistent; any cluster administrator can remove it |
| One instance namespace per service | The engine, its volumes, Services and owner credential |

Do not delete these objects while services run on the cluster.

## Create a service

Save this manifest as `orders-db.yaml`:

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
    name: postgresql-tenant-ha
  placement:
    mode: TenantCluster
    clusterRef:
      name: db-cluster
  connectivity:
    classRef:
      name: tenant-native
  deletionPolicy: Retain
  topology:
    instances: 2
  parameters:
    database: orders
    owner: orders
    backup:
      enabled: true
```

Apply it in the Project namespace on the management cluster:

```bash
kubectl apply -f orders-db.yaml
```

| Field | Value |
|---|---|
| `spec.placement.mode` | `TenantCluster`. The plan's `allowedPlacementModes` must include it |
| `spec.placement.clusterRef.name` | The Managed Cluster in this Project. Always set it; the Kube-DC API requires it for this placement. Not allowed together with `dataPlaneRef` |
| `spec.connectivity.classRef.name` | `tenant-native`: in-cluster Services only |
| `spec.deletionPolicy` | One of the plan's `allowedDeletionPolicies`. See [Delete a service](#delete-a-service) |

The whole `placement` block is immutable. To move a service to another cluster, create a new service and copy the data.
For the PostgreSQL parameters, see [Create a PostgreSQL service](postgresql-create.md).

### What to expect

The service goes through the usual phases (see [Check status and troubleshoot](managed-services-status.md)).
Once placed, `status.dataPlane` is `dp-<cluster-uid>` and `status.instanceNamespace` names the instance namespace in your cluster:

```bash
kubectl get managedservice orders-db -n my-project \
  -o jsonpath='{.status.phase}{"\t"}{.status.dataPlane}{"\t"}{.status.instanceNamespace}{"\n"}'
```

These `Placed` reasons are specific to this placement:

| Reason | Meaning and fix |
|---|---|
| `ClusterNotFound` | No Managed Cluster of that name exists in the Project |
| `ClusterNotRegistered` | The cluster has no data plane yet, or its registration is not active. Grant the cluster, or wait for registration |
| `ClusterIncarnationChanged` | The cluster was recreated after it was registered. Grant the new incarnation |
| `ClusterNotEligible` | The cluster's data plane refused the service; the message names the check, such as capacity, worker count or a failing health check |
| `ClusterAmbiguous` | The service names no cluster and more than one data plane qualifies. Set `clusterRef` |
| `PredecessorUnresolved` | A retained or unresolved earlier service of the same name still holds the name. See [Recover retained services](#recover-retained-services) |

After placement, these `Ready` reasons report the cluster's state:

| Reason | Meaning |
|---|---|
| `DataPlaneDegraded` | A cluster health check fails, for example NotReady nodes or an unreachable API endpoint. The service keeps running; fix the cluster |
| `DataPlaneRevoking` | The cluster is being opted out. New operations wait; the message gives the deadline |
| `DataPlaneRetired` | The cluster no longer serves managed services. The service is retained in the cluster or gone with it |

`BackupReady=False` with reason `BackupStoreUnreachable` means the cluster cannot reach the object store, so backups stop until the path is restored.

## Connect an application in the cluster

Applications in the same Managed Cluster connect through the `tenant-native` Services.
A binding delivers the credential as a Secret in a namespace of your cluster.

1. Create the consumer namespace and ServiceAccount in your cluster, using the cluster's kubeconfig.
   The binding waits until the namespace exists.
2. Read the service's UID on the management cluster:

   ```bash
   kubectl get managedservice orders-db -n my-project -o jsonpath='{.metadata.uid}{"\n"}'
   ```

3. Create the binding in the Project namespace on the management cluster:

   ```yaml
   apiVersion: services.kube-dc.com/v1alpha1
   kind: ServiceBinding
   metadata:
     name: shop-orders
     namespace: my-project
   spec:
     serviceRef:
       name: orders-db
     serviceUID: <service-uid>
     role: owner
     consumer:
       kind: ServiceAccount
       name: shop-app
       namespace: shop        # a namespace in your cluster
     delivery:
       mode: DataPlaneLocalSecret
       secretName: orders-db  # created in namespace shop of your cluster
       endpointName: read-write
   ```

For PostgreSQL the Secret holds `host`, `port`, `dbname`, `username`, `password` and `ca.crt`.
The host is a Service name that resolves only inside your cluster.
The Kube-DC API does not return these credentials, and `ManagedSecret` or `OpenBao` delivery is not available for this placement.
For roles and rotation, see [Connect applications](managed-services-connect.md) and [Rotate credentials](managed-services-credentials.md).

## Operations, backups and restore

Operations work as for in-Project services; the plan lists which ones are allowed. See [Request an operation](managed-services-operations.md).
Scheduled and on-demand backups go to your Project's bucket, and `ServiceBackup` records appear in the Project namespace.

A restore creates a new service from a `ServiceBackup` record; the plan must allow `RestoreToNew`.
Before it copies anything, the platform checks that:

- the backup record still holds unexpired recovery information, and the backup, the source data plane and the target identities still match what the restore claimed;
- the target plan and engine version are compatible with the backup;
- the source service is not held by an unresolved instance: one deleted under `Retain` whose instance is still retained in the cluster, or one whose outcome the platform could not determine. An operator's resolution removes this hold.

The recovery credential of a backup taken on a Managed Cluster is held by the platform's agent on that cluster.
A restore therefore needs the source's data plane to still exist and not be deleting, its admin kubeconfig to still belong to the cluster's control plane, and the cluster to be reachable with the recovery Secret in place. The registration does not have to be active. After the source cluster is deleted, its backups can no longer be restored.

To restore onto the same cluster, set the target placement to `TenantCluster` with the source's `clusterRef`:

```yaml
     target:
       name: orders-db-restored
       planRef:
         name: postgresql-tenant-ha
       engineVersion: "<qualified-version>"
       placement:
         mode: TenantCluster
         clusterRef:
           name: db-cluster
       connectivity:
         classRef:
           name: tenant-native
```

This fragment replaces `spec.restore.target` in the request shown in [Restore into a new service](managed-services-backup-restore.md#restore-into-a-new-service).
Ask your provider before restoring onto another cluster or into the Project namespace.
After the source service is deleted, its backup records remain within their retention, and each record names the data plane the source ran on in `spec.dataPlane` (`dp-<cluster-uid>`).
The API's backup listing also reports that cluster as the record's source placement.

## Delete a service

The plan's `allowedDeletionPolicies` decide what deletion does. See [Delete a service](managed-services-status-deletion.md) for the shared rules and the confirmation annotation.

| Policy | On a Managed Cluster |
|---|---|
| `Retain` | The platform stops managing the instance. The engine, volumes and credential Secrets keep running in your cluster, and the service name, its plan slot and its reserved capacity stay held |
| `SnapshotAndDelete` | The platform takes a final backup, then removes the instance namespace from your cluster |
| `Delete` | The platform removes the instance namespace without a final backup |

Before you delete a `Retain` service, record its instance namespace:

```bash
kubectl get managedservice orders-db -n my-project -o jsonpath='{.status.instanceNamespace}{"\n"}'
```

To release a retained instance, export any data you need and delete that namespace in your cluster.
The namespace carries a backup guard that admits the deletion only after the platform has recorded the instance's final backup state.
A refusal soon after the service was deleted is temporary: retry after a few minutes.
If the deletion is still refused, the platform could not record that state, for example because its records in the cluster were removed or a restore credential in the namespace is still active. Waiting does not help; ask your provider to resolve the instance.

After the namespace is gone, the platform checks the cluster itself and then frees the name, slot and capacity.
It reads the cluster through a reachable connection bound to the same cluster and control-plane incarnation. If the cluster is unreachable, has been recreated, or the name stays held, ask your provider: an operator can record the instance as destroyed, or accept releasing it.

`SnapshotAndDelete` and `Delete` need the cluster to be reachable and registered.
While the cluster is being opted out, such a deletion waits with reason `DeletionBlocked`; if the cluster retires first, the instance is retained instead.

## Opt a cluster out

A Project admin opts a cluster out in either of two ways:

- delete the grant: `kubectl delete servicedataplanegrant db-cluster -n my-project`;
- remove the opt-in annotation:

  ```bash
  kubectl annotate kdccluster db-cluster -n my-project services.kube-dc.com/data-plane-
  ```

For a registered cluster, either starts a grace period, 72 hours unless your provider configured another value.
A cluster whose registration has not finished yet retires at once, without a grace period.

| During the grace period | After the deadline |
|---|---|
| New services are not placed on the cluster | Every service on the cluster is detached under `Retain` and keeps running there |
| Running services keep running and backing up | Services report `Ready=False` with reason `DataPlaneRetired` |
| New operations wait; services report `DataPlaneRevoking` with the deadline | The platform starts removing its trust configuration and admission policy from the cluster. While the grant exists, its `status.remoteRelease` shows the progress and any objects left behind. After you delete the grant, ask your provider to check the data plane's release status |
| `Retain` deletions proceed; `Delete` and `SnapshotAndDelete` wait | The family operator and the idle agent stay installed; remove them if you no longer want them |

To cancel the opt-out, restore the opt-in annotation if you removed it and create a new grant before the deadline. The platform reinstates the registration only when all of these hold:

- the new grant names the same cluster incarnation (UID), is not being deleted and was created after the opt-out;
- it covers **every** family the cluster currently serves, not just one of the annotated families;
- it is the only grant that qualifies;
- the cluster is still bound to the same CAPI Cluster and control plane, and the platform's own checks pass.

Otherwise the grant stays `Pending` or is refused, and the cluster still retires at the deadline.
Re-adding a removed annotation alone does not cancel the opt-out, and the grant that was bound before the opt-out cannot be reused: delete it first if the new grant needs its name.
Check that the grant reports `Bound` and that your services no longer report `DataPlaneRevoking` before the deadline passes.

### Recover retained services

A retained service keeps running in your cluster but is no longer managed: the platform runs no operations on it, and you cannot restore from its backups until the instance is released or an operator resolves it.

1. Export the data with your own cluster access, for PostgreSQL with `pg_dump`, and import it into a new service with another name.
2. Delete the retained instance namespace in your cluster. Once the platform has verified that it is gone, it frees the name, plan slot and capacity. See [Delete a service](#delete-a-service) if the deletion is refused.
3. Delete the old `ManagedService`.

If you delete the `ManagedService` before step 2, the name stays held: a new service with the same name waits with `PredecessorUnresolved` until the namespace is gone.

### Grant the cluster again

Delete the consumed grant, keep or restore the opt-in annotation, and create a new grant with the cluster's current UID.
The cluster registers again as a new registration generation once the platform has finished with the previous one. It reuses the components already installed on the cluster but passes the readiness checks again.
Retained instances from the earlier registration are not taken back under management.

## Delete the cluster with services on it

:::danger Deleting the cluster destroys its services' data
No final backup is taken. Backup records remain within their retention, but the recovery credential is lost with the cluster, so those backups can no longer be restored.
Export the data you need first.
:::

When the cluster is deleted, the platform retires its data plane and records each service as destroyed with the cluster.
The services report `Ready=False` with reason `DataPlaneRetired`, and their names and capacity are released once the cluster is gone.
Delete the `ManagedService` objects afterwards.

Deleting the Project removes its clusters, services and backup records the same way.
A cluster recreated under the same name is a new incarnation: it is not registered until you grant it again.

## Limits

- Your provider qualifies Managed Clusters and service families for this placement; ask which families it supports.
- Credential inputs and runner execution profiles are not supported on Managed Clusters. Do not use them with `TenantCluster` placement.
- Connectivity is `tenant-native` only: no Gateway routes or public load balancers. Clients run in the same cluster.
- Plans for this placement include no platform metrics or alerts. The engine's metrics endpoint is available to your own collector.
- A running service cannot move between clusters, or between a cluster and the Project namespace, and retained instances are not re-adopted automatically.
- Node capacity, cluster health and the cluster's network remain your responsibility. Read the plan's support text.

## Next steps

- [Connect applications](managed-services-connect.md).
- [Back up and restore](managed-services-backup-restore.md).
- [Manage your Managed Cluster](cluster-management.md).
