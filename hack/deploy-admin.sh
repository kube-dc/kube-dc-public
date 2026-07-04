#!/bin/bash
# DEPRECATED: admin-frontend is now a first-class component of the kube-dc Helm chart
# (charts/kube-dc/templates/admin-frontend-*.yaml). For dev iteration prefer:
#     cicd/dagger-engine/dev-build admin     # build local working tree → -devN → deploy
# This local-docker script is a fallback only. It no longer defaults to the dead
# v0.5.x line — without an explicit version it derives the next -devN from the live
# deployment (same scheme as dev-build).
set -e

REGISTRY="${REGISTRY_REPO:-shalb}"
NAMESPACE="${NAMESPACE:-kube-dc}"
if [ -n "$1" ]; then
  VERSION="$1"
else
  cur=$(kubectl get deploy kube-dc-admin-frontend -n "$NAMESPACE" \
    -o jsonpath='{.spec.template.spec.containers[0].image}' 2>/dev/null | awk -F: '{print $2}')
  base=$(printf '%s' "$cur" | grep -oE 'v[0-9]+\.[0-9]+\.[0-9]+' | head -1); base=${base:-v0.3.81}
  if printf '%s' "$cur" | grep -qE -- '-dev[0-9]+$'; then n=$(( ${cur##*-dev} + 1 )); else n=1; fi
  VERSION="${base}-dev${n}"
fi

cd "$(dirname "$0")/../ui/admin"

echo "Building admin ${VERSION}..."
docker build -t ${REGISTRY}/kube-dc-admin-frontend:${VERSION} . >/dev/null 2>&1 || { echo "False"; exit 1; }

echo "Pushing admin ${VERSION}..."
docker push ${REGISTRY}/kube-dc-admin-frontend:${VERSION} >/dev/null 2>&1 || { echo "False"; exit 1; }

echo "Deploying admin ${VERSION}..."
kubectl set image -n ${NAMESPACE} deployment/kube-dc-admin-frontend \
  frontend=${REGISTRY}/kube-dc-admin-frontend:${VERSION} >/dev/null 2>&1 || { echo "False"; exit 1; }

echo "Waiting for rollout..."
kubectl rollout status deployment/kube-dc-admin-frontend -n ${NAMESPACE} --timeout=60s >/dev/null 2>&1 || { echo "False"; exit 1; }

echo "True"
