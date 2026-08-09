#!/usr/bin/env bash
#
# B-09: unstick the ui-test orgs wedged on orphaned ObjectBucket finalizers.
#
# WHAT IS WRONG
# -------------
# Rook's lib-bucket-provisioner stamps `objectbucket.io/finalizer` on the Secret
# and ConfigMap it generates for an ObjectBucketClaim. When the OBC is deleted
# out from under the provisioner — which is what the UI e2e teardown does — the
# generated pair can survive with the finalizer still on it and nothing left to
# remove it. The namespace then cannot finish terminating, which blocks the
# Project finalizer, which blocks the Organization finalizer, which leaves the
# project's VPC and EIPs live and hot-looping kube-ovn-controller.
#
# On stage this had accumulated for 25 days (oldest org 2026-07-03) and had
# grown to ~1800 ovn-eips before the first cleanup pass.
#
# WHAT THIS SCRIPT TOUCHES
# ------------------------
# ONLY objects that are BOTH:
#   1. in a namespace that is already Terminating, AND
#   2. have no ObjectBucketClaim of the same name still alive.
#
# That second condition is the important one. Most objects carrying this
# finalizer on stage are LIVE and load-bearing — monitoring/mimir-blocks,
# monitoring/loki-chunks, kube-dc/registry-depot, openbao/openbao-snapshots.
# Stripping the finalizer off those would orphan real buckets. The script
# recomputes the set every run rather than trusting a baked-in list.
#
# It prints the plan and requires an explicit --yes to act.
#
# Usage:
#   KUBECONFIG=~/.kube/stage_config hack/stage-cleanup-orphaned-obc-finalizers.sh          # dry run
#   KUBECONFIG=~/.kube/stage_config hack/stage-cleanup-orphaned-obc-finalizers.sh --yes    # act

set -euo pipefail

APPLY=0
[[ "${1:-}" == "--yes" ]] && APPLY=1

command -v kubectl >/dev/null || { echo "kubectl not found" >&2; exit 1; }
kubectl version -o json >/dev/null 2>&1 || { echo "cannot reach the cluster; is KUBECONFIG set?" >&2; exit 1; }

echo "cluster: $(kubectl config current-context)"
echo

work="$(mktemp -d)"
trap 'rm -rf "$work"' EXIT

kubectl get ns -o json                  > "$work/ns.json"
kubectl get objectbucketclaims -A -o json > "$work/obc.json"
kubectl get secrets -A -o json          > "$work/sec.json"
kubectl get configmaps -A -o json       > "$work/cm.json"

python3 - "$work" <<'PY' > "$work/targets.tsv"
import json, sys
w = sys.argv[1]
term = {n['metadata']['name'] for n in json.load(open(f'{w}/ns.json'))['items']
        if n['status']['phase'] == 'Terminating'}
live = {(o['metadata']['namespace'], o['metadata']['name'])
        for o in json.load(open(f'{w}/obc.json'))['items']}
for kind, f in (('secret', 'sec.json'), ('configmap', 'cm.json')):
    for o in json.load(open(f'{w}/{f}'))['items']:
        m = o['metadata']
        if not any('objectbucket.io' in x for x in (m.get('finalizers') or [])):
            continue
        key = (m['namespace'], m['name'])
        if key in live or m['namespace'] not in term:
            continue                      # live OBC, or a healthy namespace
        print(f"{kind}\t{m['namespace']}\t{m['name']}")
PY

count=$(wc -l < "$work/targets.tsv" | tr -d ' ')
if [[ "$count" == "0" ]]; then
  echo "nothing orphaned — no finalizers to clear"
  exit 0
fi

echo "orphaned ObjectBucket finalizers in Terminating namespaces ($count):"
awk -F'\t' '{printf "  %-10s %-36s %s\n", $1, $2, $3}' "$work/targets.tsv"
echo

if [[ "$APPLY" == "0" ]]; then
  echo "dry run — re-run with --yes to clear these finalizers"
  exit 0
fi

while IFS=$'\t' read -r kind ns name; do
  [[ -z "$kind" ]] && continue
  printf '%-10s %-36s %-24s ' "$kind" "$ns" "$name"
  kubectl patch "$kind" "$name" -n "$ns" --type=merge \
    -p '{"metadata":{"finalizers":null}}' 2>&1 || true
done < "$work/targets.tsv"

echo
echo "namespaces still Terminating:"
kubectl get ns -o json | python3 -c "
import json,sys
t=[n['metadata']['name'] for n in json.load(sys.stdin)['items'] if n['status']['phase']=='Terminating']
print('  none' if not t else '\n'.join('  '+x for x in t))
"
echo
echo "organizations still Terminating:"
kubectl get organizations.kube-dc.com -A -o json | python3 -c "
import json,sys
t=[o['metadata']['name'] for o in json.load(sys.stdin)['items'] if o['metadata'].get('deletionTimestamp')]
print('  none' if not t else '\n'.join('  '+x for x in t))
"
echo
echo "ovn-eips remaining: $(kubectl get ovn-eip --no-headers 2>/dev/null | wc -l | tr -d ' ')"
