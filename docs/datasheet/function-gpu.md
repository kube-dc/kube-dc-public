---
title: GPU Services
slug: gpu
hide_title: true
description: Shared GPU slices for containers and dedicated GPU virtual machines with governed capacity and qualification controls.
---

import {GpuServiceModelDiagram} from '@site/src/components/Diagram/DatasheetDiagrams';

# DRAFT — Kube-DC Function Datasheet: GPU Services

> 🚧 **Working draft — not for distribution.** Companion to the
> [platform datasheet](draft-artifact-a-datasheet.md). Status per owner
> direction 2026-08-07: shared GPU is **in production use** (live tenant
> workloads on the reference deployment) — the "preview" label is retired.
> Dedicated GPU VMs are described as an available capability with the
> qualification requirements the product enforces. Claims trace to the
> published GPU documentation, the chart's product catalog and the
> [claim ledger](claim-ledger-a-cloud.md).
>
> ✅ **Public docs aligned 2026-08-07** — the preview/pilot status language
> was removed from `docs/cloud/gpu-shared-workloads.md`,
> `docs/cloud/gpu-vm-guests.md` and the operator-side `docs/platform/gpu-*`
> runbooks, so the mirrored documentation no longer contradicts this
> collateral. Technical caveats (cooperative compute share, no live
> migration for GPU VMs, per-deployment qualification) were kept
> deliberately — they are properties, not maturity labels.
>
> ⚠ **One open item for you:** `docs/platform/gpu-threat-model.md` still
> carries **"Status: ready for Security and Product approval"**. That is a
> sign-off record, not a wording choice — confirm the approval and it can
> read as approved; until then a security reviewer may read it as
> unfinished governance.

---

# GPU Services

**Two ways to give teams accelerators — a fraction of a card, or a whole
one — with the governance an organization needs to hand GPUs to
departments.**

GPUs are scarce, expensive and shared badly by default. Kube-DC treats
them as a product: catalogued profiles, per-model entitlements enforced as
quota, capacity reservations for organizations that need them, and an
explicit security and supply-chain model — because the components that
make GPUs work run privileged on your nodes.

GPU capabilities are enabled per cluster by your platform team once the
hardware, driver and component set are qualified — GPU nodes couple
device, kernel, driver and operator versions, so enablement follows
qualification rather than a switch flip. Shared GPU and dedicated GPU VM
creation are separately controlled, so a deployment can offer one without
the other.

## 1. Shared GPU for containers

Several container workloads run on one physical GPU at the same time, each
holding a fixed fraction of the device — the right fit for inference,
notebooks, small training runs and batch scoring, where a whole card is
waste.

- **Fixed-product profiles.** A shared product is a defined fraction of a
  specific GPU model (for example, an 8 GiB slice of an NVIDIA V100)
  published in the platform's accelerator catalog. Tenants request a
  product; they do not freely tune memory and compute.
- **The memory slice is enforced.** Inside the container, the GPU reports
  the slice size, not the physical card.
- **The compute share is cooperative.** Workloads are steered toward their
  share in steady state; startup and some library paths can briefly exceed
  it. Stated plainly: this is a density mechanism, **not a performance
  guarantee and not a security boundary** — for a hard boundary, use a
  dedicated GPU VM.
- **Standard Kubernetes consumption.** Workloads request GPU products
  through Kubernetes' Dynamic Resource Allocation — a manifest applied
  with `kubectl`, like any other workload. Shared GPU runs tenant
  workloads in production on the reference deployment today.

## 2. Dedicated GPU VMs

One whole physical GPU attached to one virtual machine, for workloads that
need a stronger boundary than software sharing: an isolated guest with the
full device.

- Whole-device passthrough to a Linux or Windows guest, from the
  deployment's qualified image and driver matrix.
- **Not live-migratable** — planned maintenance means shutdown and
  restart, and a later start can wait for capacity or receive a different
  physical device.
- Guest images and driver packages are qualified per deployment: the
  platform team publishes an approved package version and checksum before
  enabling GPU VM creation. The published matrix names the qualified
  guests; anything outside it is qualified separately.

<details data-github-only>
<summary>Diagram source for GitHub</summary>

```mermaid
flowchart TB
  CAT["Accelerator catalog —<br/>published GPU products"] --> SH["Shared GPU product<br/>fixed slice of a device"]
  CAT --> DED["Dedicated GPU VM<br/>whole device"]
  SH --> POD["Container workloads —<br/>density; cooperative compute share"]
  DED --> VM["VM guest —<br/>stronger boundary; not live-migratable"]
  Q["Per-model entitlement<br/>(project quota)"] -. "bounds concurrent use" .-> SH
  Q -.-> DED
  RES["Operator capacity reservation<br/>(per organization)"] -. "reserves healthy devices" .-> CAT
```

