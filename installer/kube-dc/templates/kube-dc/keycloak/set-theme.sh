#!/bin/bash

# Automatically set Kube-DC theme for all Keycloak realms
# Reads credentials from master-config secret in kube-dc namespace
# Usage: ./set-theme.sh [kubeconfig-path]

set -e

KUBECONFIG="${1:-~/.kube/config}"
export KUBECONFIG

NAMESPACE_KUBE_DC="kube-dc"
NAMESPACE_KEYCLOAK="keycloak"
THEME_NAME="kube-dc"

echo "=========================================="
echo "Kube-DC Keycloak Theme Activation"
echo "=========================================="
echo ""

# Extract credentials from master-config secret
echo "Extracting Keycloak credentials from master-config secret..."
KEYCLOAK_URL=$(kubectl get secret master-config -n ${NAMESPACE_KUBE_DC} -o jsonpath='{.data.url}' | base64 -d)
KEYCLOAK_USER=$(kubectl get secret master-config -n ${NAMESPACE_KUBE_DC} -o jsonpath='{.data.user}' | base64 -d)
KEYCLOAK_PASSWORD=$(kubectl get secret master-config -n ${NAMESPACE_KUBE_DC} -o jsonpath='{.data.password}' | base64 -d)
MASTER_REALM=$(kubectl get secret master-config -n ${NAMESPACE_KUBE_DC} -o jsonpath='{.data.master_realm}' | base64 -d)

echo "✓ Keycloak URL: ${KEYCLOAK_URL}"
echo "✓ Admin user: ${KEYCLOAK_USER}"
echo "✓ Master realm: ${MASTER_REALM}"
echo ""

# Get admin access token
echo "Authenticating with Keycloak Admin API..."
TOKEN_RESPONSE=$(curl -s -X POST "${KEYCLOAK_URL}/realms/${MASTER_REALM}/protocol/openid-connect/token" \
  -H "Content-Type: application/x-www-form-urlencoded" \
  -d "username=${KEYCLOAK_USER}" \
  -d "password=${KEYCLOAK_PASSWORD}" \
  -d "grant_type=password" \
  -d "client_id=admin-cli")

ACCESS_TOKEN=$(echo "${TOKEN_RESPONSE}" | jq -r '.access_token')

if [ "${ACCESS_TOKEN}" == "null" ] || [ -z "${ACCESS_TOKEN}" ]; then
    echo "❌ Failed to authenticate with Keycloak"
    echo "Response: ${TOKEN_RESPONSE}"
    exit 1
fi

echo "✓ Authentication successful"
echo ""

# Get list of all realms
echo "Fetching list of realms..."
REALMS=$(curl -s -X GET "${KEYCLOAK_URL}/admin/realms" \
  -H "Authorization: Bearer ${ACCESS_TOKEN}" \
  -H "Content-Type: application/json")

REALM_NAMES=$(echo "${REALMS}" | jq -r '.[].realm')

echo "Found realms:"
echo "${REALM_NAMES}" | while read realm; do
    echo "  - ${realm}"
done
echo ""

# Set theme for each realm
echo "Setting '${THEME_NAME}' theme for all realms..."
echo ""

SUCCESS_COUNT=0
FAIL_COUNT=0

while read -r REALM; do
    echo "Configuring realm: ${REALM}"
    
    # Get current realm configuration
    REALM_CONFIG=$(curl -s -X GET "${KEYCLOAK_URL}/admin/realms/${REALM}" \
      -H "Authorization: Bearer ${ACCESS_TOKEN}" \
      -H "Content-Type: application/json")
    
    # Update theme settings
    UPDATED_CONFIG=$(echo "${REALM_CONFIG}" | jq \
      --arg theme "${THEME_NAME}" \
      '.loginTheme = $theme | .accountTheme = $theme | .emailTheme = $theme')
    
    # Apply configuration
    RESPONSE=$(curl -s -w "\n%{http_code}" -X PUT "${KEYCLOAK_URL}/admin/realms/${REALM}" \
      -H "Authorization: Bearer ${ACCESS_TOKEN}" \
      -H "Content-Type: application/json" \
      -d "${UPDATED_CONFIG}")
    
    HTTP_CODE=$(echo "${RESPONSE}" | tail -n1)
    
    if [ "${HTTP_CODE}" == "204" ] || [ "${HTTP_CODE}" == "200" ]; then
        echo "  ✓ Theme set successfully"
        SUCCESS_COUNT=$((SUCCESS_COUNT + 1))
    else
        echo "  ❌ Failed to set theme (HTTP ${HTTP_CODE})"
        FAIL_COUNT=$((FAIL_COUNT + 1))
    fi
    echo ""
done <<< "${REALM_NAMES}"

# Set default theme for new realms in master realm
echo "Configuring default theme for new realms..."
MASTER_CONFIG=$(curl -s -X GET "${KEYCLOAK_URL}/admin/realms/${MASTER_REALM}" \
  -H "Authorization: Bearer ${ACCESS_TOKEN}" \
  -H "Content-Type: application/json")

UPDATED_MASTER=$(echo "${MASTER_CONFIG}" | jq \
  --arg theme "${THEME_NAME}" \
  '.defaultDefaultClientScopes = (.defaultDefaultClientScopes // []) |
   .loginTheme = $theme |
   .accountTheme = $theme |
   .adminTheme = $theme |
   .emailTheme = $theme')

RESPONSE=$(curl -s -w "\n%{http_code}" -X PUT "${KEYCLOAK_URL}/admin/realms/${MASTER_REALM}" \
  -H "Authorization: Bearer ${ACCESS_TOKEN}" \
  -H "Content-Type: application/json" \
  -d "${UPDATED_MASTER}")

HTTP_CODE=$(echo "${RESPONSE}" | tail -n1)

if [ "${HTTP_CODE}" == "204" ] || [ "${HTTP_CODE}" == "200" ]; then
    echo "✓ Master realm theme configuration updated"
else
    echo "⚠ Warning: Failed to update master realm theme (HTTP ${HTTP_CODE})"
fi

echo ""
echo "=========================================="
echo "Summary"
echo "=========================================="
echo "Successfully configured: ${SUCCESS_COUNT} realms"
if [ ${FAIL_COUNT} -gt 0 ]; then
    echo "Failed to configure: ${FAIL_COUNT} realms"
fi
echo ""
echo "Theme '${THEME_NAME}' is now active for:"
echo "  - All existing realms"
echo "  - Future realms (inherited from master)"
echo ""
echo "✅ Done!"
