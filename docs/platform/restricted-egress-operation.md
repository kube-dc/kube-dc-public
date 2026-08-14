---
description: How Kube-DC keeps container and VM images flowing on clusters with restricted or no internet egress, and what each on-cluster image plane depends on.
---

# Operating with restricted or no internet egress

Some clusters run where tenant workloads have **no direct path to the internet** —
a private datacenter VLAN, a 1:1-NAT edge, or a deliberately restricted egress
policy. Kube-DC is built to keep working there: both image planes it depends on
have an **on-cluster source that is default-on for new installs**, so a tenant VM
or Pod does not need WAN egress to fetch its image.

This page is the map. The mechanics live in the linked operator guides.

## The two image planes

| Plane | On-cluster source (default-on) | Detailed guide |
|-------|--------------------------------|----------------|
| **Container images** | `registry-depot` (zot, an S3-backed registry) + the RKE2 embedded registry mirror (`spegel`) that P2P-shares pulled layers across nodes | [installation-overview.md](installation-overview.md) "What the Fleet installs" |
| **VM / OS images** | `cdi-os-mirror` — a weekly mirror of upstream OS images into the on-cluster S3 bucket (`cdi-os-images` on the local RGW), so CDI HTTP-source imports stay on-cluster | [managing-os-images.md](managing-os-images.md), [os-image-operations.md](os-image-operations.md) |

Both are scaffolded automatically by `kube-dc bootstrap init` (the
image-acceleration writer); an older fleet-starter missing a piece is skipped
with a warning rather than failing the install.

## Both planes depend on object storage

The on-cluster image sources are **S3-backed** — `registry-depot` and
`cdi-os-mirror` both write to the cluster's Rook RGW. That is the load-bearing
dependency to understand for restricted-egress clusters:

- If object storage is **healthy**, images flow on-cluster and tenants need no
  egress to boot a VM or start a Pod from a mirrored image.
- If object storage is **degraded or absent**, neither mirror has anywhere to
  serve from. A tenant with no NAT **and** no working RGW then has no image
  source at all — the "green install, VM won't boot" trap.

So on a restricted-egress cluster, **object storage is not optional** — it is the
substrate the image planes stand on. The installer now verifies the raw OSD
block devices exist and are empty before install (see below) precisely because a
silently-failed OSD takes object storage — and with it both image planes — down.

> **Known limitation.** There is no object-storage-free image route today: a
> local-path-backed golden-image path (for clusters that run VMs without Rook)
> is tracked as a follow-up, not shipped. Until then, a VM-capable
> restricted-egress cluster needs a working RGW.

## Tenant internet egress still needs a live gateway

For workloads that *do* need outbound internet (e.g. pulling an image that is not
mirrored, or a tenant app calling an external API), tenant egress leaves through
`EXT_NET_GATEWAY`. That gateway must be a live L2 neighbour that answers ARP — an
address that is merely inside the ext CIDR but silent on ARP produces a clean
install with **black-holed** tenant egress. See
[networking-external.md](networking-external.md) "The egress gateway must answer
ARP".

## What the installer checks for you

`kube-dc bootstrap init` runs two advisory (fail-open) preflight probes that
catch the most common restricted-egress traps before they become a green install
with a dead data path:

- **Egress-gateway ARP probe** — warns if `EXT_NET_GATEWAY` does not answer ARP
  on the ext interface (tenant internet egress would black-hole).
- **OSD block-device check** — warns if a configured Rook OSD device is missing
  or already carries data (object storage, and with it both image planes, would
  never come up).

Both only warn; neither blocks the install. Read the output — on a
restricted-egress cluster a warning here is usually the difference between a
working cluster and a silently broken one.

## Checklist for a restricted-egress install

1. Confirm object storage is real: raw OSD devices attached and empty (the
   installer probes this; verify by hand with `lsblk` if unsure).
2. Confirm `EXT_NET_GATEWAY` answers ARP (the installer probes this).
3. Confirm the OS-image mirror has run at least once so the images tenants will
   boot are present in `cdi-os-images` — see
   [managing-os-images.md](managing-os-images.md) "Running the refresh manually".
4. For Windows, confirm the golden image is imported and snapshotted on the
   cluster — see [os-image-operations.md](os-image-operations.md) "Distribution".
