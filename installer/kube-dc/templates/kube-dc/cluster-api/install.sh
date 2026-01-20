#!/bin/bash

[ "${KUBE_DC_DEBUG}" == "true" ] && env
[ "${KUBE_DC_DEBUG}" == "true" ] && set -x

set -e

echo "Install clusterctl tool..."
curl -L "https://github.com/kubernetes-sigs/cluster-api/releases/download/${CLUSTER_API_VERSION}/clusterctl-linux-amd64" -o clusterctl
sudo install -o root -g root -m 0755 clusterctl /usr/local/bin/clusterctl
sudo mkdir -p "${HOME}/.cluster-api/"

# create clusterci configuration dir:
sudo chown -R "$(whoami):$(whoami)" "${HOME}/.cluster-api/"
cp ./clusterctl.yaml "${HOME}/.cluster-api/"

export EXP_CLUSTER_RESOURCE_SET=true
clusterctl init --infrastructure kubevirt --bootstrap k3s --control-plane k3s --control-plane kamaji

echo "Installing CAPK webhook certificate with auto-renewal..."
# Wait for capk-system namespace to be created
timeout=60
while [ $timeout -gt 0 ]; do
  if kubectl get namespace capk-system >/dev/null 2>&1; then
    echo "capk-system namespace is ready"
    break
  fi
  echo "Waiting for capk-system namespace..."
  sleep 2
  timeout=$((timeout-2))
done

# Apply certificate issuer and certificate for auto-renewal
kubectl apply -f ./capk-webhook-cert.yaml

# Wait for certificate to be issued
echo "Waiting for CAPK webhook certificate to be issued..."
kubectl wait --for=condition=Ready certificate/capk-serving-cert -n capk-system --timeout=60s || echo "Certificate not ready yet, will be issued by cert-manager"

echo Done