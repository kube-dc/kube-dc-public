#!/usr/bin/env bash
#
# bootstrap-stripe.sh — Create Stripe products, prices, webhook endpoint,
# and a Kubernetes Secret from the billing-plans ConfigMap.
#
# Prerequisites:
#   - curl, jq, kubectl
#   - A Stripe restricted API key with Write access to:
#     Products, Prices, Customers, Checkout Sessions, Subscriptions, Customer Portal, Webhook Endpoints
#
# Usage:
#   ./hack/bootstrap-stripe.sh --key <stripe-secret-key> [--namespace kube-dc] [--webhook-url <url>] [--dry-run]
#
set -euo pipefail

# ── Defaults ──────────────────────────────────────────────────────────────────
NAMESPACE="kube-dc"
CONFIGMAP_NAME="billing-plans"
SECRET_NAME="stripe-billing"
WEBHOOK_URL=""
DRY_RUN=false
STRIPE_KEY=""
STRIPE_KEY_FILE=""
STRIPE_API="https://api.stripe.com/v1"

# ── Parse arguments ───────────────────────────────────────────────────────────
usage() {
    cat <<EOF
Usage: $0 --key <stripe-key> | --key-file <path> [OPTIONS]

Required (one of):
  --key <key>           Stripe secret/restricted API key
  --key-file <path>     File containing the Stripe API key

Options:
  --namespace <ns>      Kubernetes namespace (default: kube-dc)
  --webhook-url <url>   Stripe webhook URL (e.g. https://console.stage.kube-dc.com/api/billing/webhook)
  --dry-run             Print what would be created without calling Stripe
  -h, --help            Show this help
EOF
    exit 1
}

while [[ $# -gt 0 ]]; do
    case $1 in
        --key)        STRIPE_KEY="$2"; shift 2 ;;
        --key-file)   STRIPE_KEY_FILE="$2"; shift 2 ;;
        --namespace)  NAMESPACE="$2"; shift 2 ;;
        --webhook-url) WEBHOOK_URL="$2"; shift 2 ;;
        --dry-run)    DRY_RUN=true; shift ;;
        -h|--help)    usage ;;
        *)            echo "Unknown option: $1"; usage ;;
    esac
done

# Read key from file if provided
if [[ -n "$STRIPE_KEY_FILE" && -z "$STRIPE_KEY" ]]; then
    STRIPE_KEY="$(tr -d '[:space:]' < "$STRIPE_KEY_FILE")"
fi

if [[ -z "$STRIPE_KEY" ]]; then
    echo "ERROR: --key or --key-file is required"
    usage
fi

# ── Dependency checks ─────────────────────────────────────────────────────────
for cmd in curl jq kubectl; do
    if ! command -v "$cmd" &>/dev/null; then
        echo "ERROR: $cmd is required but not found in PATH"
        exit 1
    fi
done

# ── Helper: Stripe API call ──────────────────────────────────────────────────
stripe_post() {
    local endpoint="$1"; shift
    local response
    response=$(curl -s -w "\n%{http_code}" -X POST "${STRIPE_API}${endpoint}" \
        -u "${STRIPE_KEY}:" \
        "$@")
    local http_code
    http_code=$(echo "$response" | tail -1)
    local body
    body=$(echo "$response" | sed '$d')

    if [[ "$http_code" -ge 400 ]]; then
        echo "ERROR: Stripe API ${endpoint} returned HTTP ${http_code}:" >&2
        echo "$body" | jq -r '.error.message // .error // .' >&2
        return 1
    fi
    echo "$body"
}

stripe_get() {
    local endpoint="$1"; shift
    curl -s -X GET "${STRIPE_API}${endpoint}" -u "${STRIPE_KEY}:" "$@"
}

# ── Read billing-plans ConfigMap ──────────────────────────────────────────────
echo "==> Reading ConfigMap ${CONFIGMAP_NAME} from namespace ${NAMESPACE}..."
CM_DATA=$(kubectl get configmap "$CONFIGMAP_NAME" -n "$NAMESPACE" -o jsonpath='{.data.plans\.yaml}')
if [[ -z "$CM_DATA" ]]; then
    echo "ERROR: ConfigMap ${CONFIGMAP_NAME} not found or empty in namespace ${NAMESPACE}"
    exit 1
fi

