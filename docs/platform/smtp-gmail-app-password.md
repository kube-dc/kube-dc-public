# Configure Gmail with a Google App Password

Kube-DC uses one cluster-wide SMTP configuration for email sent by Keycloak and the console backend. This includes:

- account setup, email verification, and password-reset messages from Keycloak;
- Organization join-request notifications; and
- join-request approval or denial messages.

This guide covers the password-based Google path supported by Kube-DC's current SMTP configuration: `smtp.gmail.com` with a Google App Password. It is not the only way to send through Google. Google says App Passwords are not recommended for most applications and should be used only when an application cannot use **Sign in with Google**. Kube-DC's SMTP client does not currently implement that OAuth flow.

For a production Google Workspace deployment, also consider Google's recommended [SMTP relay service](https://support.google.com/a/answer/176600?hl=en). For another provider, see [Alternatives](#alternatives).

> **Audience:** the operator who controls the sender account, such as `noreply@yourcompany.com`, and the Kube-DC administrator who configures the cluster.

## Before you start

You need:

1. A Google account that Kube-DC may use to send mail. For production, prefer an account controlled by your organization. A personal Gmail account may be suitable for limited evaluation, subject to your security policy and Google's limits.
2. Permission to enable **2-Step Verification** on that account. A Google Workspace administrator may restrict this setting.
3. Access to create an App Password for the account.

Google requires 2-Step Verification for App Passwords. Google also says the App Password option may be unavailable when:

- 2-Step Verification is configured only with security keys;
- the account is managed by a work, school, or other organization that restricts App Passwords; or
- the account is enrolled in Advanced Protection.

If the option is unavailable, contact your Google Workspace administrator or use another SMTP relay. See Google's [App Password help](https://support.google.com/accounts/answer/185833?hl=en) for the current requirements.

## Step 1: Enable 2-Step Verification

