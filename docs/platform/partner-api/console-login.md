import Tabs from '@theme/Tabs';
import TabItem from '@theme/TabItem';

# One-click console login

Your customers never need a Kube-DC password. When a customer clicks "Open console" in your portal, your server mints a single-use login URL and redirects the browser to it.

## The flow

1. The browser calls a route on **your** portal, for example `/customers/crm-1042/console`.
2. Your server authenticates your user and checks that they may open this customer. This step is yours and it matters: the route logs the browser into the customer's console.
3. Your server calls `POST /customers/{id}/console-login` (scope `customers:sso`).
4. The response carries `url`, `expires_at`, `single_use` (always `true`) and `project`.
5. Your server answers the browser with a `302` redirect to `url`, immediately.

The URL is valid for **30 seconds** and works **once**. It is a credential:

- never store it, log it or put it in a page for later;
- never email it or send it in a chat message: it would have expired, and anyone who sees it in time is logged in;
- never mint it in advance. To put a console link in an email, link to your own portal route, which mints the URL when the link is clicked.

**Example — Step 3: One-click console login.** Select the SDK language; the choice is kept across pages.

<Tabs groupId="partner-sdk-language">
<TabItem value="typescript" label="TypeScript" default>

Tutorial file `examples/tutorial/03-console-login.ts` of the TypeScript SDK:

```ts
/**
 * Step 3 — One-click login: send your customer from your portal into the Kube-DC console.
 *
 * The browser never sees the API key. Your server mints a single-use login URL and
 * answers with a 302 redirect to it. Needs the `customers:sso` scope.
 */
import express from 'express';
import { KubeDC, ext, isNotFound } from '@kube-dc/partner-sdk';

const kdc = new KubeDC({ apiKey: env('KUBEDC_API_KEY'), baseUrl: env('KUBEDC_API_URL') });
const app = express();

// Your portal's "Open console" button links to GET /customers/crm-1042/console.
// Authenticate YOUR user first and check they may open this customer: this route logs them in.
app.get('/customers/:crmId/console', async (req, res, next) => {
  try {
    // ext('crm-1042') addresses the customer by the external_id you sent at create time.
    // No idempotencyKey here: a reused key would replay the same (already used) URL, so let the
    // SDK pick a new key for every click.
    const login = await kdc.customers.consoleLogin(ext(req.params.crmId));
    res.redirect(302, login.url); // single-use and valid for 30 s: never store, log or email it
  } catch (err) {
    if (isNotFound(err)) {
      res.status(404).send('Unknown customer');
      return;
    }
    next(err);
  }
});

app.listen(3000, () => console.log('Portal listening on http://localhost:3000'));

function env(name: string): string {
  const value = process.env[name];
  if (!value) throw new Error(`Set ${name}`);
  return value;
}
```

</TabItem>
<TabItem value="php" label="PHP">

Tutorial file `examples/tutorial/03-console-login.php` of the PHP SDK:

```php
<?php

/**
 * Step 3 — One-click login: send your customer from your portal into the Kube-DC console.
 *
 * A plain PHP front controller, e.g. public/console.php?customer=crm-1042.
 * The browser never sees the API key: the server mints a single-use URL and redirects.
 *   Laravel:  return redirect()->away($login->url);
 *   Symfony:  return new RedirectResponse($login->url);
 *   WHMCS:    ServiceSingleSignOn returns ['success' => true, 'redirectTo' => $login->url]
 */

declare(strict_types=1);

require __DIR__ . '/../../vendor/autoload.php';

use KubeDC\Partner\Client;
use KubeDC\Partner\Exception\NotFoundException;

// Authenticate YOUR user first and check they may open this customer: this script logs them in.
$crmId = $_GET['customer'] ?? '';
if (!is_string($crmId) || preg_match('/^[A-Za-z0-9._-]{1,128}$/', $crmId) !== 1) {
    http_response_code(400);
    exit;
}

$kdc = new Client(['api_key' => (string) getenv('KUBEDC_API_KEY'), 'base_url' => (string) getenv('KUBEDC_API_URL')]);

try {
    // 'ext:' addresses the customer by the external_id you sent at create time. Scope: customers:sso.
    // No idempotency key of your own here: a reused key would replay the same, already used URL.
    $login = $kdc->customers()->consoleLogin('ext:' . $crmId);
} catch (NotFoundException $e) {
    http_response_code(404);
    exit;
}

header('Location: ' . $login->url, true, 302);   // single-use, valid 30 s: never store, log or email it
exit;
```

</TabItem>
</Tabs>

## Request options

The body is optional:

| Field | Default | Notes |
|---|---|---|
| `user` | the owner, `admin` | The username to log in as. For users created through the API this is their email. It is trimmed and lowercased, and must match a user exactly. |
| `project` | the customer's first project | The project the console opens. |

## Users and owner access

Every customer has an **owner** user, `admin`, in the `org-admin` group. It is created with the customer, cannot be disabled, moved out of `org-admin` or deleted (`403` with `FORBIDDEN`), and is the default for console login. Map your portal's account owner to it.

To give other people their own console access, create users:

- `POST /customers/{id}/users` with `email` (it becomes the lowercased username), optional `first_name` and `last_name`, and a `group` (`org-admin` or `user`). Users are created enabled, with the email marked verified: you vouch for it.
- There is no password or invitation path. Users reach the console only through console login with `user` set to their email. A request with `send_invite: true` or any `password` is refused with `400`.
- `PATCH /customers/{id}/users/{user_id}` enables or disables a user or changes its group. `{user_id}` is the user's `id`, not the username.
- `GET /customers/{id}/users`, `GET /customers/{id}/users/{user_id}` and `DELETE /customers/{id}/users/{user_id}` read and remove users.
- Creating a user answers `409` with `ALREADY_EXISTS` when the email is taken in that customer, or `NOT_READY` while the customer is still provisioning.

Every login URL minted is recorded in the platform's audit log.

## Errors

| Code | Status | What to do |
|---|---|---|
| `NOT_READY` | 409 | The customer is still provisioning. Tell the user the environment is being prepared, and enable the button when the customer is `active`. |
| `USER_ACTION_REQUIRED` | 409 | The user must first complete an identity-provider action; the message names it. Show a clear message instead of retrying in a loop. |
| `NOT_FOUND` | 404 | No such customer, project or user. |
| `VALIDATION_ERROR` | 400 | `user` is not a non-empty string. |
| `FORBIDDEN` | 403 | Your token lacks `customers:sso`, or your partner account is suspended. |
