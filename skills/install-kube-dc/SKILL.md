---
name: install-kube-dc
description: Plan and install a new Kube-DC platform on prepared servers with the kube-dc CLI. Gathers the site facts, checks hosts, disks and DNS, proposes the network, front door, Ceph storage, domain and certificate design, then runs bootstrap install, init and accept. Operator skill; needs SSH access to the servers and a Git repository for the fleet.
---

## When to Use This Skill

Use it to install Kube-DC on new servers: from prepared Linux hosts to a
cluster that `kube-dc bootstrap accept` reports `usable`.

Do not use it for:

- Tenant work on an existing installation (projects, VMs, databases): use the
  tenant skills (`create-project`, `create-vm`, `create-database`, ...).
- Upgrading or changing an existing installation. Its fleet repository is the
  source of truth; change it through reviewed Git commits.

The authoritative reference is the
[installation guide](https://github.com/kube-dc/kube-dc-public/blob/main/docs/platform/installation-guide.md).
This skill is the decision path through it. Decision tables:
@decisions.md. Host checks: @host-checks.md. Input file template:
@install-input.env.example.

## Ground Rules

- **Ask, then act.** Propose each design decision with the reason and let the
  operator confirm it. Addresses, VLANs, disks and DNS records belong to the
  site; never guess one and never treat a calculated address as proven free.
- **Read before you write.** Every host check in this skill is read-only.
  Change a host only when the operator approved that exact change.
- **Never erase a disk on your own.** A disk for Ceph must already be empty.
  If it is not, report what is on it and stop; the operator decides.
- **No secrets in files, commands or output.** Git tokens come from `gh` or
  `glab` auth; TLS keys, CA bundles and DNS-01 credentials are file paths or
  environment variables. Never print a key, token, password or unseal share.
- **Pinned releases only.** Use a released, checksum-verified CLI and an
  immutable starter reference (a digest or a version tag), never `latest`.
- **Preview first.** Run `bootstrap install` and `bootstrap init` with
  `--dry-run` and review the plan before the real run.
- **Generated files are not edited.** `init` writes `clusters/<name>/` once;
  running it again resumes, it does not regenerate. Get the inputs right
  before the first push (step 7).

## Prerequisites

- Servers with a supported OS. Ceph object storage needs Linux 7.0 or later on
  every node (Ubuntu 26.04, or Ubuntu 24.04 with the HWE kernel); `init`
  refuses Ceph on an older kernel.
- A workstation or bastion with SSH access to every server (key in the agent,
  passwordless sudo on the servers), `git`, `curl`, `jq`, `ssh`, Bash 4.4 or
  later first in `PATH` (on macOS, Homebrew's: `brew install bash`), Python 3 with
  PyYAML (`python3 -c 'import yaml'` must succeed) and `kubectl`. From the first `init` run on, the CLI keeps its pinned kubectl,
  flux, helm, sops and age in `~/.kube-dc/tools/bin`; put that first in `PATH`
  for the commands below (`export PATH="$HOME/.kube-dc/tools/bin:$PATH"`).
- A Git host for the fleet repository: GitHub or GitLab (self-managed works),
  authenticated with `gh auth login` or `glab auth login`.
- A DNS zone you control for the installation domain.
- Outbound HTTPS from the servers to the image registries (Docker Hub, GHCR,
  Quay, registry.k8s.io), or a mirror.

## Steps

### 1. Gather the site facts

Collect these before proposing anything. Ask for what is unknown.

| Fact | Why it matters |
|---|---|
| Server list: hostname, role, management/SSH address, internal (node) address | Control-plane count (1 or 3), workers, `KUBE_OVN_MASTER_NODES` |
| CPU, memory, disks per server | Sizing, Ceph OSD candidates, local-path for platform state |
| NICs, bonds, VLANs per server; MTU | Kube-OVN external network and per-node NIC mapping |
| External (cloud) network: VLAN, CIDR, gateway, addresses reserved by the site | Tenant external IPs, SNAT, exclusions |
| Optional public network: VLAN, CIDR, gateway | `cloud+public-vlan` preset |
| How users reach the platform: a spare VIP on a shared L2 segment, BGP peering, node addresses, or 1:1 NAT | Front door (see @decisions.md) |
| Domain, who runs its DNS, public or private | DNS records and the TLS mode |
| Certificate: public ACME, DNS-01 provider, or a supplied wildcard and CA | TLS mode |
| Internal ranges already used on the site network | Pod, Service, join and infra ranges must not overlap them |
| Fleet repository: new, existing empty repository, or existing fleet | `--fleet-mode` |
| Wanted features: VMs, managed services, GPUs | Storage budget, VM storage, accelerators, and which acceptance checks apply |

### 2. Prepare the workstation

```bash
set -e
v=vX.Y.Z   # the released CLI version for this installation
curl -fsSLO "https://github.com/kube-dc/kube-dc-public/releases/download/$v/kube-dc_linux_amd64"
curl -fsSLO "https://github.com/kube-dc/kube-dc-public/releases/download/$v/checksums.txt"
grep ' kube-dc_linux_amd64$' checksums.txt | sha256sum -c -   # stop here if this fails
mkdir -p "$HOME/.local/bin" && export PATH="$HOME/.local/bin:$PATH"
install -m 0755 kube-dc_linux_amd64 "$HOME/.local/bin/kube-dc"
kube-dc version
```

The release notes name the platform bundle (starter) the CLI installs.
Authenticate the Git host (`gh auth status`, or
`glab auth status --hostname HOST`).

### 3. Check every host

Run the read-only checks in @host-checks.md on every server and record the
results. Start with SSH reach and sudo:

```bash
kube-dc bootstrap connect user@server-1
```

Stop and report when a host fails: kernel too old for Ceph, swap on, no
`/dev/kvm` on a host that should run VMs, a Ceph candidate disk that is not
empty or not healthy, the installation domain in the resolver search list, or
no outbound access.

### 4. Propose the design

Work through @decisions.md with the operator, in this order, and write the
agreed values into a copy of @install-input.env.example:

1. Roles: control-plane nodes (three for HA), workers, gateway and ingress
   nodes, manager nodes for platform state.
2. Network: preset, external network, exclusions, internal ranges.
3. Front door: address layer and VIP, and which class the layout is.
4. DNS records.
5. Certificates: TLS mode, private CA.
6. Storage: object storage mode, OSD disks and copies, monitors, VM disks,
   platform state, managed services class and budget, against a capacity
   worksheet.
7. Fleet repository mode and the pinned starter.

`chmod 600` the input file and keep secret values out of it.

### 5. Create the DNS records

Point `*.DOMAIN` and `kube-api.DOMAIN` at the front-door address (the VIP, or
the ingress node address with layer `none`). Check from the workstation and
from a server:

```bash
getent hosts console.DOMAIN kube-api.DOMAIN
ssh user@server-1 'getent hosts login.DOMAIN s3.DOMAIN'
```

Expected: every name returns the front-door address.

### 6. Install RKE2

First server, then each further node, one at a time. Preview each with
`--dry-run`:

```bash
kube-dc bootstrap install server-1 --ssh-host user@server-1 \
  --domain DOMAIN --node-ip NODE_IP_1 --preset PRESET \
  [--trusted-ca-bundle CA.pem] [--set POD_CIDR=... --set SVC_CIDR=... --set CLUSTER_DNS=...]

kube-dc bootstrap install server-2 --ssh-host user@server-2 --node-ip NODE_IP_2 \
  --join-server user@server-1 --cp-host NODE_IP_1 --role server \
  --domain DOMAIN --preset PRESET [same --trusted-ca-bundle and --set values]
# workers: --role worker
```

Use the same preset, CA bundle and range overrides on every node and in the
input file. Then fetch the admin kubeconfig from the first server (before the
VIP exists, address it directly):

```bash
export KUBECONFIG="$HOME/.kube/NAME.yaml"
kube-dc bootstrap fetch-kubeconfig NAME --ssh-host user@server-1 --domain DOMAIN \
  --server https://NODE_IP_1:6443 --kubeconfig "$KUBECONFIG" --set-current
kubectl get nodes -o wide     # every node listed; NotReady until the CNI is installed
```

### 7. Review the plan

```bash
kube-dc bootstrap init --config NAME.env --repo FLEET_DIR --dry-run --no-tty
kube-dc bootstrap init --config NAME.env --repo FLEET_DIR --no-push --no-tty --yes
```

The first prints the plan. The second writes and commits the overlay in the
local fleet checkout without pushing or touching the cluster, and re-checks
DNS, kernels and every selected OSD disk. Review with the operator:

- the starter reference, cluster name, domain, preset and `0 destructive gates`;
- the front-door line and, for a node-network VIP, the `Project front door`
  line with its internal VIP and anchors (reserve those addresses);
- object storage mode, OSD list, copies and the default StorageClass;
- managed services mode, class and budget;
- every `[scaffold]` decision line and every `WARNING`.

If an input must change, nothing has been pushed yet: fix the input file,
move FLEET_DIR aside (keep it until the installation is done), clone the
repository again (or start from a new empty directory for `new-repo`) and run
the `--no-push` step there. For `new-repo` and `existing-repo`, `init` imports
the starter and creates a new age key there. For `existing-fleet`, copy the
fleet's enrolled `age.key` into the new clone first: that mode never creates
one. Do not rewind the old checkout: its starter import, SOPS enrollment and
`age.key` belong together. After the push, changes are
reviewed Git commits.

A run that stopped before it wrote `clusters/<name>` (in the starter import or
the age-key step) needs no new clone: fix the cause and the input, and run the
same command in the same checkout. With CLI v0.9.19 or later, `init` replaces a
starter the input no longer selects (or whose tag moved) and completes an
interrupted key enrollment there. Once `clusters/<name>` exists, it keeps the
starter the installation was made from.

Instead of `--config`, `kube-dc bootstrap init` with no flags opens the
interactive panel with the same inputs (sections Basics, Fleet, Network,
Certificates, Storage, Accelerators, Gates); press `S` to save them as an
input file.

### 8. Install

```bash
kube-dc bootstrap init --config NAME.env --repo FLEET_DIR --no-tty --yes
```

Run it under `tmux` or `screen`. It pushes the overlay, bootstraps Flux,
follows reconciliation and finalizes: OpenBao initialization and unseal,
Keycloak clients, the API servers' OIDC cutover over SSH, and managed
services. If it stops (timeout, dropped session, "action required for
reconcile"), wait until `flux get kustomizations -A` is Ready and run the same
command again: it resumes, it does not re-create.

Tell the operator to move into their custody: the age key (`age.key` in the
fleet checkout), the RKE2 token (`/var/lib/rancher/rke2/server/token` on the
first server) and the admin kubeconfig. OpenBao's unseal shares are committed
encrypted with the age key.

Once the platform is up, fetch the kubeconfig again without `--server`, so it
uses `kube-api.DOMAIN`.

### 9. Accept

```bash
kube-dc bootstrap accept NAME --repo FLEET_DIR --domain DOMAIN; echo "exit=$?"
flux get kustomizations -A
kubectl -n rook-ceph get cephcluster    # with a Ceph object storage mode
```

Expected: `state: usable` with every required check `PASS` and `exit=0`;
every Kustomization Ready; with Ceph, `HEALTH_OK` and the planned OSD count.

Then prove, in a test Organization and Project (console at
`https://console.DOMAIN`), the features this installation includes: a Pod
resolves names and reaches the internet; with S3 exposed, it gets HTTP 200
from `https://s3.DOMAIN/`; if VMs are in scope, a VM starts and is reachable over
SSH; if managed services are on, a managed PostgreSQL becomes Ready, accepts a
connection and completes a backup. Record what is out of scope.

## Verification

**Success**: `accept` reports `usable` and the in-scope checks of step 9
pass.

**Failure**:

- `reconciling`: Flux has not settled. `flux get all -A`, wait, ask again.
- `converged`: a user-facing check failed; `accept` names it. Common causes:
  wildcard DNS not resolving from the servers, a certificate not trusted
  (private CA not passed to `install` and `init`), the wildcard certificate
  not issued (plain `acme` cannot issue it; see @decisions.md), OIDC cutover
  deferred (`init` printed the command to run).
- A Project Pod cannot reach `s3.DOMAIN` while a VM can: the front door is on
  an address the Pod dials over its locked infra interface. See Class D in
  @decisions.md and
  [How Project Pods reach the platform](https://github.com/kube-dc/kube-dc-public/blob/main/docs/platform/internal-platform-endpoints.md#how-project-pods-reach-the-platform).
- No OSDs: a disk was not empty or belonged to an earlier Ceph cluster; see
  @host-checks.md.

## Safety

- `bootstrap init` is resumable. Never delete the fleet checkout, the age key
  or a partly finalized OpenBao to "start clean" without the operator.
- A disk reused from another Ceph cluster needs Ceph's own zap and
  verification (@host-checks.md), never a judgement from `lsblk`.
