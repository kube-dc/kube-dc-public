# Managed Kubernetes etcd-at-rest Encryption

How tenant `KdcCluster` resources opt into encryption-at-rest for their
control-plane etcd, what cluster operators need to provide on the
platform, and how rotation + backup interplay.

This page is for **operators and SREs running a Kube-DC cluster**. For
the tenant-facing toggle (`spec.encryption.etcd.enabled: true` on a
`KdcCluster`), see [Provisioning a Cluster](/cloud/provisioning-cluster).

---

## What this protects

When a tenant flips `spec.encryption.etcd.enabled: true` on their
`KdcCluster`, the **wire format of every value** the tenant's
Kubernetes apiserver writes into its etcd (Secrets and ConfigMaps by
default) becomes:

```
k8s:enc:kms:v2:kube-dc-kms-plugin:vault:vN:<wrapped DEK> <AES-256-GCM ciphertext>
```

Each row carries its own random **Data Encryption Key** (DEK). The DEK
is wrapped by a per-cluster **Key Encryption Key** (KEK) that lives in
OpenBao Transit and never leaves OpenBao in plaintext. Reading a row
requires unwrapping the DEK through OpenBao — i.e. anyone with raw
disk access to the etcd PVC sees only ciphertext.

This is independent of the **backup envelope** described in
[Managed Kubernetes etcd Backup & Restore](managed-k8s-etcd-backup-restore.md):
both layers exist together when a tenant opts in. The backup envelope
re-encrypts whole snapshots before upload to S3 using the same KEK,
so anyone with bucket-level read access still cannot decrypt without
OpenBao.

## What this does NOT protect

- **Tenant workload data on PVCs.** Application state, database tables,
  uploaded files — none of that goes through this path. Tenants who need
  application-level encryption use their own mechanisms (LUKS-backed
  StorageClass, application-side encryption, Velero with restic).
- **The management cluster's own etcd.** This page covers tenant
  `KdcCluster` etcd only. Platform-side encryption is a separate
  consideration handled at the install time.
- **Kubernetes resources outside the encrypted list.** Phase-1 default
  is `[secrets]`; tenants can opt into `[secrets, configmaps]`. Other
  resources (`leases`, `events`, `endpoints`, `pods`) remain in
  plaintext — they have high write rates and low sensitivity, so
  encrypting them would multiply apiserver and KMS load without a
  proportional security gain.

---

## Platform prerequisites

For tenant opt-in to actually work, the following must be true on the
management cluster:

| Prerequisite | How to check | Where it's configured |
|---|---|---|
| OpenBao is deployed and reachable | `kubectl -n openbao get sts openbao -o jsonpath='{.status.readyReplicas}'` returns `>=2` | Fleet config `OPENBAO_ENABLED=true` |
| `enable_openbao=true` in master-config | `kubectl -n kube-dc get secret master-config -o jsonpath='{.data.enable_openbao}' \| base64 -d` | Chart values `openBao.enabled: true` |
| `openbao_url` in master-config | `kubectl -n kube-dc get secret master-config -o jsonpath='{.data.openbao_url}' \| base64 -d` | Chart values `openBao.url: https://bao.<DOMAIN>` |
| `kube-dc-k8-manager` has `KMS_PLUGIN_IMAGE` env | `kubectl -n kube-dc get deploy kube-dc-k8-manager -o jsonpath='{.spec.template.spec.containers[0].env}' \| jq '.[] \| select(.name=="KMS_PLUGIN_IMAGE")'` | Chart values `k8Manager.kmsPluginImage` |
| `kube-dc-k8-manager` has `OPENBAO_URL` env | Same as above with `OPENBAO_URL` | Chart values `k8Manager.openBaoUrl` |
| Per-Org OpenBao Transit engine exists | `bao secrets list -namespace=<org>` shows `transit/` | Provisioned by the M3 KMSKey controller on first key request |

If any of these are missing, the kdccluster reconciler refuses to
provision the sidecar with a clear `EncryptionConfigError` condition
on the `KdcCluster` and the tenant cluster's apiserver continues to
run **without** encryption. There is no silent fallback.

---

## Architecture

