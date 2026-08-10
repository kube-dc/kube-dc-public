# Quickstart: install Kube-DC on three servers

This is the shortest path from bare servers to a working Kube-DC cluster you can
log into. It is linear, every command is copy-pasteable, and it stops at a
verified checkpoint after each phase.

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

**Time:** ~60 minutes after the servers exist. Most of it is waiting for Flux.

:::info What you end up with
A three-node management cluster running the Kube-DC platform: the web console,
tenant Organizations and Projects, VM support (KubeVirt), managed Kubernetes
clusters, object storage (Ceph), and per-tenant observability.

All three nodes are control-plane **and** schedulable — this path adds no
separate workers, and that is deliberate: three nodes is the smallest shape with
etcd quorum, and tenant workloads run on the same nodes. Adding dedicated
workers later is capacity and failure-domain work, covered in the
[Installation Guide](installation-guide.md).

The control plane is HA (three etcd members). The **front door is not**: with
`INGRESS_ADDRESS_LAYER=none` the public address belongs to one node, so losing
that node removes external access until DNS or the address moves. A MetalLB VIP
is how you fix that, and it is the recommended shape — see the guide's
address-layer section.

Either way the data plane is the same: Envoy runs on the host network on the nodes
labelled `kube-dc.com/ingress`, binding their `:80`/`:443` directly, so it sees the
real client address. `init` applies that label itself; you do not label nodes by
hand. With a MetalLB VIP the address then follows a node that has a *ready* Envoy,
which is what makes the front door survive both a node loss and a rolling update.
:::

---

## 0. Prerequisites worksheet

Fill this in **before** you start. Every value is used by a command below, and
two of them (the domain and the addresses) are painful to change afterwards.

| # | Value | Example | Notes |
|---|---|---|---|
| 1 | Three servers, Ubuntu 24.04 | — | See the floors below |
| 2 | Domain you control | `dc.example.com` | Wildcard DNS must point at #4 |
| 3 | Node internal IPs | `192.168.0.11-13` | Stable, same L2, used for etcd |
| 4 | Public address for the front door | `203.0.113.10` | Where `*.dc.example.com` resolves |
| 5 | Admin email | `ops@example.com` | Used for ACME registration |
| 6 | GitHub owner + repo name | `my-org`, `my-kube-dc-fleet` | A **new, empty, private** repo. GitHub only — see below |
| 7 | Storage devices for Ceph | `/dev/nvme1n1` per node | **Will be wiped.** Must be raw |
| 8 | Cloud VLAN id + NIC | `200`, `eth1` | For tenant networking |
| 9 | SSH user with passwordless sudo | `ubuntu` | Must already exist on all three servers. `root` also works |
| 10 | Local fleet checkout path | `~/kube-dc-fleet` | Writable on your workstation; every later command needs it |
| 11 | First Project's CIDR | `10.90.0.0/16` | A Project **requires** this and it must not overlap #3, the pod/service CIDRs, or another Project |

:::warning GitHub only, for now
The CLI advertises GitLab, but `doctor` probes only `gh` and this path is not
tested end to end with `glab`. Use GitHub.
:::

Export the two paths now — later steps use them, and one of them is how every
command after `init` finds your fleet:

```bash
export KUBE_DC_FLEET="$HOME/kube-dc-fleet"       # worksheet #10
export KUBE_DC_SSH_USER=ubuntu                   # worksheet #9
```

**Per-server floors.** Below these the platform converges and then fails to run
workloads:

| Resource | Minimum | Why |
|---|---|---|
| CPU | 12 cores | Platform + reconcile churn |
| RAM | 32 GiB | 27 GiB usable after reservations |
| Root disk | 100 GB | Images and container layers |
| Ceph disk | 1 raw device, ≥100 GB | Must have **no** filesystem or old Ceph signature |
| `/dev/kvm` | present | Required for VMs and managed clusters |

