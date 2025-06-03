#!/bin/bash

mkdir -p /etc/rancher/rke2/

# Validate required token argument
[ -z "$1" ] && { echo "Error: Missing server token argument."; exit 1; }

# Assign arguments and defaults
export SERVER_TOKEN=$1
export CP_HOST=${2:-192.168.1.3}
export CP_PORT=${3:-9345}

# Generate the config from template
cat "$(dirname "$0")/config_tmpl" | envsubst > /etc/rancher/rke2/config.yaml

# Set agent install type and install RKE2
export INSTALL_RKE2_TYPE="agent"
curl -sfL https://get.rke2.io | sh -

# Enable and start the RKE2 agent service
systemctl enable rke2-agent.service
systemctl start rke2-agent.service
