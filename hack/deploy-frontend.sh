#!/bin/bash
set -e

VERSION="${1:-v0.3.42-$(date +%s)}"
REGISTRY="${REGISTRY_REPO:-shalb}"
NAMESPACE="${NAMESPACE:-kube-dc}"

cd "$(dirname "$0")/../ui/frontend"

echo "Building frontend ${VERSION}..."
docker build -t ${REGISTRY}/kube-dc-ui-frontend:${VERSION} . >/dev/null 2>&1 || { echo "False"; exit 1; }

echo "Pushing frontend ${VERSION}..."
docker push ${REGISTRY}/kube-dc-ui-frontend:${VERSION} >/dev/null 2>&1 || { echo "False"; exit 1; }

echo "Deploying frontend ${VERSION}..."
kubectl set image -n ${NAMESPACE} deployment/kube-dc-frontend \
  frontend=${REGISTRY}/kube-dc-ui-frontend:${VERSION} >/dev/null 2>&1 || { echo "False"; exit 1; }

echo "Waiting for rollout..."
kubectl rollout status deployment/kube-dc-frontend -n ${NAMESPACE} --timeout=60s >/dev/null 2>&1 || { echo "False"; exit 1; }

echo "True"
