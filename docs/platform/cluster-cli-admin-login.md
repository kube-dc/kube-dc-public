# Platform Admin Login

`kube-dc login --admin` is the daily-driver for cluster operators. Cluster-wide RBAC via the Keycloak master realm's `admin` group, with audit logs that reflect each engineer's email — never a shared kubeconfig.

```bash
kube-dc login --domain <cluster-domain> --admin
```

What happens:

1. A browser opens — same flow as tenant login, but against `https://login.<domain>/realms/master` and the `kube-dc-admin` PKCE OIDC client.
2. The CLI verifies your JWT carries the `admin` group (master realm). If it doesn't, the login is refused with a clear hint to ask a Keycloak admin to add you.
3. Tokens land at `~/.kube-dc/credentials/<server>-master.json` (separate file from any tenant tokens for the same cluster — they coexist).
4. A single context `kube-dc/<domain>/admin` is added; current-context is switched to it.

Verify:

```bash
kubectl config current-context           # → kube-dc/<domain>/admin
kubectl get nodes                         # cluster-admin
kubectl auth can-i '*' '*' --all-namespaces  # → yes
```

The audit log entry on the apiserver shows your email — not a shared service-account token.

---

## Adding a new admin (one-time per person)

The admin RBAC chain is:

> **one** Keycloak group (`admin` in master realm) → **one** K8s group (`platform:admin`) → **one** `ClusterRoleBinding` (`platform-admin` → `cluster-admin`).

There's no per-person fleet-repo change; granting admin to a new engineer is a Keycloak group-membership edit, nothing more.

### Via the Keycloak UI

1. Open `https://login.<cluster-domain>/admin` → **master** realm → **Users** → **Add user**.
2. Set username + email (the email is what `kubectl auth whoami` displays as `Username`).
3. **Credentials** tab → set the password (uncheck "Temporary" if they don't need to rotate on first login).
4. **Groups** tab → **Join** the `admin` group.
5. Have them run `kube-dc login --domain <cluster-domain> --admin`.

### Via `kcadm`

Run from any host with the Keycloak admin password (or from a pod with `kcadm.sh` available):

```bash
kcadm.sh config credentials --server https://login.<cluster-domain> \
  --realm master --user admin --password "<master-admin-pass>"

kcadm.sh create users -r master \
  -s username=engineer -s email=engineer@example.com -s enabled=true

kcadm.sh set-password -r master --username engineer --new-password "<temp>"

# Look up the admin group's UUID once, then add the user
GID=$(kcadm.sh get groups -r master -q search=admin --fields id,name | jq -r '.[]|select(.name=="admin").id')
UID=$(kcadm.sh get users -r master -q username=engineer --fields id | jq -r '.[0].id')
kcadm.sh update users/$UID/groups/$GID -r master -n
```

### Verifying it worked

After the new admin runs `kube-dc login --domain <cluster-domain> --admin`, both of these should succeed:

```bash
# Identity check — Username should be their email, Groups should include platform:admin
kubectl auth whoami
# ATTRIBUTE   VALUE
# Username    platform:engineer@example.com    ← email from the JWT
# Groups      [platform:admin system:authenticated]

# RBAC check
kubectl auth can-i '*' '*' --all-namespaces   # → yes
```

If `Username` is correct but `Groups` is missing `platform:admin`, the user isn't actually in the Keycloak `admin` group — re-check the **Groups** tab on the user.

If `Username` is correct AND `Groups` contains `platform:admin` but `kubectl get nodes` returns 403, the `platform-admin` `ClusterRoleBinding` isn't on the cluster yet. Check Flux:

```bash
kubectl get kustomization -n flux-system core
# Should be Ready=True. If not, `flux reconcile kustomization core --with-source`.
```

### Removing an admin

Remove the user from the `admin` group:

```bash
kcadm.sh delete users/$UID/groups/$GID -r master
```

Their next OIDC token refresh (≤5 min, governed by Keycloak's access-token lifespan) will land without the `groups: ["admin"]` claim, and the apiserver will start refusing privileged calls. To revoke immediately, delete the user account entirely (`kcadm.sh delete users/$UID -r master`) — this invalidates outstanding refresh tokens too.

---

## Pre-flight on a fresh cluster

The CLI side works against any cluster, but a fresh cluster needs **four** pieces wired up before `kube-dc login --admin` resolves:

1. **The `kube-dc-admin` PKCE OIDC client** in the master realm.
2. **Two protocol mappers on that client**:
   - `groups` (`oidc-group-membership-mapper`, `full.path: false`) → JWT carries `groups: ["admin"]`
   - `audience` (`oidc-audience-mapper`, `included.client.audience: kube-dc-admin`) → JWT carries `aud: [..., "kube-dc-admin", ...]`. **Without this the apiserver rejects every token with 401** even though group + claim mapping look correct — the audience-validation path is silent in apiserver logs and easy to miss.
3. **The master-realm JWT issuer in `auth-conf.yaml`** (the kube-dc-manager controller adds this on startup).
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

# 4. Roll the kube-dc-manager so its updated ReconcileAll runs and adds the
#    master-realm issuer to auth-conf.yaml. If the running manager image is
#    behind the version in your tree, push a new image first.
kubectl rollout restart deployment/kube-dc-manager -n kube-dc
```

After step 4, watch for the auth-startup-sync line (~30s after restart, post-leader-election):

```bash
kubectl logs deployment/kube-dc-manager -n kube-dc --tail=50 | grep auth-startup-sync
# expect: auth startup sync complete   {"orgs": N, "added": 1, "removed": 0}
# "added: 1" means the master-realm entry was newly inserted on this run.
```

Verify the entry is there:

```bash
kubectl get configmap kube-dc-auth-config -n kube-dc \
  -o jsonpath='{.data.auth-conf\.yaml}' | grep -A4 "realms/master"
# expect: an entry with audiences: [kube-dc-admin] and claim prefix "platform:"
```

The `auth-config-sync` DaemonSet pushes the ConfigMap to `/etc/rancher/auth-conf.yaml` on every control-plane node within ~2 s; kube-apiserver picks up the change via fsnotify (no restart needed).
