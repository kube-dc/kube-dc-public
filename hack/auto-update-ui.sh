#!/bin/bash
set -e

# Stripe configuration (set these values or use environment variables)
STRIPE_SECRET_KEY="${STRIPE_SECRET_KEY:-}"
STRIPE_PRICE_DEV_POOL="${STRIPE_PRICE_DEV_POOL:-}"
STRIPE_PRICE_PRO_POOL="${STRIPE_PRICE_PRO_POOL:-}"
STRIPE_PRICE_SCALE_POOL="${STRIPE_PRICE_SCALE_POOL:-}"
STRIPE_PRICE_TURBO_X1="${STRIPE_PRICE_TURBO_X1:-}"
STRIPE_PRICE_TURBO_X2="${STRIPE_PRICE_TURBO_X2:-}"
STRIPE_WEBHOOK_SECRET="${STRIPE_WEBHOOK_SECRET:-}"
CONSOLE_URL="${CONSOLE_URL:-}"

increment_version() {
  input_version=$1
  if [[ $input_version =~ ^(v[0-9]+\.[0-9]+\.[0-9]+)(-dev([0-9]+))?$ ]]; then
    base_version=${BASH_REMATCH[1]}
    dev_suffix=${BASH_REMATCH[3]}

    if [[ -n $dev_suffix ]]; then
      # If a dev suffix exists, increment the number
      new_dev_suffix=$((dev_suffix + 1))
      echo "${base_version}-dev${new_dev_suffix}"
    else
      # If no dev suffix, add "-dev1"
      echo "${base_version}-dev1"
    fi
  else
    echo "Invalid version format" > /dev/stderr
    exit 1
  fi
}

NAMESPACE="kube-dc"

if ! command -v kubectl 2>&1 >/dev/null
then
    echo "Error: command 'kubectl' could not be found. Auto update is not possible" > /dev/stderr
    exit 1
fi

KUBE_DC_VERSION=$(kubectl get deployment -n ${NAMESPACE} kube-dc-backend -o jsonpath='{.spec.template.spec.containers[0].image}' | awk -F: '{print $2}')
# KUBE_DC_VERSION=$(helm get metadata -n ${NAMESPACE} kube-dc | grep APP_VERSION | awk '{print $2}')

if [ -z ${KUBE_DC_VERSION+x} ]; then 
  echo "deployed release 'kube-dc' could not be found. Auto update is not possible" > /dev/stderr
  exit 1 
fi

export KUBE_DC_VERSION
export REGISTRY_URL=registry-1.docker.io
export REGISTRY_REPO=shalb

if [ -z ${KUBE_DC_VERSION+x} ]; then 
  echo "can't set KUBE_DC_VERSION automatically" > /dev/stderr
  exit 1
fi

new_version=$(increment_version "${KUBE_DC_VERSION}")


# Print the generated version and prompt for confirmation
echo -e "New version tag: \033[1;92m${new_version}\033[0m"
read -p "Do you want to build and update with this version? (y/n): " user_input

# Act based on user input
if [[ ! ${user_input} == "y" && ! ${user_input} == "Y" ]]; then
  echo "Operation canceled."
  exit 0
fi
echo "Proceeding with version: ${new_version}"

path=$(dirname -- "$( readlink -f -- "$0"; )")

frontendPath=$(cd -- "${path}/../ui/frontend" &> /dev/null && pwd) 
backendPath=$(cd -- "${path}/../ui/backend" &> /dev/null && pwd)

# Generate build info file for frontend
echo "Generating build info file..."
CURRENT_BRANCH=$(git rev-parse --abbrev-ref HEAD)
LAST_COMMITS=$(git log -5 --pretty=format:'    "%h - %s (%an, %ar)"' | sed 's/$/,/' | sed '$ s/,$//')
cat > "${frontendPath}/src/build-info.js" << EOF
// Auto-generated build information
// Generated on: $(date -u +"%Y-%m-%dT%H:%M:%SZ")
window.BUILD_INFO = {
  version: "${new_version}",
  buildTime: "$(date -u +"%Y-%m-%dT%H:%M:%SZ")",
  previousVersion: "${KUBE_DC_VERSION}",
  registry: "${REGISTRY_URL}/${REGISTRY_REPO}",
  branch: "${CURRENT_BRANCH}",
  lastCommits: [
${LAST_COMMITS}
  ]
};
EOF

cd "${backendPath}"
docker build -t ${REGISTRY_REPO}/kube-dc-ui-backend:${new_version} .
docker push ${REGISTRY_REPO}/kube-dc-ui-backend:${new_version}
kubectl set image -n ${NAMESPACE} deployment/kube-dc-backend kube-dc=${REGISTRY_REPO}/kube-dc-ui-backend:${new_version}

