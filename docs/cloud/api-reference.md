# Automate with the API

This page is for tenants who want to script their Kube-DC organization. After
reading it you can find the API reference, try requests in the browser, and
call the API from a script with your own token.

The console works through two APIs, and you can call both yourself with the
same sign-in: from scripts, CI pipelines, or your own tools.

- The **Kube-DC API** (`https://backend.<domain>/api`), described on this
  page: Kubernetes workloads, Managed Clusters, networking, object storage,
  managed services, secrets, certificates and keys, audit logs, and your
  organization's projects, users, groups and billing.
- The **Kubernetes API** (`https://kube-api.<domain>:6443`), for everything
  that is a Kubernetes object: virtual machines,
  [Applications](applications.md) (`apps.kube-dc.com/v1alpha1`), Projects,
  Floating IPs, [machine identities](machine-identities.md) and
  [event webhooks](event-webhooks.md). Use `kubectl`, the `kube-dc` CLI
  (`kube-dc app`, `kube-dc secrets`, …) or any Kubernetes client.

## Open the API reference

Open **API reference** in the console's user menu: it opens with your
organization and project filled in. The address is `https://backend.<domain>/`
unless your provider set another one; `<domain>` is the Kube-DC domain your
provider gives you.

The reference lists every operation with its parameters, request and response
examples, and errors. Its introduction explains authentication, permissions,
errors and paging, and its **Changelog** lists changes to the API, with
breaking changes marked. A deprecated operation is marked with its
replacement and removal date, stays at least 90 days, and answers with
`Deprecation` and `Sunset` headers.

The machine-readable OpenAPI 3.1 description is at
`https://backend.<domain>/api-docs.json`. Use it to generate clients or to
import the API into tools such as Postman.

## Try requests in the browser

1. Open the API reference.
2. In the bar at the top, enter your organization and, optionally, a project,
   and click **Use**. The reference fills them into the paths for you.
3. In the **Authentication** panel, choose **Authorize** and sign in with your
   usual Kube-DC account.
4. Open an operation and choose **Test Request**.

Requests run with your own permissions, exactly as in the console.

## Call the API from a script

Get a token with the `kube-dc` CLI (see [CLI and kubeconfig](cli-kubeconfig.md)),
then send it as a bearer token:

```bash
kube-dc login --domain example.com --org acme
TOKEN=$(kube-dc credential --server https://kube-api.example.com:6443 --realm acme | jq -r .status.token)
API=https://backend.example.com/api

# List the pods of project "web"
curl -H "Authorization: Bearer $TOKEN" $API/system/acme-web/pods

# Virtual machines are Kubernetes objects: read them from the Kubernetes API.
# kubectl uses the sign-in and the API certificate trust that login saved.
kube-dc use example.com/acme/web
kubectl get virtualmachines
```

Replace `example.com` with your Kube-DC domain and `acme` with your
Organization ID. Most paths take the project namespace, `<organization>-<project>` (here
`acme-web`). Tokens are short-lived (15 minutes by default); run
`kube-dc credential` again for a fresh one when a request returns 401. More
than 1200 requests a minute per user (the default) return 429 with a
`Retry-After` header.

`kube-dc login` signs in a person in a browser. For a pipeline with no person,
use a [machine identity](machine-identities.md).

## Permissions

The API enforces the same roles as the console. Organization admins manage
projects, users, quotas and billing; other users work in the projects their
groups give them a role in. A request outside your roles returns 403. See
[Team management](team-management.md).
