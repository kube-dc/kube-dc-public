# OS-image operations — canonical operator guide

This is the **single entry point** for operating the Kube-DC OS-image platform:
the mirror, the golden images (instant clone + live migration), the in-cluster
containerdisk registry, the Admin **OS Images** page, and the Windows lifecycle.
Component READMEs link here; when procedures conflict, this page wins.

## Architecture at a glance

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

## Known limitations (tracked)

- Block-golden rebuild is destructive (not blue/green) — quiet-window only.
- Windows golden has no automated refresh cadence yet; the unattended bake
  pipeline (`hack/windows/automated/`) requires one supervised validation run.
- zot depot is single-replica; `readTimeout/writeTimeout` are set to 4 h to
  support multi-GiB blob pushes.
- The mirror CronJob can exit green with per-family failures — read the
  `summary |` line, not just the Job status.

Productization review + full gap list: `docs/prd/windows-productization-review-2026-07-31.md` (internal).
