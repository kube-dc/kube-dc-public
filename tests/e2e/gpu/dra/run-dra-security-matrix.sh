#!/usr/bin/env bash

# Server-side dry-run admission/RBAC matrix for the fixed DRA product.
set -euo pipefail
source "$(cd -- "$(dirname -- "${BASH_SOURCE[0]}")/../hardware" && pwd)/require-opt-in.sh"

for command in kubectl yq grep mktemp; do
  command -v "${command}" >/dev/null 2>&1 || { echo "required command is missing: ${command}" >&2; exit 2; }
done
: "${KUBECONFIG:?set KUBECONFIG to the approved lab-cluster kubeconfig}"

script_dir=$(cd -- "$(dirname -- "${BASH_SOURCE[0]}")" && pwd)
namespace=${GPU_DRA_SECURITY_NAMESPACE:-shalb-test}
tenant_user=${GPU_DRA_TENANT_USER:-dra-security-test}
tenant_group=${GPU_DRA_TENANT_GROUP:-shalb:org-admin}
kubectl_args=(--kubeconfig "${KUBECONFIG}" -n "${namespace}")
tenant_args=(--as="${tenant_user}" --as-group="${tenant_group}")
tmp_dir=$(mktemp -d)
trap 'rm -rf "${tmp_dir}"' EXIT

expect_tenant_deny() {
  local label=$1 pattern=$2 file=$3
  if output=$(kubectl "${kubectl_args[@]}" "${tenant_args[@]}" create --dry-run=server -f "${file}" 2>&1); then
    echo "${label}: unexpectedly allowed" >&2; exit 1
  fi
  grep -E "${pattern}" <<<"${output}" >/dev/null || { echo "${label}: wrong denial: ${output}" >&2; exit 1; }
  printf 'PASS tenant deny: %s\n' "${label}"
}

expect_tenant_allow() {
  local label=$1 file=$2
  if ! output=$(kubectl "${kubectl_args[@]}" "${tenant_args[@]}" create --dry-run=server -f "${file}" 2>&1); then
    echo "${label}: unexpectedly denied: ${output}" >&2
    exit 1
  fi
  printf 'PASS tenant allow: %s\n' "${label}"
}

[[ $(kubectl --kubeconfig "${KUBECONFIG}" auth can-i create deployments.apps -n "${namespace}" "${tenant_args[@]}") == yes ]]
[[ $(kubectl --kubeconfig "${KUBECONFIG}" auth can-i create pods -n "${namespace}" "${tenant_args[@]}") == yes ]]
[[ $(kubectl --kubeconfig "${KUBECONFIG}" auth can-i create resourceclaimtemplates.resource.k8s.io -n "${namespace}" "${tenant_args[@]}" || true) == no ]]
[[ $(kubectl --kubeconfig "${KUBECONFIG}" auth can-i create resourceclaims.resource.k8s.io -n "${namespace}" "${tenant_args[@]}" || true) == no ]]
printf 'PASS RBAC: tenant may create ordinary Deployments but not raw DRA claims/templates\n'

# These two cases protect the platform-wide negative-selection boundary. The
# DRA policies bind to every project namespace, so a missing gpu-backend label
# must make the CEL match condition false, never raise a no-such-key error.
expect_tenant_allow ordinary-pod "${script_dir}/ordinary-pod.yaml"
expect_tenant_allow ordinary-deployment "${script_dir}/ordinary-deployment.yaml"

yq e 'select(.kind == "Deployment")' "${script_dir}/canonical-workload.yaml" >"${tmp_dir}/deployment.yaml"
expect_tenant_deny protected-deployment 'managed through the kube-dc API|forbidden' "${tmp_dir}/deployment.yaml"
yq e 'select(.kind == "ResourceClaimTemplate")' "${script_dir}/canonical-workload.yaml" >"${tmp_dir}/template.yaml"
expect_tenant_deny raw-template 'forbidden|managed by kube-dc' "${tmp_dir}/template.yaml"

for mutation in node-selector privileged env-from rolling-update foreign-claim; do
  case "${mutation}" in
    node-selector) expression='.spec.template.spec.nodeSelector.evil = "true"' ;;
    privileged) expression='.spec.template.spec.containers[0].securityContext.allowPrivilegeEscalation = true' ;;
    env-from) expression='.spec.template.spec.containers[0].envFrom = [{"configMapRef":{"name":"evil"}}]' ;;
    rolling-update) expression='.spec.strategy.type = "RollingUpdate"' ;;
    foreign-claim) expression='.spec.template.spec.resourceClaims[0] = {"name":"gpu","resourceClaimName":"tenant-claim"}' ;;
  esac
  yq e "select(.kind == \"Deployment\") | ${expression}" "${script_dir}/canonical-workload.yaml" >"${tmp_dir}/${mutation}.yaml"
  if kubectl "${kubectl_args[@]}" create --dry-run=server -f "${tmp_dir}/${mutation}.yaml" >/dev/null 2>"${tmp_dir}/${mutation}.err"; then
    echo "${mutation}: cluster-admin shape dry-run unexpectedly allowed" >&2; exit 1
  fi
  grep -E 'DRA GPU Deployments' "${tmp_dir}/${mutation}.err" >/dev/null || { cat "${tmp_dir}/${mutation}.err" >&2; exit 1; }
  printf 'PASS shape deny: %s\n' "${mutation}"
done

yq e 'select(.kind == "ResourceClaimTemplate") | .spec.spec.devices.requests[0].exactly.count = 2' "${script_dir}/canonical-workload.yaml" >"${tmp_dir}/oversized-template.yaml"
if kubectl "${kubectl_args[@]}" create --dry-run=server -f "${tmp_dir}/oversized-template.yaml" >/dev/null 2>"${tmp_dir}/oversized-template.err"; then
  echo "oversized claim template unexpectedly allowed" >&2; exit 1
fi
grep -E 'fixed product' "${tmp_dir}/oversized-template.err" >/dev/null
printf 'PASS shape deny: oversized fixed product\n'

kubectl "${kubectl_args[@]}" create --dry-run=server -f "${script_dir}/canonical-workload.yaml" >/dev/null
printf 'PASS allow: canonical backend-owned DRA objects pass shape validation\n'
