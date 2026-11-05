# Install managed services with local HA storage

As a platform operator, use this page to install managed services with
node-local storage for the replicated Production plans. After you follow it,
those plans use a `local-path` class through the `database-ha` storage role.
The Development plans, except OpenSearch, keep an expandable block class
through the `database` role.

The installation needs the following:

- Three distinct Ready, schedulable hosts.
- A Project bucket provider.
- A managed-services release.

The `local-ha-storage` catalog component moves the Production plans of
PostgreSQL, MySQL, MariaDB, ClickHouse, Valkey and Kafka to `database-ha`.
With an HA class, the installer also keeps both OpenSearch plans and the
RabbitMQ Production plan on `database-ha`. See
[Storage roles and backups](managed-services-catalog.md#storage-roles-and-backups).

Add these options to your reviewed `kube-dc bootstrap init` invocation:

```sh
--managed-services=on \
--services-storage-budget=100Gi \
--services-ha-storage-class=local-path \
--services-ha-storage-budget=128Gi
```

The development class is derived from the selected expandable Rook storage.
For an existing target, you can select a live verified class with
`--services-database-class`.

The HA budget records logical allocation; it does not limit directory size.
Storage expansion changes that allocation without resizing local-path PVCs.
Existing physical-storage services retain their original storage contract.
Replicas and off-host backups remain necessary: local-path does not protect a
single volume against host or disk loss.

The CLI records both roles and budgets in its saved plan and input hash. Its
starter must advertise `managed-services-local-ha-v1`; an older starter is
refused. With a verified target kubeconfig, the CLI checks the local-path
provisioner, delayed volume binding, and three eligible hosts. For greenfield
installation, it accepts the starter-owned `local-path` class and requires a
reviewed three-manager inventory. Before you publish the catalog, check
storage, operator versions, backups, and capacity on the live cluster.

After scaffolding, inspect the generated storage-role and budget values and the
`local-ha-storage` catalog component before catalog adoption. A successful
scaffold does not prove that services run: create and test a service before you
publish the catalog.
