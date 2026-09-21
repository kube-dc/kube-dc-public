# Retiring db-manager

`KdcDatabase` (`db.kube-dc.com/v1alpha1`), operated by the db-manager
controller, was Kube-DC's first database product. It is deprecated: managed
services replace it for every engine, and the console no longer offers it.
This page is for the operator of an installation that still runs one or more
of those databases.

## What changes in 0.9

- Managed services are the database offer. The console shows them to every
  organization by default (`ui.managedServices.allOrganizations: true` in
  the chart, `KUBE_DC_UI_MANAGED_SERVICES_ALL_ORGANIZATIONS` in Fleet).
- The **Databases** area of the console appears only for the organizations
  and Project namespaces you list. Everyone else sees managed services alone,
  and a bookmarked `#databases` link says where to go.
- db-manager itself keeps running. Existing `KdcDatabase` and
  `DatabaseCredentialPolicy` resources keep reconciling, their backups keep
  running, and `kube-dc db credentials` keeps working for the listed tenants.
  Nothing is deleted by the upgrade.
- Agent skills and the public documentation describe managed services only.
  The migration guide for tenants is
  [Migrating from db-manager databases](/cloud/managed-services-migration).

## Find what still runs

```sh
kubectl get kdcdatabases -A
kubectl get databasecredentialpolicies -A
```

Every namespace in that output is a `<organization>-<project>` pair. Those
are the tenants that must keep the deprecated area until they have migrated.

## Keep the area for the tenants that need it

In the cluster's `cluster-config.env`, list the organizations whose every
Project keeps the area and the individual Project namespaces that keep it:

```sh
KUBE_DC_UI_MANAGED_SERVICES_ALL_ORGANIZATIONS=true
KUBE_DC_UI_LEGACY_DATABASES_ORGANIZATIONS=[acme]
KUBE_DC_UI_LEGACY_DATABASES_PROJECTS=[team-analytics, partner-demo]
```

The values are YAML flow sequences and reach the chart as
`ui.legacyDatabases.organizations` and `ui.legacyDatabases.projects`. An
empty list on both retires the area for everyone. Changing the lists rolls the
frontend deployment; nothing else restarts.

## Help a tenant migrate

Kube-DC does not convert a `KdcDatabase` into a `ManagedService`. The tenant
creates the managed service, moves the data with the engine's own dump and
restore tools from inside the Project, repoints the application at the new
binding Secret, and deletes the `KdcDatabase`. The tenant guide has the
manifests and the Job examples for PostgreSQL and MariaDB. As the operator
you check that:

- the family the tenant needs is published for the cluster and its plans
  have the capacity the old database used (`kubectl get kdcdatabase <name> -n
  <ns> -o jsonpath='{.spec}'` shows cpu, memory, storage and replicas);
- the Project quota has room for both databases while the data is copied;
- backups of the old database are recent, in case the migration has to be
  repeated.

## Turn db-manager off

When `kubectl get kdcdatabases -A` and `kubectl get
databasecredentialpolicies -A` return nothing on the cluster:

1. Remove every tenant from `KUBE_DC_UI_LEGACY_DATABASES_ORGANIZATIONS` and
   `KUBE_DC_UI_LEGACY_DATABASES_PROJECTS`.
2. Set `DB_MANAGER_ENABLED=false` in `cluster-config.env`. The chart stops
   rendering the db-manager Deployment (`dbManager.enabled`). The
   `db.kube-dc.com` and `security.kube-dc.com` CustomResourceDefinitions stay
   installed, so nothing is deleted and the step is reversible.
3. Leave the CRDs in place until a later release removes them. Deleting a CRD
   deletes every object of that kind; do it only after the check in step 2 is
   empty on every cluster that shares the chart.

The MariaDB and PostgreSQL operators that db-manager drove are also the
operators managed services use for those families. Do not remove them.

## Related

- [Enabling managed services](managed-services-enable.md)
- [The managed-services catalog](managed-services-catalog.md)
- [Migrating from db-manager databases](/cloud/managed-services-migration)
