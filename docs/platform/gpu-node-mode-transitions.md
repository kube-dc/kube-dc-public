# GPU node-mode transitions

This page is for platform operators who change what a GPU node is used for
after installation. After you read it, you can move one GPU node between
Shared Pod GPU and Dedicated GPU VM modes without disturbing running GPU
workloads, and check the DRA shared-GPU backend.

:::note Pilot
GPU support is a pilot capability. Hardware and profile support is
deployment-specific. See [Roadmap](roadmap.md).
:::

`kube-dc bootstrap gpu transition` performs the holder-safe day-two switch
between these node modes:

| Mode | Use |
|---|---|
| `pod-hami` | Shared Pod GPU with the HAMi device-plugin allocator (`--gpu-shared-allocator=hami-device-plugin`). |
| `pod-hami-dra` | Shared Pod GPU with the Dynamic Resource Allocation (DRA) allocator (`--gpu-shared-allocator=dra` or `auto`). |
| `vm-passthrough` | Dedicated GPU VM. |

It updates the generated fleet `NodeFeatureRule`; it does not patch ownership
labels directly. The command does not support `vm-vgpu`.

## Safety contract

Before cordoning, the command requires:

- an explicit admin kubeconfig and exact Kubernetes node name;
- both `GPU_SHARED_CREATION_ENABLED=false` and
  `GPU_VM_CREATION_ENABLED=false` in the cluster overlay;
- a clean fleet checkout whose generated node-mode rule agrees with both live
  active and expected mode labels;
- zero running native GPU holders on the node, including HAMi Pod resources,
  init/ephemeral-container resources, KubeVirt `virt-launcher` resources, and
  any Pod with a DRA resource claim.

Apply order is fixed:

1. cordon the exact node;
2. repeat the node-scoped holder check to close the scheduling race;
3. atomically edit only that node's generated ownership block;
4. commit and push the clean fleet transaction;
5. wait for Node Ready, matching active/expected labels, zero holders, and
   exactly the target plugin owner with the conflicting owner absent;
6. uncordon.

Any error after step 1 leaves the node cordoned. A push error resets the local
fleet checkout to the exact pre-transition commit, but the node stays cordoned
so an operator can inspect live/Git state. The command never rebinds a device
under a holder and never enables billing, quota, add-ons, or creation gates.

## Preview and apply

Close both creation gates through the normal reviewed config workflow first:

```bash
kube-dc bootstrap config set <cluster> \
  GPU_SHARED_CREATION_ENABLED=false \
  GPU_VM_CREATION_ENABLED=false --yes
```

Preview performs API and fleet reads only:

```bash
kube-dc bootstrap gpu transition <cluster> <gpu-node> \
  --repo <fleet-checkout> \
  --kubeconfig <admin-kubeconfig> \
  --from pod-hami \
  --to vm-passthrough
```

Apply after holder owners have stopped their workloads and the preview passes:

```bash
kube-dc bootstrap gpu transition <cluster> <gpu-node> \
  --repo <fleet-checkout> \
  --kubeconfig <admin-kubeconfig> \
  --from pod-hami \
  --to vm-passthrough \
  --yes
```

The reverse direction uses `--from vm-passthrough --to pod-hami` and has the
same gates. A node that uses the DRA allocator uses `pod-hami-dra` in `--from`
or `--to`. A Dedicated GPU VM must be stopped, not migrated: GPU-backed VMIs
are intentionally non-live-migratable.

## Failure and resume

Do not manually uncordon after a failure. Inspect:

```bash
kubectl --kubeconfig <admin-kubeconfig> get node <gpu-node> \
  -L kube-dc.com/gpu.workload-mode,kube-dc.com/gpu.expected-workload-mode,nvidia.com/gpu.workload.config
kubectl --kubeconfig <admin-kubeconfig> get pods -A \
  --field-selector spec.nodeName=<gpu-node>
flux get kustomizations
```

Correct the GitOps/runtime failure without changing the requested direction.
Then resume the same transaction explicitly:

```bash
kube-dc bootstrap gpu transition <cluster> <gpu-node> \
  --repo <fleet-checkout> \
  --kubeconfig <admin-kubeconfig> \
  --from pod-hami \
  --to vm-passthrough \
  --resume-cordoned \
  --yes
```

`--resume-cordoned` authorizes this workflow to uncordon after the target is
fully verified. Never use it for a node cordoned by an unrelated maintenance
or safety incident.

## Check the DRA shared-GPU backend

`kube-dc bootstrap gpu dra` is a read-only workflow for the DRA allocator. Each
subcommand needs `--kubeconfig <admin-kubeconfig>`, and accepts
`--output json`. The subcommands are:

| Command | What it does |
|---|---|
| `status`, `detect` | Show the live DRA snapshot. |
| `doctor` | Validate the Kubernetes version, driver image digest, device inventory, ownership, and allocation canary. |
| `plan` | Run the `doctor` checks, and also require both creation gates closed and zero legacy device-plugin holders. Run it before you migrate to DRA. |
| `rollback-plan` | Require both creation gates closed and zero DRA holders and claims. Run it before you roll back from DRA. |
| `postflight` | Run the full `doctor` checks again after you upgrade each GPU node, and show the state of the driver Pods. |
| `recovery-plan` | Print one exact kubelet-plugin Pod deletion when the driver DaemonSet is degraded, its DRA inventory is empty, and an old unready Pod remains on a Ready node. It does not run the deletion. |

To validate DRA before you migrate, run this command:

```bash
kube-dc bootstrap gpu dra plan --kubeconfig <admin-kubeconfig>
```

The subcommands change nothing. `kube-dc bootstrap init` with
`--gpu-shared-allocator=dra` (or `auto`) applies DRA after a saved plan
passes. No subcommand falls back to the legacy device-plugin allocator.

DRA needs Kubernetes 1.36 or later. The default RKE2 version that
`kube-dc bootstrap init` installs is v1.35.9, so upgrade the cluster before you
select DRA. With `--gpu-shared-allocator=auto`, `init` runs the DRA checks and
stops when one fails. It does not select the device-plugin allocator instead;
choose `hami-device-plugin` explicitly for that.
