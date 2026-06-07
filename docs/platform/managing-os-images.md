# Managing OS Images in Kube-DC

This guide is for **cluster operators**. It explains how Kube-DC's
OS-image pipeline works, how to add or modify a family in the
default catalog, how multi-version discovery works under the hood,
and what to look at when things break.

For the **tenant-facing** view of the same pipeline (UI version
dropdown, choosing OS+version when creating a VM), see
[Creating a VM](/cloud/creating-vm).

## TL;DR

- All catalog OS images are mirrored onto the cluster's own S3
  bucket (`cdi-os-images` on the local RGW). Tenants pull from
  `https://s3.<DOMAIN>/cdi-os-images/...`, never from the upstream
  CDN.
- A weekly CronJob refreshes the mirror. Each Linux family has a
  **discovery adapter** that queries the upstream (streams JSON,
  releases JSON, or autoindex HTML) for new versions; up to
  `RETENTION_KEEP` (default 4) newest versions are kept per family.
- The catalog is materialised as a `cdi-os-catalog` ConfigMap
  (schema v2) and consumed by the UI backend so tenants can pick a
  specific version or take "latest".
- A second CronJob garbage-collects stuck CDI worker pods + Failed
  DataVolumes + DataVolumes stuck in import past a 30-minute
  deadline.
- Six PrometheusRule alerts watch the mirror + gc + importer
  health.

## Architecture

```
┌────────────────────────┐
│  chart values.yaml     │  osImages.catalog: [...]   (chart-author input)
│  (per-family entry +   │  cdiUploadGc.*           (gc CronJob tuning)
│   discovery: block)    │  cdiMirrorAlerts.enabled (PromRule on/off)
└───────────┬────────────┘
            │ helm
            ▼
┌────────────────────────┐        ┌────────────────────────────┐
│  images-configmap      │───────▶│  cdi-os-mirror-refresh     │  Weekly Sunday
│  (per-family inputs    │        │  CronJob                   │  04:00 Amsterdam
│   incl. discovery cfg) │        │  shalb/cdi-os-mirror:...   │
└────────────────────────┘        └────┬───────────────────────┘
                                       │ download + verify + upload
                                       ▼
                                  ┌──────────────────────────┐
                                  │  Rook RGW: cdi-os-images │
                                  │  <family>/<release>/     │
                                  │    <tag>/<file>.qcow2    │
                                  │    <tag>/manifest.json   │
                                  │    latest/<file>.qcow2   │  (alias to newest)
                                  │    _latest.json          │  (pointer)
                                  └────┬─────────────────────┘
                                       │ list + read manifests
                                       ▼
                                  ┌──────────────────────────┐
                                  │  cdi-os-catalog          │  schema v2
                                  │  ConfigMap (kube-dc ns)  │  multi-version
                                  └────┬─────────────────────┘
                                       │
                                       ▼
                                  ┌──────────────────────────┐
                                  │ kube-dc-backend          │
                                  │ /api/create-vm/{ns}/     │  with fallback to
                                  │   os-images              │  images-configmap
                                  └────┬─────────────────────┘
                                       │
                                       ▼
                                  ┌──────────────────────────┐
                                  │ Frontend Create-VM modal │
                                  │ + Version dropdown       │
                                  └──────────────────────────┘
```

## Bucket layout (v2)

For every catalog entry the refresh job writes:

```
<family>/<release>/
  _latest.json                  # {"tag":"<current-latest-tag>","updatedAt":"<rfc3339>"}
  latest/<file>                 # tenant-facing alias to the current latest bytes
  <tag-1>/                      # newest version
    <file>
    manifest.json
  <tag-2>/                      # older versions, up to RETENTION_KEEP total
    <file>
    manifest.json
  ...
```

- `latest/<file>` is what the chart's per-entry `mirrorPath` points
  to — every existing VM provisioned against `/latest/` keeps
  working regardless of how many version dirs land underneath.
- `_latest.json` is the **authoritative pointer** for which tag is
  considered "latest" by the discovery adapter. The catalog builder
  reads this to flag the right Version with `isLatest=true`. Falls
  back to lex-desc tag sort when the pointer is absent.
- `manifest.json` per version dir carries the displayName, sha256,
  upstreamURL, sizeBytes, uploadedAt. This is what the UI version
  dropdown shows.

## Discovery adapters

