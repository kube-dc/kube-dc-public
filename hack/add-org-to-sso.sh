#!/bin/bash
# Add organization to SSO realm and configure org realm IdP brokering
# Creates org group in SSO realm and configures org realm to broker via SSO
#
# Usage: ./add-org-to-sso.sh <org-slug>
#
# Prerequisites:
# - KEYCLOAK_URL, KEYCLOAK_ADMIN_USER, KEYCLOAK_ADMIN_PASSWORD
# - SSO_BROKER_SECRET (from bootstrap-sso-realm.sh output)
# - Organization realm must already exist (created by kube-dc controller)

set -e

ORG_SLUG="${1:-}"
SSO_REALM="sso"
SSO_BROKER_CLIENT="sso-broker"
SSO_IDP_ALIAS="sso"

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
    
    if [ -z "$ORG_SLUG" ]; then
        log_error "Usage: $0 <org-slug>"
        exit 1
    fi
    
    for cmd in jq curl; do
        if ! command -v $cmd &> /dev/null; then
            log_error "$cmd is not installed"
            exit 1
        fi
    done
    
    for var in KEYCLOAK_URL KEYCLOAK_ADMIN_USER KEYCLOAK_ADMIN_PASSWORD SSO_BROKER_SECRET; do
        if [ -z "${!var}" ]; then
            log_error "$var environment variable is not set"
            exit 1
        fi
    done
    
    log_info "Prerequisites OK (org: $ORG_SLUG)"
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
        exit 1
    fi
    
    log_info "Admin token obtained"
}

check_org_realm_exists() {
    log_info "Checking if org realm '$ORG_SLUG' exists..."
    
    local http_code=$(curl -s -o /dev/null -w "%{http_code}" \
        -H "Authorization: Bearer $ADMIN_TOKEN" \
        "${KEYCLOAK_URL}/admin/realms/${ORG_SLUG}")
    
    if [ "$http_code" != "200" ]; then
        log_error "Organization realm '$ORG_SLUG' does not exist"
        log_error "Create the organization first via kube-dc controller"
        exit 1
    fi
    
    log_info "Org realm exists"
}

check_sso_realm_exists() {
    log_info "Checking if SSO realm exists..."
    
    local http_code=$(curl -s -o /dev/null -w "%{http_code}" \
        -H "Authorization: Bearer $ADMIN_TOKEN" \
        "${KEYCLOAK_URL}/admin/realms/${SSO_REALM}")
    
    if [ "$http_code" != "200" ]; then
        log_error "SSO realm does not exist. Run bootstrap-sso-realm.sh first"
        exit 1
    fi
    
    log_info "SSO realm exists"
}

add_org_group_to_sso() {
    log_info "Adding org group /orgs/$ORG_SLUG to SSO realm..."
    
    # Get /orgs parent group ID
    local orgs_group=$(curl -s \
        -H "Authorization: Bearer $ADMIN_TOKEN" \
        "${KEYCLOAK_URL}/admin/realms/${SSO_REALM}/groups?search=orgs" | jq -r '.[0]')
    
    local orgs_group_id=$(echo "$orgs_group" | jq -r '.id')
    
    if [ "$orgs_group_id" == "null" ] || [ -z "$orgs_group_id" ]; then
        log_error "/orgs parent group not found in SSO realm"
        exit 1
    fi
    
    # Check if org subgroup already exists
    local existing=$(curl -s \
        -H "Authorization: Bearer $ADMIN_TOKEN" \
        "${KEYCLOAK_URL}/admin/realms/${SSO_REALM}/groups/${orgs_group_id}/children" | jq -r ".[] | select(.name==\"$ORG_SLUG\") | .id")
    
    if [ -n "$existing" ]; then
        log_warn "Org group /orgs/$ORG_SLUG already exists"
        ORG_GROUP_ID="$existing"
        return 0
    fi
    
    # Create org subgroup
    local response=$(curl -s -w "\n%{http_code}" -X POST \
        -H "Authorization: Bearer $ADMIN_TOKEN" \
        -H "Content-Type: application/json" \
        -d '{"name": "'$ORG_SLUG'"}' \
        "${KEYCLOAK_URL}/admin/realms/${SSO_REALM}/groups/${orgs_group_id}/children")
    
    local http_code=$(echo "$response" | tail -n1)
    
    if [ "$http_code" != "201" ]; then
        log_error "Failed to create org group. HTTP: $http_code"
        echo "$response"
        exit 1
    fi
    
    # Get created group ID
    ORG_GROUP_ID=$(curl -s \
        -H "Authorization: Bearer $ADMIN_TOKEN" \
        "${KEYCLOAK_URL}/admin/realms/${SSO_REALM}/groups/${orgs_group_id}/children" | jq -r ".[] | select(.name==\"$ORG_SLUG\") | .id")
    
    log_info "Org group /orgs/$ORG_SLUG created (ID: $ORG_GROUP_ID)"
}

