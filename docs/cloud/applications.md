# Manage applications

An application is an app the platform builds from GitHub on every push (see
[Deploy from GitHub](deploy-from-github.md)). In the console open your
Project, then **Applications** in the navigation. The list shows each app with
its repository, address and state; **New application** creates one from a
repository, and **Connect GitHub** sets up builds step by step.

Click an application to open its page. **Open the app** opens its address;
**Redeploy** builds the branch's latest commit and deploys it. The tabs:

| Tab | What it shows and does |
|---|---|
| Overview | State, address and the latest build |
| Deploys | Every version with its commit. **Roll back** returns to the version before the current one; **Instant rollback** keeps the previous version running so a rollback takes a second (see [Roll back an application](rollback.md)) |
| Processes | Pods, the release command, the data volume, cron jobs (**Run now**, **Suspend**), the jobs of the last day with their logs, and autoscaling |
| Environment | Variables and the Secrets they come from. Values are edited in the editor dialog |
| Domains | The app's address and your own domain |
| Previews | One copy per pull request with its commit and expiry. **Keep longer** (24 hours, 3 or 7 days) and **Remove** |
| Logs | The app's log |
| Settings | Restart, Edit YAML, Delete application |

You need the developer role in the Project to change an application. If your
provider turned the section off, it is not shown.

## Use your own domain

The platform address uses the project namespace,
`<app>-<project-namespace>.<platform-domain>`. To use a custom address, open
**Domains** and select **Change address**.

1. Choose a suggested domain or select **Another domain…** and enter a hostname, such as `app.example.com`.
2. **Add the records shown at your DNS provider.** Each has copy buttons and
   an **In DNS** column that says whether the record is found; the page checks
   again every 15 seconds (or click **Check again**).
   - **CNAME** from your domain to the app's platform address. A bare domain
     (`example.com`) cannot have a CNAME: use A records to the addresses the
     page lists, or your provider's ALIAS/ANAME record. On Cloudflare, set the
     record to **DNS only** (grey cloud): the platform issues the
     certificate itself and cannot while Cloudflare proxies.
   - **TXT**, where your provider requires verified domains: it proves your
     organization controls the domain. An organization admin clicks
     **Verify** on this tab (or adds the domain under **Organization →
     Domains**) and picks how broad: verifying `example.com` also covers
     every name under it. Keep the TXT record; it is checked again daily. See
     [Verified domains](service-exposure.md#verified-domains).
3. **Save** once the records are found. The platform checks DNS again and
   refuses a domain that does not point at it yet. The certificate is then
   issued within 1 to 3 minutes; until then browsers warn.

An app answers on one address: saving a domain replaces the platform address,
which stops working. Choose **Use the platform address** to switch back.
If a custom address stops working after its certificate was issued, check
the DNS records on this tab. AI apps do the same with `deploy_app domain`.

### Many apps under one domain

To run many apps as `git.example.com`, `jira.example.com`, `app1.example.com`
and so on, connect the domain once for the whole organization. An
organization admin opens **Organization → Domains** and clicks **Connect a
domain**:

1. **Enter the domain** and choose where apps live:
   - **Under `apps.example.com`** (recommended): apps get names like
     `shop.apps.example.com`, and your website, mail and other names on
     `example.com` are untouched.
   - **Directly under `example.com`**: apps get names like `shop.example.com`.
     Any developer of your organization can then take names such as
     `admin.example.com`.

   **New apps get `<app>.<domain>`** is ticked; the organization's first
   domain becomes the default for new apps.
2. **Add the two records shown** at your DNS provider. Each has copy buttons
   and an **In DNS** state; the dialog checks every 10 seconds and says when
   the domain is ready:
   - **TXT** `_kube-dc.<domain>`: proves your organization controls it.
   - **Wildcard** `*.<domain>`: a CNAME to the platform name shown (or A
     records to the addresses shown, if your provider does not allow a CNAME
     there). No app under the domain then needs a record of its own.

   If your DNS panel adds the domain itself, enter only the part shown (for
   example `_kube-dc.apps` and `*.apps`). On Cloudflare, set both to **DNS
   only**. Records you already have (`@`, `www`, `mail`) keep working: the
   wildcard only covers names without a record of their own. The page also
   says when the wildcard points somewhere else, when an AAAA (IPv6) record
   must go, and when a CAA record keeps Let's Encrypt from issuing
   certificates.
3. **That's it.** New public apps get `<app>.<domain>` (or
   `<app>-<project>.<domain>` when another app has that name), and the
   **New application** sheet shows the address before you create the app.
   Existing apps keep their addresses; on an app's **Domains** tab your
   organization's domains are offered in one click.

