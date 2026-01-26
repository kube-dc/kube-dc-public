# Kube-DC CLI - Browser-Based Authentication Tool

## Problem Statement

The current kubeconfig setup for Kube-DC users is cumbersome and requires multiple manual steps:

1. Copy-paste shell script from UI
2. Download and execute bash scripts
3. Manually enter environment variables
4. Source activation scripts
5. Enter username/password in terminal when tokens expire

This approach has several issues:

### Current Pain Points

| Issue | Impact |
|-------|--------|
| Shell-based scripts | Not cross-platform (bash/zsh only) |
| Password entry in terminal | Security risk (history, shoulder surfing) |
| Manual refresh prompts | Poor UX when tokens expire |
| Complex setup process | High barrier to entry for new users |
| No kubectx/kubens compatibility | Conflicts with popular tools |
| Overwrites existing kubeconfig | Risk of losing other cluster configs |

### User Expectations (Cloud CLI Patterns)

Users are accustomed to cloud provider CLIs that provide seamless authentication:

- **AWS CLI**: `aws sso login` → browser OAuth → seamless for weeks
- **GCloud**: `gcloud auth login` → browser OAuth → auto-refresh
- **DigitalOcean**: `doctl auth init` → browser OAuth → token cached
- **Azure**: `az login` → browser OAuth → device code flow option

Kube-DC should follow these established patterns.

## Requirements

### Functional Requirements

| ID | Requirement | Priority |
|----|-------------|----------|
| FR-1 | Browser-based OAuth login (PKCE flow) | High |
| FR-2 | Automatic token refresh using refresh_token | High |
| FR-3 | Exec credential plugin for kubectl integration | High |
| FR-4 | Support multiple organizations/realms | High |
| FR-5 | Preserve existing kubeconfig entries | High |
| FR-6 | kubectx/kubens compatibility | High |
| FR-7 | Namespace switching based on JWT claims | Medium |
| FR-8 | Context switching between projects | Medium |
| FR-9 | Offline token validation (JWT expiry check) | Medium |
| FR-10 | Device code flow for headless environments | Low |

### Non-Functional Requirements

| ID | Requirement | Priority |
|----|-------------|----------|
| NFR-1 | Single binary, no external dependencies | High |
| NFR-2 | Cross-platform (Linux, macOS, Windows) | High |
| NFR-3 | Credential storage with 0600 permissions | High |
| NFR-4 | < 20MB binary size | Medium |
| NFR-5 | < 100ms startup time for credential command | Medium |
| NFR-6 | Works without internet for cached tokens | Medium |

## Solution Design

### Architecture Overview

```
┌─────────────────────────────────────────────────────────────────────────────┐
│                              User Workflow                                   │
├─────────────────────────────────────────────────────────────────────────────┤
│                                                                              │
│   $ kube-dc login --server https://api.kube-dc.cloud                        │
│         │                                                                    │
│         ▼                                                                    │
│   ┌─────────────┐     ┌─────────────┐     ┌─────────────────────────┐       │
│   │ Start local │────►│ Open browser│────►│ Keycloak login page     │       │
│   │ HTTP server │     │ to auth URL │     │ (organization realm)    │       │
│   └─────────────┘     └─────────────┘     └───────────┬─────────────┘       │
│         ▲                                              │                     │
│         │              OAuth callback                  │                     │
│         └──────────────────────────────────────────────┘                     │
│                                                                              │
│   Tokens cached to ~/.kube-dc/credentials/                                   │
│   Kubeconfig updated in ~/.kube/config                                       │
│                                                                              │
├─────────────────────────────────────────────────────────────────────────────┤
│                                                                              │
│   $ kubectl get pods    ◄── Uses exec credential plugin                     │
│         │                                                                    │
│         ▼                                                                    │
│   ┌─────────────────────────────────────────────────────────────────┐       │
│   │  kubectl exec: kube-dc credential --server https://api...       │       │
│   │                      │                                          │       │
│   │     ┌────────────────┴────────────────┐                         │       │
│   │     ▼                                 ▼                         │       │
│   │  Token valid?                    Token expired?                 │       │
│   │     │                                 │                         │       │
│   │     ▼                                 ▼                         │       │
│   │  Return token               Use refresh_token                   │       │
│   │                                       │                         │       │
│   │                                       ▼                         │       │
│   │                             New tokens cached                   │       │
│   │                             Return access_token                 │       │
│   └─────────────────────────────────────────────────────────────────┘       │
│                                                                              │
└─────────────────────────────────────────────────────────────────────────────┘
```

