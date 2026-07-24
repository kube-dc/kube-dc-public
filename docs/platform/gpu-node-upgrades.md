# GPU node upgrade gate

GPU nodes add a coupled compatibility boundary: the PCI device, host kernel,
RKE2/Kubernetes, NVIDIA driver, GPU Operator, and DCGM exporter must be validated as one
tuple. Upstream support for each component independently is not sufficient.

`kube-dc bootstrap gpu upgrade-check` is the read-only gate immediately before
an operator creates or changes an RKE2 System Upgrade Controller (SUC) Plan.
It never labels, cordons, drains, restarts, patches, or applies resources.

## Required sequence

1. Keep GPU billing and tenant Pod/VM creation closed. Inventory GPU Pod and
   VMI holders; do not change ownership or drivers under a holder.
2. Select a single canary node on matching hardware. On a one-node GPU pool,
   use an approved maintenance window and accept that GPU capacity is offline.
3. Qualify the exact target tuple on that canary. Prove a real allocation,
   scheduler/plugin and monitoring health, and rollback to the prior tuple.
4. Commit the evidence and have a second operator approve it. Record the full
   evidence revision in a `GPUUpgradeQualification` document.
5. Run the CLI gate with values collected from the host and Kubernetes API.
   A PASS permits only the reviewed serialized canary/upgrade plan. It is not
   blanket authorization for parallel node upgrades.
6. Stop on any allocation, XID/ECC/thermal, plugin, scheduler, webhook, Node
   readiness, or platform-health regression. Leave a failed node cordoned and
   execute the proven rollback.
7. After every DRA GPU node, run `kube-dc bootstrap gpu dra postflight
   --kubeconfig "$KUBECONFIG"`, rerun a real GPU allocation, and confirm alerts
   clear before continuing. If postflight exposes an old unready driver Pod,
   use the separate read-only `dra recovery-plan`; never delete a Pod merely
   because postflight failed. GPU-backed VMs require an explicit stop/start or
   approved evacuation plan; they are not live-migratable.

The general RKE2 procedure remains in
[Upgrading a Management Cluster](upgrading-management-cluster.md). GPU nodes
must pass this additional gate before entering that procedure.

## Qualification record

The parser rejects unknown fields, multiple YAML documents, records larger
than 64 KiB, incomplete evidence, future/stale timestamps, and records older
than 30 days by default. Hardware uses the exact
`vendor:device/subvendor:subdevice` identity from accelerator discovery.

```yaml
apiVersion: kube-dc.io/v1alpha1
kind: GPUUpgradeQualification
id: v100-kernel-canary-1
state: qualified
approvedBy: platform-review
sourceRevision: aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa
hardware:
  - 10de:1db6/10de:124a
target:
  kernel: 6.8.0-135-generic
  rke2: v1.36.1+rke2r1
  driver: 580.130.00
  gpuOperator: v26.3.4
  dcgmExporter: 4.4.1-4.6.0-ubuntu22.04
canary:
  completedAt: 2026-07-14T12:00:00Z
  allocationPassed: true
  monitoringPassed: true
  rollbackPassed: true
  evidence: change-record-42
```

The values above illustrate the schema only; they are not a published support
claim. A target is supported only by the reviewed record for that exact fleet.

## Gate invocation

```bash
kube-dc bootstrap gpu upgrade-check \
  --qualification ./gpu-upgrade-qualification.yaml \
  --pci-id 10de:1db6/10de:124a \
  --current-kernel 6.8.0-134-generic \
  --target-kernel 6.8.0-135-generic \
  --current-rke2 v1.35.3+rke2r3 \
  --target-rke2 v1.36.1+rke2r1 \
  --current-driver 580.126.20 \
  --target-driver 580.130.00 \
  --current-gpu-operator v26.3.3 \
  --target-gpu-operator v26.3.4 \
  --current-dcgm-exporter 4.4.1-4.6.0-ubuntu22.04 \
  --target-dcgm-exporter 4.4.1-4.6.0-ubuntu22.04
```

The command blocks RKE2 downgrades and Kubernetes-minor skips even if a record
claims they passed. Capture its PASS output with the maintenance change. Never
use `--max-canary-age` to extend stale evidence for convenience; rerun the
canary when the tuple or environment has changed.

## Rollback boundary

- RKE2 itself is not treated as safely downgradeable. Validate each one-minor
  hop before continuing and restore from the platform recovery procedure if
  the control plane cannot recover.
- Driver/kernel rollback is allowed only when the qualification record proves
  that exact rollback and all GPU holders are stopped.
- Do not uncordon until accelerator doctor, a real allocation, GPU alerts, and
  the normal cluster health checks all pass.
- A postflight failure leaves the node/upgrade wave stopped. The bounded stale
  driver recovery planner requires a Ready node, empty DRA inventory, degraded
  DaemonSet readiness, and a current-DaemonSet-owned Pod unready for at least
  ten minutes before it prints a candidate; it never performs the deletion.
- The upgrade does not grant or alter GPU add-ons, project caps, HRQ, billing,
  or tenant creation gates.
