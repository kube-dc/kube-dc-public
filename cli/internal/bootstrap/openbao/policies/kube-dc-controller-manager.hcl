# Cross-Organization admin policy for kube-dc-controller-manager.
#
# Source of truth for the controller-tier OpenBao policy. Embedded
# into the kube-dc CLI binary via //go:embed in
# cli/internal/bootstrap/openbao/policies.go; applied to OpenBao by
# bootstrap_openbao_setup_controller_auth + by init Phase C.
#
# To extend (new M2 PKI / M3 Transit / M4 Database paths), edit this
# file. The CLI's tests assert specific path entries exist — tighten
# those first if you're widening grants.

# --- root namespace: create + manage child namespaces (Organizations)
path "sys/namespaces"           { capabilities = ["list"] }
path "sys/namespaces/*"         { capabilities = ["create","read","update","delete","list"] }

# --- root namespace: platform-root PKI (M2 Certificate Manager).
# Lives outside any Org namespace; signed by it, every per-Org pki_int
# intermediate gets a chain back to this root. EnsurePlatformRoot creates
# the mount on first private-cert request and generates the self-signed
# root CA inside it; the controller never re-issues the root.
#
# `sudo` is required on sys/mounts to enable any new engine in OpenBao
# (root-protected operation). The `pki_platform_root/*` glob covers
# /root/generate/internal, /root/sign-intermediate, /cert/ca, and
# /config/* — every API path EnsurePlatformRoot + the per-Org
# EnsureOrgIntermediate's CSR signing touch.
path "sys/mounts/pki_platform_root"   { capabilities = ["create","read","update","delete","sudo"] }
path "pki_platform_root/*"            { capabilities = ["create","read","update","delete","list"] }

# --- inside ANY child namespace (`+` is the single-segment glob)
# Mounts: enable / disable / tune KV, transit, PKI, database engines.
# read on the bare path also enables `sys/mounts` listing for the SDK.
path "+/sys/mounts"             { capabilities = ["read","list"] }
path "+/sys/mounts/*"           { capabilities = ["create","read","update","delete","list","sudo"] }
# Auth methods: enable / disable / configure kubernetes, oidc. Enabling
# auth methods is a root-protected operation in Vault/OpenBao, so the
# policy needs `sudo` capability — without it, `sys/auth/<method>`
# PUT returns 403 even with create+update.
path "+/sys/auth"               { capabilities = ["read","list"] }
path "+/sys/auth/*"             { capabilities = ["create","read","update","delete","list","sudo"] }
# Auth method config + roles (any mount, any role).
path "+/auth/+/config"          { capabilities = ["create","read","update"] }
path "+/auth/+/role"            { capabilities = ["list"] }
path "+/auth/+/role/*"          { capabilities = ["create","read","update","delete","list"] }
# Policies inside the child namespace.
path "+/sys/policies/acl"       { capabilities = ["list"] }
path "+/sys/policies/acl/*"     { capabilities = ["create","read","update","delete","list"] }
# Legacy policy paths (older OpenBao versions write here too).
path "+/sys/policy"             { capabilities = ["list"] }
path "+/sys/policy/*"           { capabilities = ["create","read","update","delete","list"] }
# KV v2: metadata + data paths for project mounts.
# Note: OpenBao policy `+` is a WHOLE-segment glob — it cannot do
# `kv-+` prefix matching ("kv-envoy" is not a separate segment from
# `kv-`). So we glob both mount-name segments with `+`. Reasonable
# blast radius for the cross-Org controller-manager: it already has
# create on `sys/mounts/*` so it could create any mount it likes.
path "+/+/data/*"               { capabilities = ["create","read","update","delete","list"] }
path "+/+/metadata"             { capabilities = ["list"] }
path "+/+/metadata/*"           { capabilities = ["create","read","update","delete","list"] }
path "+/+/delete/*"             { capabilities = ["update"] }
path "+/+/undelete/*"           { capabilities = ["update"] }
path "+/+/destroy/*"            { capabilities = ["update"] }
# Per-Org PKI engine (M2 Certificate Manager). EnsureOrgIntermediate
# creates the <org>/pki_int mount; EnsureProjectRole writes
# <org>/pki_int/roles/<project>; cert-manager's vault Issuer then
# signs against <org>/pki_int/sign/<project>. Glob covers every
# PKI sub-path under the per-Org mount.
path "+/sys/mounts/pki_int"     { capabilities = ["create","read","update","delete","sudo"] }
path "+/pki_int/*"              { capabilities = ["create","read","update","delete","list"] }
# Per-Org Transit engine (M3-T01 KMS). EnsureOrgTransit creates the
# <org>/transit mount; EnsureKey writes transit/keys/<name>; backend
# encrypt/decrypt hit transit/encrypt|decrypt/<name>; the controller
# rotates via transit/keys/<name>/rotate and configures
# min_decryption_version + deletion_allowed via transit/keys/<name>/config.
# `sudo` on sys/mounts/transit is required to enable the secrets
# engine (matches the pki_int path above).
path "+/sys/mounts/transit"     { capabilities = ["create","read","update","delete","sudo"] }
path "+/transit/*"              { capabilities = ["create","read","update","delete","list"] }
# Per-Org Database engine roles (M4-T02 DatabaseCredentialPolicy).
# db-manager owns the config side; the controller manager owns roles
# (static + dynamic) under <org>/database/roles/<dbname>-<rolename>.
# Mount is created by db-manager; the manager only needs role + creds.
path "+/database/roles"         { capabilities = ["list"] }
path "+/database/roles/+"       { capabilities = ["create","read","update","delete"] }
path "+/database/static-roles"  { capabilities = ["list"] }
path "+/database/static-roles/+" { capabilities = ["create","read","update","delete"] }
path "+/database/creds/+"        { capabilities = ["read"] }
path "+/database/static-creds/+" { capabilities = ["read"] }
path "+/database/rotate-role/+"  { capabilities = ["update"] }
# Identity (M2+ when external_group / group_alias are wired).
path "+/identity/group"         { capabilities = ["list"] }
path "+/identity/group/*"       { capabilities = ["create","read","update","delete","list"] }
path "+/identity/group-alias"   { capabilities = ["list"] }
path "+/identity/group-alias/*" { capabilities = ["create","read","update","delete","list"] }
# Token lookup-self (lets the manager check its own lease for renewal).
path "auth/token/lookup-self"   { capabilities = ["read"] }
path "auth/token/renew-self"    { capabilities = ["update"] }
