# HAMi hardware suite

This suite exercises the approved lab's real Shared GPU path without granting
an Organization or Project any product entitlement. It creates an ordinary
`ResourceQuota` and Pods only in a new disposable namespace.

Every entrypoint requires both explicit markers below, even when `KUBECONFIG`
is already set. This prevents a developer or generic CI runner from reaching
the cluster merely by invoking a script path:

```bash
export KUBE_DC_GPU_HARDWARE_TESTS=acknowledge-hardware-mutation
export GPU_HARDWARE_TARGET=gpu-lab
```

It proves:

- three-dimensional count, memory, and core quota rejection and release;
- three concurrent fractional CUDA holders, including exact HAMi allocation
  annotations and a pinned `nvidia-smi` image;
- physical-device sharing across the configured device count;
- full-capacity queueing and successful allocation after one holder releases;
- steady-state HAMi `gpucores` behavior with the upstream v2.9.0 PyTorch
  workload and a digest-pinned image;
- digest-pinned NVIDIA CUDA base, CUDA 12.5 vectorAdd, and PyTorch 2.4/CUDA
  12.1 application compatibility with exact allocation and quota release;
- a PyTorch allocation above the 4096-MiB virtual GPU limit is rejected while
  the CUDA context remains usable for a small follow-up allocation;
- serialized execution, refusal to overlap active GPU holders, and cleanup on
  every exit path.

Run only on an explicitly approved non-production cluster:

```bash
KUBECONFIG=/path/to/lab-kubeconfig \
GPU_HARDWARE_NAMESPACE=gpu-hardware-validation-001 \
GPU_HARDWARE_SUSPEND_CANARY=true \
KUBE_DC_GPU_HARDWARE_TESTS=acknowledge-hardware-mutation \
GPU_HARDWARE_TARGET=gpu-lab \
tests/e2e/gpu/hardware/run-hami-hardware-suite.sh
```

Run the separate core-throttle measurement only when the pool is idle:

```bash
KUBECONFIG=/path/to/lab-kubeconfig \
GPU_THROTTLE_NAMESPACE=gpu-throttle-validation-001 \
tests/e2e/gpu/hardware/run-hami-core-throttle.sh
```

The throttle test suspends and restores the allocation canary, waits 120 seconds
for HAMi's utilization watcher to converge, samples only the allocated physical
UUID through the host driver Pod, and requires a 15–45% average for a requested
30% cap. The warm-up is not an isolation guarantee: the V100 qualification
observed a startup burst and a pure-cuBLAS workload that remained above the cap.
Those limitations keep B-004/D-003 open and must stay visible to users.

Run the frozen-plugin detection/recovery drill separately:

```bash
KUBECONFIG=/path/to/lab-kubeconfig \
tests/e2e/gpu/hardware/run-hami-plugin-outage.sh
```

The outage drill uses the same atomic lock and idle-pool preflight. It suspends
the schedule (not the canary template), checks the exact process and container
cgroup before `STOP`, creates a matching manual canary, requires
`KubeDcHamiAllocationCanaryStuck` to fire through the Prometheus API, then sends
guarded `CONT`. Both the blocked and a fresh recovery canary must complete, the
alert must clear, and allocatable shares must be restored. The exit trap attempts
`CONT` before any other cleanup and replaces the DaemonSet Pod if guarded resume
is impossible.

Run the pinned application-image matrix separately:

```bash
KUBECONFIG=/path/to/lab-kubeconfig \
GPU_IMAGE_MATRIX_NAMESPACE=gpu-image-matrix-001 \
tests/e2e/gpu/hardware/run-hami-image-matrix.sh
```

The image matrix verifies a CUDA base image through `nvidia-smi`, NVIDIA's CUDA
12.5 vectorAdd sample, and PyTorch 2.4/CUDA 12.1 allocation plus matrix multiply.
It also uses the pinned PyTorch image to require CUDA OOM above 4096 MiB and a
successful small allocation afterward. All images are digest-pinned. Each Pod
must receive an exact 4096-MiB/25% allocation, observe the 4096-MiB virtual GPU
view where applicable, succeed, and release all three quota dimensions. This is a qualified starter matrix, not a
claim that arbitrary CUDA, Alpine/musl, or nested-container images are supported.

Run the dedicated KubeVirt/VFIO lifecycle suite only after the fleet has entered
the holder-safe `vm-passthrough` node mode:

```bash
KUBECONFIG=/path/to/lab-kubeconfig \
tests/e2e/gpu/hardware/run-vfio-vm-lifecycle.sh
```

