#!/bin/bash

set -e

NAMESPACE="shalb-demo"
RELEASE_NAME="ingress-nginx"
VALUES_FILE="$(dirname "$0")/values.yaml"

helm repo add ingress-nginx https://kubernetes.github.io/ingress-nginx
helm repo update

helm upgrade --install ${RELEASE_NAME} ingress-nginx/ingress-nginx \
  --namespace ${NAMESPACE} \
  --values ${VALUES_FILE} \
  --wait
