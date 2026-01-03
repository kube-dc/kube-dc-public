# Google SSO Authentication Setup

This guide explains how to enable Google OAuth authentication for Kube-DC using a central SSO Keycloak realm.

## Overview

Kube-DC supports Google OAuth authentication via a central `sso` Keycloak realm that brokers authentication to organization-specific realms. This allows:

- **Single Google OAuth configuration** - One Google client ID/secret for all organizations
- **Per-organization isolation** - Tokens issued by org realms with org-specific permissions
- **Multi-org support** - Users can belong to multiple organizations
- **Feature flag** - Enable/disable per deployment

## Architecture

```
┌─────────────────────────────────────────────────────────────────────────┐
│                          Keycloak Server                                │
│                                                                         │
│  ┌────────────────────────────────────────┐                             │
│  │           Realm: sso                   │                             │
│  │                                        │                             │
│  │  Identity Provider: google             │                             │
│  │  Client: sso-broker                    │                             │
│  │  Groups: /orgs/shalb, /orgs/acme, ...  │                             │
│  └────────────────────────────────────────┘                             │
│           │                                                             │
│           │ OIDC IdP Brokering                                          │
│           ▼                                                             │
│  ┌──────────────┐  ┌──────────────┐  ┌──────────────┐                   │
│  │ Realm: shalb │  │ Realm: acme  │  │ Realm: foo   │                   │
│  │  IdP: sso    │  │  IdP: sso    │  │  IdP: sso    │                   │
│  └──────────────┘  └──────────────┘  └──────────────┘                   │
└─────────────────────────────────────────────────────────────────────────┘
```

## Prerequisites

1. Google Cloud Console access to create OAuth credentials
2. Keycloak admin access
3. Kube-DC deployment with controller v0.1.34+

## Setup Steps

### Step 1: Create Google OAuth Credentials

1. Go to [Google Cloud Console](https://console.cloud.google.com/)
2. Create or select a project
3. Navigate to **APIs & Services → Credentials**
4. Click **Create Credentials → OAuth 2.0 Client ID**
5. Select **Web application**
6. Add authorized redirect URIs:
   ```
   https://<your-keycloak-url>/realms/sso/broker/google/endpoint
   ```
7. Save and copy the **Client ID** and **Client Secret**

### Step 2: Bootstrap SSO Realm

Run the bootstrap script to create the central SSO realm with Google IdP:

```bash
export KEYCLOAK_URL="https://login.your-domain.com"
export KEYCLOAK_ADMIN_USER="admin"
export KEYCLOAK_ADMIN_PASSWORD="<your-admin-password>"
export GOOGLE_CLIENT_ID="<your-google-client-id>"
export GOOGLE_CLIENT_SECRET="<your-google-client-secret>"

./hack/bootstrap-sso-realm.sh
```

**Output:** The script will generate and display `SSO_BROKER_SECRET`. Save this securely.

### Step 3: Configure Kube-DC

#### Option A: Helm Values (Recommended for new deployments)

Add SSO configuration to your Helm values:

```yaml
manager:
  keycloakSecret:
    ssoEnabled: true
    ssoBrokerSecret: "<from-bootstrap-output>"
    googleClientId: "<your-google-client-id>"
    googleClientSecret: "<your-google-client-secret>"
```

Then upgrade the Helm release:

```bash
helm upgrade kube-dc ./charts/kube-dc -n kube-dc -f values.yaml
```

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

Or use the manual script:

```bash
export ORG_SLUG="<organization-name>"
./hack/add-org-to-sso.sh
```

## Configuration Reference

### Helm Values

```yaml
manager:
  keycloakSecret:
    ssoEnabled: true                    # Enable Google SSO
    ssoBrokerSecret: "<secret>"         # From bootstrap script output
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
4. **Org group** - Creates `/orgs/<org-slug>` group in SSO realm

## User Experience

### Login Flow

1. User navigates to the console
2. Enters organization name
3. Clicks **"Login with Google"**
4. Authenticates with Google account
5. Returns to console, authenticated to the organization

### Organization Membership

Users must be added to the organization group in the SSO realm:

1. Log in to Keycloak admin console (`/admin/sso/console`)
2. Navigate to **Groups → orgs → <org-slug>**
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

## Troubleshooting

### SSO realm not found

**Error:** `SSO realm 'sso' does not exist. Run bootstrap-sso-realm.sh first`

**Solution:** Run the bootstrap script to create the SSO realm.

### Google login not working

1. Check Google OAuth redirect URI matches exactly
2. Verify `ssoEnabled` is `"true"` in master-config secret
3. Check controller logs: `kubectl logs -n kube-dc -l app.kubernetes.io/name=kube-dc-manager`

### User not authorized

**Error:** User can authenticate but cannot access organization

**Solution:** Add user to `/orgs/<org-slug>` group in SSO realm.

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

- **Token isolation** - SSO realm tokens are only used for authentication; final tokens come from org realms
- **Org membership verification** - Users cannot access organizations they're not members of
- **Secrets management** - All credentials stored in Kubernetes secrets, never in code
- **TLS required** - All Keycloak endpoints must use HTTPS

---

**See also:**
- [Keycloak Identity Brokering Documentation](https://www.keycloak.org/docs/latest/server_admin/#_identity_broker)
- [Google OAuth Setup Guide](https://developers.google.com/identity/protocols/oauth2)
