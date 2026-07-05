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
- On `master-2` use `192.168.0.2`, on `master-3` use `192.168.0.3`.
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

Key flags: `--name` (RKE2 node-name; defaults to the positional arg — use the
same name in `init`), `--node-ip` / `--external-ip` (override auto-detection),
`--force` (re-run on an already-installed node — restarts to apply config
changes), `--set POD_CIDR=…` (override a preset CIDR). Requires passwordless
sudo (or a root login) on the node.

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

Monitor startup (wait until the node is `Ready`):

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
# --ssh-host    the new worker (must be directly reachable)
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
control-plane SSH. Note: v1 has no ProxyJump — run from a host that can
reach the worker directly (a bastion on the network, or the control-plane
node itself).

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
| `--github-owner` / `--github-repo` | Where the fleet repo lives (auto-created in `new-repo` mode) |
| `--object-storage-mode` | `rook-ceph-multi-node` (3+ OSDs, HA), `rook-ceph-local` (single OSD — lab), `rook-ceph-pvc`, `external-*`, or `disabled` |
| `--ceph-node=NODE=DEVICE` | One raw block device per OSD node (repeat 3× for multi-node) |
| `--ssh-host` | Control-plane SSH target — enables kubeconfig auto-pull **and** NAT-topology detection (§3.2) |
| `--set=KUBE_OVN_MASTER_NODES` | Control-plane **internal** IPs (comma-separated) — not emitted by the preset, always set it |
| `--set=EXT_NET_INTERFACE` / `EXT_NET_VLAN_ID` | Trunk NIC + cloud VLAN ID from Phase 1 |

What `init` does, in order: generates a SOPS **age key** → creates +
pushes the fleet repo → scaffolds `clusters/dc1/` (network, object
storage, encrypted secrets) → `flux bootstrap` → pre-installs the
CNI/CRD-bearing charts so a bare cluster can reconcile → hands off to
Flux. It is idempotent and rolls back its own commit if the push fails.

:::info Single-node / lab install
For a one-box trial, use `--preset=internal-only --object-storage-mode=rook-ceph-local --rook-osd-node=<node> --rook-osd-size-gb=40`
and skip the provider-VLAN `--set` flags. Size the node at **≥12 vCPU /
27 GiB / 100 GB** — the full platform plus reconcile churn needs it.
:::

### 3.4 Watch the platform converge

Flux reconciles in dependency order:
`flux-system → infra-cni → infra-core → infra-object-storage → platform`.

```bash
export KUBECONFIG=~/.kube/config          # the cluster from Phase 2
flux get kustomizations                   # all five should reach Ready=True
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

### 4.1 Deploy MetalLB for HA Ingress

MetalLB provides a **floating public IP** for Envoy Gateway that automatically fails over between the three control-plane nodes.

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

Now that MetalLB is running, update the wildcard DNS record (initially set in [Phase 3.4](#34-configure-dns)) to point to the **floating IP** instead of the single-node IP:

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

**Manual addition** — Install RKE2 agent on a new server and join it to the cluster:

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
