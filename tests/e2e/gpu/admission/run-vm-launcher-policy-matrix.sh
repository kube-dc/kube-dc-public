#!/usr/bin/env bash

# Exercises the HAMi/KubeVirt Pod-policy boundary with server-side dry-run.
# KubeVirt may create a catalogued GPU launcher Pod; tenants may not create the
# same native resource directly or spoof a VMI ownerReference.

set -uo pipefail

for command in kubectl yq grep; do
  if ! command -v "${command}" >/dev/null 2>&1; then
    echo "required command is missing: ${command}" >&2
    exit 2
  fi
done

: "${KUBECONFIG:?set KUBECONFIG to the test-cluster kubeconfig}"
: "${GPU_ADMISSION_NAMESPACE:?set GPU_ADMISSION_NAMESPACE to a kube-dc project namespace}"
: "${GPU_ADMISSION_AS:?set GPU_ADMISSION_AS to a Pod-authorized non-system:masters tenant identity}"
: "${GPU_ADMISSION_KUBEVIRT_CONTROLLER_AS:=system:serviceaccount:kubevirt:kubevirt-controller}"

if [[ ! "${GPU_ADMISSION_NAMESPACE}" =~ ^[a-z0-9]([-a-z0-9]*[a-z0-9])?$ ]]; then
  echo "GPU_ADMISSION_NAMESPACE is not a valid DNS label" >&2
  exit 2
fi

script_dir=$(cd -- "$(dirname -- "${BASH_SOURCE[0]}")" && pwd)
fixture="${script_dir}/valid-gpu-vm-launcher-pod.yaml"
base_args=(--kubeconfig "${KUBECONFIG}")
tenant_args=("${base_args[@]}")
tenant_args+=(--as="${GPU_ADMISSION_AS}")
controller_args=("${base_args[@]}" --as="${GPU_ADMISSION_KUBEVIRT_CONTROLLER_AS}")
failures=0

render_fixture() {
  local expression=$1
  GPU_ADMISSION_NAMESPACE="${GPU_ADMISSION_NAMESPACE}" yq e \
    '(.metadata.namespace = strenv(GPU_ADMISSION_NAMESPACE)) | ('"${expression}"')' \
    "${fixture}"
}

run_case() {
  local expectation=$1
  local label=$2
  local actor=$3
  local expression=$4
  local expected_message=${5:-}
  local -a args
  local output
  local status

  if [[ "${actor}" == controller ]]; then
    args=("${controller_args[@]}")
  else
    args=("${tenant_args[@]}")
  fi

  output=$(render_fixture "${expression}" | \
    kubectl "${args[@]}" create --dry-run=server -o name -f - 2>&1)
  status=$?
  if [[ "${expectation}" == allow && ${status} -eq 0 ]]; then
    printf 'PASS allow: %s\n' "${label}"
  elif [[ "${expectation}" == deny && ${status} -ne 0 ]] && \
    printf '%s' "${output}" | grep -Fq "${expected_message}"; then
    printf 'PASS deny:  %s\n' "${label}"
  else
    printf 'FAIL %s: %s :: status=%s output=%s\n' \
      "${expectation}" "${label}" "${status}" "${output}" >&2
    failures=$((failures + 1))
  fi
}

deny_ephemeral_attach_case() {
  local pod=${GPU_ADMISSION_VM_LAUNCHER_POD:-}
  local output
  local status

  if [[ -z "${pod}" ]]; then
    printf 'SKIP deny:  launcher-ephemeral-attach (set GPU_ADMISSION_VM_LAUNCHER_POD)\n'
    return
  fi
  if [[ ! "${pod}" =~ ^[a-z0-9]([-a-z0-9.]*[a-z0-9])?$ ]]; then
    printf 'FAIL deny:  launcher-ephemeral-attach :: invalid Pod name\n' >&2
    failures=$((failures + 1))
    return
  fi

  output=$(kubectl "${tenant_args[@]}" -n "${GPU_ADMISSION_NAMESPACE}" get pod "${pod}" -o json | \
    yq e -o=json '.spec.ephemeralContainers = ((.spec.ephemeralContainers // []) + [{"name":"gpu-policy-debug","image":"registry.k8s.io/pause:3.10","targetContainerName":"compute"}]) | del(.status)' - | \
    kubectl "${tenant_args[@]}" replace \
      --raw "/api/v1/namespaces/${GPU_ADMISSION_NAMESPACE}/pods/${pod}/ephemeralcontainers?dryRun=All" \
      -f - 2>&1)
  status=$?
  if [[ ${status} -ne 0 ]] && printf '%s' "${output}" | grep -Fq "${ephemeral}"; then
    printf 'PASS deny:  launcher-ephemeral-attach\n'
  else
    printf 'FAIL deny:  launcher-ephemeral-attach :: status=%s output=%s\n' \
      "${status}" "${output}" >&2
    failures=$((failures + 1))
  fi
}

launcher_only='Whole-GPU VM resources may only be requested by KubeVirt launcher Pods.'
launcher_shape='GPU VM launcher Pods must carry their VMI owner and selected device resource.'
profile_shape='The kube-dc GPU profile label is unknown or the Pod does not request that profile.'
ephemeral='Ephemeral containers cannot be attached to GPU VM launcher Pods.'

run_case allow controller-launcher controller '.'
run_case deny tenant-direct-resource tenant '.' "${launcher_only}"
run_case deny tenant-spoofed-vmi-owner tenant \
  '.metadata.ownerReferences[0].uid = "00000000-0000-0000-0000-000000000002"' \
  "${launcher_only}"
run_case deny controller-missing-owner controller \
  'del(.metadata.ownerReferences)' "${launcher_shape}"
run_case deny controller-label-without-resource controller \
  'del(.spec.containers[0].resources)' "${launcher_shape}"
run_case deny controller-resource-without-label controller \
  'del(.metadata.labels)' "${launcher_only}"
deny_ephemeral_attach_case

printf 'TOTAL FAILURES: %d\n' "${failures}"
exit "${failures}"
