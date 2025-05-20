#!/bin/bash

set -e

# helm repo add ingress-nginx https://kubernetes.github.io/ingress-nginx
# helm repo update

helm upgrade --install ingress ingress-nginx/ingress-nginx --namespace "shalb-dev" --values values.yaml