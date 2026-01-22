#!/bin/bash
# Bootstrap SSO realm with Google Identity Provider
# Creates central SSO realm for Google OAuth brokering
#
# Prerequisites:
# - KEYCLOAK_URL, KEYCLOAK_ADMIN_USER, KEYCLOAK_ADMIN_PASSWORD
# - GOOGLE_CLIENT_ID, GOOGLE_CLIENT_SECRET
# - CONSOLE_URL (optional, defaults to https://console.kube-dc.com)
# - SSO_BROKER_SECRET (optional, auto-generated if not set)
#
# This script sets up:
# - SSO realm with self-registration and email verification
# - Google Identity Provider for social login
# - kube-dc client for console OIDC authentication
# - sso-broker client for backend services
# - /orgs group structure for organization management

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
        "displayNameHtml": "<div class=\"kc-logo-text\"><span>KUBE-DC SSO</span></div>",
        "enabled": true,
        "registrationAllowed": true,
        "registrationEmailAsUsername": true,
        "verifyEmail": true,
        "loginWithEmailAllowed": true,
        "duplicateEmailsAllowed": false,
        "resetPasswordAllowed": true,
        "editUsernameAllowed": false,
        "bruteForceProtected": true,
        "rememberMe": true,
        "loginTheme": "kube-dc",
        "accountTheme": "kube-dc",
        "emailTheme": "kube-dc",
        "smtpServer": {},
        "requiredCredentials": ["password"],
        "defaultSignatureAlgorithm": "RS256",
        "accessTokenLifespan": 300,
        "ssoSessionIdleTimeout": 1800,
        "ssoSessionMaxLifespan": 36000
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

create_auto_link_flow() {
    log_info "Creating auto-link authentication flow..."
    
    # Check if flow exists
    local existing=$(curl -s \
        -H "Authorization: Bearer $ADMIN_TOKEN" \
        "${KEYCLOAK_URL}/admin/realms/${SSO_REALM}/authentication/flows" | jq -r '.[] | select(.alias=="auto-link-broker-login") | .alias')
    
    if [ "$existing" == "auto-link-broker-login" ]; then
        log_warn "Auto-link flow already exists"
        return 0
    fi
    
    # Create the flow
    curl -s -X POST \
        -H "Authorization: Bearer $ADMIN_TOKEN" \
        -H "Content-Type: application/json" \
        -d '{"alias": "auto-link-broker-login", "description": "Auto-link accounts by email", "providerId": "basic-flow", "topLevel": true, "builtIn": false}' \
        "${KEYCLOAK_URL}/admin/realms/${SSO_REALM}/authentication/flows"
    
    # Add idp-create-user-if-unique execution
    curl -s -X POST \
        -H "Authorization: Bearer $ADMIN_TOKEN" \
        -H "Content-Type: application/json" \
        -d '{"provider": "idp-create-user-if-unique"}' \
        "${KEYCLOAK_URL}/admin/realms/${SSO_REALM}/authentication/flows/auto-link-broker-login/executions/execution"
    
    # Add idp-auto-link execution
    curl -s -X POST \
        -H "Authorization: Bearer $ADMIN_TOKEN" \
        -H "Content-Type: application/json" \
        -d '{"provider": "idp-auto-link"}' \
        "${KEYCLOAK_URL}/admin/realms/${SSO_REALM}/authentication/flows/auto-link-broker-login/executions/execution"
    
    # Set executions to ALTERNATIVE
    local executions=$(curl -s \
        -H "Authorization: Bearer $ADMIN_TOKEN" \
        "${KEYCLOAK_URL}/admin/realms/${SSO_REALM}/authentication/flows/auto-link-broker-login/executions")
    
    echo "$executions" | jq -c '.[]' | while read exec; do
        local id=$(echo "$exec" | jq -r '.id')
        curl -s -X PUT \
            -H "Authorization: Bearer $ADMIN_TOKEN" \
            -H "Content-Type: application/json" \
            -d "{\"id\": \"$id\", \"requirement\": \"ALTERNATIVE\"}" \
            "${KEYCLOAK_URL}/admin/realms/${SSO_REALM}/authentication/flows/auto-link-broker-login/executions"
    done
    
    log_info "Auto-link flow created"
}

