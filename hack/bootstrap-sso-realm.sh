#!/bin/bash
# Bootstrap SSO realm with Google Identity Provider
# Creates central SSO realm for Google OAuth brokering
#
# Prerequisites:
# - KEYCLOAK_URL, KEYCLOAK_ADMIN_USER, KEYCLOAK_ADMIN_PASSWORD
# - GOOGLE_CLIENT_ID, GOOGLE_CLIENT_SECRET
# - SSO_BROKER_SECRET (optional, auto-generated if not set)

set -e

SSO_REALM="sso"
SSO_BROKER_CLIENT="sso-broker"

# Colors
RED='\033[0;31m'
GREEN='\033[0;32m'
YELLOW='\033[1;33m'
NC='\033[0m'

log_info() { echo -e "${GREEN}[INFO]${NC} $1"; }
log_warn() { echo -e "${YELLOW}[WARN]${NC} $1"; }
log_error() { echo -e "${RED}[ERROR]${NC} $1"; }

check_prerequisites() {
    log_info "Checking prerequisites..."
    
    for cmd in jq curl; do
        if ! command -v $cmd &> /dev/null; then
            log_error "$cmd is not installed"
            exit 1
        fi
    done
    
    for var in KEYCLOAK_URL KEYCLOAK_ADMIN_USER KEYCLOAK_ADMIN_PASSWORD GOOGLE_CLIENT_ID GOOGLE_CLIENT_SECRET; do
        if [ -z "${!var}" ]; then
            log_error "$var environment variable is not set"
            exit 1
        fi
    done
    
    # Auto-generate broker secret if not provided
    if [ -z "$SSO_BROKER_SECRET" ]; then
        SSO_BROKER_SECRET=$(openssl rand -base64 32 | tr -d '\n')
        log_info "Generated SSO_BROKER_SECRET"
    fi
    
    log_info "Prerequisites OK"
}

get_admin_token() {
    log_info "Getting admin token..."
    
    local response=$(curl -s -X POST "${KEYCLOAK_URL}/realms/master/protocol/openid-connect/token" \
        -H "Content-Type: application/x-www-form-urlencoded" \
        -d "username=${KEYCLOAK_ADMIN_USER}" \
        -d "password=${KEYCLOAK_ADMIN_PASSWORD}" \
        -d "grant_type=password" \
        -d "client_id=admin-cli")
    
    ADMIN_TOKEN=$(echo "$response" | jq -r '.access_token')
    
    if [ "$ADMIN_TOKEN" == "null" ] || [ -z "$ADMIN_TOKEN" ]; then
        log_error "Failed to get admin token"
        echo "Response: $response"
        exit 1
    fi
    
    log_info "Admin token obtained"
}

create_sso_realm() {
    log_info "Creating SSO realm..."
    
    # Check if realm exists
    local http_code=$(curl -s -o /dev/null -w "%{http_code}" \
        -H "Authorization: Bearer $ADMIN_TOKEN" \
        "${KEYCLOAK_URL}/admin/realms/${SSO_REALM}")
    
    if [ "$http_code" == "200" ]; then
        log_warn "SSO realm already exists"
        return 0
    fi
    
    local realm_data='{
        "realm": "'$SSO_REALM'",
        "displayName": "Kube-DC SSO",
        "enabled": true,
        "registrationAllowed": false,
        "loginWithEmailAllowed": true,
        "duplicateEmailsAllowed": false,
        "resetPasswordAllowed": false,
        "editUsernameAllowed": false,
        "bruteForceProtected": true
    }'
    
    local response=$(curl -s -w "\n%{http_code}" -X POST \
        -H "Authorization: Bearer $ADMIN_TOKEN" \
        -H "Content-Type: application/json" \
        -d "$realm_data" \
        "${KEYCLOAK_URL}/admin/realms")
    
    local http_code=$(echo "$response" | tail -n1)
    
    if [ "$http_code" != "201" ]; then
        log_error "Failed to create SSO realm. HTTP: $http_code"
        echo "$response"
        exit 1
    fi
    
    log_info "SSO realm created"
}

