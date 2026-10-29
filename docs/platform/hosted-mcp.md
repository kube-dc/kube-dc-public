# Hosted MCP endpoint

Kube-DC can serve a Model Context Protocol (MCP) endpoint for every
Organization: AI apps (Claude, VS Code, Cursor and other MCP clients) connect
to `https://<backend host>/mcp/<organization>`, the user signs in on the
Organization's Keycloak realm, and every tool runs with that user's own
Kubernetes permissions.

## How it works

- The endpoint is the backend image's MCP server, its own Deployment
  (`kube-dc-backend-mcp`), Streamable HTTP without sessions, so it scales like
  the API. It holds no Kubernetes rights of its own.
- It publishes OAuth Protected Resource Metadata (RFC 9728) naming the
  Organization's realm. A token is accepted only from that realm, with the
  endpoint's URL in its audience and the `kube-dc:mcp` scope.
- For Kubernetes calls it never forwards the app's token. The realm's
  confidential client `kube-dc-mcp` exchanges it (OAuth token exchange,
  RFC 8693) for a token of the same user, so RBAC and the audit log see the
  person.
- `<org>/<project>` is the same endpoint with that project as the default of
  every tool's project argument; it has its own Protected Resource Metadata
  and accepts the Organization's token.
- Every tool call is an audit event (service `mcp`, action = tool name) in the
  backend's audit store (`backend.audit`), with the person, the app (the
  token's client, named from the manager's ConfigMap `kube-dc-mcp-app-names`)
  and the project. The MCP pods push it like the backend does, so a
  NetworkPolicy in front of the audit Loki must admit
  `app.kubernetes.io/name: kube-dc-backend-mcp` as well.
- The server's instructions and tool descriptions steer apps to the platform
  defaults: managed services (`create_managed_service`, development plan
  unless production/HA is asked for), web traffic through the shared Gateway
  (ClusterIP + `expose-route`, no EIP), public IPv4 only on request.
  `create_managed_service` calls the backend's own create path with the
  user's exchanged token: price quote (`/api/billing/managed-quote`) first,
  the order check, then the ServiceBinding. `whoami` reports master-config
  `base_domain` (env `MCP_BASE_DOMAIN`), the domain route hostnames use.
- `…/read_only` at the end of either URL is read-only use: its metadata lists
  only the `kube-dc:mcp:read` scope and only the read tools are offered. A
  token with only that scope is refused (`insufficient_scope`) on a full URL.
  The manager adds `kube-dc:mcp:read` to every realm (default optional scope)
  and to every app that already had `kube-dc:mcp`.
- The manager sets up each realm: the `kube-dc:mcp` and `kube-dc:mcp:read` client scopes, the
  `kube-dc-mcp` client and its secret (Secret `kube-dc-mcp-requesters` in the
  platform namespace), and the rules for apps that register themselves.

## App registration

Apps register in three ways, all limited per realm by the manager:

