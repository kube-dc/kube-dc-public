# Installation Guide

:::info Which installation page do I need?
Three pages, three jobs — read them in this order only if you need all three.

| Page | Answers | Read it when |
|---|---|---|
| **[Quickstart](quickstart.md)** | *What do I type?* | You are installing for the first time. One linear path, one topology, one config file, a checkpoint per phase. |
| **[Installation Overview](installation-overview.md)** | *What am I choosing, and what will it change?* | Before the quickstart, to pick a topology and understand the network model and what Flux installs. Concepts, not copy-paste. |
| **[Installation Guide](installation-guide.md)** | *What are all the options, and what if it goes wrong?* | For alternative topologies, the manual RKE2 fallback, per-flag reference, day-2 migrations, and recovery. |

All three describe the **same installer**. Where they overlap, the Quickstart is
the tested happy path — it is verified against the real command tree on every
build, so its commands and config keys cannot drift.
:::

This guide walks you through deploying Kube-DC on three bare-metal servers from scratch. By the end, you will have a fully operational cloud platform with HA control plane, virtual machine support, Managed Clusters, and public IP networking.

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

:::note The CLI installer never rewrites your resolver by default
The steps above are the **manual** fallback. When you install with `kube-dc
bootstrap init`, the RKE2 bootstrap probes DNS with `getent hosts get.rke2.io`
and, if it fails, **stops with an error rather than silently overwriting
`/etc/resolv.conf`** — rewriting a corporate or air-gapped resolver to public
DNS would break internal names and leak queries. Fix DNS (or point RKE2 at a
local mirror) and re-run. To force the public-DNS fallback anyway on a node you
know is internet-facing, set `RKE2_DNS_PUBLIC_FALLBACK=true` (it saves the
previous resolver to `/etc/resolv.conf.pre-kube-dc`). Do **not** set it on a
corporate/air-gapped network.
:::

:::danger A wildcard DNS record + a search domain breaks ALL external name resolution from pods
If your platform domain has a **wildcard A record** (`*.example.com`, which
§3.2 asks you to create) then the nodes' `search` list must **not** contain
that domain.

kubelet copies the node's `search` domains into every pod's
`/etc/resolv.conf`, and Kubernetes uses `ndots:5` — so a short name like
`github.com` (1 dot) tries the **search suffixes first**:

```
github.com.example.com   →  matches *.example.com  →  your ingress IP  ✗
github.com               →  140.82.121.4                              ✓
```

Every external hostname — `github.com`, `ghcr.io`, `docker.io` — then
resolves to your ingress IP from inside pods. Flux fails to clone with
`dial tcp <your-ingress-ip>:443: connect: connection refused`, which looks
like a Flux or firewall problem and is neither.

Use a resolver that can answer both your internal names and the internet,
and keep only a parent domain in `search` (a parent normally has no
wildcard, so it NXDOMAINs safely and the resolver falls through to the
absolute name):

```bash
sudo rm -f /etc/resolv.conf
cat <<'EOF' | sudo tee /etc/resolv.conf
nameserver 10.0.0.53           # a resolver that serves BOTH internal + external
search inf.example.com         # parent only — NOT the platform domain
options edns0 trust-ad
EOF
sudo chattr +i /etc/resolv.conf   # keep DHCP/netplan from rewriting it
```

Verify before installing — the first must NXDOMAIN, the second must return
the real address:

```bash
dig +short github.com.example.com   # empty  → good
dig +short github.com               # 140.x  → good
```

If you hit this after install, fix the nodes then restart CoreDNS **and any
pod that needs the internet** — search domains are baked into a pod's
`resolv.conf` when it is created.
:::

### 1.5 Install the `kube-dc` CLI

Install the CLI on your **bastion / workstation** before Phase 2; both the RKE2
bootstrap and the GitOps scaffold use it:

```bash
# Linux amd64 — change asset for another platform
KUBE_DC_INSTALL_VERSION=vX.Y.Z   # the approved immutable release (latest: github.com/kube-dc/kube-dc-public/releases)
asset=kube-dc_linux_amd64
task_cli_tmp="$(mktemp -d)"
curl -fSL https://github.com/kube-dc/kube-dc-public/releases/download/${KUBE_DC_INSTALL_VERSION}/${asset} \
  -o "${task_cli_tmp}/${asset}"
curl -fSL https://github.com/kube-dc/kube-dc-public/releases/download/${KUBE_DC_INSTALL_VERSION}/checksums.txt \
  -o "${task_cli_tmp}/checksums.txt"
if command -v sha256sum >/dev/null; then
  ( cd "${task_cli_tmp}" && grep " ${asset}$" checksums.txt | sha256sum -c - )
else
  ( cd "${task_cli_tmp}" && grep " ${asset}$" checksums.txt | shasum -a 256 -c - )
fi
sudo install -m 0755 "${task_cli_tmp}/${asset}" /usr/local/bin/kube-dc
rm -rf "${task_cli_tmp}"
hash -r
type -a kube-dc                 # expose an older PATH shadow, if one exists
kube-dc version
# macOS: set asset=kube-dc_darwin_amd64 or kube-dc_darwin_arm64
kube-dc bootstrap doctor --no-tty
```

The download pins one reviewed release (never the mutable `latest` alias) and
verifies it against that release's published checksum before installation.
`doctor` checks `kubectl`, `flux`,
`sops`, `age`, `git`, `gh`, and `ssh` (it does not probe `helm`, `kustomize`
or `yq` — install those by hand); fix every blocker before
continuing. Ensure the control-plane key is loaded with `ssh-add <key>`.

---

## Phase 2 — RKE2 Cluster Bootstrap

