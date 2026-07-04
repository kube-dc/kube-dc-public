#!/usr/bin/env bash
#
# ============================================================================
# DEPRECATED 2026-06-08 — use the kube-dc CLI instead.
# ============================================================================
#
# This shell script is kept for ONE release cycle as a fallback. The
# canonical paths are now:
#
#   # Fresh install (auth setup runs as part of init Phase C):
#   kube-dc bootstrap openbao init <cluster> --repo ~/projects/kube-dc-fleet
#
#   # Standalone (recovery / migration / refresh):
#   kube-dc bootstrap openbao setup-controller-auth <cluster> \
#       --repo ~/projects/kube-dc-fleet [--refresh-policy]
#
# Both CLI paths use the same engine (internal/bootstrap/openbao/
# setup_controller_auth.go) with the same in-memory share + root
# token discipline (secrets.Buffer + defer RevokeSelf the moment the
# token leaves GenerateRoot). The HCL is embedded into the CLI binary
# from cli/internal/bootstrap/openbao/policies/*.hcl — those .hcl
# files are now the source of truth, not the heredoc blocks below.
#
# Migration: the CLI is idempotent. Running setup-controller-auth on
# a cluster previously bootstrapped by this script is a no-op against
# anything that's already configured + writes nothing new.
#
# Removal scheduled: the release AFTER the kube-dc CLI version that
# ships M5-T08 (the version with `bootstrap openbao setup-controller-auth`).
# ============================================================================
#
# openbao-setup-controller-auth.sh — one-shot install of the
# kube-dc-controller-manager's OpenBao auth path.
#
# Closes the M1 bootstrap gap documented in
# docs/prd/openbao-integration-development-scope.md §8a + commit
# 7ea4a91c. Until this script runs on a cluster:
#
#   - the kube-dc-manager logs in to /auth/k8s-host as a Kubernetes
#     SA and gets ErrOpenBaoAuthNotReady,
#   - every Org / Project / OrganizationGroup / ManagedSecret reconcile
#     bails silently with that error,
#   - the rest of the platform (Keycloak, Grafana, Mimir, …) keeps
#     working as if OpenBao weren't there.
#
# === UPGRADE NOTES =========================================================
# v0.3.45 (M2 Certificate Manager) — added per-Org and root-NS PKI
# policy paths (commit b260895a).
# v0.3.46 (M3-T01 KMS) — added per-Org Transit policy paths.
# v0.3.47 (M3-T05 DB envelope encryption) — added a SECOND auth role
# (db-manager) with its own tight Transit-only policy. Clusters
# upgrading to v0.3.47 MUST re-run this script with REFRESH_POLICY=
# true so the db-manager auth role is provisioned; otherwise
# KdcDatabases with backup.encryption.enabled=true surface
# BackupEncryptionConfigured=False / Reason=OpenBaoAuthNotReady
# until the bootstrap closes.
#
# v0.3.49+ (M4-T01 Database engine config) — the db-manager policy is
# no longer Transit-only: it now also gates
# +/sys/mounts/database (sudo) + +/database/config(/+) + +/database/reset/+
# so db-manager can mount the per-Org Database engine and write
# `database/config/<projectNS>-<dbName>` per KdcDatabase. The
# kube-dc-controller-manager policy gains the matching
# +/database/{roles,static-roles}(/+) + +/database/{creds,static-creds}/+
# paths (M4-T02 surface). Clusters upgrading to v0.3.49+ MUST re-run
# this script with REFRESH_POLICY=true; new DBCPs stall on
# Ready=False/Reason=OpenBaoAuthNotReady until the refresh lands.
#
# To pick up the new policy capabilities on a cluster where the M1
# bootstrap already ran, re-run this script with REFRESH_POLICY=true:
#
#   REFRESH_POLICY=true CLUSTER=<name> DOMAIN=<domain> \
#     ./hack/openbao-setup-controller-auth.sh
#
# REFRESH_POLICY=true skips the generate-root + auth-enable + role-write
# steps (already done) and rewrites the policy + role in place. Existing
# tenant tokens keep their lease until next renewal, at which point they
# pick up the new policy. No downtime risk — OpenBao applies the policy
# write atomically.
#
# Legacy path (still works): clear the
# kube-dc.com/openbao-controller-auth-installed annotation on
# svc/openbao -n openbao before re-running. The script then re-runs the
# full bootstrap, which is safe because every step is idempotent. The
# cloud cluster was upgraded to v0.3.45 this way on 2026-05-22.
# ============================================================================
#
# What this script does (idempotent):
#
#   1. Decrypt the Shamir unseal shares from
#      clusters/<cluster>/secrets.enc.yaml.
#   2. `bao operator generate-root` with the threshold shares to
#      produce a SINGLE-USE root token.
#   3. With that token:
#        - enable `auth/k8s-host` at the root namespace,
#        - configure it against the in-cluster kube-apiserver,
#        - write the `kube-dc-controller-manager` policy (cross-Org
#          admin via `+` namespace globs),
#        - write the `kube-dc-controller-manager` role bound to the
#          kube-dc-manager ServiceAccount in the `kube-dc` namespace.
#   4. Revoke the root token.
#   5. Annotate the openbao Service so future runs see the setup is
#      complete.
#
# Requirements on the operator's machine:
#   - bao CLI (https://openbao.org/downloads/)
#   - sops, yq, jq, kubectl
#   - KUBECONFIG pointing at the target cluster
#   - SOPS age key available in the agent's keyring
#
# Requirements on the OpenBao server:
#   - listener "tcp" {} block MUST include
#     `disable_unauthed_generate_root_endpoints = false`. OpenBao
#     2.5+ ships with that flag set to true, which makes
#     /sys/generate-root/attempt return 405 "unsupported operation".
#     The fleet's openbao values-configmap already sets this.

