---
title: Provisioning API for hosting providers
---

# Provisioning API for hosting providers

A hosting provider's portal can create, size, suspend and delete Kube-DC
Organizations for its customers, sign customers into the console and read
their usage. It does this through the Kubernetes API of the installation, as a
**machine identity**, plus a small extension API group,
`platform.kube-dc.com/v1alpha1`. There is no separate provider API or API key:
Kubernetes RBAC and admission policy decide what the portal may do.

The PHP SDK (`kube-dc/platform-sdk`, `sdk/platform-sdk/` in the repository)
wraps all of it.

## What the portal can do

| Capability | Resource |
| --- | --- |
| Create and delete organization namespaces | `namespaces` (create only `<prefix>-*`) |
| Create, size, suspend, resume and delete organizations | `organizations.kube-dc.com`: `spec.quota` (verb `allocate`), `spec.suspension` (verb `suspend`), `spec.externalCommerce` |
| Create and delete projects | `projects.kube-dc.com` (network CIDR allocated automatically) |
| Manage console users | `users.platform.kube-dc.com` |
| One-click console login | `consolelogins.platform.kube-dc.com` (create only; single-use, 30 s) |
| Usage for invoicing | `organizationusages.platform.kube-dc.com` (current, and reserved capacity integrated over a window) |
| Read the plan catalog | ConfigMap `billing-plans` in the platform namespace |

It cannot read workloads or Secrets, change RBAC, or touch organizations
outside its prefix. Organization users cannot change size or lift a
suspension set by the provider.

## Setting up a provider

Enable the extension API in the chart values:

```yaml
platformApi:
  enabled: true
```

Then, in the admin console, open **Provisioning API** (superadmin):

1. **New client**: choose a client id (e.g. `webdock-portal`), the
   organization prefix (e.g. `wd`) and the grants. Creating organizations is
   always included; `users`, `consolelogins`, `organizationusages` and `plans`
   are separate, so creating organizations does not imply signing users in or
   reading usage. The page creates the Keycloak master-realm client (client
   credentials only, audience `kube-dc-machine`) and the RBAC bindings, and
   shows the secret once.
2. Give the provider what the **Connection** section shows: the API server
   URL, the cluster CA (download), the token URL, the client id and secret,
   and the prefix.

Later, the page changes grants, disables the client (tokens stop being
issued), rotates the secret or deletes the client. Deleting a client leaves
its organizations in place.

Clients can also be declared in the chart values. Their Keycloak client is
then created by hand (master realm, client credentials only, an audience
mapper adding `kube-dc-machine`), and the page shows them read-only apart from
enabling, disabling and rotating the secret:

```yaml
platformMachines:
  - clientId: webdock-portal
    prefix: wd
    grants: [users, consolelogins, organizationusages, plans]
```

## How the extension API works

`manager platform-apiserver` is registered as the APIService
`v1alpha1.platform.kube-dc.com`. The kube-apiserver authenticates each caller;
the extension server authorizes the exact verb, resource, namespace and name
with a SubjectAccessReview and applies the machine-prefix rule itself
(admission policies do not apply to aggregated APIs). Keycloak and metrics work
is done by the console backend's internal listener, which accepts only the
extension server's ServiceAccount token and is never exposed by the Gateway.

Usage history comes from the manager metric
`kube_dc_organization_resource_requests` (pod and volume requests, labelled
with the Organization UID), so a deleted and re-created organization of the
same name starts from zero. A window that cannot be measured completely is
reported as partial with null values, never as zero.