### Directory Structure

```
cli/                                    # Source code location
├── cmd/
│   └── kube-dc/
│       └── main.go                     # Entry point
├── internal/
│   ├── auth/
│   │   ├── oauth.go                    # Browser-based OAuth flow
│   │   ├── pkce.go                     # PKCE code challenge
│   │   ├── callback.go                 # Local HTTP callback server
│   │   ├── token.go                    # Token refresh logic
│   │   └── device.go                   # Device code flow (optional)
│   ├── config/
│   │   ├── config.go                   # CLI config management
│   │   └── credentials.go              # Token storage
│   ├── kubeconfig/
│   │   ├── manager.go                  # Kubeconfig read/write
│   │   ├── merge.go                    # Merge with existing config
│   │   └── context.go                  # Context management
│   └── jwt/
│       └── claims.go                   # JWT parsing for namespaces
├── pkg/
│   └── credential/
│       └── exec.go                     # Exec credential plugin
├── go.mod
└── go.sum

~/.kube-dc/                             # User data location
├── config.yaml                         # CLI configuration
└── credentials/
    └── {server-hash}.json              # Cached tokens per server

~/.kube/config                          # Standard kubeconfig (modified)
```

### Kubeconfig Integration Strategy

**Key Principle**: Never overwrite or remove non-kube-dc entries.

```yaml
# ~/.kube/config - Example after kube-dc login

apiVersion: v1
kind: Config

# Existing clusters preserved
clusters:
- name: production-aws              # ← Existing, untouched
  cluster:
    server: https://k8s.example.com
- name: minikube                    # ← Existing, untouched
  cluster:
    server: https://192.168.49.2:8443
- name: kube-dc-cloud               # ← Added by kube-dc CLI
  cluster:
    server: https://api.kube-dc.cloud:6443
    certificate-authority-data: LS0tLS1...

# Existing users preserved
users:
- name: aws-user                    # ← Existing, untouched
  user:
    exec:
      command: aws
      args: ["eks", "get-token", ...]
- name: minikube                    # ← Existing, untouched
  user:
    client-certificate: ~/.minikube/...
- name: kube-dc@shalb               # ← Added by kube-dc CLI
  user:
    exec:
      apiVersion: client.authentication.k8s.io/v1
      command: kube-dc
      args: ["credential", "--server", "https://api.kube-dc.cloud"]
      interactiveMode: IfAvailable

# Existing contexts preserved
contexts:
- name: production                  # ← Existing, untouched
  context:
    cluster: production-aws
    user: aws-user
- name: minikube                    # ← Existing, untouched
  context:
    cluster: minikube
    user: minikube
- name: kube-dc/shalb/demo          # ← Added by kube-dc CLI
  context:
    cluster: kube-dc-cloud
    user: kube-dc@shalb
    namespace: shalb-demo
- name: kube-dc/shalb/dev           # ← Added by kube-dc CLI
  context:
    cluster: kube-dc-cloud
    user: kube-dc@shalb
    namespace: shalb-dev

current-context: kube-dc/shalb/demo  # ← Updated by kube-dc use
```

### kubectx/kubens Compatibility

**Context Naming Convention**: `kube-dc/{org}/{project}`

This allows:
```bash
# Works with kubectx
$ kubectx
minikube
production
kube-dc/shalb/demo      ← Kube-DC contexts clearly identified
kube-dc/shalb/dev
kube-dc/shalb/envoy

$ kubectx kube-dc/shalb/demo
Switched to context "kube-dc/shalb/demo".

# Works with kubens
$ kubens
shalb-demo              ← Current namespace
shalb-dev
shalb-envoy

$ kubens shalb-dev
Context "kube-dc/shalb/demo" modified.
Active namespace is "shalb-dev".
```

**Kube-DC CLI namespace switching** (uses JWT claims for validation):
```bash
$ kube-dc ns
Available namespaces (from your access token):
  1) shalb-demo (current)
  2) shalb-dev
  3) shalb-envoy

$ kube-dc ns shalb-dev
Switched to namespace "shalb-dev"
```

