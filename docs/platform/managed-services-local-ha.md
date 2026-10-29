# Install managed services with local HA storage

Use a separate local-path class for HA databases and Kafka while keeping an
expandable block class for development services. Require three distinct Ready,
schedulable hosts, a project bucket provider, and qualified managed-services
artifacts.

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
reviewed three-manager inventory. Catalog publication remains subject to live
storage, operator-version, backup and capacity qualification.

After scaffolding, inspect the generated storage-role and budget values and the
`local-ha-storage` catalog component before catalog adoption. Do not treat a
successful scaffold as runtime qualification.
