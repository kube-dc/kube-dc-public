#!/bin/bash
set -e

SCRIPT_DIR="$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)"
MANIFESTS_DIR="$SCRIPT_DIR"

echo "🚀 Applying Kube-DC E2E Test Manifests..."
echo "📁 Manifests directory: $MANIFESTS_DIR"
echo

# Apply manifests in order
MANIFESTS=(
    "01-namespace.yaml"
    "02-organization.yaml" 
    "03-project.yaml"
    "04-eip.yaml"
    "05-fip.yaml"
    "06-nginx-deployment.yaml"
    "07-service-lb.yaml"
    "08-vm-examples.yaml"
)

for manifest in "${MANIFESTS[@]}"; do
    echo "📋 Applying $manifest..."
    kubectl apply -f "$MANIFESTS_DIR/$manifest"
    echo "✅ Applied $manifest"
    echo
done

echo "🎉 All manifests applied successfully!"
echo
echo "🔍 Verification commands:"
echo "  kubectl get organizations,projects -n test-org-e2e-manual"
echo "  kubectl get eip,fip,deploy,pod,svc,vm,vmi,dv -n test-org-e2e-manual-test-project-e2e-manual"
echo
echo "📖 See README.md for detailed verification and troubleshooting."