create_auto_link_flow() {
    log_info "Creating auto-link authentication flow in org realm..."
    
    local flow_alias="auto-link-broker-login"
    
    # Check if flow exists
    local existing=$(curl -s \
        -H "Authorization: Bearer $ADMIN_TOKEN" \
        "${KEYCLOAK_URL}/admin/realms/${ORG_SLUG}/authentication/flows" | jq -r ".[] | select(.alias==\"$flow_alias\") | .alias")
    
    if [ "$existing" == "$flow_alias" ]; then
        log_warn "Auto-link flow already exists"
        return 0
    fi
    
    # Create flow
    curl -s -X POST \
        -H "Authorization: Bearer $ADMIN_TOKEN" \
        -H "Content-Type: application/json" \
        -d '{"alias":"'$flow_alias'","description":"Auto-link accounts by email","providerId":"basic-flow","topLevel":true,"builtIn":false}' \
        "${KEYCLOAK_URL}/admin/realms/${ORG_SLUG}/authentication/flows"
    
    # Add executions
    curl -s -X POST \
        -H "Authorization: Bearer $ADMIN_TOKEN" \
        -H "Content-Type: application/json" \
        -d '{"provider":"idp-create-user-if-unique"}' \
        "${KEYCLOAK_URL}/admin/realms/${ORG_SLUG}/authentication/flows/${flow_alias}/executions/execution"
    
    curl -s -X POST \
        -H "Authorization: Bearer $ADMIN_TOKEN" \
        -H "Content-Type: application/json" \
        -d '{"provider":"idp-auto-link"}' \
        "${KEYCLOAK_URL}/admin/realms/${ORG_SLUG}/authentication/flows/${flow_alias}/executions/execution"
    
    # Get execution IDs and set to ALTERNATIVE
    local executions=$(curl -s \
        -H "Authorization: Bearer $ADMIN_TOKEN" \
        "${KEYCLOAK_URL}/admin/realms/${ORG_SLUG}/authentication/flows/${flow_alias}/executions")
    
    echo "$executions" | jq -c '.[]' | while read exec; do
        local exec_id=$(echo "$exec" | jq -r '.id')
        curl -s -X PUT \
            -H "Authorization: Bearer $ADMIN_TOKEN" \
            -H "Content-Type: application/json" \
            -d '{"id":"'$exec_id'","requirement":"ALTERNATIVE"}' \
            "${KEYCLOAK_URL}/admin/realms/${ORG_SLUG}/authentication/flows/${flow_alias}/executions"
    done
    
    log_info "Auto-link flow created"
}

configure_org_realm_sso_idp() {
    log_info "Configuring SSO Identity Provider in org realm '$ORG_SLUG'..."
    
    # Check if IdP already exists
    local existing=$(curl -s \
        -H "Authorization: Bearer $ADMIN_TOKEN" \
        "${KEYCLOAK_URL}/admin/realms/${ORG_SLUG}/identity-provider/instances/${SSO_IDP_ALIAS}" | jq -r '.alias')
    
    if [ "$existing" == "$SSO_IDP_ALIAS" ]; then
        log_warn "SSO IdP already exists in org realm, updating..."
        
        local idp_data='{
            "alias": "'$SSO_IDP_ALIAS'",
            "displayName": "Login with Google",
            "providerId": "keycloak-oidc",
            "enabled": true,
            "trustEmail": true,
            "storeToken": false,
            "linkOnly": false,
            "firstBrokerLoginFlowAlias": "auto-link-broker-login",
            "config": {
                "tokenUrl": "'$KEYCLOAK_URL'/realms/'$SSO_REALM'/protocol/openid-connect/token",
                "authorizationUrl": "'$KEYCLOAK_URL'/realms/'$SSO_REALM'/protocol/openid-connect/auth?kc_idp_hint=google",
                "logoutUrl": "'$KEYCLOAK_URL'/realms/'$SSO_REALM'/protocol/openid-connect/logout",
                "userInfoUrl": "'$KEYCLOAK_URL'/realms/'$SSO_REALM'/protocol/openid-connect/userinfo",
                "jwksUrl": "'$KEYCLOAK_URL'/realms/'$SSO_REALM'/protocol/openid-connect/certs",
                "issuer": "'$KEYCLOAK_URL'/realms/'$SSO_REALM'",
                "clientId": "'$SSO_BROKER_CLIENT'",
                "clientSecret": "'$SSO_BROKER_SECRET'",
                "defaultScope": "openid email profile",
                "syncMode": "FORCE",
                "validateSignature": "true",
                "useJwksUrl": "true",
                "pkceEnabled": "false",
                "backchannelSupported": "true"
            }
        }'
        
        curl -s -X PUT \
            -H "Authorization: Bearer $ADMIN_TOKEN" \
            -H "Content-Type: application/json" \
            -d "$idp_data" \
            "${KEYCLOAK_URL}/admin/realms/${ORG_SLUG}/identity-provider/instances/${SSO_IDP_ALIAS}"
        
        log_info "SSO IdP updated in org realm"
        return 0
    fi
    
    local idp_data='{
        "alias": "'$SSO_IDP_ALIAS'",
        "displayName": "Login with Google",
        "providerId": "keycloak-oidc",
        "enabled": true,
        "trustEmail": true,
        "storeToken": false,
        "linkOnly": false,
        "firstBrokerLoginFlowAlias": "auto-link-broker-login",
        "config": {
            "tokenUrl": "'$KEYCLOAK_URL'/realms/'$SSO_REALM'/protocol/openid-connect/token",
            "authorizationUrl": "'$KEYCLOAK_URL'/realms/'$SSO_REALM'/protocol/openid-connect/auth?kc_idp_hint=google",
            "logoutUrl": "'$KEYCLOAK_URL'/realms/'$SSO_REALM'/protocol/openid-connect/logout",
            "userInfoUrl": "'$KEYCLOAK_URL'/realms/'$SSO_REALM'/protocol/openid-connect/userinfo",
            "jwksUrl": "'$KEYCLOAK_URL'/realms/'$SSO_REALM'/protocol/openid-connect/certs",
            "issuer": "'$KEYCLOAK_URL'/realms/'$SSO_REALM'",
            "clientId": "'$SSO_BROKER_CLIENT'",
            "clientSecret": "'$SSO_BROKER_SECRET'",
            "defaultScope": "openid email profile",
            "syncMode": "FORCE",
            "validateSignature": "true",
            "useJwksUrl": "true",
            "pkceEnabled": "false",
            "backchannelSupported": "true"
        }
    }'
    
    local response=$(curl -s -w "\n%{http_code}" -X POST \
        -H "Authorization: Bearer $ADMIN_TOKEN" \
        -H "Content-Type: application/json" \
        -d "$idp_data" \
        "${KEYCLOAK_URL}/admin/realms/${ORG_SLUG}/identity-provider/instances")
    
    local http_code=$(echo "$response" | tail -n1)
    
    if [ "$http_code" != "201" ]; then
        log_error "Failed to create SSO IdP. HTTP: $http_code"
        echo "$response"
        exit 1
    fi
    
    log_info "SSO IdP created in org realm"
}

