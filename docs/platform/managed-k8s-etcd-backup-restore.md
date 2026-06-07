# Managed Kubernetes etcd Backup & Restore

How to back up and restore the control-plane etcd of tenant Kubernetes
clusters that Kube-DC manages via `KdcCluster`. This page is for
operators and SREs running a Kube-DC cluster — not for tenants.

Both flows are operated through standard `kubectl` against the
**management cluster**. Tenants have no direct interaction with the
backup or restore pipeline.

---

## What's protected, what isn't

This pipeline backs up the **control-plane etcd** of each managed
tenant cluster. That's the API state — Nodes, Deployments, Pods,
ConfigMaps, Secrets, CRDs — everything `kubectl get` on the tenant
would return.

It does **not** back up:

- **Tenant workload data** (PVC contents, application state). For that,
  use Velero or a workload-specific tool deployed on the tenant.
- **Cluster CAs** or kubeconfigs. Those live in `<datastore>-etcd-certs`
  Secrets in the tenant's project namespace and are managed by the
  KdcClusterDatastore certificate lifecycle. A control-plane restore
  preserves them as-is — which is what you want; a snapshot from
  before a CA rotation will work fine after restore.
- **Kube-DC platform state** (the management cluster's own etcd, MetalLB,
  Rook-Ceph, monitoring). Those are protected separately via Velero on
  the management cluster.

The recovery pattern is **disaster recovery**, not "rollback the last 10
minutes of changes." Restoring rolls a cluster's API state back to the
snapshot moment. Pods that the controllers think shouldn't exist may be
deleted or evicted; pods on workers stay running on disk until evaluated.

---

## How backups work

For every `KdcCluster` whose namespace has a working
`managed-k8s-backups` S3 ObjectBucketClaim (auto-provisioned when
Rook-Ceph is available on the platform), the controller creates a
`<cluster>-etcd-backup` CronJob in the project namespace.

| Default | Configurable via |
|---|---|
| Schedule: `0 2 * * *` (02:00 UTC daily) | `KdcCluster.spec.backup.schedule` |
| Retention: 7 days (S3 lifecycle policy) | `KdcCluster.spec.backup.retentionDays` |
| Bucket: `<projectNamespace>-managed-k8s-backups` | `KdcCluster.spec.backup.destinationPath` |
| Object key (plaintext): `<cluster>/<cluster>-<ts>.db` | (not configurable) |
| Object key (envelope-encrypted): `<cluster>/<cluster>-<ts>/` (directory of 3 objects — see Encrypted backups below) | (not configurable) |
| S3 endpoint: `S3_ENDPOINT` controller env (e.g. `https://s3.stage.kube-dc.com`) | `KdcCluster.spec.backup.s3Endpoint` |

A snapshot is roughly **20 MB per cluster** (etcd 3.5, default
quota-backend-bytes=8GB), and the upload to Rook-Ceph S3 takes ~10–25s
inside the management cluster. The CronJob is gated on the OBC being
`Bound` and on `KdcCluster.status.dataStoreName` being set, so newly
provisioned clusters don't try to take backups before their etcd
exists.

### Encrypted backups (envelope mode)

When the owning `KdcCluster` has
[etcd-at-rest encryption](managed-k8s-etcd-encryption.md) enabled
(`spec.encryption.etcd.enabled: true`), the backup CronJob switches
into **envelope mode**: it wraps the snapshot before upload using the
same per-cluster KEK that the kms-plugin sidecar uses for live etcd.
Plaintext mode is unchanged for clusters that don't opt in — both
modes coexist on the same platform.

For each snapshot under envelope mode, three sibling objects land in
S3 instead of one:

```
s3://<projectNS>-managed-k8s-backups/<cluster>/<cluster>-<ts>/
  ├── snapshot.db.enc      NONCE(12B) || CIPHERTEXT || GCM_TAG(16B)
  ├── dek.wrapped          vault:vN:... — the OpenBao-wrapped DEK
  └── metadata.json        schemaVersion + transitKey + transitKeyVersion +
                           algorithm + nonce + wrappedDek + createdAt +
                           source + etcdSnapshotSha256
```

