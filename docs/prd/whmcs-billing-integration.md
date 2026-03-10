# PRD: WHMCS Billing Integration for Kube-DC

## 1. Overview

This document describes the integration of [WHMCS](https://www.whmcs.com/) as a billing provider for Kube-DC, enabling hosting providers and resellers to sell Kube-DC plans through WHMCS — the industry-standard web hosting billing and automation platform.

### 1.1 Goal

Enable WHMCS to manage the full subscription lifecycle (order, payment, suspension, termination, upgrade/downgrade) for Kube-DC organizations, while keeping kube-dc as a **standalone service** — no Plesk, cPanel, or VM-level integrations.

### 1.2 Design Principles

1. **Minimal kube-dc backend changes** — Only add a thin WHMCS provider module (`providers/whmcs.js`) following the existing Stripe pattern. All shared quota/plan logic stays untouched.
2. **WHMCS owns the billing UI** — Unlike Stripe (which uses Checkout + Customer Portal), WHMCS provides its own client area, cart, invoicing, and payment processing. The kube-dc console billing page works in read-only mode.
3. **All WHMCS-side code lives in `integration/whmcs/`** — A self-contained directory with the PHP provisioning module, hooks, setup scripts, and documentation. This is what gets deployed to the WHMCS server.
4. **Same annotation contract** — WHMCS integration writes the same `billing.kube-dc.com/*` annotations that Stripe does, so the Organization controller (Go) requires zero changes.

### 1.3 How Plesk/cPanel Integrate with WHMCS (Reference Pattern)

Understanding the existing WHMCS integration pattern with Plesk and cPanel clarifies our approach:

| Component | Plesk/cPanel | Kube-DC (Ours) |
|-----------|-------------|----------------|
| **Provisioning Module** | PHP file in `/modules/servers/plesk/` | PHP file in `/modules/servers/kubedc/` |
| **Server Config** | WHMCS admin → Servers → Plesk hostname + credentials | WHMCS admin → Servers → kube-dc API URL + API key |
| **Products** | WHMCS Products mapped to hosting plans | WHMCS Products mapped to billing plans (dev-pool, pro-pool, scale-pool) |
| **CreateAccount** | Calls Plesk XML API to create subscription | Calls kube-dc webhook API to activate plan on Organization |
| **SuspendAccount** | Calls Plesk API to suspend subscription | Calls kube-dc webhook API to suspend Organization |
| **TerminateAccount** | Calls Plesk API to delete subscription | Calls kube-dc webhook API to cancel Organization |
| **ChangePackage** | Calls Plesk API to change plan | Calls kube-dc webhook API to change plan |
| **Client Area** | Shows hosting details, SSO to Plesk panel | Shows kube-dc console link + resource usage |
| **Hooks** | AfterModuleCreate, AfterModuleSuspend, etc. | Same hooks for logging and notifications |

**Key insight:** The Plesk module does NOT modify Plesk's internal codebase — it makes HTTP API calls from WHMCS to Plesk. Our integration follows the identical pattern: the WHMCS module makes HTTP calls to kube-dc's webhook API endpoint.

---

## 2. Architecture

### 2.1 High-Level Flow

```
┌─────────────────────────────────────────────────────────────┐
│                        WHMCS Server                         │
│                                                             │
│  Client Area ─► Cart ─► Invoice ─► Payment ─► Module Call   │
│                                                             │
│  /modules/servers/kubedc/                                   │
│    kubedc.php  ─── CreateAccount()  ──┐                     │
│                    SuspendAccount()   │                     │
│                    UnsuspendAccount() ├── HTTP POST ──┐     │
│                    TerminateAccount() │               │     │
│                    ChangePackage()  ──┘               │     │
│    hooks.php                                          │     │
│    templates/clientarea.tpl                           │     │
└───────────────────────────────────────────────────────┼─────┘
                                                        │
                                                        ▼
┌───────────────────────────────────────────────────────────────┐
│                     Kube-DC Backend                           │
│                                                               │
│  routes/billing.js                                            │
│    └── if BILLING_PROVIDER === 'whmcs'                        │
│          └── providers/whmcs.js                               │
│                POST /api/billing/webhook ◄── HMAC verified    │
│                  │                                            │
│                  ├── action: activate   → updateOrgSubscription│
│                  ├── action: suspend    → updateOrgSubscription│
│                  ├── action: unsuspend  → updateOrgSubscription│
│                  ├── action: terminate  → updateOrgSubscription│
│                  └── action: change     → updateOrgSubscription│
│                                                               │
│  quotaController.js (UNCHANGED)                               │
│    getOrganizationSubscriptionData()                          │
│    updateOrganizationSubscription()                           │
└───────────────────────────────────────────────────────────────┘
                          │
                          ▼
┌───────────────────────────────────────────────────────────────┐
│                   Kubernetes Cluster                          │
│                                                               │
│  Organization CR annotations:                                 │
│    billing.kube-dc.com/subscription: active|suspended|...     │
│    billing.kube-dc.com/plan-id: pro-pool                      │
│    billing.kube-dc.com/provider-subscription-id: whmcs-svc-42 │
│    billing.kube-dc.com/provider-customer-id: whmcs-client-7   │
│                                                               │
│  Organization Controller (Go) — NO CHANGES                    │
│    └── Watches annotations → enforces HRQ, LimitRange, EIP   │
└───────────────────────────────────────────────────────────────┘
```

### 2.2 Sequence Diagram: New Order

```
Customer         WHMCS                  kubedc module          kube-dc API          Kubernetes
   │               │                        │                      │                    │
   │ Place Order   │                        │                      │                    │
   ├──────────────►│                        │                      │                    │
   │               │ Generate Invoice       │                      │                    │
   │               ├───────┐                │                      │                    │
   │               │       │                │                      │                    │
   │ Pay Invoice   │◄──────┘                │                      │                    │
   ├──────────────►│                        │                      │                    │
   │               │ CreateAccount()        │                      │                    │
   │               ├───────────────────────►│                      │                    │
   │               │                        │ POST /billing/webhook│                    │
   │               │                        │  action: activate    │                    │
   │               │                        │  org: "acme"         │                    │
   │               │                        │  plan: "pro-pool"    │                    │
   │               │                        ├─────────────────────►│                    │
   │               │                        │                      │ PATCH Organization  │
   │               │                        │                      │  annotations        │
   │               │                        │                      ├───────────────────►│
   │               │                        │                      │                    │
   │               │                        │     200 OK           │  HRQ + LimitRange  │
   │               │                        │◄─────────────────────┤  enforced          │
   │               │      success           │                      │                    │
   │               │◄───────────────────────┤                      │                    │
   │  Provisioned  │                        │                      │                    │
   │◄──────────────┤                        │                      │                    │
```

### 2.3 Sequence Diagram: Suspension (Overdue Invoice)

```
WHMCS Cron       WHMCS                  kubedc module          kube-dc API          Kubernetes
   │               │                        │                      │                    │
   │ Invoice       │                        │                      │                    │
   │ overdue       │                        │                      │                    │
   ├──────────────►│                        │                      │                    │
   │               │ SuspendAccount()       │                      │                    │
   │               ├───────────────────────►│                      │                    │
   │               │                        │ POST /billing/webhook│                    │
   │               │                        │  action: suspend     │                    │
   │               │                        ├─────────────────────►│                    │
   │               │                        │                      │ PATCH Organization  │
   │               │                        │                      │  subscription:      │
   │               │                        │                      │  "suspended"        │
   │               │                        │                      ├───────────────────►│
   │               │                        │     200 OK           │                    │
   │               │◄───────────────────────┤                      │                    │
```

---

## 3. Artifacts

### 3.1 WHMCS-Side Artifacts (`integration/whmcs/`)

These are deployed to the WHMCS server. They are NOT part of the kube-dc backend.

```
integration/whmcs/
├── README.md                           # Setup & deployment guide
├── modules/
│   └── servers/
│       └── kubedc/
│           ├── kubedc.php              # Main provisioning module
│           ├── hooks.php               # WHMCS hook handlers
│           ├── lib/
│           │   └── KubeDCApiClient.php # HTTP client for kube-dc API
│           ├── templates/
│           │   └── clientarea.tpl      # Client area output (console link, usage)
│           └── logo.png                # Module logo for WHMCS admin
├── setup/
│   ├── configure-products.php          # CLI script: create WHMCS products from plans
│   ├── configure-server.php            # CLI script: register kube-dc server
│   └── configure-addons.php            # CLI script: create configurable options for turbo addons
└── docs/
    ├── installation.md                 # Step-by-step WHMCS setup
    └── troubleshooting.md              # Common issues and solutions
```

#### 3.1.1 Provisioning Module (`kubedc.php`)

The core PHP file implementing WHMCS provisioning module functions:

| Function | When Called | What It Does |
|----------|-----------|-------------|
| `kubedc_MetaData()` | Module registration | Returns module display name, version, author |
| `kubedc_ConfigOptions()` | Product setup | Defines per-product settings (plan ID, organization field name) |
| `kubedc_TestConnection()` | Server setup | Tests connectivity to kube-dc webhook API |
| `kubedc_CreateAccount($params)` | Order paid / admin action | POST webhook `action: activate` with plan and org |
| `kubedc_SuspendAccount($params)` | Invoice overdue / admin action | POST webhook `action: suspend` |
| `kubedc_UnsuspendAccount($params)` | Overdue invoice paid / admin action | POST webhook `action: unsuspend` (restores plan) |
| `kubedc_TerminateAccount($params)` | Cancellation / admin action | POST webhook `action: terminate` |
| `kubedc_ChangePackage($params)` | Upgrade/downgrade paid | POST webhook `action: change` with new plan |
| `kubedc_Renew($params)` | Renewal invoice paid | POST webhook `action: renew` (no-op, keeps active) |
| `kubedc_AdminLink($params)` | Admin product page | Returns link to kube-dc admin console |
| `kubedc_ClientArea($params)` | Client product page | Renders console link and resource usage |
| `kubedc_UsageUpdate($params)` | Daily cron | Fetches resource usage from kube-dc API for WHMCS display |

**Module Parameters Available** (passed by WHMCS to every function):
- `$params['serverip']` — kube-dc API hostname (from Server config)
- `$params['serveraccesshash']` — WHMCS webhook secret (from Server config)
- `$params['configoption1']` — Plan ID (e.g., "pro-pool")
- `$params['customfields']['Organization']` — Organization name in kube-dc
- `$params['serviceid']` — WHMCS service ID (used as providerSubscriptionId)
- `$params['clientsdetails']` — Client email, name, etc.
- `$params['userid']` — WHMCS client ID (used as providerCustomerId)

#### 3.1.2 API Client (`lib/KubeDCApiClient.php`)

Handles authenticated HTTP communication with kube-dc:

```php
class KubeDCApiClient {
    private $apiUrl;    // e.g., https://console.kube-dc.com/api/billing
    private $apiSecret; // Shared HMAC secret

    public function sendWebhook(string $action, array $payload): array;
    public function getOrganizationUsage(string $organization): array;
    public function testConnection(): bool;
}
```

- Uses **HMAC-SHA256** signature in `X-KubeDC-Signature` header
- Includes **timestamp** in `X-KubeDC-Timestamp` header (replay protection, 5-min window)
- Payload: JSON with `action`, `organization`, `planId`, `serviceId`, `clientId`, `metadata`

#### 3.1.3 Hook Handlers (`hooks.php`)

```php
// Log provisioning events for audit trail
add_hook('AfterModuleCreate', 1, function($vars) { /* log */ });
add_hook('AfterModuleSuspend', 1, function($vars) { /* log */ });
add_hook('AfterModuleTerminate', 1, function($vars) { /* log */ });

// Optional: sync usage data on client area view
add_hook('ClientAreaPageProductDetails', 1, function($vars) { /* fetch usage */ });
```

#### 3.1.4 Setup Scripts (`setup/`)

CLI scripts run once during initial setup, using WHMCS Internal API:

**`configure-server.php`** — Registers kube-dc as a server in WHMCS:
```
php configure-server.php \
  --api-url=https://console.kube-dc.com/api/billing \
  --api-secret=<shared-hmac-secret> \
  --name="Kube-DC Cloud"
```

**`configure-products.php`** — Creates WHMCS products matching billing plans:
```
php configure-products.php \
  --server="Kube-DC Cloud" \
  --plans=dev-pool:9.99,pro-pool:29.99,scale-pool:99.99 \
  --billing-cycle=monthly
```

**`configure-addons.php`** — Creates configurable options for turbo add-ons:
```
php configure-addons.php \
  --addons=turbo-x1:4.99,turbo-x2:9.99
```

### 3.2 Kube-DC Backend Changes (Minimal)

#### 3.2.1 New File: `ui/backend/controllers/billing/providers/whmcs.js`

A single Express router file (~200 lines), following the same pattern as `providers/stripe.js`:

```javascript
const express = require('express');
const router = express.Router();
const crypto = require('crypto');
const {
    getServiceAccountToken,
    getSubscriptionPlans,
    getOrganizationSubscriptionData,
    updateOrganizationSubscription,
} = require('../quotaController');

// Env vars
const WHMCS_WEBHOOK_SECRET = process.env.WHMCS_WEBHOOK_SECRET;

// HMAC signature verification middleware
function verifyWhmcsSignature(req, res, next) { ... }

// Webhook handler
router.post('/webhook', express.raw({ type: 'application/json' }), verifyWhmcsSignature, async (req, res) => {
    const { action, organization, planId, serviceId, clientId } = JSON.parse(req.body);

    switch (action) {
        case 'activate':   // → status: active, set plan
        case 'suspend':    // → status: suspended
        case 'unsuspend':  // → status: active, restore plan
        case 'terminate':  // → status: canceled, clear plan
        case 'change':     // → update planId
        case 'renew':      // → no-op (keep active)
    }
});

module.exports = router;
```

**Key environment variables:**
| Variable | Description |
|----------|-------------|
| `WHMCS_WEBHOOK_SECRET` | Shared HMAC secret for webhook signature verification |

That's it — **one new file, one env var**.

#### 3.2.2 Modified File: `ui/backend/routes/billing.js`

Add two lines to the existing conditional:

```javascript
// Existing:
if (BILLING_PROVIDER === 'stripe') {
    const stripeRouter = require('../controllers/billing/providers/stripe');
    router.use('/', stripeRouter);
    logger.info('Billing: Stripe provider loaded');
}
// New:
else if (BILLING_PROVIDER === 'whmcs') {
    const whmcsRouter = require('../controllers/billing/providers/whmcs');
    router.use('/', whmcsRouter);
    logger.info('Billing: WHMCS provider loaded');
}
```

#### 3.2.3 Modified File: `ui/backend/controllers/billing/quotaController.js`

Update the `/config` endpoint feature flags for WHMCS:

```javascript
router.get('/config', (req, res) => {
    sendSuccessResponse(res, {
        provider: BILLING_PROVIDER,
        features: {
            quotas: true,
            plans: true,
            checkout: BILLING_PROVIDER === 'stripe',        // WHMCS has its own cart
            portal: BILLING_PROVIDER === 'stripe',          // WHMCS has its own client area
            webhooks: BILLING_PROVIDER !== 'none',
            addons: BILLING_PROVIDER !== 'none',
            metering: BILLING_METERING_ENABLED,
            externalBillingUrl: BILLING_PROVIDER === 'whmcs' // New: link to WHMCS client area
                ? (process.env.WHMCS_CLIENT_AREA_URL || null) : null,
        },
    }, 'Billing configuration');
});
```

**Frontend impact:** When `provider === 'whmcs'`, the billing page hides Subscribe/Cancel/Portal buttons (they don't apply), and optionally shows a "Manage in WHMCS" link using `externalBillingUrl`. No other frontend changes needed — it already gates on `features.checkout` and `features.portal`.

---

## 4. Webhook API Contract

### 4.1 Endpoint

```
POST /api/billing/webhook
Content-Type: application/json
X-KubeDC-Signature: sha256=<hmac-hex>
X-KubeDC-Timestamp: <unix-epoch-seconds>
```

### 4.2 Request Body

```json
{
    "action": "activate|suspend|unsuspend|terminate|change|renew",
    "organization": "acme",
    "planId": "pro-pool",
    "serviceId": "42",
    "clientId": "7",
    "clientEmail": "admin@acme.com",
    "metadata": {
        "whmcsInvoiceId": "1234",
        "whmcsOrderId": "567"
    }
}
```

### 4.3 Actions

| Action | Required Fields | Organization Annotation Changes |
|--------|----------------|-------------------------------|
| `activate` | organization, planId, serviceId, clientId | `subscription=active`, `plan-id=<planId>`, `provider-subscription-id=whmcs-svc-<serviceId>`, `provider-customer-id=whmcs-client-<clientId>` |
| `suspend` | organization | `subscription=suspended`, `suspended-at=<now>` |
| `unsuspend` | organization | `subscription=active`, clears `suspended-at` |
| `terminate` | organization | `subscription=canceled`, clears plan and provider IDs |
| `change` | organization, planId | `plan-id=<newPlanId>`, `plan-name=<newPlanName>` |
| `renew` | organization | No-op (keep active), log renewal |

### 4.4 Response

```json
// Success
{ "success": true, "message": "Organization acme activated with plan pro-pool" }

// Error
{ "success": false, "error": "Organization not found", "code": "ORG_NOT_FOUND" }
```

### 4.5 Signature Verification

```
payload = request_body_raw
timestamp = X-KubeDC-Timestamp header
message = timestamp + "." + payload
expected = HMAC-SHA256(WHMCS_WEBHOOK_SECRET, message)
actual = X-KubeDC-Signature header (after removing "sha256=" prefix)
```

Reject if:
- Signature mismatch
- Timestamp older than 300 seconds (replay protection)

---

## 5. WHMCS Product Configuration

### 5.1 Server Setup

| Field | Value |
|-------|-------|
| Name | Kube-DC Cloud |
| Module | kubedc |
| Hostname | `console.kube-dc.com` |
| IP Address | (optional) |
| Access Hash | `<WHMCS_WEBHOOK_SECRET>` — shared HMAC secret |
| Secure | ✅ (HTTPS) |

### 5.2 Product Mapping

Create three products in WHMCS, one per billing plan:

| WHMCS Product | Plan ID (Config Option 1) | Price | Billing Cycle |
|---------------|---------------------------|-------|---------------|
| Kube-DC Dev Pool | `dev-pool` | $9.99/mo | Monthly |
| Kube-DC Pro Pool | `pro-pool` | $29.99/mo | Monthly |
| Kube-DC Scale Pool | `scale-pool` | $99.99/mo | Monthly |

### 5.3 Custom Fields

Each product needs one custom field:

| Field Name | Type | Description |
|-----------|------|-------------|
| Organization | Text | Kube-DC organization name (entered at order time) |

### 5.4 Configurable Options (for Turbo Add-ons)

| Option Group | Options | Price |
|-------------|---------|-------|
| Turbo Resources | Turbo x1 (0-10), Turbo x2 (0-10) | $4.99/mo per unit |

---

## 6. Security

### 6.1 Authentication

- **WHMCS → kube-dc:** HMAC-SHA256 signed webhooks with shared secret and timestamp replay protection
- **kube-dc → WHMCS (optional):** API credentials (identifier + secret) for usage data retrieval

### 6.2 Network

- WHMCS server must be able to reach kube-dc API endpoint (HTTPS)
- kube-dc webhook endpoint should be IP-restricted to WHMCS server IP(s) if possible
- TLS required for all communication

### 6.3 Secrets Management

| Secret | Location | Purpose |
|--------|----------|---------|
| `WHMCS_WEBHOOK_SECRET` | kube-dc backend env | Verify incoming webhook signatures |
| Server Access Hash | WHMCS server config | WHMCS module uses this to sign requests |
| WHMCS API Credentials | kube-dc env (optional) | Call WHMCS API for usage sync |

---

## 7. Implementation Steps

### Phase 1: Kube-DC Backend (1–2 days)

| Step | Task | Files | Effort |
|------|------|-------|--------|
| 1.1 | Create `providers/whmcs.js` with webhook handler | `ui/backend/controllers/billing/providers/whmcs.js` (new) | 4h |
| 1.2 | Add WHMCS route mounting in `billing.js` | `ui/backend/routes/billing.js` (2 lines) | 15m |
| 1.3 | Update `/config` endpoint feature flags | `ui/backend/controllers/billing/quotaController.js` (~5 lines) | 15m |
| 1.4 | Add `WHMCS_WEBHOOK_SECRET` to deployment env | Helm chart / deployment manifest | 15m |
| 1.5 | Test webhook endpoint with curl | Manual testing | 1h |

### Phase 2: WHMCS Provisioning Module (2–3 days)

| Step | Task | Files | Effort |
|------|------|-------|--------|
| 2.1 | Create module directory structure | `integration/whmcs/modules/servers/kubedc/` | 30m |
| 2.2 | Implement `KubeDCApiClient.php` (HTTP + HMAC) | `lib/KubeDCApiClient.php` | 2h |
| 2.3 | Implement `kubedc.php` core functions | `kubedc.php` | 4h |
| 2.4 | Implement `hooks.php` audit logging | `hooks.php` | 1h |
| 2.5 | Create `clientarea.tpl` template | `templates/clientarea.tpl` | 1h |
| 2.6 | Write setup scripts | `setup/*.php` | 2h |
| 2.7 | Deploy module to WHMCS VM | SCP to `/var/www/html/whmcs/modules/servers/kubedc/` | 30m |

### Phase 3: WHMCS Configuration (0.5 day)

| Step | Task | Where | Effort |
|------|------|-------|--------|
| 3.1 | Register kube-dc server in WHMCS admin | WHMCS Admin → Servers | 15m |
| 3.2 | Create products (dev, pro, scale) | WHMCS Admin → Products | 30m |
| 3.3 | Configure custom fields | WHMCS Admin → Product Custom Fields | 15m |
| 3.4 | Create API credentials for kube-dc | WHMCS Admin → API Credentials | 15m |
| 3.5 | Configure configurable options (turbo) | WHMCS Admin → Configurable Options | 30m |

### Phase 4: End-to-End Testing (1 day)

| Step | Test Case | Expected Result |
|------|-----------|----------------|
| 4.1 | Place order for Pro Pool plan | Invoice created, payment processed, `CreateAccount` fires, org activated |
| 4.2 | Let invoice go overdue | `SuspendAccount` fires, org suspended, HRQ prevents new pods |
| 4.3 | Pay overdue invoice | `UnsuspendAccount` fires, org active again |
| 4.4 | Upgrade from Dev to Pro | `ChangePackage` fires, plan-id annotation updated |
| 4.5 | Cancel service | `TerminateAccount` fires, org deactivated |
| 4.6 | Invalid webhook signature | 401 rejected |
| 4.7 | Replay old webhook | 401 rejected (timestamp expired) |
| 4.8 | kube-dc console billing page | Shows plan info, read-only, "Manage in WHMCS" link |

### Phase 5: Documentation (0.5 day)

| Step | Task | File |
|------|------|------|
| 5.1 | Write WHMCS installation guide | `integration/whmcs/docs/installation.md` |
| 5.2 | Write troubleshooting guide | `integration/whmcs/docs/troubleshooting.md` |
| 5.3 | Update billing-plans-configuration.md | `docs/billing-plans-configuration.md` |
| 5.4 | Update internal-billing-integration.md | `docs/internal-billing-integration.md` |

---

## 8. What Does NOT Change

| Component | Why |
|-----------|-----|
| **Organization Controller (Go)** | Already watches `billing.kube-dc.com/*` annotations — provider-agnostic |
| **HRQ / LimitRange / EIP Quota enforcement** | Driven by annotations, not by provider type |
| **billing-plans ConfigMap** | Plan definitions are shared across all providers |
| **quotaController.js shared functions** | `updateOrganizationSubscription()` writes provider-agnostic annotations |
| **Frontend Billing UI** | Already gates on `features.checkout` / `features.portal` — auto-hides Stripe-specific buttons |
| **Stripe provider** | Untouched — both providers can coexist (only one active per deployment) |

---

## 9. Configuration Summary

### Kube-DC Deployment

```yaml
# Backend environment variables
BILLING_PROVIDER: "whmcs"
WHMCS_WEBHOOK_SECRET: "<shared-hmac-secret>"
WHMCS_CLIENT_AREA_URL: "https://billing.example.com/clientarea.php"  # optional
```

### WHMCS Server

```
Module location: /var/www/html/whmcs/modules/servers/kubedc/
Server config:   WHMCS Admin → System Settings → Servers
Products:        WHMCS Admin → System Settings → Products/Services
```

---

## 10. Future Enhancements

| Feature | Description | Priority |
|---------|-------------|----------|
| **Usage-based billing** | `UsageUpdate` function reports CPU/memory/storage to WHMCS for overage billing | Medium |
| **SSO from WHMCS to kube-dc console** | Generate JWT from WHMCS client session for seamless login | Low |
| **Automatic organization creation** | `CreateAccount` creates the kube-dc Organization if it doesn't exist | Medium |
| **Multi-organization support** | One WHMCS client can have multiple kube-dc organizations (one per service) | Low |
| **WHMCS API polling** | kube-dc periodically polls WHMCS API to reconcile subscription states | Low |

---

## 11. Comparison: Stripe vs WHMCS Integration

| Aspect | Stripe | WHMCS |
|--------|--------|-------|
| **Direction** | kube-dc → Stripe (creates checkout, subscriptions) | WHMCS → kube-dc (calls webhook) |
| **Billing UI** | Stripe Checkout + Customer Portal | WHMCS Client Area + Cart |
| **Payment processing** | Stripe handles payments | WHMCS handles payments (supports 100+ gateways) |
| **Subscription management** | kube-dc backend manages via Stripe SDK | WHMCS manages natively; kube-dc receives events |
| **Webhook direction** | Stripe → kube-dc (Stripe calls us) | WHMCS → kube-dc (WHMCS module calls us) |
| **Backend code** | ~790 lines (`providers/stripe.js`) | ~200 lines (`providers/whmcs.js`) |
| **WHMCS-side code** | None | ~500 lines (provisioning module + hooks + API client) |
| **Env vars** | 8 (Stripe keys + price IDs) | 2 (webhook secret + optional client area URL) |

---

## 12. References

- [WHMCS Developer Documentation](https://developers.whmcs.com/)
- [WHMCS Provisioning Modules](https://developers.whmcs.com/provisioning-modules/)
- [WHMCS Supported Functions](https://developers.whmcs.com/provisioning-modules/supported-functions/)
- [WHMCS Module Parameters](https://developers.whmcs.com/provisioning-modules/module-parameters/)
- [WHMCS Hook Reference](https://developers.whmcs.com/hooks-reference/module/)
- [WHMCS API Authentication](https://developers.whmcs.com/api/authentication/)
- [WHMCS Sample Provisioning Module](https://github.com/WHMCS/sample-provisioning-module)
- [WHMCS Plesk Integration](https://docs.plesk.com/en-US/onyx/multi-server-guide/integration-with-whmcs.77894/)
- [Kube-DC Billing Plans Configuration](../billing-plans-configuration.md)
- [Kube-DC Internal Billing Integration](../internal-billing-integration.md)
