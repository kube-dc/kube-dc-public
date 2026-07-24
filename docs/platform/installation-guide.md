# Installation Guide

This guide walks you through deploying Kube-DC on three bare-metal servers from scratch. By the end, you will have a fully operational cloud platform with HA control plane, virtual machine support, managed Kubernetes clusters, and public IP networking.

**Time estimate:** ~60 minutes (excluding server provisioning).

:::info Prerequisites
Read the [Installation Overview](installation-overview.md) first to understand the reference architecture and network requirements.
:::

## Phase 1 — Server Preparation

### 1.1 Provision Servers

Provision three servers with **Ubuntu 24.04 LTS**. Throughout this guide, we use:

| Hostname | Role | Management IP |
|----------|------|---------------|
| `master-1` | Control plane + workloads | `192.168.0.1` |
| `master-2` | Control plane + workloads | `192.168.0.2` |
| `master-3` | Control plane + workloads | `192.168.0.3` |
| `bastion` (optional) | SSH jump host | `192.168.0.10` |

### 1.2 Configure Network Interfaces

Each server needs a static management IP and trunk access to the cloud and provider VLANs. The management network can be either the native (untagged) VLAN or a tagged VLAN — adapt the Netplan config below to match your switch configuration.

Create `/etc/netplan/60-kube-dc.yaml` on **each server** (adjust IPs and interface names):

```yaml
# /etc/netplan/60-kube-dc.yaml — master-1 example
network:
  version: 2
  renderer: networkd
  ethernets:
    # Management interface — carries node-to-node, API server, etcd traffic
    eth0:
      addresses:
        - 192.168.0.1/18          # Static IP on management network
      routes:
        - to: 0.0.0.0/0
          via: 192.168.0.254      # Management network gateway (internet access)
          on-link: true
          metric: 100
      nameservers:
        addresses: [8.8.8.8, 8.8.4.4]

    # Trunk interface — carries Cloud and Provider VLANs
    # Do NOT assign an IP here; Kube-OVN manages this interface via OVS bridges
    eth1:
      mtu: 9000                   # Jumbo frames recommended for cloud traffic
```

:::warning Important
- Replace `eth0` and `eth1` with your actual interface names (run `ip link` to check).
- Do **not** assign IPs to the trunk interface (`eth1`) — Kube-OVN will create OVS bridges and VLAN subinterfaces automatically.
- Do **not** pre-create empty VLAN subinterfaces for the cloud/provider VLANs (`vlans:` entries with `addresses: []`) — Kube-OVN owns those VLANs on the trunk; pre-created ones are redundant and can conflict with the OVS bridge setup. Pass the **trunk** interface (`EXT_NET_INTERFACE`) + the VLAN ID instead.
- On `master-2` use `192.168.0.2`, on `master-3` use `192.168.0.3`.
:::

:::tip LACP bonds (common pitfalls)
If the trunk is an 802.3ad bond, three defaults quietly hurt:

- `transmit-hash-policy: layer2` hashes all traffic toward one MAC onto a
  **single slave** — use `layer3+4` to actually use both links.
- `lacp-rate: slow` means link-failure detection up to **90 s** — use
  `lacp-rate: fast` (3 s) if you care about node-loss failover time.
- An `mtu: 9000` on the bond is **inherited by every VLAN on it**,
  including management. Only use jumbo end-to-end where the switch/router
  path supports it — a jumbo management VLAN against a 1500-byte gateway
  produces path-MTU blackholes that look like random hangs.

Also ensure only **one** default route: if another NIC runs DHCP, add
`dhcp4-overrides: { use-routes: false }` (or a high `route-metric`) so it
can't inject a second default route next to the management one.
:::

Back up the default netplan and apply:

```bash
sudo mkdir -p /root/netplan-backup
sudo cp /etc/netplan/*.yaml /root/netplan-backup/
sudo netplan apply
```

### 1.3 Update Hosts File

On **each server**, add all node entries:

```bash
cat <<EOF | sudo tee -a /etc/hosts
192.168.0.1  master-1
192.168.0.2  master-2
192.168.0.3  master-3
EOF
```

### 1.4 System Optimization

Run the following on **all three nodes**:

```bash
# Install required packages
sudo apt update && sudo apt -y upgrade
sudo apt -y install unzip iptables curl linux-headers-$(uname -r)

# Kernel parameters
cat <<EOF | sudo tee -a /etc/sysctl.conf
# Kube-DC requirements
fs.inotify.max_user_watches=1524288
fs.inotify.max_user_instances=4024
net.ipv4.ip_forward = 1
EOF
sudo sysctl -p

# Load conntrack module (required for kube-proxy)
sudo modprobe nf_conntrack
echo "nf_conntrack" | sudo tee -a /etc/modules

# Disable systemd-resolved to prevent DNS conflicts with CoreDNS
sudo systemctl stop systemd-resolved
sudo systemctl disable systemd-resolved
sudo rm -f /etc/resolv.conf
echo -e "nameserver 8.8.8.8\nnameserver 8.8.4.4" | sudo tee /etc/resolv.conf
```

---

## Phase 2 — RKE2 Cluster Bootstrap

The fastest path is `kube-dc bootstrap install`, which writes the canonical
RKE2 config and installs RKE2 for you over SSH (§2.0). If you'd rather do it
by hand — or need to understand exactly what that command produces — the
manual steps follow in §2.1+.

### 2.0 One command: `kube-dc bootstrap install` (recommended)

From your bastion (after installing the CLI — see [Phase 3.1](#31-install-the-kube-dc-cli)):

```bash
kube-dc bootstrap install master-1 \
  --ssh-host root@203.0.113.10 \
  --domain example.com \
  --preset cloud+public-vlan \
  --dry-run                       # review the resolved config, then drop --dry-run
```

It resolves the node's internal IP over SSH, then writes
`/etc/rancher/rke2/config.yaml` and installs + starts `rke2-server` with the
exact config the manual steps below produce:
`cni: none`, **`advertise-address` = the node's internal IP** (never a
NAT/floating public IP — this is the single-IP-NAT trap), cluster/service
CIDRs **pulled from the same `--preset` you'll pass to `init`** (so kube-ovn
and the fleet never disagree), and memory-tiered kubelet reserves with a
**`max-pods` floor of 200** (the platform is pod-dense; the upstream 110
default is too small for an all-in-one node). The node comes up **NotReady**
until Phase 3 installs the CNI — that's expected.

Since CLI v0.5.2 it also enables the **RKE2 embedded registry mirror (spegel)**
on every node by default: nodes P2P-share image content, so repeated
containerdisk/image pulls stay off the WAN. `EMBEDDED_REGISTRY=false` in the
environment opts out, and an existing operator-managed
`/etc/rancher/rke2/registries.yaml` is never overwritten. Pair it with the
image-acceleration stack that `bootstrap init` scaffolds by default
(tenant-cluster addons, zot registry depot, CDI OS-image mirror —
`--image-acceleration=false` opts out; see the
[enterprise install guide](private-ca-enterprise-install.md) §6).

