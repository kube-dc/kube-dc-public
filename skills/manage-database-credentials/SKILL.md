---
name: manage-database-credentials
description: Deliver a managed service credential role to a workload with a ServiceBinding Secret, rotate it now with a RotateCredentials operation, or rotate it on a schedule with a ServiceCredentialPolicy. Replaces the deprecated DatabaseCredentialPolicy of db-manager databases.
---

## Prerequisites

- The `ManagedService` is Ready; record its `metadata.uid`.
- Know the Project's backing namespace: `{organization}-{project}`.
- The `admin` or `developer` Project role. `project-manager` may pause or
  change an existing policy but not create one.

## Model

- A **credential role** is a login the family declares: `owner` and
  `readonly` for PostgreSQL, MySQL, MariaDB and ClickHouse; `default` for
  Valkey (`read-only` on `valkey-ha`); `admin` and `client` for Kafka. All
  bindings of one role share one credential.
- A **`ServiceBinding`** delivers a role as a Secret in the Project and pins
  the service UID. It is the only way credentials reach workloads; status
  never carries values.
- A **`RotateCredentials`** operation issues a new password for a role and
  republishes it to every binding of that role.
- A **`ServiceCredentialPolicy`** does the rotation on an interval. It never
  creates a login or grants privileges.

## 1. Deliver a credential

[binding-template.yaml](binding-template.yaml):

```yaml
apiVersion: services.kube-dc.com/v1alpha1
kind: ServiceBinding
metadata:
  name: "{service-name}-{role}"
  namespace: "{backing-namespace}"
spec:
  serviceRef: { name: "{service-name}" }
  serviceUID: "{service-uid}"
  role: "{role}"
  consumer:
    kind: ServiceAccount
    name: "{workload-service-account}"
    namespace: "{backing-namespace}"
  delivery:
    secretName: "{service-name}-{role}"
```

```bash
kubectl apply -f binding.yaml
kubectl get servicebinding {service-name}-{role} -n {backing-namespace} -w   # READY True, CredentialDelivered
kubectl describe secret {service-name}-{role} -n {backing-namespace}           # keys only
```

The Secret carries the annotation `services.kube-dc.com/credential-version`;
`status.credentialVersion` on the binding matches it after every rotation.

## 2. Rotate now

[rotate-operation-template.yaml](rotate-operation-template.yaml):

```yaml
apiVersion: services.kube-dc.com/v1alpha1
kind: ServiceOperation
metadata:
  name: "{service-name}-rotate-{role}-{n}"
  namespace: "{backing-namespace}"
spec:
  serviceRef: { name: "{service-name}" }
  serviceUID: "{service-uid}"
  type: RotateCredentials
  idempotencyKey: "{service-name}-rotate-{role}-{n}"
  parameters:
    role: "{role}"
  execution:
    window: Immediate
```

Wait for `Succeeded`, then restart workloads that read the password from
environment variables (`kubectl rollout restart deployment/{name}`). Workloads
that mount the Secret as a file and reread it need no restart. The plan may
require provider approval (`AwaitingApproval`); tenants cannot approve.

## 3. Rotate on a schedule

[rotation-policy-template.yaml](rotation-policy-template.yaml):

```yaml
apiVersion: services.kube-dc.com/v1alpha1
kind: ServiceCredentialPolicy
metadata:
  name: "{service-name}-{role}-rotation"
  namespace: "{backing-namespace}"
spec:
  serviceRef: { name: "{service-name}" }
  serviceUID: "{service-uid}"
  role: "{role}"
  rotationIntervalSeconds: 2592000     # 30 days; 60 to 31536000
  paused: false
```

For an existing SQL login a DBA created (PostgreSQL, plan entitlement
`credentials.allowExistingUsers`), replace `role` with
`existingUser: { username: "{login}", database: "{database}" }`; the policy's
`status.credentialRole` is then the role bindings must use.

```bash
kubectl get servicecredentialpolicy -n {backing-namespace}
kubectl patch servicecredentialpolicy {name} -n {backing-namespace} --type merge -p '{"spec":{"paused":true}}'
```

Deleting a policy of a declared role keeps the bindings and their Secrets.

## Verification

Report the binding name, role, Secret name and keys, the credential version,
and for a rotation the operation phase. Never print `password` or `uri`.

## Deprecated: DatabaseCredentialPolicy

`DatabaseCredentialPolicy` rotated passwords of deprecated `KdcDatabase`
databases and projected them into a Secret with `dsn` and `database` keys.
Never create one. When a Project still has one, keep it until the database is
migrated (`docs/cloud/managed-services-migration.md`), then delete it with the
`KdcDatabase`.
