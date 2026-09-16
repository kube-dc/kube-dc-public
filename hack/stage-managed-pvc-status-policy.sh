#!/usr/bin/env bash
# Stage qualification only. Apply the two reviewed CSI/provenance variables,
# not an unrelated full platform release. Keeps the live handoff phases,
# release ownership, bindings, exclusions and every other policy expression.
# Ship the chart change through the normal fleet release before promotion.
set -euo pipefail
ROOT="$(cd "$(dirname "${BASH_SOURCE[0]}")/.." && pwd)"
STAGE_CONFIG="${STAGE_CONFIG:-$HOME/.kube/stage_config}"
k() { kubectl --kubeconfig="$STAGE_CONFIG" --request-timeout=20s "$@"; }
server=$(k config view --minify -o jsonpath='{.clusters[0].cluster.server}')
[[ "$server" == https://192.168.1.3:6443 ]] || { echo "Refusing: not the verified stage API" >&2; exit 1; }
[[ "${1:-}" == --apply ]] || { echo "Usage: $0 --apply (stage policy variables only)" >&2; exit 2; }
rendered=$(helm template kube-dc "$ROOT/charts/kube-dc" --namespace kube-dc \
  --show-only templates/vap-protect-managed-services.yaml \
  --set-string backend.gateway.hostname=backend.example.test | \
  yq -o=json 'select(.kind == "ValidatingAdmissionPolicy" and .metadata.name == "protect-managed-services-in-projects") | .spec.variables')
live=$(k get validatingadmissionpolicy protect-managed-services-in-projects -o json)
patch=$(jq -cn --argjson live "$live" --argjson rendered "$rendered" '
  ($live.spec.variables | map(.name) | index("rookOwn")) as $ri |
  ($live.spec.variables | map(.name) | index("rookCSIStatus")) as $ci |
  ($rendered[] | select(.name=="rookOwn")) as $rook |
  ($rendered[] | select(.name=="rookCSIStatus")) as $csi |
  if $ri == null then error("live rookOwn variable missing") else
  [{op:"test",path:"/metadata/resourceVersion",value:$live.metadata.resourceVersion}] +
  (if $ci == null then
    [{op:"add",path:("/spec/variables/"+($ri|tostring)),value:$csi},
     {op:"replace",path:("/spec/variables/"+(($ri+1)|tostring)),value:$rook}]
   else
    [{op:"replace",path:("/spec/variables/"+($ci|tostring)),value:$csi},
     {op:"replace",path:("/spec/variables/"+($ri|tostring)),value:$rook}]
   end) end')
k patch validatingadmissionpolicy protect-managed-services-in-projects --type=json -p "$patch" --dry-run=server >/dev/null
k patch validatingadmissionpolicy protect-managed-services-in-projects --type=json -p "$patch"
k get validatingadmissionpolicy protect-managed-services-in-projects -o json | \
  jq '{generation:.metadata.generation,typeChecking:.status.typeChecking}'