## Command Reference

### `kube-dc login`

Authenticate with a Kube-DC server using browser-based OAuth.

```bash
# Interactive - prompts for server if not provided
$ kube-dc login

# Explicit server
$ kube-dc login --server https://api.kube-dc.cloud

# With organization (realm) specified
$ kube-dc login --server https://api.kube-dc.cloud --org shalb

# Device code flow for headless/SSH environments
$ kube-dc login --server https://api.kube-dc.cloud --device-code
```

**Flow:**
1. Start local HTTP server on random available port
2. Generate PKCE code verifier and challenge
3. Build authorization URL with parameters:
   - `client_id=kube-dc`
   - `redirect_uri=http://localhost:{port}/callback`
   - `response_type=code`
   - `scope=openid`
   - `code_challenge={challenge}`
   - `code_challenge_method=S256`
4. Open browser to authorization URL
5. Wait for callback with authorization code
6. Exchange code for tokens
7. Parse JWT to extract organization and available projects
8. Cache tokens to `~/.kube-dc/credentials/`
9. Update `~/.kube/config` with new contexts

### `kube-dc logout`

Remove cached credentials and kubeconfig entries.

```bash
# Logout from current server
$ kube-dc logout

# Logout from specific server
$ kube-dc logout --server https://api.kube-dc.cloud

# Logout from all servers
$ kube-dc logout --all
```

### `kube-dc use`

Switch between organizations and projects.

```bash
# Switch to specific project
$ kube-dc use shalb/demo
Switched to context "kube-dc/shalb/demo" (namespace: shalb-demo)

# Interactive selection
$ kube-dc use
Available contexts:
  1) kube-dc/shalb/demo
  2) kube-dc/shalb/dev
  3) kube-dc/shalb/envoy
Select [1-3]: 2
Switched to context "kube-dc/shalb/dev"
```

### `kube-dc ns`

Switch or list namespaces (from JWT claims).

```bash
# List available namespaces
$ kube-dc ns
Available namespaces:
  shalb-demo (current)
  shalb-dev
  shalb-envoy

# Switch namespace
$ kube-dc ns shalb-dev
Switched to namespace "shalb-dev"
```

### `kube-dc config`

Manage CLI configuration.

```bash
# Show current configuration
$ kube-dc config show

# List all kube-dc contexts
$ kube-dc config get-contexts

# Set default server
$ kube-dc config set-default-server https://api.kube-dc.cloud
```

### `kube-dc credential`

Exec credential plugin for kubectl (not typically called directly).

```bash
# Returns ExecCredential JSON for kubectl
$ kube-dc credential --server https://api.kube-dc.cloud
{
  "apiVersion": "client.authentication.k8s.io/v1",
  "kind": "ExecCredential",
  "status": {
    "token": "eyJhbGciOiJSUzI1NiIs...",
    "expirationTimestamp": "2026-01-26T12:30:00Z"
  }
}
```

### `kube-dc version`

Show version information.

```bash
$ kube-dc version
kube-dc CLI v1.0.0
  Git commit: abc1234
  Built: 2026-01-26T10:00:00Z
  Go version: go1.22.0
```

## Token Lifecycle

