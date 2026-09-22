# Kube-DC Cloud

Kube-DC Cloud is a managed cloud platform. You deploy applications, virtual
machines, and Managed Clusters. The service provider operates the
infrastructure underneath.

This documentation is for tenants: the people who build and run workloads in a
Kube-DC organization. For the operator documentation, see the
[Platform docs](/platform).

## What you can do

- Deploy applications, compatible Helm charts, and VMs straight into a
  [Project](kubernetes-projects.md) with its kubeconfig. There is no cluster to
  provision first.
- Run Linux and Windows [virtual machines](creating-vm.md) from the console or
  with `kubectl`.
- Provision a [Managed Cluster](provisioning-cluster.md) of your own for
  operators, custom resource definitions, and other cluster-scoped software.
- Connect and expose workloads with a private network per Project, public and
  floating IPs, load balancers, and HTTPS ingress. See
  [Service exposure](service-exposure.md).
- Store data on persistent block volumes, in S3-compatible object storage, and
  in snapshots and backups.
- Run [managed services](managed-services.md): PostgreSQL, MySQL, MariaDB,
  ClickHouse, Valkey, and Kafka, with backups, credentials delivered as
  Secrets, and day-2 operations.
- Keep API tokens out of Git with [Secrets Manager](secrets-manager.md), and
  sync selected values into Kubernetes Secrets.
- Manage users, roles, and billing across your organization. See
  [Team management](team-management.md).

## Where to start

If you are new to the platform, read [What is Kube-DC?](what-is-kube-dc.md) and
[Core concepts](core-concepts.md) first.

To build something, follow these pages in order:

1. [Create your first Project](first-project.md).
2. [Deploy your first application](deploy-first-app.md).
3. [Set up the CLI and kubeconfig](cli-kubeconfig.md).

For a specific task, use these entry points:

- To add a database, create a [managed service](managed-services.md) from the
  catalog.
- To publish a workload on the internet, see
  [Public and floating IPs](public-floating-ips.md).
- To store credentials outside Git, see [Secrets Manager](secrets-manager.md).

## Get help

For Slack, GitHub issues, and professional support options, see
[Community and support](community-support.md).