1. Sign in to the Google account that will send the messages.
2. Open [Google Account security](https://myaccount.google.com/security).
3. Under **How you sign in to Google**, select **2-Step Verification**.
4. Follow Google's prompts to configure an allowed second step.

When the account shows that 2-Step Verification is on, continue to the next step.

## Step 2: Create the App Password

1. Open [Google App Passwords](https://myaccount.google.com/apppasswords).
2. Enter a descriptive app name, such as **`Kube-DC SMTP - prod-1`**. Including the cluster name makes the credential easier to identify and revoke later.
3. Select **Create**.
4. Copy the generated 16-character App Password. Google shows it only once.

Remove the display spaces before adding the password to Kube-DC. If you lose it, revoke that entry and create a new one.

## Step 3: Give the SMTP values to the Kube-DC administrator

Use your organization's approved secret manager or encrypted handoff process. Do not send the App Password in plain email or commit it to Git.

| Field | Value | Example |
|---|---|---|
| **SMTP host** | Google's Gmail SMTP server | `smtp.gmail.com` |
| **SMTP port** | Submission port with STARTTLS | `587` |
| **Secure** | `false` for STARTTLS on port 587 | `false` |
| **SMTP user** | Full address of the account that created the App Password | `noreply@yourcompany.com` |
| **SMTP password** | App Password from Step 2, without spaces | `abcdefghijklmnop` |
| **From address** | Authenticated address or an alias allowed for that account | `noreply@yourcompany.com` |
| **From name** | Display name shown to recipients | `YourCompany Cloud` |

Using the authenticated address as the **From address** is the simplest configuration. If you use an alias, configure and verify it in Google first and confirm that your Workspace policy permits it. Google may rewrite or reject a sender address that the account is not allowed to use.

Google documents `smtp.gmail.com`, port `587`, and TLS for authenticated application sending in [Send email from a printer, scanner, or app](https://support.google.com/a/answer/176600?hl=en).

## Configure Kube-DC

The administrator should provide the credential through the installation's protected Helm or GitOps secret flow. For a chart-based deployment, the resulting values have this shape:

```yaml
backend:
  smtp:
    enabled: true
    host: "smtp.gmail.com"
    port: "587"
    secure: "false"
    user: "noreply@yourcompany.com"
    password: "<APP_PASSWORD_WITHOUT_SPACES>"
    from: "noreply@yourcompany.com"
    fromName: "YourCompany Cloud"
```

Do not treat a direct edit to the generated `master-config` Secret as durable configuration. The chart renders that Secret for the controller and also configures the console backend from the same `backend.smtp` values.

After the deployment reconciles, the Organization controller writes the SMTP settings into each Organization's Keycloak realm and corrects SMTP drift when that realm is reconciled. Propagation time depends on the deployment and reconciliation state, so verify both email paths before relying on them.

## Verify the configuration

1. Confirm that the updated Kube-DC manager and backend workloads are ready.
2. In the Keycloak Admin Console, select an Organization realm, open **Realm settings > Email**, and use **Test connection**.
3. Run an account-setup or password-reset flow for a test user.
4. If your deployment uses Organization join requests, submit and approve a test request to verify the console backend path too.

Successful SMTP authentication does not guarantee inbox placement. Google may filter or reject messages under its account, anti-abuse, and sender policies.

## Sending limits

Google's published limits depend on the account and sending method. Google currently documents:

- up to 500 outgoing messages per day for a standard personal Gmail account;
- up to 2,000 messages per day for a Google Workspace user account; and
- 500 messages per day for a Google Workspace trial account.

Recipient limits and anti-abuse controls also apply, and Google may change or temporarily reduce limits. Review the current [personal Gmail limits](https://support.google.com/mail/answer/22839?hl=en) and [Google Workspace sending limits](https://support.google.com/a/answer/166852?hl=en) before sizing a production service. Use a transactional email provider or an appropriately configured Workspace SMTP relay when these limits or policies do not fit your workload.

## Rotate or revoke the App Password

App Passwords can be revoked independently:

1. Open [Google App Passwords](https://myaccount.google.com/apppasswords).
2. Remove the entry for this cluster.
3. Create a replacement with the same naming convention.
4. Update the credential through the same protected Helm or GitOps path.
5. Reconcile the deployment and repeat the verification steps above.

Google revokes App Passwords when the Google Account password changes. If SMTP authentication starts failing after an account-password change, create and deploy a new App Password.

## Troubleshooting

| Symptom | Likely cause | What to do |
|---|---|---|
| The App Password option is missing | The account does not meet Google's requirements, or an administrator has restricted the feature. | Review [Google's availability conditions](https://support.google.com/accounts/answer/185833?hl=en), then contact the account administrator or choose another relay. |
| `535 5.7.8 Username and Password not accepted` | The regular account password was used, the App Password is incorrect or revoked, or the account policy blocks it. | Confirm the full account address, create a new App Password, remove its display spaces, and update the protected deployment value. |
| `530 5.7.0 Must issue a STARTTLS command first` | The port and TLS mode do not match. | For this guide, use port `587` with `secure: "false"`. Port `465` requires implicit TLS with `secure: "true"`. |
| Sending stops or messages are deferred | The account reached a sending or recipient limit, or Google applied an anti-abuse control. | Check the Google Account, Kube-DC logs, and the current sending-limit pages. Wait for Google to restore sending or move the workload to a suitable relay. |
| The sender is rejected or rewritten | The **From address** is not permitted for the authenticated account. | Use the authenticated address or a verified alias allowed by the Google Workspace administrator. |
| Keycloak mail works but join-request notifications do not | The Organization realm has the SMTP settings, but the console backend has not received or reloaded its configuration. | Confirm the backend rollout and inspect its logs for SMTP errors. |
| Join-request notifications work but Keycloak mail does not | The backend is configured, but the Organization realm has not converged or has a realm-specific SMTP problem. | Inspect **Realm settings > Email**, reconcile the Organization, and retry **Test connection**. |

Google maintains a reference of [Gmail SMTP errors and codes](https://support.google.com/mail/answer/3726730?hl=en) for server-side failures.

## Alternatives

Kube-DC accepts any SMTP relay that matches the same host, port, TLS, and optional username/password settings:

- **Google Workspace SMTP relay**: Google's recommended Workspace path for applications and devices. It requires Workspace administrator configuration and can authenticate trusted source IP addresses. Use `smtp-relay.gmail.com` and follow Google's [relay setup](https://support.google.com/a/answer/2956491?hl=en).
- **Transactional email provider**: services such as Amazon SES, Postmark, SendGrid, or Mailgun provide SMTP credentials. Use the endpoint, port, TLS mode, and sender-verification rules documented by that provider.
- **Organization-managed relay**: use an existing SMTP relay if your organization operates one. Leave the username and password empty only when the relay explicitly allows the cluster's source network to send without authentication.

App Passwords are documented here because they provide a supported username-and-password path for Kube-DC's current SMTP client. They are not a general recommendation over OAuth, a Workspace SMTP relay, or a dedicated transactional email service.
