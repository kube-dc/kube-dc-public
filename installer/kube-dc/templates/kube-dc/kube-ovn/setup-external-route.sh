#!/bin/bash
#
# Setup external route for Kube-OVN
# This script configures the default route via external gateway
#

set -e

# Check if external gateway is configured
if [ -z "$KUBE_DC_EXTERNAL_GATEWAY" ]; then
    echo "No external gateway configured, skipping external route setup"
    exit 0
fi

echo "Setting up external route via gateway: $KUBE_DC_EXTERNAL_GATEWAY"

# Wait for OVN central to be ready
echo "Waiting for OVN central to be ready..."
for i in {1..60}; do
    if kubectl get pod -n kube-system -l app=ovn-central | grep -q Running; then
        echo "OVN central is ready"
        break
    fi
    sleep 5
done

# Get OVN central pod
OVN_POD=$(kubectl get pod -n kube-system -l app=ovn-central -o jsonpath='{.items[0].metadata.name}')

if [ -z "$OVN_POD" ]; then
    echo "ERROR: Could not find OVN central pod"
    exit 1
fi

echo "Using OVN central pod: $OVN_POD"

# Check for existing default routes
echo "Checking existing default routes..."
EXISTING_ROUTES=$(kubectl exec -n kube-system "$OVN_POD" -- ovn-nbctl lr-route-list ovn-cluster 2>/dev/null | grep "0.0.0.0/0" || true)

if [ -n "$EXISTING_ROUTES" ]; then
    echo "Found existing default routes:"
    echo "$EXISTING_ROUTES"
    
    # Remove old default route via join network if it exists
    if echo "$EXISTING_ROUTES" | grep -q "172.30.0.1"; then
        echo "Removing old default route via join network (172.30.0.1)..."
        kubectl exec -n kube-system "$OVN_POD" -- ovn-nbctl lr-route-del ovn-cluster 0.0.0.0/0 172.30.0.1 || true
    fi
fi

# Add new default route via external gateway
echo "Adding default route via external gateway: $KUBE_DC_EXTERNAL_GATEWAY"
kubectl exec -n kube-system "$OVN_POD" -- ovn-nbctl lr-route-add ovn-cluster 0.0.0.0/0 "$KUBE_DC_EXTERNAL_GATEWAY" || {
    echo "Route may already exist, checking..."
    if kubectl exec -n kube-system "$OVN_POD" -- ovn-nbctl lr-route-list ovn-cluster | grep -q "$KUBE_DC_EXTERNAL_GATEWAY"; then
        echo "Route already configured correctly"
    else
        echo "ERROR: Failed to add route"
        exit 1
    fi
}

# Verify the route
echo "Verifying route configuration..."
kubectl exec -n kube-system "$OVN_POD" -- ovn-nbctl lr-route-list ovn-cluster | grep "0.0.0.0/0"

echo "External route setup completed successfully"
