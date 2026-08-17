---
name: manage-networking
description: Manage Kube-DC Project networking with EIp and FIp resources, Service exposure, and Organization-authorized Routed Network attachments to approved external destinations.
---

## Prerequisites

- The target Project is Ready.
- Know its backing namespace: `{organization}-{project}`.
- Check Organization public IPv4 quota before requesting a public address.
- Confirm the provider offers the requested `public` or `cloud` pool.
- For a Routed Network, confirm the platform has allocated it to the
  Organization and the caller is an Organization administrator.

## Concepts

- **Project VPC**: the isolated network created with the Project. Its default
  Multus network is `{backing-namespace}/default`.
- **EIp**: an address allocated from an external pool. Bind it to one or more
  LoadBalancer Services that expose selected ports.
- **FIp**: one-to-one NAT to an internal IP or VM interface. It exposes the
  target directly and does not load-balance.
- **Routed Network**: L3 reachability from the entire Project VPC to an explicit
  external-prefix allowlist. The platform manages BGP and the Project Internet
  default route remains unchanged.
- **Datacenter VLAN**: a separate L2 interface on selected workloads. Use the
  `manage-networking` platform documentation when direct segment attachment,
  rather than whole-Project routing, is required.

| Need | Use |
|---|---|
| HTTP/HTTPS hostname | Gateway route via the `expose-service` skill |
| Selected TCP/UDP ports | EIP-backed LoadBalancer |
| Direct access to one VM interface | FIP with `vmTarget` |
| Managed database workstation access | `spec.expose.type: loadbalancer` |
| Whole Project reaches approved corporate prefixes | Routed Network attachment |

## Create an EIP

```yaml
apiVersion: kube-dc.com/v1
kind: EIp
metadata:
  name: "{eip-name}"
  namespace: "{backing-namespace}"
spec:
  externalNetworkType: public # or cloud
```

Bind it to a LoadBalancer Service with:

```yaml
metadata:
  annotations:
    service.nlb.kube-dc.com/bind-on-eip: "{eip-name}"
```

`externalNetworkType` is immutable after allocation.

## Create a Floating IP for a VM

```yaml
apiVersion: kube-dc.com/v1
kind: FIp
metadata:
  name: "{fip-name}"
  namespace: "{backing-namespace}"
spec:
  externalNetworkType: public
  vmTarget:
    vmName: "{vm-name}"
    interfaceName: default
```

When `externalNetworkType` is set, the FIP controller creates and owns its
required EIP. Do not create a second EIP for the same FIP. Alternatively, an
advanced manifest can reference an existing EIP with `spec.eip`, but
`spec.eip` and `spec.externalNetworkType` are mutually exclusive.

## Inspect Status

```bash
# EIP readiness and allocated address
kubectl get eip {eip-name} -n {backing-namespace} \
  -o jsonpath='{.status.ready}{"\t"}{.status.ipAddress}{"\n"}'

# FIP readiness, external address, and resolved target
kubectl get fip {fip-name} -n {backing-namespace} \
  -o jsonpath='{.status.ready}{"\t"}{.status.externalIP}{"\t"}{.status.resolvedTargetIP}{"\n"}'

kubectl describe eip {eip-name} -n {backing-namespace}
kubectl describe fip {fip-name} -n {backing-namespace}
```

A Ready EIP has a non-empty `.status.ipAddress`. A Ready FIP has non-empty
`.status.externalIP` and `.status.resolvedTargetIP`.

## Network Types

- `public`: internet-routable subject to firewall and provider policy.
- `cloud`: not internet-routable; reachable only from configured cloud or
  platform networks.

A Project's `egressNetworkType` chooses its default gateway address pool. It
does not by itself prevent a cloud Project from requesting a public EIP when
the provider enables that pool and quota is available.

## Attach a Routed Network

Prefer the filtered console at **Organization → Networks → Routed Networks** to
discover allocations and attach/detach Projects. BGP peers, ASNs, authentication,
imports, and exports are platform-owned and must never be inferred or authored.

When the caller already knows the allocated handle, create the namespaced
request from `routed-network-attachment-template.yaml`:

```yaml
apiVersion: kube-dc.com/v1
kind: ProjectRouteAttachment
metadata:
  name: "{attachment-name}"
  namespace: "{backing-namespace}"
spec:
  allocationRef: "{routed-network-allocation}"
  direction: routed-egress
```

Organization administrators receive only `create` and `delete`; they do not
receive cluster-wide discovery or `update`/`patch`. Admission verifies that the
allocation and Project belong to the caller's Organization and rechecks every
prefix collision.

View tenant-safe health at **Project → Network → Routed Networks**. Confirm:

- `Internet gateway: unchanged`;
- `BGP is managed by your Organization`;
- `These routes are available to this Project only`; and
- status is `Ready`, or `Degraded` with at least one ready replica.

To detach a known request:

```bash
kubectl delete projectrouteattachment {attachment-name} \
  -n {backing-namespace}
```

Deletion drains managed gateways and retains the destination drop until the
transport is gone. Do not force-remove the finalizer.

## Safety

- Never infer address-pool availability from the network type alone.
- A public FIP target cannot also be a cloud LoadBalancer backend.
- Prefer Service exposure when only selected ports should be reachable.
- Use an FIP only when direct one-to-one access is intended.
- A Routed Network is not Internet exposure and never accepts `0.0.0.0/0`.
- Never edit a `ProjectRoutingGateway`, FRR ConfigMap/Secret, gateway Deployment,
  routing-link Subnet/NAD, or VPC route. They are controller-owned security
  assets and admission should deny the mutation.
- Routed Networks are `routed-egress` in v1: Project-initiated flows and replies
  only. They do not peer Projects or make a Project transit.
- Delete unused public addresses to release quota.
