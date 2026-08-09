#!/usr/bin/env bash

# Destructive only to a new disposable namespace. Proves quota rejection and
# release, concurrent fractional allocation, full-capacity queueing, and queue
# recovery on a real HAMi GPU node. Run only on an explicitly approved lab.

set -euo pipefail

source "$(cd -- "$(dirname -- "${BASH_SOURCE[0]}")" && pwd)/require-opt-in.sh"

for command in kubectl yq grep sort uniq wc seq date awk; do
  if ! command -v "${command}" >/dev/null 2>&1; then
    echo "required command is missing: ${command}" >&2
    exit 2
  fi
done

: "${KUBECONFIG:?set KUBECONFIG to the approved lab-cluster kubeconfig}"

script_dir=$(cd -- "$(dirname -- "${BASH_SOURCE[0]}")" && pwd)
kubectl_args=(--kubeconfig "${KUBECONFIG}")
run_id="hami-hardware-$(date +%s)-${RANDOM}"
namespace=${GPU_HARDWARE_NAMESPACE:-${run_id}}
lock_namespace=${GPU_HARDWARE_LOCK_NAMESPACE:-gpu-operator}
lock_name=${GPU_HARDWARE_LOCK_NAME:-kube-dc-gpu-hardware-suite-lock}
profile_id=${GPU_HARDWARE_PROFILE_ID:-nvidia-v100-hami}
expected_shares=${GPU_HARDWARE_EXPECTED_SHARES:-20}
expected_devices=${GPU_HARDWARE_EXPECTED_DEVICES:-2}
timeout=${GPU_HARDWARE_TIMEOUT:-300s}
hold_seconds=${GPU_HARDWARE_HOLD_SECONDS:-600}
suspend_canary=${GPU_HARDWARE_SUSPEND_CANARY:-false}
canary_name=${GPU_HARDWARE_CANARY_NAME:-hami-device-plugin-canary}
canary_was_suspended=
lock_acquired=false