The VFIO suite requires exactly two allowlisted V100 resources and an idle pool.
It proves a launcher Pod carries the exact external resource and VMI ownership,
libvirt attaches the PCI device through `vfio`, stop/start produces a new Ready
VMI, two VMs exclusively consume both devices, a third queues and starts after
release, and KubeVirt rejects live migration as `HostDeviceNotLiveMigratable`.
It does not install a guest NVIDIA driver; guest CUDA qualification is a separate
G0-T08/G6-T09 gate. The suite uses the same atomic hardware lock as HAMi tests.

Run the stable-v1 DRA plus Tenant Networking v2 acceptance only against an
existing, explicitly approved dual-homed project with at least one free DRA
entitlement unit:

```bash
KUBECONFIG=/path/to/lab-kubeconfig \
GPU_DRA_DUAL_HOME_NAMESPACE=approved-project \
KUBE_DC_GPU_HARDWARE_TESTS=acknowledge-hardware-mutation \
GPU_HARDWARE_TARGET=gpu-lab \
tests/e2e/gpu/dra/run-dra-dual-home-lifecycle.sh
```

This test never creates, labels, patches, or deletes the project namespace,
its `default` NetworkAttachmentDefinition, or its ResourceQuota. It first
proves a conflicting custom network is rejected before a Pod or ResourceClaim
is persisted. It then runs the canonical fixed DRA workload and requires CUDA,
one allocated claim, an exact quota increment, an infra `eth0` whose address is
the Pod IP, and the tenant `net1` with its default route. Cleanup must return
Pod, claim, and quota usage to the exact pre-test baseline.

The lock is acquired before fixed-name reuse inspection. Cleanup deletes only
object sets this invocation owns, so a lock loser or reuse-guard refusal cannot
delete another run's Deployment, template, or claim. Negative admission
evidence is scoped to the current ReplicaSet UID, the current run's start time,
and the denial message exported by the production webhook. Existing claims are
fingerprinted before allocation; concurrent claim activity invalidates the
acceptance run with a distinct retry-in-an-idle-project diagnostic instead of
being reported as quota drift.

Before acquiring the hardware-suite lock or creating any test object, the
script also requires the HAMi DRA DaemonSet to have `desired == current ==
ready` and its driver to publish at least one shareable ResourceSlice device.
This makes a stale `Unknown` kubelet-plugin Pod or an empty post-reboot
inventory fail fast with an operator-facing diagnostic instead of waiting for
an unallocatable claim timeout. Alternate qualified driver identities can be
supplied through `GPU_DRA_DRIVER`, `GPU_DRA_DRIVER_NAMESPACE`, and
`GPU_DRA_DRIVER_DAEMONSET`.

Run the controlled B-013 stale-driver recovery game day only while the pool is
idle and the new driver alerts are deployed:

```bash
KUBECONFIG=/path/to/lab-kubeconfig \
KUBE_DC_CLI=/path/to/reviewed/kube-dc \
KUBE_DC_GPU_HARDWARE_TESTS=acknowledge-hardware-mutation \
GPU_HARDWARE_TARGET=gpu-lab \
tests/e2e/gpu/dra/run-dra-stale-driver-recovery.sh
```

The game day keeps the DaemonSet UID stable, creates one current-owner unready
Pod with an `OnDelete` invalid-image template, waits for the production stale
threshold, and requires the CLI planner to name that exact Pod. It restores the
reviewed image before executing the separately emitted deletion and then proves
driver, ResourceSlice, ResourceClaim, quota, and postflight convergence.

`GPU_HARDWARE_SUSPEND_CANARY=true` records the CronJob's prior state and restores
it in the exit trap. The suite also uses an atomic ConfigMap lock in
`gpu-operator`; an existing lock is never stolen automatically. Inspect and
remove a stale lock only after proving its holder process and namespace no longer
exist.

Defaults describe the approved V100 pilot: one `pod-hami` node, 20 HAMi shares,
two physical V100 devices, profile `nvidia-v100-hami`, and the digest-pinned CUDA
image used by the operator canary. Override expected shares/devices/profile only
for a separately qualified lab.

This is not normal CI. A scheduled runner must supply the cluster kubeconfig and
external job serialization. Three consecutive manual non-production lab runs passed
on 2026-07-15; see [the retained evidence](gpu-lab-2026-07-15.md). The
same evidence records the steady-state throttle pass, its startup/cuBLAS
limitations, the pinned three-image compatibility pass, and the automated
frozen-plugin detection/recovery pass. A product decision on the compute
limitations remains required before the G5 gate closes.
