#!/bin/bash
set -e

VERSION="${1:-v0.3.42-$(date +%s)}"
REGISTRY="${REGISTRY_REPO:-shalb}"
NAMESPACE="${NAMESPACE:-kube-dc}"

cd "$(dirname "$0")/../ui/backend"

echo "Building backend ${VERSION}..."
docker build -t ${REGISTRY}/kube-dc-ui-backend:${VERSION} . >/dev/null 2>&1 || { echo "False"; exit 1; }

echo "Pushing backend ${VERSION}..."
docker push ${REGISTRY}/kube-dc-ui-backend:${VERSION} >/dev/null 2>&1 || { echo "False"; exit 1; }

echo "Deploying backend ${VERSION}..."
kubectl set image -n ${NAMESPACE} deployment/kube-dc-backend \
  kube-dc=${REGISTRY}/kube-dc-ui-backend:${VERSION} >/dev/null 2>&1 || { echo "False"; exit 1; }

echo "Waiting for rollout..."
kubectl rollout status deployment/kube-dc-backend -n ${NAMESPACE} --timeout=60s >/dev/null 2>&1 || { echo "False"; exit 1; }

echo "True"
