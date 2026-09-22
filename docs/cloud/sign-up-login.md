# Sign up and sign in

To use Kube-DC, you need a verified user account and membership of an
Organization. This guide covers account creation, Organization setup, and the
sign-in options.

## Before you begin

You need:

- A valid email address
- A current web browser: Chrome, Firefox, Safari, or Edge

## Create your account

1. Go to [console.kube-dc.cloud](https://console.kube-dc.cloud).
2. Click **Sign up with email** or **Sign up with Google**.
3. Fill in the registration form:

   - **Email**: your email address. Kube-DC uses it for sign-in and
     notifications.
   - **First name**: your first name.
   - **Last name**: your last name.

4. Click **Register**.
5. Open the verification email and confirm your account.

:::tip Google single sign-on
If you sign up with Google, Kube-DC verifies your account automatically. Go
straight to Organization setup.
:::

## Set up your Organization

After registration, the console asks you to set up or join an Organization. An
Organization is the identity, membership, billing, and shared-quota boundary
for its Projects.

### Create an Organization

Follow these steps if you start fresh, or if you need a separate Organization:

1. Enter a unique **Organization name**, such as your company or team name.
2. Wait for the availability check. A green checkmark means the name is free.
3. Set your **Organization login password**. It needs at least 8 characters.
4. Confirm the password.
5. Click **Continue**.

:::note Organization Admin
The user who creates an Organization becomes its Organization Admin, with full
access to users, Projects, and settings. Keep your Organization login password
secure. It belongs to your own user account. Organization members do not share
it.
:::

### Join an existing Organization

Follow these steps if your team already has an Organization:

1. Enter the existing **Organization name**.
2. Read the message "Organization exists. You can request to join."
3. Click **Request to join**.
4. Wait for an Organization Admin to approve the request.

An Organization Admin must approve every join request. Kube-DC sends you an
email when the request is processed.

## Sign in

1. Go to [console.kube-dc.cloud](https://console.kube-dc.cloud).
2. Enter your **Organization** name.
3. Choose a sign-in method:

   - **Log in**: enter your Organization login password.
   - **Sign in with Google**: use Google single sign-on, if your Organization
     has it configured.

![Login page](images/sign-up-4.png)

:::tip Bookmark your Organization
For faster access, bookmark your Organization's direct sign-in URL:
`https://console.kube-dc.cloud/?realm=ORGANIZATION_NAME`. Replace
`ORGANIZATION_NAME` with your Organization name.
:::

## Choose a plan or a trial

Subscription and trial options depend on the Kube-DC deployment and its billing
provider. Some providers offer a trial with the first subscription. A valid
promo code or an administrator can also enable trial access. The Billing screen
shows your eligibility and the duration before checkout.

After you create the Organization, open **Billing** to:

- Choose one of the plans your provider offers
- Redeem a promo code, if you have one
- Review the CPU, memory, storage, IP, and accelerator quota before checkout
- See the subscription state and the remaining trial time, if a trial applies

The Billing screen is the source of truth for plan capacity and commercial
terms. For quota and subscription details, see
[Billing and usage](billing-usage.md).

## Next steps

- [Explore the dashboard](dashboard-overview.md) to learn how to navigate the
  Kube-DC console
- [Create your first Project](first-project.md)
- [Set up user groups and permissions](team-management.md)
- [Configure Google SSO for your Organization](/platform/sso-google-auth)
