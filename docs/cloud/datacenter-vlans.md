# Datacenter VLANs

A datacenter VLAN puts your workloads **directly on a physical network segment**
in the datacenter — the same broadcast domain as your own hardware. Use it when a
pod or VM has to reach equipment that cannot be routed to: a storage array, a
license dongle, a PLC, a legacy appliance that only speaks to its own subnet.

This is different from everything else in [VPC & Private Networking](private-networking.md).
Your project VPC is an overlay that Kube-DC creates for you. A datacenter VLAN is
a real wire that already exists, that your organization already owns, and that
your platform administrator hands to you.

---

## How it works

Your platform administrator allocates a VLAN to your **organization**. From then
on you decide, without raising a ticket, which of your **projects** uses it — and
you can change your mind later.

```
    Platform administrator                You (organization admin)
    ─────────────────────                 ────────────────────────
    allocates VLAN 4014      ──────►      assign to project "production"
    to organization "acme"                        │
                                                  │  later, freely
                                                  ▼
                                          unassign → assign to "staging"
```

A workload on that project then gets a **second network interface**:

```
┌──────────────────────────────────────────────────────────┐
│  Pod or VM in project acme-production                    │
│                                                          │
│    eth0            10.0.0.15/24    ← project VPC         │
│                                      DEFAULT ROUTE       │
│                                                          │
│    net1            192.0.2.30/24   ← datacenter VLAN     │
│                                      no default route    │
└───────────────────────┬──────────────────────────────────┘
                        │  802.1Q tagged
                        ▼
        ┌───────────────────────────────────┐
        │  Physical VLAN 4014               │
        │  192.0.2.0/24                     │
        │                                   │
        │  Your storage array  192.0.2.10   │
        │  Your appliance      192.0.2.11   │
        └───────────────────────────────────┘
```

**Your default route does not move.** Internet access, DNS and everything else
keep working exactly as before, over the VPC interface. The VLAN interface
carries only traffic to that segment.

---

## Rules worth knowing up front

| | |
|---|---|
| **One project per VLAN** | A physical segment is a shared broadcast domain. Only one project may hold it at a time — that is what stops another tenant seeing your traffic at layer 2. |
| **Several VLANs per project** | A project can hold more than one VLAN, and one workload can attach to several — provided at least one node carries *all* of them (see below). |
| **The addressing is fixed** | Subnet, gateway and reserved addresses are set by your platform administrator from what your network team told them. You cannot change them, and you should not want to — they describe real equipment. |
| **Reversible, not instant** | Unassigning drains the workloads that are still attached before the VLAN can go to another project. |
| **Layer 2 only** | The VLAN is not routed into your VPC. Only workloads that explicitly attach can reach it. |

---

## Assign a VLAN to a project

Go to **Organization Management → Datacenter VLANs**. Every VLAN allocated to
your organization is listed with its subnet, gateway and current state.

![Datacenter VLANs in Organization Management](images/vlan-1-org-tab.png)

Pick a project from the dropdown on an **Available** row and press **Assign**.

![Assigning a VLAN to a project](images/vlan-2-assign.png)

The row moves to **Assigned** once the network is published into the project,
usually within a few seconds.

| State | Meaning |
|---|---|
| **Available** | Allocated to your organization, not in use. Assign it to a project. |
| **Provisioning** / **Pending** | The network is being created. Wait. |
| **Assigned** | Ready. Workloads in that project can attach. |
| **Releasing** | Being unassigned. Attached workloads are draining; the count is shown. Not yet re-assignable. |
| **Error** | The binding could not be realised. The most common cause is that the VLAN is already held by another project. Contact your platform administrator. |
| **segment not ready** | Your platform administrator has not finished delivering this VLAN to the cluster's nodes. Nothing you can do — ask them. |

### Unassign and re-assign

Press **Unassign** to return the VLAN to your organization's pool. It stays
yours; only the project binding goes away, so you can give it to a different
project afterwards.

The row shows **Releasing** with the number of workloads still attached until
teardown finishes. That wait is deliberate: the VLAN is not handed to another
project while anything is still on the wire.

:::warning
**Unassigning does not move your workloads for you.** Attached workloads keep
running, but teardown *waits* for them: while the VLAN is releasing, any pod or
VM that still asks for it is **refused**, so a Deployment that recreates a pod
will not come back.