> ⚠️ When retrofitting spegel onto an **existing** cluster, restart
> `rke2-server`/`rke2-agent` one node at a time and **drain or stop KubeVirt
> VMs on the node first** — restarting under running VMs can wedge the node
> (see the enterprise guide §1 for the failure signature and recovery).

Key flags: `--name` (RKE2 node-name; defaults to the positional arg — use the
same name in `init`), `--node-ip` / `--external-ip` (override auto-detection),
`--force` (re-run on an already-installed node — restarts to apply config
changes), `--set POD_CIDR=…` (override a preset CIDR). Requires passwordless
sudo (or a root login) on the node.

**Reaching nodes through a bastion.** `install`, the joins, `fetch-kubeconfig`,
`remove-node`, and `connect` all honour an SSH jump host from `~/.ssh/config`
(`ProxyJump`) or via `--ssh-jump user@bastion` — so you can run from your
laptop against nodes' **internal** IPs, tunnelling through a bastion (the jump
also covers the `--join-server` control-plane). Host keys are verified
strictly; for unattended runs add `--ssh-accept-new-host-keys` (records +
trusts an **unknown** host key — a key **mismatch** is still refused as a
possible MITM).

> The jump covers the **SSH-driven** steps only (`connect` / `install` /
> `fetch-kubeconfig`). Phase 3's `bootstrap init` reaches the cluster's
> **apiserver over HTTPS** (via `kube-api.<domain>` → the node's public
> IP), not over SSH — so the node still needs its API endpoint reachable
> from wherever you run `init` (a public FIP + the wildcard DNS from
> §3.2). A node with *no* public endpoint would need the apiserver
> tunnelled separately; `--ssh-jump` does not do that.

**Pre-flight (optional):** `kube-dc bootstrap connect root@203.0.113.10`
checks a node is reachable + drivable before you install — SSH reach/auth,
passwordless `sudo -n` (install needs it), and the internal IP that would
become the apiserver advertise-address. It takes the same `--ssh-jump` /
`--ssh-accept-new-host-keys` and exits non-zero if the node isn't ready, so
it works as a CI gate.

