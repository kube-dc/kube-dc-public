# VM guest capabilities by OS image

This page is for **cluster operators**. It lists which day-2 VM actions work for each
guest OS family in the default image catalog and what each one depends on. For the
image pipeline see
[Managing OS images](/platform/managing-os-images); for the tenant view of the same
actions see [VM lifecycle](/cloud/vm-lifecycle).

## What depends on the guest

Most actions are performed by KubeVirt on the virtual hardware and work for any guest.
Two groups depend on the guest:

- **qemu-guest-agent.** The guest OS name, kernel, hostname and filesystem list in the
  console, SSH key delivery by `qemuGuestAgent` propagation, and the `guestAgentPing`
  readiness probe all need the agent running in the guest. KubeVirt reports it as the
  VMI condition `AgentConnected=True`.
- **Guest drivers.** Hot-plugged disks use the SCSI bus (virtio-scsi). Online expansion
  of an attached disk needs the guest to accept a capacity-change event. Every family
  whose disks could be inspected from inside the guest (all rows below except Talos,
  CirrOS and Windows) showed the hot-plugged disk and its new size.

Pause and unpause (a libvirt suspend), restart, stop, the serial console, the VNC
console and the screenshot do not use the agent.

## Capability matrix

Measured end to end against a live cluster on KubeVirt v1.8.1 with the
`HotplugVolumes` and `ExpandDisks` feature gates, through the same console API calls
the browser makes. Each family got a small VM built like the create wizard builds it.
The hot-plug disk was a new blank `local-path` DataVolume, and the expansion disk an
`rbd-vm` DataVolume grown from 1 GiB to 2 GiB while attached. "Seen in guest" means a
login over SSH found the device, or its new size, in `/sys/block`. "API only" means
KubeVirt reported the volume attached and the PVC reported its new capacity; with no
login available, neither the disk nor its new size was checked inside the guest. Kube-DC re-runs this qualification against a live cluster before a release.

| Guest OS | qemu-guest-agent | Guest OS info and filesystems | Pause | Hot-plug SCSI attach and detach | Online disk expansion | Serial, VNC, screenshot | Restart, stop |
|---|---|---|---|---|---|---|---|
| Ubuntu 26.04 LTS | Yes (baked or first boot) | Yes | Yes | Yes, seen in guest | Yes, seen in guest | Yes | Yes |
| Ubuntu 24.04 LTS | Yes (baked or first boot) | Yes | Yes | Yes, seen in guest | Yes, seen in guest | Yes | Yes |
| Debian 12 | Yes (baked or first boot) | Yes | Yes | Yes, seen in guest | Yes, seen in guest | Yes | Yes |
| Debian 13 | Yes (baked or first boot) | Yes | Yes | Yes, seen in guest | Yes, seen in guest | Yes | Yes |
| CentOS Stream 9 | Yes (baked or first boot) | Yes | Yes | Yes, seen in guest | Yes, seen in guest | Yes | Yes |
| Fedora 42 | Yes (baked or first boot) | Yes | Yes | Yes, seen in guest | Yes, seen in guest | Yes | Yes |
| openSUSE Leap 15.6 | Yes (baked or first boot) | Yes | Yes | Yes, seen in guest | Yes, seen in guest | Yes | Yes |
| Alpine Linux 3.21 | Yes (first boot, `apk`) | Yes | Yes | Yes, seen in guest | Yes, seen in guest | Yes | Yes |
| Gentoo Linux | **No** (not in the image) | **No**: the console says the agent is not connected | Yes | Yes, seen in guest | Yes, seen in guest | Yes | Yes |
| Talos Linux 1.14 | Yes (in the image) | Yes | Yes | Yes (API only, no login) | Yes (API only, no login) | Yes | Yes |
| CirrOS (test) | **No** | **No**: the console says the agent is not connected | Yes | Not qualified | Not qualified | Yes | Yes |
| Windows 11 (golden) | Yes (in the golden image) | Yes | Not in the harness | Not in the harness | Not in the harness | Not in the harness | Not in the harness |

**Baked** means the published image already contains qemu-guest-agent, from the
`qga-bake` pipeline (see [Managing OS images](/platform/managing-os-images)).
**First boot** means the catalog's cloud-init installs and starts it on first boot.
That needs package-repository egress from the project network and takes from a few
seconds (Alpine `apk`) to about a minute (`apt-get update`). A cluster that does not
publish baked artifacts serves the plain mirror image, so the Ubuntu, Debian, CentOS
Stream, Fedora and openSUSE families get the agent at first boot there. In chart
v0.9.11-rc.15 and earlier the openSUSE entry ran `apt-get`, which openSUSE does not
have, so an openSUSE VM from the plain image got no agent. The entry now uses
`zypper`, like the bake recipe. The matrix row was measured with the baked image. Talos and the
Windows golden image include the agent. Gentoo and CirrOS have none (below).

