---
name: create-vm
description: Create a KubeVirt virtual machine in a Kube-DC Project using the live OS catalog, Project networking, supported storage, and the generated SSH keypair.
---

# Create a Virtual Machine

A VM is a workload inside a Project. Its Kubernetes resources live in the
Project's backing namespace; it is not a Managed Cluster and does not create a
separate Kubernetes API.

## Prerequisites

- The Project is Ready and selected with `kube-dc use`.
- CPU, memory, storage, and public IPv4 quota are checked as applicable.
- The live OS catalog provides the image, default user, minimum resources,
  firmware, and import source.
- A supported VM StorageClass and access-mode/volume-mode pair are selected.
- `ssh-keypair-default` and `authorized-keys-default` exist in the backing
  namespace.

## 1. Select from the live catalog

Use the console's **OS Images** catalog. API clients can query:

```http
GET /api/create-vm/{project-backing-namespace}/os-images
Authorization: Bearer {kube-dc-jwt}
```

Do not embed a static OS/version table or provider S3 hostname. Catalog entries
can change independently on each installation.

Prefer the catalog's digest-pinned registry reference with
`pullMethod: node`. Use its HTTP URL only for entries such as installation
media or custom images that do not offer a registry source. Never turn a
mutable tag into an apparently reproducible boot disk.

## 2. Select storage

Use the VM wizard or the live storage-class endpoint:

```http
GET /api/create-vm/{project-backing-namespace}/storageclasses
Authorization: Bearer {kube-dc-jwt}
```

The chosen access mode and volume mode must be a supported pair for that class.
Node-local storage and shared block storage have different snapshot and live
migration behavior. Do not infer capabilities from a class name alone.

## 3. Create the DataVolume and VM

Start from [vm-template.yaml](vm-template.yaml). It is a Linux cloud-image
template; Windows and provider-specific golden images should use the manifest
rendered by the VM wizard.

The essential contracts are:

- DataVolume source comes from the live catalog.
- Registry sources are digest-pinned and use `pullMethod: node`.
- The root disk is at least the catalog minimum.
- The network is a Multus bridge to
  `{project-backing-namespace}/default`.
- `accessCredentials` references `authorized-keys-default` and names the
  catalog's OS user.
- The guest image runs QEMU Guest Agent for key injection and IP/readiness
  reporting.
- CPU and memory fit quota with operational headroom.

```bash
kubectl apply -f vm.yaml
kubectl get datavolume,virtualmachine -w
```

Do not create the VM until the manifest has the correct user, firmware,
machine type, and storage shape for the selected catalog entry.

## 4. Optional dedicated GPU

When a whole-device GPU is explicitly requested, follow
[references/dedicated-gpu.md](references/dedicated-gpu.md). Use the Kube-DC VM
wizard or authenticated validate/create transaction. Do not raw-apply native
host-device resources. Dedicated GPU VMs cannot live migrate and must use
`evictionStrategy: None`.

## 5. Connect

The generated Project keypair can be used only when the VM references
`authorized-keys-default` and the guest agent has injected the public key.

For local access, first choose a supported route:

- browser SSH or console in the Kube-DC UI;
- FIP for direct one-to-one VM access;
- EIP-bound LoadBalancer Service for an exposed port;
- an approved private route to the Project network.

Follow `ssh-into-vm` for key handling and `manage-networking` for FIP/EIP
status fields. A private VMI address is not automatically reachable from a
workstation.

## Verify

```bash
kubectl get datavolume {vm}-disk \
  -o jsonpath='{.status.phase}{"\n"}'
kubectl get virtualmachine {vm} \
  -o jsonpath='{.status.printableStatus}{"\n"}'
kubectl get virtualmachineinstance {vm} \
  -o jsonpath='{.status.conditions[?(@.type=="AgentConnected")].status}{"\n"}'
kubectl get virtualmachineinstance {vm} \
  -o jsonpath='{.status.interfaces[?(@.name=="default")].ipAddress}{"\n"}'
```

Expected signals are DataVolume `Succeeded`, VM `Running`, guest agent
connected, and a non-empty interface address.

If provisioning stalls:

```bash
kubectl describe datavolume {vm}-disk
kubectl describe virtualmachine {vm}
kubectl get events --sort-by=.lastTimestamp
```

Check image reachability, disk size, storage-class capabilities, quota,
firmware, and placement. These checks do not require pod exec.

## Safety

- Use live catalog data; do not copy an old image URL or default user.
- Pin registry sources by digest.
- Never expose a VM only because SSH is convenient; confirm the required
  network path and public IPv4 quota.
- Keep the generated private key out of logs and long-lived shared files.
- Do not combine FIP and LoadBalancer exposure for the same VM without a
  deliberate network design.
- Use the gated product flow for Dedicated GPU.
