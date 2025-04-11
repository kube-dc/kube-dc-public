#!/bin/bash

# https://github.com/piraeusdatastore/piraeus-operator/blob/v2/docs/how-to/install-kernel-headers.md

sudo apt-get update
sudo apt-get install -y linux-headers-$(uname -r)
sudo apt-get install -y linux-headers-virtual

kubectl apply --server-side -k "https://github.com/piraeusdatastore/piraeus-operator//config/default?ref=v2.8.0"
kubectl apply -f linstor-cluster.yaml
