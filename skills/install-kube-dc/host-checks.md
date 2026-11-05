# Host checks

All commands are read-only. Run them on every server (`ssh user@server '...'`)
and record the output. Each check names what passes.

## Reach and identity

```bash
kube-dc bootstrap connect user@server           # SSH, passwordless sudo, internal IP, OS
ssh-keyscan -t ed25519 server | ssh-keygen -lf -   # compare with an independent record
```

Pass: `connect` succeeds; the host key fingerprint matches the operator's
record (pass it to `init` as `KUBE_DC_INIT_SSH_HOST_KEY_SHA256`).

## Operating system

```bash
uname -r; . /etc/os-release; echo "$PRETTY_NAME"
swapon --show
timedatectl show -p NTPSynchronized
test -e /dev/kvm && echo kvm || echo no-kvm
nproc; free -g
```

Pass: kernel 7.0 or later when Ceph is used (7.2 or later for FIPS); no swap;
`NTPSynchronized=yes`; `/dev/kvm` on every host that should run VMs (otherwise
plan `--no-kubevirt`, or `--allow-no-kubevirt-eligible` for a lab). A one-node
lab needs at least 12 vCPU, 27 GiB memory and 100 GB disk.

## DNS resolver

```bash
grep -E '^(nameserver|search|domain)' /etc/resolv.conf
getent hosts get.rke2.io github.com
```

Pass: the resolvers answer both site and internet names, and neither `search`
nor `domain` contains the installation domain (or a name under it). Pods
inherit the node's search list with `ndots:5`, so with `*.DOMAIN` a wildcard
record, a search entry for the domain makes every short external name inside
Pods resolve to the front door. Keep only a parent domain in `search`.

## Network

```bash
ip -br link; ip -br addr
ip -d link show | grep -E 'vlan|bond|mtu' | head -40
ip route; ip rule
cat /proc/net/bonding/bond0 2>/dev/null | head -20
```

Record per server: the carrier for the external network (a bond or NIC, never
a bond member), whether a Linux VLAN interface already uses the external
VLAN tag (Kube-OVN must own that tag on the carrier; plan its removal with the
operator), the MTU, and the default route. The interactive panel's **Inspect
network hosts** reads the same facts and proposes interface mappings.

## Outbound access

```bash
for u in https://registry-1.docker.io/v2/ https://ghcr.io/v2/ https://quay.io/v2/ https://registry.k8s.io/v2/; do
  curl -s -o /dev/null -m 10 -w "%{http_code} $u\n" "$u"; done
```

Pass: an HTTP status (200 or 401) for each, not a timeout. Docker Hub limits
anonymous pulls per source address
([current limits](https://docs.docker.com/docker-hub/usage/pulls/)); a
multi-node install behind one NAT address can exhaust it. Pods then show
`ImagePullBackOff` with `429 Too Many Requests` until the limit resets and
recover on their own. To avoid it, give `/etc/rancher/rke2/registries.yaml`
on every host a `mirrors` entry for `docker.io` (your mirror, or Docker Hub
itself) and, for authenticated pulls, its credentials under `configs`, before
`bootstrap install`. The embedded registry mirror the CLI enables refuses a
file with credentials but no `mirrors` entry.

## Disks

```bash
lsblk -o NAME,SIZE,TYPE,ROTA,MODEL,SERIAL,MOUNTPOINTS,FSTYPE
ls -l /dev/disk/by-id/ | grep -v part
sudo wipefs -n /dev/DEVICE          # -n: report signatures, change nothing
ls /sys/class/block/DEVICE/holders/ # must be empty
sudo smartctl -H -A /dev/DEVICE     # or, for NVMe: sudo nvme smart-log /dev/DEVICE
findmnt /opt/local-path-provisioner
df -h /opt/local-path-provisioner /var/lib/rancher 2>/dev/null
```

Pass for a Ceph OSD disk: whole raw device, no partitions, no filesystem or
LVM signature, no holders, not mounted, not the boot disk, and healthy: SMART
overall `PASSED`, no media or integrity errors, wear and spare within the
vendor's limits. Report any error to the operator and do not select that
disk. Name a selected disk by its `/dev/disk/by-id/` path in the input file. `init` checks each selected disk
again over SSH and stops if it is not empty.

A disk that ever belonged to another Ceph cluster can look empty to `lsblk`
and `blkid` and still be refused by Ceph. Check with Ceph's own tools from a
privileged pod with the Ceph image after the nodes joined, before Rook
reconciles:

```bash
ceph-volume raw list                              # must not list the device
ceph-bluestore-tool show-label --dev /dev/DEVICE  # must fail with "unable to read label"
```

Only the operator decides to erase a disk (`ceph-volume lvm zap --destroy`).

Platform state: with three or more manager nodes, `init` places the Grafana
and Keycloak PostgreSQL databases (and OpenBao with `OPENBAO_REPLICAS=3`) on
those nodes with `local-path`. Give each control-plane node a dedicated
filesystem mounted at `/opt/local-path-provisioner` (tens of GiB) before RKE2
is installed.
