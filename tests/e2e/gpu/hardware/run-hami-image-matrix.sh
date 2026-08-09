#!/usr/bin/env bash

# Runs a small digest-pinned CUDA runtime/framework matrix against the real HAMi
# path. This proves compatibility; it does not grant product entitlement.

set -euo pipefail

source "$(cd -- "$(dirname -- "${BASH_SOURCE[0]}")" && pwd)/require-opt-in.sh"

for command in kubectl yq grep seq date awk sleep; do
  if ! command -v "${command}" >/dev/null 2>&1; then
    echo "required command is missing: ${command}" >&2
    exit 2
  fi
done

: "${KUBECONFIG:?set KUBECONFIG to the approved lab-cluster kubeconfig}"

script_dir=$(cd -- "$(dirname -- "${BASH_SOURCE[0]}")" && pwd)
kubectl_args=(--kubeconfig "${KUBECONFIG}")
run_id="hami-images-$(date +%s)-${RANDOM}"
namespace=${GPU_IMAGE_MATRIX_NAMESPACE:-${run_id}}
lock_namespace=${GPU_HARDWARE_LOCK_NAMESPACE:-gpu-operator}
lock_name=${GPU_HARDWARE_LOCK_NAME:-kube-dc-gpu-hardware-suite-lock}
canary_name=${GPU_HARDWARE_CANARY_NAME:-hami-device-plugin-canary}
profile_id=${GPU_HARDWARE_PROFILE_ID:-nvidia-v100-hami}
expected_shares=${GPU_HARDWARE_EXPECTED_SHARES:-20}
timeout_seconds=${GPU_IMAGE_MATRIX_TIMEOUT_SECONDS:-600}
base_image='nvcr.io/nvidia/cuda@sha256:520292dbb4f755fd360766059e62956e9379485d9e073bbd2f6e3c20c270ed66'
vector_image='nvcr.io/nvidia/k8s/cuda-sample@sha256:9855f4c8500addf185360474184b9efdaf4384284779aa9173dcd70164a4ae6f'
pytorch_image='docker.io/pytorch/pytorch@sha256:68c022c2f4627943a6f3e574cfd2c8ae4256210d5f66ae2b117942e0a8d4fa9d'
canary_was_suspended=
lock_acquired=false

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