**Outbound network.** The nodes and your workstation need HTTPS egress to:
`get.rke2.io`, `ghcr.io`, `docker.io` / `registry-1.docker.io`, `quay.io`,
`registry.k8s.io`, your Git provider, and Let's Encrypt. Also NTP — a clock skew
of more than a few minutes fails TLS in confusing ways.

**Between nodes:** `6443`, `9345`, `2379-2380`, `10250`, plus Geneve/VXLAN for
the CNI. **From the internet:** `80` and `443` to #4, and `6443` if you want
`kubectl` from outside.

---

## 1. Prepare your workstation

Everything below runs from one machine that can SSH to all three servers **and**
reach the cluster API over HTTPS. A jump host works for SSH, but note that
`ProxyJump` tunnels SSH only — Phase 3 needs independent HTTPS access to
`kube-api.<your-domain>:6443`.

```bash
# 1. Install the CLI at a PINNED version, with its checksum verified.
#    Do not use `latest`: an install you cannot reproduce is an install you
#    cannot support.
KUBE_DC_INSTALL_VERSION=vX.Y.Z          # the release you were given
asset=kube-dc_linux_amd64
tmp="$(mktemp -d)"
curl -fSL "https://github.com/kube-dc/kube-dc-public/releases/download/${KUBE_DC_INSTALL_VERSION}/${asset}" -o "${tmp}/${asset}"
curl -fSL "https://github.com/kube-dc/kube-dc-public/releases/download/${KUBE_DC_INSTALL_VERSION}/checksums.txt" -o "${tmp}/checksums.txt"
( cd "${tmp}" && grep " ${asset}$" checksums.txt | sha256sum -c - )
sudo install -m 0755 "${tmp}/${asset}" /usr/local/bin/kube-dc && rm -rf "${tmp}" && hash -r
kube-dc version

# 2. Install the other tools the CLI drives. On Ubuntu/Debian:
sudo apt-get update && sudo apt-get install -y git curl openssh-client jq
#    kubectl, flux, sops, age, kustomize, helm and yq: use your usual method, or
#    see the Installation Guide §1.5. `kube-dc bootstrap install-prereqs` can do
#    it, but only AFTER a fleet checkout exists (it runs a script from the fleet),
#    so it is not usable at this point on a clean machine.

# 3. Authenticate to GitHub — a HARD requirement, not a warning. Flux bootstrap
#    creates a repo and a deploy key; without these scopes it fails AFTER the
#    cluster is already running.
gh auth login --scopes repo,workflow

# 4. Confirm the workstation is ready
kube-dc bootstrap doctor
```

`doctor` must show no **blockers**. It probes kubectl, flux, sops, age, git, gh,
ssh and bao — note that it does **not** verify helm, kustomize or yq, so a green
doctor is not proof those exist. A missing local `bao` is expected and
informational: the CLI runs OpenBao commands inside the cluster, never on your
machine.

:::tip A browser matters later
`kube-dc login` uses a browser redirect to `localhost`. If you run it on a
headless bastion, the redirect lands on the bastion's loopback, not your laptop.
Plan to run the login step on a machine with a browser. The
client-certificate kubeconfig from step 3 works headless and needs no browser.
:::

---

## 2. Point DNS at the cluster

Create these records **now** — ACME certificate issuance in Phase 4 needs them,
and DNS propagation is the most common reason a first install stalls.

