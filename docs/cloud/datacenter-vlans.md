import {VlanAllocationDiagram} from '@site/src/components/Diagram/CloudFlowDiagrams';
import {VlanWorkloadTopologyDiagram} from '@site/src/components/Diagram/CloudTopologyDiagrams';

# Datacenter VLANs

A datacenter VLAN puts your workloads **directly on a physical network segment**
in the datacenter, in the same broadcast domain as your own hardware. Use it when a
pod or VM has to reach equipment that cannot be routed to: a storage array, a
license dongle, a PLC, a legacy appliance that only speaks to its own subnet.

This is different from everything else in [VPC and private networking](private-networking.md).
Your Project VPC is an overlay that Kube-DC creates for you. A datacenter VLAN is
a real wire that already exists, that your organization already owns, and that
your platform administrator hands to you.

---

## How it works

Your platform administrator allocates a VLAN to your **organization**. From then
on you decide, without raising a ticket, which of your **Projects** uses it, and
you can change your mind later.

<details data-github-only>
<summary>Diagram source for GitHub</summary>

```
    Platform administrator                You (organization admin)
    ─────────────────────                 ────────────────────────
    allocates VLAN 4014      ──────►      assign to Project "production"
    to organization "acme"                        │
                                                  │  later, freely
                                                  ▼
                                          unassign → assign to "staging"
```

</details>

<VlanAllocationDiagram />

A workload in that Project then gets a **second network interface**:

<details data-github-only>
<summary>Diagram source for GitHub</summary>

```
┌──────────────────────────────────────────────────────────┐
│  Pod or VM in Project "production"                       │
│  Backing namespace: acme-production                      │
│                                                          │
│    eth0            10.0.0.15/24    ← Project VPC         │
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

</details>

<VlanWorkloadTopologyDiagram />

**Your default route does not move.** Internet access, DNS and everything else
keep working exactly as before, over the VPC interface. The VLAN interface
carries only traffic to that segment.

---

## Rules worth knowing up front

The following table lists the rules that govern a datacenter VLAN:

| Rule | What it means |
|---|---|
| **One Project per VLAN** | A physical segment is a shared broadcast domain. Only one Project may hold it at a time; this prevents users in another Project from seeing your layer 2 traffic. |
| **Several VLANs per Project** | A Project can hold more than one VLAN, and one workload can attach to several, provided at least one node carries *all* of them. See [Attach a workload](#attach-a-workload). |
| **The addressing is fixed** | Subnet, gateway and reserved addresses are set by your platform administrator from what your network team told them. You cannot change them, because they describe real equipment. |
| **Reversible, not instant** | Unassigning drains the workloads that are still attached before the VLAN can go to another Project. |
| **Layer 2 only** | The VLAN is not routed into your VPC. Only workloads that explicitly attach can reach it. |

---

## Assign a VLAN to a Project

You need the Organization administrator role to assign a VLAN.

1. Open the account menu, and select **Manage organization**.
2. In the **Networking** group, select **Datacenter VLANs**. The table lists every VLAN allocated to your Organization with its **Segment**, **VLAN ID**, **CIDR**, **Gateway**, **MTU**, **Assignment**, **Project**, and **Attached ports**.

   ![The Datacenter VLANs page of Manage organization](images/vlan-1-org-tab.png)

3. In the actions menu of an `unassigned` row, select **Assign to project**. The **Assign VLAN** dialog opens.
4. Select the **Project**.
5. Click **Assign project**.

   ![The Assign VLAN dialog with a Project selected](images/vlan-2-assign.png)

The **Assignment** column changes to `assigned`. To see the progress of the network, expand the row. **Phase** shows the binding state, and **Ready** shows if the segment is delivered to the nodes.

The **Assignment** column has these values:

| Assignment | Meaning |
|---|---|
| `unassigned` | Allocated to your Organization, not in use. Assign it to a Project. |
| `assigned` | Bound to the Project in the **Project** column. Workloads in that Project can attach after the **Phase** is `Ready`. |
| `releasing` | Being unassigned. Attached workloads are draining; **Attached ports** shows the count. You cannot assign it again yet. |

The expanded row shows these values:

| Field | Meaning |
|---|---|
| **Phase** `Pending` or `Provisioning` | The network is being created. Wait. |
| **Phase** `Ready` | Workloads in the Project can attach. |
| **Phase** `Denying` | The binding is being removed. The platform refuses new attachments. |
| **Phase** `Error` | The binding could not be realized. The most common cause is that another Project already holds the VLAN. Contact your platform administrator. |
| **Ready** `No` | Your platform administrator has not finished delivering this VLAN to the cluster's nodes. You cannot fix this yourself; ask them. |

### Unassign and re-assign

To return the VLAN to your Organization's pool, select **Unassign** in the
actions menu of the row, then click **Unassign** in the **Unassign VLAN**
dialog. The VLAN stays yours; only the Project binding goes away, so you can
give it to a different Project afterwards.

The row shows `releasing` and the number of **Attached ports** until teardown
finishes. That wait is deliberate: the VLAN is not handed to another
Project while anything is still on the wire.

:::warning
**Unassigning does not move your workloads for you.** Attached workloads keep
running, but teardown *waits* for them: while the VLAN is releasing, any pod or
VM that still asks for it is **refused**, so a Deployment that recreates a pod
will not come back.

Remove the attachment first. Delete the `k8s.v1.cni.cncf.io/networks` annotation
from the Deployment/StatefulSet/Job template (or the network from a stopped VM's
template), then replace the running instances. Only then unassign.
:::

The **Attached ports** count on a `releasing` row is attachment *evidence*, not a headcount:
a pod and its address record can both be counted. It falls to zero when the wire
is genuinely clear.

---

## Attach a workload

Assigning the VLAN makes it *available* to the Project. Each pod or VM still has
to ask for it.

To find the attachment value, open the Project, select **Networking** in the
section rail, and open the **Datacenter VLANs** tab. The table lists every
VLAN the Project holds with its **Name**, **Status**, **Segment**, **CIDR**,
**Gateway**, and **Attachment**. Click a VLAN name to see its details. For a
ready VLAN, the details show a **Pod attachment** and a **VM attachment**
snippet.

![VLAN details for a Project](images/vlan-3-project-card.png)

Click **Copy pod attachment** or **Copy VM attachment** to copy a snippet. The
pod snippet has the annotation value of the following example, already in the
right form.

### Attach a pod

Add one annotation. The value is `<backing-namespace>/<network-name>`. The
console shows the exact string, so copy it from there instead of assembling it
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

To attach several VLANs, separate them with commas. A workload is pinned to the
nodes that carry **every** VLAN it names. If two VLANs reach different sets of
nodes, the pod stays `Pending` with no node available:

```yaml
    k8s.v1.cni.cncf.io/networks: acme-production/pn-ext-4014-production,acme-production/pn-ext-4015-production
