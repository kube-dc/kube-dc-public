---
name: deploy-app
description: Deploy a containerized application to a Kube-DC project with optional database, service exposure (HTTPS via Gateway or direct EIP), and persistent storage. Covers Helm deployments and raw manifests.
---

## Prerequisites
- Target project must exist and be Ready
- Project namespace: `{org}-{project}`
- For HTTPS exposure: cert-manager `Issuer` must exist (or will be created)
- **Quota**: verify sufficient CPU, memory, and pod capacity before deploying — use the `check-quota` skill

## Steps

### 1. Create Namespace Resources (if needed)

If the app needs HTTPS with auto TLS, ensure an Issuer exists:

```yaml
apiVersion: cert-manager.io/v1
kind: Issuer
metadata:
  name: letsencrypt
  namespace: {project-namespace}
spec:
  acme:
    server: https://acme-v02.api.letsencrypt.org/directory
    email: {email}
    privateKeySecretRef:
      name: letsencrypt-key
    solvers:
      - http01:
          ingress:
            ingressClassName: envoy
```

### 2. Create the Deployment

```yaml
apiVersion: apps/v1
kind: Deployment
metadata:
  name: {app-name}
  namespace: {project-namespace}
  labels:
    app: {app-name}
spec:
  replicas: {replicas}
  selector:
    matchLabels:
      app: {app-name}
  template:
    metadata:
      labels:
        app: {app-name}
    spec:
      containers:
        - name: {app-name}
          image: {image}
          ports:
            - containerPort: {port}
          resources:
            requests:
              cpu: "100m"
              memory: "128Mi"
            limits:
              cpu: "500m"
              memory: "512Mi"
```

### 3. Create the Service

**For HTTP/HTTPS apps (Gateway Route — recommended):**

```yaml
apiVersion: v1
kind: Service
metadata:
  name: {app-name}
  namespace: {project-namespace}
  annotations:
    service.nlb.kube-dc.com/expose-route: "https"
    # Optional: service.nlb.kube-dc.com/route-hostname: "myapp.example.com"
    # Optional: service.nlb.kube-dc.com/route-port: "{port}"
spec:
  type: LoadBalancer
  ports:
    - port: {port}
      targetPort: {port}
  selector:
    app: {app-name}
```

→ Auto hostname: `{service-name}-{project-namespace}.kube-dc.cloud`
   The hostname is derived from the **Service** name, not the Deployment or
   Helm release name. For Helm charts that use `{release}-{chart}` as the
   default name (e.g. release `wp` + chart `wordpress` → Service `wp-wordpress`),
   the hostname becomes `wp-wordpress-{project-namespace}.kube-dc.cloud`. If
   you want a shorter hostname, set `fullnameOverride` in the chart values.
→ Auto TLS certificate via Let's Encrypt

**For TCP/UDP apps (Direct EIP):**

```yaml
apiVersion: kube-dc.com/v1
kind: EIp
metadata:
  name: {app-name}-eip
  namespace: {project-namespace}
spec:
  externalNetworkType: public
---
apiVersion: v1
kind: Service
metadata:
  name: {app-name}
  namespace: {project-namespace}
  annotations:
    service.nlb.kube-dc.com/bind-on-eip: "{app-name}-eip"
spec:
  type: LoadBalancer
  ports:
    - port: {port}
      targetPort: {port}
  selector:
    app: {app-name}
```

### 4. Add Database (Optional)

If the app needs a database, use the `create-database` skill to provision one, then
add the secret references to the deployment.

**Pick the engine the app actually supports — don't default to PostgreSQL for
everything.** Many off-the-shelf charts and apps are MySQL-only:

| Engine | Apps that REQUIRE this engine |
|--------|------------------------------|
| `mariadb` (or `mysql`) | WordPress, Joomla, Drupal (MySQL by default), Magento, Matomo, MediaWiki, phpBB, Moodle, Mautic, Roundcube, Bitnami's `*-mariadb` charts |
| `postgresql` | Discourse, Mastodon, GitLab, Gitea, Sentry, Keycloak, Harbor, Outline, Plausible, Metabase, NextCloud (recommended), most modern Node/Go/Rust apps |
| Either, app-configurable | Drupal, NextCloud, Redmine, Zabbix, Jira, Confluence — verify the chart's `externalDatabase.type` / `database.type` parameter |