| Record | Type | Value |
|---|---|---|
| `*.dc.example.com` | A | `203.0.113.10` (worksheet #4) |
| `dc.example.com` | A | `203.0.113.10` |

```bash
dig +short console.dc.example.com     # must return your #4 address
dig +short kube-api.dc.example.com    # same
```

Both must answer before you continue.

---

## 3. Build the management cluster

Run these **in order**, waiting for each to finish. The first node initialises
the cluster; the other two join it.

:::note First SSH contact
Host-key checking is strict by default, so the very first connection to a new
server is refused until its key is known. Either enrol the keys after verifying
them out of band (`ssh-keyscan -H <host> >> ~/.ssh/known_hosts`), or add
`--ssh-accept-new-host-keys` to the three commands below to trust them on first
sight. A key that later *changes* is always refused, whichever you pick.
:::

```bash
# First control-plane node. Review with --dry-run first if you like.
kube-dc bootstrap install dc1-master-1 \
  --ssh-host "$KUBE_DC_SSH_USER@192.168.0.11" \
  --name dc1-master-1 \
  --domain dc.example.com \
  --preset cloud-vlan \
  --node-ip 192.168.0.11 \
  --external-ip 203.0.113.10

# Second and third control-plane nodes. --domain and --preset must MATCH the
# first node, or the API certificate SANs diverge between control planes.
kube-dc bootstrap install dc1-master-2 \
  --ssh-host "$KUBE_DC_SSH_USER@192.168.0.12" --name dc1-master-2 \
  --join-server "$KUBE_DC_SSH_USER@192.168.0.11" --role server \
  --domain dc.example.com --preset cloud-vlan --node-ip 192.168.0.12

kube-dc bootstrap install dc1-master-3 \
  --ssh-host "$KUBE_DC_SSH_USER@192.168.0.13" --name dc1-master-3 \
  --join-server "$KUBE_DC_SSH_USER@192.168.0.11" --role server \
  --domain dc.example.com --preset cloud-vlan --node-ip 192.168.0.13
```

Always pass `--node-ip` explicitly on multihomed servers. Left out, the
installer guesses from the default route, which is not necessarily the address
you want carrying etcd. `--join-server` takes an **SSH endpoint** — the join
token and the control-plane IP are read from that node over SSH.

Then pull an admin kubeconfig to your workstation:

```bash
kube-dc bootstrap fetch-kubeconfig dc1 \
  --ssh-host "$KUBE_DC_SSH_USER@192.168.0.11" \
  --domain dc.example.com \
  --set-current
```

**Checkpoint — do not continue until both pass:**

```bash
kubectl get --raw=/readyz             # ok
kubectl get nodes                     # 3 nodes REGISTERED (see below)
```

:::note `NotReady` here is correct — do not wait for Ready
RKE2 is installed with `cni: none` on purpose; Kube-OVN arrives with Flux in
step 4. Until then every node reports `NotReady` with
`container runtime network not ready`, and that is the expected state. What
matters at this checkpoint is that all three nodes are **registered** and the
API answers `/readyz`. They turn `Ready` during step 4.
:::

---

## 4. Install the platform

One command scaffolds a GitOps repository, hands off to Flux, and reconciles the
whole platform. Put your answers in a config file so the run is reviewable and
repeatable.

```bash
cat > dc1.env <<'EOF'
# --- Orchestration: how init should run. These keep the KUBE_DC_INIT_ prefix.
KUBE_DC_INIT_MODE=install
KUBE_DC_INIT_PRESET=cloud-vlan
KUBE_DC_INIT_SSH_HOST=ubuntu@192.168.0.11   # worksheet #9@#3
# GitOps target: a NEW, EMPTY, PRIVATE repository.
# KUBE_DC_INIT_REPO is the LOCAL directory the fleet is checked out into — it is
# required even for new-repo (dry-run passes without it; apply stops at
# "RepoPath is required"). Later steps use this path too.
KUBE_DC_INIT_FLEET_MODE=new-repo
KUBE_DC_INIT_PROVIDER=github
KUBE_DC_INIT_GITHUB_OWNER=my-org
KUBE_DC_INIT_GITHUB_REPO=my-kube-dc-fleet
KUBE_DC_INIT_REPO=/home/ubuntu/kube-dc-fleet   # must equal $KUBE_DC_FLEET (worksheet #10)

# --- Cluster identity. These are PLAIN keys, with NO prefix.
CLUSTER_NAME=dc1
DOMAIN=dc.example.com
NODE_EXTERNAL_IP=203.0.113.10
EMAIL=ops@example.com

# --- Tenant networking
EXT_NET_VLAN_ID=200
EXT_NET_INTERFACE=eth1
KUBE_OVN_MASTER_NODES=192.168.0.11,192.168.0.12,192.168.0.13
KUBE_OVN_GW_NODES=dc1-master-1,dc1-master-2,dc1-master-3

# --- Front door: who owns the address your users dial.
#   none        clients reach the ingress nodes' own IPs; no MetalLB installed
#   metallb-l2  a floating VIP announced by ARP (needs a spare address, and
#               METALLB_FLOATING_IP + METALLB_INTERFACE)
# Start with none: it needs nothing from your network and always comes up.
# A reserved address is NOT assumed to be your front door, so if you set
# METALLB_FLOATING_IP you MUST also state the layer or init refuses.
INGRESS_ADDRESS_LAYER=none

# --- Object storage: one raw device per node, WHICH WILL BE WIPED
OBJECT_STORAGE_MODE=rook-ceph-multi-node
EOF

# Review the plan first. No cluster or fleet mutation — a local consent
# marker is written under ~/.kube-dc/init-state/.
kube-dc bootstrap init --config dc1.env \
  --ceph-node dc1-master-1=/dev/nvme1n1 \
  --ceph-node dc1-master-2=/dev/nvme1n1 \
  --ceph-node dc1-master-3=/dev/nvme1n1 \
  --dry-run
```

Read the plan. In particular check the **`Front door:`** line — it states the
address your users will dial and which nodes will answer — and the list of
**devices to be wiped**. When it matches what you intended:

```bash
# Same command, --yes instead of --dry-run
kube-dc bootstrap init --config dc1.env \
  --ceph-node dc1-master-1=/dev/nvme1n1 \
  --ceph-node dc1-master-2=/dev/nvme1n1 \
  --ceph-node dc1-master-3=/dev/nvme1n1 \
  --yes
```

This takes 20-40 minutes. It commits the fleet repo, bootstraps Flux, and waits
for reconciliation.

:::warning Two secrets you must back up yourself
Neither is recoverable by anything in the product, and both live inside the
cluster or repo you are protecting — so copy them somewhere else.

- **The age private key.** `init` generates it and prints its PATH (the key
  itself is not echoed). Copy that file off this machine. Without it nobody can
  decrypt the fleet's secrets.
- **The OpenBao recovery shares.** These stay in the SOPS-encrypted fleet copy
  by default, which is fine for day-to-day but circular for a full outage: you
  would need the age key to read them. For an independent plaintext copy, add
  `--openbao-shares-out ~/dc1-openbao-shares.yaml` to the command above and
  store that file securely, NEVER inside a Git tree.
:::

**Checkpoint:**

```bash
flux get kustomizations              # all Ready=True
kubectl -n kube-dc get pods          # manager, backend, frontend Running
```

---

## 5. Make logins work

**This step is mandatory and nothing will tell you that you skipped it.** RKE2
starts with certificate-only authentication, because the OIDC webhook does not
exist until Flux brings it up. Until the API server is pointed at that webhook,
every Keycloak token is rejected — so the console cannot manage Organizations,
tenant `kubectl` fails, and two operators fail — while the cluster looks
perfectly healthy.

```bash
kube-dc bootstrap oidc-cutover --ssh-user "$KUBE_DC_SSH_USER" --dry-run   # review
kube-dc bootstrap oidc-cutover --ssh-user "$KUBE_DC_SSH_USER"             # apply
```

It finds the control-plane nodes itself, wires them one at a time, and waits for
each API server to come back before touching the next. It is safe to re-run: a
node already wired is skipped, not restarted.

It refuses to run unless it can reach **every** control-plane node. That is
deliberate — a half-wired cluster returns *intermittent* 401s, because `kubectl`
load-balances across API servers and only some of them accept the token. That
symptom looks like a Keycloak or clock problem and wastes hours.

---

## 6. Get API access

Three different ways in, for three different purposes. Set up the first two now
and the third before you need it.

### a. Operator, certificate-based — works without OIDC

This is what step 3 already gave you: a cluster-admin kubeconfig using a client
certificate, independent of Keycloak. Keep it. It is how you fix the cluster when
identity itself is broken, and it needs no browser.

```bash
kubectl get nodes
```

### b. Operator, OIDC — the day-to-day path

Named accounts, audited, and revocable in Keycloak. Requires membership of the
`admin` group in the master realm; RBAC maps that to `cluster-admin`.

```bash
kube-dc bootstrap kubeconfig dc1 --repo "$KUBE_DC_FLEET"   # writes an OIDC exec-plugin context
kube-dc login --domain dc.example.com --admin
kubectl auth whoami
```

### c. Tenant users

Do this **after** step 7 creates an Organization and a Project — before that
there is nothing for a tenant token to grant.

```bash
kube-dc login --domain dc.example.com --org acme
kubectl get pods
```

:::warning An Organization alone produces no kubectl access
Tenant contexts are created per **Project**. An Organization with no Project
yields a token with no namespaces, so `kube-dc login` authenticates, writes zero
contexts, prints "Kubeconfig updated" and exits successfully — while `kubectl`
has nothing to talk to. Create at least one Project (step 7) before testing
tenant login.
:::

### Adopt break-glass credentials — do this now

A long-lived, SOPS-encrypted, cluster-admin token committed to your fleet repo.
It must be created **while your certificate access still works**; if you wait
until identity is broken it is too late to make one.

```bash
kube-dc bootstrap break-glass adopt dc1 --repo "$KUBE_DC_FLEET"
cd "$KUBE_DC_FLEET" && git add -A && git commit -m "break-glass for dc1" && git push

# Later, when you need it:
#   kube-dc bootstrap break-glass use dc1 --repo "$KUBE_DC_FLEET"
#   kube-dc bootstrap break-glass status dc1 --repo "$KUBE_DC_FLEET"
```

### Which API endpoint do tenants' controllers use?

`MANAGEMENT_API_MODE` decides how platform controllers running **inside tenant
namespaces** (managed-K8s CCM/CSI, CloudNativePG, the cloud shell, the
cluster-autoscaler) reach the management API. It does **not** affect your or
your tenants' `kubectl`, which always uses `kube-api.<domain>:6443`.

Leave it at the default **`auto`**. The installer resolves it per topology:

- **`service`** whenever dual-homing is enabled and the cluster has a canonical
  `K8S_SERVICE_IP` inside `SVC_CIDR` (the normal case). Tenant controllers reach
  the apiserver's own in-cluster ClusterIP over the dual-home infra NIC — a
  `/32` route the platform injects into the pods that earn the
  `management-api-client` role. This is the only path that works when tenant
  networks are isolated from the outside, which they are on any private or
  single-ingress cluster.
- **`external`** only where a tenant-routable external endpoint genuinely
  exists. **Do not assume your workstation's reachability proves this** —
  `kubectl` from your laptop and a pod inside a tenant VPC are different routing
  domains. A tenant VPC is OVN-isolated from the node/service networks and often
  from the internet, so `kube-api.<domain>:6443` being reachable from outside
  says nothing about whether a tenant pod can reach it. Choosing `external` on a
  private cluster leaves every in-tenant controller with no route to the API
  (CNPG bootstrap hangs, managed-cluster CSI/CCM fail) — silently, because
  nothing else breaks.

Set it explicitly only to override the automatic choice.

---

## 7. Create the first tenant and verify

An Organization lives in a namespace named after **itself**, not in `kube-dc`,
and a Project **requires** a `cidrBlock` (worksheet #11) — the API rejects it
without one.

```bash
kubectl create namespace acme

kubectl apply -f - <<'EOF'
apiVersion: kube-dc.com/v1
kind: Organization
metadata:
  name: acme
  namespace: acme
spec:
  description: First tenant
  email: admin@acme.example.com
---
apiVersion: kube-dc.com/v1
kind: Project
metadata:
  name: web
  namespace: acme
spec:
  # REQUIRED. Must not overlap your node network, the pod/service CIDRs, or
  # another Project — each Project is its own VPC subnet.
  cidrBlock: 10.90.0.0/16
  egressNetworkType: cloud
EOF

kubectl get organization acme -n acme         # Ready
kubectl get project web -n acme               # Ready
```

**Final acceptance.** One command checks the wiring that is machine-checkable:

```bash
kube-dc bootstrap accept dc1 --repo "$KUBE_DC_FLEET" --domain dc.example.com
```

It reports one of three states, and only the last means finished:

| State | Meaning | Exit |
|---|---|---|
| `reconciling` | Flux has not settled — wait and re-run | 2 |
| `converged` | Components are up but something a user would hit is broken | 1 |
| `usable` | Identity works, the front door is trusted, tenancy is installed | 0 |

The check worth knowing about is `identity/oidc-cutover`: it reads the flags every
`kube-apiserver` is **actually running with**, so it catches both a skipped step 5
and — more importantly — a *partial* one, whose symptom is intermittent 401s that
look like a Keycloak or clock problem.

:::note What `usable` does and does not prove
It proves the wiring: Flux settled, nodes and control planes present, every
apiserver calling the OIDC webhook, the console answering over a trusted
certificate, and the tenancy CRDs installed.

It does **not** authenticate a real token, create an Organization or Project, or
check Ceph health. So `usable` means "nothing known-broken stands between a user
and this cluster" — the tests below are what actually confirm they can use it.
Run them.
:::

Then confirm what only a real login can:

```bash
# 1. Front door serves a valid, publicly-trusted certificate (no -k)
curl -sSI https://console.dc.example.com | head -1

# 2. OIDC actually authenticates — this is what step 5 bought you
kube-dc login --domain dc.example.com --admin && kubectl auth whoami

# 3. A tenant gets a working, correctly-scoped context
kube-dc login --domain dc.example.com --org acme
kubectl auth can-i create pods                # yes, in their Project
kubectl auth can-i get nodes                  # NO — tenants are namespaced

# 4. Storage is real
kubectl get cephcluster -n rook-ceph          # HEALTH_OK
```

Then log into `https://console.dc.example.com` as the admin.

---

## If something goes wrong

Which phases are safe to simply re-run:

| Interrupted at | Re-run safe? | What to do |
|---|---|---|
| Workstation prep, DNS | Yes | Re-run freely |
| `bootstrap install` (RKE2) | Yes | Re-runs skip an already-active node. It now fails loudly if the service or API server never came up, instead of printing success |
| `fetch-kubeconfig` | Yes | Atomic merge |
| `bootstrap init` before it pushed | Inspect first | It commits locally and *then* pushes, so a kill in between leaves a clean local commit. `git -C "$KUBE_DC_FLEET" log -1` — a re-run pushes HEAD |
| `bootstrap init` after it pushed | Fix forward | Do **not** delete the repo. Fix the cause and re-run; it resumes |
| `init` killed mid-scaffold | Inspect first | Untracked files in the fleet repo trip the clean-tree gate — inspect and remove or commit them |
| `oidc-cutover` | Yes | Finished nodes are skipped. `--rollback` undoes it |
| Ceph device wipe | **No** | Destructive and not reversible. Verify the device list in the dry run |

Two things that are **not** convergence: changing `init` arguments against an
existing overlay does not re-apply most of them (edit the fleet repo instead),
and `bootstrap install --force` rewrites a node's RKE2 config (it now preserves
your API-server flags, including the OIDC cutover, but still restarts the node).

For anything else, start with:

```bash
kube-dc bootstrap status dc1 --repo "$KUBE_DC_FLEET"   # per-cluster deep view
flux get kustomizations             # what is not Ready, and why
```

and see [Troubleshooting](cluster-cli-troubleshooting.md) plus the
[Installation Guide](installation-guide.md#troubleshooting).
