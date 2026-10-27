---
description: Deploy WordPress with a managed MariaDB, a replicated volume and HTTPS through the platform Gateway
---

WordPress core has no PostgreSQL driver: use a managed MariaDB (or MySQL)
service. The full manifest set, with explanations, is
`docs/cloud/deploy-wordpress-stack.md`; a Helm variant is
`examples/organization/wordpress/`.

## Steps

1. Ask the user for the organization and project, and whether they need
   production or high availability (default: no).

2. Select the Project and check that it is Ready and has quota:
```bash
kube-dc use {domain}/{org}/{project}
kubectl get project {project} -n {org}
kubectl get resourcequota -n {org}-{project}
```

3. Read the MariaDB plans offered to the Project. Use `mariadb-development`
   (one server) unless the user asked for production or HA; then
   `mariadb-production` (3-node Galera, three times the quota). Say which you
   chose.
```bash
kubectl get serviceofferings -n {org}-{project} -l services.kube-dc.com/class=mariadb
```

4. Create the database and its credential binding (`create-database` skill).
   Name it `wordpress-db`, not `wordpress`: the platform creates Services
   named after it.
```yaml
apiVersion: services.kube-dc.com/v1alpha1
kind: ManagedService
metadata:
  name: wordpress-db
  namespace: {org}-{project}
spec:
  classRef: {name: mariadb}
  planRef: {name: mariadb-development}
  placement: {mode: ProviderShared}
  connectivity: {classRef: {name: tenant-native}}
  parameters: {database: wordpress}
  deletionPolicy: Retain
---
apiVersion: services.kube-dc.com/v1alpha1
kind: ServiceBinding
metadata:
  name: wordpress-db-password
  namespace: {org}-{project}
spec:
  serviceRef: {name: wordpress-db}
  role: owner
  consumer: {kind: ServiceAccount, name: default, namespace: {org}-{project}}
  delivery: {secretName: wordpress-db-password}
```
```bash
kubectl wait --for=jsonpath='{.status.phase}'=Ready managedservice/wordpress-db -n {org}-{project} --timeout=15m
kubectl wait --for=condition=Ready servicebinding/wordpress-db-password -n {org}-{project} --timeout=5m
```

5. Create a PVC on a replicated class (for example `rbd-vm`, not the
   node-local default) and the WordPress Deployment. Read `host`,
   `username`, `password` and `database` from the Secret
   `wordpress-db-password`, and set `WORDPRESS_CONFIG_EXTRA` so WordPress
   honours `X-Forwarded-Proto: https` from the Gateway. Use the manifests in
   `docs/cloud/deploy-wordpress-stack.md`.

6. Publish it over HTTPS with a ClusterIP Service. The platform creates the
   route and the certificate; there is no Issuer to create and no external IP
   is allocated. Do not use `type: LoadBalancer`.
```yaml
apiVersion: v1
kind: Service
metadata:
  name: wordpress
  namespace: {org}-{project}
  annotations:
    service.nlb.kube-dc.com/expose-route: https
    service.nlb.kube-dc.com/https-redirect: "true"
spec:
  type: ClusterIP
  selector: {app: wordpress}
  ports:
  - {name: http, port: 80, targetPort: 80}
```

7. Wait for the hostname (set once the certificate is issued) and report it:
```bash
kubectl get svc wordpress -n {org}-{project} \
  -o jsonpath='{.metadata.annotations.service\.nlb\.kube-dc\.com/route-hostname-status}'
```
   If it stays empty, check `kubectl get certificate,challenge -n {org}-{project}`
   and the Service events (`GatewayHostnameConflict`, listener capacity).

8. Install WordPress headless with a WP-CLI Job (pod exec is not available in
   Project namespaces), as shown in the guide.
