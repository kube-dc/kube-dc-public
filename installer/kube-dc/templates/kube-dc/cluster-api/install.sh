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
clusterctl init --infrastructure kubevirt --bootstrap k3s --bootstrap kubeadm --control-plane k3s --control-plane kamaji

echo "Installing webhook certificates with auto-renewal for all CAPI providers..."

# Wait for all CAPI provider namespaces to be created
for ns in capk-system capi-system capi-kubeadm-bootstrap-system capi-k3s-bootstrap-system capi-k3s-control-plane-system; do
  timeout=60
  while [ $timeout -gt 0 ]; do
    if kubectl get namespace "$ns" >/dev/null 2>&1; then
      echo "$ns namespace is ready"
      break
    fi
    echo "Waiting for $ns namespace..."
    sleep 2
    timeout=$((timeout-2))
  done
done

# Apply certificate issuers and certificates for auto-renewal
kubectl apply -f ./capk-webhook-cert.yaml
kubectl apply -f ./capi-webhook-certs.yaml

# Wait for certificates to be issued
echo "Waiting for webhook certificates to be issued..."
kubectl wait --for=condition=Ready certificate/capk-serving-cert -n capk-system --timeout=60s || echo "CAPK certificate not ready yet"
kubectl wait --for=condition=Ready certificate/capi-serving-cert -n capi-system --timeout=60s || echo "CAPI core certificate not ready yet"
kubectl wait --for=condition=Ready certificate/capi-kubeadm-bootstrap-serving-cert -n capi-kubeadm-bootstrap-system --timeout=60s || echo "Kubeadm bootstrap certificate not ready yet"
kubectl wait --for=condition=Ready certificate/capi-k3s-bootstrap-serving-cert -n capi-k3s-bootstrap-system --timeout=60s || echo "K3s bootstrap certificate not ready yet"
kubectl wait --for=condition=Ready certificate/capi-k3s-control-plane-serving-cert -n capi-k3s-control-plane-system --timeout=60s || echo "K3s control plane certificate not ready yet"

echo Done