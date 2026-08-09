#!/usr/bin/env bash

# Proves HNC HRQ propagation, admission, status accounting, and release for
# HAMi's count, memory, and core request resources without scheduling a Pod.

set -euo pipefail

for command in kubectl yq grep; do
  if ! command -v "${command}" >/dev/null 2>&1; then
    echo "required command is missing: ${command}" >&2
    exit 2
  fi
done

: "${KUBECONFIG:?set KUBECONFIG to the test-cluster kubeconfig}"
: "${GPU_QUOTA_ROOT_NAMESPACE:?set GPU_QUOTA_ROOT_NAMESPACE to a new disposable namespace}"

if [[ ! "${GPU_QUOTA_ROOT_NAMESPACE}" =~ ^[a-z0-9]([-a-z0-9]*[a-z0-9])?$ ]]; then
  echo "GPU_QUOTA_ROOT_NAMESPACE is not a valid DNS label" >&2
  exit 2
fi

child_namespace="${GPU_QUOTA_ROOT_NAMESPACE}-child"
if (( ${#child_namespace} > 63 )); then
  echo "derived child namespace exceeds 63 characters" >&2
  exit 2
fi

script_dir=$(cd -- "$(dirname -- "${BASH_SOURCE[0]}")" && pwd)
kubectl_args=(--kubeconfig "${KUBECONFIG}")
hrq_name=gpu-accounting-validation
first_pod=gpu-accounting-one
second_pod=gpu-accounting-two

if kubectl "${kubectl_args[@]}" get namespace "${GPU_QUOTA_ROOT_NAMESPACE}" >/dev/null 2>&1; then
  echo "refusing to reuse existing namespace ${GPU_QUOTA_ROOT_NAMESPACE}" >&2
  exit 2
fi

cleanup() {
  kubectl "${kubectl_args[@]}" -n "${GPU_QUOTA_ROOT_NAMESPACE}" \
    delete subnamespaceanchor "${child_namespace}" --ignore-not-found --wait=false >/dev/null 2>&1 || true
  kubectl "${kubectl_args[@]}" delete namespace "${child_namespace}" \
    --ignore-not-found --wait=false >/dev/null 2>&1 || true
  kubectl "${kubectl_args[@]}" delete namespace "${GPU_QUOTA_ROOT_NAMESPACE}" \
    --ignore-not-found --wait=false >/dev/null 2>&1 || true
}
trap cleanup EXIT

render() {
  local file=$1
  local expression=$2
  GPU_QUOTA_ROOT_NAMESPACE="${GPU_QUOTA_ROOT_NAMESPACE}" \
  GPU_QUOTA_CHILD_NAMESPACE="${child_namespace}" \
    yq e "${expression}" "${file}"
}

resource_value() {
  local object=$1
  local resource_name=$2
  kubectl "${kubectl_args[@]}" get ${object} -o json | \
    RESOURCE_NAME="${resource_name}" yq e '.status.used[strenv(RESOURCE_NAME)] // "0"' -
}

wait_for_used() {
  local object=$1
  local resource_name=$2
  local expected=$3
  local actual
  local attempt
  for attempt in $(seq 1 60); do
    actual=$(resource_value "${object}" "${resource_name}" 2>/dev/null || true)
    if [[ "${actual}" == "${expected}" ]]; then
      printf 'PASS used:   %s=%s\n' "${resource_name}" "${expected}"
      return 0
    fi
    sleep 1
  done
  printf 'FAIL used:   %s expected=%s actual=%s\n' \
    "${resource_name}" "${expected}" "${actual:-unavailable}" >&2
  return 1
}

create_pod() {
  local name=$1
  local count=$2
  local memory=$3
  local cores=$4
  POD_NAME="${name}" GPU_COUNT="${count}" GPU_MEMORY="${memory}" GPU_CORES="${cores}" \
    render "${script_dir}/hami-quota-pod.yaml" \
      '(.metadata.name = strenv(POD_NAME)) |
       (.metadata.namespace = strenv(GPU_QUOTA_CHILD_NAMESPACE)) |
       (.spec.containers[0].resources.requests."nvidia.com/gpu" = strenv(GPU_COUNT)) |
       (.spec.containers[0].resources.limits."nvidia.com/gpu" = strenv(GPU_COUNT)) |
       (.spec.containers[0].resources.requests."nvidia.com/gpumem" = strenv(GPU_MEMORY)) |
       (.spec.containers[0].resources.limits."nvidia.com/gpumem" = strenv(GPU_MEMORY)) |
       (.spec.containers[0].resources.requests."nvidia.com/gpucores" = strenv(GPU_CORES)) |
       (.spec.containers[0].resources.limits."nvidia.com/gpucores" = strenv(GPU_CORES))' | \
    kubectl "${kubectl_args[@]}" create -f -
}

deny_pod() {
  local label=$1
  local name=$2
  local count=$3
  local memory=$4
  local cores=$5
  local expected_resource=$6
  local output
  local status
  if output=$(create_pod "${name}" "${count}" "${memory}" "${cores}" 2>&1); then
    status=0
  else
    status=$?
  fi
  if [[ ${status} -ne 0 ]] && printf '%s' "${output}" | grep -Fq "${expected_resource}"; then
    printf 'PASS deny:   %s\n' "${label}"
  else
    printf 'FAIL deny:   %s status=%s output=%s\n' "${label}" "${status}" "${output}" >&2
    return 1
  fi
}

# Prove this is HAMi's default annotation/request mode, not mock capacity.
published_nodes=$(kubectl "${kubectl_args[@]}" get nodes -o json | \
  yq e '[.items[] | select(.status.allocatable."nvidia.com/gpumem" != null or .status.allocatable."nvidia.com/gpucores" != null)] | length' -)
if [[ "${published_nodes}" != "0" ]]; then
  echo "expected no Node to publish gpumem/gpucores; mock-device capacity is active" >&2
  exit 1
fi
printf 'PASS mode:   gpumem/gpucores are request-only (no mock Node capacity)\n'

kubectl "${kubectl_args[@]}" create namespace "${GPU_QUOTA_ROOT_NAMESPACE}" >/dev/null
render "${script_dir}/hami-subnamespace.yaml" \
  '(.metadata.namespace = strenv(GPU_QUOTA_ROOT_NAMESPACE)) |
   (.metadata.name = strenv(GPU_QUOTA_CHILD_NAMESPACE))' | \
  kubectl "${kubectl_args[@]}" apply -f - >/dev/null
render "${script_dir}/hami-hrq.yaml" \
  '.metadata.namespace = strenv(GPU_QUOTA_ROOT_NAMESPACE)' | \
  kubectl "${kubectl_args[@]}" apply -f - >/dev/null

kubectl "${kubectl_args[@]}" wait --for=jsonpath='{.status.phase}'=Active \
  "namespace/${child_namespace}" --timeout=90s >/dev/null
for attempt in $(seq 1 60); do
  if kubectl "${kubectl_args[@]}" -n "${child_namespace}" \
    get resourcequota hrq.hnc.x-k8s.io >/dev/null 2>&1; then
    break
  fi
  sleep 1
done
kubectl "${kubectl_args[@]}" -n "${child_namespace}" \
  get resourcequota hrq.hnc.x-k8s.io >/dev/null
printf 'PASS setup:  HRQ propagated to %s\n' "${child_namespace}"

create_pod "${first_pod}" 1 1024 10 >/dev/null
printf 'PASS allow:  first HAMi request\n'
wait_for_used "hierarchicalresourcequota/${hrq_name} -n ${GPU_QUOTA_ROOT_NAMESPACE}" \
  requests.nvidia.com/gpu 1
wait_for_used "hierarchicalresourcequota/${hrq_name} -n ${GPU_QUOTA_ROOT_NAMESPACE}" \
  requests.nvidia.com/gpumem 1024
wait_for_used "hierarchicalresourcequota/${hrq_name} -n ${GPU_QUOTA_ROOT_NAMESPACE}" \
  requests.nvidia.com/gpucores 10

deny_pod count-overflow gpu-accounting-count-overflow 2 1024 10 requests.nvidia.com/gpu
deny_pod memory-overflow gpu-accounting-memory-overflow 1 4096 10 requests.nvidia.com/gpumem
deny_pod core-overflow gpu-accounting-core-overflow 1 1024 50 requests.nvidia.com/gpucores

create_pod "${second_pod}" 1 1024 10 >/dev/null
printf 'PASS allow:  second in-budget HAMi request\n'
wait_for_used "hierarchicalresourcequota/${hrq_name} -n ${GPU_QUOTA_ROOT_NAMESPACE}" \
  requests.nvidia.com/gpu 2
wait_for_used "hierarchicalresourcequota/${hrq_name} -n ${GPU_QUOTA_ROOT_NAMESPACE}" \
  requests.nvidia.com/gpumem 2048
wait_for_used "hierarchicalresourcequota/${hrq_name} -n ${GPU_QUOTA_ROOT_NAMESPACE}" \
  requests.nvidia.com/gpucores 20

kubectl "${kubectl_args[@]}" -n "${child_namespace}" delete pod "${second_pod}" --wait=true >/dev/null
wait_for_used "hierarchicalresourcequota/${hrq_name} -n ${GPU_QUOTA_ROOT_NAMESPACE}" \
  requests.nvidia.com/gpu 1
kubectl "${kubectl_args[@]}" -n "${child_namespace}" delete pod "${first_pod}" --wait=true >/dev/null
wait_for_used "hierarchicalresourcequota/${hrq_name} -n ${GPU_QUOTA_ROOT_NAMESPACE}" \
  requests.nvidia.com/gpu 0
wait_for_used "hierarchicalresourcequota/${hrq_name} -n ${GPU_QUOTA_ROOT_NAMESPACE}" \
  requests.nvidia.com/gpumem 0
wait_for_used "hierarchicalresourcequota/${hrq_name} -n ${GPU_QUOTA_ROOT_NAMESPACE}" \
  requests.nvidia.com/gpucores 0

printf 'TOTAL FAILURES: 0\n'