if [[ ! "${namespace}" =~ ^[a-z0-9]([-a-z0-9]*[a-z0-9])?$ ]] || (( ${#namespace} > 63 )); then
  echo "GPU_HARDWARE_NAMESPACE must be a valid DNS label of at most 63 characters" >&2
  exit 2
fi

cleanup() {
  kubectl "${kubectl_args[@]}" delete namespace "${namespace}" \
    --ignore-not-found --wait=false >/dev/null 2>&1 || true

  if [[ -n "${canary_was_suspended}" ]]; then
    kubectl "${kubectl_args[@]}" -n "${lock_namespace}" patch cronjob "${canary_name}" \
      --type=merge -p "{\"spec\":{\"suspend\":${canary_was_suspended}}}" >/dev/null 2>&1 || true
  fi

  if [[ "${lock_acquired}" == true ]]; then
    holder=$(kubectl "${kubectl_args[@]}" -n "${lock_namespace}" get configmap "${lock_name}" \
      -o jsonpath='{.data.holder}' 2>/dev/null || true)
    if [[ "${holder}" == "${run_id}" ]]; then
      kubectl "${kubectl_args[@]}" -n "${lock_namespace}" delete configmap "${lock_name}" \
        --ignore-not-found >/dev/null 2>&1 || true
    fi
  fi
}

if kubectl "${kubectl_args[@]}" get namespace "${namespace}" >/dev/null 2>&1; then
  echo "refusing to reuse existing namespace ${namespace}" >&2
  exit 2
fi
trap cleanup EXIT

if ! kubectl "${kubectl_args[@]}" -n "${lock_namespace}" create configmap "${lock_name}" \
  --from-literal="holder=${run_id}" --from-literal="startedAt=$(date -u +%Y-%m-%dT%H:%M:%SZ)" >/dev/null; then
  echo "GPU hardware suite lock is already held:" >&2
  kubectl "${kubectl_args[@]}" -n "${lock_namespace}" get configmap "${lock_name}" -o yaml >&2 || true
  exit 2
fi
lock_acquired=true

if [[ "${suspend_canary}" == true ]]; then
  canary_was_suspended=$(kubectl "${kubectl_args[@]}" -n "${lock_namespace}" get cronjob "${canary_name}" \
    -o jsonpath='{.spec.suspend}')
  [[ -n "${canary_was_suspended}" ]] || canary_was_suspended=false
  kubectl "${kubectl_args[@]}" -n "${lock_namespace}" patch cronjob "${canary_name}" \
    --type=merge -p '{"spec":{"suspend":true}}' >/dev/null
fi

node_json=$(kubectl "${kubectl_args[@]}" get nodes -o json)
expected_nodes=$(printf '%s' "${node_json}" | yq e \
  '[.items[] | select(.metadata.labels."kube-dc.com/gpu.expected-workload-mode" == "pod-hami")] | length' -)
wrong_mode=$(printf '%s' "${node_json}" | yq e \
  '[.items[] | select(.metadata.labels."kube-dc.com/gpu.expected-workload-mode" == "pod-hami") | select(.metadata.labels."kube-dc.com/gpu.workload-mode" != "pod-hami")] | length' -)
shares=$(printf '%s' "${node_json}" | yq e \
  '.items[] | select(.metadata.labels."kube-dc.com/gpu.expected-workload-mode" == "pod-hami") | (.status.allocatable."nvidia.com/gpu" // "0")' - | \
  awk '{ total += $1 } END { print total + 0 }')

if [[ "${expected_nodes}" == 0 || "${wrong_mode}" != 0 || "${shares}" != "${expected_shares}" ]]; then
  echo "preflight failed: expectedNodes=${expected_nodes} wrongMode=${wrong_mode} shares=${shares} expectedShares=${expected_shares}" >&2
  exit 1
fi

for attempt in $(seq 1 60); do
  active_holders=$(kubectl "${kubectl_args[@]}" get pods -A -o json | yq e \
    '[.items[] | select(.status.phase != "Succeeded" and .status.phase != "Failed") | select([.spec.containers[]? | select(.resources.requests."nvidia.com/gpu" != null)] | length > 0)] | length' -)
  [[ "${active_holders}" == 0 ]] && break
  sleep 1
done
if [[ "${active_holders}" != 0 ]]; then
  echo "refusing to run with ${active_holders} active GPU holder(s)" >&2
  exit 1
fi
printf 'PASS preflight: mode=pod-hami shares=%s activeHolders=0\n' "${shares}"

kubectl "${kubectl_args[@]}" create namespace "${namespace}" >/dev/null
kubectl "${kubectl_args[@]}" label namespace "${namespace}" \
  pod-security.kubernetes.io/enforce=restricted \
  pod-security.kubernetes.io/enforce-version=latest >/dev/null

render_quota() {
  local count=$1 memory=$2 cores=$3
  GPU_HARDWARE_NAMESPACE="${namespace}" GPU_COUNT="${count}" GPU_MEMORY="${memory}" GPU_CORES="${cores}" \
    yq e '
      .metadata.namespace = strenv(GPU_HARDWARE_NAMESPACE) |
      .spec.hard."requests.nvidia.com/gpu" = strenv(GPU_COUNT) |
      .spec.hard."requests.nvidia.com/gpumem" = strenv(GPU_MEMORY) |
      .spec.hard."requests.nvidia.com/gpucores" = strenv(GPU_CORES)
    ' "${script_dir}/hami-hardware-quota.yaml"
}

create_probe() {
  local name=$1 memory=$2 cores=$3
  POD_NAME="${name}" GPU_HARDWARE_NAMESPACE="${namespace}" GPU_PROFILE_ID="${profile_id}" \
  GPU_MEMORY="${memory}" GPU_CORES="${cores}" HOLD_SECONDS="${hold_seconds}" \
    yq e '
      .metadata.name = strenv(POD_NAME) |
      .metadata.namespace = strenv(GPU_HARDWARE_NAMESPACE) |
      .metadata.labels."kube-dc.com/gpu-profile" = strenv(GPU_PROFILE_ID) |
      .spec.containers[0].args[0] = "nvidia-smi --query-gpu=uuid,name,memory.total --format=csv,noheader; sleep " + strenv(HOLD_SECONDS) |
      .spec.containers[0].resources.requests."nvidia.com/gpumem" = strenv(GPU_MEMORY) |
      .spec.containers[0].resources.limits."nvidia.com/gpumem" = strenv(GPU_MEMORY) |
      .spec.containers[0].resources.requests."nvidia.com/gpucores" = strenv(GPU_CORES) |
      .spec.containers[0].resources.limits."nvidia.com/gpucores" = strenv(GPU_CORES)
    ' "${script_dir}/hami-hardware-pod.yaml" | kubectl "${kubectl_args[@]}" create -f -
}

wait_ready() {
  kubectl "${kubectl_args[@]}" -n "${namespace}" wait --for=condition=Ready "pod/$1" --timeout="${timeout}" >/dev/null
}

quota_used() {
  local resource=$1
  RESOURCE="${resource}" kubectl "${kubectl_args[@]}" -n "${namespace}" get resourcequota gpu-hardware-suite -o json | \
    RESOURCE="${resource}" yq e '.status.used[strenv(RESOURCE)] // "0"' -
}

wait_quota_zero() {
  local attempt
  for attempt in $(seq 1 90); do
    count=$(quota_used requests.nvidia.com/gpu 2>/dev/null || true)
    memory=$(quota_used requests.nvidia.com/gpumem 2>/dev/null || true)
    cores=$(quota_used requests.nvidia.com/gpucores 2>/dev/null || true)
    if [[ "${count}" == 0 && "${memory}" == 0 && "${cores}" == 0 ]]; then
      return 0
    fi
    sleep 1
  done
  echo "quota did not release to zero: count=${count:-?} memory=${memory:-?} cores=${cores:-?}" >&2
  return 1
}

assert_allocation() {
  local name=$1 memory=$2 cores=$3
  allocation=$(kubectl "${kubectl_args[@]}" -n "${namespace}" get pod "${name}" \
    -o jsonpath='{.metadata.annotations.hami\.io/vgpu-devices-allocated}')
  if ! grep -Eq ",NVIDIA,${memory},${cores}:" <<<"${allocation}"; then
    echo "unexpected HAMi allocation for ${name}: ${allocation}" >&2
    return 1
  fi
  kubectl "${kubectl_args[@]}" -n "${namespace}" logs "${name}" -c cuda-probe | grep -F 'Tesla V100' >/dev/null
}

# Phase 1: ResourceQuota rejects overflow and releases every native dimension.
render_quota 1 4096 25 | kubectl "${kubectl_args[@]}" apply -f - >/dev/null
create_probe quota-one 4096 25 >/dev/null
wait_ready quota-one
if output=$(create_probe quota-overflow 4096 25 2>&1); then
  echo "quota overflow Pod was unexpectedly admitted" >&2
  exit 1
elif ! grep -Fq 'exceeded quota' <<<"${output}"; then
  echo "quota overflow failed for an unexpected reason: ${output}" >&2
  exit 1
fi
kubectl "${kubectl_args[@]}" -n "${namespace}" delete pod quota-one --wait=true >/dev/null
wait_quota_zero
printf 'PASS quota: deny and three-dimensional release\n'

# Phase 2: three fractional CUDA holders allocate with exact memory/core bounds.
render_quota 3 12288 75 | kubectl "${kubectl_args[@]}" apply -f - >/dev/null
for index in 1 2 3; do create_probe "share-${index}" 4096 25 >/dev/null; done
for index in 1 2 3; do wait_ready "share-${index}"; assert_allocation "share-${index}" 4096 25; done
unique_devices=$(kubectl "${kubectl_args[@]}" -n "${namespace}" get pods -l app.kubernetes.io/name=kube-dc-gpu-hardware-suite -o json | \
  yq e -r '.items[] | select(.metadata.name | test("^share-")) | .metadata.annotations."hami.io/vgpu-devices-allocated"' - | \
  sed 's/,.*//' | sort -u | wc -l)
unique_devices=${unique_devices//[[:space:]]/}
if (( unique_devices < 1 || unique_devices > expected_devices )); then
  echo "fractional holders used ${unique_devices} devices; expected at most ${expected_devices}" >&2
  exit 1
fi
kubectl "${kubectl_args[@]}" -n "${namespace}" delete pod -l app.kubernetes.io/name=kube-dc-gpu-hardware-suite --wait=true >/dev/null
wait_quota_zero
printf 'PASS sharing: holders=3 physicalDevices=%s memory=4096 core=25\n' "${unique_devices}"

# Phase 3: fill both V100s, prove the third request queues, then release one.
render_quota 3 98304 300 | kubectl "${kubectl_args[@]}" apply -f - >/dev/null
create_probe full-1 32768 100 >/dev/null
create_probe full-2 32768 100 >/dev/null
wait_ready full-1
wait_ready full-2
assert_allocation full-1 32768 100
assert_allocation full-2 32768 100
create_probe full-3 32768 100 >/dev/null

queued=false
for attempt in $(seq 1 30); do
  node_name=$(kubectl "${kubectl_args[@]}" -n "${namespace}" get pod full-3 -o jsonpath='{.spec.nodeName}')
  if [[ -z "${node_name}" ]]; then queued=true; else queued=false; break; fi
  sleep 1
done
if [[ "${queued}" != true ]]; then
  echo "third full-capacity request did not remain queued" >&2
  exit 1
fi
queue_message=$(kubectl "${kubectl_args[@]}" -n "${namespace}" get pod full-3 -o json | \
  yq e -r '.status.conditions[]? | select(.type == "PodScheduled" and .status == "False") | .message' -)
[[ -n "${queue_message}" ]] || { echo "queued Pod has no scheduling condition" >&2; exit 1; }
kubectl "${kubectl_args[@]}" -n "${namespace}" delete pod full-1 --wait=true >/dev/null
wait_ready full-3
assert_allocation full-3 32768 100
kubectl "${kubectl_args[@]}" -n "${namespace}" delete pod -l app.kubernetes.io/name=kube-dc-gpu-hardware-suite --wait=true >/dev/null
wait_quota_zero
printf 'PASS queue: full capacity queued, release allocated; reason=%s\n' "${queue_message}"

printf 'PASS cleanup: quota released; namespace=%s will be removed\n' "${namespace}"