The wire format is locked at `schemaVersion=1` and `algorithm=AES-256-GCM`.
The `wrappedDek` is duplicated inside `metadata.json` for operators who
prefer file-side recovery; `metadata.json` alone is enough to restore
because it includes the SHA-256 of the original snapshot for post-decrypt
verification.

**Anyone with bucket-level read access cannot decrypt** — the wrapped
DEK is useless without the OpenBao Transit key, and that key never
leaves OpenBao in plaintext. This is the data-at-rest companion to the
etcd-at-rest layer documented in
[managed-k8s-etcd-encryption.md](managed-k8s-etcd-encryption.md).

The restore controller auto-detects layout from the snapshot key
shape: a trailing slash signals envelope mode and triggers the
unwrap + decrypt path; a plain `.db` key signals the legacy plaintext
path. Operators normally don't pass these keys by hand — the
`kube-dc.com/restore-from` annotation flow (see "Performing a restore"
below) handles both layouts transparently.

### Quick checks

```bash
# Are backups configured for every tenant cluster?
kubectl get kdccluster -A -o jsonpath='{range .items[*]}{.metadata.namespace}/{.metadata.name}: {.status.conditions[?(@.type=="BackupReady")].status} {.status.conditions[?(@.type=="BackupReady")].reason}{"\n"}{end}'

# Last 24h of CronJob runs across the cluster
kubectl get jobs -A --sort-by=.metadata.creationTimestamp | grep etcd-backup | tail -20

# Snapshot files for one tenant
NS=<project-ns>
SECRET=$(kubectl -n $NS get obc managed-k8s-backups -o jsonpath='{.spec.secretName}')
BUCKET=$(kubectl -n $NS get obc managed-k8s-backups -o jsonpath='{.spec.bucketName}')
ENDPOINT=$(kubectl -n kube-dc get cm cluster-config -o jsonpath='{.data.S3_HOSTNAME}' \
             | sed 's|^|https://|')

kubectl -n $NS run s3 --rm -i --restart=Never \
  --image=amazon/aws-cli:2.15.0 \
  --overrides="{\"spec\":{\"containers\":[{\"name\":\"s3\",\"image\":\"amazon/aws-cli:2.15.0\",\"command\":[\"sh\",\"-c\",\"aws --endpoint-url $ENDPOINT s3 ls s3://$BUCKET/<cluster>/\"],\"envFrom\":[{\"secretRef\":{\"name\":\"$SECRET\"}}]}]}}"
```

### Symptoms / fixes

| Symptom | Likely cause | Resolution |
|---|---|---|
| `BackupReady=False reason=S3NotAvailable` | Rook-Ceph OBC not Bound yet | Wait for `kubectl -n $NS get obc managed-k8s-backups` to reach `Bound`. Check Rook health on mgmt cluster. |
| `BackupReady=False reason=DataStoreUnknown` | Cluster newly created, etcd not yet ready | Resolves itself once `KdcCluster.status.dataStoreName` is set. |
| Job stuck in `Failed` | Common: etcd unreachable (TLS, DNS), S3 unreachable from project namespace | `kubectl logs job/<cluster>-etcd-backup-<unix-time>` — exit 2 = snapshot too small (etcd write failed); other exits = check container output. |
| No CronJob exists | `KdcCluster.spec.backup.enabled: false`, OR project lacks the OBC | Re-enable in spec; provision OBC. |

A sample CronJob, by name, you can copy:

```bash
kubectl -n <project-ns> get cronjob <cluster-name>-etcd-backup -o yaml
```

---

## Performing a restore

You trigger a restore by setting **one annotation** on the `KdcCluster`:

```bash
kubectl -n <project-ns> annotate kdccluster <cluster-name> \
  kube-dc.com/restore-from="<cluster-name>/<cluster-name>-<timestamp>.db" \
  --overwrite
```

