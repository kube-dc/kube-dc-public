# Tutorials

Choose a path by what you are trying to operate. Most application work happens
inside a Project; Kubernetes platform software belongs in a Managed Cluster.

## First Session

1. [Sign Up and Log In](sign-up-login.md)
2. [Understand Organizations, Projects, and Managed Clusters](core-concepts.md)
3. [Create Your First Project](first-project.md)
4. [Configure CLI Access](cli-kubeconfig.md)
5. [Deploy Your First Application](deploy-first-app.md)

## Run an Application in a Project

- [Projects](kubernetes-projects.md): understand the supported API boundary
- [Deploy a WordPress Stack](deploy-wordpress-stack.md): combine an app with managed services
- [Service Exposure](service-exposure.md): publish HTTP, HTTPS, TCP, or UDP
- [Scaling and Performance](scaling-performance.md): size from measurements
- [GitOps](gitops.md): deliver from an external controller or CI

A Project is backed by the `{organization}-{project}` namespace. Use the
Project name in product language and the backing namespace only in YAML or `kubectl`
commands.

## Run a Virtual Machine

1. [Create a Virtual Machine](creating-vm.md)
2. [Connect to the VM](connecting-vm.md)
3. [Manage the VM Lifecycle](vm-lifecycle.md)
4. [Choose Block Storage](block-storage.md)
5. [Configure External or Floating IPs](public-floating-ips.md)

## Use Managed Data Services

- [Managed Databases](managed-databases.md)
- [Database Credentials](database-credentials.md)
- [Object Storage](object-storage.md)
- [Secrets Manager](secrets-manager.md)
- [Key Management](kms.md)
- [Data Protection and Recovery](backups-snapshots.md)

## Operate a Managed Cluster

Use a Managed Cluster when an application needs its own Kubernetes API,
operators, CRDs, multiple namespaces, or cluster-scoped administration.

1. [Provision a Managed Cluster](provisioning-cluster.md)
2. [Manage Workers, Storage, Exposure, and Upgrades](cluster-management.md)
3. [Install GitOps in the Managed Cluster](gitops.md#pattern-2-gitops-in-a-managed-cluster)

Managed Cluster workers consume quota from the parent Project.

## Manage Access and Security

- [User and Group Management](team-management.md)
- [Security Restrictions](security-restrictions.md)
- [Certificate Management](certificate-manager.md)

Organization admins manage membership and Project role assignments. Project
roles do not remove the platform admission policies that protect shared
infrastructure.

## Platform Operator Guides

Installation, shared networking, identity-provider configuration, and platform
recovery are operator responsibilities. They are documented separately in the
[Platform Guide](/platform). Project users should not run platform-cluster
commands from customer tutorials.