```
┌────────────────────────────────────────────────────────────────┐
│  Kamaji TenantControlPlane pod (one per KdcCluster)            │
│                                                                │
│  ┌────────────────┐  encrypt/decrypt   ┌──────────────────┐    │
│  │ kube-apiserver │ ─────UDS RPC─────► │ kms-plugin       │    │
│  │  --enc-prov-   │ ◄──KMS v2 envelope │ sidecar          │    │
│  │  config=...    │                    │ (native init c.) │    │
│  └────────┬───────┘                    └────────┬─────────┘    │
│           │                                     │              │
│           │ etcd writes/reads                   │ HTTPS        │
│           ▼                                     ▼              │
│   ┌────────────────┐                  ┌──────────────────┐     │
│   │ Kamaji         │                  │ OpenBao          │     │
│   │ DataStore etcd │                  │ Transit          │     │
│   │ (per-cluster   │                  │ /v1/transit/     │     │
│   │  StatefulSet)  │                  │   keys/<KEK>/    │     │
│   └────────────────┘                  └──────────────────┘     │
└────────────────────────────────────────────────────────────────┘
```

Two binaries the platform ships:

- **`shalb/kube-dc-kms-plugin`** — small Go gRPC server that talks
  Kubernetes KMS v2 protocol on a Unix Domain Socket. Runs as a
  native sidecar (Kubernetes 1.29+ `restartPolicy: Always` on an
  init container).
- **`shalb/kube-dc-k8-manager`** — extends the existing controller
  manager. On reconcile of an encrypted `KdcCluster` it:

  1. Auto-creates a `KMSKey` CR (`<cluster>-etcd`, purpose `etcd`,
     retain on delete).
  2. Auto-creates a per-cluster `ServiceAccount` (`<cluster>-kms-plugin`).
  3. Builds an `EncryptionConfiguration` Secret with the KMS v2
     provider pointing at the UDS.
  4. Patches the Kamaji `TenantControlPlane` spec to mount the UDS
     volume, run the chown init + kms-plugin sidecar, project the SA
     token, and pass `--encryption-provider-config=...` to the apiserver.
  5. Mirrors the underlying KMSKey's rotation state onto
     `KdcCluster.status.encryption.kekRotation`.

One Go binary in the kube-dc-manager (NOT the same as k8-manager):

- **`KdcClusterEncryptionReconciler`** in `kube-dc-manager` — watches
  `KdcCluster`, reads its resolved `KMSKey`, and provisions the OpenBao
  ACL policy + Kubernetes-auth role bound to the per-cluster SA. The
  SA is forward-declared in the policy before the kdccluster reconciler
  creates it — that's intentional and lets OpenBao accept the binding
  immediately without races.

---

## Enabling encryption on a tenant cluster

Tenants enable it themselves through the `KdcCluster` spec. The
operator's role is to ensure the platform prerequisites are met (above)
and then verify the reconciler did its job.

### What the tenant submits

```yaml
apiVersion: k8s.kube-dc.com/v1alpha1
kind: KdcCluster
metadata:
  name: prod
  namespace: shalb-docs
spec:
  version: v1.35.0
  controlPlane:
    replicas: 3
  encryption:
    etcd:
      enabled: true              # the toggle
      # Everything else defaults — resources=[secrets], keyRef auto.
```

### What the operator checks afterwards

| Check | Command | Healthy value |
|---|---|---|
| `KdcCluster` is Ready | `kubectl -n <ns> get kdccluster <name> -o jsonpath='{.status.phase}'` | `Ready` |
| Resolved key reference | `kubectl -n <ns> get kdccluster <name> -o jsonpath='{.status.encryption.resolvedKeyRef}'` | `<name>-etcd` |
| KMSKey is Ready | `kubectl -n <ns> get kmskey <name>-etcd -o jsonpath='{.status.conditions[?(@.type=="Ready")].status}'` | `True` |
| Sidecar pod has kms-plugin running | `kubectl -n <ns> get pod -l kamaji.clastix.io/name=<name>-cp -o jsonpath='{.items[0].status.initContainerStatuses[?(@.name=="kms-plugin")].ready}'` | `true` |
| sidecar logged in to OpenBao | `kubectl -n <ns> logs <pod> -c kms-plugin \| grep "OpenBao login ok"` | a recent line |
| Sidecar is listening | `kubectl -n <ns> logs <pod> -c kms-plugin \| grep "kms-plugin listening"` | a recent line |