set -euo pipefail

# ----- inputs ----------------------------------------------------------------

CLUSTER=${CLUSTER:?CLUSTER=stage|cloud|cs-zrh required}
DOMAIN=${DOMAIN:?DOMAIN=<cluster domain> required (e.g. stage.kube-dc.com)}
KUBE_DC_NS=${KUBE_DC_NS:-kube-dc}
MANAGER_SA=${MANAGER_SA:-kube-dc-manager}
ROLE_NAME=${ROLE_NAME:-kube-dc-controller-manager}
POLICY_NAME=${POLICY_NAME:-kube-dc-controller-manager}
# M3-T05: db-manager runs as its own SA and gets a narrow Transit-
# only policy (encrypt + decrypt on the per-Org transit mount). Same
# auth/k8s-host mount as kube-dc-manager; different role + policy
# names so audit logs distinguish the two controllers.
#
# DB_MANAGER_SA defaults to `kube-dc-db-manager` to match the chart-
# rendered ServiceAccount name (templates/db-manager-serviceaccount.yaml
# names it `<release>-db-manager` via the dbmanager.fullname helper,
# and the chart's release is `kube-dc`). Override when the Helm
# release uses a non-default name OR when fullnameOverride is set.
DB_MANAGER_NS=${DB_MANAGER_NS:-kube-dc}
DB_MANAGER_SA=${DB_MANAGER_SA:-kube-dc-db-manager}
DB_MANAGER_ROLE_NAME=${DB_MANAGER_ROLE_NAME:-db-manager}
DB_MANAGER_POLICY_NAME=${DB_MANAGER_POLICY_NAME:-db-manager}
SHARE_THRESHOLD=${SHARE_THRESHOLD:-3}
FLEET_DIR=${FLEET_DIR:-${HOME}/projects/kube-dc-fleet}

# REFRESH_POLICY=true re-runs the generate-root ceremony and rewrites
# the policy + auth role IN PLACE on a cluster where the M1 bootstrap
# is already installed. Use this to pick up new policy paths added in
# later milestones (M2 PKI in v0.3.45, M3 Transit in v0.3.46+) without
# clearing the openbao-controller-auth-installed annotation.
REFRESH_POLICY=${REFRESH_POLICY:-false}

# Allow override; default to the corresponding cluster's secrets file.
SOPS_FILE=${SOPS_FILE:-${FLEET_DIR}/clusters/${CLUSTER}/secrets.enc.yaml}