```

Kube-DC fills in the address, MAC, port security and node placement for you. The
pod is scheduled only onto nodes where the VLAN is actually delivered, so it
cannot land somewhere the wire does not reach.

### Attach a virtual machine

**From the console.** In the **Create virtual machine** sheet, the **Networking
& GPU** step has an **Additional interfaces** list with every VLAN this Project
holds. Select the VLANs that you want. The generated manifest gets each network
and its matching interface, correctly paired. Review it on the **Review** step
before you create the VM.

![Selecting a datacenter VLAN when creating a VM](images/vlan-4-create-vm.png)

A VLAN that is not ready to attach is shown, but you cannot select it. If you
select more than one VLAN, the VM can run only on nodes that carry **all** of
them.

To change the VLANs of an existing VM, open the **Network** tab of the VM and
click **Manage VLAN interfaces**. Restart the VM to apply the change.

**By hand.** A VM needs the network **and** a matching interface. This is what the
console does for you, and it trips people up when done manually:

:::warning
KubeVirt requires a one-to-one match between `networks` and
`devices.interfaces`. Get the `name:` fields out of step and the VM does not come
up on the VLAN, with no obvious error pointing at the cause.
:::

The following snippet is a **networking fragment, not a complete VM**. It has no
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
route, and therefore its internet access, on the VPC.

:::note
A VLAN change on a VM applies only after the VM restarts. With kubectl, stop
the VM, edit the template, and start it again. The same applies to pods:
recreate them instead of editing them.
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
networks the workload has, and on how your cluster is configured. The VLAN can
land on `net1`, `net2` or later, and inside a VM the guest names it however its OS
chooses. Always match by the network name or the address, as above.
:::

`kubectl exec` is blocked in Project backing namespaces. Run the checks as a
short-lived diagnostic Job and read its logs instead. This Job discovers the
VLAN interface from the route to your target, so it does not assume `net1`.
Replace the network annotation and target address with your values:

```bash
NS=acme-production
kubectl delete job -n "$NS" vlan-check --ignore-not-found