Each chart entry under `osImages.catalog` optionally carries a
`discovery:` block. The refresh job picks an adapter by
`discovery.type`; entries without a discovery block fall back to
`StaticURLAdapter` (single-version, always flagged latest).

| Adapter type | Upstream shape | sha256 source | Used for |
|---|---|---|---|
| `ubuntu-streams` | Canonical streams JSON | streams JSON | Ubuntu 22.04, 24.04 LTS |
| `debian-html` | autoindex HTML, dated build dirs | none (computed locally) | Debian 12 |
| `centos-stream-html` | autoindex HTML | `.SHA256SUM` (BSD format) | CentOS Stream 9 |
| `alpine-html` | autoindex HTML | none (computed locally) | Alpine 3.21 |
| `opensuse-leap-html` | autoindex HTML | `.sha256` (GNU format) | openSUSE Leap 15.6 |
| `gentoo-html` | autoindex HTML (cloudinit autobuilds) | PGP-wrapped `.sha256` | Gentoo |
| `fedora-releases` | `fedoraproject.org/releases.json` | from JSON | Fedora 42 |
| `static-url` | single URL (no discovery) | computed locally | Ubuntu 26.04, CirrOS, Windows |

Each adapter caps at `RETENTION_KEEP` newest versions (default 4) so
the per-cycle download budget stays bounded.

### Example: adapter config for Ubuntu

```yaml
- osName: "Ubuntu 24.04 LTS"
  cloudUser: ubuntu
  upstreamURL: "https://cloud-images.ubuntu.com/noble/current/noble-server-cloudimg-amd64.img"
  mirrorPath: "ubuntu/24.04/latest/noble-server-cloudimg-amd64.img"
  discovery:
    type: ubuntu-streams
    streamsURL: "https://cloud-images.ubuntu.com/releases/streams/v1/com.ubuntu.cloud:released:download.json"
    productID: "com.ubuntu.cloud:server:24.04:amd64"
    ftype: "disk1.img"
```

### Example: HTML-listing adapter (openSUSE)

```yaml
- osName: "openSUSE Leap 15.6"
  cloudUser: opensuse
  upstreamURL: "https://download.opensuse.org/distribution/leap/15.6/appliances/openSUSE-Leap-15.6-Minimal-VM.x86_64-Cloud.qcow2"
  mirrorPath: "opensuse-leap/15.6/latest/openSUSE-Leap-15.6-Minimal-VM.x86_64-Cloud.qcow2"
  discovery:
    type: opensuse-leap-html
    indexURL: "https://download.opensuse.org/distribution/leap/15.6/appliances/"
    release: "15.6"
```

### Example: Windows-style entry (no discovery)

Two Windows entries share `windows/11/` so `familyId` disambiguates
them in the catalog. No `discovery:` block — `static-url` adapter
mirrors the upstream URL verbatim.

```yaml
- osName: "Windows 11 Enterprise (Golden Image)"
  familyId: "windows-11-golden"
  cloudUser: kube-dc
  upstreamURL: "https://iso.stage.kube-dc.com/windows11-x64-golden.qcow2"
  mirrorPath: "windows/11/latest/windows11-x64-golden.qcow2"
```

## Refresh CronJob

`platform/cdi-os-mirror/refresh-cronjob.yaml` (in
`kube-dc-fleet`) runs the refresh tool weekly:

- Schedule: `0 4 * * 0` Europe/Amsterdam — every Sunday 04:00 local
  time, one hour after the etcd defrag CronJob.
- `concurrencyPolicy: Forbid` — only one run at a time.
- `activeDeadlineSeconds: 14400` (4h) — bounds the worst case
  Windows golden refresh.
- Image: `docker.io/shalb/cdi-os-mirror:main-<sha>`. The Python
  source lives at `kube-dc/images/cdi-os-mirror/refresh.py`.

The refresh job iterates every entry in `images-configmap`, dispatches
to the per-family adapter, downloads new versions to a scratch
`emptyDir` (40 GiB sizeLimit to fit the Windows golden), uploads to
the versioned path, copies to the `/latest/` alias **only for the
adapter's `is_latest=True` Version**, then runs retention.

Retention runs **before** the catalog rebuild (since v0.3.15) so the
catalog never advertises a version that's about to be pruned.

### Running the refresh manually

```bash
# Trigger a one-off run that mirrors the cronjob's template
kubectl -n kube-dc create job --from=cronjob/cdi-os-mirror-refresh refresh-manual-$(date +%s)
```