Remove the attachment first — delete the `k8s.v1.cni.cncf.io/networks` annotation
from the Deployment/StatefulSet/Job template (or the network from a stopped VM's
template), then replace the running instances. Only then unassign.
:::

The count shown on a **Releasing** row is attachment *evidence*, not a headcount:
a pod and its address record can both be counted. It falls to zero when the wire
is genuinely clear.

---

## Attach a workload

Assigning the VLAN makes it *available* to the project. Each pod or VM still has
to ask for it.

Open **Organization Management → Projects** and click the project. Its details
panel has a **Datacenter VLANs** card listing every VLAN the project holds, with
the segment, the network name, and the exact string to attach with.

![VLAN details in the project panel](images/vlan-3-project-card.png)

Copy the value shown under **Attach with** — that is the annotation value below,
already in the right form.

### Attach a pod

Add one annotation. The value is `<project-namespace>/<network-name>`, and the
console shows you the exact string — copy it from there rather than assembling it
by hand. When a VLAN is assigned from the console the network is named
`<segment>-<project>`, so the examples below use `pn-ext-4014-production`.

```yaml
apiVersion: v1
kind: Pod
metadata:
  name: storage-client
  namespace: acme-production
  annotations:
    k8s.v1.cni.cncf.io/networks: acme-production/pn-ext-4014-production
spec:
  containers:
    - name: app
      image: busybox:1.36
      command: ["sh", "-c", "sleep infinity"]
```

To attach several VLANs, comma-separate them. Note that a workload is pinned to
nodes carrying **every** VLAN it names — if two VLANs are delivered to different
sets of nodes, the pod stays `Pending` with no node available:

```yaml
    k8s.v1.cni.cncf.io/networks: acme-production/pn-ext-4014-production,acme-production/pn-ext-4015-production
```

Kube-DC fills in the address, MAC, port security and node placement for you. The
pod is scheduled only onto nodes where the VLAN is actually delivered, so it
cannot land somewhere the wire does not reach.

### Attach a virtual machine

**From the console.** When you create a VM, Step 1 shows a **Datacenter VLANs**
section listing every VLAN this project holds. Tick the ones you want and the
generated manifest gets the network and its matching interface, correctly paired —
review it on the next step before creating.

<!-- SCREENSHOT: Create VM → Step 1, scrolled to the "Datacenter VLANs" section
     with at least one VLAN ticked.
     Save as: docs/cloud/images/vlan-4-create-vm.png
     Then replace this comment with:
     ![Selecting a datacenter VLAN when creating a VM](images/vlan-4-create-vm.png)
-->

The section only appears when this project actually holds a VLAN, and only lists
VLANs that are ready to attach. Ticking more than one warns you that the VM will
be pinned to nodes carrying **all** of them.

**By hand.** A VM needs the network **and** a matching interface. This is what the
console does for you, and it trips people up when done manually:

:::warning
KubeVirt requires a one-to-one match between `networks` and
`devices.interfaces`. Get the `name:` fields out of step and the VM does not come
up on the VLAN — with no obvious error pointing at the cause.
:::

The snippet below is a **networking fragment, not a complete VM** — it has no
disks or volumes. Merge it into a VM template you already know boots, with the VM
stopped.

```yaml
apiVersion: kubevirt.io/v1
kind: VirtualMachine
metadata:
  name: app-vm
  namespace: acme-production
spec:
  template:
    spec:
      networks:
        - name: vpc                                  # keep the VPC as default
          multus:
            default: true
            networkName: acme-production/default
        - name: customer                             # the datacenter VLAN
          multus:
            networkName: acme-production/pn-ext-4014-production
      domain:
        devices:
          interfaces:
            - name: vpc                              # must match networks[0].name
              bridge: {}
            - name: customer                         # must match networks[1].name
              bridge: {}
```

Keep `default: true` on the VPC network. That is what leaves the VM's default
route — and therefore its internet access — on the VPC.

:::note
A running VM cannot have a VLAN added to it. Stop the VM, edit the template,
start it again. The same applies to pods: recreate them rather than editing.
:::

---

## Verify it works

The most reliable way to see what a pod actually got is the status Multus writes
back, which names each network explicitly:

```bash
kubectl get pod storage-client -n acme-production \
  -o jsonpath='{.metadata.annotations.k8s\.v1\.cni\.cncf\.io/network-status}'
```

```json
[
  { "name": "kube-ovn",                                   "interface": "eth0", "ips": ["10.0.0.15"] },
  { "name": "acme-production/pn-ext-4014-production",     "interface": "net1", "ips": ["192.0.2.30"] }
]
```

