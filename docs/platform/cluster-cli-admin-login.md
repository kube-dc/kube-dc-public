# Platform admin login

`kube-dc login --admin` is the daily-driver for cluster operators. Cluster-wide RBAC runs through the Keycloak master realm's `admin` group, and the audit logs carry each engineer's email. There is no shared kubeconfig.

```bash
kube-dc login --domain <cluster-domain> --admin
```

What happens:

1. A browser opens. The flow is the same as tenant sign-in, but it runs against `https://login.<domain>/realms/master` and the `kube-dc-admin` PKCE OIDC client.
2. The CLI verifies your JWT carries the `admin` group (master realm). If it doesn't, the login is refused with a clear hint to ask a Keycloak admin to add you.
3. Tokens land at `~/.kube-dc/credentials/<server>-master.json`. That is a separate file from any tenant tokens for the same cluster, and the two coexist.
4. A single context `kube-dc/<domain>/admin` is added; current-context is switched to it.

Verify:

```bash
kubectl config current-context           # → kube-dc/<domain>/admin
kubectl get nodes                         # cluster-admin
kubectl auth can-i '*' '*' --all-namespaces  # → yes
```

The audit log entry on the apiserver shows your email, not a shared service-account token.

---

## Add an admin (once for each person)

There are two grants. They are separate decisions: give a person the one they need, and the smaller role where it is enough.

| Grant | In the Keycloak `master` realm | What it opens |
|---|---|---|
| Admin console | realm role `platform-admin` (read-mostly) or `superadmin` (everything, including managing the other administrators) | `https://admin.<cluster-domain>` |
| Cluster access | membership of the group `admin` | `kube-dc login --admin` and `kubectl` as `cluster-admin` (Kubernetes group `platform:admin` → `ClusterRoleBinding` `platform-admin`), the Flux Web UI, and in the default Grafana configuration `GrafanaAdmin` |

**Cluster access does not open the admin console, and a console role gives no cluster access.** A person with cluster access only who signs in to the console is refused with "This account has no platform administration role" (earlier backends: "Access denied: Required role(s): superadmin or platform-admin").

Cluster access is unrestricted: it reads every Secret of the installation. Give it to the people who operate the cluster, not to everyone who uses the console.

Neither grant needs a change in the fleet repository.