The controller then walks an eight-step state machine. Total runtime is
typically 60–120 seconds for a snapshot under ~30 MB.

### What happens, step by step (operator view)

| Phase | Wait time | Visible effect |
|---|---|---|
| `Validating` | 1 reconcile | Snapshot key prefix matched, etcd replica count == 1 |
| `DrainingControlPlane` | ~10s | `<cluster>-cp` Deployment scaled to 0 (Kamaji may immediately recreate apiserver pods — that's fine, they can't talk to etcd) |
| `StoppingEtcd` | ~10–30s | `<datastore>-etcd` StatefulSet scaled to 0; etcd-0 Pod terminates |
| `RestoringSnapshot` | ~25s for 19MB | Job `<cluster>-etcd-restore` runs: pulls .db from S3, runs `etcdutl snapshot restore`, replaces contents of `/var/run/etcd/default.etcd` |
| `StartingEtcd` | ~10–20s | STS scaled back to 1; etcd-0 boots from restored data |
| `StartingControlPlane` | ~30s | `<cluster>-cp` Deployment scaled back to spec replicas; apiserver becomes Ready |
| `Succeeded` | terminal | Annotations cleared; `status.latestRestoreKey` + `status.latestRestoreTime` stamped; restore Job deleted |

The tenant cluster's API is **unreachable for the entire restore
window**. Workload pods on tenant worker nodes keep running on disk
during this — only the API plane is offline. Once the apiserver comes
back, kubelets re-register their node and the cluster catches up.

### What the SRE sees in `kubectl get`

```bash
kubectl -n <project-ns> get kdccluster <cluster-name> \
  -o jsonpath='{range .status.conditions[?(@.type=="RestoreReady")]}{.status} {.reason}: {.message}{"\n"}{end}'

# Live progress (state annotation tracks the current step):
kubectl -n <project-ns> get kdccluster <cluster-name> \
  -o jsonpath='{.metadata.annotations.kube-dc\.com/restore-state}{"\n"}'
```

### Final state after success

| Field | Value |
|---|---|
| `metadata.annotations[kube-dc.com/restore-from]` | (cleared) |
| `metadata.annotations[kube-dc.com/restore-state]` | (cleared) |
| `status.conditions[type=RestoreReady]` | `True / Succeeded / Restore from "<key>" complete` |
| `status.latestRestoreKey` | `<cluster>/<cluster>-<timestamp>.db` |
| `status.latestRestoreTime` | `<UTC timestamp>` |

A second restore can be requested by re-applying the annotation; the
state machine re-validates and walks the steps again.

### Failure handling

If a restore fails (validation rejected the request, the Job didn't
complete, or scaling errored), the controller writes:

```
RestoreReady=False reason=Failed message="Restore from \"<key>\" failed (<reason>): <detail>"
```

