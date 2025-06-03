#!/bin/bash
# config.sh
# Static configurations
ORG_NAME="shalb"
CLIENT_ID="kube-dc"
KUBE_DC_NAMES="kube-dc"
# export CLIENT_ID="realm-management"
# Function to get Keycloak Admin Token

BASE_URL=$(kubectl get -n $KUBE_DC_NAMES secret master-config -o jsonpath="{.data.url}" | base64 -d)

ADMIN_USERNAME=$(kubectl get -n $ORG_NAME secret realm-access -o jsonpath="{.data.user}" | base64 -d)
ADMIN_PASSWORD=$(kubectl get -n $ORG_NAME secret realm-access -o jsonpath="{.data.password}" | base64 -d)

get_keycloak_admin_token() {
    local TOKEN_RESPONSE=$(curl -s -X POST "$BASE_URL/realms/$ORG_NAME/protocol/openid-connect/token" \
                            -H "Content-Type: application/x-www-form-urlencoded" \
                            -d "grant_type=password" \
                            -d "client_id=$CLIENT_ID" \
                            -d "username=$ADMIN_USERNAME" \
                            -d "password=$ADMIN_PASSWORD")
    echo $(echo $TOKEN_RESPONSE | jq -r '.access_token')
}
get_keycloak_admin_token
