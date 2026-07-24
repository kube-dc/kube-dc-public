# GPU capacity reservations

GPU quota and GPU reservation are different products in kube-dc:

- a GPU add-on grants concurrent-use **entitlement** through organization and
  project quota; a workload may queue when healthy matching capacity is busy;
- a GPU reservation is an operator-held amount of matching healthy physical
  capacity for one organization, backed by a separate capacity ledger and an
  operator-only entitlement.

The normal **Manage add-ons** action does not create a capacity reservation.
Do not promise guaranteed capacity from an add-on assignment alone.

## Pilot decision

The MVP may offer manual, fungible whole-device reservations only. It does not
promise a particular PCI address, device UUID, or host. The guarantee is that
the named organization can consume the reserved number of healthy devices from
the named profile pool, subject to an announced maintenance or hardware-failure
event.

A pool with a single GPU node switches that node sequentially between
Shared Pod and Dedicated VM modes. A Dedicated VM reservation therefore keeps
that node in `vm-passthrough` mode and takes its devices out of the Shared GPU
pool. It is not safe to sell Shared and Dedicated reserved capacity from the
same sequential node at the same time. Until a dedicated pool exists and the
steps below pass, no reserved SKU is orderable.

## Capacity invariant

For every stable profile pool, all active and pending changes must satisfy:

```text
healthy schedulable devices
  >= active reserved devices
   + non-reserved organization hard quota
   + operator spare
```

`non-reserved organization hard quota` is the sum across organizations, not
current usage. Project caps do not add capacity because the organization HRQ
is the final concurrent-use ceiling. Quota from another workload mode or GPU
profile is not interchangeable.

This invariant is what turns fungible quota into a guarantee. If ordinary
dedicated entitlement is oversold, a reservation cannot be guaranteed even
when the pool is idle at assignment time.

The initial operator spare is one device for pools larger than one device, and
zero only for an explicitly approved single-device pilot. Product and SRE must
approve any smaller spare and record the resulting maintenance availability.

## Required ledger record

Keep the capacity ledger in the private fleet Git repository. One reviewed
record per reservation must contain:

```yaml
apiVersion: operations.kube-dc.com/v1alpha1
kind: GPUReservationRecord
metadata:
  id: gpu-res-<ticket>
spec:
  organization: <organization>
  project: <project-or-empty-for-org-pool>
  profileId: <stable-whole-device-profile>
  devices: 1
  state: pending # pending | active | releasing | released | canceled
  requestedAt: <RFC3339>
  startsAt: <RFC3339>
  endsAt: <RFC3339-or-empty>
  addonId: <operator-only-addon>
  billingReference: <ticket-or-provider-reference>
  approvedBy:
    product: <identity>
    sre: <identity>
status:
  activatedAt: <RFC3339-or-empty>
  releasedAt: <RFC3339-or-empty>
  evidenceRevision: <private-evidence-reference>
  reason: <short-static-reason>
```

This is an audit document, not a Kubernetes CRD and not a scheduler input.
Never put device UUIDs, PCI addresses, secrets, customer payment data, or raw
HAMi annotations in it. The Git review serializes reservation changes; only one
reservation/add-on capacity transaction may be in progress per profile pool.

## Assignment procedure

### 1. Qualify the order

Record the organization, optional project, stable profile, device count,
start/end time, support owner, billing reference, and maintenance terms. Accept
only a whole-device VM profile whose catalog, KubeVirt permitted host device,
external resource provider, PCI selector, and live Node capacity agree.

The dedicated add-on must be operator-only (`selfService: false`) and grant
only `devices` for that exact profile. A Shared GPU shares/memory/compute add-on
cannot be converted into a hard reservation.

### 2. Freeze and snapshot

Keep Dedicated VM creation closed while assigning the hold. Acquire the
profile-pool Git change lock and capture:

```bash
kubectl get nodes \
  -L kube-dc.com/gpu.workload-mode,kube-dc.com/gpu.expected-workload-mode
kubectl get organizations.kube-dc.com -A \
  -o custom-columns=ORG:.metadata.name,ACCELERATORS:.status.quotaUsage.accelerators
kubectl get hierarchicalresourcequotas.hnc.x-k8s.io -A
kubectl get virtualmachineinstances.kubevirt.io -A
```