```
┌─────────────────────────────────────────────────────────────────────────────┐
│                           Token Lifecycle                                    │
├─────────────────────────────────────────────────────────────────────────────┤
│                                                                              │
│  ┌──────────┐                                                                │
│  │  Login   │ ──► access_token (15 min) + refresh_token (30 days)           │
│  └──────────┘                                                                │
│       │                                                                      │
│       ▼                                                                      │
│  ┌──────────────────────────────────────────────────────────────────────┐   │
│  │                    kubectl command executed                          │   │
│  │                              │                                       │   │
│  │                              ▼                                       │   │
│  │                  ┌───────────────────────┐                           │   │
│  │                  │ kube-dc credential    │                           │   │
│  │                  └───────────┬───────────┘                           │   │
│  │                              │                                       │   │
│  │           ┌──────────────────┼──────────────────┐                    │   │
│  │           ▼                  ▼                  ▼                    │   │
│  │   ┌──────────────┐  ┌──────────────┐  ┌──────────────────┐          │   │
│  │   │ access_token │  │ access_token │  │ refresh_token    │          │   │
│  │   │ valid        │  │ expired      │  │ also expired     │          │   │
│  │   └──────┬───────┘  └──────┬───────┘  └────────┬─────────┘          │   │
│  │          │                 │                   │                     │   │
│  │          ▼                 ▼                   ▼                     │   │
│  │   Return token      Refresh token       Error + prompt:              │   │
│  │   immediately       via Keycloak        "Run: kube-dc login"         │   │
│  │                           │                                          │   │
│  │                           ▼                                          │   │
│  │                    Cache new tokens                                  │   │
│  │                    Return access_token                               │   │
│  └──────────────────────────────────────────────────────────────────────┘   │
│                                                                              │
│  Timeline:                                                                   │
│  ├─────────────────────────────────────────────────────────────────────────┤│
│  │ 0          15min        30min        ...        30 days                 ││
│  │ │           │            │                        │                     ││
│  │ Login   Refresh     Refresh                   Re-login                  ││
│  │         (auto)      (auto)                    required                   ││
│  │                                                                          ││
│  │ ◄─────── Seamless kubectl usage ──────────────►│◄── Browser login ──►   ││
│  └──────────────────────────────────────────────────────────────────────────┘│
│                                                                              │
└─────────────────────────────────────────────────────────────────────────────┘
```

## Credential Storage

### File Format

```json
// ~/.kube-dc/credentials/api-kube-dc-cloud.json
{
  "server": "https://api.kube-dc.cloud",
  "keycloak_url": "https://login.kube-dc.cloud",
  "realm": "shalb",
  "client_id": "kube-dc",
  "access_token": "eyJhbGciOiJSUzI1NiIs...",
  "refresh_token": "eyJhbGciOiJSUzI1NiIs...",
  "access_token_expiry": "2026-01-26T12:00:00Z",
  "refresh_token_expiry": "2026-02-25T11:41:00Z",
  "id_token": "eyJhbGciOiJSUzI1NiIs...",
  "user": {
    "email": "voa@shalb.com",
    "org": "shalb",
    "groups": ["org-admin"],
    "namespaces": ["shalb-demo", "shalb-dev", "shalb-envoy"]
  },
  "created_at": "2026-01-26T11:41:00Z",
  "updated_at": "2026-01-26T11:45:00Z"
}
```

### Security

- File permissions: `0600` (owner read/write only)
- Directory permissions: `0700`
- Tokens never logged or displayed
- Optional: Keychain/Credential Manager integration (future)

## CLI Configuration

```yaml
# ~/.kube-dc/config.yaml
default_server: https://api.kube-dc.cloud

servers:
  - url: https://api.kube-dc.cloud
    keycloak_url: https://login.kube-dc.cloud
    ca_cert: |
      -----BEGIN CERTIFICATE-----
      ...
      -----END CERTIFICATE-----

preferences:
  auto_open_browser: true
  credential_cache_ttl: 120  # seconds before re-checking
```

## OAuth Flow Details

### PKCE (Proof Key for Code Exchange)

Required for public clients to prevent authorization code interception.

```go
// Generate code verifier (43-128 characters)
verifier := generateRandomString(64)

// Generate code challenge (SHA256 hash, base64url encoded)
hash := sha256.Sum256([]byte(verifier))
challenge := base64.RawURLEncoding.EncodeToString(hash[:])
```

### Authorization Request

```
GET https://login.kube-dc.cloud/realms/{org}/protocol/openid-connect/auth?
  client_id=kube-dc&
  redirect_uri=http://localhost:8400/callback&
  response_type=code&
  scope=openid&
  state={random_state}&
  code_challenge={challenge}&
  code_challenge_method=S256
```

### Token Exchange

```
POST https://login.kube-dc.cloud/realms/{org}/protocol/openid-connect/token
Content-Type: application/x-www-form-urlencoded

grant_type=authorization_code&
client_id=kube-dc&
code={authorization_code}&
redirect_uri=http://localhost:8400/callback&
code_verifier={verifier}
```

### Token Refresh

```
POST https://login.kube-dc.cloud/realms/{org}/protocol/openid-connect/token
Content-Type: application/x-www-form-urlencoded

grant_type=refresh_token&
client_id=kube-dc&
refresh_token={refresh_token}
```

## Discovery Flow

