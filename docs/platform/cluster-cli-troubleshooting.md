# Common Checks & Troubleshooting

Daily-driver health checks first, then a catalogue of errors with fixes.

## Common checks

### "Is my fleet healthy?"

```bash
kube-dc bootstrap
# expect: every row Ready (or Reconciling during a deploy)
```

### "Can I reach this cluster?"

```bash
kube-dc bootstrap kubeconfig <cluster>     # writes the context
kube-dc login --domain <domain> --admin    # mints OIDC tokens
kubectl --context kube-dc/<domain>/admin get nodes
```

### "Who am I on this cluster?"

```bash
kube-dc bootstrap context
# select the row, read the right pane: email + groups + expiry
```

Or one-shot (Kubernetes ≥ 1.28):

```bash
kubectl auth whoami
```

### "Is my admin login wired up correctly?"

```bash
# 1. master OpenIDConnect CR exists with correct audience + prefix?
kubectl get openidconnect master -o jsonpath='{.spec.audiences}{"  "}{.spec.usernamePrefix}{"\n"}'
# expect: ["kube-dc-admin"]  platform:

# 2. ClusterRoleBinding present?
kubectl get clusterrolebinding platform-admin -o yaml | grep -A3 subjects

# 3. The kube-dc-admin OIDC client exists?
KEYCLOAK_URL=https://login.<domain>
# Manual: open ${KEYCLOAK_URL}/admin → master → Clients → kube-dc-admin

# 4. The gardener oidc-webhook-authenticator pod is Ready on each CP node?
kubectl -n oidc-webhook-authenticator get pods -o wide
```

### "What contexts has the CLI written?"

```bash
kubectl config get-contexts | grep kube-dc/
# Or visually:
kube-dc bootstrap context
```

### "How do I drop everything kube-dc and start clean?"

```bash
kube-dc logout --all --remove-contexts
```

This deletes every cached token in `~/.kube-dc/credentials/` and every `kube-dc/*` context in `~/.kube/config`. Non-kube-dc contexts (`kubectx`-managed, vendor exec plugins) are untouched.

---

## Troubleshooting

### "not logged in" / "session expired"

The exec plugin can't find a valid cached token. Run the matching login again:

```bash
# Organization login, followed by an explicit Project selection
kube-dc login --domain <domain> --org <org>
kube-dc use <domain>/<org>/<project>

# admin
kube-dc login --domain <domain> --admin
```

The error message always contains the right command — copy-paste it.

### `kube-dc bootstrap` says all clusters are `Unreachable`

You haven't logged in to any of them yet. The probe needs an OIDC bearer token to query the apiserver. Run `kube-dc login --admin` for one cluster, hit `r` in the fleet view, and that row should turn `Ready`.

### `kube-dc login --admin` fails with "user is authenticated but NOT in the 'admin' group"

