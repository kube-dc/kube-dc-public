# Tutorials

Choose a path by what you want to operate. Most application work happens inside
a Project. Kubernetes platform software belongs in a Managed Cluster.

## First session

1. [Sign up and sign in](sign-up-login.md)
2. [Understand Organizations, Projects, and Managed Clusters](core-concepts.md)
3. [Create your first Project](first-project.md)
4. [Configure CLI access](cli-kubeconfig.md)
5. [Deploy your first application](deploy-first-app.md)

## Run an application in a Project

- [Projects](kubernetes-projects.md) explains the supported API boundary.
- [Deploy a WordPress stack](deploy-wordpress-stack.md) combines an app with managed services.
- [Service exposure](service-exposure.md) publishes HTTP, HTTPS, TCP, or UDP.
- [Scaling and performance](scaling-performance.md) sizes a workload from measurements.
- [GitOps](gitops.md) delivers from an external controller or from CI.

The `{organization}-{project}` namespace backs each Project. Use the Project
name in prose, and the backing namespace only in YAML or in `kubectl` commands.

## Run a virtual machine

1. [Create a virtual machine](creating-vm.md)
2. [Connect to the VM](connecting-vm.md)
3. [Manage the VM lifecycle](vm-lifecycle.md)
4. [Choose block storage](block-storage.md)
5. [Configure external or floating IPs](public-floating-ips.md)

## Use managed data services

- [Managed services](managed-services.md): databases, caches, and brokers from the catalog
- [Credentials and rotation](postgresql-credentials.md)
- [Object storage](object-storage.md)
- [Secrets Manager](secrets-manager.md)
- [Key management](kms.md)
- [Data protection and recovery](backups-snapshots.md)

## Operate a Managed Cluster

Use a Managed Cluster when an application needs its own Kubernetes API,
operators, CRDs, multiple namespaces, or cluster-scoped administration.

1. [Provision a Managed Cluster](provisioning-cluster.md)
2. [Manage workers, storage, exposure, and upgrades](cluster-management.md)
3. [Install GitOps in the Managed Cluster](gitops.md#pattern-2-gitops-in-a-managed-cluster)

Managed Cluster workers consume quota from the parent Project.

## Manage access and security

- [User and group management](team-management.md)
- [Security restrictions](security-restrictions.md)
- [Certificate management](certificate-manager.md)

Organization admins manage membership and Project role assignments. Project
roles do not remove the platform admission policies that protect shared
infrastructure.

## Platform operator guides

Installation, shared networking, identity-provider configuration, and platform
recovery are operator responsibilities. They are documented separately in the
[Platform guide](/platform). Project users should not run platform-cluster
commands from tenant tutorials.
