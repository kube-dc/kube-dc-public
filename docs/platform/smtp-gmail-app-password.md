# Getting a Google App Password for Kube-DC SMTP

Kube-DC sends transactional emails for several flows:

- **Keycloak password reset** — the link a user clicks when they hit "Forgot password" on `login.<your-domain>`.
- **Keycloak user verification** — the welcome email a new user gets when an admin invites them.
- **Kube-DC organization invites** — when an org owner adds a member to a Project.

All of these go through a single SMTP relay. For most installs that relay is **Gmail / Google Workspace** — it's free for small volumes, lands in inboxes (not spam), and doesn't require a dedicated email provider account.

Google removed "less secure app passwords" in 2022, so the **only** way to use SMTP with a Google account today is via an **App Password** + 2-Step Verification on the account. This guide walks the operator (you) through generating one and handing the credentials to your Kube-DC administrator.

> **Time:** ~3 minutes.
> **Cost:** free.
> **Audience:** the operator who will own the "from address" (e.g. `noreply@yourcompany.com`). You do this once per Kube-DC cluster.

## Before you start

You need:

1. A Google account that will appear in the `From:` header of every email Kube-DC sends. Two reasonable choices:
   - A **Google Workspace** mailbox you control (e.g. `noreply@yourcompany.com`). Recommended for production — the sender domain matches your company and customers don't see `@gmail.com`.
   - A **personal Gmail address** (e.g. `acme.kube-dc@gmail.com`). Fine for staging or proof-of-concept clusters.
2. **Permission to enable 2-Step Verification** on that account. If it's a Workspace account, your domain admin must allow 2SV — most do by default.

