# Rotate managed service credentials

As a Project administrator or developer, rotate a published credential role manually or on a schedule.
This guide covers platform-managed roles on services whose plans allow `RotateCredentials`.
For PostgreSQL SQL logins and superuser access, see [PostgreSQL credentials](postgresql-credentials.md).

## Before you begin

Check these requirements:

- The service is ready, and you know its name and UID.
- You have the Project `admin` or `developer` role.
- The family supports rotation for the selected role, and the plan allows it.
- Your applications can reload credentials and reconnect after rotation.

Role names differ between families. Examples include PostgreSQL, MySQL, and MariaDB `owner`, and Valkey `default`.
Use the family guide to check supported roles and any credential overlap period.
Do not assume that the previous password remains usable after rotation.

Read the service UID before you create an operation or policy:

```bash
kubectl get managedservice <service> -n <project> \
  -o jsonpath='{.metadata.uid}{"\n"}'
```

Replace `<service>` with the service name and `<project>` with its Project namespace.

## Rotate a role manually

1. Submit the `RotateCredentials` example in [Request an operation](managed-services-operations.md#submit-an-operation).
2. Wait for the operation to reach `Succeeded`.
3. Check the affected bindings:

   ```bash
   kubectl get servicebindings -n <project>
   ```

   Confirm that bindings for the role report `Ready=True` and the replacement credential version.

4. Reload the credential in your applications.
5. Verify that each application can reconnect.

All bindings of the same role receive the replacement credential.
An operation's success does not prove that applications have loaded it.

## Schedule rotation

1. Save this manifest as `app-credential-rotation.yaml`:

   ```yaml
   apiVersion: services.kube-dc.com/v1alpha1
   kind: ServiceCredentialPolicy
   metadata:
     name: app-credential-rotation
     namespace: <project>
   spec:
     serviceRef:
       name: <service>
     serviceUID: <service-uid>
     role: <role>
     rotationIntervalSeconds: 2592000
     paused: false
   ```

   Replace `<service-uid>` with the UID you read and `<role>` with a supported credential role.
   Use the same `<service>` and `<project>` as the lookup command.
   The interval is 30 days. Allowed values range from 60 to 31536000 seconds.

2. Create the policy:

   ```bash
   kubectl apply -f app-credential-rotation.yaml
   ```

3. Check its schedule:

   ```bash
   kubectl get servicecredentialpolicy app-credential-rotation -n <project> -o yaml
   ```

   A scheduled policy reports `Ready=True` with reason `Scheduled` and a `status.nextRotationAt` value.
   After a successful rotation, the reason can be `Rotated`.

Only one policy can manage a credential target. A duplicate reports `CredentialPolicyConflict`.
The first declared-role rotation is due one interval after policy creation.
Later intervals start after the previous attempt finishes, including a failed attempt.
Missed intervals produce one attempt, not a series of catch-up rotations.

A policy submits rotation only while the service is ready.
If the plan requires approval, its operations wait for the provider in `AwaitingApproval`.
Project identities cannot modify or cancel policy-created operations.

## Pause or retire a policy

1. Pause new submissions:

   ```bash
   kubectl patch servicecredentialpolicy app-credential-rotation -n <project> \
     --type merge -p '{"spec":{"paused":true}}'
   ```

2. Check the policy until `status.activeOperation` is empty.
3. Read the result of `status.lastOperation`, if present.
4. Confirm that applications connect with the current credential.

To resume the schedule, set `paused: false` in the policy manifest and apply it.
The interval and paused state can change; the service identity and credential target cannot.

To remove a declared-role schedule after these checks, delete its policy:

```bash
kubectl delete servicecredentialpolicy app-credential-rotation -n <project>
```

Deleting a declared-role policy stops future rotations. Its role, bindings, and credentials remain.
Existing PostgreSQL login policies have additional cleanup behavior; see [Retire a PostgreSQL policy](postgresql-credentials.md#retire-a-policy).
