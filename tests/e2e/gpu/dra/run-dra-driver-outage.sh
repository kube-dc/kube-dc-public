#!/usr/bin/env bash

# Reversible lab-only failure injection. Stops the HAMi DRA kubelet plugin,
# proves a fixed-SKU workload cannot start, restores the driver through its
# original selector, and proves the same workload recovers without re-creation.
set -euo pipefail
source "$(cd -- "$(dirname -- "${BASH_SOURCE[0]}")/../hardware" && pwd)/require-opt-in.sh"

for command in kubectl yq grep date seq; do
  command -v "${command}" >/dev/null 2>&1 || { echo "required command is missing: ${command}" >&2; exit 2; }
done
: "${KUBECONFIG:?set KUBECONFIG to the approved lab-cluster kubeconfig}"

script_dir=$(cd -- "$(dirname -- "${BASH_SOURCE[0]}")" && pwd)
k=(kubectl --kubeconfig "${KUBECONFIG}")
run_id="dra-outage-$(date +%s)-${RANDOM}"
namespace=${GPU_DRA_NAMESPACE:-${run_id}}
class=${GPU_DRA_DEVICE_CLASS:-kube-dc-nvidia-v100-shared-8g}
quota_resource="${class}.deviceclass.resource.k8s.io/devices"
driver_namespace=${GPU_DRA_DRIVER_NAMESPACE:-hami-system}
driver_name=${GPU_DRA_DRIVER_DAEMONSET:-hami-dra-driver-kubelet-plugin}
flux_namespace=${GPU_DRA_FLUX_NAMESPACE:-flux-system}
flux_name=${GPU_DRA_FLUX_KUSTOMIZATION:-hami}
flux_parent_name=${GPU_DRA_FLUX_PARENT_KUSTOMIZATION:-flux-system}
lock_namespace=${GPU_HARDWARE_LOCK_NAMESPACE:-gpu-operator}
lock_name=${GPU_HARDWARE_LOCK_NAME:-kube-dc-gpu-hardware-suite-lock}
timeout=${GPU_DRA_TIMEOUT:-300s}
flux_stabilize_seconds=${GPU_DRA_FLUX_STABILIZE_SECONDS:-30}
flux_restore_attempts=${GPU_DRA_FLUX_RESTORE_ATTEMPTS:-30}
lock_acquired=false
driver_mutated=false
flux_mutated=false
flux_parent_mutated=false
namespace_created=false

resume_flux_owner() {
  local name=$1
  for _ in $(seq 1 "${flux_restore_attempts}"); do
    if "${k[@]}" -n "${flux_namespace}" patch kustomization "${name}" --type=merge -p='{"spec":{"suspend":false}}' >/dev/null 2>&1 &&
       [[ "$("${k[@]}" -n "${flux_namespace}" get kustomization "${name}" -o jsonpath='{.spec.suspend}' 2>/dev/null)" != "true" ]]; then
      return 0
    fi
    sleep 1
  done
  echo "failed to resume Flux Kustomization ${flux_namespace}/${name}" >&2
  return 1
}

restore_driver() {
  local restore_failed=false
  if [[ "${driver_mutated}" == true ]]; then
    if "${k[@]}" -n "${driver_namespace}" patch daemonset "${driver_name}" --type=json -p='[{"op":"remove","path":"/spec/template/spec/nodeSelector/kube-dc.com~1gpu.outage-test"}]' >/dev/null 2>&1; then
      driver_mutated=false
    else
      echo "failed to restore HAMi DRA driver selector" >&2
      restore_failed=true
    fi
  fi
  if [[ "${flux_mutated}" == true ]]; then
    if resume_flux_owner "${flux_name}"; then
      flux_mutated=false
    else
      restore_failed=true
    fi
  fi
  if [[ "${flux_parent_mutated}" == true ]]; then
    if resume_flux_owner "${flux_parent_name}"; then
      flux_parent_mutated=false
    else
      restore_failed=true
    fi
  fi
  [[ "${restore_failed}" == false ]]
}

suspend_flux_owners() {
  if [[ "${flux_parent_name}" != "${flux_name}" ]]; then
    parent_suspended=$("${k[@]}" -n "${flux_namespace}" get kustomization "${flux_parent_name}" -o jsonpath='{.spec.suspend}')
    if [[ "${parent_suspended}" != "true" ]]; then
      flux_parent_mutated=true
      "${k[@]}" -n "${flux_namespace}" patch kustomization "${flux_parent_name}" --type=merge -p='{"spec":{"suspend":true}}' >/dev/null
    fi
  fi

  flux_suspended=$("${k[@]}" -n "${flux_namespace}" get kustomization "${flux_name}" -o jsonpath='{.spec.suspend}')
  if [[ "${flux_suspended}" != "true" ]]; then
    flux_mutated=true
    "${k[@]}" -n "${flux_namespace}" patch kustomization "${flux_name}" --type=merge -p='{"spec":{"suspend":true}}' >/dev/null
  fi

  for _ in $(seq 1 "${flux_stabilize_seconds}"); do
    if [[ "${flux_parent_name}" != "${flux_name}" && "$("${k[@]}" -n "${flux_namespace}" get kustomization "${flux_parent_name}" -o jsonpath='{.spec.suspend}')" != "true" ]]; then
      echo "Flux parent ${flux_namespace}/${flux_parent_name} resumed during outage isolation" >&2
      return 1
    fi
    if [[ "$("${k[@]}" -n "${flux_namespace}" get kustomization "${flux_name}" -o jsonpath='{.spec.suspend}')" != "true" ]]; then
      "${k[@]}" -n "${flux_namespace}" patch kustomization "${flux_name}" --type=merge -p='{"spec":{"suspend":true}}' >/dev/null
    fi
    sleep 1
  done
  [[ "$("${k[@]}" -n "${flux_namespace}" get kustomization "${flux_name}" -o jsonpath='{.spec.suspend}')" == "true" ]] || {
    echo "failed to suspend the Flux owner chain for ${flux_namespace}/${flux_name}" >&2
    return 1
  }
}

