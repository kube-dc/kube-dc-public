# Deploy a Full WordPress Stack

This guide deploys a production-shaped WordPress on Kube-DC using the platform's managed services end to end — no cluster to provision, and every step below was tested exactly as written:

- **Managed MariaDB** provisioned by the platform, with **daily backups encrypted by a project KMS key**
- **Rotating database credentials** stored in the platform vault and projected into a Secret
- A **shared Ceph-backed volume** that two WordPress replicas and admin Jobs read and write together
- **S3 object storage** for content archives, with per-bucket access keys
- **HTTPS exposure with an automatic certificate** — one annotation on the Service
- **Autoscaling** with a HorizontalPodAutoscaler
- Headless WordPress installation with **WP-CLI running as a Job**

```mermaid
flowchart LR
    U([Browser]) -->|"HTTPS + auto cert"| GW["Platform Gateway"]
    GW --> S["Service wordpress"]
    subgraph P["Your Project"]
        S --> W1["wordpress pod"]
        S --> W2["wordpress pod"]
        W1 & W2 --- V[("shared volume<br/>rbd-vm")]
        W1 & W2 -->|"rotating creds"| DB[("Managed MariaDB")]
        J["backup Job"] --- V
    end
    DB -->|"daily, KMS-encrypted"| S3[("Project S3 bucket")]
    J -->|"wp-content archive"| S3B[("wordpress-files bucket")]
```

## Prerequisites

- A Kube-DC [project](first-project.md) with `egressNetworkType: cloud`
- [CLI access](cli-kubeconfig.md) — `kubectl` working against your project
- Replace `my-project` below with your project namespace

## Step 1 — Platform services

One manifest creates the data layer: an encryption key, an S3 bucket, the managed database with encrypted backups, and a rotating application credential.

```yaml title="01-platform-services.yaml"
# Encryption key for database backups. Lives in the platform vault
# (OpenBao Transit) — key material never leaves it.
apiVersion: security.kube-dc.com/v1alpha1
kind: KMSKey
metadata:
  name: wp-backup-key
  namespace: my-project
spec:
  purpose: backup
  algorithm: aes256-gcm96
  deletionPolicy: retain
---
# S3 bucket for content archives. The platform creates the bucket plus a
# Secret and ConfigMap of the same name holding access keys and endpoint.
apiVersion: objectbucket.io/v1alpha1
kind: ObjectBucketClaim
metadata:
  name: wordpress-files
  namespace: my-project
spec:
  generateBucketName: wordpress-files
  storageClassName: ceph-bucket
---
# Managed MariaDB. The platform provisions, operates and backs it up daily —
# backups land in your project's backup bucket, encrypted with the key above.
apiVersion: db.kube-dc.com/v1alpha1
kind: KdcDatabase
metadata:
  name: wordpress-db          # NOT "wordpress" — see the note below
  namespace: my-project
spec:
  engine: mariadb
  version: "11.4"
  replicas: 1
  cpu: "1"
  memory: 1Gi
  storage: 10Gi
  databaseName: wordpress
  username: app
  expose:
    type: internal
  backup:
    enabled: true
    schedule: "0 2 * * *"
    retentionDays: 7
    encryption:
      enabled: true
      kmsKeyName: wp-backup-key
---
# Rotating application credential: the password lives in the platform vault,
# rotates monthly, and is projected into the wordpress-db-auth Secret with
# keys: host, port, username, password, database, dsn.
apiVersion: security.kube-dc.com/v1alpha1
kind: DatabaseCredentialPolicy
metadata:
  name: wordpress-app
  namespace: my-project
spec:
  databaseRef:
    name: wordpress-db
  mode: static-rotated
  username: app
  rotation:
    interval: 720h
    strategy: rolling
  sync:
    enabled: true
    targetSecretName: wordpress-db-auth
```

:::caution Name the database differently from your app Service
The platform creates a Service named after the `KdcDatabase` (here `wordpress-db.my-project.svc:3306`). If you name the database `wordpress` and later create an app Service called `wordpress`, they collide. Keep them distinct.
:::

