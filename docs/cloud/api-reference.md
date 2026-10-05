# Automate with the API

This page is for tenants who want to script their Kube-DC organization. After
reading it you can find the API reference, try requests in the browser, and
call the API from a script with your own token.

Everything the console does goes through the Kube-DC API, and you can call it
yourself: from scripts, CI pipelines, or your own tools. It covers virtual
machines, Kubernetes workloads and Managed Clusters, networking, storage,
databases and managed services, secrets, certificates and keys, and your
organization's projects, users and billing.

## Open the API reference

The API reference is at [backend.kube-dc.cloud](https://backend.kube-dc.cloud/).
On another Kube-DC installation, it is at `https://backend.<domain>/`.

The reference lists every operation with its parameters, request and response
examples, and errors. Its introduction explains authentication, permissions,
errors and paging, and its **Changelog** lists changes to the API, with
breaking changes marked.

The machine-readable OpenAPI description is at
[/api-docs.json](https://backend.kube-dc.cloud/api-docs.json), for generating
clients or importing into tools such as Postman.

## Try requests in the browser

1. Open the API reference.
2. In the bar at the top, enter your organization and, optionally, a project.
   The reference fills them into the paths for you.
3. In the **Authentication** panel, choose **Authorize** and sign in with your
   usual Kube-DC account.
4. Open an operation and choose **Test Request**.

Requests run with your own permissions, exactly as in the console.

## Call the API from a script

Get a token with the `kube-dc` CLI (see [CLI and kubeconfig](cli-kubeconfig.md)),
then send it as a bearer token:

```bash
kube-dc login --domain kube-dc.cloud --org acme
TOKEN=$(kube-dc credential --server https://kube-api.kube-dc.cloud:6443 --realm acme | jq -r .status.token)
API=https://backend.kube-dc.cloud/api

# List the virtual machines of project "web"
curl -H "Authorization: Bearer $TOKEN" $API/virtual-machines/acme-web/vms
```

Most paths take the project namespace, `<organization>-<project>` (here
`acme-web`). Tokens are short-lived; run `kube-dc credential` again for a fresh
one when a request returns 401.

## Permissions

The API enforces the same roles as the console. Organization admins manage
projects, users, quotas and billing; other users work in the projects their
groups give them a role in. A request outside your roles returns 403. See
[Team management](team-management.md).
