#!/bin/bash
# Apply Keycloak theme ConfigMap
# Usage: ./apply-theme.sh <kubeconfig-path>

KUBECONFIG="${1:-~/.kube/config}"
SCRIPT_DIR="$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)"

# Create ConfigMap with binary data from JAR file
kubectl --kubeconfig="$KUBECONFIG" apply -f - <<EOF
apiVersion: v1
kind: ConfigMap
metadata:
  name: kube-dc-theme
  namespace: keycloak
  labels:
    app.kubernetes.io/name: keycloak
    app.kubernetes.io/component: theme
binaryData:
  kube-dc-theme.jar: $(base64 -w0 "$SCRIPT_DIR/kube-dc-theme.jar")
EOF