The fastest path is
[`kube-dc bootstrap install` in §2.0](#20-one-command-kube-dc-bootstrap-install-recommended),
which writes the canonical RKE2 config and installs RKE2 for you over SSH. If
you’d rather do it by hand — or need to understand exactly what that command
produces — the manual reference is grouped in
[§2.3](#23-manual-fallback-install-rke2-without-the-cli).

### 2.0 One command: `kube-dc bootstrap install` (recommended)

From your bastion (after installing the CLI — see [§1.5](#15-install-the-kube-dc-cli)):

```bash
RKE2_VERSION=v1.36.3+rke2r1  # pin the same reviewed version on EVERY node
kube-dc bootstrap install master-1 \
  --ssh-host root@203.0.113.10 \
  --domain example.com \
  --preset cloud+public-vlan \
  --rke2-version "$RKE2_VERSION" \
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

The current CLI also enables the **RKE2 embedded registry mirror (spegel)**
on every node by default: nodes P2P-share image content, so repeated
containerdisk/image pulls stay off the WAN. `--embedded-registry=false` opts
out. An existing operator-managed `/etc/rancher/rke2/registries.yaml` is never
overwritten, but default-on install refuses to restart RKE2 when that file has
no non-empty `mirrors:` mapping. Pair it with the image-acceleration stack that
`bootstrap init` scaffolds by default
(Managed Cluster add-ons, zot registry depot, CDI OS-image mirror —
`--image-acceleration=false` opts out; see the
[enterprise install guide](private-ca-enterprise-install.md) §6).

> ⚠️ When retrofitting spegel onto an **existing** cluster, restart
> `rke2-server`/`rke2-agent` one node at a time and **drain or stop KubeVirt
> VMs on the node first** — restarting under running VMs can wedge the node
> (see the enterprise guide §1 for the failure signature and recovery).

Key flags: `--name` (RKE2 node-name; defaults to the positional arg — use the
same name in `init`), `--node-ip` / `--external-ip` (override auto-detection),
`--force` (re-run on an already-installed node — restarts to apply config
changes, but refuses while KubeVirt/QEMU workloads are resident),
`--set POD_CIDR=…` (override a preset CIDR). Requires passwordless sudo (or a root login) on the node.

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

Continue in topology order:

- For an HA control plane,
  [add master-2 and master-3 in §2.1](#21-add-master-2-and-master-3-with-kube-dc-bootstrap-install---role-server).
- Add workload capacity with
  [worker nodes in §2.2](#22-add-worker-nodes-with-kube-dc-bootstrap-install---join-server).
- For a no-CLI installation, use the
  [manual reference in §2.3](#23-manual-fallback-install-rke2-without-the-cli).
- For a single-node cluster, or after all planned nodes have joined,
  [verify the cluster in §2.4](#24-verify-the-ha-cluster), then continue to
  [Phase 3](#phase-3--deploy-kube-dc-with-the-kube-dc-cli).

### 2.1 Add master-2 and master-3 with `kube-dc bootstrap install --role server`

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
  --domain kube.example.com \
  --preset cloud+public-vlan \
  --rke2-version "$RKE2_VERSION" \
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

### 2.2 Add worker nodes with `kube-dc bootstrap install --join-server`

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
  --rke2-version "$RKE2_VERSION" \
  --dry-run
```

No `--domain`/`--preset` needed — a worker inherits cluster config from
the server it joins. The agent dials the control-plane's **internal** IP
(auto-detected, never a NAT/floating IP). The worker registers and shows
up in `kubectl get nodes` (NotReady until kube-ovn schedules onto it). If
you already have the token, pass `--join-token` + `--cp-host` to skip the
control-plane SSH. To reach the worker (and the `--join-server`
control-plane) through a bastion, add `--ssh-jump user@bastion` — see the
[reachability note in §2.0](#20-one-command-kube-dc-bootstrap-install-recommended).

> This flow is validated end-to-end (a worker VM joining a live cluster).

### 2.3 Manual fallback: install RKE2 without the CLI

<details>
<summary>Manual fallback (no CLI) — write the RKE2 server and join configs by hand</summary>

The CLI flow in §§2.0–2.2 is recommended. Expand this only when the CLI cannot
be used or when auditing the exact RKE2 files it generates.

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

cat <<EOF | sudo tee /etc/rancher/rke2/registries.yaml
mirrors:
  "*":
EOF

cat <<EOF | sudo tee /etc/rancher/rke2/config.yaml
node-name: master-1
disable-cloud-controller: true
disable: rke2-ingress-nginx              # Replaced by Envoy Gateway
cni: none                                # Replaced by Kube-OVN
embedded-registry: true
supervisor-metrics: true
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
export INSTALL_RKE2_VERSION="v1.36.3+rke2r1"
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
# master-1   NotReady   control-plane,etcd,master   1m    v1.36.3+rke2r1
```

The node will show `NotReady` until a CNI is installed — this is expected.

#### 2.3.1 Get the join token for manual joins

On `master-1`, retrieve the join token:

```bash
sudo cat /var/lib/rancher/rke2/server/node-token
```

Save this token — you need it for `master-2` and `master-3`.

#### 2.3.2 Join master-2 and master-3 manually

On **each additional node** (`master-2`, `master-3`), create the RKE2 config and join:

```bash
sudo mkdir -p /etc/rancher/rke2/

cat <<EOF | sudo tee /etc/rancher/rke2/registries.yaml
mirrors:
  "*":
EOF

cat <<EOF | sudo tee /etc/rancher/rke2/config.yaml
token: <TOKEN_FROM_MASTER_1>
server: https://192.168.0.1:9345         # master-1 management IP
node-name: master-2                      # Use master-3 on the third node
disable-cloud-controller: true
disable: rke2-ingress-nginx
cni: none
embedded-registry: true
supervisor-metrics: true
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
export INSTALL_RKE2_VERSION="v1.36.3+rke2r1"
export INSTALL_RKE2_TYPE="server"
curl -sfL https://get.rke2.io | sh -
sudo systemctl enable rke2-server.service
sudo systemctl start rke2-server.service
```

#### 2.3.3 Join worker nodes manually

Use the token from [§2.3.1](#231-get-the-join-token-for-manual-joins) and the
control-plane internal IP:

```bash
# On the new worker node
sudo mkdir -p /etc/rancher/rke2/
cat <<EOF | sudo tee /etc/rancher/rke2/registries.yaml
mirrors:
  "*":
EOF

cat <<EOF | sudo tee /etc/rancher/rke2/config.yaml
token: <TOKEN_FROM_MASTER_1>
server: https://192.168.0.1:9345
node-name: worker-1
node-ip: 192.168.0.11
EOF

export INSTALL_RKE2_VERSION="v1.36.3+rke2r1"
export INSTALL_RKE2_TYPE="agent"
curl -sfL https://get.rke2.io | sh -
sudo systemctl enable rke2-agent.service
sudo systemctl start rke2-agent.service
```

</details>

### 2.4 Verify the HA Cluster

Back on `master-1`:

```bash
kubectl get nodes
# NAME       STATUS     ROLES                       AGE   VERSION
# master-1   NotReady   control-plane,etcd,master   10m   v1.36.3+rke2r1
# master-2   NotReady   control-plane,etcd,master   3m    v1.36.3+rke2r1
# master-3   NotReady   control-plane,etcd,master   1m    v1.36.3+rke2r1
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

### 3.1 Confirm the CLI and local tooling

The CLI was installed on the bastion/workstation in
[§1.5](#15-install-the-kube-dc-cli). Reconfirm the executable and its local
prerequisites before scaffolding GitOps state:

```bash
type -a kube-dc                 # expose an older PATH shadow, if one exists
kube-dc version
kube-dc bootstrap doctor --no-tty
```

`doctor` checks `kubectl`, `flux`, `sops`, `age`, `git`, `gh`, `ssh` and `bao`
(the last informationally — the CLI runs `bao` inside the cluster, never here).
It does NOT probe `helm`, `kustomize` or `yq`, so a green doctor is not proof
those exist. Fix every blocker before continuing. Ensure the control-plane SSH key is
loaded (`ssh-add <key>`); the CLI reads it from `ssh-agent`, never from a key
flag.

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

:::tip Private ingress IP (internal DC, VPN-only)?
The wildcard can point at a **private** address — a public zone with private
A records is fine. But Let's Encrypt then cannot reach `:80` for HTTP-01, so
pass `--tls-mode acme-dns01-route53` (zone in Route53 — certificates still
auto-renew) or `--tls-mode byo-wildcard`.
See [Platform TLS certificates](certificates.md).
:::

:::tip Behind a 1:1 NAT / floating IP?
On clouds where the node never sees its own public IP locally (a
kube-dc FIP, an EC2 elastic IP, an OpenStack/Hetzner floating IP), pass
`--ssh-host`. The CLI SSH-probes the node and writes the **arriving
(internal) IP** into the fleet as `NODE_EXTERNAL_IP` / `KUBE_API_ARRIVAL_IP`
— kube-proxy matches externalIP rules against the address packets *actually*
carry, so configuring the public IP there would make `:80/:443/:6443`
silently reset. Bare metal with the public IP bound on the NIC needs none of
this.

**Tenants and the management API on this topology.** The apiserver is served
off-Envoy on the arrival IP (see the kube-api tip in §4), but on a
private/single-ingress cluster a tenant VPC pod cannot route to it anyway (it is
OVN-isolated from the node and service networks). `MANAGEMENT_API_MODE` defaults
to `auto`, and the installer resolves it to **`service`** here: in-tenant
platform controllers (managed-K8s CCM/CSI, CloudNativePG, cloud shell,
autoscaler) reach the apiserver's in-cluster ClusterIP over the dual-home infra
NIC. Do **not** force `external` on such a cluster — it has no tenant-reachable
API endpoint, and every in-tenant controller silently loses its route.
:::

:::warning Internal domain served by a private CA
A corporate/private CA is not one switch: the install host, manager, backend,
Kubernetes OIDC authenticator, and OpenBao discovery each verify TLS in a
separate process. Configure all of them from the same full root+intermediate
PEM bundle.

First trust it on the machine running `bootstrap init` and verify discovery:

```bash
sudo cp corp-ca.pem /usr/local/share/ca-certificates/kube-dc-corp-ca.crt
sudo update-ca-certificates
curl -fsS https://login.<domain>/realms/master/.well-known/openid-configuration >/dev/null
```

Pass the same CA-only bundle to `bootstrap init`:

```bash
kube-dc bootstrap init ... \
  --trusted-ca-bundle=corp-ca.pem
```

The CLI rejects private-key and non-CA PEM blocks, plan-pins the canonical
bundle fingerprint, writes `clusters/<name>/trusted-ca.yaml`, and sets both
`MANAGER_TRUSTED_CA_CONFIGMAP` and `BACKEND_TRUSTED_CA_CONFIGMAP`. This is
the durable GitOps source; do not patch a live ConfigMap by hand.

The manager mount sets `SSL_CERT_DIR`. The manager also copies the validated
certificate bundle into each `OpenIDConnect.spec.caBundle` and forwards it to
OpenBao as `oidc_discovery_ca_pem`; a configured missing, unreadable, or
malformed bundle now fails reconciliation explicitly instead of silently
shipping OIDC resources that reject every token with 401. The backend mount
sets `NODE_EXTRA_CA_CERTS`, including the cloud-shell token-refresh path.

Greenfield installs enable the chart-owned admin frontend. The deferred Keycloak
finalizer creates a SOPS-encrypted Secret containing both `client-id` and
`client-secret`, adds it to the generated `addons` Kustomization, and only then
sets `backend.keycloakAdminClient.secretName`. This ordering avoids blocking the
core backend on a Secret that cannot exist until Keycloak is Ready; no manual
`extraEnv` or postRenderer is required. Populate the Gateway TLS secrets with the
corporate wildcard certificate instead of waiting for public ACME validation.
See [Private-CA enterprise installation](private-ca-enterprise-install.md) for
the consumer-by-consumer verification matrix and recovery checks.
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
  --ceph-node=master-1=nvme1n1 \
  --ceph-node=master-2=nvme1n1 \
  --ceph-node=master-3=nvme1n1 \
  --ssh-host=root@203.0.113.10 \
  --set=EXT_NET_INTERFACE=eth1 \
  --set=EXT_NET_VLAN_ID=200 \
  --set=KUBE_OVN_MASTER_NODES=192.168.0.1,192.168.0.2,192.168.0.3 \
  --set=KUBE_OVN_GW_NODES=master-1,master-2,master-3 \
  --node-nic=master-3=eno1 \
  --set=EXT_PUBLIC_VLAN_ID=300 \
  --set=EXT_PUBLIC_CIDR=203.0.113.0/24 \
  --set=EXT_PUBLIC_GATEWAY=203.0.113.1 \
  --ingress-address-layer=metallb-l2 \
  --set=METALLB_FLOATING_IP=203.0.113.20 \
  --set=METALLB_INTERFACE=ext-pub-anchor \
  --openbao-shares-out=$HOME/dc1-openbao-shares.yaml \
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
| `--starter-ref` | Immutable full OCI starter reference. Released CLIs default to their own version; pin it explicitly in controlled/reinstall procedures and never use `:latest` |
| `--github-owner` / `--github-repo` | Where the fleet repo lives (auto-created in `new-repo` mode) |
| `--object-storage-mode` | **REQUIRED.** Working: `rook-ceph-local` (loop-file backed, single node), `rook-ceph-multi-node` (raw devices, exactly 3 nodes), `rook-ceph-pvc`. `disabled` installs a **deliberately degraded** cluster: Mimir and Loki are SUSPENDED (no metrics or logs storage), Grafana's database runs with backups and WAL archiving OFF, and alloy log-delivery errors are expected — never for a customer-facing cluster. `external-ceph` and `external-s3` are **recognised but fail closed** (fleet stubs); do not select them |
| `--ceph-node=NODE=DEVICE` | One raw block device per OSD node (repeat 3× for multi-node). Device is the **bare name** as `lsblk` shows it (`nvme1n1`, `sdb`) — a `/dev/` prefix is stripped automatically since v0.5.13. When a raw device is named (not the loop-file default) `init` probes it over SSH and **warns** if it is missing or already carries data — see the zap warning below. **Re-used hardware: see the zap warning below** |
| `--rook-osd-device` | `rook-ceph-local` only: OSD block device (bare name). Default = the fleet template's loop file (`loop0`, sized by `--rook-osd-size-gb`) — pass a real device for anything beyond a lab; when set, `init` probes it over SSH and warns if missing or non-empty |
| `--ceph-storage-class` / `--ceph-osd-count` / `--ceph-osd-volume-size-gb` | `rook-ceph-pvc` only: the StorageClass backing the OSD PVCs (required), OSD PVC count (0 = fleet default 2) and size in GB (0 = fleet default 200). For clusters that already have a CSI-backed StorageClass and no raw disks |
| `--no-kubevirt` | VMs are out of scope for this cluster (e.g. a CloudSigma `cs` cluster that only runs managed Kubernetes) — skips the KubeVirt-eligibility (`/dev/kvm`) preflight so the install does not block on nodes with no nested virtualization. **Leave it off for any cluster that will host tenant VMs.** Distinct from `--allow-no-kubevirt-eligible`, which keeps the VM feature but bypasses the *eligibility gate* on a single non-KVM node |
| `--ssh-host` | Control-plane SSH target — enables kubeconfig auto-pull **and** NAT-topology detection (§3.2) |
| `--set=KUBE_OVN_MASTER_NODES` | Control-plane **internal** IPs (comma-separated) — not emitted by the preset, always set it |
| `--set=KUBE_OVN_GW_NODES` | Gateway/announcer **node names**. Required when an L2 VIP is inside `EXT_PUBLIC_CIDR`; the CLI derives one public anchor per listed node |
| `--set=EXT_NET_INTERFACE` / `EXT_NET_VLAN_ID` | Trunk NIC + cloud VLAN ID from Phase 1 (`EXT_NET_VLAN_ID=0` = untagged carrier) |
| `--node-nic=NODE=IFACE` | Per-node override when a node's provider/trunk NIC differs from `EXT_NET_INTERFACE` (repeatable; also exposed in TUI/config) |
| `--set=EXT_PUBLIC_*` | Public VLAN/CIDR/gateway for `cloud+public-vlan`. When the VIP is in this CIDR, the CLI derives the minimum gateway/VIP/anchor exclusions; widen them for any other reserved addresses |
| `--set=EXT_PUBLIC_EXCLUDE_IPS_1` / `_2` | **Required for `cloud+public-vlan`** — the two IPAM exclusion ranges (`a.b.c.d..a.b.c.e`) that reserve gateway/VIP/anchors out of the public pool. Derived automatically **only** when the MetalLB VIP sits inside `EXT_PUBLIC_CIDR`; on `--ingress-address-layer=none`, or a VIP on the cloud VLAN, you must set both yourself or `init` refuses with `missing EXT_PUBLIC_EXCLUDE_IPS_1, EXT_PUBLIC_EXCLUDE_IPS_2` |
| `--set=METALLB_FLOATING_IP` / `METALLB_INTERFACE` | Dedicated ingress VIP and the host interface that carries its L2 segment. For an L2 VIP inside `EXT_PUBLIC_CIDR`, the current CLI selects the fleet-managed `ext-pub-anchor`; elsewhere the operator supplies the real interface |
| `--ingress-address-layer` | **Who owns the address your users dial** — the one front-door question. `metallb-l2` (recommended) — a floating VIP announced by ARP on a shared L2 segment; `metallb-bgp` — the same VIP announced as a `/32` to a routed fabric; `none` — clients reach the ingress nodes' own IPs via wildcard DNS and MetalLB is not installed at all. Declaring a `METALLB_FLOATING_IP` **does not** select a layer for you: a reserved address is not assumed to be your front door, so declaring one without a layer is refused rather than guessed. Left unset this resolves to `none`. The **data plane does not vary with this choice** — host-bind Envoy on the ingress nodes either way — but the *Service shape* does: `ClusterIP` + `externalIPs` on `none`, `LoadBalancer` + `metallb` + `externalTrafficPolicy: Local` with `externalIPs` cleared on a MetalLB layer. See "Choosing an address layer" below |
| `--ingress-node` | Node that should carry the `kube-dc.com/ingress` label and bind `:80`/`:443` (repeatable). `init` **applies the label** to this set before committing the overlay, and the front-door component places Envoy on it. Leave it unset and the `KUBE_OVN_GW_NODES` set is used, which is the recommended shape because it keeps the ingress set and the MetalLB announcer set identical. `ENVOY_REPLICAS` and `INGRESS_HOST_CIDR` are **derived** from this set. Under a VIP layer the set must be a SUBSET of `KUBE_OVN_GW_NODES` — a partial overlap is refused, because the single node in both sets becomes a point of failure that looks like HA |
| `--set=INGRESS_MODE` | **Deprecated** — kept for one release so older config files round-trip. `metallb-lb` and `hostnetwork` both now select the same universal host-bind data plane; use `--ingress-address-layer` instead |
| `--set=METALLB_MODE` | **Read-only legacy output — do not set it.** It is DERIVED from `--ingress-address-layer`, and setting it against the layer is refused (the addon tree that gets wired is chosen from the layer). For BGP pass `--ingress-address-layer=metallb-bgp`, which additionally requires `METALLB_BGP_LOCAL_ASN`, `METALLB_BGP_PEER_ASN` and `METALLB_BGP_PEER_ADDRESS` (all validated). See §4.3 |
| `--tls-mode` | `acme` (default — HTTP-01 through the Gateway; needs inbound `:80`), `acme-dns01-route53` (same issuer, proves control via Route53 DNS records — for private/VPN-only clusters whose zone is in Route53; auto-renews; requires `--dns01-route53-zone-id` + `--dns01-route53-access-key-id`, secret key via `--dns01-route53-secret-key-file` or `KUBE_DC_DNS01_ROUTE53_SECRET_KEY`), or `byo-wildcard` (operator-supplied certificate; requires `--tls-cert`/`--tls-key`; nothing renews it). See [Platform TLS certificates](certificates.md) |
| `--trusted-ca-bundle` | Certificate-only root/intermediate PEM for a private-CA platform. The CLI creates the durable ConfigMap and wires manager, backend, OIDC and OpenBao from one plan-pinned source |
| `--openbao-shares-out` | Additional off-git `0600` custody copy of the five Shamir shares. The automatic post-apply finalizer honors this path; never place it inside a Git tree |

**Less common flags** (all `kube-dc bootstrap init --help` for the full list):

| Flag | Meaning |
|------|---------|
| `--dry-run` / `--print-plan` · `--yes` · `--no-tty` | Plan only (writes a consent marker) · apply without prompting · plain stdout for CI/agents. The documented loop is `--dry-run` → read the plan → same command with `--yes` |
| `--plan-file` / `--apply-plan` | Save the dry-run plan as JSON, then apply *exactly that reviewed plan* (`--apply-plan plan.json`); the apply refuses on any input drift (hash-pinned) or plan-schema mismatch (a plan from an older CLI must be re-dry-run) |
| `--config <file>` / `--save-config <file>` | Prefill every input from a `cluster-config.env`-format spec (config keys + `KUBE_DC_INIT_*` orchestration keys — see "Prefill from a file" below) / write the resolved inputs back out as one (never the git token) |
| `--vm-storage-mode` / `--vm-golden` | VM root-disk storage: `local` (default) or `shared-rbd` (needs a rook-ceph-* object-storage mode; adds the rbd-vm layers + FS golden images — `--vm-golden debian-12,alpine-3.21`, Windows opt-in) |
| `--s3-hostname` / `--no-s3-exposure` | S3 endpoint hostname for the exposure layer (default `s3.<domain>`) / keep S3 cluster-internal (no Certificate + HTTPRoute) |
| `--image-acceleration` (default true) | Wire the on-cluster image path — `cdi-os-mirror` (OS images), `registry-depot` (zot) + spegel P2P — see [restricted-egress-operation](restricted-egress-operation.md) |
| `--mirror-registry` / `--bundle-pull-secret` | Air-gap: pull platform images through your mirror registry, with a Docker pull-secret JSON |
| `--provider github` / `gitlab` | Remote hosting for `--fleet-mode=new-repo` (default github) |
| `--no-push` · `--no-ssh` · `--no-create-repo` · `--no-install-prereqs` | Commit locally without pushing · skip the SSH kubeconfig pull + node probes · skip remote-repo creation · skip prerequisite install |
| `--no-kubevirt` · `--allow-no-kubevirt-eligible` · `--allow-dns-not-ready` · `--allow-unpinned-adopt` | The gates: VMs out of scope (skip the KVM preflight) · keep VMs but bypass the eligibility gate on a non-KVM node · proceed with the wildcard record not yet resolving (ACME certs sit Pending until it does) · `adopt` without pinning live versions (RISKY) |
| `--gpu-*`, `--hami-*`, `--nvidia-*` | Accelerator products — see [gpu-node-mode-transitions](gpu-node-mode-transitions.md) and the GPU docs |

:::note `.starter-version` and `.starter-manifest` are vendor-managed — leave them alone
After install, the fleet repo root carries two files the platform maintains:
`.starter-version` (which fleet-starter release this repo came from, including
the immutable artifact digest when resolved) and `.starter-manifest` (a
checksum baseline of the shared `bootstrap/ infrastructure/ platform/ addons/
scripts/` trees). They are how a future `kube-dc` upgrade distinguishes
vendor-clean files from your local changes. Don't edit or delete them — and
put your own customization in `clusters/<name>/` (config keys + patches), not
in the shared trees, so upgrades stay clean.
:::

`EXT_NET_ANCHOR_INTERFACE` names the parent OVS bridge even when `EXT_NET_ANCHOR_IPS` is empty; public L2 mode creates `EXT_NET_PUBLIC_ANCHOR_INTERFACE` on that bridge. Do not clear the bridge key merely because ext-cloud anchor addresses or node-egress NAT are disabled.

`EXT_NET_NODE_EGRESS_ENABLED` is deliberately not part of the normal install
recipe. It is a default-off escape hatch for a site whose physical cloud-VLAN
gateway cannot forward tenant traffic correctly. For a reviewed new-install
spec, opt in with `KUBE_DC_INIT_NODE_EGRESS_ENABLED=true`; init writes the live
`EXT_NET_NODE_EGRESS_ENABLED=true` value, but clone-from-sibling intentionally
drops that live key. Enabling it turns gateway nodes into internet NAT routers,
requires `EXT_NET_ANCHOR_REQUIRED=true` plus complete audited ext-cloud
anchors, and changes the security/failure-domain model. Fix the upstream router
instead whenever possible.

:::danger Re-used disks: `lsblk`/`blkid` looking clean is NOT enough
If the OSD devices ever belonged to another Ceph cluster, wiping them with
`wipefs` / `sgdisk --zap-all` / `dd` of the first few hundred MB makes
`lsblk` and `blkid` report them as empty — while **Ceph still refuses
them**. `osd-prepare` completes without creating a single OSD and logs:

```
--> Raw device /dev/sdb is already prepared.
skipping osd.4: "<uuid>" belonging to a different ceph cluster "<old-fsid>"
skipping OSD configuration as no devices matched the storage settings
```

Ceph Squid (v19) documents **redundant bluestore labels** at 0, 1 GiB,
10 GiB, 100 GiB, and 1 TiB. Do not infer that this list is exhaustive or
hand-zero it: even after a large window at every documented offset was zeroed,
`ceph-bluestore-tool show-label` still returned the original valid label. The
cause was not pinned down (a missed copy, stale read path, or another offset),
so use Ceph's own tool, which owns the on-disk format:

```bash
# privileged pod on the node, hostPath /dev + /run/udev, ceph image
ceph-volume lvm zap --destroy /dev/sdb     # works on RAW devices too
                                           # (`ceph-volume zap` is NOT a subcommand)
```

Verify with the tools Ceph itself uses — **not** `lsblk`/`blkid`:

```bash
ceph-volume raw list                      # must not list the device
ceph-bluestore-tool show-label --dev /dev/sdb   # must say "unable to read label"
```

Both commands must execute successfully enough to prove their result. Parse
`ceph-volume raw list` as JSON and require that the exact device is absent; a
command or JSON error is **not** an empty inventory. For `show-label`, success
means the device is still owned, and only the clean-device "unable to read
label ... (2) No such file or directory" result is acceptance. Treat every
other error as a hard stop. On a clean reinstall, run this paired gate after
nodes join but **before** Flux/Rook is allowed to reconcile.

Then `kubectl -n rook-ceph delete job -l app=rook-ceph-osd-prepare` and
`kubectl -n rook-ceph rollout restart deploy/rook-ceph-operator`; the OSDs
appear immediately.
:::

What `init` does, in order: **fetches the fleet starter** (when `--repo`
is a fresh/empty directory, the shared platform trees — `bootstrap/`,
`infrastructure/`, `platform/`, `addons/` — are pulled from the
versioned OCI artifact `oci://ghcr.io/kube-dc/fleet-starter:<cli-version>`
and committed; a directory that already carries them is used as-is and is **not upgraded**; converge an old fleet to the matching starter before init;
override with `--starter-ref`) → generates a SOPS **age key** → creates +
pushes the fleet repo → scaffolds `clusters/dc1/` (cloud/public networks,
per-node ProviderNetwork NIC mappings, ordered MetalLB operator/config,
object storage, and encrypted secrets) → `flux bootstrap` → pre-installs the
CNI/CRD-bearing charts so a bare cluster can reconcile → hands off to Flux.
It is idempotent and rolls back its own commit if the push fails.
You do **not** need to clone or download anything besides the CLI —
point `--repo` at an empty directory.

:::info Single-node / lab install
For a one-box trial, use `--preset=internal-only --object-storage-mode=rook-ceph-local --rook-osd-node=<node> --rook-osd-size-gb=40`
and skip the public-VLAN `--set` flags. A dedicated MetalLB VIP is **not** required for a one-box trial: pass `--ingress-address-layer=none` and the front door answers on the node's own address with no MetalLB installed. If you do have a spare address, `--ingress-address-layer=metallb-l2` plus `METALLB_FLOATING_IP` and a real `METALLB_INTERFACE` gives you the floating shape instead. Size the node at **≥12 vCPU /
27 GiB / 100 GB** — the full platform plus reconcile churn needs it.

On one node, object storage runs a single Ceph OSD, so the RBD pool is
provisioned at replica **`size 1`** automatically (no redundancy — correct for a
lab; multi-node installs use 3), and Ceph settles at `HEALTH_OK` instead of the
`HEALTH_WARN` a size-2 pool would show on one OSD. One caveat: the front-door
Envoy PodDisruptionBudget wants two healthy replicas, so a voluntary node drain
(e.g. an RKE2 upgrade) blocks on a one-box cluster and the front door has a brief
gap while the single Envoy restarts — plan upgrades for a maintenance window.
:::

### 3.3.1 Interactive panel + reusable config (`--config` / `--save-config`)

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
written. The panel has dedicated fields for install-critical topology:
gateway nodes/type, Ceph replication, default and per-node NICs, cloud/public
VLANs, public exclusions, control-plane IPs, MetalLB L2/BGP settings, and OSD
devices. Other operator `--set` keys from a prefill/clone (anchors, platform
endpoints, SMTP, quotas, and feature flags) are **preserved** and shown under
Review as advanced values. It never contains the git token (that comes from
`gh`/`glab` auth). Starter templates:
[`examples/install/`](https://github.com/kube-dc/kube-dc-public/tree/main/examples/install) — `internal-only.env`,
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

### 3.3.2 Which mode? `install` / `adopt` / `resume`

`--mode` tells `init` what it's walking into and is **required** — pass
`install` for a fresh RKE2 cluster (the §3.3 flow). `--mode=auto` is an
opt-in for day-2 runs *against a cluster your kubeconfig already reaches*:
`init` probes it and picks `install` / `adopt` / `resume`, printing
"Auto-detected mode: … — `<reason>`" above the plan (`KUBE_DC_MOCK` scenarios
drive it from the fixture, never your real kubeconfig). It deliberately
**never guesses greenfield**: with no kubeconfig source at all it stops with
"pass `--mode=install` or `fetch-kubeconfig` first", and a `KUBECONFIG` that
points at a missing or unreachable cluster is an error — an unread cluster
must never be silently installed over. Knowing the model helps you pick the
right path — and avoid the one that isn't automated yet:

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

`bootstrap init` attempts both finalizers automatically once Keycloak and OpenBao
are Ready. If either component was still reconciling, the CLI marks that step
deferred without undoing the install; resume only the named step from the fleet
clone with `KUBECONFIG` at the new cluster:

```bash
# 1. OIDC clients (Flux Web, Grafana, admin console) — materialises the
#    backend Secret safely, commits, and pushes through the CLI Git adapter.
kube-dc bootstrap keycloak init dc1 --repo .
flux reconcile kustomization flux-system --with-source
flux reconcile kustomization addons
flux reconcile kustomization platform

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

### 3.5.1 MANDATORY — OIDC-webhook cutover on every control-plane node

RKE2 boots cert-only (Phase 2). Until the apiserver is pointed at the
oidc-webhook-authenticator, **every Keycloak JWT returns HTTP 401** — tenant
`kubectl`, the console's Manage-Organization calls, and the k8-manager /
db-manager operators all fail. The cluster meanwhile looks perfectly healthy:
Flux is green, every pod is Ready, and nothing anywhere says "nobody can log
in". Do this after Flux finishes `infra-core`.

```bash
kube-dc bootstrap oidc-cutover --ssh-user root --dry-run   # review; use the SSH user from Phase 2
kube-dc bootstrap oidc-cutover --ssh-user root             # apply
```

It discovers the control-plane nodes from the live cluster, wires them **one at
a time**, and waits for each apiserver to return before touching the next.
Safe to re-run — a node already wired is skipped, not restarted. `--rollback`
restores each node's pre-cutover snapshot.

Verify:

```bash
kube-dc bootstrap accept <cluster>   # identity/oidc-cutover must PASS
```

#### Why this is a command and not a copy-paste block

The manual procedure had three ways to take a control-plane node out, and the
command checks all of them:

- **A partial cutover is worse than none.** `kubectl` load-balances across
  apiservers, so a tenant token is accepted only by the nodes already wired —
  intermittent 401s that read as a Keycloak or a clock problem. The command
  refuses to start unless it can reach *every* control-plane node (override with
  `--allow-partial` only if you are deliberately batching).
- **`tee -a` can silently discard apiserver flags.** Appending a second
  `kube-apiserver-arg:` key when one already exists produces duplicate YAML
  mapping keys, and RKE2 resolves that by honouring one block and dropping the
  other — so your audit-log flags disappear, or these do, with no error. The
  command merges into the existing block instead, and refuses a file that
  already has two such keys rather than guessing.
- **Restarting a node where something else holds `:6443`** leaves the apiserver
  unable to re-bind (`SO_REUSEPORT`: the second binder must ask for it, and the
  apiserver only asks when the port is free). This took a control-plane node out
  in June. The command checks the port owner and refuses that node with the
  drain instruction; it does not cordon or delete pods on its own.

It also snapshots with `cp -n` so a re-run cannot overwrite the good original,
verifies the flag on the **running process** (RKE2 accepts a malformed config
and drops the arg silently), and probes `/readyz` on `127.0.0.1` — the
loopback answer is the apiserver itself, independent of any front-door
Service or kube-proxy rule on the node IP.

:::note Doing it by hand
If you must — an unreachable node, a bastion without the CLI — the equivalent is
below. Read the three hazards above first; every one of them applies.

```bash
# ONE node at a time. Check nothing else holds :6443 first:
ssh <cp> "ss -tlpn | grep ':6443' | grep -v kube-apiserver"   # must print nothing

sudo cp -n /etc/rancher/rke2/config.yaml /etc/rancher/rke2/config.yaml.pre-oidc
# If config.yaml ALREADY has kube-apiserver-arg:, add these two items INTO that
# block. Do not append a second kube-apiserver-arg: key.
sudo vi /etc/rancher/rke2/config.yaml
#   kube-apiserver-arg:
#     - authentication-token-webhook-config-file=/etc/rancher/oidc-webhook-kubeconfig.yaml
#     - authentication-token-webhook-cache-ttl=2m
sudo systemctl restart rke2-server

# Wait for the apiserver on LOOPBACK, not the node IP:
ssh <cp> "curl -sk -o /dev/null -w '%{http_code}\n' https://127.0.0.1:6443/readyz"

# Confirm the flag reached the running process, not just the file:
ssh <cp> "ps -eo args= | grep '[k]ube-apiserver' | tr ' ' '\n' | grep authentication-token-webhook"
```
:::

### 3.6 Verify the front door

```bash
for h in login console grafana flux; do
  curl -s -o /dev/null -w "$h=%{http_code}\n" https://$h.example.com/
done
# login=302  console=200  grafana=302  flux=200   → Let's Encrypt certs live
```

Then create your first tenant with the [First Project](/cloud/first-project)
flow, or apply an `Organization` + `Project` directly:

```bash
kubectl create ns acme
kubectl apply -f - <<'EOF'
apiVersion: kube-dc.com/v1
kind: Organization
metadata: { name: acme, namespace: acme }
spec: { email: admin@example.com, description: "Acme Inc." }
---
apiVersion: kube-dc.com/v1
kind: Project
metadata: { name: web, namespace: acme }
# cidrBlock is REQUIRED — the API rejects a Project without one. It must not
# overlap the node network, the pod/service CIDRs, or another Project: each
# Project is its own VPC subnet.
spec: { cidrBlock: 10.90.0.0/16, egressNetworkType: cloud }
EOF
kubectl -n acme get organization acme -o jsonpath='{.status.ready}'   # → true
kubectl -n acme get project web -o jsonpath='{.status.ready}'         # → true
```

:::warning An Organization alone gives a tenant no kubectl access
Tenant contexts are created per **Project**. With an Organization and no
Project, the tenant's token carries no namespaces, so `kube-dc login`
authenticates, writes zero contexts, prints "Kubeconfig updated" and exits
successfully — while their `kubectl` has nothing to talk to. Create at least one
Project before handing the organization over.
:::

---

## Phase 4 — Verify generated networking and cut over ingress

The recommended flow is still GitOps, but these resources are no longer a
manual construction exercise. `bootstrap init` writes the cluster overlay and
Flux applies it. Phase 4 is where you prove that the chosen physical topology
matches the generated resources before moving DNS.

### 4.1 What `bootstrap init` already generates

| Input | Generated result |
|---|---|
| `EXT_NET_INTERFACE` | default NIC on the `${EXT_NET_NAME}` Kube-OVN `ProviderNetwork` |
| repeated `--node-nic=NODE=IFACE` or the TUI **Per-node NIC overrides** field | one label-safe, deterministic `ProviderNetwork.spec.customInterfaces` patch in `infra-core` |
| `--preset=cloud+public-vlan` + complete `EXT_PUBLIC_*` values | `infra-public-network` Flux layer and the `ext-public` Kube-OVN VLAN/Subnet |
| L2 `METALLB_FLOATING_IP` inside `EXT_PUBLIC_CIDR` + `KUBE_OVN_GW_NODES` | `ext-pub-anchor` access port, one derived per-node anchor, VIP return-policy routing, and minimum IPAM exclusions |
| `--ingress-address-layer=metallb-l2\|metallb-bgp` | MetalLB operator plus an ordered, health-gated config layer, the matching advertisement CRs, and the Gateway/Service VIP request. The `ENVOY_SERVICE_TYPE` / `ENVOY_TRAFFIC_POLICY` / `ENVOY_LB_CLASS` scalars are written to `cluster-config.env` for the host-bind data plane (see the note below) |
| `--ingress-address-layer=none` | **no MetalLB at all** — nothing is installed to claim an address, and the front door answers on the ingress nodes' own addresses. The generated cluster selects `host-bind` only, and deliberately NOT `address-metallb` |
| any layer | `spec.components` on the generated `platform` Kustomization: `gateway-config/components/host-bind` always, plus `gateway-config/components/address-metallb` on a MetalLB layer, in that order |
| `--ingress-node=NODE` (repeatable) | validation, the plan's `Front door:` line, **and the `kube-dc.com/ingress` label applied to those nodes** in the `ingress-nodes` step. Fail-closed: an empty set, a node that does not exist, or a node outside the set already carrying the label all stop the run before anything is committed |
| `METALLB_MODE` | **derived from the address layer, not an independent choice.** `metallb-l2` → `IPAddressPool` + `L2Advertisement` on `METALLB_INTERFACE`; `metallb-bgp` → `IPAddressPool` + `BGPPeer` + `/32` `BGPAdvertisement`. Setting it against the layer is refused rather than silently overridden, because the addon tree that gets wired is chosen from the layer |
| `METALLB_FLOATING_IP` | explicit Envoy Service `loadBalancerIPs` request (the pool has `autoAssign: false`) and, when different from the node address, the Gateway address patch |

:::note What the address layer controls
Choosing a layer decides three things, all of them live:

- **whether MetalLB is installed** and whether the Gateway/Service asks for a VIP;
- **which front-door components the generated cluster selects.** Every new cluster
  selects `platform/gateway-config/components/host-bind`; a `metallb-l2` or
  `metallb-bgp` layer additionally selects
  `platform/gateway-config/components/address-metallb`. A layer of `none` must
  **not** select the address component — it clears `externalIPs`, which on a
  node-address cluster is the only thing giving the Gateway an address;
- **the `ENVOY_SERVICE_TYPE` / `ENVOY_TRAFFIC_POLICY` / `ENVOY_LB_CLASS` scalars**
  in `cluster-config.env`, which those components read.

The order in `spec.components` is load-bearing: `address-metallb` must be listed
**after** `host-bind`, because its patch on the Service overwrites `host-bind`'s and
that is what clears `externalIPs` on a VIP cluster. The generator emits them in the
right order and the fleet's render gate asserts it.

**What the layer defaults to.** Omitting `--ingress-address-layer` resolves to `none` —
the fail-safe: clients reach the ingress nodes' own addresses. It cannot default to
`metallb-l2`, because that layer needs a VIP and an interface that only you can supply.
But declaring `METALLB_FLOATING_IP` **without** naming a layer is *refused*, not quietly
downgraded: a reserved address that no layer claims would never be announced, and the
install would silently serve on node addresses instead. So either pass
`--ingress-address-layer=metallb-l2` with the VIP, or pass neither.

`kube-dc bootstrap init` **applies the `kube-dc.com/ingress` label** to the resolved
ingress set (the `ingress-nodes` step, which runs before the overlay is committed).
This is a hard prerequisite rather than a nicety: the component places Envoy by that
label with *required* anti-affinity, so an unlabelled cluster renders replicas that
are all unschedulable — every manifest correct, Flux green, and no front door at all.
The step is fail-closed; see §4.1.

`ENVOY_REPLICAS` **is derived** from the resolved ingress set — you do not set it. It
must EQUAL the number of labelled nodes: fewer, and some labelled node has no Envoy,
which on a single-address cluster can be exactly the node that owns the address; more, and
the surplus stays `Pending` forever under required anti-affinity. The platform PDB is
`minAvailable: 2`, so fewer than three ingress nodes makes every voluntary drain block.
An explicit `--set ENVOY_REPLICAS=` still wins if you need it.

`INGRESS_HOST_CIDR` **is also derived**, from those nodes' `InternalIP` addresses, as the
smallest single prefix that covers them. It is what admits the front door: Envoy runs on
the host network, so what it proxies to an upstream arrives with a *node* address, and the
platform NetworkPolicies can only admit that by `ipBlock` — a `namespaceSelector` cannot
map a node IP back to a pod namespace. Left unset, OpenBao and the Flux UI answer `503`
while every manifest is correct and Flux is green.

It is deliberately **not** derived from `NODE_CIDR`: those are not always the same network
(on one real cluster `NODE_CIDR` is a public `/26` taken from an external NIC while the
nodes' `InternalIP`s are private). If your nodes reach upstreams from a different NIC than
their `InternalIP`, pass `--set INGRESS_HOST_CIDR=` explicitly. A comma-separated list is
**not** supported — the policies render one `ipBlock.cidr`, so a list is a single
malformed CIDR. `scripts/covering_cidr.py` prints the correct single prefix for a set of
node addresses.

:::

:::caution Existing clusters: check the Service's target ports first
A data-plane Service created **before** host-bind maps `443 → 10443` and `80 → 10080`.
Envoy Gateway does **not** rewrite those target ports on a Service that already exists,
so after the switch kube-proxy sends the front-door address to a port nothing listens
on: every Envoy bound correctly, and the door dark.

Check before migrating:

```bash
kubectl -n envoy-gateway-system get svc \
  -l gateway.envoyproxy.io/owning-gateway-name=eg \
  -o jsonpath='{range .items[*].spec.ports[*]}{.port}->{.targetPort}{"\n"}{end}'
```

If `443` does not map to `443`, patch the target ports **in place** — that preserves the
Service UID and therefore any MetalLB allocation. Deleting the Service also works, but on
a VIP cluster it withdraws the announcement, and MetalLB can only re-acquire the same
address if the Service requests it explicitly (see `METALLB_FLOATING_IP`).
:::

The layer decision is refused up front when incoherent — a `METALLB_FLOATING_IP`
without a layer, or a `METALLB_MODE` that contradicts one — rather than being silently
resolved, because the components and the addon tree are both chosen from it and a wrong
answer here is only visible as an unreachable address later.

### 4.1a Choosing an address layer

There is only one question, and it is about the address — never about the
data plane. Whatever you choose, the data plane is the same on every cluster, so
the choice cannot make the front door faster or slower — only reachable or not.

That data plane is an Envoy **Deployment** — one replica per node labelled
`kube-dc.com/ingress`, with required anti-affinity — running on the **host network** and
binding those nodes' `:80`/`:443` directly. That is what preserves the real client
address, which per-client rate limits and IP allowlists need.

A DaemonSet was considered and deliberately not used: `kubectl drain
--ignore-daemonsets` does not evict DaemonSet pods and DaemonSet rolling updates ignore
PodDisruptionBudgets, so a DaemonSet keeps serving on a cordoned node and its PDB is
decorative. A Deployment is evicted and its endpoint deprogrammed before the node stops,
which is what you want during planned maintenance.

**Answer it explicitly.** `metallb-l2` is the recommended shape and the
interactive wizard's default: a stable address is what DNS wants. Pick `none`
when your fabric cannot deliver a floating address. The plan's `Front door:`
line then states, in words, the address users will dial, how it is announced,
and which nodes will answer — read that line before you apply; it is checkable
against the network you were handed.

| Your situation | Use | Why |
|---|---|---|
| You have a spare routable IP and the nodes share an L2 segment with the router | `metallb-l2` | A floating VIP that MetalLB moves automatically |
| You have a spare routable IP but the fabric is routed / L3-only | `metallb-bgp` | Same VIP, announced as a `/32` over BGP |
| You have no spare IP — the public address is already on a node | `none` | Wildcard DNS points at the ingress nodes; nothing extra to install |
| Your public address is 1:1 NAT'd upstream and never appears on a NIC | `none` | Nothing in-cluster can claim that address, so host bind is the only shape that answers |

#### How the installer decides (so you can predict it)

| What you declared | What you get | Why |
|---|---|---|
| `--ingress-address-layer` explicitly | exactly what you asked for | An answer you gave is never overridden — not even by a declared VIP |
| nothing at all | `none` | The fail-safe: needs nothing from your network and always comes up. A VIP default would leave the front door `<pending>` forever on a site with no spare address — and broken is worse than suboptimal |
| a `METALLB_FLOATING_IP` but **no layer** | **refused**, with both fixes spelled out | A reservation is not a front door. Auditing a live fleet — by resolving each cluster's own wildcard hostname, not by reading its config back to itself — found **four** clusters declaring a floating IP and only **two** serving on one: one address was a spare, another was never delivered by the provider's fabric. Inferring would have rewired two production front doors to addresses that cannot carry them |
| explicit `none` **and** a `METALLB_FLOATING_IP` | **refused**, with the flag to pass | Contradictory: that address could never be announced. The installer will not silently pick one meaning |

Two things to know before choosing a VIP layer:

- **Failover is automatic but not instant.** BGP withdrawal waits for the
  hold timer (`METALLB_BGP_HOLD_TIME`, default `90s`); L2 recovery is
  memberlist failure detection plus ARP convergence — seconds to tens of
  seconds. With `none`, recovery is client-driven instead: publish every
  ingress node as an A record with a short TTL and clients retry the next
  one.
- **The announcing node must be both a gateway node and an ingress node.**
  This is the one front-door mistake that fails *silently*, so it is worth
  understanding rather than memorising:

  - MetalLB announces the VIP only from nodes labelled
    `ovn.kubernetes.io/external-gw` — i.e. your `KUBE_OVN_GW_NODES` set. That
    selector is pinned in the shared `addons/metallb-config` tree for both the
    L2 and BGP variants.
  - Envoy answers only on the nodes labelled `kube-dc.com/ingress`.
  - On a **MetalLB layer**, the Envoy Service uses `externalTrafficPolicy: Local`
    so the real client IP survives. Under `Local`, a node advertises the VIP
    **only while it holds a ready local endpoint** — which here means a running
    Envoy. That is also what makes a rolling update near-gapless: the VIP moves
    to a node that is already serving instead of waiting for a new pod to start.
  - On a layer of **`none`** there is no VIP to move. The Service stays `ClusterIP`
    and keeps `externalIPs`, and `externalTrafficPolicy` is left alone — do **not**
    set `Local` there. With `externalIPs` rather than a LoadBalancer address, `Local`
    leaves no usable local path and drops the traffic outright; this was measured on a
    live cluster, which went from partly-working to 13 failed probes out of 13 and had
    to be reverted the same hour.

  So the VIP is announced only from the *intersection* of those two sets. If
  they are disjoint, **nothing announces it**: the Service shows its external
  IP, the pods are `Ready`, Flux is green, and the address is simply dark. You
  find out by curling it.

  `kube-dc bootstrap init` refuses that combination before writing anything — and
  under a VIP layer it also refuses a *partial* overlap. One node in both sets can
  announce, and that node is then a single point of failure dressed as HA: losing it
  darkens the address while every other component still reports healthy. So the
  ingress set must be a **subset** of `KUBE_OVN_GW_NODES`.
  `scripts/frontdoor-check.sh preflight` enforces the same rule against the live
  cluster, so anything looser would only move the failure to after the fleet was
  committed. **The simplest correct answer is to pass no `--ingress-node` at all**:
  the gateway nodes are then used, which satisfies the invariant by construction.

:::tip kube-api (`:6443`) is served OFF-Envoy; tenant clusters ride Envoy `:443`
The two paths carry different traffic and are wired differently:

- **`kube-api.<domain>:6443`** — the **management apiserver**. It is **never
  routed through Envoy**. The front door ships a selectorless
  `ClusterIP` + `externalIPs` Service on `:6443` whose external IP is
  `KUBE_API_ARRIVAL_IP` — the address external kube-api traffic *arrives on*
  at the node: the node's own IP on the `none` / 1:1-NAT layers, the announced
  VIP on a MetalLB layer (`init` substitutes `METALLB_FLOATING_IP` there so
  both keys carry one value). kube-proxy matches that arriving address and
  hands the connection to the apiserver. There is no Envoy `:6443` listener at
  all — one would collide with the apiserver on a control-plane ingress node
  (production incident 2026-08-11), which is exactly why the old listener +
  "drop it behind NAT" patch were retired.
- **`:443`** (`tls-passthrough-wildcard`, hostname `*.<domain>`) carries the
  **managed Kubernetes clusters**. A tenant's kubeconfig points at
  `https://<cluster>-cp-<namespace>.<domain>:443`, and Envoy passes the TLS
  session through to that Kamaji control plane. Managed clusters never use
  `:6443`.

`init` derives `KUBE_API_ARRIVAL_IP` while post-processing
`cluster-config.env` (on a MetalLB layer it becomes `METALLB_FLOATING_IP`);
if the VIP itself is still a placeholder the scaffold refuses with
`still contains placeholder METALLB_FLOATING_IP` — the misconfiguration fails
loudly instead of shipping a dark kube-api. Point `kube-api.<domain>` DNS at
that arrival address.
:::

The installer rejects missing public-network reservations, a missing L2
interface, incomplete BGP peer data, unsupported IPv6 VIPs, malformed/range-
invalid ASNs and hold times, and any unresolved `CHANGEME` left in the final
cluster config. The starter artifact must contain the matching `addons/` and
`infrastructure/kube-ovn-network-public/` and `infrastructure/ext-net-bridge-tag/` source trees; mixing a newer CLI with
an older starter fails before partial overlay files are promoted.

:::warning The CLI creates the OVS anchor port, not the physical trunk
The public preset creates Kube-OVN logical `ext-public` network for tenant
EIp/FIp allocation. When an **L2** `METALLB_FLOATING_IP` is inside that CIDR,
the current CLI also derives one address per `KUBE_OVN_GW_NODES` entry,
selects `ext-pub-anchor`, reserves gateway + VIP + anchors from tenant IPAM,
and the starter-provided `ext-net-bridge-tag` DaemonSet continuously creates
the OVS internal access port and VIP return-policy route on those nodes.

Before binding a derived anchor, the worker performs ARP duplicate-address
detection on the tagged port. This protects upgrades where an LRP/EIP received
the address before the exclusion existed; Kube-OVN exclusions protect new
allocations but do not evict old ones. A detected reply leaves the anchor
container NotReady and the Flux health gate fails instead of creating two ARP
owners.

That automation stops at the host. The parent provider bridge and physical NIC
must already carry `EXT_PUBLIC_VLAN_ID`, and the upstream switch must allow the
VLAN and ARP/GARP. An OVS access port carries one VLAN tag, which is why the
public VIP uses a second internal port instead of `br-ext-cloud`. If the VIP
stays in `EXT_NET_CIDR`, no public anchors are derived and the operator-supplied
`METALLB_INTERFACE` remains authoritative. BGP mode reserves the VIP but does
not create L2 anchors. Only the live checks below can prove the physical path.
:::

### 4.2 Verify ProviderNetwork and custom NIC mapping

```bash
kubectl get providernetwork ext-cloud -o yaml
kubectl get providernetwork ext-cloud \
  -o jsonpath='{.status.readyNodes}{"\n"}{.status.notReadyNodes}{"\n"}'
kubectl get vlan
kubectl get subnet ext-cloud ext-public   # ext-public only for the public preset
```

Nodes with different trunk NIC names should have been supplied during init:

```bash
kube-dc bootstrap init ... \
  --node-nic=master-2=eno1 \
  --node-nic=master-3=enp6s0
```

The same comma-separated `NODE=IFACE` mapping is editable in the TUI and
round-trips through `KUBE_DC_INIT_NODE_NICS` in a saved config. Do **not** apply
a raw `ProviderNetwork` object after install: that creates drift from the fleet
source of truth.

<details>
<summary>Day-2 fallback for an older overlay without the generated patch</summary>

Add this to the `infra-core` Flux Kustomization in
`clusters/<name>/infrastructure.yaml`, commit, and reconcile. The current CLI
generates this shape automatically.

```yaml
spec:
  patches:
    - target:
        group: kubeovn.io
        version: v1
        kind: ProviderNetwork
        name: ${EXT_NET_NAME}
      patch: |-
        - op: add
          path: /spec/customInterfaces
          value:
            - interface: eno1
              nodes: [master-2]
            - interface: enp6s0
              nodes: [master-3]
```

</details>

### 4.2a Check the front door before and after Flux reconciles

Two scripted checks live in the fleet repo. Use them; the front door has a failure mode
that every other signal reports as healthy.

```bash
# BEFORE letting Flux reconcile a front-door change
scripts/frontdoor-check.sh preflight <cluster> <kubeconfig>

# AFTER it has reconciled
scripts/frontdoor-check.sh smoke <cluster> <kubeconfig> [hostname ...]
```

`preflight` runs a **server-side** apply dry-run of the rendered `EnvoyProxy`, then checks
the ingress labels exist and are a subset of the MetalLB announcer set, that
`ENVOY_REPLICAS` equals the labelled node count and exceeds the PDB minimum, that `:80`
and `:443` are free (or already held by this cluster's own Envoy) in each node's host
netns, and that the Service has no stale pre-host-bind target ports.

The server-side part matters: `kubectl apply --dry-run=server` strips explicit nulls
client-side and reports success on objects Flux rejects — and because these
Kustomizations run with `force: true`, a rejected object is **deleted** rather than left
alone, after which Envoy Gateway regenerates a default 1-replica non-hostNetwork
Deployment and nothing is listening at all.

`smoke` asserts what a reconcile cannot: that `:80` and `:443` are actually LISTEN in each
ingress node's host netns, that the `envoy` container ended up root with `NET_BIND_SERVICE`
*and* kept `drop: [ALL]` and its seccomp profile while the sidecar stayed non-root, that the
Gateway is `Programmed`, and that the hostnames really answer. A non-root Envoy with the
capability starts, reports `2/2 Ready`, passes its probes and logs
`cannot bind '0.0.0.0:443': Permission denied` for every listener — the pods look perfect
and the site is down, which is why the socket assertion exists rather than a log grep.

### 4.3 Verify MetalLB allocation and announcement

```bash
kubectl -n metallb-system get ipaddresspool,l2advertisement,bgppeer,bgpadvertisement
kubectl -n envoy-gateway-system get svc \
  -l gateway.envoyproxy.io/owning-gateway-name -o yaml
kubectl get gateway -A
```

Confirm all of these, not just a controller log line:

1. the Envoy Service annotation requests exactly `METALLB_FLOATING_IP`;
2. `status.loadBalancer.ingress` contains that IP;
3. the Gateway address is that IP when it differs from `NODE_EXTERNAL_IP`;
4. the announcing interface exists and is on the intended VLAN; and
5. TCP reaches the VIP from both the local segment and the actual client path.

```bash
ip -br link show "$METALLB_INTERFACE"
ip neigh show <VIP>       # INCOMPLETE while real neighbours are REACHABLE is evidence
curl -sk -o /dev/null -w '%{http_code}\n' \
  --resolve console.example.com:443:<VIP> https://console.example.com/
```

A MetalLB L2 VIP need not answer ICMP. `serviceAnnounced` only means the
speaker accepted desired state; it does not prove the interface has the right
VLAN or that the upstream router learned the address. Restart speakers after
creating an interface that was absent when they first reconciled, then repeat
the network-side check.

#### BGP mode and mode changes

For a new cluster, choosing `--ingress-address-layer=metallb-bgp` in CLI/TUI selects the BGP base
and requires `METALLB_BGP_LOCAL_ASN`, `METALLB_BGP_PEER_ASN`, and
`METALLB_BGP_PEER_ADDRESS`; optional port and hold time are validated against
the wire format. The shared fleet defaults both `BGPPeer` sessions and
`BGPAdvertisement` announcements to nodes labelled
`ovn.kubernetes.io/external-gw=true`; the router must accept sessions from every
node selected by `BGPPeer`. A per-cluster label-targeted patch can replace this
when a routed fabric uses dedicated speakers. `BGPAdvertisement.spec.nodeSelectors`
controls who announces a prefix; it does not limit who opens a session.

For a day-2 L2↔BGP migration, change the `addons-config` base in Git and remove
the old advertisement explicitly after reconcile because that Kustomization
uses `prune: false`. Target per-cluster selector patches by the stable label
`kube-dc.com/advertisement=envoy-gateway`, not by kind/name; a kind-specific
patch is silently ignored after the mode changes. L2/BGP here covers platform
VIPs only. Tenant EIp/FIp announcement remains Kube-OVN's L2 responsibility.

When leaving public-subnet L2 mode, clear `EXT_NET_PUBLIC_ANCHOR_IPS` and
`EXT_NET_PUBLIC_ANCHOR_VLAN` in the same change. Current CLI/TUI validation
rejects stale L2 host state; the fleet worker also retires its owned address,
policy rule, route table, and OVS port whenever `METALLB_MODE` is not `l2`.
Clusters created before the mode key existed default to `l2` for compatibility.

### 4.4 Optional ext-cloud anchor units

Some L2 topologies need one real address on the provider bridge of every
eligible gateway/speaker node before the kernel will source announcements
correctly. This is explicit day-2 work, not a hidden init side effect:

1. enumerate every existing host address, DHCP reservation, Kube-OVN LRP/EIP,
   VIP, and excluded range first;
2. put unique `host=CIDR` entries in `EXT_NET_ANCHOR_IPS`, the interface in
   `EXT_NET_ANCHOR_INTERFACE`, and SSH targets in
   `EXT_NET_ANCHOR_SSH_HOSTS`;
3. set `EXT_NET_ANCHOR_REQUIRED=true` only when every gateway node is covered;
4. run and verify:

```bash
kube-dc bootstrap anchors apply <cluster> --repo <fleet>
kube-dc bootstrap doctor anchors <cluster> --repo <fleet>
```

Kube-OVN removes `excludeIps` from the free pool used for **new** random LRP/EIP
allocations, so exclusions protect anchors on a greenfield install. They do not
evict an address already allocated before the exclusion was widened. Enumerate
live LRP/EIP and host claims, and rely on the worker's duplicate-address probe:
a host anchor colliding with an older OVN LRP produces two MACs answering for
one IP and intermittent outages.

### 4.5 Move DNS only after client-side acceptance

Test the VIP from outside the node segment before moving DNS to it.

On a MetalLB layer you do **not** hand-write an `externalIPs` removal any more: the
`address-metallb` component clears it, because on a VIP cluster the announced address is
the front door and leaving `externalIPs` in place would make the node's own address a
second, unannounced entrance whose traffic takes the kube-proxy path instead. Selecting
that component *is* the removal.

Two things worth knowing about that clearing, both learned the hard way:

- Dropping the key from a patch does not remove it from a **live** Service. Envoy Gateway
  never deletes fields it no longer wants, so the value has to be explicitly nulled —
  which is what the component does. A cluster that had `externalIPs` before the migration
  keeps it otherwise, silently.
- The clearing only happens if `address-metallb` is listed **after** `host-bind` in
  `spec.components`. Reversed, `host-bind` re-asserts `externalIPs` from
  `NODE_EXTERNAL_IP` and the second entrance survives.

Once the VIP is proven, commit, push, reconcile, and change DNS:

```text
*.example.com        → <METALLB_FLOATING_IP>
kube-api.example.com → <METALLB_FLOATING_IP>   # the VIP is KUBE_API_ARRIVAL_IP on a MetalLB layer
```

Keep the old DNS target until the new records resolve and HTTPS/API probes pass
from the user network. Roll back DNS first if acceptance fails.

---

## Phase 5 — Verify Installation

### The one command that checks the wiring

```bash
kube-dc bootstrap accept <cluster>
```

It reports one of three states:

| State | Meaning | Exit |
|---|---|---|
| `reconciling` | Flux has not settled — wait and re-run | 2 |
| `converged` | Components are up, but something a user would hit is broken | 1 |
| `usable` | Identity works, the front door is trusted, tenancy is installed | 0 |

That distinction is the point. Everything else in this phase — and `flux get
kustomizations`, and `kube-dc bootstrap status` — reports **convergence**, and a
converged cluster can be entirely unusable: green Flux, Running pods, and every
Keycloak login returning 401.

The check worth knowing is `identity/oidc-cutover`: it reads the flags each
`kube-apiserver` is **actually running with**, from the static pods RKE2
registers per control-plane node. That catches both a skipped §3.5.1 and — more
importantly — a *partial* one, whose symptom is intermittent and misleading.

A check that cannot be performed reports `SKIP` with the reason, and a skipped
required check never yields `usable`: "I could not tell" must not read as "fine".

`usable` proves the **wiring** — Flux settled, nodes and control planes present,
every apiserver calling the OIDC webhook, the console answering over a trusted
certificate, tenancy CRDs installed. It does **not** authenticate a real token,
create an Organization or Project, or check Ceph health. Treat it as the gate that
must pass before the human checks below are worth running, not as a substitute for
them.

### Check All Components

The manual sweep below is still useful when `accept` reports a failure and you
want to see where. It is not a substitute for it — every command here can pass
on a cluster nobody can log into.

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

### Access the web consoles

Open `https://console.example.com` for tenants and `https://admin.example.com`
for platform administrators. The admin frontend is enabled by the greenfield
scaffold; its Keycloak-backed pages become active after §3.5 finalization.

The **admin console** authenticates against the Keycloak `master` realm. §3.5
finalization grants the master-realm `admin` user the **`superadmin`** realm role
automatically, so the console opens straight to the dashboard — no more bare
"Required role: superadmin or platform-admin" 403. Log in as Keycloak `admin`;
add further platform admins by granting them the `superadmin` (full) or
`platform-admin` (read-mostly) realm role in Keycloak.

Retrieve the admin password of the organization you created in
[§3.6](#36-verify-the-front-door) (the manager writes a `realm-access`
Secret into every Organization's namespace on reconcile — nothing named
`demo-org` exists on a fresh install):

```bash
kubectl get secret realm-access -n acme -o jsonpath='{.data.password}' | base64 -d; echo
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

For direct node joins, use the [CLI procedure in §2.2](#22-add-worker-nodes-with-kube-dc-bootstrap-install---join-server) or the [manual procedure in §2.3.3](#233-join-worker-nodes-manually).

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

Common issue: nodes have different NIC names. Supply `--node-nic=NODE=IFACE` during init or update the generated GitOps patch (see [Phase 4.2](#42-verify-providernetwork-and-custom-nic-mapping)).

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
`--allow-dns-not-ready` to proceed — the install completes and the ACME
Certificates simply sit Pending until the record resolves), or a missing
`repo,workflow`/`repo` scope on the `gh` token for `new-repo` mode.

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