cleanup() {
  local original_status=$?
  local restore_status=0
  restore_driver || restore_status=$?
  if [[ "${namespace_created}" == true ]]; then
    "${k[@]}" delete namespace "${namespace}" --ignore-not-found --wait=false >/dev/null 2>&1 || true
  fi
  if [[ "${lock_acquired}" == true ]]; then
    if [[ "${restore_status}" != 0 ]]; then
      echo "retaining hardware lock ${lock_namespace}/${lock_name}: restoration is incomplete" >&2
    else
      holder=$("${k[@]}" -n "${lock_namespace}" get configmap "${lock_name}" -o jsonpath='{.data.holder}' 2>/dev/null || true)
      [[ "${holder}" != "${run_id}" ]] || "${k[@]}" -n "${lock_namespace}" delete configmap "${lock_name}" --ignore-not-found >/dev/null 2>&1 || true
    fi
  fi
  if [[ "${restore_status}" != 0 ]]; then
    return "${restore_status}"
  fi
  return "${original_status}"
}

trap cleanup EXIT

"${k[@]}" -n "${driver_namespace}" rollout status daemonset/"${driver_name}" --timeout="${timeout}" >/dev/null
"${k[@]}" get namespace "${namespace}" >/dev/null 2>&1 && { echo "refusing to reuse namespace ${namespace}" >&2; exit 2; }
lock_acquired=true
"${k[@]}" -n "${lock_namespace}" create configmap "${lock_name}" --from-literal="holder=${run_id}" --from-literal="startedAt=$(date -u +%Y-%m-%dT%H:%M:%SZ)" >/dev/null || {
  echo "GPU hardware suite lock is already held" >&2; exit 2;
}

suspend_flux_owners
driver_mutated=true
"${k[@]}" -n "${driver_namespace}" patch daemonset "${driver_name}" --type=merge -p='{"spec":{"template":{"spec":{"nodeSelector":{"kube-dc.com/gpu.outage-test":"true"}}}}}' >/dev/null
for _ in $(seq 1 90); do
  ready=$("${k[@]}" -n "${driver_namespace}" get daemonset "${driver_name}" -o jsonpath='{.status.numberReady}')
  [[ "${ready:-0}" == "0" ]] && break
  sleep 1
done
[[ "${ready:-0}" == "0" ]] || { echo "DRA driver did not stop" >&2; exit 1; }

"${k[@]}" create namespace "${namespace}" >/dev/null
namespace_created=true
"${k[@]}" label namespace "${namespace}" kube-dc.com/project= pod-security.kubernetes.io/enforce=restricted pod-security.kubernetes.io/enforce-version=latest >/dev/null
"${k[@]}" -n "${namespace}" create quota gpu-dra-test --hard="${quota_resource}=1" >/dev/null
"${k[@]}" -n "${namespace}" apply -f "${script_dir}/canonical-workload.yaml" >/dev/null
for _ in $(seq 1 45); do
  pod=$("${k[@]}" -n "${namespace}" get pod -l app.kubernetes.io/name=dra-live-test -o jsonpath='{.items[0].metadata.name}' 2>/dev/null || true)
  [[ -n "${pod}" ]] && break
  sleep 1
done
[[ -n "${pod:-}" ]] || { echo "outage workload Pod was not created" >&2; exit 1; }
if "${k[@]}" -n "${namespace}" wait --for=condition=Ready "pod/${pod}" --timeout=30s >/dev/null 2>&1; then
  echo "DRA workload became Ready while the kubelet plugin was absent" >&2; exit 1
fi
printf 'PASS outage: workload remained unavailable while the DRA driver was absent\n'

restore_driver
"${k[@]}" -n "${driver_namespace}" rollout status daemonset/"${driver_name}" --timeout="${timeout}" >/dev/null
"${k[@]}" -n "${namespace}" wait --for=condition=Ready "pod/${pod}" --timeout="${timeout}" >/dev/null
"${k[@]}" -n "${namespace}" logs "${pod}" | grep -E 'Tesla V100|NVIDIA V100' >/dev/null
printf 'PASS recovery: the same claim and Pod recovered with CUDA visibility\n'