The OAuth flow worked but Keycloak says you're not a platform admin. Ask someone with Keycloak access to add you to the master realm's `admin` group (see [Adding a new admin](cluster-cli-admin-login.md#adding-a-new-admin-one-time-per-person)).

### Browser shows "We are sorry... Client not found" (and the CLI hangs)

The cluster's master realm does not have the `kube-dc-admin` PKCE OIDC client yet. Review and run the setup script from the Fleet version deployed to this cluster:

```bash
cd <fleet-repo-path>
git pull
export KUBECONFIG=~/.kube/<cluster>_config
bash bootstrap/setup-keycloak-oidc.sh <cluster>
```

Then retry `kube-dc login --domain <domain> --admin`. The script reconciles known stale loopback-callback configurations. Review identity changes before applying them to a production realm and verify the existing platform clients afterward.

Verify the client now exists by probing the auth endpoint:

```bash
curl -s -o /dev/null -w "%{http_code}\n" \
  "https://login.<domain>/realms/master/protocol/openid-connect/auth?response_type=code&client_id=kube-dc-admin&redirect_uri=http%3A%2F%2Flocalhost%3A55432%2Fcallback&state=t&scope=openid&code_challenge=abc&code_challenge_method=S256"
# 302 → client exists, redirect accepted
# 400 → client missing OR redirect rejected
```

### Browser shows "Invalid parameter: redirect_uri"

The `kube-dc-admin` client and the CLI disagree about the loopback callback.
Re-run the version-matched `bootstrap/setup-keycloak-oidc.sh <cluster>`, then
inspect the client in Keycloak and retry the login.

Do not add an unrestricted redirect URI as an ad hoc workaround. PKCE protects
the authorization code, but it does not make redirect-URI validation optional.
Keep this public client limited to loopback callbacks supported by the shipped
CLI, and do not add web origins or production hostnames to it.

### `kubectl get nodes` says forbidden under `--admin`

The OIDC chain is fine but the cluster-side RBAC isn't wired. Check ["Is my admin login wired up correctly?"](#is-my-admin-login-wired-up-correctly) above — usually the `platform-admin` `ClusterRoleBinding` hasn't reconciled yet.

### A cluster row shows `Drifted`

The image reference pinned in `cluster-config.env` differs from what is
running. Treat the Fleet repository as desired state:

1. Confirm the intended immutable tag or digest in `cluster-config.env`.
2. Commit and push the correction.
3. Run `flux reconcile kustomization platform --with-source`.
4. Verify that the Deployment and Fleet pin converge.

A direct `kubectl set image` is incident-only drift. Record it, then either
commit the same reference to Fleet or let Flux restore the reviewed value.

### My `~/.kube/config` got broken

Login and logout operations target Kube-DC context-name patterns. The Contexts
tab is broader: pressing `d` deliberately deletes whichever row is selected,
including an `EXTERNAL` context, and does not ask for confirmation.

Restore your most recent kubeconfig backup, identify the operation that changed
the file, and include a redacted before/after diff when reporting a CLI issue.

### "I logged in but kubectx doesn't show the new context"

The likely cause is **`$KUBECONFIG` leaking from a previous step**. If you ran something like:

```bash
export KUBECONFIG=~/.kube/<cluster>_kubeconfig_tunnel    # for a fleet-bootstrap step
bash bootstrap/setup-keycloak-oidc.sh <cluster>
kube-dc login --domain <domain> --admin   # ← context lands in <cluster>_kubeconfig_tunnel, NOT ~/.kube/config
```

…then the new context is in whatever file `$KUBECONFIG` pointed at, not in `~/.kube/config`. `kubectx` reads `~/.kube/config` by default.

**Recent versions of `kube-dc login` print a banner + confirmation prompt** when `$KUBECONFIG` points at anything other than `~/.kube/config`:

```
  ┌─ kubeconfig destination ─
  │  $KUBECONFIG = /home/<you>/.kube/<cluster>_kubeconfig_tunnel
  │  → writing to: /home/<you>/.kube/<cluster>_kubeconfig_tunnel
  │  (default would be /home/<you>/.kube/config — kubectx reads from there)
  └──
  Continue writing to this file? [y/N]
```

In a non-interactive shell (CI, IDE-launched processes) the banner still prints but the command proceeds without prompting.

**Recovery path** if you ended up here without seeing the prompt:

```bash
# 1. Back up
cp ~/.kube/config ~/.kube/config.before-recovery.$(date +%Y%m%dT%H%M%S)

# 2. Merge the stray contexts back in
KUBECONFIG=~/.kube/config:~/.kube/<the-stray-file> \
  kubectl config view --raw --flatten > /tmp/merged.config
mv /tmp/merged.config ~/.kube/config
chmod 0600 ~/.kube/config

# 3. Unset KUBECONFIG so future logins go to the default
unset KUBECONFIG
```

### "I want to debug what kube-dc is doing"

```bash
# Show the cached creds + expiry for every server
kube-dc config show

# Print the ExecCredential the plugin emits (without going through kubectl)
kube-dc credential --server https://kube-api.<domain>:6443 --realm master
```

### Decode a cached JWT to see what the apiserver actually receives

When admin login succeeds but `kubectl get nodes` returns 401, decode the token and look at the actual claims:

```bash
TOKEN_FILE=$(ls -t ~/.kube-dc/credentials/*-master.json 2>/dev/null | head -1)
python3 -c "
import json, base64
t = json.load(open('$TOKEN_FILE'))['access_token']
p = t.split('.')[1] + '=' * (-len(t.split('.')[1]) % 4)
c = json.loads(base64.urlsafe_b64decode(p))
print('iss:    ', c['iss'])
print('aud:    ', c.get('aud'))
print('azp:    ', c.get('azp'))
print('groups: ', c.get('groups'))
print('email:  ', c.get('email'))
"
```

The most common 401 cause: `aud` doesn't include `kube-dc-admin`. That means the **audience mapper** wasn't attached to the client in Keycloak — re-run `setup-keycloak-oidc.sh <cluster>` to add it, then `kube-dc login --domain <domain> --admin` again to mint a token with the new audience.

### kubelet image cache trap (when `kubectl set image` doesn't actually update the pod)

**Symptom**: you push a new image, run `kubectl set image`, the pod rolls — but the new pod is still running the OLD binary. Verified by comparing image digests:

```bash
# What the pod actually pulled:
kubectl get pod -n kube-dc -l app.kubernetes.io/name=kube-dc-manager \
  -o jsonpath='{.items[0].status.containerStatuses[0].imageID}'

# What's in the registry NOW:
docker manifest inspect --verbose <registry>/<image>:<tag> \
  | python3 -c "import json,sys; print(json.load(sys.stdin)['Descriptor']['digest'])"
```

If they differ, the Deployment uses `imagePullPolicy: IfNotPresent` and the
tag was reused on a node. Publish a new immutable tag or select the reviewed
digest, update the corresponding Fleet pin, commit, push, and reconcile Flux.
Do not make `kubectl set image` the durable fix: it creates drift and Flux can
replace it on the next reconciliation.