Add `--dry-run` (set `args:` on the Job) to walk through what would
be uploaded without actually writing to S3 — useful for adapter
debugging.

## gc CronJob

`charts/kube-dc/templates/cdi-upload-gc-cronjob.yaml` runs every 6h
and does three passes:

| Pass | Targets | Threshold | Why |
|---|---|---|---|
| 1 | `cdi-(upload\|importer\|clone)-*` pods in Pending/Unknown | `staleDays` (default 7d) | catches the "upload scheduled but never landed" class. Parent DV is deleted. |
| 2 | DataVolumes in phase `Failed` | `failedHours` (default 6h) | catches the "importer crashloops indefinitely against an unreachable URL" class. |
| 3 | DataVolumes in `ImportInProgress` / `ImportScheduled` | `importDeadlineMinutes` (default 30m) | implements PRD Phase 0 time-bound importer retries; CDI has no native `failureRetryLimit`. |

`cdiUploadGc.delete=false` (audit-only) is the safe default; set to
`true` to actually delete. Cloud cluster runs with `delete: true`
since v0.3.20.

### Tuning knobs (chart values)

```yaml
cdiUploadGc:
  enabled: true
  staleDays: 7
  failedHours: 6
  importDeadlineMinutes: 30
  delete: false                       # set true to actually delete
  schedule: "0 */6 * * *"
  image: "shalb/kubectl-jq:v1.35.4"
```

### Resource limits

128 Mi OOM-killed the gc pod on busy clusters (346 pods → ~7 MiB
JSON output → kubectl+jq peak working set >128 Mi). Limits are
`64Mi/384Mi` since v0.3.21; covers up to ~1000 pods.

## Mirror & importer health alerts

`cdiMirrorAlerts.enabled` (default `true`) renders a `PrometheusRule`
that prom-operator picks up automatically. Sources entirely from
`kube-state-metrics` — no custom exporter required.

| Alert | Group | Trips when |
|---|---|---|
| `KubeDCImageMirrorRefreshFailed` | cdi-os-mirror | weekly refresh Job failed |
| `KubeDCImageMirrorStale` | cdi-os-mirror | no successful refresh in >14d |
| `KubeDCCdiGcJobFailed` | cdi-upload-gc | 6-hourly gc Job failed |
| `KubeDCCdiGcStale` | cdi-upload-gc | no successful gc run in >24h |
| `CDIImporterRetryStorm` | cdi-importer | importer pod restart rate >0.1/s over 10m |
| `CDIImporterImportInProgressStale` | cdi-importer | DV stuck importing >importDeadlineMinutes (requires textfile-collector — defined-but-inert until that metric source ships) |

Disable on clusters without prometheus-operator installed:

```yaml
cdiMirrorAlerts:
  enabled: false
```

## Adding a new family to the catalog

The minimum viable entry has `osName`, `cloudUser`, `upstreamURL`,
and `mirrorPath`. That gives you single-version mirroring via
`StaticURLAdapter`. For multi-version, add a `discovery:` block.

```yaml
osImages:
  catalog:
    - osName: "Rocky Linux 9"
      cloudUser: rocky
      upstreamURL: "https://dl.rockylinux.org/pub/rocky/9/images/x86_64/Rocky-9-GenericCloud.latest.x86_64.qcow2"
      mirrorPath: "rocky/9/latest/Rocky-9-GenericCloud.latest.x86_64.qcow2"
      minMemory: "2G"
      minVCPU: "1"
      minStorage: "20G"
      firmwareType: "bios"
      machineType: "q35"
      features: "acpi"
      cloudInit: |
        #cloud-config
        packages:
          - qemu-guest-agent
        runcmd:
          - systemctl enable --now qemu-guest-agent
```

After bumping the chart on the cluster, trigger a manual refresh to
pull the bytes:

```bash
kubectl -n kube-dc create job --from=cronjob/cdi-os-mirror-refresh first-rocky-pull
```

### Writing a new discovery adapter

If the upstream doesn't fit any of the seven shipped adapters
(streams JSON / releases JSON / HTML listing / static URL), the
adapter is a ~50-line Python subclass in
`kube-dc/images/cdi-os-mirror/refresh.py`. Implement
`Adapter.discover(entry: CatalogEntry) -> list[Version]`. Register
the type string in `ADAPTERS = {...}`. Add an `entry.discovery.type`
in the chart pointing at the new key.

