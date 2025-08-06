#!/bin/bash
set -e

SCRIPT_DIR="$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)"
MANIFESTS_DIR="$SCRIPT_DIR"

echo "🗑️  Deleting Kube-DC E2E Test Manifests..."
echo "📁 Manifests directory: $MANIFESTS_DIR"
echo

# Delete manifests in reverse order for proper cleanup
MANIFESTS=(
    "08-vm-examples.yaml"
    "07-service-lb.yaml"
    "06-nginx-deployment.yaml"
    "05-fip.yaml"
    "04-eip.yaml"
    "03-project.yaml"
    "02-organization.yaml"
    "01-namespace.yaml"
)

for manifest in "${MANIFESTS[@]}"; do
    echo "🗑️  Deleting $manifest..."
    if kubectl delete -f "$MANIFESTS_DIR/$manifest" --ignore-not-found=true; then
        echo "✅ Deleted $manifest"
    else
        echo "⚠️  Failed to delete $manifest (may not exist)"
    fi
    echo
done

echo "🧹 Cleaning up any stuck resources..."

# Force cleanup stuck namespaces if they exist
NAMESPACES=(
    "test-org-e2e-manual-test-project-e2e-manual"
    "test-org-e2e-manual"
)

for ns in "${NAMESPACES[@]}"; do
    if kubectl get ns "$ns" >/dev/null 2>&1; then
        echo "🧹 Force cleaning namespace: $ns"
        kubectl patch ns "$ns" -p '{"metadata":{"finalizers":null}}' --type=merge || true
        kubectl delete ns "$ns" --ignore-not-found=true || true
    fi
done

echo "✅ Cleanup completed!"
echo
echo "🔍 Verification (should show no resources):"
echo "  kubectl get organizations,projects,eip,fip -A | grep e2e-manual"
