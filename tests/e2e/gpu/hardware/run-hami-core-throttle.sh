#!/usr/bin/env bash

# Measures HAMi's strict gpucores duty-cycle enforcement on an approved lab.
# The workload and namespace are disposable; an atomic lock serializes this
# script with the broader hardware suite.

set -euo pipefail

source "$(cd -- "$(dirname -- "${BASH_SOURCE[0]}")" && pwd)/require-opt-in.sh"

for command in kubectl yq awk grep seq date sleep; do
  if ! command -v "${command}" >/dev/null 2>&1; then
    echo "required command is missing: ${command}" >&2
    exit 2
  fi
done

: "${KUBECONFIG:?set KUBECONFIG to the approved lab-cluster kubeconfig}"

script_dir=$(cd -- "$(dirname -- "${BASH_SOURCE[0]}")" && pwd)
kubectl_args=(--kubeconfig "${KUBECONFIG}")
run_id="hami-throttle-$(date +%s)-${RANDOM}"
namespace=${GPU_THROTTLE_NAMESPACE:-${run_id}}
lock_namespace=${GPU_HARDWARE_LOCK_NAMESPACE:-gpu-operator}
lock_name=${GPU_HARDWARE_LOCK_NAME:-kube-dc-gpu-hardware-suite-lock}
canary_name=${GPU_HARDWARE_CANARY_NAME:-hami-device-plugin-canary}
profile_id=${GPU_HARDWARE_PROFILE_ID:-nvidia-v100-hami}
expected_shares=${GPU_HARDWARE_EXPECTED_SHARES:-20}
core_limit=${GPU_THROTTLE_CORE_LIMIT:-30}
sample_count=${GPU_THROTTLE_SAMPLE_COUNT:-36}
sample_interval=${GPU_THROTTLE_SAMPLE_INTERVAL:-5}
warmup_seconds=${GPU_THROTTLE_WARMUP_SECONDS:-120}
minimum_average=${GPU_THROTTLE_MINIMUM_AVERAGE:-15}
maximum_average=${GPU_THROTTLE_MAXIMUM_AVERAGE:-45}
timeout=${GPU_HARDWARE_TIMEOUT:-900s}
canary_was_suspended=
lock_acquired=false

