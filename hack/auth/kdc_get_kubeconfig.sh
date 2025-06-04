#!/bin/bash

# kdc_get_kubeconfig.sh <project-name>
# Script to set up kubeconfig and authentication for kube-dc project
# Usage: ./kdc_get_kubeconfig.sh [project-name]

set -e

# Input argument (e.g., org/project)
ARG_INPUT=$1

# Check if input argument is provided
if [ -z "${ARG_INPUT}" ]; then
    echo -e "${RED}Error: Organization and Project name are required.${NC}"
    echo -e "Usage: ./kdc_get_kubeconfig.sh <organization/project-name>"
    echo -e "Example: ./kdc_get_kubeconfig.sh my-org/my-project"
    exit 1
fi

# Parse ORGANIZATION and PROJECT_NAME from the input
if [[ "${ARG_INPUT}" == */* ]]; then
    ORGANIZATION=$(echo "${ARG_INPUT}" | cut -d'/' -f1)
    PROJECT_NAME=$(echo "${ARG_INPUT}" | cut -d'/' -f2)
else
    echo -e "${RED}Error: Invalid format. Please use <organization/project-name>.${NC}"
    echo -e "Usage: ./kdc_get_kubeconfig.sh <organization/project-name>"
    echo -e "Example: ./kdc_get_kubeconfig.sh my-org/my-project"
    exit 1
fi

# Validate that ORGANIZATION and PROJECT_NAME are not empty after parsing
if [ -z "${ORGANIZATION}" ] || [ -z "${PROJECT_NAME}" ]; then
    echo -e "${RED}Error: Organization or Project name cannot be empty.${NC}"
    echo -e "Usage: ./kdc_get_kubeconfig.sh <organization/project-name>"
    echo -e "Example: ./kdc_get_kubeconfig.sh my-org/my-project"
    exit 1
fi

export ORGANIZATION # Export it so subsequent calls and scripts can see it as set
echo -e "Using Organization: ${GREEN}${ORGANIZATION}${NC}"
echo -e "Using Project: ${GREEN}${PROJECT_NAME}${NC}"

# Base directories
BASE_DIR=~/.kube-dc/${ORGANIZATION}-${PROJECT_NAME}
SCRIPTS_DIR=${BASE_DIR}/scripts
CONFIG_DIR=${BASE_DIR}

# Console colors
RED='\033[0;31m'
GREEN='\033[0;32m'
YELLOW='\033[0;33m'
BLUE='\033[0;34m'
NC='\033[0m' # No Color

# Banner
echo -e "${BLUE}==========================================${NC}"
echo -e "${GREEN}Kube-DC Kubeconfig Setup Utility${NC}"
echo -e "${BLUE}==========================================${NC}"
echo -e "Setting up for project: ${YELLOW}${PROJECT_NAME}${NC}\n"

# Check and prompt for required environment variables
# Function to safely update environment variables in .env file
safe_update_env() {
    local var_name=$1
    local var_value=$2
    
    # Create .env file if it doesn't exist
    if [ ! -f "${CONFIG_DIR}/.env" ]; then
        touch "${CONFIG_DIR}/.env"
        chmod 600 "${CONFIG_DIR}/.env"
    fi
    
    # Remove any existing entries for this variable
    if [ -f "${CONFIG_DIR}/.env" ]; then
        # Different approach for macOS (BSD) vs Linux (GNU)
        if [[ "$(uname)" == "Darwin" ]]; then
            # macOS/BSD version
            sed -i "" "/^export ${var_name}=/d" "${CONFIG_DIR}/.env" 2>/dev/null || true
        else
            # Linux/GNU version
            sed -i "/^export ${var_name}=/d" "${CONFIG_DIR}/.env" 2>/dev/null || true
        fi
    fi
    
    # Add the new value
    echo "export ${var_name}=\"${var_value}\"" >> "${CONFIG_DIR}/.env"
    echo -e "${var_name} saved to config: ${GREEN}${var_value}${NC}"
}

check_env_var() {
    local var_name=$1
    local var_description=$2
    local var_default=$3
    local var_value

    # Check if variable is already exported
    if [ -z "${!var_name}" ]; then
        # If not found, check if we have it saved before
        if [ -f "${CONFIG_DIR}/.env" ] && grep -q "^export ${var_name}=" "${CONFIG_DIR}/.env"; then
            var_value=$(grep "^export ${var_name}=" "${CONFIG_DIR}/.env" | cut -d '=' -f2 | tr -d '"')
            echo -e "${var_name} found in config: ${YELLOW}${var_value}${NC}"
            read -p "Use saved ${var_description} [${var_value}]? (Y/n): " use_saved
            if [[ -z "$use_saved" || "$use_saved" =~ ^[Yy] ]]; then
                export "${var_name}"="${var_value}"
                # No need to update .env as it already has this value
                return
            fi
        fi

        # Prompt for the value
        echo -e "${YELLOW}${var_name}${NC} is not set."
        if [ -n "$var_default" ]; then
            read -p "Enter ${var_description} [${var_default}]: " var_value
            var_value=${var_value:-$var_default}
        else
            read -p "Enter ${var_description}: " var_value
        fi

        # Directly set the variable without displaying it again
        export "${var_name}"="${var_value}"
        # Save to .env file using the safe update function
        safe_update_env "${var_name}" "${var_value}"
    else
        echo -e "${var_name} already set: ${GREEN}${!var_name}${NC}"
        # Also save existing environment variables
        safe_update_env "${var_name}" "${!var_name}"
    fi
}

# Create directory structure
create_directories() {
    echo -e "\n${BLUE}Creating directory structure...${NC}"
    mkdir -p "${SCRIPTS_DIR}"
    mkdir -p "${CONFIG_DIR}"
    mkdir -p "${HOME}/.kube-dc/bin"
    chmod 700 "${BASE_DIR}"
    echo -e "${GREEN}Directories created.${NC}"
}

# Create refresh token script
create_refresh_token_script() {
    echo -e "\n${BLUE}Creating token refresh script...${NC}"
    
    # Fix for the EOF issue - use a different delimiter for the heredoc
    cat > "${SCRIPTS_DIR}/refresh_token.sh" << 'EOFSCRIPT'
#!/bin/bash

# Load environment variables
CONFIG_DIR=$(dirname "$(dirname "$0")")
source "${CONFIG_DIR}/.env"

REFRESH_TOKEN_FILE="${CONFIG_DIR}/.refresh_token"
# Cache file location
CACHE_FILE="${CONFIG_DIR}/.token_cache"
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
    TOKEN_RESPONSE=$(curl -s -X POST "${KEYCLOAK_ENDPOINT}/realms/${ORGANIZATION}/protocol/openid-connect/token" \
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
        TOKEN_RESPONSE=$(curl -s -X POST "${KEYCLOAK_ENDPOINT}/realms/${ORGANIZATION}/protocol/openid-connect/token" \
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
cat <<TOKENOUTPUT
{
  "apiVersion": "client.authentication.k8s.io/v1",
  "kind": "ExecCredential",
  "status": {
    "token": "$ACCESS_TOKEN"
  }
}
TOKENOUTPUT
EOFSCRIPT
    chmod +x "${SCRIPTS_DIR}/refresh_token.sh"
    echo -e "${GREEN}Token refresh script created.${NC}"
}

# Create kubeconfig
create_kubeconfig() {
    echo -e "\n${BLUE}Creating kubeconfig...${NC}"
    
    # Use BASE64_ENCODED_CA_CERT if available
    local ca_cert_data=""
    if [ -n "${BASE64_ENCODED_CA_CERT}" ]; then
        ca_cert_data="${BASE64_ENCODED_CA_CERT}"
    elif [ -n "${CA_CERT_FILE}" ] && [ -f "${CA_CERT_FILE}" ]; then
        ca_cert_data=$(base64 -w 0 "${CA_CERT_FILE}")
    else
        echo -e "${YELLOW}Warning: No CA certificate data provided. Using insecure-skip-tls-verify.${NC}"
    fi
    
    # Create kubeconfig
    cat > "${CONFIG_DIR}/kubeconfig" << EOF
apiVersion: v1
kind: Config
clusters:
- name: ${CLUSTER_NAME}
  cluster:
EOF

    if [ -n "${ca_cert_data}" ]; then
        cat >> "${CONFIG_DIR}/kubeconfig" << EOF
    server: ${API_SERVER}
    certificate-authority-data: ${ca_cert_data}
EOF
    else
        cat >> "${CONFIG_DIR}/kubeconfig" << EOF
    server: ${API_SERVER}
    insecure-skip-tls-verify: true
EOF
    fi

    # Generate namespace from organization and project name
    NAMESPACE="${ORGANIZATION}-${PROJECT_NAME}"
    echo -e "${GREEN}Using namespace:${NC} ${NAMESPACE}"
    
    cat >> "${CONFIG_DIR}/kubeconfig" << EOF
users:
- name: ${USER_NAME}
  user:
    exec:
      apiVersion: client.authentication.k8s.io/v1
      command: ${SCRIPTS_DIR}/refresh_token.sh
      interactiveMode: IfAvailable
contexts:
- name: ${CONTEXT_NAME}
  context:
    cluster: ${CLUSTER_NAME}
    user: ${USER_NAME}
    namespace: ${NAMESPACE}
current-context: ${CONTEXT_NAME}
preferences:
  colors: true
EOF
    
    # Save the namespace to env file
    safe_update_env "NAMESPACE" "${NAMESPACE}"

    chmod 600 "${CONFIG_DIR}/kubeconfig"
    echo -e "${GREEN}Kubeconfig created at:${NC} ${CONFIG_DIR}/kubeconfig"
}

# Create activation script
create_activation_script() {
    echo -e "\n${BLUE}Creating activation script...${NC}"
    cat > "${BASE_DIR}/activate.sh" << EOF
#!/bin/bash

# Source this file to activate the kube-dc environment
# Usage: source ~/.kube-dc/${ORGANIZATION}-${PROJECT_NAME}/activate.sh

export KUBECONFIG=~/.kube-dc/${ORGANIZATION}-${PROJECT_NAME}/kubeconfig
source ~/.kube-dc/${ORGANIZATION}-${PROJECT_NAME}/.env

# Set up kn alias for namespace switching
alias kn="${HOME}/.kube-dc/bin/kn"

echo "Kube-DC environment for ${PROJECT_NAME} activated."
echo "Using namespace: ${NAMESPACE}"
echo "You can now use kubectl commands to interact with your cluster."
echo "For example: kubectl get pods"
echo "Use 'kn' to list and switch between available namespaces."
EOF
    chmod +x "${BASE_DIR}/activate.sh"
    echo -e "${GREEN}Activation script created.${NC}"
}

# Display required environment variables
show_required_env() {
    echo -e "\n${BLUE}Required environment variables:${NC}"
    echo -e "${YELLOW}export KEYCLOAK_ENDPOINT=\"https://login.dev.kube-dc.com\"${NC}"
    echo -e "${YELLOW}export ORGANIZATION=\"orgname\"${NC}"
    echo -e "${YELLOW}export API_SERVER=\"https://kube-api.dev.kube-dc.com:6443\"${NC}"
    echo -e "${YELLOW}export CLUSTER_NAME=\"kube-dc\"${NC}"
    echo -e "${YELLOW}export USER_NAME=\"admin\"${NC}"
    echo -e "${YELLOW}export CONTEXT_NAME=\"kube-dc\"${NC}"
    echo -e "${YELLOW}export BASE64_ENCODED_CA_CERT=\"LS0tLS1CRUdJTi...\" (optional)\n"
    echo -e "${BLUE}You can export these variables before running the script${NC}"
    echo -e "${BLUE}or enter them when prompted during script execution.${NC}\n"
}

# Debug info about environment variables
debug_env_info() {
    echo "=========================================="
    echo "Kube-DC Kubeconfig Setup Utility"
    echo "=========================================="
    echo "Setting up for project: ${PROJECT_NAME}"
    echo ""

    echo "Required environment variables:"
    echo "export KEYCLOAK_ENDPOINT=\"https://login.dev.kube-dc.com\""
    echo "export ORGANIZATION=\"orgname\""
    echo "export API_SERVER=\"https://kube-api.dev.kube-dc.com:6443\""
    echo "export CLUSTER_NAME=\"kube-dc\""
    echo "export USER_NAME=\"admin\""
    echo "export CONTEXT_NAME=\"kube-dc\""
    echo "export BASE64_ENCODED_CA_CERT=\"LS0tLS1CRUdJTi...\" (optional)"
    echo ""
    echo "You can export these variables before running the script"
    echo "or enter them when prompted during script execution."
    echo ""

    echo "Environment variables status:"
    if [ -n "${KEYCLOAK_ENDPOINT+x}" ]; then echo "  KEYCLOAK_ENDPOINT: [set]"; else echo "  KEYCLOAK_ENDPOINT: [not set]"; fi
    if [ -n "${ORGANIZATION+x}" ]; then echo "  ORGANIZATION: [set]"; else echo "  ORGANIZATION: [not set]"; fi
    if [ -n "${API_SERVER+x}" ]; then echo "  API_SERVER: [set]"; else echo "  API_SERVER: [not set]"; fi
    if [ -n "${CLUSTER_NAME+x}" ]; then echo "  CLUSTER_NAME: [set]"; else echo "  CLUSTER_NAME: [not set]"; fi
    if [ -n "${USER_NAME+x}" ]; then echo "  USER_NAME: [set]"; else echo "  USER_NAME: [not set]"; fi
    if [ -n "${CONTEXT_NAME+x}" ]; then echo "  CONTEXT_NAME: [set]"; else echo "  CONTEXT_NAME: [not set]"; fi
    if [ -n "${BASE64_ENCODED_CA_CERT+x}" ]; then echo "  BASE64_ENCODED_CA_CERT: [set]"; else echo "  BASE64_ENCODED_CA_CERT: [not set]"; fi
    echo ""
}

# Main function
# Install the kn script for namespace switching
install_kn_script() {
    echo -e "\n${BLUE}Installing namespace switcher...${NC}"
    
    # Check if the kn script exists in the same directory as this script
    # Use a cross-platform approach for getting the script directory
    local script_path="$0"
    if [[ "$script_path" != /* ]]; then
        # If not an absolute path, prepend current directory
        script_path="$(pwd)/$script_path"
    fi
    local script_dir=$(dirname "$script_path")
    local kn_source="${script_dir}/kn"
    
    mkdir -p "${HOME}/.kube-dc/bin"
    
    # First try to copy from the same directory if it exists
    if [ -f "$kn_source" ]; then
        echo -e "${GREEN}Found kn script in the same directory.${NC}"
        cp "$kn_source" "${HOME}/.kube-dc/bin/kn"
    else
        # If not found locally, download from GitHub
        echo -e "${YELLOW}Downloading kn script from GitHub...${NC}"
        if command -v curl &>/dev/null; then
            curl -s -o "${HOME}/.kube-dc/bin/kn" https://raw.githubusercontent.com/shalb/kube-dc/main/hack/auth/kn
        elif command -v wget &>/dev/null; then
            wget -q -O "${HOME}/.kube-dc/bin/kn" https://raw.githubusercontent.com/shalb/kube-dc/main/hack/auth/kn
        else
            echo -e "${RED}Error: Neither curl nor wget found. Cannot download kn script.${NC}"
            echo -e "${YELLOW}Please install the kn script manually by copying it from the GitHub repository.${NC}"
            return 1
        fi
    fi
    
    # Make the script executable
    chmod +x "${HOME}/.kube-dc/bin/kn"
    echo -e "${GREEN}Namespace switcher installed to ${HOME}/.kube-dc/bin/kn${NC}"
}

# Function to get available namespaces from JWT claims
get_available_namespaces() {
    local token=$1
    local jwt_payload=$(decode_jwt "$token")
    
    # Add debug output if DEBUG=1 is set
    if [ "${DEBUG:-0}" = "1" ]; then
        echo "DEBUG: JWT payload structure:" >&2
        echo "$jwt_payload" | jq . >&2
    fi
    
    # Look for project roles with namespace prefix
    local roles_found=false
    local namespaces=""
    
    # If the token didn't decode properly or jq isn't available, show a warning
    if ! command -v jq &>/dev/null; then
        echo "WARNING: jq is not installed. Cannot parse JWT token." >&2
        return 1
    fi
    
    # First try with the standard format
    if namespaces=$(echo "$jwt_payload" | jq -r '.resource_access["kube-dc"].roles[]? // empty' 2>/dev/null | grep -o "^namespace:.*" 2>/dev/null | sed 's/^namespace://'); then
        if [ -n "$namespaces" ]; then
            roles_found=true
        fi
    fi
    
    # If that failed, try with realm_access
    if [ "$roles_found" = "false" ]; then
        if namespaces=$(echo "$jwt_payload" | jq -r '.realm_access.roles[]? // empty' 2>/dev/null | grep -o "^namespace:.*" 2>/dev/null | sed 's/^namespace://'); then
            if [ -n "$namespaces" ]; then
                roles_found=true
            fi
        fi
    fi
    
    # Last resort: try to find anything that looks like a namespace
    if [ "$roles_found" = "false" ]; then
        if [ "${DEBUG:-0}" = "1" ]; then
            echo "DEBUG: Trying to extract any role that looks like a namespace" >&2
        fi
        
        # Try to get all roles from different locations
        local all_roles=""
        all_roles+=$(echo "$jwt_payload" | jq -r '.resource_access[]?.roles[]? // empty' 2>/dev/null)$'\n'
        all_roles+=$(echo "$jwt_payload" | jq -r '.realm_access.roles[]? // empty' 2>/dev/null)$'\n'
        all_roles+=$(echo "$jwt_payload" | jq -r '.groups[]? // empty' 2>/dev/null)$'\n'
        
        # For debugging, print all roles if available
        if [ "${DEBUG:-0}" = "1" ] && [ -n "$all_roles" ]; then
            echo "DEBUG: All roles found:" >&2
            echo "$all_roles" >&2
        fi
        
        # Filter for valid kubernetes namespace names (lowercase, numbers, dashes)
        namespaces=$(echo "$all_roles" | grep -E '^[a-z0-9]([-a-z0-9]*[a-z0-9])?$' 2>/dev/null || echo "")
    fi
    
    # If we couldn't find any namespaces, use the current namespace
    if [ -z "$namespaces" ]; then
        current_ns=$(kubectl config view --minify --output 'jsonpath={..namespace}' 2>/dev/null)
        if [ -n "$current_ns" ]; then
            echo "$current_ns"
        fi
    else
        echo "$namespaces"
    fi
}

# Function to decode JWT payload
decode_jwt() {
    local token=$1
    local payload=$(echo "$token" | cut -d '.' -f 2)
    
    # Try to decode with both standard base64 and macOS base64
    echo "$payload" | base64 --decode 2>/dev/null || echo "$payload" | base64 -D 2>/dev/null || echo "$payload"
}


main() {
    # Show required environment variables
    debug_env_info
    
    create_directories
    
    # Create .env file if it doesn't exist and ensure ORGANIZATION is saved
    if [ ! -f "${CONFIG_DIR}/.env" ]; then
        touch "${CONFIG_DIR}/.env"
        chmod 600 "${CONFIG_DIR}/.env"
    fi
    
    # Always ensure ORGANIZATION is saved in the project's .env file
    safe_update_env "ORGANIZATION" "${ORGANIZATION}"

    # Check or prompt for environment variables
    check_env_var "KEYCLOAK_ENDPOINT" "Keycloak endpoint URL" "https://keycloak.example.com"
    check_env_var "ORGANIZATION" "Keycloak realm" "orgname"
    # Client ID is always kube-dc for this project
export CLIENT_ID="kube-dc"
echo -e "CLIENT_ID set to: ${GREEN}${CLIENT_ID}${NC}"
safe_update_env "CLIENT_ID" "${CLIENT_ID}"
    check_env_var "API_SERVER" "Kubernetes API server URL" "https://kube-api.example.com:6443"
    check_env_var "CLUSTER_NAME" "Kubernetes cluster name" "kube-dc"
    check_env_var "USER_NAME" "Kubernetes user name" "keycloak-user"
    check_env_var "CONTEXT_NAME" "Kubernetes context name" "kube-dc"
    # Check for CA certificate - either from file, base64 encoded data, or direct input
if [ -z "${BASE64_ENCODED_CA_CERT}" ]; then
    echo -e "\n${BLUE}CA Certificate Options:${NC}"
    echo -e "1) Enter base64 encoded certificate directly"
    echo -e "2) Provide path to certificate file"
    echo -e "3) Skip certificate (use insecure connection)"
    read -p "Select option [1-3]: " cert_option
    
    case "$cert_option" in
        1)
            echo -e "\n${YELLOW}Paste your base64 encoded certificate below${NC}"
            echo -e "${YELLOW}(The certificate will be captured automatically, press Enter after pasting)${NC}"
            read -r certificate_data
            BASE64_ENCODED_CA_CERT="$certificate_data"
            safe_update_env "BASE64_ENCODED_CA_CERT" "${BASE64_ENCODED_CA_CERT}"
            echo -e "${GREEN}Certificate data saved.${NC}"
            ;;
        2)
            check_env_var "CA_CERT_FILE" "Path to cluster CA certificate file" ""
            if [ -n "${CA_CERT_FILE}" ] && [ -f "${CA_CERT_FILE}" ]; then
                export BASE64_ENCODED_CA_CERT=$(base64 -w 0 "${CA_CERT_FILE}")
                safe_update_env "BASE64_ENCODED_CA_CERT" "${BASE64_ENCODED_CA_CERT}"
                echo -e "${GREEN}Certificate encoded and saved.${NC}"
            else
                echo -e "${YELLOW}Invalid certificate file path. Using insecure connection.${NC}"
            fi
            ;;
        *)
            echo -e "${YELLOW}Skipping certificate. Using insecure connection.${NC}"
            ;;
    esac
else
    echo -e "BASE64_ENCODED_CA_CERT is set: ${GREEN}Using encoded certificate${NC}"
fi
    
    # Create scripts and config files
    create_refresh_token_script
    create_kubeconfig
    create_activation_script
    install_kn_script
    
    echo -e "\n${GREEN}Setup completed successfully!${NC}"
    echo -e "To use your new kubeconfig, run:"
    echo -e "${YELLOW}source ~/.kube-dc/${ORGANIZATION}-${PROJECT_NAME}/activate.sh${NC}"
    echo -e "\nThis will set KUBECONFIG to point to your new configuration."
    echo -e "When you first run a kubectl command, you may be prompted for your credentials."
    echo -e "\nUse the ${YELLOW}kn${NC} command to view and select from available namespaces."
    
    if [ -n "${BASE64_ENCODED_CA_CERT}" ]; then
        echo -e "\n${GREEN}Your connection is secure with the provided certificate.${NC}"
    else
        echo -e "\n${YELLOW}Warning: You are using an insecure connection.${NC}"
        echo -e "Consider adding a certificate next time for better security."
    fi
}

# Run the main function
main
