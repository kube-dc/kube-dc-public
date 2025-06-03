#!/bin/bash
# Static configurations
ORG_NAME="shalb"
CLIENT_ID="kube-dc"
KUBE_DC_NAMES="kube-dc"

# Retrieve BASE_URL from Kubernetes secret
BASE_URL=$(kubectl get -n $KUBE_DC_NAMES secret master-config -o jsonpath="{.data.url}" | base64 -d)

# Prompt for username and password
read -p "Enter Keycloak username: " USERNAME
read -s -p "Enter Keycloak password: " PASSWORD
echo ""

# Function to get Keycloak token using username & password
get_keycloak_user_token() {
    local TOKEN_RESPONSE=$(curl -s -X POST "$BASE_URL/realms/$ORG_NAME/protocol/openid-connect/token" \
                            -H "Content-Type: application/x-www-form-urlencoded" \
                            -d "grant_type=password" \
                            -d "client_id=$CLIENT_ID" \
                            -d "username=$USERNAME" \
                            -d "password=$PASSWORD")
    
    echo $(echo $TOKEN_RESPONSE | jq -r '.access_token') # Extract access token
}

get_keycloak_user_token
