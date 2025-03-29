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
clusterctl init --infrastructure kubevirt --bootstrap k3s --control-plane k3s
echo Done