### Why a capability is missing

- **Gentoo: no guest agent.** The Gentoo cloud-init autobuild ships no
  qemu-guest-agent package, and Portage cannot install one at first boot without
  syncing a tree and building. The catalog marks the family `guestAgent: false`. The
  wizard then delivers the SSH key with `noCloud` propagation and leaves out the
  `guestAgentPing` probe. Guest OS information and filesystems are unavailable. The
  console says so instead of showing an error. Everything else works. A tenant can
  install the agent later (`emerge --sync && emerge app-emulation/qemu-guest-agent`,
  then enable the service). After that the guest panels fill in without any VM change.
- **CirrOS (test image): no guest agent, no SSH from current clients.** CirrOS has no
  package manager. Its dropbear 2018.76 refuses the project key from a current OpenSSH
  client ("no mutual signature supported"). Use it only to test the hypervisor path.
- **Talos: no login user.** Talos has no SSH or shell, so the in-guest checks do not
  apply. The agent, consoles, pause and the API actions work.
- **Windows** is not in the automated matrix because its golden image is tens of GiB.
  The golden image ships the agent (`AgentConnected=True`), so the guest panels work.
  Pause and hot plug use the same KubeVirt API as Linux. Whether a hot-plugged SCSI
  disk appears in the guest depends on the VirtIO SCSI (`vioscsi`) driver in the
  golden image. The harness has not checked this. Re-qualify it by hand on a cluster
  that holds the golden image.

### Platform requirements behind the matrix

- Hot plug needs the KubeVirt feature gate `HotplugVolumes`. Online expansion needs
  `ExpandDisks` and a storage class with `allowVolumeExpansion: true`. `local-path`
  cannot expand. Use `rbd-vm` for disks you expect to grow.
- A new blank or imported disk is written by a CDI importer pod in the tenant
  namespace. That pod counts against the organization's hierarchical quota with the
  limits configured in the CDI resource (`spec.config.podResourceRequirements`; read
  the effective value with `kubectl get cdiconfig config -o
  jsonpath='{.status.defaultPodResourceRequirements}'`; 2 GiB memory on the cluster
  that produced this matrix). With the quota nearly used up, the importer is
  refused, the DataVolume stays in `ImportScheduled`, and a hot-plug attach cannot
  complete until quota is freed. CDI keeps retrying the importer; whether a stuck
  DataVolume is later removed depends on the cluster's import garbage collection. `kubectl -n cdi logs deploy/cdi-deployment` shows
  `exceeded hierarchical quota`.
- KubeVirt refuses to pause a VMI that has a `livenessProbe` (403). The console does
  not offer Pause for such a VMI.

## How the console reports these states

The console API (`/api/ui/v1/vm/...` in the backend) answers guest OS information and
filesystem requests from the VMI status before it calls KubeVirt. It reads the VMI
first; only when there is no VMI does it read the VM:

| State, checked in this order | HTTP | `reason` |
|---|---|---|
| no VMI, and the VM does not exist either | 404 | — |
| no VMI for an existing VM, or the VMI is not `Running` | 409 | `VMNotRunning` |
| `Paused=True` | 409 | `VMPaused` |
| no `AgentConnected=True` | 409 | `GuestAgentNotConnected` (with `agentConnected: false`) |

The console asks for guest information only for a running VM with
`AgentConnected=True`. A stopped VM shows a prompt to start it, a paused VM a prompt
to resume it, and a running VM without the agent the agent-not-connected message.
Any other KubeVirt refusal of a VM action
(pause, hot plug, start, stop) reaches the console as JSON carrying KubeVirt's own
message and status (`upstreamStatus`), never a bare HTTP status.

:::note KubeVirt and the Accept header
virt-api registers the lifecycle and hot-plug subresources (`start`, `stop`,
`restart`, `pause`, `unpause`, `addvolume`, `removevolume`, `vnc/screenshot`) without
a list of produced media types. A request whose `Accept` header names only concrete
types, such as `application/json`, matches no route and is refused with `406 Not
Acceptable` before KubeVirt handles it. An integration that calls these subresources
directly must include `*/*` in `Accept`, as kubectl and virtctl do, for example
`Accept: application/json, */*` for actions and `Accept: image/png, */*` for the
screenshot.
:::
