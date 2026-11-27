---
name: deploy-web-stack
description: Deploy a complete web application in a Kube-DC Project — a managed database (PostgreSQL, MySQL or MariaDB) with delivered credentials, an optional persistent volume, and an HTTPS hostname with a platform certificate through the shared Gateway, without a public IP. Use for WordPress, CMSs, APIs and any app that needs a database and a URL.
---

# Deploy a Web Stack

One procedure for the common case: **app + managed database + HTTPS URL**.
It uses the platform's defaults, which agents most often get wrong:

- The database is a **managed service**, never a database container, a
  StatefulSet or a chart's bundled database.
- The plan is **`{family}-development`** unless the user asks for production
  or high availability. Production (`{family}-production`) runs three
  instances and draws three times the quota. Tell the user which you chose.
- The app is an **Application** (`apps.kube-dc.com/v1alpha1`): the platform
  creates its volume, Deployment, Service, HTTPS route and certificate, and
  allocates **no external IP**. Never use `type: LoadBalancer` for web
  traffic, and never create an Issuer. Only where the installation does not
  run Applications (step 0), use the template's plain manifests (step 6).

Single-step references: `create-database` (plans, families, bindings),
`deploy-app` (workload hygiene), `expose-service` (other route options),
`manage-storage` (volumes). A worked WordPress example with every manifest is the
Kube-DC documentation page *Deploy a full WordPress stack*.

## 0. Ask and check

Ask only what you cannot read: the application and image, which database
engine it supports, whether it needs production/HA, and a custom domain if
any. Then check the Project:

```bash
kubectl config view --minify -o jsonpath='{.contexts[0].context.namespace}{"\n"}'
kubectl get resourcequota
kubectl auth can-i create managedservices.services.kube-dc.com
kubectl auth can-i create applications.apps.kube-dc.com
curl -s https://backend.<domain>/api/platform/info   # applications.enabled, applications.create
```

`applications.enabled` and `applications.create` both the JSON value `true`:
step 3 deploys an Application. Both present and either `false`: use step 6
instead of step 3. If the request fails or the answer lacks either value,
do not guess: tell the user and ask which path their installation offers.
Decide this before you write anything; never apply an Application to find
out.

Pick the engine the application supports: WordPress and most PHP CMSs need
MySQL or MariaDB (WordPress has no PostgreSQL driver); Django, Rails,
Node and Go apps usually prefer PostgreSQL.

## 1. Read the offered plans

```bash
kubectl get serviceofferings -l services.kube-dc.com/class={family}
kubectl get serviceoffering {family}-development -o jsonpath='{.status.createExample}'
```

Use only plans listed as Ready. Ignore other names (qualification plans);
use `{family}-development` or `{family}-production`. The offering's
`createExample` is a ready manifest for that plan.

## 2. Create the database and its credentials

Name the service after the app plus `-db` (for example `shop-db`), never the
app's own name: the platform creates Services named after it, and an app
Service with the same name collides.

Apply [web-stack-template.yaml](web-stack-template.yaml) parts 1 and 2 (the
`ManagedService` and `ServiceBinding`), then wait:

```bash
kubectl wait --for=jsonpath='{.status.phase}'=Ready managedservice/{app}-db --timeout=15m
kubectl wait --for=condition=Ready servicebinding/{app}-db-owner --timeout=5m
```

The Secret `{app}-db-owner` carries the connection:

| Family | Keys |
|---|---|
| PostgreSQL | `host`, `port`, `dbname`, `username`, `password`, `uri` (`sslmode=verify-full`), `ca.crt` |
| MySQL, MariaDB | `host`, `port`, `database`, `username`, `password`, `uri`, `jdbcUrl`, `tls`, `ca.crt` |

Endpoints are TLS-only. Use the delivered `host`; do not build one.

## 3. Deploy the app as an Application

One Application runs the image, reads the credentials, keeps files on a
volume if the app writes any, and publishes it over HTTPS:

```yaml
apiVersion: apps.kube-dc.com/v1alpha1
kind: Application
metadata:
  name: "{app}"
spec:
  source:
    image: { reference: "{image}" }
  run:
    port: {port}          # the port the image listens on (WordPress: 80)
    healthPath: "{path}"  # optional: a path that answers 2xx without signing in
    env:
      - name: DB_PASSWORD # the names the image expects
        secretKeyRef: { name: "{app}-db-owner", key: password }
    volume: { path: "{data-path}", size: 10Gi, storageClass: "{replicated-class}" }
    restartOnSecretChange: true
```

- Credentials: map each key the image needs with `run.env` and
  `secretKeyRef`, or use `run.envFromSecrets: [{ name: "{app}-db-owner" }]`
  when the image reads the Secret's key names (`host`, `password`, ...) as
  they are.
- The volume (only if the app writes files: uploads, plugins, caches) is the
  claim `{app}-data`. Pick a replicated class the Project offers
  (`kubectl get storageclass`); the cluster default can be node-local
  (`local-path`: no expansion, data lost with the node). An app with a volume
  runs one pod and cannot autoscale; for several replicas, keep files in
  object storage (`manage-storage`) instead. Do not create the claim
  yourself: the Application refuses a claim it did not make (`NameInUse`).
