# Upgrade a management cluster (RKE2)

This guide describes how to upgrade the Kubernetes version of a Kube-DC
**management cluster** (the RKE2 cluster that runs the controllers, KubeVirt,
Kube-OVN, Keycloak, and tenant workloads) using Rancher's
[system-upgrade-controller](https://github.com/rancher/system-upgrade-controller)
(SUC).

It is written to be cluster-agnostic. Substitute your own node names and target
versions.

## Why SUC

SUC turns a node-by-node RKE2 upgrade into a declarative `Plan`: you label the
nodes, set a target version, and the controller cordons, drains, upgrades, and
uncordons each node in a controlled, serialized order. This replaces error-prone manual
`curl … | INSTALL_RKE2_VERSION=… sh` runs and keeps the fleet consistent.

## Hard constraints: read these first

| Constraint | What it means for you |
|---|---|
| **No minor-version skipping** | Kubernetes does not support jumping the control plane more than one minor at a time. Upgrade **one minor per pass** (for example, `1.35 → 1.36`, then `1.36 → 1.37`), not straight to the target. |
| **Drain before rke2 restarts** | No KubeVirt VM may run on a node while rke2 restarts on it. Every Plan on this page drains the node first. See [KubeVirt VMs and the drain rule](#kubevirt-vms-and-the-drain-rule). |
| **No downgrade** | RKE2 cannot be rolled back. If a pass breaks the API server or CNI, there is no easy recovery. **Validate every pass before the next.** |
| **CNI / KubeVirt support windows** | Kube-OVN and KubeVirt are each certified against a range of Kubernetes versions. Before targeting a brand-new minor, confirm your installed Kube-OVN and KubeVirt versions support it. Don't outrun your data-plane. |
| **Single control-plane** | If the cluster has one server node, its RKE2 restart is a brief (~1–2 min) full API-server outage per pass. Plan for it. |

> **Picking a target.** Use the RKE2 **`stable`** channel, not `latest`
> (which is bleeding edge). Check current versions at
> [update.rke2.io/v1-release/channels](https://update.rke2.io/v1-release/channels).

## Pre-flight checklist

Run these before you start. They prevent the most common mid-upgrade stalls:

- **Node disk headroom.** Each upgrade pulls the new RKE2 release (~1–2 GiB) onto
  every node. Ensure each node has comfortable free space; a node near its disk
  limit can trip `DiskPressure`, which evicts pods and **blocks the upgrade job
  from scheduling**. Storage-dense nodes (large `local-path` PVCs) are the usual
  offenders.
- **`max-pods` consistency.** A node that is at its `max-pods` cap cannot
  schedule the SUC upgrade pod (`FailedScheduling … Too many pods`). Confirm
  every node has the same, adequate `max-pods` (Kube-DC's bootstrap sets it by
  memory tier; nodes installed by older tooling may be on the default 110).
- **Eviction thresholds.** Never set a disk-eviction threshold *tighter* than a
  node's free space, so on a legitimately full node it triggers
  `DiskPressure` immediately. Match RKE2's default (`nodefs.available<5%`) or
  looser on storage-dense nodes.
- **OpenBao re-seal.** OpenBao seals on every pod restart. If a node hosting the
  OpenBao pod is upgraded (or the pod reschedules), **OpenBao will need to be
  unsealed again**. Have your unseal procedure ready.
- **Quiesce / snapshot.** Take an etcd snapshot and note current component health
  so you have a clean baseline to compare against.

## Procedure

### 1. Install the controller (once per cluster)

The fleet starter ships the controller as the shared component
`infrastructure/system-upgrade-controller/`. Its kustomization applies only
the SUC v0.19.2 controller and the `Plan` CRD. Do not use
`plans/rke2-upgrade-plans.yaml` from that directory: its Plans do not drain the
node. Use the Plans on this page instead. `bootstrap init` does not wire
this component, so you add it to your cluster yourself. Do not edit the shared
tree. Reference it from your cluster directory instead.

Create `clusters/<name>/system-upgrade.yaml` with this Flux `Kustomization`:

```yaml
apiVersion: kustomize.toolkit.fluxcd.io/v1
kind: Kustomization
metadata:
  name: infra-system-upgrade
  namespace: flux-system
spec:
  dependsOn:
    - name: infra-cni
  interval: 30m
  retryInterval: 5m
  timeout: 5m
  path: ./infrastructure/system-upgrade-controller
  prune: true
  sourceRef:
    kind: GitRepository
    name: flux-system
```

`<name>` is your cluster name. Add `system-upgrade.yaml` to the `resources:`
list in `clusters/<name>/kustomization.yaml`, then commit and push. After Flux
reconciles, the controller runs in the `system-upgrade` namespace. It does
nothing until a `Plan` exists.

> Keep the **controller** in GitOps, but apply the **upgrade `Plan`s by hand**.
> A GitOps-managed Plan would re-apply on every reconcile and could re-run.

### 2. Opt nodes in

```bash
kubectl label node <all-nodes> rke2-upgrade=true --overwrite
```

### 3. Apply the plans (first minor)

There are two Plans, one for the control plane (`rke2-server`) and one for workers
(`rke2-agent`). The agent Plan's `prepare` step blocks until the server Plan
finishes, so the control plane always upgrades first. Both use
`concurrency: 1` (one node at a time), and both drain each node before the
upgrade. The example starts from the `v1.35.9+rke2r1` baseline that
`kube-dc bootstrap install` installs, so the first pass targets 1.36.

Save the Plans as `rke2-upgrade-plans.yaml`:

```yaml
apiVersion: upgrade.cattle.io/v1
kind: Plan
metadata: { name: rke2-server, namespace: system-upgrade }
spec:
  concurrency: 1
  cordon: true
  drain: { ignoreDaemonSets: true, deleteEmptydirData: true, force: true, skipWaitForDeleteTimeout: 60 }
  nodeSelector:
    matchExpressions:
      - { key: rke2-upgrade, operator: In, values: ["true"] }
      - { key: node-role.kubernetes.io/control-plane, operator: In, values: ["true"] }
  serviceAccountName: system-upgrade
  tolerations: [ { operator: Exists } ]
  upgrade: { image: rancher/rke2-upgrade }
  version: v1.36.3+rke2r1           # <-- next minor, not the final target
---
apiVersion: upgrade.cattle.io/v1
kind: Plan
metadata: { name: rke2-agent, namespace: system-upgrade }
spec:
  concurrency: 1
  cordon: true
  drain: { ignoreDaemonSets: true, deleteEmptydirData: true, force: true, skipWaitForDeleteTimeout: 60 }
  nodeSelector:
    matchExpressions:
      - { key: rke2-upgrade, operator: In, values: ["true"] }
      - { key: node-role.kubernetes.io/control-plane, operator: NotIn, values: ["true"] }
  serviceAccountName: system-upgrade
  tolerations: [ { operator: Exists } ]
  prepare: { args: ["prepare", "rke2-server"], image: rancher/rke2-upgrade }
  upgrade: { image: rancher/rke2-upgrade }
  version: v1.36.3+rke2r1
```

Apply the Plans:

```bash
kubectl apply -f rke2-upgrade-plans.yaml
```

### 4. Watch the pass

```bash
kubectl -n system-upgrade get plans
kubectl -n system-upgrade get jobs,pods
kubectl get nodes -o wide        # kubeletVersion flips per node as each finishes
```

Nodes cycle `Ready,SchedulingDisabled` → `NotReady` (brief) → `Ready`. The
drain runs while the node is `SchedulingDisabled`. A drain waits while a
PodDisruptionBudget refuses an eviction. The controller uncordons each node
when its job succeeds.

### 5. Validate before the next minor

```bash
kubectl get nodes                                   # all Ready, all on the new minor
kubectl -n kube-system get pods | grep -E 'kube-ovn|ovs|multus'   # CNI healthy
kubectl get vmi -A                                  # VMs Running again
kubectl get pods -A | grep -vE 'Running|Completed'  # nothing stuck
```

If it is healthy, advance both Plans to the next minor. SUC re-runs, because the
version hash changes:

```bash
NEXT=<next-version>
kubectl -n system-upgrade patch plan rke2-server --type=merge -p "{\"spec\":{\"version\":\"$NEXT\"}}"
kubectl -n system-upgrade patch plan rke2-agent  --type=merge -p "{\"spec\":{\"version\":\"$NEXT\"}}"
```

`<next-version>` is an RKE2 release of the next minor from the `stable`
channel. Repeat until you reach the final target. When done, you can clear the target:

```bash
kubectl -n system-upgrade delete plan rke2-server rke2-agent
```

## KubeVirt VMs and the drain rule

Drain a node before rke2 restarts on it, so that no KubeVirt VM runs there.
This rule is mandatory. Do not remove the `drain:` field from the Plans.
Restarting rke2 under running VMs hard-kills QEMU. It can also race the CSI
node plugin into unmapping an RBD device under a live mount, and the node can
wedge hours later. For the failure signature and the recovery, see
[Base install with kube-dc commands only](private-ca-enterprise-install.md#1-base-install-with-kube-dc-commands-only)
in the enterprise install guide.

The drain treats VMs by their eviction strategy:

- **`evictionStrategy: LiveMigrate`**: the drain live-migrates the VM to
  another node. The Project needs free CPU quota for the second copy during
  the migration. See [VM storage tiers](vm-storage-tiers.md).
- **No live migration**: the drain evicts the `virt-launcher` pod, and the VM
  stops. A VM with `runStrategy: Always` starts again on another node.

To control when a VM that cannot migrate goes down, stop it before the pass and
start it after the node is `Ready`.

## If a node's upgrade job won't schedule

Symptom: the node stays on the old version and
`kubectl -n system-upgrade get pods` shows the apply pod `Pending` or `Evicted`.

- `FailedScheduling … Too many pods` → the node is at its `max-pods` cap. Raise
  `max-pods` (kubelet arg) and restart the agent, or free a slot.
- `Pod was rejected … DiskPressure` → the node is out of disk. Free space (image
  prune helps only marginally if the bulk is PVC data) and/or relax the disk
  eviction threshold to no tighter than current free space.
- A previous attempt left a **Failed Job** that SUC is backing off from. Delete
  the stale Job so the controller recreates it:
  `kubectl -n system-upgrade delete job <apply-…-job>`.

## Post-upgrade

- Confirm all nodes report the target version and are `Ready`.
- Re-check CNI, KubeVirt VMs, and every operator pod.
- **Re-unseal OpenBao** if its pod restarted.
- The OIDC-webhook API-server flag lives in `/etc/rancher/rke2/config.yaml` and
  survives the binary swap, and no re-cutover is needed.