The `_ListingAdapter` base class is a one-line subclass for any
HTML-autoindex upstream — set `file_regex` and (optionally)
`sha256_sidecar_suffix`.

## Troubleshooting

### A family is missing from the UI

1. Check the chart entry made it into `images-configmap`:
   ```bash
   kubectl -n kube-dc get cm images-configmap -o jsonpath='{.data.images\.yaml}' | yq '.images[].OS_NAME'
   ```
2. Check the catalog ConfigMap (multi-version output):
   ```bash
   kubectl -n kube-dc get cm cdi-os-catalog -o jsonpath='{.data.catalog\.json}' | jq '.families[].id'
   ```
3. If the family is in `images-configmap` but missing from
   `cdi-os-catalog`, the refresh job hasn't run yet for that entry.
   Trigger a manual refresh.

### The wrong version shows as "latest"

Check `_latest.json`:

```bash
kubectl -n kube-dc port-forward svc/kube-dc-backend 8080:8080 &
curl -sS http://localhost:8080/api/create-vm/<ns>/os-images | jq '.[] | select(.OS_NAME=="<family>") | ._versions'
```

If `_latest.json` is missing or names a pruned tag, the catalog
falls back to lex-desc tag sort (and logs a warning in the refresh
job). The next refresh cycle re-writes the pointer.

To force a rewrite immediately: delete the `_latest.json` object and
trigger the cronjob. The next adapter run will recreate it.

### Refresh job OOM / wedge

`cdi-os-mirror` peak working set is download buffer + boto3 S3 client.
Worst case is the Windows golden (21 GiB + 5 GiB ISO). 40 GiB
emptyDir + 1 GiB memory limit on the refresh container handles it
comfortably.

The gc CronJob has its own resource profile (`64Mi/384Mi`). If you
see `gc-deep-diag` OOMKilled, the cluster has grown past what the
limit handles — bump `cdiUploadGc.resources.limits.memory`.

### Alerts fired

| Alert | Check first |
|---|---|
| `KubeDCImageMirrorRefreshFailed` | `kubectl -n kube-dc logs job/cdi-os-mirror-refresh-<n>` |
| `KubeDCImageMirrorStale` | Has any cronjob fired recently? `kubectl -n kube-dc get cronjob cdi-os-mirror-refresh` |
| `KubeDCCdiGcJobFailed` | First capture pod state **before** deleting the Job: `kubectl -n kube-dc describe pod -l job-name=<failed-job>` |
| `KubeDCCdiGcStale` | CronJob suspended? schedule mis-edit? |
| `CDIImporterRetryStorm` | Find the DV, check `kubectl -n <ns> describe datavolume <name>`; broken source URL is the usual cause. |

## API reference

`GET /api/create-vm/{namespace}/os-images` returns a flat array of
catalog entries. Each entry now includes Phase 2.3 extension fields:

```json
{
  "OS_NAME": "Ubuntu 24.04 LTS",
  "CLOUD_USER": "ubuntu",
  "OS_IMAGE_URL": "https://s3.kube-dc.cloud/cdi-os-images/ubuntu/24.04/latest/noble-server-cloudimg-amd64.img",
  "_familyId": "ubuntu-24.04",
  "_versions": [
    { "tag": "20260321", "displayName": "...", "imageURL": "...", "isLatest": true, "sha256": "...", "uploadedAt": "..." },
    { "tag": "20260225", "...": "..." },
    { "tag": "20260209", "...": "..." },
    { "tag": "20260131", "...": "..." }
  ],
  "_latestURL": "https://s3.kube-dc.cloud/cdi-os-images/ubuntu/24.04/latest/noble-server-cloudimg-amd64.img"
}
```

`_versions`, `_familyId`, and `_latestURL` are present only when
`cdi-os-catalog` exists; the backend falls back to the flat
single-version shape when it doesn't (e.g. fresh cluster before the
first refresh).

## References

- Refresh tool: `kube-dc/images/cdi-os-mirror/refresh.py`
- Refresh CronJob (fleet): `kube-dc-fleet/platform/cdi-os-mirror/refresh-cronjob.yaml`
- Chart template (gc + alerts): `charts/kube-dc/templates/cdi-upload-gc-cronjob.yaml`, `cdi-mirror-alerts-prometheusrule.yaml`
