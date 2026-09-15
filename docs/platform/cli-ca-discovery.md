# CLI API CA discovery

New CLI installations can log in without an existing kubeconfig or a manually
copied API CA. Browser and device login use the same trust setup for Organization
and platform-admin identities.

## Discovery contract

The backend serves this unauthenticated endpoint over its normal HTTPS route:

```text
GET https://backend.<domain>/.well-known/kube-dc-config
```

```json
{
  "apiVersion": "v1",
  "apiServer": "https://kube-api.example.com:6443",
  "certificateAuthorityData": "<base64-encoded certificate-only PEM CA bundle>"
}
```

The API address comes from the chart's `kubeApiExternalUrl` setting, passed as
`PROJECT_SHELL_API_URL`. Request Host and forwarding headers cannot change it.
The default CA source is Kubernetes' projected service-account `ca.crt`. It
contains public trust material, as described in the [Kubernetes service account
documentation](https://kubernetes.io/docs/reference/access-authn-authz/service-accounts-admin/#bound-service-account-token-volume).

The endpoint publishes only the schema version, API address, and CA certificates.
It does not read or publish service-account tokens, private keys, kubeconfigs, or
Keycloak credentials. Missing or invalid configuration returns HTTP 503 with
`{"error":"CLI_DISCOVERY_UNAVAILABLE"}`. Responses use `Cache-Control: no-store`;
the CA file is read on each request to pick up projected bundle rotation.

## Trust rules

1. An explicit `--ca-cert` bundle is authoritative and must verify the API.
   Operator bundles may include PEM annotations, complete certificate chains,
   or an explicitly trusted self-signed server certificate. The CLI removes
   annotations before caching or embedding the bundle and rejects private keys
   and other non-certificate PEM blocks.
2. Without that flag, a valid CA already configured for the same named API
   cluster is reused, preserving existing CA-file references for rotation. With
   no existing CA, or after an old CA fails verification, system roots are tried.
   Successful system verification clears stale CA data and file references.
3. If certificate trust still fails, the CLI requests discovery over HTTPS validated with
   system roots. No redirects are followed and no bearer token is sent.
4. The metadata must be version `v1`, name the exact expected API endpoint, and
   contain a valid certificate-only CA bundle. The CLI verifies the API's TLS
   certificate and hostname with that bundle before caching credentials or
   creating a context.
5. A newly supplied or discovered CA is embedded in kubeconfig for the API. It is not imported into system
   trust or used to authenticate Keycloak. Explicit `--insecure` skips discovery
   and writes the requested insecure context.

API verification honors the existing cluster's `tls-server-name` and `proxy-url`;
otherwise it uses the URL hostname and the normal HTTP proxy environment settings.
It sends a credential-free `GET /version`, follows no redirects, and accepts any
HTTP status after successful TLS verification, including 401/403. The complete
request is limited to eight seconds. Network failures stop login with the original
connection error rather than prompting the user to replace a CA.

`kube-dc bootstrap kubeconfig <cluster>` also clears stale private CA settings when
its API probe selects system trust. If that probe fails, existing CA settings are
preserved. HTTPS proxy failures are reported as proxy connection errors and do not
trigger API CA discovery. Explicit API bundles replace system roots;
for Keycloak and token refresh, `--ca-cert` supplements system roots so a private
API and publicly trusted Keycloak can be used together. This changes the older
behavior where supplying that flag also replaced Keycloak's root store.

For privately issued platform HTTPS certificates, the operator must supply an
out-of-band trusted bundle containing the required API and Keycloak CAs via
`--ca-cert`. Discovery never learns trust from an unverified HTTPS connection.

If the API is publicly issued or a working CA is already configured, login does
not depend on discovery. An older backend that returns 404 cannot bootstrap an
unknown private API CA; the CLI reports an actionable error asking for `--ca-cert`.

## External API CA override

If an external API TLS proxy uses a different CA from the projected cluster CA,
mount a certificate-only ConfigMap in the backend namespace:

```yaml
backend:
  cliDiscovery:
    caConfigMapName: external-api-ca
    caKey: ca.crt
```

The chart mounts that key at `/etc/kube-dc/api-ca/ca.crt` and sets
`KUBE_API_CA_FILE`. It uses a directory mount, so ConfigMap updates are projected
without restarting the pod. Do not place a private key in this bundle.

## Rollout and verification

Deploy the updated backend first, then distribute the updated CLI. The existing
backend HTTPS route already forwards `/.well-known/kube-dc-config`; no new
network exposure or Kubernetes RBAC is required. The service-account CA is
mounted by Kubernetes by default. Use the chart override only if needed.

```bash
curl --fail https://backend.example.com/.well-known/kube-dc-config
kube-dc login --domain example.com --org acme --device-code
kubectl get pods
```

Test on a machine with no existing kubeconfig or cached credentials. Login should
show a URL and code, accept browser approval, and write an embedded CA with TLS
verification enabled. Confirm automatic refresh works on subsequent kubectl
requests. `kube-dc ns` lists the user's accessible Project namespaces; tenant
accounts may not have permission for cluster-wide `kubectl get namespaces`.

Device authorization additionally requires the Keycloak client capability:
`oauth2.device.authorization.grant.enabled=true`. Tenant client reconciliation
sets it on new and existing realms; the fleet Keycloak bootstrap sets it on the
master-realm `kube-dc-admin` client. Deploy the updated controller and bootstrap
configuration, or enable that capability on existing clients before testing.

Rolling back the backend removes discovery but does not remove already embedded
CA certificates or invalidate cached sessions. A later API CA rotation is picked
up on login when the old bundle no longer verifies the API; during a planned
rotation, publish a bundle containing the current and replacement CAs.