# ----- preflight -------------------------------------------------------------

err() { echo "openbao-setup: $*" >&2; exit 1; }
log() { echo "openbao-setup: $*"; }

# bao CLI is no longer required on the operator machine — all bao ops
# run inside the active OpenBao pod via kubectl exec. Left in the
# dependency comment block at the top of the file for the README copy.
for cmd in sops yq jq kubectl; do
  command -v "$cmd" >/dev/null || err "$cmd not in PATH"
done
[ -f "$SOPS_FILE" ] || err "SOPS file not found: $SOPS_FILE"
[ -n "${KUBECONFIG:-}" ] || err "KUBECONFIG must be set (target cluster's kubeconfig)"

# ----- 1. unseal shares ------------------------------------------------------

log "decrypting unseal shares from $SOPS_FILE ..."
SHARES=()
for i in $(seq 1 "$SHARE_THRESHOLD"); do
  share=$(sops -d "$SOPS_FILE" | yq -r ".stringData.OPENBAO_UNSEAL_KEY_${i}")
  if [ -z "$share" ] || [ "$share" = "null" ]; then
    err "OPENBAO_UNSEAL_KEY_${i} missing from $SOPS_FILE"
  fi
  SHARES+=("$share")
done
log "loaded ${#SHARES[@]} unseal shares"

# ----- 2. point at OpenBao ---------------------------------------------------
#
# generate-root must be sent to the active Raft node. On a single-pod
# cluster the public DNS works; on HA (replicas>1) the gateway round-
# robins and standby nodes return 500 "Vault is in standby mode". To
# stay correct under both, we exec all bao operations inside the active
# pod (BAO_ADDR=http://127.0.0.1:8200, BAO_TOKEN supplied per call).
#
# Active-pod detection: `bao status` exposes `HA Mode = active`. We
# loop over openbao-{0..N-1} pods and pick the first reporting active.

OPENBAO_NS=${OPENBAO_NS:-openbao}
ACTIVE_POD=""
for pod in $(kubectl -n "$OPENBAO_NS" get pods -l app.kubernetes.io/name=openbao -o name 2>/dev/null | sed 's|pod/||'); do
  if kubectl -n "$OPENBAO_NS" exec "$pod" -- bao status 2>/dev/null | grep -q "HA Mode\s*active"; then
    ACTIVE_POD="$pod"
    break
  fi
done
[ -n "$ACTIVE_POD" ] || err "could not find an active OpenBao pod in $OPENBAO_NS (none reported 'HA Mode active' from bao status)"
log "active OpenBao pod: $ACTIVE_POD"

# bao_exec runs a bao subcommand inside the active pod against
# localhost. We pass BAO_TOKEN through env when set; otherwise the
# pod's empty BAO_TOKEN means an unauthenticated request (only valid
# for the generate-root ceremony itself).
bao_exec() {
  if [ -n "${BAO_TOKEN:-}" ]; then
    kubectl -n "$OPENBAO_NS" exec "$ACTIVE_POD" -- env BAO_TOKEN="$BAO_TOKEN" bao "$@"
  else
    kubectl -n "$OPENBAO_NS" exec "$ACTIVE_POD" -- bao "$@"
  fi
}

# Short-circuit if the setup-finalized annotation is already there,
# UNLESS REFRESH_POLICY=true — in which case we proceed to the
# policy/role rewrite block below and SKIP generate-root + auth-enable.
ALREADY_INSTALLED="false"
if kubectl -n "$OPENBAO_NS" get svc openbao -o jsonpath='{.metadata.annotations.kube-dc\.com/openbao-controller-auth-installed}' 2>/dev/null | grep -q .; then
  ALREADY_INSTALLED="true"
fi
if [ "$ALREADY_INSTALLED" = "true" ] && [ "$REFRESH_POLICY" != "true" ]; then
  log "openbao Service already annotated as setup-complete — exiting"
  log "(set REFRESH_POLICY=true to rewrite the policy + role in place)"
  exit 0
fi