You do **not** need: a paid Google Workspace tier (the free / personal Gmail SMTP relay works for kube-dc bootstrap volumes — Google's published limit is 500 messages/day for personal accounts, 2000/day for Workspace).

## Step 1 — Enable 2-Step Verification

App Passwords are only available when 2SV is on.

1. Sign in to the Google account you want to use.
2. Open **[myaccount.google.com/security](https://myaccount.google.com/security)**.
3. Under **How you sign in to Google**, click **2-Step Verification**.
4. Follow the wizard. The minimum setup is a phone number that receives a verification code; you can add Authenticator app or hardware key afterwards.

When you're done the page shows **"2-Step Verification is on"**. You can close that tab.

## Step 2 — Generate the App Password

1. Open **[myaccount.google.com/apppasswords](https://myaccount.google.com/apppasswords)** (if the link redirects you back to Security, 2SV isn't fully enabled yet — go back to Step 1).
2. Under **App name**, enter something descriptive, e.g. **`Kube-DC SMTP - cloudacropolis`**. Use the cluster name so you can revoke this specific token later without affecting other clusters.
3. Click **Create**.
4. Google shows a **16-character password** in a yellow box, formatted like `abcd efgh ijkl mnop`.

**Copy it now** — Google does not show it again. If you lose it, just delete the entry from the App Passwords list and create a new one.

The spaces are cosmetic; you may copy them or strip them, both work.

## Step 3 — Hand the values to the Kube-DC administrator

Send your Kube-DC administrator these six values. **Treat the password like any other API key**: use 1Password, your team's secret manager, or an end-to-end encrypted channel (Signal, ProtonMail, etc.) — not plain email or Slack.

| Field | Value | Example |
|---|---|---|
| **SMTP host** | `smtp.gmail.com` | (same for everyone) |
| **SMTP port** | `587` | (same for everyone; STARTTLS) |
| **SMTP user** | The full Google account email | `noreply@yourcompany.com` |
| **SMTP password** | The 16-char App Password from Step 2 | `abcdefghijklmnop` |
| **From address** | What recipients see in their `From:` header | `noreply@yourcompany.com` |
| **From name** | Display name in the `From:` header | `YourCompany Cloud` |

For most Google setups, **SMTP user** and **From address** are identical — Google's SMTP relay rejects messages whose `From:` header doesn't match the authenticated account (this is anti-spam policy, not a Kube-DC limitation). If you want to send from a different domain, you need a Workspace account on that domain + the matching App Password.

## Step 4 — Verify it works (optional, but recommended)

Before handing the credentials over, you can sanity-check them from any Linux/macOS shell with `swaks` or `curl`:

```bash
# swaks (apt install swaks)
swaks \
  --to YOUR_PERSONAL_EMAIL@example.com \
  --from noreply@yourcompany.com \
  --server smtp.gmail.com:587 \
  --tls \
  --auth LOGIN \
  --auth-user noreply@yourcompany.com \
  --auth-password 'abcdefghijklmnop'

# or with curl
curl --url 'smtp://smtp.gmail.com:587' \
  --ssl-reqd \
  --mail-from 'noreply@yourcompany.com' \
  --mail-rcpt 'YOUR_PERSONAL_EMAIL@example.com' \
  --user 'noreply@yourcompany.com:abcdefghijklmnop' \
  --upload-file <(printf 'Subject: Kube-DC SMTP test\r\n\r\nIt works.\r\n')
```

If the email arrives, the credentials are good. If you get **535 5.7.8 Username and Password not accepted**, the App Password is wrong — go back to Step 2.

## What the Kube-DC administrator does with these values

They populate the `master-config` Secret in the cluster:

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
  smtp_user: "noreply@yourcompany.com"
  smtp_password: "abcdefghijklmnop"   # the App Password (no spaces)
  smtp_from: "noreply@yourcompany.com"
  smtp_from_name: "YourCompany Cloud"
```

Once the controller picks up the change, it propagates SMTP settings to **every organization's Keycloak realm** automatically — both new realms created after the change and existing ones (the controller detects SMTP config drift and applies updates). Users in any org can immediately use "Forgot password", admins can invite members, etc.

The architecture is documented in [docs/prd/keycloak-smtp-configuration.md](../prd/keycloak-smtp-configuration.md) (internal).

## Rotating, revoking, or replacing the password

App Passwords are revocable per-token. To rotate:

1. Open **[myaccount.google.com/apppasswords](https://myaccount.google.com/apppasswords)**.
2. Find the entry you created (e.g. `Kube-DC SMTP - cloudacropolis`).
3. Click the trash icon to delete it.
4. Create a new one with the same naming convention.
5. Send the new value to your administrator; they re-update the `master-config` Secret. The controller picks up the change on the next reconcile (≤ 30s).

This is the recommended pattern for any operator handover, security audit, or after an incident.

## Troubleshooting

| Symptom | Likely cause | Fix |
|---|---|---|
| `535 5.7.8 Username and Password not accepted` in Keycloak server logs | Password is wrong, OR 2SV was disabled and re-enabled (this revokes all App Passwords). | Generate a fresh App Password (Step 2). |
| `530 5.7.0 Must issue a STARTTLS command first` | Port is set to `465` instead of `587`. | Set `smtp_port: 587`. Port 465 needs implicit TLS, which Kube-DC's SMTP client doesn't use. |
| Emails sent but recipients never receive them | Gmail throttled the account — most likely the SMTP user is a personal Gmail account hitting the 500/day limit. | Upgrade to Google Workspace (2000/day per user), or switch to a transactional-email provider like AWS SES / Postmark for high volume. |
| `From:` address rejected (`555 5.5.2 Syntax error` or arrives as `noreply@gmail.com` instead of your domain) | SMTP user is a personal Gmail; Google rewrites the `From:` to the authenticated account on personal Gmail. | Use Google Workspace — only Workspace accounts can send with a custom `From:` domain. |
| Keycloak's "Test connection" button in Realm Settings → Email passes but real password-reset emails never go out | The SMTP test uses **port 587** but the realm-level SMTP config can override; double-check the saved realm config matches `master-config`. | Re-apply via `kube-dc bootstrap keycloak init <cluster>` or edit the realm under Keycloak admin UI. |

## Alternatives

Gmail / Workspace is the easiest starting point but not the only option. The `master-config` Secret accepts any SMTP relay:

- **AWS SES** — `email-smtp.us-east-1.amazonaws.com:587`, IAM-issued SMTP credentials. Higher daily limit, lower deliverability friction for production.
- **Postmark / SendGrid / Mailgun** — transactional email providers with native templating and bounce tracking. Better suited for high-volume customer-facing email; usually not needed at the kube-dc-cluster operator scale.
- **Your own postfix relay** — if your company already operates one, point Kube-DC at it. Set `smtp_user` and `smtp_password` to empty if the relay accepts internal traffic without auth.

The Gmail App Password flow above is documented because it's the path operators most often start with. All other relays use the same Secret shape; only the host/port/user/password values change.