# ── Parse plans from ConfigMap ────────────────────────────────────────────────
echo "==> Parsing plans and addons..."

# Extract plan IDs
PLAN_IDS=$(echo "$CM_DATA" | python3 -c "
import sys, json
try:
    import yaml
except ImportError:
    print('ERROR: python3 pyyaml required. Install with: pip3 install pyyaml', file=sys.stderr)
    sys.exit(1)
data = yaml.safe_load(sys.stdin.read())
plans = data.get('plans', {})
addons = data.get('addons', {})
result = {'plans': {}, 'addons': {}}
for pid, pdata in plans.items():
    result['plans'][pid] = {
        'displayName': pdata.get('displayName', pid),
        'description': pdata.get('description', ''),
        'price': int(pdata.get('price', 0)),
        'currency': pdata.get('currency', 'EUR').lower(),
    }
for aid, adata in addons.items():
    result['addons'][aid] = {
        'displayName': adata.get('displayName', aid),
        'description': adata.get('description', ''),
        'price': int(adata.get('price', 0)),
        'currency': adata.get('currency', 'EUR').lower(),
    }
print(json.dumps(result))
")

echo "$PLAN_IDS" | jq -r '
    "Plans:",
    (.plans | to_entries[] | "  - \(.key): \(.value.displayName) — \(.value.price) \(.value.currency | ascii_upcase)/mo"),
    "Addons:",
    (.addons | to_entries[] | "  - \(.key): \(.value.displayName) — \(.value.price) \(.value.currency | ascii_upcase)/mo")
'

if $DRY_RUN; then
    echo ""
    echo "==> DRY RUN: Would create the above products and prices in Stripe."
    echo "    Would create K8s Secret '${SECRET_NAME}' in namespace '${NAMESPACE}'."
    exit 0
fi

# ── Verify Stripe key works ──────────────────────────────────────────────────
echo ""
echo "==> Verifying Stripe API key..."
ACCOUNT_INFO=$(stripe_get "/account" 2>/dev/null || true)
ACCOUNT_NAME=$(echo "$ACCOUNT_INFO" | jq -r '.settings.dashboard.display_name // .business_profile.name // .id // "unknown"' 2>/dev/null || echo "unknown")
echo "    Connected to Stripe account: ${ACCOUNT_NAME}"

# ── Create products and prices ────────────────────────────────────────────────
declare -A PRICE_IDS

create_product_and_price() {
    local id="$1"
    local name="$2"
    local description="$3"
    local price_cents="$4"
    local currency="$5"
    local env_key="$6"

    echo ""
    echo "--- Creating product: ${name} (${id})..."

    # Check if product already exists by listing all and filtering
    local all_products product_id=""
    all_products=$(stripe_get "/products?limit=100&active=true")
    product_id=$(echo "$all_products" | jq -r --arg kid "$id" '.data[] | select(.metadata.kube_dc_id == $kid) | .id' 2>/dev/null | head -1)

    if [[ -n "$product_id" ]]; then
        echo "    Product already exists: ${product_id}"
    else
        local product
        product=$(stripe_post "/products" \
            -d "name=${name}" \
            -d "description=${description}" \
            -d "metadata[kube_dc_id]=${id}")
        product_id=$(echo "$product" | jq -r '.id')
        echo "    Created product: ${product_id}"
    fi

    # Check if a recurring price already exists for this product
    local existing_prices
    existing_prices=$(stripe_get "/prices?product=${product_id}&type=recurring&active=true&limit=5")
    local existing_price_count
    existing_price_count=$(echo "$existing_prices" | jq -r '.data | length' 2>/dev/null || echo "0")

    local price_id
    if [[ "$existing_price_count" -gt 0 ]]; then
        price_id=$(echo "$existing_prices" | jq -r '.data[0].id')
        echo "    Price already exists: ${price_id}"
    else
        local price
        price=$(stripe_post "/prices" \
            -d "product=${product_id}" \
            -d "unit_amount=${price_cents}" \
            -d "currency=${currency}" \
            -d "recurring[interval]=month" \
            -d "metadata[kube_dc_id]=${id}")
        price_id=$(echo "$price" | jq -r '.id')
        echo "    Created price: ${price_id} (${price_cents} cents/${currency}/mo)"
    fi

    PRICE_IDS["$env_key"]="$price_id"
}

# Create plan products
for plan_id in $(echo "$PLAN_IDS" | jq -r '.plans | keys[]'); do
    name=$(echo "$PLAN_IDS" | jq -r ".plans[\"${plan_id}\"].displayName")
    desc=$(echo "$PLAN_IDS" | jq -r ".plans[\"${plan_id}\"].description")
    price=$(echo "$PLAN_IDS" | jq -r ".plans[\"${plan_id}\"].price")
    currency=$(echo "$PLAN_IDS" | jq -r ".plans[\"${plan_id}\"].currency")
    price_cents=$((price * 100))

    # Convert plan-id to ENV key: dev-pool → STRIPE_PRICE_DEV_POOL
    env_key="STRIPE_PRICE_$(echo "$plan_id" | tr '[:lower:]-' '[:upper:]_')"

    create_product_and_price "$plan_id" "$name" "$desc" "$price_cents" "$currency" "$env_key"
done

# Create addon products
for addon_id in $(echo "$PLAN_IDS" | jq -r '.addons | keys[]'); do
    name=$(echo "$PLAN_IDS" | jq -r ".addons[\"${addon_id}\"].displayName")
    desc=$(echo "$PLAN_IDS" | jq -r ".addons[\"${addon_id}\"].description")
    price=$(echo "$PLAN_IDS" | jq -r ".addons[\"${addon_id}\"].price")
    currency=$(echo "$PLAN_IDS" | jq -r ".addons[\"${addon_id}\"].currency")
    price_cents=$((price * 100))

    env_key="STRIPE_PRICE_$(echo "$addon_id" | tr '[:lower:]-' '[:upper:]_')"

    create_product_and_price "$addon_id" "$name" "$desc" "$price_cents" "$currency" "$env_key"
done

# ── Create webhook endpoint (optional) ────────────────────────────────────────
WEBHOOK_SECRET=""
if [[ -n "$WEBHOOK_URL" ]]; then
    echo ""
    echo "==> Creating Stripe webhook endpoint: ${WEBHOOK_URL}..."

    # Check if webhook already exists
    existing_webhooks=$(stripe_get "/webhook_endpoints?limit=100")
    existing_wh_id=$(echo "$existing_webhooks" | jq -r --arg url "$WEBHOOK_URL" '.data[] | select(.url == $url) | .id' 2>/dev/null | head -1)

    if [[ -n "$existing_wh_id" ]]; then
        echo "    Webhook already exists: ${existing_wh_id}"
        echo "    WARNING: Cannot retrieve existing webhook secret. You may need to re-create it."
        echo "    To re-create, delete the webhook in Stripe Dashboard and re-run this script."
    else
        webhook_result=$(stripe_post "/webhook_endpoints" \
            -d "url=${WEBHOOK_URL}" \
            -d "enabled_events[]=checkout.session.completed" \
            -d "enabled_events[]=customer.subscription.updated" \
            -d "enabled_events[]=customer.subscription.deleted" \
            -d "enabled_events[]=invoice.payment_failed" \
            -d "description=Kube-DC Billing Webhook")
        WEBHOOK_SECRET=$(echo "$webhook_result" | jq -r '.secret // empty')
        webhook_id=$(echo "$webhook_result" | jq -r '.id')
        echo "    Created webhook: ${webhook_id}"
        if [[ -n "$WEBHOOK_SECRET" ]]; then
            echo "    Webhook secret: ${WEBHOOK_SECRET}"
        else
            echo "    WARNING: Webhook secret not returned. Check Stripe Dashboard."
        fi
    fi
fi

# ── Summary ───────────────────────────────────────────────────────────────────
echo ""
echo "============================================================"
echo "  Stripe Bootstrap Complete"
echo "============================================================"
echo ""
echo "Price IDs:"
for key in $(echo "${!PRICE_IDS[@]}" | tr ' ' '\n' | sort); do
    echo "  ${key}=${PRICE_IDS[$key]}"
done
if [[ -n "$WEBHOOK_SECRET" ]]; then
    echo ""
    echo "Webhook Secret: ${WEBHOOK_SECRET}"
fi

# ── Create / update Kubernetes Secret ─────────────────────────────────────────
echo ""
echo "==> Creating Kubernetes Secret '${SECRET_NAME}' in namespace '${NAMESPACE}'..."

# Build the secret data
SECRET_ARGS=(
    "--from-literal=STRIPE_SECRET_KEY=${STRIPE_KEY}"
)
for key in $(echo "${!PRICE_IDS[@]}" | tr ' ' '\n' | sort); do
    SECRET_ARGS+=("--from-literal=${key}=${PRICE_IDS[$key]}")
done
if [[ -n "$WEBHOOK_SECRET" ]]; then
    SECRET_ARGS+=("--from-literal=STRIPE_WEBHOOK_SECRET=${WEBHOOK_SECRET}")
else
    echo "    NOTE: No webhook secret available. Add STRIPE_WEBHOOK_SECRET manually later."
    SECRET_ARGS+=("--from-literal=STRIPE_WEBHOOK_SECRET=whsec_PLACEHOLDER")
fi

# Delete existing secret if present, then create
kubectl delete secret "$SECRET_NAME" -n "$NAMESPACE" --ignore-not-found
kubectl create secret generic "$SECRET_NAME" -n "$NAMESPACE" "${SECRET_ARGS[@]}"
echo "    Secret '${SECRET_NAME}' created."

# ── Patch backend deployment ──────────────────────────────────────────────────
echo ""
echo "==> Patching backend deployment with Stripe env vars..."

# Build the env patch
ENV_PATCH='[{"name":"BILLING_PROVIDER","value":"stripe"}'
ENV_PATCH+=',{"name":"STRIPE_SECRET_KEY","valueFrom":{"secretKeyRef":{"name":"'"${SECRET_NAME}"'","key":"STRIPE_SECRET_KEY"}}}'
ENV_PATCH+=',{"name":"STRIPE_WEBHOOK_SECRET","valueFrom":{"secretKeyRef":{"name":"'"${SECRET_NAME}"'","key":"STRIPE_WEBHOOK_SECRET"}}}'
for key in $(echo "${!PRICE_IDS[@]}" | tr ' ' '\n' | sort); do
    ENV_PATCH+=',{"name":"'"${key}"'","valueFrom":{"secretKeyRef":{"name":"'"${SECRET_NAME}"'","key":"'"${key}"'"}}}'
done
ENV_PATCH+=']'

# Get current env vars from deployment (excluding any existing BILLING/STRIPE vars)
CURRENT_ENV=$(kubectl get deployment kube-dc-backend -n "$NAMESPACE" -o json | \
    jq '[.spec.template.spec.containers[0].env[] | select(.name | test("^(BILLING_PROVIDER|STRIPE_)") | not)]')

# Merge current + new
MERGED_ENV=$(echo "$CURRENT_ENV" "$ENV_PATCH" | jq -s 'add')

# Apply the patch
kubectl patch deployment kube-dc-backend -n "$NAMESPACE" --type=json \
    -p '[{"op":"replace","path":"/spec/template/spec/containers/0/env","value":'"${MERGED_ENV}"'}]'

echo "    Backend deployment patched. Pod will restart automatically."

# ── Wait for rollout ──────────────────────────────────────────────────────────
echo ""
echo "==> Waiting for backend rollout..."
kubectl rollout status deployment/kube-dc-backend -n "$NAMESPACE" --timeout=120s

echo ""
echo "============================================================"
echo "  Stripe integration is now ACTIVE"
echo "============================================================"
echo ""
echo "Next steps:"
echo "  1. Verify: curl -s https://console.stage.kube-dc.com/api/billing/config | jq"
echo "     → provider should be 'stripe', checkout/portal/addons should be true"
if [[ -z "$WEBHOOK_URL" ]]; then
    echo "  2. Set up a webhook in Stripe Dashboard:"
    echo "     URL: https://console.stage.kube-dc.com/api/billing/webhook"
    echo "     Events: checkout.session.completed, customer.subscription.updated,"
    echo "             customer.subscription.deleted, invoice.payment_failed"
    echo "     Then update the secret:"
    echo "     kubectl patch secret ${SECRET_NAME} -n ${NAMESPACE} -p '{\"stringData\":{\"STRIPE_WEBHOOK_SECRET\":\"whsec_...\"}}}'"
    echo "     kubectl rollout restart deployment/kube-dc-backend -n ${NAMESPACE}"
fi
echo ""