Also capture the admin GPU inventory, active alerts, node health, live device
capacity, all existing reservation records, and all organization hard quota for
the profile. Physical identifiers belong only in restricted operator evidence.

Stop if state is stale, health is degraded, expected/active modes differ, both
device plugins are present, an alert is active, or any quantity cannot be
attributed.

### 3. Prove the invariant before mutation

Calculate the post-change capacity equation using hard quota, including the
new reservation and every concurrent pending transaction. Do not use current
usage as a substitute. If the equation fails, add healthy dedicated capacity
or reduce unreserved hard quota through its normal holder-safe flow first.

On a sequential pool, transition to `vm-passthrough` only with both creation
gates closed and zero holders, using the [GPU node-mode transition
runbook](gpu-node-mode-transitions.md). Recalculate capacity after the
transition.

### 4. Create the hold and entitlement

Commit a `pending` ledger record first. After review:

1. use **Admin → Organization → Billing → Manage add-ons** to assign the exact
   operator-only dedicated add-on quantity;
2. wait until the organization HRQ hard quota and Organization status show the
   expected device count;
3. if the reservation is project-specific, set a project device cap no greater
   than the reserved count; remember that other project caps remain pooled
   under the organization ceiling;
4. repeat the global capacity calculation using live hard quota;
5. perform one catalog-profile VM create → attach → guest verification → stop
   → start → release cycle without exposing physical identity to the tenant;
6. commit the ledger state `active`, activation time, and restricted evidence
   reference.

Billing starts at `activatedAt`, never at the initial request or `pending`
record. Reopen VM creation only after the service recovery checks and active
ledger/HRQ state agree.

## Release and cancellation

1. Close new VM creation for the affected pilot pool and set the ledger state
   to `releasing`.
2. Identify every Pod/VMI holder attributed to the organization/profile. Ask
   the owner to stop the VM normally. Never rebind or delete a running holder
   as a billing side effect.
3. Wait for organization and project device usage plus admin active holders to
   reach zero.
4. Remove/reduce the operator-only add-on in the admin UI. The backend must
   reject the mutation if authoritative quota use or named holders remain.
5. Wait for HRQ hard quota, Organization status, project cap, and UI to
   converge. Remove a reservation-only project cap if it is no longer needed.
6. Recalculate the pool invariant, then commit `released`, `releasedAt`, and
   the evidence reference. Stop billing at `releasedAt`.
7. Only after all reservations in a sequential pool are released may an
   operator consider a holder-safe transition back to Shared Pod mode.

Cancellation before activation changes `pending` to `canceled` and grants no
entitlement or invoice. A failed release stays `releasing`, keeps entitlement
at or above use, and pages the owner; it must not silently free or resell the
capacity.

## Audit and reconciliation

Run the audit after every mutation and at least daily while a reservation is
active. Retain:

- fleet and kube-dc revisions, catalog version, profile and workload mode;
- healthy/schedulable device total and operator spare;
- each reservation ID/state/count and aggregate active reserved count;
- every organization's profile hard/used device quota;
- the computed invariant before and after the change;
- add-on assignment, HRQ, Organization/Project status and UI convergence;
- holder list, alerts, smoke result, timestamps, approvers and billing
  reference.

The audit fails closed on a missing ledger record, unknown add-on, stale
capacity, hard quota above the capacity equation, duplicate reservation ID,
profile/mode mismatch, active alert, or status disagreement. On failure:

1. disable new grants and creation;
2. preserve existing holders and entitlement;
3. mark affected records `releasing` only when an actual release was requested;
4. restore the invariant by adding healthy capacity or holder-safely reducing
   non-reserved entitlement;
5. require two-person Product/SRE review before resuming sales.

## Tenant and commercial copy

Use **GPU entitlement** for ordinary add-ons:

> This add-on increases your concurrent GPU quota. It does not reserve idle
> physical capacity, so workloads may queue when matching devices are busy.

Use **Reserved GPU capacity** only after an active ledger record exists:

> This reservation holds fungible capacity for the named GPU profile and
> organization. It does not guarantee a particular device or host. Planned
> maintenance and hardware-failure terms apply.

Never use “dedicated,” “reserved,” “guaranteed,” or “always available” for a
plain quota add-on. Dedicated passthrough describes runtime isolation, not
capacity availability.
