---
description: Deploy WordPress with managed PostgreSQL database, HTTPS exposure, and auto TLS
---

## Steps

1. Ask the user for: organization name, project name, and database name (default: `wordpress-db`)

2. Verify the project exists and is Ready:
```bash
kubectl get project {project-name} -n {org-name}
```

3. Create a cert-manager Issuer if one doesn't exist:
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

4. Create a managed PostgreSQL database:
```yaml
apiVersion: db.kube-dc.com/v1alpha1
kind: KdcDatabase
metadata:
  name: {db-name}
  namespace: {org}-{project}
spec:
  engine: postgresql
  version: "16"
  replicas: 2
  cpu: "1"
  memory: 2Gi
  storage: 10Gi
  database: wordpress
  username: app
  expose:
    type: internal
```

5. Wait for the database to be Ready:
```bash
kubectl get kdcdb {db-name} -n {org}-{project} -w
```

6. Deploy WordPress using Helm:
```bash
helm install wordpress oci://registry-1.docker.io/bitnamicharts/wordpress \
  --namespace {org}-{project} \
  --set mariadb.enabled=false \
  --set externalDatabase.host={db-name}-rw.{org}-{project}.svc \
  --set externalDatabase.port=5432 \
  --set externalDatabase.user=app \
  --set externalDatabase.database=wordpress \
  --set externalDatabase.existingSecret={db-name}-app \
  --set service.type=LoadBalancer \
  --set "service.annotations.service\.nlb\.kube-dc\.com/expose-route=https"
```

7. Verify the deployment:
```bash
kubectl get pods -l app.kubernetes.io/name=wordpress -n {org}-{project}
kubectl get svc wordpress -n {org}-{project} -o jsonpath='{.metadata.annotations.service\.nlb\.kube-dc\.com/route-hostname-status}'
```

8. Report the WordPress URL to the user: `https://wordpress-{org}-{project}.kube-dc.cloud`