If everything is green the cluster is encrypted. To prove it
bit-for-bit, an operator with platform-admin etcdctl access can read
a fresh row from the Kamaji DataStore — every encrypted value carries
the `k8s:enc:kms:v2:bao:` wire prefix. Tenant exec into the etcd pod
is blocked by the cluster's `restrict-pod-exec-in-projects`
ValidatingAdmissionPolicy, so this verification is operator-only.

---

## KEK rotation

The Key Encryption Key — the OpenBao Transit key that wraps every DEK —
rotates on a schedule the **tenant** chooses. The platform owns nothing
here except OpenBao itself; rotation is driven by the M3 KMSKey
reconciler in the kube-dc-manager, which we lean on rather than ship a
separate CronJob.

### Tenant spec

```yaml
spec:
  encryption:
    etcd:
      enabled: true
      kekRotation:
        enabled: true
        interval: 90d
```

Validation bounds (rejected at reconcile time with a clear
`EncryptionConfigError` condition):

| Rule |
|---|
| `enabled: true` requires `interval` |
| Interval units must be `d`, `h`, `m`, `s` — `w` is NOT supported (M3 parser does not accept weeks) |
| Interval ≥ 7d (OpenBao Transit's own `min_rotation_interval` floor) |
| Interval ≤ 730d |
| Interval ≥ `spec.backup.retentionDays * 24h` when backups are enabled |

### What rotation does

1. Creates a **new** Transit key version. The kms-plugin sidecar's
   next Encrypt call uses the new version automatically.
2. New backup snapshots have `metadata.transitKeyVersion = N+1`.
3. **Old etcd rows do NOT bulk-rewrap.** They re-wrap on next Update.
   Bulk re-wrap is deferred to phase 6 (`kube-dc cluster rewrap-etcd`).
4. **`min_decryption_version` is NEVER advanced by automation.** Old
   DEKs and old backups remain decryptable indefinitely. See §
   "Advancing `min_decryption_version`" below — that's a manual +
   irreversible operator gesture.

### Tenant-side observability

The tenant sees rotation state at `status.encryption.kekRotation`:

```yaml
status:
  encryption:
    kekRotation:
      enabled: true
      currentVersion: 3                 # latest encryption version
      lastRotatedTime: 2026-06-07T...   # when N just bumped
      nextRotationTime: 2026-09-05T...  # scheduled next bump
      minDecryptionVersion: 1           # operator-controlled; never moves on its own
```

The same data is on the underlying `KMSKey/<cluster>-etcd` —
the KdcCluster mirror is for tenant convenience.

### Operator-initiated rotation outside the schedule

Three paths, in order of preference:

1. **Schedule the rotation via the tenant CR.** Set
   `spec.encryption.etcd.kekRotation.interval` to whatever cadence is
   desired; the M3 reconciler picks it up on its next loop.
2. **Direct Transit call as a platform admin.** Use the platform-root
   token (or a dedicated platform-admin OpenBao policy):

   ```bash
   bao write -force -namespace=<org> \
     transit/keys/<projectNS>-<projectName>-<keyName>/rotate
   ```

3. **NEVER grant the kms-plugin SA `rotate` capability.** Its
   `tcp-<cluster>` policy intentionally has only `encrypt`, `decrypt`,
   `keys` (read). Granting rotate would widen the tenant-side blast
   radius if a TCP pod is compromised.

---

## Advancing `min_decryption_version`

The single irreversible operator action. Advancing
`min_decryption_version` to `N` makes any DEK wrapped with a version
below `N` **undecryptable forever** — that includes etcd rows and S3
backups.

**Use only when:**

- A KEK version is suspected compromised AND
- Every backup wrapped with that version has aged out of retention OR
  been re-wrapped via the deferred `kube-dc cluster rewrap-backups` CLI

**Never use as a routine operation.** No automation does this; the
controllers explicitly refuse. Pre-flight checklist for the manual
operator action:

1. Inventory every S3 backup under `s3://<projectNS>-managed-k8s-backups/`
   and parse each `metadata.json` for its `transitKeyVersion`. Refuse
   to advance if any version is below the target.
2. Confirm every workload using this KEK has either re-wrapped or
   aged out of the affected version.
3. Confirm OpenBao audit traceability — every advance lands in the
   audit log.
4. Issue the advance against OpenBao:

   ```bash
   bao write -namespace=<org> \
     transit/keys/<projectNS>-<projectName>-<keyName>/config \
     min_decryption_version=N
   ```

5. File the incident regardless of outcome — even successful advances
   are unusual enough to warrant an operator note.

---

## Operational gotchas

### OpenBao outage behavior

The kms-plugin sidecar caches DEKs locally for ~5 minutes (the
apiserver's KMS v2 cache TTL). Short OpenBao outages are invisible;
long ones surface as apiserver errors on encrypted resource reads/writes.

| Outage duration | Tenant apiserver effect | Recovery |
|---|---|---|
| < 5 min | None (cache covers it) | Auto-recovers when OpenBao returns |
| 5 min – 1 h | Reads of recently-encrypted resources start to fail with `transformation failed` | sidecar re-logins automatically when OpenBao returns; apiserver retries succeed within ~60s |
| > 1 h | Same as above; tenant operators may file tickets | Same auto-recovery; no manual intervention required |

If the apiserver does NOT auto-recover after OpenBao returns, restart
the TCP pod (`kubectl -n <ns> delete pod -l kamaji.clastix.io/name=<cluster>-cp`).
That forces a fresh kms-plugin login on next pod start. If even that
doesn't recover, it's a P0 — file a ticket with the kms-plugin logs
attached.

### Transit key accidental delete

The KMSKey CR's `deletionPolicy: retain` prevents the platform from
sweeping the Transit key on KdcCluster delete. The kube-dc-manager
also sets `deletion_allowed=false` on the Transit key itself, which
means even a platform admin needs to flip that flag before OpenBao
will let them delete.

If a Transit key is somehow deleted with encrypted backups still
present, those backups are unrecoverable. There is no recovery path
short of restoring the OpenBao deployment from snapshot.

### KMS plugin can't log in

Symptom: TCP pod CrashLoops with the kms-plugin sidecar logging
`OpenBao login (mount=... role=tcp-<cluster>): 403`.

Cause: OpenBao ACL policy or Kubernetes-auth role drifted from the
expected shape. The kube-dc-manager's `KdcClusterEncryptionReconciler`
re-asserts these on every reconcile — usually just deleting the role
or policy out-of-band and letting the controller put it back fixes it.

Verify via:

```bash
bao read -namespace=<org> auth/k8s-host/role/tcp-<cluster>
```

The bound SA name should be `<cluster>-kms-plugin` in the project
namespace, the audience should be `openbao`, and the policy should
include `tcp-<cluster>`.

---

## Removing encryption from a cluster

This is intentionally not a one-step operation. Setting
`spec.encryption.etcd.enabled: false` on a cluster that previously
had it on would brick the apiserver: encrypted rows would become
unreadable because the apiserver would no longer have a KMS provider
configured to unwrap them.

The proper flow uses the two-step `disableRequested` migration documented
in the design (§12.3) and runbook §8 — phase-1 implementation is
deferred. Until the migration controller lands, the platform-admin
break-glass is:

```bash
kubectl -n <ns> annotate kdccluster <name> \
  security.kube-dc.com/encryption-force-disable-confirmed="$(date -u +%FT%TZ)"
```

That bypasses the safety guard. **Use only when the existing etcd rows
are already known unrecoverable** (e.g. OpenBao permanently lost). The
annotation is audit-flagged on every reconcile.

---

## Cross-references

- [Provisioning a Cluster](/cloud/provisioning-cluster) — tenant-facing toggle
- [Managed Kubernetes etcd Backup & Restore](managed-k8s-etcd-backup-restore.md) — backup envelope encryption companion
