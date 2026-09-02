# Project-Internal LoadBalancer VIPs

How a Managed Cluster's `LoadBalancer` Services get a private VIP inside the
Project VPC, what an operator switches on, and how to read the state when
something is not delegated.

This page is for **operators and SREs running a Kube-DC cluster**. For the
tenant-facing contract (the label and `loadBalancerClass` a user sets on a
Service), see [Manage a Managed Cluster](/cloud/cluster-management).

---

## What it is

A tenant Service inside a Managed Cluster can take an address from a
**per-project VIP pool** instead of a platform external IP:

```yaml
metadata:
  labels:
    network.kube-dc.com/lb-pool: project
spec:
  type: LoadBalancer
  loadBalancerClass: kube-dc.com/project
```

The address is reachable from everything in the same Project — VMs, platform
pods, other Managed Clusters — and from nowhere else. It consumes no external
IP and no public-IP quota.

## Why it works this way

Managed-cluster workers are KubeVirt VMs behind MASQUERADE networking, so no
guest can answer ARP for a VIP. The platform therefore does the work: MetalLB
runs inside the tenant as an **allocator only** (the speaker is disabled and
admission policies deny announcement objects), and the platform realises each
assigned address as an **OVN load-balancer rule** on the Project's VPC.

The pool lives **outside** every project subnet in a dedicated per-VPC CIDR
(`--vip-pool-cidr`, default `10.242.0.0/24`), so a VIP never consumes an
address in the tenant's own subnet.

## Enabling it

Two settings, both per management cluster:

| Setting | Where | Effect |
|---|---|---|
| `LB_BLOCK_MODE` | `cluster-config.env` | `off` \| `observe` (default) \| `enable` |
| `VIP_POOL_CIDR` | `cluster-config.env` | The pool CIDR. **Pin this on any cluster that has already delegated VIPs.** |

With `LB_BLOCK_MODE=enable`, every KubeVirt-backed Managed Cluster on that
management cluster participates automatically — no per-cluster action, no
tenant request. The tenant profiles (allocator, guardrails, pools) are
delivered by the controller; operators do not stamp labels by hand.

Advanced overrides:

- `network.kube-dc.com/lb-block: disabled` on a `KdcCluster` — opt that one
  cluster out.
- `LB_BLOCK_REQUIRE_OPT_IN=true` — restore explicit per-cluster opt-in
  (`network.kube-dc.com/lb-block: enabled` required).
- `network.kube-dc.com/address-reservation: disabled` on a Project namespace —
  exclude the whole Project.

:::warning[CIDR changes are not an in-place migration]
Arenas record the CIDR their addresses came from. Changing `VIP_POOL_CIDR` on a
cluster that already delegated VIPs revokes every live delegation until an
operator migrates. Pin the existing value; see the migration runbook below.
:::

## Reading the state

```bash
# per-cluster delegation
kubectl -n <project-ns> get kdccluster <name> \
  -o jsonpath='{.status.loadBalancer}' | jq

# the reservations backing it
kubectl get addressreservation | grep <cluster>

# the project's ledger
kubectl get addressarena vip-<project-ns>
```

`LoadBalancerBlockReady` reasons an operator will actually see:

| Reason | Meaning |
|---|---|
| `Delegatable` | Working. Blocks delegated and the tenant pool is enabled. |
| `Pending` | A reservation has not settled yet. Resolves by itself. |
| `PoolExhausted` | **Operator action needed.** No free block; see capacity below. |
| `TenantNetworkConflict` | The cluster's own pod/service CIDR overlaps the VIP pool. |
| `ProviderUnsupported` | Not a KubeVirt cluster — LoadBalancers take the provider's own path. |
| `OptedOut` / `NotActivated` | Excluded by annotation or by `LB_BLOCK_REQUIRE_OPT_IN`. |
| `ClusterAPIObjectMissing` | No CAPI Cluster yet; transitional during provisioning. |

## Capacity

Each cluster is delegated a **16-address block** (`spec.loadBalancer.blocks`,
1–8, grow-only). The binding limit is **not** the address space but the
project's arena ledger, which holds **64 claim slots**.

A block released by a deleted cluster is **reclaimed** once teardown is proven
— no mirror or SwitchLBRule references the range, and two leader-consistent
OVN-NB reads five seconds apart show it absent. Until that proof holds the
range stays tombstoned and keeps its slot. So capacity is bounded by
*concurrent* usage, not by how many clusters a project has ever created.

Watch it before it bites:

- `kube_dc_manager_address_arena_claim_slots_used` / `_maximum`
- alert `KubeDcAddressArenaClaimSlotsHigh` fires at 75%
- alert `KubeDcAddressReservationExhausted` fires at the wall

## Migrating the pool CIDR

Operator-only, and not an in-place change. Releasing a reservation tombstones
its range; it does **not** reclaim capacity into a different pool.

1. Revoke, and confirm it: the certificate is republished disabled and the
   datapath drains (no mirrors, no `SwitchLBRule` for the cluster).
2. Set `spec.disposition: Release` on every old reservation.
3. Delete the released reservation records so a rebuild cannot resurrect the
   old claims.
4. Retire the old arena explicitly — it is finalizer-protected while the
   Project lives.
5. Create and re-authorize against the new pool.

## Limits

- `externalTrafficPolicy: Local`, SCTP and client source-IP preservation are
  not supported on this path; traffic arrives source-NATed.
- KubeVirt-backed clusters only.
- Turning the feature off withdraws the **pool**. The allocator and its
  guardrail policies remain until the cluster is deleted, so `off` does not
  mean "the tenant may now install their own LoadBalancer stack".
