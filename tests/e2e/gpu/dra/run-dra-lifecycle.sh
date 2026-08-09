#!/usr/bin/env bash

# Destructive only to a new disposable namespace. Proves the fixed DRA SKU,
# exact quota accounting, CUDA injection, Recreate rollout behavior, rejection,
# release, and namespace teardown on an explicitly approved lab cluster.

set -euo pipefail
source "$(cd -- "$(dirname -- "${BASH_SOURCE[0]}")/../hardware" && pwd)/require-opt-in.sh"

for command in kubectl yq grep date seq; do
  command -v "${command}" >/dev/null 2>&1 || { echo "required command is missing: ${command}" >&2; exit 2; }
done
: "${KUBECONFIG:?set KUBECONFIG to the approved lab-cluster kubeconfig}"

script_dir=$(cd -- "$(dirname -- "${BASH_SOURCE[0]}")" && pwd)
kubectl_args=(--kubeconfig "${KUBECONFIG}")
run_id="dra-lifecycle-$(date +%s)-${RANDOM}"
namespace=${GPU_DRA_NAMESPACE:-${run_id}}
class=${GPU_DRA_DEVICE_CLASS:-kube-dc-nvidia-v100-shared-8g}
quota_resource="${class}.deviceclass.resource.k8s.io/devices"
timeout=${GPU_DRA_TIMEOUT:-300s}
lock_namespace=${GPU_HARDWARE_LOCK_NAMESPACE:-gpu-operator}
lock_name=${GPU_HARDWARE_LOCK_NAME:-kube-dc-gpu-hardware-suite-lock}
lock_acquired=false
namespace_created=false

cleanup() {
  if [[ "${namespace_created}" == true ]]; then
    kubectl "${kubectl_args[@]}" delete namespace "${namespace}" --ignore-not-found --wait=false >/dev/null 2>&1 || true
  fi
  if [[ "${lock_acquired}" == true ]]; then
    holder=$(kubectl "${kubectl_args[@]}" -n "${lock_namespace}" get configmap "${lock_name}" -o jsonpath='{.data.holder}' 2>/dev/null || true)
    [[ "${holder}" != "${run_id}" ]] || kubectl "${kubectl_args[@]}" -n "${lock_namespace}" delete configmap "${lock_name}" --ignore-not-found >/dev/null 2>&1 || true
  fi
}
trap cleanup EXIT

kubectl "${kubectl_args[@]}" get deviceclass "${class}" >/dev/null
shareable=$(kubectl "${kubectl_args[@]}" get resourceslices -o json | yq e '[.items[].spec.devices[]? | select(.allowMultipleAllocations == true)] | length' -)
[[ "${shareable}" -gt 0 ]] || { echo "no shareable DRA devices are discoverable" >&2; exit 1; }
kubectl "${kubectl_args[@]}" get namespace "${namespace}" >/dev/null 2>&1 && { echo "refusing to reuse namespace ${namespace}" >&2; exit 2; }
lock_acquired=true
kubectl "${kubectl_args[@]}" -n "${lock_namespace}" create configmap "${lock_name}" --from-literal="holder=${run_id}" --from-literal="startedAt=$(date -u +%Y-%m-%dT%H:%M:%SZ)" >/dev/null || {
  echo "GPU hardware suite lock is already held" >&2; exit 2;
}

kubectl "${kubectl_args[@]}" create namespace "${namespace}" >/dev/null
namespace_created=true
kubectl "${kubectl_args[@]}" label namespace "${namespace}" kube-dc.com/project= pod-security.kubernetes.io/enforce=restricted pod-security.kubernetes.io/enforce-version=latest >/dev/null
kubectl "${kubectl_args[@]}" -n "${namespace}" create quota gpu-dra-test --hard="${quota_resource}=1" >/dev/null
kubectl "${kubectl_args[@]}" -n "${namespace}" apply -f "${script_dir}/canonical-workload.yaml" >/dev/null
kubectl "${kubectl_args[@]}" -n "${namespace}" rollout status deploy/dra-live-test --timeout="${timeout}" >/dev/null
kubectl "${kubectl_args[@]}" -n "${namespace}" logs deploy/dra-live-test | grep -E 'Tesla V100|NVIDIA V100' >/dev/null

