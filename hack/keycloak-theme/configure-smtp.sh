#!/bin/bash

# Configure SMTP settings for all Keycloak realms
# Reads credentials from master-config secret in kube-dc namespace
# Usage: ./configure-smtp.sh [kubeconfig-path]
#
# Environment variables (optional - reads from stack config if not set):
# - SMTP_HOST, SMTP_PORT, SMTP_FROM, SMTP_FROM_NAME
# - SMTP_USER, SMTP_PASSWORD (if authentication required)

set -e

KUBECONFIG="${1:-~/.kube/config}"
export KUBECONFIG

NAMESPACE_KUBE_DC="kube-dc"
NAMESPACE_KEYCLOAK="keycloak"

echo "=========================================="
echo "Kube-DC Keycloak SMTP Configuration"
echo "=========================================="
echo ""

# Check if SMTP is configured
if [ -z "$SMTP_HOST" ]; then
    echo "SMTP_HOST not set, skipping SMTP configuration"
    exit 0
fi

echo "SMTP Configuration:"
echo "  Host: ${SMTP_HOST}"
echo "  Port: ${SMTP_PORT:-587}"
echo "  From: ${SMTP_FROM:-noreply@kube-dc.com}"
echo "  Auth: $([ -n "$SMTP_USER" ] && echo "enabled" || echo "disabled")"
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

# Build SMTP configuration JSON
build_smtp_config() {
    local smtp_json='{'
    smtp_json+='"host":"'"$SMTP_HOST"'"'
    smtp_json+=',"port":"'"${SMTP_PORT:-587}"'"'
    smtp_json+=',"from":"'"${SMTP_FROM:-noreply@kube-dc.com}"'"'
    smtp_json+=',"fromDisplayName":"'"${SMTP_FROM_NAME:-Kube-DC}"'"'
    smtp_json+=',"starttls":"true"'
    smtp_json+=',"ssl":"false"'
    if [ -n "$SMTP_USER" ]; then
        smtp_json+=',"auth":"true"'
        smtp_json+=',"user":"'"$SMTP_USER"'"'
        smtp_json+=',"password":"'"$SMTP_PASSWORD"'"'
    else
        smtp_json+=',"auth":"false"'
    fi
    smtp_json+='}'
    echo "$smtp_json"
}

SMTP_CONFIG=$(build_smtp_config)

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

# Configure SMTP for each realm
echo "Configuring SMTP for all realms..."
echo ""

SUCCESS_COUNT=0
FAIL_COUNT=0

while read -r REALM; do
    [ -z "$REALM" ] && continue
    
    echo "Configuring realm: ${REALM}"
    
    # Get current realm configuration
    REALM_CONFIG=$(curl -s -X GET "${KEYCLOAK_URL}/admin/realms/${REALM}" \
      -H "Authorization: Bearer ${ACCESS_TOKEN}" \
      -H "Content-Type: application/json")
    
    # Update SMTP settings
    UPDATED_CONFIG=$(echo "${REALM_CONFIG}" | jq --argjson smtp "$SMTP_CONFIG" '.smtpServer = $smtp')
    
    # Apply configuration
    RESPONSE=$(curl -s -w "\n%{http_code}" -X PUT "${KEYCLOAK_URL}/admin/realms/${REALM}" \
      -H "Authorization: Bearer ${ACCESS_TOKEN}" \
      -H "Content-Type: application/json" \
      -d "${UPDATED_CONFIG}")
    
    HTTP_CODE=$(echo "${RESPONSE}" | tail -n1)
    
    if [ "${HTTP_CODE}" == "204" ] || [ "${HTTP_CODE}" == "200" ]; then
        echo "  ✓ SMTP configured successfully"
        SUCCESS_COUNT=$((SUCCESS_COUNT + 1))
    else
        echo "  ❌ Failed to configure SMTP (HTTP ${HTTP_CODE})"
        FAIL_COUNT=$((FAIL_COUNT + 1))
    fi
done <<< "${REALM_NAMES}"

echo ""
echo "=========================================="
echo "Summary"
echo "=========================================="
echo "Successfully configured: ${SUCCESS_COUNT} realms"
if [ ${FAIL_COUNT} -gt 0 ]; then
    echo "Failed to configure: ${FAIL_COUNT} realms"
fi
echo ""
echo "✅ SMTP configuration complete!"
