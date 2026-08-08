import {OsImageOperationsDiagram} from '@site/src/components/Diagram/PlatformTopologyDiagrams';

# OS-image operations — canonical operator guide

This is the **single entry point** for operating the Kube-DC OS-image platform:
the mirror, the golden images (instant clone + live migration), the in-cluster
containerdisk registry, the Admin **OS Images** page, and the Windows lifecycle.
Component READMEs link here; when procedures conflict, this page wins.

## Architecture at a glance

<details data-github-only>
<summary>Diagram source for GitHub</summary>

```
upstream cloud images ──► cdi-os-mirror CronJob (weekly) ──► s3://<s3-host>/cdi-os-images
                                     │                          │
                                     │ builds FROM-scratch      │ http source
                                     ▼ containerdisks           ▼
                          zot depot registry.<domain>   FS goldens (golden-images ns)
                          cdi-os/<family>@<digest>      DataImportCron / one-shot DV
                                                          │ snapshot
                                                          ▼
                       Block goldens (golden-images-block ns) ◄── in-cluster converter
                          │                                       (import→convert→snapshot)
                          ▼
              per-project seeder (manager, 15m resync) ──► instant clone / live migration
```

</details>

<OsImageOperationsDiagram />

| Piece | Where | Doc |
|---|---|---|
| Mirror + catalog (`cdi-os-catalog`) | `kube-dc` ns CronJob `cdi-os-mirror-refresh` | [Managing OS images](managing-os-images.md) |
| Storage tiers + live migration | `rbd-vm` SC, Block goldens | [VM storage tiers](vm-storage-tiers.md) |
| FS goldens (instant clone) | fleet `platform/rbd-vm/goldens-fs/os/` | this page |
| Block goldens (migratable) | fleet `platform/rbd-vm-block/incluster/` | fleet README + this page |
| Containerdisk depot (zot) | fleet `platform/registry-depot/` | this page |
| Windows golden + bake | `hack/windows/` (+ `automated/`) | [Windows VM setup](windows-vm-setup.md) |

## Image modes — what a tenant actually gets

- **Filesystem golden (instant clone)** — per-project VolumeSnapshot restore,
  ~1 s provisioning. The default fast path ("Prepared image" in Create-VM).
- **Block golden (migratable)** — RWX-Block clone; enables the live-migration
  checkbox for that OS. Built by the in-cluster converter.
- **URL import** — CDI http import from the mirror; slowest, always available.
- **Containerdisk** — `registry.<domain>/cdi-os/<family>@<digest>` from the zot
  depot, used with `pullMethod: node`; removes the quay.io dependency.

## Reading the Admin → OS Images page

- **Goldens** = cluster golden VolumeSnapshots that are `ReadyToUse`. This shows
  the *cluster* golden exists — it does **not** prove every project's seeded copy
  is healthy (the seeder converges within ~15 min; check per-project snapshots
  when debugging a single tenant).
- **Catalog** = the `cdi-os-catalog` ConfigMap the mirror rebuilds **at the end
  of each successful run**. "Last run" is the schedule time, not proof every
  family refreshed — check the CronJob logs `summary |` line for `ok/fail`.
- **Builds** = Block-golden converter Jobs (Complete = the Block golden was
  written; the snapshot follows).

## Routine operations

**Add an OS** — catalog entry in `charts/kube-dc/values.yaml` (`osImages.catalog`,
ships with a chart release) → FS golden file in fleet `goldens-fs/os/` → optional
Block trio in `rbd-vm-block/incluster/*/os/` → add the OS lines to the cluster
overlays. The seeder and UI pick everything up from labels; no controller change.

**Refresh a Linux golden** — registry-sourced DataImportCrons poll every 6 h
automatically. Block goldens: follow the rebuild procedure in the fleet
`rbd-vm-block/incluster/README.md` (delete stage objects → Flux re-creates).
⚠️ The Block rebuild is currently **not blue/green** — the old snapshot is
deleted before the new one is ready; schedule it in a quiet window.

**Refresh the Windows golden** — see [Windows VM setup](windows-vm-setup.md).
The Windows FS golden is a one-shot import: updating the S3 object does **not**
re-import automatically; a rebuild is a deliberate operator action.

**Blue/green golden refresh (non-destructive).** Never delete a live golden to
rebuild it. The seeder prefers the snapshot labelled
`kube-dc.com/golden-active: "true"` when a family has more than one ready golden,
so a refresh is a *switch*, not a gap:

1. Stage the new golden **alongside** the live one — same
   `kube-dc.com/golden-os` label, a distinct object name (e.g. suffix the build
   date), and **no** `golden-active` label yet. Tenants keep using the old one.
