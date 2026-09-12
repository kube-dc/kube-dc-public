import {
  SsoArchitectureDiagram,
  SsoUserJourneyDiagram,
} from '@site/src/components/Diagram/PlatformTopologyDiagrams';

# Google SSO Authentication Setup

This guide explains how to enable Google OAuth authentication for Kube-DC using a central SSO Keycloak realm.

## Overview

Kube-DC supports Google OAuth authentication via a central `sso` Keycloak realm that brokers authentication to organization-specific realms. This allows:

- **Single Google OAuth configuration** - One Google client ID/secret for all organizations
- **Per-organization isolation** - Tokens issued by Organization realms with Organization-specific permissions
- **Multi-Organization support** - Users can belong to multiple organizations
- **Self-service registration** - Users can sign up and create organizations
- **Feature flag** - Enable/disable per deployment

## Architecture

<details data-github-only>
<summary>Diagram source for GitHub</summary>

```
┌─────────────────────────────────────────────────────────────────────────────┐
│                              Keycloak Server                                │
│                                                                             │
│  ┌────────────────────────────────────────────────────────────────────┐     │
│  │                         Realm: sso                                 │     │
│  │                                                                    │     │
│  │  ┌─────────────────┐  ┌─────────────────┐  ┌──────────────────┐    │     │
│  │  │ Google IdP      │  │ Console Client  │  │ Broker Client    │    │     │
│  │  │ (auto-link)     │  │ (kube-dc)       │  │ (sso-broker)     │    │     │
│  │  └─────────────────┘  └─────────────────┘  └──────────────────┘    │     │
│  │                                                                    │     │
│  │  Registration: Passwordless (email verification required)          │     │
│  │  Groups: /orgs/shalb, /orgs/acme, ...                              │     │
│  └────────────────────────────────────────────────────────────────────┘     │
│                              │                                              │
│                              │ OIDC IdP Brokering                           │
│                              ▼                                              │
│  ┌──────────────────┐  ┌──────────────────┐  ┌──────────────────┐           │
│  │  Realm: shalb    │  │  Realm: acme     │  │  Realm: foo      │           │
│  │  IdP: sso ───────┼──┼──────────────────┼──┼──► SSO Realm     │           │
│  │  Users: admin    │  │  Users: admin    │  │  Users: admin    │           │
│  └──────────────────┘  └──────────────────┘  └──────────────────┘           │
└─────────────────────────────────────────────────────────────────────────────┘
```

</details>

<SsoArchitectureDiagram />

## User Journey

<details data-github-only>
<summary>Diagram source for GitHub</summary>

```
┌─────────────────────────────────────────────────────────────────────────────┐
│                         SELF-SERVICE REGISTRATION                           │
├─────────────────────────────────────────────────────────────────────────────┤
│                                                                             │
│  1. SIGN UP                    2. VERIFY EMAIL                              │
│  ┌─────────────────────┐       ┌─────────────────────┐                      │
│  │ Enter:              │       │ Check inbox         │                      │
│  │ • Email             │ ───►  │ Click verify link   │                      │
│  │ • First/Last Name   │       │                     │                      │
│  │ (No password yet!)  │       └─────────────────────┘                      │
│  └─────────────────────┘                 │                                  │
│                                          ▼                                  │
│  3. CHOOSE ORGANIZATION          4. SET PASSWORD                            │
│  ┌──────────────────────────┐    ┌─────────────────────┐                    │
│  │ Choose:                  │    │ Set password        │                    │
│  │ • Create Organization    │ ─► │ (only when creating │                    │
│  │ • Join Organization      │    │  an Organization)   │                    │
│  └──────────────────────────┘    └─────────────────────┘                    │
│                                          │                                  │
│                                          ▼                                  │
│                                ┌─────────────────────┐                      │
│                                │ ✓ Organization      │                      │
│                                │   created!          │                      │
│                                │ ✓ Auto-redirected   │                      │
│                                │   to console        │                      │
│                                └─────────────────────┘                      │
└─────────────────────────────────────────────────────────────────────────────┘

┌─────────────────────────────────────────────────────────────────────────────┐
│                           GOOGLE SSO LOGIN                                  │
├─────────────────────────────────────────────────────────────────────────────┤
│                                                                             │
│  User clicks              Google OAuth              Auto-link by email      │
│  "Login with Google"      authentication            (no extra prompts)      │
│  ┌─────────────────┐     ┌─────────────┐       ┌─────────────┐              │
│  │     Console     │ ───►│   Google    │ ─────►│  Keycloak   │              │
│  │ (Organization)  │     │   Sign-in   │       │  SSO Realm  │              │
│  └─────────────────┘     └─────────────┘       └─────────────┘              │
│                                                             │               │
│                                                             ▼               │
│                     Broker to Organization realm       Token issued         │
│                     ┌────────────────────┐         ┌─────────────┐          │
│                     │ Organization Realm │ ──────► │  Console    │          │
│                     │     (via SSO)      │         │ (logged in) │          │
│                     └────────────────────┘         └─────────────┘          │
└─────────────────────────────────────────────────────────────────────────────┘
```