The CLI needs to discover Keycloak URL and CA certificate from the API server.

### Option 1: Well-Known Endpoint (Recommended)

```bash
# API server exposes discovery endpoint
GET https://api.kube-dc.cloud/.well-known/kube-dc-config

{
  "keycloak_url": "https://login.kube-dc.cloud",
  "client_id": "kube-dc",
  "ca_certificate": "LS0tLS1...",
  "realms": ["shalb", "acme", "demo"]
}
```

### Option 2: OIDC Discovery from API Server

```bash
# Use Kubernetes OIDC discovery
GET https://api.kube-dc.cloud/.well-known/openid-configuration
```

### Option 3: Manual Configuration

```bash
$ kube-dc login --server https://api.kube-dc.cloud \
    --keycloak-url https://login.kube-dc.cloud \
    --org shalb
```

## Implementation Plan

### Phase 1: Core Authentication (Week 1-2)

1. **Project setup**
   - Initialize Go module in `cli/`
   - Set up cobra for CLI commands
   - Configure goreleaser for builds

2. **OAuth implementation**
   - PKCE code generation
   - Local callback server
   - Browser opening (cross-platform)
   - Token exchange

3. **Credential storage**
   - Secure file storage
   - Token caching
   - Expiry checking

**Deliverables:**
- `kube-dc login` working
- `kube-dc logout` working
- `kube-dc credential` working

### Phase 2: Kubeconfig Integration (Week 2-3)

1. **Kubeconfig management**
   - Parse existing kubeconfig
   - Merge kube-dc entries
   - Preserve non-kube-dc entries

2. **Context management**
   - Create contexts from JWT namespaces
   - Context naming convention
   - Context switching

3. **kubectx compatibility testing**
   - Test with kubectx
   - Test with kubens
   - Fix any conflicts

**Deliverables:**
- `kube-dc use` working
- `kube-dc ns` working
- `kube-dc config` working

### Phase 3: Polish & Distribution (Week 3-4)

1. **Discovery endpoint**
   - Implement `/.well-known/kube-dc-config` in manager
   - CLI auto-discovery

2. **Cross-platform testing**
   - Linux (amd64, arm64)
   - macOS (amd64, arm64)
   - Windows (amd64)

3. **Distribution**
   - GitHub releases
   - Homebrew tap
   - APT/YUM repositories (optional)
   - Installation script

**Deliverables:**
- Multi-platform binaries
- Homebrew formula
- Installation documentation

### Phase 4: Advanced Features (Future)

1. Device code flow for headless environments
2. Keychain/Credential Manager integration
3. Token introspection and validation
4. Offline mode improvements

## Testing Strategy

### Unit Tests

- PKCE generation
- JWT parsing
- Kubeconfig merging
- Token refresh logic

### Integration Tests

- Full OAuth flow against test Keycloak
- Kubeconfig manipulation
- Credential caching

### E2E Tests

- Login → kubectl command → token refresh
- Multiple contexts
- kubectx/kubens compatibility

## Implementation Status

### ✅ Completed (v0.2.2 - January 2026)

| Feature | Status | Notes |
|---------|--------|-------|
| `kube-dc login` | ✅ Done | Browser OAuth with PKCE, `--domain`/`--org` flags |
| `kube-dc logout` | ✅ Done | Clear credentials, optional context removal |
| `kube-dc use` | ✅ Done | List and switch contexts |
| `kube-dc ns` | ✅ Done | List and switch namespaces |
| `kube-dc config show` | ✅ Done | Show context, credentials, token status |
| `kube-dc config get-contexts` | ✅ Done | List kube-dc contexts |
| `kube-dc credential` | ✅ Done | kubectl exec plugin with auto-refresh |
| `kube-dc version` | ✅ Done | Version info |
| Token caching | ✅ Done | `~/.kube-dc/credentials/` with 0600 perms |
| Kubeconfig merging | ✅ Done | Preserves existing entries |
| JWT namespace extraction | ✅ Done | From token claims |
| Automatic token refresh | ✅ Done | Via credential plugin, Keycloak as source of truth |
| Offline access tokens | ✅ Done | 30-day refresh token expiry (industry standard) |
| GoReleaser setup | ✅ Done | Multi-platform builds via GitHub Actions |
| CA certificate support | ✅ Done | `--ca-cert` flag for self-hosted |

