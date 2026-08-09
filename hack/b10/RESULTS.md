# B-10 acceptance run — RESULT

**Date:** 2026-07-29
**Cluster:** stage (stage-admin)
**Release:** kube-dc v0.5.36 (chart + all four images, one bundle)
**Fabric:** Hetzner vSwitch #65858, VLAN 4014, ProviderNetwork `ext-cloud`
**Range:** 192.168.214.0/24, gateway/peer 192.168.214.1 (the bastion — a real
machine on the wire that is NOT part of the cluster)
**Nodes:** master-1 + worker-1 on the vSwitch; worker-2 deliberately not

## Result: **PASS — 58/58 checks, complete**

| Script | Checks | Result |
|---|---|---|
| 00-preflight | 8 | PASS |
| 20-declare | 10 | PASS |
| 30-pod | 8 | PASS |
| 40-vm | 11 | PASS |
| 50-breach | 17 | PASS |
| 70-teardown | 12 | PASS |
| 60-resilience | 10 | PASS |

## What was actually proven on the wire

- A pod and a VM, on **different nodes**, both attached from manifests the
  **tenant API itself produced** — not hand-written.
- **Off-cluster reachability**: the bastion (192.168.214.1), which is not part of
  this cluster, reached both the pod and the VM at 0% loss.
- **Cross-node on the physical wire**: pod on worker-1 → VM on master-1, 0% loss.
  Same-host traffic would have proven only that OVS works.
- The **default route stayed on the tenant VPC** in both cases; the customer's
  gateway never carries tenant egress.
- MTU 1400 enforced: full-size frames cross, oversized are rejected.
- KubeVirt exposed **both** interfaces — the assertion that catches a VM
  fragment that looks right but attaches nothing.
- Every one of 17 breach vectors refused, including cross-namespace Multus in
  both spellings, tenant-written NADs, alias Subnet/Vlan on a held wire,
  deleting the reservation, provider-scoped IP/MAC/route pinning, placement on
  an unprepared node, and a **forged `readyNodes`** entry.
- Teardown is genuinely drain-first: observed `ports=4 → 2 → 0`, with the NAD
  held throughout and removed only at zero. Held independently by a Terminating
  pod and by surviving OVN allocations. The running workload was never
  disturbed, and the wire was grantable again afterwards.

## Found by running it (would not have been found by review)

1. **Release blocker — the gate deadlocked the CNI.** It matched any key ending
   `kubernetes.io/logical_switch`, but kube-ovn writes the bare
   `ovn.kubernetes.io/logical_switch` on every pod. Every pod hit a fail-closed
   webhook whose own manager could not get an address, because kube-ovn's patch
   to allocate it was the thing being blocked. Fixed in v0.5.36 two ways
   (bare-key exclusion + CNI identity exemption), both contract-tested.
2. **The mandatory second manager replica was unschedulable.** The chart refuses
   `enabled=true` below two replicas, but `nodeSelector: {kube-dc-manager:true}`
   matched one node. Enabling requires >=2 nodes carrying that label.
3. `ext-public` is a /28 with 12 usable addresses against a health suite that
   churns orgs constantly — the real B-09 root cause, still open.

## Fail-closed behaviour (60-resilience)

Both halves of the property the design rests on, proven with the manager scaled
to zero:

- an **attaching** pod is REFUSED while the gate is down — the fail-closed
  guarantee; nothing reaches a VLAN unauthorised even during an outage;
- an **ordinary** pod is still ADMITTED — which is what makes a cluster-wide
  fail-closed webhook survivable at all, and is precisely what the
  matchCondition bug broke on the first attempt.

Also: both pod kinds admitted mid-rollout, a PDB protects the manager,
attachment recovers after a cold start with no intervention, and the webhook
certificate survives the whole cycle.

## Still to do

- Re-arm the stage release gate (`clusters/stage/platform.yaml`, `suspend: true`).
- §18.4 remains true for a real customer: address authority must be agreed, not
  assumed. It holds here only because this vSwitch is entirely ours.
| 14:15:52 | PASS | at least two manager replicas |
| 14:15:52 | PASS | a PodDisruptionBudget protects the manager |
| 14:15:56 | PASS | an attaching pod is admitted mid-rollout |
| 14:15:57 | PASS | an ordinary pod is admitted mid-rollout |
| 14:16:38 | PASS | rollout completed |
| 14:17:17 | PASS | an ATTACHING pod is refused while the gate is down |
| 14:17:18 | PASS | an ORDINARY pod is still admitted while the gate is down |
| 14:17:43 | PASS | manager recovered to 2 replicas |
| 14:17:44 | PASS | attachment admitted after a cold start |
| 14:18:34 | PASS | webhook service has endpoints |