used=$(kubectl "${kubectl_args[@]}" -n "${namespace}" get resourcequota gpu-dra-test -o json | RESOURCE="${quota_resource}" yq e '.status.used[strenv(RESOURCE)]' -)
[[ "${used}" == "1" ]] || { echo "expected DRA quota use 1, got ${used}" >&2; exit 1; }
claim_count=$(kubectl "${kubectl_args[@]}" -n "${namespace}" get resourceclaims -o json | yq e '[.items[] | select(.status.allocation != null)] | length' -)
[[ "${claim_count}" == "1" ]] || { echo "expected one allocated claim, got ${claim_count}" >&2; exit 1; }
printf 'PASS lifecycle: allocated claim, quota 1/1, and CUDA sees the V100\n'

kubectl "${kubectl_args[@]}" -n "${namespace}" scale deploy dra-live-test --replicas=2 >/dev/null
for _ in $(seq 1 30); do
  denied=$(kubectl "${kubectl_args[@]}" -n "${namespace}" get events -o json | yq e '[.items[] | select(.reason == "FailedResourceClaimCreation")] | length' -)
  used=$(kubectl "${kubectl_args[@]}" -n "${namespace}" get resourcequota gpu-dra-test -o json | RESOURCE="${quota_resource}" yq e '.status.used[strenv(RESOURCE)]' -)
  claim_count=$(kubectl "${kubectl_args[@]}" -n "${namespace}" get resourceclaims -o json | yq e '[.items[] | select(.status.allocation != null)] | length' -)
  [[ "${denied}" -gt 0 && "${used}" == "1" && "${claim_count}" == "1" ]] && break
  sleep 1
done
[[ "${denied:-0}" -gt 0 && "${used:-0}" == "1" && "${claim_count:-0}" == "1" ]] || {
  echo "second replica did not fail closed with quota and allocated claims held at 1" >&2; exit 1;
}
kubectl "${kubectl_args[@]}" -n "${namespace}" scale deploy dra-live-test --replicas=1 >/dev/null
kubectl "${kubectl_args[@]}" -n "${namespace}" rollout status deploy/dra-live-test --timeout="${timeout}" >/dev/null
printf 'PASS quota: overflow rejected and one holder remains healthy\n'

before=$(kubectl "${kubectl_args[@]}" -n "${namespace}" get pod -l app.kubernetes.io/name=dra-live-test -o jsonpath='{.items[0].metadata.uid}')
kubectl "${kubectl_args[@]}" -n "${namespace}" patch deploy dra-live-test --type=merge -p '{"spec":{"template":{"metadata":{"annotations":{"kube-dc.com/test-rollout":"recreate"}}}}}' >/dev/null
kubectl "${kubectl_args[@]}" -n "${namespace}" rollout status deploy/dra-live-test --timeout="${timeout}" >/dev/null
after=$(kubectl "${kubectl_args[@]}" -n "${namespace}" get pod -l app.kubernetes.io/name=dra-live-test -o jsonpath='{.items[0].metadata.uid}')
[[ "${before}" != "${after}" ]] || { echo "Recreate rollout did not replace the Pod" >&2; exit 1; }
kubectl "${kubectl_args[@]}" -n "${namespace}" logs deploy/dra-live-test | grep -E 'Tesla V100|NVIDIA V100' >/dev/null
printf 'PASS rollout: Recreate released and reacquired the exact quota\n'

kubectl "${kubectl_args[@]}" delete namespace "${namespace}" --wait=true --timeout="${timeout}" >/dev/null
namespace_created=false
kubectl "${kubectl_args[@]}" get namespace "${namespace}" >/dev/null 2>&1 && { echo "namespace teardown wedged" >&2; exit 1; }
printf 'PASS teardown: generated claims did not wedge namespace deletion\n'