A new installation has one account with a console role: `init` gives the Keycloak `admin` user `superadmin`. Its password is in the `keycloak` Secret ([Installation guide](installation-guide.md#35-post-install-sso-clients-openbao-credentials)). Use it to add the first people, then keep it for recovery.

### Admin console access

1. Sign in to `https://admin.<cluster-domain>` as a superadmin.
2. Open **Platform access → Platform administrators** and choose **Create administrator**: the person's email, the role you decided on, and a password (temporary by default: it must be changed at the first sign-in), or a setup email when the realm has SMTP. For a person who already has a `master` realm account, assign the role to that account instead.
3. The person signs in to the admin console. Someone who was signed in when the role was assigned signs out and in again: the role is read from the token.

### Cluster access, in the admin console

Consoles that have the **Cluster access** option grant it on the same page; with an earlier console, use the Keycloak UI below.

- **Create administrator** → tick **Cluster access (kubectl as cluster-admin)**. It is off by default. With a password set in the dialog you must also confirm that the email address belongs to the person: the cluster identifies them by it and refuses an unverified address. With the setup email the person verifies the address themselves, and cluster access works once they have.
- For an existing account, choose **Cluster access** in the assign list. The account's email must already be verified; **Set password** with "mark email verified" is the way to vouch for it.
- The list shows who holds cluster access, and removing it there removes the membership of the `admin` group.

Where the installation's audit log is on, each grant and removal is sent to it as `cluster-access.grant` or `cluster-access.revoke`, with the account and the superadmin who did it. An event with result `error` means the operation as a whole did not complete after the membership change had been sent (the change itself, or a later step such as enabling a new account, failed or was not confirmed). The membership may or may not be in effect: check the account and its `admin` group membership. The console never creates the `admin` group: where an installation has none, the option is unavailable.

### Cluster access, in the Keycloak UI

1. Open `https://login.<cluster-domain>/admin` → **master** realm → **Users** and open the person's account. Create it if it does not exist: username and email (the email is what `kubectl auth whoami` displays as `Username`), and a password on the **Credentials** tab.
2. **Details** tab: **Email verified** must be on. The cluster takes the email as the username and refuses the token of an unverified address: `kube-dc login --admin` succeeds and `kubectl` then answers 401. An account created in the console with a password starts unverified, unless its email was confirmed in the dialog together with cluster access; one that completed the setup email is verified.
3. **Groups** tab → **Join** the `admin` group.
4. Have them run `kube-dc login --domain <cluster-domain> --admin`.

### With `kcadm`

Run from any host with the Keycloak admin password (or from a pod with `kcadm.sh` available). The three blocks are separate on purpose: the first creates the account, and you then run only the grant you decided on, or both.

**Create the account.** Run it as it is, with `set -euo pipefail`: when the account already exists, `create` fails, and nothing may then set a password on that account.

```bash
set -euo pipefail
kcadm.sh config credentials --server https://login.<cluster-domain> \
  --realm master --user admin --password "<master-admin-pass>"

EMAIL=engineer@example.com
# -i prints the ID of the account this command created.
# emailVerified=true only for an address you know is the person's.
KC_USER_ID=$(kcadm.sh create users -r master -i \
  -s username="$EMAIL" -s email="$EMAIL" -s emailVerified=true -s enabled=true)
kcadm.sh set-password -r master --userid "$KC_USER_ID" --new-password "<temp>" --temporary
echo "KC_USER_ID=$KC_USER_ID"
```

For an account that already exists, skip the block above: look the account up by its exact name, check that it is the person you mean, and set `KC_USER_ID` to its ID.

```bash
kcadm.sh get users -r master -q username="$EMAIL" -q exact=true --fields id,username,email,emailVerified
```

**Grant: admin console.** One role: `platform-admin`, or `superadmin`.

```bash
kcadm.sh add-roles -r master --uid "$KC_USER_ID" --rolename platform-admin
```

**Grant: cluster access.** Only for a person who operates the cluster.

```bash
KC_GROUP_ID=$(kcadm.sh get groups -r master -q search=admin -q exact=true --fields id,name \
  | jq -r '.[] | select(.name=="admin").id')
kcadm.sh update "users/$KC_USER_ID/groups/$KC_GROUP_ID" -r master -n
```

### Verify that it worked

The admin console opens on the platform overview after sign-in. For cluster access, after the new admin runs `kube-dc login --domain <cluster-domain> --admin`, both of these should succeed:

```bash
# Identity check: Username should be their email, Groups should include platform:admin
kubectl auth whoami
# ATTRIBUTE   VALUE
# Username    platform:engineer@example.com    ← email from the JWT
# Groups      [platform:admin system:authenticated]

# RBAC check
kubectl auth can-i '*' '*' --all-namespaces   # → yes
```

If `Username` is correct but `Groups` is missing `platform:admin`, the user is not in the Keycloak `admin` group. Re-check the **Groups** tab on the user.

If `Username` is correct AND `Groups` contains `platform:admin` but `kubectl get nodes` returns 403, the `platform-admin` `ClusterRoleBinding` isn't on the cluster yet. Check Flux:

```bash
kubectl get kustomization -n flux-system core
# Should be Ready=True. If not, `flux reconcile kustomization core --with-source`.
```

### Remove an admin

Remove every grant the person holds:

- the console role, on the **Platform administrators** page (it lists each administrator with the roles held);
- Grafana-only administrator access, on the same page, when it was assigned (the group `grafana-admins`);
- the membership of the `admin` group.

```bash
kcadm.sh get-roles -r master --uid "$KC_USER_ID"            # the realm roles held
kcadm.sh get "users/$KC_USER_ID/groups" -r master           # the groups held
kcadm.sh remove-roles -r master --uid "$KC_USER_ID" --rolename <role-held>   # for each of superadmin, platform-admin
kcadm.sh delete "users/$KC_USER_ID/groups/<group-id>" -r master              # for each of admin, grafana-admins
```

The admin backend also accepts a Keycloak group named `superadmin` or `platform-admin`, where an installation uses groups instead of roles: remove that membership too.

**When it takes effect.** Tokens already issued stay valid until they expire: the admin console and the cluster both read the grants from the token and do not ask Keycloak again. After the removal, the person's next token (≤5 min, governed by Keycloak's access-token lifespan) no longer carries the grant, and a fresh sign-in to the admin console is refused. Disabling the account (`kcadm.sh update "users/$KC_USER_ID" -r master -s enabled=false`) or deleting it (`kcadm.sh delete "users/$KC_USER_ID" -r master`) stops Keycloak from issuing it any new token, refreshes included; a token issued before that still works until it expires. Existing Grafana sessions end separately, in Grafana.

---

## Login from a headless machine

Both browser and device login [discover the API CA automatically](cli-ca-discovery.md)
on a fresh machine. The master-realm identity and permissions are independent of
the public certificate metadata.

```bash
kube-dc login --domain example.com --admin --device-code
```

Replace `example.com` with your platform domain.

The CLI displays a URL and code. Open the URL in a browser on another device,
enter the code, and approve the login. The same master-realm `admin` group
check, credential cache, and admin kubeconfig context apply.

The `kube-dc-admin` client in `master` must have **OAuth 2.0 Device Authorization
Grant** enabled. The updated fleet `bootstrap/setup-keycloak-oidc.sh` sets this
on both creation and update. For an existing installation, rerun that script
or enable the capability in Keycloak's client settings. Its JSON attribute is
`"oauth2.device.authorization.grant.enabled": "true"`.

The same client's **Valid redirect URIs** are `http://localhost:*` and
`http://127.0.0.1:*`: the browser login's loopback callback and nothing
else. The client is public, so this list alone decides where Keycloak returns
a login code. Clusters bootstrapped with an older fleet script carried `*`, which let
any site that could show an administrator the real login page collect a
platform-admin code. Rerunning `bootstrap/setup-keycloak-oidc.sh` converges an
installed cluster. Keycloak matches a trailing `*` as a string prefix, which is
why `http://localhost:*` admits the CLI's random port while `http://localhost/*`
does not.

## Pre-flight on a fresh cluster

The CLI side works against any cluster, but a fresh cluster needs **four** pieces wired up before `kube-dc login --admin` resolves:

1. **The `kube-dc-admin` PKCE OIDC client** in the master realm.
2. **Two protocol mappers on that client**:
   - `groups` (`oidc-group-membership-mapper`, `full.path: false`) → JWT carries `groups: ["admin"]`
   - `audience` (`oidc-audience-mapper`, `included.client.audience: kube-dc-admin`) → JWT carries `aud: [..., "kube-dc-admin", ...]`. **Without it the apiserver rejects every token with 401**, even when the group and claim mapping look correct. the audience-validation path is silent in apiserver logs and easy to miss.
3. **The `master` `OpenIDConnect.authentication.gardener.cloud/v1alpha1` CR** (kube-dc-manager creates this on startup with `audiences: [kube-dc-admin]` + `usernamePrefix: "platform:"`).
4. **The `platform-admin` `ClusterRoleBinding`** (`Group: platform:admin → cluster-admin`).

Install them like this:

```bash
# 1. From the fleet repo, on a workstation with admin (or break-glass) access:
cd <fleet-repo-path>
git pull
export KUBECONFIG=~/.kube/<cluster>-admin-or-break-glass

# 2. Apply the ClusterRoleBinding (or wait for Flux to reconcile it)
kubectl apply -f infrastructure/platform-admin/clusterrolebinding-platform-admin.yaml

# 3. Create the kube-dc-admin client + both protocol mappers in Keycloak (idempotent)
bash bootstrap/setup-keycloak-oidc.sh <cluster>

# 4. Make sure kube-dc-manager is running so its OIDCRegistry
#    ReconcileAll creates the master OpenIDConnect CR. Restart it if the CR is missing:
kubectl rollout restart deployment/kube-dc-manager -n kube-dc
```

Verify the master CR landed with the right shape:

```bash
kubectl get openidconnect master -o yaml | grep -A1 "audiences:\|usernamePrefix:"
# expect:
#   audiences:
#   - kube-dc-admin
#   usernamePrefix: 'platform:'
```

The gardener oidc-webhook-authenticator picks the CR up through informer within seconds and registers it in its in-memory issuer→authenticator map. There is no apiserver restart and no file sync. `kube-dc login --admin` resolves on the next attempt.