### 📋 Planned

| Feature | Priority | Notes |
|---------|----------|-------|
| Auto-discovery | Medium | `/.well-known/kube-dc-config` endpoint |
| Device code flow | Medium | For SSH/headless environments |
| Homebrew formula | Low | macOS distribution |

## Success Criteria

- [x] User can login with single `kube-dc login` command
- [x] Browser opens automatically for authentication
- [x] Tokens are refreshed automatically (no password prompts for 30 days)
- [x] Existing kubeconfig entries are preserved
- [x] kubectx and kubens work correctly
- [x] Works on Linux, macOS, and Windows
- [x] Binary size < 20MB (~5MB actual)
- [x] Credential command responds in < 100ms
- [x] Clear error messages with correct login command format

## Migration Path

### From Shell Scripts

Users with existing `~/.kube-dc/{org}-{project}/` directories:

1. Install `kube-dc` CLI
2. Run `kube-dc login` (creates new credentials)
3. Old directories can be removed manually

### Deprecation Timeline

| Phase | Action |
|-------|--------|
| v1.0 | CLI released, shell scripts still available |
| v1.1 | Shell scripts deprecated (warning in UI) |
| v1.2 | Shell scripts removed from documentation |
| v2.0 | Shell scripts removed from repository |

## Dependencies

### Go Libraries

```go
require (
    github.com/spf13/cobra v1.8.0           // CLI framework
    github.com/pkg/browser v0.0.0-20210911075715-681adbf594b8  // Open browser
    golang.org/x/oauth2 v0.16.0             // OAuth2 client
    gopkg.in/yaml.v3 v3.0.1                 // YAML parsing
    github.com/golang-jwt/jwt/v5 v5.2.0     // JWT parsing
)
```

### External Dependencies

- Keycloak server with `kube-dc` client configured
- Well-known endpoint on API server (optional)

## Open Questions

1. **Realm discovery**: How does user know which organization/realm to use?
   - *Proposal*: Discovery endpoint lists available realms, or user specifies

2. **Multiple organizations**: Can user be logged into multiple orgs simultaneously?
   - *Proposal*: Yes, separate credential files per server/realm

3. **CA certificate distribution**: How to securely distribute cluster CA?
   - *Proposal*: Include in discovery endpoint, verify against well-known CA

4. **Offline token validation**: Should we validate JWT signature locally?
   - *Proposal*: Check expiry only (faster), server validates signature

## Release Process

### Creating a Release

1. **Update version** in code if needed
2. **Commit and push** to `shalb/kube-dc` main branch
3. **Sync workflow** automatically pushes CLI changes to `kube-dc/kube-dc-public`
4. **Create tag** on `kube-dc-public`:
   ```bash
   cd kube-dc-public
   git pull origin main
   git tag v0.2.3  # increment version
   git push origin v0.2.3
   ```
5. **GoReleaser workflow** automatically builds and publishes binaries

### Release Artifacts

GoReleaser produces binaries for:
- `kube-dc_linux_amd64`
- `kube-dc_linux_arm64`
- `kube-dc_darwin_amd64`
- `kube-dc_darwin_arm64`
- `kube-dc_windows_amd64.exe`

Binaries are published to: https://github.com/kube-dc/kube-dc-public/releases

### Version History

| Version | Date | Changes |
|---------|------|---------|
| v0.2.2 | 2026-01-26 | Offline access tokens (30-day sessions), fix error messages |
| v0.2.1 | 2026-01-26 | Raw binary releases, remove homebrew |
| v0.2.0 | 2026-01-26 | Initial release with browser OAuth, auto-refresh |

## References

- [Kubernetes Client Authentication](https://kubernetes.io/docs/reference/access-authn-authz/authentication/#client-go-credential-plugins)
- [OAuth 2.0 PKCE](https://datatracker.ietf.org/doc/html/rfc7636)
- [Keycloak OIDC](https://www.keycloak.org/docs/latest/securing_apps/#_oidc)
- [kubelogin (kubectl oidc-login)](https://github.com/int128/kubelogin)
- [AWS CLI SSO](https://docs.aws.amazon.com/cli/latest/userguide/cli-configure-sso.html)
- [GCloud Auth](https://cloud.google.com/sdk/gcloud/reference/auth/login)