create_google_idp() {
    log_info "Creating Google Identity Provider..."
    
    # Check if IdP exists
    local existing=$(curl -s \
        -H "Authorization: Bearer $ADMIN_TOKEN" \
        "${KEYCLOAK_URL}/admin/realms/${SSO_REALM}/identity-provider/instances/google" | jq -r '.alias')
    
    if [ "$existing" == "google" ]; then
        log_warn "Google IdP already exists, updating..."
        
        local idp_data='{
            "alias": "google",
            "displayName": "Google",
            "providerId": "google",
            "enabled": true,
            "trustEmail": true,
            "storeToken": false,
            "linkOnly": false,
            "firstBrokerLoginFlowAlias": "first broker login",
            "authenticateByDefault": true,
            "config": {
                "clientId": "'$GOOGLE_CLIENT_ID'",
                "clientSecret": "'$GOOGLE_CLIENT_SECRET'",
                "defaultScope": "openid email profile",
                "syncMode": "FORCE"
            }
        }'
        
        curl -s -X PUT \
            -H "Authorization: Bearer $ADMIN_TOKEN" \
            -H "Content-Type: application/json" \
            -d "$idp_data" \
            "${KEYCLOAK_URL}/admin/realms/${SSO_REALM}/identity-provider/instances/google"
        
        log_info "Google IdP updated"
        return 0
    fi
    
    local idp_data='{
        "alias": "google",
        "displayName": "Google",
        "providerId": "google",
        "enabled": true,
        "trustEmail": true,
        "storeToken": false,
        "linkOnly": false,
        "firstBrokerLoginFlowAlias": "first broker login",
        "authenticateByDefault": true,
        "config": {
            "clientId": "'$GOOGLE_CLIENT_ID'",
            "clientSecret": "'$GOOGLE_CLIENT_SECRET'",
            "defaultScope": "openid email profile",
            "syncMode": "FORCE"
        }
    }'
    
    local response=$(curl -s -w "\n%{http_code}" -X POST \
        -H "Authorization: Bearer $ADMIN_TOKEN" \
        -H "Content-Type: application/json" \
        -d "$idp_data" \
        "${KEYCLOAK_URL}/admin/realms/${SSO_REALM}/identity-provider/instances")
    
    local http_code=$(echo "$response" | tail -n1)
    
    if [ "$http_code" != "201" ]; then
        log_error "Failed to create Google IdP. HTTP: $http_code"
        echo "$response"
        exit 1
    fi
    
    log_info "Google IdP created"
}

create_broker_client() {
    log_info "Creating SSO broker client..."
    
    # Check if client exists
    local existing=$(curl -s \
        -H "Authorization: Bearer $ADMIN_TOKEN" \
        "${KEYCLOAK_URL}/admin/realms/${SSO_REALM}/clients?clientId=${SSO_BROKER_CLIENT}" | jq -r '.[0].id')
    
    if [ "$existing" != "null" ] && [ -n "$existing" ]; then
        log_warn "Broker client already exists, updating secret..."
        
        # Update client secret
        curl -s -X POST \
            -H "Authorization: Bearer $ADMIN_TOKEN" \
            -H "Content-Type: application/json" \
            -d '{"type":"secret","value":"'$SSO_BROKER_SECRET'"}' \
            "${KEYCLOAK_URL}/admin/realms/${SSO_REALM}/clients/${existing}/client-secret"
        
        log_info "Broker client secret updated"
        return 0
    fi
    
    local client_data='{
        "clientId": "'$SSO_BROKER_CLIENT'",
        "enabled": true,
        "clientAuthenticatorType": "client-secret",
        "secret": "'$SSO_BROKER_SECRET'",
        "standardFlowEnabled": true,
        "directAccessGrantsEnabled": false,
        "publicClient": false,
        "protocol": "openid-connect",
        "redirectUris": ["*"],
        "webOrigins": ["*"],
        "defaultClientScopes": ["openid", "profile", "email"],
        "attributes": {
            "backchannel.logout.session.required": "true"
        }
    }'
    
    local response=$(curl -s -w "\n%{http_code}" -X POST \
        -H "Authorization: Bearer $ADMIN_TOKEN" \
        -H "Content-Type: application/json" \
        -d "$client_data" \
        "${KEYCLOAK_URL}/admin/realms/${SSO_REALM}/clients")
    
    local http_code=$(echo "$response" | tail -n1)
    
    if [ "$http_code" != "201" ]; then
        log_error "Failed to create broker client. HTTP: $http_code"
        echo "$response"
        exit 1
    fi
    
    log_info "Broker client created"
}

create_orgs_group() {
    log_info "Creating /orgs parent group..."
    
    local existing=$(curl -s \
        -H "Authorization: Bearer $ADMIN_TOKEN" \
        "${KEYCLOAK_URL}/admin/realms/${SSO_REALM}/groups?search=orgs" | jq -r '.[0].name')
    
    if [ "$existing" == "orgs" ]; then
        log_warn "/orgs group already exists"
        return 0
    fi
    
    curl -s -X POST \
        -H "Authorization: Bearer $ADMIN_TOKEN" \
        -H "Content-Type: application/json" \
        -d '{"name": "orgs"}' \
        "${KEYCLOAK_URL}/admin/realms/${SSO_REALM}/groups"
    
    log_info "/orgs group created"
}

print_summary() {
    echo ""
    echo "=========================================="
    echo "SSO Realm Bootstrap Complete"
    echo "=========================================="
    echo "SSO Realm: $SSO_REALM"
    echo "Keycloak URL: $KEYCLOAK_URL"
    echo "Google IdP: Configured"
    echo "Broker Client: $SSO_BROKER_CLIENT"
    echo ""
    echo "SSO_BROKER_SECRET=$SSO_BROKER_SECRET"
    echo ""
    echo "Next steps:"
    echo "1. Run: ./add-org-to-sso.sh <org-slug> to add organizations"
    echo "2. Update master-config secret with SSO credentials"
    echo "=========================================="
}

# Main
main() {
    check_prerequisites
    get_admin_token
    create_sso_realm
    create_google_idp
    create_broker_client
    create_orgs_group
    print_summary
}

main "$@"
