#!/bin/bash

[ "${KUBE_DC_DEBUG}" == "true" ] && env
[ "${KUBE_DC_DEBUG}" == "true" ] && set -x

# Variables
DEPLOYMENT_NAME="kube-ovn-controller" 
DAEMONSET_NAME="kube-ovn-cni"
NAMESPACE="kube-system"

check_arg_exists() {
  local resource_type=$1
  local resource_name=$2
  local namespace=$3
  local arg=$4

  kubectl get "$resource_type" "$resource_name" -n "$namespace" -o jsonpath='{.spec.template.spec.containers[0].args}' | grep -q -- "$arg"
}

# Get VLAN ID from script argument
if [ -z "$KUBE_DC_GATEWAY_SWITCH" || -z "$KUBE_DC_VLAN_ID" || -z "$KUBE_DC_EXT_NET_NODES_LIST" || -z "$KUBE_DC_MTU"  ]; then
  echo "Usage: $0 KUBE_DC_GATEWAY_SWITCH KUBE_DC_VLAN_ID KUBE_DC_EXT_NET_NODES_LIST KUBE_DC_MTU required"
  exit 1
fi

# Split the nodes into an array
IFS=',' read -r -a NODE_ARRAY <<< "$KUBE_DC_EXT_NET_NODES_LIST"

# Label to check and add
LABEL_KEY="ovn.kubernetes.io/external-gw"
LABEL_VALUE="true"
set -e
# Iterate over each node
for NODE in "${NODE_ARRAY[@]}"; do
  echo "Processing node: $NODE"

  # Check if the label exists and has the correct value
  if kubectl get node "$NODE" --show-labels | grep -q "$LABEL_KEY=$LABEL_VALUE"; then
    echo "Node $NODE already has the label $LABEL_KEY=$LABEL_VALUE"
  else
    # Add the label if it does not exist
    echo "Adding label $LABEL_KEY=$LABEL_VALUE to node $NODE"
    kubectl label node "$NODE" "$LABEL_KEY=$LABEL_VALUE" --overwrite

    # Check if the label was added successfully
    if [ $? -eq 0 ]; then
      echo "Successfully labeled node $NODE"
    else
      echo "Failed to label node $NODE"
    fi
  fi
done
set +e

UPD="false"

# Patch deployment
if check_arg_exists deployment "$DEPLOYMENT_NAME" "$NAMESPACE" "--external-gateway-vlanid=$KUBE_DC_VLAN_ID"; then
    echo "Argument --external-gateway-vlanid=$KUBE_DC_VLAN_ID already exists in deployment $DEPLOYMENT_NAME. Skipping patch."
else
    kubectl patch deployment "$DEPLOYMENT_NAME" -n "$NAMESPACE" --type=json -p="[
    {
        \"op\": \"add\",
        \"path\": \"/spec/template/spec/containers/0/args/-\",
        \"value\": \"--external-gateway-vlanid=$KUBE_DC_VLAN_ID\"
    }
    ]"
    UPD="true"
    if [ $? -ne 0 ]; then
        echo "Failed to patch deployment $DEPLOYMENT_NAME in namespace $NAMESPACE"
        exit 1
    else
        echo "Successfully patched deployment $DEPLOYMENT_NAME in namespace $NAMESPACE"
    fi

fi
if check_arg_exists deployment "$DEPLOYMENT_NAME" "$NAMESPACE" "--external-gateway-switch=$KUBE_DC_GATEWAY_SWITCH"; then
    echo "Argument "--external-gateway-switch=$KUBE_DC_GATEWAY_SWITCH" already exists in deployment $DEPLOYMENT_NAME. Skipping patch."
else
    kubectl patch deployment "$DEPLOYMENT_NAME" -n "$NAMESPACE" --type=json -p="[
    {
        \"op\": \"add\",
        \"path\": \"/spec/template/spec/containers/0/args/-\",
        \"value\": \"--external-gateway-switch=$KUBE_DC_GATEWAY_SWITCH\"
    }
    ]"
    UPD="true"
    if [ $? -ne 0 ]; then
        echo "Failed to patch deployment $DEPLOYMENT_NAME in namespace $NAMESPACE"
        exit 1
    else
        echo "Successfully patched deployment $DEPLOYMENT_NAME in namespace $NAMESPACE"
    fi

fi

if [ $UPD == "true" ]; then
    kubectl rollout status deployment/$DEPLOYMENT_NAME -n $NAMESPACE --timeout 100s
fi

UPD="false"

if check_arg_exists daemonset "$DAEMONSET_NAME" "$NAMESPACE" "--external-gateway-switch=$KUBE_DC_GATEWAY_SWITCH"; then
    echo "Argument --external-gateway-switch=$KUBE_DC_GATEWAY_SWITCH already exists in daemonset $DAEMONSET_NAME. Skipping patch."
else
    # Patch daemonset
    kubectl patch daemonset "$DAEMONSET_NAME" -n "$NAMESPACE" --type=json -p="[
    {
        \"op\": \"add\",
        \"path\": \"/spec/template/spec/containers/0/args/-\",
        \"value\": \"--external-gateway-switch=$KUBE_DC_GATEWAY_SWITCH\"
    }
    ]"
    UPD="true"
    if [ $? -ne 0 ]; then
        echo "Failed to patch daemonset $DAEMONSET_NAME in namespace $NAMESPACE"
        exit 1
    else
        echo "Successfully patched daemonset $DAEMONSET_NAME in namespace $NAMESPACE"
    fi
fi

if [ $UPD == "true" ]; then
    kubectl rollout status daemonset/$DAEMONSET_NAME -n $NAMESPACE --timeout 100s
fi