:::warning
**Do not assume the VLAN is on `net1`.** Interface numbering depends on how many
networks the workload has and on how your cluster is configured — the VLAN can
land on `net1`, `net2` or later, and inside a VM the guest names it however its OS
chooses. Always match by the network name or the address, as above.
:::

Then test from inside, substituting the interface you just identified. The
`busybox` image used earlier does **not** have these tools — use a diagnostic
image such as `nicolaka/netshoot` for this:

```bash
ip -4 -br addr                          # confirm the address
ping -c3 -I net1 192.0.2.1              # the VLAN gateway answers
ping -c3 -I net1 192.0.2.10             # your own hardware answers
ip route get 1.1.1.1                    # default route is still the VPC interface
```

### MTU

The VLAN interface uses the MTU your platform administrator configured for the
segment, commonly **1400**. If an application sends larger frames with
don't-fragment set, it will fail silently while ping still works. Check with:

```bash
ping -c1 -M do -s 1372 -I net1 192.0.2.10     # 1372 + 28 = 1400, should pass
ping -c1 -M do -s 1500 -I net1 192.0.2.10     # should fail
```

Again: a diagnostic image, and whichever interface carries the VLAN subnet.

---

## What you cannot do

These are refused at admission, with a message explaining why:

- **Choose your own addressing.** IP, MAC, routes and default-route settings are
  assigned by Kube-DC. Multus runtime options (`ips`, `mac`, `default-route`,
  `cni-args`) are rejected, as are provider-scoped annotations that would override
  IPAM — `ip_address`, `ip_pool`, `mac_address`, `routes`, `gateway`, `cidr` and
  `default_route` on the VLAN's provider key. (Port security and security groups
  are set for you and validated.)
- **Attach a VLAN your project does not hold**, including one held by another
  project in your own organization.
- **Change the subnet or gateway** on a VLAN assigned to you.
- **Edit a VLAN assignment in place.** To move a VLAN between projects, unassign
  and assign again.

### Working from the command line

Organization admins can drive assignment with `kubectl`. The addressing must
match what your platform administrator recorded for the wire **exactly** —
admission rejects anything else — so copy the subnet, gateway and reserved ranges
from the Datacenter VLANs table:

```yaml
# projectnetwork.yaml
apiVersion: kube-dc.com/v1
kind: ProjectNetwork
metadata:
  name: pn-ext-4014-production        # your choice; this becomes the network name
spec:
  org: acme
  project: production
  segmentRef: pn-ext-4014             # the segment shown in the console
  mode: l2
  cidrBlock: 192.0.2.0/24             # ─┐
  gateway: 192.0.2.1                  #  ├─ must match the console exactly
  gatewayCheck: false                 #  │
  excludeIps:                         #  │
    - 192.0.2.1..192.0.2.99           # ─┘
```

```bash
kubectl create -f projectnetwork.yaml          # assign
kubectl delete projectnetwork pn-ext-4014-production   # unassign
```

`kubectl get` and `kubectl apply` on `projectnetwork` are **not** granted to
tenants: the objects are cluster-scoped, so read access could not be limited to
your own organization. Use the console to see state; use `create`/`delete` to
change it.

---

## Troubleshooting

**"network … is not a datacenter VLAN assigned to this project"**
The name is wrong, or the VLAN is assigned to a different project. The message
lists the VLANs this project actually holds — compare against it.

**"network … is in another project's namespace"**
You referenced a VLAN belonging to a different project. Each project can only
attach the VLANs assigned to it.

**Pod stuck in `Pending` / `ContainerCreating`**
Usually the segment is not ready on any node, or the pod's own scheduling
constraints conflict with the nodes carrying the VLAN. Check the VLAN's status in
Organization Management; if it shows *segment not ready*, contact your platform
administrator.

**VM boots with no VLAN interface**
Almost always a missing or misnamed entry under `devices.interfaces`. See the
warning above.

**Unassign appears stuck**
Look at the attached-workload count on the **Releasing** row. Teardown waits for
those workloads to detach. Delete them, or wait for the controller to finish.

---

## Related

- [VPC & Private Networking](private-networking.md) — your project's own network
- [Networking Overview](networking-overview.md) — how the pieces fit together
- [Public & Floating IPs](public-floating-ips.md) — reaching workloads from the internet
