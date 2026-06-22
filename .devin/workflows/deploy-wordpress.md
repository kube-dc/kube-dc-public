---
description: Deploy WordPress with managed MariaDB database, HTTPS exposure, and auto TLS
---

WordPress (Bitnami chart) only supports MySQL/MariaDB — there is no PostgreSQL
driver in WordPress core. Always use `engine: mariadb` for KdcDatabase.

## Steps

1. Ask the user for: organization name, project name, and database name (default: `wp-mariadb`).
   The Helm release name will be `wp` and the chart's resulting Service is `wp-wordpress`,
   so the auto-hostname is `wp-wordpress-{org}-{project}.kube-dc.cloud`.

2. Verify the project exists and is Ready:
```bash
kubectl get project {project-name} -n {org-name}
```

3. Create a cert-manager Issuer if one doesn't exist (idempotent — skip if present):
```yaml
apiVersion: cert-manager.io/v1
kind: Issuer
metadata:
  name: letsencrypt
  namespace: {org}-{project}
spec:
  acme:
    server: https://acme-v02.api.letsencrypt.org/directory
    email: admin@{org}.com
    privateKeySecretRef:
      name: letsencrypt-key
    solvers:
      - http01:
          ingress:
            ingressClassName: envoy
```

4. Create a managed MariaDB database. Note the field is `databaseName`, not `database`:
```yaml
apiVersion: db.kube-dc.com/v1alpha1
kind: KdcDatabase
metadata:
  name: {db-name}
  namespace: {org}-{project}
spec:
  engine: mariadb
  version: "11.4"
  replicas: 1
  cpu: "1"
  memory: 2Gi
  storage: 10Gi
  databaseName: wordpress
  username: app
  expose:
    type: internal
```

5. Wait for the database to be Ready (typically ~60s):
```bash
kubectl wait --for=jsonpath='{.status.phase}'=Ready kdcdb/{db-name} \
  -n {org}-{project} --timeout=5m
```

6. Create a "bridge" Secret with key `mariadb-password`. The KdcDatabase
   auto-secret `{db-name}-password` carries the password under key `password`,
   but the Bitnami WordPress chart hard-codes the secret key name to
   `mariadb-password` (no `existingSecretPasswordKey` parameter at chart
   v30.x). Without this step the WordPress pod boots into a tight CrashLoop
   reading an empty password file:

```bash
PASSWORD=$(kubectl get secret {db-name}-password -n {org}-{project} \
  -o jsonpath='{.data.password}' | base64 -d)
kubectl create secret generic wp-db-bridge \
  --namespace {org}-{project} \
  --from-literal=mariadb-password="$PASSWORD" \
  --dry-run=client -o yaml | kubectl apply -f -
```

7. Deploy WordPress using Helm. `service.type=LoadBalancer` plus the
   `expose-route: https` annotation routes through Envoy Gateway with auto-TLS;
   no separate Ingress or HTTPRoute is needed:

```bash
helm upgrade --install wp oci://registry-1.docker.io/bitnamicharts/wordpress \
  --namespace {org}-{project} \
  --version 30.0.13 \
  --set mariadb.enabled=false \
  --set externalDatabase.host={db-name}.{org}-{project}.svc \
  --set externalDatabase.port=3306 \
  --set externalDatabase.user=app \
  --set externalDatabase.database=wordpress \
  --set externalDatabase.existingSecret=wp-db-bridge \
  --set service.type=LoadBalancer \
  --set "service.annotations.service\.nlb\.kube-dc\.com/expose-route=https" \
  --wait --timeout 5m
```

8. Verify the deployment:
```bash
# Pod is ready (chart names everything {release}-wordpress)
kubectl get pods -l app.kubernetes.io/instance=wp -n {org}-{project}

# Auto-assigned hostname
kubectl get svc wp-wordpress -n {org}-{project} \
  -o jsonpath='{.metadata.annotations.service\.nlb\.kube-dc\.com/route-hostname-status}'

# End-to-end check (allow 1-2 min for TLS issue + propagation)
curl -s -o /dev/null -w '%{http_code}\n' \
  https://wp-wordpress-{org}-{project}.kube-dc.cloud
# Expect: 200
```

9. Report to the user:
   - Site URL: `https://wp-wordpress-{org}-{project}.kube-dc.cloud`
   - Admin URL: `https://wp-wordpress-{org}-{project}.kube-dc.cloud/wp-admin`
   - Admin user: `user`
   - Admin password: retrieve with
     `kubectl get secret wp-wordpress -n {org}-{project} -o jsonpath='{.data.wordpress-password}' | base64 -d`

## Common pitfalls

- **PostgreSQL backend is not supported** — WordPress core has no Postgres
  driver. The chart will install but the pod CrashLoops on every boot trying
  to speak MySQL protocol to a Postgres server.
- **Skipping the bridge Secret** — `existingSecret={db-name}-password` (the
  KdcDatabase auto-secret) does not work directly because that secret's key is
  `password`, while the chart reads `mariadb-password`.
- **Field name `database:` vs `databaseName:`** — only `databaseName` is
  accepted by the KdcDatabase CRD.
- **Helm release name vs Service name** — release `wp` with the wordpress
  chart produces Service `wp-wordpress`, so the auto-hostname carries that
  full name. If you want a shorter hostname, set `fullnameOverride: wp` in
  the values, or use release name `wordpress` and override accordingly.
