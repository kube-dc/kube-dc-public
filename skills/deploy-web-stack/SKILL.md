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
- The URL comes from a **`ClusterIP` Service with
  `service.nlb.kube-dc.com/expose-route: https`**. The platform creates the
  route and the certificate and allocates **no external IP**. Never use
  `type: LoadBalancer` for web traffic, and never create an Issuer.

Single-step references: `create-database` (plans, families, bindings),
`deploy-app` (workload hygiene), `expose-service` (other route options),
`manage-storage` (volumes). A worked WordPress example with every manifest is
`docs/cloud/deploy-wordpress-stack.md`.

## 0. Ask and check

Ask only what you cannot read: the application and image, which database
engine it supports, whether it needs production/HA, and a custom domain if
any. Then check the Project:

```bash
kubectl config view --minify -o jsonpath='{.contexts[0].context.namespace}{"\n"}'
kubectl get resourcequota
kubectl auth can-i create managedservices.services.kube-dc.com
kubectl auth can-i create deployments.apps
```

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
| MySQL, MariaDB | `host`, `port`, `database`, `username`, `password`, `uri`, `jdbcUrl`, `ca.crt` |

Endpoints are TLS-only. Use the delivered `host`; do not build one.

## 3. Add a volume (only if the app writes files)

Uploads, plugins and caches that must survive restarts need a PVC. Pick a
replicated class the Project offers; the cluster default can be node-local
(`local-path`: no expansion, data lost with the node):

```bash
kubectl get storageclass
```

Part 3 of the template uses `rbd-vm` (Ceph block, `ReadWriteOnce`). With
`ReadWriteOnce`, keep one replica or pin replicas to the volume's node.
For several replicas across nodes, keep files in object storage
(`manage-storage`) instead.

## 4. Deploy the application

Part 4 of the template: read every credential from the Secret, set requests
and limits, and add a readiness probe the image implements. Behind the
Gateway the app receives HTTP with `X-Forwarded-Proto: https`; apps that build
absolute URLs must honour it (WordPress: `WORDPRESS_CONFIG_EXTRA`, Django:
`SECURE_PROXY_SSL_HEADER`, Rails: `config.assume_ssl`).

Pod exec is not available in Project namespaces. Run one-off setup
(migrations, `wp core install`) as a Job with the same image and Secret.

## 5. Publish over HTTPS

Part 5 of the template: a `ClusterIP` Service with `expose-route: https` and
`https-redirect: "true"`. For a custom domain, first point its DNS record at
the auto-assigned hostname (CNAME), then add
`service.nlb.kube-dc.com/route-hostname: {domain}`.

The hostname is written once the certificate is ready (a minute or two):

```bash
kubectl get service {app} \
  -o jsonpath='{.metadata.annotations.service\.nlb\.kube-dc\.com/route-hostname-status}{"\n"}'
curl -sI https://{hostname}/
```

If it stays empty: `kubectl get certificate,challenge` and
`kubectl get events --field-selector involvedObject.name={app}`
(`GatewayHostnameConflict`, listener capacity exhausted).

## 6. Report

Tell the user the URL, the database plan and why, where the credentials are
(Secret name, never the values), and how to remove everything:

```bash
kubectl delete service/{app} deployment/{app} pvc/{app}-data
kubectl delete servicebinding/{app}-db-owner managedservice/{app}-db
```

The template sets `deletionPolicy: Retain`: deleting the `ManagedService`
keeps its data until the user deletes it from the console. Say so.

## When a public IP is right

Only for non-HTTP ports (SMTP, game servers, MQTT), gRPC, or a dedicated
address the user asked for. Then use a `LoadBalancer` with
`bind-on-default-gw-eip: "true"` (shares the Project gateway address) before
allocating anything new, check public IPv4 quota, and ask first
(`expose-service`, `manage-networking`).
