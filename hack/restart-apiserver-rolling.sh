#!/bin/bash
set -e

# Rolling restart of RKE2 API servers to reload auth config
# Usage: ./restart-apiserver-rolling.sh

# Control plane nodes with external IPs
# Update these values with your actual control plane nodes and IPs
declare -A CONTROL_PLANE_NODES=(
  ["cp-node-1"]="<CP_NODE_1_IP>"
  ["cp-node-2"]="<CP_NODE_2_IP>"
  ["cp-node-3"]="<CP_NODE_3_IP>"
)

SSH_USER="root"
WAIT_TIME=60  # seconds to wait between restarts

echo "Starting rolling restart of RKE2 servers..."
echo "This will restart API servers one at a time to reload auth config."
echo ""

for node in "${!CONTROL_PLANE_NODES[@]}"; do
  ip="${CONTROL_PLANE_NODES[$node]}"
  echo "=== Restarting rke2-server on ${node} (${ip}) ==="
  
  ssh ${SSH_USER}@${ip} "systemctl restart rke2-server"
  
  echo "Waiting ${WAIT_TIME}s for ${node} to become ready..."
  sleep ${WAIT_TIME}
  
  # Check if node is ready
  echo "Checking node status..."
  if kubectl get node ${node} --no-headers 2>/dev/null | grep -q "Ready"; then
    echo "✓ ${node} is Ready"
  else
    echo "⚠ Warning: ${node} may not be ready yet, continuing anyway..."
  fi
  
  echo ""
done

echo "=== Rolling restart complete ==="
echo "Checking API server OIDC status..."
# Check OIDC status on first control plane node
FIRST_NODE=$(echo "${!CONTROL_PLANE_NODES[@]}" | awk '{print $1}')
kubectl logs -n kube-system kube-apiserver-${FIRST_NODE} --tail=5 2>&1 | grep -i oidc || echo "No recent OIDC errors (good!)"