if [[ ! "${namespace}" =~ ^[a-z0-9]([-a-z0-9]*[a-z0-9])?$ ]] || (( ${#namespace} > 63 )); then
  echo "GPU_THROTTLE_NAMESPACE must be a valid DNS label of at most 63 characters" >&2
  exit 2
fi
for value in core_limit sample_count sample_interval warmup_seconds; do
  if ! [[ ${!value} =~ ^[1-9][0-9]*$ ]]; then
    echo "${value} must be a positive integer" >&2
    exit 2
  fi
done
if (( core_limit > 100 )); then
  echo "core_limit must not exceed 100" >&2
  exit 2
fi

preflight() {
  node_json=$(kubectl "${kubectl_args[@]}" get nodes -o json)
  wrong_mode=$(printf '%s' "${node_json}" | yq e \
    '[.items[] | select(.metadata.labels."kube-dc.com/gpu.expected-workload-mode" == "pod-hami") | select(.metadata.labels."kube-dc.com/gpu.workload-mode" != "pod-hami")] | length' -)
  shares=$(printf '%s' "${node_json}" | yq e \
    '.items[] | select(.metadata.labels."kube-dc.com/gpu.expected-workload-mode" == "pod-hami") | (.status.allocatable."nvidia.com/gpu" // "0")' - | \
    awk '{ total += $1 } END { print total + 0 }')
  active_holders=$(kubectl "${kubectl_args[@]}" get pods -A -o json | yq e \
    '[.items[] | select(.status.phase != "Succeeded" and .status.phase != "Failed") | select([.spec.containers[]? | select(.resources.requests."nvidia.com/gpu" != null)] | length > 0)] | length' -)
  if [[ "${wrong_mode}" != 0 || "${shares}" != "${expected_shares}" || "${active_holders}" != 0 ]]; then
    echo "preflight failed: wrongMode=${wrong_mode} shares=${shares}/${expected_shares} activeHolders=${active_holders}" >&2
    return 1
  fi
}

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
preflight
trap cleanup EXIT
if ! kubectl "${kubectl_args[@]}" -n "${lock_namespace}" create configmap "${lock_name}" \
  --from-literal="holder=${run_id}" --from-literal="startedAt=$(date -u +%Y-%m-%dT%H:%M:%SZ)" >/dev/null; then
  echo "GPU hardware suite lock is already held" >&2
  exit 2
fi
lock_acquired=true
preflight

canary_was_suspended=$(kubectl "${kubectl_args[@]}" -n "${lock_namespace}" get cronjob "${canary_name}" \
  -o jsonpath='{.spec.suspend}')
[[ -n "${canary_was_suspended}" ]] || canary_was_suspended=false
kubectl "${kubectl_args[@]}" -n "${lock_namespace}" patch cronjob "${canary_name}" \
  --type=merge -p '{"spec":{"suspend":true}}' >/dev/null

kubectl "${kubectl_args[@]}" create namespace "${namespace}" >/dev/null
kubectl "${kubectl_args[@]}" label namespace "${namespace}" \
  pod-security.kubernetes.io/enforce=restricted \
  pod-security.kubernetes.io/enforce-version=latest >/dev/null

GPU_THROTTLE_NAMESPACE="${namespace}" GPU_PROFILE_ID="${profile_id}" GPU_CORE_LIMIT="${core_limit}" \
  yq e '
    .metadata.namespace = strenv(GPU_THROTTLE_NAMESPACE) |
    .metadata.labels."kube-dc.com/gpu-profile" = strenv(GPU_PROFILE_ID) |
    .spec.containers[0].resources.requests."nvidia.com/gpucores" = strenv(GPU_CORE_LIMIT) |
    .spec.containers[0].resources.limits."nvidia.com/gpucores" = strenv(GPU_CORE_LIMIT)
  ' "${script_dir}/hami-core-throttle-pod.yaml" | kubectl "${kubectl_args[@]}" create -f - >/dev/null

kubectl "${kubectl_args[@]}" -n "${namespace}" wait --for=condition=Ready pod/hami-core-throttle \
  --timeout="${timeout}" >/dev/null
for attempt in $(seq 1 60); do
  if kubectl "${kubectl_args[@]}" -n "${namespace}" logs hami-core-throttle | grep -Fq 'benchmark-ready'; then
    break
  fi
  sleep 1
done
if (( attempt == 60 )); then
  echo "benchmark did not report ready" >&2
  exit 1
fi

allocation=$(kubectl "${kubectl_args[@]}" -n "${namespace}" get pod hami-core-throttle \
  -o jsonpath='{.metadata.annotations.hami\.io/vgpu-devices-allocated}')
if ! grep -Eq ",NVIDIA,4096,${core_limit}:" <<<"${allocation}"; then
  echo "unexpected HAMi allocation: ${allocation}" >&2
  exit 1
fi
device_uuid=${allocation%%,*}
node_name=$(kubectl "${kubectl_args[@]}" -n "${namespace}" get pod hami-core-throttle -o jsonpath='{.spec.nodeName}')
driver_pod=$(kubectl "${kubectl_args[@]}" -n gpu-operator get pods -l app=nvidia-driver-daemonset -o json | \
  NODE_NAME="${node_name}" yq e -r '.items[] | select(.spec.nodeName == strenv(NODE_NAME) and .status.phase == "Running") | .metadata.name' - | head -n 1)
if [[ -z "${driver_pod}" ]]; then
  echo "no running NVIDIA driver Pod found on ${node_name}" >&2
  exit 1
fi

injected_env=$(kubectl "${kubectl_args[@]}" -n "${namespace}" exec hami-core-throttle -- env)
grep -Fx "CUDA_DEVICE_SM_LIMIT=${core_limit}" <<<"${injected_env}" >/dev/null
grep -Fx 'GPU_CORE_UTILIZATION_POLICY=force' <<<"${injected_env}" >/dev/null
sleep "${warmup_seconds}"

samples=()
for index in $(seq 1 "${sample_count}"); do
  utilization=$(kubectl "${kubectl_args[@]}" -n gpu-operator exec "${driver_pod}" -- \
    nvidia-smi --query-gpu=uuid,utilization.gpu --format=csv,noheader,nounits | \
    awk -F, -v uuid="${device_uuid}" '$1 == uuid { gsub(/[[:space:]]/, "", $2); print $2 }')
  if ! [[ "${utilization}" =~ ^[0-9]+$ ]] || (( utilization > 100 )); then
    echo "invalid utilization sample ${index}: ${utilization:-empty}" >&2
    exit 1
  fi
  samples+=("${utilization}")
  sleep "${sample_interval}"
done

summary=$(printf '%s\n' "${samples[@]}" | awk '
  { sum += $1; if ($1 > 0) nonzero++; if ($1 > max) max = $1 }
  END { printf "average=%.2f nonzero=%d max=%d", sum / NR, nonzero, max }
')
average=${summary#average=}
average=${average%% *}
if ! awk -v average="${average}" -v minimum="${minimum_average}" -v maximum="${maximum_average}" \
  'BEGIN { exit !(average >= minimum && average <= maximum) }'; then
  echo "core throttle outside acceptance band ${minimum_average}-${maximum_average}: ${summary}; samples=${samples[*]}" >&2
  exit 1
fi

printf 'PASS throttle: requested=%s%% %s samples=%s interval=%ss uuid=%s\n' \
  "${core_limit}" "${summary}" "${sample_count}" "${sample_interval}" "${device_uuid}"
