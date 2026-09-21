# Connection patterns for managed services

Every pattern reads the `ServiceBinding` Secret; nothing is embedded in a
manifest. Every endpoint is TLS-only with the platform's own CA, so the CA is
always mounted and verified.

## Mount the Secret

```yaml
env:
  - name: DB_HOST
    valueFrom: { secretKeyRef: { name: "{binding-secret}", key: host } }
  - name: DB_PORT
    valueFrom: { secretKeyRef: { name: "{binding-secret}", key: port } }
  - name: DB_USER
    valueFrom: { secretKeyRef: { name: "{binding-secret}", key: username } }
  - name: DB_PASSWORD
    valueFrom: { secretKeyRef: { name: "{binding-secret}", key: password } }
volumeMounts:
  - name: db-ca
    mountPath: /etc/db
    readOnly: true
volumes:
  - name: db-ca
    secret:
      secretName: "{binding-secret}"
      items: [{ key: ca.crt, path: ca.crt }]
```

Environment variables are fixed at Pod start; after a credential rotation the
Pod needs a restart (`kubectl rollout restart`). Mount `password` as a file
and reread it when the application should follow rotation without restarts.

## PostgreSQL

Key for the database name: `dbname`.

```yaml
  - name: PGSSLMODE
    value: verify-full
  - name: PGSSLROOTCERT
    value: /etc/db/ca.crt
```

Connection URI: the `uri` key already carries `sslmode=verify-full`;
libpq still needs `PGSSLROOTCERT` or `sslrootcert=` pointing at the CA file.

```sh
psql "$(cat /etc/db/uri)"                      # if uri is also mounted as a file
psql "postgresql://$DB_USER:$DB_PASSWORD@$DB_HOST:$DB_PORT/$DB_NAME?sslmode=verify-full&sslrootcert=/etc/db/ca.crt"
```

Endpoints: `read-write` (default), `read-only` (replicas on production plans),
`pooled` (PgBouncer, when `parameters.pooler.enabled`).

## MySQL and MariaDB

Key for the database name: `database`. `jdbcUrl` is ready for JVM clients.

```sh
mariadb --host "$DB_HOST" --port "$DB_PORT" --user "$DB_USER" --password="$DB_PASSWORD" \
  --ssl-ca /etc/db/ca.crt --ssl-verify-server-cert "$DB_NAME"
mysql --host "$DB_HOST" --port "$DB_PORT" --user "$DB_USER" --password="$DB_PASSWORD" \
  --ssl-ca=/etc/db/ca.crt --ssl-mode=VERIFY_IDENTITY "$DB_NAME"
```

MySQL's read-only Router endpoint listens on `6447`; the binding Secret's
`port` is always the right one for the endpoint it was made for.

## ClickHouse

Native protocol on `port` (9440) or HTTPS on `httpsPort` (8443).

```sh
clickhouse-client --host "$DB_HOST" --port 9440 --secure \
  --user "$DB_USER" --password "$DB_PASSWORD" --database "$DB_NAME"
curl --cacert /etc/db/ca.crt -u "$DB_USER:$DB_PASSWORD" \
  "https://$DB_HOST:8443/?database=$DB_NAME" --data-binary 'SELECT 1'
```

`clickhouse-client` trusts the CA through its config
(`<openSSL><client><caConfig>/etc/db/ca.crt</caConfig></client></openSSL>`).

## Valkey

```sh
valkey-cli --tls --cacert /etc/db/ca.crt -h "$DB_HOST" -p "$DB_PORT" \
  --user default --pass "$DB_PASSWORD" --no-auth-warning PING
```

Client libraries need TLS enabled explicitly and the CA passed in; a plain
connection fails to handshake.

## Helm charts that want their own Secret key

Prefer the chart's `existingSecret` plus `existingSecretPasswordKey` settings.
When the chart insists on a fixed key name, bridge it:

```sh
kubectl create secret generic {chart-secret} -n {backing-namespace} \
  --from-literal={chart-key}="$(kubectl get secret {binding-secret} -n {backing-namespace} -o jsonpath='{.data.password}' | base64 -d)"
```

Recreate the bridge after every rotation, or let a small controller or Job do
it. Never commit the bridge Secret.
