# Connect applications to a managed service

As a Project administrator or developer, create a binding to deliver service credentials to your application.
This guide covers Kubernetes Secret delivery within the service's Project.
Use the family guide for endpoint names, Secret keys, and client configuration.

## Before you begin

Check these requirements:

- The service is ready. See [Check service status](managed-services-status.md).
- You have the Project `admin` or `developer` role.
- Your workload and its ServiceAccount exist in the service's Project.
- You know the credential role and endpoint your application needs.

The service family defines roles and endpoints. A role controls credentials; an endpoint selects where the client connects.
For example, a read-only endpoint does not turn an owner credential into a read-only credential.

## Create a binding

1. Read the service UID:

   ```bash
   kubectl get managedservice <service> -n <project> \
     -o jsonpath='{.metadata.uid}{"\n"}'
   ```

   Replace `<service>` with the service name and `<project>` with its Project namespace.

2. Save this manifest as `app-service-binding.yaml`:

   ```yaml
   apiVersion: services.kube-dc.com/v1alpha1
   kind: ServiceBinding
   metadata:
     name: app-service
     namespace: <project>
   spec:
     serviceRef:
       name: <service>
     serviceUID: <service-uid>
     role: <role>
     consumer:
       kind: ServiceAccount
       name: <service-account>
       namespace: <project>
     delivery:
       mode: DataPlaneLocalSecret
       secretName: app-service
       endpointName: <endpoint>
   ```

   Replace `<service-uid>` with the UID you read and `<service-account>` with your workload's ServiceAccount name.
   Replace `<role>` and `<endpoint>` with values from the family guide.
   Use the same `<service>` and `<project>` as the lookup command.

3. Create the binding:

   ```bash
   kubectl apply -f app-service-binding.yaml
   ```

4. Check the binding result:

   ```bash
   kubectl get servicebinding app-service -n <project> -o yaml
   ```

   Wait for `Ready=True`. Read `status.secretRef` for the delivered Secret's name and namespace.
   If delivery fails, inspect the condition's reason and message.

The UID pins the binding to this service instance. A replacement service with the same name has a different UID.
Create a binding for the replacement instead of reusing the old identity.

## Configure the client

Mount the delivered Secret or select its keys with `secretKeyRef` in your workload.
Use the host and port it supplies instead of constructing service addresses.
Secret keys, protocol settings, and TLS requirements depend on the family.

Use these guides for complete client examples:

| Service | Connection guide |
|---|---|
| PostgreSQL | [Roles, endpoints, pooling, and application examples](postgresql-connect.md) |
| MySQL and MariaDB | [SQL clients and verified TLS](managed-services-mysql-mariadb.md#connect-an-application) |
| Valkey | [Cache endpoints and verified TLS](managed-services-valkey.md#connect-an-application) |
| ClickHouse | [HTTP and native clients](managed-services-clickhouse.md#connect-an-application) |
| Kafka | [Bootstrap servers and client authentication](managed-services-kafka.md#connect-an-application) |

Where the service supplies `ca.crt`, configure the client to trust that CA and verify the server name.
Do not disable certificate verification.
Verify the connection with the family guide's client command before you send application traffic.

## Credential access and rotation

A binding delivers a credential; its consumer field does not restrict Kubernetes Secret readers.
Project identities with Secret read permission can read delivered credentials.
Use separate Projects when teams or workloads must not share credentials.

Bindings of the same platform-managed role share its credential.
Creating another binding does not create an independent database user.
Rotation updates the role's bindings, and applications must load the replacement credential.
Environment variables remain fixed for a running container; restart affected Pods to load their replacement values.
Applications that read mounted files must reread them when credentials change.

See [Rotate credentials](managed-services-credentials.md) for manual rotation and schedules.