If you replace an existing default, finish the wizard after the new domain is
verified. The previous default serves new apps until the replacement is ready.
If you close the wizard, select **Use for new apps** on the verified domain.
An explicit DNS record takes precedence over a wildcard. If a proposed app
hostname points to another site, keep that record and use the platform address
or choose a different hostname. **Check now** uses the installation's DNS
resolver. A previously missing record can remain cached until its DNS TTL expires.

The manager rechecks the TXT record daily after verification. If the record
disappears, the domain stays verified and existing apps keep serving. To revoke
that domain in Kube-DC, open **Organization → Domains**, clear **Use for new
apps** if it is the default, then choose **Remove domain** and review the apps
that will move. Keep or remove your public DNS records separately at your provider.

The **Applications** card on the project overview shows where new apps get
their address. The default changes nothing for existing apps, and any app can
still use another domain or go back to its platform address (**Remove
domain**). Apps created by `kubectl apply` or a CI workflow, rather than
**New application** or `deploy_app`, do not get the default, and previews
keep their platform addresses. Each name gets its own certificate.

Removing a domain lists the apps that use it, and is refused while it is the
default for new apps. Where your provider requires verified domains, those
apps stop being published when it goes.

### The main domain itself

One app can answer on the domain itself (`example.com`), with
`www.example.com` redirecting to it. Choose **`example.com` (main domain)**
on the app's **Domains** tab, or **Use `example.com` itself for an app** in
the domain's records dialog (pick the project and app). It is offered while no
other app has it.

The main domain needs its own records, shown on the page:

- `@` (the domain itself): A records to the addresses shown. If your provider
  offers ALIAS, ANAME or CNAME flattening (Cloudflare does), point it at the
  platform name shown instead, so a change of the platform's address cannot
  break it.
- `www`: a CNAME to the domain itself.

A website you run on the domain now moves to the app once you change these
records. The redirect for `www` belongs to the app: it goes when the app moves
to another address or is deleted.

## Autoscaling

**Processes → Autoscaling** runs between a minimum and a maximum number of pods
and adds pods while the average CPU use is above a share of the CPU limit, for
example "2–6 pods past 70% of the CPU limit". Turn it on, set the minimum,
the maximum and the CPU target, and click **Save**. Extra pods are billed like
the app's other pods.

Turning it off keeps the number of pods running at that moment.

Autoscaling is not available for:

- previews;
- apps with a data volume (one pod only);
- apps without a CPU limit (the target is a share of it);
- installations without CPU metrics.

An autoscaler that someone created outside the platform is shown, and
replacing it asks first. AI apps set the same thing with `deploy_app
autoscale`.

## Delete an application

**Settings → Delete application**, then type the app's name. This removes the
app, its address and route, its autoscaler, its cron jobs, the warm previous
version and all its previews with their databases (within a minute). Its data volume
(`<app>-data`) is kept unless you turn on **Also delete volume `<app>-data` and its data**.
The repository on GitHub and the images in the registry are not touched.
Pushes are no longer built once the app is gone.

AI apps delete an application with `delete` kind `app` (`deleteData` for the
volume), after you confirm.