- TLS to the database: the endpoints accept TLS only, and MySQL/MariaDB
  clients must ask for it. WordPress: `define('MYSQL_CLIENT_FLAGS',
  MYSQLI_CLIENT_SSL | MYSQLI_CLIENT_SSL_DONT_VERIFY_SERVER_CERT);` in
  `WORDPRESS_CONFIG_EXTRA`, for the app and for a WP-CLI Job (encrypted, but
  the certificate is not checked). Clients that can read `ca.crt` should
  verify it; PostgreSQL clients use the `uri` (`sslmode=verify-full`).
- Behind the gateway the app receives HTTP with `X-Forwarded-Proto: https`;
  apps that build absolute URLs must honour it (WordPress:
  `WORDPRESS_CONFIG_EXTRA`, Django: `SECURE_PROXY_SSL_HEADER`, Rails:
  `config.assume_ssl`).
- A custom domain goes in `run.hostnames: ["{domain}"]`, after its DNS points
  at the platform (a CNAME to the app's platform address). Only one hostname.

Pod exec is not available in Project namespaces. Run one-off setup
(migrations, `wp core install`) as a Job with the same image, variables and
Secret. A Job that needs the files mounts the claim `{app}-data` and must run
on the volume's node: give it a `podAffinity` on
`app.kubernetes.io/name: {app}` with `topologyKey: kubernetes.io/hostname`.
A migration that needs only the database is `run.releaseCommand` instead
(`deploy-app`).

## 4. Check the address

```bash
kubectl wait --for=condition=Ready app/{app} --timeout=15m
kubectl get app {app} -o jsonpath='{.status.url}{"\n"}'
curl -sI "$(kubectl get app {app} -o jsonpath='{.status.url}')"
```

The certificate takes 1 to 3 minutes. If the app does not get ready,
`kubectl describe app {app}`: the first condition that is not True
(`Released`, `Available`, `RoutePublished`) says what it waits for.

## 5. Report

Tell the user the URL, the database plan and why, where the credentials are
(Secret name, never the values), and how to remove everything:

```bash
kubectl delete app {app}
kubectl delete pvc {app}-data        # the files: kept unless deletionPolicy.volume is Delete
kubectl delete servicebinding/{app}-db-owner
# Remove the ManagedService only as described below.
```

The template sets `deletionPolicy: Retain`: deleting the `ManagedService`
keeps the database running in the Project, still charged, and only an
operator can remove it. Ask the user before applying whether they want that,
`Delete` (data and capacity freed on deletion, no final backup) or
`SnapshotAndDelete` (a final backup first, where the family and plan support
it), and set it. Keep the user's choice: do not replace `SnapshotAndDelete`
with `Delete`.

`Delete` needs the service's exact name in the
`services.kube-dc.com/confirm-delete` annotation. To remove a database
without a final backup, including one created with `Retain`, while the
`ManagedService` still exists:

```bash
db_uid=$(kubectl get managedservice {app}-db -o jsonpath='{.metadata.uid}') || exit 1
kubectl annotate managedservice {app}-db services.kube-dc.com/confirm-delete={app}-db
kubectl patch managedservice {app}-db --type merge \
  -p '{"spec":{"deletionPolicy":"Delete","deletionProtection":false}}'
kubectl get managedservice {app}-db -o yaml
# Repeat until Accepted=True and Ready=True both have observedGeneration
# equal to metadata.generation, acceptedRevision equals appliedRevision,
# acceptedGeneration equals appliedGeneration, and Stale is not True; and
# lastObservedAt advances and is under four minutes old. Stop and tell the
# user if these checks do not pass.
kubectl delete managedservice {app}-db
kubectl get pods -l "services.kube-dc.com/instance-uid=${db_uid}"   # No resources found
```

If pods remain, look at the service and its events:

```bash
kubectl get managedservice {app}-db -o yaml
kubectl get events --field-selector "involvedObject.uid=${db_uid}"
```

While the `ManagedService` still exists, its `Ready` condition says whether
the deletion is progressing or blocked. A `DeletionPolicyDowngraded` event
names the cause and the policy that was used. If the `ManagedService` is gone
but pods remain, tell the user to ask their provider to remove them.

## 6. Without Applications

Where step 0 found that the installation does not run Applications
(`applications.enabled` or `applications.create` false), apply parts 3 to 5
of [web-stack-template.yaml](web-stack-template.yaml) instead of step 3:

- Part 3, a PVC: replace `rbd-vm` with the replicated class you picked;
  without it, also remove the volume and its mount from part 4. With
  `ReadWriteOnce`, keep one replica or pin replicas to the volume's node.
- Part 4, the Deployment: every credential from the Secret, requests and
  limits, a readiness probe the image implements.
- Part 5, a `ClusterIP` Service with `service.nlb.kube-dc.com/expose-route:
  https` and `https-redirect: "true"`. For a custom domain, point its DNS at
  the auto-assigned hostname first, then add
  `service.nlb.kube-dc.com/route-hostname: {domain}`.

The hostname is written once the certificate is ready:

```bash
kubectl get service {app} \
  -o jsonpath='{.metadata.annotations.service\.nlb\.kube-dc\.com/route-hostname-status}{"\n"}'
```

If it stays empty: `kubectl get certificate,challenge` and
`kubectl get events --field-selector involvedObject.name={app}`. Remove it
with `kubectl delete service/{app} deployment/{app} pvc/{app}-data`.

## When a public IP is right

Only for non-HTTP ports (SMTP, game servers, MQTT), gRPC, or a dedicated
address the user asked for. Then use a `LoadBalancer` with
`bind-on-default-gw-eip: "true"` (shares the Project gateway address) before
allocating anything new, check public IPv4 quota, and ask first
(`expose-service`, `manage-networking`).