2. Boot-test it: `./hack/windows/automated/validate-clone.sh <ns> <new-snapshot>`
   — must exit 0 (PHASE 1 RDP boot proof, PHASE 2 guest agent).
3. Promote: set `kube-dc.com/golden-active: "true"` on the new snapshot and
   remove it from the old one. Within one Project resync (≤15 min) every project
   is re-seeded to the new golden; the seeder re-creates each per-project
   snapshot in the same pass, so there is no window without a golden.
4. Keep the previous golden until the new one has soaked, then delete it —
   that is your rollback (flip the label back).

With no `golden-active` label anywhere, behaviour is exactly as before (first
ready golden wins), so single-golden clusters need no changes.

⚠️ **Exactly one golden per family may carry `golden-active=true`.** Sorting is
not an atomic pointer: if a flip leaves two goldens active (or none), which one
wins falls back to list order, and different projects can end up seeded from
different images. The seeder therefore *reports* a conflict as a reconcile error
rather than silently picking one — so do step 3 as a single change (set the new,
clear the old) and check the manager logs afterwards.

**Rebuild a stuck mirror family** — check the mirror Job's `summary |` log line;
per-family failures keep the previous catalog pin (nothing breaks, it goes
stale). Re-run: `kubectl -n kube-dc create job --from=cronjob/cdi-os-mirror-refresh <name>`.

## Windows — product posture

**BYOL.** The platform ships an evaluation-based starter golden; tenants
activate with their own license (details + `slmgr` commands in
[Windows VM setup](windows-vm-setup.md)). The tenant path is the **golden
quick-install** (instant clone). Fresh-install-from-ISO is not offered (the
Create-VM flow cannot yet attach installer CDROMs). The Windows containerdisk
is deliberately not published (a >20 GiB blob defeats the depot's S3 driver and
spegel serves a blob from a single peer).

### Windows media in the public image bucket — ACCEPTED (2026-07-31)

The `cdi-os-images` bucket is **anonymously readable**: CDI importers pull golden
and catalog images over plain HTTP from inside every tenant VPC, with no
credentials. That bucket also holds the Windows installer ISO and the Windows
golden qcow2, so those objects are publicly fetchable by URL.

**Decision: accepted.** Rationale:

- The media is Microsoft's **Windows 11 Enterprise Evaluation** build — the same
  bits any person can download from Microsoft's Evaluation Center without a
  licence or account.
- The golden carries **no product key, no activation state, and no customer
  data** — it is the neutral BYOL base image (see
  [Windows VM setup](windows-vm-setup.md)); each tenant applies their own
  entitlement inside their VM.
- Making the bucket credentialed would break the unauthenticated CDI import path
  that every OS family depends on, for no gain while the media stays evaluation-grade.

**Guardrails — these are what keep the decision valid:**

- **Never** publish an *activated*, volume-licensed, KMS-configured, or
  product-key-bearing Windows image to this bucket. If we ever ship licensed
  media, it must move to a credentialed/private path **first**.
- Never place customer-specific or tenant-derived images here.
- Re-open this decision if we switch away from evaluation media, or if the
  redistribution terms of the source media change.

## Known limitations (tracked)

- **The live Windows golden has no working qemu-guest-agent** (found 2026-07-31 by
  `hack/windows/automated/validate-clone.sh`). A clone boots fully — RDP 3389 and
  SSH 22 both answer within minutes — but `AgentConnected` never becomes true.
  Because KubeVirt injects tenant SSH keys **through** the agent, and
  `guestOSInfo` comes from it:
  - ✅ Windows VMs work: RDP and password/SSH login with the baked `kube-dc` user.
  - ❌ **Per-tenant SSH-key injection does not take effect**, and the VM reports no
    guest OS info in the UI.
  Fix path: the automated bake (`hack/windows/automated/`) installs the agent in
  `FirstLogonCommands`, so the next bake resolves it. The currently published
  golden needs a re-bake (or an in-place agent install + re-snapshot).

- Block-golden rebuild is destructive (not blue/green) — quiet-window only.
- Windows golden has no automated refresh cadence yet; the unattended bake
  pipeline (`hack/windows/automated/`) requires one supervised validation run.
- zot depot is single-replica; `readTimeout/writeTimeout` are set to 4 h to
  support multi-GiB blob pushes.
- The mirror CronJob can exit green with per-family failures — read the
  `summary |` line, not just the Job status.

Productization review + full gap list: `docs/prd/windows-productization-review-2026-07-31.md` (internal).