if [[ ! "${namespace}" =~ ^[a-z0-9]([-a-z0-9]*[a-z0-9])?$ ]] || (( ${#namespace} > 63 )); then
  echo "GPU_IMAGE_MATRIX_NAMESPACE must be a valid DNS label of at most 63 characters" >&2
  exit 2
fi
if kubectl "${kubectl_args[@]}" get namespace "${namespace}" >/dev/null 2>&1; then
  echo "refusing to reuse existing namespace ${namespace}" >&2
  exit 2
fi
trap cleanup EXIT
if ! kubectl "${kubectl_args[@]}" -n "${lock_namespace}" create configmap "${lock_name}" \
  --from-literal="holder=${run_id}" --from-literal="startedAt=$(date -u +%Y-%m-%dT%H:%M:%SZ)" >/dev/null; then
  echo "GPU hardware suite lock is already held" >&2
  exit 2
fi
lock_acquired=true

canary_was_suspended=$(kubectl "${kubectl_args[@]}" -n "${lock_namespace}" get cronjob "${canary_name}" \
  -o jsonpath='{.spec.suspend}')
[[ -n "${canary_was_suspended}" ]] || canary_was_suspended=false
kubectl "${kubectl_args[@]}" -n "${lock_namespace}" patch cronjob "${canary_name}" \
  --type=merge -p '{"spec":{"suspend":true}}' >/dev/null

shares=$(kubectl "${kubectl_args[@]}" get nodes -o json | yq e -r \
  '.items[] | select(.metadata.labels."kube-dc.com/gpu.expected-workload-mode" == "pod-hami") | (.status.allocatable."nvidia.com/gpu" // "0")' - | \
  awk '{ total += $1 } END { print total + 0 }')
active_holders=$(kubectl "${kubectl_args[@]}" get pods -A -o json | yq e \
  '[.items[] | select(.status.phase != "Succeeded" and .status.phase != "Failed") | select([.spec.containers[]? | select(.resources.requests."nvidia.com/gpu" != null)] | length > 0)] | length' -)
wrong_mode=$(kubectl "${kubectl_args[@]}" get nodes -o json | yq e \
  '[.items[] | select(.metadata.labels."kube-dc.com/gpu.expected-workload-mode" == "pod-hami") | select(.metadata.labels."kube-dc.com/gpu.workload-mode" != "pod-hami")] | length' -)
if [[ "${shares}" != "${expected_shares}" || "${active_holders}" != 0 || "${wrong_mode}" != 0 ]]; then
  echo "preflight failed: shares=${shares}/${expected_shares} activeHolders=${active_holders} wrongMode=${wrong_mode}" >&2
  exit 1
fi

kubectl "${kubectl_args[@]}" create namespace "${namespace}" >/dev/null
kubectl "${kubectl_args[@]}" label namespace "${namespace}" \
  pod-security.kubernetes.io/enforce=restricted \
  pod-security.kubernetes.io/enforce-version=latest >/dev/null
GPU_HARDWARE_NAMESPACE="${namespace}" GPU_COUNT=4 GPU_MEMORY=16384 GPU_CORES=100 \
  yq e '
    .metadata.namespace = strenv(GPU_HARDWARE_NAMESPACE) |
    .spec.hard."requests.nvidia.com/gpu" = strenv(GPU_COUNT) |
    .spec.hard."requests.nvidia.com/gpumem" = strenv(GPU_MEMORY) |
    .spec.hard."requests.nvidia.com/gpucores" = strenv(GPU_CORES)
  ' "${script_dir}/hami-hardware-quota.yaml" | kubectl "${kubectl_args[@]}" apply -f - >/dev/null

render_probe() {
  local name=$1 image=$2 command_json=$3 args_json=$4
  local memory_limit=${5:-128Mi} memory_request=${6:-32Mi}
  POD_NAME="${name}" GPU_HARDWARE_NAMESPACE="${namespace}" GPU_PROFILE_ID="${profile_id}" \
  GPU_IMAGE="${image}" GPU_COMMAND="${command_json}" GPU_ARGS="${args_json}" \
  MEMORY_LIMIT="${memory_limit}" MEMORY_REQUEST="${memory_request}" \
    yq e '
      .metadata.name = strenv(POD_NAME) |
      .metadata.namespace = strenv(GPU_HARDWARE_NAMESPACE) |
      .metadata.labels."kube-dc.com/gpu-profile" = strenv(GPU_PROFILE_ID) |
      .spec.containers[0].image = strenv(GPU_IMAGE) |
      .spec.containers[0].command = (strenv(GPU_COMMAND) | from_json) |
      .spec.containers[0].args = (strenv(GPU_ARGS) | from_json) |
      .spec.containers[0].resources.requests.memory = strenv(MEMORY_REQUEST) |
      .spec.containers[0].resources.limits.memory = strenv(MEMORY_LIMIT) |
      .spec.containers[0].resources.requests."nvidia.com/gpumem" = "4096" |
      .spec.containers[0].resources.limits."nvidia.com/gpumem" = "4096" |
      .spec.containers[0].resources.requests."nvidia.com/gpucores" = "25" |
      .spec.containers[0].resources.limits."nvidia.com/gpucores" = "25"
    ' "${script_dir}/hami-hardware-pod.yaml"
}

wait_succeeded() {
  local name=$1 phase attempt
  for attempt in $(seq 1 "${timeout_seconds}"); do
    phase=$(kubectl "${kubectl_args[@]}" -n "${namespace}" get pod "${name}" \
      -o jsonpath='{.status.phase}' 2>/dev/null || true)
    [[ "${phase}" == Succeeded ]] && return 0
    if [[ "${phase}" == Failed ]]; then
      kubectl "${kubectl_args[@]}" -n "${namespace}" logs "${name}" >&2 || true
      kubectl "${kubectl_args[@]}" -n "${namespace}" describe pod "${name}" >&2 || true
      echo "${name} failed" >&2
      return 1
    fi
    sleep 1
  done
  echo "${name} did not succeed within ${timeout_seconds}s" >&2
  kubectl "${kubectl_args[@]}" -n "${namespace}" describe pod "${name}" >&2 || true
  return 1
}

assert_allocation() {
  local name=$1
  allocation=$(kubectl "${kubectl_args[@]}" -n "${namespace}" get pod "${name}" \
    -o jsonpath='{.metadata.annotations.hami\.io/vgpu-devices-allocated}')
  grep -Eq ',NVIDIA,4096,25:' <<<"${allocation}"
}

render_probe image-cuda-base "${base_image}" \
  '["/bin/sh","-ec"]' \
  '["nvidia-smi --query-gpu=name,memory.total --format=csv,noheader"]' | \
  kubectl "${kubectl_args[@]}" create -f - >/dev/null
wait_succeeded image-cuda-base
assert_allocation image-cuda-base
kubectl "${kubectl_args[@]}" -n "${namespace}" logs image-cuda-base | grep -Eq 'Tesla V100.*4096 MiB'
printf 'PASS image: NVIDIA CUDA base + nvidia-smi virtual 4096 MiB\n'

render_probe image-vectoradd "${vector_image}" \
  '["/cuda-samples/sample"]' '[]' | kubectl "${kubectl_args[@]}" create -f - >/dev/null
wait_succeeded image-vectoradd
assert_allocation image-vectoradd
kubectl "${kubectl_args[@]}" -n "${namespace}" logs image-vectoradd | grep -F 'Test PASSED' >/dev/null
printf 'PASS image: NVIDIA CUDA 12.5 vectorAdd\n'

python_probe='import torch
assert torch.cuda.is_available()
total = torch.cuda.get_device_properties(0).total_memory
assert 4000 * 1024 * 1024 <= total <= 4096 * 1024 * 1024
a = torch.randn(2048, 2048, device="cuda")
b = torch.randn(2048, 2048, device="cuda")
torch.mm(a, b)
torch.cuda.synchronize()
print(f"PYTORCH PASS device={torch.cuda.get_device_name(0)} memory={total}")'
PYTHON_PROBE="${python_probe}" render_probe image-pytorch "${pytorch_image}" \
  '["python","-u","-c"]' "$(PYTHON_PROBE="${python_probe}" yq e -n -o=json '[strenv(PYTHON_PROBE)]')" \
  1Gi 256Mi | \
  kubectl "${kubectl_args[@]}" create -f - >/dev/null
wait_succeeded image-pytorch
assert_allocation image-pytorch
kubectl "${kubectl_args[@]}" -n "${namespace}" logs image-pytorch | grep -F 'PYTORCH PASS device=Tesla V100' >/dev/null
printf 'PASS image: PyTorch 2.4 CUDA 12.1 allocation + matmul + virtual memory\n'

memory_probe='import torch
assert torch.cuda.is_available()
total = torch.cuda.get_device_properties(0).total_memory
assert 4000 * 1024 * 1024 <= total <= 4096 * 1024 * 1024
try:
    torch.empty((5 * 1024 * 1024 * 1024 // 4,), dtype=torch.float32, device="cuda")
    torch.cuda.synchronize()
except RuntimeError as error:
    assert "out of memory" in str(error).lower(), str(error)
else:
    raise AssertionError("allocation above the virtual GPU memory limit succeeded")
torch.cuda.empty_cache()
probe = torch.ones(1024, device="cuda")
assert probe.sum().item() == 1024
print(f"GPU MEMORY LIMIT PASS virtual={total}")'
MEMORY_PROBE="${memory_probe}" render_probe image-memory-limit "${pytorch_image}" \
  '["python","-u","-c"]' "$(MEMORY_PROBE="${memory_probe}" yq e -n -o=json '[strenv(MEMORY_PROBE)]')" \
  1Gi 256Mi | \
  kubectl "${kubectl_args[@]}" create -f - >/dev/null
wait_succeeded image-memory-limit
assert_allocation image-memory-limit
kubectl "${kubectl_args[@]}" -n "${namespace}" logs image-memory-limit | grep -F 'GPU MEMORY LIMIT PASS' >/dev/null
printf 'PASS limit: PyTorch allocation above 4096 MiB rejected; CUDA context recovered\n'

kubectl "${kubectl_args[@]}" -n "${namespace}" delete pod --all --wait=true >/dev/null
for attempt in $(seq 1 90); do
  used=$(kubectl "${kubectl_args[@]}" -n "${namespace}" get resourcequota gpu-hardware-suite -o json | \
    yq e '[.status.used."requests.nvidia.com/gpu" // "0", .status.used."requests.nvidia.com/gpumem" // "0", .status.used."requests.nvidia.com/gpucores" // "0"] | join(",")' -)
  [[ "${used}" == '0,0,0' ]] && break
  sleep 1
done
if [[ "${used}" != '0,0,0' ]]; then
  echo "image matrix quota did not release: ${used}" >&2
  exit 1
fi
printf 'PASS cleanup: all four application/limit probes released quota\n'