kubectl apply -f - <<'EOF'
apiVersion: batch/v1
kind: Job
metadata:
  name: vlan-check
  namespace: acme-production
spec:
  backoffLimit: 0
  template:
    metadata:
      annotations:
        k8s.v1.cni.cncf.io/networks: acme-production/pn-ext-4014-production
    spec:
      restartPolicy: Never
      containers:
        - name: check
          image: nicolaka/netshoot
          env:
            - name: TARGET
              value: 192.0.2.10
          command: ["/bin/sh", "-ec"]
          args:
            - |
              IFACE="$(ip route get "$TARGET" | awk '{for (i=1; i<=NF; i++) if ($i == "dev") {print $(i+1); exit}}')"
              test -n "$IFACE"
              echo "VLAN interface: $IFACE"
              ip -4 -br addr show dev "$IFACE"
              ping -c3 -I "$IFACE" "$TARGET"
              ip route get 1.1.1.1
              ping -c1 -M do -s 1372 -I "$IFACE" "$TARGET"
              if ping -c1 -M do -s 1500 -I "$IFACE" "$TARGET"; then
                echo "unexpected success above the configured MTU" >&2
                exit 1
              fi
              echo "oversized DF packet failed as expected"
EOF

kubectl wait -n "$NS" --for=condition=Complete job/vlan-check --timeout=120s
kubectl logs -n "$NS" job/vlan-check
kubectl get pod -n "$NS" -l job-name=vlan-check \
  -o jsonpath='{.items[0].metadata.annotations.k8s\.v1\.cni\.cncf\.io/network-status}'
echo
kubectl delete job -n "$NS" vlan-check
```

The example checks a 1400-byte MTU: a 1372-byte ICMP payload plus headers should
pass, while a 1500-byte payload with don't-fragment set should fail. Adjust both
sizes if your platform administrator configured a different MTU.

---

## What you cannot do

These are refused at admission, with a message explaining why:

- **Choose your own addressing.** IP, MAC, routes and default-route settings are
  assigned by Kube-DC. Multus runtime options (`ips`, `mac`, `default-route`,
  `cni-args`) are rejected, as are provider-scoped annotations that would override
  IPAM: `ip_address`, `ip_pool`, `mac_address`, `routes`, `gateway`, `cidr`, and
  `default_route` on the VLAN's provider key. (Port security and security groups
  are set for you and validated.)
- **Attach a VLAN your Project does not hold**, including one held by another
  Project in your own Organization.
- **Change the subnet or gateway** on a VLAN assigned to you.
- **Edit a VLAN assignment in place.** To move a VLAN between Projects, unassign
  and assign again.

### Work from the command line

Organization admins can drive assignment with `kubectl`. The addressing must
match **exactly** what your platform administrator recorded for the wire.
Admission rejects anything else, so copy the subnet, the gateway, and the reserved ranges
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
The name is wrong, or the VLAN is assigned to a different Project. The message
lists the VLANs this Project holds. Compare your value against it.

**"network … is in another project's namespace"**
You referenced a VLAN belonging to a different Project. Each Project can only
attach the VLANs assigned to it.

**Pod stuck in `Pending` / `ContainerCreating`**
Usually the segment is not ready on any node, or the pod's own scheduling
constraints conflict with the nodes carrying the VLAN. Expand the VLAN row on
the **Datacenter VLANs** page of **Manage organization**. If **Ready** shows
`No`, contact your platform administrator.

**VM boots with no VLAN interface**
Almost always a missing or misnamed entry under `devices.interfaces`. See the
warning above.

**Unassign appears stuck**
Look at the **Attached ports** count on the `releasing` row. Teardown waits for
those workloads to detach. Delete them, or wait for the controller to finish.

---

## Related

- [VPC and private networking](private-networking.md): your Project's own network
- [Networking overview](networking-overview.md): how the pieces fit together
- [Public and floating IPs](public-floating-ips.md): reaching workloads from the internet