# ----- 3. generate temporary root token --------------------------------------
#
# OpenBao 2.5.x diverged from Vault here: `bao operator generate-root -init`
# no longer auto-generates an OTP — it returns 405 unless you supply one.
# We pre-generate the OTP locally with `bao operator generate-root
# -generate-otp` (a pure client-side base64 string, no API call) and pass
# it explicitly to -init.

log "starting generate-root ceremony ..."
OTP=$(bao_exec operator generate-root -generate-otp 2>/dev/null | tr -d '[:space:]')
[ -n "$OTP" ] || err "generate-otp returned empty"
INIT_JSON=$(bao_exec operator generate-root -init -otp="$OTP" -format=json)
NONCE=$(echo "$INIT_JSON" | jq -r .nonce)
[ -n "$NONCE" ] && [ "$NONCE" != "null" ] || err "generate-root -init returned empty nonce — output: $INIT_JSON"

LAST_OUT=""
for share in "${SHARES[@]}"; do
  LAST_OUT=$(bao_exec operator generate-root -nonce="$NONCE" -format=json "$share")
done

ENCODED=$(echo "$LAST_OUT" | jq -r '.encoded_root_token // empty')
[ -n "$ENCODED" ] || err "generate-root did not return an encoded token after $SHARE_THRESHOLD shares — output: $LAST_OUT"

ROOT_TOKEN=$(bao_exec operator generate-root -decode="$ENCODED" -otp="$OTP" -format=json | jq -r '.token // empty')
[ -n "$ROOT_TOKEN" ] || err "decode of encoded root token failed"
log "obtained single-use root token"

# Trap: even on later failure, attempt to revoke the token so we don't
# leave a long-lived admin token behind.
revoke_root() {
  if [ -n "${ROOT_TOKEN:-}" ]; then
    log "revoking temporary root token ..."
    BAO_TOKEN="$ROOT_TOKEN" bao_exec token revoke -self >/dev/null 2>&1 || log "WARN: revoke failed (token may have already expired)"
  fi
}
trap revoke_root EXIT

export BAO_TOKEN="$ROOT_TOKEN"

# ----- 4. enable auth/k8s-host ----------------------------------------------

log "enabling auth/k8s-host (idempotent) ..."
bao_exec auth enable -path=k8s-host kubernetes >/dev/null 2>&1 || true

# Fetch the kube-apiserver CA. The manager pod mounts this file from
# its SA volume; we read it via a one-shot get on kube-root-ca.crt.
SA_CA=$(kubectl -n "$KUBE_DC_NS" get configmap kube-root-ca.crt -o jsonpath='{.data.ca\.crt}')
[ -n "$SA_CA" ] || err "could not read kube-root-ca.crt in namespace $KUBE_DC_NS"

log "configuring auth/k8s-host against kube-apiserver ..."
bao_exec write auth/k8s-host/config \
  kubernetes_host="https://kubernetes.default.svc" \
  kubernetes_ca_cert="$SA_CA" \
  disable_iss_validation=true \
  >/dev/null

# ----- 5. policy + role -----------------------------------------------------

log "writing policy ${POLICY_NAME} ..."
# Pipe the HCL into bao via stdin so we don't need a temp file. The
# `bao policy write NAME -` reads stdin when given `-` as the file arg.
kubectl -n "$OPENBAO_NS" exec -i "$ACTIVE_POD" -- env BAO_TOKEN="$BAO_TOKEN" bao policy write "$POLICY_NAME" - <<'HCL' >/dev/null
# Cross-Organization admin policy for kube-dc-controller-manager.
# Generated by hack/openbao-setup-controller-auth.sh; do not edit by
# hand. To extend (e.g. M2 PKI, M3 Transit), update the script.

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
HCL

# token_ttl is short (15m) on purpose: the controllers create a k8s-auth login
# lease per login, and under load (e.g. many reconciles) these accumulate. A long
# TTL keeps each lease alive long enough to pile up into a backlog that, on an
# OpenBao restart, must all be loaded into the expiration manager at once and can
# OOM the pod (stage incident 2026-06-27). 15m clears them ~4× faster; renewals
# (up to token_max_ttl) keep long-lived controller tokens valid.
log "writing role ${ROLE_NAME} ..."
bao_exec write "auth/k8s-host/role/${ROLE_NAME}" \
  bound_service_account_names="$MANAGER_SA" \
  bound_service_account_namespaces="$KUBE_DC_NS" \
  policies="$POLICY_NAME" \
  token_ttl=15m \
  token_max_ttl=24h \
  >/dev/null