create_passwordless_registration_flow() {
    log_info "Creating passwordless registration flow..."
    
    # Check if flow exists
    local existing=$(curl -s \
        -H "Authorization: Bearer $ADMIN_TOKEN" \
        "${KEYCLOAK_URL}/admin/realms/${SSO_REALM}/authentication/flows" | jq -r '.[] | select(.alias=="registration-no-password") | .alias')
    
    if [ "$existing" == "registration-no-password" ]; then
        log_warn "Passwordless registration flow already exists"
    else
        # Copy the built-in registration flow
        curl -s -X POST \
            -H "Authorization: Bearer $ADMIN_TOKEN" \
            -H "Content-Type: application/json" \
            -d '{"newName": "registration-no-password"}' \
            "${KEYCLOAK_URL}/admin/realms/${SSO_REALM}/authentication/flows/registration/copy"
        
        # Get the password validation execution ID and disable it
        local password_exec_id=$(curl -s \
            -H "Authorization: Bearer $ADMIN_TOKEN" \
            "${KEYCLOAK_URL}/admin/realms/${SSO_REALM}/authentication/flows/registration-no-password/executions" | \
            jq -r '.[] | select(.providerId=="registration-password-action") | .id')
        
        if [ -n "$password_exec_id" ] && [ "$password_exec_id" != "null" ]; then
            curl -s -X PUT \
                -H "Authorization: Bearer $ADMIN_TOKEN" \
                -H "Content-Type: application/json" \
                -d "{\"id\": \"$password_exec_id\", \"requirement\": \"DISABLED\"}" \
                "${KEYCLOAK_URL}/admin/realms/${SSO_REALM}/authentication/flows/registration-no-password/executions"
        fi
        
        log_info "Passwordless registration flow created"
    fi
    
    # Bind the flow to the realm
    curl -s -X PUT \
        -H "Authorization: Bearer $ADMIN_TOKEN" \
        -H "Content-Type: application/json" \
        -d '{"registrationFlow": "registration-no-password"}' \
        "${KEYCLOAK_URL}/admin/realms/${SSO_REALM}"
    
    log_info "Passwordless registration flow bound to realm"
}

create_google_idp() {
    log_info "Creating Google Identity Provider..."
    
    # Check if IdP exists
    local existing=$(curl -s \
        -H "Authorization: Bearer $ADMIN_TOKEN" \
        "${KEYCLOAK_URL}/admin/realms/${SSO_REALM}/identity-provider/instances/google" | jq -r '.alias')
    
    local idp_data='{
        "alias": "google",
        "displayName": "Google",
        "providerId": "google",
        "enabled": true,
        "trustEmail": true,
        "storeToken": false,
        "linkOnly": false,
        "firstBrokerLoginFlowAlias": "auto-link-broker-login",
        "authenticateByDefault": false,
        "config": {
            "clientId": "'$GOOGLE_CLIENT_ID'",
            "clientSecret": "'$GOOGLE_CLIENT_SECRET'",
            "defaultScope": "openid email profile",
            "syncMode": "LEGACY"
        }
    }'
    
    if [ "$existing" == "google" ]; then
        log_warn "Google IdP already exists, updating..."
        
        curl -s -X PUT \
            -H "Authorization: Bearer $ADMIN_TOKEN" \
            -H "Content-Type: application/json" \
            -d "$idp_data" \
            "${KEYCLOAK_URL}/admin/realms/${SSO_REALM}/identity-provider/instances/google"
        
        log_info "Google IdP updated"
        return 0
    fi
    
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

