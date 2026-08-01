# Managed Cluster etcd backup and restore

This page documents the operator internals behind scheduled etcd snapshots and
the customer-facing **Take snapshot now** and **Restore** actions in a Managed
Cluster's danger zone. Customers should use
[Data Protection and Recovery](/cloud/backups-snapshots#managed-cluster-snapshots)
as the canonical workflow.

Operators inspect and recover the pipeline with `kubectl` against the
**management cluster**. The underlying API object is a `KdcCluster`; use that
name only for API fields, manifests, and commands that address the custom
resource directly.

---

## What's protected, what isn't

This pipeline backs up the **control-plane etcd** of each Managed Cluster. That
is the Kubernetes API state, including Nodes, Deployments, Pods, ConfigMaps,
Secrets, and custom resources returned by the Managed Cluster API.

It does **not** back up:

- **Workload data** (PVC contents and application state). Use a
  consistency-aware, workload-specific backup method inside the Managed
  Cluster.
- **Cluster CAs** or kubeconfigs. Those live in `<datastore>-etcd-certs`
  Secrets in the Managed Cluster's Project backing namespace and are managed by the
  `KdcClusterDatastore` certificate lifecycle. Verify certificate, endpoint,
  and kubeconfig compatibility after restoring across a certificate rotation.
- **Kube-DC platform state** (the management cluster's own etcd, MetalLB,
  Rook Ceph, and monitoring). Follow the management cluster's separate backup
  and disaster-recovery procedures.

The recovery pattern is **disaster recovery**, not a general-purpose undo
operation. Restoring rolls API state back to the selected snapshot. Processes
on workers can continue while the API is unavailable, then controllers and
kubelets reconcile them against the restored state.

---

## How backups work

For every Managed Cluster whose Project backing namespace has a working
`managed-k8s-backups` S3 ObjectBucketClaim, the controller creates a
`<cluster>-etcd-backup` CronJob. The platform provisions the claim automatically
when Rook Ceph object storage is available.

| Default | Configurable via |
|---|---|
| Schedule: `0 2 * * *` (02:00 UTC daily) | `KdcCluster.spec.backup.schedule` |
| Retention: 7 days (S3 lifecycle policy) | `KdcCluster.spec.backup.retentionDays` |
| Bucket: `<projectNamespace>-managed-k8s-backups` | `KdcCluster.spec.backup.destinationPath` |
| Object key (plaintext): `<cluster>/<cluster>-<ts>.db` | (not configurable) |
| Object key (envelope-encrypted): `<cluster>/<cluster>-<ts>/` (directory of 3 objects — see Encrypted backups below) | (not configurable) |
| S3 endpoint: `S3_ENDPOINT` controller environment (for example, `https://s3.<domain>`) | `KdcCluster.spec.backup.s3Endpoint` |

Snapshot size and upload duration depend on the Managed Cluster's API state,
etcd history, object storage, and network path. The CronJob is gated on the OBC
being `Bound` and on `KdcCluster.status.dataStoreName` being set, so a newly
provisioned Managed Cluster does not try to take a snapshot before its etcd
datastore exists.

### Encrypted backups (envelope mode)

When the owning `KdcCluster` has
[etcd-at-rest encryption](managed-k8s-etcd-encryption.md) enabled
(`spec.encryption.etcd.enabled: true`), the backup CronJob switches
into **envelope mode**: it wraps the snapshot before upload using the
same KEK assigned to that Managed Cluster for live etcd.
Plaintext mode is unchanged for Managed Clusters that do not opt in — both
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
The `wrappedDek` is duplicated inside `metadata.json`. The encrypted snapshot
and `metadata.json` can therefore be restored without the separate
`dek.wrapped` object. The metadata also includes the original snapshot's SHA-256
for verification after decryption.

Bucket read access alone is insufficient to decrypt an envelope snapshot: the
wrapped DEK also requires the corresponding OpenBao Transit key. Protect object
storage credentials and OpenBao access as separate recovery dependencies. This
is the data-at-rest companion to the etcd-at-rest layer documented in
[managed-k8s-etcd-encryption.md](managed-k8s-etcd-encryption.md).

The restore controller detects the layout from the snapshot key shape: a
trailing slash signals envelope mode and triggers the unwrap-and-decrypt path;
a plain `.db` key signals the plaintext path. Customers select a snapshot in
the Managed Cluster danger zone; the underlying `kube-dc.com/restore-from`
annotation carries its key to the controller. The controller handles both
layouts transparently.

### Quick checks

```bash
# Are backups configured for every Managed Cluster?
kubectl get kdccluster -A -o jsonpath='{range .items[*]}{.metadata.namespace}/{.metadata.name}: {.status.conditions[?(@.type=="BackupReady")].status} {.status.conditions[?(@.type=="BackupReady")].reason}{"\n"}{end}'

# Most recent backup Jobs across the management cluster
kubectl get jobs -A --sort-by=.metadata.creationTimestamp | grep etcd-backup | tail -20

# Snapshot objects for one Managed Cluster
NS=<backing-namespace>
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
| `BackupReady=False reason=S3NotAvailable` | Rook Ceph OBC not Bound yet | Wait for `kubectl -n $NS get obc managed-k8s-backups` to reach `Bound`. Check Rook health on the management cluster. |
| `BackupReady=False reason=DataStoreUnknown` | Managed Cluster newly created, etcd not yet ready | Resolves itself once `KdcCluster.status.dataStoreName` is set. |
| Backup Job reports `Failed` | etcd, TLS, DNS, or S3 connectivity failed | Inspect `kubectl logs job/<cluster>-etcd-backup-<job-suffix>` and verify both the datastore and object-storage path from the Project backing namespace. |
| No CronJob exists | `KdcCluster.spec.backup.enabled: false`, or the Project lacks the OBC | Re-enable in spec; provision OBC. |

Inspect the generated CronJob by name:

```bash
kubectl -n <backing-namespace> get cronjob <cluster-name>-etcd-backup -o yaml
```

---

## Performing a restore

Customers should select and restore the snapshot from the Managed Cluster's
danger zone as described in the Cloud guide. Internally, that workflow sets the
following annotation on the `KdcCluster`. Use the direct command only in an
approved operator runbook because restore is disruptive:

```bash
kubectl -n <backing-namespace> annotate kdccluster <cluster-name> \
  kube-dc.com/restore-from="<snapshot-key>" \
  --overwrite
```

The controller then advances through a restore state machine. Duration depends
on snapshot contents, storage, scheduling, and control-plane readiness.

### What happens, step by step (operator view)

| Phase | Completion signal | Operator-visible effect |
|---|---|---|
| `Validating` | The restore state advances after the snapshot key and supported etcd topology pass validation | No workload resources are changed |
| `DrainingControlPlane` | The scale patch is accepted; this phase does not wait for the Deployment to settle | Kamaji may recreate API Pods, so stopping etcd provides the write barrier |
| `StoppingEtcd` | The etcd StatefulSet reports zero current and ready replicas | The datastore Pod is gone before the restore Job mounts its PVC |
| `RestoringSnapshot` | Job `<cluster>-etcd-restore` reaches `Complete=True` | The Job downloads the snapshot, runs `etcdutl snapshot restore`, and replaces `/var/run/etcd/default.etcd` |
| `StartingEtcd` | The etcd StatefulSet reports its expected ready replica count | etcd starts from the restored data |
| `StartingControlPlane` | The control-plane Deployment reaches its expected ready replica count | The Managed Cluster API can return; verify `/readyz` after the controller succeeds |
| `Succeeded` | `RestoreReady=True` with reason `Succeeded` | Trigger annotations are cleared and latest-restore status is recorded |

The Managed Cluster API is unavailable while its control plane is stopped and
restarted. Workloads on worker nodes may continue running, but they cannot rely
on control-plane operations until the API and controllers recover.

### What the SRE sees in `kubectl get`

```bash
kubectl -n <backing-namespace> get kdccluster <cluster-name> \
  -o jsonpath='{range .status.conditions[?(@.type=="RestoreReady")]}{.status} {.reason}: {.message}{"\n"}{end}'

# Live progress (state annotation tracks the current step):
kubectl -n <backing-namespace> get kdccluster <cluster-name> \
  -o jsonpath='{.metadata.annotations.kube-dc\.com/restore-state}{"\n"}'
```

### Final state after success

| Field | Value |
|---|---|
| `metadata.annotations[kube-dc.com/restore-from]` | (cleared) |
| `metadata.annotations[kube-dc.com/restore-state]` | (cleared) |
| `status.conditions[type=RestoreReady]` | `True / Succeeded / Restore from "<key>" complete` |
| `status.latestRestoreKey` | The selected plaintext object key or encrypted snapshot directory key |
| `status.latestRestoreTime` | `<UTC timestamp>` |

A second restore can be requested by re-applying the annotation; the
state machine re-validates and walks the steps again.

### Failure handling

If a restore fails (validation rejected the request, the Job didn't
complete, or scaling errored), the controller writes:

```
RestoreReady=False reason=Failed message="Restore from \"<key>\" failed (<reason>): <detail>"
```

and clears the trigger annotations. The Managed Cluster control plane remains
in the state where the failure occurred. If snapshot replacement had started,
follow the installed-version recovery runbook before retrying or making further
changes.

Common failures:

| Reason | What to do |
|---|---|
| `MultiReplicaUnsupported` | The Managed Cluster uses more than one etcd replica. The current controller restore flow supports one replica only; follow the installed-version manual recovery runbook. |
| `ForeignKey` | The snapshot key does not start with this Managed Cluster's name. Use `<cluster-name>/...` paths only. |
| `RestoreJobFailed` | Inspect `kubectl logs job/<cluster>-etcd-restore` — common causes: S3 credentials invalid, snapshot integrity check failed, network to S3 from the Project backing namespace blocked. |
| `EtcdStartFailed` / `ControlPlaneStartFailed` | Restored etcd did not become ready. Inspect the etcd Pod logs and the Kamaji `TenantControlPlane` custom-resource status; validate snapshot integrity, peer URLs, and certificates before retrying. |

---

## Verifying a restore is complete

```bash
# 1. Confirm controller completion and the recorded snapshot key.
kubectl -n <backing-namespace> get kdccluster <cluster-name> \
  -o jsonpath='{range .status.conditions[?(@.type=="RestoreReady")]}{.status}{" "}{.reason}{": "}{.message}{"\n"}{end}'
kubectl -n <backing-namespace> get kdccluster <cluster-name> \
  -o jsonpath='lastKey={.status.latestRestoreKey}{"\n"}lastTime={.status.latestRestoreTime}{"\n"}'

# 2. Load the external admin kubeconfig and verify API readiness.
KUBECONFIG_FILE=$(mktemp)
trap 'rm -f "$KUBECONFIG_FILE"' EXIT
kubectl -n <backing-namespace> get secret \
  <cluster-name>-cp-admin-kubeconfig-external \
  -o jsonpath='{.data.admin\.conf}' | base64 -d > "$KUBECONFIG_FILE"
chmod 0600 "$KUBECONFIG_FILE"
kubectl --kubeconfig "$KUBECONFIG_FILE" get --raw=/readyz
kubectl --kubeconfig "$KUBECONFIG_FILE" get nodes

# 3. Verify an object known to exist in the selected snapshot, then inspect
#    controller status for the workloads that should recover.
kubectl --kubeconfig "$KUBECONFIG_FILE" -n <expected-namespace> \
  get <expected-kind> <expected-name> -o yaml
kubectl --kubeconfig "$KUBECONFIG_FILE" get deployments,statefulsets -A
```

Before restore, record at least one named resource and meaningful field that is
known to exist in the selected snapshot. After restore, compare that object with
the recovery record and verify that expected Nodes and workload controllers
report healthy status. Pod age is not evidence of a successful restore: Pods
can be recreated after a valid restore, and worker processes can survive an API
outage.

---

## Current constraints

| Constraint | Current behavior |
|---|---|
| etcd replica count | The controller-driven restore supports one replica only |
| Restore to another Managed Cluster | Rejected; the snapshot key must belong to the target Managed Cluster |
| Concurrent restores | Not queued; do not submit a second restore annotation while one is active |
| Effective RPO | Determined by the configured schedule and the latest verified successful snapshot |
| Restore duration | No fixed RTO; measure restore tests on the installed storage, network, and control-plane topology |

For a Managed Cluster with more than one etcd replica, use the manual recovery
runbook that matches the installed controller version and topology. To reduce
RPO, configure a supported snapshot schedule or take an on-demand snapshot from
the danger zone, then validate object-storage capacity and restore behavior.

---

## When the controller is unavailable

If `kube-dc-k8-manager` or one of its dependencies is unavailable, use the
manual runbook in `kube-dc-k8-manager/docs/RESTORE.md` from the installed
version. It covers the restore sequence with controlled scaling and a debug Pod
that runs `etcdutl snapshot restore` against the member-0 PVC.

The following commands show the shape of the single-replica procedure. They are
not a substitute for the complete runbook, snapshot validation, and change
approval:

```bash
NS=<backing-namespace>
CLUSTER=<cluster-name>
DS=<datastore-name>   # usually <CLUSTER>-etcd
CP_REPLICAS=$(kubectl -n "$NS" get deployment "${CLUSTER}-cp" \
  -o jsonpath='{.spec.replicas}')

# 1. Pause the control plane and etcd.
kubectl -n "$NS" scale deployment "${CLUSTER}-cp" --replicas=0
kubectl -n "$NS" scale statefulset "${DS}-etcd" --replicas=0

# 2. Run a one-shot Pod that mounts the etcd-data-${DS}-etcd-0 PVC
#    and runs etcdutl snapshot restore against /var/run/etcd/default.etcd
#    (full Pod spec in the runbook)

# 3. Bring etcd back and wait for its StatefulSet to report ready.
kubectl -n "$NS" scale statefulset "${DS}-etcd" --replicas=1
kubectl -n "$NS" rollout status statefulset/"${DS}-etcd"

# 4. Restore the recorded control-plane replica count and verify it.
kubectl -n "$NS" scale deployment "${CLUSTER}-cp" --replicas="$CP_REPLICAS"
kubectl -n "$NS" rollout status deployment/"${CLUSTER}-cp"
```

The critical filesystem detail is the PVC mount path: restore into
`/var/run/etcd/default.etcd`, which is the etcd container's `--data-dir`,
not the PVC mount root.

---

## Architecture context

For the bigger picture of how Managed Clusters fit into
Kube-DC's networking, control-plane, and storage layers:

- [Architecture: multi-tenancy](architecture-multi-tenancy.md)
- [Architecture: networking](architecture-networking.md)
- Storage: ObjectBucketClaims are provisioned per-Project by the
  same Rook Ceph object store that backs the management cluster's
  S3 endpoint.