# ----- 5b. M3-T05 db-manager policy + role ---------------------------------
#
# db-manager runs as its own SA (kube-dc-db-manager via the kube-dc
# chart) and needs:
#   - Transit encrypt + decrypt on per-Org keys (M3-T05 envelope
#     encryption of DB backups). NEVER mounts/rotates/destroys keys —
#     that surface stays on kube-dc-controller-manager.
#   - Database engine mount + config (M4-T01, v0.3.49+) so it can
#     mount per-Org `database/` and write
#     `database/config/<projectNS>-<dbName>` per KdcDatabase. NEVER
#     writes roles — that stays on kube-dc-controller-manager.

log "writing policy ${DB_MANAGER_POLICY_NAME} (M3-T05 envelope encryption for DB backups) ..."
kubectl -n "$OPENBAO_NS" exec -i "$ACTIVE_POD" -- env BAO_TOKEN="$BAO_TOKEN" bao policy write "$DB_MANAGER_POLICY_NAME" - <<'HCL' >/dev/null
# db-manager (M3-T05) — wraps + unwraps backup DEKs against per-Org
# Transit keys. Tight scope: read key metadata + encrypt + decrypt
# only. db-manager NEVER mounts, rotates, or destroys keys — those
# are kube-dc-controller-manager's surface.

# Per-Org Transit grants. The `+` is OpenBao policy's single-segment
# glob — matches any child namespace (== Organization), since the
# kube-dc KMSKey reconciler stamps status.keyId as
# "<org>/transit/keys/<name>".
path "+/transit/keys/+"      { capabilities = ["read"] }
path "+/transit/encrypt/+"   { capabilities = ["update"] }
path "+/transit/decrypt/+"   { capabilities = ["update"] }

# M4-T01: db-manager registers each KdcDatabase as
# "<org>/database/config/<dbname>". Owns the per-Org database mount
# (sys/mounts/database) and the config entries underneath, but NEVER
# writes roles — kube-dc-controller-manager owns
# "<org>/database/roles/<dbname>-<role>" via DatabaseCredentialPolicy.
# `sudo` on sys/mounts is required to enable the secrets engine.
path "+/sys/mounts/database"   { capabilities = ["create","read","update","sudo"] }
path "+/database/config"       { capabilities = ["list"] }
path "+/database/config/+"     { capabilities = ["create","read","update","delete"] }
# Reset the connection after credential rotation so OpenBao re-handshakes.
path "+/database/reset/+"      { capabilities = ["update"] }

# Token self-management for the controller's lease renewal loop.
path "auth/token/lookup-self" { capabilities = ["read"] }
path "auth/token/renew-self"  { capabilities = ["update"] }
HCL

log "writing role ${DB_MANAGER_ROLE_NAME} (bound to ${DB_MANAGER_SA} in ${DB_MANAGER_NS}) ..."
bao_exec write "auth/k8s-host/role/${DB_MANAGER_ROLE_NAME}" \
  bound_service_account_names="$DB_MANAGER_SA" \
  bound_service_account_namespaces="$DB_MANAGER_NS" \
  policies="$DB_MANAGER_POLICY_NAME" \
  token_ttl=15m \
  token_max_ttl=24h \
  >/dev/null

# ----- 6. mark Service as installed ----------------------------------------

kubectl -n "$OPENBAO_NS" annotate svc openbao \
  "kube-dc.com/openbao-controller-auth-installed=$(date -u +%Y-%m-%dT%H:%M:%SZ)" \
  --overwrite >/dev/null

# revoke_root() runs via the EXIT trap.

log "done — kube-dc-manager can now log in via auth/k8s-host/role/${ROLE_NAME}"
log "      and db-manager can now log in via auth/k8s-host/role/${DB_MANAGER_ROLE_NAME} (M3-T05 + M4-T01)"
log "      restart the manager + db-manager pods (or wait for token-renew loop) to pick up the new auth"
