#!/bin/bash

# Load environment variables
REFRESH_TOKEN_FILE="/tmp/kube_dc_refresh_token"
KEYCLOAK_ENDPOINT="${KEYCLOAK_ENDPOINT}"
REALM="${ORGANIZATION}"
CLIENT_ID="kube-dc"    # Change this to your actual client ID

# Cache file location
CACHE_FILE="/tmp/kube_dc_token_cache"
CACHE_TTL=120  # 2 minutes in seconds

# Function to get current timestamp
get_timestamp() {
    date +%s
}

# Function to check if token is still valid
is_token_valid() {
    if [ ! -f "$CACHE_FILE" ]; then
        return 1
    fi

    local cached_time
    local current_time
    local age

    cached_time=$(head -n 1 "$CACHE_FILE")
    current_time=$(get_timestamp)
    age=$((current_time - cached_time))

    if [ $age -lt $CACHE_TTL ]; then
        return 0
    else
        return 1
    fi
}

# Function to get cached token
get_cached_token() {
    tail -n 1 "$CACHE_FILE"
}

# Function to save token to cache
save_token_to_cache() {
    local token=$1
    echo "$(get_timestamp)" > "$CACHE_FILE"
    echo "$token" >> "$CACHE_FILE"
    chmod 600 "$CACHE_FILE"
}

# Function to save refresh token to file
save_refresh_token() {
    local refresh_token=$1
    echo "$refresh_token" > "$REFRESH_TOKEN_FILE"
    chmod 600 "$REFRESH_TOKEN_FILE"
}

# Function to check if refresh token exists and is readable
check_refresh_token() {
    if [ ! -f "$REFRESH_TOKEN_FILE" ]; then
        return 1
    fi
    REFRESH_TOKEN=$(cat "$REFRESH_TOKEN_FILE")
    if [ -z "$REFRESH_TOKEN" ]; then
        return 1
    fi
    return 0
}

# Function to get new tokens with username and password
get_new_tokens_with_credentials() {
    echo "Refresh token is expired or invalid. Need to get new tokens with credentials." >&2
    
    # Prompt for username and password
    read -p "Enter Keycloak username: " USERNAME
    read -s -p "Enter Keycloak password: " PASSWORD
    echo ""
    
    # Get new tokens using username & password
    TOKEN_RESPONSE=$(curl -s -X POST "${KEYCLOAK_ENDPOINT}/realms/${REALM}/protocol/openid-connect/token" \
                      -H "Content-Type: application/x-www-form-urlencoded" \
                      -d "grant_type=password" \
                      -d "client_id=${CLIENT_ID}" \
                      -d "username=${USERNAME}" \
                      -d "password=${PASSWORD}")
    
    # Extract tokens
    ACCESS_TOKEN=$(echo "$TOKEN_RESPONSE" | jq -r '.access_token')
    REFRESH_TOKEN=$(echo "$TOKEN_RESPONSE" | jq -r '.refresh_token')
    
    # Validate response
    if [[ "$ACCESS_TOKEN" == "null" || -z "$ACCESS_TOKEN" || "$REFRESH_TOKEN" == "null" || -z "$REFRESH_TOKEN" ]]; then
        echo "Failed to authenticate with provided credentials." >&2
        exit 1
    fi
    
    # Save tokens
    save_token_to_cache "$ACCESS_TOKEN"
    save_refresh_token "$REFRESH_TOKEN"
    
    echo "New tokens obtained successfully." >&2
}

# Check if we have a valid cached token
if is_token_valid; then
    ACCESS_TOKEN=$(get_cached_token)
else
    # Try to use refresh token if it exists
    if check_refresh_token; then
        # If no valid cached token, fetch a new one using refresh token
        TOKEN_RESPONSE=$(curl -s -X POST "${KEYCLOAK_ENDPOINT}/realms/${REALM}/protocol/openid-connect/token" \
          -H "Content-Type: application/x-www-form-urlencoded" \
          -d "client_id=${CLIENT_ID}" \
          -d "grant_type=refresh_token" \
          -d "refresh_token=${REFRESH_TOKEN}")

        # Extract the access token
        ACCESS_TOKEN=$(echo "$TOKEN_RESPONSE" | jq -r .access_token)
        NEW_REFRESH_TOKEN=$(echo "$TOKEN_RESPONSE" | jq -r .refresh_token)

        # Check if refresh token is expired or invalid
        if [[ "$ACCESS_TOKEN" == "null" || -z "$ACCESS_TOKEN" ]]; then
            # Fall back to username/password authentication
            get_new_tokens_with_credentials
            ACCESS_TOKEN=$(get_cached_token)
        else
            # Save the new token to cache
            save_token_to_cache "$ACCESS_TOKEN"
            
            # Save the new refresh token if provided
            if [[ "$NEW_REFRESH_TOKEN" != "null" && -n "$NEW_REFRESH_TOKEN" ]]; then
                save_refresh_token "$NEW_REFRESH_TOKEN"
            fi
        fi
    else
        # No valid refresh token, get new tokens with username/password
        get_new_tokens_with_credentials
        ACCESS_TOKEN=$(get_cached_token)
    fi
fi

# Print the token (this will be used in kubeconfig)
cat <<EOF
{
  "apiVersion": "client.authentication.k8s.io/v1",
  "kind": "ExecCredential",
  "status": {
    "token": "$ACCESS_TOKEN"
  }
}
EOF