#!/bin/bash

# Build Kube-DC Keycloak Theme
# This script packages the theme into a JAR file and generates Kubernetes manifests

set -e

SCRIPT_DIR="$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)"
THEME_DIR="${SCRIPT_DIR}/kube-dc"
OUTPUT_DIR="${SCRIPT_DIR}/build"
THEME_JAR="${OUTPUT_DIR}/kube-dc-theme.jar"

echo "Building Kube-DC Keycloak Theme..."

# Create output directory
mkdir -p "${OUTPUT_DIR}"

# Ensure images are copied from common/ to login/ and account/
echo "Copying images from common/resources/ to theme directories..."
mkdir -p "${THEME_DIR}/login/resources/img"
mkdir -p "${THEME_DIR}/account/resources"
cp "${THEME_DIR}/common/resources/favicon.png" "${THEME_DIR}/login/resources/img/" 2>/dev/null || true
cp "${THEME_DIR}/common/resources/logo.png" "${THEME_DIR}/login/resources/img/" 2>/dev/null || true
cp "${THEME_DIR}/common/resources/kube-dc-white-transparent.png" "${THEME_DIR}/login/resources/img/" 2>/dev/null || true
cp "${THEME_DIR}/common/resources/logo.png" "${THEME_DIR}/account/resources/" 2>/dev/null || true
cp "${THEME_DIR}/common/resources/favicon.png" "${THEME_DIR}/account/resources/" 2>/dev/null || true

# Package theme into JAR
echo "Packaging theme into JAR..."
cd "${THEME_DIR}"
jar cf "${THEME_JAR}" *

echo "✓ Theme JAR created: ${THEME_JAR}"

# Generate base64 encoded JAR for ConfigMap
echo ""
echo "Generating base64 encoded JAR for ConfigMap..."
BASE64_JAR=$(base64 -w 0 "${THEME_JAR}" 2>/dev/null || base64 "${THEME_JAR}")

# Create ConfigMap YAML
cat > "${OUTPUT_DIR}/keycloak-theme-configmap.yaml" <<EOF
apiVersion: v1
kind: ConfigMap
metadata:
  name: kube-dc-theme
  namespace: keycloak
  labels:
    app: keycloak
    component: theme
binaryData:
  kube-dc-theme.jar: |
    ${BASE64_JAR}
EOF

echo "✓ ConfigMap created: ${OUTPUT_DIR}/keycloak-theme-configmap.yaml"

# Create Helm values patch
cat > "${OUTPUT_DIR}/helm-values-patch.yaml" <<EOF
# Helm values patch to add Kube-DC theme to Keycloak
# Add these values to installer/kube-dc/templates/kube-dc/keycloak/values.yaml

extraVolumes:
  - name: kube-dc-theme
    configMap:
      name: kube-dc-theme

extraVolumeMounts:
  - name: kube-dc-theme
    mountPath: /opt/bitnami/keycloak/providers/kube-dc-theme.jar
    subPath: kube-dc-theme.jar
EOF

echo "✓ Helm values patch created: ${OUTPUT_DIR}/helm-values-patch.yaml"

# Create deployment script
cat > "${OUTPUT_DIR}/deploy-theme.sh" <<'DEPLOY_SCRIPT'
#!/bin/bash

# Deploy Kube-DC Keycloak Theme
set -e

KUBECONFIG="${KUBECONFIG:-~/.kube/config}"
NAMESPACE="keycloak"

echo "Deploying Kube-DC Keycloak theme to cluster..."

# Check if namespace exists
if ! kubectl --kubeconfig="${KUBECONFIG}" get namespace "${NAMESPACE}" &>/dev/null; then
    echo "Error: Namespace '${NAMESPACE}' does not exist"
    exit 1
fi

# Apply ConfigMap
echo "Applying theme ConfigMap..."
kubectl --kubeconfig="${KUBECONFIG}" apply -f keycloak-theme-configmap.yaml

# Check if Keycloak is managed by Helm
if helm list -n "${NAMESPACE}" | grep -q keycloak; then
    echo ""
    echo "⚠️  Keycloak is managed by Helm."
    echo "To apply the theme, add the following to your Helm values:"
    echo ""
    cat helm-values-patch.yaml
    echo ""
    echo "Then run: helm upgrade keycloak ... -f keycloak/values.yaml"
else
    echo ""
    echo "⚠️  Manual patching required."
    echo "Add volume and volumeMount to Keycloak StatefulSet/Deployment"
fi

echo ""
echo "✓ Theme ConfigMap deployed successfully"
echo ""
echo "Next steps:"
echo "1. Update Keycloak Helm values (see helm-values-patch.yaml)"
echo "2. Restart Keycloak pods: kubectl rollout restart statefulset/keycloak -n keycloak"
echo "3. In Keycloak Admin Console, go to Realm Settings → Themes"
echo "4. Set 'Login Theme' to 'kube-dc'"
echo "5. Save and test the login page"
DEPLOY_SCRIPT

chmod +x "${OUTPUT_DIR}/deploy-theme.sh"

echo "✓ Deployment script created: ${OUTPUT_DIR}/deploy-theme.sh"

echo ""
echo "================================================"
echo "Build Complete!"
echo "================================================"
echo ""
echo "Generated files:"
echo "  - ${THEME_JAR}"
echo "  - ${OUTPUT_DIR}/keycloak-theme-configmap.yaml"
echo "  - ${OUTPUT_DIR}/helm-values-patch.yaml"
echo "  - ${OUTPUT_DIR}/deploy-theme.sh"
echo ""
echo "To deploy:"
echo "  cd ${OUTPUT_DIR}"
echo "  ./deploy-theme.sh"
echo ""
