# B-10 on stage — the exact sequence

Stage was chosen deliberately over a disposable cluster. That is a workable
choice, but it makes the ordering below load-bearing rather than advisory: the
health suite creates and deletes tenant orgs continuously, and `60-resilience.sh`
takes the admission webhook down.

## What stage already gives us

Checked 2026-07-29:

| | |
|---|---|
| ProviderNetwork | `ext-cloud`, ready on all three nodes |
| VLAN under test | **4014** (`test-vlan` on the Hetzner vSwitch) |
| On the vSwitch | `kube-dc-master-1`, `kube-dc-worker-1` |
| **NOT** on the vSwitch | `kube-dc-worker-2` |
| VLAN tags already used | 4011, 4013 — 4014 is free |

`kube-dc-worker-2` being in the ProviderNetwork but *not* on the VLAN is the
ideal negative case, and it is exactly the hazard the design warns about: the
ProviderNetwork reports all three nodes ready, so a segment with no
`nodeSelector` would happily place a workload on worker-2, where it would come
up with a NIC on a wire carrying nothing. **The segment must carry a
nodeSelector.**

## Still needed before the run

Three values only someone with the fabric in front of them can supply:

1. **`B10_CIDR`** — a range on VLAN 4014 that is *exclusively ours*, excluded
   from every other DHCP scope and static assignment (§18.4). This is the
   likeliest production failure and no script can check it.
2. **`B10_GATEWAY`** — the gateway on that VLAN.
3. **`B10_EXTERNAL_PEER`** — a machine already on VLAN 4014 that is **not** part
   of this cluster. Pod-to-pod proves the overlay; only this proves we are on
   the customer's physical broadcast domain.

The earlier M0–M4 proof used addressing on this VLAN, but it was not recorded,
and §18.4 requires a human to confirm exclusivity regardless.

## Sequence

### 1. Label the VLAN-capable nodes (worker-2 deliberately excluded)

```sh
kubectl label node kube-dc-master-1 network.kube-dc.com/customer-vlan-4014=true --overwrite
kubectl label node kube-dc-worker-1 network.kube-dc.com/customer-vlan-4014=true --overwrite
# kube-dc-worker-2: leave UNLABELLED — it is the negative placement case
```

### 2. Deploy the RC, feature still OFF

RC images and digests: [RC.md](RC.md) (`v0.5.35-b10rc1`).

```sh
kubectl -n kube-dc set image deploy/kube-dc-manager  manager=shalb/kube-dc-manager:v0.5.35-b10rc1
kubectl -n kube-dc set image deploy/kube-dc-backend  backend=shalb/kube-dc-ui-backend:v0.5.35-b10rc1
kubectl -n kube-dc set image deploy/kube-dc-admin-frontend admin-frontend=shalb/kube-dc-admin-frontend:v0.5.35-b10rc1
kubectl -n kube-dc rollout status deploy/kube-dc-manager deploy/kube-dc-backend --timeout=300s
```

`set image` is volatile — Flux reverts it. That is *fine and preferable* here:
the RC should not outlive the test. Do not pin it in the fleet.

Then confirm the disabled state still holds on the RC:

```sh
hack/b10/10-absent.sh      # must still pass with the new images
```

### 3. Enable — this is the disruptive boundary

`projectNetwork.enabled=true` requires `manager.replicaCount >= 2`; the chart
refuses otherwise, because the attach gate is a fail-closed webhook on the pod
path and a single replica turns every rollout into an outage.

Enabling means editing the stage overlay in `kube-dc-fleet` and letting Flux
reconcile — not `kubectl set env`, which Flux would revert mid-test and which
would leave the webhook registered against a manager that no longer has the
feature.

```
clusters/stage/cluster-config.env:
  PROJECT_NETWORK_ENABLED=true
  KUBE_DC_MANAGER_REPLICAS=2
```

**Before this step:** confirm no Playwright run is in flight and that none will
start during the window.

```sh
pgrep -f 'playwright:v1.61' && echo "IN FLIGHT — wait"
kubectl get configmap stage-health -n kube-dc -o jsonpath='{.data.running\.json}'
```

### 4. Run the pass

```sh
export KUBECONFIG=~/.kube/stage_config
export B10_ORG=b10 B10_PROJECT=wire            # create this org/project first
export B10_OTHER_NS=<another project ns>       # holds NO grant — for the breach matrix
export B10_PROVIDER_NETWORK=ext-cloud
export B10_VLAN_ID=4014
export B10_NODE_SELECTOR='{"network.kube-dc.com/customer-vlan-4014":"true"}'
export B10_UNPREPARED_NODE=kube-dc-worker-2
export B10_CIDR=<exclusive range>
export B10_GATEWAY=<gateway>
export B10_EXTERNAL_PEER=<a real machine on 4014>
export B10_ADDRESS_AUTHORITY_CONFIRMED=yes     # only after §18.4 is genuinely true

hack/b10/run-all.sh
```

`60-resilience.sh` is excluded unless you also set:

```sh
export B10_INCLUDE_RESILIENCE=yes
export B10_I_HAVE_A_MAINTENANCE_WINDOW=yes
```

Run that one last, and only with the suite paused — it scales the manager to
zero.

### 5. Put stage back

```sh
hack/b10/99-cleanup.sh          # workloads, grant (drains), segment
```

Then revert the fleet overlay (`PROJECT_NETWORK_ENABLED=false`,
`KUBE_DC_MANAGER_REPLICAS=1`), let Flux reconcile, drop the node labels, and
confirm with `10-absent.sh`. Flux will restore the pre-RC images on its own.

## If it fails

Fix only the observed blocker, cut RC2, rerun the **whole** pass. A partial
rerun does not establish that the bundle passed together, which is the only
thing an acceptance run is for.