| Way | Rules |
|---|---|
| Client ID Metadata Documents | Metadata URLs only from `backend.mcp.cimdClientDomains` (default `claude.ai`, `vscode.dev`, `code.visualstudio.com`, `antigravity.google`) |
| Pre-registered apps | `backend.mcp.preregisteredApps`: registered under their metadata URL in every realm, because Keycloak cannot read their metadata document. Defaults: ChatGPT (`private_key_jwt` against its JWKS) and Codex (public, PKCE) |
| Dynamic Client Registration (fallback) | Redirect URIs only on `backend.mcp.trustedRedirectHosts` (default: `localhost`, `127.0.0.1`, `claude.ai`, `claude.com`, `chatgpt.com`, `vscode.dev`, `cursor.com`, `www.cursor.com`, `anysphere.cursor-mcp` (Cursor's OAuth callback), `global.consent.azure-apim.net` (Copilot Studio), `callback.mistral.ai`); no claim mappers; only default scopes, `kube-dc:mcp` and `kube-dc:mcp:read`; consent screen; at most 200 apps per realm |

Registered apps never get claim mappers: the claim that makes a token a
Kubernetes credential (`org`) is added only by the platform's own client.

## Enable it

1. Keycloak 26.6 or later with the experimental feature `cimd`: **append**
   `cimd` to the features the cluster already enables (for example
   `preview,organization` → `preview,organization,cimd`); do not replace the
   list, or features such as `admin-fine-grained-authz:v1` are lost. One
   Keycloak restart.
2. Chart values:

   ```yaml
   backend:
     mcp:
       enabled: true
       # publicURL: https://backend.<domain>/mcp   (default)
       # trustedRedirectHosts / cimdClientDomains / preregisteredApps: chart defaults (above)
       # metricsPort: 9464
       # rateLimit: { perUser: 600, perAddress: 1200 }   # requests a minute
   ```

   The default URL is routed by the backend's HTTPRoute (`/mcp` and
   `/.well-known/oauth-protected-resource/mcp`). A URL on another host needs a
   gateway listener, a certificate and its own route.
3. The manager configures every Organization realm on its next reconcile;
   Secret `kube-dc-mcp-requesters` gains one key per Organization. A manager
   whose realm setup changed (for example the read-only scope) sets every realm
   up again on its next reconcile.
4. If a NetworkPolicy guards the audit Loki, admit
   `app.kubernetes.io/name: kube-dc-backend-mcp` next to the backend.

## Operations

- **Metrics** on port `metrics` (9464): `kube_dc_mcp_requests_total{status}`,
  `kube_dc_mcp_tool_calls_total{tool,result,access}`,
  `kube_dc_mcp_token_exchange_failures_total`, `kube_dc_mcp_auth_denied_total`.
  With `backend.metrics.serviceMonitor.enabled` the chart adds a
  ServiceMonitor and the alerts `KubeDcMcpNoMetrics`, `KubeDcMcpServerErrors`
  and `KubeDcMcpTokenExchangeFailing`.
- **Rate limits**: per person and app, and per client address; refusals answer
  429 with `Retry-After`. `RATE_LIMIT_DISABLED=true` turns them off.
- **Audit**: every tool call (service `mcp`, action = tool), and refused tokens
  (action `auth.denied`, at most one a minute per Organization and address).
- **Revoking an app** stops new sign-ins at once; tokens already issued stay
  valid until they expire (the realm's access token lifespan, 15 minutes by
  default); exchanged Kubernetes tokens are reused for at most a minute.
- **Secrets**: `get_resources` masks Secret values unless the caller asks to
  reveal them (audited, never on read-only URLs).

## Kubernetes-enforced read-only (optional)

`backend.mcp.enforceReadOnly: true` makes read-only sessions read-only in
Kubernetes too, not only in the MCP server. Needs this chart's manager and
backend.

- A read-only session exchanges its token for audience `kube-dc-mcp-read`
  with scope `kube-dc:mcp-session-read`, which adds the claim
  `kube_dc_access=read`. The manager creates that client and scope in every
  realm.
- A second OpenIDConnect per realm, `mcp-read.<org>`, has the realm's spec
  with only that audience and `extraClaims: [kube_dc_access]`; it copies the
  claim to the user's extra (`gardener.cloud/user/kube_dc_access`). The
  realm's own entry and the `kube-dc` audience are unchanged, so no other
  token is affected.
- The ValidatingAdmissionPolicy `kube-dc-mcp-read-only` denies CREATE,
  UPDATE, DELETE and CONNECT (exec, attach, port-forward) for that extra.
- The MCP server fails closed: without the read token it refuses the session
  and never falls back to a full token.

Check: `kubectl get openidconnect mcp-read.<org>` and
`kubectl get validatingadmissionpolicy kube-dc-mcp-read-only`. Rollback:
`enforceReadOnly: false` (the manager stops creating the entries; the next
manager start removes them).

## Checks

- `curl https://<backend host>/.well-known/oauth-protected-resource/mcp/<org>`
  names `https://<keycloak>/realms/<org>`.
- An unauthenticated `POST https://<backend host>/mcp/<org>` answers 401 with
  `WWW-Authenticate: Bearer resource_metadata="…", scope="kube-dc:mcp"`.
- `kubectl -n kube-dc get pods -l app.kubernetes.io/component=mcp` shows the
  replicas ready.
- After a tool call, the project's audit log in the console (Security →
  Audit) shows a `mcp` event naming the app.

## Protocol

MCP revisions 2026-07-28 (stateless, per-request envelope, `server/discover`)
and 2025-11-25 / 2025-06-18 / 2025-03-26, on the official TypeScript SDK v2
(`@modelcontextprotocol/server`). One URL serves both eras: 2025-era requests
get a fresh server per request with JSON answers, as before; 2026-07-28
requests go through the SDK's per-request handler, never streamed. Browser
clients' `Mcp-Method`/`Mcp-Name` headers are allowed by CORS. Keycloak's own MCP support (CIMD, resource
indicators) is experimental upstream: pin the Keycloak version per release.