Picking the wrong engine produces a tight CrashLoop with confusing log lines —
e.g. WordPress against PostgreSQL boots, sets `MARIADB_HOST`, then hangs on
`/wp-login.php` because there's no Postgres driver in WP core.

**Bridging KdcDatabase secrets to chart-expected key names.** The
`KdcDatabase` auto-secret stores the password under key `password`. Many
Helm charts hard-code a different key name:

| Chart | Expected secret key | Workaround |
|-------|--------------------|-----------|
| Bitnami WordPress | `mariadb-password` | Bridge Secret (below) |
| Bitnami Discourse, Bitnami Joomla | `db-password` | Bridge Secret |
| Most modern charts | configurable via `existingSecretPasswordKey` | Set the parameter |

When the chart has no `existingSecretPasswordKey` parameter, create a
small bridge Secret aliasing the password to the chart's expected key name:

```bash
PASSWORD=$(kubectl get secret {db-name}-password -n {project-namespace} \
  -o jsonpath='{.data.password}' | base64 -d)
kubectl create secret generic {app-name}-db-bridge \
  --namespace {project-namespace} \
  --from-literal={chart-expected-key}="$PASSWORD" \
  --dry-run=client -o yaml | kubectl apply -f -
```

Then reference `existingSecret: {app-name}-db-bridge` in the chart values.

**PostgreSQL:**

**PostgreSQL:**
```yaml
env:
  - name: DB_PASSWORD
    valueFrom:
      secretKeyRef:
        name: {db-name}-app           # Secret: {db-name}-app
        key: password
  - name: DB_HOST
    value: "{db-name}-rw.{project-namespace}.svc"
  - name: DATABASE_URL
    value: "postgresql://app:$(DB_PASSWORD)@$(DB_HOST):5432/{database}"
```

**MariaDB:**
```yaml
env:
  - name: DB_PASSWORD
    valueFrom:
      secretKeyRef:
        name: {db-name}-password      # Secret: {db-name}-password
        key: password
  - name: DB_HOST
    value: "{db-name}.{project-namespace}.svc"
  - name: DATABASE_URL
    value: "mysql://app:$(DB_PASSWORD)@$(DB_HOST):3306/{database}"
```

### Helm Deployment Alternative

For Helm-based apps:

```bash
helm install {release-name} {chart} \
  --namespace {project-namespace} \
  --set service.type=LoadBalancer \
  --set service.annotations."service\.nlb\.kube-dc\.com/expose-route"=https
```

## Verification

After deploying, run these checks:

```bash
# 1. Check deployment rollout
kubectl rollout status deployment/{app-name} -n {project-namespace}
# Expected: "successfully rolled out"

# 2. Verify pods are Running
kubectl get pods -l app={app-name} -n {project-namespace}
# Expected: All pods STATUS=Running, READY=1/1

# 3. Check service has endpoint
kubectl get endpoints {app-name} -n {project-namespace}
# Expected: Shows pod IPs in ENDPOINTS column (not <none>)

# 4. For Gateway Route — verify hostname assigned
kubectl get svc {app-name} -n {project-namespace} -o jsonpath='{.metadata.annotations.service\.nlb\.kube-dc\.com/route-hostname-status}'
# Expected: {app-name}-{project-namespace}.kube-dc.cloud

# 5. Test HTTP endpoint (if exposed via Gateway)
curl -s -o /dev/null -w "%{http_code}" https://\{app-name\}-\{project-namespace\}.kube-dc.cloud
# Expected: 200 (may take 1-2 min for TLS cert provisioning)
```

**Success**: Pods running, endpoints assigned, hostname active, HTTP 200.
**Failure**:
- Pods not starting: `kubectl describe pod -l app={app-name} -n {project-namespace}`
- No endpoints: selector doesn't match pod labels
- No hostname: Issuer may be missing — check `kubectl get issuer -n {project-namespace}`
- 503/404: App may not be listening on the expected port
## Safety
- Always set resource requests/limits on containers
- Default to Gateway Route (`expose-route: https`) for web apps
- Use Direct EIP only for non-HTTP protocols
- Verify Issuer exists before using `expose-route: https`