cd "${frontendPath}"
docker build -t ${REGISTRY_REPO}/kube-dc-ui-frontend:${new_version} .
docker push ${REGISTRY_REPO}/kube-dc-ui-frontend:${new_version}
kubectl set image -n ${NAMESPACE} deployment/kube-dc-frontend kube-dc=${REGISTRY_REPO}/kube-dc-ui-frontend:${new_version}

# Create/update Stripe secret and patch deployment if credentials are provided
if [[ -n "${STRIPE_SECRET_KEY}" ]]; then
  # Default console URL if not provided
  if [[ -z "${CONSOLE_URL}" ]]; then
    echo "Warning: CONSOLE_URL not set. Using default. Set CONSOLE_URL to your frontend URL for Stripe redirects."
    CONSOLE_URL="https://console.kube-dc.com"
  fi

  echo "Creating/updating Stripe secret..."
  kubectl create secret generic kube-dc-backend-stripe \
    --namespace ${NAMESPACE} \
    --from-literal=secret-key="${STRIPE_SECRET_KEY}" \
    --from-literal=webhook-secret="${STRIPE_WEBHOOK_SECRET}" \
    --from-literal=price-dev-pool="${STRIPE_PRICE_DEV_POOL}" \
    --from-literal=price-pro-pool="${STRIPE_PRICE_PRO_POOL}" \
    --from-literal=price-scale-pool="${STRIPE_PRICE_SCALE_POOL}" \
    --from-literal=price-turbo-x1="${STRIPE_PRICE_TURBO_X1}" \
    --from-literal=price-turbo-x2="${STRIPE_PRICE_TURBO_X2}" \
    --from-literal=console-url="${CONSOLE_URL}" \
    --dry-run=client -o yaml | kubectl apply -f -
  
  # Check if STRIPE_SECRET_KEY env var already exists in deployment
  if ! kubectl get deployment kube-dc-backend -n ${NAMESPACE} -o jsonpath='{.spec.template.spec.containers[0].env[*].name}' | grep -q "STRIPE_SECRET_KEY"; then
    echo "Adding Stripe environment variables to backend deployment..."
    kubectl patch deployment kube-dc-backend -n ${NAMESPACE} --type='json' -p='[
      {"op": "add", "path": "/spec/template/spec/containers/0/env/-", "value": {"name": "STRIPE_SECRET_KEY", "valueFrom": {"secretKeyRef": {"name": "kube-dc-backend-stripe", "key": "secret-key"}}}},
      {"op": "add", "path": "/spec/template/spec/containers/0/env/-", "value": {"name": "STRIPE_WEBHOOK_SECRET", "valueFrom": {"secretKeyRef": {"name": "kube-dc-backend-stripe", "key": "webhook-secret"}}}},
      {"op": "add", "path": "/spec/template/spec/containers/0/env/-", "value": {"name": "STRIPE_PRICE_DEV_POOL", "valueFrom": {"secretKeyRef": {"name": "kube-dc-backend-stripe", "key": "price-dev-pool"}}}},
      {"op": "add", "path": "/spec/template/spec/containers/0/env/-", "value": {"name": "STRIPE_PRICE_PRO_POOL", "valueFrom": {"secretKeyRef": {"name": "kube-dc-backend-stripe", "key": "price-pro-pool"}}}},
      {"op": "add", "path": "/spec/template/spec/containers/0/env/-", "value": {"name": "STRIPE_PRICE_SCALE_POOL", "valueFrom": {"secretKeyRef": {"name": "kube-dc-backend-stripe", "key": "price-scale-pool"}}}},
      {"op": "add", "path": "/spec/template/spec/containers/0/env/-", "value": {"name": "STRIPE_PRICE_TURBO_X1", "valueFrom": {"secretKeyRef": {"name": "kube-dc-backend-stripe", "key": "price-turbo-x1"}}}},
      {"op": "add", "path": "/spec/template/spec/containers/0/env/-", "value": {"name": "STRIPE_PRICE_TURBO_X2", "valueFrom": {"secretKeyRef": {"name": "kube-dc-backend-stripe", "key": "price-turbo-x2"}}}},
      {"op": "add", "path": "/spec/template/spec/containers/0/env/-", "value": {"name": "CONSOLE_URL", "valueFrom": {"secretKeyRef": {"name": "kube-dc-backend-stripe", "key": "console-url"}}}}
    ]'
  else
    echo "Stripe environment variables already configured. Restarting backend deployment..."
    kubectl rollout restart -n ${NAMESPACE} deployment/kube-dc-backend
  fi
  echo "Stripe integration complete."
fi




