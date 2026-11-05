# GPU supply-chain and image policy

:::note Pilot
GPU support is a pilot capability. Hardware and profile support is
deployment-specific. See [Roadmap](roadmap.md).
:::

GPU drivers, device plugins, schedulers, webhooks, and monitoring agents run
with enough privilege to compromise a node. Version selection and image
provenance are therefore part of the GPU security boundary, not an ordinary
application upgrade.

This runbook is for platform operators who install, upgrade, and audit the GPU
stack. Fleet owns the Helm sources and component values; kube-dc owns the
qualified installer tuple, runtime audit, promotion gate, and evidence contract.

## Released qualified tuple

An enabled installer accepts one indivisible qualified tuple:

| Component | Released value |
|---|---|
| NVIDIA GPU Operator chart | `v26.3.3` |
| NVIDIA data-center driver | `580.126.20` proprietary |
| NVIDIA container toolkit | `v1.19.1` |
| HAMi chart/runtime | `2.9.0` |
| HAMi scheduler image | Kubernetes `v1.35.3` |
| NVIDIA DCGM exporter | `4.4.1-4.6.0-ubuntu22.04` plus its resolved runtime digest |

The CLI rejects any other value even if it is syntactically valid. This keeps
version flags from becoming unreviewed "latest" knobs. A newly qualified tuple
is promoted by updating the release constants and contracts after the GPU
upgrade gate, hardware canary, monitoring, and rollback evidence pass.

## Reference policy

- Helm chart versions must be exact; ranges and floating aliases are forbidden.
- Desired container images must use an explicit immutable release tag or a
  `@sha256` digest. Explicit `latest` and implicit tagless/latest references are
  forbidden.
- The running Pod `imageID` must resolve to a SHA-256 digest. The release record
  uses that digest, not the mutable desired tag, for SBOM and vulnerability
  evidence.
- The CUDA liveness/application canary is digest-pinned in Git.
- Driver auto-upgrade stays disabled. Driver, kernel, RKE2, and GPU Operator are
  promoted as one reviewed compatibility tuple.
- Private registry and licensed vGPU material belongs in SOPS; it must never
  enter installer configuration, generated plans, logs, or tenant APIs.

## Runtime provenance audit

Run the read-only audit against an approved cluster:

```bash
KUBECONFIG=/path/to/admin-kubeconfig \
  tests/e2e/gpu/supply-chain/audit-runtime-images.sh
```

The default scope is `gpu-operator` and `hami-system`. Additional or replacement
namespaces can be supplied with repeated `--namespace`. The output records
namespace, Pod, container type/name, desired reference, and resolved image ID;
it does not read nodes or physical device identity.

The audit fails when:

- no GPU runtime container exists;
- a desired reference uses explicit or implicit `latest`;
- a container has not started and therefore has no resolved image ID; or
- the runtime ID is not a SHA-256 digest.

For hardware-free validation or an archived rerun, feed a captured PodList:

```bash
tests/e2e/gpu/supply-chain/audit-runtime-images.sh \
  --pod-json gpu-runtime-pods.json
```

Normal GPU CI exercises good, floating-tag, and unresolved-runtime fixtures.
The installer tuple and mode/upgrade state machines are also part of the GPU
workflow so a CLI change cannot bypass the release tuple silently.

## SBOM, signature, and vulnerability gate

The runtime audit proves resolution, not that an image is safe. For every
unique resolved digest, the release pipeline must:

1. verify registry/signature provenance when the publisher provides it;
2. generate and retain a CycloneDX or SPDX SBOM;
3. run a vulnerability scan with the database timestamp recorded;
4. reject known Critical findings without a fixed, time-bounded Security
   exception;
5. disposition High findings by exploitability, privileges, mitigation, owner,
   and expiry; and
6. record the exact tool versions, commands, digest, result, and exception IDs.

Example tools (versions must themselves be pinned by CI):

```bash
cosign verify <registry>/<image>@sha256:<digest>
syft <registry>/<image>@sha256:<digest> -o cyclonedx-json
trivy image --severity HIGH,CRITICAL <registry>/<image>@sha256:<digest>
```

Do not scan only the chart version or desired tag. GPU Operator expands into
multiple operand images, so the running Pod digest inventory is the final
artifact set.

## Promotion procedure

1. Keep billing eligibility and both tenant creation gates false.
2. Select one proposed kernel/RKE2/driver/GPU-Operator/DCGM-exporter tuple and pass
   `kube-dc bootstrap gpu upgrade-check` with recent allocation, monitoring,
   and rollback canary evidence.
3. Update the fleet chart/operand pins in one review and render the target
   manifests. Reject `latest`, tagless images, chart ranges, and secret values.
4. Upgrade one cordoned, zero-holder canary node using the GPU upgrade runbook.
5. Run validators, allocation/application canaries, failure alerts, rollback
   rehearsal, and the runtime provenance audit.
6. Generate SBOM/signature/vulnerability evidence for every unique runtime
   digest and obtain Security disposition.
7. Update the CLI released tuple and its qualification record only after the
   complete evidence package is approved.
8. Observe the stabilization window before admitting entitlement or workloads.

Changing one component without the rest creates a new, unqualified tuple and is
not a patch-level exception.

## Rollback and incident response

Immediately stop new creation when a GPU image is revoked, unexpectedly changes
digest, lacks provenance, or has an unaccepted Critical vulnerability. Preserve
existing holder safety, cordon the affected node, and follow the serialized GPU
upgrade/mode rollback. Restore the last approved tuple, rerun runtime provenance
and allocation canaries, and retain both the rejected and restored digest sets.

Any digest change under an unchanged desired tag is a supply-chain event. It
requires investigation and a new evidence record even when workloads appear
healthy.

## Check a GPU VM guest before you offer it

Tenants create GPU VMs only after you enable them for a guest image. Check
Ubuntu and Windows guests separately, and record these results for each one:

1. The exact image version or digest and the OS build.
2. The exact signed driver package version and its SHA-256 checksum.
3. The first boot, the driver installation, the required reboot, and the
   second boot all succeed.
4. `nvidia-smi` reports the expected model, driver, memory, and ECC state,
   with zero errors.
5. A pinned CUDA program allocates memory, runs a kernel, synchronizes, and
   returns the correct result.
6. A stop releases the device, a start attaches it again, and the checks in
   steps 4 and 5 still pass.
7. When capacity is exhausted, a second VM waits in the queue and starts after
   the first VM releases the device.
8. The platform refuses live migration, and a planned stop and start recovers
   the VM.
9. Deleting the VM releases the quota and the device, with no stale holder.
10. You have a support owner, a rollback procedure, and an update policy for
    the driver and the image.

Keep the records from both guests. Enable tenant GPU VM creation on a cluster
after the records are complete.
