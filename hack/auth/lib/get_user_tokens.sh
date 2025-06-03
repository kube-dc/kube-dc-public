#!/bin/bash
# Function to get Keycloak token using username & password
get_keycloak_user_token_response() {
    ORG_NAME=$1
    USERNAME=$2
    PASSWORD=$3
    ORG_NAME=$4
    KUBE_DC_NAMESPACE="kube-dc"
    CLIENT_ID="kube-dc"

    BASE_URL=$(kubectl get -n $KUBE_DC_NAMESPACE secret master-config -o jsonpath="{.data.url}" | base64 -d)
    local TOKEN_RESPONSE=$(curl -s -X POST "$BASE_URL/realms/$ORG_NAME/protocol/openid-connect/token" \
                            -H "Content-Type: application/x-www-form-urlencoded" \
                            -d "grant_type=password" \
                            -d "client_id=$CLIENT_ID" \
                            -d "username=$USERNAME" \
                            -d "password=$PASSWORD")
    
    echo $TOKEN_RESPONSE
}

refresh_token() {
 # Get new access token
    ACCESS_TOKEN=$(curl -s -X POST "$BASE_URL/realms/$ORG_NAME/protocol/openid-connect/token" \
     -H "Content-Type: application/x-www-form-urlencoded" \
     -d "client_id=$CLIENT_ID" \
     -d "grant_type=refresh_token" \
     -d "refresh_token=$REFRESH_TOKEN" | jq -r .access_token)   
}

get_keycloak_user_token() {
    get_keycloak_user_token_response $1 $2 $3 $4 | jq -r '.access_token'
}

get_keycloak_user_refresh_token() {
    get_keycloak_user_token_response $1 $2 $3 $4 | jq -r '.refresh_token'
}


