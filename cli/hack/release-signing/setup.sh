#!/usr/bin/env bash
# One-time setup of the CLI self-update signing key in kube-dc/kube-dc-public.
# Needs repo ADMIN (environments and their secrets are admin-only): run it as
# voatsap, from a kube-dc checkout (it uses cli/hack/signchecksums):
#
#   bash cli/hack/release-signing/setup.sh .
#
# What it does:
#  1. creates the environment "release": deployments only from tags matching
#     v*, approved by either bohdaq or voatsap;
#  2. generates a NEW ed25519 key pair on this machine, stores the private
#     seed as the ENVIRONMENT secret KUBE_DC_UPDATE_SIGNING_KEY and destroys
#     the local copy, so the private key never leaves this machine;
#  3. prints the PUBLIC key: send it to bohdaq, it goes into
#     cli/internal/selfupdate/selfupdate.go (PublicKey).
# The workflow change (environment: release) is applied with the next mirror
# update: cli/hack/release-signing/cli-release-workflow.patch.
set -euo pipefail
REPO=kube-dc/kube-dc-public
KUBE_DC=${1:?usage: $0 /path/to/kube-dc}
[ -f "$KUBE_DC/cli/hack/signchecksums/main.go" ] || { echo "no cli/hack/signchecksums in $KUBE_DC (pull main)"; exit 1; }
[ "$(gh api "repos/$REPO" --jq .permissions.admin)" = true ] || { echo "you need admin on $REPO"; exit 1; }

id() { gh api "users/$1" --jq .id; }
BOHDAQ=$(id bohdaq); VOATSAP=$(id voatsap)

# 1. Either reviewer approves; the person who pushed the tag may approve it.
gh api -X PUT "repos/$REPO/environments/release" --input - <<JSON
{
  "wait_timer": 0,
  "prevent_self_review": false,
  "reviewers": [{ "type": "User", "id": $BOHDAQ }, { "type": "User", "id": $VOATSAP }],
  "deployment_branch_policy": { "protected_branches": false, "custom_branch_policies": true }
}
JSON
# Only tags matching v* may deploy to it (no branch at all).
if ! gh api "repos/$REPO/environments/release/deployment-branch-policies" --jq '.branch_policies[] | select(.type=="tag" and .name=="v*") | .name' | grep -q .; then
  gh api -X POST "repos/$REPO/environments/release/deployment-branch-policies" -f name='v*' -f type=tag >/dev/null
fi
gh api "repos/$REPO/environments/release/deployment-branch-policies" --jq '.branch_policies[] | "allowed: \(.type) \(.name)"'

# 2. A new key, generated here, stored, destroyed.
umask 077
KEY=$(mktemp)
trap 'shred -u "$KEY" 2>/dev/null || rm -f "$KEY"' EXIT
PUB=$(cd "$KUBE_DC/cli" && go run ./hack/signchecksums -keygen "$KEY")
gh secret set KUBE_DC_UPDATE_SIGNING_KEY --env release -R "$REPO" < "$KEY"
gh secret list --env release -R "$REPO"

# 3. Only the public half leaves this machine.
echo
echo "PUBLIC KEY (send to bohdaq for cli/internal/selfupdate/selfupdate.go):"
echo "$PUB"

# Optional, recommended: only maintainers may create v* tags, so nobody else
# can start a release run that asks for an approval in the first place.
#   gh api -X POST "repos/$REPO/rulesets" --input - <<'JSON'
#   {"name":"release tags","target":"tag","enforcement":"active",
#    "conditions":{"ref_name":{"include":["refs/tags/v*"],"exclude":[]}},
#    "rules":[{"type":"creation"},{"type":"update"},{"type":"deletion"}],
#    "bypass_actors":[{"actor_id":5,"actor_type":"RepositoryRole","bypass_mode":"always"}]}
#   JSON
