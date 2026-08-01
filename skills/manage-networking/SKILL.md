---
name: manage-networking
description: Manage a Kube-DC Project's VPC addresses with EIp and FIp resources, and choose between Service-level exposure and direct VM access.
---

## Prerequisites

- The target Project is Ready.
- Know its backing namespace: `{organization}-{project}`.
- Check Organization public IPv4 quota before requesting a public address.
- Confirm the provider offers the requested `public` or `cloud` pool.

## Concepts

- **Project VPC**: the isolated network created with the Project. Its default
  Multus network is `{backing-namespace}/default`.
- **EIp**: an address allocated from an external pool. Bind it to one or more
  LoadBalancer Services that expose selected ports.
- **FIp**: one-to-one NAT to an internal IP or VM interface. It exposes the
  target directly and does not load-balance.

| Need | Use |
|---|---|
| HTTP/HTTPS hostname | Gateway route via the `expose-service` skill |
| Selected TCP/UDP ports | EIP-backed LoadBalancer |
| Direct access to one VM interface | FIP with `vmTarget` |
| Managed database workstation access | `spec.expose.type: loadbalancer` |

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

## Safety

- Never infer address-pool availability from the network type alone.
- A public FIP target cannot also be a cloud LoadBalancer backend.
- Prefer Service exposure when only selected ports should be reachable.
- Use an FIP only when direct one-to-one access is intended.
- Delete unused public addresses to release quota.