</details>

<GpuServiceModelDiagram />

## 3. Entitlements, quota and reservations

Two different things, deliberately distinguished:

- **Entitlement (quota)** — each GPU model is metered independently in
  organization and project quota, so a team's V100 allowance never
  consumes another model's headroom. An entitlement bounds concurrent
  use; it does **not** reserve a physical device, so a valid workload can
  legitimately queue when all matching GPUs are busy.
- **Capacity reservation** — an operator-held amount of healthy physical
  capacity set aside for one organization, tracked in a separate capacity
  ledger. This is what a department buys when queuing is unacceptable.
  Reservations are fungible: the guarantee is the number of healthy
  devices, not a specific card.

Assigning a GPU add-on does not by itself create a reservation — the
distinction is enforced in the product, not just in the documentation.

## 4. Node modes and day-2 operations

A GPU node runs in one mode at a time — shared-container or dedicated-VM
passthrough — because the two require different host configuration.

- **Holder-safe mode transitions**: the `kube-dc` CLI performs the
  day-2 switch between modes with an explicit safety contract — creation
  gates closed, GPU holders inventoried, node cordoned, fleet state and
  live state in agreement before anything moves.
- **Upgrade gating**: GPU nodes couple the PCI device, host kernel,
  Kubernetes distribution, driver, GPU operator and monitoring exporter
  into **one tuple** that must be validated together — upstream support
  for each component separately is not sufficient. The CLI provides a
  read-only pre-upgrade gate; the documented sequence qualifies a canary
  node and proves rollback before a fleet-wide plan.

## 5. Security and supply chain

GPU drivers, device plugins, schedulers and monitoring agents run
privileged enough to compromise a node, so version selection and image
provenance are part of the security boundary — not routine application
upgrades.

- **A published threat model** covers shared containers, dedicated VMs,
  catalog and discovery, quota, billing, installation, node-mode
  transitions, monitoring and the user interfaces — including the
  invariants the design preserves (a tenant cannot mint GPU entitlement
  by editing ordinary resources).
- **A qualified installer tuple**: an enabled installation accepts one
  indivisible, version-pinned component set, with runtime audit and a
  promotion gate — not "latest".
- Admission policies force tenant GPU requests into the exact catalog
  product, and per-model quota bounds the count, so entitlement cannot be
  exceeded by hand-written manifests.

## 6. Responsibilities — GPU services

| Concern | Your platform team | Tenant |
|---|---|---|
| Hardware, drivers, GPU operator, qualification of the component tuple | ✅ | — |
| Enabling GPU capabilities per cluster and per project | ✅ (gated, after qualification) | — |
| Catalog products (which slices and models exist) | ✅ defines | ✅ requests |
| Entitlements and reservations | ✅ grants | ✅ consumes within quota |
| Node-mode transitions and GPU node upgrades | ✅ | — |
| Workload design, drivers inside VM guests, performance tuning | — | ✅ |

---

## Draft apparatus (stripped at publication)

Evidence: shared-GPU product model, enforced memory slice, cooperative
compute share, queuing-vs-reservation semantics and per-model quota per
`docs/cloud/gpu-shared-workloads.md`; dedicated-VM passthrough, image/driver
matrix and no-live-migration per `docs/cloud/gpu-vm-guests.md`; reservations
vs entitlement per `docs/platform/gpu-capacity-reservations.md`; node-mode
transitions, upgrade gate, supply chain and threat model per the
corresponding `docs/platform/gpu-*.md` runbooks; catalog shape and the
per-capability enablement controls per `charts/kube-dc/values.yaml`.

**Production-use evidence, verified live 2026-08-07** (reference deployment
cloudacropolis): full GPU stack running (GPU operator, NVIDIA driver
daemonset, container toolkit, DCGM exporter, HAMi device plugin);
DeviceClasses `kube-dc-nvidia-v100-shared-8g` and `hami-core-gpu…` present
for 21 days; a tenant Stable-Diffusion workload holding an
`allocated,reserved` ResourceClaim for 8 days; ResourceClaimTemplates in
two tenant projects; deviceclass quota lines across 17 project namespaces.
**Asymmetry to respect:** no GPU-attached VM was running at the time of
check — the shared-GPU path has live tenant usage, the dedicated-VM path
does not on this deployment, which is why §2 describes capability and
qualification rather than claiming production use.

**Not claimed:** vGPU (deliberately unqualified), MIG, performance figures
or guarantees, GPU as a security boundary for shared containers,
supported-guest breadth beyond the published qualification matrix,
GPU-VM production usage. Keep the cooperative-share caveat and the
per-deployment qualification framing in every customer-facing version.
