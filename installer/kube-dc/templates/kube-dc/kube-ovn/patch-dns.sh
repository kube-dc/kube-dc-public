#!/bin/bash

[ "${KUBE_DC_DEBUG}" == "true" ] && env
[ "${KUBE_DC_DEBUG}" == "true" ] && set -x

# Name of the resource
RESOURCE_NAME="ovn-default"

# Desired provider value
DESIRED_PROVIDER="ovn-nad.default.ovn"

# Get the current provider value
CURRENT_PROVIDER=$(kubectl get subnet $RESOURCE_NAME -o jsonpath='{.spec.provider}')

# Check if the current provider matches the desired value
if [[ "$CURRENT_PROVIDER" != "$DESIRED_PROVIDER" ]]; then
  echo "Provider value is '$CURRENT_PROVIDER'. Updating to '$DESIRED_PROVIDER'..."
  kubectl patch subnet $RESOURCE_NAME --type=json -p='[{"op": "replace", "path": "/spec/provider", "value": "'$DESIRED_PROVIDER'"}]'
  
  # Check if the patch was successful
  if [[ $? -eq 0 ]]; then
    echo "Successfully updated the provider value."
  else
    echo "Failed to update the provider value."
    exit 1
  fi
else
  echo "Provider value is already set to '$DESIRED_PROVIDER'. No changes needed."
fi
