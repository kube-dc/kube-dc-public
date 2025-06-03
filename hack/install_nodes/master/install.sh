#!/bin/bash

set -e

rm -rf /etc/rancher/
cp -r "$(dirname "$0")/rancher/" /etc/rancher/
chmod 666 /etc/rancher/auth-conf.yaml
export INSTALL_RKE2_TYPE="server"
curl -sfL https://get.rke2.io | sh -
systemctl enable rke2-server.service
systemctl start rke2-server.service


cat /var/lib/rancher/rke2/server/node-token

mkdir -p ~/.kube
cp /etc/rancher/rke2/rke2.yaml ~/.kube/config
chmod 600 ~/.kube/config

sed -i 's|https://127.0.0.1:6443|https://192.168.1.3:6443|g' ~/.kube/config
