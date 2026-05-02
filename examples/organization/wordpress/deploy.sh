#!/bin/bash
set -euo pipefail

# Deploy WordPress with a managed MariaDB KdcDatabase + Gateway Route auto-TLS.
#
# Usage: NAMESPACE=shalb-docs ./deploy.sh
# (NAMESPACE is the project namespace, normally {org}-{project}.)

NAMESPACE="${NAMESPACE:?NAMESPACE must be set, e.g. NAMESPACE=shalb-docs}"
RELEASE="${RELEASE:-wp}"
DB_NAME="${DB_NAME:-wp-mariadb}"

# 1. MariaDB. Substitute PROJECT_NAMESPACE and apply.
sed "s/PROJECT_NAMESPACE/${NAMESPACE}/g" kdcdatabase.yaml \
  | kubectl apply -f -

# 2. Wait until Ready (typically ~60s).
kubectl wait --for=jsonpath='{.status.phase}'=Ready \
  "kdcdb/${DB_NAME}" -n "${NAMESPACE}" --timeout=5m

# 3. Bridge Secret. The Bitnami WP chart reads key "mariadb-password";
#    KdcDatabase's auto-secret carries the password under key "password".
PASSWORD=$(kubectl get secret "${DB_NAME}-password" -n "${NAMESPACE}" \
  -o jsonpath='{.data.password}' | base64 -d)
kubectl create secret generic wp-db-bridge \
  --namespace "${NAMESPACE}" \
  --from-literal=mariadb-password="${PASSWORD}" \
  --dry-run=client -o yaml | kubectl apply -f -

# 4. Helm install (or upgrade).
sed "s/PROJECT_NAMESPACE/${NAMESPACE}/g" values.yaml > /tmp/wp-values.rendered.yaml
helm upgrade --install "${RELEASE}" \
  oci://registry-1.docker.io/bitnamicharts/wordpress \
  --namespace "${NAMESPACE}" \
  --version 30.0.13 \
  -f /tmp/wp-values.rendered.yaml \
  --wait --timeout 5m

# 5. Print the auto-assigned URL.
HOST=$(kubectl get svc "${RELEASE}-wordpress" -n "${NAMESPACE}" \
  -o jsonpath='{.metadata.annotations.service\.nlb\.kube-dc\.com/route-hostname-status}')
echo
echo "WordPress deployed: https://${HOST}"
echo "Admin password:"
kubectl get secret "${RELEASE}-wordpress" -n "${NAMESPACE}" \
  -o jsonpath='{.data.wordpress-password}' | base64 -d
echo