create_idp_mappers() {
    log_info "Creating IdP mappers for user attributes..."
    
    local mappers='[
        {
            "name": "email",
            "identityProviderMapper": "oidc-user-attribute-idp-mapper",
            "identityProviderAlias": "'$SSO_IDP_ALIAS'",
            "config": {
                "claim": "email",
                "user.attribute": "email",
                "syncMode": "FORCE"
            }
        },
        {
            "name": "firstName",
            "identityProviderMapper": "oidc-user-attribute-idp-mapper",
            "identityProviderAlias": "'$SSO_IDP_ALIAS'",
            "config": {
                "claim": "given_name",
                "user.attribute": "firstName",
                "syncMode": "FORCE"
            }
        },
        {
            "name": "lastName",
            "identityProviderMapper": "oidc-user-attribute-idp-mapper",
            "identityProviderAlias": "'$SSO_IDP_ALIAS'",
            "config": {
                "claim": "family_name",
                "user.attribute": "lastName",
                "syncMode": "FORCE"
            }
        }
    ]'
    
    echo "$mappers" | jq -c '.[]' | while read mapper; do
        local mapper_name=$(echo "$mapper" | jq -r '.name')
        
        # Check if mapper exists
        local existing=$(curl -s \
            -H "Authorization: Bearer $ADMIN_TOKEN" \
            "${KEYCLOAK_URL}/admin/realms/${ORG_SLUG}/identity-provider/instances/${SSO_IDP_ALIAS}/mappers" | jq -r ".[] | select(.name==\"$mapper_name\") | .id")
        
        if [ -n "$existing" ]; then
            log_warn "Mapper '$mapper_name' already exists"
            continue
        fi
        
        curl -s -X POST \
            -H "Authorization: Bearer $ADMIN_TOKEN" \
            -H "Content-Type: application/json" \
            -d "$mapper" \
            "${KEYCLOAK_URL}/admin/realms/${ORG_SLUG}/identity-provider/instances/${SSO_IDP_ALIAS}/mappers"
        
        log_info "Created mapper: $mapper_name"
    done
}

print_summary() {
    echo ""
    echo "=========================================="
    echo "Organization SSO Configuration Complete"
    echo "=========================================="
    echo "Organization: $ORG_SLUG"
    echo "SSO Group: /orgs/$ORG_SLUG"
    echo "Org Realm IdP: $SSO_IDP_ALIAS (keycloak-oidc)"
    echo ""
    echo "Login URL with SSO:"
    echo "${KEYCLOAK_URL}/realms/${ORG_SLUG}/protocol/openid-connect/auth?kc_idp_hint=${SSO_IDP_ALIAS}&client_id=kube-dc&response_type=code&redirect_uri=<your-redirect-uri>"
    echo ""
    echo "To add a user to this org in SSO realm:"
    echo "1. User logs in via Google to SSO realm"
    echo "2. Admin adds user to /orgs/$ORG_SLUG group in SSO realm"
    echo "=========================================="
}

# Main
main() {
    check_prerequisites
    get_admin_token
    check_sso_realm_exists
    check_org_realm_exists
    add_org_group_to_sso
    create_auto_link_flow
    configure_org_realm_sso_idp
    create_idp_mappers
    print_summary
}

main "$@"
