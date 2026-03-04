# Keycloak SMTP Configuration

## Overview

This document describes how SMTP email configuration works in Kube-DC's Keycloak integration, including how credentials are stored, synchronized to realms, and how to configure Gmail App Passwords.

## Architecture

### Configuration Flow

```
┌─────────────────────┐     ┌──────────────────────┐     ┌─────────────────┐
│  master-config      │────▶│  Organization        │────▶│  Keycloak       │
│  Secret (kube-dc)   │     │  Controller          │     │  Realms         │
└─────────────────────┘     └──────────────────────┘     └─────────────────┘
```

1. **master-config Secret**: Stores SMTP credentials in `kube-dc` namespace
2. **Organization Controller**: Reads config, creates/updates Keycloak realms
3. **Keycloak Realms**: Each organization gets a realm with SMTP configured

### Components

| Component | Location | Purpose |
|-----------|----------|---------|
| `master-config` Secret | `kube-dc` namespace | Stores SMTP host, port, user, password |
| `buildSmtpConfig()` | `internal/organization/res_keycloak_realm.go` | Builds SMTP config from MasterConfigSpec |
| `diffSmtp()` | `internal/organization/res_keycloak_realm.go` | Detects SMTP config changes |
| `KeycloakRealm.Sync()` | `internal/organization/res_keycloak_realm.go` | Creates/updates realm with SMTP |

## master-config Secret Structure

```yaml
apiVersion: v1
kind: Secret
metadata:
  name: master-config
  namespace: kube-dc
type: Opaque
stringData:
  smtp_host: "smtp.gmail.com"
  smtp_port: "587"
  smtp_user: "console@kube-dc.cloud"
  smtp_password: "yhxlutqhppiggbwx"  # Gmail App Password (no spaces)
  smtp_from: "noreply@kube-dc.com"
  smtp_from_name: "Kube-DC"
```

## Controller Logic

### Realm Creation

When a new Organization is created, the controller:

1. Reads SMTP config from `MasterConfigSpec`
2. Calls `buildSmtpConfig()` to generate SMTP settings
3. Creates Keycloak realm with SMTP server configuration

```go
// buildSmtpConfig creates SMTP server configuration from MasterConfigSpec
func buildSmtpConfig(config kubedccomv1.MasterConfigSpec) *map[string]string {
    if config.SmtpHost == "" {
        return nil
    }
    smtp := map[string]string{
        "host":            config.SmtpHost,
        "port":            config.SmtpPort,
        "from":            config.SmtpFrom,
        "fromDisplayName": config.SmtpFromName,
        "starttls":        "true",
        "ssl":             "false",
    }
    if config.SmtpUser != "" {
        smtp["auth"] = "true"
        smtp["user"] = config.SmtpUser
        smtp["password"] = config.SmtpPassword
    } else {
        smtp["auth"] = "false"
    }
    return &smtp
}
```

### SMTP Sync on Change

When SMTP credentials change in `master-config`, the controller detects this during reconciliation:

```go
// diffSmtp compares SMTP configurations and returns true if they differ
func (r *KeycloakRealm) diffSmtp(target, source *map[string]string) bool {
    keysToCheck := []string{"host", "port", "user", "password", "auth", "from"}
    for _, key := range keysToCheck {
        if srcMap[key] != tgtMap[key] {
            return true
        }
    }
    return false
}
```

If a diff is detected, the controller updates the realm's SMTP configuration.

## Gmail App Password Setup

Gmail with 2-Factor Authentication requires an **App Password** instead of the regular account password.

### Why App Password?

- Gmail blocks "less secure apps" by default
- 2FA-enabled accounts cannot use regular passwords for SMTP
- App Passwords provide secure, revocable access for specific applications

### How to Generate

1. Go to [Google Account App Passwords](https://myaccount.google.com/apppasswords)
2. Sign in with your Google account
3. Select **App**: "Mail"
4. Select **Device**: "Other (Custom name)" → Enter "Keycloak"
5. Click **Generate**
6. Copy the 16-character password (format: `xxxx xxxx xxxx xxxx`)
7. Remove spaces when storing: `xxxxxxxxxxxx`

### Common Errors

| Error | Cause | Solution |
|-------|-------|----------|
| `530-5.7.0 Authentication Required` | `auth: false` in SMTP config | Enable authentication |
| `534-5.7.9 Application-specific password required` | Using regular password with 2FA | Generate and use App Password |
| `535-5.7.8 Username and Password not accepted` | Wrong credentials | Verify App Password is correct |

## Updating SMTP Credentials

### Method 1: Update master-config Secret

```bash
# Update the password in master-config
kubectl patch secret master-config -n kube-dc --type='json' \
  -p='[{"op": "replace", "path": "/data/smtp_password", "value": "'$(echo -n 'NEW_APP_PASSWORD' | base64)'"}]'

# Restart controller to trigger reconciliation
kubectl rollout restart deployment/kube-dc-manager -n kube-dc
```

### Method 2: Direct Keycloak API Update

For immediate updates to all realms:

```bash
# Get admin token
TOKEN=$(curl -s -X POST "https://login.kube-dc.cloud/realms/master/protocol/openid-connect/token" \
  -H "Content-Type: application/x-www-form-urlencoded" \
  -d "username=admin" \
  -d "password=ADMIN_PASSWORD" \
  -d "grant_type=password" \
  -d "client_id=admin-cli" | jq -r '.access_token')

# Update realm SMTP
curl -X PUT "https://login.kube-dc.cloud/admin/realms/REALM_NAME" \
  -H "Authorization: Bearer $TOKEN" \
  -H "Content-Type: application/json" \
  -d '{
    "smtpServer": {
      "host": "smtp.gmail.com",
      "port": "587",
      "from": "console@kube-dc.cloud",
      "fromDisplayName": "Kube-DC",
      "auth": "true",
      "starttls": "true",
      "ssl": "false",
      "user": "console@kube-dc.cloud",
      "password": "NEW_APP_PASSWORD"
    }
  }'
```

## Testing Email

### Via Keycloak Admin Console

1. Go to **Realm Settings** → **Email**
2. Click **Test Connection**
3. Check for success message or error details

### Via Password Reset Flow

1. Go to login page for a realm
2. Click "Forgot Password"
3. Enter a valid user email
4. Check if email is received

### Check Logs for Errors

```bash
kubectl logs -n keycloak statefulset/keycloak | grep -i -E "smtp|mail|email|error"
```

## Security Considerations

1. **Never commit App Passwords to git** - Store in secrets only
2. **Rotate App Passwords periodically** - Revoke old ones in Google Account
3. **Use dedicated email account** - Don't use personal Gmail for production
4. **Monitor for failures** - Set up alerting on email send errors

## Files Reference

| File | Purpose |
|------|---------|
| `internal/organization/res_keycloak_realm.go` | Realm creation with SMTP config |
| `api/kube-dc.com/v1/master_config_types.go` | MasterConfigSpec with SMTP fields |
| `charts/kube-dc/templates/master-config-secret.yaml` | Secret template |

## Troubleshooting

### Email Not Sending

1. Check Keycloak logs for specific error
2. Verify `auth: true` is set in realm SMTP config
3. Confirm App Password is used (not regular password)
4. Test SMTP connectivity from cluster

### Controller Not Syncing SMTP

1. Check controller logs for reconciliation errors
2. Verify `master-config` secret has correct fields
3. Trigger reconciliation by updating Organization resource