create_console_client() {
    log_info "Creating kube-dc console client..."
    
    # Check if client exists
    local existing=$(curl -s \
        -H "Authorization: Bearer $ADMIN_TOKEN" \
        "${KEYCLOAK_URL}/admin/realms/${SSO_REALM}/clients?clientId=kube-dc" | jq -r '.[0].id')
    
    local client_data='{
        "clientId": "kube-dc",
        "name": "Kube-DC Console",
        "enabled": true,
        "publicClient": true,
        "standardFlowEnabled": true,
        "implicitFlowEnabled": false,
        "directAccessGrantsEnabled": false,
        "protocol": "openid-connect",
        "rootUrl": "'${CONSOLE_URL:-https://console.kube-dc.com}'",
        "baseUrl": "'${CONSOLE_URL:-https://console.kube-dc.com}'?realm=sso",
        "redirectUris": [
            "'${CONSOLE_URL:-https://console.kube-dc.com}'/*",
            "http://localhost:*"
        ],
        "webOrigins": [
            "'${CONSOLE_URL:-https://console.kube-dc.com}'",
            "http://localhost:9000",
            "+"
        ],
        "defaultClientScopes": ["openid", "profile", "email"],
        "attributes": {
            "pkce.code.challenge.method": "S256",
            "post.logout.redirect.uris": "'${CONSOLE_URL:-https://console.kube-dc.com}'/*"
        }
    }'
    
    if [ "$existing" != "null" ] && [ -n "$existing" ]; then
        log_warn "kube-dc client already exists, updating..."
        
        curl -s -X PUT \
            -H "Authorization: Bearer $ADMIN_TOKEN" \
            -H "Content-Type: application/json" \
            -d "$client_data" \
            "${KEYCLOAK_URL}/admin/realms/${SSO_REALM}/clients/${existing}"
        
        log_info "kube-dc client updated"
        return 0
    fi
    
    local response=$(curl -s -w "\n%{http_code}" -X POST \
        -H "Authorization: Bearer $ADMIN_TOKEN" \
        -H "Content-Type: application/json" \
        -d "$client_data" \
        "${KEYCLOAK_URL}/admin/realms/${SSO_REALM}/clients")
    
    local http_code=$(echo "$response" | tail -n1)
    
    if [ "$http_code" != "201" ]; then
        log_error "Failed to create kube-dc client. HTTP: $http_code"
        echo "$response"
        exit 1
    fi
    
    log_info "kube-dc console client created"
}

configure_user_profile() {
    log_info "Configuring user profile with custom attributes..."
    
    # Get current user profile
    local current_profile=$(curl -s \
        -H "Authorization: Bearer $ADMIN_TOKEN" \
        "${KEYCLOAK_URL}/admin/realms/${SSO_REALM}/users/profile")
    
    # Check if pending_org_requests already exists
    local has_attr=$(echo "$current_profile" | jq -r '.attributes[] | select(.name=="pending_org_requests") | .name')
    
    if [ "$has_attr" == "pending_org_requests" ]; then
        log_warn "pending_org_requests attribute already configured"
        return 0
    fi
    
    # Add pending_org_requests attribute to the profile
    local updated_profile=$(echo "$current_profile" | jq '.attributes += [{
        "name": "pending_org_requests",
        "displayName": "Pending Organization Requests",
        "permissions": {
            "view": ["admin"],
            "edit": ["admin"]
        },
        "multivalued": true
    }]')
    
    local response=$(curl -s -w "\n%{http_code}" -X PUT \
        -H "Authorization: Bearer $ADMIN_TOKEN" \
        -H "Content-Type: application/json" \
        -d "$updated_profile" \
        "${KEYCLOAK_URL}/admin/realms/${SSO_REALM}/users/profile")
    
    local http_code=$(echo "$response" | tail -n1)
    
    if [ "$http_code" != "200" ]; then
        log_error "Failed to configure user profile. HTTP: $http_code"
        echo "$response"
        exit 1
    fi
    
    log_info "User profile configured with pending_org_requests attribute"
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
    echo "Console URL: ${CONSOLE_URL:-https://console.kube-dc.com}"
    echo ""
    echo "Configuration:"
    echo "  - Self-registration: Enabled (passwordless)"
    echo "  - Email verification: Required"
    echo "  - Google IdP: Configured with auto-link"
    echo "  - Console Client: kube-dc (public, PKCE)"
    echo "  - Broker Client: $SSO_BROKER_CLIENT"
    echo ""
    echo "SSO_BROKER_SECRET=$SSO_BROKER_SECRET"
    echo ""
    echo "Next steps:"
    echo "1. Configure SMTP server for email verification"
    echo "2. Run: ./add-org-to-sso.sh <org-slug> to add organizations"
    echo "3. Update master-config secret with SSO credentials"
    echo "=========================================="
}

# Main
main() {
    check_prerequisites
    get_admin_token
    create_sso_realm
    create_auto_link_flow
    create_passwordless_registration_flow
    create_google_idp
    create_console_client
    create_broker_client
    configure_user_profile
    create_orgs_group
    print_summary
}

main "$@"