```bash
kubectl apply -f 01-platform-services.yaml

# Wait for the database (≈1 minute) and the projected credential:
kubectl get kdcdatabase wordpress-db     # PHASE: Ready
kubectl get secret wordpress-db-auth     # created by the credential policy
```

## Step 2 — WordPress

The application layer: a Ceph-backed content volume, two co-located replicas that share it, a Service whose single annotation publishes the app over HTTPS, and an autoscaler.

```yaml title="02-wordpress.yaml"
# Content volume on Ceph (rbd-vm). ReadWriteOnce attaches to one node;
# the Deployment co-locates all replicas on that node with podAffinity,
# so every pod reads and writes the same /var/www/html.
apiVersion: v1
kind: PersistentVolumeClaim
metadata:
  name: wordpress-content
  namespace: my-project
spec:
  accessModes: ["ReadWriteOnce"]
  storageClassName: rbd-vm
  resources:
    requests:
      storage: 10Gi
---
apiVersion: apps/v1
kind: Deployment
metadata:
  name: wordpress
  namespace: my-project
  labels:
    app: wordpress
spec:
  replicas: 2
  selector:
    matchLabels:
      app: wordpress
  strategy:
    type: RollingUpdate
    rollingUpdate:
      maxSurge: 1
      maxUnavailable: 0
  template:
    metadata:
      labels:
        app: wordpress
    spec:
      affinity:
        podAffinity:
          requiredDuringSchedulingIgnoredDuringExecution:
          - labelSelector:
              matchLabels:
                app: wordpress
            topologyKey: kubernetes.io/hostname
      containers:
      - name: wordpress
        image: wordpress:6.8-apache
        ports:
        - containerPort: 80
          name: http
        env:
        - name: WORDPRESS_DB_HOST
          valueFrom:
            secretKeyRef: {name: wordpress-db-auth, key: host}
        - name: WORDPRESS_DB_USER
          valueFrom:
            secretKeyRef: {name: wordpress-db-auth, key: username}
        - name: WORDPRESS_DB_PASSWORD
          valueFrom:
            secretKeyRef: {name: wordpress-db-auth, key: password}
        - name: WORDPRESS_DB_NAME
          value: wordpress
        - name: WORDPRESS_CONFIG_EXTRA
          value: |
            /* Behind the platform HTTPS gateway */
            if (isset($_SERVER['HTTP_X_FORWARDED_PROTO']) && $_SERVER['HTTP_X_FORWARDED_PROTO'] === 'https') {
              $_SERVER['HTTPS'] = 'on';
            }
        volumeMounts:
        - name: content
          mountPath: /var/www/html
        resources:
          requests: {cpu: 250m, memory: 384Mi}
          limits: {cpu: "1", memory: 768Mi}
        readinessProbe:
          httpGet: {path: /wp-includes/images/blank.gif, port: 80}
          initialDelaySeconds: 15
          periodSeconds: 10
        livenessProbe:
          tcpSocket: {port: 80}
          initialDelaySeconds: 30
          periodSeconds: 20
      volumes:
      - name: content
        persistentVolumeClaim:
          claimName: wordpress-content
---
# One annotation publishes the app: the platform creates the HTTPS listener,
# certificate and route at https://wordpress-<namespace>.<cluster-domain>
apiVersion: v1
kind: Service
metadata:
  name: wordpress
  namespace: my-project
  annotations:
    service.nlb.kube-dc.com/expose-route: https
spec:
  type: LoadBalancer        # expose-route is processed on LoadBalancer Services
  selector:
    app: wordpress
  ports:
  - port: 80
    targetPort: 80
    name: http
---
apiVersion: autoscaling/v2
kind: HorizontalPodAutoscaler
metadata:
  name: wordpress
  namespace: my-project
spec:
  scaleTargetRef:
    apiVersion: apps/v1
    kind: Deployment
    name: wordpress
  minReplicas: 2
  maxReplicas: 4
  metrics:
  - type: Resource
    resource:
      name: cpu
      target:
        type: Utilization
        averageUtilization: 70
```