</details>

<SsoUserJourneyDiagram />

## Prerequisites

1. The `kube-dc` CLI and write access to the cluster's fleet repository
2. A `KUBECONFIG` for the cluster (the CLI reads the Keycloak admin password
   from it — you do not need the password yourself)
3. An SMTP relay: email verification cannot work without one
4. Google Cloud Console access, **only** if you want Google social login on top
   of email sign-up

You do not need a checkout of the Kube-DC source. Everything below runs from the
CLI against your fleet repository.

## Setup Steps

### Step 1: Create Google OAuth Credentials

#### 1.1 Create a Google Cloud Project

1. Go to [Google Cloud Console](https://console.cloud.google.com/)
2. Click the project dropdown → **New Project**
3. Enter a project name (e.g., `kube-dc-sso`)
4. Click **Create**

#### 1.2 Configure OAuth Consent Screen

1. Navigate to **APIs & Services → OAuth consent screen**
2. Select **External** user type (or Internal for Google Workspace)
3. Fill in required fields:
   - **App name:** `Kube-DC`
   - **User support email:** Your email
   - **Developer contact:** Your email
4. Click **Save and Continue**
5. Add scopes: `email`, `profile`, `openid`
6. Click **Save and Continue** through remaining steps

#### 1.3 Create OAuth 2.0 Client ID

1. Navigate to **APIs & Services → Credentials**
2. Click **Create Credentials → OAuth 2.0 Client ID**
3. Select **Web application**
4. Configure:
   - **Name:** `Kube-DC SSO`
   - **Authorized JavaScript origins:** `https://<your-keycloak-url>`
   - **Authorized redirect URIs:**
     ```
     https://<your-keycloak-url>/realms/sso/broker/google/endpoint
     ```
5. Click **Create**
6. **Copy and save** the **Client ID** and **Client Secret**

> ⚠️ **Important:** Keep the Client Secret secure. You'll need both values for the next step.

### Step 2: Bootstrap SSO Realm

> **Email-only sign-up:** Google is optional. Skipping this whole page's
> Google steps and providing only SMTP gives you a realm with email
> self-registration + verification — the console then shows "Sign up with
> email" and hides the Google buttons automatically.

The whole realm ceremony is one CLI command. It reads the Keycloak admin
password from the cluster, creates the realm and its clients, verifies the
sign-up surface end-to-end, SOPS-encrypts the SMTP credentials and the broker
secret into your fleet overlay, and flips `SSO_ENABLED=true` only after the
realm exists — which is the order that matters, because the flag alone makes
every Organization Sync require a realm that may not be there yet.

```bash
# Email-only:
SMTP_USER=<user> SMTP_PASSWORD=<password> \
  kube-dc bootstrap keycloak sso <cluster> --repo <fleet-repo> \
  --smtp-host smtp.example.com --smtp-port 587 --smtp-from noreply@your-domain.com

# With Google social login (add the pair):
SMTP_USER=<user> SMTP_PASSWORD=<password> GOOGLE_CLIENT_SECRET=<secret> \
  kube-dc bootstrap keycloak sso <cluster> --repo <fleet-repo> \
  --smtp-host smtp.example.com --google-client-id <client-id>
```

The same runs automatically during `kube-dc bootstrap init` when
`SMTP_USER`/`SMTP_PASSWORD` are exported for the install. Afterwards:
`flux reconcile kustomization flux-system --with-source && flux reconcile
kustomization platform`.

`SSO_BROKER_SECRET` is generated for you and stored SOPS-encrypted in your
fleet overlay's `secrets.enc.yaml` — there is nothing to copy down and
nothing to keep outside the repo. Re-running the command converges with no
Git diff and never rotates that secret implicitly.

#### What the command configures

| Component | Description |
|-----------|-------------|
| **SSO Realm** | Central realm for authentication brokering |
| **Passwordless Registration** | Users sign up without password (set during Organization creation) |
| **Email Verification** | Required before organization setup |
| **Auto-link Flow** | Automatically links Google accounts by email |
| **Google IdP** | Configured with your OAuth credentials |
| **Console Client** | `kube-dc` client with PKCE for frontend |
| **Broker Client** | `sso-broker` for Organization realm federation |
| **Organization Groups** | `/orgs` group structure for membership |

### Step 3: Configure Kube-DC

#### Option A: GitOps fleet (recommended — this is the supported path)

The chart is deployed by Flux from `kube-dc-fleet`, and the four SSO values are
already wired into `platform/kube-dc/helmrelease.yaml`. You do not edit the
chart or run Helm yourself; you set the variables the HelmRelease substitutes.

Turn the feature on in `clusters/<cluster>/cluster-config.env`:

```bash
SSO_ENABLED=true
```

The three credentials are secrets, so they belong in the SOPS-encrypted
`clusters/<cluster>/secrets.enc.yaml`, **not** in `cluster-config.env`:

```yaml
stringData:
  SSO_BROKER_SECRET: "<from-bootstrap-output>"
  GOOGLE_CLIENT_ID: "<your-google-client-id>"
  GOOGLE_CLIENT_SECRET: "<your-google-client-secret>"
```

:::warning Keep the keys present in `cluster-config.env`
`platform/kube-dc/helmrelease.yaml` references all four keys
*unconditionally*, and Flux's `postBuild` envsubst runs in **strict** mode — an
undefined key fails the entire `platform` Kustomization with `variable not set
(strict mode)`, not just the SSO feature. So `GOOGLE_CLIENT_ID`,
`GOOGLE_CLIENT_SECRET` and `SSO_BROKER_SECRET` must still *exist* in
`cluster-config.env`; leave them **empty** there. The cluster-secrets Secret is
listed after the ConfigMap in every Kustomization's `substituteFrom`, so the
encrypted values override the empty ones.
:::

Commit and push the fleet repo, then let Flux apply it:

```bash
flux reconcile kustomization platform --with-source
```

:::danger Never `helm upgrade` a fleet-managed cluster
Do not run `helm upgrade kube-dc ./charts/kube-dc`. The chart in a git checkout
is not necessarily the version the cluster runs, so this can silently
**downgrade** the platform, and Flux reverts whatever it applies on the next
reconcile. If you need Helm directly for a non-fleet cluster, pull the released
chart by version from the registry
(`oci://registry-1.docker.io/shalb/kube-dc`) rather than using a local path.
:::

#### Option B: kubectl patch (Existing deployments)

Add SSO configuration to the `master-config` secret:

```bash
export SSO_BROKER_SECRET="<from-bootstrap-output>"
export GOOGLE_CLIENT_ID="<your-google-client-id>"
export GOOGLE_CLIENT_SECRET="<your-google-client-secret>"

kubectl patch secret master-config -n kube-dc --type='json' -p="[
  {\"op\":\"add\",\"path\":\"/data/ssoEnabled\",\"value\":\"$(echo -n true | base64 -w0)\"},
  {\"op\":\"add\",\"path\":\"/data/ssoBrokerSecret\",\"value\":\"$(echo -n $SSO_BROKER_SECRET | base64 -w0)\"},
  {\"op\":\"add\",\"path\":\"/data/googleClientId\",\"value\":\"$(echo -n $GOOGLE_CLIENT_ID | base64 -w0)\"},
  {\"op\":\"add\",\"path\":\"/data/googleClientSecret\",\"value\":\"$(echo -n $GOOGLE_CLIENT_SECRET | base64 -w0)\"}
]"
```

### Step 4: Restart Controller

```bash
kubectl rollout restart deployment kube-dc-manager -n kube-dc
```

The controller will now automatically configure SSO IdP for all new organizations.

### Step 5: Add Existing Organizations to SSO (Optional)

For organizations created before SSO was enabled, trigger a reconciliation:

```bash
kubectl annotate organization <org-name> -n <org-name> reconcile=$(date +%s) --overwrite
```

The controller does the rest: it creates the org's group under `/orgs` in the
`sso` realm, the SSO identity provider in the org realm, the auto-link flow and
the IdP mappers. There is nothing to run by hand.

## Configuration Reference

### Helm Values

```yaml
manager:
  keycloakSecret:
    ssoEnabled: true                    # Enable Google SSO
    ssoBrokerSecret: "<secret>"         # Generated by the CLI, SOPS-encrypted in the fleet
    googleClientId: "<client-id>"       # Google OAuth Client ID
    googleClientSecret: "<secret>"      # Google OAuth Client Secret
```

The Helm chart automatically:
- Stores SSO credentials in `master-config` secret
- Configures frontend ConfigMap with `ssoEnabled` flag
- Exposes "Login with Google" button when enabled

### Master Config Secret Keys

| Key | Type | Description |
|-----|------|-------------|
| `ssoEnabled` | string | `"true"` to enable Google SSO |
| `ssoBrokerSecret` | string | Secret for SSO broker client (from bootstrap) |
| `googleClientId` | string | Google OAuth Client ID |
| `googleClientSecret` | string | Google OAuth Client Secret |

### Automatic Configuration per Organization

When SSO is enabled, the controller automatically configures each organization realm with:

1. **SSO IdP** - OIDC identity provider pointing to the `sso` realm
2. **Auto-link flow** - Authentication flow that links existing users by email
3. **IdP mappers** - Maps email, firstName, lastName from Google
4. **Organization group** - Creates `/orgs/<org-slug>` group in SSO realm

## User Experience

### Self-Service Registration

New users can sign up and create their own organization:

1. User clicks **"Sign Up"** on the console login page
2. Enters email, first name, and last name (no password required)
3. Receives verification email and clicks the link
4. After verification, chooses to:
   - **Create a new organization** - Sets password and becomes Organization admin
   - **Join existing organization** - Submits join request for admin approval
5. Redirected to the console, fully authenticated

> 💡 **Why passwordless registration?** Users set their password only when creating an organization. This simplifies the signup flow and ensures passwords are only needed for Organization-level access.

### Login Flow (Existing Users)

1. User navigates to the console
2. Enters organization name
3. Clicks **"Login with Google"** or uses username/password
4. Authenticates with Google account (single click, no extra screens)
5. Returns to console, authenticated to the organization

### Organization Membership

For self-registered users, membership is automatic when they create an organization. For joining existing organizations:

1. Log in to Keycloak admin console (`/admin/sso/console`)
2. Navigate to **Groups → orgs → `<org-slug>`**
3. Add user to the group

Or via API:
```bash
# Get user ID
USER_ID=$(curl -s -H "Authorization: Bearer $TOKEN" \
  "$KEYCLOAK_URL/admin/realms/sso/users?email=user@example.com" | jq -r '.[0].id')

# Get group ID
GROUP_ID=$(curl -s -H "Authorization: Bearer $TOKEN" \
  "$KEYCLOAK_URL/admin/realms/sso/groups" | jq -r '.[] | select(.name=="orgs") | .subGroups[] | select(.name=="<org-slug>") | .id')

# Add user to group
curl -X PUT -H "Authorization: Bearer $TOKEN" \
  "$KEYCLOAK_URL/admin/realms/sso/users/$USER_ID/groups/$GROUP_ID"
```

## Verification

After setup, verify the configuration is correct:

```bash
# Get Keycloak credentials
KC_URL=$(kubectl get secret -n kube-dc master-config -o jsonpath='{.data.url}' | base64 -d)
KC_USER=$(kubectl get secret -n kube-dc master-config -o jsonpath='{.data.user}' | base64 -d)
KC_PASS=$(kubectl get secret -n kube-dc master-config -o jsonpath='{.data.password}' | base64 -d)

# Get admin token
ADMIN_TOKEN=$(curl -s -X POST "$KC_URL/realms/master/protocol/openid-connect/token" \
  -d "username=$KC_USER" -d "password=$KC_PASS" \
  -d "grant_type=password" -d "client_id=admin-cli" | jq -r '.access_token')

# Check SSO realm configuration
echo "Registration Flow:"
curl -s -H "Authorization: Bearer $ADMIN_TOKEN" "$KC_URL/admin/realms/sso" | jq -r '.registrationFlow'
# Expected: registration-no-password

echo "Auto-link Flow:"
curl -s -H "Authorization: Bearer $ADMIN_TOKEN" "$KC_URL/admin/realms/sso/authentication/flows" | \
  jq -r '.[] | select(.alias=="auto-link-broker-login") | .alias'
# Expected: auto-link-broker-login

echo "Google IdP Broker Flow:"
curl -s -H "Authorization: Bearer $ADMIN_TOKEN" "$KC_URL/admin/realms/sso/identity-provider/instances/google" | \
  jq -r '.firstBrokerLoginFlowAlias'
# Expected: auto-link-broker-login
```

## Troubleshooting

### SSO realm not found

**Error:** `SSO realm 'sso' does not exist` in the kube-dc-manager log, and
organizations do not converge.

**Cause:** `SSO_ENABLED=true` is set on the cluster but the `sso` realm was
never created — every Organization Sync then hard-requires a realm that is not
there.

**Solution:** Run Step 2 (`kube-dc bootstrap keycloak sso <cluster> --repo
<fleet-repo>`). It creates the realm and only then flips `SSO_ENABLED`, which is
the order that avoids this state. If the flag was set by hand ahead of the realm,
either run the command or set `SSO_ENABLED=false` until you do.

### Google login shows "Account already exists" prompt

**Cause:** Auto-link flow not configured on Google IdP.

**Solution:** Verify the Google IdP uses `auto-link-broker-login` as its first broker login flow:
```bash
curl -s -X PUT -H "Authorization: Bearer $ADMIN_TOKEN" \
  -H "Content-Type: application/json" \
  -d '{"firstBrokerLoginFlowAlias": "auto-link-broker-login"}' \
  "$KC_URL/admin/realms/sso/identity-provider/instances/google"
```

### Google login not working

1. Check Google OAuth redirect URI matches exactly:
   ```
   https://<your-keycloak-url>/realms/sso/broker/google/endpoint
   ```
2. Verify `ssoEnabled` is `"true"` in master-config secret
3. Check Google IdP has client secret configured
4. Check controller logs: `kubectl logs -n kube-dc -l app.kubernetes.io/name=kube-dc-manager`

### User not authorized

**Error:** User can authenticate but cannot access organization

**Solution:** Add user to `/orgs/<org-slug>` group in SSO realm.

### Registration email not received

1. Verify SMTP is configured in Keycloak SSO realm
2. Check Keycloak logs for email sending errors
3. Verify the email address is correct

## Disabling SSO

To disable Google SSO:

```bash
kubectl patch secret master-config -n kube-dc --type='json' -p='[
  {"op":"replace","path":"/data/ssoEnabled","value":"'$(echo -n false | base64 -w0)'"}
]'

kubectl rollout restart deployment kube-dc-manager -n kube-dc
```

Users will fall back to direct organization login with username/password.

## Security Considerations

- **Token isolation** - SSO realm tokens are only used for authentication; final tokens come from Organization realms
- **Organization membership verification** - Users cannot access organizations they're not members of
- **Secrets management** - All credentials stored in Kubernetes secrets, never in code
- **TLS required** - All Keycloak endpoints must use HTTPS

---

**See also:**
- [Keycloak Identity Brokering Documentation](https://www.keycloak.org/docs/latest/server_admin/#_identity_broker)
- [Google OAuth Setup Guide](https://developers.google.com/identity/protocols/oauth2)
