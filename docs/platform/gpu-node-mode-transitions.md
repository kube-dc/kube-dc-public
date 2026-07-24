# GPU node-mode transitions

`kube-dc bootstrap gpu transition` performs the holder-safe day-two switch
between Shared Pod GPU (`pod-hami`) and Dedicated GPU VM
(`vm-passthrough`). It updates the generated fleet `NodeFeatureRule`; it does
not patch ownership labels directly.

The transition is deliberately unavailable for `vm-vgpu` until the licensed
driver, registry, SOPS, and mdev path is qualified.

## Safety contract

Before cordoning, the command requires:

- an explicit admin kubeconfig and exact Kubernetes node name;
- both `GPU_SHARED_CREATION_ENABLED=false` and
  `GPU_VM_CREATION_ENABLED=false` in the cluster overlay;
- a clean fleet checkout whose generated node-mode rule agrees with both live
  active and expected mode labels;
- zero running native GPU holders on the node, including HAMi Pod resources,
  init/ephemeral-container resources, and KubeVirt `virt-launcher` resources.

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
same gates. A Dedicated GPU VM must be stopped, not migrated: GPU-backed VMIs
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
