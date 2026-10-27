#!/bin/bash
set -euo pipefail

# Deploy WordPress (Bitnami chart) with a managed MariaDB and an HTTPS
# Gateway route (ClusterIP Service: hostname + certificate, no external IP).
# A step-by-step version without Helm: docs/cloud/deploy-wordpress-stack.md.
#
# Usage: NAMESPACE=shalb-docs ./deploy.sh
# (NAMESPACE is the project namespace, normally {org}-{project}.)
# Needs the project admin role: Helm stores releases in Secrets, and step 3
# creates one.

NAMESPACE="${NAMESPACE:?NAMESPACE must be set, e.g. NAMESPACE=shalb-docs}"
RELEASE="${RELEASE:-wp}"

# 1. Managed MariaDB and its credential binding.
sed "s/PROJECT_NAMESPACE/${NAMESPACE}/g" managed-database.yaml \
  | kubectl apply -f -

# 2. Wait until the service and the credential Secret are ready (minutes).
kubectl wait --for=jsonpath='{.status.phase}'=Ready \
  managedservice/wp-db -n "${NAMESPACE}" --timeout=15m
kubectl wait --for=condition=Ready servicebinding/wp-db -n "${NAMESPACE}" --timeout=5m

# 3. Bridge Secret. The Bitnami chart reads the password under key
#    "mariadb-password"; the binding delivers it under "password".
secret() { kubectl get secret wp-db -n "${NAMESPACE}" -o jsonpath="{.data.$1}" | base64 -d; }
kubectl create secret generic wp-db-bridge \
  --namespace "${NAMESPACE}" \
  --from-literal=mariadb-password="$(secret password)" \
  --dry-run=client -o yaml | kubectl apply -f -

# 4. Helm install (or upgrade), with the delivered host, user and database.
helm upgrade --install "${RELEASE}" \
  oci://registry-1.docker.io/bitnamicharts/wordpress \
  --namespace "${NAMESPACE}" \
  --version 30.0.13 \
  -f values.yaml \
  --set externalDatabase.host="$(secret host)" \
  --set externalDatabase.port="$(secret port)" \
  --set externalDatabase.user="$(secret username)" \
  --set externalDatabase.database="$(secret database)" \
  --wait --timeout 10m

# 5. Print the URL (set once the certificate is issued).
HOST=$(kubectl get svc "${RELEASE}-wordpress" -n "${NAMESPACE}" \
  -o jsonpath='{.metadata.annotations.service\.nlb\.kube-dc\.com/route-hostname-status}')
echo
echo "WordPress deployed: https://${HOST:-<pending: kubectl get certificate -n ${NAMESPACE}>}"
echo "Admin password:"
kubectl get secret "${RELEASE}-wordpress" -n "${NAMESPACE}" \
  -o jsonpath='{.data.wordpress-password}' | base64 -d
echo