```bash
kubectl apply -f 02-wordpress.yaml

# The platform assigns the hostname and issues the certificate (≈1–2 min):
kubectl get svc wordpress -o jsonpath='{.metadata.annotations.service\.nlb\.kube-dc\.com/route-hostname-status}'
kubectl get certificate            # wordpress-tls  READY: True
kubectl get httproute              # wordpress-route
```

:::tip About the shared volume
`ReadWriteOnce` on Ceph RBD attaches the volume to one node; every pod on that node can mount it simultaneously. The `podAffinity` rule keeps all replicas (and the Jobs below) on that node, so they genuinely share `/var/www/html` — writes from one pod are immediately visible to the others. The trade-off is that all replicas live on one node at a time; the volume and database remain safe across node failure, and the pods reschedule together.
:::

## Step 3 — Install WordPress headlessly

Instead of the browser wizard, run WP-CLI as a Job. Admin tasks in projects run as Jobs — direct `kubectl exec` into pods is restricted in project namespaces.

```bash
# Admin password, kept in a Secret (you can also store it in the
# Secrets Manager in the console):
kubectl create secret generic wordpress-admin \
  --from-literal=password="$(openssl rand -base64 18)"
```

```yaml title="03-install-job.yaml"
apiVersion: batch/v1
kind: Job
metadata:
  name: wordpress-install
  namespace: my-project
spec:
  backoffLimit: 3
  ttlSecondsAfterFinished: 3600
  template:
    spec:
      restartPolicy: Never
      # run on the node that holds the content volume
      affinity:
        podAffinity:
          requiredDuringSchedulingIgnoredDuringExecution:
          - labelSelector:
              matchLabels:
                app: wordpress
            topologyKey: kubernetes.io/hostname
      containers:
      - name: wp-cli
        image: wordpress:cli
        command: ["sh", "-c"]
        args:
        - |
          set -e
          until wp core is-installed --path=/var/www/html 2>/dev/null; do
            if wp core install --path=/var/www/html \
                 --url="https://REPLACE-WITH-YOUR-HOSTNAME" \
                 --title="My Site" \
                 --admin_user=admin \
                 --admin_password="$ADMIN_PASSWORD" \
                 --admin_email=admin@example.com \
                 --skip-email; then break; fi
            echo "waiting for wp-config/database..."; sleep 10
          done
          echo "WordPress installed."
        env:
        - name: WORDPRESS_DB_HOST
          valueFrom: {secretKeyRef: {name: wordpress-db-auth, key: host}}
        - name: WORDPRESS_DB_USER
          valueFrom: {secretKeyRef: {name: wordpress-db-auth, key: username}}
        - name: WORDPRESS_DB_PASSWORD
          valueFrom: {secretKeyRef: {name: wordpress-db-auth, key: password}}
        - name: WORDPRESS_DB_NAME
          value: wordpress
        - name: ADMIN_PASSWORD
          valueFrom: {secretKeyRef: {name: wordpress-admin, key: password}}
        volumeMounts:
        - name: content
          mountPath: /var/www/html
      volumes:
      - name: content
        persistentVolumeClaim:
          claimName: wordpress-content
```

Set `--url` to the hostname from Step 2, then:

```bash
kubectl apply -f 03-install-job.yaml
kubectl wait --for=condition=complete job/wordpress-install --timeout=240s
kubectl logs job/wordpress-install
# Success: WordPress installed successfully.

curl -s -o /dev/null -w "%{http_code}\n" https://<your-hostname>/   # 200
```

Log in at `https://<your-hostname>/wp-admin/` with `admin` and the password from the `wordpress-admin` Secret:

```bash
kubectl get secret wordpress-admin -o jsonpath='{.data.password}' | base64 -d
```

## Step 4 — Back up wp-content to your S3 bucket

The database is already backed up daily by the platform (encrypted with your KMS key — check `kubectl get kdcdatabase wordpress-db -o yaml`, conditions `BackupReady` and `BackupEncryptionConfigured`). Files are yours to archive — a Job with your bucket's access keys does it:

```yaml title="04-content-backup.yaml"
apiVersion: batch/v1
kind: Job
metadata:
  name: wp-content-backup
  namespace: my-project
spec:
  ttlSecondsAfterFinished: 1800
  backoffLimit: 2
  template:
    spec:
      restartPolicy: Never
      affinity:
        podAffinity:
          requiredDuringSchedulingIgnoredDuringExecution:
          - labelSelector:
              matchLabels:
                app: wordpress
            topologyKey: kubernetes.io/hostname
      initContainers:
      - name: archive
        image: busybox:1.36
        command: ["sh","-c","tar czf /out/wp-content.tgz -C /data wp-content"]
        volumeMounts:
        - {name: content, mountPath: /data, readOnly: true}
        - {name: out, mountPath: /out}
      containers:
      - name: upload
        image: amazon/aws-cli:2.17.16
        command: ["sh","-c"]
        args:
        - |
          set -e
          STAMP=$(date -u +%Y%m%d-%H%M%S)
          aws s3 cp /out/wp-content.tgz \
            "s3://$BUCKET_NAME/backups/wp-content-$STAMP.tgz" \
            --endpoint-url "https://s3.kube-dc.cloud"
          aws s3 ls "s3://$BUCKET_NAME/backups/" --endpoint-url "https://s3.kube-dc.cloud"
        envFrom:
        - configMapRef: {name: wordpress-files}   # BUCKET_NAME, BUCKET_HOST
        - secretRef: {name: wordpress-files}      # AWS_ACCESS_KEY_ID / SECRET
        volumeMounts:
        - {name: out, mountPath: /out}
      volumes:
      - {name: content, persistentVolumeClaim: {claimName: wordpress-content}}
      - {name: out, emptyDir: {}}
```

```bash
kubectl apply -f 04-content-backup.yaml
kubectl wait --for=condition=complete job/wp-content-backup --timeout=240s
kubectl logs job/wp-content-backup -c upload | tail -2
# upload: ../out/wp-content.tgz to s3://wordpress-files-.../backups/wp-content-....tgz
```

:::info Use the public S3 endpoint from workloads
Use your cluster's public S3 endpoint (`https://s3.kube-dc.cloud` on Kube-DC Cloud) from pods. The in-cluster RGW service address in the ConfigMap is not reachable from project networks. Note `amazon/aws-cli` has no `tar` — hence the busybox init container.
:::

## What you built

| Concern | Handled by |
|---|---|
| Database provisioning, HA, upgrades | `KdcDatabase` (platform-operated MariaDB) |
| Database credentials + rotation | `DatabaseCredentialPolicy` → vault-backed Secret |
| Database backups, encrypted | `spec.backup` + `KMSKey` — daily, 7-day retention |
| Content storage | Ceph-backed PVC shared by all replicas |
| File backups + access keys | `ObjectBucketClaim` bucket + Job |
| HTTPS, certificate, DNS name | One Service annotation |
| Scaling | HorizontalPodAutoscaler 2→4 |
| Admin operations | WP-CLI Jobs on the shared volume |

## Cleanup

```bash
kubectl delete hpa/wordpress svc/wordpress deploy/wordpress \
  job/wordpress-install job/wp-content-backup \
  pvc/wordpress-content secret/wordpress-admin
kubectl delete databasecredentialpolicy/wordpress-app kdcdatabase/wordpress-db
kubectl delete obc/wordpress-files kmskey/wp-backup-key
```

## Troubleshooting

| Symptom | Cause |
|---|---|
| No hostname/certificate appears | The Service must be `type: LoadBalancer` for `expose-route` to be processed |
| Second replica `Pending` | podAffinity needs capacity on the volume's node — free capacity or lower requests |
| `wordpress-db-auth` missing | The credential policy projects it after the database is `Ready` — check `kubectl get kdcdatabase` |
| S3 upload times out | Use the public S3 endpoint, not the in-cluster `BUCKET_HOST` |
| DB Service name collides | Name the `KdcDatabase` differently from your app Service |
