---
name: create-project
description: Create a Kube-DC Project inside an existing Organization. Covers network selection, quota preflight, the Project manifest, and readiness verification.
---

# Create a Project

A Project is the governed workload boundary for applications, VMs, databases,
and Managed Clusters. Kube-DC implements it with a backing namespace named
`{organization}-{project}`, but users work with the Project name.

## Prerequisites

- The Organization exists and reports `status.ready: true`.
- The caller can create `Project` resources in the Organization API namespace.
- Organization quota has enough capacity for the planned workloads.
- The Project name is a valid lowercase Kubernetes name.

Confirm the Organization and current identity before making changes:

```bash
kubectl -n {organization} get organization {organization} -o custom-columns='NAME:.metadata.name,READY:.status.ready'
kubectl auth can-i create projects.kube-dc.com -n {organization}
```

## Choose the network type

`spec.egressNetworkType` is required and immutable.

| Value | Meaning | Use it when |
|---|---|---|
| `cloud` | The default gateway uses the provider's cloud-internal external network | Normal application and private-workload Projects |
| `public` | The default gateway uses the provider's public external network | A public source address at the Project gateway is required |

Prefer `cloud`. A `public` Project is available only when the platform operator
has configured a public network and enabled public Project creation. Neither
choice exposes a workload for inbound traffic; use a Gateway Route,
LoadBalancer Service, EIP, or FIP for that.

See [network-types.md](network-types.md) for the decision details.

## Choose the CIDR

`spec.cidrBlock` is required. Select a private subnet that does not overlap any
network the workloads must reach through routing, VPN, peering, or attached
networks. Do not assume `10.0.0.0/16` is appropriate for every environment.

## Create the Project

```yaml
apiVersion: kube-dc.com/v1
kind: Project
metadata:
  name: "{project}"
  namespace: "{organization}"
spec:
  cidrBlock: "{project-cidr}"
  egressNetworkType: cloud
```

Apply [project-template.yaml](project-template.yaml), then wait on the actual
readiness field:

```bash
kubectl apply -f project.yaml
kubectl -n {organization} wait --for=jsonpath='{.status.ready}'=true project/{project} --timeout=10m
```

## Verify

Read the generated namespace from status instead of reconstructing it in
automation:

```bash
PROJECT_NS="$(kubectl -n {organization} get project {project} -o jsonpath='{.status.namespace}')"

printf 'Backing namespace: %s\n' "$PROJECT_NS"
kubectl -n {organization} get project {project} -o custom-columns='NAME:.metadata.name,READY:.status.ready,NAMESPACE:.status.namespace'
kubectl get namespace "$PROJECT_NS"
kubectl -n "$PROJECT_NS" get network-attachment-definition default
kubectl -n "$PROJECT_NS" get eip default-gw
kubectl -n "$PROJECT_NS" get secret ssh-keypair-default authorized-keys-default
```

`status.ready: true` means the core Project resources reconciled. Optional
services have their own readiness and must be checked separately.

If readiness does not become true:

```bash
kubectl -n {organization} describe project {project}
kubectl -n {organization} get project {project} -o yaml
```

Inspect the Project conditions and events. For a rejected `public` Project,
use `cloud` or ask the platform operator whether public Projects are supported.

## Safety

- Treat the Project as the user-facing isolation and governance boundary; call
  `{organization}-{project}` the backing namespace only when a Kubernetes
  command needs it.
- Do not change `egressNetworkType` after creation; the API makes it immutable.
- Check quota before creating workloads. A new empty Project consumes little,
  but its workloads consume the Organization's shared quota.
- Delete a Project through its `Project` resource. Do not delete only its
  backing namespace or strip finalizers; coordinated cleanup releases network,
  identity, storage, and security state.
