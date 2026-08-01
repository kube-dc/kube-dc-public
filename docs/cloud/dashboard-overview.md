---
sidebar_label: Dashboard Overview
title: Navigating the Kube-DC Dashboard
---


The Kube-DC dashboard is the central interface for managing Projects, workloads, virtual machines, Managed Clusters, and your account.

## Projects View

After logging in, you land on the **Projects** page. It lists all projects in your organization along with their status, network CIDR, running pods, resource quotas, and creation date.

![Projects view with navigation menu](images/projects-view-navigation.png)

From here you can:

- **Go to Project** — open the workloads dashboard for a specific project
- **Details** — view project configuration and resource limits
- **Delete** — remove a project (requires appropriate permissions)

### User Menu

Click your name in the top-right corner to open the user menu with the following options:

| Menu Item | Description |
|---|---|
| **Manage Workloads** | Open the workloads dashboard for the selected project |
| **Project console** | Launch a web-based terminal with `kubectl` access scoped to your projects |
| **Manage user** | Open account settings (password, 2FA) |
| **Logout** | Sign out of the dashboard |

---

## Workloads Dashboard

Selecting **Manage Workloads** or clicking **Go to Project** takes you to the main workloads dashboard.

The workloads dashboard provides:

### Quick Actions

Three action cards at the top let you jump straight into common tasks:

- **Get CLI Access** — download your kubeconfig for programmatic access via `kubectl`
- **Deploy Virtual Server** — create a new Linux or Windows virtual machine
- **Create Managed Cluster** — provision a Managed Cluster with its own Kubernetes API

### Sidebar Navigation

The left sidebar shows a tree view of all resources in the current project:

- **Virtual Machines** — grouped by OS (e.g., `debian`, `ubuntu`, `win`)
- **Managed Clusters** — clusters and their worker pools

### Project Overview

The center panel adapts to the provider's billing mode:

- **Subscription plans** show running and total Pods and VMs, storage volume count and size, LoadBalancers, and public IPs.
- **Metered plans** show running compute usage and the current billing-period totals.

### Resource Quotas

When quota data is available, **Quota Usage** compares Project use with either its Project cap or the Organization's shared pool. It can include CPU, memory, storage, Pods, public IPv4, object storage, and provider-enabled accelerators.

Organization Admins can select **View Organization Billing** to open plan, usage, and cost details. Other Project members do not see that action.

---

## Switching Projects

Use the **project switcher** dropdown at the top of the dashboard (next to the Kube-DC logo) to switch between projects without returning to the projects list.

![Project switcher dropdown](images/change-projects-tab.png)

Simply click the current project name and select another project from the dropdown.

---

## Resource Tabs

Below the top navigation bar, a row of icon tabs lets you switch between different resource categories within the current project.

![Resource navigation tabs](images/manage-resources-k8s.png)

The tabs from left to right are:

| Resource Area | What You Will Find |
|---|---|
| **Compute** | Pods, Deployments, StatefulSets, DaemonSets, Jobs |
| **Kubernetes Resources** | ConfigMaps, Secrets, ServiceAccounts, and platform-provided custom resources |
| **Volumes** | PersistentVolumeClaims, storage usage |
| **Network** | Services, Ingresses, Load Balancers, IPs |
| **Object Storage** | S3-compatible buckets and access credentials |

---

## Organization Management

From the Projects page, click the **Kube-DC** logo in the top-left corner to access the organization management view.

![Organization management view](images/kube-dc-manage-org-view.png)

The left sidebar provides access to:

- **Projects** — create, view, and manage projects
- **Users** — invite and manage organization members
- **Organization Groups** — manage user groups and role assignments
- **Project Roles** — define custom roles for project-level access control
- **Billing** — view usage, costs, and billing plan details
- **Audit Logs** — review actions performed across the organization
- **Settings** — configure organization-level settings

---

## Project Web Console

Select **Project console** from the user menu to launch a browser-based terminal. The console provides a pre-authenticated `kubectl` session scoped to the projects in your organization.

From the console you can:

- List and switch Project contexts with `kube-dc use`
- Run `kubectl` commands (aliased as `kgp`, `kgs`, etc.)
- Manage resources directly without installing any CLI tools locally

---

## Account Settings

Select **Manage user** from the user menu to open your account settings.

![Account settings — password and 2FA](images/accont-change-password.png)

### Change Password

Under **Basic authentication**, click **Update** next to your password entry to set a new password. The page shows when your current password was created.

### Two-Factor Authentication (2FA)

Under **Two-factor authentication**, click **Set up Authenticator application** to enable 2FA using an app like Google Authenticator or Authy. Once configured, you will be prompted for a verification code on every login.

:::tip
Enabling 2FA is strongly recommended to protect your account, especially for organization administrators.
:::

---

## Next Steps

- [Core Concepts](core-concepts.md) — understand organizations, projects, and resource isolation
- [Creating Your First Project](first-project.md) — set up your first project
- [CLI & Kubeconfig Access](cli-kubeconfig.md) — manage resources from the command line
- [Team Management](team-management.md) — invite users and assign roles