and clears the trigger annotations. The tenant control plane is left
in whatever state the failure occurred at — if etcd was already wiped
(step 4 onwards), the cluster needs another restore attempt, or a
fall-back to the manual runbook (see "When the controller is
unavailable" below).

Common failures:

| Reason | What to do |
|---|---|
| `MultiReplicaUnsupported` | The cluster is running HA etcd (replicas > 1). Phase 1 only supports single-replica. Use the manual runbook or wait for Phase 2. |
| `ForeignKey` | Snapshot key doesn't start with this cluster's name. Use `<cluster-name>/...` paths only. |
| `RestoreJobFailed` | Inspect `kubectl logs job/<cluster>-etcd-restore` — common causes: S3 credentials invalid, snapshot integrity check failed, network to S3 from project namespace blocked. |
| `EtcdStartFailed` / `ControlPlaneStartFailed` | Restored etcd didn't come up. Check `kubectl logs <datastore>-etcd-etcd-0` — usually a cert mismatch (peer URL changed since snapshot) or a corrupted snapshot. Cert mismatch → re-trigger Kamaji's certificate reconcile by annotating the TenantControlPlane. |

---

## Verifying a restore is complete

```bash
# 1. Condition + status fields
kubectl -n <project-ns> get kdccluster <cluster-name> -o yaml | grep -A2 RestoreReady
kubectl -n <project-ns> get kdccluster <cluster-name> \
  -o jsonpath='lastKey={.status.latestRestoreKey}{"\n"}lastTime={.status.latestRestoreTime}{"\n"}'

# 2. Tenant API responds
kubectl -n <project-ns> get secret <cluster-name>-kubeconfig \
  -o jsonpath='{.data.value}' | base64 -d > /tmp/kc.yaml
kubectl --kubeconfig /tmp/kc.yaml get nodes
kubectl --kubeconfig /tmp/kc.yaml get pods -A | head

# 3. Pod ages preserved (sanity — not 0m old which would mean fresh-bootstrap, not restore)
kubectl --kubeconfig /tmp/kc.yaml -n kube-system get pods -o wide
```

If the tenant pods show ages similar to the cluster's ORIGINAL ages
(e.g. several days, matching when they were last redeployed), the
restore preserved data. If they're all freshly-created (within minutes),
something went wrong — etcd booted on empty data, not on the snapshot.

---

## Constraints

| | Phase 1 (today) |
|---|---|
| Etcd replica count | 1 only |
| Cross-cluster restore | rejected (snapshot key must belong to target cluster's bucket) |
| Concurrent restores on same cluster | the second annotation overwrites the first; controller doesn't queue — admission webhook rejection is on the Phase 2 roadmap |
| RPO | 24 hours (daily 02:00 UTC backup) |
| RTO | 60–120 seconds (controller-driven), 30–60 minutes (manual runbook) |

If a tenant runs HA etcd (`KdcClusterDatastore.spec.etcd.replicas`>1)
and you need to restore, use the manual runbook below. Phase 2 will
add multi-replica support.

If a tenant needs sub-24h RPO, the path forward is delta snapshots —
also planned for Phase 2/3.

---

## When the controller is unavailable

Sometimes you can't use the annotation flow — the management
cluster's `kube-dc-k8-manager` is down, or the controller has a bug,
or you're recovering during a dependency outage. Use the manual
runbook in `kube-dc-k8-manager/docs/RESTORE.md`. It covers the same
8-step flow but with `kubectl scale` + a debug Pod that runs
`etcdutl snapshot restore` against the etcd member-0 PVC.

In short:

```bash
NS=<project-ns>
CLUSTER=<cluster-name>
DS=<datastore-name>   # usually <CLUSTER>-etcd

# 1. Pause the control plane and etcd
kubectl -n $NS scale deploy ${CLUSTER}-cp --replicas=0
kubectl -n $NS scale sts ${DS}-etcd --replicas=0

# 2. Run a one-shot Pod that mounts the etcd-data-${DS}-etcd-0 PVC
#    and runs etcdutl snapshot restore against /var/run/etcd/default.etcd
#    (full Pod spec in the runbook)

# 3. Bring etcd back up
kubectl -n $NS scale sts ${DS}-etcd --replicas=1
kubectl -n $NS wait --for=condition=Ready pod/${DS}-etcd-0 --timeout=120s

# 4. Bring the control plane back up
kubectl -n $NS scale deploy ${CLUSTER}-cp --replicas=1
```

The trickiest part is the PVC mount path: the restore data dir must
be `/var/run/etcd/default.etcd` (the etcd container's `--data-dir`),
NOT the PVC mount root.

---

## Architecture context

For the bigger picture of how managed Kubernetes clusters fit into
Kube-DC's networking, control-plane, and storage layers:

- [Architecture: multi-tenancy](architecture-multi-tenancy.md)
- [Architecture: networking](architecture-networking.md)
- Storage: ObjectBucketClaims are provisioned per-Project by the
  same Rook-Ceph object store that backs the management cluster's
  S3 endpoint.
