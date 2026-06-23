# Upgrading a Management Cluster (RKE2)

This guide describes how to upgrade the Kubernetes version of a Kube-DC
**management cluster** (the RKE2 cluster that runs the controllers, KubeVirt,
Kube-OVN, Keycloak, and tenant workloads) using Rancher's
[system-upgrade-controller](https://github.com/rancher/system-upgrade-controller)
(SUC).

It is written to be cluster-agnostic. Substitute your own node names and target
versions.

## Why SUC

SUC turns a node-by-node RKE2 upgrade into a declarative `Plan`: you label the
nodes, set a target version, and the controller **cordons → upgrades → uncordons**
each node in a controlled, serialized order. This replaces error-prone manual
`curl … | INSTALL_RKE2_VERSION=… sh` runs and keeps the fleet consistent.

## Hard constraints — read first

| Constraint | What it means for you |
|---|---|
| **No minor-version skipping** | Kubernetes does not support jumping the control plane more than one minor at a time. Upgrade **one minor per pass** (e.g. `1.32 → 1.33 → 1.34`), not straight to the target. |
| **No downgrade** | RKE2 cannot be rolled back. If a pass breaks the API server or CNI, there is no easy recovery — **validate every pass before the next**. |
| **CNI / KubeVirt support windows** | Kube-OVN and KubeVirt are each certified against a range of Kubernetes versions. Before targeting a brand-new minor, confirm your installed Kube-OVN and KubeVirt versions support it. Don't outrun your data-plane. |
| **Single control-plane** | If the cluster has one server node, its RKE2 restart is a brief (~1–2 min) full API-server outage per pass. Plan for it. |

> **Picking a target.** Use the RKE2 **`stable`** channel, not `latest`
> (which is bleeding edge). Check current versions at
> <https://update.rke2.io/v1-release/channels>.

## Pre-flight checklist

Run these before starting — they prevent the most common mid-upgrade stalls:

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
  node's current free space — on a legitimately-full node it triggers
  `DiskPressure` immediately. Match RKE2's default (`nodefs.available<5%`) or
  looser on storage-dense nodes.
- **OpenBao re-seal.** OpenBao seals on every pod restart. If a node hosting the
  OpenBao pod is upgraded (or the pod reschedules), **OpenBao will need to be
  unsealed again**. Have your unseal procedure ready.
- **Quiesce / snapshot.** Take an etcd snapshot and note current component health
  so you have a clean baseline to compare against.

## Procedure

### 1. Install the controller (once per cluster)

Vendor the SUC release manifests (`crd.yaml` + `system-upgrade-controller.yaml`)
into your GitOps repo and apply them — in a Kube-DC fleet this is an
`infrastructure/system-upgrade-controller/` kustomization wired as its own Flux
`Kustomization`. The controller runs in the `system-upgrade` namespace and does
nothing until a `Plan` exists.

> Keep the **controller** in GitOps but apply the **upgrade `Plan`s manually** —
> a GitOps-managed Plan would re-apply on every reconcile and could re-run.

### 2. Opt nodes in

```bash
kubectl label node <all-nodes> rke2-upgrade=true --overwrite
```

### 3. Apply the Plans (first minor)

Two Plans — one for control-plane (`rke2-server`) and one for workers
(`rke2-agent`). The agent Plan's `prepare` step blocks until the server Plan
finishes, so the control plane always upgrades first. Both use
`concurrency: 1` (one node at a time).

```yaml
apiVersion: upgrade.cattle.io/v1
kind: Plan
metadata: { name: rke2-server, namespace: system-upgrade }
spec:
  concurrency: 1
  cordon: true
  nodeSelector:
    matchExpressions:
      - { key: rke2-upgrade, operator: In, values: ["true"] }
      - { key: node-role.kubernetes.io/control-plane, operator: In, values: ["true"] }
  serviceAccountName: system-upgrade
  tolerations: [ { operator: Exists } ]
  upgrade: { image: rancher/rke2-upgrade }
  version: v1.33.12+rke2r2          # <-- next minor, not the final target
---
apiVersion: upgrade.cattle.io/v1
kind: Plan
metadata: { name: rke2-agent, namespace: system-upgrade }
spec:
  concurrency: 1
  cordon: true
  nodeSelector:
    matchExpressions:
      - { key: rke2-upgrade, operator: In, values: ["true"] }
      - { key: node-role.kubernetes.io/control-plane, operator: NotIn, values: ["true"] }
  serviceAccountName: system-upgrade
  tolerations: [ { operator: Exists } ]
  prepare: { args: ["prepare", "rke2-server"], image: rancher/rke2-upgrade }
  upgrade: { image: rancher/rke2-upgrade }
  version: v1.33.12+rke2r2
```

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
controller uncordons each node when its job succeeds.

### 5. Validate before the next minor

```bash
kubectl get nodes                                   # all Ready, all on the new minor
kubectl -n kube-system get pods | grep -E 'kube-ovn|ovs|multus'   # CNI healthy
kubectl get vmi -A                                  # VMs still Running
kubectl get pods -A | grep -vE 'Running|Completed'  # nothing stuck
```

If healthy, advance both Plans to the next minor — SUC re-runs because the
version hash changes:

```bash
NEXT=v1.34.8+rke2r2
kubectl -n system-upgrade patch plan rke2-server --type=merge -p "{\"spec\":{\"version\":\"$NEXT\"}}"
kubectl -n system-upgrade patch plan rke2-agent  --type=merge -p "{\"spec\":{\"version\":\"$NEXT\"}}"
```

Repeat until you reach the final target. When done, you can clear the target:

```bash
kubectl -n system-upgrade delete plan rke2-server rke2-agent
```

## KubeVirt VMs: cordon-only vs drain

The example Plans above are **cordon-only** (no `drain:`). This is the right
default when VMs are **not** live-migratable (`evictionStrategy: None`): a drain
would evict their `virt-launcher` pods and stop the VMs, whereas cordon-only
leaves the containerd-managed VM pods running across the in-place rke2 restart
(`rke2-upgrade` swaps the binary, it does not reboot the host).

If your VMs **are** live-migratable, add a drain to the agent Plan for a cleaner
roll:

```yaml
  drain: { force: true, skipWaitForDeleteTimeout: 60 }
```

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
  survives the binary swap — no re-cutover needed.