Then skip to [§2.4 Verify](#24-verify-the-ha-cluster) (single node) or use §2.3
to join additional control-plane nodes, and continue to Phase 3.

### 2.1 Install RKE2 on master-1 (manual alternative)

SSH into `master-1` and install kubectl:

```bash
curl -LO "https://dl.k8s.io/release/$(curl -L -s https://dl.k8s.io/release/stable.txt)/bin/linux/amd64/kubectl"
chmod +x kubectl && sudo mv kubectl /usr/local/bin/
```

Create the RKE2 server configuration. The kubelet `system-reserved` /
`kube-reserved` / `eviction-hard` block protects kubelet, containerd,
and etcd from kernel OOM under sudden memory pressure (without it,
the kernel picks any victim, which has caused a 4-hour control-plane
recovery on production). Pick the tier that matches your node memory:

| Node memory | `system-reserved` | `kube-reserved` | `eviction-hard` | `max-pods` |
|---|---|---|---|---|
| **\<32 GiB** | `cpu=200m,memory=1Gi` | `cpu=200m,memory=1Gi` | `memory.available<500Mi,nodefs.available<10%` | `200` |
| **32–64 GiB** | `cpu=300m,memory=2Gi` | `cpu=300m,memory=2Gi` | `memory.available<1Gi,nodefs.available<10%` | `220` |
| **≥64 GiB** | `cpu=500m,memory=4Gi` | `cpu=500m,memory=4Gi` | `memory.available<2Gi,nodefs.available<10%` | `250` |

Each tier reserves ≈10–15% of total memory — generous enough to
protect system services even under burst, slim enough to leave the
bulk of the box for tenant workloads. `max-pods` overrides Kubernetes'
upstream 110-pods-per-node default — kube-dc is pod-dense (an all-in-one
node runs the whole platform and exceeds 110 during reconcile), so the
floor is **200** even on the smallest tier; larger nodes get more
headroom. The fleet bootstrap script
(`kube-dc-fleet/bootstrap/rke2/install-server.sh`) — the same one
`kube-dc bootstrap install` embeds — selects the right tier
automatically from `/proc/meminfo`. The example below uses the
**≥64 GiB tier** since production Kube-DC nodes are typically large.

```bash
sudo mkdir -p /etc/rancher/rke2/

cat <<EOF | sudo tee /etc/rancher/rke2/config.yaml
node-name: master-1
disable-cloud-controller: true
disable: rke2-ingress-nginx              # Replaced by Envoy Gateway
cni: none                                # Replaced by Kube-OVN
cluster-cidr: "10.100.0.0/16"
service-cidr: "10.101.0.0/16"
cluster-dns: "10.101.0.11"
node-label:
  - kube-dc-manager=true
  - kube-ovn/role=master
# OIDC authn is wired in a post-install step (after the gardener
# oidc-webhook-authenticator DaemonSet is up). Do not pre-set
# --authentication-config or --authentication-token-webhook-config-file
# here — RKE2 boots cert-only, then the operator adds the webhook flag
# per node. Pick the kubelet-arg block matching your node memory (see
# table above).
kubelet-arg:
  - system-reserved=cpu=500m,memory=4Gi
  - kube-reserved=cpu=500m,memory=4Gi
  - eviction-hard=memory.available<2Gi,nodefs.available<10%
  - max-pods=250
node-ip: 192.168.0.1                     # Management network IP
advertise-address: 192.168.0.1
tls-san:
  - kube-api.example.com                 # Your API server domain
  - 192.168.0.1
  - 192.168.0.2
  - 192.168.0.3
EOF
```

Install and start RKE2:

```bash
export INSTALL_RKE2_VERSION="v1.35.0+rke2r1"
export INSTALL_RKE2_TYPE="server"
curl -sfL https://get.rke2.io | sh -
sudo systemctl enable rke2-server.service
sudo systemctl start rke2-server.service
```

Monitor startup (wait until `rke2-server` settles; the node registers **NotReady** — expected until the CNI lands in Phase 3):

```bash
sudo journalctl -u rke2-server -f
```

Configure kubectl:

```bash
mkdir -p ~/.kube
sudo cp /etc/rancher/rke2/rke2.yaml ~/.kube/config
sudo chown $(id -u):$(id -g) ~/.kube/config
chmod 600 ~/.kube/config
```

Verify:

```bash
kubectl get nodes
# NAME       STATUS     ROLES                       AGE   VERSION
# master-1   NotReady   control-plane,etcd,master   1m    v1.35.0+rke2r1
```

The node will show `NotReady` until a CNI is installed — this is expected.

### 2.2 Get the Join Token

On `master-1`, retrieve the join token:

```bash
sudo cat /var/lib/rancher/rke2/server/node-token
```

Save this token — you need it for `master-2` and `master-3`.

### 2.3 Add worker nodes with `kube-dc bootstrap install --join-server`

To add a **worker** (rke2-agent) to the cluster, point the same
`bootstrap install` command at an existing control-plane node — its
node-token and internal IP are read over SSH, and the worker's RKE2 agent
is installed and joined:

```bash
# --ssh-host    the new worker
# --join-server any existing control-plane node (token + internal IP read over SSH)
# --dry-run     review the plan first, then drop it to apply
kube-dc bootstrap install worker-1 \
  --ssh-host root@203.0.113.20 \
  --name worker-1 \
  --join-server root@203.0.113.10 \
  --dry-run
```

No `--domain`/`--preset` needed — a worker inherits cluster config from
the server it joins. The agent dials the control-plane's **internal** IP
(auto-detected, never a NAT/floating IP). The worker registers and shows
up in `kubectl get nodes` (NotReady until kube-ovn schedules onto it). If
you already have the token, pass `--join-token` + `--cp-host` to skip the
control-plane SSH. To reach the worker (and the `--join-server`
control-plane) through a bastion, add `--ssh-jump user@bastion` — see the
[reachability note](#20-one-command-kube-dc-bootstrap-install-recommended)
in §2.0.

> This flow is validated end-to-end (a worker VM joining a live cluster).

### 2.3.1 Join master-2 and master-3 with `--role server`

Additional control-plane nodes (for etcd quorum — run 3 for HA) use the
**same command with `--role server`**. Unlike a worker, an additional
server writes its own config, so it still needs `--domain` + `--preset`
(use the SAME values as the first server):

```bash
# --role server      makes this an ADDITIONAL control-plane, not a worker
# --join-server       any existing control-plane node (token + internal IP read over SSH)
# --domain/--preset   MUST match the first server (an additional server writes its own config)
kube-dc bootstrap install master-2 \
  --ssh-host root@203.0.113.11 \
  --name master-2 \
  --join-server root@203.0.113.10 \
  --role server \
  --domain acme.com \
  --preset cloud+public-vlan \
  --dry-run
```

Review the plan (it announces "control-plane JOIN", the dialled
`<cp>:9345` supervisor, and the redacted token), then drop `--dry-run`.
Repeat for `master-3`. Each node registers with the `control-plane,etcd`
roles and its etcd joins the quorum. The join token is read over SSH and
**never printed**. This flow is validated end-to-end (a VM joining a live
cluster as a second `control-plane,etcd` node + etcd member).

> **etcd quorum:** run an ODD number of control-plane nodes (1 or 3, not
> 2). With exactly 2 members, losing either breaks quorum. To *remove* a
> control-plane node later, remove its etcd member first
> (`etcdctl member remove`) — deleting the node/VM alone strands the
> member and can break quorum.

<details>
<summary>Manual fallback (no CLI) — write the RKE2 join config by hand</summary>

On **each additional node** (`master-2`, `master-3`), create the RKE2 config and join:

```bash
sudo mkdir -p /etc/rancher/rke2/

cat <<EOF | sudo tee /etc/rancher/rke2/config.yaml
token: <TOKEN_FROM_MASTER_1>
server: https://192.168.0.1:9345         # master-1 management IP
node-name: master-2                      # Use master-3 on the third node
disable-cloud-controller: true
disable: rke2-ingress-nginx
cni: none
node-label:
  - kube-dc-manager=true
  - kube-ovn/role=master
kubelet-arg:
  - system-reserved=cpu=500m,memory=4Gi   # tier matching this node — see table above
  - kube-reserved=cpu=500m,memory=4Gi
  - eviction-hard=memory.available<2Gi,nodefs.available<10%
node-ip: 192.168.0.2                     # This node's management IP
advertise-address: 192.168.0.2
tls-san:
  - kube-api.example.com
  - 192.168.0.1
  - 192.168.0.2
  - 192.168.0.3
EOF

# Install and start
export INSTALL_RKE2_VERSION="v1.35.0+rke2r1"
export INSTALL_RKE2_TYPE="server"
curl -sfL https://get.rke2.io | sh -
sudo systemctl enable rke2-server.service
sudo systemctl start rke2-server.service
```

</details>

### 2.4 Verify the HA Cluster

Back on `master-1`:

```bash
kubectl get nodes
# NAME       STATUS     ROLES                       AGE   VERSION
# master-1   NotReady   control-plane,etcd,master   10m   v1.35.0+rke2r1
# master-2   NotReady   control-plane,etcd,master   3m    v1.35.0+rke2r1
# master-3   NotReady   control-plane,etcd,master   1m    v1.35.0+rke2r1
```

All three nodes should appear with the `control-plane,etcd,master` roles. `NotReady` status is expected until the CNI is deployed in Phase 3.

### 2.5 Remove a node with `kube-dc bootstrap remove-node`

To take a node out of the cluster safely, use `remove-node` — it performs
the steps in the order that protects etcd quorum:

```bash
# Preview first (prints the plan, changes nothing); then add --yes to apply
kube-dc bootstrap remove-node worker-3 --ssh-host root@203.0.113.23
kube-dc bootstrap remove-node worker-3 --ssh-host root@203.0.113.23 --yes
```

It (1) for a control-plane/etcd node, removes the etcd member **first**
(while it's still healthy), then (2) cordons + drains, (3) deletes the node
object, and (4) tears down `rke2` on the host over SSH (`rke2-killall.sh`, or
`rke2-uninstall.sh` with `--uninstall`). Cluster access uses your `kubectl`
(`KUBECONFIG` / `--kubeconfig`); the node-side teardown needs `--ssh-host`
(omit it to skip and run the script yourself). It **refuses to remove the last
control-plane/etcd node**.

> **etcd quorum:** this ordering matters — deleting a control-plane node/VM
> *without* removing its etcd member first strands the member and, on a
> 2-member cluster, breaks quorum the moment the node stops. `remove-node`
> handles it for you. `remove-node` does **not** delete the VM/host — that's
> your infrastructure to remove afterwards.

---

## Phase 3 — Deploy Kube-DC with the `kube-dc` CLI

Kube-DC installs through its own CLI (`kube-dc bootstrap init`), which
scaffolds a **GitOps fleet repository**, bootstraps Flux against it, and
lets Flux reconcile the whole platform (Kube-OVN, cert-manager, Envoy
Gateway, Keycloak, KubeVirt, Kamaji, Rook Ceph, Grafana/Mimir/Loki, and
the Kube-DC controllers). After install, **the fleet repo is the source
of truth** — you change the cluster by committing to it, not by running
`kubectl apply` or `helm install` by hand.

:::info Why a fleet repo?
Every component version, network setting, and credential lives in
`clusters/<name>/` in your fleet repo (secrets encrypted with SOPS +
age). Flux continuously reconciles it. This is what makes upgrades,
disaster recovery, and multi-cluster fleets tractable — and it is the
path validated end-to-end in the project's installer test plan.
:::

### 3.1 Install the `kube-dc` CLI

On your **bastion / workstation** (not necessarily a cluster node):

```bash
# Linux amd64
curl -sL https://github.com/kube-dc/kube-dc-public/releases/latest/download/kube-dc_linux_amd64 \
  -o /usr/local/bin/kube-dc && sudo chmod +x /usr/local/bin/kube-dc
# macOS: swap in kube-dc_darwin_amd64 / kube-dc_darwin_arm64
kube-dc bootstrap doctor --no-tty     # verify local tooling
```

`doctor` checks the required tools — `kubectl`, `flux`, `helm`, `sops`,
`age`, `git`, `gh`, `ssh` — and reports any that are missing or below the
minimum version. Fix every blocker before continuing. Also ensure the
control-plane SSH key is loaded (`ssh-add <key>`); the CLI reads it from
your `ssh-agent`, it never takes a key flag.

### 3.2 Configure wildcard DNS (required before init)

Point a **wildcard A record** at the public IP of `master-1`. `init`
runs a DNS gate up front and Let's Encrypt (HTTP-01) needs the names to
resolve during reconcile:

```
*.example.com       →  203.0.113.10      (A record — master-1 public IP)
kube-api.example.com →  203.0.113.10     (A record — API server SNI)
```

This is what makes `console.`, `login.`, `grafana.`, `flux.`, and every
per-tenant hostname resolve. After Phase 4 (MetalLB HA) you re-point the
wildcard at the floating IP.

:::tip Behind a 1:1 NAT / floating IP?
On clouds where the node never sees its own public IP locally (a
kube-dc FIP, an EC2 elastic IP, an OpenStack/Hetzner floating IP), pass
`--ssh-host`. The CLI SSH-probes the node, writes the **arriving
(internal) IP** into the fleet, and drops the Gateway's `:6443`
passthrough listener — otherwise the front door silently resets. Bare
metal with the public IP bound on the NIC needs none of this.
:::

### 3.3 Run `kube-dc bootstrap init`

Run this from the bastion with `KUBECONFIG` pointing at the RKE2 cluster
from Phase 2. Start with `--dry-run` to review the plan, then re-run with
`--yes` to apply:

```bash
kube-dc bootstrap init \
  --preset=cloud+public-vlan \
  --mode=install \
  --name=dc1 \
  --domain=example.com \
  --node-external-ip=203.0.113.10 \
  --email=admin@example.com \
  --fleet-mode=new-repo \
  --repo=$HOME/fleet-dc1 \
  --github-owner=my-org --github-repo=my-kube-dc-fleet \
  --object-storage-mode=rook-ceph-multi-node \
  --ceph-node=master-1=/dev/nvme1n1 \
  --ceph-node=master-2=/dev/nvme1n1 \
  --ceph-node=master-3=/dev/nvme1n1 \
  --ssh-host=admin@203.0.113.10 \
  --set=EXT_NET_INTERFACE=eth1 \
  --set=EXT_NET_VLAN_ID=200 \
  --set=KUBE_OVN_MASTER_NODES=192.168.0.1,192.168.0.2,192.168.0.3 \
  --dry-run                                # review, then swap for --yes
```

**Key flags:**

| Flag | Meaning |
|------|---------|
| `--preset` | `cloud+public-vlan` (cloud + provider VLANs), `cloud-vlan`, `internal-only` (single-node / lab, no provider VLAN), or `custom` |
| `--name` | Cluster name — becomes `clusters/<name>/` in the fleet repo |
| `--domain` / `--node-external-ip` | Wildcard domain + the public IP it resolves to (§3.2) |
| `--fleet-mode` | `new-repo` (CLI creates the GitHub/GitLab repo), `existing-repo`, or `existing-fleet` (add a cluster to a repo that already has siblings — inherits their version pins) |
| `--repo` | Local path for the fleet checkout. Point it at an **empty directory** — the CLI pulls the shared platform trees into it from the fleet-starter OCI artifact (see below). Default when omitted: `$KUBE_DC_FLEET`, else `~/.kube-dc/fleet` |
| `--github-owner` / `--github-repo` | Where the fleet repo lives (auto-created in `new-repo` mode) |
| `--object-storage-mode` | `rook-ceph-multi-node` (3+ OSDs, HA), `rook-ceph-local` (single OSD — lab), `rook-ceph-pvc`, `external-*`, or `disabled` |
| `--ceph-node=NODE=DEVICE` | One raw block device per OSD node (repeat 3× for multi-node) |
| `--ssh-host` | Control-plane SSH target — enables kubeconfig auto-pull **and** NAT-topology detection (§3.2) |
| `--set=KUBE_OVN_MASTER_NODES` | Control-plane **internal** IPs (comma-separated) — not emitted by the preset, always set it |
| `--set=EXT_NET_INTERFACE` / `EXT_NET_VLAN_ID` | Trunk NIC + cloud VLAN ID from Phase 1 (`EXT_NET_VLAN_ID=0` = untagged carrier) |
| `--set=INGRESS_MODE` | `metallb-lb` (default — Envoy Service `type: LoadBalancer` via MetalLB). `hostnetwork` (Envoy binds `:443` on the host) is a real topology but **not yet automated — `init` rejects it**; scaffold with `metallb-lb` and apply the EnvoyProxy hostNetwork patch manually if you need it |
| `--set=METALLB_MODE` | `l2` (default — ARP on a shared L2 segment) or `bgp` (announce VIPs as `/32` BGP routes — routed/L3-only fabrics; see §5 “BGP announcement”). `bgp` requires `METALLB_BGP_LOCAL_ASN`, `METALLB_BGP_PEER_ASN`, `METALLB_BGP_PEER_ADDRESS` (all validated) |

What `init` does, in order: **fetches the fleet starter** (when `--repo`
is a fresh/empty directory, the shared platform trees — `bootstrap/`,
`infrastructure/`, `platform/`, `addons/` — are pulled from the
versioned OCI artifact `oci://ghcr.io/kube-dc/fleet-starter:<cli-version>`
and committed; a directory that already carries them is used as-is;
override with `--starter-ref`) → generates a SOPS **age key** → creates +
pushes the fleet repo → scaffolds `clusters/dc1/` (network, object
storage, encrypted secrets) → `flux bootstrap` → pre-installs the
CNI/CRD-bearing charts so a bare cluster can reconcile → hands off to
Flux. It is idempotent and rolls back its own commit if the push fails.
You do **not** need to clone or download anything besides the CLI —
point `--repo` at an empty directory.

:::info Single-node / lab install
For a one-box trial, use `--preset=internal-only --object-storage-mode=rook-ceph-local --rook-osd-node=<node> --rook-osd-size-gb=40`
and skip the provider-VLAN `--set` flags. Size the node at **≥12 vCPU /
27 GiB / 100 GB** — the full platform plus reconcile churn needs it.
:::

### 3.3.2 Interactive panel + reusable config (`--config` / `--save-config`)

Run `kube-dc bootstrap init` **with no flags** in a terminal and you get a
guided settings panel (sections: Basics / Fleet / Network / Storage /
Gates / Review) instead of a long flag line. Keys: `Tab` switches the
section list ↔ fields, `↑↓` move, `Enter` edits a text field / cycles a
select / toggles / Applies, `←/→` cycle a select in place, `S` saves a
draft, `?` shows full help, `Esc` steps back, `q` quits. Each field shows a
`*` if required and `✓`/`⚠` for valid/invalid; the section list shows
`✓`/`⚠` per section and the title shows live readiness. Long sections
scroll. The Review pane shows the equivalent flag command (and any
preserved advanced `--set` keys) before you Apply.

You don't have to retype everything each run. The wizard, the flags, and
CI all share **one prefill format — the fleet's own `cluster-config.env`**:

| Action | How |
|--------|-----|
| **Prefill from a file** | `kube-dc bootstrap init --config install.env` — opens the panel **pre-filled**; add `--yes --no-tty` to run headless |
| **Clone from a sibling** | `--config` an existing cluster's `clusters/<name>/cluster-config.env` — **every operator key is carried** (identity, network, gateway nodes/type, MetalLB, anchors, object storage + replication, SMTP, quotas, feature flags); only scaffold-owned keys are dropped (versions/image tags + domain-derived `KUBE_API_EXTERNAL_URL`/`KEYCLOAK_HOSTNAME`/`OVN_DB_IPS` + universal/preset network defaults), logged as "N ignored" |
| **Prefill from env** | export `KUBE_DC_INIT_*` vars (`KUBE_DC_INIT_CLUSTER_NAME`, `KUBE_DC_INIT_MODE`, …) — handy in CI |
| **Save a reusable spec** | `--save-config install.env` writes the resolved inputs (runs on `--dry-run` too) |
| **Save a draft, decide later** | press **`S`** in the panel → writes `kube-dc-init.draft.env`; resume with `--config kube-dc-init.draft.env` |

Precedence (lowest → highest): **defaults → `--config` file → `KUBE_DC_INIT_*` env → explicit flags → your edits in the TUI.**

The file uses cluster-config.env-native keys for config (`CLUSTER_NAME`,
`DOMAIN`, `EXT_NET_INTERFACE`, `KUBE_OVN_MASTER_NODES`, `KUBE_OVN_GW_NODES`,
`OBJECT_STORAGE_MODE`, …) and a `KUBE_DC_INIT_` prefix for install-only
orchestration (`_MODE`, `_FLEET_MODE`, `_GITHUB_REPO`, `_SSH_HOST`,
`_ALLOW_NO_KVM`, …), which is stripped before the cluster's real config is
written. The panel has dedicated fields for the install-critical
disk/network *pointers* (gateway nodes + type, Ceph replication size,
NIC/VLAN/master-nodes, OSD devices); any other operator `--set` key from a
prefill/clone (MetalLB, anchors, platform-endpoints, SMTP, quotas, feature
flags) is **preserved** through the panel and listed under Review as
"advanced (--set)" — edit those in the `.env`. It never contains the
git token (that comes from `gh`/`glab` auth). Starter templates:
[`examples/install/`](../../examples/install/) — `internal-only.env`,
`cloud-vlan.env`, `cloud-public-vlan.env`.

```bash
cp examples/install/internal-only.env my-cluster.env
$EDITOR my-cluster.env                          # edit CLUSTER_NAME, DOMAIN, IPs, repo…
kube-dc bootstrap init --config my-cluster.env  # panel opens pre-filled → review → Apply
```

:::tip Nested / cloud VM without `/dev/kvm`
Set `KUBE_DC_INIT_ALLOW_NO_KVM=true` (or toggle **Gates → Allow node
without /dev/kvm**) so the KubeVirt-eligibility gate doesn't block — VM
workloads won't schedule until a node exposes `/dev/kvm`, but the install
completes.
:::

### 3.3.1 Which mode? `install` / `adopt` / `resume`

`--mode` tells `init` what it's walking into. It auto-detects when
omitted (`--mode=auto`), but knowing the model helps you pick the right
path — and avoid the one that isn't automated yet:

| Your situation | Mode | What happens |
|----------------|------|--------------|
| Fresh RKE2 cluster, no Flux | `install` | Scaffolds the fleet + `flux bootstrap` + installs the whole platform (the flow in §3.3). |
| Cluster already runs some of kube-dc's components (cert-manager, kube-ovn, kubevirt, …) under Flux, but no kube-dc yet, **and it already has a fleet overlay** | `adopt` | kube-dc's Flux **takes those components over in place** — see below. |
| kube-dc is already installed here | `resume` | Re-runs the post-install steps idempotently; no re-scaffold. |
| A **foreign** cluster with no `clusters/<name>/cluster-config.env` in your fleet | *not automated yet* | Scaffold it into the fleet first (`install`/`existing-fleet`); full foreign import is a planned follow-up. |

#### What `adopt` means

Flux **takes existing components over in place** — the fleet's
Kustomizations run with `prune: false` + `force: true`, so Flux adopts
the running Helm releases instead of deleting and recreating them. The
one safety step is **pinning your fleet's component versions to the
versions already running**, so Flux's first reconcile doesn't upgrade or
restart anything.

#### Supported adopt flow

Adopt-in-place assumes the cluster **already has a fleet overlay**
(`clusters/<cluster>/cluster-config.env`). Pin live versions, then init:

```bash
# 1. Inventory what's already on the cluster (read-only)
kube-dc bootstrap adopt <cluster> --kubeconfig ./target.yaml

# 2. Preview the version pins, then write them (commit + push)
kube-dc bootstrap adopt <cluster> --kubeconfig ./target.yaml --pin-versions
kube-dc bootstrap adopt <cluster> --kubeconfig ./target.yaml --pin-versions --yes

# 3. Install kube-dc — the adopt gate verifies everything is pinned first
kube-dc bootstrap init --mode=adopt --name <cluster> … --yes
```

KubeVirt and CDI aren't Helm releases, so `--pin-versions` reads their
version off the operator CR automatically. Anything it genuinely can't
read is reported as *undetected* — resolve it with
`--manual-pin KEY=VERSION` or `--skip-component NAME`.

#### What `adopt` does **not** do (yet)

- It does **not** import a completely foreign cluster with no fleet
  overlay — scaffold the cluster into the fleet first.
- It does **not** generate "leave-this-component-unmanaged" (overlay-SKIP)
  rules — that's a planned, more invasive follow-up.

#### Adopt failure table

| Symptom | Cause | Fix |
|---------|-------|-----|
| `cluster … has no fleet overlay` | No `clusters/<name>/cluster-config.env` | Scaffold the cluster into the fleet first (this is the import boundary). |
| `N component(s) not version-pinned` (from `init --mode=adopt`) | Fleet pins drift from the live versions | Run `kube-dc bootstrap adopt <cluster> --pin-versions --yes`, then re-run `init`. |
| `… unresolved (…)` (from `--pin-versions`) | A component's live version can't be read (not a Helm release, CR absent) | `--manual-pin KEY=VERSION` or `--skip-component NAME`. |
| You accept the upgrade/restart risk anyway | — | `init --mode=adopt --allow-unpinned-adopt` (RISKY — expect components to upgrade/restart on the first reconcile). |

### 3.4 Watch the platform converge

Flux reconciles in dependency order:
`flux-system → infra-cni → infra-core → infra-object-storage → platform`
(plus the isolated `platform-cdi-storage` child, which converges on its
own once the CDI operator registers its CRDs).

```bash
export KUBECONFIG=~/.kube/config          # the cluster from Phase 2
flux get kustomizations                   # every listed Kustomization reaches Ready=True
flux get helmreleases -A                  # ~20 releases go Ready
kubectl -n rook-ceph get cephcluster      # Mons → OSDs → Ready
```

A full converge takes roughly **10–20 minutes** on adequately-sized
nodes. If a HelmRelease exhausts its retries during an early
resource-tight phase, nudge it with a suspend/resume flip:
`kubectl -n <ns> patch hr <name> --type=merge -p '{"spec":{"suspend":true}}'`
then set it back to `false`.

### 3.5 Post-install — SSO clients, OpenBao, credentials

Two post-install steps run once Keycloak and OpenBao are Ready. Run them
from your fleet-repo clone with `KUBECONFIG` at the new cluster:

```bash
# 1. OIDC clients (Flux Web, Grafana, admin console) — writes the client
#    secrets SOPS-encrypted into clusters/dc1/, then commit + push.
bash bootstrap/setup-keycloak-oidc.sh dc1
git push && flux reconcile kustomization platform --with-source

# 2. OpenBao — unseal-share custody + controller auth, fully automated.
kube-dc bootstrap openbao init dc1 --repo .
```

The Keycloak admin password is generated into the `keycloak` secret:

```bash
kubectl -n keycloak get secret keycloak \
  -o jsonpath='{.data.admin-password}' | base64 -d; echo
```

Organizations work **without** external SSO out of the box. To enable
Google login for tenants, run `hack/bootstrap-sso-realm.sh` (needs a
Google OAuth client), set `SSO_ENABLED=true` in
`clusters/dc1/cluster-config.env`, and push.

### 3.6 Verify the front door

```bash
for h in login console grafana flux; do
  curl -s -o /dev/null -w "$h=%{http_code}\n" https://$h.example.com/
done
# login=302  console=200  grafana=302  flux=200   → Let's Encrypt certs live
```

Then create your first tenant with the [First Project](../cloud/first-project.md)
flow, or apply an `Organization` + `Project` directly:

```bash
kubectl create ns acme
kubectl apply -f - <<'EOF'
apiVersion: kube-dc.com/v1
kind: Organization
metadata: { name: acme, namespace: acme }
spec: { email: admin@example.com, description: "Acme Inc." }
EOF
kubectl -n acme get organization acme -o jsonpath='{.status.ready}'   # → true
```

---

## Phase 4 — Post-Deployment Configuration

:::note GitOps flow
With the CLI install, MetalLB, monitoring, and the platform components
below are **already deployed by Flux** from your fleet repo. This phase
covers the cluster-specific pieces the fleet can't guess — chiefly the
**floating public IP** for HA ingress. Apply these as fleet commits
(under `clusters/<name>/`) rather than raw `kubectl` so they survive
reconciles.
:::

### 4.1 Promote Ingress to a Floating IP (MetalLB HA)

Right after install, Envoy Gateway serves on **one node's public IP**
(the fleet base pins the Service to `externalIPs: [NODE_EXTERNAL_IP]`) —
working, but not HA. MetalLB promotes it to a **floating public IP**
that fails over between nodes.

**GitOps path (recommended — how the production clusters run).** Three
fleet commits under `clusters/<name>/`:

1. **Set the keys** in `clusters/<name>/cluster-config.env` (scaffolded
   with `CHANGEME` values):

   ```bash
   METALLB_FLOATING_IP=203.0.113.20   # your dedicated floating public IP
   METALLB_INTERFACE=br-ext-cloud     # NIC/bridge for the ARP announcement (l2 mode)
   METALLB_MODE=l2                    # or bgp — see the BGP section below
   ```

2. **Wire the MetalLB layers** — two Flux Kustomizations in the cluster
   overlay (skip if already present). `addons` installs the operator
   (`path: ./addons/metallb`, `dependsOn: platform`); `addons-config`
   ships the pool + advertisement CRs (`path: ./addons/metallb-config`,
   or `./addons/metallb-config-bgp` for BGP mode) and MUST `dependsOn:
   addons` **with a healthCheck on the metallb-controller Deployment** —
   the CRs reference MetalLB CRDs, and without the gate the first
   server-side dry-run fails with `no matches for kind
   "L2Advertisement"`. Both take `postBuild.substituteFrom:
   cluster-config` so the env keys above land in the CRs. Reference both
   files from `clusters/<name>/kustomization.yaml`.

3. **Point the Envoy Service at the floating IP** — patch the shared
   EnvoyProxy from the cluster's `platform.yaml` Flux Kustomization
   (replacing the single-node `externalIPs` pin):

   ```yaml
   # add under clusters/<name>/platform.yaml → spec.patches
   patches:
     - target:
         group: gateway.envoyproxy.io
         version: v1alpha1
         kind: EnvoyProxy
         name: custom-proxy-config
       patch: |-
         - op: add
           path: /spec/provider/kubernetes/envoyService/patch/value/metadata
           value:
             annotations:
               metallb.universe.tf/loadBalancerIPs: "${METALLB_FLOATING_IP}"
         - op: remove
           path: /spec/provider/kubernetes/envoyService/patch/value/spec/externalIPs
   ```

Commit + push, then `flux reconcile kustomization platform --with-source`.
Because the live Service's `loadBalancerClass` is immutable, delete it
once so it is recreated with the MetalLB class + floating IP:

```bash
kubectl delete svc -n envoy-gateway-system -l gateway.envoyproxy.io/owning-gateway-name
```

<details>
<summary>Manual fallback (no GitOps) — raw helm + kubectl</summary>

```bash
# Install MetalLB
helm repo add metallb https://metallb.github.io/metallb
helm repo update
helm install metallb metallb/metallb \
  --namespace metallb-system \
  --create-namespace \
  --set loadBalancerClass=metallb \
  --wait
```

Create the IP pool and L2 advertisement:

```bash
cat <<'EOF' | kubectl apply -f -
apiVersion: metallb.io/v1beta1
kind: IPAddressPool
metadata:
  name: envoy-gateway-pool
  namespace: metallb-system
spec:
  addresses:
    - 203.0.113.20/32                     # Your dedicated floating public IP
  autoAssign: false
---
apiVersion: metallb.io/v1beta1
kind: L2Advertisement
metadata:
  name: envoy-gateway-l2
  namespace: metallb-system
  labels:
    kube-dc.com/advertisement: envoy-gateway   # shared patch-target label (see BGP notes)
spec:
  ipAddressPools:
    - envoy-gateway-pool
  interfaces:
    - br-ext-cloud                        # Kube-OVN provider bridge
EOF
```

Update the Envoy Gateway service to use MetalLB:

```bash
cat <<'EOF' | kubectl replace -f -
apiVersion: gateway.envoyproxy.io/v1alpha1
kind: EnvoyProxy
metadata:
  name: custom-proxy-config
  namespace: envoy-gateway-system
spec:
  logging:
    level:
      default: warn
  provider:
    type: Kubernetes
    kubernetes:
      envoyService:
        externalTrafficPolicy: Cluster
        loadBalancerClass: metallb
        patch:
          type: StrategicMerge
          value:
            metadata:
              annotations:
                metallb.universe.tf/loadBalancerIPs: "203.0.113.20"
EOF
```

Delete the existing Envoy service so it is recreated with the new config (`loadBalancerClass` is immutable):

```bash
kubectl delete svc -n envoy-gateway-system -l gateway.envoyproxy.io/owning-gateway-name
```


</details>

#### Alternative: BGP announcement (routed / L3-only datacenters)

L2 advertisement (above, the default — `METALLB_MODE=l2`) announces the
VIP via ARP and therefore needs a **shared L2 segment** between the
announcing nodes and your router. If your datacenter is fully routed
(L3-to-the-host, no shared broadcast domain for the VIP), switch MetalLB
to **BGP mode** instead: each speaker node opens a BGP session to your
router and announces the VIP as a `/32` route (your router should ECMP
across the nodes). MetalLB ships FRR mode by default, so no extra
components are needed.

For GitOps-managed clusters, set `METALLB_MODE=bgp` plus the
`METALLB_BGP_*` keys in `cluster-config.env` and point the cluster's
`addons-config` at `addons/metallb-config-bgp` (instead of
`addons/metallb-config`) — the keys are validated by
`kube-dc bootstrap init`. The equivalent manual objects:

```bash
cat <<'EOF' | kubectl apply -f -
apiVersion: metallb.io/v1beta2
kind: BGPPeer
metadata:
  name: envoy-gateway-peer
  namespace: metallb-system
spec:
  myASN: 64512                          # your cluster's ASN (private range 64512-65534 is fine)
  peerASN: 64513                        # your router's ASN
  peerAddress: 192.0.2.1                # router IP reachable from every speaker node
  holdTime: "90s"
---
apiVersion: metallb.io/v1beta1
kind: BGPAdvertisement
metadata:
  name: envoy-gateway-bgp
  namespace: metallb-system
spec:
  ipAddressPools:
    - envoy-gateway-pool
  aggregationLength: 32                 # exact /32 host routes
EOF
```

Notes:
- **The two modes are exclusive per pool** — create either the
  `L2Advertisement` or the `BGPAdvertisement` for `envoy-gateway-pool`,
  never both.
- **Migrating an existing GitOps cluster l2 → bgp**: the addons-config
  Flux Kustomization typically runs `prune: false`, so swapping the
  referenced dir does **not** remove the old `L2Advertisement` — the VIP
  would be announced via ARP *and* BGP simultaneously. After the swap
  reconciles, delete it explicitly:
  `kubectl -n metallb-system delete l2advertisement envoy-gateway-l2`
  (or set `prune: true` on the addons-config Kustomization — it only
  ships MetalLB CRs, so pruning is safe there).
- **Per-cluster patches must target the shared label, not kind+name.**
  A kustomize patch targeting `kind: L2Advertisement, name:
  envoy-gateway-l2` is *silently ignored* after the base swap — a
  `nodeSelectors` restriction would vanish and **every** speaker node
  would start announcing. Both advertisement variants carry the label
  `kube-dc.com/advertisement=envoy-gateway` and the same-shaped
  `spec.nodeSelectors`; target that
  (`target: { labelSelector: "kube-dc.com/advertisement=envoy-gateway" }`)
  and the patch survives mode swaps unchanged.
- **Two orthogonal node-selector layers** (per MetalLB's advanced BGP
  docs): `BGPAdvertisement.spec.nodeSelectors` limits which nodes
  *announce* the route; `BGPPeer.spec.nodeSelectors` limits which nodes
  *establish a session* at all. On heterogeneous clusters restrict
  both — a node that can't reach the router shouldn't hold a flapping
  session.
- Your router must accept sessions from **every node selected by the
  `BGPPeer`** — by default that is every node running a MetalLB
  speaker. To restrict which nodes open sessions, use
  `BGPPeer.spec.nodeSelectors` (advertisement selectors do **not**
  limit sessions — see the next bullet).
- For session auth add `password:` (TCP-MD5) to the `BGPPeer`; for
  sub-second failover add a `BFDProfile` and reference it via
  `bfdProfile:`.
- **Scope**: BGP mode covers the platform ingress VIP(s) announced by
  MetalLB. Tenant floating IPs (EIp/FIp) are announced by Kube-OVN via
  ARP on the external network and still require the L2 segment described
  in the network prerequisites — BGP mode does not remove that
  requirement.

Verify:

```bash
# Check MetalLB speakers are running
kubectl get pods -n metallb-system

# Check the floating IP is assigned
kubectl get svc -n envoy-gateway-system -o wide

# Check speaker announcement
kubectl logs -n metallb-system -l app.kubernetes.io/component=speaker --tail=20 | grep serviceAnnounced
```

:::warning loadBalancerClass Isolation
The `loadBalancerClass: metallb` setting is **critical**. Without it, MetalLB will attempt to handle all `LoadBalancer` services in the cluster, conflicting with the Kube-DC LoadBalancer controller that manages project service IPs. This isolation ensures MetalLB only manages the Envoy Gateway floating IP.
:::

### 4.2 Configure Provider Network (Custom NIC Names)

If your nodes have **different NIC names** for the trunk interface, you need to patch the ProviderNetwork with custom interface mappings.

Create `provider-network-patch.yaml`:

```yaml
apiVersion: kubeovn.io/v1
kind: ProviderNetwork
metadata:
  name: ext-cloud
spec:
  defaultInterface: eth1                  # Default for most nodes
  customInterfaces:
    - interface: eno1                     # Override for nodes with different NIC names
      nodes:
        - master-3
  autoCreateVlanSubinterfaces: true       # Auto-create VLAN subinterfaces
  preserveVlanInterfaces: true            # Migrate existing VLAN configs to OVS
```

Apply:

```bash
kubectl apply -f provider-network-patch.yaml
```

Verify all nodes are ready:

```bash
kubectl get provider-networks ext-cloud -o jsonpath='{.status.readyNodes}' | jq .
```

### 4.3 Configure External Networks

The base installer creates the cloud network. If you need a **provider (public) network** for dedicated public IPs, create additional Kube-OVN resources.

Create `external-networks.yaml`:

```yaml
---
# Cloud network VLAN and Subnet (created by installer, shown for reference)
apiVersion: kubeovn.io/v1
kind: Vlan
metadata:
  name: vlan200
spec:
  id: 200                                # Your cloud VLAN ID
  provider: ext-cloud
---
apiVersion: kubeovn.io/v1
kind: Subnet
metadata:
  name: ext-cloud
  labels:
    network.kube-dc.com/external-network-type: cloud
    network.kube-dc.com/default-external: "true"
spec:
  protocol: IPv4
  cidrBlock: 10.64.0.0/16
  gateway: 10.64.0.1
  vlan: vlan200
  mtu: 1400
  gatewayType: distributed
  natOutgoing: false
  private: false
  enableLb: true
  excludeIps:
    - 10.64.0.1..10.64.0.100

---
# Provider (public) network — additional VLAN on same ProviderNetwork
apiVersion: kubeovn.io/v1
kind: Vlan
metadata:
  name: vlan300
spec:
  id: 300                                # Your provider VLAN ID
  provider: ext-cloud
---
apiVersion: kubeovn.io/v1
kind: Subnet
metadata:
  name: ext-public
  labels:
    network.kube-dc.com/external-network-type: public
spec:
  protocol: IPv4
  cidrBlock: 203.0.113.0/24              # Your public IPv4 subnet
  gateway: 203.0.113.1
  vlan: vlan300
  mtu: 1400
  gatewayType: distributed
  natOutgoing: false
  private: false
  enableLb: true
  excludeIps:
    - 203.0.113.1..203.0.113.3           # Reserved for infrastructure
    - 203.0.113.254
```

Apply the provider network (the cloud network is already created by the installer):

```bash
kubectl apply -f external-networks.yaml
```

Verify:

```bash
kubectl get vlan
kubectl get subnet ext-cloud ext-public
kubectl get provider-networks ext-cloud -o jsonpath='{.status.vlans}'
# Expected: ["vlan200","vlan300"]
```

### 4.4 Update DNS to MetalLB Floating IP

Now that MetalLB is running, update the wildcard DNS record (initially set in [§3.2](#32-configure-wildcard-dns-required-before-init)) to point to the **floating IP** instead of the single-node IP:

```
*.example.com  →  203.0.113.20  (A record, MetalLB floating IP)
```

This ensures high availability — if any node goes down, MetalLB migrates the IP to a healthy node and all services remain reachable.

---

## Phase 5 — Verify Installation

### Check All Components

```bash
# All nodes should be Ready
kubectl get nodes

# Core namespaces should have all pods Running
kubectl get pods -n kube-system          # Kube-OVN, CoreDNS
kubectl get pods -n kube-dc              # Kube-DC controllers
kubectl get pods -n keycloak             # Keycloak
kubectl get pods -n envoy-gateway-system # Envoy Gateway
kubectl get pods -n kubevirt             # KubeVirt
kubectl get pods -n monitoring           # Prometheus, Grafana, Loki
kubectl get pods -n kamaji-system        # Kamaji
```

### Access the Web Console

Open `https://console.example.com` in your browser.

Retrieve the demo organization admin password:

```bash
kubectl get secret realm-access -n demo-org -o jsonpath='{.data.password}' | base64 -d
```

Log in with:
- **Username:** `admin`
- **Password:** _(output from above)_

### Test External Connectivity

```bash
# Envoy Gateway should respond on the floating IP
curl -v https://console.example.com

# Check MetalLB floating IP
curl -v http://203.0.113.20
```

---

## Optional Add-ons

### Rook Ceph Object Storage (S3)

For S3-compatible object storage, see [Deploying Rook Ceph Object Storage](deploy-rook-ceph-object-storage.md).

### SSO with Google OAuth

To enable Google OAuth login, see [SSO with Google Auth](sso-google-auth.md).

### Worker Node Scaling with Metal3

Additional worker nodes can be added to the management cluster in two ways:

**Recommended** — use the CLI join from [§2.3](#23-add-worker-nodes-with-kube-dc-bootstrap-install---join-server):
`kube-dc bootstrap install worker-1 --ssh-host root@<worker-ip> --join-server root@<cp-ip>`.

**Manual addition** (no CLI) — install the RKE2 agent by hand:

```bash
# On the new worker node
sudo mkdir -p /etc/rancher/rke2/
cat <<EOF | sudo tee /etc/rancher/rke2/config.yaml
token: <TOKEN_FROM_MASTER_1>
server: https://192.168.0.1:9345
node-name: worker-1
node-ip: 192.168.0.11
EOF

export INSTALL_RKE2_VERSION="v1.35.0+rke2r1"
export INSTALL_RKE2_TYPE="agent"
curl -sfL https://get.rke2.io | sh -
sudo systemctl enable rke2-agent.service
sudo systemctl start rke2-agent.service
```

**Automated provisioning with Metal3** — Metal3 uses the Cluster API bare-metal provider to PXE-boot and provision new servers automatically. This is ideal for large-scale deployments where servers are managed via IPMI/BMC. Metal3 handles:

- Hardware discovery and inventory via Ironic
- PXE boot and OS provisioning
- Automatic Kubernetes node joining
- Lifecycle management (scale up/down, OS upgrades)

For the complete guide, see [Metal3 Bare-Metal Worker Nodes](deploy-metal3-bare-metal-workers.md).

---

## Troubleshooting

### RKE2 Nodes Not Joining

```bash
# Check RKE2 logs on the joining node
sudo journalctl -u rke2-server -f   # For server nodes
sudo journalctl -u rke2-agent -f    # For worker nodes

# Verify connectivity to master-1
ping 192.168.0.1
curl -k https://192.168.0.1:9345/v1-rke2/readyz
```

### Kube-OVN Pods Not Starting

```bash
kubectl get pods -n kube-system -l app=kube-ovn-controller
kubectl logs -n kube-system -l app=kube-ovn-controller --tail=50
```

Common issue: Nodes have different NIC names. Apply a ProviderNetwork patch (see [Phase 4.2](#42-configure-provider-network-custom-nic-names)).

### MetalLB Not Announcing IP

```bash
kubectl get pods -n metallb-system
kubectl logs -n metallb-system -l app.kubernetes.io/component=speaker --tail=50

# Ensure loadBalancerClass is set correctly
kubectl get svc -n envoy-gateway-system -o yaml | grep loadBalancerClass
```

### Envoy Gateway Not Responding

```bash
kubectl get svc -n envoy-gateway-system
kubectl get gateway -A
kubectl logs -n envoy-gateway-system -l control-plane=envoy-gateway --tail=50
```

### `kube-dc bootstrap init` Fails

```bash
# Review the plan without mutating anything
kube-dc bootstrap init <same flags> --dry-run

# Re-check local tooling + the target cluster
kube-dc bootstrap doctor --no-tty
kube-dc bootstrap status <cluster> --repo <fleet-repo>
```

`init` is idempotent and rolls back its own commit if the push fails —
fix the reported cause and re-run. Common ones: `KUBE_OVN_MASTER_NODES`
unset (pass the control-plane **internal** IPs via `--set`), the wildcard
DNS record not yet resolving (the DNS gate blocks; re-run once
`dig +short test.<domain>` returns your IP, or pass
`--allow-dns-not-ready` to proceed without TLS), or a missing
`delete_repo`/`repo` scope on the `gh` token for `new-repo` mode.

### Flux Not Reconciling

```bash
flux get kustomizations                 # which layer is stuck?
flux get helmreleases -A                # which HelmRelease failed?
kubectl -n flux-system logs deploy/kustomize-controller --tail=50
# reset an exhausted HelmRelease's retries:
kubectl -n <ns> patch hr <name> --type=merge -p '{"spec":{"suspend":true}}'
kubectl -n <ns> patch hr <name> --type=merge -p '{"spec":{"suspend":false}}'
```

For additional help, consult the [Community & Support](/cloud/community-support) page.
